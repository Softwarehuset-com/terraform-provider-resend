package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	resend "github.com/resend/resend-go/v4"
)

// fakeClaims is the test seam for the domain claims service: it records
// verify calls and replays claim states.
type fakeClaims struct {
	states      []resend.DomainClaim
	verifyCalls int
	calls       int
}

func (f *fakeClaims) GetWithContext(_ context.Context, _ string) (resend.DomainClaim, error) {
	defer func() { f.calls++ }()
	if f.calls >= len(f.states) {
		return f.states[len(f.states)-1], nil
	}
	return f.states[f.calls], nil
}

func (f *fakeClaims) VerifyWithContext(_ context.Context, _ string) (resend.DomainClaim, error) {
	f.verifyCalls++
	return resend.DomainClaim{}, nil
}

func claimVerificationSchemaResp(t *testing.T) *resource.SchemaResponse {
	t.Helper()
	r, ok := NewDomainClaimVerificationResource().(*domainClaimVerificationResource)
	if !ok {
		t.Fatalf("expected *domainClaimVerificationResource, got %T", NewDomainClaimVerificationResource())
	}
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp
}

func claimVerificationRaw(objType tftypes.Type, domainID interface{}, status interface{}) map[string]tftypes.Value {
	attrTypes := objType.(tftypes.Object).AttributeTypes
	return map[string]tftypes.Value{
		"domain_id":      tftypes.NewValue(attrTypes["domain_id"], domainID),
		"status":         tftypes.NewValue(attrTypes["status"], status),
		"blocked_reason": tftypes.NewValue(attrTypes["blocked_reason"], nil),
		"failure_reason": tftypes.NewValue(attrTypes["failure_reason"], nil),
		"timeouts":       tftypes.NewValue(attrTypes["timeouts"], nil),
	}
}

func claimVerificationCreatePair(t *testing.T) (resource.CreateRequest, *resource.CreateResponse) {
	t.Helper()
	s := claimVerificationSchemaResp(t).Schema
	objType := s.Type().TerraformType(context.Background())

	raw := claimVerificationRaw(objType, "dom-1", tftypes.UnknownValue)
	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Raw: tftypes.NewValue(objType, raw), Schema: s},
	}
	resp := &resource.CreateResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: s},
	}
	return req, resp
}

func TestDomainClaimVerificationSchema(t *testing.T) {
	s := claimVerificationSchemaResp(t).Schema

	domainID, ok := s.Attributes["domain_id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("domain_id should be string attribute, got %T", s.Attributes["domain_id"])
	}
	if !domainID.Required {
		t.Error("domain_id should be required")
	}
	if !hasRequiresReplaceIfConfigured(domainID.PlanModifiers) {
		t.Error("domain_id should require replacement")
	}

	if a, ok := s.Attributes["status"]; !ok || !a.IsComputed() {
		t.Error("status should be computed")
	}
	if _, has := s.Attributes["timeouts"]; !has {
		t.Error("verification resource should carry a timeouts block")
	}
}

func TestDomainClaimVerificationCreateVerified(t *testing.T) {
	fake := &fakeClaims{
		states: []resend.DomainClaim{
			{Status: resend.DomainClaimStatusPending},
			{Status: resend.DomainClaimStatusPending},
			{Status: resend.DomainClaimStatusVerified, DomainId: "dom-1"},
		},
	}
	r := &domainClaimVerificationResource{
		claims: fake,
		sleep:  func(context.Context, time.Duration) error { return nil },
	}

	req, resp := claimVerificationCreatePair(t)
	r.Create(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create should succeed: %v", resp.Diagnostics)
	}
	if fake.verifyCalls != 1 {
		t.Errorf("verify should be called exactly once, got %d", fake.verifyCalls)
	}
	var state domainClaimVerificationResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Status.ValueString() != resend.DomainClaimStatusVerified {
		t.Errorf("status should be verified, got %q", state.Status.ValueString())
	}
}

func claimVerificationCreateExpectError(t *testing.T, fake *fakeClaims, wantSubstr string) {
	t.Helper()
	r := &domainClaimVerificationResource{
		claims: fake,
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	req, resp := claimVerificationCreatePair(t)
	r.Create(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected create to error")
	}
	var msg strings.Builder
	for _, d := range resp.Diagnostics {
		msg.WriteString(d.Summary() + " " + d.Detail() + " ")
	}
	if !strings.Contains(msg.String(), wantSubstr) {
		t.Errorf("error should mention %q, got: %s", wantSubstr, msg.String())
	}
}

// Review Focus 5: failure/blocked reasons must surface in the error, not
// vanish into a generic timeout.
func TestDomainClaimVerificationCreateBlockedSurfacesReason(t *testing.T) {
	claimVerificationCreateExpectError(t, &fakeClaims{
		states: []resend.DomainClaim{
			{Status: resend.DomainClaimStatusBlocked, BlockedReason: resend.DomainClaimBlockedReasonGracePeriod},
		},
	}, "grace_period")
}

func TestDomainClaimVerificationCreateFailedSurfacesReason(t *testing.T) {
	claimVerificationCreateExpectError(t, &fakeClaims{
		states: []resend.DomainClaim{
			{Status: resend.DomainClaimStatusFailed, FailureReason: "TXT record mismatch"},
		},
	}, "TXT record mismatch")
}

func TestDomainClaimVerificationReadSemantics(t *testing.T) {
	s := claimVerificationSchemaResp(t).Schema
	objType := s.Type().TerraformType(context.Background())

	run := func(status string) bool {
		fake := &fakeClaims{states: []resend.DomainClaim{{Status: status}}}
		r := &domainClaimVerificationResource{claims: fake}
		raw := claimVerificationRaw(objType, "dom-1", status)
		st := tfsdk.State{Raw: tftypes.NewValue(objType, raw), Schema: s}
		resp := &resource.ReadResponse{State: st}
		r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("read for status %q errored: %v", status, resp.Diagnostics)
		}
		return !resp.State.Raw.IsNull()
	}

	for _, status := range []string{
		resend.DomainClaimStatusVerified,
		resend.DomainClaimStatusCompleted,
		resend.DomainClaimStatusExpired,
	} {
		if !run(status) {
			t.Errorf("status %q should keep the resource in state", status)
		}
	}
	if run(resend.DomainClaimStatusPending) {
		t.Error("pending claim should be removed from state to re-trigger verification")
	}
}
