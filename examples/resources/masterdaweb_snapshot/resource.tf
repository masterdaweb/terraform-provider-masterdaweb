resource "masterdaweb_snapshot" "before_upgrade" {
  instance_id = masterdaweb_instance.web.id
  name        = "before-upgrade"
  description = "Before the PHP 8.4 upgrade"
}
