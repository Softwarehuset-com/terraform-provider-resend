package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	resend "github.com/resend/resend-go/v4"
)

// Ensure the implementation satisfies the expected interface.
var _ datasource.DataSourceWithConfigure = &domainDataSource{}

func NewDomainDataSource() datasource.DataSource {
	return &domainDataSource{}
}

type domainDataSource struct {
	client *resend.Client
}

// domainDataSourceModel looks up a domain by id or name (exactly one) and
// exposes everything the API returns, including DNS records, so they can be
// wired into a DNS provider without owning the domain in the same stack.
type domainDataSourceModel struct {
	ID                types.String             `tfsdk:"id"`
	Name              types.String             `tfsdk:"name"`
	Status            types.String             `tfsdk:"status"`
	Region            types.String             `tfsdk:"region"`
	CreatedAt         types.String             `tfsdk:"created_at"`
	Capabilities      *domainCapabilitiesModel `tfsdk:"capabilities"`
	Records           types.List               `tfsdk:"records"`
	OpenTracking      types.Bool               `tfsdk:"open_tracking"`
	ClickTracking     types.Bool               `tfsdk:"click_tracking"`
	TrackingSubdomain types.String             `tfsdk:"tracking_subdomain"`
}

func (d *domainDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (d *domainDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasourceschema.Schema{
		Description: "Reads an existing Resend domain by id or name (exactly one), including its DNS records — e.g. to wire records into a DNS provider for a domain managed outside this stack.",
		Attributes: map[string]datasourceschema.Attribute{
			"id": datasourceschema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Resend domain id to look up. Exactly one of id or name must be set.",
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("name")),
				},
			},
			"name": datasourceschema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Domain name to look up (exact match). Exactly one of id or name must be set.",
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("id")),
				},
			},
			"status":             datasourceschema.StringAttribute{Computed: true, Description: "Domain status: pending, verified, failed, not_started."},
			"region":             datasourceschema.StringAttribute{Computed: true, Description: "Region the domain is hosted in."},
			"created_at":         datasourceschema.StringAttribute{Computed: true, Description: "Creation timestamp."},
			"tracking_subdomain": datasourceschema.StringAttribute{Computed: true, Description: "Subdomain used for open tracking links."},
			"open_tracking":      datasourceschema.BoolAttribute{Computed: true, Description: "Whether open tracking is enabled."},
			"click_tracking":     datasourceschema.BoolAttribute{Computed: true, Description: "Whether click tracking is enabled."},
			"capabilities": datasourceschema.SingleNestedAttribute{
				Computed:    true,
				Description: "Sending/receiving capabilities.",
				Attributes: map[string]datasourceschema.Attribute{
					"sending":   datasourceschema.StringAttribute{Computed: true, Description: "Sending capability: enabled or disabled."},
					"receiving": datasourceschema.StringAttribute{Computed: true, Description: "Receiving (inbound email) capability: enabled or disabled."},
				},
			},
			"records": datasourceschema.ListNestedAttribute{
				Computed:    true,
				Description: "DNS records Resend requires for this domain, including inbound MX records when receiving is enabled. Same shape as resend_domain.records.",
				NestedObject: datasourceschema.NestedAttributeObject{
					Attributes: map[string]datasourceschema.Attribute{
						"record":   datasourceschema.StringAttribute{Computed: true, Description: "Record purpose: DKIM, SPF, MX or Tracking."},
						"name":     datasourceschema.StringAttribute{Computed: true, Description: "Record name relative to the domain."},
						"type":     datasourceschema.StringAttribute{Computed: true, Description: "DNS type: TXT, MX or CNAME."},
						"ttl":      datasourceschema.StringAttribute{Computed: true, Description: "TTL (usually Auto)."},
						"status":   datasourceschema.StringAttribute{Computed: true, Description: "Verification status of this record."},
						"value":    datasourceschema.StringAttribute{Computed: true, Description: "Record value to publish."},
						"priority": datasourceschema.NumberAttribute{Computed: true, Description: "MX priority when type is MX, null otherwise."},
					},
				},
			},
		},
	}
}

func (d *domainDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*resend.Client)
	if !ok {
		resp.Diagnostics.AddError("unexpected provider data type", "expected *resend.Client")
		return
	}
	d.client = client
}

func (d *domainDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config domainDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var dom resend.Domain
	if !config.ID.IsNull() && !config.ID.IsUnknown() {
		found, err := d.client.Domains.GetWithContext(ctx, config.ID.ValueString())
		if err != nil {
			resp.Diagnostics.Append(diagFromErr(fmt.Sprintf("reading domain %q", config.ID.ValueString()), err)...)
			return
		}
		dom = found
	} else {
		list, err := d.client.Domains.ListWithContext(ctx)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("listing domains", err)...)
			return
		}
		matches := filterDomainsByName(list.Data, config.Name.ValueString())
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError(
				"domain not found",
				fmt.Sprintf("no domain named %q", config.Name.ValueString()),
			)
			return
		case 1:
			dom = matches[0]
		default:
			resp.Diagnostics.AddError(
				"ambiguous domain name",
				fmt.Sprintf("%d domains named %q — look up by id instead", len(matches), config.Name.ValueString()),
			)
			return
		}
	}

	state := &domainDataSourceModel{}
	mapDomainToDataSourceState(state, dom)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func filterDomainsByName(domains []resend.Domain, name string) []resend.Domain {
	var matches []resend.Domain
	for _, dom := range domains {
		if dom.Name == name {
			matches = append(matches, dom)
		}
	}
	return matches
}

func mapDomainToDataSourceState(model *domainDataSourceModel, dom resend.Domain) {
	model.ID = types.StringValue(dom.Id)
	model.Name = types.StringValue(dom.Name)
	model.Status = types.StringValue(dom.Status)
	model.Region = types.StringValue(dom.Region)
	if dom.CreatedAt != "" {
		model.CreatedAt = types.StringValue(dom.CreatedAt)
	}
	model.TrackingSubdomain = types.StringValue(dom.TrackingSubdomain)
	model.OpenTracking = types.BoolValue(dom.OpenTracking)
	model.ClickTracking = types.BoolValue(dom.ClickTracking)
	model.Records, _ = mapRecords(dom.Records)
	model.Capabilities = mapDomainCapabilities(dom.Capabilities, model.Capabilities)
}

// mapDomainCapabilities materializes API capabilities onto the data source's
// struct model. Data source state is only ever written (never read from an
// unknown plan), so a struct pointer is safe here; the resend_domain
// resource instead uses types.Object (capabilitiesObjectFromAPI) because
// Optional+Computed objects arrive as unknown at plan time.
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
