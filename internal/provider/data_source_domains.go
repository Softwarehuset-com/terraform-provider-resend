package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	resend "github.com/resend/resend-go/v4"
)

// Ensure the implementation satisfies the expected interface.
var _ datasource.DataSourceWithConfigure = &domainsDataSource{}

func NewDomainsDataSource() datasource.DataSource {
	return &domainsDataSource{}
}

type domainsDataSource struct {
	client *resend.Client
}

// domainsDataSourceModel lists every domain in the account. Elements share
// the shape of the resend_domain data source (same model, same mappers) so
// DNS wiring works identically for a single lookup and a list.
type domainsDataSourceModel struct {
	Domains []domainDataSourceModel `tfsdk:"domains"`
}

func (d *domainsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domains"
}

func (d *domainsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := domainComputedOutputAttributes()
	attrs["id"] = datasourceschema.StringAttribute{
		Computed:    true,
		Description: "Resend domain id.",
	}
	attrs["name"] = datasourceschema.StringAttribute{
		Computed:    true,
		Description: "Domain name.",
	}
	attrs["records"] = domainRecordsAttribute()

	resp.Schema = datasourceschema.Schema{
		Description: "Reads every Resend domain in the account with its DNS records. The list endpoint omits records, so each domain is fetched in full after listing (one request per domain).",
		Attributes: map[string]datasourceschema.Attribute{
			"domains": datasourceschema.ListNestedAttribute{
				Computed:    true,
				Description: "All domains in the account, in the order the API returns them. Same element shape as the resend_domain data source.",
				NestedObject: datasourceschema.NestedAttributeObject{
					Attributes: attrs,
				},
			},
		},
	}
}

func (d *domainsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *domainsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config domainsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Page through the full list first.
	var listed []resend.Domain
	var after *string
	capped := true
	for page := 0; page < maxListPages; page++ {
		list, err := d.client.Domains.ListWithOptions(ctx, &resend.ListOptions{After: after})
		if err != nil {
			resp.Diagnostics.Append(diagFromErr("listing domains", err)...)
			return
		}
		listed = append(listed, list.Data...)
		if !list.HasMore || len(list.Data) == 0 {
			capped = false
			break
		}
		last := list.Data[len(list.Data)-1].Id
		after = &last
	}
	if capped {
		resp.Diagnostics.AddError(
			"listing domains",
			fmt.Sprintf("more than %d pages of domains; refusing to return a partial list", maxListPages),
		)
		return
	}

	// The List endpoint omits records; fetch each domain in full.
	state := &domainsDataSourceModel{Domains: []domainDataSourceModel{}}
	for _, dom := range listed {
		full, err := d.client.Domains.GetWithContext(ctx, dom.Id)
		if err != nil {
			resp.Diagnostics.Append(diagFromErr(fmt.Sprintf("reading domain %q", dom.Id), err)...)
			return
		}
		elem := domainDataSourceModel{}
		mapDomainToDataSourceState(&elem, full)
		state.Domains = append(state.Domains, elem)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
