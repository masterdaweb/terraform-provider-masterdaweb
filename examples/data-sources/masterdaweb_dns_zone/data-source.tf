data "masterdaweb_dns_zone" "example" {
  name = "example.com"
}

resource "masterdaweb_dns_record" "www" {
  zone_id = data.masterdaweb_dns_zone.example.id
  name    = "www"
  type    = "A"
  records = ["203.0.113.10"]
}
