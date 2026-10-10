# Trigger a DNS re-check for the domain. Default behaviour: the verify
# endpoint is called once, the current status is recorded, and the apply
# moves on. A slow DNS propagation never fails the apply; the status
# settles to "verified" on later refreshes.
resource "resend_domain_verification" "inbound" {
  domain_id = resend_domain.inbound.id

  depends_on = [cloudflare_record.resend]
}

# Strict variant: fail the apply unless the domain verifies within 10m.
# While waiting, the provider re-triggers Resend's verify endpoint every
# 60 seconds, because Resend only re-checks DNS records when verify is
# called.
#
# resource "resend_domain_verification" "inbound" {
#   domain_id           = resend_domain.inbound.id
#   wait_until_verified = true
#
#   timeouts = {
#     create = "10m"
#   }
#
#   depends_on = [cloudflare_record.resend]
# }
