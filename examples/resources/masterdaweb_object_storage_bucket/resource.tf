resource "masterdaweb_object_storage_bucket" "files" {
  name = "shop-files-acme" # unique on the whole platform

  quota_bytes = 50000000000 # 50 GB
  quota_mode  = "alert"

  cors = [{
    allowed_origins = ["https://shop.example.com"]
    allowed_methods = ["GET", "PUT"]
    allowed_headers = ["content-type"]
    max_age_seconds = 3600
  }]

  lifecycle_rules = [{
    id          = "tmp"
    prefix      = "tmp/"
    expire_days = 7
  }]
}

resource "masterdaweb_object_storage_bucket" "images" {
  name   = "shop-images-acme"
  public = true
}
