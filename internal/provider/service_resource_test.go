package provider

import (
	"strings"
	"testing"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

const testProjectID = "0193f0e2-0000-7000-8000-00000000aaaa"

func withProject(api *fakeAPI) {
	api.projects[testProjectID] = &client.Project{ID: testProjectID, Name: "shop", Region: "bh", DeletionProtection: true, CreatedAt: fakeNow}
}

func webService(extra map[string]any) map[string]any {
	cfg := map[string]any{
		"project_id": testProjectID,
		"name":       "web",
		"kind":       "WEB",
		"git":        map[string]any{"repo_url": "https://github.com/masterdaweb/mw-template-aspnet"},
		"port":       8080,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestServiceFromAPublicRepository(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")

	st := r.create(r.config(webService(map[string]any{"health_check_path": "/health", "size": "small"})))
	api.expectBody("POST", "/v1/serverless/projects/"+testProjectID+"/services", `{
		"name":"web","kind":"WEB","auto_deploy":true,"wait_for_ci":false,"deletion_protection":false,"deploy":true,
		"source":{"kind":"GIT","git":{"provider":"PUBLIC_URL","repo_url":"https://github.com/masterdaweb/mw-template-aspnet"}},
		"settings":{"port":8080,"health_check_path":"/health","size":"small"}}`)
	if got := str(t, st, "url"); got != "https://web-x1y2z3.mwapp.com.br" {
		t.Fatalf("url %q", got)
	}
	if str(t, st, "git", "branch") != "main" || str(t, st, "git", "provider") != "PUBLIC_URL" || !isNull(t, st, "git", "repo") {
		t.Fatalf("git %v", attrAt(t, st, "git"))
	}
	if num(t, st, "vcpu_millis") != 500 || num(t, st, "memory_mib") != 1024 || num(t, st, "max_instances") != 3 {
		t.Fatalf("settings %v", st)
	}
	if str(t, st, "release_command") != "" || str(t, st, "cron_timezone") != "America/Sao_Paulo" ||
		!isNull(t, st, "rollout_percent") || num(t, st, "rollout_minutes") != 10 {
		t.Fatalf("defaults %v", st)
	}
	if got := api.mutations(); len(got) != 1 {
		t.Fatalf("a create is one call, got %v", got)
	}
}

func TestServiceSettingsChangeSendsOnlyWhatChanged(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	st := r.create(r.config(webService(nil)))
	id := str(t, st, "id")
	api.resetCalls()

	// A runtime setting: applied on the live image, no build.
	st = r.update(st, r.config(webService(map[string]any{"size": "medium", "min_instances": 1, "rollout_percent": 10})))
	api.expectBody("PATCH", "/v1/serverless/services/"+id+"/settings",
		`{"settings":{"size":"medium","min_instances":1,"rollout_percent":10},"deploy":true}`)
	if got := api.mutations(); len(got) != 1 {
		t.Fatalf("calls %v", got)
	}
	if num(t, st, "vcpu_millis") != 1000 || num(t, st, "memory_mib") != 2048 || num(t, st, "rollout_percent") != 10 {
		t.Fatalf("state %v", st)
	}

	// A setting the build reads takes a new build: the latest commit.
	api.resetCalls()
	st = r.update(st, r.config(webService(map[string]any{"size": "medium", "min_instances": 1, "rollout_percent": 10, "root_dir": "src/MwApp"})))
	api.expectBody("PATCH", "/v1/serverless/services/"+id+"/settings", `{"settings":{"root_dir":"src/MwApp"},"deploy":false}`)
	api.expectBody("POST", "/v1/serverless/services/"+id+"/deployments", `{"action":"latest"}`)

	// "" clears a command; the state says "".
	api.services[id].Settings.StartCommand = strPtr("dotnet MwApp.dll")
	st = r.read(st)
	api.resetCalls()
	st = r.update(st, r.config(webService(map[string]any{"size": "medium", "min_instances": 1, "rollout_percent": 10, "root_dir": "src/MwApp", "start_command": ""})))
	api.expectBody("PATCH", "/v1/serverless/services/"+id+"/settings", `{"settings":{"start_command":null},"deploy":true}`)
	if str(t, st, "start_command") != "" {
		t.Fatalf("start_command %v", attrAt(t, st, "start_command"))
	}

	// Leaving a setting out keeps what the service has (a gradual rollout
	// left out is off: it stays in the configuration).
	api.resetCalls()
	p := r.plan(r.read(st), r.config(webService(map[string]any{"rollout_percent": 10})))
	failOnDiags(t, "plan", p.diags)
	if !p.noop() {
		t.Fatalf("leaving settings out planned a change:\n%s", strings.Join(differences("service", p.prior, p.state), "\n"))
	}
}

func strPtr(s string) *string { return &s }

func TestServiceSourceChangeDeploysTheLatest(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	st := r.create(r.config(webService(nil)))
	id := str(t, st, "id")
	api.resetCalls()

	st = r.update(st, r.config(webService(map[string]any{
		"git": map[string]any{"repo_url": "https://github.com/masterdaweb/mw-template-aspnet", "branch": "release", "tag_pattern": "v*"},
	})))
	api.expectBody("PATCH", "/v1/serverless/services/"+id+"/source", `{"git":{"provider":"PUBLIC_URL",
		"repo_url":"https://github.com/masterdaweb/mw-template-aspnet","connection_id":null,"repo_full_name":null,
		"branch":"release","tag_pattern":"v*"}}`)
	api.expectBody("POST", "/v1/serverless/services/"+id+"/deployments", `{"action":"latest"}`)
	if str(t, st, "git", "branch") != "release" || str(t, st, "git", "tag_pattern") != "v*" {
		t.Fatalf("git %v", attrAt(t, st, "git"))
	}

	// auto_deploy is part of the source, but no reason to build.
	api.resetCalls()
	r.update(st, r.config(webService(map[string]any{
		"git":         map[string]any{"repo_url": "https://github.com/masterdaweb/mw-template-aspnet", "branch": "release", "tag_pattern": "v*"},
		"auto_deploy": false,
	})))
	api.expectBody("PATCH", "/v1/serverless/services/"+id+"/source", `{"auto_deploy":false}`)
	if got := api.mutations(); len(got) != 1 {
		t.Fatalf("calls %v", got)
	}
}

func TestServiceDeployFalseNeverDeploys(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	img := func(ref string, extra map[string]any) map[string]any {
		cfg := map[string]any{"project_id": testProjectID, "name": "proxy", "kind": "WEB", "deploy": false,
			"image": map[string]any{"ref": ref}, "port": 80}
		for k, v := range extra {
			cfg[k] = v
		}
		return cfg
	}
	st := r.create(r.config(img("nginx:1.27", nil)))
	id := str(t, st, "id")
	api.expectBody("POST", "/v1/serverless/projects/"+testProjectID+"/services", `{
		"name":"proxy","kind":"WEB","auto_deploy":true,"wait_for_ci":false,"deletion_protection":false,"deploy":false,
		"source":{"kind":"IMAGE","image":{"ref":"nginx:1.27"}},"settings":{"port":80}}`)
	if str(t, st, "status") != "NEW" {
		t.Fatalf("status %s", str(t, st, "status"))
	}
	api.resetCalls()
	r.update(st, r.config(img("nginx:1.28", map[string]any{"max_instances": 5})))
	api.expectBody("PATCH", "/v1/serverless/services/"+id+"/source", `{"image":{"ref":"nginx:1.28","registry_credential_id":null}}`)
	api.expectBody("PATCH", "/v1/serverless/services/"+id+"/settings", `{"settings":{"max_instances":5},"deploy":false}`)
	if n := len(api.callsTo("POST", "/v1/serverless/services/"+id+"/deployments")); n != 0 {
		t.Fatalf("%d deployments with deploy = false", n)
	}
}

func TestServiceReplacements(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	st := r.create(r.config(webService(nil)))

	p := r.plan(st, r.config(webService(map[string]any{"git": nil, "image": map[string]any{"ref": "nginx:1.27"}})))
	failOnDiags(t, "plan", p.diags)
	if strings.Join(p.requiresReplace, ",") != `AttributeName("git"),AttributeName("image")` {
		t.Fatalf("a move from Git to an image replaces the service, got %v", p.requiresReplace)
	}
	p = r.plan(st, r.config(webService(map[string]any{"kind": "PRIVATE"})))
	if strings.Join(p.requiresReplace, ",") != `AttributeName("kind")` {
		t.Fatalf("a new kind replaces the service, got %v", p.requiresReplace)
	}
	p = r.plan(st, r.config(webService(map[string]any{"name": "site"})))
	if strings.Join(p.requiresReplace, ",") != `AttributeName("name")` {
		t.Fatalf("a new name replaces the service, got %v", p.requiresReplace)
	}
}

func TestServiceFromAConnectedRepository(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	api.gitConnections = []client.GitConnection{
		{ID: "conn-old", Provider: "GITLAB", AccountLogin: "ana", Status: "REVOKED"},
		{ID: "conn-1", Provider: "GITHUB", AccountLogin: "acme", Status: "ACTIVE"},
	}
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	cfg := r.config(map[string]any{"project_id": testProjectID, "name": "api", "kind": "PRIVATE",
		"git": map[string]any{"repo": "Acme/Shop"}})
	st := r.create(cfg)
	api.expectBody("POST", "/v1/serverless/projects/"+testProjectID+"/services", `{
		"name":"api","kind":"PRIVATE","auto_deploy":true,"wait_for_ci":false,"deletion_protection":false,"deploy":true,
		"source":{"kind":"GIT","git":{"provider":"GITHUB","connection_id":"conn-1","repo_full_name":"Acme/Shop"}}}`)
	// The provider's own casing of owner/name does not show up as a change.
	if str(t, st, "git", "repo") != "Acme/Shop" || str(t, st, "git", "connection_id") != "conn-1" || str(t, st, "git", "branch") != "trunk" {
		t.Fatalf("git %v", attrAt(t, st, "git"))
	}
	if str(t, st, "internal_url") != "http://api" || !isNull(t, st, "url") {
		t.Fatalf("addresses %v", st)
	}

	api.gitConnections = append(api.gitConnections, client.GitConnection{ID: "conn-2", Provider: "BITBUCKET", AccountLogin: "acme", Status: "ACTIVE"})
	p := r.plan(r.null(), r.config(map[string]any{"project_id": testProjectID, "name": "two", "kind": "WORKER",
		"git": map[string]any{"repo": "acme/shop"}}))
	_, diags := r.apply(p)
	expectError(t, diags, "more than one Git account is connected (github acme [conn-1], bitbucket acme [conn-2]): set git.provider or git.connection_id")

	r.create(r.config(map[string]any{"project_id": testProjectID, "name": "three", "kind": "WORKER",
		"git": map[string]any{"repo": "acme/shop", "provider": "BITBUCKET"}}))

	api.gitConnections = nil
	p = r.plan(r.null(), r.config(map[string]any{"project_id": testProjectID, "name": "four", "kind": "WORKER",
		"git": map[string]any{"repo": "acme/shop"}}))
	_, diags = r.apply(p)
	expectError(t, diags, "no Git account is connected")
}

func TestServiceRepositoryManagedSettings(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	st := r.create(r.config(webService(nil)))
	id := str(t, st, "id")

	// A deployment applied the repository's mw.toml.
	api.services[id].Settings.ReleaseCommand = strPtr("./efbundle")
	api.services[id].FileManagedFields = []string{"release_command"}
	st = r.expectNoDiff(st, r.config(webService(nil)))
	if str(t, st, "release_command") != "./efbundle" || strings.Join(strs(t, st, "repository_settings"), ",") != "release_command" {
		t.Fatalf("state %v", st)
	}

	p := r.plan(st, r.config(webService(map[string]any{"release_command": "dotnet ef database update"})))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "This setting is defined in the repository's mw.toml. Change it there. (HTTP 409, SERVERLESS_SETTING_MANAGED_BY_FILE")
}

func TestServiceDeleteWaitsUntilGone(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	st := r.create(r.config(webService(map[string]any{"deletion_protection": true})))
	id := str(t, st, "id")

	expectError(t, r.destroy(st), "The service web is protected from deletion. Set deletion_protection = false")
	st = r.update(st, r.config(webService(map[string]any{"deletion_protection": false})))
	api.expectBody("PUT", "/v1/serverless/services/"+id+"/deletion-protection", `{"enabled":false}`)

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	del := api.callsTo("DELETE", "/v1/serverless/services/"+id)
	if len(del) != 1 || del[0].Query.Get("confirm") != "web" {
		t.Fatalf("delete %+v", del)
	}
	if gets := len(api.callsTo("GET", "/v1/serverless/services/"+id)); gets != 3 {
		t.Fatalf("%d reads while waiting, want 3 (deleting, deleting, gone)", gets)
	}
	if _, ok := api.services[id]; ok {
		t.Fatal("the destroy returned before the service was gone")
	}
}

func TestServiceGoneIsRemovedFromState(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	st := r.create(r.config(webService(nil)))
	delete(api.services, str(t, st, "id"))
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a 404 kept the service: %v", got)
	}
	st = r.create(r.config(webService(map[string]any{"name": "web2"})))
	api.services[str(t, st, "id")].Status = "DELETING"
	api.serviceGoneAfter[str(t, st, "id")] = 9
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a service being deleted stayed: %v", got)
	}
}

func TestServiceConflictAndRefusedDeployment(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	r.create(r.config(webService(nil)))
	p := r.plan(r.null(), r.config(webService(nil)))
	_, diags := r.apply(p)
	expectError(t, diags, `The project already has a service named "web". (HTTP 409, SERVERLESS_NAME_TAKEN`)

	// Frozen deploys: the service is made, its deployment refused. It is kept.
	api.projects[testProjectID].DeploysFrozen = true
	p = r.plan(r.null(), r.config(webService(map[string]any{"name": "web2"})))
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	expectWarning(t, diags, "Deployments are frozen for this project")
	if str(t, st, "name") != "web2" {
		t.Fatalf("state %v", st)
	}
}

func TestServiceImport(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	cfg := r.config(webService(map[string]any{"size": "large"}))
	st := r.create(cfg)
	id := str(t, st, "id")
	for _, ref := range []string{id, testProjectID + "/web"} {
		got, diags := r.importState(ref)
		failOnDiags(t, "import", diags)
		if str(t, got, "id") != id || !boolean(t, got, "deploy") {
			t.Fatalf("imported %v", got)
		}
		r.expectNoDiff(got, cfg)
	}
	_, diags := r.importState(testProjectID + "/nope")
	expectError(t, diags, "has no service named nope")
}

func TestServiceValidation(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	expectError(t, r.validate(r.config(webService(map[string]any{"git": nil}))), "")
	expectError(t, r.validate(r.config(webService(map[string]any{"image": map[string]any{"ref": "nginx"}}))), "")
	expectError(t, r.validate(r.config(webService(map[string]any{"git": map[string]any{}}))), "Which repository?")
	expectError(t, r.validate(r.config(webService(map[string]any{"git": map[string]any{"repo": "a/b", "repo_url": "https://x.com/a/b"}}))), "not both")
	expectError(t, r.validate(r.config(webService(map[string]any{"git": map[string]any{"repo_url": "https://x.com/a/b", "provider": "GITHUB"}}))), "A public repository has no provider")
	expectError(t, r.validate(r.config(webService(map[string]any{"kind": "CRON"}))), "A cron job needs a schedule")
	expectError(t, r.validate(r.config(webService(map[string]any{"name": "Web_1"}))), "lowercase")
	expectError(t, r.validate(r.config(webService(map[string]any{"size": "huge"}))), "size")
	expectError(t, r.validate(r.config(webService(map[string]any{"port": 70000}))), "port")
	failOnDiags(t, "validate", r.validate(r.config(webService(map[string]any{"kind": "CRON", "cron_schedule": "*/5 * * * *"}))))
	failOnDiags(t, "validate unknown", r.validate(r.config(webService(map[string]any{"project_id": unknown, "git": map[string]any{"repo_url": unknown}}))))
}

func TestServiceGradualRollouts(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	settings := "/v1/serverless/projects/" + testProjectID + "/services"

	st := r.create(r.config(webService(map[string]any{"rollout_percent": 10, "rollout_minutes": 15})))
	api.expectBody("POST", settings, `{
		"name":"web","kind":"WEB","auto_deploy":true,"wait_for_ci":false,"deletion_protection":false,"deploy":true,
		"source":{"kind":"GIT","git":{"provider":"PUBLIC_URL","repo_url":"https://github.com/masterdaweb/mw-template-aspnet"}},
		"settings":{"port":8080,"rollout_percent":10,"rollout_minutes":15}}`)
	if num(t, st, "rollout_percent") != 10 || num(t, st, "rollout_minutes") != 15 {
		t.Fatalf("state %v", st)
	}
	id := str(t, st, "id")
	path := "/v1/serverless/services/" + id + "/settings"
	deployed := func() int { return api.deployments }

	// A rollout setting alone shapes the next deployment: it starts none.
	before := deployed()
	api.resetCalls()
	st = r.update(st, r.config(webService(map[string]any{"rollout_percent": 25, "rollout_minutes": 15})))
	api.expectBody("PATCH", path, `{"settings":{"rollout_percent":25},"deploy":false}`)
	api.resetCalls()
	st = r.update(st, r.config(webService(map[string]any{"rollout_percent": 25, "rollout_minutes": 30})))
	api.expectBody("PATCH", path, `{"settings":{"rollout_minutes":30},"deploy":false}`)
	if deployed() != before {
		t.Fatalf("a rollout setting started %d deployment(s)", deployed()-before)
	}

	// 0 is off, sent as null; it reads back as 0, with no diff.
	api.resetCalls()
	st = r.update(st, r.config(webService(map[string]any{"rollout_percent": 0, "rollout_minutes": 30})))
	api.expectBody("PATCH", path, `{"settings":{"rollout_percent":null},"deploy":false}`)
	if num(t, st, "rollout_percent") != 0 || api.services[id].Settings.RolloutPercent != nil {
		t.Fatalf("state %v, API %v", attrAt(t, st, "rollout_percent"), api.services[id].Settings.RolloutPercent)
	}

	// From 0 to left out: off both, nothing to send.
	api.resetCalls()
	st = r.update(st, r.config(webService(map[string]any{"rollout_minutes": 30})))
	if got := api.mutations(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	if !isNull(t, st, "rollout_percent") {
		t.Fatalf("rollout_percent %v", attrAt(t, st, "rollout_percent"))
	}

	// Left out: off, and the default length. Turned on elsewhere, it is
	// turned off again.
	api.services[id].Settings.RolloutPercent = int64Ptr(20)
	api.resetCalls()
	st = r.update(r.read(st), r.config(webService(nil)))
	api.expectBody("PATCH", path, `{"settings":{"rollout_percent":null,"rollout_minutes":10},"deploy":false}`)

	// With another setting, the change deploys as usual.
	api.resetCalls()
	r.update(st, r.config(webService(map[string]any{"rollout_percent": 5, "max_instances": 6})))
	api.expectBody("PATCH", path, `{"settings":{"max_instances":6,"rollout_percent":5},"deploy":true}`)

	expectError(t, r.validate(r.config(webService(map[string]any{"rollout_percent": 51}))), "rollout_percent")
	expectError(t, r.validate(r.config(webService(map[string]any{"rollout_minutes": 0}))), "rollout_minutes")
}

func int64Ptr(n int64) *int64 { return &n }

func TestServiceRolloutsDefinedByTheRepository(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")
	st := r.create(r.config(webService(nil)))
	id := str(t, st, "id")

	// The repository's mw.toml turned a gradual rollout on: left out here,
	// it stays the repository's, with no diff.
	api.services[id].Settings.RolloutPercent = int64Ptr(25)
	api.services[id].Settings.RolloutMinutes = int64Ptr(20)
	api.services[id].FileManagedFields = []string{"rollout_percent", "rollout_minutes"}
	st = r.expectNoDiff(st, r.config(webService(nil)))
	if num(t, st, "rollout_percent") != 25 || num(t, st, "rollout_minutes") != 20 {
		t.Fatalf("state %v", st)
	}
	p := r.plan(st, r.config(webService(map[string]any{"rollout_percent": 10})))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "This setting is defined in the repository's mw.toml. Change it there.")
}

func TestServiceOnAnAPIWithoutRollouts(t *testing.T) {
	api := newFakeAPI(t)
	api.withoutRollouts = true
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_service")

	// Not using them, nothing about them is sent or missed.
	st := r.create(r.config(webService(map[string]any{"size": "small"})))
	if !isNull(t, st, "rollout_percent") || num(t, st, "rollout_minutes") != 10 {
		t.Fatalf("state %v", st)
	}
	r.update(st, r.config(webService(map[string]any{"size": "medium"})))

	p := r.plan(r.null(), r.config(webService(map[string]any{"name": "web2", "rollout_percent": 10})))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "property rollout_percent should not exist")
}
