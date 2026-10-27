resource "masterdaweb_dns_zone" "example" {
  name = "example.com"
}

# The NS records to set at the registrar.
output "name_servers" {
  value = masterdaweb_dns_zone.example.name_servers
}
