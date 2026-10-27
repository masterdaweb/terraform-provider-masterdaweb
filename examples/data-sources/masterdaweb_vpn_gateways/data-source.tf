data "masterdaweb_vpn_gateways" "offices" {
  name_regex = "^office-"
}

output "gateways_with_pending_changes" {
  value = [for g in data.masterdaweb_vpn_gateways.offices.gateways : g.name if g.pending_changes]
}
