resource "resend_domain" "inbound" {
  name = "mail.appcompany.com"

  capabilities = {
    receiving = "enabled"
  }
}

# A key that can only send email via the domain above.
resource "resend_api_key" "ci" {
  name       = "github-actions"
  permission = "sending_access"
  domain_id  = resend_domain.inbound.id
}

output "resend_api_key_token" {
  value     = resend_api_key.ci.token
  sensitive = true
}
