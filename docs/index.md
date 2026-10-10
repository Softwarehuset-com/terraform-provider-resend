---
subcategory: ""
page_title: "Provider: Resend"
description: |-
  Terraform provider for Resend, the email API for developers.
---

# Resend Provider

Manage [Resend](https://resend.com) domains, domain verification, API keys, domain claims and webhooks with Terraform. Existing domains can be read with the `resend_domain` data source (by id or name) to wire their DNS records into any DNS provider, and domains verified under another Resend account can be taken over via the `resend_domain_claim` + `resend_domain_claim_verification` pair.

The flagship use case is inbound email: register a domain with the `receiving` capability, publish the returned DNS records (including MX), verify the domain, and subscribe an `email.received` webhook so inbound mail is POSTed to your HTTP endpoint.

## Example Usage

```terraform
provider "resend" {
  api_key = var.resend_api_key # defaults to RESEND_API_KEY
}
```

## Authentication

The provider uses a Resend API key:

- Set the `api_key` attribute in the provider block, or
- export `RESEND_API_KEY`.

API keys are created in the [Resend dashboard](https://resend.com/dashboard/api-keys).

## Special Considerations

- DNS records are outputs, not managed objects: `resend_domain.records` returns every record (DKIM, SPF, MX, tracking CNAME). Create them with your DNS provider (Cloudflare, Route53, ...) and point `resend_domain_verification`'s `depends_on` at those resources.
- `resend_webhook.signing_secret` is only returned when the webhook is created or rotated; importing an existing webhook leaves it null. Rotate via the Resend dashboard or API if you need the value in state.
