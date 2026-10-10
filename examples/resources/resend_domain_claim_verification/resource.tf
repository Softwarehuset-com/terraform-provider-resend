resource "resend_domain_claim" "takeover" {
  name = "automated.appcompany.com"
}

# Trigger the claim's TXT record check. Default behaviour: the check runs
# once and the apply moves on; the status settles via refresh.
resource "resend_domain_claim_verification" "takeover" {
  domain_id = resend_domain_claim.takeover.domain_id

  # Point depends_on at the DNS resource publishing
  # resend_domain_claim.takeover.record, e.g.:
  #
  # depends_on = [cloudflare_record.claim_txt]

  # Strict variant: block until the claim verifies, at most 10m.
  #   wait_until_verified = true
  #   timeouts = {
  #     create = "10m"
  #   }
}
