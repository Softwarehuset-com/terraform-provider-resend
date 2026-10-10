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
	_ resource.ResourceWithConfigure   = &domainVerificationResource{}
	_ resource.ResourceWithImportState = &domainVerificationResource{}
)

const (
	verifyPollInterval   = 10 * time.Second
	verifyDefaultTimeout = 30 * time.Minute
	// reverifyInterval re-triggers the verify endpoint while waiting: Resend
	// only re-checks DNS records when verify is called, so polling GET alone
	// can read a stale "pending" long after the records are correct.
	reverifyInterval = 60 * time.Second
)

func NewDomainVerificationResource() resource.Resource {
	return &domainVerificationResource{}
}

type domainVerificationResource struct {
	client *resend.Client
	// getter is the seam tests use to stub domain reads; it defaults to
	// client.Domains in Configure.
	getter domaincheck.Getter
	// verifier triggers (and re-triggers) DNS verification; defaults to
	// client.Domains in Configure.
	verifier domaincheck.Verifier
	sleep    func(context.Context, time.Duration) error
	// reverifyEvery is the cadence for re-triggering verification while
	// waiting; tests shrink it to exercise re-verify without real delays.
	reverifyEvery time.Duration
}

type domainVerificationResourceModel struct {
	DomainID          types.String   `tfsdk:"domain_id"`
	WaitUntilVerified types.Bool     `tfsdk:"wait_until_verified"`
	Status            types.String   `tfsdk:"status"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (r *domainVerificationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain_verification"
}

func (r *domainVerificationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Triggers verification of a Resend domain. By default it triggers the DNS re-check once and returns immediately with the current status — apply stays green while DNS propagates. Set wait_until_verified = true to block until the domain is verified (with a create timeout, default 30m). Point depends_on at the DNS resources created from resend_domain.records so records exist before verification runs.",
		Attributes: map[string]schema.Attribute{
			"domain_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the resend_domain to verify. The verification resource's ID equals this value.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"wait_until_verified": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Wait (poll) until the domain reaches verified before apply completes. Default false: verification is triggered once and the resource is created with the current status; it converges to verified via refresh. Changing this forces re-running verification.",
				PlanModifiers: []planmodifier.Bool{
					// IfConfigured, not plain RequiresReplace: imported and
					// pre-2.0 states carry null while the plan fills the
					// default, and plain RequiresReplace would force-replace
					// every existing verification resource after import or
					// upgrade. Null config is a no-op; explicit toggles
					// replace.
					boolplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Current domain status at last trigger/refresh: pending, verified, partially_verified, not_started or failed. With wait_until_verified = true the create only completes on verified/partially_verified.",
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
			}),
		},
	}
}

func (r *domainVerificationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*resend.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", "expected *resend.Client")
		return
	}
	r.client = client
	r.getter = client.Domains
	r.verifier = client.Domains
	r.sleep = realSleep
	r.reverifyEvery = reverifyInterval
}

func (r *domainVerificationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainVerificationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID := plan.DomainID.ValueString()

	// Always trigger the DNS re-check once: it is the only way Resend
	// re-examines the domain's records.
	if _, err := r.verifier.VerifyWithContext(ctx, domainID); err != nil {
		resp.Diagnostics.Append(diagFromErr("triggering domain verification", err)...)
		return
	}

	if !plan.WaitUntilVerified.ValueBool() {
		// Non-blocking mode: record the current status and return. The
		// status converges to verified via refresh; apply never blocks on
		// DNS propagation.
		dom, err := r.getter.GetWithContext(ctx, domainID)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("reading domain after triggering verification", err)...)
			return
		}
		plan.Status = types.StringValue(dom.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, verifyDefaultTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	dom, err := domaincheck.WaitForVerified(ctx, r.getter, r.verifier, domainID, verifyPollInterval, timeout, r.reverifyEvery, r.sleep)
	if err != nil {
		resp.Diagnostics.AddError("verifying domain", err.Error())
		return
	}

	plan.Status = types.StringValue(dom.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainVerificationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainVerificationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	dom, err := r.getter.GetWithContext(ctx, state.DomainID.ValueString())
	if err != nil {
		if domaincheck.IsGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("reading domain verification", err)...)
		return
	}

	if state.WaitUntilVerified.ValueBool() {
		switch dom.Status {
		case resend.DomainStatusVerified, resend.DomainStatusPartiallyVerified:
			state.Status = types.StringValue(dom.Status)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		default:
			// Strict mode: verification regressed or never completed — drop
			// from state so the next apply re-triggers verification.
			resp.State.RemoveResource(ctx)
		}
		return
	}

	// Non-blocking mode: keep the resource regardless of status; the
	// status converges via refresh.
	state.Status = types.StringValue(dom.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *domainVerificationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// domain_id and wait_until_verified changes force replacement; this
	// path handles timeouts-only changes and removal of an explicit
	// wait_until_verified. Computed attributes arrive unknown in the plan —
	// carry them from state so final state stays concrete.
	var plan, state domainVerificationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Status = state.Status
	if plan.Status.IsUnknown() || plan.Status.IsNull() {
		plan.Status = types.StringValue("")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainVerificationResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// Verification has no remote object of its own; nothing to delete.
}

func (r *domainVerificationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("domain_id"), req, resp)
}

func realSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
