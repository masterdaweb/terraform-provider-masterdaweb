resource "masterdaweb_database" "db" {
  name       = "shop-db"
  engine     = "postgres"
  version    = "18"
  size       = "pg-1-1"
  project_id = masterdaweb_project.shop.id
  extensions = ["vector", "pg_trgm"]

  # Your own address, besides the services bound to it.
  allowed_cidrs = ["203.0.113.7"]

  maintenance_day      = "sunday"
  maintenance_hour_utc = 4

  # On by default. To destroy the database, set it to false and apply first.
  deletion_protection = true
}

resource "masterdaweb_database" "cache" {
  name            = "shop-cache"
  engine          = "valkey"
  size            = "kv-1-1"
  eviction_policy = "allkeys-lru"
}
