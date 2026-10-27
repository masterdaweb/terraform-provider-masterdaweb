package client

import (
	"context"
	"net/http"
	"net/url"
)

// ── Databases ───────────────────────────────────────────────────────────────

// GetDatabasesInfo is GET /v1/databases/info: engines, versions and sizes.
func (c *Client) GetDatabasesInfo(ctx context.Context) (*DatabasesInfo, error) {
	var out DatabasesInfo
	if err := c.Do(ctx, http.MethodGet, "/databases/info", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDatabases is GET /v1/databases (deleted ones whose backups are kept
// included).
func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	out := []Database{}
	if err := c.Do(ctx, http.MethodGet, "/databases", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetDatabase is GET /v1/databases/{name}.
func (c *Client) GetDatabase(ctx context.Context, name string) (*Database, error) {
	var d Database
	if err := c.Do(ctx, http.MethodGet, "/databases/"+esc(name), nil, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDatabase is POST /v1/databases.
func (c *Client) CreateDatabase(ctx context.Context, body map[string]any) (*CreatedDatabase, error) {
	var out CreatedDatabase
	if err := c.Do(ctx, http.MethodPost, "/databases", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateDatabase is PATCH /v1/databases/{name}.
func (c *Client) UpdateDatabase(ctx context.Context, name string, patch map[string]any) (*Database, error) {
	var d Database
	if err := c.Do(ctx, http.MethodPatch, "/databases/"+esc(name), nil, patch, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ResizeDatabase is POST /v1/databases/{name}/resize.
func (c *Client) ResizeDatabase(ctx context.Context, name string, body map[string]any) (*Database, error) {
	var d Database
	if err := c.Do(ctx, http.MethodPost, "/databases/"+esc(name)+"/resize", nil, body, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteDatabase is DELETE /v1/databases/{name}?confirm=<name>.
func (c *Client) DeleteDatabase(ctx context.Context, name string) error {
	return c.Do(ctx, http.MethodDelete, "/databases/"+esc(name), url.Values{"confirm": {name}}, nil, nil)
}

// ── Object Storage ──────────────────────────────────────────────────────────

// ListBuckets is GET /v1/object-storage/buckets (deleted ones still
// restorable included).
func (c *Client) ListBuckets(ctx context.Context) ([]Bucket, error) {
	out := []Bucket{}
	if err := c.Do(ctx, http.MethodGet, "/object-storage/buckets", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetBucket is GET /v1/object-storage/buckets/{name}.
func (c *Client) GetBucket(ctx context.Context, name string) (*Bucket, error) {
	var b Bucket
	if err := c.Do(ctx, http.MethodGet, "/object-storage/buckets/"+esc(name), nil, nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// CreateBucket is POST /v1/object-storage/buckets.
func (c *Client) CreateBucket(ctx context.Context, body map[string]any) (*Bucket, error) {
	var b Bucket
	if err := c.Do(ctx, http.MethodPost, "/object-storage/buckets", nil, body, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// UpdateBucket is PATCH /v1/object-storage/buckets/{name}.
func (c *Client) UpdateBucket(ctx context.Context, name string, patch map[string]any) (*Bucket, error) {
	var b Bucket
	if err := c.Do(ctx, http.MethodPatch, "/object-storage/buckets/"+esc(name), nil, patch, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// DeleteBucket is DELETE /v1/object-storage/buckets/{name}?confirm=<name>:
// restorable for a few days.
func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	return c.Do(ctx, http.MethodDelete, "/object-storage/buckets/"+esc(name), url.Values{"confirm": {name}}, nil, nil)
}
