package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// NewDomainResource returns a stub until Task 3 replaces it.
func NewDomainResource() resource.Resource {
	return &domainResource{}
}

type domainResource struct{}

func (r *domainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *domainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *domainResource) Configure(_ context.Context, _ resource.ConfigureRequest, _ *resource.ConfigureResponse) {
}

func (r *domainResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_domain is implemented in task 3")
}

func (r *domainResource) Read(_ context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_domain is implemented in task 3")
}

func (r *domainResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_domain is implemented in task 3")
}

func (r *domainResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}
