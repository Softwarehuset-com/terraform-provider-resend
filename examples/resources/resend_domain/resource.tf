# Register automated.appcompany.com with sending + receiving (inbound) enabled.
resource "resend_domain" "inbound" {
  name = "automated.appcompany.com"

  capabilities = {
    sending   = "enabled"
    receiving = "enabled" # Resend serves MX records for inbound email
  }
}

# Publish the DNS records Resend requires (DKIM/SPF for sending, MX for
# receiving). Example with the Cloudflare provider:
#
# resource "cloudflare_record" "resend" {
#   for_each = {
#     for rec in resend_domain.inbound.records :
#     "${rec.record}-${rec.type}-${replace(rec.name, "*", "wild")}" => rec
#   }
#
#   zone_id = var.cloudflare_zone_id
#   type    = each.value.type
#   name    = each.value.name
#   value   = each.value.value
#   ttl     = 1 # Auto
#   priority = each.value.type == "MX" ? each.value.priority : null
#   allow_overwrite = true
# }

# Once the DNS records exist, verify the domain:
#
# resource "resend_domain_verification" "inbound" {
#   domain_id = resend_domain.inbound.id
#   depends_on = [cloudflare_record.resend]
# }
