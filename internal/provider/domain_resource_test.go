package provider

import (
	"strings"
	"testing"
)

func TestDomainLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_domain")

	// The API keeps hostnames lowercase without the trailing dot: no diff.
	cfg := r.config(map[string]any{"service_id": testServiceID, "hostname": "App.Shop.com.br."})
	st := r.create(cfg)
	api.expectBody("POST", "/v1/serverless/services/"+testServiceID+"/domains", `{"hostname":"app.shop.com.br"}`)
	if str(t, st, "hostname") != "App.Shop.com.br." || str(t, st, "status") != "PENDING_DNS" || boolean(t, st, "apex") {
		t.Fatalf("state %v", st)
	}
	if str(t, st, "dns_records", 0, "type") != "TXT" || str(t, st, "dns_records", 0, "name") != "_mw-verify.app.shop.com.br" ||
		str(t, st, "dns_records", 1, "type") != "CNAME" || str(t, st, "dns_records", 1, "value") != "cname.mwapp.com.br" {
		t.Fatalf("records %v", attrAt(t, st, "dns_records"))
	}
	id := str(t, st, "id")

	// The DNS check moves on: computed values change, the plan does not.
	api.domains[testServiceID][1].Status = "ACTIVE"
	activated := fakeNow
	api.domains[testServiceID][1].ActivatedAt = &activated
	st = r.expectNoDiff(st, cfg)
	if str(t, st, "status") != "ACTIVE" {
		t.Fatalf("status %s", str(t, st, "status"))
	}

	p := r.plan(st, r.config(map[string]any{"service_id": testServiceID, "hostname": "www.shop.com.br"}))
	if strings.Join(p.requiresReplace, ",") != `AttributeName("hostname")` {
		t.Fatalf("requires replace %v", p.requiresReplace)
	}

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if len(api.callsTo("DELETE", "/v1/serverless/domains/"+id)) != 1 {
		t.Fatalf("calls %v", api.mutations())
	}
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a removed domain stayed: %v", got)
	}
	// Already gone: destroying again is fine.
	failOnDiags(t, "destroy again", r.destroy(st))
}

func TestDomainApexHasAlternatives(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_domain")
	st := r.create(r.config(map[string]any{"service_id": testServiceID, "hostname": "shop.com.br"}))
	if !boolean(t, st, "apex") || str(t, st, "redirect_to") != "www.shop.com.br" {
		t.Fatalf("state %v", st)
	}
	if str(t, st, "dns_records", 1, "option") != "alias" || str(t, st, "dns_records", 2, "option") != "redirect" ||
		!isNull(t, st, "dns_records", 0, "found") {
		t.Fatalf("records %v", attrAt(t, st, "dns_records"))
	}
}

func TestDomainImportAndRefusals(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_domain")
	cfg := r.config(map[string]any{"service_id": testServiceID, "hostname": "app.shop.com.br"})
	st := r.create(cfg)
	id := str(t, st, "id")
	for _, ref := range []string{id, testServiceID + "/" + id, testServiceID + "/app.shop.com.br"} {
		got, diags := r.importState(ref)
		failOnDiags(t, "import "+ref, diags)
		if str(t, got, "id") != id || str(t, got, "service_id") != testServiceID {
			t.Fatalf("imported %v", got)
		}
		r.expectNoDiff(got, cfg)
	}
	_, diags := r.importState(testServiceID + "/web-x1y2z3.mwapp.com.br")
	expectError(t, diags, "is the service's default address")

	p := r.plan(r.null(), cfg)
	_, diags = r.apply(p)
	expectError(t, diags, "This domain is already in use on the platform. (HTTP 409, SERVERLESS_DOMAIN_TAKEN")

	api.services[testServiceID].Kind = "WORKER"
	p = r.plan(r.null(), r.config(map[string]any{"service_id": testServiceID, "hostname": "jobs.shop.com.br"}))
	_, diags = r.apply(p)
	expectError(t, diags, "Only web services have a public address")
}
