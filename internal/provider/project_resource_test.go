package provider

import (
	"net/http"
	"strings"
	"testing"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

func TestProjectLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_project")

	// The API trims the description: the configuration's spelling stays.
	cfg := r.config(map[string]any{"name": "shop", "description": "  The shop  "})
	st := r.create(cfg)
	call := api.expectBody("POST", "/v1/projects", `{"name":"shop","description":"  The shop  "}`)
	if call.Key == "" || call.Auth != "Bearer mwk_test" {
		t.Fatalf("the create carried no idempotency key or token: %+v", call)
	}
	if got := api.mutations(); len(got) != 1 {
		t.Fatalf("protection on by default needs no second call, got %v", got)
	}
	id := str(t, st, "id")
	if !boolean(t, st, "deletion_protection") || str(t, st, "region") != "bh" || str(t, st, "description") != "  The shop  " {
		t.Fatalf("state %v", st)
	}

	api.resetCalls()
	st = r.update(st, r.config(map[string]any{"name": "shop-prod", "deletion_protection": false}))
	api.expectBody("PATCH", "/v1/projects/"+id, `{"name":"shop-prod","description":null,"deletion_protection":false}`)
	if str(t, st, "name") != "shop-prod" || !isNull(t, st, "description") || boolean(t, st, "deletion_protection") {
		t.Fatalf("state %v", st)
	}

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	del := api.callsTo("DELETE", "/v1/projects/"+id)
	if len(del) != 1 || del[0].Query.Get("confirm") != "shop-prod" || del[0].Key == "" {
		t.Fatalf("delete calls %+v", del)
	}
	// Two-phase: the destroy waited until the project was gone.
	if _, ok := api.projects[id]; ok {
		t.Fatal("the destroy returned before the project was gone")
	}
}

func TestProjectCreatedUnprotected(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	st := r.create(r.config(map[string]any{"name": "staging", "deletion_protection": false}))
	// Unprotected from the start: one call.
	if got := strings.Join(api.mutations(), "\n"); got != "POST /v1/projects" {
		t.Fatalf("calls:\n%s", got)
	}
	api.expectBody("POST", "/v1/projects", `{"name":"staging","deletion_protection":false}`)
	if boolean(t, st, "deletion_protection") {
		t.Fatal("still protected")
	}
}

// An API from before /v1/projects keeps them under /v1/serverless/projects:
// the provider finds them there.
func TestProjectOnAnAPIBeforeV1Projects(t *testing.T) {
	api := newFakeAPI(t)
	api.legacyProjects = true
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	st := r.create(r.config(map[string]any{"name": "shop", "deletion_protection": false}))
	id := str(t, st, "id")
	failOnDiags(t, "destroy", r.destroy(st))
	want := "POST /v1/serverless/projects\nDELETE /v1/serverless/projects/" + id + "?confirm=shop"
	if got := strings.Join(api.mutations(), "\n"); got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
	if n := len(api.callsTo("GET", "/v1/projects")); n != 1 {
		t.Fatalf("asked %d times where projects live, want once", n)
	}
}

func TestProjectDestroyRefusedWhileProtected(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	st := r.create(r.config(map[string]any{"name": "shop"}))
	api.resetCalls()
	expectError(t, r.destroy(st), "Set deletion_protection = false, run terraform apply, and then destroy it")
	if got := api.mutations(); len(got) != 0 {
		t.Fatalf("a protected project was sent %v", got)
	}
}

func TestProjectGoneIsRemovedFromState(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	st := r.create(r.config(map[string]any{"name": "shop"}))
	delete(api.projects, str(t, st, "id"))
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a 404 kept the project in state: %v", got)
	}

	st = r.create(r.config(map[string]any{"name": "other"}))
	api.projects[str(t, st, "id")].Deleting = true
	api.projectGoneAfter[str(t, st, "id")] = 5
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a project being deleted stayed in state: %v", got)
	}
}

func TestProjectNameTakenIsAConflict(t *testing.T) {
	api := newFakeAPI(t)
	api.projects["p0"] = &client.Project{ID: "p0", Name: "shop", Region: "bh", CreatedAt: fakeNow}
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	p := r.plan(r.null(), r.config(map[string]any{"name": "shop"}))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, `A project named "shop" already exists. (HTTP 409, SERVERLESS_NAME_TAKEN, request id req-fake)`)
}

func TestProjectCreateRetriesARateLimit(t *testing.T) {
	api := newFakeAPI(t)
	refused := 0
	api.intercept = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		if r.Method == http.MethodPost && refused < 2 {
			refused++
			w.Header().Set("Retry-After", "0")
			problem(w, 429, "", "Rate limit exceeded for write requests. Retry in 0s.", nil)
			return true
		}
		return false
	}
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	r.create(r.config(map[string]any{"name": "shop"}))
	posts := api.callsTo("POST", "/v1/projects")
	if len(posts) != 3 {
		t.Fatalf("%d POSTs, want 3", len(posts))
	}
	if posts[0].Key != posts[1].Key || posts[1].Key != posts[2].Key {
		t.Fatalf("the retries changed the idempotency key: %q %q %q", posts[0].Key, posts[1].Key, posts[2].Key)
	}
	if len(api.projects) != 1 {
		t.Fatalf("%d projects made", len(api.projects))
	}
}

func TestProjectImport(t *testing.T) {
	api := newFakeAPI(t)
	desc := "The shop"
	api.projects["0193f0e2-0000-7000-8000-000000000099"] = &client.Project{ID: "0193f0e2-0000-7000-8000-000000000099",
		Name: "shop", Description: &desc, Region: "bh", DeletionProtection: true, CreatedAt: fakeNow}
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	cfg := r.config(map[string]any{"name": "shop", "description": "The shop"})

	for _, ref := range []string{"0193f0e2-0000-7000-8000-000000000099", "shop"} {
		st, diags := r.importState(ref)
		failOnDiags(t, "import "+ref, diags)
		if str(t, st, "id") != "0193f0e2-0000-7000-8000-000000000099" {
			t.Fatalf("imported %v", st)
		}
		r.expectNoDiff(st, cfg)
	}
	_, diags := r.importState("nope")
	expectError(t, diags, `No project has the id or name "nope"`)
}

func TestProjectValidation(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_project")
	expectError(t, r.validate(r.config(map[string]any{"name": strings.Repeat("x", 65)})), "name")
	expectError(t, r.validate(r.config(map[string]any{"name": "ok", "description": strings.Repeat("x", 501)})), "description")
}

func TestProjectDataSource(t *testing.T) {
	api := newFakeAPI(t)
	api.projects["0193f0e2-0000-7000-8000-000000000001"] = &client.Project{ID: "0193f0e2-0000-7000-8000-000000000001",
		Name: "shop", Region: "bh", DeletionProtection: true, CreatedAt: fakeNow}
	h := newHarness(t, api.URL())

	byName, diags := h.readDataSource("masterdaweb_project", map[string]any{"name": "shop"})
	failOnDiags(t, "read by name", diags)
	if str(t, byName, "id") != "0193f0e2-0000-7000-8000-000000000001" || !boolean(t, byName, "deletion_protection") {
		t.Fatalf("by name %v", byName)
	}
	if num(t, byName, "instance_count") != 0 || num(t, byName, "service_count") != 0 {
		t.Fatalf("counts %v", byName)
	}
	byID, diags := h.readDataSource("masterdaweb_project", map[string]any{"id": "0193f0e2-0000-7000-8000-000000000001"})
	failOnDiags(t, "read by id", diags)
	if str(t, byID, "name") != "shop" {
		t.Fatalf("by id %v", byID)
	}
	_, diags = h.readDataSource("masterdaweb_project", map[string]any{"name": "nope"})
	expectError(t, diags, `The account has no project named "nope"`)
	_, diags = h.readDataSource("masterdaweb_project", map[string]any{"name": "shop", "id": "x"})
	expectError(t, diags, "")
	_, diags = h.readDataSource("masterdaweb_project", map[string]any{"id": "0193f0e2-0000-7000-8000-000000000404"})
	expectError(t, diags, "Project not found. (HTTP 404")
}

func TestOutboundAddressesDataSource(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	st, diags := h.readDataSource("masterdaweb_serverless_outbound_addresses", map[string]any{})
	failOnDiags(t, "read", diags)
	if got := strings.Join(strs(t, st, "ranges"), ","); got != "148.224.62.0/27" {
		t.Fatalf("ranges %s", got)
	}
	if got := strings.Join(strs(t, st, "addresses"), ","); got != "148.224.62.21,148.224.62.23" {
		t.Fatalf("addresses %s", got)
	}
	if got := strings.Join(strs(t, st, "addresses_v6"), ","); got != "2804:2d00:1::21" {
		t.Fatalf("addresses_v6 %s", got)
	}
	if len(api.callsTo("GET", "/v1/serverless/outbound-addresses")) != 1 {
		t.Fatal("the data source did not read GET /v1/serverless/outbound-addresses")
	}
}
