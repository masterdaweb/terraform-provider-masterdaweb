resource "masterdaweb_vpc_attachment" "web" {
  vpc_id      = masterdaweb_vpc.backend.id
  instance_id = masterdaweb_instance.web.id
  private_ip  = "10.20.0.10" # DHCP picks one when left out

  # Restart it so the private interface comes up now.
  restart = true
}
