package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	resend "github.com/resend/resend-go/v4"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.ResourceWithConfigure   = &apiKeyResource{}
	_ resource.ResourceWithImportState = &apiKeyResource{}
)

func NewApiKeyResource() resource.Resource {
	return &apiKeyResource{}
}

type apiKeyResource struct {
	client *resend.Client
}

type apiKeyResourceModel struct {
	Name       types.String `tfsdk:"name"`
	Permission types.String `tfsdk:"permission"`
	DomainID   types.String `tfsdk:"domain_id"`
	Token      types.String `tfsdk:"token"`
	ID         types.String `tfsdk:"id"`
	CreatedAt  types.String `tfsdk:"created_at"`
	LastUsedAt types.String `tfsdk:"last_used_at"`
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Resend API key. The token is returned only when the key is created; import leaves it null. If the key is deleted outside Terraform, the next plan recreates it with a new token. Destroying this resource deletes the key.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "API key name.",
			},
			"permission": schema.StringAttribute{
				Optional:    true,
				Description: "Access level: full_access (default) or sending_access. Changing it creates a new key (and token).",
				Validators: []validator.String{
					stringvalidator.OneOf("full_access", "sending_access"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"domain_id": schema.StringAttribute{
				Optional:    true,
				Description: "Resend domain id scoping a sending_access key to a single domain. Changing it creates a new key (and token).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"token": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "API key token (re_...). Only returned when the key is created; never shown again afterwards.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resend API key id.",
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
			"last_used_at": schema.StringAttribute{
				Computed:    true,
				Description: "Timestamp of the last request made with this key, if any.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *apiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.ApiKeys.CreateWithContext(ctx, &resend.CreateApiKeyRequest{
		Name:       plan.Name.ValueString(),
		Permission: plan.Permission.ValueString(),
		DomainId:   plan.DomainID.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("creating api key", err)...)
		return
	}
	plan.ID = types.StringValue(created.Id)
	plan.Token = types.StringValue(created.Token)

	// Persist state as soon as the remote object exists so a later failure
	// cannot orphan it. Refresh is best-effort and never sees the token.
	key, rerr := r.find(ctx, created.Id)
	if rerr != nil {
		resp.Diagnostics.AddWarning("refreshing api key after create", rerr.Error())
	} else if key != nil {
		mapApiKeyToState(&plan, key)
	}
	// Eventual consistency: if the refresh miss leaves computed attributes
	// unknown, normalize them to null — unknown values are invalid in final
	// state ("Provider returned invalid result object after apply").
	if plan.CreatedAt.IsUnknown() {
		plan.CreatedAt = types.StringNull()
	}
	if plan.LastUsedAt.IsUnknown() {
		plan.LastUsedAt = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.find(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(diagFromErr("reading api key", err)...)
		return
	}
	if key == nil {
		// Deleted outside Terraform: plan a recreate (with a new token).
		resp.State.RemoveResource(ctx)
		return
	}
	mapApiKeyToState(&state, key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apiKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Only the name is updatable; permission/domain_id changes force replacement.
	if plan.Name.ValueString() != state.Name.ValueString() {
		if _, err := r.client.ApiKeys.UpdateWithContext(ctx, plan.ID.ValueString(), &resend.UpdateApiKeyRequest{
			Name: plan.Name.ValueString(),
		}); err != nil {
			resp.Diagnostics.Append(diagFromErr("updating api key", err)...)
			return
		}
	}

	// No post-rename refresh: the list can lag behind the rename and would
	// clobber the new name into state (costing a no-op rename next apply).
	// Computed timestamps carry over from state via UseStateForUnknown.
	plan.CreatedAt = state.CreatedAt
	plan.LastUsedAt = state.LastUsedAt
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.ApiKeys.RemoveWithContext(ctx, state.ID.ValueString()); err != nil && !IsNotFound(err) {
		resp.Diagnostics.Append(diagFromErr("deleting api key", err)...)
		return
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// maxListPages bounds pagination so an API misbehaving with endless
// has_more cannot loop forever.
const maxListPages = 100

// find lists the account's API keys (following pagination) and returns the
// one with the given id, or nil when it no longer exists (drift).
func (r *apiKeyResource) find(ctx context.Context, id string) (*resend.ApiKey, error) {
	var after *string
	for page := 0; page < maxListPages; page++ {
		list, err := r.client.ApiKeys.ListWithOptions(ctx, &resend.ListOptions{After: after})
		if err != nil {
			return nil, err
		}
		if key := findApiKey(list.Data, id); key != nil {
			return key, nil
		}
		if !list.HasMore || len(list.Data) == 0 {
			return nil, nil
		}
		last := list.Data[len(list.Data)-1].Id
		after = &last
	}
	return nil, fmt.Errorf("listing api keys: more than %d pages", maxListPages)
}

func findApiKey(keys []resend.ApiKey, id string) *resend.ApiKey {
	for i := range keys {
		if keys[i].Id == id {
			return &keys[i]
		}
	}
	return nil
}

// mapApiKeyToState maps an API key as returned by List. The token and
// permission are never returned by the API and are intentionally preserved
// from whatever the model already holds.
func mapApiKeyToState(model *apiKeyResourceModel, key *resend.ApiKey) {
	model.ID = types.StringValue(key.Id)
	model.Name = types.StringValue(key.Name)
	if key.CreatedAt != "" {
		model.CreatedAt = types.StringValue(key.CreatedAt)
	}
	if key.LastUsedAt != nil && *key.LastUsedAt != "" {
		model.LastUsedAt = types.StringValue(*key.LastUsedAt)
	}
}
