resource "resend_domain_claim" "takeover" {
  name = "automated.appcompany.com"
}

# Publish the claim's TXT record in the claimed domain's DNS. Example with
# the Cloudflare provider:
#
# resource "cloudflare_record" "claim_txt" {
#   zone_id = var.cloudflare_zone_id
#   name    = resend_domain_claim.takeover.record.name
#   type    = resend_domain_claim.takeover.record.type
#   value   = resend_domain_claim.takeover.record.value
#   ttl     = 1 # Auto
# }
#
# Then verify with resend_domain_claim_verification, pointing depends_on at
# the DNS record so it exists before verification runs.
