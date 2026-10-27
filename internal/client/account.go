package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// ── Projects: where they live ───────────────────────────────────────────────

// projectsBase is the path projects live under: /projects, now that a
// project groups instances, databases and buckets as well as serverless
// services, or /serverless/projects on an API from before. A list never
// answers 404, so one that does says the path is not there.
func (c *Client) projectsBase(ctx context.Context) string {
	c.projectsMu.Lock()
	defer c.projectsMu.Unlock()
	if c.projectsPath != "" {
		return c.projectsPath
	}
	var p Page[Project]
	err := c.Do(ctx, http.MethodGet, "/projects", url.Values{"limit": {"1"}}, nil, &p)
	switch {
	case err == nil:
		c.projectsPath = "/projects"
	case IsNotFound(err):
		c.projectsPath = "/serverless/projects"
	default:
		// Not known yet (a token without the scope, the API unreachable):
		// the call itself says why; ask again next time.
		return "/projects"
	}
	return c.projectsPath
}

// ProjectResource is an instance, a database or a bucket in a project.
type ProjectResource struct {
	// INSTANCE, BUCKET or DATABASE.
	Kind    string  `json:"kind"`
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Detail  *string `json:"detail"`
	Image   *string `json:"image"`
	AddedAt *string `json:"added_at"`
}

// ListProjectResources is GET …/projects/{id}/resources.
func (c *Client) ListProjectResources(ctx context.Context, projectID string) ([]ProjectResource, error) {
	out := []ProjectResource{}
	if err := c.Do(ctx, http.MethodGet, c.projectsBase(ctx)+"/"+esc(projectID)+"/resources", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddProjectResource is POST …/projects/{id}/resources with one resource:
// moved from the project it was in, if any.
func (c *Client) AddProjectResource(ctx context.Context, projectID, kind, id string) ([]ProjectResource, error) {
	out := []ProjectResource{}
	body := map[string]any{"resources": []map[string]any{{"kind": kind, "id": id}}}
	if err := c.Do(ctx, http.MethodPost, c.projectsBase(ctx)+"/"+esc(projectID)+"/resources", nil, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveProjectResource is DELETE …/projects/{id}/resources/{kind}/{id}.
// The resource itself is not touched.
func (c *Client) RemoveProjectResource(ctx context.Context, projectID, kind, id string) error {
	p := c.projectsBase(ctx) + "/" + esc(projectID) + "/resources/" + esc(strings.ToLower(kind)) + "/" + esc(id)
	return c.Do(ctx, http.MethodDelete, p, nil, nil, nil)
}

// ── SSH keys ────────────────────────────────────────────────────────────────

// SSHKey is a public key put on instances when they are created or
// reinstalled.
type SSHKey struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Fingerprint string  `json:"fingerprint"`
	PublicKey   string  `json:"public_key"`
	CreatedAt   *string `json:"created_at"`
}

// ListSSHKeys is GET /v1/ssh-keys, every page.
func (c *Client) ListSSHKeys(ctx context.Context) ([]SSHKey, error) {
	return All[SSHKey](ctx, c, "/ssh-keys", url.Values{"limit": {"100"}})
}

// GetSSHKey is GET /v1/ssh-keys/{id}.
func (c *Client) GetSSHKey(ctx context.Context, id string) (*SSHKey, error) {
	var k SSHKey
	if err := c.Do(ctx, http.MethodGet, "/ssh-keys/"+esc(id), nil, nil, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

// CreateSSHKey is POST /v1/ssh-keys.
func (c *Client) CreateSSHKey(ctx context.Context, name, publicKey string) (*SSHKey, error) {
	var k SSHKey
	if err := c.Do(ctx, http.MethodPost, "/ssh-keys", nil, map[string]any{"name": name, "public_key": publicKey}, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

// RenameSSHKey is PATCH /v1/ssh-keys/{id}.
func (c *Client) RenameSSHKey(ctx context.Context, id, name string) (*SSHKey, error) {
	var k SSHKey
	if err := c.Do(ctx, http.MethodPatch, "/ssh-keys/"+esc(id), nil, map[string]any{"name": name}, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

// DeleteSSHKey is DELETE /v1/ssh-keys/{id}.
func (c *Client) DeleteSSHKey(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/ssh-keys/"+esc(id), nil, nil, nil)
}

// ── Snapshots ───────────────────────────────────────────────────────────────

// Snapshot is a point an instance can be restored to; it expires 30 days
// after it is taken.
type Snapshot struct {
	ID          string  `json:"id"`
	InstanceID  string  `json:"instance_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	IncludeRAM  bool    `json:"include_ram"`
	CreatedAt   *string `json:"created_at"`
	ExpiresAt   *string `json:"expires_at"`
}

// SnapshotList is an instance's snapshots and how many more it may have.
type SnapshotList struct {
	Data         []Snapshot `json:"data"`
	MaxSnapshots *int64     `json:"max_snapshots"`
	Remaining    *int64     `json:"remaining"`
}

// ListSnapshots is GET /v1/instances/{id}/snapshots.
func (c *Client) ListSnapshots(ctx context.Context, instanceID string) (*SnapshotList, error) {
	var out SnapshotList
	if err := c.Do(ctx, http.MethodGet, "/instances/"+esc(instanceID)+"/snapshots", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSnapshot is POST /v1/instances/{id}/snapshots: taken when it
// answers (201).
func (c *Client) CreateSnapshot(ctx context.Context, instanceID string, body map[string]any) (*Snapshot, error) {
	var out Snapshot
	if err := c.Do(ctx, http.MethodPost, "/instances/"+esc(instanceID)+"/snapshots", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSnapshot is DELETE /v1/instances/{id}/snapshots/{snapshotId}.
func (c *Client) DeleteSnapshot(ctx context.Context, instanceID, snapshotID string) error {
	return c.Do(ctx, http.MethodDelete, "/instances/"+esc(instanceID)+"/snapshots/"+esc(snapshotID), nil, nil, nil)
}

// ── Object Storage access keys ──────────────────────────────────────────────

// AccessKey is an S3 access key to the account's buckets.
type AccessKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccessKeyID string `json:"access_key_id"`
	// read_write or read_only.
	Access string `json:"access"`
	// Nil: every bucket of the account, present and future.
	Buckets    []string `json:"buckets"`
	ExpiresAt  *string  `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	CreatedAt  string   `json:"created_at"`
}

// NewAccessKey is a key just made, with its secret (shown this once).
type NewAccessKey struct {
	Key             AccessKey `json:"key"`
	SecretAccessKey string    `json:"secret_access_key"`
}

// ListAccessKeys is GET /v1/object-storage/keys.
func (c *Client) ListAccessKeys(ctx context.Context) ([]AccessKey, error) {
	out := []AccessKey{}
	if err := c.Do(ctx, http.MethodGet, "/object-storage/keys", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateAccessKey is POST /v1/object-storage/keys.
func (c *Client) CreateAccessKey(ctx context.Context, body map[string]any) (*NewAccessKey, error) {
	var out NewAccessKey
	if err := c.Do(ctx, http.MethodPost, "/object-storage/keys", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeAccessKey is DELETE /v1/object-storage/keys/{id}.
func (c *Client) RevokeAccessKey(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/object-storage/keys/"+esc(id), nil, nil, nil)
}

// ── Webhooks ────────────────────────────────────────────────────────────────

// Webhook is an endpoint the account's events go to: an address of yours
// (GENERIC: signed JSON), or a Slack or Discord channel.
type Webhook struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// The address is a credential: only its host and last characters.
	URL         string   `json:"url"`
	Description *string  `json:"description"`
	Events      []string `json:"events"`
	Enabled     bool     `json:"enabled"`
	// Why it was switched off: failing or removed.
	DisabledReason      *string `json:"disabled_reason"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	LastSuccessAt       *string `json:"last_success_at"`
	LastFailureAt       *string `json:"last_failure_at"`
	CreatedAt           string  `json:"created_at"`
}

// CreatedWebhook is a webhook just added, with its signing secret (GENERIC
// only), shown this once.
type CreatedWebhook struct {
	Webhook Webhook `json:"webhook"`
	Secret  *string `json:"secret"`
}

// ListWebhooks is GET /v1/webhooks.
func (c *Client) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	out := []Webhook{}
	if err := c.Do(ctx, http.MethodGet, "/webhooks", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetWebhook is GET /v1/webhooks/{id}.
func (c *Client) GetWebhook(ctx context.Context, id string) (*Webhook, error) {
	var out Webhook
	if err := c.Do(ctx, http.MethodGet, "/webhooks/"+esc(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateWebhook is POST /v1/webhooks.
func (c *Client) CreateWebhook(ctx context.Context, body map[string]any) (*CreatedWebhook, error) {
	var out CreatedWebhook
	if err := c.Do(ctx, http.MethodPost, "/webhooks", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateWebhook is PATCH /v1/webhooks/{id}: its events, description, and
// whether it is on.
func (c *Client) UpdateWebhook(ctx context.Context, id string, patch map[string]any) (*Webhook, error) {
	var out Webhook
	if err := c.Do(ctx, http.MethodPatch, "/webhooks/"+esc(id), nil, patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWebhook is DELETE /v1/webhooks/{id}: deliveries stop at once.
func (c *Client) DeleteWebhook(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/webhooks/"+esc(id), nil, nil, nil)
}
