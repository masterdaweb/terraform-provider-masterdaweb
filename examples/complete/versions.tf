terraform {
  required_version = ">= 1.5"

  required_providers {
    masterdaweb = {
      source = "masterdaweb/masterdaweb"
    }
  }
}

# The token comes from the MW_TOKEN environment variable: create one in the
# console under Settings → API tokens, with serverless:write, databases:write
# and storage:write (and the :delete scopes for terraform destroy).
provider "masterdaweb" {}
