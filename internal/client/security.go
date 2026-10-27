package client

import (
	"context"
	"net/http"
	"net/url"
)

// ── Security Center ─────────────────────────────────────────────────────────

// SeverityCounts are findings by severity.
type SeverityCounts struct {
	Critical int64 `json:"critical"`
	High     int64 `json:"high"`
	Medium   int64 `json:"medium"`
	Low      int64 `json:"low"`
	Info     int64 `json:"info"`
}

// SecurityEngine is one of the parts the score is made of: the exposure scan
// (nmap), TLS (sslyze), vulnerabilities (nuclei, openvas), configuration
// checks (posture) and container images (image).
type SecurityEngine struct {
	Engine string `json:"engine"`
	// Null until it has run.
	Score        *int64         `json:"score"`
	Grade        *string        `json:"grade"`
	LastScanAt   *string        `json:"last_scan_at"`
	OpenFindings SeverityCounts `json:"open_findings"`
}

// SecurityInstanceCoverage is how many instances the scans reach.
type SecurityInstanceCoverage struct {
	Total             int64            `json:"total"`
	Assessed          int64            `json:"assessed"`
	AwaitingFirstScan int64            `json:"awaiting_first_scan"`
	Excluded          int64            `json:"excluded"`
	OptedOut          int64            `json:"opted_out"`
	ExcludedReasons   map[string]int64 `json:"excluded_reasons"`
}

// SecurityResourceCoverage is how many resources of one kind the
// configuration checks have looked at.
type SecurityResourceCoverage struct {
	Type    string `json:"type"`
	Total   int64  `json:"total"`
	Checked int64  `json:"checked"`
	// Serverless services: container image scans by state.
	ImageScans map[string]int64 `json:"image_scans"`
}

// SecurityResourceScore is a resource among the worst of the account.
type SecurityResourceScore struct {
	Type         string  `json:"type"`
	ID           string  `json:"id"`
	Name         *string `json:"name"`
	Score        *int64  `json:"score"`
	Grade        *string `json:"grade"`
	OpenCritical int64   `json:"open_critical"`
	OpenHigh     int64   `json:"open_high"`
}

// SecurityOverview is the account's security score and what it is made of.
type SecurityOverview struct {
	// 0–100; null before the first check.
	Score      *int64  `json:"score"`
	Grade      *string `json:"grade"`
	LastScanAt *string `json:"last_scan_at"`
	// False while the platform has scanning paused.
	ScanningAvailable bool                       `json:"scanning_available"`
	OpenFindings      SeverityCounts             `json:"open_findings"`
	Engines           []SecurityEngine           `json:"engines"`
	Instances         SecurityInstanceCoverage   `json:"instances"`
	Resources         []SecurityResourceCoverage `json:"resources"`
	WorstInstances    []SecurityResourceScore    `json:"worst_instances"`
	WorstResources    []SecurityResourceScore    `json:"worst_resources"`
}

// SecurityResourceRef is what a finding is about.
type SecurityResourceRef struct {
	// instance, serverless_service, database, bucket, storage_key or account.
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SecurityFinding is something that lowers the score: an exposed service,
// an outdated package, a weak TLS setup, a public bucket…
type SecurityFinding struct {
	ID       string              `json:"id"`
	Resource SecurityResourceRef `json:"resource"`
	// The check that raised it, the same across resources.
	RuleID   string `json:"rule_id"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	// open, resolved, excepted or false_positive.
	Status    string `json:"status"`
	RiskScore int64  `json:"risk_score"`
	// Listed as actively exploited (CISA KEV).
	KnownExploited  bool     `json:"known_exploited"`
	CVEIDs          []string `json:"cve_ids"`
	Port            *int64   `json:"port"`
	Protocol        *string  `json:"protocol"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Impact          string   `json:"impact"`
	Recommendation  string   `json:"recommendation"`
	FirstSeenAt     string   `json:"first_seen_at"`
	LastSeenAt      string   `json:"last_seen_at"`
	LastVerifiedAt  *string  `json:"last_verified_at"`
	ResolvedAt      *string  `json:"resolved_at"`
	OccurrenceCount int64    `json:"occurrence_count"`
}

// SecurityExceptionResource is the resource a finding exception was granted
// for, when it is not an instance.
type SecurityExceptionResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// SecurityException is a risk the account accepted: its findings do not
// lower the score until it lapses or is revoked.
type SecurityException struct {
	ID string `json:"id"`
	// finding, rule or port.
	Scope string `json:"scope"`
	// active, expired or revoked.
	Status string `json:"status"`
	// With scope finding: the finding it was accepted for (null on an API
	// from before it showed it).
	FindingID  *string                    `json:"finding_id"`
	RuleID     *string                    `json:"rule_id"`
	Port       *int64                     `json:"port"`
	Protocol   *string                    `json:"protocol"`
	InstanceID *string                    `json:"instance_id"`
	Resource   *SecurityExceptionResource `json:"resource"`
	// Why the risk is accepted, kept for the audit.
	Justification string `json:"justification"`
	StartsAt      string `json:"starts_at"`
	// When it lapses: the date asked for, capped by the platform's maximum.
	ExpiresAt string  `json:"expires_at"`
	RevokedAt *string `json:"revoked_at"`
}

// SecuritySettings are the account's scanning and notification settings.
type SecuritySettings struct {
	Enabled bool `json:"enabled"`
	// weekly or monthly.
	DefaultFrequency    string `json:"default_frequency"`
	NotifyOnNewCritical bool   `json:"notify_on_new_critical"`
	NotifyOnScoreDrop   bool   `json:"notify_on_score_drop"`
}

// GetSecurityOverview is GET /v1/security/overview.
func (c *Client) GetSecurityOverview(ctx context.Context) (*SecurityOverview, error) {
	var o SecurityOverview
	if err := c.Do(ctx, http.MethodGet, "/security/overview", nil, nil, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListSecurityFindings is GET /v1/security/findings, every page, riskiest
// first. The filters (severity, status, resource_type, resource_id,
// instance_id, category, search) are passed as they are.
func (c *Client) ListSecurityFindings(ctx context.Context, filters url.Values) ([]SecurityFinding, error) {
	q := url.Values{"limit": {"100"}}
	for k, v := range filters {
		q[k] = v
	}
	return All[SecurityFinding](ctx, c, "/security/findings", q)
}

// ListSecurityExceptions is GET /v1/security/exceptions: the active ones, or
// every one (expired and revoked too).
func (c *Client) ListSecurityExceptions(ctx context.Context, includeInactive bool) ([]SecurityException, error) {
	q := url.Values{}
	if includeInactive {
		q.Set("include_inactive", "true")
	}
	var p Page[SecurityException]
	if err := c.Do(ctx, http.MethodGet, "/security/exceptions", q, nil, &p); err != nil {
		return nil, err
	}
	return p.Data, nil
}

// GetSecurityException is GET /v1/security/exceptions/{id}: one exception,
// whatever its status.
func (c *Client) GetSecurityException(ctx context.Context, id string) (*SecurityException, error) {
	var e SecurityException
	if err := c.Do(ctx, http.MethodGet, "/security/exceptions/"+esc(id), nil, nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// CreateSecurityException is POST /v1/security/exceptions.
func (c *Client) CreateSecurityException(ctx context.Context, body map[string]any) (*SecurityException, error) {
	var e SecurityException
	if err := c.Do(ctx, http.MethodPost, "/security/exceptions", nil, body, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// RevokeSecurityException is DELETE /v1/security/exceptions/{id}: its
// findings count again at once. One that already ended is left as it is.
func (c *Client) RevokeSecurityException(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/security/exceptions/"+esc(id), nil, nil, nil)
}

// GetSecuritySettings is GET /v1/security/settings.
func (c *Client) GetSecuritySettings(ctx context.Context) (*SecuritySettings, error) {
	var s SecuritySettings
	if err := c.Do(ctx, http.MethodGet, "/security/settings", nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateSecuritySettings is PATCH /v1/security/settings. A new
// default_frequency applies to every instance at once.
func (c *Client) UpdateSecuritySettings(ctx context.Context, patch map[string]any) (*SecuritySettings, error) {
	var s SecuritySettings
	if err := c.Do(ctx, http.MethodPatch, "/security/settings", nil, patch, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
