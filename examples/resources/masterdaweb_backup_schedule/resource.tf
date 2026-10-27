resource "masterdaweb_instance" "db" {
  name            = "db-1"
  region_id       = "3"
  plan            = "c4m8"
  image           = "debian-12"
  subscription_id = var.subscription_id
  payment         = "balance"

  # Backups need the backup add-on: its slots keep them.
  addons = [{ code = "backup-slot", quantity = 3 }]
}

resource "masterdaweb_backup_schedule" "db" {
  instance_id = masterdaweb_instance.db.id
  days        = ["mon", "wed", "fri"]
  time        = "03:30"
  timezone    = "America/Sao_Paulo"
}

variable "subscription_id" {
  type = string
}
