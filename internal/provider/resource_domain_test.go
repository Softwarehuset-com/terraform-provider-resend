package provider

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	resend "github.com/resend/resend-go/v4"
)

func domainSchema(t *testing.T) schema.Schema {
	t.Helper()
	r, ok := NewDomainResource().(*domainResource)
	if !ok {
		t.Fatalf("expected *domainResource, got %T", NewDomainResource())
	}
	resp := resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics errored: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestDomainSchema(t *testing.T) {
	s := domainSchema(t)

	// Arguments
	name, ok := s.Attributes["name"]
	if !ok {
		t.Fatal("missing attribute name")
	}
	if sa, ok := name.(schema.StringAttribute); !ok || !sa.Required {
		t.Errorf("name should be required string")
	}

	region, ok := s.Attributes["region"]
	if !ok {
		t.Fatal("missing attribute region")
	}
	sa, ok := region.(schema.StringAttribute)
	if !ok || sa.Required || !sa.Optional {
		t.Errorf("region should be optional string")
	}
	if len(sa.PlanModifiers) == 0 {
		t.Error("region should have plan modifiers (RequiresReplace)")
	}

	caps, ok := s.Attributes["capabilities"]
	if !ok {
		t.Fatal("missing attribute capabilities")
	}
	oa, ok := caps.(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("capabilities should be single nested attribute, got %T", caps)
	}
	for _, sub := range []string{"sending", "receiving"} {
		if _, ok := oa.Attributes[sub]; !ok {
			t.Errorf("capabilities missing %q", sub)
		}
	}

	if _, ok := s.Attributes["custom_return_path"]; !ok {
		t.Error("missing attribute custom_return_path")
	}
	if _, ok := s.Attributes["tracking_subdomain"]; !ok {
		t.Error("missing attribute tracking_subdomain")
	}
	if _, ok := s.Attributes["open_tracking"]; !ok {
		t.Error("missing attribute open_tracking")
	}
	if _, ok := s.Attributes["click_tracking"]; !ok {
		t.Error("missing attribute click_tracking")
	}
	tlsAttr, ok := s.Attributes["tls"]
	if !ok {
		t.Fatal("missing attribute tls")
	}
	if ta, ok := tlsAttr.(schema.StringAttribute); !ok || !ta.Optional {
		t.Errorf("tls should be optional string")
	}

	// Computed
	for _, name := range []string{"id", "status", "created_at", "records"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("missing computed attribute %q", name)
		}
	}

}

func TestDomainSchemaValidators(t *testing.T) {
	s := domainSchema(t)

	region := s.Attributes["region"].(schema.StringAttribute)
	if len(region.Validators) == 0 {
		t.Error("region should carry a one-of validator")
	}

	tlsAttr := s.Attributes["tls"].(schema.StringAttribute)
	if len(tlsAttr.Validators) == 0 {
		t.Error("tls should carry a one-of validator")
	}

	caps := s.Attributes["capabilities"].(schema.SingleNestedAttribute)
	for _, sub := range []string{"sending", "receiving"} {
		attr, ok := caps.Attributes[sub].(schema.StringAttribute)
		if !ok {
			t.Fatalf("capabilities.%s should be a string attribute, got %T", sub, caps.Attributes[sub])
		}
		if len(attr.Validators) == 0 {
			t.Errorf("capabilities.%s should carry an enabled/disabled validator", sub)
		}
	}
}

func TestMapRecords(t *testing.T) {
	in := []resend.Record{
		{
			Record: "SPF",
			Name:   "send",
			Type:   "TXT",
			Ttl:    "Auto",
			Status: "pending",
			Value:  "v=spf1 include:amazonses.com ~all",
		},
		{
			Record:   "SPF",
			Name:     "send",
			Type:     "MX",
			Ttl:      "Auto",
			Status:   "pending",
			Value:    "feedback-smtp.us-east-1.amazonses.com",
			Priority: json.Number("10"),
		},
		{
			Record: "MX",
			Name:   "automated.appcompany.com",
			Type:   "MX",
			Ttl:    "Auto",
			Status: "pending",
			Value:  "inbound.resend.io",
		},
	}

	got := mapRecords(in)

	if len(got) != 3 {
		t.Fatalf("expected 3 records, got %d", len(got))
	}
	if !got[0].Priority.IsNull() {
		t.Errorf("TXT record priority should be null, got %s", got[0].Priority)
	}
	pf := got[1].Priority.ValueBigFloat()
	if pf == nil || pf.Cmp(big.NewFloat(10)) != 0 {
		t.Errorf("MX record priority should be 10, got %s", got[1].Priority)
	}
	if !got[2].Priority.IsNull() {
		t.Errorf("record without priority should map to null, got %s", got[2].Priority)
	}
	if got[0].Record.ValueString() != "SPF" || got[0].Type.ValueString() != "TXT" {
		t.Errorf("record fields not mapped: %+v", got[0])
	}
}
