# The smallest plan with 2 vCPUs and 4 GB, billed monthly.
data "masterdaweb_plans" "medium" {
  term       = "MONTHLY"
  min_vcpus  = 2
  min_ram_mb = 4096
}

output "plan" {
  value = data.masterdaweb_plans.medium.plans[0].code
}
