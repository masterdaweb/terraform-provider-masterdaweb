data "masterdaweb_instances" "web" {
  name_regex = "^web-"
}

resource "masterdaweb_firewall" "web" {
  name         = "web"
  rules        = [{ direction = "in", action = "ACCEPT", macro = "HTTPS" }]
  instance_ids = [for i in data.masterdaweb_instances.web.instances : i.id]
}
