# The database as ConnectionStrings__DefaultConnection, in Npgsql's format.
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
  access        = "read_write"
}

# Redis as REDIS_URL.
resource "masterdaweb_serverless_binding" "cache" {
  service_id    = masterdaweb_serverless_service.web.id
  resource_kind = "REDIS"
  resource_name = masterdaweb_database.cache.name
}
