# Look up an existing domain by name (or use `id` instead — exactly one).
data "resend_domain" "inbound" {
  name = "mail.appcompany.com"
}

# Wire its records into your DNS provider without owning the domain in this
# stack. Example with the Cloudflare provider:
#
# resource "cloudflare_record" "mx" {
#   for_each = {
#     for rec in data.resend_domain.inbound.records :
#     "${rec.record}-${rec.type}-${replace(rec.name, "*", "wild")}" => rec
#     if rec.type == "MX"
#   }
#
#   zone_id  = var.cloudflare_zone_id
#   type     = each.value.type
#   name     = each.value.name
#   value    = each.value.value
#   priority = each.value.priority
#   ttl      = 1 # Auto
# }
