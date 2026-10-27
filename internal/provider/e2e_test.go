package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// The tests here run configurations through Terraform itself — plan,
// apply, the empty plan after, imports, updates, destroy — against the fake
// API. Opt in with MW_TF_E2E=1: they need a Terraform binary
// (TF_ACC_TERRAFORM_PATH, one on PATH, or one downloaded into a temporary
// folder).

func e2e(t *testing.T) *fakeAPI {
	t.Helper()
	if os.Getenv("MW_TF_E2E") == "" {
		t.Skip("set MW_TF_E2E=1 to run configurations through Terraform against the fake API")
	}
	api := newFakeAPI(t)
	t.Setenv("MW_TOKEN", "mwk_test")
	t.Setenv("MW_API_URL", api.URL())
	// The provider is masterdaweb/masterdaweb, not hashicorp/masterdaweb.
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "masterdaweb")
	return api
}

var e2eProviders = map[string]func() (tfprotov6.ProviderServer, error){
	"masterdaweb": providerserver.NewProtocol6WithError(New("test")()),
}

func nothingLeft(api *fakeAPI) func(*terraform.State) error {
	return func(*terraform.State) error {
		if n := len(api.projects) + len(api.services) + len(api.databases) + len(api.drains); n > 0 {
			return fmt.Errorf("left behind: %d projects, %d services, %d databases, %d log drains",
				len(api.projects), len(api.services), len(api.databases), len(api.drains))
		}
		for _, b := range api.buckets {
			if b.Status != "deleting" {
				return fmt.Errorf("bucket %s left behind", b.Name)
			}
		}
		return nil
	}
}

// TestCompleteExampleThroughTerraform applies examples/complete as it ships.
func TestCompleteExampleThroughTerraform(t *testing.T) {
	api := e2e(t)
	dir := config.StaticDirectory("../../examples/complete")
	vars := config.Variables{
		"bucket_name":     config.StringVariable("shop-files-e2e"),
		"domain":          config.StringVariable("shop.example.com"),
		"datadog_api_key": config.StringVariable("dd-test-key"),
		"protect":         config.BoolVariable(false),
	}
	attr := func(s *terraform.State, address, name string) string {
		return s.RootModule().Resources[address].Primary.Attributes[name]
	}
	// Imports run in a fresh state, where the example's outputs are null:
	// the testing library cannot read null outputs, so imports use the
	// example without them.
	noOutputs := t.TempDir()
	for _, f := range []string{"main.tf", "variables.tf", "versions.tf"} {
		raw, err := os.ReadFile(filepath.Join("../../examples/complete", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(noOutputs, f), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The example has a provider block: providers are given per step.
	imported := func(address string, ignore ...string) resource.TestStep {
		return resource.TestStep{ConfigDirectory: config.StaticDirectory(noOutputs), ConfigVariables: vars, ResourceName: address,
			ProtoV6ProviderFactories: e2eProviders,
			ImportState:              true, ImportStateVerify: true, ImportStateVerifyIgnore: ignore}
	}
	byName := func(address, name string, ignore ...string) resource.TestStep {
		s := imported(address, ignore...)
		s.ImportStateIdFunc = func(*terraform.State) (string, error) { return name, nil }
		s.ImportStateVerifyIdentifierAttribute = "name"
		return s
	}
	binding := imported("masterdaweb_serverless_binding.db")
	binding.ImportStateIdFunc = func(s *terraform.State) (string, error) {
		return attr(s, "masterdaweb_serverless_binding.db", "service_id") + "/" + attr(s, "masterdaweb_serverless_binding.db", "id"), nil
	}
	variable := imported("masterdaweb_serverless_variable.environment")

	resource.UnitTest(t, resource.TestCase{
		CheckDestroy: nothingLeft(api),
		Steps: []resource.TestStep{
			{
				ConfigDirectory:          dir,
				ConfigVariables:          vars,
				ProtoV6ProviderFactories: e2eProviders,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("masterdaweb_serverless_service.web", "url", "https://web-x1y2z3.mwapp.com.br"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_service.web", "git.branch", "main"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_binding.db", "variables.0", "ConnectionStrings__DefaultConnection"),
					resource.TestCheckTypeSetElemAttr("masterdaweb_serverless_binding.files", "variables.*", "MW_STORAGE_ENDPOINT"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_domain.site", "dns_records.#", "2"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_log_drain.datadog", "destination", "https://http-intake.logs.datadoghq.com/…logs"),
					resource.TestCheckResourceAttr("masterdaweb_database.db", "password", "s3cret-owner-password"),
				),
			},
			imported("masterdaweb_project.shop"),
			imported("masterdaweb_serverless_service.web"),
			variable,
			binding,
			imported("masterdaweb_serverless_domain.site"),
			imported("masterdaweb_serverless_log_drain.datadog", "token"),
			byName("masterdaweb_database.db", "shop-db", "password"),
			byName("masterdaweb_object_storage_bucket.files", "shop-files-e2e"),
		},
	})
}

// TestUpdatesThroughTerraform changes settings, sources and variables in
// place, and replaces what cannot change in place.
func TestUpdatesThroughTerraform(t *testing.T) {
	api := e2e(t)
	cfg := func(size, ref, value string, secret bool, prefix, rollout string) string {
		return fmt.Sprintf(`
resource "masterdaweb_project" "p" {
  name                = "  e2e   project "
  deletion_protection = false
}
resource "masterdaweb_object_storage_bucket" "b" {
  name                = "e2e-files"
  deletion_protection = false
  cors = [{
    allowed_origins = [" https://e2e.example.com "]
    allowed_methods = ["GET", "PUT"]
  }]
  lifecycle_rules = [{ id = "tmp", prefix = "tmp/", expire_days = 3 }]
}
resource "masterdaweb_database" "d" {
  name                = "e2e-db"
  engine              = "postgres"
  size                = "pg-1-1"
  allowed_cidrs       = ["203.0.113.7"]
  deletion_protection = false
}
resource "masterdaweb_serverless_service" "s" {
  project_id = masterdaweb_project.p.id
  name       = "proxy"
  kind       = "WEB"
  image      = { ref = %q }
  size       = %q
  port       = 80
  %s
}
resource "masterdaweb_serverless_variable" "v" {
  service_id = masterdaweb_serverless_service.s.id
  key        = "GREETING"
  value      = %q
  secret     = %t
}
resource "masterdaweb_serverless_binding" "b" {
  service_id    = masterdaweb_serverless_service.s.id
  resource_kind = "BUCKET"
  resource_name = masterdaweb_object_storage_bucket.b.name
  env_prefix    = %q
}
resource "masterdaweb_serverless_binding" "d" {
  service_id    = masterdaweb_serverless_service.s.id
  resource_kind = "POSTGRES"
  resource_name = masterdaweb_database.d.name
}
`, ref, size, rollout, value, secret, prefix)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: e2eProviders,
		CheckDestroy:             nothingLeft(api),
		Steps: []resource.TestStep{
			{Config: cfg("small", "nginx:1.27", "hello", false, "FILES_", "")},
			// A gradual rollout alone: saved for the next deployment.
			{
				Config: cfg("small", "nginx:1.27", "hello", false, "FILES_", "rollout_percent = 10\n  rollout_minutes = 20"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("masterdaweb_serverless_service.s", "rollout_percent", "10"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_service.s", "rollout_minutes", "20"),
				),
			},
			{
				Config: cfg("medium", "nginx:1.28", "bonjour", true, "FILES_", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("masterdaweb_serverless_service.s", "rollout_percent"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_service.s", "rollout_minutes", "10"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_service.s", "memory_mib", "2048"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_service.s", "image.ref", "nginx:1.28"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_variable.v", "secret", "true"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_variable.v", "version", "2"),
				),
			},
			// A secret back to plain, and a new prefix: both replace.
			{
				Config: cfg("medium", "nginx:1.28", "bonjour", false, "UPLOADS_", "rollout_percent = 0"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("masterdaweb_serverless_variable.v", "secret", "false"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_variable.v", "version", "1"),
					resource.TestCheckTypeSetElemAttr("masterdaweb_serverless_binding.b", "variables.*", "UPLOADS_ENDPOINT"),
				),
			},
		},
	})
}

// infrastructureLeft says what the infrastructure test left behind.
func infrastructureLeft(api *fakeAPI) func(*terraform.State) error {
	return func(*terraform.State) error {
		in := api.infra
		snapshots, members := 0, 0
		for _, s := range in.snapshots {
			snapshots += len(s)
		}
		for _, m := range in.members {
			members += len(m)
		}
		left := map[string]int{
			"instances": len(in.instances), "firewalls": len(in.firewalls), "VPCs": len(in.vpcs), "DNS zones": len(in.zones),
			"SSH keys": len(in.sshKeys), "snapshots": snapshots, "access keys": len(in.accessKeys), "PTR records": len(in.ptr),
			"project members": members, "projects": len(api.projects), "webhooks": len(in.webhooks),
			"backup schedules": len(in.schedules),
		}
		for what, n := range left {
			if n > 0 {
				return fmt.Errorf("left behind: %d %s", n, what)
			}
		}
		return nil
	}
}

// TestInfrastructureThroughTerraform builds an instance with everything
// around it — an SSH key, a VPC, a firewall, DNS, reverse DNS, a snapshot, a
// backup schedule, a project, an Object Storage key and a webhook — changes
// it in place (renamed, stopped, reinstalled from another image, resized),
// imports every resource, and destroys it all.
func TestInfrastructureThroughTerraform(t *testing.T) {
	api := e2e(t)
	type size struct {
		plan       string
		mainDisk   int
		extraIPv4  int
		backupDays string
	}
	small := size{"c1m1", 0, 0, `["mon", "thu"]`}
	cfg := func(name, power, image, port, vpcName string, www []string, sz size) string {
		quoted := ""
		for i, ip := range www {
			if i > 0 {
				quoted += ", "
			}
			quoted += fmt.Sprintf("%q", ip)
		}
		return fmt.Sprintf(`
terraform {
  required_providers {
    masterdaweb = { source = "masterdaweb/masterdaweb" }
  }
}

data "masterdaweb_regions" "open" {
  available = true
}
data "masterdaweb_plans" "small" {
  term      = "MONTHLY"
  min_vcpus = 1
}
data "masterdaweb_images" "linux" {
  os_family = "linux"
  type      = "OPERATING_SYSTEM"
}

resource "masterdaweb_project" "shop" {
  name                = "shop"
  deletion_protection = false
}

resource "masterdaweb_ssh_key" "laptop" {
  name       = "laptop"
  public_key = %[7]q
}

resource "masterdaweb_vpc" "backend" {
  name      = %[5]q
  region_id = data.masterdaweb_regions.open.regions[0].id
  subnet    = "10.20.0.0/24"
}

resource "masterdaweb_instance" "web" {
  name               = %[1]q
  region_id          = data.masterdaweb_regions.open.regions[0].id
  plan               = %[8]q
  main_disk_extra_gb = %[9]d
  extra_ipv4         = %[10]d
  image              = %[3]q
  subscription_id    = "sub-9"
  payment            = "balance"
  ssh_key_ids        = [masterdaweb_ssh_key.laptop.id]
  user_data          = "#cloud-config\npackages: [nginx]\n"
  power_state        = %[2]q
  addons             = [{ code = "backup-slot", quantity = 1 }]
}

resource "masterdaweb_backup_schedule" "web" {
  instance_id = masterdaweb_instance.web.id
  days        = %[11]s
  time        = "03:30"
  timezone    = "America/Sao_Paulo"
}

resource "masterdaweb_vpc_attachment" "web" {
  vpc_id      = masterdaweb_vpc.backend.id
  instance_id = masterdaweb_instance.web.id
  private_ip  = "10.20.0.50"
}

resource "masterdaweb_project_resource" "web" {
  project_id  = masterdaweb_project.shop.id
  kind        = "INSTANCE"
  resource_id = masterdaweb_instance.web.id
}

resource "masterdaweb_firewall" "web" {
  name      = "web"
  policy_in = "DROP"
  rules = [
    { direction = "in", action = "ACCEPT", macro = "SSH", source = "203.0.113.0/24" },
    { direction = "in", action = "ACCEPT", protocol = "tcp", dest_port = %[4]q, comment = "app" },
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
  records = [%[6]s]
}

resource "masterdaweb_dns_record" "mail" {
  zone_id = masterdaweb_dns_zone.example.id
  name    = "mail"
  type    = "A"
  records = [masterdaweb_instance.web.ipv4_address]
}

resource "masterdaweb_reverse_dns" "mail" {
  ip         = masterdaweb_instance.web.ipv4_address
  hostname   = "mail.example.com"
  depends_on = [masterdaweb_dns_record.mail]
}

resource "masterdaweb_snapshot" "before" {
  instance_id = masterdaweb_instance.web.id
  name        = "before-changes"
}

resource "masterdaweb_object_storage_bucket" "files" {
  name                = "shop-files-e2e"
  deletion_protection = false
}

resource "masterdaweb_object_storage_key" "ci" {
  name    = "ci"
  access  = "read_only"
  buckets = [masterdaweb_object_storage_bucket.files.name]
}

resource "masterdaweb_webhook" "alerts" {
  url    = "https://hooks.example.com/mw/abcd1234"
  events = ["deployment.failed", "alert.*"]
}
`, name, power, image, port, vpcName, quoted, testPublicKey(9, "ana@laptop"), sz.plan, sz.mainDisk, sz.extraIPv4, sz.backupDays)
	}
	big := size{"c2m4", 20, 1, `["sun", "mon", "tue", "wed", "thu", "fri", "sat"]`}
	var instanceID string
	sameInstance := func(s *terraform.State) error {
		id := s.RootModule().Resources["masterdaweb_instance.web"].Primary.ID
		if instanceID == "" {
			instanceID = id
		} else if id != instanceID {
			return fmt.Errorf("the instance was replaced: %s became %s", instanceID, id)
		}
		return nil
	}
	imported := func(address string, ignore ...string) resource.TestStep {
		return resource.TestStep{
			Config:            cfg("web-01", "stopped", "debian-12", "8443", "backend-2", []string{"203.0.113.10", "203.0.113.11"}, big),
			ResourceName:      address,
			ImportState:       true,
			ImportStateVerify: true, ImportStateVerifyIgnore: ignore,
		}
	}
	attr := func(s *terraform.State, address, name string) string {
		return s.RootModule().Resources[address].Primary.Attributes[name]
	}
	byID := func(step resource.TestStep, id func(*terraform.State) string) resource.TestStep {
		step.ImportStateIdFunc = func(s *terraform.State) (string, error) { return id(s), nil }
		return step
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: e2eProviders,
		CheckDestroy:             infrastructureLeft(api),
		Steps: []resource.TestStep{
			{
				Config: cfg("web-1", "running", "ubuntu-24-04", "443", "backend", []string{"203.0.113.10"}, small),
				Check: resource.ComposeAggregateTestCheckFunc(
					sameInstance,
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "status", "running"),
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "plan", "c1m1"),
					resource.TestCheckResourceAttrSet("masterdaweb_instance.web", "ipv4_address"),
					resource.TestCheckResourceAttr("masterdaweb_firewall.web", "has_pending_changes", "false"),
					resource.TestCheckResourceAttr("masterdaweb_dns_zone.example", "name_servers.#", "2"),
					resource.TestCheckResourceAttr("masterdaweb_dns_record.www", "fqdn", "www.example.com."),
					resource.TestCheckResourceAttr("masterdaweb_reverse_dns.mail", "version", "4"),
					resource.TestCheckResourceAttr("masterdaweb_vpc_attachment.web", "private_ip", "10.20.0.50"),
					resource.TestCheckResourceAttr("masterdaweb_backup_schedule.web", "days.#", "2"),
					resource.TestCheckResourceAttrSet("masterdaweb_snapshot.before", "expires_at"),
					resource.TestCheckResourceAttr("masterdaweb_project_resource.web", "status", "RUNNING"),
					resource.TestCheckResourceAttrSet("masterdaweb_object_storage_key.ci", "secret_access_key"),
				),
			},
			// Renamed and stopped, a rule and a record changed, the VPC
			// renamed: all in place.
			{
				Config: cfg("web-01", "stopped", "ubuntu-24-04", "8443", "backend-2", []string{"203.0.113.10", "203.0.113.11"}, small),
				Check: resource.ComposeAggregateTestCheckFunc(
					sameInstance,
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "status", "stopped"),
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "name", "web-01"),
					resource.TestCheckResourceAttr("masterdaweb_firewall.web", "rules.1.dest_port", "8443"),
					resource.TestCheckResourceAttr("masterdaweb_dns_record.www", "records.#", "2"),
					resource.TestCheckResourceAttr("masterdaweb_vpc.backend", "name", "backend-2"),
					// Refreshed: the instance shows the VPC it was attached to.
					resource.TestCheckResourceAttrPair("masterdaweb_instance.web", "vpc_id", "masterdaweb_vpc.backend", "id"),
				),
			},
			// Another image: reinstalled where it is, and stopped again.
			{
				Config: cfg("web-01", "stopped", "debian-12", "8443", "backend-2", []string{"203.0.113.10", "203.0.113.11"}, small),
				Check: resource.ComposeAggregateTestCheckFunc(
					sameInstance,
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "image", "debian-12"),
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "status", "stopped"),
					resource.TestCheckResourceAttr("masterdaweb_project_resource.web", "status", "STOPPED"),
					func(*terraform.State) error {
						if fi := api.infra.instances[instanceID]; fi == nil || fi.reinstalls != 1 || fi.image != "debian-12" {
							return fmt.Errorf("not reinstalled: %+v", fi)
						}
						return nil
					},
				),
			},
			// A bigger plan, a bigger main disk and an extra address: resized
			// where it is, and stopped again; backups every day.
			{
				Config: cfg("web-01", "stopped", "debian-12", "8443", "backend-2", []string{"203.0.113.10", "203.0.113.11"}, big),
				Check: resource.ComposeAggregateTestCheckFunc(
					sameInstance,
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "plan", "c2m4"),
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "vcpus", "2"),
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "disk_gb", "100"),
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "extra_ipv4", "1"),
					resource.TestCheckResourceAttr("masterdaweb_instance.web", "status", "stopped"),
					resource.TestCheckResourceAttr("masterdaweb_backup_schedule.web", "days.#", "7"),
					func(*terraform.State) error {
						if fi := api.infra.instances[instanceID]; fi == nil || fi.resizes != 1 || fi.mainExtra != 20 {
							return fmt.Errorf("not resized: %+v", fi)
						}
						return nil
					},
				),
			},
			// What the API cannot show of an instance, and the add-ons an
			// import does not know Terraform manages.
			imported("masterdaweb_instance.web", "ssh_key_ids", "term", "payment", "order_id", "user_data", "addons"),
			imported("masterdaweb_backup_schedule.web"),
			imported("masterdaweb_ssh_key.laptop"),
			imported("masterdaweb_vpc.backend"),
			byID(imported("masterdaweb_vpc_attachment.web"), func(s *terraform.State) string {
				return attr(s, "masterdaweb_vpc_attachment.web", "id")
			}),
			byID(imported("masterdaweb_project_resource.web"), func(s *terraform.State) string {
				return attr(s, "masterdaweb_project_resource.web", "id")
			}),
			// The macro's protocol and port are the API's after an import.
			imported("masterdaweb_firewall.web", "rules.0.protocol", "rules.0.dest_port"),
			imported("masterdaweb_dns_zone.example"),
			byID(imported("masterdaweb_dns_record.www"), func(s *terraform.State) string {
				return attr(s, "masterdaweb_dns_record.www", "id")
			}),
			imported("masterdaweb_reverse_dns.mail"),
			byID(imported("masterdaweb_snapshot.before"), func(s *terraform.State) string {
				return attr(s, "masterdaweb_snapshot.before", "instance_id") + "/" + attr(s, "masterdaweb_snapshot.before", "id")
			}),
			imported("masterdaweb_object_storage_key.ci", "secret_access_key"),
			imported("masterdaweb_webhook.alerts", "url", "secret"),
		},
	})
}
