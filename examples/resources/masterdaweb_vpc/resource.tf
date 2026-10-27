resource "masterdaweb_vpc" "backend" {
  name        = "backend"
  description = "App servers and their database"
  region_id   = masterdaweb_instance.web.region_id
  subnet      = "10.20.0.0/24"
}
