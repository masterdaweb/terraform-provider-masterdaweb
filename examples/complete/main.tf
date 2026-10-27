# An ASP.NET Core app (.NET 10, Entity Framework Core) on Master da Web:
# built from the public template repository, with a managed PostgreSQL it
# reads as ConnectionStrings__DefaultConnection, a private bucket it reads
# as MW_STORAGE_*, a domain of yours and its logs sent to Datadog.

resource "masterdaweb_project" "shop" {
  name                = "shop"
  description         = "The shop, in ASP.NET Core"
  deletion_protection = var.protect
}

resource "masterdaweb_database" "db" {
  name                = "shop-db"
  engine              = "postgres"
  size                = "pg-1-1"
  project_id          = masterdaweb_project.shop.id
  deletion_protection = var.protect
}

resource "masterdaweb_object_storage_bucket" "files" {
  name                = var.bucket_name
  deletion_protection = var.protect
}

resource "masterdaweb_serverless_service" "web" {
  project_id = masterdaweb_project.shop.id
  name       = "web"
  kind       = "WEB"

  git = {
    repo_url = "https://github.com/masterdaweb/mw-template-aspnet"
    branch   = "main"
  }

  port          = 8080
  size          = "small"
  min_instances = 1
  max_instances = 3

  # The repository's mw.toml sets the release command (./efbundle, the
  # migrations) and the health check (/health): settings it defines are
  # left out here.
}

# The database as ConnectionStrings__DefaultConnection, in Npgsql's format,
# with a login of the service's own.
resource "masterdaweb_serverless_binding" "db" {
  service_id    = masterdaweb_serverless_service.web.id
  resource_kind = "POSTGRES"
  resource_name = masterdaweb_database.db.name
  env_profile   = "dotnet"
}

# The bucket as MW_STORAGE_ENDPOINT, MW_STORAGE_BUCKET, MW_STORAGE_ACCESS_KEY_ID…
resource "masterdaweb_serverless_binding" "files" {
  service_id    = masterdaweb_serverless_service.web.id
  resource_kind = "BUCKET"
  resource_name = masterdaweb_object_storage_bucket.files.name
  env_profile   = "prefixed"
  env_prefix    = "MW_STORAGE_"
}

resource "masterdaweb_serverless_variable" "environment" {
  service_id = masterdaweb_serverless_service.web.id
  key        = "ASPNETCORE_ENVIRONMENT"
  value      = "Production"
}

resource "masterdaweb_serverless_domain" "site" {
  service_id = masterdaweb_serverless_service.web.id
  hostname   = var.domain
}

resource "masterdaweb_serverless_log_drain" "datadog" {
  project_id = masterdaweb_project.shop.id
  kind       = "DATADOG"
  token      = var.datadog_api_key
  site       = "us1"
}
