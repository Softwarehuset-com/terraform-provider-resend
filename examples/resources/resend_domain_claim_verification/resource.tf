resource "resend_domain_claim" "takeover" {
  name = "automated.appcompany.com"
}

resource "resend_domain_claim_verification" "takeover" {
  domain_id = resend_domain_claim.takeover.domain_id

  # Point depends_on at the DNS resource publishing
  # resend_domain_claim.takeover.record, e.g.:
  #
  # depends_on = [cloudflare_record.claim_txt]

  timeouts = {
    create = "10m"
  }
}
