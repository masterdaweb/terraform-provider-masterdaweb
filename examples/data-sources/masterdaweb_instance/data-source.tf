data "masterdaweb_instance" "legacy" {
  name = "legacy-web"
}

output "legacy_ipv4" {
  value = data.masterdaweb_instance.legacy.ipv4_address
}
