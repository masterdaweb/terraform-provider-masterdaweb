package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestSecurityAndVPNThroughTerraform runs the Security Center and VPN
// gateway resources through Terraform itself: a gateway ordered outside
// Terraform, looked up, its settings and a login managed; the account's scan
// settings and an accepted risk; updates, imports and destroy.
func TestSecurityAndVPNThroughTerraform(t *testing.T) {
	api := e2e(t)
	gw := api.seedVPNGateway("office")
	api.seedFinding("bucket", "b-1", "critical", "open", "Public bucket", 95)
	mysql := api.seedFinding("instance", gw.InstanceID, "high", "open", "MySQL open", 81)
	score, grade := int64(64), "C"
	api.sec.overview.Score, api.sec.overview.Grade = &score, &grade
	cfg := func(protocol string, mtu int, email, status, frequency string) string {
		return fmt.Sprintf(`
terraform {
  required_providers {
    masterdaweb = { source = "masterdaweb/masterdaweb" }
  }
}

data "masterdaweb_vpn_gateway" "office" {
  name = "office"
}

resource "masterdaweb_vpn_gateway_settings" "office" {
  gateway_id = data.masterdaweb_vpn_gateway.office.id
  protocol   = %[1]q
  mtu        = %[2]d
}

resource "masterdaweb_vpn_user" "maria" {
  gateway_id = data.masterdaweb_vpn_gateway.office.id
  username   = "maria"
  email      = %[3]q
  status     = %[4]q
}

resource "masterdaweb_security_settings" "this" {
  default_frequency = %[5]q
}

resource "masterdaweb_security_exception" "replica" {
  scope         = "port"
  port          = 3306
  protocol      = "tcp"
  justification = "Reachable only by the analytics vendor over its fixed address."
  expires_at    = "2026-12-01T00:00:00Z"
}

resource "masterdaweb_security_exception" "mysql" {
  scope         = "finding"
  finding_id    = %[6]q
  justification = "The replica's MySQL is reachable only from the vendor's fixed address."
  expires_at    = "2026-12-01T00:00:00Z"
}

data "masterdaweb_security_overview" "account" {}

data "masterdaweb_security_findings" "critical" {
  severity = "critical"
}
`, protocol, mtu, email, status, frequency, mysql.ID)
	}
	last := cfg("tcp", 1380, "m.silva@example.com", "inactive", "monthly")
	imported := func(address string, ignore ...string) resource.TestStep {
		return resource.TestStep{Config: last, ResourceName: address, ImportState: true,
			ImportStateVerify: true, ImportStateVerifyIgnore: ignore}
	}
	login := imported("masterdaweb_vpn_user.maria", "password")
	login.ImportStateIdFunc = func(s *terraform.State) (string, error) {
		return gw.ID + "/" + s.RootModule().Resources["masterdaweb_vpn_user.maria"].Primary.ID, nil
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: e2eProviders,
		CheckDestroy: func(*terraform.State) error {
			if n := len(api.sec.users[gw.ID]); n > 0 {
				return fmt.Errorf("%d VPN logins left behind", n)
			}
			for _, e := range api.sec.exceptions {
				if e.Status == "active" {
					return fmt.Errorf("exception %s still in force", e.ID)
				}
			}
			if api.sec.gateways[gw.ID].Protocol != "tcp" {
				return fmt.Errorf("destroying the settings changed the gateway")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg("udp", 1420, "maria@example.com", "active", "weekly"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("masterdaweb_vpn_user.maria", "password"),
					resource.TestCheckResourceAttr("masterdaweb_vpn_gateway_settings.office", "pending_changes", "false"),
					resource.TestCheckResourceAttr("masterdaweb_vpn_gateway_settings.office", "public_ip", "203.0.113.50"),
					resource.TestCheckResourceAttr("masterdaweb_security_settings.this", "enabled", "true"),
					resource.TestCheckResourceAttr("masterdaweb_security_exception.replica", "status", "active"),
					resource.TestCheckResourceAttr("masterdaweb_security_exception.mysql", "instance_id", gw.InstanceID),
					resource.TestCheckResourceAttr("data.masterdaweb_security_overview.account", "score", "64"),
					resource.TestCheckResourceAttr("data.masterdaweb_security_findings.critical", "findings.#", "1"),
					func(*terraform.State) error {
						if gw.PendingChanges || len(api.sec.applies) == 0 {
							return fmt.Errorf("the login was not applied: %v", api.sec.applies)
						}
						return nil
					},
				),
			},
			{
				Config: last,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("masterdaweb_vpn_gateway_settings.office", "protocol", "tcp"),
					resource.TestCheckResourceAttr("masterdaweb_vpn_gateway_settings.office", "mtu", "1380"),
					resource.TestCheckResourceAttr("masterdaweb_vpn_user.maria", "status", "inactive"),
					resource.TestCheckResourceAttr("masterdaweb_security_settings.this", "default_frequency", "monthly"),
					func(*terraform.State) error {
						if gw.Protocol != "tcp" || gw.MTU != 1380 || gw.PendingChanges || api.sec.settings.DefaultFrequency != "monthly" {
							return fmt.Errorf("gateway %+v, settings %+v", gw, api.sec.settings)
						}
						return nil
					},
				),
			},
			login,
			imported("masterdaweb_vpn_gateway_settings.office"),
			imported("masterdaweb_security_settings.this"),
			// The API writes the date its own way (…00.000Z); the finding is
			// the API's, exactly.
			imported("masterdaweb_security_exception.replica", "expires_at"),
			imported("masterdaweb_security_exception.mysql", "expires_at"),
		},
	})
}
