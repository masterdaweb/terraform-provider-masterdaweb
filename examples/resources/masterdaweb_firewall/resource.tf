resource "masterdaweb_firewall" "web" {
  name        = "web"
  description = "Web servers: HTTP(S) from anywhere, SSH from the office"
  policy_in   = "DROP"
  policy_out  = "ACCEPT"

  # Evaluated in this order.
  rules = [
    { direction = "in", action = "ACCEPT", macro = "SSH", source = "203.0.113.0/24", comment = "office" },
    { direction = "in", action = "ACCEPT", macro = "HTTP" },
    { direction = "in", action = "ACCEPT", macro = "HTTPS" },
    { direction = "in", action = "ACCEPT", protocol = "tcp", dest_port = "8000-8100", comment = "app ports" },
    { direction = "in", action = "ACCEPT", protocol = "icmp" },
  ]

  instance_ids = [masterdaweb_instance.web.id]
}
