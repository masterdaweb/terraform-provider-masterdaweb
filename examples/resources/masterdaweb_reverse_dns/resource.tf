resource "masterdaweb_dns_record" "mail" {
  zone_id = masterdaweb_dns_zone.example.id
  name    = "mail"
  type    = "A"
  records = [masterdaweb_instance.mail.ipv4_address]
}

resource "masterdaweb_reverse_dns" "mail" {
  ip       = masterdaweb_instance.mail.ipv4_address
  hostname = "mail.example.com"

  # The name must resolve to the address first.
  depends_on = [masterdaweb_dns_record.mail]
}
