package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	tfsdk "github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	resend "github.com/resend/resend-go/v4"
)

func domainClaimSchema(t *testing.T) schema.Schema {
	t.Helper()
	r, ok := NewDomainClaimResource().(*domainClaimResource)
	if !ok {
		t.Fatalf("expected *domainClaimResource, got %T", NewDomainClaimResource())
	}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestDomainClaimSchema(t *testing.T) {
	s := domainClaimSchema(t)

	name, ok := s.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("name should be string attribute, got %T", s.Attributes["name"])
	}
	if !name.Required {
		t.Error("name should be required")
	}
	if !hasRequiresReplaceIfConfigured(name.PlanModifiers) {
		t.Error("name should require replacement")
	}

	for _, attr := range []string{"region", "custom_return_path", "tracking_subdomain"} {
		a, ok := s.Attributes[attr].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s should be string attribute, got %T", attr, s.Attributes[attr])
		}
		if !a.Optional {
			t.Errorf("%s should be optional", attr)
		}
		if !hasRequiresReplaceIfConfigured(a.PlanModifiers) {
			t.Errorf("%s should require replacement", attr)
		}
	}

	for _, attr := range []string{"open_tracking", "click_tracking"} {
		a, ok := s.Attributes[attr].(schema.BoolAttribute)
		if !ok {
			t.Fatalf("%s should be bool attribute, got %T", attr, s.Attributes[attr])
		}
		if !a.Optional {
			t.Errorf("%s should be optional", attr)
		}
	}

	for _, attr := range []string{"id", "domain_id", "status", "record", "blocked_reason", "failure_reason", "expires_at"} {
		if a, ok := s.Attributes[attr]; !ok || !a.IsComputed() {
			t.Errorf("%s should be computed", attr)
		}
	}

	// The claim resource deliberately does NOT poll: verification runs in
	// resend_domain_claim_verification.
	if _, has := s.Attributes["timeouts"]; has {
		t.Error("claim resource should not carry a timeouts block")
	}
}

func TestBuildCreateClaimRequest(t *testing.T) {
	// Minimal: only name.
	req := buildCreateClaimRequest(&domainClaimResourceModel{
		Name: types.StringValue("automated.appcompany.com"),
	})
	if req.Name != "automated.appcompany.com" || req.Region != "" || req.OpenTracking != nil || req.ClickTracking != nil {
		t.Errorf("minimal request wrong: %+v", req)
	}

	// All optionals set.
	tt, ff := true, false
	req = buildCreateClaimRequest(&domainClaimResourceModel{
		Name:              types.StringValue("automated.appcompany.com"),
		Region:            types.StringValue("eu-central-1"),
		CustomReturnPath:  types.StringValue("bounces"),
		TrackingSubdomain: types.StringValue("links"),
		OpenTracking:      types.BoolValue(tt),
		ClickTracking:     types.BoolValue(ff),
	})
	if req.Region != "eu-central-1" || req.CustomReturnPath != "bounces" || req.TrackingSubdomain != "links" {
		t.Errorf("optional strings not mapped: %+v", req)
	}
	if req.OpenTracking == nil || !*req.OpenTracking || req.ClickTracking == nil || *req.ClickTracking {
		t.Errorf("tracking bools not mapped: %+v", req)
	}
}

func TestMapDomainClaimToState(t *testing.T) {
	m := &domainClaimResourceModel{}
	claim := resend.DomainClaim{
		Id:        "claim-1",
		DomainId:  "dom-1",
		Name:      "automated.appcompany.com",
		Status:    resend.DomainClaimStatusPending,
		Region:    "eu-central-1",
		Record:    &resend.DomainClaimRecord{Type: "TXT", Name: "resend-verification", Value: "abc123", Ttl: "Auto"},
		ExpiresAt: "2026-10-17T10:00:00.000Z",
	}
	mapDomainClaimToState(m, claim)

	if m.ID.ValueString() != "claim-1" || m.DomainID.ValueString() != "dom-1" {
		t.Errorf("ids not mapped: %+v", m)
	}
	if m.Status.ValueString() != "pending" {
		t.Errorf("status not mapped: %q", m.Status.ValueString())
	}
	if m.ExpiresAt.ValueString() != "2026-10-17T10:00:00.000Z" {
		t.Errorf("expires_at not mapped: %q", m.ExpiresAt.ValueString())
	}
	if m.Record.IsNull() || m.Record.IsUnknown() {
		t.Fatal("record should be a concrete object")
	}
	rec := m.Record.Attributes()
	if rec["value"].(types.String).ValueString() != "abc123" {
		t.Errorf("record value not mapped: %v", rec)
	}
	if rec["type"].(types.String).ValueString() != "TXT" {
		t.Errorf("record type not mapped: %v", rec)
	}

	// Blocked/failed reasons surface when present.
	m2 := &domainClaimResourceModel{}
	mapDomainClaimToState(m2, resend.DomainClaim{Status: "blocked", BlockedReason: "grace_period", FailureReason: "nope"})
	if m2.BlockedReason.ValueString() != "grace_period" || m2.FailureReason.ValueString() != "nope" {
		t.Errorf("reasons not mapped: %+v", m2)
	}
	// Region is request-only: reading the API's effective region back would
	// forever-diff against a null config value.
	m3 := &domainClaimResourceModel{Region: types.StringNull()}
	mapDomainClaimToState(m3, resend.DomainClaim{Id: "c", Status: "pending", Region: "us-east-1"})
	if !m3.Region.IsNull() {
		t.Errorf("region must not be read back from the API, got %q", m3.Region.ValueString())
	}
}

// TestDomainClaimCreateFast verifies the claim resource creates WITHOUT
// polling or verifying: the TXT record value only becomes known after create,
// so verification must run in a separate resource once DNS is in place.
func TestDomainClaimCreateFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/domains/claim") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "claim-1", "domain_id": "dom-1", "name": "automated.appcompany.com",
				"status": "pending",
				"record": map[string]any{"type": "TXT", "name": "resend-verification", "value": "abc123", "ttl": "Auto"},
			})
			return
		}
		if r.Method == http.MethodGet { // any refresh/poll attempt must NOT happen
			t.Error("claim create must not read back or poll")
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	r := &domainClaimResource{client: NewResendClient("re_test", srv.URL)}
	sch := domainClaimSchema(t)
	objType := sch.Type().TerraformType(context.Background()).(tftypes.Object)
	attrTypes := objType.AttributeTypes

	raw := map[string]tftypes.Value{
		"name": tftypes.NewValue(attrTypes["name"], "automated.appcompany.com"),
	}
	for _, a := range []string{"region", "custom_return_path", "tracking_subdomain", "open_tracking", "click_tracking"} {
		raw[a] = tftypes.NewValue(attrTypes[a], nil)
	}
	for _, a := range []string{"id", "domain_id", "status", "record", "blocked_reason", "failure_reason", "expires_at"} {
		raw[a] = tftypes.NewValue(attrTypes[a], tftypes.UnknownValue)
	}

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Raw: tftypes.NewValue(objType, raw), Schema: sch},
	}
	resp := resource.CreateResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: sch},
	}
	r.Create(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("claim create should succeed without polling: %v", resp.Diagnostics)
	}
	var state domainClaimResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Status.ValueString() != "pending" {
		t.Errorf("status should be pending, got %q", state.Status.ValueString())
	}
	if state.DomainID.ValueString() != "dom-1" {
		t.Errorf("domain_id should map claim domain, got %q", state.DomainID.ValueString())
	}
}
