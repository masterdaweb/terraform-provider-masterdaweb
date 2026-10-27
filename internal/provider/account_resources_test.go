package provider

import (
	"strings"
	"testing"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

func TestSSHKeyLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_ssh_key")
	line := testPublicKey(7, "ana@laptop")
	// As file("~/.ssh/id_ed25519.pub") reads it: with its newline.
	cfg := map[string]any{"name": "laptop", "public_key": line + "\n"}
	st := r.create(r.config(cfg))
	api.expectBody("POST", "/v1/ssh-keys", `{"name":"laptop","public_key":"`+line+`"}`)
	id := str(t, st, "id")
	if str(t, st, "type") != "ssh-ed25519" || !strings.HasPrefix(str(t, st, "fingerprint"), "SHA256:") || str(t, st, "public_key") != line+"\n" {
		t.Fatalf("state %v", st)
	}

	api.resetCalls()
	cfg["name"] = "laptop-2"
	st = r.update(st, r.config(cfg))
	api.expectBody("PATCH", "/v1/ssh-keys/"+id, `{"name":"laptop-2"}`)

	other := map[string]any{"name": "laptop-2", "public_key": testPublicKey(8, "")}
	if p := r.plan(st, r.config(other)); strings.Join(p.requiresReplace, ",") != "AttributeName(\"public_key\")" {
		t.Fatalf("a new key replaces the old one: %v", p.requiresReplace)
	}

	// The same key twice is the API's 409, said as it says it.
	p := r.plan(r.null(), r.config(map[string]any{"name": "again", "public_key": line}))
	_, diags := r.apply(p)
	expectError(t, diags, `This key is already saved as "laptop-2"`)

	imp, diags := r.importState(id)
	failOnDiags(t, "import", diags)
	r.expectNoDiff(imp, r.config(map[string]any{"name": "laptop-2", "public_key": line}))

	fp := str(t, st, "fingerprint")
	for _, by := range []map[string]any{{"fingerprint": fp}, {"name": "laptop-2"}, {"id": id}} {
		ds, diags := h.readDataSource("masterdaweb_ssh_key", by)
		failOnDiags(t, "data source", diags)
		if str(t, ds, "id") != id || str(t, ds, "public_key") != line {
			t.Fatalf("key %v", ds)
		}
	}

	failOnDiags(t, "destroy", r.destroy(st))
	if len(api.infra.sshKeys) != 0 {
		t.Fatal("still there")
	}
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a deleted key stayed: %v", got)
	}

	expectError(t, r.validate(r.config(map[string]any{"name": "x", "public_key": "-----BEGIN OPENSSH PRIVATE KEY-----"})), "That is a PRIVATE key")
	expectError(t, r.validate(r.config(map[string]any{"name": "x", "public_key": "ssh-dss AAAAB3NzaC1kc3M="})), "Invalid public key")
	expectError(t, r.validate(r.config(map[string]any{"name": "x", "public_key": line + "\n" + line})), "Invalid public key")
	expectError(t, r.validate(r.config(map[string]any{"name": strings.Repeat("n", 65), "public_key": line})), "name")
	expectError(t, r.validate(r.config(map[string]any{"name": "x", "public_key": line + " " + strings.Repeat("c", 8200)})), "Public key too long")
}

func TestSnapshotLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	web := api.seedInstance("web", "3")
	r := newHarness(t, api.URL()).resource("masterdaweb_snapshot")
	path := "/v1/instances/" + web.inst.ID + "/snapshots"
	// Another snapshot still being taken: the first try is refused.
	api.infra.busy["POST "+path] = 1
	cfg := r.config(map[string]any{"instance_id": web.inst.ID, "name": "before-upgrade", "description": "pre"})
	st := r.create(cfg)
	posts := api.callsTo("POST", path)
	if len(posts) != 2 || posts[0].Body != posts[1].Body {
		t.Fatalf("posts %+v", posts)
	}
	assertJSON(t, posts[1].Body, `{"name":"before-upgrade","include_ram":false,"description":"pre"}`)
	id := str(t, st, "id")
	if str(t, st, "expires_at") != "2026-11-25T12:00:00.000Z" || boolean(t, st, "include_ram") {
		t.Fatalf("state %v", st)
	}

	imp, diags := r.importState(web.inst.ID + "/" + id)
	failOnDiags(t, "import", diags)
	r.expectNoDiff(imp, cfg)
	_, diags = r.importState(id)
	expectError(t, diags, "<instance_id>/<snapshot_id>")

	if p := r.plan(st, r.config(map[string]any{"instance_id": web.inst.ID, "name": "before-upgrade", "include_ram": true})); len(p.requiresReplace) == 0 {
		t.Fatal("a snapshot is taken again to change it")
	}

	api.resetCalls()
	api.infra.busy["DELETE "+path+"/"+id] = 1
	failOnDiags(t, "destroy", r.destroy(st))
	if n := len(api.callsTo("DELETE", path+"/"+id)); n != 2 {
		t.Fatalf("%d deletes, want 1 refused while busy and 1 that went through", n)
	}

	// Expired after 30 days: gone from the state, taken again next apply.
	st = r.create(cfg)
	api.infra.snapshots[web.inst.ID] = nil
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("an expired snapshot stayed: %v", got)
	}

	for _, name := range []string{"1st", "a", "with space", strings.Repeat("a", 41)} {
		expectError(t, r.validate(r.config(map[string]any{"instance_id": "1", "name": name})), "must start with a letter")
	}
	expectError(t, r.validate(r.config(map[string]any{"instance_id": "1", "name": "ok", "description": strings.Repeat("d", 256)})), "description")
}

func TestStorageKeyLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	api.buckets["shop-files"] = &client.Bucket{ID: "0193f0e2-0000-7000-8000-00000000b001", Name: "shop-files", Location: "bh"}
	r := newHarness(t, api.URL()).resource("masterdaweb_object_storage_key")
	cfg := r.config(map[string]any{"name": "ci", "access": "read_only", "buckets": []string{"shop-files"},
		"expires_at": "2027-01-01T00:00:00Z"})
	st := r.create(cfg)
	api.expectBody("POST", "/v1/object-storage/keys", `{"name":"ci","access":"read_only","buckets":["shop-files"],"expires_at":"2027-01-01T00:00:00Z"}`)
	id := str(t, st, "id")
	if str(t, st, "secret_access_key") != "mw-secret-1" || str(t, st, "access_key_id") != "MWAK000001" {
		t.Fatalf("state %v", st)
	}
	// The secret is the API's once: a refresh keeps it.
	if str(t, r.read(st), "secret_access_key") != "mw-secret-1" {
		t.Fatal("the secret was lost")
	}

	imp, diags := r.importState(id)
	failOnDiags(t, "import", diags)
	if !isNull(t, imp, "secret_access_key") {
		t.Fatalf("an imported key has a secret: %v", imp)
	}
	r.expectNoDiff(imp, cfg)

	if p := r.plan(st, r.config(map[string]any{"name": "ci-2", "access": "read_only", "buckets": []string{"shop-files"},
		"expires_at": "2027-01-01T00:00:00Z"})); len(p.requiresReplace) == 0 {
		t.Fatal("a key is made again to change it")
	}

	every := r.create(r.config(map[string]any{"name": "backup"}))
	if !isNull(t, every, "buckets") || str(t, every, "access") != "read_write" {
		t.Fatalf("a key to every bucket %v", every)
	}

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if got := strings.Join(api.mutations(), "\n"); got != "DELETE /v1/object-storage/keys/"+id {
		t.Fatalf("calls:\n%s", got)
	}
	expectError(t, r.validate(r.config(map[string]any{"name": "x", "access": "admin"})), "access")
}

func TestProjectResourceMembership(t *testing.T) {
	api := newFakeAPI(t)
	shop, blog := api.seedProject("shop"), api.seedProject("blog")
	web := api.seedInstance("web", "3")
	api.buckets["shop-files"] = &client.Bucket{ID: "0193f0e2-0000-7000-8000-00000000b001", Name: "shop-files", Location: "bh"}
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_project_resource")

	inst := r.config(map[string]any{"project_id": shop.ID, "kind": "INSTANCE", "resource_id": web.inst.ID})
	st := r.create(inst)
	api.expectBody("POST", "/v1/projects/"+shop.ID+"/resources", `{"resources":[{"kind":"INSTANCE","id":"`+web.inst.ID+`"}]}`)
	if str(t, st, "id") != shop.ID+"/INSTANCE/"+web.inst.ID || str(t, st, "name") != "web" || str(t, st, "status") != "RUNNING" {
		t.Fatalf("state %v", st)
	}
	bucket := r.create(r.config(map[string]any{"project_id": shop.ID, "kind": "BUCKET", "resource_id": "shop-files"}))
	if str(t, bucket, "name") != "shop-files" {
		t.Fatalf("bucket %v", bucket)
	}
	ds, diags := h.readDataSource("masterdaweb_project", map[string]any{"name": "shop"})
	failOnDiags(t, "project", diags)
	if num(t, ds, "instance_count") != 1 || num(t, ds, "bucket_count") != 1 {
		t.Fatalf("project %v", ds)
	}

	// Another project: the membership is replaced (the instance moves).
	moved := r.config(map[string]any{"project_id": blog.ID, "kind": "INSTANCE", "resource_id": web.inst.ID})
	if p := r.plan(st, moved); len(p.requiresReplace) == 0 {
		t.Fatal("a move is a new membership")
	}

	imp, diags := r.importState(shop.ID + "/instance/" + web.inst.ID)
	failOnDiags(t, "import", diags)
	r.expectNoDiff(imp, inst)
	_, diags = r.importState(shop.ID + "/VPS/" + web.inst.ID)
	expectError(t, diags, "INSTANCE, BUCKET or DATABASE")

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if got := strings.Join(api.mutations(), "\n"); got != "DELETE /v1/projects/"+shop.ID+"/resources/instance/"+web.inst.ID {
		t.Fatalf("calls:\n%s", got)
	}
	if _, ok := api.infra.instances[web.inst.ID]; !ok {
		t.Fatal("taking the instance out of the project destroyed it")
	}
	// Out of the project already: gone from the state.
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("stayed %v", got)
	}

	p := r.plan(r.null(), r.config(map[string]any{"project_id": shop.ID, "kind": "DATABASE", "resource_id": "nope"}))
	_, diags = r.apply(p)
	expectError(t, diags, "Database nope not found")
}

func TestWebhookLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_webhook")
	cfg := map[string]any{"url": "https://hooks.example.com/mw/abcd1234", "description": "Alerts",
		"events": []string{"deployment.failed", "alert.*"}}
	st := r.create(r.config(cfg))
	api.expectBody("POST", "/v1/webhooks", `{"kind":"GENERIC","url":"https://hooks.example.com/mw/abcd1234","description":"Alerts",
		"events":["alert.*","deployment.failed"]}`)
	id := str(t, st, "id")
	if !strings.HasPrefix(str(t, st, "secret"), "whsec_") || str(t, st, "masked_url") != "hooks.example.com/…1234" ||
		str(t, st, "url") != "https://hooks.example.com/mw/abcd1234" {
		t.Fatalf("state %v", st)
	}

	// Switched off by the platform after failing: the next apply switches it on.
	reason := "failing"
	api.infra.webhooks[id].Enabled, api.infra.webhooks[id].DisabledReason = false, &reason
	api.resetCalls()
	refreshed := r.read(st)
	p := r.plan(refreshed, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	api.expectBody("PATCH", "/v1/webhooks/"+id, `{"enabled":true}`)

	// Every event, without a description: in place.
	api.resetCalls()
	st = r.update(st, r.config(map[string]any{"url": "https://hooks.example.com/mw/abcd1234"}))
	api.expectBody("PATCH", "/v1/webhooks/"+id, `{"description":null,"events":[]}`)
	if !isNull(t, st, "events") {
		t.Fatalf("state %v", st)
	}
	if p := r.plan(st, r.config(map[string]any{"url": "https://hooks.example.com/other"})); len(p.requiresReplace) == 0 {
		t.Fatal("a new address is a new webhook")
	}

	// Read one by one.
	if n := len(api.callsTo("GET", "/v1/webhooks/"+id)); n == 0 {
		t.Fatal("the webhook was not read by its id")
	}
	imp, diags := r.importState(id)
	failOnDiags(t, "import", diags)
	if !isNull(t, imp, "secret") || !isNull(t, imp, "url") {
		t.Fatalf("imported %v", imp)
	}
	// An API from before GET /v1/webhooks/{id}: read from the list.
	api.infra.legacyFields = true
	if got := r.read(imp); str(t, got, "id") != id {
		t.Fatalf("read from the list %v", got)
	}
	api.infra.legacyFields = false
	// The address the configuration gives is taken as it is.
	p = r.plan(imp, r.config(map[string]any{"url": "https://hooks.example.com/mw/abcd1234"}))
	failOnDiags(t, "plan after import", p.diags)
	if len(p.requiresReplace) > 0 {
		t.Fatalf("an import replaces the webhook: %v", p.requiresReplace)
	}

	slack := r.create(r.config(map[string]any{"kind": "SLACK", "url": "https://hooks.slack.com/services/T0/B0/XYZW", "locale": "pt-BR"}))
	if !isNull(t, slack, "secret") {
		t.Fatalf("a Slack webhook has a secret: %v", slack)
	}

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if got := strings.Join(api.mutations(), "\n"); got != "DELETE /v1/webhooks/"+id {
		t.Fatalf("calls:\n%s", got)
	}
	expectError(t, r.validate(r.config(map[string]any{"url": "http://insecure.example.com"})), "https://")
	expectError(t, r.validate(r.config(map[string]any{"url": "https://x.example.com/h", "events": []string{"Deploy failed"}})), "must be an event")
	expectError(t, r.validate(r.config(map[string]any{"url": "https://x.example.com/h", "locale": "en"})), "A locale is for messages")
}

func TestBackupScheduleLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	web := api.seedInstance("web", "3")
	r := newHarness(t, api.URL()).resource("masterdaweb_backup_schedule")
	cfg := map[string]any{"instance_id": web.inst.ID, "days": []string{"thu", "mon"}, "time": "03:30", "timezone": "America/Sao_Paulo"}

	// Without the backup add-on, the API says what is missing.
	p := r.plan(r.null(), r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "Backups need the backup add-on")

	web.addons["backup-slot"] = 2
	path := "/v1/instances/" + web.inst.ID + "/backup-schedule"
	api.resetCalls()
	st := r.create(r.config(cfg))
	api.expectBody("PUT", path, `{"days":["mon","thu"],"time":"03:30","timezone":"America/Sao_Paulo"}`)
	if str(t, st, "id") != web.inst.ID || len(attrList(t, st, "days")) != 2 {
		t.Fatalf("state %v", st)
	}

	api.resetCalls()
	cfg["days"], cfg["time"] = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}, "23:15"
	delete(cfg, "timezone")
	st = r.update(st, r.config(cfg))
	api.expectBody("PUT", path, `{"days":["sun","mon","tue","wed","thu","fri","sat"],"time":"23:15"}`)
	if !isNull(t, st, "timezone") {
		t.Fatalf("state %v", st)
	}

	imp, diags := r.importState(web.inst.ID)
	failOnDiags(t, "import", diags)
	r.expectNoDiff(imp, r.config(cfg))

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if got := strings.Join(api.mutations(), "\n"); got != "DELETE "+path {
		t.Fatalf("calls:\n%s", got)
	}
	// Gone (its 404): out of the state.
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("stayed %v", got)
	}
	_, diags = r.importState(web.inst.ID)
	if !hasError(diags) {
		if got := r.read(st); !got.IsNull() {
			t.Fatal("imported a schedule that is not there")
		}
	}

	for _, bad := range []map[string]any{
		{"instance_id": "1", "days": []string{"monday"}, "time": "03:30"},
		{"instance_id": "1", "days": []string{}, "time": "03:30"},
		{"instance_id": "1", "days": []string{"mon"}, "time": "3:30"},
		{"instance_id": "1", "days": []string{"mon"}, "time": "24:00"},
		{"instance_id": "1", "days": []string{"mon"}, "time": "03:30", "timezone": "Sao Paulo"},
	} {
		if !hasError(r.validate(r.config(bad))) {
			t.Errorf("%v was accepted", bad)
		}
	}
}
