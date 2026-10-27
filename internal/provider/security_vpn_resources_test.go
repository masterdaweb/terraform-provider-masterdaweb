package provider

import (
	"net/http"
	"strings"
	"testing"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// operationPolls are the GETs of operations: the waits a change made.
func (f *fakeAPI) operationPolls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == "GET" && strings.HasPrefix(c.Path, "/v1/operations/") {
			n++
		}
	}
	return n
}

func TestVPNUserLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	gw := api.seedVPNGateway("office")
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_vpn_user")
	users, apply := "/v1/vpn-gateways/"+gw.ID+"/users", "/v1/vpn-gateways/"+gw.ID+"/apply"

	// A login with a generated password, kept in the state; applied in place
	// and waited for.
	cfg := map[string]any{"gateway_id": gw.ID, "username": "maria", "email": "maria@example.com"}
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	if got := strings.Join(api.mutations(), "\n"); got != "POST "+users+"\nPOST "+apply {
		t.Fatalf("calls:\n%s", got)
	}
	api.expectBody("POST", users, `{"username":"maria","email":"maria@example.com","status":"active"}`)
	api.expectBody("POST", apply, `{}`)
	u := api.sec.users[gw.ID][0]
	if !strings.HasPrefix(u.password, "gen-") || str(t, st, "password") != u.password {
		t.Fatalf("password: state %q, API %q", str(t, st, "password"), u.password)
	}
	if api.operationPolls() == 0 || gw.PendingChanges || len(api.sec.applies) != 1 {
		t.Fatalf("not applied: polls %d, pending %v, applies %v", api.operationPolls(), gw.PendingChanges, api.sec.applies)
	}
	if str(t, st, "status") != "active" || boolean(t, st, "send_credentials") || str(t, st, "created_at") != fakeNow {
		t.Fatalf("state %v", st)
	}

	// Another email, the login switched off and the person told: one change,
	// one apply.
	api.resetCalls()
	cfg["email"], cfg["status"], cfg["send_credentials"] = "m.silva@example.com", "inactive", true
	st = r.update(st, r.config(cfg))
	api.expectBody("PATCH", users+"/"+id, `{"email":"m.silva@example.com","status":"inactive","send_credentials":true}`)
	if len(api.callsTo("POST", apply)) != 1 || u.emailed != 1 || u.user.Status != "inactive" {
		t.Fatalf("applies %d, emailed %d, status %s", len(api.callsTo("POST", apply)), u.emailed, u.user.Status)
	}

	// A password of our own, set in place.
	api.resetCalls()
	cfg["password"] = "correct horse battery"
	st = r.update(st, r.config(cfg))
	api.expectBody("PATCH", users+"/"+id, `{"password":"correct horse battery","send_credentials":true}`)
	if u.password != "correct horse battery" || str(t, st, "password") != "correct horse battery" {
		t.Fatalf("password %q", u.password)
	}
	// Left out again, it stays what it is.
	delete(cfg, "password")
	r.expectNoDiff(st, r.config(cfg))

	// Imported by <gateway_id>/<login_id>: everything but the password.
	imp, diags := r.importState(gw.ID + "/" + id)
	failOnDiags(t, "import", diags)
	if str(t, imp, "username") != "maria" || str(t, imp, "email") != "m.silva@example.com" || !isNull(t, imp, "password") {
		t.Fatalf("imported %v", imp)
	}
	_, diags = r.importState(id)
	expectError(t, diags, "<gateway_id>/<login_id>")

	// Destroyed: removed, and the removal applied.
	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if got := strings.Join(api.mutations(), "\n"); got != "DELETE "+users+"/"+id+"\nPOST "+apply {
		t.Fatalf("calls:\n%s", got)
	}
	if len(api.sec.users[gw.ID]) != 0 {
		t.Fatal("still there")
	}

	// Removed outside Terraform: it leaves the state.
	st = r.create(r.config(map[string]any{"gateway_id": gw.ID, "username": "joao", "email": "joao@example.com"}))
	api.sec.users[gw.ID] = nil
	if !r.read(st).IsNull() {
		t.Fatal("a removed login stayed in the state")
	}
}

func TestVPNUserSavedWhenTheGatewayDoesNotApply(t *testing.T) {
	api := newFakeAPI(t)
	gw := api.seedVPNGateway("office")
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_vpn_user")

	// Applying needs a rebuild, which a login never starts: saved, a warning
	// says how to apply it, and nothing is rebuilt.
	api.sec.needsRebuild[gw.ID] = true
	p := r.plan(r.null(), r.config(map[string]any{"gateway_id": gw.ID, "username": "maria", "email": "maria@example.com"}))
	failOnDiags(t, "plan", p.diags)
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	expectWarning(t, diags, "needs a rebuild")
	if isNull(t, st, "id") || len(api.sec.users[gw.ID]) != 1 || len(api.sec.applies) != 0 || !gw.PendingChanges {
		t.Fatalf("state %v, applies %v", st, api.sec.applies)
	}
	r.expectNoDiff(st, p.config)

	// The apply fails: still saved, said as a warning.
	delete(api.sec.needsRebuild, gw.ID)
	api.sec.failApply = true
	p = r.plan(r.null(), r.config(map[string]any{"gateway_id": gw.ID, "username": "joao", "email": "joao@example.com"}))
	_, diags = r.apply(p)
	failOnDiags(t, "apply", diags)
	expectWarning(t, diags, "did not apply it")
	expectWarning(t, diags, "The gateway did not answer.")

	// A taken username is refused as the API says it, and nothing is saved.
	p = r.plan(r.null(), r.config(map[string]any{"gateway_id": gw.ID, "username": "maria", "email": "other@example.com"}))
	st, diags = r.apply(p)
	expectError(t, diags, "already exists")
	if !st.IsNull() {
		t.Fatalf("saved %v", st)
	}

	// Not a username the API takes, said at validate.
	expectError(t, r.validate(r.config(map[string]any{"gateway_id": gw.ID, "username": "maria.s", "email": "m@example.com"})),
		"letters and digits")
	expectError(t, r.validate(r.config(map[string]any{"gateway_id": "office", "username": "maria", "email": "m@example.com"})),
		"UUID")
}

func TestVPNGatewaySettings(t *testing.T) {
	api := newFakeAPI(t)
	gw := api.seedVPNGateway("office")
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_vpn_gateway_settings")
	gwPath := "/v1/vpn-gateways/" + gw.ID

	// Only what is configured is managed: tcp and a smaller MTU, applied in
	// place; the rest is the gateway's own.
	cfg := map[string]any{"gateway_id": gw.ID, "protocol": "tcp", "mtu": 1380}
	st := r.create(r.config(cfg))
	if got := strings.Join(api.mutations(), "\n"); got != "PATCH "+gwPath+"\nPOST "+gwPath+"/apply" {
		t.Fatalf("calls:\n%s", got)
	}
	api.expectBody("PATCH", gwPath, `{"protocol":"tcp","mtu":1380}`)
	if str(t, st, "name") != "office" || str(t, st, "remote_access_pool") != "10.8.0.0/24" || boolean(t, st, "pending_changes") ||
		str(t, st, "instance_id") != gw.InstanceID || str(t, st, "public_ip") != "203.0.113.50" ||
		str(t, st, "wireguard_public_key") != "wgPublicKey0123456789=" || num(t, st, "wireguard_listen_port") != 51820 ||
		str(t, st, "apply_mode") != "in_place" || boolean(t, st, "allow_rebuild") {
		t.Fatalf("state %v", st)
	}

	// Changes waiting from elsewhere (a login added in the console): the
	// next plan applies them, and only that.
	gw.PendingChanges = true
	refreshed := r.read(st)
	p := r.plan(refreshed, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	if p.noop() {
		t.Fatal("changes waiting on the gateway were left alone")
	}
	api.resetCalls()
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	if got := strings.Join(api.mutations(), "\n"); got != "POST "+gwPath+"/apply" {
		t.Fatalf("calls:\n%s", got)
	}
	st = r.expectNoDiff(st, r.config(cfg))

	// The same pool written another way is the same pool.
	cfg["remote_access_pool"] = "10.8.0.0/24"
	st = r.expectNoDiff(st, r.config(cfg))

	// Routing everything through the gateway needs a rebuild: refused without
	// allow_rebuild, the setting saved…
	cfg["route_all_traffic"] = true
	p = r.plan(st, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	st, diags = r.apply(p)
	expectError(t, diags, "allow_rebuild = true")
	if !gw.RouteAllTraffic || !gw.PendingChanges || !boolean(t, st, "pending_changes") {
		t.Fatalf("gateway %+v, state %v", gw, st)
	}
	// …and rebuilt once allowed.
	cfg["allow_rebuild"] = true
	st = r.update(st, r.config(cfg))
	if last := api.sec.applies[len(api.sec.applies)-1]; last != gw.ID+" rebuild" || gw.PendingChanges {
		t.Fatalf("applies %v", api.sec.applies)
	}

	// A pool overlapping the private network is refused as the API says it.
	bad := map[string]any{}
	for k, v := range cfg {
		bad[k] = v
	}
	bad["remote_access_pool"] = "10.10.10.0/25"
	p = r.plan(st, r.config(bad))
	_, diags = r.apply(p)
	expectError(t, diags, "overlaps the gateway's private network")

	// Imported by the gateway's id; destroying it changes nothing on the
	// gateway.
	imp, diags := r.importState(gw.ID)
	failOnDiags(t, "import", diags)
	if str(t, imp, "gateway_id") != gw.ID || str(t, imp, "protocol") != "tcp" {
		t.Fatalf("imported %v", imp)
	}
	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if len(api.mutations()) != 0 {
		t.Fatalf("destroy called %v", api.mutations())
	}

	// A gateway that is gone leaves the state.
	delete(api.sec.gateways, gw.ID)
	if !r.read(st).IsNull() {
		t.Fatal("a deleted gateway stayed in the state")
	}
}

func TestVPNGatewayDataSources(t *testing.T) {
	api := newFakeAPI(t)
	office := api.seedVPNGateway("office")
	lab := api.seedVPNGateway("lab")
	h := newHarness(t, api.URL())

	st, diags := h.readDataSource("masterdaweb_vpn_gateway", map[string]any{"name": "office"})
	failOnDiags(t, "by name", diags)
	if str(t, st, "id") != office.ID || str(t, st, "public_ip") != "203.0.113.50" || num(t, st, "wireguard_listen_port") != 51820 ||
		str(t, st, "remote_access_pool") != "10.8.0.0/24" || str(t, st, "apply_state") != "idle" || boolean(t, st, "pending_changes") {
		t.Fatalf("office %v", st)
	}
	st, diags = h.readDataSource("masterdaweb_vpn_gateway", map[string]any{"instance_id": lab.InstanceID})
	failOnDiags(t, "by instance", diags)
	if str(t, st, "name") != "lab" {
		t.Fatalf("lab %v", st)
	}
	st, diags = h.readDataSource("masterdaweb_vpn_gateway", map[string]any{"id": lab.ID})
	failOnDiags(t, "by id", diags)
	if str(t, st, "instance_id") != lab.InstanceID {
		t.Fatalf("lab %v", st)
	}
	_, diags = h.readDataSource("masterdaweb_vpn_gateway", map[string]any{"name": "nope"})
	expectError(t, diags, `No VPN gateway is named "nope"`)
	_, diags = h.readDataSource("masterdaweb_vpn_gateway", map[string]any{})
	expectError(t, diags, "Exactly one of these attributes must be configured")
	twin := api.seedVPNGateway("office")
	_, diags = h.readDataSource("masterdaweb_vpn_gateway", map[string]any{"name": "office"})
	expectError(t, diags, twin.ID)

	all, diags := h.readDataSource("masterdaweb_vpn_gateways", map[string]any{})
	failOnDiags(t, "all", diags)
	if len(attrList(t, all, "gateways")) != 3 {
		t.Fatalf("all %v", all)
	}
	some, diags := h.readDataSource("masterdaweb_vpn_gateways", map[string]any{"name_regex": "^la"})
	failOnDiags(t, "regex", diags)
	if len(attrList(t, some, "gateways")) != 1 || str(t, some, "gateways", 0, "name") != "lab" {
		t.Fatalf("some %v", some)
	}
}

const testJustification = "Reachable only by the analytics vendor over its fixed address."

func TestSecurityExceptionLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	web := api.seedInstance("web", "3")
	finding := api.seedFinding("instance", web.inst.ID, "high", "open", "MySQL open", 81)
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_security_exception")

	// A finding accepted until a date the platform allows: kept as written.
	cfg := map[string]any{"scope": "finding", "finding_id": finding.ID, "justification": testJustification,
		"expires_at": "2026-12-01T00:00:00Z"}
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	api.expectBody("POST", "/v1/security/exceptions", `{"scope":"finding","finding_id":"`+finding.ID+`",
		"justification":"`+testJustification+`","expires_at":"2026-12-01T00:00:00Z"}`)
	if str(t, st, "expires_at") != "2026-12-01T00:00:00Z" || str(t, st, "effective_expires_at") != "2026-12-01T00:00:00.000Z" ||
		str(t, st, "instance_id") != web.inst.ID || str(t, st, "status") != "active" || str(t, st, "starts_at") != fakeNow ||
		!isNull(t, st, "resource_type") {
		t.Fatalf("state %v", st)
	}

	// Read one by one, whatever its status.
	if len(api.callsTo("GET", "/v1/security/exceptions/"+id)) == 0 || len(api.callsTo("GET", "/v1/security/exceptions")) != 0 {
		t.Fatalf("reads: %d by id, %d of the list", len(api.callsTo("GET", "/v1/security/exceptions/"+id)),
			len(api.callsTo("GET", "/v1/security/exceptions")))
	}

	// Imported: its finding exactly, and the API's spelling of the date —
	// the configuration's spelling is taken in place, not by replacing the
	// exception.
	imp, diags := r.importState(id)
	failOnDiags(t, "import", diags)
	if str(t, imp, "expires_at") != "2026-12-01T00:00:00.000Z" || str(t, imp, "finding_id") != finding.ID {
		t.Fatalf("imported %v", imp)
	}
	p := r.plan(imp, r.config(cfg))
	failOnDiags(t, "plan after import", p.diags)
	if len(p.requiresReplace) > 0 {
		t.Fatalf("an import plans a replacement of %v", p.requiresReplace)
	}
	api.resetCalls()
	after, diags := r.apply(p)
	failOnDiags(t, "apply after import", diags)
	r.expectNoDiff(after, r.config(cfg))
	if len(api.mutations()) != 0 {
		t.Fatalf("calls %v", api.mutations())
	}

	// Another justification is another exception.
	changed := map[string]any{}
	for k, v := range cfg {
		changed[k] = v
	}
	changed["justification"] = testJustification + " Reviewed again."
	if p := r.plan(st, r.config(changed)); strings.Join(p.requiresReplace, ",") != "AttributeName(\"justification\")" {
		t.Fatalf("requires replace %v", p.requiresReplace)
	}

	// A port, everywhere and both protocols, until past the platform's cap:
	// shorter than asked, and said so.
	port := map[string]any{"scope": "port", "port": 3306, "justification": testJustification, "expires_at": "2027-12-31T23:59:59Z"}
	p = r.plan(r.null(), r.config(port))
	failOnDiags(t, "plan", p.diags)
	pst, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	expectWarning(t, diags, "Shorter than asked")
	if str(t, pst, "effective_expires_at") != "2027-01-24T12:00:00.000Z" || str(t, pst, "expires_at") != "2027-12-31T23:59:59Z" ||
		!isNull(t, pst, "protocol") || num(t, pst, "port") != 3306 || !isNull(t, pst, "instance_id") {
		t.Fatalf("state %v", pst)
	}
	r.expectNoDiff(pst, r.config(port))

	// Revoked in the console: it leaves the state, and the next plan accepts
	// it again.
	api.sec.exceptions[str(t, pst, "id")].Status = "revoked"
	if !r.read(pst).IsNull() {
		t.Fatal("a revoked exception stayed in the state")
	}

	// A check, on one instance.
	rule := r.create(r.config(map[string]any{"scope": "rule", "rule_id": "ssh-password-auth", "instance_id": web.inst.ID,
		"justification": testJustification, "expires_at": "2026-11-30T00:00:00-03:00"}))
	if str(t, rule, "rule_id") != "ssh-password-auth" || str(t, rule, "instance_id") != web.inst.ID {
		t.Fatalf("rule %v", rule)
	}

	// Destroying revokes; a date already past is refused as the API says it.
	failOnDiags(t, "destroy", r.destroy(st))
	if api.sec.exceptions[id].Status != "revoked" {
		t.Fatalf("exception %+v", api.sec.exceptions[id])
	}
	p = r.plan(r.null(), r.config(map[string]any{"scope": "rule", "rule_id": "x", "justification": testJustification,
		"expires_at": "2026-01-01T00:00:00Z"}))
	_, diags = r.apply(p)
	expectError(t, diags, "the expiry date must be in the future")
}

// An API from before GET /v1/security/exceptions/{id}, and before exceptions
// showed their finding: the list is read, and an import takes the
// configuration's finding in place.
func TestSecurityExceptionOnAnOlderAPI(t *testing.T) {
	api := newFakeAPI(t)
	api.sec.noFindingID = true
	api.intercept = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/security/exceptions/") {
			problem(w, 404, "", "Cannot GET "+r.URL.Path, nil)
			return true
		}
		return false
	}
	web := api.seedInstance("web", "3")
	finding := api.seedFinding("instance", web.inst.ID, "high", "open", "MySQL open", 81)
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_security_exception")
	cfg := map[string]any{"scope": "finding", "finding_id": finding.ID, "justification": testJustification,
		"expires_at": "2026-12-01T00:00:00Z"}
	created := r.create(r.config(cfg))
	if str(t, created, "finding_id") != finding.ID {
		t.Fatalf("state %v", created)
	}
	imp, diags := r.importState(str(t, created, "id"))
	failOnDiags(t, "import", diags)
	if !isNull(t, imp, "finding_id") {
		t.Fatalf("imported %v", imp)
	}
	p := r.plan(imp, r.config(cfg))
	failOnDiags(t, "plan after import", p.diags)
	if len(p.requiresReplace) > 0 {
		t.Fatalf("an import plans a replacement of %v", p.requiresReplace)
	}
	after, diags := r.apply(p)
	failOnDiags(t, "apply after import", diags)
	r.expectNoDiff(after, r.config(cfg))

	st := r.create(r.config(map[string]any{"scope": "rule", "rule_id": "ssh-password-auth",
		"justification": testJustification, "expires_at": "2026-12-01T00:00:00Z"}))
	if len(api.callsTo("GET", "/v1/security/exceptions")) == 0 {
		t.Fatal("the list was not read")
	}
	// Lapsed: it leaves the state, and the next plan proposes it again.
	api.sec.exceptions[str(t, st, "id")].Status = "expired"
	if !r.read(st).IsNull() {
		t.Fatal("a lapsed exception stayed in the state")
	}
}

func TestSecurityExceptionValidation(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_security_exception")
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"justification": testJustification, "expires_at": "2026-12-01T00:00:00Z"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	expectError(t, r.validate(r.config(base(map[string]any{"scope": "finding"}))), "Missing finding_id")
	expectError(t, r.validate(r.config(base(map[string]any{"scope": "port", "port": 22,
		"finding_id": "0193f0e2-0000-7000-8000-000000000001"}))), "finding_id is not for an exception of scope port")
	expectError(t, r.validate(r.config(base(map[string]any{"scope": "finding",
		"finding_id": "0193f0e2-0000-7000-8000-000000000001", "instance_id": "4001"}))), "instance_id is not for")
	expectError(t, r.validate(r.config(map[string]any{"scope": "rule", "rule_id": "x", "justification": "too short",
		"expires_at": "2026-12-01T00:00:00Z"})), "string length must be between 30 and 2000")
	expectError(t, r.validate(r.config(base(map[string]any{"scope": "rule", "rule_id": "x", "expires_at": "next week"}))),
		"RFC 3339")
	failOnDiags(t, "a valid port exception", r.validate(r.config(base(map[string]any{"scope": "port", "port": 8443,
		"protocol": "udp", "instance_id": "4001"}))))
}

func TestSecuritySettings(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_security_settings")
	settings := "/v1/security/settings"

	// Scanning turned off, nothing else touched.
	cfg := map[string]any{"enabled": false}
	st := r.create(r.config(cfg))
	api.expectBody("PATCH", settings, `{"enabled":false}`)
	if boolean(t, st, "enabled") || str(t, st, "default_frequency") != "weekly" || !boolean(t, st, "notify_on_new_critical") ||
		str(t, st, "id") != "security_settings" {
		t.Fatalf("state %v", st)
	}

	// On again, monthly.
	api.resetCalls()
	cfg = map[string]any{"enabled": true, "default_frequency": "monthly"}
	st = r.update(st, r.config(cfg))
	api.expectBody("PATCH", settings, `{"enabled":true,"default_frequency":"monthly"}`)

	// Switched off in the console: the next plan switches it on again.
	api.sec.settings.Enabled = false
	if p := r.plan(r.read(st), r.config(cfg)); p.noop() {
		t.Fatal("a setting changed in the console was left alone")
	}
	api.sec.settings.Enabled = true

	// Nothing configured: the settings are read, not written.
	api.resetCalls()
	r.create(r.config(map[string]any{}))
	if len(api.mutations()) != 0 {
		t.Fatalf("calls %v", api.mutations())
	}

	imp, diags := r.importState("account")
	failOnDiags(t, "import", diags)
	if str(t, imp, "id") != "security_settings" || str(t, imp, "default_frequency") != "monthly" {
		t.Fatalf("imported %v", imp)
	}
	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if len(api.mutations()) != 0 || !api.sec.settings.Enabled {
		t.Fatalf("destroy changed the settings: %v", api.mutations())
	}
	expectError(t, r.validate(r.config(map[string]any{"default_frequency": "daily"})), "weekly")
}

func TestSecurityDataSources(t *testing.T) {
	api := newFakeAPI(t)
	web := api.seedInstance("web", "3")
	score, low, grade := int64(72), int64(40), "C"
	scanned := map[string]int64{"scanned": 2, "not_scanned": 1}
	api.sec.overview = client.SecurityOverview{
		Score: &score, Grade: &grade, ScanningAvailable: true,
		OpenFindings: client.SeverityCounts{High: 2, Medium: 1, Info: 4},
		Engines: []client.SecurityEngine{
			{Engine: "posture", Score: &score, Grade: &grade, OpenFindings: client.SeverityCounts{High: 2}},
			{Engine: "image"},
		},
		Instances: client.SecurityInstanceCoverage{Total: 3, Assessed: 1, AwaitingFirstScan: 1, Excluded: 1,
			ExcludedReasons: map[string]int64{"no_public_ip": 1}},
		Resources: []client.SecurityResourceCoverage{
			{Type: "bucket", Total: 2, Checked: 2},
			{Type: "serverless_service", Total: 3, Checked: 3, ImageScans: scanned},
		},
		WorstInstances: []client.SecurityResourceScore{{Type: "instance", ID: web.inst.ID, Score: &low, OpenHigh: 2}},
		WorstResources: []client.SecurityResourceScore{},
	}
	h := newHarness(t, api.URL())
	o, diags := h.readDataSource("masterdaweb_security_overview", map[string]any{})
	failOnDiags(t, "overview", diags)
	if num(t, o, "score") != 72 || num(t, o, "open_findings", "high") != 2 || !isNull(t, o, "engines", 1, "score") ||
		num(t, o, "engines", 0, "open_findings", "high") != 2 || num(t, o, "instances", "excluded_reasons", "no_public_ip") != 1 ||
		!isNull(t, o, "resources", 0, "image_scans") || num(t, o, "resources", 1, "image_scans", "scanned") != 2 ||
		str(t, o, "worst_instances", 0, "id") != web.inst.ID || len(attrList(t, o, "worst_resources")) != 0 ||
		!boolean(t, o, "scanning_available") {
		t.Fatalf("overview %v", o)
	}

	high := api.seedFinding("instance", web.inst.ID, "high", "open", "MySQL open", 81)
	api.seedFinding("bucket", "b-1", "critical", "open", "Public bucket", 95)
	api.seedFinding("instance", web.inst.ID, "low", "resolved", "Old TLS", 10)
	// One per page: the data source follows the cursor.
	api.sec.findingsPage = 1
	all, diags := h.readDataSource("masterdaweb_security_findings", map[string]any{})
	failOnDiags(t, "findings", diags)
	if items := attrList(t, all, "findings"); len(items) != 2 || str(t, all, "findings", 0, "title") != "Public bucket" ||
		str(t, all, "findings", 1, "id") != high.ID || num(t, all, "findings", 1, "port") != 3306 {
		t.Fatalf("findings %v", all)
	}
	if len(api.callsTo("GET", "/v1/security/findings")) != 2 {
		t.Fatalf("calls %v", api.callsTo("GET", "/v1/security/findings"))
	}

	one, diags := h.readDataSource("masterdaweb_security_findings", map[string]any{"severity": "high", "instance_id": web.inst.ID})
	failOnDiags(t, "filtered", diags)
	if len(attrList(t, one, "findings")) != 1 || str(t, one, "findings", 0, "rule_id") != high.RuleID {
		t.Fatalf("filtered %v", one)
	}
	calls := api.callsTo("GET", "/v1/security/findings")
	if q := calls[len(calls)-1].Query; q.Get("severity") != "high" || q.Get("instance_id") != web.inst.ID || q.Get("limit") != "100" {
		t.Fatalf("query %v", q)
	}

	_, diags = h.readDataSource("masterdaweb_security_findings", map[string]any{"resource_id": "b-1"})
	expectError(t, diags, "resource_id needs its kind")
	_, diags = h.readDataSource("masterdaweb_security_findings", map[string]any{"severity": "urgent"})
	expectError(t, diags, "critical")
}
