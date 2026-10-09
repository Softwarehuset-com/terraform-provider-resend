package provider

import (
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// IsNotFound reports whether an error returned by resend-go represents a
// 404/not-found response. The SDK flattens most errors into plain strings, so
// this matches on status code or "not found" text.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "404") || strings.Contains(msg, "not found")
}

// diagFromErr converts an API error into a single ERROR diagnostic whose
// Detail carries the original message verbatim.
func diagFromErr(summary string, err error) diag.Diagnostics {
	var d diag.Diagnostics
	d.AddError(summary, err.Error())
	return d
}
