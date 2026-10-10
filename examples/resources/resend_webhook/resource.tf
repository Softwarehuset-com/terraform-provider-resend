# Mail -> HTTP: Resend POSTs parsed inbound emails to your endpoint
# whenever mail lands on any address of a domain with receiving enabled.
resource "resend_webhook" "inbound" {
  endpoint = "https://api.appcompany.com/hooks/resend"
  events   = ["email.received"]
}

# Store the signing secret securely; your HTTP handler uses it to verify
# Svix signature headers on every delivery.
output "webhook_signing_secret" {
  value     = resend_webhook.inbound.signing_secret
  sensitive = true
}
