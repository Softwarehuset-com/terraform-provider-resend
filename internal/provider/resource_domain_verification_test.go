package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	resend "github.com/resend/resend-go/v4"
)

type goneGetter struct{}

func (goneGetter) GetWithContext(context.Context, string) (resend.Domain, error) {
	return resend.Domain{}, &notFoundError{}
}

type notFoundError struct{}

func (*notFoundError) Error() string { return "[ERROR]: 404 Not Found" }

// seqDomainGetter replays a status sequence, holding the last one.
type seqDomainGetter struct {
	statuses []string
	calls    int
}

func (f *seqDomainGetter) GetWithContext(_ context.Context, _ string) (resend.Domain, error) {
	idx := f.calls
	if idx >= len(f.statuses) {
		idx = len(f.statuses) - 1
	}
	f.calls++
	return resend.Domain{Status: f.statuses[idx]}, nil
}

type countingVerifier struct {
	calls int
	err   error
}

func (f *countingVerifier) VerifyWithContext(context.Context, string) (bool, error) {
	f.calls++
	return f.err == nil, f.err
}

func verificationSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewDomainVerificationResource()
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema errored: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// verificationValue builds a raw object; status/wait accept nil, a concrete
// value, or tftypes.UnknownValue to mimic plans and states.
func verificationValue(t *testing.T, s schema.Schema, domainID string, status, wait interface{}) tftypes.Value {
	t.Helper()
	objType := s.Type().TerraformType(context.Background())
	attrTypes := objType.(tftypes.Object).AttributeTypes
	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"domain_id":           tftypes.NewValue(tftypes.String, domainID),
		"status":              tftypes.NewValue(tftypes.String, status),
		"wait_until_verified": tftypes.NewValue(tftypes.Bool, wait),
		"timeouts":            tftypes.NewValue(attrTypes["timeouts"], nil),
	})
}

// TestDomainVerificationReadGone pins Review Focus 4: when the domain was
// deleted out-of-band, Read drops the verification resource from state
// instead of erroring.
func TestDomainVerificationReadGone(t *testing.T) {
	r := &domainVerificationResource{getter: goneGetter{}}
	s := verificationSchema(t)

	raw := verificationValue(t, s, "dom-1", resend.DomainStatusVerified, true)
	st := tfsdk.State{Raw: raw, Schema: s}
	resp := &resource.ReadResponse{State: st}

	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error on gone domain, got: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be removed when domain is gone")
	}
}

// defaultBoolValue evaluates a schema bool default through its
// defaults.Bool implementation.
func defaultBoolValue(t *testing.T, attr schema.BoolAttribute) bool {
	t.Helper()
	def, ok := attr.Default.(defaults.Bool)
	if !ok {
		t.Fatalf("default should implement defaults.Bool, got %T", attr.Default)
	}
	resp := &defaults.BoolResponse{}
	def.DefaultBool(context.Background(), defaults.BoolRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("evaluating default: %v", resp.Diagnostics)
	}
	return resp.PlanValue.ValueBool()
}

// TestWaitUntilVerifiedReplaceSemantics pins the modifier behaviorally:
// type-based checks cannot distinguish RequiresReplace from
// RequiresReplaceIfConfigured (both wrap RequiresReplaceIf).
func TestWaitUntilVerifiedReplaceSemantics(t *testing.T) {
	cases := []struct {
		name                string
		config, state, plan types.Bool
		wantReplace         bool
	}{
		{
			// Imported and v1.x states carry null; the plan fills the
			// default (false). Forcing replace here would churn every
			// existing verification resource on upgrade.
			name:        "null state with default-filled plan must not replace",
			config:      types.BoolNull(),
			state:       types.BoolNull(),
			plan:        types.BoolValue(false),
			wantReplace: false,
		},
		{
			name:        "explicit toggle replaces",
			config:      types.BoolValue(false),
			state:       types.BoolValue(true),
			plan:        types.BoolValue(false),
			wantReplace: true,
		},
		{
			name:        "unchanged explicit value does not replace",
			config:      types.BoolValue(true),
			state:       types.BoolValue(true),
			plan:        types.BoolValue(true),
			wantReplace: false,
		},
	}

	for resName, s := range map[string]schema.Schema{
		"resend_domain_verification":       verificationSchema(t),
		"resend_domain_claim_verification": claimVerificationSchemaResp(t).Schema,
	} {
		attr, ok := s.Attributes["wait_until_verified"].(schema.BoolAttribute)
		if !ok {
			t.Fatalf("%s: wait_until_verified should be bool attribute, got %T", resName, s.Attributes["wait_until_verified"])
		}

		toWait := func(b types.Bool) interface{} {
			if b.IsNull() {
				return nil
			}
			return b.ValueBool()
		}

		for _, tc := range cases {
			t.Run(resName+" / "+tc.name, func(t *testing.T) {
				stateRaw := objectWithWait(t, s, toWait(tc.state))
				planRaw := objectWithWait(t, s, toWait(tc.plan))

				req := planmodifier.BoolRequest{
					ConfigValue: tc.config,
					StateValue:  tc.state,
					PlanValue:   tc.plan,
					State:       tfsdk.State{Raw: stateRaw, Schema: s},
					Plan:        tfsdk.Plan{Raw: planRaw, Schema: s},
				}
				resp := &planmodifier.BoolResponse{}
				for _, m := range attr.PlanModifiers {
					m.PlanModifyBool(context.Background(), req, resp)
				}
				if resp.Diagnostics.HasError() {
					t.Fatalf("modifier errored: %v", resp.Diagnostics)
				}
				if resp.RequiresReplace != tc.wantReplace {
					t.Errorf("requiresReplace = %v, want %v", resp.RequiresReplace, tc.wantReplace)
				}
			})
		}
	}
}

// objectWithWait builds a schema-complete object with wait_until_verified
// set (nil -> null) and domain_id filled, so State.Raw/Plan.Raw are
// non-null for plan modifier tests.
func objectWithWait(t *testing.T, s schema.Schema, wait interface{}) tftypes.Value {
	t.Helper()
	objType := s.Type().TerraformType(context.Background())
	attrTypes := objType.(tftypes.Object).AttributeTypes
	raw := map[string]tftypes.Value{}
	for a, at := range attrTypes {
		raw[a] = tftypes.NewValue(at, nil)
	}
	raw["wait_until_verified"] = tftypes.NewValue(tftypes.Bool, wait)
	raw["domain_id"] = tftypes.NewValue(tftypes.String, "dom-1")
	return tftypes.NewValue(objType, raw)
}

func TestDomainVerificationSchema(t *testing.T) {
	s := verificationSchema(t)

	wait, ok := s.Attributes["wait_until_verified"].(schema.BoolAttribute)
	if !ok {
		t.Fatalf("wait_until_verified should be bool attribute, got %T", s.Attributes["wait_until_verified"])
	}
	if !wait.Optional || !wait.Computed {
		t.Error("wait_until_verified should be Optional+Computed")
	}
	// Modifier semantics pinned behaviorally in
	// TestWaitUntilVerifiedReplaceSemantics (type checks cannot distinguish
	// RequiresReplace from RequiresReplaceIfConfigured).
	if defaultBoolValue(t, wait) {
		t.Error("wait_until_verified must default to false (non-blocking verification)")
	}
}

// Default (unset wait_until_verified) triggers verification once and returns
// immediately with the current status — no polling, no timeout, CI applies
// stay green while DNS propagates.
func TestDomainVerificationCreateDefaultDoesNotWait(t *testing.T) {
	s := verificationSchema(t)
	g := &seqDomainGetter{statuses: []string{resend.DomainStatusPending}}
	v := &countingVerifier{}
	r := &domainVerificationResource{
		getter:        g,
		verifier:      v,
		sleep:         func(context.Context, time.Duration) error { return nil },
		reverifyEvery: time.Hour,
	}

	plan := verificationValue(t, s, "dom-1", tftypes.UnknownValue, false)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Raw: plan, Schema: s}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}

	r.Create(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create should succeed without waiting: %v", resp.Diagnostics)
	}
	if v.calls != 1 {
		t.Errorf("expected only the initial verify trigger, got %d", v.calls)
	}
	if g.calls != 1 {
		t.Errorf("expected exactly one status read, got %d", g.calls)
	}
	var state domainVerificationResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Status.ValueString() != resend.DomainStatusPending {
		t.Errorf("expected pending status recorded, got %q", state.Status.ValueString())
	}
	if state.WaitUntilVerified.ValueBool() {
		t.Error("wait_until_verified should be false in state")
	}
}

// Opt-in strict mode: polls until verified and re-triggers verification on a
// cadence while pending (Resend only re-checks DNS when verify is called).
func TestDomainVerificationCreateWaitsWhenOptedIn(t *testing.T) {
	s := verificationSchema(t)
	g := &seqDomainGetter{statuses: []string{
		resend.DomainStatusPending,
		resend.DomainStatusPending,
		resend.DomainStatusVerified,
	}}
	v := &countingVerifier{}
	r := &domainVerificationResource{
		getter:        g,
		verifier:      v,
		sleep:         func(context.Context, time.Duration) error { return nil },
		reverifyEvery: time.Nanosecond,
	}

	plan := verificationValue(t, s, "dom-1", tftypes.UnknownValue, true)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Raw: plan, Schema: s}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}

	r.Create(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create should succeed after waiting: %v", resp.Diagnostics)
	}
	if g.calls != 3 {
		t.Errorf("expected 3 polls, got %d", g.calls)
	}
	if v.calls != 3 { // initial trigger + one re-trigger per pending poll
		t.Errorf("expected initial trigger plus 2 re-triggers, got %d", v.calls)
	}
	var state domainVerificationResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Status.ValueString() != resend.DomainStatusVerified {
		t.Errorf("expected verified status, got %q", state.Status.ValueString())
	}
}

// Non-wait mode: Read keeps the resource while pending so the status
// converges via refresh instead of churning create/destroy.
func TestDomainVerificationReadKeepsPendingWhenNotWaiting(t *testing.T) {
	s := verificationSchema(t)
	g := &seqDomainGetter{statuses: []string{resend.DomainStatusPending}}
	r := &domainVerificationResource{getter: g}

	raw := verificationValue(t, s, "dom-1", resend.DomainStatusPending, false)
	st := tfsdk.State{Raw: raw, Schema: s}
	resp := &resource.ReadResponse{State: st}

	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read should succeed: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("non-waiting verification must stay in state while pending")
	}
	var state domainVerificationResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Status.ValueString() != resend.DomainStatusPending {
		t.Errorf("expected refreshed pending status, got %q", state.Status.ValueString())
	}
}

// Strict mode keeps v1.x semantics: a pending domain drops the resource so
// the next apply re-triggers verification.
func TestDomainVerificationReadDropsPendingWhenWaiting(t *testing.T) {
	s := verificationSchema(t)
	g := &seqDomainGetter{statuses: []string{resend.DomainStatusPending}}
	r := &domainVerificationResource{getter: g}

	raw := verificationValue(t, s, "dom-1", resend.DomainStatusVerified, true)
	st := tfsdk.State{Raw: raw, Schema: s}
	resp := &resource.ReadResponse{State: st}

	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read should succeed: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("waiting verification should drop from state when pending")
	}
}

// In-place updates (timeouts-only change, or removing an explicit
// wait_until_verified) carry computed attributes as unknown in the plan;
// final state must stay concrete or Terraform Core rejects the apply.
func TestDomainVerificationUpdateCarriesStatus(t *testing.T) {
	s := verificationSchema(t)
	r := &domainVerificationResource{getter: &seqDomainGetter{statuses: []string{resend.DomainStatusVerified}}}

	stateRaw := verificationValue(t, s, "dom-1", resend.DomainStatusVerified, true)
	planRaw := verificationValue(t, s, "dom-1", tftypes.UnknownValue, false)

	req := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Raw: planRaw, Schema: s},
		State: tfsdk.State{Raw: stateRaw, Schema: s},
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}

	r.Update(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("update should succeed: %v", resp.Diagnostics)
	}
	var model domainVerificationResourceModel
	if diags := resp.State.Get(context.Background(), &model); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if model.Status.IsUnknown() || model.Status.IsNull() {
		t.Fatal("status must be carried from state, not left unknown in final state")
	}
	if model.Status.ValueString() != resend.DomainStatusVerified {
		t.Errorf("status should carry over as verified, got %q", model.Status.ValueString())
	}
	if model.WaitUntilVerified.ValueBool() {
		t.Error("wait_until_verified should be false after removal")
	}
}

// A failing verify trigger must fail the create even in non-wait mode —
// silently recording a stale status would hide the API error.
func TestDomainVerificationCreateDefaultVerifyTriggerFails(t *testing.T) {
	s := verificationSchema(t)
	v := &countingVerifier{err: errors.New("missing_permissions")}
	r := &domainVerificationResource{
		getter:   &seqDomainGetter{statuses: []string{resend.DomainStatusPending}},
		verifier: v,
		sleep:    func(context.Context, time.Duration) error { return nil },
	}

	plan := verificationValue(t, s, "dom-1", tftypes.UnknownValue, false)
	req := resource.CreateRequest{Plan: tfsdk.Plan{Raw: plan, Schema: s}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}

	r.Create(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("create must fail when the verify trigger fails")
	}
	var msg strings.Builder
	for _, d := range resp.Diagnostics {
		msg.WriteString(d.Summary() + " " + d.Detail() + " ")
	}
	if !strings.Contains(msg.String(), "missing_permissions") {
		t.Errorf("error should surface the API cause, got: %s", msg.String())
	}
}

// Non-wait Read keeps the resource for every non-verified status; only
// strict mode drops.
func TestDomainVerificationReadKeepsAllStatusesWhenNotWaiting(t *testing.T) {
	s := verificationSchema(t)

	for _, status := range []string{
		resend.DomainStatusPending,
		resend.DomainStatusNotStarted,
		resend.DomainStatusFailed,
	} {
		g := &seqDomainGetter{statuses: []string{status}}
		r := &domainVerificationResource{getter: g}
		raw := verificationValue(t, s, "dom-1", resend.DomainStatusVerified, false)
		st := tfsdk.State{Raw: raw, Schema: s}
		resp := &resource.ReadResponse{State: st}

		r.Read(context.Background(), resource.ReadRequest{State: st}, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("read for %q errored: %v", status, resp.Diagnostics)
		}
		if resp.State.Raw.IsNull() {
			t.Errorf("non-waiting read must keep the resource for status %q", status)
		}
	}
}

func TestAccDomainVerificationNonWaitingDefault(t *testing.T) {
	testAccPreCheck(t)

	name := fmt.Sprintf("tfacc-%d.%s", time.Now().UnixNano(), testAccDomainBase(t))

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config: testAccVerificationNonWaitingConfig(name),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("resend_domain_verification.test", "wait_until_verified", "false"),
					tfresource.TestCheckResourceAttrSet("resend_domain_verification.test", "status"),
				),
			},
		},
	})
}

func TestAccDomainVerificationFailsWithoutDNS(t *testing.T) {
	testAccPreCheck(t)

	name := fmt.Sprintf("tfacc-%d.%s", time.Now().UnixNano(), testAccDomainBase(t))

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config:      testAccVerificationConfig(name),
				ExpectError: regexp.MustCompile(`not verified|verifying domain`),
			},
		},
	})
}

func testAccVerificationConfig(name string) string {
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %[1]q
  capabilities = {
    sending   = "enabled"
    receiving = "enabled"
  }
}

resource "resend_domain_verification" "test" {
  domain_id = resend_domain.test.id
  wait_until_verified = true
  timeouts = {
    create = "1m"
  }
}
`, name)
}

func testAccVerificationNonWaitingConfig(name string) string {
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %[1]q
  capabilities = {
    sending   = "enabled"
    receiving = "enabled"
  }
}

resource "resend_domain_verification" "test" {
  domain_id = resend_domain.test.id
}
`, name)
}
