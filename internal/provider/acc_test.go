package provider

// Acceptance tests: real resources on the real API, billed as any others.
// They run with TF_ACC=1 and MW_TOKEN (and MW_API_URL for another API), a
// token with serverless, storage and databases write and delete scopes.
// The database test also needs MW_ACC_DATABASES=1: a database bills by the
// hour from its first minute.

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func accPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv(EnvToken) == "" {
		t.Fatal("MW_TOKEN must be set for acceptance tests")
	}
}

func accName(prefix string) string {
	return prefix + "-" + acctest.RandStringFromCharSet(8, "abcdefghijklmnopqrstuvwxyz0123456789")
}

func TestAccServerless(t *testing.T) {
	project, bucket := accName("tf-acc"), accName("tf-acc-files")
	cfg := func(ref, greeting string) string {
		return fmt.Sprintf(`
resource "masterdaweb_project" "p" {
  name                = %[1]q
  deletion_protection = false
}

resource "masterdaweb_object_storage_bucket" "b" {
  name                = %[2]q
  deletion_protection = false
}

resource "masterdaweb_serverless_service" "web" {
  project_id    = masterdaweb_project.p.id
  name          = "web"
  kind          = "WEB"
  image         = { ref = %[3]q }
  port          = 80
  min_instances = 0
}

resource "masterdaweb_serverless_variable" "greeting" {
  service_id = masterdaweb_serverless_service.web.id
  key        = "GREETING"
  value      = %[4]q
}

resource "masterdaweb_serverless_variable" "secret" {
  service_id = masterdaweb_serverless_service.web.id
  key        = "API_KEY"
  value      = "acc-secret"
  secret     = true
}

resource "masterdaweb_serverless_binding" "files" {
  service_id    = masterdaweb_serverless_service.web.id
  resource_kind = "BUCKET"
  resource_name = masterdaweb_object_storage_bucket.b.name
  access        = "read_only"
}

data "masterdaweb_project" "byname" {
  name = masterdaweb_project.p.name
}

data "masterdaweb_serverless_outbound_addresses" "out" {}
`, project, bucket, ref, greeting)
	}
	attr := func(s *terraform.State, address, name string) string {
		return s.RootModule().Resources[address].Primary.Attributes[name]
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: e2eProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg("nginx:1.27", "hello"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("masterdaweb_serverless_service.web", "url"),
					resource.TestCheckResourceAttrPair("data.masterdaweb_project.byname", "id", "masterdaweb_project.p", "id"),
					resource.TestCheckResourceAttrSet("data.masterdaweb_serverless_outbound_addresses.out", "id"),
					resource.TestCheckResourceAttr("masterdaweb_serverless_binding.files", "env_prefix", "MW_STORAGE_"),
				),
			},
			{
				Config: cfg("nginx:1.28", "bonjour"),
				Check:  resource.TestCheckResourceAttr("masterdaweb_serverless_service.web", "image.ref", "nginx:1.28"),
			},
			{ResourceName: "masterdaweb_project.p", ImportState: true, ImportStateVerify: true},
			{ResourceName: "masterdaweb_serverless_service.web", ImportState: true, ImportStateVerify: true},
			{
				ResourceName: "masterdaweb_serverless_variable.greeting", ImportState: true, ImportStateVerify: true,
			},
			{
				ResourceName: "masterdaweb_serverless_binding.files", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return attr(s, "masterdaweb_serverless_binding.files", "service_id") + "/" + attr(s, "masterdaweb_serverless_binding.files", "id"), nil
				},
			},
			{
				ResourceName: "masterdaweb_object_storage_bucket.b", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return bucket, nil },
				ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})
}

func TestAccDatabase(t *testing.T) {
	if os.Getenv("MW_ACC_DATABASES") == "" {
		t.Skip("set MW_ACC_DATABASES=1 to create a database (it bills by the hour)")
	}
	name := accName("tf-acc-db")
	cfg := func(cidr string) string {
		return fmt.Sprintf(`
resource "masterdaweb_database" "db" {
  name                = %q
  engine              = "postgres"
  size                = "pg-1-1"
  allowed_cidrs       = [%q]
  deletion_protection = false
}
`, name, cidr)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: e2eProviders,
		Steps: []resource.TestStep{
			{
				Config: cfg("203.0.113.7"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("masterdaweb_database.db", "host"),
					resource.TestCheckResourceAttr("masterdaweb_database.db", "allowed_cidrs.0", "203.0.113.7"),
				),
			},
			{Config: cfg("198.51.100.0/24")},
			{
				ResourceName: "masterdaweb_database.db", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return name, nil },
				ImportStateVerifyIdentifierAttribute: "name",
				ImportStateVerifyIgnore:              []string{"password", "status"},
			},
		},
	})
}
