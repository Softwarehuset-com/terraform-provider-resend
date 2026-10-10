package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	tfsdk "github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	resend "github.com/resend/resend-go/v4"
)

func apiKeySchema(t *testing.T) schema.Schema {
	t.Helper()
	r, ok := NewApiKeyResource().(*apiKeyResource)
	if !ok {
		t.Fatalf("expected *apiKeyResource, got %T", NewApiKeyResource())
	}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestApiKeySchema(t *testing.T) {
	s := apiKeySchema(t)

	name, ok := s.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("name should be string attribute, got %T", s.Attributes["name"])
	}
	if !name.Required {
		t.Error("name should be required")
	}

	permission, ok := s.Attributes["permission"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("permission should be string attribute, got %T", s.Attributes["permission"])
	}
	if !permission.Optional {
		t.Error("permission should be optional")
	}
	if !hasRequiresReplaceIfConfigured(permission.PlanModifiers) {
		t.Error("permission should require replacement when configured")
	}

	domainID, ok := s.Attributes["domain_id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("domain_id should be string attribute, got %T", s.Attributes["domain_id"])
	}
	if !domainID.Optional {
		t.Error("domain_id should be optional")
	}
	if !hasRequiresReplaceIfConfigured(domainID.PlanModifiers) {
		t.Error("domain_id should require replacement when configured")
	}
	if hasRequiresReplaceIfConfigured(name.PlanModifiers) {
		t.Error("name should be updatable in place, not require replacement")
	}

	token, ok := s.Attributes["token"].(schema.StringAttribute)
	if !ok || !token.Computed {
		t.Fatal("token should be computed string")
	}
	if !token.Sensitive {
		t.Error("token should be sensitive")
	}

	id, ok := s.Attributes["id"].(schema.StringAttribute)
	if !ok || !id.Computed {
		t.Error("id should be computed string")
	}
}

// hasRequiresReplaceIfConfigured reports whether mods contains a
// RequiresReplaceIfConfigured plan modifier. Plan modifiers expose no
// equality, so concrete type is the practical comparison.
func hasRequiresReplaceIfConfigured(mods []planmodifier.String) bool {
	for _, m := range mods {
		if reflect.TypeOf(m) == reflect.TypeOf(stringplanmodifier.RequiresReplaceIfConfigured()) {
			return true
		}
	}
	return false
}

func TestApiKeyPermissionValidator(t *testing.T) {
	s := apiKeySchema(t)
	permission := s.Attributes["permission"].(schema.StringAttribute)
	if len(permission.Validators) == 0 {
		t.Fatal("permission should carry validators")
	}

	run := func(value string) bool {
		req := validator.StringRequest{
			ConfigValue: types.StringValue(value),
		}
		hasErr := false
		for _, v := range permission.Validators {
			resp := &validator.StringResponse{}
			v.ValidateString(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				hasErr = true
			}
		}
		return hasErr
	}

	if !run("total_access") {
		t.Error(`invalid permission "total_access" should be rejected`)
	}
	for _, valid := range []string{"full_access", "sending_access"} {
		if run(valid) {
			t.Errorf("permission %q should be accepted", valid)
		}
	}
}

func TestFindApiKey(t *testing.T) {
	keys := []resend.ApiKey{
		{Id: "key-1", Name: "ci"},
		{Id: "key-2", Name: "prod"},
	}

	if got := findApiKey(keys, "key-2"); got == nil || got.Name != "prod" {
		t.Errorf("expected to find key-2 (prod), got %+v", got)
	}
	// Drift: key deleted outside Terraform must resolve to nil so Read can
	// remove the resource from state and plan a recreate.
	if got := findApiKey(keys, "key-gone"); got != nil {
		t.Errorf("absent key should return nil, got %+v", got)
	}
}

func TestMapApiKeyToState(t *testing.T) {
	model := &apiKeyResourceModel{
		Name:       types.StringValue("ci"),
		Permission: types.StringValue("sending_access"),
		Token:      types.StringValue("re_secret_original"),
		ID:         types.StringValue("key-1"),
	}

	key := &resend.ApiKey{
		Id:         "key-1",
		Name:       "ci-renamed",
		CreatedAt:  "2026-10-10T10:00:00.000Z",
		LastUsedAt: strPtr("2026-10-10T11:00:00.000Z"),
	}

	mapApiKeyToState(model, key)

	if model.Name.ValueString() != "ci-renamed" {
		t.Errorf("name should follow API, got %q", model.Name.ValueString())
	}
	if model.ID.ValueString() != "key-1" {
		t.Errorf("id should be %q, got %q", "key-1", model.ID.ValueString())
	}
	// The API never returns the token; mapping must preserve it.
	if model.Token.ValueString() != "re_secret_original" {
		t.Errorf("token must be preserved across refresh, got %q", model.Token.ValueString())
	}
	if model.CreatedAt.ValueString() == "" {
		t.Error("created_at should be mapped")
	}
	if model.LastUsedAt.ValueString() != "2026-10-10T11:00:00.000Z" {
		t.Errorf("last_used_at should be mapped, got %q", model.LastUsedAt.ValueString())
	}
	// Permission is not returned by the API; mapping must preserve it.
	if model.Permission.ValueString() != "sending_access" {
		t.Errorf("permission must be preserved across refresh, got %q", model.Permission.ValueString())
	}
}

func TestAccApiKeyBasic(t *testing.T) {
	testAccPreCheck(t)

	domain := fmt.Sprintf("tfacc-%d.%s", time.Now().UnixNano(), testAccDomainBase(t))

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				// Create a sending_access key scoped to the throwaway domain.
				Config: testAccApiKeyConfig(domain, "github-actions", "sending_access", true),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("resend_api_key.test", "name", "github-actions"),
					tfresource.TestCheckResourceAttr("resend_api_key.test", "permission", "sending_access"),
					tfresource.TestCheckResourceAttrSet("resend_api_key.test", "token"),
					tfresource.TestCheckResourceAttrSet("resend_api_key.test", "id"),
				),
			},
			{
				// Rename updates in place: the token survives (no replacement).
				Config: testAccApiKeyConfig(domain, "github-actions-renamed", "sending_access", true),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("resend_api_key.test", "name", "github-actions-renamed"),
					tfresource.TestCheckResourceAttrSet("resend_api_key.test", "token"),
				),
			},
			{
				// Changing permission forces replacement; a new token is issued.
				Config: testAccApiKeyConfig(domain, "github-actions-renamed", "full_access", false),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("resend_api_key.test", "permission", "full_access"),
					tfresource.TestCheckResourceAttrSet("resend_api_key.test", "token"),
				),
			},
			{
				ResourceName:            "resend_api_key.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"token", "last_used_at"},
			},
		},
	})
}

func testAccApiKeyConfig(domain, name, permission string, domainScoped bool) string {
	scope := ""
	if domainScoped {
		scope = "domain_id = resend_domain.test.id"
	}
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %q
}

resource "resend_api_key" "test" {
  name       = %q
  permission = %q
  %s
}
`, domain, name, permission, scope)
}

func strPtr(s string) *string { return &s }

// TestApiKeyCreateSurvivesSlowList pins the CI failure "Provider returned
// invalid result object after apply": if the post-create List refresh does
// not yet contain the key (eventual consistency), computed attributes must
// be normalized to null instead of leaking unknown values into state.
func TestApiKeyCreateSurvivesSlowList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api-keys"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "key-1", "token": "re_secret"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/api-keys"):
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := &apiKeyResource{client: NewResendClient("re_test", srv.URL)}
	sch := apiKeySchema(t)
	objType := sch.Type().TerraformType(context.Background()).(tftypes.Object)
	attrTypes := objType.AttributeTypes

	raw := map[string]tftypes.Value{
		"name":         tftypes.NewValue(attrTypes["name"], "ci"),
		"token":        tftypes.NewValue(attrTypes["token"], tftypes.UnknownValue),
		"id":           tftypes.NewValue(attrTypes["id"], tftypes.UnknownValue),
		"created_at":   tftypes.NewValue(attrTypes["created_at"], tftypes.UnknownValue),
		"last_used_at": tftypes.NewValue(attrTypes["last_used_at"], tftypes.UnknownValue),
	}
	for _, a := range []string{"permission", "domain_id"} {
		raw[a] = tftypes.NewValue(attrTypes[a], nil)
	}

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Raw: tftypes.NewValue(objType, raw), Schema: sch},
	}
	resp := resource.CreateResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: sch},
	}
	r.Create(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create should succeed despite slow list: %v", resp.Diagnostics)
	}
	var state apiKeyResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Token.ValueString() != "re_secret" {
		t.Errorf("token must survive refresh miss, got %q", state.Token.ValueString())
	}
	if state.CreatedAt.IsUnknown() {
		t.Error("created_at must not leak unknown into final state when list misses the key")
	}
	if state.LastUsedAt.IsUnknown() {
		t.Error("last_used_at must not leak unknown into final state")
	}
}
