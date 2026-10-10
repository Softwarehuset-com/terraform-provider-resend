package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
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

func claimVerificationRaw(objType tftypes.Type, domainID, status, wait interface{}) map[string]tftypes.Value {
	attrTypes := objType.(tftypes.Object).AttributeTypes
	return map[string]tftypes.Value{
		"domain_id": tftypes.NewValue(attrTypes["domain_id"], domainID),
		"status":    tftypes.NewValue(attrTypes["status"], status),
		// Real Terraform sends unknown for computed attributes at create.
		"blocked_reason":      tftypes.NewValue(attrTypes["blocked_reason"], tftypes.UnknownValue),
		"failure_reason":      tftypes.NewValue(attrTypes["failure_reason"], tftypes.UnknownValue),
		"wait_until_verified": tftypes.NewValue(attrTypes["wait_until_verified"], wait),
		"timeouts":            tftypes.NewValue(attrTypes["timeouts"], nil),
	}
}

func claimVerificationCreatePair(t *testing.T, wait interface{}) (resource.CreateRequest, *resource.CreateResponse) {
	t.Helper()
	s := claimVerificationSchemaResp(t).Schema
	objType := s.Type().TerraformType(context.Background())

	// Computed attributes are unknown at create, like real Terraform plans.
	raw := claimVerificationRaw(objType, "dom-1", tftypes.UnknownValue, wait)
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

	wait, ok := s.Attributes["wait_until_verified"].(schema.BoolAttribute)
	if !ok {
		t.Fatalf("wait_until_verified should be bool attribute, got %T", s.Attributes["wait_until_verified"])
	}
	if !wait.Optional || !wait.Computed {
		t.Error("wait_until_verified should be Optional+Computed")
	}
	if defaultBoolValue(t, wait) {
		t.Error("wait_until_verified must default to false (non-blocking verification)")
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

	req, resp := claimVerificationCreatePair(t, true)
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
	// Terraform Core rejects unknown values in final state ("Provider
	// returned invalid result object after apply"); success must leave the
	// reason attributes concrete even when the claim has none.
	if state.BlockedReason.IsUnknown() || state.BlockedReason.IsNull() {
		t.Error("blocked_reason must be a concrete (possibly empty) string in final state")
	}
	if state.FailureReason.IsUnknown() || state.FailureReason.IsNull() {
		t.Error("failure_reason must be a concrete (possibly empty) string in final state")
	}
}

// Opt-in strict mode re-triggers verification on a cadence while the claim
// stays pending: Resend only re-checks the TXT record when verify is called.
func TestDomainClaimVerificationCreateReTriggersWhilePending(t *testing.T) {
	fake := &fakeClaims{
		states: []resend.DomainClaim{
			{Status: resend.DomainClaimStatusPending},
			{Status: resend.DomainClaimStatusPending},
			{Status: resend.DomainClaimStatusVerified},
		},
	}
	r := &domainClaimVerificationResource{
		claims:        fake,
		sleep:         func(context.Context, time.Duration) error { return nil },
		reverifyEvery: time.Nanosecond,
	}

	req, resp := claimVerificationCreatePair(t, true)
	r.Create(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create should succeed: %v", resp.Diagnostics)
	}
	if fake.verifyCalls != 3 { // initial trigger + one re-trigger per pending poll
		t.Errorf("expected initial trigger plus 2 re-triggers, got %d", fake.verifyCalls)
	}
}

// Default (unset wait_until_verified) triggers verification once and returns
// immediately — no polling, no timeout.
func TestDomainClaimVerificationCreateDefaultDoesNotWait(t *testing.T) {
	fake := &fakeClaims{
		states: []resend.DomainClaim{{Status: resend.DomainClaimStatusPending, DomainId: "dom-1"}},
	}
	r := &domainClaimVerificationResource{
		claims: fake,
		sleep:  func(context.Context, time.Duration) error { return nil },
	}

	req, resp := claimVerificationCreatePair(t, false)
	r.Create(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create should succeed without waiting: %v", resp.Diagnostics)
	}
	if fake.verifyCalls != 1 {
		t.Errorf("expected only the initial verify trigger, got %d", fake.verifyCalls)
	}
	if fake.calls != 1 {
		t.Errorf("expected exactly one status read, got %d", fake.calls)
	}
	var state domainClaimVerificationResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Status.ValueString() != resend.DomainClaimStatusPending {
		t.Errorf("expected pending status recorded, got %q", state.Status.ValueString())
	}
	if state.WaitUntilVerified.ValueBool() {
		t.Error("wait_until_verified should be false in state")
	}
	// Reasons must be concrete even in non-wait mode.
	if state.BlockedReason.IsUnknown() || state.BlockedReason.IsNull() {
		t.Error("blocked_reason must be a concrete (possibly empty) string in final state")
	}
}

func claimVerificationCreateExpectError(t *testing.T, fake *fakeClaims, wantSubstr string) {
	t.Helper()
	r := &domainClaimVerificationResource{
		claims: fake,
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	req, resp := claimVerificationCreatePair(t, true)
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

	run := func(status string, wait interface{}) bool {
		fake := &fakeClaims{states: []resend.DomainClaim{{Status: status}}}
		r := &domainClaimVerificationResource{claims: fake}
		raw := claimVerificationRaw(objType, "dom-1", status, wait)
		st := tfsdk.State{Raw: tftypes.NewValue(objType, raw), Schema: s}
		resp := &resource.ReadResponse{State: st}
		r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("read for status %q errored: %v", status, resp.Diagnostics)
		}
		return !resp.State.Raw.IsNull()
	}

	// Strict mode (wait_until_verified = true) keeps v1.x semantics.
	for _, status := range []string{
		resend.DomainClaimStatusVerified,
		resend.DomainClaimStatusCompleted,
		resend.DomainClaimStatusExpired,
	} {
		if !run(status, true) {
			t.Errorf("status %q should keep the resource in state", status)
		}
	}
	if run(resend.DomainClaimStatusPending, true) {
		t.Error("pending claim should be removed from state to re-trigger verification")
	}

	// Non-waiting mode keeps the resource regardless of status.
	for _, status := range []string{
		resend.DomainClaimStatusPending,
		resend.DomainClaimStatusBlocked,
	} {
		if !run(status, false) {
			t.Errorf("non-waiting: status %q should keep the resource in state", status)
		}
	}
}

// Non-waiting Read refreshes status and keeps reason attributes concrete.
func TestDomainClaimVerificationReadRefreshesReasons(t *testing.T) {
	s := claimVerificationSchemaResp(t).Schema
	objType := s.Type().TerraformType(context.Background())

	fake := &fakeClaims{states: []resend.DomainClaim{{
		Status:        resend.DomainClaimStatusBlocked,
		BlockedReason: resend.DomainClaimBlockedReasonGracePeriod,
	}}}
	r := &domainClaimVerificationResource{claims: fake}

	raw := claimVerificationRaw(objType, "dom-1", resend.DomainClaimStatusPending, false)
	st := tfsdk.State{Raw: tftypes.NewValue(objType, raw), Schema: s}
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read should succeed: %v", resp.Diagnostics)
	}
	var state domainClaimVerificationResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Status.ValueString() != resend.DomainClaimStatusBlocked {
		t.Errorf("expected refreshed blocked status, got %q", state.Status.ValueString())
	}
	if got := state.BlockedReason; got.IsNull() || got.IsUnknown() || got.Equal(types.StringValue("")) {
		t.Errorf("expected refreshed blocked_reason, got %v", got)
	}
}
