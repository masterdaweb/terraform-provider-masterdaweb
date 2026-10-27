package client

import (
	"context"
	"net/http"
	"net/url"
)

// ── Firewalls ───────────────────────────────────────────────────────────────

// FirewallRule is one of a firewall's rules, in evaluation order.
type FirewallRule struct {
	ID       string `json:"id"`
	Position int64  `json:"position"`
	// in or out.
	Direction string `json:"direction"`
	// ACCEPT, DROP or REJECT.
	Action      string  `json:"action"`
	Macro       *string `json:"macro"`
	Protocol    *string `json:"protocol"`
	DestPort    *string `json:"dest_port"`
	SourcePort  *string `json:"source_port"`
	Source      *string `json:"source"`
	Destination *string `json:"destination"`
	Enabled     bool    `json:"enabled"`
	Comment     *string `json:"comment"`
}

// Firewall is a set of rules applied to instances. Its edits are staged
// until committed (HasPendingChanges).
type Firewall struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Description       *string        `json:"description"`
	Enabled           bool           `json:"enabled"`
	HasPendingChanges bool           `json:"has_pending_changes"`
	PolicyIn          string         `json:"policy_in"`
	PolicyOut         string         `json:"policy_out"`
	Rules             []FirewallRule `json:"rules"`
	InstanceIDs       []string       `json:"instance_ids"`
	CreatedAt         *string        `json:"created_at"`
}

// FirewallCommit is what committing a firewall answers.
type FirewallCommit struct {
	FirewallID  string `json:"firewall_id"`
	OperationID string `json:"operation_id"`
}

// FirewallMacro is a named service preset (HTTP, SSH…).
type FirewallMacro struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListFirewalls is GET /v1/firewalls, every page.
func (c *Client) ListFirewalls(ctx context.Context) ([]Firewall, error) {
	return All[Firewall](ctx, c, "/firewalls", url.Values{"limit": {"100"}})
}

// GetFirewall is GET /v1/firewalls/{id}.
func (c *Client) GetFirewall(ctx context.Context, id string) (*Firewall, error) {
	var f Firewall
	if err := c.Do(ctx, http.MethodGet, "/firewalls/"+esc(id), nil, nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// CreateFirewall is POST /v1/firewalls: empty, attached to nothing.
func (c *Client) CreateFirewall(ctx context.Context, body map[string]any) (*Firewall, error) {
	var f Firewall
	if err := c.Do(ctx, http.MethodPost, "/firewalls", nil, body, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// UpdateFirewall is PATCH /v1/firewalls/{id}.
func (c *Client) UpdateFirewall(ctx context.Context, id string, patch map[string]any) (*Firewall, error) {
	var f Firewall
	if err := c.Do(ctx, http.MethodPatch, "/firewalls/"+esc(id), nil, patch, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// DeleteFirewall is DELETE /v1/firewalls/{id}: detached from every instance.
func (c *Client) DeleteFirewall(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/firewalls/"+esc(id), nil, nil, nil)
}

// AddFirewallRule is POST /v1/firewalls/{id}/rules (staged).
func (c *Client) AddFirewallRule(ctx context.Context, id string, rule map[string]any) (*Firewall, error) {
	var f Firewall
	if err := c.Do(ctx, http.MethodPost, "/firewalls/"+esc(id)+"/rules", nil, rule, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// UpdateFirewallRule is PATCH /v1/firewalls/{id}/rules/{ruleId} (staged).
func (c *Client) UpdateFirewallRule(ctx context.Context, id, ruleID string, rule map[string]any) (*Firewall, error) {
	var f Firewall
	if err := c.Do(ctx, http.MethodPatch, "/firewalls/"+esc(id)+"/rules/"+esc(ruleID), nil, rule, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// DeleteFirewallRule is DELETE /v1/firewalls/{id}/rules/{ruleId} (staged).
func (c *Client) DeleteFirewallRule(ctx context.Context, id, ruleID string) error {
	return c.Do(ctx, http.MethodDelete, "/firewalls/"+esc(id)+"/rules/"+esc(ruleID), nil, nil, nil)
}

// CommitFirewall is POST /v1/firewalls/{id}/commit: the staged rules applied
// to exactly these instances (one not listed is detached).
func (c *Client) CommitFirewall(ctx context.Context, id string, instanceIDs []string) (*FirewallCommit, error) {
	if instanceIDs == nil {
		instanceIDs = []string{}
	}
	var out FirewallCommit
	if err := c.Do(ctx, http.MethodPost, "/firewalls/"+esc(id)+"/commit", nil, map[string]any{"instance_ids": instanceIDs}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListFirewallMacros is GET /v1/firewalls/macros.
func (c *Client) ListFirewallMacros(ctx context.Context) ([]FirewallMacro, error) {
	out := []FirewallMacro{}
	if err := c.Do(ctx, http.MethodGet, "/firewalls/macros", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── VPCs ────────────────────────────────────────────────────────────────────

// VPC is a private network between instances.
type VPC struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description *string     `json:"description"`
	Subnet      *string     `json:"subnet"`
	Default     bool        `json:"default"`
	RegionID    *string     `json:"region_id"`
	InstanceIDs []string    `json:"instance_ids"`
	Members     []VPCMember `json:"members"`
	CreatedAt   *string     `json:"created_at"`
}

// VPCMember is an instance in a VPC, with its address there.
type VPCMember struct {
	InstanceID string `json:"instance_id"`
	// Null until the private NIC is set up.
	PrivateIP *string `json:"private_ip"`
	// The private NIC as last seen.
	Status string `json:"status"`
}

// ListVPCs is GET /v1/vpcs, every page.
func (c *Client) ListVPCs(ctx context.Context) ([]VPC, error) {
	return All[VPC](ctx, c, "/vpcs", url.Values{"limit": {"100"}})
}

// GetVPC is GET /v1/vpcs/{id}.
func (c *Client) GetVPC(ctx context.Context, id string) (*VPC, error) {
	var v VPC
	if err := c.Do(ctx, http.MethodGet, "/vpcs/"+esc(id), nil, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// CreateVPC is POST /v1/vpcs.
func (c *Client) CreateVPC(ctx context.Context, body map[string]any) (*VPC, error) {
	var v VPC
	if err := c.Do(ctx, http.MethodPost, "/vpcs", nil, body, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// UpdateVPC is PATCH /v1/vpcs/{id}: its name and description.
func (c *Client) UpdateVPC(ctx context.Context, id string, patch map[string]any) (*VPC, error) {
	var v VPC
	if err := c.Do(ctx, http.MethodPatch, "/vpcs/"+esc(id), nil, patch, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// DeleteVPC is DELETE /v1/vpcs/{id}: its instances leave it. The account's
// default VPC is never deleted.
func (c *Client) DeleteVPC(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/vpcs/"+esc(id), nil, nil, nil)
}

// AttachVPCInstance is POST /v1/vpcs/{id}/instances: an instance on the
// network (202), at privateIP when given (else DHCP picks one). Its private
// NIC is plugged in by the operation; restart restarts it so the NIC comes up
// at once.
func (c *Client) AttachVPCInstance(ctx context.Context, vpcID, instanceID, privateIP string, restart bool) (*OperationAccepted, error) {
	body := map[string]any{"instance_id": instanceID}
	if privateIP != "" {
		body["private_ip"] = privateIP
	}
	if restart {
		body["restart"] = true
	}
	var out OperationAccepted
	if err := c.Do(ctx, http.MethodPost, "/vpcs/"+esc(vpcID)+"/instances", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DetachVPCInstance is DELETE /v1/vpcs/{id}/instances/{instanceId} (202):
// its private NIC removed, its public one kept.
func (c *Client) DetachVPCInstance(ctx context.Context, vpcID, instanceID string, restart bool) (*OperationAccepted, error) {
	var q url.Values
	if restart {
		q = url.Values{"restart": {"true"}}
	}
	var out OperationAccepted
	if err := c.Do(ctx, http.MethodDelete, "/vpcs/"+esc(vpcID)+"/instances/"+esc(instanceID), q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Reverse DNS ─────────────────────────────────────────────────────────────

// ReverseDNS is an address's PTR record.
type ReverseDNS struct {
	IP string `json:"ip"`
	// 4 or 6.
	Version  int64   `json:"version"`
	Hostname *string `json:"hostname"`
	// What a reset puts back.
	DefaultHostname *string `json:"default_hostname"`
	InstanceID      *string `json:"instance_id"`
	// Whether the PTR may be changed now: a new address is held a few days
	// first (EditableAt).
	Editable   bool    `json:"editable"`
	EditableAt *string `json:"editable_at"`
}

// ListReverseDNS is GET /v1/reverse-dns: every address of the account, in
// one list.
func (c *Client) ListReverseDNS(ctx context.Context) ([]ReverseDNS, error) {
	var p Page[ReverseDNS]
	if err := c.Do(ctx, http.MethodGet, "/reverse-dns", nil, nil, &p); err != nil {
		return nil, err
	}
	return p.Data, nil
}

// SetReverseDNS is PUT /v1/reverse-dns/{ip}. The hostname must resolve to
// the address (forward-confirmed).
func (c *Client) SetReverseDNS(ctx context.Context, ip, hostname string) (*ReverseDNS, error) {
	var r ReverseDNS
	if err := c.Do(ctx, http.MethodPut, "/reverse-dns/"+esc(ip), nil, map[string]any{"hostname": hostname}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ResetReverseDNS is POST /v1/reverse-dns/{ip}/reset: back to the default.
func (c *Client) ResetReverseDNS(ctx context.Context, ip string) (*ReverseDNS, error) {
	var r ReverseDNS
	if err := c.Do(ctx, http.MethodPost, "/reverse-dns/"+esc(ip)+"/reset", nil, nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
