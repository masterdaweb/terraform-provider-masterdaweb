data "masterdaweb_images" "apps" {
  type = "MARKETPLACE_APP"
}

# What each app asks before it can run (masterdaweb_instance.app_inputs).
output "app_inputs" {
  value = { for i in data.masterdaweb_images.apps.images : i.code => [for q in i.inputs : q.key] }
}
