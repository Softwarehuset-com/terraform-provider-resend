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
	_ resource.ResourceWithConfigure   = &domainVerificationResource{}
	_ resource.ResourceWithImportState = &domainVerificationResource{}
)

const (
	verifyPollInterval   = 10 * time.Second
	verifyDefaultTimeout = 30 * time.Minute
)

func NewDomainVerificationResource() resource.Resource {
	return &domainVerificationResource{}
}

type domainVerificationResource struct {
	client *resend.Client
	// getter is the seam tests use to stub domain reads; it defaults to
	// client.Domains in Configure.
	getter domaincheck.Getter
}

type domainVerificationResourceModel struct {
	DomainID types.String   `tfsdk:"domain_id"`
	Status   types.String   `tfsdk:"status"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func (r *domainVerificationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain_verification"
}

func (r *domainVerificationResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Triggers verification of a Resend domain and waits until it completes. Point depends_on at the DNS resources created from resend_domain.records so records exist before verification runs.",
		Attributes: map[string]schema.Attribute{
			"domain_id": schema.StringAttribute{
				Required:    true,
				Description: "ID of the resend_domain to verify. The verification resource's ID equals this value.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Terminal domain status after verification: verified or partially_verified.",
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
}

func (r *domainVerificationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainVerificationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	timeout, diags := plan.Timeouts.Create(ctx, verifyDefaultTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID := plan.DomainID.ValueString()

	if _, err := r.client.Domains.VerifyWithContext(ctx, domainID); err != nil {
		resp.Diagnostics.Append(diagFromErr("triggering domain verification", err)...)
		return
	}

	dom, err := domaincheck.WaitForVerified(ctx, r.client.Domains, domainID, verifyPollInterval, timeout, realSleep)
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

	switch dom.Status {
	case resend.DomainStatusVerified, resend.DomainStatusPartiallyVerified:
		state.Status = types.StringValue(dom.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	default:
		// Verification regressed or never completed: drop from state so the
		// next apply re-triggers verification.
		resp.State.RemoveResource(ctx)
	}
}

func (r *domainVerificationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// No mutable attributes: changes replace the resource. Satisfy the
	// interface by carrying the plan through.
	var plan domainVerificationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
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
