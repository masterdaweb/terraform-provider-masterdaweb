# Your endpoint gets signed JSON: check MW-Signature with the secret.
resource "masterdaweb_webhook" "ops" {
  url         = "https://ops.example.com/hooks/masterdaweb"
  description = "Deploys and alerts"
  events      = ["deployment.failed", "alert.*"]
}

output "webhook_secret" {
  value     = masterdaweb_webhook.ops.secret
  sensitive = true
}

# A Slack channel gets a message per event.
resource "masterdaweb_webhook" "billing" {
  kind   = "SLACK"
  url    = var.slack_webhook_url
  events = ["billing.*"]
  locale = "pt-BR"
}

variable "slack_webhook_url" {
  type      = string
  sensitive = true
}
