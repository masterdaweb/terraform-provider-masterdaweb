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
