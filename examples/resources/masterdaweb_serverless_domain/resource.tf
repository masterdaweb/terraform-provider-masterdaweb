resource "masterdaweb_serverless_domain" "site" {
  service_id = masterdaweb_serverless_service.web.id
  hostname   = "shop.example.com"
}

# The records to create at your DNS provider.
output "dns_records" {
  value = masterdaweb_serverless_domain.site.dns_records
}
