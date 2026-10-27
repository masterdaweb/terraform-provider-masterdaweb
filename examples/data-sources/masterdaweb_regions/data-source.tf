data "masterdaweb_regions" "open" {
  available = true
}

output "regions" {
  value = { for r in data.masterdaweb_regions.open.regions : r.region_name => r.id }
}
