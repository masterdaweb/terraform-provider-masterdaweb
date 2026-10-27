package provider

import (
	"strings"
	"testing"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

const testServiceID = "0193f0e2-0000-7000-8000-00000000bbbb"

func withService(api *fakeAPI, kind string) {
	withProject(api)
	small := "small"
	minutes := int64(10)
	s := &client.Service{ID: testServiceID, ProjectID: testProjectID, ProjectName: "shop", Name: "web", Kind: kind, Status: "LIVE",
		Source: client.ServiceSource{Kind: "IMAGE", Image: &client.ImageSource{Ref: "nginx:1.27"}},
		Settings: client.ServiceSettings{Size: &small, VCPUMillis: 500, MemoryMiB: 1024, WatchPaths: []string{}, Port: 8080,
			StartupTimeoutSeconds: 300, MaxInstances: 3, Concurrency: 80, IdleTimeoutSeconds: 300, RequestTimeoutSeconds: 100,
			GracefulShutdownSeconds: 30, RolloutMinutes: &minutes, Instances: 1, CronTimezone: "UTC", CronTimeoutSeconds: 3600},
		FileManagedFields: []string{}, AutoDeploy: true, CreatedAt: fakeNow}
	api.services[s.ID] = s
	if kind == "WEB" {
		api.domains[s.ID] = []*client.Domain{{ID: "0193f0e2-0000-7000-8000-00000000dddd", Hostname: "web-x1y2z3.mwapp.com.br",
			IsDefault: true, Status: "ACTIVE", Instructions: []client.DNSInstruction{}, CreatedAt: fakeNow}}
	}
}

func variable(key, value string, extra map[string]any) map[string]any {
	cfg := map[string]any{"service_id": testServiceID, "key": key, "value": value}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestVariablePlainLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_variable")
	path := "/v1/serverless/services/" + testServiceID + "/variables"

	st := r.create(r.config(variable("APP_ENV", "production", nil)))
	api.expectBody("PUT", path, `{"variables":[{"key":"APP_ENV","value":"production","is_secret":false,"available_at_build":false}],"deploy":true}`)
	if str(t, st, "id") != testServiceID+"/APP_ENV" || num(t, st, "version") != 1 || str(t, st, "value") != "production" {
		t.Fatalf("state %v", st)
	}

	// Changed elsewhere: a plain value is read back, so the change shows.
	api.variables[testServiceID][0].value = "staging"
	p := r.plan(r.read(st), r.config(variable("APP_ENV", "production", nil)))
	if p.noop() {
		t.Fatal("a value changed elsewhere planned nothing")
	}

	api.resetCalls()
	st = r.update(r.read(st), r.config(variable("APP_ENV", "production", map[string]any{"available_at_build": true, "deploy": false})))
	api.expectBody("PUT", path, `{"variables":[{"key":"APP_ENV","value":"production","is_secret":false,"available_at_build":true}],"deploy":false}`)
	if !boolean(t, st, "available_at_build") || num(t, st, "version") != 2 {
		t.Fatalf("state %v", st)
	}

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	// The API's delete takes keys only (a deploy flag would be refused).
	api.expectBody("POST", path+"/delete", `{"keys":["APP_ENV"]}`)
	if len(api.variables[testServiceID]) != 0 {
		t.Fatal("not removed")
	}
}

func TestVariableSecretIsNeverReadBack(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_variable")
	cfg := r.config(variable("STRIPE_KEY", "sk_live_1", map[string]any{"secret": true}))
	st := r.create(cfg) // and no diff: the API answers null, the configured value stays
	if str(t, st, "value") != "sk_live_1" || !boolean(t, st, "secret") {
		t.Fatalf("state %v", st)
	}

	// Changed elsewhere: its version moved, so the configured value is set again.
	api.variables[testServiceID][0].value = "sk_live_other"
	api.variables[testServiceID][0].version = 2
	refreshed := r.read(st)
	if !isNull(t, refreshed, "value") {
		t.Fatalf("a secret changed elsewhere kept its value in state: %v", refreshed)
	}
	api.resetCalls()
	st = r.update(refreshed, cfg)
	api.expectBody("PUT", "/v1/serverless/services/"+testServiceID+"/variables",
		`{"variables":[{"key":"STRIPE_KEY","value":"sk_live_1","is_secret":true,"available_at_build":false}],"deploy":true}`)
	if api.variables[testServiceID][0].value != "sk_live_1" || num(t, st, "version") != 3 {
		t.Fatalf("not set again: %v", st)
	}

	// A secret stays a secret: making it plain replaces the variable.
	p := r.plan(st, r.config(variable("STRIPE_KEY", "sk_live_1", map[string]any{"secret": false})))
	if strings.Join(p.requiresReplace, ",") != `AttributeName("secret")` {
		t.Fatalf("requires replace %v", p.requiresReplace)
	}
}

func TestVariableBecomesSecretInPlace(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_variable")
	st := r.create(r.config(variable("DB_PASSWORD", "x", nil)))
	api.resetCalls()
	r.update(st, r.config(variable("DB_PASSWORD", "x", map[string]any{"secret": true})))
	// The value did not change, so it is not sent.
	api.expectBody("PUT", "/v1/serverless/services/"+testServiceID+"/variables",
		`{"variables":[{"key":"DB_PASSWORD","is_secret":true,"available_at_build":false}],"deploy":true}`)
}

func TestVariableExistingAndGone(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	api.variables[testServiceID] = []*fakeVariable{{key: "APP_ENV", value: "x", version: 1, updatedAt: fakeNow}}
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_variable")
	p := r.plan(r.null(), r.config(variable("APP_ENV", "production", nil)))
	_, diags := r.apply(p)
	expectError(t, diags, "The service already has a variable APP_ENV. Import it to manage it here: terraform import <address> "+testServiceID+"/APP_ENV")
	if got := api.mutations(); len(got) != 0 {
		t.Fatalf("an existing variable was overwritten: %v", got)
	}

	st, diags := r.importState(testServiceID + "/APP_ENV")
	failOnDiags(t, "import", diags)
	r.expectNoDiff(st, r.config(variable("APP_ENV", "x", nil)))

	api.variables[testServiceID] = nil
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a removed variable stayed: %v", got)
	}
	delete(api.services, testServiceID)
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("the variable of a removed service stayed: %v", got)
	}
}

func TestVariableImportOfASecret(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	api.variables[testServiceID] = []*fakeVariable{{key: "APP_KEY", value: "base64:abc", secret: true, version: 4, updatedAt: fakeNow}}
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_variable")
	st, diags := r.importState(testServiceID + "/APP_KEY")
	failOnDiags(t, "import", diags)
	if !isNull(t, st, "value") || num(t, st, "version") != 4 {
		t.Fatalf("imported %v", st)
	}
	// The secret cannot be read: the next apply sets the configured value
	// (the API leaves it unchanged when it is the same).
	api.resetCalls()
	st = r.update(st, r.config(variable("APP_KEY", "base64:abc", map[string]any{"secret": true})))
	if num(t, st, "version") != 4 {
		t.Fatalf("setting the same secret again changed its version: %v", st)
	}
	_, diags = r.importState("APP_KEY")
	expectError(t, diags, "Import a variable as <service_id>/<key>")
}

func TestVariableRefusals(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	api.bindings[testServiceID] = []*client.Binding{{ID: "b1", ResourceKind: "POSTGRES", Variables: []string{"DATABASE_URL"}}}
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_variable")
	p := r.plan(r.null(), r.config(variable("DATABASE_URL", "postgres://x", nil)))
	_, diags := r.apply(p)
	expectError(t, diags, "DATABASE_URL is set by a binding. Change the binding's variable layout or prefix instead. (HTTP 409, SERVERLESS_BINDING_CONFLICT")

	api.projects[testProjectID].DeploysFrozen = true
	p = r.plan(r.null(), r.config(variable("APP_ENV", "production", nil)))
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	expectWarning(t, diags, "Saved, but not deployed")
	if str(t, st, "key") != "APP_ENV" {
		t.Fatalf("state %v", st)
	}

	expectError(t, r.validate(r.config(variable("PORT", "1", nil))), "PORT is set by the platform")
	expectError(t, r.validate(r.config(variable("mw_storage_x", "1", nil))), "Names starting with MW_ are the platform's")
	expectError(t, r.validate(r.config(variable("1ABC", "1", nil))), "must be letters, digits and underscores")
}
