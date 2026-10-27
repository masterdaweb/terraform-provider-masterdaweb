data "masterdaweb_security_findings" "exploited" {
  severity = "critical"
}

output "actively_exploited" {
  value = [for f in data.masterdaweb_security_findings.exploited.findings : "${f.resource_name}: ${f.title}" if f.known_exploited]
}
