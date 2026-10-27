package provider

import (
	"strings"
	"testing"
)

func drain(extra map[string]any) map[string]any {
	cfg := map[string]any{"project_id": testProjectID, "kind": "HTTPS",
		"url": "https://logs.example.com/ingest/abcd1234", "token": "Bearer s3cret"}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestLogDrainHTTPS(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_log_drain")
	path := "/v1/serverless/projects/" + testProjectID + "/log-drains"

	st := r.create(r.config(drain(nil)))
	api.expectBody("POST", path, `{"kind":"HTTPS","url":"https://logs.example.com/ingest/abcd1234","token":"Bearer s3cret"}`)
	// The API shows the address shortened and never the token: both stay as configured.
	if str(t, st, "destination") != "https://logs.example.com/…1234" || str(t, st, "url") != "https://logs.example.com/ingest/abcd1234" ||
		str(t, st, "token") != "Bearer s3cret" || str(t, st, "status") != "ok" || !isNull(t, st, "site") {
		t.Fatalf("state %v", st)
	}
	id := str(t, st, "id")

	api.resetCalls()
	st = r.update(st, r.config(drain(map[string]any{"enabled": false})))
	api.expectBody("PATCH", "/v1/serverless/log-drains/"+id, `{"enabled":false}`)
	if str(t, st, "status") != "off" {
		t.Fatalf("status %s", str(t, st, "status"))
	}

	// Stopped after failures elsewhere: the configuration turns it back on.
	api.drains[id].drain.Status = "stopped"
	api.resetCalls()
	st = r.update(r.read(st), r.config(drain(map[string]any{"enabled": true})))
	api.expectBody("PATCH", "/v1/serverless/log-drains/"+id, `{"enabled":true}`)

	for attr, v := range map[string]any{"token": "Bearer other", "url": "https://logs.example.com/other"} {
		p := r.plan(st, r.config(drain(map[string]any{attr: v})))
		if strings.Join(p.requiresReplace, ",") != `AttributeName("`+attr+`")` {
			t.Fatalf("a new %s requires replace, got %v", attr, p.requiresReplace)
		}
	}

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if len(api.callsTo("DELETE", "/v1/serverless/log-drains/"+id)) != 1 {
		t.Fatalf("calls %v", api.mutations())
	}
	if got := r.read(st); !got.IsNull() {
		t.Fatalf("a removed drain stayed: %v", got)
	}
	failOnDiags(t, "destroy again", r.destroy(st))
}

func TestLogDrainCreatedOff(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_log_drain")
	st := r.create(r.config(drain(map[string]any{"enabled": false})))
	id := str(t, st, "id")
	if got := strings.Join(api.mutations(), "\n"); got != "POST /v1/serverless/projects/"+testProjectID+"/log-drains\nPATCH /v1/serverless/log-drains/"+id {
		t.Fatalf("calls\n%s", got)
	}
	if boolean(t, st, "enabled") {
		t.Fatal("still on")
	}
}

func TestLogDrainDatadogSite(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_log_drain")
	st := r.create(r.config(map[string]any{"project_id": testProjectID, "kind": "DATADOG", "token": "dd-key", "site": "eu1"}))
	api.expectBody("POST", "/v1/serverless/projects/"+testProjectID+"/log-drains", `{"kind":"DATADOG","token":"dd-key","site":"eu1"}`)
	if str(t, st, "destination") != "https://http-intake.logs.datadoghq.eu/…logs" {
		t.Fatalf("destination %s", str(t, st, "destination"))
	}
	// Left out, the site is the default, read back from where the logs go.
	st = r.create(r.config(map[string]any{"project_id": testProjectID, "kind": "DATADOG", "token": "dd-key"}))
	if str(t, st, "site") != "us1" {
		t.Fatalf("site %v", attrAt(t, st, "site"))
	}
	p := r.plan(st, r.config(map[string]any{"project_id": testProjectID, "kind": "DATADOG", "token": "dd-key", "site": "us3"}))
	if strings.Join(p.requiresReplace, ",") != `AttributeName("site")` {
		t.Fatalf("requires replace %v", p.requiresReplace)
	}
}

func TestLogDrainImport(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_log_drain")
	created := r.create(r.config(drain(nil)))
	id := str(t, created, "id")

	for _, ref := range []string{id, testProjectID + "/" + id} {
		st, diags := r.importState(ref)
		failOnDiags(t, "import", diags)
		if !isNull(t, st, "url") || !isNull(t, st, "token") || str(t, st, "project_id") != testProjectID {
			t.Fatalf("imported %v", st)
		}
		// What cannot be read back is taken from the configuration, in place.
		api.resetCalls()
		r.update(st, r.config(drain(nil)))
		if got := api.mutations(); len(got) != 0 {
			t.Fatalf("calls %v", got)
		}
	}

	st, diags := r.importState(id)
	failOnDiags(t, "import", diags)
	p := r.plan(st, r.config(drain(map[string]any{"url": "https://elsewhere.example.com/x"})))
	failOnDiags(t, "plan", p.diags)
	_, diags = r.apply(p)
	expectError(t, diags, "The log drain sends to https://logs.example.com/…1234, which does not look like https://elsewhere.example.com/x")

	_, diags = r.importState("0193f0e2-0000-7000-8000-000000000404")
	expectError(t, diags, "No project has the log drain")
}

func TestLogDrainValidation(t *testing.T) {
	api := newFakeAPI(t)
	withProject(api)
	r := newHarness(t, api.URL()).resource("masterdaweb_serverless_log_drain")
	expectError(t, r.validate(r.config(drain(map[string]any{"url": nil}))), "An HTTPS log drain needs url")
	expectError(t, r.validate(r.config(drain(map[string]any{"url": "http://logs.example.com"}))), "Logs are only sent over HTTPS")
	expectError(t, r.validate(r.config(drain(map[string]any{"site": "eu1"}))), "site is Datadog's")
	expectError(t, r.validate(r.config(map[string]any{"project_id": testProjectID, "kind": "DATADOG"})), "the account's API key")
	expectError(t, r.validate(r.config(map[string]any{"project_id": testProjectID, "kind": "DATADOG", "token": "k", "url": "https://x.example.com"})), "Leave url out for Datadog")
	expectError(t, r.validate(r.config(map[string]any{"project_id": testProjectID, "kind": "BETTERSTACK"})), "the source's token")
	failOnDiags(t, "betterstack", r.validate(r.config(map[string]any{"project_id": testProjectID, "kind": "BETTERSTACK", "token": "t"})))

	// A fourth drain: the API's limit is a conflict.
	for i := 0; i < 3; i++ {
		r.create(r.config(drain(nil)))
	}
	p := r.plan(r.null(), r.config(drain(nil)))
	_, diags := r.apply(p)
	expectError(t, diags, "A project sends its logs to at most 3 places. (HTTP 409, SERVERLESS_LOG_DRAIN_LIMIT")
}

func TestDrainDisplay(t *testing.T) {
	for raw, want := range map[string]string{
		"https://logs.example.com/ingest/abcd1234": "https://logs.example.com/…1234",
		"https://Logs.Example.com":                 "https://logs.example.com/",
		"https://logs.example.com:443/x":           "https://logs.example.com/x",
		"https://logs.example.com/a?token=12345":   "https://logs.example.com/…2345",
	} {
		if got, ok := drainDisplay(raw); !ok || got != want {
			t.Errorf("drainDisplay(%q) = %q, want %q", raw, got, want)
		}
	}
}
