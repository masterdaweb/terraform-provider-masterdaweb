resource "masterdaweb_object_storage_key" "ci" {
  name    = "ci-uploads"
  access  = "read_write"
  buckets = [masterdaweb_object_storage_bucket.files.name]
}

output "ci_access_key_id" {
  value = masterdaweb_object_storage_key.ci.access_key_id
}

output "ci_secret_access_key" {
  value     = masterdaweb_object_storage_key.ci.secret_access_key
  sensitive = true
}
