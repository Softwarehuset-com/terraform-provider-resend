package provider

import (
	"context"
	"time"

	"github.com/Softwarehuset-com/terraform-provider-resend/internal/domaincheck"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	resend "github.com/resend/resend-go/v4"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.ResourceWithConfigure   = &domainClaimVerificationResource{}
	_ resource.ResourceWithImportState = &domainClaimVerificationResource{}
)

const (
	claimVerifyPollInterval   = 10 * time.Second
	claimVerifyDefaultTimeout = 10 * time.Minute
	// claimReverifyInterval re-triggers the claim verify endpoint while
	// waiting: Resend only re-checks the TXT record when verify is called.
	claimReverifyInterval = 60 * time.Second
)

func NewDomainClaimVerificationResource() resource.Resource {
	return &domainClaimVerificationResource{}
}

// domainClaimsSvc is the seam between the resource and the SDK: the subset
// of resend.DomainClaimsSvc used here. *resend.DomainClaimsSvcImpl
// satisfies it via client.DomainClaims.
type domainClaimsSvc interface {
	domaincheck.ClaimGetter
	VerifyWithContext(ctx context.Context, domainID string) (resend.DomainClaim, error)
}

type domainClaimVerificationResource struct {
	client *resend.Client
	claims domainClaimsSvc
	sleep  func(context.Context, time.Duration) error
	// reverifyEvery is the cadence for re-triggering verification while
	// waiting; tests shrink it to exercise re-verify without real delays.
	reverifyEvery time.Duration
}

type domainClaimVerificationResourceModel struct {
	DomainID          types.String   `tfsdk:"domain_id"`
	WaitUntilVerified types.Bool     `tfsdk:"wait_until_verified"`
	Status            types.String   `tfsdk:"status"`
	BlockedReason     types.String   `tfsdk:"blocked_reason"`
	FailureReason     types.String   `tfsdk:"failure_reason"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (r *domainClaimVerificationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain_claim_verification"
}

func (r *domainClaimVerificationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Triggers verification of a Resend domain claim (resend_domain_claim). By default it triggers the TXT record check once and returns immediately with the current status — apply stays green while DNS propagates. Set wait_until_verified = true to block until the claim is verified (with a create timeout, default 10m). Publish the claim's record in DNS first — point depends_on at your DNS resources so they exist before verification runs.",
		Attributes: map[string]schema.Attribute{
			"domain_id": schema.StringAttribute{
				Required:    true,
				Description: "domain_id of the resend_domain_claim to verify (the resource ID equals this value).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"wait_until_verified": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Wait (poll) until the claim reaches verified/completed before apply completes. Default false: verification is triggered once and the resource is created with the current status; it converges via refresh. Changing this forces re-running verification.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Current claim status at last trigger/refresh: pending, verified, completed, blocked, failed, expired, superseded or canceled. With wait_until_verified = true the create only completes on verified/completed.",
			},
			"blocked_reason": schema.StringAttribute{
				Computed:    true,
				Description: "Why the claim is blocked (e.g. grace_period), when applicable.",
			},
			"failure_reason": schema.StringAttribute{
				Computed:    true,
				Description: "Why the claim failed, when applicable.",
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
			}),
		},
	}
}

func (r *domainClaimVerificationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*resend.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", "expected *resend.Client")
		return
	}
	r.client = client
	r.claims = client.DomainClaims
	r.sleep = realSleep
	r.reverifyEvery = claimReverifyInterval
}

func (r *domainClaimVerificationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainClaimVerificationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID := plan.DomainID.ValueString()

	// Always trigger the TXT record check once: it is the only way Resend
	// re-examines the claim's record.
	if _, err := r.claims.VerifyWithContext(ctx, domainID); err != nil {
		resp.Diagnostics.Append(diagFromErr("triggering domain claim verification", err)...)
		return
	}

	if !plan.WaitUntilVerified.ValueBool() {
		// Non-blocking mode: record the current status and return. The
		// status converges via refresh; apply never blocks on DNS
		// propagation.
		claim, err := r.claims.GetWithContext(ctx, domainID)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("reading domain claim after triggering verification", err)...)
			return
		}
		plan.Status = types.StringValue(claim.Status)
		// Unconditional: computed attributes must never stay unknown/null
		// in final state, even when empty.
		plan.BlockedReason = types.StringValue(claim.BlockedReason)
		plan.FailureReason = types.StringValue(claim.FailureReason)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, claimVerifyDefaultTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	claim, err := domaincheck.WaitForClaimVerified(ctx, r.claims, r.claims, domainID, claimVerifyPollInterval, timeout, r.reverifyEvery, r.sleep)
	if err != nil {
		resp.Diagnostics.AddError("verifying domain claim", err.Error())
		return
	}

	plan.Status = types.StringValue(claim.Status)
	// Unconditional: computed attributes must never stay unknown in final
	// state, even when empty.
	plan.BlockedReason = types.StringValue(claim.BlockedReason)
	plan.FailureReason = types.StringValue(claim.FailureReason)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainClaimVerificationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainClaimVerificationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	claim, err := r.claims.GetWithContext(ctx, state.DomainID.ValueString())
	if err != nil {
		if domaincheck.IsGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("reading domain claim verification", err)...)
		return
	}

	if state.WaitUntilVerified.ValueBool() {
		switch claim.Status {
		case resend.DomainClaimStatusVerified, resend.DomainClaimStatusCompleted,
			resend.DomainClaimStatusExpired, resend.DomainClaimStatusSuperseded, resend.DomainClaimStatusCanceled:
			// Stable states: keep in state; status is refreshed.
			state.Status = types.StringValue(claim.Status)
			if claim.BlockedReason != "" {
				state.BlockedReason = types.StringValue(claim.BlockedReason)
			}
			if claim.FailureReason != "" {
				state.FailureReason = types.StringValue(claim.FailureReason)
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		default:
			// pending/blocked/not-yet-verified: drop from state so the next
			// apply re-triggers verification (e.g. after a grace period
			// ends).
			resp.State.RemoveResource(ctx)
		}
		return
	}

	// Non-blocking mode: keep the resource regardless of status; status and
	// reasons converge via refresh.
	state.Status = types.StringValue(claim.Status)
	state.BlockedReason = types.StringValue(claim.BlockedReason)
	state.FailureReason = types.StringValue(claim.FailureReason)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *domainClaimVerificationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// No mutable attributes: domain_id and wait_until_verified changes
	// replace the resource.
	var plan domainClaimVerificationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainClaimVerificationResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// Verification has no remote object of its own; nothing to delete.
}

func (r *domainClaimVerificationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("domain_id"), req, resp)
}
