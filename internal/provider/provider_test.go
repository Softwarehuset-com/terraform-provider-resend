package provider_test

import (
	"context"
	"os"
	"strings"
	"testing"

	tfprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/nicolajhartmann/terraform-provider-resend/internal/provider"
	"github.com/resend/resend-go/v4"
)

func testProvider(t *testing.T) tfprovider.Provider {
	t.Helper()
	return provider.New("test")()
}

// providerConfigFor builds a tfsdk.Config with every provider attribute set to
// the given values (nil meaning null).
func providerConfigFor(t *testing.T, p tfprovider.Provider, attrs map[string]*string) tfsdk.Config {
	t.Helper()
	schemaResp := tfprovider.SchemaResponse{}
	p.Schema(context.Background(), tfprovider.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("provider schema has errors: %v", schemaResp.Diagnostics)
	}

	vals := map[string]tftypes.Value{}
	for name := range schemaResp.Schema.Attributes {
		if v, ok := attrs[name]; ok && v != nil {
			vals[name] = tftypes.NewValue(tftypes.String, *v)
		} else {
			vals[name] = tftypes.NewValue(tftypes.String, nil)
		}
	}

	raw := tftypes.NewValue(
		tftypes.Object{AttributeTypes: map[string]tftypes.Type{
			"api_key":  tftypes.String,
			"base_url": tftypes.String,
		}},
		vals,
	)
	return tfsdk.Config{Raw: raw, Schema: schemaResp.Schema}
}

func TestProviderSchema(t *testing.T) {
	p := testProvider(t)
	resp := tfprovider.SchemaResponse{}
	p.Schema(context.Background(), tfprovider.SchemaRequest{}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics unexpectedly errored: %v", resp.Diagnostics)
	}

	apiKey, ok := resp.Schema.Attributes["api_key"]
	if !ok {
		t.Fatal("expected attribute api_key to exist")
	}
	if got := apiKey.GetType().String(); got != "basetypes.StringType" {
		t.Errorf("api_key type = %s, want string", got)
	}
	if !apiKey.IsOptional() {
		t.Error("api_key should be optional (RESEND_API_KEY env fallback)")
	}
	if apiKey.IsRequired() {
		t.Error("api_key must not be required; the env fallback would be unreachable")
	}
	if !apiKey.IsSensitive() {
		t.Error("api_key should be sensitive")
	}
	if !strings.Contains(apiKey.GetDescription(), "RESEND_API_KEY") {
		t.Errorf("api_key description should mention RESEND_API_KEY, got %q", apiKey.GetDescription())
	}

	baseURLAttr, ok := resp.Schema.Attributes["base_url"]
	if !ok {
		t.Fatal("expected attribute base_url to exist")
	}
	baseURL, ok := baseURLAttr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("base_url should be a string attribute, got %T", baseURLAttr)
	}
	if baseURL.Required {
		t.Error("base_url should be optional")
	}
	if !strings.Contains(baseURL.Description, "https://api.resend.com") {
		t.Errorf("base_url description should document the default, got %q", baseURL.Description)
	}
}

func TestProviderConfigureDefaultsBaseURL(t *testing.T) {
	p := testProvider(t)
	key := "re_test"
	cfg := providerConfigFor(t, p, map[string]*string{"api_key": &key})

	resp := tfprovider.ConfigureResponse{}
	p.Configure(context.Background(), tfprovider.ConfigureRequest{Config: cfg}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected configure to succeed, got: %v", resp.Diagnostics)
	}
	if _, ok := resp.ResourceData.(*resend.Client); !ok {
		t.Fatalf("expected ResourceData to be *resend.Client, got %T", resp.ResourceData)
	}
}

func TestProviderConfigureMissingKey(t *testing.T) {
	// Ensure the environment fallback does not paper over a missing key.
	old, wasSet := os.LookupEnv("RESEND_API_KEY")
	if wasSet {
		defer os.Setenv("RESEND_API_KEY", old)
		os.Unsetenv("RESEND_API_KEY")
	}

	p := testProvider(t)
	cfg := providerConfigFor(t, p, nil)

	resp := tfprovider.ConfigureResponse{}
	p.Configure(context.Background(), tfprovider.ConfigureRequest{Config: cfg}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected configure to error when api_key is missing")
	}
	found := false
	for _, d := range resp.Diagnostics {
		if strings.Contains(d.Summary(), "RESEND_API_KEY") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a diagnostic summary mentioning RESEND_API_KEY, got: %v", resp.Diagnostics)
	}
}
