# The gateway is an instance ordered from the VPN gateway image.
data "masterdaweb_vpn_gateway" "office" {
  name = "office-vpn"
}

# The platform generates the password and emails the person how to connect.
resource "masterdaweb_vpn_user" "maria" {
  gateway_id       = data.masterdaweb_vpn_gateway.office.id
  username         = "maria"
  email            = "maria@example.com"
  send_credentials = true
}

# A login whose password comes from a secret store, switched off for now.
resource "masterdaweb_vpn_user" "contractor" {
  gateway_id = data.masterdaweb_vpn_gateway.office.id
  username   = "acmecontractor"
  email      = "it@acme.example"
  password   = var.contractor_password
  status     = "inactive"
}

variable "contractor_password" {
  type      = string
  sensitive = true
}
