data "masterdaweb_vpc" "default" {
  default = true
}

output "default_subnet" {
  value = data.masterdaweb_vpc.default.subnet
}
