# Verify a domain once its DNS records exist. `domain_id` accepts the
# resend_domain ID; with depends_on pointing at your DNS resources the
# verification only runs after the records are live.
resource "resend_domain_verification" "inbound" {
  domain_id = resend_domain.inbound.id

  # Optional: how long to wait for DNS propagation (default 30m).
  timeouts = {
    create = "45m"
  }

  depends_on = [cloudflare_record.resend]
}
