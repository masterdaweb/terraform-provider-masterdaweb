package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "mwk_test", "terraform-provider-masterdaweb/test")
	// Known up front: these tests are about requests, not where projects
	// live (TestProjectsLiveWhereTheAPIHasThem is).
	c.projectsPath = "/serverless/projects"
	var waits []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return c, &waits
}

func TestDoSendsTokenJSONAndIdempotencyKey(t *testing.T) {
	var got *http.Request
	var body []byte
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"p1","name":"shop"}`))
	})
	p, err := c.CreateProject(context.Background(), map[string]any{"name": "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "p1" || p.Name != "shop" {
		t.Fatalf("decoded %+v", p)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v1/serverless/projects" {
		t.Fatalf("sent %s %s", got.Method, got.URL.Path)
	}
	if got.Header.Get("Authorization") != "Bearer mwk_test" {
		t.Fatalf("authorization %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type %q", got.Header.Get("Content-Type"))
	}
	if !strings.HasPrefix(got.Header.Get("User-Agent"), "terraform-provider-masterdaweb/") {
		t.Fatalf("user agent %q", got.Header.Get("User-Agent"))
	}
	key := got.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 255 {
		t.Fatalf("idempotency key %q", key)
	}
	if string(body) != `{"name":"shop"}` {
		t.Fatalf("body %s", body)
	}
}

func TestGetHasNoIdempotencyKey(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") != "" {
			t.Errorf("a read carried an idempotency key")
		}
		_, _ = w.Write([]byte(`{"id":"p1"}`))
	})
	if _, err := c.GetProject(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitedRequestIsRetriedWithTheSameKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	c, waits := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		n := len(keys)
		mu.Unlock()
		if n < 3 {
			w.Header().Set("Retry-After", "2")
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":429,"code":"rate_limited","detail":"Rate limit exceeded for write requests. Retry in 2s."}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"p1","name":"shop"}`))
	})
	if _, err := c.CreateProject(context.Background(), map[string]any{"name": "shop"}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("%d attempts, want 3", len(keys))
	}
	if keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("the retries changed the idempotency key: %v", keys)
	}
	if len(*waits) != 2 || (*waits)[0] != 2*time.Second {
		t.Fatalf("waits %v, want two of 2s", *waits)
	}
}

func TestRateLimitWithoutRetryAfterBacksOff(t *testing.T) {
	n := 0
	c, waits := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.GetProject(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 1 || (*waits)[0] != 500*time.Millisecond {
		t.Fatalf("waits %v", *waits)
	}
}

func TestRateLimitGivesUpAfterTheRetries(t *testing.T) {
	n := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":429,"code":"rate_limited","detail":"Rate limit exceeded."}`))
	})
	c.Retries = 2
	_, err := c.GetProject(context.Background(), "p1")
	if !HasStatus(err, http.StatusTooManyRequests) {
		t.Fatalf("err %v", err)
	}
	if n != 3 {
		t.Fatalf("%d attempts, want 3", n)
	}
}

func TestTooLongAWaitIsReturned(t *testing.T) {
	c, waits := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.GetProject(context.Background(), "p1")
	if !HasStatus(err, http.StatusTooManyRequests) || len(*waits) != 0 {
		t.Fatalf("err %v waits %v", err, *waits)
	}
}

func TestUnavailableIsRetriedUnlessItIsAProductRefusal(t *testing.T) {
	// One of the product's own refusals (a reason, no Retry-After): asking
	// again does not change it.
	n := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"code":"service_unavailable","reason":"SERVERLESS_LOGS_UNAVAILABLE","detail":"The log store did not answer."}`))
	})
	if _, err := c.GetProject(context.Background(), "p1"); !HasReason(err, "SERVERLESS_LOGS_UNAVAILABLE") {
		t.Fatalf("err %v", err)
	}
	if n != 1 {
		t.Fatalf("%d attempts at a product refusal, want 1", n)
	}

	// A gateway's 503, with no body to speak of: briefly unavailable.
	n = 0
	c2, waits := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c2.GetProject(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(*waits) != 2 || (*waits)[0] != 500*time.Millisecond || (*waits)[1] != time.Second {
		t.Fatalf("%d attempts, waits %v", n, *waits)
	}

	// A 503 that says when is retried after that long.
	n = 0
	c3, waits3 := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c3.GetProject(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if n != 2 || (*waits3)[0] != time.Second {
		t.Fatalf("%d attempts, waits %v", n, *waits3)
	}
}

func TestGatewayErrorsAreRetriedWithTheSameKey(t *testing.T) {
	var keys []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		switch len(keys) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.WriteHeader(http.StatusGatewayTimeout)
		default:
			_, _ = w.Write([]byte(`{"id":"p1","name":"shop"}`))
		}
	})
	c.projectsPath = "/projects"
	p, err := c.CreateProject(context.Background(), map[string]any{"name": "shop"})
	if err != nil || p.ID != "p1" {
		t.Fatalf("project %+v err %v", p, err)
	}
	if len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("keys %v", keys)
	}
}

func TestServerErrorIsRetriedOnlyForAReading(t *testing.T) {
	n := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.Method == http.MethodGet && n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	c.projectsPath = "/projects"
	if _, err := c.GetProject(context.Background(), "p1"); err != nil || n != 2 {
		t.Fatalf("a read: %d attempts, err %v", n, err)
	}
	n = 0
	if _, err := c.CreateProject(context.Background(), map[string]any{"name": "shop"}); !HasStatus(err, http.StatusInternalServerError) || n != 1 {
		t.Fatalf("a change: %d attempts, err %v", n, err)
	}
}

func TestProblemIsParsed(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("X-Request-Id", "req-header")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"https://apiv2.masterdaweb.com/v1/errors/conflict","title":"Conflict","status":409,
			"detail":"Deletion protection is on for this project. Switch it off first.","code":"conflict",
			"request_id":"req-1","reason":"SERVERLESS_DELETION_PROTECTED","params":{}}`))
	})
	err := c.DeleteProject(context.Background(), "p1", "shop")
	p, ok := AsProblem(err)
	if !ok {
		t.Fatalf("not a problem: %v", err)
	}
	if p.Status != 409 || p.Reason != "SERVERLESS_DELETION_PROTECTED" || p.RequestID != "req-1" {
		t.Fatalf("parsed %+v", p)
	}
	if !HasReason(err, "SERVERLESS_DELETION_PROTECTED") || !HasStatus(err, 409) || IsNotFound(err) {
		t.Fatal("helpers disagree with the problem")
	}
	want := "Deletion protection is on for this project. Switch it off first. (HTTP 409, SERVERLESS_DELETION_PROTECTED, request id req-1)"
	if err.Error() != want {
		t.Fatalf("message\n got %q\nwant %q", err.Error(), want)
	}
}

func TestValidationProblemNamesTheFields(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":422,"code":"validation_failed","detail":"One or more fields are invalid.",
			"errors":[{"field":"settings.port","issues":["port must be an integer number"]}]}`))
	})
	_, err := c.UpdateServiceSettings(context.Background(), "s1", map[string]any{"port": "x"}, true)
	if err == nil || !strings.Contains(err.Error(), "settings.port: port must be an integer number") {
		t.Fatalf("err %v", err)
	}
}

func TestAServiceFieldRefusalNamesTheFieldOnce(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":400,"code":"invalid_request","detail":"Key type \"ssh-dss\" is not accepted.",
			"errors":[{"field":"public_key","issues":["Key type \"ssh-dss\" is not accepted."],"code":"unsupported_type"}]}`))
	})
	_, err := c.CreateSSHKey(context.Background(), "old", "ssh-dss AAAA")
	want := `Key type "ssh-dss" is not accepted. (HTTP 400, field public_key, invalid_request)`
	if err == nil || err.Error() != want {
		t.Fatalf("message\n got %v\nwant %q", err, want)
	}
	if p, ok := AsProblem(err); !ok || len(p.Errors) != 1 || p.Errors[0].Code != "unsupported_type" {
		t.Fatalf("problem %+v", p)
	}
}

func TestReverseDNSAndSnapshotShapes(t *testing.T) {
	var calls []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/reverse-dns":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"reverse_dns","ip":"2001:db8::10","version":6,"hostname":null,
				"default_hostname":"10.v6.example.net","instance_id":"42","editable":false,"editable_at":"2026-11-01T00:00:00.000Z"}]}`))
		case "POST /v1/reverse-dns/203.0.113.10/reset":
			_, _ = w.Write([]byte(`{"object":"reverse_dns","ip":"203.0.113.10","version":4,"hostname":"h.example.net",
				"default_hostname":"h.example.net","instance_id":"42","editable":true,"editable_at":null}`))
		case "POST /v1/instances/42/snapshots":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"object":"snapshot","id":"0190","instance_id":"42","name":"before","description":null,
				"include_ram":false,"created_at":"2026-10-26T00:00:00.000Z","expires_at":"2026-11-25T00:00:00.000Z"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	list, err := c.ListReverseDNS(ctx)
	if err != nil || len(list) != 1 || list[0].Editable || deref(list[0].EditableAt) == "" || list[0].Version != 6 {
		t.Fatalf("list %+v %v", list, err)
	}
	rr, err := c.ResetReverseDNS(ctx, "203.0.113.10")
	if err != nil || deref(rr.Hostname) != "h.example.net" || !rr.Editable {
		t.Fatalf("reset %+v %v", rr, err)
	}
	snap, err := c.CreateSnapshot(ctx, "42", map[string]any{"name": "before"})
	if err != nil || snap.ID != "0190" || deref(snap.ExpiresAt) == "" {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	want := []string{"GET /v1/reverse-dns", "POST /v1/reverse-dns/203.0.113.10/reset", "POST /v1/instances/42/snapshots"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", calls)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestAMoneyRefusalSaysWhatIsShort(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("a resize without an Idempotency-Key")
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"type":"https://apiv2.masterdaweb.com/v1/errors/insufficient_balance","title":"Insufficient balance",
			"status":402,"code":"insufficient_balance","detail":"The subscription's balance does not cover this resize.",
			"subscription_id":"sub-9","shortfall_cents":"1234","currency":"BRL","next":{"top_up":"POST /v1/subscriptions/sub-9/top-ups"}}`))
	})
	_, err := c.ResizeInstance(context.Background(), "42", map[string]any{"offering_code": "c2m4"})
	p, ok := AsProblem(err)
	if !ok || p.ShortfallCents == nil || *p.ShortfallCents != "1234" || deref(p.SubscriptionID) != "sub-9" {
		t.Fatalf("problem %+v", p)
	}
	if !strings.Contains(err.Error(), "Short by 12.34 BRL.") {
		t.Fatalf("message %q", err.Error())
	}
	for in, want := range map[string]string{"5": "0.05 BRL", "100000": "1000.00 BRL", "-250": "-2.50 BRL", "x": "x cents BRL"} {
		if got := FormatCents(in, "BRL"); got != want {
			t.Errorf("FormatCents(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnInstanceSaysWhichFieldsItHas(t *testing.T) {
	var i Instance
	if err := json.Unmarshal([]byte(`{"id":"42","image":null,"custom_image_id":"0190","subscription_id":"sub-9"}`), &i); err != nil {
		t.Fatal(err)
	}
	if !i.Has("image") || !i.Has("custom_image_id") || i.Has("project_id") || i.Image != nil || deref(i.SubscriptionID) != "sub-9" {
		t.Fatalf("instance %+v", i)
	}
}

func TestNotFoundAndPlainBodies(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-9")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`Cannot GET /v1/serverless/projects/x`))
	})
	_, err := c.GetProject(context.Background(), "x")
	if !IsNotFound(err) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "Cannot GET") || !strings.Contains(err.Error(), "req-9") {
		t.Fatalf("message %q", err.Error())
	}
}

func TestUnauthorizedSaysWhatToDo(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":401,"code":"invalid_token","detail":"Invalid or expired API token"}`))
	})
	_, err := c.GetProject(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "MW_TOKEN") {
		t.Fatalf("err %v", err)
	}
}

func TestPagesAreFollowed(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"a"}],"has_more":true,"next_cursor":"a"}`))
			return
		}
		if r.URL.Query().Get("cursor") != "a" {
			t.Errorf("cursor %q", r.URL.Query().Get("cursor"))
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"b"}],"has_more":false,"next_cursor":null}`))
	})
	all, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "a" || all[1].ID != "b" {
		t.Fatalf("got %+v", all)
	}
}

func TestDeletesConfirmWithTheName(t *testing.T) {
	var got []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("%s %s without an idempotency key", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ctx := context.Background()
	c.projectsPath = "/projects"
	_ = c.DeleteProject(ctx, "p1", "my shop")
	_ = c.DeleteService(ctx, "s1", "web")
	_ = c.DeleteDatabase(ctx, "shop-db")
	_ = c.DeleteBucket(ctx, "shop-files")
	want := []string{
		"DELETE /v1/projects/p1?confirm=my+shop",
		"DELETE /v1/serverless/services/s1?confirm=web",
		"DELETE /v1/databases/shop-db?confirm=shop-db",
		"DELETE /v1/object-storage/buckets/shop-files?confirm=shop-files",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestProjectsLiveWhereTheAPIHasThem(t *testing.T) {
	var got []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/v1/projects" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"code":"not_found","detail":"Cannot GET /v1/projects"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"p1","name":"shop"}`))
	})
	ctx := context.Background()
	c.projectsPath = ""
	if _, err := c.GetProject(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetProject(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	// An API from before /v1/projects: asked once, then the old path.
	want := "GET /v1/projects\nGET /v1/serverless/projects/p1\nGET /v1/serverless/projects/p1"
	if strings.Join(got, "\n") != want {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}

	got = nil
	c2, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path == "/v1/projects" {
			_, _ = w.Write([]byte(`{"object":"list","data":[],"has_more":false,"next_cursor":null}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	c2.projectsPath = ""
	if _, err := c2.ListProjectResources(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := c2.RemoveProjectResource(ctx, "p1", "INSTANCE", "42"); err != nil {
		t.Fatal(err)
	}
	want = "GET /v1/projects?limit=1\nGET /v1/projects/p1/resources?\nDELETE /v1/projects/p1/resources/instance/42?"
	if strings.Join(got, "\n") != want {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

func TestSeriesReadsStringsAndNumbers(t *testing.T) {
	var d struct {
		A Series `json:"a"`
		B Series `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":"8.4","b":18}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.A != "8.4" || d.B != "18" {
		t.Fatalf("got %+v", d)
	}
}

func TestContextCancelStopsTheWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := New(srv.URL, "t", "ua")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.GetProject(ctx, "p1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}
