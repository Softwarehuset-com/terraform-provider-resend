package provider

import (
	"context"
	"math/big"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	resend "github.com/resend/resend-go/v4"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.ResourceWithImportState = &domainResource{}
	_ resource.ResourceWithConfigure   = &domainResource{}
)

func NewDomainResource() resource.Resource {
	return &domainResource{}
}

type domainResource struct {
	client *resend.Client
}

type domainCapabilitiesModel struct {
	Sending   types.String `tfsdk:"sending"`
	Receiving types.String `tfsdk:"receiving"`
}

// domainResourceModel uses types.List for records so the model can hold the
// unknown value Terraform plans for computed collections at create time.
type domainResourceModel struct {
	Name              types.String             `tfsdk:"name"`
	Region            types.String             `tfsdk:"region"`
	CustomReturnPath  types.String             `tfsdk:"custom_return_path"`
	Tls               types.String             `tfsdk:"tls"`
	TrackingSubdomain types.String             `tfsdk:"tracking_subdomain"`
	OpenTracking      types.Bool               `tfsdk:"open_tracking"`
	ClickTracking     types.Bool               `tfsdk:"click_tracking"`
	Capabilities      *domainCapabilitiesModel `tfsdk:"capabilities"`
	ID                types.String             `tfsdk:"id"`
	Status            types.String             `tfsdk:"status"`
	CreatedAt         types.String             `tfsdk:"created_at"`
	Records           types.List               `tfsdk:"records"`
}

// recordAttrTypes is the object type of a records element.
var recordAttrTypes = map[string]attr.Type{
	"record":   types.StringType,
	"name":     types.StringType,
	"type":     types.StringType,
	"ttl":      types.StringType,
	"status":   types.StringType,
	"value":    types.StringType,
	"priority": types.NumberType,
}

func (r *domainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *domainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Resend domain. Enabling the receiving capability makes Resend serve MX records so inbound email can be routed to webhooks.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Domain name, e.g. automated.appcompany.com.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"region": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Region emails are sent from. One of us-east-1, eu-west-1, sa-east-1, ap-northeast-1. Defaults to us-east-1.",
				Validators: []validator.String{
					stringvalidator.OneOf("us-east-1", "eu-west-1", "sa-east-1", "ap-northeast-1"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// tls and custom_return_path are write-only wishes: the Resend API
			// does not return them, so they are Optional-only (never Computed)
			// to avoid unknowable state and perpetual diffs.
			"custom_return_path": schema.StringAttribute{
				Optional:    true,
				Description: "Subdomain for the Return-Path address used for SPF alignment and bounce handling. Defaults to send on Resend's side. Changing this replaces the domain.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tls": schema.StringAttribute{
				Optional:    true,
				Description: "TLS policy for outbound mail: opportunistic or enforced. Applied via an update after creation.",
				Validators: []validator.String{
					stringvalidator.OneOf(resend.Opportunistic, resend.Enforced),
				},
			},
			"tracking_subdomain": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Custom subdomain for open/click tracking (produces a CNAME record). Empty when unset.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"open_tracking": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Track email opens (requires tracking_subdomain).",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"click_tracking": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Track link clicks (requires tracking_subdomain).",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"capabilities": schema.SingleNestedAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Sending/receiving capabilities. Changing capabilities requires replacement.",
				Attributes: map[string]schema.Attribute{
					"sending": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Description: "Enable sending: enabled or disabled. Defaults to enabled.",
						Validators: []validator.String{
							stringvalidator.OneOf(resend.DomainCapabilityStatusEnabled, resend.DomainCapabilityStatusDisabled),
						},
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
						},
					},
					"receiving": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Description: "Enable receiving (inbound email via MX records): enabled or disabled. Defaults to disabled.",
						Validators: []validator.String{
							stringvalidator.OneOf(resend.DomainCapabilityStatusEnabled, resend.DomainCapabilityStatusDisabled),
						},
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
						},
					},
				},
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
					objectplanmodifier.UseStateForUnknown(),
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resend domain UUID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Domain status: not_started, pending, verified, partially_verified, failed, partially_failed.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "Creation timestamp.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"records": schema.ListNestedAttribute{
				Computed:    true,
				Description: "DNS records Resend requires for this domain, including inbound MX records when receiving is enabled. Create these in your DNS provider.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"record":   schema.StringAttribute{Computed: true, Description: "Record purpose: DKIM, SPF, MX or Tracking."},
						"name":     schema.StringAttribute{Computed: true, Description: "Record name relative to the domain."},
						"type":     schema.StringAttribute{Computed: true, Description: "DNS type: TXT, MX or CNAME."},
						"ttl":      schema.StringAttribute{Computed: true, Description: "TTL (usually Auto)."},
						"status":   schema.StringAttribute{Computed: true, Description: "Verification status of this record."},
						"value":    schema.StringAttribute{Computed: true, Description: "Record value to publish."},
						"priority": schema.NumberAttribute{Computed: true, Description: "MX priority when type is MX, null otherwise."},
					},
				},
			},
		},
	}
}

func (r *domainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *domainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := &resend.CreateDomainRequest{
		Name:             plan.Name.ValueString(),
		Region:           plan.Region.ValueString(),
		CustomReturnPath: plan.CustomReturnPath.ValueString(),
	}
	if !plan.TrackingSubdomain.IsNull() && plan.TrackingSubdomain.IsUnknown() == false {
		createReq.TrackingSubdomain = plan.TrackingSubdomain.ValueString()
	}
	if !plan.OpenTracking.IsNull() {
		v := plan.OpenTracking.ValueBool()
		createReq.OpenTracking = &v
	}
	if !plan.ClickTracking.IsNull() {
		v := plan.ClickTracking.ValueBool()
		createReq.ClickTracking = &v
	}
	if plan.Capabilities != nil {
		caps := &resend.DomainCapabilities{
			Sending:   plan.Capabilities.Sending.ValueString(),
			Receiving: plan.Capabilities.Receiving.ValueString(),
		}
		createReq.Capabilities = caps
	}

	created, err := r.client.Domains.CreateWithContext(ctx, createReq)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("creating domain", err)...)
		return
	}
	plan.ID = types.StringValue(created.Id)

	// Persist state as soon as the remote object exists so a later failure
	// cannot orphan it. Refresh is best-effort here.
	if dom, rerr := r.client.Domains.GetWithContext(ctx, created.Id); rerr == nil {
		mapDomainToState(&plan, dom)
	} else {
		resp.Diagnostics.AddWarning("refreshing domain after create", rerr.Error())
	}
	finalizeDomainState(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The create endpoint does not accept tls; enforce it via update.
	if plan.Tls.ValueString() == resend.Enforced {
		if _, uerr := r.client.Domains.UpdateWithContext(ctx, created.Id, &resend.UpdateDomainRequest{Tls: resend.Enforced}); uerr != nil {
			resp.Diagnostics.Append(diagFromErr("enforcing TLS on domain", uerr)...)
			return
		}
		if dom, rerr := r.client.Domains.GetWithContext(ctx, created.Id); rerr == nil {
			mapDomainToState(&plan, dom)
		} else {
			resp.Diagnostics.AddWarning("refreshing domain after TLS update", rerr.Error())
		}
		finalizeDomainState(&plan)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *domainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	dom, err := r.client.Domains.GetWithContext(ctx, state.ID.ValueString())
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("reading domain", err)...)
		return
	}

	mapDomainToState(&state, dom)
	finalizeDomainState(&state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *domainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state domainResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateReq := &resend.UpdateDomainRequest{}
	if !plan.Tls.IsNull() && !plan.Tls.Equal(state.Tls) {
		updateReq.Tls = plan.Tls.ValueString()
	}
	if !plan.TrackingSubdomain.IsUnknown() && !plan.TrackingSubdomain.Equal(state.TrackingSubdomain) {
		if plan.TrackingSubdomain.IsNull() {
			// The API cannot unset a tracking subdomain; keep the recorded
			// value so plans stay stable.
			plan.TrackingSubdomain = state.TrackingSubdomain
		} else {
			updateReq.TrackingSubdomain = plan.TrackingSubdomain.ValueString()
		}
	}
	if !plan.OpenTracking.IsNull() && !plan.OpenTracking.Equal(state.OpenTracking) {
		updateReq.SetOpenTracking(plan.OpenTracking.ValueBool())
	}
	if !plan.ClickTracking.IsNull() && !plan.ClickTracking.Equal(state.ClickTracking) {
		updateReq.SetClickTracking(plan.ClickTracking.ValueBool())
	}

	if _, err := r.client.Domains.UpdateWithContext(ctx, plan.ID.ValueString(), updateReq); err != nil {
		resp.Diagnostics.Append(diagFromErr("updating domain", err)...)
		return
	}

	if dom, rerr := r.client.Domains.GetWithContext(ctx, plan.ID.ValueString()); rerr == nil {
		mapDomainToState(&plan, dom)
	} else {
		resp.Diagnostics.AddWarning("refreshing domain after update", rerr.Error())
	}
	finalizeDomainState(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state domainResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Domains.RemoveWithContext(ctx, state.ID.ValueString())
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.Append(diagFromErr("deleting domain", err)...)
		return
	}
}

func (r *domainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// mapDomainToState copies API-visible fields onto the model. Attributes the
// API does not return (tls, custom_return_path) are never touched here.
func mapDomainToState(model *domainResourceModel, dom resend.Domain) {
	model.ID = types.StringValue(dom.Id)
	model.Name = types.StringValue(dom.Name)
	model.Status = types.StringValue(dom.Status)
	if dom.CreatedAt != "" {
		model.CreatedAt = types.StringValue(dom.CreatedAt)
	}
	model.Region = types.StringValue(dom.Region)
	model.TrackingSubdomain = types.StringValue(dom.TrackingSubdomain)
	model.OpenTracking = types.BoolValue(dom.OpenTracking)
	model.ClickTracking = types.BoolValue(dom.ClickTracking)
	model.Records, _ = mapRecords(dom.Records)
	model.Capabilities = mapDomainCapabilities(dom.Capabilities, model.Capabilities)
}

// mapDomainCapabilities materializes API capabilities onto the model.
// capabilities is Optional+Computed: values may legitimately be populated
// from the API even when the config omitted the block (import/refresh), so
// an existing model object is filled in place. A nil API value leaves the
// model untouched. Shared by the resend_domain resource and data source.
func mapDomainCapabilities(caps *resend.DomainCapabilities, model *domainCapabilitiesModel) *domainCapabilitiesModel {
	if caps == nil {
		return model
	}
	if model == nil {
		model = &domainCapabilitiesModel{}
	}
	if caps.Sending != "" {
		model.Sending = types.StringValue(caps.Sending)
	}
	if caps.Receiving != "" {
		model.Receiving = types.StringValue(caps.Receiving)
	}
	return model
}

// finalizeDomainState guarantees every computed attribute holds a known,
// concrete value, resolving unknowns with documented API defaults. Terraform
// rejects unknown values in final state.
func finalizeDomainState(model *domainResourceModel) {
	if model.Records.IsNull() || model.Records.IsUnknown() {
		model.Records = types.ListValueMust(
			types.ObjectType{AttrTypes: recordAttrTypes},
			nil,
		)
	}
	if model.Status.IsNull() || model.Status.IsUnknown() {
		model.Status = types.StringValue(resend.DomainStatusNotStarted)
	}
	if model.CreatedAt.IsNull() || model.CreatedAt.IsUnknown() {
		model.CreatedAt = types.StringValue("")
	}
	if model.Region.IsNull() || model.Region.IsUnknown() {
		model.Region = types.StringValue("us-east-1")
	}
	if model.TrackingSubdomain.IsNull() || model.TrackingSubdomain.IsUnknown() {
		model.TrackingSubdomain = types.StringValue("")
	}
	if model.OpenTracking.IsNull() || model.OpenTracking.IsUnknown() {
		model.OpenTracking = types.BoolValue(false)
	}
	if model.ClickTracking.IsNull() || model.ClickTracking.IsUnknown() {
		model.ClickTracking = types.BoolValue(false)
	}
	if model.Tls.IsUnknown() {
		model.Tls = types.StringNull()
	}
	if model.Capabilities != nil {
		if model.Capabilities.Sending.IsNull() || model.Capabilities.Sending.IsUnknown() {
			model.Capabilities.Sending = types.StringValue(resend.DomainCapabilityStatusEnabled)
		}
		if model.Capabilities.Receiving.IsNull() || model.Capabilities.Receiving.IsUnknown() {
			model.Capabilities.Receiving = types.StringValue(resend.DomainCapabilityStatusDisabled)
		}
	}
}

func mapRecords(records []resend.Record) (types.List, diag.Diagnostics) {
	elems := make([]attr.Value, 0, len(records))
	for _, rec := range records {
		obj := map[string]attr.Value{
			"record":   types.StringValue(rec.Record),
			"name":     types.StringValue(rec.Name),
			"type":     types.StringValue(rec.Type),
			"ttl":      types.StringValue(rec.Ttl),
			"status":   types.StringValue(rec.Status),
			"value":    types.StringValue(rec.Value),
			"priority": types.NumberNull(),
		}
		if rec.Priority != "" {
			if n, err := rec.Priority.Int64(); err == nil {
				obj["priority"] = types.NumberValue(big.NewFloat(float64(n)))
			}
		}
		objVal, diags := types.ObjectValue(recordAttrTypes, obj)
		if diags.HasError() {
			return types.ListNull(types.ObjectType{AttrTypes: recordAttrTypes}), diags
		}
		elems = append(elems, objVal)
	}
	return types.ListValue(types.ObjectType{AttrTypes: recordAttrTypes}, elems)
}
