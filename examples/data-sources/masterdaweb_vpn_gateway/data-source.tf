data "masterdaweb_vpn_gateway" "office" {
  name = "office-vpn"
}

output "vpn_address" {
  value = data.masterdaweb_vpn_gateway.office.public_ip
}

output "wireguard_public_key" {
  value = data.masterdaweb_vpn_gateway.office.wireguard_public_key
}
