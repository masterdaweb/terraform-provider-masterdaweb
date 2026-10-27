data "masterdaweb_ssh_key" "laptop" {
  name = "laptop"
}

resource "masterdaweb_instance" "web" {
  name        = "web-1"
  region_id   = "3"
  plan        = "c2m4"
  image       = "debian-12"
  ssh_key_ids = [data.masterdaweb_ssh_key.laptop.id]
}
