# The gateway is an instance ordered from the VPN gateway image.
data "masterdaweb_vpn_gateway" "office" {
  name = "office-vpn"
}

resource "masterdaweb_vpn_gateway_settings" "office" {
  gateway_id         = data.masterdaweb_vpn_gateway.office.id
  protocol           = "tcp"          # for networks that block UDP
  remote_access_pool = "10.66.0.0/24" # what people connecting get
  route_all_traffic  = true           # everything through the office
  mtu                = 1380

  # route_all_traffic needs a rebuild: a few minutes offline.
  allow_rebuild = true
}
