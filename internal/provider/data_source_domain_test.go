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
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	tfsdk "github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	resend "github.com/resend/resend-go/v4"
)

func domainDataSourceSchema(t *testing.T) datasourceschema.Schema {
	t.Helper()
	ds, ok := NewDomainDataSource().(*domainDataSource)
	if !ok {
		t.Fatalf("expected *domainDataSource, got %T", NewDomainDataSource())
	}
	resp := datasource.SchemaResponse{}
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestDomainDataSourceSchema(t *testing.T) {
	s := domainDataSourceSchema(t)

	for _, name := range []string{"id", "name"} {
		attr, ok := s.Attributes[name].(datasourceschema.StringAttribute)
		if !ok {
			t.Fatalf("%s should be string attribute, got %T", name, s.Attributes[name])
		}
		if !attr.Optional || !attr.Computed {
			t.Errorf("%s should be Optional+Computed (lookup input and output)", name)
		}
		// Exactly-one-of wiring: declared once (on `name`), evaluating the
		// whole (name, id) set; an id+name or empty config is rejected at
		// validation time. Behavior pinned in TestDomainDataSourceExactlyOneOf.
		if name, ok := s.Attributes["name"].(datasourceschema.StringAttribute); !ok || len(name.Validators) == 0 {
			t.Error("name should carry the exactly-one-of validator")
		}
	}

	for _, name := range []string{
		"status", "region", "created_at", "capabilities",
		"records", "open_tracking", "click_tracking", "tracking_subdomain",
	} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("missing computed output %q", name)
		}
		if !attr.IsComputed() {
			t.Errorf("%s should be computed", name)
		}
	}
}

func TestFilterDomainsByName(t *testing.T) {
	domains := []resend.Domain{
		{Id: "d-1", Name: "mail.appcompany.com"},
		{Id: "d-2", Name: "mail.appcompany.com"}, // duplicate: ambiguous
		{Id: "d-3", Name: "other.appcompany.com"},
	}

	if got := filterDomainsByName(domains, "other.appcompany.com"); len(got) != 1 || got[0].Id != "d-3" {
		t.Errorf("expected single match d-3, got %+v", got)
	}
	if got := filterDomainsByName(domains, "mail.appcompany.com"); len(got) != 2 {
		t.Errorf("expected 2 matches for ambiguous name, got %d", len(got))
	}
	if got := filterDomainsByName(domains, "nope.appcompany.com"); len(got) != 0 {
		t.Errorf("expected 0 matches, got %+v", got)
	}
}

func TestMapDomainToDataSourceState(t *testing.T) {
	model := &domainDataSourceModel{}
	dom := resend.Domain{
		Id:                "d-1",
		Name:              "mail.appcompany.com",
		Status:            "verified",
		Region:            "eu-central-1",
		CreatedAt:         "2026-10-10T10:00:00.000Z",
		OpenTracking:      true,
		ClickTracking:     false,
		TrackingSubdomain: "track",
		Records: []resend.Record{
			{Record: "resend", Name: "send._domainkey", Type: "CNAME", Ttl: "3600", Status: "verified", Value: "send.mail.appcompany.com"},
		},
		Capabilities: &resend.DomainCapabilities{Sending: "enabled", Receiving: "enabled"},
	}

	mapDomainToDataSourceState(model, dom)

	if model.ID.ValueString() != "d-1" || model.Name.ValueString() != "mail.appcompany.com" {
		t.Errorf("id/name not mapped: %+v", model)
	}
	if model.Status.ValueString() != "verified" || model.Region.ValueString() != "eu-central-1" {
		t.Errorf("status/region not mapped: %+v", model)
	}
	if model.OpenTracking.ValueBool() != true || model.ClickTracking.ValueBool() != false {
		t.Error("tracking flags not mapped")
	}
	if model.TrackingSubdomain.ValueString() != "track" {
		t.Error("tracking_subdomain not mapped")
	}
	if model.Records.IsNull() || len(model.Records.Elements()) != 1 {
		t.Errorf("records not mapped: %+v", model.Records)
	}
	if model.Capabilities == nil || model.Capabilities.Sending.ValueString() != "enabled" {
		t.Errorf("capabilities not mapped: %+v", model.Capabilities)
	}

	// Nil capabilities must not panic and leave nil model state.
	nilModel := &domainDataSourceModel{}
	mapDomainToDataSourceState(nilModel, resend.Domain{Id: "d-2", Name: "x", Status: "pending", Region: "us-east-1"})
	if nilModel.Capabilities != nil {
		t.Error("nil API capabilities should leave model capabilities nil")
	}
}

func TestAccDomainDataSource(t *testing.T) {
	testAccPreCheck(t)

	name := fmt.Sprintf("tfacc-%d.%s", time.Now().UnixNano(), testAccDomainBase(t))

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config: testAccDomainDataSourceConfig(name),
				Check: tfresource.ComposeTestCheckFunc(
					tfresource.TestCheckResourceAttr("data.resend_domain.test", "name", name),
					tfresource.TestCheckResourceAttrSet("data.resend_domain.test", "id"),
					tfresource.TestMatchResourceAttr("data.resend_domain.test", "status", regexp.MustCompile(`^(pending|not_started)$`)),
					tfresource.TestMatchResourceAttr("data.resend_domain.test", "records.#", regexp.MustCompile(`^[1-9]`)),
				),
			},
		},
	})
}

func TestAccDomainDataSourceByNameNotFound(t *testing.T) {
	testAccPreCheck(t)

	name := fmt.Sprintf("tfacc-none-%d.%s", time.Now().UnixNano(), testAccDomainBase(t))

	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProviderFactories,
		Steps: []tfresource.TestStep{
			{
				// Data source only: the domain must NOT exist, so the lookup
				// errors at plan time.
				Config: fmt.Sprintf(`
data "resend_domain" "test" {
  name = %q
}
`, name),
				ExpectError: regexp.MustCompile(`no domain named`),
			},
		},
	})
}

func testAccDomainDataSourceConfig(name string) string {
	return fmt.Sprintf(`
resource "resend_domain" "test" {
  name = %q
}

data "resend_domain" "test" {
  name = resend_domain.test.name
}
`, name)
}

// TestDomainDataSourceReadNamePathFetchesFullDomain pins the CI finding:
// the List endpoint omits records, so the name-lookup path must GET the
// matched domain by id to return the full record set.
func TestDomainDataSourceReadNamePathFetchesFullDomain(t *testing.T) {
	var getPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/domains"):
			// List: no records field at all.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "d-1", "name": "mail.appcompany.com", "status": "verified", "region": "us-east-1"}},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/domains/d-1"):
			getPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "d-1", "name": "mail.appcompany.com", "status": "verified", "region": "us-east-1",
				"records": []map[string]any{{"record": "SPF", "name": "send", "type": "TXT", "ttl": "Auto", "status": "verified", "value": "v=spf1"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := &domainDataSource{client: NewResendClient("re_test", srv.URL)}
	sch := domainDataSourceSchema(t)
	objType := sch.Type().TerraformType(context.Background())

	raw := map[string]tftypes.Value{
		"id":   tftypes.NewValue(tftypes.String, nil),
		"name": tftypes.NewValue(tftypes.String, "mail.appcompany.com"),
	}
	for attr, at := range objType.(tftypes.Object).AttributeTypes {
		if _, set := raw[attr]; !set {
			raw[attr] = tftypes.NewValue(at, nil)
		}
	}
	req := datasource.ReadRequest{
		Config: tfsdk.Config{Raw: tftypes.NewValue(objType, raw), Schema: sch},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: sch},
	}
	r.Read(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read should succeed: %v", resp.Diagnostics)
	}
	if getPath == "" {
		t.Fatal("name lookup must follow up with a GET on the matched id")
	}
	var state domainDataSourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.Records.IsNull() || len(state.Records.Elements()) != 1 {
		t.Errorf("records must come from the full GET, got: %v", state.Records)
	}
}

// TestDomainDataSourceNameLookupPaginates pins the review finding: domains
// past the first page must be findable by name.
func TestDomainDataSourceNameLookupPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/domains/d-2") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "d-2", "name": "late.appcompany.com", "status": "verified", "region": "us-east-1"})
			return
		}
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
	}))
	defer srv.Close()

	r := &domainDataSource{client: NewResendClient("re_test", srv.URL)}
	sch := domainDataSourceSchema(t)
	objType := sch.Type().TerraformType(context.Background())

	raw := map[string]tftypes.Value{
		"name": tftypes.NewValue(tftypes.String, "late.appcompany.com"),
	}
	for attr, at := range objType.(tftypes.Object).AttributeTypes {
		if _, set := raw[attr]; !set {
			raw[attr] = tftypes.NewValue(at, nil)
		}
	}
	req := datasource.ReadRequest{
		Config: tfsdk.Config{Raw: tftypes.NewValue(objType, raw), Schema: sch},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Raw: tftypes.NewValue(objType, tftypes.UnknownValue), Schema: sch},
	}
	r.Read(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read should succeed: %v", resp.Diagnostics)
	}
	var state domainDataSourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if state.ID.ValueString() != "d-2" {
		t.Errorf("page-2 domain must be found by name, got id %q", state.ID.ValueString())
	}
}

// TestDomainDataSourceExactlyOneOf is the behavioral test for the lookup
// validator: both-set and neither-set must fail, a single set side must
// pass. Runs the validator actually wired into the schema against full
// configs.
func TestDomainDataSourceExactlyOneOf(t *testing.T) {
	sch := domainDataSourceSchema(t)
	objType := sch.Type().TerraformType(context.Background())

	run := func(id, name interface{}) bool {
		raw := map[string]tftypes.Value{}
		for attr, at := range objType.(tftypes.Object).AttributeTypes {
			var v interface{}
			switch attr {
			case "id":
				v = id
			case "name":
				v = name
			default:
				v = nil
			}
			if v == tftypes.UnknownValue {
				raw[attr] = tftypes.NewValue(at, tftypes.UnknownValue)
			} else {
				raw[attr] = tftypes.NewValue(at, v)
			}
		}
		config := tfsdk.Config{Raw: tftypes.NewValue(objType, raw), Schema: sch}

		hasErr := false
		for _, attr := range []string{"id", "name"} {
			val := id
			if attr == "name" {
				val = name
			}
			cv := types.StringNull()
			if val != nil {
				cv = types.StringValue(val.(string))
			}
			a := sch.Attributes[attr].(datasourceschema.StringAttribute)
			req := validator.StringRequest{
				ConfigValue: cv,
				Config:      config,
			}
			for _, v := range a.Validators {
				resp := &validator.StringResponse{}
				v.ValidateString(context.Background(), req, resp)
				if resp.Diagnostics.HasError() {
					hasErr = true
				}
			}
		}
		return hasErr
	}

	if !run("d-1", "mail.appcompany.com") {
		t.Error("both id and name set should fail validation")
	}
	if !run(nil, nil) {
		t.Error("neither id nor name set should fail validation")
	}
	if run("d-1", nil) {
		t.Error("id-only lookup should pass validation")
	}
	if run(nil, "mail.appcompany.com") {
		t.Error("name-only lookup should pass validation")
	}
}
