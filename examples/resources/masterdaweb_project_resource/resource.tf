resource "masterdaweb_project_resource" "web" {
  project_id  = masterdaweb_project.shop.id
  kind        = "INSTANCE"
  resource_id = masterdaweb_instance.web.id
}

resource "masterdaweb_project_resource" "files" {
  project_id  = masterdaweb_project.shop.id
  kind        = "BUCKET"
  resource_id = masterdaweb_object_storage_bucket.files.name
}
