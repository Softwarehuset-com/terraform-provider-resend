package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// NewWebhookResource returns a stub until Task 5 replaces it.
func NewWebhookResource() resource.Resource {
	return &webhookResource{}
}

type webhookResource struct{}

func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true},
		},
	}
}

func (r *webhookResource) Configure(_ context.Context, _ resource.ConfigureRequest, _ *resource.ConfigureResponse) {
}

func (r *webhookResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_webhook is implemented in task 5")
}

func (r *webhookResource) Read(_ context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_webhook is implemented in task 5")
}

func (r *webhookResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("not implemented", "resend_webhook is implemented in task 5")
}

func (r *webhookResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}
