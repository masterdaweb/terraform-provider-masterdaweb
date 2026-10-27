package client

import (
	"context"
	"net/http"
	"net/url"
)

func esc(s string) string { return url.PathEscape(s) }

// ── Projects ────────────────────────────────────────────────────────────────

// ListProjects is GET /v1/projects, every page.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	return All[Project](ctx, c, c.projectsBase(ctx), nil)
}

// GetProject is GET /v1/projects/{id}.
func (c *Client) GetProject(ctx context.Context, id string) (*Project, error) {
	var p Project
	if err := c.Do(ctx, http.MethodGet, c.projectsBase(ctx)+"/"+esc(id), nil, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateProject is POST /v1/projects.
func (c *Client) CreateProject(ctx context.Context, body map[string]any) (*Project, error) {
	var p Project
	if err := c.Do(ctx, http.MethodPost, c.projectsBase(ctx), nil, body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateProject is PATCH /v1/projects/{id}.
func (c *Client) UpdateProject(ctx context.Context, id string, patch map[string]any) (*Project, error) {
	var p Project
	if err := c.Do(ctx, http.MethodPatch, c.projectsBase(ctx)+"/"+esc(id), nil, patch, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// DeleteProject is DELETE /v1/projects/{id}?confirm=<name>: its serverless
// services torn down shortly after; the instances, databases and buckets in
// it are not touched (they only leave it).
func (c *Client) DeleteProject(ctx context.Context, id, name string) error {
	return c.Do(ctx, http.MethodDelete, c.projectsBase(ctx)+"/"+esc(id), url.Values{"confirm": {name}}, nil, nil)
}

// ── Services ────────────────────────────────────────────────────────────────

// ListServices is GET /v1/serverless/services, of one project when given.
func (c *Client) ListServices(ctx context.Context, projectID string) ([]Service, error) {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	return All[Service](ctx, c, "/serverless/services", q)
}

// GetService is GET /v1/serverless/services/{id}.
func (c *Client) GetService(ctx context.Context, id string) (*Service, error) {
	var s Service
	if err := c.Do(ctx, http.MethodGet, "/serverless/services/"+esc(id), nil, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateService is POST /v1/serverless/projects/{id}/services (services
// are created under the serverless path either way).
func (c *Client) CreateService(ctx context.Context, projectID string, body map[string]any) (*ServiceWithDeployment, error) {
	var out ServiceWithDeployment
	if err := c.Do(ctx, http.MethodPost, "/serverless/projects/"+esc(projectID)+"/services", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateServiceSettings is PATCH /v1/serverless/services/{id}/settings.
func (c *Client) UpdateServiceSettings(ctx context.Context, id string, settings map[string]any, deploy bool) (*ServiceWithDeployment, error) {
	var out ServiceWithDeployment
	body := map[string]any{"settings": settings, "deploy": deploy}
	if err := c.Do(ctx, http.MethodPatch, "/serverless/services/"+esc(id)+"/settings", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateServiceSource is PATCH /v1/serverless/services/{id}/source: the
// repository, branch or image, and auto-deploy. It does not deploy.
func (c *Client) UpdateServiceSource(ctx context.Context, id string, patch map[string]any) (*Service, error) {
	var s Service
	if err := c.Do(ctx, http.MethodPatch, "/serverless/services/"+esc(id)+"/source", nil, patch, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SetServiceDeletionProtection is PUT /v1/serverless/services/{id}/deletion-protection.
func (c *Client) SetServiceDeletionProtection(ctx context.Context, id string, enabled bool) (*Service, error) {
	var s Service
	if err := c.Do(ctx, http.MethodPut, "/serverless/services/"+esc(id)+"/deletion-protection", nil, map[string]any{"enabled": enabled}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// DeployService is POST /v1/serverless/services/{id}/deployments with an
// action: latest (the branch tip or the image's current digest) or apply.
func (c *Client) DeployService(ctx context.Context, id, action string) (*Deployment, error) {
	var d Deployment
	if err := c.Do(ctx, http.MethodPost, "/serverless/services/"+esc(id)+"/deployments", nil, map[string]any{"action": action}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteService is DELETE /v1/serverless/services/{id}?confirm=<name>.
func (c *Client) DeleteService(ctx context.Context, id, name string) error {
	return c.Do(ctx, http.MethodDelete, "/serverless/services/"+esc(id), url.Values{"confirm": {name}}, nil, nil)
}

// ListGitConnections is GET /v1/serverless/git/connections.
func (c *Client) ListGitConnections(ctx context.Context) ([]GitConnection, error) {
	out := []GitConnection{}
	if err := c.Do(ctx, http.MethodGet, "/serverless/git/connections", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── Variables ───────────────────────────────────────────────────────────────

// ListVariables is GET /v1/serverless/services/{id}/variables.
func (c *Client) ListVariables(ctx context.Context, serviceID string) ([]Variable, error) {
	out := []Variable{}
	if err := c.Do(ctx, http.MethodGet, "/serverless/services/"+esc(serviceID)+"/variables", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetVariables is PUT /v1/serverless/services/{id}/variables (all or
// nothing; the variables not listed are left as they are).
func (c *Client) SetVariables(ctx context.Context, serviceID string, vars []VariableInput, deploy bool) (*VariableChange, error) {
	var out VariableChange
	body := map[string]any{"variables": vars, "deploy": deploy}
	if err := c.Do(ctx, http.MethodPut, "/serverless/services/"+esc(serviceID)+"/variables", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteVariables is POST /v1/serverless/services/{id}/variables/delete. The
// API applies a removal with a new deployment, always.
func (c *Client) DeleteVariables(ctx context.Context, serviceID string, keys []string) (*VariableChange, error) {
	var out VariableChange
	if err := c.Do(ctx, http.MethodPost, "/serverless/services/"+esc(serviceID)+"/variables/delete", nil, map[string]any{"keys": keys}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Domains ─────────────────────────────────────────────────────────────────

// ListDomains is GET /v1/serverless/services/{id}/domains (its default
// address included).
func (c *Client) ListDomains(ctx context.Context, serviceID string) ([]Domain, error) {
	out := []Domain{}
	if err := c.Do(ctx, http.MethodGet, "/serverless/services/"+esc(serviceID)+"/domains", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddDomain is POST /v1/serverless/services/{id}/domains.
func (c *Client) AddDomain(ctx context.Context, serviceID, hostname string) (*Domain, error) {
	var d Domain
	if err := c.Do(ctx, http.MethodPost, "/serverless/services/"+esc(serviceID)+"/domains", nil, map[string]any{"hostname": hostname}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteDomain is DELETE /v1/serverless/domains/{id}.
func (c *Client) DeleteDomain(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/serverless/domains/"+esc(id), nil, nil, nil)
}

// ── Bindings ────────────────────────────────────────────────────────────────

// ListBindings is GET /v1/serverless/services/{id}/bindings.
func (c *Client) ListBindings(ctx context.Context, serviceID string) ([]Binding, error) {
	out := []Binding{}
	if err := c.Do(ctx, http.MethodGet, "/serverless/services/"+esc(serviceID)+"/bindings", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateBinding is POST /v1/serverless/services/{id}/bindings.
func (c *Client) CreateBinding(ctx context.Context, serviceID string, body map[string]any) (*BindingChange, error) {
	var out BindingChange
	if err := c.Do(ctx, http.MethodPost, "/serverless/services/"+esc(serviceID)+"/bindings", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBinding is DELETE /v1/serverless/services/{id}/bindings/{bindingId}.
// The API applies it with a new deployment, always.
func (c *Client) DeleteBinding(ctx context.Context, serviceID, bindingID string) error {
	return c.Do(ctx, http.MethodDelete, "/serverless/services/"+esc(serviceID)+"/bindings/"+esc(bindingID), nil, nil, nil)
}

// ── Log drains ──────────────────────────────────────────────────────────────

// ListLogDrains is GET /v1/serverless/projects/{id}/log-drains.
func (c *Client) ListLogDrains(ctx context.Context, projectID string) ([]LogDrain, error) {
	out := []LogDrain{}
	if err := c.Do(ctx, http.MethodGet, "/serverless/projects/"+esc(projectID)+"/log-drains", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateLogDrain is POST /v1/serverless/projects/{id}/log-drains.
func (c *Client) CreateLogDrain(ctx context.Context, projectID string, body map[string]any) (*LogDrain, error) {
	var d LogDrain
	if err := c.Do(ctx, http.MethodPost, "/serverless/projects/"+esc(projectID)+"/log-drains", nil, body, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// SetLogDrainEnabled is PATCH /v1/serverless/log-drains/{id}.
func (c *Client) SetLogDrainEnabled(ctx context.Context, id string, enabled bool) (*LogDrain, error) {
	var d LogDrain
	if err := c.Do(ctx, http.MethodPatch, "/serverless/log-drains/"+esc(id), nil, map[string]any{"enabled": enabled}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeleteLogDrain is DELETE /v1/serverless/log-drains/{id}.
func (c *Client) DeleteLogDrain(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/serverless/log-drains/"+esc(id), nil, nil, nil)
}

// ── Network ─────────────────────────────────────────────────────────────────

// GetOutboundAddresses is GET /v1/serverless/outbound-addresses.
func (c *Client) GetOutboundAddresses(ctx context.Context) (*OutboundAddresses, error) {
	var out OutboundAddresses
	if err := c.Do(ctx, http.MethodGet, "/serverless/outbound-addresses", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
