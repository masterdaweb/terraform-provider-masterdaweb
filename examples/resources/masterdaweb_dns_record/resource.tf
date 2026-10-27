resource "masterdaweb_dns_record" "www" {
  zone_id = masterdaweb_dns_zone.example.id
  name    = "www"
  type    = "A"
  ttl     = 300
  records = ["203.0.113.10", "203.0.113.11"]
}

resource "masterdaweb_dns_record" "blog" {
  zone_id = masterdaweb_dns_zone.example.id
  name    = "blog"
  type    = "CNAME"
  records = ["www.example.com"]
}

resource "masterdaweb_dns_record" "mx" {
  zone_id = masterdaweb_dns_zone.example.id
  name    = "@"
  type    = "MX"
  records = ["10 mail.example.com", "20 mail2.example.com"]
}

resource "masterdaweb_dns_record" "spf" {
  zone_id = masterdaweb_dns_zone.example.id
  name    = "@"
  type    = "TXT"
  records = ["v=spf1 mx -all"]
}
