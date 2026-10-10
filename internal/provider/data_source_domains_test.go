package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	tfsdk "github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func domainsDataSourceSchema(t *testing.T) (datasourceschema.Schema, datasource.DataSource) {
	t.Helper()
	ds := NewDomainsDataSource()
	resp := datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp.Schema, ds
}

func TestDomainsDataSourceSchema(t *testing.T) {
	s, _ := domainsDataSourceSchema(t)

	attr, ok := s.Attributes["domains"]
	if !ok {
		t.Fatal("domains attribute missing")
	}
	if !attr.IsComputed() {
		t.Error("domains should be computed")
	}
}

// The list endpoint omits records, so every domain must be fetched in full
// after listing. Pins pagination plus the per-domain GET.
func TestDomainsDataSourceReadPaginatesAndFetchesRecords(t *testing.T) {
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/domains/d-1"):
			gets++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "d-1", "name": "early.appcompany.com", "status": "pending", "region": "eu-central-1",
				"records": []map[string]any{
					{"record": "DKIM", "name": "resend._domainkey", "type": "TXT", "ttl": "Auto", "status": "pending", "value": "p=one"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/domains/d-2"):
			gets++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "d-2", "name": "late.appcompany.com", "status": "verified", "region": "us-east-1",
				"records": []map[string]any{
					{"record": "DKIM", "name": "resend._domainkey", "type": "TXT", "ttl": "Auto", "status": "verified", "value": "p=two"},
					{"record": "SPF", "name": "send", "type": "TXT", "ttl": "Auto", "status": "verified", "value": "v=spf1"},
				},
			})
		default:
			switch r.URL.Query().Get("after") {
			case "":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{{"id": "d-1", "name": "early.appcompany.com"}}, "has_more": true,
				})
			case "d-1":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{{"id": "d-2", "name": "late.appcompany.com"}}, "has_more": false,
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "has_more": false})
			}
		}
	}))
	defer srv.Close()

	ds := &domainsDataSource{client: NewResendClient("re_test", srv.URL)}
	sch, _ := domainsDataSourceSchema(t)
	objType := sch.Type().TerraformType(context.Background())

	raw := map[string]tftypes.Value{}
	for attr, at := range objType.(tftypes.Object).AttributeTypes {
		raw[attr] = tftypes.NewValue(at, nil)
	}
	req := datasource.ReadRequest{
		Config: tfsdk.Config{Raw: tftypes.NewValue(objType, raw), Schema: sch},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: sch},
	}
	ds.Read(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read should succeed: %v", resp.Diagnostics)
	}
	if gets != 2 {
		t.Fatalf("each listed domain must be fetched in full, got %d GETs", gets)
	}

	var state domainsDataSourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if len(state.Domains) != 2 {
		t.Fatalf("expected 2 domains across pages, got %d", len(state.Domains))
	}
	if state.Domains[0].ID.ValueString() != "d-1" || state.Domains[1].ID.ValueString() != "d-2" {
		t.Errorf("list order not preserved: %s, %s", state.Domains[0].ID.ValueString(), state.Domains[1].ID.ValueString())
	}
	if len(state.Domains[1].Records.Elements()) != 2 {
		t.Errorf("records must come from the full GET (2 expected), got %d", len(state.Domains[1].Records.Elements()))
	}
}

func TestDomainsDataSourceReadEmptyAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "has_more": false})
	}))
	defer srv.Close()

	ds := &domainsDataSource{client: NewResendClient("re_test", srv.URL)}
	sch, _ := domainsDataSourceSchema(t)
	objType := sch.Type().TerraformType(context.Background())

	raw := map[string]tftypes.Value{}
	for attr, at := range objType.(tftypes.Object).AttributeTypes {
		raw[attr] = tftypes.NewValue(at, nil)
	}
	req := datasource.ReadRequest{
		Config: tfsdk.Config{Raw: tftypes.NewValue(objType, raw), Schema: sch},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: sch},
	}
	ds.Read(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("empty account should read successfully: %v", resp.Diagnostics)
	}
	var state domainsDataSourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if len(state.Domains) != 0 {
		t.Errorf("expected empty list, got %d domains", len(state.Domains))
	}
}

// Endless has_more must error, not silently truncate: a partial list would
// produce wrong downstream DNS wiring.
func TestDomainsDataSourcePaginationCapErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "d-loop", "name": "loop.appcompany.com"}}, "has_more": true,
		})
	}))
	defer srv.Close()

	ds := &domainsDataSource{client: NewResendClient("re_test", srv.URL)}
	sch, _ := domainsDataSourceSchema(t)
	objType := sch.Type().TerraformType(context.Background())

	raw := map[string]tftypes.Value{}
	for attr, at := range objType.(tftypes.Object).AttributeTypes {
		raw[attr] = tftypes.NewValue(at, nil)
	}
	req := datasource.ReadRequest{
		Config: tfsdk.Config{Raw: tftypes.NewValue(objType, raw), Schema: sch},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: sch},
	}
	ds.Read(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("endless pagination must error instead of returning a partial list")
	}
	var msg strings.Builder
	for _, d := range resp.Diagnostics {
		msg.WriteString(d.Summary() + " " + d.Detail() + " ")
	}
	if !strings.Contains(msg.String(), "more than") {
		t.Errorf("error should mention the page cap, got: %s", msg.String())
	}
}

func TestAccDomainsDataSource(t *testing.T) {
	testAccPreCheck(t)

	name := fmt.Sprintf("tfacc-%d.%s", time.Now().UnixNano(), testAccDomainBase(t))

	// Step 1 creates the domain; step 2 reads the list when the domain is
	// already in the account, and verifies it is listed with records (the
	// list endpoint omits records, so presence of records proves the
	// per-domain fetch too).
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config: testAccDomainsDataSourceStep1(name),
			},
			{
				Config: testAccDomainsDataSourceStep2(name),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("terraform_data.found", "output", "1"),
					tfresource.TestMatchResourceAttr("terraform_data.records", "output", regexp.MustCompile(`^[1-9][0-9]*$`)),
				),
			},
		},
	})
}

func testAccDomainsDataSourceStep1(name string) string {
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %[1]q
  capabilities = {
    sending   = "enabled"
    receiving = "enabled"
  }
}
`, name)
}

func testAccDomainsDataSourceStep2(name string) string {
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %[1]q
  capabilities = {
    sending   = "enabled"
    receiving = "enabled"
  }
}

data "resend_domains" "all" {}

locals {
  mine = [for d in data.resend_domains.all.domains : d if d.id == resend_domain.test.id]
}

resource "terraform_data" "found" {
  input = length(local.mine)
}

resource "terraform_data" "records" {
  input = length(local.mine) > 0 ? length(local.mine[0].records) : 0
}
`, name)
}
