variable "subscription_id" {
  description = "A subscription of the account, whose balance pays for the instance."
  type        = string
}

data "masterdaweb_regions" "open" {
  available = true
}

data "masterdaweb_plans" "medium" {
  term       = "MONTHLY"
  min_vcpus  = 2
  min_ram_mb = 4096
}

resource "masterdaweb_ssh_key" "laptop" {
  name       = "laptop"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "masterdaweb_instance" "web" {
  name        = "web-1"
  description = "The shop's web server"
  region_id   = data.masterdaweb_regions.open.regions[0].id
  plan        = data.masterdaweb_plans.medium.plans[0].code
  image       = "ubuntu-24-04"

  # Unattended: paid from the subscription's balance, never by invoice.
  subscription_id = var.subscription_id
  payment         = "balance"

  ssh_key_ids = [masterdaweb_ssh_key.laptop.id]
  user_data   = <<-EOT
    #cloud-config
    packages: [nginx]
  EOT

  # Changed in place: a resize, quoted at plan time.
  main_disk_extra_gb = 40
  extra_disks_gb     = [200]
  extra_ipv4         = 1
  addons             = [{ code = "backup-slot", quantity = 2 }]

  timeouts = {
    create = "45m"
  }
}

output "web_ipv4" {
  value = masterdaweb_instance.web.ipv4_address
}
