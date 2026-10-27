// Package client is the Master da Web public API (v1) as the Terraform
// provider uses it: a personal access token, an Idempotency-Key on every
// request that changes something (a retried request is never done twice),
// a wait and a retry when the API asks for one (429, or 503 with
// Retry-After), and the API's RFC 9457 problems turned into errors that say
// what happened.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://apiv2.masterdaweb.com"

// Client talks to /v1 with one token. It is safe for concurrent use.
type Client struct {
	// BaseURL has no trailing slash and no /v1.
	BaseURL   string
	Token     string
	UserAgent string
	HTTP      *http.Client
	// Retries is how many times a request the API refused for its rate (429),
	// or because it is briefly unavailable (503 with Retry-After), is sent
	// again.
	Retries int
	// MaxWait is the longest single wait before a retry; an answer asking for
	// longer is returned as it is.
	MaxWait time.Duration
	// Sleep waits before a retry. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error

	// Where projects live on this API (account.go), found on first use.
	projectsMu   sync.Mutex
	projectsPath string
	// Whether this API lacks the DNS record set PUT (dns.go), found on
	// first use.
	dnsMu          sync.Mutex
	noRecordSetPut bool
}

// New returns a client for a base URL and a token.
func New(baseURL, token, userAgent string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:     token,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		Retries:   5,
		MaxWait:   60 * time.Second,
		Sleep:     sleep,
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// FieldError is one field a request was refused over: by validation, or by
// the service, which names the field it refuses (code says why, when it
// can).
type FieldError struct {
	Field  string   `json:"field"`
	Issues []string `json:"issues"`
	Code   string   `json:"code"`
}

// Problem is an API refusal (RFC 9457 problem details). Code is the generic
// v1 code (not_found, conflict…); Reason is the product's own stable code
// (SERVERLESS_DELETION_PROTECTED, DATABASE_IN_USE…) when there is one.
type Problem struct {
	Status    int            `json:"status"`
	Code      string         `json:"code"`
	Title     string         `json:"title"`
	Detail    string         `json:"detail"`
	Reason    string         `json:"reason"`
	Params    map[string]any `json:"params"`
	RequestID string         `json:"request_id"`
	Errors    []FieldError   `json:"errors"`
	// A refusal for money (402) says what is short, on which subscription.
	ShortfallCents *string `json:"shortfall_cents"`
	SubscriptionID *string `json:"subscription_id"`
	Currency       string  `json:"currency"`
}

func (p *Problem) Error() string {
	msg := strings.TrimSpace(p.Detail)
	if msg == "" {
		msg = p.Title
	}
	if msg == "" {
		msg = http.StatusText(p.Status)
	}
	if p.Status == http.StatusUnauthorized {
		msg = "The API did not accept the token. Check `token` in the provider block (or MW_TOKEN): " +
			"tokens are created in the console under Settings → API tokens."
	}
	// A service's refusal of one field repeats its sentence as the field's
	// issue: the field is named once, not the sentence twice.
	var parts, fields []string
	for _, e := range p.Errors {
		var issues []string
		for _, issue := range e.Issues {
			if strings.TrimSpace(issue) != strings.TrimSpace(p.Detail) {
				issues = append(issues, issue)
			}
		}
		if len(issues) == 0 {
			fields = append(fields, e.Field)
			continue
		}
		parts = append(parts, e.Field+": "+strings.Join(issues, ", "))
	}
	if len(parts) > 0 {
		msg += " " + strings.Join(parts, "; ") + "."
	}
	if p.ShortfallCents != nil {
		msg += " Short by " + FormatCents(*p.ShortfallCents, p.Currency) + "."
	}
	var tags []string
	if p.Status > 0 {
		tags = append(tags, "HTTP "+strconv.Itoa(p.Status))
	}
	for _, f := range fields {
		tags = append(tags, "field "+f)
	}
	if p.Reason != "" {
		tags = append(tags, p.Reason)
	} else if p.Code != "" {
		tags = append(tags, p.Code)
	}
	if p.RequestID != "" {
		tags = append(tags, "request id "+p.RequestID)
	}
	if len(tags) > 0 {
		msg += " (" + strings.Join(tags, ", ") + ")"
	}
	return msg
}

// FormatCents is an amount in cents (a decimal string, as the API sends
// money) as "12.34 BRL".
func FormatCents(cents, currency string) string {
	n, err := strconv.ParseInt(strings.TrimSpace(cents), 10, 64)
	if err != nil {
		return strings.TrimSpace(cents + " cents " + currency)
	}
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	out := fmt.Sprintf("%s%d.%02d", sign, n/100, n%100)
	if currency != "" {
		out += " " + currency
	}
	return out
}

// AsProblem returns the API refusal inside err, if there is one.
func AsProblem(err error) (*Problem, bool) {
	var p *Problem
	if errors.As(err, &p) {
		return p, true
	}
	return nil, false
}

// IsNotFound says whether err is the API answering 404.
func IsNotFound(err error) bool {
	p, ok := AsProblem(err)
	return ok && p.Status == http.StatusNotFound
}

// HasStatus says whether err is the API answering with this status.
func HasStatus(err error, status int) bool {
	p, ok := AsProblem(err)
	return ok && p.Status == status
}

// RouteMissing says whether a refusal is the API not having the route at all
// (an API from before it), rather than a refusal of the request: 405, or a
// 404 from the router ("Cannot PUT …") with no reason of the product's.
func RouteMissing(err error) bool {
	p, ok := AsProblem(err)
	if !ok {
		return false
	}
	if p.Status == http.StatusMethodNotAllowed {
		return true
	}
	return p.Status == http.StatusNotFound && p.Reason == "" && strings.HasPrefix(strings.TrimSpace(p.Detail), "Cannot ")
}

// HasReason says whether err is a refusal with one of these stable codes.
func HasReason(err error, reasons ...string) bool {
	p, ok := AsProblem(err)
	if !ok {
		return false
	}
	for _, r := range reasons {
		if p.Reason == r || p.Code == r {
			return true
		}
	}
	return false
}

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "tf-" + hex.EncodeToString(b)
}

// Do sends one request to /v1 + path. `in` is JSON-encoded when not nil;
// `out` is decoded from a 2xx answer when not nil.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var raw []byte
	if in != nil {
		var err error
		if raw, err = json.Marshal(in); err != nil {
			return err
		}
	}
	u := c.BaseURL + "/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	// A change keeps its key across attempts: the API recognises the retry
	// instead of doing the work twice.
	key := ""
	if method != http.MethodGet && method != http.MethodHead {
		key = newIdempotencyKey()
	}
	for attempt := 0; ; attempt++ {
		var body io.Reader
		if raw != nil {
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return err
		}
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("could not reach the API at %s: %w", c.BaseURL, err)
		}
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		tflog.Debug(ctx, "Master da Web API request", map[string]any{
			"method":     method,
			"path":       path,
			"status":     res.StatusCode,
			"attempt":    attempt + 1,
			"request_id": res.Header.Get("X-Request-Id"),
		})
		if wait, ok := c.retryAfter(method, res, payload, attempt); ok && attempt < c.Retries {
			tflog.Info(ctx, "The API asked to wait before trying again", map[string]any{
				"method": method, "path": path, "status": res.StatusCode, "wait": wait.String(),
			})
			s := c.Sleep
			if s == nil {
				s = sleep
			}
			if err := s(ctx, wait); err != nil {
				return err
			}
			continue
		}
		return answer(res, payload, out)
	}
}

// retryAfter is how long to wait before sending a refused request again, and
// whether to. A rate refusal (429) always: the API's Retry-After, else a
// growing backoff. A briefly unavailable API too — a gateway that could not
// reach it (502, 504), or a 503 that says when, or that is not one of the
// product's own refusals (those carry a stable reason, like "log store
// unavailable", and asking again does not change them). A change carries its
// Idempotency-Key into the retry, so the API never does it twice. A 500 is
// retried only for a read: whether a change behind it was made is not
// knowable from here.
func (c *Client) retryAfter(method string, res *http.Response, body []byte, attempt int) (time.Duration, bool) {
	header := strings.TrimSpace(res.Header.Get("Retry-After"))
	switch res.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusGatewayTimeout:
	case http.StatusServiceUnavailable:
		if header == "" && productRefusal(body) {
			return 0, false
		}
	case http.StatusInternalServerError:
		if method != http.MethodGet && method != http.MethodHead {
			return 0, false
		}
	default:
		return 0, false
	}
	wait := time.Duration(500<<attempt) * time.Millisecond
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		wait = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(header); err == nil {
		wait = time.Until(at)
		if wait < 0 {
			wait = 0
		}
	}
	limit := c.MaxWait
	if limit <= 0 {
		limit = 60 * time.Second
	}
	if wait > limit {
		return 0, false
	}
	return wait, true
}

// productRefusal says whether an answer is one of the product's own refusals:
// a problem with a stable reason.
func productRefusal(body []byte) bool {
	var p struct {
		Reason string `json:"reason"`
	}
	return json.Unmarshal(body, &p) == nil && p.Reason != ""
}

// answer reads a response: the decoded body of a 2xx, else the refusal.
func answer(res *http.Response, raw []byte, out any) error {
	if res.StatusCode >= 300 {
		p := &Problem{}
		if json.Unmarshal(raw, p) != nil || (p.Code == "" && p.Detail == "" && p.Title == "") {
			p = &Problem{Detail: strings.TrimSpace(string(raw))}
		}
		p.Status = res.StatusCode
		if p.RequestID == "" {
			p.RequestID = res.Header.Get("X-Request-Id")
		}
		return p
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer from the API: %w", err)
	}
	return nil
}

// Page is a v1 list.
type Page[T any] struct {
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// All follows a paged list's cursor to the end.
func All[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	out := []T{}
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	for {
		var p Page[T]
		if err := c.Do(ctx, http.MethodGet, path, q, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Data...)
		if !p.HasMore || p.NextCursor == nil || *p.NextCursor == "" {
			return out, nil
		}
		q.Set("cursor", *p.NextCursor)
	}
}
