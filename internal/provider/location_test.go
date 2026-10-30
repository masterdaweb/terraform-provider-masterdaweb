package provider

import (
	"strings"
	"testing"
)

// A project and a database run where they are made, for good: a region_id
// asked for is sent as the API's datacenter_id, one left out is read back,
// and changing it replaces the resource — never a silent move.

func TestProjectInARegion(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_project")

	// Left out: the platform's default, read back.
	st := r.create(r.config(map[string]any{"name": "shop", "deletion_protection": false}))
	api.expectBody("POST", "/v1/projects", `{"name":"shop","deletion_protection":false}`)
	if str(t, st, "region_id") != "3" || str(t, st, "region") != "bh" {
		t.Fatalf("state %v", st)
	}
	// The same configuration plans nothing; naming the same region neither.
	if p := r.plan(st, r.config(map[string]any{"name": "shop", "deletion_protection": false})); !p.noop() {
		t.Fatal("an unchanged project plans a change")
	}
	if p := r.plan(st, r.config(map[string]any{"name": "shop", "deletion_protection": false, "region_id": "3"})); len(p.requiresReplace) != 0 {
		t.Fatalf("its own region replaces it: %v", p.requiresReplace)
	}
	// Another region is another project.
	if p := r.plan(st, r.config(map[string]any{"name": "shop", "deletion_protection": false, "region_id": "4"})); strings.Join(p.requiresReplace, ",") != `AttributeName("region_id")` {
		t.Fatalf("a move plans %v", p.requiresReplace)
	}

	api.resetCalls()
	us := r.create(r.config(map[string]any{"name": "shop-us", "deletion_protection": false, "region_id": "4"}))
	api.expectBody("POST", "/v1/projects", `{"name":"shop-us","deletion_protection":false,"datacenter_id":4}`)
	if str(t, us, "region_id") != "4" || str(t, us, "region") != "dal" {
		t.Fatalf("state %v", us)
	}
}

func TestDatabaseInARegion(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_database")
	st := r.create(r.config(pg(map[string]any{"region_id": "4", "deletion_protection": false})))
	api.expectBody("POST", "/v1/databases", `{"name":"shop-db","engine":"postgres","size":"pg-1-1","deletion_protection":false,"datacenter_id":4}`)
	if str(t, st, "region_id") != "4" {
		t.Fatalf("state %v", st)
	}
	if p := r.plan(st, r.config(pg(map[string]any{"region_id": "3", "deletion_protection": false}))); strings.Join(p.requiresReplace, ",") != `AttributeName("region_id")` {
		t.Fatalf("a move plans %v", p.requiresReplace)
	}

	// In a project: the project's location, read back.
	api2 := newFakeAPI(t)
	withProject(api2)
	r2 := newHarness(t, api2.URL()).resource("masterdaweb_database")
	in := r2.create(r2.config(pg(map[string]any{"project_id": testProjectID, "deletion_protection": false})))
	if str(t, in, "region_id") != "3" {
		t.Fatalf("state %v", in)
	}
}
