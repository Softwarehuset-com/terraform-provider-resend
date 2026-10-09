package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// NewDomainVerificationResource returns a stub until Task 4 replaces it.
func NewDomainVerificationResource() resource.Resource {
	return &domainVerificationResource{}
}

type domainVerificationResource struct{}

func (r *domainVerificationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain_verification"
}

func (r *domainVerificationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *domainVerificationResource) Configure(_ context.Context, _ resource.ConfigureRequest, _ *resource.ConfigureResponse) {
}

func (r *domainVerificationResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_domain_verification is implemented in task 4")
}

func (r *domainVerificationResource) Read(_ context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_domain_verification is implemented in task 4")
}

func (r *domainVerificationResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_domain_verification is implemented in task 4")
}

func (r *domainVerificationResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}
