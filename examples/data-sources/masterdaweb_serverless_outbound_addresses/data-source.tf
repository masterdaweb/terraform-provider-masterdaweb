data "masterdaweb_serverless_outbound_addresses" "current" {}

# Allow the services on a database elsewhere: the ranges are stable.
output "allowlist" {
  value = data.masterdaweb_serverless_outbound_addresses.current.ranges
}
