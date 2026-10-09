package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	resend "github.com/resend/resend-go/v4"
)

type goneGetter struct{}

func (goneGetter) GetWithContext(context.Context, string) (resend.Domain, error) {
	return resend.Domain{}, &notFoundError{}
}

type notFoundError struct{}

func (*notFoundError) Error() string { return "[ERROR]: 404 Not Found" }

func verificationReadResponse(t *testing.T) *resource.ReadResponse {
	t.Helper()

	r := NewDomainVerificationResource()
	schemaResp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema errored: %v", schemaResp.Diagnostics)
	}
	s := schemaResp.Schema

	objType := s.Type().TerraformType(context.Background())
	attrTypes := objType.(tftypes.Object).AttributeTypes

	raw := tftypes.NewValue(objType, map[string]tftypes.Value{
		"domain_id": tftypes.NewValue(tftypes.String, "dom-1"),
		"status":    tftypes.NewValue(tftypes.String, resend.DomainStatusVerified),
		"timeouts":  tftypes.NewValue(attrTypes["timeouts"], nil),
	})

	return &resource.ReadResponse{
		State: tfsdk.State{Raw: raw, Schema: s},
	}
}

// TestDomainVerificationReadGone pins Review Focus 4: when the domain was
// deleted out-of-band, Read drops the verification resource from state
// instead of erroring.
func TestDomainVerificationReadGone(t *testing.T) {
	r := &domainVerificationResource{getter: goneGetter{}}

	resp := verificationReadResponse(t)
	req := resource.ReadRequest{State: resp.State}

	r.Read(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error on gone domain, got: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be removed when domain is gone")
	}
}

func TestAccDomainVerificationFailsWithoutDNS(t *testing.T) {
	testAccPreCheck(t)

	name := fmt.Sprintf("tfacc-%d.example.com", time.Now().UnixNano()%1_000_000)

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config:      testAccVerificationConfig(name),
				ExpectError: regexp.MustCompile(`not verified|verifying domain`),
			},
		},
	})
}

func testAccVerificationConfig(name string) string {
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %[1]q
  capabilities = {
    sending   = "enabled"
    receiving = "enabled"
  }
}

resource "resend_domain_verification" "test" {
  domain_id = resend_domain.test.id
  timeouts = {
    create = "1m"
  }
}
`, name)
}
