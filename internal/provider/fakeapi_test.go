package provider

// An in-memory imitation of the public API (v1) endpoints the provider uses,
// with the behaviour the real one has where it matters to Terraform: what it
// trims and normalises, its defaults, secrets that never come back, shortened
// log drain addresses, two-phase deletes — and its strict validation, which
// refuses a field a body should not carry. Every request is recorded, so
// tests assert the exact paths, methods and bodies.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

const fakeNow = "2026-10-26T12:00:00.000Z"

type apiCall struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
	Key    string
	Auth   string
}

func (c apiCall) String() string {
	s := c.Method + " " + c.Path
	if len(c.Query) > 0 {
		s += "?" + c.Query.Encode()
	}
	return s
}

type fakeVariable struct {
	key, value string
	secret     bool
	build      bool
	version    int64
	updatedAt  string
}

type fakeDrain struct {
	drain  client.LogDrain
	rawURL string
	token  string
	site   string
}

type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	calls []apiCall
	seq   int

	projects         map[string]*client.Project
	projectGoneAfter map[string]int // GETs answered "deleting" before the project is gone
	services         map[string]*client.Service
	serviceGoneAfter map[string]int
	gitConnections   []client.GitConnection
	variables        map[string][]*fakeVariable
	domains          map[string][]*client.Domain
	bindings         map[string][]*client.Binding
	drains           map[string]*fakeDrain
	databases        map[string]*client.Database
	buckets          map[string]*client.Bucket
	outbound         client.OutboundAddresses
	deployments      int
	// withoutRollouts is the API before gradual rollouts: it refuses their
	// settings and does not return them.
	withoutRollouts bool
	// legacyProjects is the API from before /v1/projects: projects are
	// under /v1/serverless/projects only.
	legacyProjects bool
	// The instances, networks, DNS and keys (fakeapi_infra_test.go).
	infra *fakeInfra
	// The Security Center and VPN gateways (fakeapi_security_vpn_test.go).
	sec *fakeSecVPN

	// intercept lets a test answer a request itself; it returns true when
	// it did. It runs before the fake's own handling.
	intercept func(w http.ResponseWriter, r *http.Request, body []byte) bool
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		t:                t,
		projects:         map[string]*client.Project{},
		projectGoneAfter: map[string]int{},
		services:         map[string]*client.Service{},
		serviceGoneAfter: map[string]int{},
		variables:        map[string][]*fakeVariable{},
		domains:          map[string][]*client.Domain{},
		bindings:         map[string][]*client.Binding{},
		drains:           map[string]*fakeDrain{},
		databases:        map[string]*client.Database{},
		buckets:          map[string]*client.Bucket{},
		infra:            newFakeInfra(),
		sec:              newFakeSecVPN(),
		outbound: client.OutboundAddresses{
			Ranges:      []string{"148.224.62.0/27"},
			Addresses:   []string{"148.224.62.21", "148.224.62.23"},
			AddressesV6: []string{"2804:2d00:1::21"},
		},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	// Waits in tests are short: a destroy polls the fake, not the clock.
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
	return f
}

func (f *fakeAPI) URL() string { return f.srv.URL }

func (f *fakeAPI) newID() string {
	f.seq++
	return fmt.Sprintf("0193f0e2-0000-7000-8000-%012d", f.seq)
}

// ── Recorded calls ─────────────────────────────────────────────────────────

// callsTo are the recorded calls with this method whose path is this one.
func (f *fakeAPI) callsTo(method, path string) []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apiCall
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// mutations are the recorded calls that change something, in order.
func (f *fakeAPI) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.Method != http.MethodGet {
			out = append(out, c.String())
		}
	}
	return out
}

func (f *fakeAPI) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// expectBody fails unless the one call to method+path carried this JSON.
func (f *fakeAPI) expectBody(method, path, want string) apiCall {
	f.t.Helper()
	calls := f.callsTo(method, path)
	if len(calls) != 1 {
		f.t.Fatalf("%d calls to %s %s, want 1 (all mutations: %v)", len(calls), method, path, f.mutations())
	}
	assertJSON(f.t, calls[0].Body, want)
	return calls[0]
}

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("body %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("expected body %q is not JSON: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		t.Fatalf("body\n got %s\nwant %s", gb, wb)
	}
}

// ── Answers ────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "req-fake")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func problem(w http.ResponseWriter, status int, reason, detail string, params map[string]any) {
	codes := map[int]string{400: "invalid_request", 402: "insufficient_balance", 403: "forbidden", 404: "not_found",
		409: "conflict", 422: "validation_failed", 429: "rate_limited", 503: "service_unavailable"}
	if params == nil {
		params = map[string]any{}
	}
	body := map[string]any{
		"type": "https://apiv2.masterdaweb.com/v1/errors/" + codes[status], "title": http.StatusText(status),
		"status": status, "detail": detail, "code": codes[status], "request_id": "req-fake",
	}
	if reason != "" {
		body["reason"] = reason
		body["params"] = params
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", "req-fake")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// strict refuses fields a body should not carry, as the API's validation
// does (whitelist, forbidNonWhitelisted).
func strict(w http.ResponseWriter, body map[string]any, allowed ...string) bool {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var bad []any
	for k := range body {
		if !ok[k] {
			bad = append(bad, map[string]any{"field": k, "issues": []string{"property " + k + " should not exist"}})
		}
	}
	if len(bad) > 0 {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(422)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 422, "code": "validation_failed",
			"detail": "One or more fields are invalid.", "errors": bad, "request_id": "req-fake"})
		return false
	}
	return true
}

func notFound(w http.ResponseWriter, reason, what string) {
	problem(w, 404, reason, what+" not found.", nil)
}

// route matches a path against a pattern with :params.
func route(pattern, path string) (map[string]string, bool) {
	ps, xs := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(ps) != len(xs) {
		return nil, false
	}
	params := map[string]string{}
	for i := range ps {
		if strings.HasPrefix(ps[i], ":") {
			v, _ := url.PathUnescape(xs[i])
			params[ps[i][1:]] = v
		} else if ps[i] != xs[i] {
			return nil, false
		}
	}
	return params, true
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, apiCall{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: string(raw),
		Key: r.Header.Get("Idempotency-Key"), Auth: r.Header.Get("Authorization"),
	})
	intercept := f.intercept
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer mwk_test" {
		problem(w, 401, "", "Invalid or expired API token", nil)
		return
	}
	if r.Method != http.MethodGet && r.Header.Get("Idempotency-Key") == "" {
		problem(w, 400, "", "This operation requires an Idempotency-Key header.", nil)
		return
	}
	if intercept != nil && intercept(w, r, raw) {
		return
	}
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			problem(w, 400, "", "The body is not JSON.", nil)
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	for _, rt := range f.routes() {
		if rt.method != r.Method {
			continue
		}
		if params, ok := route(rt.pattern, path); ok {
			rt.handle(w, r, params, body)
			return
		}
	}
	problem(w, 404, "", "Cannot "+r.Method+" "+r.URL.Path, nil)
}

type fakeRoute struct {
	method, pattern string
	handle          func(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any)
}

func (f *fakeAPI) routes() []fakeRoute {
	var projects []fakeRoute
	if !f.legacyProjects {
		// What a project groups is not only services: /v1/projects, the
		// same handlers as the serverless path that stays.
		projects = []fakeRoute{
			{"GET", "projects", f.listProjects},
			{"POST", "projects", f.createProject},
			{"GET", "projects/:id", f.getProject},
			{"PATCH", "projects/:id", f.updateProject},
			{"DELETE", "projects/:id", f.deleteProject},
		}
		projects = append(projects, f.infraRoutes()...)
	} else {
		for _, r := range f.infraRoutes() {
			if !strings.HasPrefix(r.pattern, "projects/") {
				projects = append(projects, r)
			}
		}
	}
	projects = append(projects, f.secVPNRoutes()...)
	return append(projects, []fakeRoute{
		{"GET", "serverless/projects", f.listProjects},
		{"POST", "serverless/projects", f.createProject},
		{"GET", "serverless/projects/:id", f.getProject},
		{"PATCH", "serverless/projects/:id", f.updateProject},
		{"DELETE", "serverless/projects/:id", f.deleteProject},
		{"GET", "serverless/services", f.listServices},
		{"POST", "serverless/projects/:id/services", f.createService},
		{"GET", "serverless/services/:id", f.getService},
		{"PATCH", "serverless/services/:id/settings", f.updateSettings},
		{"PATCH", "serverless/services/:id/source", f.updateSource},
		{"PUT", "serverless/services/:id/deletion-protection", f.protectService},
		{"POST", "serverless/services/:id/deployments", f.deployService},
		{"DELETE", "serverless/services/:id", f.deleteService},
		{"GET", "serverless/git/connections", f.listGitConnections},
		{"GET", "serverless/services/:id/variables", f.listVariables},
		{"PUT", "serverless/services/:id/variables", f.setVariables},
		{"POST", "serverless/services/:id/variables/delete", f.deleteVariables},
		{"GET", "serverless/services/:id/domains", f.listDomains},
		{"POST", "serverless/services/:id/domains", f.addDomain},
		{"DELETE", "serverless/domains/:id", f.deleteDomain},
		{"GET", "serverless/services/:id/bindings", f.listBindings},
		{"POST", "serverless/services/:id/bindings", f.createBinding},
		{"DELETE", "serverless/services/:id/bindings/:bid", f.deleteBinding},
		{"GET", "serverless/projects/:id/log-drains", f.listDrains},
		{"POST", "serverless/projects/:id/log-drains", f.createDrain},
		{"PATCH", "serverless/log-drains/:id", f.updateDrain},
		{"DELETE", "serverless/log-drains/:id", f.deleteDrain},
		{"GET", "serverless/outbound-addresses", f.getOutbound},
		{"GET", "databases/info", f.databasesInfo},
		{"GET", "databases", f.listDatabases},
		{"POST", "databases", f.createDatabase},
		{"GET", "databases/:name", f.getDatabase},
		{"PATCH", "databases/:name", f.updateDatabase},
		{"POST", "databases/:name/resize", f.resizeDatabase},
		{"DELETE", "databases/:name", f.deleteDatabase},
		{"GET", "object-storage/buckets", f.listBuckets},
		{"POST", "object-storage/buckets", f.createBucket},
		{"GET", "object-storage/buckets/:name", f.getBucket},
		{"PATCH", "object-storage/buckets/:name", f.updateBucket},
		{"DELETE", "object-storage/buckets/:name", f.deleteBucket},
	}...)
}

func page(items any) map[string]any {
	return map[string]any{"object": "list", "data": items, "has_more": false, "next_cursor": nil}
}

func optString(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

// trimOrNull is what the API keeps of an optional text: trimmed, "" as null.
func trimOrNull(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func asInt(v any) (int64, bool) {
	n, ok := v.(float64)
	if !ok || n != float64(int64(n)) {
		return 0, false
	}
	return int64(n), true
}

func strList(v any) []string {
	items, _ := v.([]any)
	out := []string{}
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (f *fakeAPI) deployment() *client.Deployment {
	f.deployments++
	return &client.Deployment{ID: f.newID(), Number: int64(f.deployments), Status: "QUEUED", Trigger: "CONFIG_CHANGE"}
}

// ── Projects ────────────────────────────────────────────────────────────────

func (f *fakeAPI) listProjects(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	var out []client.Project
	for _, p := range f.projects {
		out = append(out, f.projectCounts(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) createProject(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description", "deletion_protection", "datacenter_id") {
		return
	}
	name := normalizeProjectName(fmt.Sprint(body["name"]))
	for _, p := range f.projects {
		if p.Name == name {
			problem(w, 409, "SERVERLESS_NAME_TAKEN", fmt.Sprintf("A project named %q already exists.", name), map[string]any{"field": "name"})
			return
		}
	}
	protected := true
	if v, ok := body["deletion_protection"].(bool); ok {
		protected = v
	}
	// Belo Horizonte (3) unless another location is asked for.
	dc, region := fakeLocation(body["datacenter_id"], nil)
	p := &client.Project{ID: f.newID(), Name: name, Description: trimOrNull(body["description"]), Region: region,
		DatacenterID: &dc, DeletionProtection: protected, CreatedAt: fakeNow, UpdatedAt: fakeNow}
	f.projects[p.ID] = p
	writeJSON(w, 201, p)
}

func (f *fakeAPI) getProject(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	pr, ok := f.projects[p["id"]]
	if !ok {
		notFound(w, "SERVERLESS_PROJECT_NOT_FOUND", "Project")
		return
	}
	if pr.Deleting {
		if f.projectGoneAfter[pr.ID] <= 0 {
			delete(f.projects, pr.ID)
			notFound(w, "SERVERLESS_PROJECT_NOT_FOUND", "Project")
			return
		}
		f.projectGoneAfter[pr.ID]--
	}
	writeJSON(w, 200, pr)
}

func (f *fakeAPI) updateProject(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description", "deletion_protection", "deploys_frozen", "frozen_reason") {
		return
	}
	pr, ok := f.projects[p["id"]]
	if !ok {
		notFound(w, "SERVERLESS_PROJECT_NOT_FOUND", "Project")
		return
	}
	if v, ok := body["name"]; ok {
		pr.Name = normalizeProjectName(fmt.Sprint(v))
	}
	if v, ok := body["description"]; ok {
		pr.Description = trimOrNull(v)
	}
	if v, ok := body["deletion_protection"].(bool); ok {
		pr.DeletionProtection = v
	}
	if v, ok := body["deploys_frozen"].(bool); ok {
		pr.DeploysFrozen = v
	}
	writeJSON(w, 200, pr)
}

func (f *fakeAPI) deleteProject(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	pr, ok := f.projects[p["id"]]
	if !ok {
		notFound(w, "SERVERLESS_PROJECT_NOT_FOUND", "Project")
		return
	}
	if pr.DeletionProtection {
		problem(w, 409, "SERVERLESS_DELETION_PROTECTED", "Deletion protection is on for this project. Switch it off first.", nil)
		return
	}
	if r.URL.Query().Get("confirm") != pr.Name {
		problem(w, 400, "SERVERLESS_CONFIRMATION_MISMATCH", "Type the project name exactly to confirm.", nil)
		return
	}
	pr.Deleting = true
	f.projectGoneAfter[pr.ID] = 1
	for _, s := range f.services {
		if s.ProjectID == pr.ID {
			s.Status = "DELETING"
		}
	}
	writeJSON(w, 200, pr)
}

// ── Services ────────────────────────────────────────────────────────────────

var fakeServiceName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

var settingFields = []string{"size", "build_command", "start_command", "release_command", "root_dir", "dockerfile_path",
	"watch_paths", "php_version", "php_server", "php_extensions", "document_root", "node_version", "system_packages", "port", "health_check_path", "startup_timeout_seconds", "min_instances", "max_instances", "concurrency",
	"idle_timeout_seconds", "request_timeout_seconds", "graceful_shutdown_seconds", "rollout_percent", "rollout_minutes",
	"instances", "cron_schedule", "cron_timezone", "cron_timeout_seconds"}

func defaultFakeSettings() client.ServiceSettings {
	small, minutes := "small", int64(10)
	return client.ServiceSettings{Size: &small, VCPUMillis: 500, MemoryMiB: 1024, RootDir: "", WatchPaths: []string{},
		Port: 8080, StartupTimeoutSeconds: 300, MinInstances: 0, MaxInstances: 3, Concurrency: 80,
		IdleTimeoutSeconds: 300, RequestTimeoutSeconds: 100, GracefulShutdownSeconds: 30, RolloutMinutes: &minutes,
		Instances: 1, CronTimezone: "America/Sao_Paulo", CronTimeoutSeconds: 3600}
}

func (f *fakeAPI) settingFields() []string {
	if !f.withoutRollouts {
		return settingFields
	}
	var out []string
	for _, s := range settingFields {
		if !strings.HasPrefix(s, "rollout_") {
			out = append(out, s)
		}
	}
	return out
}

// mergeSettings applies a patch as the API does; it answers the refusal.
func (f *fakeAPI) mergeSettings(w http.ResponseWriter, s *client.ServiceSettings, patch map[string]any, managed []string) bool {
	if !strict(w, patch, f.settingFields()...) {
		return false
	}
	isManaged := func(f string) bool {
		for _, m := range managed {
			if m == f {
				return true
			}
		}
		return false
	}
	for k, v := range patch {
		if isManaged(k) {
			problem(w, 409, "SERVERLESS_SETTING_MANAGED_BY_FILE", "This setting is defined in the repository's mw.toml. Change it there.", map[string]any{"field": k})
			return false
		}
		switch k {
		case "size":
			sz, ok := serviceSizes[fmt.Sprint(v)]
			if !ok {
				problem(w, 400, "SERVERLESS_INVALID_SETTING", fmt.Sprintf("Unknown size %q.", v), map[string]any{"field": "size"})
				return false
			}
			code := fmt.Sprint(v)
			s.Size, s.VCPUMillis, s.MemoryMiB = &code, sz[0], sz[1]
		case "build_command":
			s.BuildCommand = trimOrNull(v)
		case "start_command":
			s.StartCommand = trimOrNull(v)
		case "release_command":
			s.ReleaseCommand = trimOrNull(v)
		case "dockerfile_path":
			s.DockerfilePath = trimOrNull(v)
		case "health_check_path":
			s.HealthCheckPath = trimOrNull(v)
		case "cron_schedule":
			s.CronSchedule = trimOrNull(v)
		case "root_dir":
			s.RootDir, _ = v.(string)
		case "cron_timezone":
			s.CronTimezone = fmt.Sprint(v)
		case "watch_paths":
			s.WatchPaths = strList(v)
		case "php_version":
			s.PHPVersion = trimOrNull(v)
		case "php_server":
			s.PHPServer = trimOrNull(v)
		case "document_root":
			s.DocumentRoot = trimOrNull(v)
		case "node_version":
			s.NodeVersion = trimOrNull(v)
		case "php_extensions":
			s.PHPExtensions = strList(v)
		case "system_packages":
			s.SystemPackages = strList(v)
		case "rollout_percent":
			if v == nil {
				s.RolloutPercent = nil
			} else if n, ok := asInt(v); ok && n >= 0 && n <= 50 {
				s.RolloutPercent = &n
			} else {
				problem(w, 400, "SERVERLESS_INVALID_SETTING", "A new version's first share of the requests (%) must be a whole number between 0 and 50.", map[string]any{"field": k})
				return false
			}
		case "rollout_minutes":
			if n, ok := asInt(v); ok && n >= 1 && n <= 60 {
				s.RolloutMinutes = &n
			} else {
				problem(w, 400, "SERVERLESS_INVALID_SETTING", "The gradual rollout (minutes) must be a whole number between 1 and 60.", map[string]any{"field": k})
				return false
			}
		default:
			n, ok := asInt(v)
			if !ok {
				problem(w, 422, "", "One or more fields are invalid.", nil)
				return false
			}
			*map[string]*int64{
				"port": &s.Port, "startup_timeout_seconds": &s.StartupTimeoutSeconds, "min_instances": &s.MinInstances,
				"max_instances": &s.MaxInstances, "concurrency": &s.Concurrency, "idle_timeout_seconds": &s.IdleTimeoutSeconds,
				"request_timeout_seconds": &s.RequestTimeoutSeconds, "graceful_shutdown_seconds": &s.GracefulShutdownSeconds,
				"instances": &s.Instances, "cron_timeout_seconds": &s.CronTimeoutSeconds,
			}[k] = n
		}
	}
	return true
}

func (f *fakeAPI) resolveSource(w http.ResponseWriter, src map[string]any) (client.ServiceSource, bool) {
	kind := fmt.Sprint(src["kind"])
	if !strict(w, src, "kind", "git", "image") {
		return client.ServiceSource{}, false
	}
	switch kind {
	case "IMAGE":
		img, _ := src["image"].(map[string]any)
		if !strict(w, img, "ref", "registry_credential_id") {
			return client.ServiceSource{}, false
		}
		ref := strings.TrimSpace(fmt.Sprint(img["ref"]))
		if ref == "" || img["ref"] == nil {
			problem(w, 400, "SERVERLESS_INVALID_SOURCE", "Enter an image like ghcr.io/acme/api:1.4 or nginx:1.27.", map[string]any{"field": "image.ref"})
			return client.ServiceSource{}, false
		}
		return client.ServiceSource{Kind: "IMAGE", Image: &client.ImageSource{Ref: ref, RegistryCredentialID: optString(img["registry_credential_id"])}}, true
	case "GIT":
		g, _ := src["git"].(map[string]any)
		gs, ok := f.resolveGit(w, g)
		return client.ServiceSource{Kind: "GIT", Git: gs}, ok
	}
	problem(w, 400, "SERVERLESS_INVALID_SOURCE", "Pick where the code comes from.", map[string]any{"field": "source"})
	return client.ServiceSource{}, false
}

func (f *fakeAPI) resolveGit(w http.ResponseWriter, g map[string]any) (*client.GitSource, bool) {
	if !strict(w, g, "provider", "connection_id", "repo_full_name", "repo_url", "branch", "tag_pattern") {
		return nil, false
	}
	branch := trimOrNull(g["branch"])
	out := &client.GitSource{Provider: fmt.Sprint(g["provider"]), TagPattern: trimOrNull(g["tag_pattern"])}
	if out.Provider == "PUBLIC_URL" {
		u := trimOrNull(g["repo_url"])
		if u == nil || !strings.HasPrefix(*u, "https://") {
			problem(w, 400, "SERVERLESS_INVALID_SOURCE", "Enter the repository's public HTTPS address, like https://github.com/acme/api.git.", map[string]any{"field": "git.repo_url"})
			return nil, false
		}
		main := "main"
		if branch == nil {
			branch = &main
		}
		out.RepoURL, out.Branch = u, branch
		return out, true
	}
	conn, repo := optString(g["connection_id"]), trimOrNull(g["repo_full_name"])
	if conn == nil || repo == nil {
		problem(w, 400, "SERVERLESS_INVALID_SOURCE", "Pick a connected repository.", map[string]any{"field": "git.repo_full_name"})
		return nil, false
	}
	found := false
	for _, c := range f.gitConnections {
		found = found || (c.ID == *conn && c.Provider == out.Provider)
	}
	if !found {
		notFound(w, "SERVERLESS_GIT_CONNECTION_NOT_FOUND", "Git connection")
		return nil, false
	}
	// The provider answers with its own casing of owner/name.
	canonical := strings.ToLower(*repo)
	defaultBranch := "trunk"
	if branch == nil {
		branch = &defaultBranch
	}
	out.ConnectionID, out.RepoFullName, out.Branch = conn, &canonical, branch
	return out, true
}

func (f *fakeAPI) deployRefused(projectID string) (string, string) {
	pr := f.projects[projectID]
	switch {
	case pr == nil:
		return "", ""
	case pr.DeploysFrozen:
		return "SERVERLESS_DEPLOYS_FROZEN", "Deployments are frozen for this project. Lift the freeze to deploy."
	case pr.Paused:
		return "SERVERLESS_PROJECT_PAUSED", "The project is paused. Resume it to deploy."
	}
	return "", ""
}

func (f *fakeAPI) createService(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "kind", "source", "settings", "auto_deploy", "wait_for_ci", "deletion_protection",
		"env_group_ids", "variables", "deploy") {
		return
	}
	pr, ok := f.projects[p["id"]]
	if !ok || pr.Deleting {
		notFound(w, "SERVERLESS_PROJECT_NOT_FOUND", "Project")
		return
	}
	name := strings.TrimSpace(fmt.Sprint(body["name"]))
	if !fakeServiceName.MatchString(name) {
		problem(w, 400, "SERVERLESS_INVALID_NAME", "A service name has up to 40 lowercase letters, digits and hyphens, starting with a letter.", map[string]any{"field": "name"})
		return
	}
	for _, s := range f.services {
		if s.ProjectID == pr.ID && s.Name == name {
			problem(w, 409, "SERVERLESS_NAME_TAKEN", fmt.Sprintf("The project already has a service named %q.", name), map[string]any{"field": "name"})
			return
		}
	}
	src, _ := body["source"].(map[string]any)
	source, ok := f.resolveSource(w, src)
	if !ok {
		return
	}
	settings := defaultFakeSettings()
	if f.withoutRollouts {
		settings.RolloutMinutes = nil
	}
	if patch, ok := body["settings"].(map[string]any); ok && !f.mergeSettings(w, &settings, patch, nil) {
		return
	}
	kind := fmt.Sprint(body["kind"])
	s := &client.Service{ID: f.newID(), ProjectID: pr.ID, ProjectName: pr.Name, Name: name, Kind: kind, Status: "NEW",
		Source: source, Settings: settings, FileManagedFields: []string{}, AutoDeploy: true,
		DeletionProtection: false, CreatedAt: fakeNow}
	if v, ok := body["auto_deploy"].(bool); ok {
		s.AutoDeploy = v
	}
	if v, ok := body["wait_for_ci"].(bool); ok {
		s.WaitForCI = v
	}
	if v, ok := body["deletion_protection"].(bool); ok {
		s.DeletionProtection = v
	}
	switch kind {
	case "WEB":
		u := "https://" + name + "-x1y2z3.mwapp.com.br"
		s.URL = &u
		f.domains[s.ID] = []*client.Domain{{ID: f.newID(), Hostname: name + "-x1y2z3.mwapp.com.br", IsDefault: true,
			Status: "ACTIVE", Instructions: []client.DNSInstruction{}, CreatedAt: fakeNow}}
	case "PRIVATE":
		u := "http://" + name
		s.InternalURL = &u
	}
	f.services[s.ID] = s
	var d *client.Deployment
	if body["deploy"] != false {
		if reason, msg := f.deployRefused(pr.ID); reason != "" {
			problem(w, 409, reason, msg, nil)
			return
		}
		s.Status = "DEPLOYING"
		d = f.deployment()
	}
	writeJSON(w, 201, map[string]any{"service": s, "deployment": d})
}

func (f *fakeAPI) listServices(w http.ResponseWriter, r *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.Service{}
	for _, s := range f.services {
		if pid := r.URL.Query().Get("project_id"); pid == "" || s.ProjectID == pid {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) service(w http.ResponseWriter, id string) *client.Service {
	s, ok := f.services[id]
	if !ok {
		notFound(w, "SERVERLESS_SERVICE_NOT_FOUND", "Service")
		return nil
	}
	return s
}

func (f *fakeAPI) getService(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	if s.Status == "DELETING" {
		if f.serviceGoneAfter[s.ID] <= 0 {
			delete(f.services, s.ID)
			notFound(w, "SERVERLESS_SERVICE_NOT_FOUND", "Service")
			return
		}
		f.serviceGoneAfter[s.ID]--
	}
	writeJSON(w, 200, s)
}

func (f *fakeAPI) updateSettings(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "settings", "deploy") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	patch, _ := body["settings"].(map[string]any)
	next := s.Settings
	if !f.mergeSettings(w, &next, patch, s.FileManagedFields) {
		return
	}
	s.Settings = next
	var d *client.Deployment
	if body["deploy"] != false && s.Status != "NEW" {
		if reason, msg := f.deployRefused(s.ProjectID); reason != "" {
			problem(w, 409, reason, msg, nil)
			return
		}
		d = f.deployment()
	}
	writeJSON(w, 202, map[string]any{"service": s, "deployment": d})
}

func (f *fakeAPI) updateSource(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "git", "image", "auto_deploy", "wait_for_ci") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	if g, ok := body["git"].(map[string]any); ok && s.Source.Kind == "GIT" {
		merged := map[string]any{}
		cur := s.Source.Git
		set := func(k string, v *string) {
			if v != nil {
				merged[k] = *v
			}
		}
		merged["provider"] = cur.Provider
		set("connection_id", cur.ConnectionID)
		set("repo_full_name", cur.RepoFullName)
		set("repo_url", cur.RepoURL)
		set("branch", cur.Branch)
		set("tag_pattern", cur.TagPattern)
		for k, v := range g {
			merged[k] = v
		}
		for k, v := range merged {
			if v == nil {
				delete(merged, k)
			}
		}
		gs, ok := f.resolveGit(w, merged)
		if !ok {
			return
		}
		s.Source.Git = gs
	}
	if img, ok := body["image"].(map[string]any); ok && s.Source.Kind == "IMAGE" {
		if !strict(w, img, "ref", "registry_credential_id") {
			return
		}
		if ref, ok := img["ref"].(string); ok {
			s.Source.Image.Ref = strings.TrimSpace(ref)
		}
		if v, ok := img["registry_credential_id"]; ok {
			s.Source.Image.RegistryCredentialID = optString(v)
		}
	}
	if v, ok := body["auto_deploy"].(bool); ok {
		s.AutoDeploy = v
	}
	if v, ok := body["wait_for_ci"].(bool); ok {
		s.WaitForCI = v
	}
	writeJSON(w, 200, s)
}

func (f *fakeAPI) protectService(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "enabled") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	s.DeletionProtection, _ = body["enabled"].(bool)
	writeJSON(w, 200, s)
}

func (f *fakeAPI) deployService(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "action", "ref", "no_cache") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	if reason, msg := f.deployRefused(s.ProjectID); reason != "" {
		problem(w, 409, reason, msg, nil)
		return
	}
	d := f.deployment()
	d.Trigger = "MANUAL_LATEST"
	writeJSON(w, 202, d)
}

func (f *fakeAPI) deleteService(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	if s.DeletionProtection {
		problem(w, 409, "SERVERLESS_DELETION_PROTECTED", "Deletion protection is on for this service. Switch it off first.", nil)
		return
	}
	if r.URL.Query().Get("confirm") != s.Name {
		problem(w, 400, "SERVERLESS_CONFIRMATION_MISMATCH", "Type the service name exactly to confirm.", nil)
		return
	}
	s.Status = "DELETING"
	f.serviceGoneAfter[s.ID] = 2
	writeJSON(w, 200, s)
}

func (f *fakeAPI) listGitConnections(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := f.gitConnections
	if out == nil {
		out = []client.GitConnection{}
	}
	writeJSON(w, 200, out)
}

// ── Variables ───────────────────────────────────────────────────────────────

var fakeKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (f *fakeAPI) variableViews(serviceID string) []client.Variable {
	out := []client.Variable{}
	for _, v := range f.variables[serviceID] {
		view := client.Variable{Key: v.key, IsSecret: v.secret, AvailableAtBuild: v.build, Version: v.version, UpdatedAt: v.updatedAt}
		if !v.secret {
			val := v.value
			view.Value = &val
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (f *fakeAPI) listVariables(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.service(w, p["id"]) == nil {
		return
	}
	writeJSON(w, 200, f.variableViews(p["id"]))
}

func (f *fakeAPI) bindingKeys(serviceID string) map[string]bool {
	out := map[string]bool{}
	for _, b := range f.bindings[serviceID] {
		for _, k := range b.Variables {
			out[k] = true
		}
	}
	return out
}

func (f *fakeAPI) setVariables(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "variables", "replace_all", "deploy") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	items, _ := body["variables"].([]any)
	change := map[string][]string{"created": {}, "updated": {}, "removed": {}, "unchanged": {}}
	taken := f.bindingKeys(s.ID)
	for _, it := range items {
		in, _ := it.(map[string]any)
		if !strict(w, in, "key", "value", "is_secret", "available_at_build") {
			return
		}
		key := fmt.Sprint(in["key"])
		if !fakeKey.MatchString(key) || strings.HasPrefix(strings.ToUpper(key), "MW_") || key == "PORT" {
			problem(w, 400, "SERVERLESS_INVALID_VARIABLE", fmt.Sprintf("%q is not a valid variable name.", key), map[string]any{"key": key})
			return
		}
		if taken[key] {
			problem(w, 409, "SERVERLESS_BINDING_CONFLICT", key+" is set by a binding. Change the binding's variable layout or prefix instead.", map[string]any{"keys": []string{key}})
			return
		}
		var cur *fakeVariable
		for _, v := range f.variables[s.ID] {
			if v.key == key {
				cur = v
			}
		}
		value, hasValue := in["value"].(string)
		if cur == nil {
			if !hasValue {
				problem(w, 400, "SERVERLESS_INVALID_VARIABLE", key+" needs a value.", map[string]any{"key": key})
				return
			}
			secret, _ := in["is_secret"].(bool)
			build, _ := in["available_at_build"].(bool)
			f.variables[s.ID] = append(f.variables[s.ID], &fakeVariable{key: key, value: value, secret: secret, build: build, version: 1, updatedAt: fakeNow})
			change["created"] = append(change["created"], key)
			continue
		}
		valueChanged := hasValue && value != cur.value
		nextSecret := cur.secret
		if v, ok := in["is_secret"].(bool); ok && v {
			nextSecret = true // a secret stays a secret
		}
		nextBuild := cur.build
		if v, ok := in["available_at_build"].(bool); ok {
			nextBuild = v
		}
		if !valueChanged && nextSecret == cur.secret && nextBuild == cur.build {
			change["unchanged"] = append(change["unchanged"], key)
			continue
		}
		if valueChanged {
			cur.value = value
			cur.version++
		}
		cur.secret, cur.build, cur.updatedAt = nextSecret, nextBuild, "2026-10-26T12:30:00.000Z"
		change["updated"] = append(change["updated"], key)
	}
	var d *client.Deployment
	changed := len(change["created"])+len(change["updated"]) > 0
	if body["deploy"] != false && changed && s.Status != "NEW" {
		if reason, msg := f.deployRefused(s.ProjectID); reason != "" {
			problem(w, 409, reason, msg, nil)
			return
		}
		d = f.deployment()
	}
	writeJSON(w, 200, map[string]any{"created": change["created"], "updated": change["updated"],
		"removed": change["removed"], "unchanged": change["unchanged"], "deployment": d})
}

func (f *fakeAPI) deleteVariables(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "keys") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	drop := map[string]bool{}
	for _, k := range strList(body["keys"]) {
		drop[k] = true
	}
	var keep []*fakeVariable
	removed := []string{}
	for _, v := range f.variables[s.ID] {
		if drop[v.key] {
			removed = append(removed, v.key)
		} else {
			keep = append(keep, v)
		}
	}
	f.variables[s.ID] = keep
	var d *client.Deployment
	if len(removed) > 0 {
		d = f.deployment()
	}
	writeJSON(w, 200, map[string]any{"created": []string{}, "updated": []string{}, "removed": removed, "unchanged": []string{}, "deployment": d})
}

// ── Domains ─────────────────────────────────────────────────────────────────

func (f *fakeAPI) listDomains(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.service(w, p["id"]) == nil {
		return
	}
	out := []client.Domain{}
	for _, d := range f.domains[p["id"]] {
		out = append(out, *d)
	}
	writeJSON(w, 200, out)
}

func (f *fakeAPI) addDomain(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "hostname") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	if s.Kind != "WEB" {
		problem(w, 400, "SERVERLESS_DOMAIN_NOT_SUPPORTED", "Only web services have a public address, so only they take domains.", nil)
		return
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fmt.Sprint(body["hostname"]))), ".")
	for _, list := range f.domains {
		for _, d := range list {
			if d.Hostname == host {
				problem(w, 409, "SERVERLESS_DOMAIN_TAKEN", "This domain is already in use on the platform.", nil)
				return
			}
		}
	}
	labels := strings.Split(host, ".")
	apex := len(labels) == 2 || (len(labels) == 3 && labels[1] == "com" && labels[2] == "br")
	pending := (*bool)(nil)
	d := &client.Domain{ID: f.newID(), Hostname: host, Status: "PENDING_DNS", Apex: apex, CreatedAt: fakeNow,
		Instructions: []client.DNSInstruction{{Type: "TXT", Name: "_mw-verify." + host, Value: "mw-verify=tok123", Found: pending}}}
	if apex {
		www := "www." + host
		alias, redirect := "alias", "redirect"
		d.RedirectTo = &www
		d.Instructions = append(d.Instructions,
			client.DNSInstruction{Type: "ALIAS", Name: host, Value: "cname.mwapp.com.br", Option: &alias},
			client.DNSInstruction{Type: "A", Name: host, Value: "148.224.62.10", Option: &redirect})
	} else {
		d.Instructions = append(d.Instructions, client.DNSInstruction{Type: "CNAME", Name: host, Value: "cname.mwapp.com.br"})
	}
	f.domains[s.ID] = append(f.domains[s.ID], d)
	writeJSON(w, 201, d)
}

func (f *fakeAPI) deleteDomain(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	for sid, list := range f.domains {
		for i, d := range list {
			if d.ID == p["id"] {
				if d.IsDefault {
					problem(w, 409, "SERVERLESS_DEFAULT_DOMAIN_PERMANENT", "The service's default address cannot be removed.", nil)
					return
				}
				f.domains[sid] = append(list[:i:i], list[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
	}
	notFound(w, "SERVERLESS_DOMAIN_NOT_FOUND", "Domain")
}

// ── Bindings ────────────────────────────────────────────────────────────────

var fakeProfiles = map[string][]string{
	"POSTGRES": {"url", "framework", "libpq", "prefixed", "dotnet"},
	"MYSQL":    {"url", "framework", "prefixed", "dotnet"},
	"REDIS":    {"url", "framework", "prefixed", "dotnet"},
	"BUCKET":   {"prefixed", "framework"},
}

var fakePrefixes = map[string]string{"POSTGRES": "PG_", "MYSQL": "MYSQL_", "REDIS": "REDIS_", "BUCKET": "MW_STORAGE_"}

// fakeBindingKeys are a binding's variable names, sorted as the API sorts them.
func fakeBindingKeys(kind, profile, prefix string) []string {
	keys := unsortedBindingKeys(kind, profile, prefix)
	sort.Strings(keys)
	return keys
}

func unsortedBindingKeys(kind, profile, prefix string) []string {
	switch {
	case kind == "BUCKET" && profile == "framework":
		return []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION", "AWS_BUCKET", "AWS_ENDPOINT"}
	case kind == "BUCKET":
		return []string{prefix + "ENDPOINT", prefix + "REGION", prefix + "BUCKET", prefix + "ACCESS_KEY_ID", prefix + "SECRET_ACCESS_KEY"}
	case profile == "dotnet":
		name := "DefaultConnection"
		if kind == "REDIS" {
			name = "Redis"
		}
		if prefix != "" {
			name = ""
			for _, part := range strings.Split(strings.TrimSuffix(prefix, "_"), "_") {
				name += part[:1] + strings.ToLower(part[1:])
			}
		}
		return []string{"ConnectionStrings__" + name}
	case profile == "url" && kind == "REDIS":
		return []string{prefix + "REDIS_URL"}
	case profile == "url":
		return []string{prefix + "DATABASE_URL"}
	default:
		return []string{prefix + "HOST", prefix + "PORT", prefix + "DATABASE", prefix + "USERNAME", prefix + "PASSWORD"}
	}
}

func (f *fakeAPI) listBindings(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.service(w, p["id"]) == nil {
		return
	}
	out := []client.Binding{}
	for _, b := range f.bindings[p["id"]] {
		out = append(out, *b)
	}
	writeJSON(w, 200, out)
}

func (f *fakeAPI) createBinding(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "resource_kind", "resource", "env_profile", "env_prefix", "access", "deploy") {
		return
	}
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	kind, name := fmt.Sprint(body["resource_kind"]), fmt.Sprint(body["resource"])
	var resourceID string
	if kind == "BUCKET" {
		b, ok := f.buckets[name]
		if !ok {
			notFound(w, "OBJECT_STORAGE_BUCKET_NOT_FOUND", "Bucket")
			return
		}
		resourceID = b.ID
	} else {
		d, ok := f.databases[name]
		engines := map[string][]string{"POSTGRES": {"postgres"}, "MYSQL": {"mysql", "mariadb"}, "REDIS": {"valkey"}}[kind]
		if !ok {
			notFound(w, "DATABASE_NOT_FOUND", "Database")
			return
		}
		fits := false
		for _, e := range engines {
			fits = fits || e == d.Engine
		}
		if !fits {
			problem(w, 400, "DATABASE_INVALID_SETTING", name+" is not that kind of database.", map[string]any{"field": "resource"})
			return
		}
		resourceID = d.ID
		if a, ok := body["access"]; ok && a != "read_write" {
			problem(w, 400, "SERVERLESS_INVALID_SETTING", "A database binding has the owner’s rights.", map[string]any{"field": "access"})
			return
		}
	}
	profile := fakeProfiles[kind][0]
	if v, ok := body["env_profile"].(string); ok {
		profile = v
	}
	prefix := ""
	if profile == "prefixed" {
		prefix = fakePrefixes[kind]
	}
	if v, ok := body["env_prefix"].(string); ok {
		prefix = v
	}
	okProfile := false
	for _, pr := range fakeProfiles[kind] {
		okProfile = okProfile || pr == profile
	}
	if !okProfile || (profile == "prefixed" && prefix == "") {
		problem(w, 400, "SERVERLESS_INVALID_SETTING", "The "+profile+" variable layout does not apply to this resource.", map[string]any{"field": "env_profile"})
		return
	}
	for _, b := range f.bindings[s.ID] {
		if b.ResourceKind == kind && b.ResourceID == resourceID {
			problem(w, 409, "SERVERLESS_BINDING_CONFLICT", "The resource "+name+" is already bound to this service.", map[string]any{"already_bound": true})
			return
		}
	}
	access := "read_write"
	if v, ok := body["access"].(string); ok {
		access = v
	}
	rn := name
	b := &client.Binding{ID: f.newID(), ResourceKind: kind, ResourceID: resourceID, ResourceName: &rn, EnvProfile: profile,
		EnvPrefix: prefix, Access: access, Variables: fakeBindingKeys(kind, profile, prefix), CreatedAt: fakeNow}
	f.bindings[s.ID] = append(f.bindings[s.ID], b)
	var d *client.Deployment
	if body["deploy"] != false {
		if reason, msg := f.deployRefused(s.ProjectID); reason != "" {
			problem(w, 409, reason, msg, nil)
			return
		}
		d = f.deployment()
	}
	writeJSON(w, 200, map[string]any{"binding": b, "deployment": d})
}

func (f *fakeAPI) deleteBinding(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	s := f.service(w, p["id"])
	if s == nil {
		return
	}
	for i, b := range f.bindings[s.ID] {
		if b.ID == p["bid"] {
			f.bindings[s.ID] = append(f.bindings[s.ID][:i:i], f.bindings[s.ID][i+1:]...)
			writeJSON(w, 200, map[string]any{"binding": nil, "deployment": f.deployment()})
			return
		}
	}
	// What the API answers for a binding already gone.
	problem(w, 400, "SERVERLESS_INVALID_SETTING", "No such binding on this service.", map[string]any{"field": "binding_id"})
}

// ── Log drains ──────────────────────────────────────────────────────────────

var fakeDatadog = map[string]string{
	"us1": "https://http-intake.logs.datadoghq.com/api/v2/logs",
	"us3": "https://http-intake.logs.us3.datadoghq.com/api/v2/logs",
	"eu1": "https://http-intake.logs.datadoghq.eu/api/v2/logs",
}

// fakeDisplay is the API's displayUrl: host, and the last four characters.
func fakeDisplay(raw string) string {
	u, _ := url.Parse(raw)
	tail := u.Path
	if tail == "" {
		tail = "/"
	}
	if u.RawQuery != "" {
		tail += "?" + u.RawQuery
	}
	if len(tail) > 8 {
		tail = "/…" + tail[len(tail)-4:]
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + tail
}

func (f *fakeAPI) listDrains(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if _, ok := f.projects[p["id"]]; !ok {
		problem(w, 404, "SERVERLESS_PROJECT_NOT_FOUND", "Project not found.", nil)
		return
	}
	out := []client.LogDrain{}
	for _, d := range f.drains {
		if d.drain.ProjectID == p["id"] {
			out = append(out, d.drain)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, out)
}

func (f *fakeAPI) createDrain(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "kind", "url", "token", "site") {
		return
	}
	if _, ok := f.projects[p["id"]]; !ok {
		problem(w, 404, "SERVERLESS_PROJECT_NOT_FOUND", "Project not found.", nil)
		return
	}
	n := 0
	for _, d := range f.drains {
		if d.drain.ProjectID == p["id"] {
			n++
		}
	}
	if n >= 3 {
		problem(w, 409, "SERVERLESS_LOG_DRAIN_LIMIT", "A project sends its logs to at most 3 places.", map[string]any{"limit": 3})
		return
	}
	kind := fmt.Sprint(body["kind"])
	token := trimOrNull(body["token"])
	var raw string
	site := ""
	switch kind {
	case "DATADOG":
		site = "us1"
		if s, ok := body["site"].(string); ok {
			site = s
		}
		raw = fakeDatadog[site]
		if token == nil {
			problem(w, 400, "SERVERLESS_INVALID_SETTING", "Give the Datadog API key.", map[string]any{"field": "token"})
			return
		}
	case "BETTERSTACK":
		raw = "https://in.logs.betterstack.com"
		if u := trimOrNull(body["url"]); u != nil {
			raw = *u
			if !strings.HasPrefix(raw, "https://") {
				raw = "https://" + raw
			}
		}
	case "HTTPS":
		raw = strings.TrimSpace(fmt.Sprint(body["url"]))
		if !strings.HasPrefix(raw, "https://") {
			problem(w, 400, "SERVERLESS_INVALID_SETTING", "Logs are only sent over HTTPS.", map[string]any{"field": "url"})
			return
		}
	}
	d := &fakeDrain{rawURL: raw, site: site, drain: client.LogDrain{ID: f.newID(), ProjectID: p["id"], Kind: kind,
		URL: fakeDisplay(raw), Enabled: true, Status: "ok", HasToken: token != nil, LinesSent: "0", CreatedAt: fakeNow}}
	if token != nil {
		d.token = *token
	}
	f.drains[d.drain.ID] = d
	writeJSON(w, 201, d.drain)
}

func (f *fakeAPI) updateDrain(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "enabled") {
		return
	}
	d, ok := f.drains[p["id"]]
	if !ok {
		problem(w, 404, "SERVERLESS_LOG_DRAIN_NOT_FOUND", "Log drain not found.", nil)
		return
	}
	d.drain.Enabled, _ = body["enabled"].(bool)
	d.drain.Status = map[bool]string{true: "ok", false: "off"}[d.drain.Enabled]
	writeJSON(w, 200, d.drain)
}

func (f *fakeAPI) deleteDrain(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if _, ok := f.drains[p["id"]]; !ok {
		problem(w, 404, "SERVERLESS_LOG_DRAIN_NOT_FOUND", "Log drain not found.", nil)
		return
	}
	delete(f.drains, p["id"])
	w.WriteHeader(204)
}

func (f *fakeAPI) getOutbound(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	writeJSON(w, 200, f.outbound)
}

// ── Databases ───────────────────────────────────────────────────────────────

var fakeDBSizes = map[string][]client.DatabaseSize{
	"postgres": {{Code: "pg-1-1", CPUs: 1, MemoryMB: 1024, DiskGB: 10}, {Code: "pg-2-4", CPUs: 2, MemoryMB: 4096, DiskGB: 40}},
	"mysql":    {{Code: "my-1-1", CPUs: 1, MemoryMB: 1024, DiskGB: 10}},
	"mariadb":  {{Code: "my-1-1", CPUs: 1, MemoryMB: 1024, DiskGB: 10}},
	"valkey":   {{Code: "kv-1-1", CPUs: 1, MemoryMB: 1024, DiskGB: 10}},
}

var fakeDBVersions = map[string]string{"postgres": "18", "mysql": "8.4", "mariadb": "11.8", "valkey": "9"}

// fakeCIDR normalises an allowed source with the net package (not the
// provider's own code), as the API keeps it.
func fakeCIDR(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return "", false
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String() + "/32", true
		}
		return ip.String() + "/128", true
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return "", false
	}
	return n.String(), true
}

func fakeCIDRs(w http.ResponseWriter, v any) ([]string, bool) {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range strList(v) {
		n, ok := fakeCIDR(c)
		if !ok {
			problem(w, 400, "DATABASE_INVALID_SETTING", c+" is not an address or a network.", map[string]any{"field": "allowed_cidrs"})
			return nil, false
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, true
}

func (f *fakeAPI) databasesInfo(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	var engines []client.DatabaseEngineInfo
	for _, e := range []string{"postgres", "mysql", "mariadb", "valkey"} {
		engines = append(engines, client.DatabaseEngineInfo{Engine: e, Versions: []client.Series{client.Series(fakeDBVersions[e])}, Sizes: fakeDBSizes[e]})
	}
	writeJSON(w, 200, client.DatabasesInfo{Available: true, Engines: engines})
}

func (f *fakeAPI) listDatabases(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.Database{}
	for _, d := range f.databases {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func (f *fakeAPI) createDatabase(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name", "engine", "version", "size", "project_id", "datacenter_id", "allowed_cidrs", "extensions",
		"eviction_policy", "backups", "deletion_protection") {
		return
	}
	name, engine := fmt.Sprint(body["name"]), fmt.Sprint(body["engine"])
	if _, ok := f.databases[name]; ok {
		problem(w, 409, "DATABASE_NAME_TAKEN", "This account already has a database named "+name+".", map[string]any{"name": name})
		return
	}
	var size *client.DatabaseSize
	for _, s := range fakeDBSizes[engine] {
		if s.Code == body["size"] {
			s := s
			size = &s
		}
	}
	if size == nil {
		problem(w, 400, "DATABASE_INVALID_SETTING", fmt.Sprintf("Size %v is not on offer.", body["size"]), map[string]any{"field": "size"})
		return
	}
	cidrs, ok := fakeCIDRs(w, body["allowed_cidrs"])
	if !ok {
		return
	}
	ext := strList(body["extensions"])
	if engine != "postgres" && len(ext) > 0 {
		problem(w, 400, "DATABASE_INVALID_SETTING", "Extensions are for PostgreSQL.", map[string]any{"field": "extensions"})
		return
	}
	sort.Strings(ext)
	id := f.newID()
	host := id + ".db.mwapp.com.br"
	// Its project's location, else the one asked for, else Belo Horizonte.
	var inProject *int64
	if pid := optString(body["project_id"]); pid != nil {
		if pr, ok := f.projects[*pid]; ok {
			inProject = pr.DatacenterID
		}
	}
	dc, _ := fakeLocation(body["datacenter_id"], inProject)
	d := &client.Database{ID: id, Name: name, Engine: engine, Version: client.Series(fakeDBVersions[engine]), Size: *size,
		DatacenterID: &dc,
		Status:       "provisioning", ProjectID: optString(body["project_id"]), AllowedCIDRs: cidrs, Extensions: ext,
		BackupsEnabled: engine != "valkey", DeletionProtection: true, Parameters: map[string]string{},
		Maintenance: client.DatabaseMaintenance{HourUTC: 3}, Disk: client.DatabaseDisk{SizeGB: size.DiskGB}, CreatedAt: fakeNow}
	if v, ok := body["version"].(string); ok {
		d.Version = client.Series(v)
	}
	if v, ok := body["backups"].(bool); ok {
		d.BackupsEnabled = v
	}
	if v, ok := body["deletion_protection"].(bool); ok {
		d.DeletionProtection = v
	}
	if engine == "valkey" {
		policy := "noeviction"
		if v, ok := body["eviction_policy"].(string); ok {
			policy = v
		}
		d.EvictionPolicy = &policy
	}
	switch engine {
	case "postgres":
		pooler, db, direct := int64(6432), "app", "postgresql://app@"+host+":5432/app?sslmode=require"
		d.Connection = client.DatabaseConnection{Host: host, Port: 5432, PoolerPort: &pooler, Database: &db, Username: "app",
			URI: "postgresql://app@" + host + ":6432/app?sslmode=require", DirectURI: &direct}
	case "valkey":
		d.Connection = client.DatabaseConnection{Host: host, Port: 6379, Username: "kv_app", URI: "rediss://kv_app@" + host + ":6379"}
	default:
		db := "app"
		d.Connection = client.DatabaseConnection{Host: host, Port: 3306, Database: &db, Username: "app", URI: "mysql://app@" + host + ":3306/app"}
	}
	f.databases[name] = d
	writeJSON(w, 202, map[string]any{"database": d, "password": "s3cret-owner-password"})
}

func (f *fakeAPI) database(w http.ResponseWriter, name string) *client.Database {
	d, ok := f.databases[name]
	if !ok {
		notFound(w, "DATABASE_NOT_FOUND", "Database")
		return nil
	}
	return d
}

func (f *fakeAPI) getDatabase(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if d := f.database(w, p["name"]); d != nil {
		writeJSON(w, 200, d)
	}
}

func (f *fakeAPI) updateDatabase(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "allowed_cidrs", "extensions", "eviction_policy", "parameters", "backups", "deletion_protection", "project_id",
		"maintenance_day", "maintenance_hour_utc", "disk_autogrow_max_gb") {
		return
	}
	d := f.database(w, p["name"])
	if d == nil {
		return
	}
	if v, ok := body["allowed_cidrs"]; ok {
		cidrs, ok := fakeCIDRs(w, v)
		if !ok {
			return
		}
		d.AllowedCIDRs = cidrs
	}
	if v, ok := body["extensions"]; ok {
		if d.Engine != "postgres" {
			problem(w, 400, "DATABASE_INVALID_SETTING", "Extensions are for PostgreSQL.", map[string]any{"field": "extensions"})
			return
		}
		next := strList(v)
		for _, e := range d.Extensions {
			found := false
			for _, n := range next {
				found = found || n == e
			}
			if !found {
				problem(w, 409, "DATABASE_EXTENSION_REMOVAL", "Extensions are never removed from here ("+e+").", nil)
				return
			}
		}
		sort.Strings(next)
		d.Extensions = next
	}
	if v, ok := body["eviction_policy"].(string); ok {
		d.EvictionPolicy = &v
	}
	if v, ok := body["parameters"].(map[string]any); ok {
		d.Parameters = map[string]string{}
		for k, x := range v {
			d.Parameters[k] = fmt.Sprint(x)
		}
	}
	if v, ok := body["backups"].(bool); ok {
		d.BackupsEnabled = v
	}
	if v, ok := body["deletion_protection"].(bool); ok {
		d.DeletionProtection = v
	}
	if v, ok := body["project_id"]; ok {
		d.ProjectID = optString(v)
	}
	if v, ok := body["maintenance_day"]; ok {
		if v == nil {
			d.Maintenance.Day = nil
		} else if n, ok := asInt(v); ok {
			d.Maintenance.Day = &n
		}
	}
	if v, ok := asInt(body["maintenance_hour_utc"]); ok {
		d.Maintenance.HourUTC = v
	}
	if v, ok := body["disk_autogrow_max_gb"]; ok {
		if v == nil {
			d.Disk.AutogrowMaxGB = nil
		} else if n, ok := asInt(v); ok {
			d.Disk.AutogrowMaxGB = &n
		}
	}
	writeJSON(w, 200, d)
}

func (f *fakeAPI) resizeDatabase(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "size", "disk_gb", "when") {
		return
	}
	d := f.database(w, p["name"])
	if d == nil {
		return
	}
	if d.Status != "running" {
		problem(w, 409, "DATABASE_NOT_READY", "The database is not running yet.", map[string]any{"status": d.Status})
		return
	}
	if code, ok := body["size"].(string); ok {
		for _, s := range fakeDBSizes[d.Engine] {
			if s.Code == code {
				d.Size = s
				if s.DiskGB > d.Disk.SizeGB {
					d.Disk.SizeGB = s.DiskGB
				}
			}
		}
	}
	if n, ok := asInt(body["disk_gb"]); ok {
		if n < d.Disk.SizeGB {
			problem(w, 400, "DATABASE_INVALID_SETTING", "The disk never shrinks.", map[string]any{"field": "disk_gb"})
			return
		}
		d.Disk.SizeGB = n
	}
	writeJSON(w, 202, d)
}

func (f *fakeAPI) deleteDatabase(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	d := f.database(w, p["name"])
	if d == nil {
		return
	}
	if d.DeletionProtection {
		problem(w, 409, "DATABASE_DELETION_PROTECTED", "Deletion protection is on for this database. Switch it off first.", nil)
		return
	}
	for _, list := range f.bindings {
		for _, b := range list {
			if b.ResourceID == d.ID {
				problem(w, 409, "DATABASE_IN_USE", "Services are still bound to this database. Unbind them first.", nil)
				return
			}
		}
	}
	if r.URL.Query().Get("confirm") != d.Name {
		problem(w, 400, "DATABASE_CONFIRMATION_MISMATCH", "Type the database name to confirm.", nil)
		return
	}
	delete(f.databases, d.Name)
	writeJSON(w, 200, d)
}

// ── Buckets ─────────────────────────────────────────────────────────────────

func fakeCors(v any) []client.CorsRule {
	items, _ := v.([]any)
	out := []client.CorsRule{}
	for _, it := range items {
		r, _ := it.(map[string]any)
		trim := func(k string) []string {
			out := []string{}
			for _, s := range strList(r[k]) {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
			return out
		}
		var methods []string
		seen := map[string]bool{}
		for _, m := range trim("allowed_methods") {
			m = strings.ToUpper(m)
			if !seen[m] {
				seen[m] = true
				methods = append(methods, m)
			}
		}
		c := client.CorsRule{AllowedOrigins: trim("allowed_origins"), AllowedMethods: methods,
			AllowedHeaders: trim("allowed_headers"), ExposeHeaders: trim("expose_headers")}
		if n, ok := asInt(r["max_age_seconds"]); ok {
			c.MaxAgeSeconds = &n
		}
		out = append(out, c)
	}
	return out
}

func fakeLifecycle(v any) []client.LifecycleRule {
	items, _ := v.([]any)
	out := []client.LifecycleRule{}
	for i, it := range items {
		r, _ := it.(map[string]any)
		id, ok := r["id"].(string)
		if !ok {
			id = fmt.Sprintf("rule-%d", i+1)
		}
		prefix, _ := r["prefix"].(string)
		days, _ := asInt(r["expire_days"])
		out = append(out, client.LifecycleRule{ID: strings.TrimSpace(id), Prefix: prefix, ExpireDays: days, Enabled: r["enabled"] != false})
	}
	return out
}

func (f *fakeAPI) applyBucket(w http.ResponseWriter, b *client.Bucket, body map[string]any) bool {
	if v, ok := body["public"].(bool); ok {
		b.Public = v
		b.PublicURL = nil
		if v {
			u := "https://files.mwapp.com.br/" + b.Name
			b.PublicURL = &u
		}
	}
	if v, ok := body["deletion_protection"].(bool); ok {
		b.DeletionProtection = v
	}
	if v, ok := body["quota_bytes"]; ok {
		if v != nil {
			if _, isString := v.(string); !isString {
				problem(w, 422, "", "One or more fields are invalid.", nil)
				return false
			}
		}
		b.QuotaBytes = optString(v)
	}
	if v, ok := body["quota_mode"].(string); ok {
		b.QuotaMode = v
	}
	if v, ok := body["cors"]; ok {
		b.Cors = fakeCors(v)
	}
	if v, ok := body["lifecycle"]; ok {
		b.Lifecycle = fakeLifecycle(v)
	}
	if v, ok := body["abort_multipart_days"]; ok {
		if v == nil {
			b.AbortMultipartDays = nil
		} else if n, ok := asInt(v); ok {
			b.AbortMultipartDays = &n
		}
	}
	return true
}

func (f *fakeAPI) listBuckets(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.Bucket{}
	for _, b := range f.buckets {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func (f *fakeAPI) createBucket(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name", "public", "deletion_protection", "quota_bytes", "quota_mode", "cors", "lifecycle", "abort_multipart_days") {
		return
	}
	name := strings.TrimSpace(fmt.Sprint(body["name"]))
	if _, ok := f.buckets[name]; ok {
		problem(w, 409, "OBJECT_STORAGE_BUCKET_NAME_TAKEN", "This bucket name is taken. Bucket names are shared by every account, so choose another.", map[string]any{"name": name})
		return
	}
	seven := int64(7)
	b := &client.Bucket{ID: f.newID(), Name: name, Location: "north-america", DeletionProtection: true, QuotaMode: "alert",
		SizeBytes: "0", ObjectCount: "0", Cors: []client.CorsRule{}, Lifecycle: []client.LifecycleRule{},
		AbortMultipartDays: &seven, Status: "active", CreatedAt: fakeNow}
	if !f.applyBucket(w, b, body) {
		return
	}
	f.buckets[name] = b
	writeJSON(w, 201, b)
}

func (f *fakeAPI) bucket(w http.ResponseWriter, name string) *client.Bucket {
	b, ok := f.buckets[name]
	if !ok {
		problem(w, 404, "OBJECT_STORAGE_BUCKET_NOT_FOUND", "No bucket named "+name+" in this account.", map[string]any{"name": name})
		return nil
	}
	return b
}

func (f *fakeAPI) getBucket(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if b := f.bucket(w, p["name"]); b != nil {
		writeJSON(w, 200, b)
	}
}

func (f *fakeAPI) updateBucket(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "public", "deletion_protection", "quota_bytes", "quota_mode", "cors", "lifecycle", "abort_multipart_days") {
		return
	}
	b := f.bucket(w, p["name"])
	if b == nil {
		return
	}
	if b.Status == "deleting" {
		problem(w, 409, "OBJECT_STORAGE_BUCKET_DELETING", "The bucket "+b.Name+" is deleted; restore it first.", nil)
		return
	}
	if f.applyBucket(w, b, body) {
		writeJSON(w, 200, b)
	}
}

func (f *fakeAPI) deleteBucket(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	b := f.bucket(w, p["name"])
	if b == nil {
		return
	}
	if b.DeletionProtection {
		problem(w, 409, "OBJECT_STORAGE_DELETION_PROTECTED", "Deletion protection is on for this bucket. Switch it off first.", nil)
		return
	}
	if r.URL.Query().Get("confirm") != b.Name {
		problem(w, 400, "OBJECT_STORAGE_CONFIRMATION_MISMATCH", "Type the bucket name to confirm.", nil)
		return
	}
	for _, list := range f.bindings {
		for _, x := range list {
			if x.ResourceID == b.ID {
				problem(w, 409, "OBJECT_STORAGE_BUCKET_IN_USE", "Services still use this bucket. Unbind it first.", nil)
				return
			}
		}
	}
	now := fakeNow
	b.Status, b.DeletedAt = "deleting", &now
	writeJSON(w, 200, b)
}

// fakeLocation is where a new project or database runs: the datacenter asked
// for, else its project's, else Belo Horizonte (3, "bh"); Dallas is 4 ("dal").
func fakeLocation(asked any, project *int64) (int64, string) {
	dc := int64(3)
	if project != nil {
		dc = *project
	}
	if v, ok := asked.(float64); ok {
		dc = int64(v)
	}
	if dc == 4 {
		return dc, "dal"
	}
	return dc, "bh"
}
