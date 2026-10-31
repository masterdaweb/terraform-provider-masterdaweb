---
page_title: "masterdaweb_serverless_service Resource - masterdaweb"
subcategory: "Serverless"
description: |-
  A serverless service: a web app, a private service, a worker or a cron job, built from a Git repository or run from an image. Creating it starts its first deployment, and changes start a new one, unless deploy is false; Terraform does not wait for deployments to finish.
---

# masterdaweb_serverless_service (Resource)

A serverless service: a web app, a private service, a worker or a cron job, built from a Git repository or run from an image. Creating it starts its first deployment, and changes start a new one, unless deploy is false; Terraform does not wait for deployments to finish.

## Example Usage

```terraform
# A site built from a public repository: every apply that changes it starts
# a deployment, which Terraform does not wait for.
resource "masterdaweb_serverless_service" "web" {
  project_id = masterdaweb_project.shop.id
  name       = "web"
  kind       = "WEB"

  git = {
    repo_url = "https://github.com/masterdaweb/mw-template-aspnet"
    branch   = "main"
  }

  port              = 8080
  size              = "small"
  min_instances     = 1
  max_instances     = 5
  health_check_path = "/health"

  # A gradual rollout: each new version takes 10% of the requests for
  # 15 minutes first, then all of them if its error rate stays low.
  rollout_percent = 10
  rollout_minutes = 15
}

# A classic PHP site on PHP 7.4 and Apache, which honours its .htaccess.
# Without a Dockerfile the release comes from composer.json; these settings
# choose it and the extensions instead, and changing one builds the live
# code again.
resource "masterdaweb_serverless_service" "legacy" {
  project_id = masterdaweb_project.shop.id
  name       = "legacy-site"
  kind       = "WEB"

  git = {
    repo_url = "https://github.com/acme/legacy-site"
    branch   = "main"
  }

  php_version     = "7.4"
  php_server      = "apache"
  php_extensions  = ["gd", "soap"]
  system_packages = ["ghostscript"]
}

# A queue worker from a repository connected in the console (Settings ›
# Integrations): every push to main deploys.
resource "masterdaweb_serverless_service" "worker" {
  project_id = masterdaweb_project.shop.id
  name       = "worker"
  kind       = "WORKER"

  git = {
    repo   = "acme/shop"
    branch = "main"
  }

  start_command = "php artisan queue:work"
  instances     = 2
}

# A nightly job from an image.
resource "masterdaweb_serverless_service" "report" {
  project_id = masterdaweb_project.shop.id
  name       = "nightly-report"
  kind       = "CRON"

  image = {
    ref = "ghcr.io/acme/reports:1.4"
  }

  cron_schedule = "0 3 * * *"
  cron_timezone = "America/Sao_Paulo"
}
```

## Source

Give `git` or `image`:

- `git.repo_url` builds a public repository from its https address (GitHub, GitLab, Bitbucket or any Git
  host). It deploys when Terraform asks, not on pushes.
- `git.repo` builds a repository connected in the console (Settings › Integrations), as `owner/name`: every
  push to the branch deploys (`auto_deploy`), after its checks pass with `wait_for_ci`. When one account
  is connected it is found by itself; with several, set `git.provider` or `git.connection_id`.
- `image.ref` runs an image as it is. A private registry takes a credential (`mw registry`).

Moving a service between `git` and `image` replaces it. A service deployed from a folder with `mw deploy`
has neither, and is not managed here.

## Deployments

Creating the service starts its first deployment. Afterwards, with `deploy = true` (the default):

- a new repository, branch, tag pattern or image, or a setting the build reads (`build_command`,
  `root_dir`, `dockerfile_path`), deploys the latest commit (or the image's current digest), built afresh;
- any other setting is applied on the version running now, without a build.

With `deploy = false` changes are saved and go out with the next deployment. Terraform never waits for a
deployment to finish: `status` says where the service stood when it was last read.

### Gradual rollouts

Web and private services can roll a new version out gradually: with `rollout_percent = 10` and
`rollout_minutes = 15`, each new version takes 10% of the requests for 15 minutes first, then all of them —
or none, if its error rate climbs. Left out (or `0`), a new version takes every request at once;
`rollout_minutes` is 10 when left out. Both shape the next deployment: changing only them never starts one.
When the repository's `mw.toml` defines them, they are the repository's (see Settings).

## Settings

Settings left out keep what the service has: the platform's defaults, or what the repository's `mw.toml`
defines. Settings the `mw.toml` defines (listed in `repository_settings`) are read-only here: change them in
the file and push. To clear a command or a path, set it to `""`.

Destroying the service waits until it is gone, so its name is free again. A service with
`deletion_protection = true` is not destroyed: set it to false and apply first.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `kind` (String) WEB (a public address), PRIVATE (reached only by the project's other services), WORKER (runs all the time, no address) or CRON (runs on a schedule). Changing it replaces the service.
- `name` (String) The service's name: up to 40 lowercase letters, digits and hyphens, starting with a letter. Other services of the project reach a private service at `http://<name>`. Changing it replaces the service.
- `project_id` (String) The project the service belongs to. Changing it replaces the service.

### Optional

- `auto_deploy` (Boolean) Whether every push to the branch (or a new image digest) deploys by itself.
- `build_command` (String) The command that builds the app, when the platform's detection is not enough. "" for none.
- `concurrency` (Number) Web and private services: the requests one instance takes at once.
- `cron_schedule` (String) Cron jobs: when it runs, in cron syntax, like `*/5 * * * *`.
- `cron_timeout_seconds` (Number) Cron jobs: the longest a run may take, in seconds.
- `cron_timezone` (String) Cron jobs: the time zone of the schedule, like America/Sao_Paulo or UTC.
- `deletion_protection` (Boolean) Whether the service is protected from deletion. To destroy a protected service, set it to false and apply first.
- `deploy` (Boolean) Whether creating the service, and the changes Terraform makes to it, start a deployment. False saves them for the next deployment. Terraform never waits for a deployment to finish.
- `dockerfile_path` (String) The Dockerfile to build with, when it is not ./Dockerfile. "" for the default.
- `document_root` (String) PHP: the folder served, relative to root_dir ("." for the root). "" to detect the folder with index.php.
- `git` (Attributes) Build the service from a Git repository: a public one by its https address, or one connected in the console (Settings › Integrations), which deploys on every push. Give git or image. (see [below for nested schema](#nestedatt--git))
- `graceful_shutdown_seconds` (Number) How long an instance has to finish its work when it stops, in seconds.
- `health_check_path` (String) Web and private services: a path that answers 200 when the app is ready, like /health. "" for none.
- `idle_timeout_seconds` (Number) Web and private services: how long an instance stays without requests before it stops.
- `image` (Attributes) Run an image as it is, like nginx:1.27 or ghcr.io/acme/api:1.4. Give git or image. (see [below for nested schema](#nestedatt--image))
- `instances` (Number) Workers: how many instances run.
- `max_instances` (Number) Web and private services: the most instances it scales to.
- `min_instances` (Number) Web and private services: the fewest instances. 0 sleeps when idle.
- `node_version` (String) Builds without a Dockerfile: the Node.js release, like 22, 20.11 or lts. "" for package.json's engines or .nvmrc.
- `php_extensions` (List of String) PHP extensions beyond the database drivers every PHP build has and composer.json's ext-*, like gd or redis-6.1.0.
- `php_server` (String) Builds without a Dockerfile: apache (honours .htaccess, every release) or frankenphp (8.2 on). "" to detect.
- `php_version` (String) Builds without a Dockerfile: the PHP release (7.4 to 8.5). "" for what composer.json allows, preferring 8.4.
- `port` (Number) Web and private services: the port the app listens on.
- `release_command` (String) A command run once per deployment, before the new version takes traffic (migrations). "" for none.
- `request_timeout_seconds` (Number) Web and private services: the longest a request may take, in seconds (up to 600). On the public address the app must also send something at least every 100 s.
- `rollout_minutes` (Number) Web and private services: how long a gradual rollout lasts, in minutes (1 to 60, 10 by default). It shapes the next deployment and never starts one by itself.
- `rollout_percent` (Number) Web and private services, a gradual rollout: the share of the requests (1 to 50%) a new version takes first, for rollout_minutes, before it takes them all — or none, if its error rate climbs. Left out, or 0: all at once. It shapes the next deployment and never starts one by itself.
- `root_dir` (String) The folder of the repository the app is in, like apps/api. "" for the root.
- `size` (String) The instance size: x-small, small, medium or large (x-large on request).
- `start_command` (String) The command that starts the app, when the build's own is not the one. "" for none.
- `startup_timeout_seconds` (Number) How long a new version has to answer its health check, in seconds.
- `system_packages` (List of String) Builds without a Dockerfile: Debian packages in the image and at build, like ffmpeg.
- `wait_for_ci` (Boolean) Whether a push waits for the repository's checks to pass before it deploys.
- `watch_paths` (List of String) Only pushes that change these paths (patterns) deploy. Empty: every push.

### Read-Only

- `created_at` (String) When the service was created (RFC 3339).
- `id` (String) The service's id.
- `internal_url` (String) The address inside the project (private services).
- `memory_mib` (Number) The memory of one instance, in MiB.
- `repository_settings` (List of String) Settings the repository's mw.toml defines. They are read-only here: change them in the file and push, and leave them out of this resource.
- `status` (String) NEW, DEPLOYING, LIVE, SLEEPING, FAILED, SUSPENDED or PAUSED, when last read.
- `url` (String) The public address (web services).
- `vcpu_millis` (Number) The vCPU of one instance, in thousandths.

<a id="nestedatt--git"></a>
### Nested Schema for `git`

Optional:

- `branch` (String) The branch to deploy. Defaults to the repository's default branch (main for a public URL).
- `connection_id` (String) The connected account's id, when one provider has several. Found by itself when only one fits.
- `provider` (String) GITHUB, GITLAB or BITBUCKET: the connected account repo is on, when more than one is connected. PUBLIC_URL for a repository given by repo_url.
- `repo` (String) A repository connected in the console, as owner/name (acme/shop). Give repo_url or repo.
- `repo_url` (String) A public repository's https address, like https://github.com/acme/shop. Give repo_url or repo.
- `tag_pattern` (String) Deploy only tags matching this pattern, like v*.


<a id="nestedatt--image"></a>
### Nested Schema for `image`

Required:

- `ref` (String) The image, like ghcr.io/acme/api:1.4.

Optional:

- `registry_credential_id` (String) The credential a private image is pulled with (created with `mw registry` or in the console).

## Import

A service is imported by its id, or as `<project_id>/<service name>`:

```shell
# By id, or as <project_id>/<service name>.
terraform import masterdaweb_serverless_service.web 0193f0e2-9a4b-7c2d-8e1f-3a5b6c7d8e9f
terraform import masterdaweb_serverless_service.web 0193f0e2-8c1d-7a7e-9b1e-2f6a4c0d9e11/web
```
