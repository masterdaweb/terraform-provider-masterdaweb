resource "masterdaweb_serverless_variable" "environment" {
  service_id = masterdaweb_serverless_service.web.id
  key        = "ASPNETCORE_ENVIRONMENT"
  value      = "Production"
}

# Write-only once saved: Terraform keeps the value it was given.
resource "masterdaweb_serverless_variable" "stripe" {
  service_id = masterdaweb_serverless_service.web.id
  key        = "STRIPE_SECRET_KEY"
  value      = var.stripe_secret_key
  secret     = true
}

# Given to the build too.
resource "masterdaweb_serverless_variable" "public_api" {
  service_id         = masterdaweb_serverless_service.web.id
  key                = "NEXT_PUBLIC_API_URL"
  value              = "https://api.example.com"
  available_at_build = true
}
