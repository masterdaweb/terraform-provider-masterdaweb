data "masterdaweb_firewall" "web" {
  name = "web"
}

output "web_rules" {
  value = data.masterdaweb_firewall.web.rules
}
