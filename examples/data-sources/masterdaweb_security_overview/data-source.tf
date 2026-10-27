data "masterdaweb_security_overview" "account" {}

output "security_score" {
  value = data.masterdaweb_security_overview.account.score
}

# A pipeline step can fail on new critical findings.
output "open_critical" {
  value = data.masterdaweb_security_overview.account.open_findings.critical
}
