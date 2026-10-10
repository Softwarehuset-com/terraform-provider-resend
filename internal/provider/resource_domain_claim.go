package provider

import (
	"context"

	"github.com/Softwarehuset-com/terraform-provider-resend/internal/domaincheck"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	resend "github.com/resend/resend-go/v4"
)

// Ensure the implementation satisfies the expected interface.
var _ resource.ResourceWithConfigure = &domainClaimResource{}

// claimRecordAttrTypes mirrors the computed claim record object schema.
var claimRecordAttrTypes = map[string]attr.Type{
	"type":  types.StringType,
	"name":  types.StringType,
	"value": types.StringType,
	"ttl":   types.StringType,
}

func NewDomainClaimResource() resource.Resource {
	return &domainClaimResource{}
}

type domainClaimResource struct {
	client *resend.Client
}

// domainClaimResourceModel starts a domain claim. The claim's TXT record
// only becomes known after create, so verification is deliberately left to
// resend_domain_claim_verification once the record is published in DNS.
type domainClaimResourceModel struct {
	Name              types.String `tfsdk:"name"`
	Region            types.String `tfsdk:"region"`
	CustomReturnPath  types.String `tfsdk:"custom_return_path"`
	TrackingSubdomain types.String `tfsdk:"tracking_subdomain"`
	OpenTracking      types.Bool   `tfsdk:"open_tracking"`
	ClickTracking     types.Bool   `tfsdk:"click_tracking"`
	// record is Computed: it is an unknown object at plan time, which only
	// types.Object (not a struct pointer) can represent.
	Record        types.Object `tfsdk:"record"`
	ID            types.String `tfsdk:"id"`
	DomainID      types.String `tfsdk:"domain_id"`
	Status        types.String `tfsdk:"status"`
	BlockedReason types.String `tfsdk:"blocked_reason"`
	FailureReason types.String `tfsdk:"failure_reason"`
	ExpiresAt     types.String `tfsdk:"expires_at"`
}

func (r *domainClaimResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain_claim"
}

func (r *domainClaimResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Starts a Resend domain claim to take over a domain another Resend account has verified. Publish the computed TXT record in your DNS, then complete the flow with resend_domain_claim_verification. Destroying this resource does not cancel the claim server-side (no API exists); abandoned claims expire on their own.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Domain to claim.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"region": schema.StringAttribute{
				Optional:    true,
				Description: "Region to host the domain in once the claim completes.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"custom_return_path": schema.StringAttribute{
				Optional:    true,
				Description: "Custom subdomain for the bounce return path.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"tracking_subdomain": schema.StringAttribute{
				Optional:    true,
				Description: "Subdomain used for tracking links.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"open_tracking": schema.BoolAttribute{
				Optional:    true,
				Description: "Enable open tracking.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"click_tracking": schema.BoolAttribute{
				Optional:    true,
				Description: "Enable click tracking.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"record": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "TXT record proving ownership of the claim. Publish it in the claimed domain's DNS, then run resend_domain_claim_verification.",
				Attributes: map[string]schema.Attribute{
					"type":  schema.StringAttribute{Computed: true, Description: "DNS record type (TXT)."},
					"name":  schema.StringAttribute{Computed: true, Description: "Record name."},
					"value": schema.StringAttribute{Computed: true, Description: "Record value to publish."},
					"ttl":   schema.StringAttribute{Computed: true, Description: "TTL."},
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resend claim id.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"domain_id": schema.StringAttribute{
				Computed:    true,
				Description: "Placeholder domain id the claim created; also the id resend_domain_claim_verification references.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Claim status: pending, verified, completed, blocked, expired, superseded, canceled or failed. Updated on refresh.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"blocked_reason": schema.StringAttribute{
				Computed:    true,
				Description: "Why the claim is blocked (e.g. grace_period), when applicable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"failure_reason": schema.StringAttribute{
				Computed:    true,
				Description: "Why the claim failed, when applicable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expires_at": schema.StringAttribute{
				Computed:    true,
				Description: "When the claim expires if it is not verified.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *domainClaimResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*resend.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", "expected *resend.Client")
		return
	}
	r.client = client
}

func (r *domainClaimResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainClaimResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.DomainClaims.CreateWithContext(ctx, buildCreateClaimRequest(&plan))
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("creating domain claim", err)...)
		return
	}
	mapDomainClaimToState(&plan, created)
	finalizeClaimState(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainClaimResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainClaimResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Defensive: without a domain_id the claim Get would hit /domains//claim.
	// Treat as gone (the next apply starts a fresh claim).
	if state.DomainID.IsNull() || state.DomainID.IsUnknown() {
		resp.State.RemoveResource(ctx)
		return
	}

	claim, err := r.client.DomainClaims.GetWithContext(ctx, state.DomainID.ValueString())
	if err != nil {
		if domaincheck.IsGone(err) {
			// Claim expired or the placeholder domain was removed: plan a
			// recreate (which starts a fresh claim).
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("reading domain claim", err)...)
		return
	}
	mapDomainClaimToState(&state, claim)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *domainClaimResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every request attribute requires replacement; nothing is updatable.
	var plan domainClaimResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainClaimResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// Resend exposes no claim-cancel API. Dropping from state only; the
	// abandoned claim expires server-side (see expires_at).
}

func buildCreateClaimRequest(model *domainClaimResourceModel) *resend.CreateDomainClaimRequest {
	req := &resend.CreateDomainClaimRequest{
		Name: model.Name.ValueString(),
	}
	if !model.Region.IsNull() && !model.Region.IsUnknown() {
		req.Region = model.Region.ValueString()
	}
	if !model.CustomReturnPath.IsNull() && !model.CustomReturnPath.IsUnknown() {
		req.CustomReturnPath = model.CustomReturnPath.ValueString()
	}
	if !model.TrackingSubdomain.IsNull() && !model.TrackingSubdomain.IsUnknown() {
		req.TrackingSubdomain = model.TrackingSubdomain.ValueString()
	}
	if !model.OpenTracking.IsNull() && !model.OpenTracking.IsUnknown() {
		v := model.OpenTracking.ValueBool()
		req.OpenTracking = &v
	}
	if !model.ClickTracking.IsNull() && !model.ClickTracking.IsUnknown() {
		v := model.ClickTracking.ValueBool()
		req.ClickTracking = &v
	}
	return req
}

func mapDomainClaimToState(model *domainClaimResourceModel, claim resend.DomainClaim) {
	if claim.Id != "" {
		model.ID = types.StringValue(claim.Id)
	}
	if claim.DomainId != "" {
		model.DomainID = types.StringValue(claim.DomainId)
	}
	if claim.Status != "" {
		model.Status = types.StringValue(claim.Status)
	}
	// Region is request-only: the API materializes the effective region
	// even when config omits it, and region is not Computed — reading it
	// back would create a perpetual plan diff.
	if claim.BlockedReason != "" {
		model.BlockedReason = types.StringValue(claim.BlockedReason)
	}
	if claim.FailureReason != "" {
		model.FailureReason = types.StringValue(claim.FailureReason)
	}
	if claim.ExpiresAt != "" {
		model.ExpiresAt = types.StringValue(claim.ExpiresAt)
	}
	if claim.Record != nil {
		vals := map[string]attr.Value{
			"type":  types.StringValue(claim.Record.Type),
			"name":  types.StringValue(claim.Record.Name),
			"value": types.StringValue(claim.Record.Value),
			"ttl":   types.StringValue(claim.Record.Ttl),
		}
		if obj, diags := types.ObjectValue(claimRecordAttrTypes, vals); !diags.HasError() {
			model.Record = obj
		}
	}
}

// finalizeClaimState guarantees no computed attribute stays unknown in final
// state when the API response omitted it.
func finalizeClaimState(model *domainClaimResourceModel) {
	unknownToNull := func(v types.String) types.String {
		if v.IsUnknown() {
			return types.StringNull()
		}
		return v
	}
	model.ID = unknownToNull(model.ID)
	model.DomainID = unknownToNull(model.DomainID)
	model.Status = unknownToNull(model.Status)
	model.BlockedReason = unknownToNull(model.BlockedReason)
	model.FailureReason = unknownToNull(model.FailureReason)
	model.ExpiresAt = unknownToNull(model.ExpiresAt)
	if model.Record.IsUnknown() {
		model.Record = types.ObjectNull(claimRecordAttrTypes)
	}
}
