package provider

// The Security Center and VPN gateway half of the fake API. As in the rest of
// the fake, what the real API normalises is normalised here and its
// validation refuses what it refuses: a VPN login's password is shown once,
// an apply whose changes need a rebuild is refused without rebuild=true, an
// exception's end date is capped, and a revoked or lapsed exception stays in
// the list with its status.

import (
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

type fakeVPNUser struct {
	user     client.VPNUser
	password string
	// Credentials emails sent to the person.
	emailed int
}

type fakeSecVPN struct {
	gateways map[string]*client.VPNGateway
	users    map[string][]*fakeVPNUser
	// Gateways whose pending changes need a rebuild.
	needsRebuild map[string]bool
	// What each apply did: "<gateway> in_place" or "<gateway> rebuild".
	applies []string
	// The next apply's operation fails.
	failApply bool

	exceptions map[string]*client.SecurityException
	// How long an exception may last, in days.
	maxExceptionDays int
	// The API from before exceptions showed their finding.
	noFindingID bool
	settings    client.SecuritySettings
	overview    client.SecurityOverview
	findings    []client.SecurityFinding
	// Findings per page, to make the provider follow the cursor.
	findingsPage int
}

func newFakeSecVPN() *fakeSecVPN {
	return &fakeSecVPN{
		gateways:         map[string]*client.VPNGateway{},
		users:            map[string][]*fakeVPNUser{},
		needsRebuild:     map[string]bool{},
		exceptions:       map[string]*client.SecurityException{},
		maxExceptionDays: 90,
		settings:         client.SecuritySettings{Enabled: true, DefaultFrequency: "weekly", NotifyOnNewCritical: true, NotifyOnScoreDrop: true},
		findingsPage:     100,
	}
}

func (f *fakeAPI) secVPNRoutes() []fakeRoute {
	return []fakeRoute{
		{"GET", "vpn-gateways", f.listVPNGateways},
		{"GET", "vpn-gateways/:id", f.getVPNGateway},
		{"PATCH", "vpn-gateways/:id", f.updateVPNGateway},
		{"POST", "vpn-gateways/:id/apply", f.applyVPNGateway},
		{"GET", "vpn-gateways/:id/users", f.listVPNUsers},
		{"POST", "vpn-gateways/:id/users", f.createVPNUser},
		{"GET", "vpn-gateways/:id/users/:uid", f.getVPNUser},
		{"PATCH", "vpn-gateways/:id/users/:uid", f.updateVPNUser},
		{"DELETE", "vpn-gateways/:id/users/:uid", f.deleteVPNUser},
		{"GET", "security/overview", f.securityOverview},
		{"GET", "security/findings", f.listSecurityFindings},
		{"GET", "security/exceptions", f.listSecurityExceptions},
		{"GET", "security/exceptions/:id", f.getSecurityException},
		{"POST", "security/exceptions", f.createSecurityException},
		{"DELETE", "security/exceptions/:id", f.revokeSecurityException},
		{"GET", "security/settings", f.getSecuritySettings},
		{"PATCH", "security/settings", f.updateSecuritySettings},
	}
}

// ── VPN gateways ────────────────────────────────────────────────────────────

func (f *fakeAPI) gateway(w http.ResponseWriter, id string) *client.VPNGateway {
	g, ok := f.sec.gateways[id]
	if !ok {
		notFound(w, "", "VPN gateway "+id)
		return nil
	}
	return g
}

func (f *fakeAPI) listVPNGateways(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.VPNGateway{}
	for _, g := range f.sec.gateways {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) getVPNGateway(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if g := f.gateway(w, p["id"]); g != nil {
		writeJSON(w, 200, g)
	}
}

// The gateway's private network, which the pool must not overlap.
var fakeGatewayVPC = netip.MustParsePrefix("10.10.10.0/24")

func (f *fakeAPI) updateVPNGateway(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "protocol", "remote_access_pool", "route_all_traffic", "allow_shared_logins", "mtu") {
		return
	}
	g := f.gateway(w, p["id"])
	if g == nil {
		return
	}
	changed := false
	if v, ok := body["protocol"].(string); ok {
		if v != "udp" && v != "tcp" {
			fieldProblem(w, 400, "protocol", "protocol must be udp or tcp.")
			return
		}
		changed = changed || v != g.Protocol
		g.Protocol = v
	}
	if v, ok := body["remote_access_pool"].(string); ok {
		pfx, err := netip.ParsePrefix(v)
		if err != nil || !pfx.Addr().Is4() {
			fieldProblem(w, 400, "remote_access_pool", "remoteAccessPool must be an IPv4 network in CIDR notation, like 10.8.0.0/24")
			return
		}
		if pfx.Overlaps(fakeGatewayVPC) {
			fieldProblem(w, 400, "remote_access_pool", "The pool overlaps the gateway's private network, 10.10.10.0/24.")
			return
		}
		pool := pfx.Masked().String()
		changed = changed || g.RemoteAccessPool == nil || *g.RemoteAccessPool != pool
		g.RemoteAccessPool = &pool
	}
	if v, ok := body["route_all_traffic"].(bool); ok {
		if v != g.RouteAllTraffic {
			changed = true
			// Re-wiring the private network's routing is a rebuild.
			f.sec.needsRebuild[g.ID] = true
			g.ApplyMode = "rebuild"
		}
		g.RouteAllTraffic = v
	}
	if v, ok := body["allow_shared_logins"].(bool); ok {
		changed = changed || v != g.AllowSharedLogins
		g.AllowSharedLogins = v
	}
	if v, ok := body["mtu"]; ok {
		n, isInt := asInt(v)
		if !isInt || n < 576 || n > 1500 {
			fieldProblem(w, 400, "mtu", "mtu must not be less than 576 or greater than 1500")
			return
		}
		changed = changed || n != g.MTU
		g.MTU = n
	}
	if v, ok := body["name"].(string); ok {
		name := strings.TrimSpace(v)
		g.Name = &name
	}
	if changed {
		g.PendingChanges = true
	}
	writeJSON(w, 200, g)
}

func (f *fakeAPI) applyVPNGateway(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "rebuild") {
		return
	}
	g := f.gateway(w, p["id"])
	if g == nil {
		return
	}
	rebuild, _ := body["rebuild"].(bool)
	if f.sec.needsRebuild[g.ID] && !rebuild {
		problem(w, 409, client.VPNGatewayRebuildRequired,
			"Applying these changes rebuilds this gateway: the VPN goes offline for a few minutes and everyone "+
				"connected is disconnected. Confirm the rebuild to go ahead.", nil)
		return
	}
	mode := "in_place"
	op := f.newOperation("vpn_gateway_apply", "vpn_gateway", g.ID)
	if rebuild {
		mode = "rebuild"
		op = f.newOperation("vm_reinstall", "vm", g.InstanceID)
	}
	f.sec.applies = append(f.sec.applies, g.ID+" "+mode)
	if f.sec.failApply {
		f.sec.failApply = false
		why := "The gateway did not answer."
		op.Status, op.Error = "failed", &why
		f.infra.opPolls[op.ID] = 0
		code := "AGENT_UNREACHABLE"
		g.ApplyState, g.LastApplyErrorCode = "failed", &code
	} else {
		g.PendingChanges, g.ApplyMode, g.ApplyState, g.LastApplyErrorCode = false, "in_place", "idle", nil
		at := fakeNow
		g.AppliedAt = &at
		delete(f.sec.needsRebuild, g.ID)
	}
	writeJSON(w, 202, map[string]any{"object": "vpn_gateway_apply", "gateway_id": g.ID, "mode": mode,
		"operation_id": op.ID, "coalesced": false})
}

var fakeVPNUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

func (f *fakeAPI) vpnUser(w http.ResponseWriter, gatewayID, userID string) *fakeVPNUser {
	for _, u := range f.sec.users[gatewayID] {
		if u.user.ID == userID {
			return u
		}
	}
	notFound(w, "", "VPN user "+userID+" on this gateway")
	return nil
}

func (f *fakeAPI) listVPNUsers(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.gateway(w, p["id"]) == nil {
		return
	}
	out := []client.VPNUser{}
	for _, u := range f.sec.users[p["id"]] {
		out = append(out, u.user)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) getVPNUser(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.gateway(w, p["id"]) == nil {
		return
	}
	if u := f.vpnUser(w, p["id"], p["uid"]); u != nil {
		writeJSON(w, 200, u.user)
	}
}

// taken says whether another login of the gateway has this username or
// email.
func (f *fakeAPI) taken(gatewayID, exceptID, username, email string) string {
	for _, u := range f.sec.users[gatewayID] {
		if u.user.ID == exceptID {
			continue
		}
		if username != "" && u.user.Username == username {
			return "User " + username + " already exists for VPN Gateway " + gatewayID
		}
		if email != "" && u.user.Email == email {
			return "Email " + email + " already exists for VPN Gateway " + gatewayID
		}
	}
	return ""
}

func (f *fakeAPI) createVPNUser(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "username", "email", "password", "status", "send_credentials") {
		return
	}
	g := f.gateway(w, p["id"])
	if g == nil {
		return
	}
	username, _ := body["username"].(string)
	email, _ := body["email"].(string)
	if !fakeVPNUsernameRe.MatchString(username) {
		fieldProblem(w, 400, "username", "Username must contain only alphanumeric characters (letters and numbers)")
		return
	}
	if msg := f.taken(g.ID, "", username, email); msg != "" {
		problem(w, 409, "VPN_USER_TAKEN", msg, nil)
		return
	}
	status := "active"
	if s, ok := body["status"].(string); ok {
		status = s
	}
	u := &fakeVPNUser{user: client.VPNUser{ID: f.newID(), GatewayID: g.ID, Username: username, Email: email, Status: status}}
	created := fakeNow
	u.user.CreatedAt, u.user.UpdatedAt = &created, &created
	out := u.user
	if pw, ok := body["password"].(string); ok {
		u.password = pw
	} else {
		u.password = "gen-" + u.user.ID[len(u.user.ID)-8:]
		out.Password = &u.password
	}
	if send, _ := body["send_credentials"].(bool); send {
		u.emailed++
	}
	f.sec.users[g.ID] = append(f.sec.users[g.ID], u)
	g.UsersCount++
	g.PendingChanges = true
	writeJSON(w, 201, out)
}

func (f *fakeAPI) updateVPNUser(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "username", "email", "password", "status", "send_credentials") {
		return
	}
	g := f.gateway(w, p["id"])
	if g == nil {
		return
	}
	u := f.vpnUser(w, g.ID, p["uid"])
	if u == nil {
		return
	}
	username, _ := body["username"].(string)
	email, _ := body["email"].(string)
	if msg := f.taken(g.ID, u.user.ID, username, email); msg != "" {
		problem(w, 409, "VPN_USER_TAKEN", msg, nil)
		return
	}
	if username != "" {
		u.user.Username = username
	}
	if email != "" {
		u.user.Email = email
	}
	if s, ok := body["status"].(string); ok {
		u.user.Status = s
	}
	if pw, ok := body["password"].(string); ok && strings.TrimSpace(pw) != "" {
		u.password = pw
	}
	if send, _ := body["send_credentials"].(bool); send {
		u.emailed++
	}
	g.PendingChanges = true
	writeJSON(w, 200, u.user)
}

func (f *fakeAPI) deleteVPNUser(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	g := f.gateway(w, p["id"])
	if g == nil {
		return
	}
	if f.vpnUser(w, g.ID, p["uid"]) == nil {
		return
	}
	kept := []*fakeVPNUser{}
	for _, u := range f.sec.users[g.ID] {
		if u.user.ID != p["uid"] {
			kept = append(kept, u)
		}
	}
	f.sec.users[g.ID] = kept
	g.UsersCount--
	g.PendingChanges = true
	w.WriteHeader(http.StatusNoContent)
}

// seedVPNGateway is a gateway ordered outside Terraform, built and applied.
func (f *fakeAPI) seedVPNGateway(name string) *client.VPNGateway {
	inst := f.seedInstance(name, "3")
	f.mu.Lock()
	defer f.mu.Unlock()
	pool, ip, key, at := "10.8.0.0/24", "203.0.113.50", "wgPublicKey0123456789=", fakeNow
	location := "São Paulo"
	g := &client.VPNGateway{ID: f.newID(), InstanceID: inst.inst.ID, Name: &name, Location: &location, PublicIP: &ip,
		Running: true, Protocol: "udp", RemoteAccessPool: &pool, MTU: 1420,
		Wireguard: client.VPNGatewayWireguard{PublicKey: &key, ListenPort: 51820}, ApplyMode: "in_place",
		ApplyState: "idle", AppliedAt: &at, IncludedSiteConnections: 1, CreatedAt: &at}
	f.sec.gateways[g.ID] = g
	return g
}

// ── Security Center ─────────────────────────────────────────────────────────

func (f *fakeAPI) securityOverview(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	writeJSON(w, 200, f.sec.overview)
}

func (f *fakeAPI) listSecurityFindings(w http.ResponseWriter, r *http.Request, _ map[string]string, _ map[string]any) {
	q := r.URL.Query()
	if q.Get("resource_id") != "" && (q.Get("resource_type") == "" || q.Get("resource_type") == "instance") {
		problem(w, 422, "", "`resource_id` goes with a `resource_type` other than `instance`; for an instance use `instance_id`.", nil)
		return
	}
	var out []client.SecurityFinding
	for _, fd := range f.sec.findings {
		switch {
		case q.Get("severity") != "" && fd.Severity != q.Get("severity"),
			q.Get("status") != "" && fd.Status != q.Get("status"),
			q.Get("status") == "" && (fd.Status == "resolved" || fd.Status == "excepted"),
			q.Get("resource_type") != "" && fd.Resource.Type != q.Get("resource_type"),
			q.Get("resource_id") != "" && fd.Resource.ID != q.Get("resource_id"),
			q.Get("instance_id") != "" && (fd.Resource.Type != "instance" || fd.Resource.ID != q.Get("instance_id")),
			q.Get("category") != "" && fd.Category != q.Get("category"),
			q.Get("search") != "" && !strings.Contains(strings.ToLower(fd.Title+" "+fd.RuleID), strings.ToLower(q.Get("search"))):
			continue
		}
		out = append(out, fd)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RiskScore > out[j].RiskScore })
	// The cursor is the page, as the API's (risk order is not a key).
	pageNo := 1
	if c := q.Get("cursor"); c != "" {
		pageNo, _ = strconv.Atoi(c)
	}
	size := f.sec.findingsPage
	start := min((pageNo-1)*size, len(out))
	end := min(start+size, len(out))
	items := append([]client.SecurityFinding{}, out[start:end]...)
	var next *string
	if end < len(out) {
		n := strconv.Itoa(pageNo + 1)
		next = &n
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": items, "has_more": next != nil, "next_cursor": next})
}

func (f *fakeAPI) listSecurityExceptions(w http.ResponseWriter, r *http.Request, _ map[string]string, _ map[string]any) {
	all := r.URL.Query().Get("include_inactive") == "true"
	out := []client.SecurityException{}
	for _, e := range f.sec.exceptions {
		if all || e.Status == "active" {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if f.sec.noFindingID {
		for i := range out {
			out[i].FindingID = nil
		}
	}
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) getSecurityException(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	e, ok := f.sec.exceptions[p["id"]]
	if !ok {
		notFound(w, "", "exception")
		return
	}
	out := *e
	if f.sec.noFindingID {
		out.FindingID = nil
	}
	writeJSON(w, 200, out)
}

func (f *fakeAPI) createSecurityException(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "scope", "finding_id", "rule_id", "port", "protocol", "instance_id", "justification", "expires_at") {
		return
	}
	now, _ := time.Parse(time.RFC3339, fakeNow)
	scope, _ := body["scope"].(string)
	justification, _ := body["justification"].(string)
	if n := len(strings.TrimSpace(justification)); n < 30 || n > 2000 {
		fieldProblem(w, 400, "justification", "justification must be longer than or equal to 30 characters")
		return
	}
	expires, err := time.Parse(time.RFC3339, strings.TrimSpace(optStr(body["expires_at"])))
	if err != nil {
		fieldProblem(w, 400, "expires_at", "expires_at must be a valid ISO 8601 date string")
		return
	}
	if !expires.After(now) {
		problem(w, 400, "", "the expiry date must be in the future", nil)
		return
	}
	if ceiling := now.AddDate(0, 0, f.sec.maxExceptionDays); expires.After(ceiling) {
		expires = ceiling
	}
	e := &client.SecurityException{ID: f.newID(), Scope: scope, Status: "active",
		Justification: strings.TrimSpace(justification), StartsAt: fakeNow,
		ExpiresAt: expires.UTC().Format("2006-01-02T15:04:05.000Z")}
	switch scope {
	case "finding":
		id := optStr(body["finding_id"])
		var found *client.SecurityFinding
		for i := range f.sec.findings {
			if f.sec.findings[i].ID == id {
				found = &f.sec.findings[i]
			}
		}
		if found == nil {
			notFound(w, "", "finding")
			return
		}
		if found.Status == "resolved" {
			problem(w, 400, "", "this finding is already resolved", nil)
			return
		}
		if found.Status == "excepted" {
			problem(w, 409, "", "this finding already has an active exception", nil)
			return
		}
		found.Status = "excepted"
		e.FindingID = &found.ID
		if found.Resource.Type == "instance" {
			vm := found.Resource.ID
			e.InstanceID = &vm
		} else {
			e.Resource = &client.SecurityExceptionResource{Type: found.Resource.Type, ID: found.Resource.ID}
		}
	case "rule":
		rule := optStr(body["rule_id"])
		if rule == "" {
			problem(w, 400, "", "a rule exception needs a rule id", nil)
			return
		}
		e.RuleID = &rule
	case "port":
		port, ok := asInt(body["port"])
		if !ok || port < 1 || port > 65535 {
			problem(w, 400, "", "a port exception needs a port between 1 and 65535", nil)
			return
		}
		e.Port = &port
		e.Protocol = optString(body["protocol"])
	default:
		fieldProblem(w, 400, "scope", "scope must be one of the following values: finding, rule, port")
		return
	}
	if vm := optString(body["instance_id"]); vm != nil && scope != "finding" {
		if _, ok := f.infra.instances[*vm]; !ok {
			notFound(w, "", "instance")
			return
		}
		e.InstanceID = vm
	}
	f.sec.exceptions[e.ID] = e
	writeJSON(w, 201, e)
}

func (f *fakeAPI) revokeSecurityException(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	e, ok := f.sec.exceptions[p["id"]]
	if !ok {
		notFound(w, "", "exception")
		return
	}
	if e.Status == "active" {
		at := fakeNow
		e.Status, e.RevokedAt = "revoked", &at
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeAPI) getSecuritySettings(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	writeJSON(w, 200, map[string]any{"object": "security_settings", "enabled": f.sec.settings.Enabled,
		"default_frequency": f.sec.settings.DefaultFrequency, "notify_on_new_critical": f.sec.settings.NotifyOnNewCritical,
		"notify_on_score_drop": f.sec.settings.NotifyOnScoreDrop})
}

func (f *fakeAPI) updateSecuritySettings(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "enabled", "default_frequency", "notify_on_new_critical", "notify_on_score_drop") {
		return
	}
	s := &f.sec.settings
	if v, ok := body["default_frequency"].(string); ok {
		if v != "weekly" && v != "monthly" {
			fieldProblem(w, 400, "default_frequency", "default_frequency must be one of the following values: weekly, monthly")
			return
		}
		s.DefaultFrequency = v
	}
	if v, ok := body["enabled"].(bool); ok {
		s.Enabled = v
	}
	if v, ok := body["notify_on_new_critical"].(bool); ok {
		s.NotifyOnNewCritical = v
	}
	if v, ok := body["notify_on_score_drop"].(bool); ok {
		s.NotifyOnScoreDrop = v
	}
	f.getSecuritySettings(w, r, p, nil)
}

// seedFinding is a finding the scans raised.
func (f *fakeAPI) seedFinding(resourceType, resourceID, severity, status, title string, risk int64) client.SecurityFinding {
	f.mu.Lock()
	defer f.mu.Unlock()
	port := int64(3306)
	tcp := "tcp"
	fd := client.SecurityFinding{ID: f.newID(), Resource: client.SecurityResourceRef{Type: resourceType, ID: resourceID, Name: "res-" + resourceID},
		RuleID: "exposed-" + strings.ToLower(strings.ReplaceAll(title, " ", "-")), Category: "EXPOSED_SERVICE", Severity: severity,
		Status: status, RiskScore: risk, CVEIDs: []string{}, Port: &port, Protocol: &tcp, Title: title,
		Description: "d", Impact: "i", Recommendation: "r", FirstSeenAt: fakeNow, LastSeenAt: fakeNow, OccurrenceCount: 1}
	f.sec.findings = append(f.sec.findings, fd)
	return fd
}

func optStr(v any) string {
	s, _ := v.(string)
	return s
}
