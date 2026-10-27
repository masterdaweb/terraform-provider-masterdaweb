# One finding: the replica's MySQL port, open on purpose to a vendor.
data "masterdaweb_security_findings" "mysql" {
  instance_id = "4123"
  search      = "mysql"
}

resource "masterdaweb_security_exception" "replica" {
  scope         = "finding"
  finding_id    = data.masterdaweb_security_findings.mysql.findings[0].id
  justification = "Read replica reachable only by the analytics vendor's fixed address (ticket OPS-412)."
  expires_at    = "2026-12-31T23:59:59Z"
}

# A port everywhere: a VPN's UDP port is meant to be open.
resource "masterdaweb_security_exception" "wireguard" {
  scope         = "port"
  port          = 51820
  protocol      = "udp"
  justification = "WireGuard listens here on every VPN gateway; reviewed by the security team."
  expires_at    = "2027-03-31T00:00:00Z"
}
