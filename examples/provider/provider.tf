terraform {
  required_providers {
    masterdaweb = {
      source = "masterdaweb/masterdaweb"
    }
  }
}

# The token comes from MW_TOKEN when it is left out.
provider "masterdaweb" {
  token = var.mw_token
}

variable "mw_token" {
  description = "An API token, created in the console under Settings → API tokens."
  type        = string
  sensitive   = true
}
