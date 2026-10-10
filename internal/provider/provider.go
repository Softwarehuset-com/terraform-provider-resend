package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const defaultBaseURL = "https://api.resend.com"

// Ensure the implementation satisfies the provider.Provider interface.
var _ provider.Provider = &resendProvider{}

type resendProvider struct {
	version string
}

// New creates a factory for the Resend provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &resendProvider{version: version}
	}
}

func (p *resendProvider) Metadata(_ context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "resend"
	resp.Version = p.version
}

func (p *resendProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Resend API key. Defaults to the RESEND_API_KEY environment variable.",
			},
			"base_url": schema.StringAttribute{
				Optional:    true,
				Description: "Base URL of the Resend API. Defaults to https://api.resend.com. Primarily useful for testing against a stub server.",
			},
		},
	}
}

type resendProviderModel struct {
	APIKey  types.String `tfsdk:"api_key"`
	BaseURL types.String `tfsdk:"base_url"`
}

func (p *resendProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config resendProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey := config.APIKey.ValueString()
	if apiKey == "" {
		apiKey = os.Getenv("RESEND_API_KEY")
	}
	if apiKey == "" {
		resp.Diagnostics.AddError(
			"Missing Resend API key (RESEND_API_KEY)",
			"The provider could not determine an API key. Set the api_key attribute in the provider configuration block or export RESEND_API_KEY.",
		)
		return
	}

	baseURL := config.BaseURL.ValueString()
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	client := NewResendClient(apiKey, baseURL)
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *resendProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewDomainResource,
		NewDomainVerificationResource,
		NewWebhookResource,
		NewApiKeyResource,
		NewDomainClaimResource,
		NewDomainClaimVerificationResource,
	}
}

func (p *resendProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewDomainDataSource,
		NewDomainsDataSource,
	}
}
