# Terraform provider for Master da Web

This provider manages [Master da Web](https://www.masterdaweb.com) as code, with Terraform or OpenTofu: cloud
instances (VPS) with their SSH keys, snapshots, firewalls, VPCs and reverse DNS; DNS zones and records; projects;
the serverless platform (services, variables, domains, bindings, log drains); managed databases; Object Storage
buckets and access keys; and webhooks.

Everything goes through the public API (v1) with an API token, as the `mw` CLI does.

## Resources and data sources

| Resource | What it is |
| --- | --- |
| `masterdaweb_instance` | A cloud instance (VPS): ordered, built, renamed, started and stopped, its password reset, resized (plan, disks, add-ons, addresses) and reinstalled from another image — all in place. |
| `masterdaweb_ssh_key` | An SSH public key of the account, put on instances when they are built. |
| `masterdaweb_snapshot` | A snapshot of an instance (disk, optionally memory), kept 30 days. |
| `masterdaweb_backup_schedule` | When an instance is backed up (days, time, time zone), with the backup add-on. |
| `masterdaweb_firewall` | Rules in order and default policies, committed onto exactly the instances listed. |
| `masterdaweb_vpc` | A private network between instances of one region. |
| `masterdaweb_vpc_attachment` | An instance on a VPC. |
| `masterdaweb_reverse_dns` | The PTR of one of the account's addresses. |
| `masterdaweb_dns_zone` | A domain on the platform's nameservers, with the NS records to set at the registrar. |
| `masterdaweb_dns_record` | A whole record set (every value at one name and type). |
| `masterdaweb_project` | What groups instances, databases, buckets and serverless services. Protected from deletion by default. |
| `masterdaweb_project_resource` | An instance, database or bucket in a project. |
| `masterdaweb_serverless_service` | A web app, private service, worker or cron job, from a repository or an image. Changes start a deployment unless `deploy = false`. |
| `masterdaweb_serverless_variable` | One environment variable of a service, plain or secret (write-only). |
| `masterdaweb_serverless_domain` | A domain of yours on a web service, with the DNS records to create. |
| `masterdaweb_serverless_binding` | A bucket or database given to a service as variables, with a credential of its own. |
| `masterdaweb_serverless_log_drain` | A project's logs sent to an HTTPS endpoint of yours, Datadog or Better Stack. |
| `masterdaweb_database` | A managed PostgreSQL, MySQL, MariaDB or Redis database. Protected from deletion by default. |
| `masterdaweb_object_storage_bucket` | An Object Storage bucket, with its quota, CORS and lifecycle rules. Protected from deletion by default. |
| `masterdaweb_object_storage_key` | An S3 access key to all buckets or some, read-write or read-only. |
| `masterdaweb_webhook` | The account's events sent to an address of yours (signed JSON), or to a Slack or Discord channel. |
| `masterdaweb_vpn_user` | A login on a VPN gateway (the VPN app), applied to the gateway in place; a generated password is kept in the state. |
| `masterdaweb_vpn_gateway_settings` | A VPN gateway's settings (transport, address pool, routing, MTU), applied to it, with an opt-in rebuild. |
| `masterdaweb_security_exception` | A risk the Security Center accepts — a finding, a check or a port — with a justification and an end date. |
| `masterdaweb_security_settings` | Whether and how often the account is scanned, and who is told. |

| Data source | What it finds |
| --- | --- |
| `masterdaweb_regions`, `masterdaweb_plans`, `masterdaweb_images` | The catalog: where instances go, what they run on, what they are installed with (filtered). |
| `masterdaweb_instance`, `masterdaweb_instances` | An instance by id or name; the instances by name pattern, region or project. |
| `masterdaweb_ssh_key` | A key by id, name or fingerprint. |
| `masterdaweb_vpc` | A VPC by id or name, or the default one. |
| `masterdaweb_firewall`, `masterdaweb_firewall_macros` | A firewall with its rules; the named services rules can use. |
| `masterdaweb_dns_zone` | A zone by id or domain. |
| `masterdaweb_project` | A project by id or name, with how much it groups. |
| `masterdaweb_serverless_outbound_addresses` | The addresses services reach the internet from, for allowlists elsewhere. |
| `masterdaweb_vpn_gateway`, `masterdaweb_vpn_gateways` | A VPN gateway by id, name or instance; the gateways by name pattern. |
| `masterdaweb_security_overview`, `masterdaweb_security_findings` | The security score and what it is made of; the findings, riskiest first (filtered). |

The full reference is in [`docs/`](docs/index.md), in the Terraform Registry's layout.

## Install

### Terraform

```hcl
terraform {
  required_providers {
    masterdaweb = {
      source  = "masterdaweb/masterdaweb"
      version = "~> 0.1"
    }
  }
}
```

`terraform init` installs it from the [Terraform Registry](https://registry.terraform.io) once it is published.

### OpenTofu

The same block works with OpenTofu: `tofu init` installs `masterdaweb/masterdaweb` from the
[OpenTofu Registry](https://search.opentofu.org) once it is listed there. The provider speaks plugin protocol 6,
which both support (Terraform 1.0 and later, every OpenTofu).

### From this repository

Build it (Go 1.26 or newer) and point Terraform or OpenTofu at the binary with a development override:

```sh
go install .   # terraform-provider-masterdaweb lands in $(go env GOPATH)/bin
```

In `~/.terraformrc` (`%APPDATA%\terraform.rc` on Windows; OpenTofu also reads `~/.tofurc`):

```hcl
provider_installation {
  dev_overrides {
    "masterdaweb/masterdaweb" = "/Users/you/go/bin" # $(go env GOPATH)/bin
  }
  direct {}
}
```

Terraform then uses your build for `masterdaweb/masterdaweb` and says so on every run. Skip `init` for it (run
`plan` or `apply` directly); remove the override to go back to the Registry's.

## Sign in

Create a token in the console under **Settings → API tokens**, and give it to the provider as `token` or in
`MW_TOKEN` (the variable the `mw` CLI reads). `api_url` (or `MW_API_URL`) points at another API; it defaults to
`https://apiv2.masterdaweb.com`.

```hcl
provider "masterdaweb" {} # MW_TOKEN
```

Give the token the scopes of what Terraform manages:

| To manage | Scopes |
| --- | --- |
| Instances, their snapshots and SSH keys | `vm:read`, `vm:write`, `vm:power`, `order:write`, `billing:read` (`vm:delete` to destroy instances) |
| Firewalls, VPCs, reverse DNS, VPN gateways | `network:read`, `network:write` |
| The Security Center | `security:read`, `security:write` |
| DNS zones and records | `dns:read`, `dns:write` |
| Projects and the serverless platform | `serverless:read`, `serverless:write` (`serverless:delete` to destroy) |
| Databases | `databases:read`, `databases:write` (`databases:delete`) |
| Buckets and access keys | `storage:read`, `storage:write` (`storage:delete`) |
| Webhooks | `account:read`, `webhooks:write` (`webhooks:delete`) |

Log drains, secret variables, removing a binding, access keys, webhooks, and destroying a project, a database or
a bucket are an administrator's.

## An instance, its network and its DNS

```hcl
resource "masterdaweb_ssh_key" "laptop" {
  name       = "laptop"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "masterdaweb_instance" "web" {
  name            = "web-1"
  region_id       = "3"     # data.masterdaweb_regions
  plan            = "c2m4"  # data.masterdaweb_plans
  image           = "ubuntu-24-04"
  subscription_id = var.subscription_id # paid from its balance: unattended
  payment         = "balance"
  ssh_key_ids     = [masterdaweb_ssh_key.laptop.id]
}

resource "masterdaweb_firewall" "web" {
  name      = "web"
  policy_in = "DROP"
  rules = [
    { direction = "in", action = "ACCEPT", macro = "SSH", source = "203.0.113.0/24" },
    { direction = "in", action = "ACCEPT", macro = "HTTPS" },
  ]
  instance_ids = [masterdaweb_instance.web.id]
}

resource "masterdaweb_dns_zone" "example" {
  name = "example.com"
}

resource "masterdaweb_dns_record" "www" {
  zone_id = masterdaweb_dns_zone.example.id
  name    = "www"
  type    = "A"
  records = [masterdaweb_instance.web.ipv4_address]
}
```

## A whole app on the serverless platform

[`examples/complete`](examples/complete) publishes an ASP.NET Core app built from
[mw-template-aspnet](https://github.com/masterdaweb/mw-template-aspnet), with a managed PostgreSQL it reads as
`ConnectionStrings__DefaultConnection` (the `dotnet` layout), a private bucket it reads as `MW_STORAGE_*`, a domain
and its logs sent to Datadog:

```sh
cd examples/complete
export MW_TOKEN=mwk_…
cp terraform.tfvars.example terraform.tfvars   # a bucket name of your own, your domain, Datadog's key
terraform init && terraform apply
terraform output dns_records                   # create these at your DNS provider
```

## How it behaves

- **Waits and timeouts.** Terraform waits for what the API does in the background — an instance's order and
  build, a reinstall, a firewall's commit, an instance joining a VPC, a project's deletion — up to each resource's
  `timeouts`. While another operation holds an instance (a backup, a snapshot being taken) it asks again instead
  of failing.
- **Paying for instances.** With `subscription_id` and `payment = "balance"` an instance is paid from that
  subscription's balance and built at once. Without a subscription, its order raises an invoice: Terraform waits
  for it to be paid, logging where to pay it; past the `create` timeout, pay it and import the instance.
- **Resizes and reinstalls, not replacements.** Changing an instance's plan, main disk, data disks, add-ons or
  extra addresses resizes it where it is, quoted at plan time (what it costs, whether it restarts, what it removes;
  a disk that would shrink is refused). Changing its image, SSH keys, user data or app inputs reinstalls it where it
  is (its disk is replaced; its addresses, plan and billing stay), and the plan warns about it.
- **Deployments.** Creating a service starts its first deployment, and changes to a service, its variables or
  its bindings start a new one, unless `deploy = false`. Terraform does not wait for deployments to finish.
- **Deletion protection.** Projects, databases and buckets are protected by default. Destroying a protected one
  fails, saying what to do: set `deletion_protection = false`, apply, then destroy.
- **Write-only values.** Passwords, a secret variable, a log drain's token and an access key's secret never come
  back from the API: Terraform keeps the values it was given, in its state (which is sensitive).
- **No cosmetic diffs.** What the API trims or normalises (hostnames, DNS values as the nameservers keep them,
  addresses written as `203.0.113.7` and kept as `203.0.113.7/32`, port ranges, a macro's ports) keeps your
  spelling.
- **Retries.** A request refused for its rate (429), or by a briefly unavailable API (502, 503, 504), is sent
  again after the wait the API asks for, with the same `Idempotency-Key`: a retry never does the work twice.
  The API's refusals (RFC 9457 problems) are shown as it says them, with the field and the request id.

Every resource can be imported:

| Resource | `terraform import` id |
| --- | --- |
| `masterdaweb_instance` | its id, or a name only it has (its SSH keys, user data and password are taken from the configuration) |
| `masterdaweb_ssh_key` | its id |
| `masterdaweb_snapshot` | `<instance_id>/<snapshot_id>` |
| `masterdaweb_backup_schedule` | the instance's id |
| `masterdaweb_firewall` | its id, or a name only it has |
| `masterdaweb_vpc` | its id, or a name only it has |
| `masterdaweb_vpc_attachment` | `<vpc_id>/<instance_id>` |
| `masterdaweb_reverse_dns` | the address |
| `masterdaweb_dns_zone` | its id, or the domain |
| `masterdaweb_dns_record` | `<zone_id>/<name>/<type>` |
| `masterdaweb_project` | its id, or its name |
| `masterdaweb_project_resource` | `<project_id>/<kind>/<resource_id>` |
| `masterdaweb_serverless_service` | its id, or `<project_id>/<name>` |
| `masterdaweb_serverless_variable` | `<service_id>/<key>` |
| `masterdaweb_serverless_domain` | its id, or `<service_id>/<hostname>` |
| `masterdaweb_serverless_binding` | `<service_id>/<binding_id>`, or `<service_id>/<bucket or database name>` |
| `masterdaweb_serverless_log_drain` | its id, or `<project_id>/<id>` |
| `masterdaweb_database` | its name, or its id |
| `masterdaweb_object_storage_bucket` | its name, or its id |
| `masterdaweb_object_storage_key` | its id (its secret is not imported) |
| `masterdaweb_webhook` | its id (its address and secret are not imported) |
| `masterdaweb_vpn_user` | `<gateway_id>/<login_id>` (its password is not imported) |
| `masterdaweb_vpn_gateway_settings` | the gateway's id |
| `masterdaweb_security_exception` | its id, while it is in force |
| `masterdaweb_security_settings` | any id: an account has one set |

## Develop

```sh
make test       # go test ./...: no network, no Terraform
make vet
make e2e        # the examples, updates, imports and destroys, through Terraform itself
make testacc    # real resources on the real API (MW_TOKEN): billed as any others
make docs       # regenerate docs/ from the schema, templates/ and examples/
```

`go test ./...` checks every resource against a fake of the API (`internal/provider/fakeapi_test.go` and
`fakeapi_infra_test.go`) that answers as the real one does where it matters: what it trims and normalises, its
defaults, its strict validation of request bodies, secrets that never come back, orders waiting for their invoice,
instances busy while they build, two-phase deletes. A small stand-in for Terraform core
(`internal/provider/harness_test.go`) drives each resource through validate, plan, apply, refresh and a second
plan, which must be empty: the perpetual diffs a provider must not have.

`make e2e` (`MW_TF_E2E=1`) runs the same fake through Terraform itself — the complete example, an instance with its
SSH key, VPC, firewall, DNS, reverse DNS, snapshot, backup schedule, project, access key and webhook built,
changed in place, reinstalled, resized, imported and destroyed — with the Terraform on `PATH`, `TF_ACC_TERRAFORM_PATH`, or one it downloads.

`make testacc` (`TF_ACC=1`) makes real projects, services and buckets with `MW_TOKEN`; the database test also
needs `MW_ACC_DATABASES=1` (a database bills by the hour).

The API client is `internal/client`; the resources are in `internal/provider`.

## Release

Releases are built by [GoReleaser](https://goreleaser.com) (`.goreleaser.yml`) in GitHub Actions
(`.github/workflows/release.yml`) when a `v*` tag is pushed: one zip per system, a `SHA256SUMS` file signed with
the GPG key registered with the Terraform Registry (secrets `GPG_PRIVATE_KEY` and `PASSPHRASE`), and
`terraform-registry-manifest.json` (protocol 6) beside them. The Registry publishes public GitHub repositories
named `terraform-provider-<name>`: this one is meant to be `github.com/masterdaweb/terraform-provider-masterdaweb`.
The OpenTofu Registry lists the same releases once the provider is submitted to it (with the same GPG public
key). [`CHANGELOG.md`](CHANGELOG.md) says what each release changes.

## Not covered yet

- One-off actions — a backup now, restoring a backup or a snapshot, rescue mode: the console, the CLI or the API.
- Custom image uploads and VPN gateways: not in the API v1 yet.
- Variable groups (`env-groups`): the API sets and lists a group's variables but has no way to remove one of them.
- Services deployed from a folder with `mw deploy`: Terraform cannot send a folder.
- Registry credentials and deploy hooks: the CLI and the console manage them.
