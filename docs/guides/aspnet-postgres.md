---
page_title: "ASP.NET Core and PostgreSQL"
subcategory: ""
description: |-
  A whole app as code: an ASP.NET Core service, a managed PostgreSQL bound as its connection string, a bucket, a domain and its logs sent to Datadog.
---

# ASP.NET Core and PostgreSQL

This configuration publishes the [ASP.NET Core template](https://github.com/masterdaweb/mw-template-aspnet)
(.NET 10 with Entity Framework Core) as a project of its own:

- a **web service** built from the repository: the platform finds the solution, publishes it and runs the
  migrations bundle (`./efbundle`) once per deployment, before the new version takes traffic — the
  repository's `mw.toml` says so, which is why those settings are not in the configuration;
- a managed **PostgreSQL** database, bound with the `dotnet` layout: the app reads
  `ConnectionStrings__DefaultConnection` (`GetConnectionString("DefaultConnection")`), in Npgsql's format,
  with a login of its own;
- a private **bucket**, bound with the `MW_STORAGE_` prefix (`MW_STORAGE_ENDPOINT`, `MW_STORAGE_BUCKET`,
  `MW_STORAGE_ACCESS_KEY_ID`, `MW_STORAGE_SECRET_ACCESS_KEY`…);
- a **domain** of yours, and the project's **logs** sent to Datadog.

## Run it

```shell
export MW_TOKEN=mwk_…            # Settings → API tokens in the console
cp terraform.tfvars.example terraform.tfvars   # and fill it in
terraform init
terraform apply
terraform output dns_records     # create these at your DNS provider
```

The service's first deployment starts with the apply, and the bindings and the variable made right after are
usually folded into it. Terraform does not wait for it to finish: follow it in the console, or with
`mw logs --service web`. The site answers at the `url` output once it is live, and at your domain once its
records are found and its certificate issued.

The project, the database and the bucket are protected from deletion (`protect`). To destroy everything, set
`protect = false`, apply, then `terraform destroy`.

## The configuration

```terraform
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
```

```terraform
variable "bucket_name" {
  description = "The bucket's name. Bucket names are shared by every account: pick one of your own, like shop-files-acme."
  type        = string
}

variable "domain" {
  description = "A domain of yours for the site, like shop.example.com. Create the DNS records in the dns_records output."
  type        = string
}

variable "datadog_api_key" {
  description = "Datadog's API key, where the project's logs go."
  type        = string
  sensitive   = true
}

variable "protect" {
  description = "Protect the project, the database and the bucket from deletion. Keep it on for anything that matters: to destroy them, set it to false and apply first."
  type        = bool
  default     = true
}
```

```terraform
# An ASP.NET Core app (.NET 10, Entity Framework Core) on Master da Web:
# built from the public template repository, with a managed PostgreSQL it
# reads as ConnectionStrings__DefaultConnection, a private bucket it reads
# as MW_STORAGE_*, a domain of yours and its logs sent to Datadog.

resource "masterdaweb_project" "shop" {
  name                = "shop"
  description         = "The shop, in ASP.NET Core"
  deletion_protection = var.protect
}

resource "masterdaweb_database" "db" {
  name                = "shop-db"
  engine              = "postgres"
  size                = "pg-1-1"
  project_id          = masterdaweb_project.shop.id
  deletion_protection = var.protect
}

resource "masterdaweb_object_storage_bucket" "files" {
  name                = var.bucket_name
  deletion_protection = var.protect
}

resource "masterdaweb_serverless_service" "web" {
  project_id = masterdaweb_project.shop.id
  name       = "web"
  kind       = "WEB"

  git = {
    repo_url = "https://github.com/masterdaweb/mw-template-aspnet"
    branch   = "main"
  }

  port          = 8080
  size          = "small"
  min_instances = 1
  max_instances = 3

  # The repository's mw.toml sets the release command (./efbundle, the
  # migrations) and the health check (/health): settings it defines are
  # left out here.
}

# The database as ConnectionStrings__DefaultConnection, in Npgsql's format,
# with a login of the service's own.
resource "masterdaweb_serverless_binding" "db" {
  service_id    = masterdaweb_serverless_service.web.id
  resource_kind = "POSTGRES"
  resource_name = masterdaweb_database.db.name
  env_profile   = "dotnet"
}

# The bucket as MW_STORAGE_ENDPOINT, MW_STORAGE_BUCKET, MW_STORAGE_ACCESS_KEY_ID…
resource "masterdaweb_serverless_binding" "files" {
  service_id    = masterdaweb_serverless_service.web.id
  resource_kind = "BUCKET"
  resource_name = masterdaweb_object_storage_bucket.files.name
  env_profile   = "prefixed"
  env_prefix    = "MW_STORAGE_"
}

resource "masterdaweb_serverless_variable" "environment" {
  service_id = masterdaweb_serverless_service.web.id
  key        = "ASPNETCORE_ENVIRONMENT"
  value      = "Production"
}

resource "masterdaweb_serverless_domain" "site" {
  service_id = masterdaweb_serverless_service.web.id
  hostname   = var.domain
}

resource "masterdaweb_serverless_log_drain" "datadog" {
  project_id = masterdaweb_project.shop.id
  kind       = "DATADOG"
  token      = var.datadog_api_key
  site       = "us1"
}
```

```terraform
output "url" {
  description = "The site's own address, live once its first deployment is."
  value       = masterdaweb_serverless_service.web.url
}

output "dns_records" {
  description = "The records to create at your DNS provider for the domain."
  value       = masterdaweb_serverless_domain.site.dns_records
}

output "database_host" {
  description = "Where the database answers (the service reaches it through its binding)."
  value       = masterdaweb_database.db.host
}

output "database_password" {
  description = "The database owner's password, for your own tools (the service has a login of its own)."
  value       = masterdaweb_database.db.password
  sensitive   = true
}
```
