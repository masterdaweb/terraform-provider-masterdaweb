data "masterdaweb_firewall_macros" "all" {}

output "macros" {
  value = [for m in data.masterdaweb_firewall_macros.all.macros : m.name]
}
