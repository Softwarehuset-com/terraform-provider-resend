package provider

import (
	"context"
	"math/big"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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

type recordModel struct {
	Record   types.String `tfsdk:"record"`
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	Ttl      types.String `tfsdk:"ttl"`
	Status   types.String `tfsdk:"status"`
	Value    types.String `tfsdk:"value"`
	Priority types.Number `tfsdk:"priority"`
}

type domainCapabilitiesModel struct {
	Sending   types.String `tfsdk:"sending"`
	Receiving types.String `tfsdk:"receiving"`
}

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
	Records           []recordModel            `tfsdk:"records"`
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
				Description: "Region emails are sent from. One of us-east-1, eu-west-1, sa-east-1, ap-northeast-1.",
				Validators: []validator.String{
					stringvalidator.OneOf("us-east-1", "eu-west-1", "sa-east-1", "ap-northeast-1"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"custom_return_path": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Subdomain for the Return-Path address used for SPF alignment and bounce handling. Defaults to send.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tls": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "TLS policy for outbound mail: opportunistic or enforced.",
				Validators: []validator.String{
					stringvalidator.OneOf(resend.Opportunistic, resend.Enforced),
				},
			},
			"tracking_subdomain": schema.StringAttribute{
				Optional:    true,
				Description: "Custom subdomain for open/click tracking (produces a CNAME record).",
			},
			"open_tracking": schema.BoolAttribute{
				Optional:    true,
				Description: "Track email opens (requires tracking_subdomain).",
			},
			"click_tracking": schema.BoolAttribute{
				Optional:    true,
				Description: "Track link clicks (requires tracking_subdomain).",
			},
			"capabilities": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Sending/receiving capabilities. Changing capabilities requires replacement.",
				Attributes: map[string]schema.Attribute{
					"sending": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Description: "Enable sending: enabled or disabled.",
						Validators: []validator.String{
							stringvalidator.OneOf(resend.DomainCapabilityStatusEnabled, resend.DomainCapabilityStatusDisabled),
						},
					},
					"receiving": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Description: "Enable receiving (inbound email via MX records): enabled or disabled.",
						Validators: []validator.String{
							stringvalidator.OneOf(resend.DomainCapabilityStatusEnabled, resend.DomainCapabilityStatusDisabled),
						},
					},
				},
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplace(),
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resend domain UUID.",
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Domain status: not_started, pending, verified, partially_verified, failed, partially_failed.",
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "Creation timestamp.",
			},
			"records": schema.ListNestedAttribute{
				Computed:    true,
				Description: "DNS records Resend requires for this domain, including inbound MX records when receiving is enabled. Create these in your DNS provider.",
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
		Name:              plan.Name.ValueString(),
		Region:            plan.Region.ValueString(),
		CustomReturnPath:  plan.CustomReturnPath.ValueString(),
		TrackingSubdomain: plan.TrackingSubdomain.ValueString(),
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

	// The create endpoint does not accept tls; enforce it via update.
	if plan.Tls.ValueString() == resend.Enforced {
		_, err = r.client.Domains.UpdateWithContext(ctx, created.Id, &resend.UpdateDomainRequest{Tls: resend.Enforced})
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("enforcing TLS on domain", err)...)
			return
		}
	}

	resp.Diagnostics.Append(r.refreshDomain(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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
	if !plan.Tls.IsNull() && plan.Tls != state.Tls {
		updateReq.Tls = plan.Tls.ValueString()
	}
	if !plan.TrackingSubdomain.IsNull() && plan.TrackingSubdomain != state.TrackingSubdomain {
		updateReq.TrackingSubdomain = plan.TrackingSubdomain.ValueString()
	}
	if !plan.OpenTracking.IsNull() && plan.OpenTracking != state.OpenTracking {
		updateReq.SetOpenTracking(plan.OpenTracking.ValueBool())
	}
	if !plan.ClickTracking.IsNull() && plan.ClickTracking != state.ClickTracking {
		updateReq.SetClickTracking(plan.ClickTracking.ValueBool())
	}

	_, err := r.client.Domains.UpdateWithContext(ctx, plan.ID.ValueString(), updateReq)
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("updating domain", err)...)
		return
	}

	resp.Diagnostics.Append(r.refreshDomain(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
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

// refreshDomain fetches the current remote state of the domain into the model.
func (r *domainResource) refreshDomain(ctx context.Context, model *domainResourceModel) diag.Diagnostics {
	dom, err := r.client.Domains.GetWithContext(ctx, model.ID.ValueString())
	if err != nil {
		return diagFromErr("reading domain after write", err)
	}
	mapDomainToState(model, dom)
	return nil
}

func mapDomainToState(model *domainResourceModel, dom resend.Domain) {
	model.ID = types.StringValue(dom.Id)
	model.Name = types.StringValue(dom.Name)
	model.Status = types.StringValue(dom.Status)
	if dom.CreatedAt != "" {
		model.CreatedAt = types.StringValue(dom.CreatedAt)
	}
	if dom.Region != "" {
		model.Region = types.StringValue(dom.Region)
	}
	if dom.TrackingSubdomain != "" {
		model.TrackingSubdomain = types.StringValue(dom.TrackingSubdomain)
	}
	model.OpenTracking = types.BoolValue(dom.OpenTracking)
	model.ClickTracking = types.BoolValue(dom.ClickTracking)
	if dom.Capabilities != nil {
		if model.Capabilities == nil {
			model.Capabilities = &domainCapabilitiesModel{}
		}
		if dom.Capabilities.Sending != "" {
			model.Capabilities.Sending = types.StringValue(dom.Capabilities.Sending)
		}
		if dom.Capabilities.Receiving != "" {
			model.Capabilities.Receiving = types.StringValue(dom.Capabilities.Receiving)
		}
	}
	model.Records = mapRecords(dom.Records)
}

func mapRecords(records []resend.Record) []recordModel {
	out := make([]recordModel, 0, len(records))
	for _, rec := range records {
		m := recordModel{
			Record:   types.StringValue(rec.Record),
			Name:     types.StringValue(rec.Name),
			Type:     types.StringValue(rec.Type),
			Ttl:      types.StringValue(rec.Ttl),
			Status:   types.StringValue(rec.Status),
			Value:    types.StringValue(rec.Value),
			Priority: types.NumberNull(),
		}
		if rec.Priority != "" {
			if n, err := rec.Priority.Int64(); err == nil {
				m.Priority = types.NumberValue(big.NewFloat(float64(n)))
			}
		}
		out = append(out, m)
	}
	return out
}
