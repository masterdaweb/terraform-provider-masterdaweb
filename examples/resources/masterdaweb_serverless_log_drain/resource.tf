resource "masterdaweb_serverless_log_drain" "datadog" {
  project_id = masterdaweb_project.shop.id
  kind       = "DATADOG"
  token      = var.datadog_api_key
  site       = "eu1"
}

resource "masterdaweb_serverless_log_drain" "betterstack" {
  project_id = masterdaweb_project.shop.id
  kind       = "BETTERSTACK"
  token      = var.betterstack_source_token
}

# Your own endpoint takes NDJSON lines: timestamp, message, project, service,
# revision, instance.
resource "masterdaweb_serverless_log_drain" "loki" {
  project_id = masterdaweb_project.shop.id
  kind       = "HTTPS"
  url        = "https://logs.example.com/ingest"
  token      = "Bearer ${var.ingest_token}"
}
