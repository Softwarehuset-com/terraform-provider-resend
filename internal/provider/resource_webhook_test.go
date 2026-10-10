package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func webhookSchema(t *testing.T) schema.Schema {
	t.Helper()
	r, ok := NewWebhookResource().(*webhookResource)
	if !ok {
		t.Fatalf("expected *webhookResource, got %T", NewWebhookResource())
	}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestWebhookSchema(t *testing.T) {
	s := webhookSchema(t)

	endpoint, ok := s.Attributes["endpoint"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("endpoint should be string attribute, got %T", s.Attributes["endpoint"])
	}
	if !endpoint.Required {
		t.Error("endpoint should be required")
	}

	events, ok := s.Attributes["events"].(schema.SetAttribute)
	if !ok {
		t.Fatalf("events should be set attribute, got %T", s.Attributes["events"])
	}
	if !events.Required {
		t.Error("events should be required")
	}

	id, ok := s.Attributes["id"].(schema.StringAttribute)
	if !ok || !id.Computed {
		t.Error("id should be computed string")
	}

	secret, ok := s.Attributes["signing_secret"].(schema.StringAttribute)
	if !ok || !secret.Computed {
		t.Fatal("signing_secret should be computed string")
	}
	if !secret.Sensitive {
		t.Error("signing_secret should be sensitive")
	}

	for _, name := range []string{"status", "created_at"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("missing computed attribute %q", name)
		}
	}
}

func TestWebhookEventValidator(t *testing.T) {
	s := webhookSchema(t)
	events := s.Attributes["events"].(schema.SetAttribute)
	if len(events.Validators) == 0 {
		t.Fatal("events should carry validators")
	}

	run := func(event string) bool {
		req := validator.SetRequest{
			ConfigValue: types.SetValueMust(types.StringType, []attr.Value{types.StringValue(event)}),
		}
		hasErr := false
		for _, v := range events.Validators {
			resp := &validator.SetResponse{}
			v.ValidateSet(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				hasErr = true
			}
		}
		return hasErr
	}

	if !run("email.recieved") {
		t.Error(`typo "email.recieved" should be rejected by the events validator`)
	}
	if run("email.received") {
		t.Error(`"email.received" should be accepted by the events validator`)
	}
}

func TestAccWebhookBasic(t *testing.T) {
	testAccPreCheck(t)

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config: testAccWebhookConfig(`["email.received"]`),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttrSet("resend_webhook.test", "signing_secret"),
				),
			},
			{
				Config: testAccWebhookConfig(`["email.received", "email.sent"]`),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttrSet("resend_webhook.test", "signing_secret"),
				),
			},
			{
				ResourceName:            "resend_webhook.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"signing_secret"},
			},
		},
	})
}

func testAccWebhookConfig(events string) string {
	return fmt.Sprintf(`
resource "resend_webhook" "test" {
  endpoint = "https://api.appcompany.com/hooks/resend"
  events   = %s
}
`, events)
}
