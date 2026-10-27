package client

import (
	"context"
	"net/http"
	"net/url"
)

// ── VPN gateways ────────────────────────────────────────────────────────────

// VPNGatewayWireguard is a gateway's WireGuard identity, for the far end of a
// WireGuard site connection.
type VPNGatewayWireguard struct {
	PublicKey  *string `json:"public_key"`
	ListenPort int64   `json:"listen_port"`
}

// VPNGateway is an instance running the platform's VPN appliance: remote
// access for people (the VPN app) and site-to-site tunnels. It is ordered as
// an instance, from the VPN gateway image.
type VPNGateway struct {
	ID         string  `json:"id"`
	InstanceID string  `json:"instance_id"`
	Name       *string `json:"name"`
	Location   *string `json:"location"`
	PublicIP   *string `json:"public_ip"`
	Running    bool    `json:"running"`
	// udp or tcp: the VPN app's transport.
	Protocol string `json:"protocol"`
	// The addresses people connecting get, as a CIDR.
	RemoteAccessPool      *string             `json:"remote_access_pool"`
	RemoteAccessPoolIssue *string             `json:"remote_access_pool_issue"`
	RouteAllTraffic       bool                `json:"route_all_traffic"`
	AllowSharedLogins     bool                `json:"allow_shared_logins"`
	MTU                   int64               `json:"mtu"`
	Wireguard             VPNGatewayWireguard `json:"wireguard"`
	// Settings or users changed since the last apply.
	PendingChanges bool `json:"pending_changes"`
	// What the next apply does: in_place, or rebuild (minutes offline).
	ApplyMode string `json:"apply_mode"`
	// idle, queued, applying or failed.
	ApplyState              string  `json:"apply_state"`
	ApplyOperationID        *string `json:"apply_operation_id"`
	AppliedAt               *string `json:"applied_at"`
	LastApplyErrorCode      *string `json:"last_apply_error_code"`
	UpdateAvailable         bool    `json:"update_available"`
	UsersCount              int64   `json:"users_count"`
	SiteConnectionsCount    int64   `json:"site_connections_count"`
	IncludedSiteConnections int64   `json:"included_site_connections"`
	CreatedAt               *string `json:"created_at"`
}

// VPNGatewayApply is what applying a gateway's pending changes started.
type VPNGatewayApply struct {
	GatewayID string `json:"gateway_id"`
	// in_place or rebuild.
	Mode string `json:"mode"`
	// GET /v1/operations/{id}.
	OperationID *string `json:"operation_id"`
	// An apply already running carries the change too.
	Coalesced bool `json:"coalesced"`
}

// VPNUser is a login on a gateway: a person connecting with the VPN app.
// Its password is never shown again (only a generated one, once, on create).
type VPNUser struct {
	ID        string `json:"id"`
	GatewayID string `json:"gateway_id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	// active or inactive (the login is kept, and refused).
	Status    string  `json:"status"`
	CreatedAt *string `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
	// Only on create, when the platform generated it.
	Password *string `json:"password"`
}

// VPNGatewayRebuildRequired is the API's reason for refusing an in-place
// apply whose changes need a rebuild.
const VPNGatewayRebuildRequired = "VPN_GATEWAY_REBUILD_REQUIRED"

// ListVPNGateways is GET /v1/vpn-gateways, every page.
func (c *Client) ListVPNGateways(ctx context.Context) ([]VPNGateway, error) {
	return All[VPNGateway](ctx, c, "/vpn-gateways", url.Values{"limit": {"100"}})
}

// GetVPNGateway is GET /v1/vpn-gateways/{id}.
func (c *Client) GetVPNGateway(ctx context.Context, id string) (*VPNGateway, error) {
	var g VPNGateway
	if err := c.Do(ctx, http.MethodGet, "/vpn-gateways/"+esc(id), nil, nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateVPNGateway is PATCH /v1/vpn-gateways/{id}. The name changes at once;
// the rest waits for an apply (PendingChanges).
func (c *Client) UpdateVPNGateway(ctx context.Context, id string, patch map[string]any) (*VPNGateway, error) {
	var g VPNGateway
	if err := c.Do(ctx, http.MethodPatch, "/vpn-gateways/"+esc(id), nil, patch, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// ApplyVPNGateway is POST /v1/vpn-gateways/{id}/apply (202). Without rebuild,
// changes that need one are refused with VPNGatewayRebuildRequired.
func (c *Client) ApplyVPNGateway(ctx context.Context, id string, rebuild bool) (*VPNGatewayApply, error) {
	body := map[string]any{}
	if rebuild {
		body["rebuild"] = true
	}
	var a VPNGatewayApply
	if err := c.Do(ctx, http.MethodPost, "/vpn-gateways/"+esc(id)+"/apply", nil, body, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// ListVPNUsers is GET /v1/vpn-gateways/{id}/users, every page.
func (c *Client) ListVPNUsers(ctx context.Context, gatewayID string) ([]VPNUser, error) {
	return All[VPNUser](ctx, c, "/vpn-gateways/"+esc(gatewayID)+"/users", url.Values{"limit": {"100"}})
}

// GetVPNUser is GET /v1/vpn-gateways/{id}/users/{user_id}.
func (c *Client) GetVPNUser(ctx context.Context, gatewayID, userID string) (*VPNUser, error) {
	var u VPNUser
	if err := c.Do(ctx, http.MethodGet, "/vpn-gateways/"+esc(gatewayID)+"/users/"+esc(userID), nil, nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateVPNUser is POST /v1/vpn-gateways/{id}/users. Without a password, one
// is generated and returned once.
func (c *Client) CreateVPNUser(ctx context.Context, gatewayID string, body map[string]any) (*VPNUser, error) {
	var u VPNUser
	if err := c.Do(ctx, http.MethodPost, "/vpn-gateways/"+esc(gatewayID)+"/users", nil, body, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateVPNUser is PATCH /v1/vpn-gateways/{id}/users/{user_id}.
func (c *Client) UpdateVPNUser(ctx context.Context, gatewayID, userID string, patch map[string]any) (*VPNUser, error) {
	var u VPNUser
	if err := c.Do(ctx, http.MethodPatch, "/vpn-gateways/"+esc(gatewayID)+"/users/"+esc(userID), nil, patch, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteVPNUser is DELETE /v1/vpn-gateways/{id}/users/{user_id}.
func (c *Client) DeleteVPNUser(ctx context.Context, gatewayID, userID string) error {
	return c.Do(ctx, http.MethodDelete, "/vpn-gateways/"+esc(gatewayID)+"/users/"+esc(userID), nil, nil, nil)
}
