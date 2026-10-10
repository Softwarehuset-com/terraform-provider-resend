# All domains in the account, records included.
data "resend_domains" "all" {}

# Flatten every domain's records into one list, ready for a DNS provider's
# for_each. The tuple key keeps domains apart when record names repeat.
locals {
  records = flatten([
    for d in data.resend_domains.all.domains : [
      for r in d.records : {
        domain = d.name
        record = r.record
        name   = r.name
        type   = r.type
        value  = r.value
      }
    ]
  ])
}

# Example: publish every record in Cloudflare. Record names are relative to
# the domain; resolve them against the zone before use.
#
# resource "cloudflare_record" "resend" {
#   for_each = { for r in local.records : "${r.domain}/${r.record}" => r }
#   zone_id  = var.zone_id
#   name     = "${r.name}.${r.domain}"
#   type     = r.type
#   value    = r.value
#   ttl      = 1 # auto
# }
