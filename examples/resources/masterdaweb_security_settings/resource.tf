resource "masterdaweb_security_settings" "this" {
  enabled                = true
  default_frequency      = "weekly"
  notify_on_new_critical = true
  notify_on_score_drop   = false
}
