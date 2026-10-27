data "masterdaweb_project" "shop" {
  name = "shop"
}

resource "masterdaweb_serverless_service" "api" {
  project_id = data.masterdaweb_project.shop.id
  name       = "api"
  kind       = "PRIVATE"
  image      = { ref = "ghcr.io/acme/api:2.0" }
}
