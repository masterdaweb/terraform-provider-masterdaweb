package provider

import (
	"strings"
	"testing"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

func withBucketAndDatabase(api *fakeAPI) {
	api.buckets["shop-files"] = &client.Bucket{ID: "0193f0e2-0000-7000-8000-0000000b0001", Name: "shop-files", Location: "north-america",
		QuotaMode: "alert", Status: "active", Cors: []client.CorsRule{}, Lifecycle: []client.LifecycleRule{}, CreatedAt: fakeNow}
	api.databases["shop-db"] = &client.Database{ID: "0193f0e2-0000-7000-8000-0000000d0001", Name: "shop-db", Engine: "postgres",
		Version: "18", Status: "running", DeletionProtection: true, CreatedAt: fakeNow}
}

func TestBindingBucketWithDefaults(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	withBucketAndDatabase(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_binding")
	path := "/v1/serverless/services/" + testServiceID + "/bindings"

	st := r.create(r.config(map[string]any{"service_id": testServiceID, "resource_kind": "BUCKET", "resource_name": "shop-files"}))
	api.expectBody("POST", path, `{"resource_kind":"BUCKET","resource":"shop-files","deploy":true}`)
	if str(t, st, "env_profile") != "prefixed" || str(t, st, "env_prefix") != "MW_STORAGE_" || str(t, st, "access") != "read_write" {
		t.Fatalf("state %v", st)
	}
	if got := strings.Join(strs(t, st, "variables"), " "); !strings.Contains(got, "MW_STORAGE_ACCESS_KEY_ID") {
		t.Fatalf("variables %s", got)
	}
	if str(t, st, "resource_id") != "0193f0e2-0000-7000-8000-0000000b0001" {
		t.Fatalf("resource_id %s", str(t, st, "resource_id"))
	}

	// Saying the defaults out loud changes nothing.
	r.expectNoDiff(st, r.config(map[string]any{"service_id": testServiceID, "resource_kind": "BUCKET", "resource_name": "shop-files",
		"env_profile": "prefixed", "env_prefix": "MW_STORAGE_", "access": "read_write"}))

	p := r.plan(st, r.config(map[string]any{"service_id": testServiceID, "resource_kind": "BUCKET", "resource_name": "shop-files", "access": "read_only"}))
	if strings.Join(p.requiresReplace, ",") != `AttributeName("access")` {
		t.Fatalf("requires replace %v", p.requiresReplace)
	}

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if len(api.callsTo("DELETE", path+"/"+str(t, st, "id"))) != 1 {
		t.Fatalf("calls %v", api.mutations())
	}
	// The API answers 400 "No such binding" for one already gone: fine.
	failOnDiags(t, "destroy again", r.destroy(st))
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a removed binding stayed: %v", got)
	}
}

func TestBindingPostgresDotnet(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	withBucketAndDatabase(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_binding")
	cfg := r.config(map[string]any{"service_id": testServiceID, "resource_kind": "POSTGRES", "resource_name": "shop-db",
		"env_profile": "dotnet", "deploy": false})
	st := r.create(cfg)
	api.expectBody("POST", "/v1/serverless/services/"+testServiceID+"/bindings",
		`{"resource_kind":"POSTGRES","resource":"shop-db","env_profile":"dotnet","deploy":false}`)
	if got := strings.Join(strs(t, st, "variables"), " "); got != "ConnectionStrings__DefaultConnection" {
		t.Fatalf("variables %s", got)
	}
	if str(t, st, "env_prefix") != "" || str(t, st, "access") != "read_write" {
		t.Fatalf("state %v", st)
	}

	// deploy alone changes in place, with no call.
	api.resetCalls()
	st = r.update(st, r.config(map[string]any{"service_id": testServiceID, "resource_kind": "POSTGRES", "resource_name": "shop-db", "env_profile": "dotnet"}))
	if got := api.mutations(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}

	// The database deleted elsewhere keeps the binding's name in state.
	api.bindings[testServiceID][0].ResourceName = nil
	r.expectNoDiff(st, r.config(map[string]any{"service_id": testServiceID, "resource_kind": "POSTGRES", "resource_name": "shop-db", "env_profile": "dotnet"}))
}

func TestBindingRefusedDeploymentIsKept(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	withBucketAndDatabase(api)
	api.projects[testProjectID].Paused = true
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_binding")
	p := r.plan(r.null(), r.config(map[string]any{"service_id": testServiceID, "resource_kind": "BUCKET", "resource_name": "shop-files"}))
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	expectWarning(t, diags, "The project is paused. Resume it to deploy.")
	if isNull(t, st, "id") {
		t.Fatal("the binding made was not kept")
	}
}

func TestBindingImportAndValidation(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	withBucketAndDatabase(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_binding")
	cfg := r.config(map[string]any{"service_id": testServiceID, "resource_kind": "POSTGRES", "resource_name": "shop-db"})
	st := r.create(cfg)
	if got := strings.Join(strs(t, st, "variables"), " "); got != "DATABASE_URL" {
		t.Fatalf("variables %s", got)
	}
	for _, ref := range []string{testServiceID + "/" + str(t, st, "id"), testServiceID + "/shop-db"} {
		got, diags := r.importState(ref)
		failOnDiags(t, "import", diags)
		r.expectNoDiff(got, cfg)
	}
	_, diags := r.importState(str(t, st, "id"))
	expectError(t, diags, "Import a binding as <service_id>/<binding_id>")

	p := r.plan(r.null(), cfg)
	_, diags = r.apply(p)
	expectError(t, diags, "is already bound to this service. (HTTP 409, SERVERLESS_BINDING_CONFLICT")

	bucket := func(extra map[string]any) map[string]any {
		c := map[string]any{"service_id": testServiceID, "resource_kind": "BUCKET", "resource_name": "shop-files"}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	expectError(t, r.validate(r.config(bucket(map[string]any{"env_profile": "libpq"}))), "A BUCKET binding takes prefixed, framework")
	expectError(t, r.validate(r.config(bucket(map[string]any{"env_profile": "framework", "env_prefix": "FILES_"}))), "fixed names")
	expectError(t, r.validate(r.config(bucket(map[string]any{"env_prefix": "files_"}))), "ending in an underscore")
	expectError(t, r.validate(r.config(map[string]any{"service_id": testServiceID, "resource_kind": "POSTGRES",
		"resource_name": "shop-db", "access": "read_only"})), "read_only is for buckets")
}
