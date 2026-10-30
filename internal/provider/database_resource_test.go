package provider

import (
	"strings"
	"testing"
)

func pg(extra map[string]any) map[string]any {
	cfg := map[string]any{"name": "shop-db", "engine": "postgres", "size": "pg-1-1"}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestDatabasePostgresLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_database")

	// The API keeps 203.0.113.7 as 203.0.113.7/32: the configuration's spelling stays.
	cfg := r.config(pg(map[string]any{"project_id": testProjectID, "allowed_cidrs": []string{"203.0.113.7", "2001:DB8::1/64"},
		"extensions": []string{"vector"}, "maintenance_day": "sunday", "maintenance_hour_utc": 4,
		"parameters": map[string]any{"statement_timeout": "30s"}}))
	st := r.create(cfg)
	api.expectBody("POST", "/v1/databases", `{"name":"shop-db","engine":"postgres","size":"pg-1-1","deletion_protection":true,
		"project_id":"`+testProjectID+`","allowed_cidrs":["2001:DB8::1/64","203.0.113.7"],"extensions":["vector"]}`)
	// The server settings, like the maintenance window, are set once the database exists.
	api.expectBody("PATCH", "/v1/databases/shop-db", `{"maintenance_day":0,"maintenance_hour_utc":4,"parameters":{"statement_timeout":"30s"}}`)
	if got := api.databases["shop-db"].AllowedCIDRs; strings.Join(got, ",") != "2001:db8::/64,203.0.113.7/32" {
		t.Fatalf("the fake normalised to %v", got)
	}
	if str(t, st, "password") != "s3cret-owner-password" || str(t, st, "version") != "18" || num(t, st, "disk_gb") != 10 ||
		num(t, st, "pooler_port") != 6432 || str(t, st, "status") != "provisioning" || !boolean(t, st, "backups") ||
		str(t, st, "maintenance_day") != "sunday" || num(t, st, "disk_autogrow_max_gb") != 0 || !isNull(t, st, "eviction_policy") ||
		str(t, st, "parameters", "statement_timeout") != "30s" {
		t.Fatalf("state %v", st)
	}

	// Resizing waits for the database to run: the API says so.
	p := r.plan(st, r.config(pg(map[string]any{"size": "pg-2-4", "project_id": testProjectID,
		"allowed_cidrs": []string{"203.0.113.7", "2001:DB8::1/64"}, "extensions": []string{"vector"}, "maintenance_day": "sunday", "maintenance_hour_utc": 4})))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "The database is not running yet. (HTTP 409, DATABASE_NOT_READY")

	api.databases["shop-db"].Status = "running"
	api.resetCalls()
	next := pg(map[string]any{"size": "pg-2-4", "project_id": testProjectID, "allowed_cidrs": []string{"203.0.113.7", "198.51.100.0/24"},
		"extensions": []string{"vector", "pg_trgm"}, "maintenance_day": "daily", "maintenance_hour_utc": 4, "disk_autogrow_max_gb": 100,
		"parameters": map[string]any{"statement_timeout": "1min", "work_mem": "8MB"}})
	st = r.update(st, r.config(next))
	api.expectBody("PATCH", "/v1/databases/shop-db", `{"maintenance_day":null,"disk_autogrow_max_gb":100,
		"parameters":{"statement_timeout":"1min","work_mem":"8MB"},
		"allowed_cidrs":["198.51.100.0/24","203.0.113.7"],"extensions":["pg_trgm","vector"]}`)
	api.expectBody("POST", "/v1/databases/shop-db/resize", `{"size":"pg-2-4","when":"now"}`)
	// The bigger size came with a bigger disk.
	if num(t, st, "disk_gb") != 40 || str(t, st, "size") != "pg-2-4" || str(t, st, "maintenance_day") != "daily" ||
		str(t, st, "parameters", "work_mem") != "8MB" {
		t.Fatalf("state %v", st)
	}

	next["disk_gb"] = 60
	api.resetCalls()
	st = r.update(st, r.config(next))
	api.expectBody("POST", "/v1/databases/shop-db/resize", `{"disk_gb":60,"when":"now"}`)

	next["disk_gb"] = 50
	p = r.plan(st, r.config(next))
	expectError(t, p.diags, "The database's disk is 60 GB and never shrinks")
	next["disk_gb"] = 60

	next["extensions"] = []string{"pg_trgm"}
	p = r.plan(st, r.config(next))
	expectError(t, p.diags, "Keep vector in extensions")
	next["extensions"] = []string{"vector", "pg_trgm"}

	expectError(t, r.destroy(st), "The database shop-db is protected from deletion")
	next["deletion_protection"] = false
	st = r.update(st, r.config(next))
	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	del := api.callsTo("DELETE", "/v1/databases/shop-db")
	if len(del) != 1 || del[0].Query.Get("confirm") != "shop-db" {
		t.Fatalf("delete %+v", del)
	}
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a deleted database stayed: %v", got)
	}
}

func TestDatabaseRedisAndRefusals(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_database")
	st := r.create(r.config(map[string]any{"name": "queues", "engine": "valkey", "size": "kv-1-1", "deletion_protection": false}))
	api.expectBody("POST", "/v1/databases", `{"name":"queues","engine":"valkey","size":"kv-1-1","deletion_protection":false}`)
	if str(t, st, "eviction_policy") != "noeviction" || boolean(t, st, "backups") || !isNull(t, st, "pooler_port") || !isNull(t, st, "database_name") {
		t.Fatalf("state %v", st)
	}
	api.resetCalls()
	r.update(st, r.config(map[string]any{"name": "queues", "engine": "valkey", "size": "kv-1-1", "deletion_protection": false,
		"eviction_policy": "allkeys-lru"}))
	api.expectBody("PATCH", "/v1/databases/queues", `{"eviction_policy":"allkeys-lru"}`)

	expectError(t, r.validate(r.config(map[string]any{"name": "x-db", "engine": "mysql", "size": "my-1-1", "extensions": []string{"vector"}})), "Extensions are for PostgreSQL")
	expectError(t, r.validate(r.config(pg(map[string]any{"eviction_policy": "allkeys-lru"}))), "An eviction policy is for Redis")
	expectError(t, r.validate(r.config(map[string]any{"name": "q2", "engine": "valkey", "size": "kv-1-1", "backups": true})), "Backups are for SQL databases")
	expectError(t, r.validate(r.config(pg(map[string]any{"name": "Shop"}))), "lowercase")
	expectError(t, r.validate(r.config(pg(map[string]any{"name": "shop--db"}))), "two hyphens")

	// A database starts with its size's disk.
	p := r.plan(r.null(), r.config(pg(map[string]any{"disk_gb": 50})))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "A pg-1-1 database starts with 10 GB")
	if len(api.callsTo("POST", "/v1/databases")) != 0 {
		t.Fatal("a refused disk still created the database")
	}
	r.create(r.config(pg(map[string]any{"disk_gb": 10})))

	p = r.plan(r.null(), r.config(pg(nil)))
	_, diags = r.apply(p)
	expectError(t, diags, "This account already has a database named shop-db. (HTTP 409, DATABASE_NAME_TAKEN")
}

func TestDatabaseImportAndGone(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_database")
	cfg := r.config(pg(map[string]any{"allowed_cidrs": []string{"203.0.113.7/32"}}))
	st := r.create(cfg)
	id := str(t, st, "id")
	for _, ref := range []string{"shop-db", id} {
		got, diags := r.importState(ref)
		failOnDiags(t, "import "+ref, diags)
		if str(t, got, "id") != id || !isNull(t, got, "password") {
			t.Fatalf("imported %v", got)
		}
		r.expectNoDiff(got, cfg)
	}
	_, diags := r.importState("0193f0e2-0000-7000-8000-000000000404")
	expectError(t, diags, "No database has the id")

	// The name went to another database: this one is gone.
	api.databases["shop-db"].ID = "0193f0e2-0000-7000-8000-0000000000ff"
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("another database's state was taken: %v", got)
	}
	delete(api.databases, "shop-db")
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a 404 kept the database: %v", got)
	}
}

func TestDatabaseInUseCannotBeDeleted(t *testing.T) {
	api := newFakeAPI(t)
	withService(api, "WEB")
	r := newHarness(t, api.URL()).resource("masterdaweb_database")
	st := r.create(r.config(pg(map[string]any{"deletion_protection": false})))
	b := newHarness(t, api.URL()).resource("masterdaweb_serverless_binding")
	b.create(b.config(map[string]any{"service_id": testServiceID, "resource_kind": "POSTGRES", "resource_name": "shop-db"}))
	expectError(t, r.destroy(st), "Services are still bound to this database. Unbind them first. (HTTP 409, DATABASE_IN_USE")
}

func TestNormalizeCIDR(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":         "203.0.113.7/32",
		"203.0.113.7/24":      "203.0.113.0/24",
		" 10.0.0.1/8 ":        "10.0.0.0/8",
		"2001:DB8::1":         "2001:db8::1/128",
		"2001:db8:0:0:1::/64": "2001:db8::/64",
		"2001:0:0:1:0:0:0:1":  "2001:0:0:1::1/128",
		"not-an-address":      "not-an-address",
	} {
		if got := normalizeCIDR(in); got != want {
			t.Errorf("normalizeCIDR(%q) = %q, want %q", in, got, want)
		}
	}
}
