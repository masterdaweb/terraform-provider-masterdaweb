package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// DNSZone is a domain hosted on the platform's nameservers.
type DNSZone struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt *string `json:"created_at"`
}

// DNSZoneHealth says whether a domain's registrar points at the platform's
// nameservers, and which they are.
type DNSZoneHealth struct {
	Delegated bool `json:"delegated"`
	// The NS records the domain has now.
	Nameservers []string `json:"nameservers"`
	// The platform's nameservers, the ones to set at the registrar.
	ExpectedNameservers []string `json:"expected_nameservers"`
}

// DNSRecord is a record set: the values at one name and type.
type DNSRecord struct {
	// Fully qualified, with its trailing dot, as the nameservers keep it.
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	TTL    int64    `json:"ttl"`
	Values []string `json:"values"`
	// Values present but disabled: in the zone, not answered.
	DisabledValues []string `json:"disabled_values"`
}

// ListDNSZones is GET /v1/dns/zones, every page.
func (c *Client) ListDNSZones(ctx context.Context) ([]DNSZone, error) {
	return All[DNSZone](ctx, c, "/dns/zones", url.Values{"limit": {"100"}})
}

// GetDNSZone is GET /v1/dns/zones/{id}.
func (c *Client) GetDNSZone(ctx context.Context, id string) (*DNSZone, error) {
	var z DNSZone
	if err := c.Do(ctx, http.MethodGet, "/dns/zones/"+esc(id), nil, nil, &z); err != nil {
		return nil, err
	}
	return &z, nil
}

// CreateDNSZone is POST /v1/dns/zones. Asking for a zone the account
// already hosts answers that zone.
func (c *Client) CreateDNSZone(ctx context.Context, name string) (*DNSZone, error) {
	var z DNSZone
	if err := c.Do(ctx, http.MethodPost, "/dns/zones", nil, map[string]any{"name": name}, &z); err != nil {
		return nil, err
	}
	return &z, nil
}

// GetDNSZoneHealth is GET /v1/dns/zones/{id}/health: the domain's NS
// records, looked up now.
func (c *Client) GetDNSZoneHealth(ctx context.Context, id string) (*DNSZoneHealth, error) {
	var h DNSZoneHealth
	if err := c.Do(ctx, http.MethodGet, "/dns/zones/"+esc(id)+"/health", nil, nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// DeleteDNSZone is DELETE /v1/dns/zones/{id}: the zone and every record in it.
func (c *Client) DeleteDNSZone(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/dns/zones/"+esc(id), nil, nil, nil)
}

// ListDNSRecords is GET /v1/dns/zones/{id}/records, every page.
func (c *Client) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	return All[DNSRecord](ctx, c, "/dns/zones/"+esc(zoneID)+"/records", url.Values{"limit": {"100"}})
}

// UpsertDNSRecord is POST /v1/dns/zones/{id}/records: the record set at
// (name, type) replaced by this one value.
func (c *Client) UpsertDNSRecord(ctx context.Context, zoneID string, body map[string]any) error {
	return c.Do(ctx, http.MethodPost, "/dns/zones/"+esc(zoneID)+"/records", nil, body, nil)
}

// ReplaceDNSRecordSet is PUT /v1/dns/zones/{id}/records: the record set at
// (name, type) replaced by all these values.
func (c *Client) ReplaceDNSRecordSet(ctx context.Context, zoneID string, body map[string]any) error {
	return c.Do(ctx, http.MethodPut, "/dns/zones/"+esc(zoneID)+"/records", nil, body, nil)
}

// SetDNSRecordSet makes the record set at (name, type) hold exactly these
// values: in one step with the record set PUT, or — on an API from before it
// — with the one-value POST, which a set of several values cannot go
// through.
func (c *Client) SetDNSRecordSet(ctx context.Context, zoneID, name, typ string, ttl int64, values []string) error {
	c.dnsMu.Lock()
	noPut := c.noRecordSetPut
	c.dnsMu.Unlock()
	if !noPut {
		err := c.ReplaceDNSRecordSet(ctx, zoneID, map[string]any{"name": name, "type": typ, "ttl": ttl, "records": values})
		if !RouteMissing(err) {
			return err
		}
		c.dnsMu.Lock()
		c.noRecordSetPut = true
		c.dnsMu.Unlock()
	}
	if len(values) != 1 {
		return fmt.Errorf("this API keeps one value per record set through its v1 endpoint (POST): a set of %d values "+
			"needs its PUT endpoint, which it does not have yet. Give one value, or wait for the API to have it", len(values))
	}
	return c.UpsertDNSRecord(ctx, zoneID, map[string]any{"name": name, "type": typ, "content": values[0], "ttl": ttl})
}

// DeleteDNSRecord is DELETE /v1/dns/zones/{id}/records: the whole record set
// at (name, type).
func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, name, typ string) error {
	return c.Do(ctx, http.MethodDelete, "/dns/zones/"+esc(zoneID)+"/records", nil, map[string]any{"name": name, "type": typ}, nil)
}
