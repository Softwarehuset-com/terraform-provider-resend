package provider

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	resend "github.com/resend/resend-go/v4"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.ResourceWithConfigure   = &webhookResource{}
	_ resource.ResourceWithImportState = &webhookResource{}
)

// webhookEvents is the closed set of event types Resend accepts on webhooks,
// sourced from the resend-go SDK constants.
var webhookEvents = []string{
	resend.EventEmailSent,
	resend.EventEmailDelivered,
	resend.EventEmailDeliveryDelayed,
	resend.EventEmailComplained,
	resend.EventEmailBounced,
	resend.EventEmailOpened,
	resend.EventEmailClicked,
	resend.EventEmailReceived,
	resend.EventEmailFailed,
	resend.EventEmailScheduled,
	resend.EventEmailSuppressed,
	resend.EventContactCreated,
	resend.EventContactUpdated,
	resend.EventContactDeleted,
	resend.EventContactTopicsUpdated,
	resend.EventDomainCreated,
	resend.EventDomainUpdated,
	resend.EventDomainDeleted,
	resend.EventSuppressionAdded,
	resend.EventSuppressionRemoved,
	resend.EventTopicCreated,
	resend.EventTopicUpdated,
	resend.EventTopicDeleted,
}

// httpsURLRegex accepts absolute HTTPS URLs with a host.
var httpsURLRegex = regexp.MustCompile(`^https://[^\s/$.?#].[^\s]*$`)

func NewWebhookResource() resource.Resource {
	return &webhookResource{}
}

type webhookResource struct {
	client *resend.Client
}

type webhookResourceModel struct {
	Endpoint      types.String `tfsdk:"endpoint"`
	Events        []string     `tfsdk:"events"`
	ID            types.String `tfsdk:"id"`
	SigningSecret types.String `tfsdk:"signing_secret"`
	Status        types.String `tfsdk:"status"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Resend webhook. Subscribe email.received (plus a domain with receiving enabled) to POST parsed inbound emails to your HTTP endpoint.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Required:    true,
				Description: "HTTPS URL Resend posts webhook events to.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(httpsURLRegex, "must be a valid HTTPS URL"),
				},
			},
			"events": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Event types to subscribe to, e.g. email.received.",
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.OneOf(webhookEvents...)),
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resend webhook UUID.",
			},
			"signing_secret": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Svix signing secret used to verify webhook deliveries. Only returned when the webhook is created or its secret is rotated; import leaves it null.",
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Webhook delivery status (e.g. enabled).",
			},
			"created_at": schema.StringAttribute{
				Computed:    true,
				Description: "Creation timestamp.",
			},
		},
	}
}

func (r *webhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *webhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan webhookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.Webhooks.CreateWithContext(ctx, &resend.CreateWebhookRequest{
		Endpoint: plan.Endpoint.ValueString(),
		Events:   plan.Events,
	})
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("creating webhook", err)...)
		return
	}
	plan.ID = types.StringValue(created.Id)
	if created.SigningSecret != "" {
		plan.SigningSecret = types.StringValue(created.SigningSecret)
	}

	resp.Diagnostics.Append(r.refreshWebhook(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state webhookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hook, err := r.client.Webhooks.GetWithContext(ctx, state.ID.ValueString())
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(diagFromErr("reading webhook", err)...)
		return
	}

	// GET does not return the signing secret; keep whatever state already has.
	secret := state.SigningSecret
	mapWebhookToState(&state, hook)
	state.SigningSecret = secret
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *webhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state webhookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := plan.Endpoint.ValueString()
	_, err := r.client.Webhooks.UpdateWithContext(ctx, plan.ID.ValueString(), &resend.UpdateWebhookRequest{
		Endpoint: &endpoint,
		Events:   plan.Events,
	})
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("updating webhook", err)...)
		return
	}

	secret := state.SigningSecret
	resp.Diagnostics.Append(r.refreshWebhook(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.SigningSecret = secret
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state webhookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Webhooks.RemoveWithContext(ctx, state.ID.ValueString())
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.Append(diagFromErr("deleting webhook", err)...)
		return
	}
}

func (r *webhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *webhookResource) refreshWebhook(ctx context.Context, model *webhookResourceModel) diag.Diagnostics {
	hook, err := r.client.Webhooks.GetWithContext(ctx, model.ID.ValueString())
	if err != nil {
		return diagFromErr("reading webhook after write", err)
	}
	mapWebhookToState(model, hook)
	return nil
}

func mapWebhookToState(model *webhookResourceModel, hook *resend.Webhook) {
	model.ID = types.StringValue(hook.Id)
	model.Endpoint = types.StringValue(hook.Endpoint)
	model.Events = hook.Events
	if hook.Status != "" {
		model.Status = types.StringValue(hook.Status)
	}
	if hook.CreatedAt != "" {
		model.CreatedAt = types.StringValue(hook.CreatedAt)
	}
}
