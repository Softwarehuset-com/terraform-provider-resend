package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("[ERROR]: 404 Not Found"), true},
		{errors.New("[ERROR]: Domain not found"), true},
		{errors.New("[ERROR]: missing_permissions"), false},
		{errors.New("[ERROR]: 422 Unprocessable Entity"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsNotFound(c.err); got != c.want {
			t.Errorf("IsNotFound(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestDiagFromErr(t *testing.T) {
	d := diagFromErr("creating domain", errors.New("[ERROR]: Domain already exists"))

	if len(d) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(d))
	}
	if d[0].Severity() != diag.SeverityError {
		t.Errorf("expected ERROR severity, got %v", d[0].Severity())
	}
	if d[0].Summary() != "creating domain" {
		t.Errorf("expected summary %q, got %q", "creating domain", d[0].Summary())
	}
	if !strings.Contains(d[0].Detail(), "Domain already exists") {
		t.Errorf("expected detail to contain original message, got %q", d[0].Detail())
	}
}
