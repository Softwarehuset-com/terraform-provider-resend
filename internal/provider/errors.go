package provider

import (
	"github.com/Softwarehuset-com/terraform-provider-resend/internal/domaincheck"
	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// IsNotFound reports whether an error returned by resend-go represents a
// 404/not-found response. The matching logic lives in domaincheck so the
// verification poller and provider resources share one implementation.
func IsNotFound(err error) bool {
	return domaincheck.IsGone(err)
}

// diagFromErr converts an API error into a single ERROR diagnostic whose
// Detail carries the original message verbatim.
func diagFromErr(summary string, err error) diag.Diagnostics {
	var d diag.Diagnostics
	d.AddError(summary, err.Error())
	return d
}
