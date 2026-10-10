package provider

import (
	"context"
	"time"

	"github.com/Softwarehuset-com/terraform-provider-resend/internal/domaincheck"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
}

type domainClaimVerificationResourceModel struct {
	DomainID      types.String   `tfsdk:"domain_id"`
	Status        types.String   `tfsdk:"status"`
	BlockedReason types.String   `tfsdk:"blocked_reason"`
	FailureReason types.String   `tfsdk:"failure_reason"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func (r *domainClaimVerificationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain_claim_verification"
}

func (r *domainClaimVerificationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Triggers verification of a Resend domain claim (resend_domain_claim) and waits until the TXT record check passes. Publish the claim's record in DNS first — point depends_on at your DNS resources so they exist before verification runs. A failed verification is not persisted, so the next apply retries automatically.",
		Attributes: map[string]schema.Attribute{
			"domain_id": schema.StringAttribute{
				Required:    true,
				Description: "domain_id of the resend_domain_claim to verify (the resource ID equals this value).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Claim status after verification: verified (ownership transfer queued) or completed (transfer done).",
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
}

func (r *domainClaimVerificationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainClaimVerificationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, claimVerifyDefaultTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID := plan.DomainID.ValueString()

	if _, err := r.claims.VerifyWithContext(ctx, domainID); err != nil {
		resp.Diagnostics.Append(diagFromErr("triggering domain claim verification", err)...)
		return
	}

	claim, err := domaincheck.WaitForClaimVerified(ctx, r.claims, domainID, claimVerifyPollInterval, timeout, r.sleep)
	if err != nil {
		resp.Diagnostics.AddError("verifying domain claim", err.Error())
		return
	}

	plan.Status = types.StringValue(claim.Status)
	if claim.BlockedReason != "" {
		plan.BlockedReason = types.StringValue(claim.BlockedReason)
	}
	if claim.FailureReason != "" {
		plan.FailureReason = types.StringValue(claim.FailureReason)
	}
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
		// apply re-triggers verification (e.g. after a grace period ends).
		resp.State.RemoveResource(ctx)
	}
}

func (r *domainClaimVerificationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// No mutable attributes: changes replace the resource.
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
