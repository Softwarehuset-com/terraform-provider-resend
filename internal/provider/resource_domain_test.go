package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	tfsdk "github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	resend "github.com/resend/resend-go/v4"
)

func domainSchema(t *testing.T) schema.Schema {
	t.Helper()
	r, ok := NewDomainResource().(*domainResource)
	if !ok {
		t.Fatalf("expected *domainResource, got %T", NewDomainResource())
	}
	resp := resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestDomainSchema(t *testing.T) {
	s := domainSchema(t)

	// Arguments
	name, ok := s.Attributes["name"]
	if !ok {
		t.Fatal("missing attribute name")
	}
	if sa, ok := name.(schema.StringAttribute); !ok || !sa.Required {
		t.Errorf("name should be required string")
	}

	region, ok := s.Attributes["region"]
	if !ok {
		t.Fatal("missing attribute region")
	}
	sa, ok := region.(schema.StringAttribute)
	if !ok || sa.Required || !sa.Optional {
		t.Errorf("region should be optional string")
	}
	if len(sa.PlanModifiers) == 0 {
		t.Error("region should have plan modifiers (RequiresReplace)")
	}

	caps, ok := s.Attributes["capabilities"]
	if !ok {
		t.Fatal("missing attribute capabilities")
	}
	oa, ok := caps.(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("capabilities should be single nested attribute, got %T", caps)
	}
	for _, sub := range []string{"sending", "receiving"} {
		if _, ok := oa.Attributes[sub]; !ok {
			t.Errorf("capabilities missing %q", sub)
		}
	}

	if _, ok := s.Attributes["custom_return_path"]; !ok {
		t.Error("missing attribute custom_return_path")
	}
	if _, ok := s.Attributes["tracking_subdomain"]; !ok {
		t.Error("missing attribute tracking_subdomain")
	}
	if _, ok := s.Attributes["open_tracking"]; !ok {
		t.Error("missing attribute open_tracking")
	}
	if _, ok := s.Attributes["click_tracking"]; !ok {
		t.Error("missing attribute click_tracking")
	}
	tlsAttr, ok := s.Attributes["tls"]
	if !ok {
		t.Fatal("missing attribute tls")
	}
	if ta, ok := tlsAttr.(schema.StringAttribute); !ok || !ta.Optional {
		t.Errorf("tls should be optional string")
	}

	// Computed
	for _, name := range []string{"id", "status", "created_at", "records"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("missing computed attribute %q", name)
		}
	}

}

func TestDomainSchemaValidators(t *testing.T) {
	s := domainSchema(t)

	region := s.Attributes["region"].(schema.StringAttribute)
	if len(region.Validators) == 0 {
		t.Error("region should carry a one-of validator")
	}

	tlsAttr := s.Attributes["tls"].(schema.StringAttribute)
	if len(tlsAttr.Validators) == 0 {
		t.Error("tls should carry a one-of validator")
	}

	caps := s.Attributes["capabilities"].(schema.SingleNestedAttribute)
	for _, sub := range []string{"sending", "receiving"} {
		attr, ok := caps.Attributes[sub].(schema.StringAttribute)
		if !ok {
			t.Fatalf("capabilities.%s should be a string attribute, got %T", sub, caps.Attributes[sub])
		}
		if len(attr.Validators) == 0 {
			t.Errorf("capabilities.%s should carry an enabled/disabled validator", sub)
		}
	}
}

func TestMapRecords(t *testing.T) {
	in := []resend.Record{
		{
			Record: "SPF",
			Name:   "send",
			Type:   "TXT",
			Ttl:    "Auto",
			Status: "pending",
			Value:  "v=spf1 include:amazonses.com ~all",
		},
		{
			Record:   "SPF",
			Name:     "send",
			Type:     "MX",
			Ttl:      "Auto",
			Status:   "pending",
			Value:    "feedback-smtp.us-east-1.amazonses.com",
			Priority: json.Number("10"),
		},
	}

	got, diags := mapRecords(in)
	if diags.HasError() {
		t.Fatalf("mapRecords errored: %v", diags)
	}

	if got.IsNull() || got.IsUnknown() {
		t.Fatalf("records must be a known, non-null list, got %s", got)
	}
	elems := got.Elements()
	if len(elems) != 2 {
		t.Fatalf("expected 2 records, got %d", len(elems))
	}

	first := elems[0].(types.Object)
	prio, ok := first.Attributes()["priority"].(types.Number)
	if !ok || !prio.IsNull() {
		t.Errorf("TXT record priority should be null, got %s", first.Attributes()["priority"])
	}
	rec, _ := first.Attributes()["record"].(types.String)
	if rec.ValueString() != "SPF" {
		t.Errorf("record field not mapped: %v", first.Attributes()["record"])
	}

	second := elems[1].(types.Object)
	prio = second.Attributes()["priority"].(types.Number)
	if pf := prio.ValueBigFloat(); pf == nil || pf.Cmp(big.NewFloat(10)) != 0 {
		t.Errorf("MX record priority should be 10, got %s", second.Attributes()["priority"])
	}
}

// TestFinalizeDomainState pins the review finding that unknown values from
// the plan must never leak into final state: Optional+Computed attributes
// resolve to concrete values even when the API response omits them.
func TestFinalizeDomainState(t *testing.T) {
	m := domainResourceModel{
		Name:              types.StringValue("automated.appcompany.com"),
		Region:            types.StringUnknown(),
		TrackingSubdomain: types.StringUnknown(),
		OpenTracking:      types.BoolUnknown(),
		ClickTracking:     types.BoolUnknown(),
		Tls:               types.StringUnknown(),
		Capabilities:      types.ObjectUnknown(capabilitiesAttrTypes),
	}

	// Simulate an API response that omits region/capabilities entirely.
	mapDomainToState(&m, resend.Domain{
		Id:           "dom-1",
		Name:         "automated.appcompany.com",
		Status:       "not_started",
		OpenTracking: false,
	})
	finalizeDomainState(&m)

	for name, v := range map[string]attr.Value{
		"region":             m.Region,
		"tracking_subdomain": m.TrackingSubdomain,
	} {
		if v.IsUnknown() {
			t.Errorf("%s still unknown after finalize", name)
		}
	}
	if m.Tls.IsUnknown() {
		t.Error("tls still unknown after finalize")
	}
	if m.OpenTracking.IsUnknown() || m.ClickTracking.IsUnknown() {
		t.Error("tracking bools still unknown after finalize")
	}
	if m.Capabilities.IsUnknown() || m.Capabilities.IsNull() {
		t.Fatal("capabilities should be a concrete object after finalize")
	}
	sending, _ := m.Capabilities.Attributes()["sending"].(types.String)
	receiving, _ := m.Capabilities.Attributes()["receiving"].(types.String)
	if sending.IsUnknown() || sending.IsNull() || receiving.IsUnknown() || receiving.IsNull() {
		t.Error("capabilities members still unknown/null after finalize")
	}
	if sending.ValueString() != resend.DomainCapabilityStatusEnabled || receiving.ValueString() != resend.DomainCapabilityStatusDisabled {
		t.Errorf("capabilities defaults wrong: sending=%q receiving=%q", sending.ValueString(), receiving.ValueString())
	}
}

// --- Acceptance tests (require TF_ACC and RESEND_API_KEY) ---

var testAccProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"resend": providerserver.NewProtocol6WithError(New("test")()),
}

func testAccPreCheck(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance test")
	}
	if os.Getenv("RESEND_API_KEY") == "" {
		t.Skip("RESEND_API_KEY not set; skipping acceptance test")
	}
}

// testAccDomainBase returns the parent domain under which acceptance tests
// create throwaway subdomains. Resend rejects reserved domains (example.com,
// .test) with a misleading "registered already" error, so the base must be a
// real domain whose mail namespace you are willing to create records under.
// Provide it via TF_ACC_DOMAIN_BASE; tests skip when it is unset.
func testAccDomainBase(t *testing.T) string {
	base := os.Getenv("TF_ACC_DOMAIN_BASE")
	if base == "" {
		t.Skip("TF_ACC_DOMAIN_BASE not set; skipping acceptance test (set it to a domain you control)")
	}
	return base
}

func TestAccDomainBasic(t *testing.T) {
	testAccPreCheck(t)

	name := fmt.Sprintf("tfacc-%d.%s", time.Now().UnixNano(), testAccDomainBase(t))

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config: testAccDomainConfig(name, "false"),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("resend_domain.test", "name", name),
					tfresource.TestCheckResourceAttr("resend_domain.test", "capabilities.receiving", "enabled"),
					tfresource.TestMatchResourceAttr("resend_domain.test", "records.#", regexp.MustCompile(`^[1-9]`)),
				),
			},
			{
				// In-place update: tracking toggles patch without replacement.
				Config: testAccDomainConfig(name, "true"),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("resend_domain.test", "name", name),
				),
			},
			{
				ResourceName:      "resend_domain.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccDomainConfig(name, clickTracking string) string {
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %[1]q
  capabilities = {
    sending   = "enabled"
    receiving = "enabled"
  }
  tracking_subdomain = "links"
  click_tracking     = %[2]s
}
`, name, clickTracking)
}

// TestDomainCreateWithoutCapabilitiesBlock reproduces a latent v1.0.1 bug:
// capabilities is Optional+Computed, so on first create without the block
// the plan value is an unknown object, which a struct-pointer model cannot
// hold ("Value Conversion Error ... Path: capabilities"). Exercised via a
// fake API so no network is needed.
func TestDomainCreateWithoutCapabilitiesBlock(t *testing.T) {
	var createBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/domains"):
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "d-1", "name": "mail.appcompany.com", "status": "pending", "region": "us-east-1",
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/domains/d-1"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "d-1", "name": "mail.appcompany.com", "status": "pending", "region": "us-east-1",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := &domainResource{client: NewResendClient("re_test", srv.URL)}
	sch := domainSchema(t)
	objType := sch.Type().TerraformType(context.Background()).(tftypes.Object)
	attrTypes := objType.AttributeTypes

	raw := map[string]tftypes.Value{
		"name":         tftypes.NewValue(attrTypes["name"], "mail.appcompany.com"),
		"capabilities": tftypes.NewValue(attrTypes["capabilities"], tftypes.UnknownValue),
	}
	for _, a := range []string{"region", "custom_return_path", "tls", "tracking_subdomain", "open_tracking", "click_tracking"} {
		raw[a] = tftypes.NewValue(attrTypes[a], nil)
	}
	for _, a := range []string{"id", "status", "created_at", "records"} {
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
		t.Fatalf("create without capabilities block should not error: %v", resp.Diagnostics)
	}
	if _, has := createBody["capabilities"]; has {
		t.Errorf("create request should omit capabilities when block absent, got body %v", createBody)
	}

	// Finalized state must hold concrete API defaults, never unknown.
	var state domainResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	caps := state.Capabilities
	sending, _ := caps.Attributes()["sending"].(types.String)
	receiving, _ := caps.Attributes()["receiving"].(types.String)
	if sending.ValueString() != resend.DomainCapabilityStatusEnabled {
		t.Errorf("sending default should be enabled, got %q", sending.ValueString())
	}
	if receiving.ValueString() != resend.DomainCapabilityStatusDisabled {
		t.Errorf("receiving default should be disabled, got %q", receiving.ValueString())
	}
}
