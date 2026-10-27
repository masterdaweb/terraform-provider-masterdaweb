package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// ── Catalog ─────────────────────────────────────────────────────────────────

// Region is where instances are placed (GET /v1/regions).
type Region struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	RegionName    *string `json:"region_name"`
	Available     bool    `json:"available"`
	IPv4Available bool    `json:"ipv4_available"`
	IPv6Available bool    `json:"ipv6_available"`
}

// TermRate is what an offering or an add-on costs on one billing term.
type TermRate struct {
	Term                  string  `json:"term"`
	RateMicroCentsPerHour *string `json:"rate_micro_cents_per_hour"`
}

// Offering is a plan an instance is ordered on (GET /v1/catalog/offerings).
type Offering struct {
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Description *string    `json:"description"`
	Kind        string     `json:"kind"`
	VCPUs       int64      `json:"vcpus"`
	RAMMB       int64      `json:"ram_mb"`
	BaseDiskGB  int64      `json:"base_disk_gb"`
	Rates       []TermRate `json:"rates"`
	Currency    string     `json:"currency"`
}

// CatalogImage is an operating system or a ready-made app an instance is
// installed with (GET /v1/catalog/images).
type CatalogImage struct {
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	OSFamily         string  `json:"os_family"`
	Type             string  `json:"type"`
	Category         string  `json:"category"`
	Description      *string `json:"description"`
	SupportsPassword bool    `json:"supports_password"`
	MinDiskGB        *int64  `json:"min_disk_gb"`
	MinRAMMB         *int64  `json:"min_ram_mb"`
	MinVCPU          *int64  `json:"min_vcpu"`
	// What the app asks before it can run (order body: app_inputs), or nil.
	Inputs []AppInput `json:"inputs"`
}

// AppInput is one of an app's setup questions.
type AppInput struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	// required (an order is refused without it), recommended or optional.
	Need     string `json:"need"`
	Default  any    `json:"default"`
	Fallback struct {
		Label string `json:"label"`
		Help  string `json:"help"`
	} `json:"fallback"`
}

// Addon is an extra an instance can be ordered with (GET /v1/catalog/addons).
type Addon struct {
	Code            string     `json:"code"`
	Name            string     `json:"name"`
	Description     *string    `json:"description"`
	Type            string     `json:"type"`
	QuantityEnabled bool       `json:"quantity_enabled"`
	MinQuantity     int64      `json:"min_quantity"`
	MaxQuantity     *int64     `json:"max_quantity"`
	QuantityStep    int64      `json:"quantity_step"`
	Rates           []TermRate `json:"rates"`
	Currency        string     `json:"currency"`
}

// ListRegions is GET /v1/regions, every page.
func (c *Client) ListRegions(ctx context.Context) ([]Region, error) {
	return All[Region](ctx, c, "/regions", url.Values{"limit": {"100"}})
}

// ListOfferings is GET /v1/catalog/offerings, every page.
func (c *Client) ListOfferings(ctx context.Context) ([]Offering, error) {
	return All[Offering](ctx, c, "/catalog/offerings", url.Values{"limit": {"100"}})
}

// ListCatalogImages is GET /v1/catalog/images, every page.
func (c *Client) ListCatalogImages(ctx context.Context) ([]CatalogImage, error) {
	return All[CatalogImage](ctx, c, "/catalog/images", url.Values{"limit": {"100"}})
}

// ListAddons is GET /v1/catalog/addons, every page.
func (c *Client) ListAddons(ctx context.Context) ([]Addon, error) {
	return All[Addon](ctx, c, "/catalog/addons", url.Values{"limit": {"100"}})
}

// ── Instances ───────────────────────────────────────────────────────────────

// InstanceRegion is the region an instance runs in.
type InstanceRegion struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	RegionName *string `json:"region_name"`
}

// InstanceSpecs are an instance's effective resources.
type InstanceSpecs struct {
	VCPUs  *int64 `json:"vcpus"`
	RAMMB  *int64 `json:"ram_mb"`
	DiskGB *int64 `json:"disk_gb"`
}

// InstanceIP is one of an instance's addresses.
type InstanceIP struct {
	Address string `json:"address"`
	// ipv4 or ipv6.
	Version string `json:"version"`
	Primary bool   `json:"primary"`
}

// Instance is a cloud instance (VPS).
type Instance struct {
	ID          string  `json:"id"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	// The last power state on record (running or stopped); the live one is
	// GET /v1/instances/{id}/status.
	PowerStateCached string          `json:"power_state_cached"`
	Suspended        bool            `json:"suspended"`
	Region           *InstanceRegion `json:"region"`
	Specs            InstanceSpecs   `json:"specs"`
	Hostname         *string         `json:"hostname"`
	IPAddresses      []InstanceIP    `json:"ip_addresses"`
	VPCID            *string         `json:"vpc_id"`
	OfferingCode     *string         `json:"offering_code"`
	// The catalog image it was built or last reinstalled from (null for a
	// custom image), the custom image when it was one, its project and its
	// subscription.
	Image          *string `json:"image"`
	CustomImageID  *string `json:"custom_image_id"`
	ProjectID      *string `json:"project_id"`
	SubscriptionID *string `json:"subscription_id"`
	CreatedAt      *string `json:"created_at"`

	// The fields the answer had: an API from before some of them leaves
	// them out, which is not the same as null.
	fields map[string]bool
}

// UnmarshalJSON reads an instance and which fields the answer had.
func (i *Instance) UnmarshalJSON(raw []byte) error {
	type plain Instance
	if err := json.Unmarshal(raw, (*plain)(i)); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return err
	}
	i.fields = map[string]bool{}
	for k := range keys {
		i.fields[k] = true
	}
	return nil
}

// Has says whether the API's answer had this field (null or not).
func (i *Instance) Has(field string) bool { return i.fields[field] }

// InstanceStatus is an instance's live state, from the hypervisor.
type InstanceStatus struct {
	InstanceID    string   `json:"instance_id"`
	PowerState    string   `json:"power_state"`
	Suspended     bool     `json:"suspended"`
	UptimeSeconds *float64 `json:"uptime_seconds"`
	// True while a deploy, backup or restore runs on it.
	Busy               bool     `json:"busy"`
	DeploymentProgress *float64 `json:"deployment_progress"`
	Cached             bool     `json:"cached"`
}

// InstanceDeletion is what destroying an instance answers.
type InstanceDeletion struct {
	InstanceID string `json:"instance_id"`
	Deleted    bool   `json:"deleted"`
	Message    string `json:"message"`
}

// TaskAccepted is what a power action answers (202).
type TaskAccepted struct {
	InstanceID string  `json:"instance_id"`
	Action     string  `json:"action"`
	TaskID     *string `json:"task_id"`
}

// PasswordReset is what resetting an instance's password answers: the new
// password when the API made it up, else null.
type PasswordReset struct {
	InstanceID string  `json:"instance_id"`
	Password   *string `json:"password"`
}

// ListInstances is GET /v1/instances, every page.
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	return All[Instance](ctx, c, "/instances", url.Values{"limit": {"100"}})
}

// GetInstance is GET /v1/instances/{id}.
func (c *Client) GetInstance(ctx context.Context, id string) (*Instance, error) {
	var i Instance
	if err := c.Do(ctx, http.MethodGet, "/instances/"+esc(id), nil, nil, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

// UpdateInstance is PATCH /v1/instances/{id}: its name and description.
func (c *Client) UpdateInstance(ctx context.Context, id string, patch map[string]any) (*Instance, error) {
	var i Instance
	if err := c.Do(ctx, http.MethodPatch, "/instances/"+esc(id), nil, patch, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

// DeleteInstance is DELETE /v1/instances/{id}?confirm=<id>: irreversible.
func (c *Client) DeleteInstance(ctx context.Context, id string) (*InstanceDeletion, error) {
	var out InstanceDeletion
	if err := c.Do(ctx, http.MethodDelete, "/instances/"+esc(id), url.Values{"confirm": {id}}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetInstanceStatus is GET /v1/instances/{id}/status; fresh skips the
// API's short-lived cache.
func (c *Client) GetInstanceStatus(ctx context.Context, id string, fresh bool) (*InstanceStatus, error) {
	var s InstanceStatus
	q := url.Values{}
	if fresh {
		q.Set("fresh", "true")
	}
	if err := c.Do(ctx, http.MethodGet, "/instances/"+esc(id)+"/status", q, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// PowerInstance is POST /v1/instances/{id}/power: start, stop or reboot.
func (c *Client) PowerInstance(ctx context.Context, id, action string) (*TaskAccepted, error) {
	var out TaskAccepted
	if err := c.Do(ctx, http.MethodPost, "/instances/"+esc(id)+"/power", nil, map[string]any{"action": action}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResetInstancePassword is POST /v1/instances/{id}/reset-password: root's
// (Administrator's) password set to the one given, or made up when empty.
func (c *Client) ResetInstancePassword(ctx context.Context, id, password string) (*PasswordReset, error) {
	body := map[string]any{}
	if password != "" {
		body["password"] = password
	}
	var out PasswordReset
	if err := c.Do(ctx, http.MethodPost, "/instances/"+esc(id)+"/reset-password", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReinstallInstance is POST /v1/instances/{id}/reinstall: the instance
// rebuilt from an image, its disk replaced (202; follow the operation).
func (c *Client) ReinstallInstance(ctx context.Context, id string, body map[string]any) (*OperationAccepted, error) {
	var out OperationAccepted
	if err := c.Do(ctx, http.MethodPost, "/instances/"+esc(id)+"/reinstall", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Resize ──────────────────────────────────────────────────────────────────

// ResizeOptions is what an instance can be resized to, and what it has now
// (GET /v1/instances/{id}/resize-options).
type ResizeOptions struct {
	Resizable bool    `json:"resizable"`
	Reason    *string `json:"reason"`
	// ON_DEMAND or COMMITTED.
	CommitmentType string             `json:"commitment_type"`
	Term           *string            `json:"term"`
	CycleEndsAt    *string            `json:"cycle_ends_at"`
	Current        ResizeCurrent      `json:"current"`
	Capabilities   ResizeCapabilities `json:"capabilities"`
	Offerings      []ResizeOffering   `json:"offerings"`
	Addons         []ResizeAddon      `json:"addons"`
	IPAssignments  []IPAssignment     `json:"ip_assignments"`
	ExtraDisks     []ResizeDisk       `json:"extra_disks"`
	Currency       string             `json:"currency"`
}

// ResizeCurrent is an instance's plan and extras now.
type ResizeCurrent struct {
	OfferingCode         string `json:"offering_code"`
	OfferingOnSale       bool   `json:"offering_on_sale"`
	VCPU                 int64  `json:"vcpu"`
	RAMMB                int64  `json:"ram_mb"`
	MainDiskGB           int64  `json:"main_disk_gb"`
	ExtraIPv4            int64  `json:"extra_ipv4"`
	ExtraIPv6            int64  `json:"extra_ipv6"`
	HourlyRateMicroCents string `json:"hourly_rate_micro_cents"`
}

// ResizeCapabilities says which extras can change on the instance.
type ResizeCapabilities struct {
	Addons   bool `json:"addons"`
	ExtraIPs bool `json:"extra_ips"`
	Storage  bool `json:"storage"`
}

// ResizeOffering is a plan an instance can move to.
type ResizeOffering struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	VCPU           int64  `json:"vcpu"`
	RAMMB          int64  `json:"ram_mb"`
	MainDiskBaseGB int64  `json:"main_disk_base_gb"`
	IncludedIPv4   int64  `json:"included_ipv4"`
	IncludedIPv6   int64  `json:"included_ipv6"`
	Current        bool   `json:"current"`
}

// ResizeAddon is an add-on an instance has, or can have.
type ResizeAddon struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	CurrentQuantity int64  `json:"current_quantity"`
	MinQuantity     int64  `json:"min_quantity"`
	MaxQuantity     *int64 `json:"max_quantity"`
	QuantityStep    int64  `json:"quantity_step"`
	// A per-vCPU licence: its quantity is the plan's vCPUs.
	QuantityFollowsVCPU bool `json:"quantity_follows_vcpu"`
	// False when it may only be raised.
	Removable bool `json:"removable"`
}

// IPAssignment is one of an instance's public addresses, by the id a resize
// releases it with.
type IPAssignment struct {
	AssignmentID string `json:"assignment_id"`
	// 4 or 6.
	Version int64   `json:"version"`
	Address *string `json:"address"`
	Primary bool    `json:"primary"`
	// When it was assigned; the list is oldest first.
	AssignedAt *string `json:"assigned_at"`
}

// ResizeDisk is one of an instance's extra data disks, in order.
type ResizeDisk struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ResizeNotice is a reason a resize is refused, or a warning about it.
type ResizeNotice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResizeQuote is what a resize would cost and remove, and whether it can be
// applied now (POST /v1/instances/{id}/resize/quote; changes nothing).
type ResizeQuote struct {
	HasChanges                  bool    `json:"has_changes"`
	CanApply                    bool    `json:"can_apply"`
	CurrentHourlyRateMicroCents string  `json:"current_hourly_rate_micro_cents"`
	NewHourlyRateMicroCents     string  `json:"new_hourly_rate_micro_cents"`
	NewCycleCents               *string `json:"new_cycle_cents"`
	ChargeNowCents              string  `json:"charge_now_cents"`
	CreditCents                 string  `json:"credit_cents"`
	// What to top up before applying, on SubscriptionID; null when the
	// balance covers it.
	ShortfallCents  *string `json:"shortfall_cents"`
	SubscriptionID  *string `json:"subscription_id"`
	RequiresRestart bool    `json:"requires_restart"`
	Removes         []struct {
		// ipv4, ipv6 or extra_disk.
		Type    string `json:"type"`
		Label   string `json:"label"`
		Warning string `json:"warning"`
	} `json:"removes"`
	Violations []ResizeNotice `json:"violations"`
	Warnings   []ResizeNotice `json:"warnings"`
	Currency   string         `json:"currency"`
}

// ResizeAccepted is a resize the API took (202): billing changed, the
// hypervisor work is the operation.
type ResizeAccepted struct {
	OperationID             *string `json:"operation_id"`
	NewHourlyRateMicroCents *string `json:"new_hourly_rate_micro_cents"`
	CreditCents             string  `json:"credit_cents"`
	Planned                 *struct {
		RequiresRestart bool  `json:"requires_restart"`
		MainDiskGrowGB  int64 `json:"main_disk_grow_gb"`
		DisksAdded      int64 `json:"disks_added"`
		DisksRemoved    int64 `json:"disks_removed"`
		IPv4Added       int64 `json:"ipv4_added"`
		IPv4Removed     int64 `json:"ipv4_removed"`
		IPv6Added       int64 `json:"ipv6_added"`
		IPv6Removed     int64 `json:"ipv6_removed"`
		RebootRequired  bool  `json:"reboot_required"`
	} `json:"planned"`
}

// GetResizeOptions is GET /v1/instances/{id}/resize-options.
func (c *Client) GetResizeOptions(ctx context.Context, id string) (*ResizeOptions, error) {
	var out ResizeOptions
	if err := c.Do(ctx, http.MethodGet, "/instances/"+esc(id)+"/resize-options", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QuoteResize is POST /v1/instances/{id}/resize/quote: the price of a
// resize, and why it would be refused. It changes nothing.
func (c *Client) QuoteResize(ctx context.Context, id string, body map[string]any) (*ResizeQuote, error) {
	var out ResizeQuote
	if err := c.Do(ctx, http.MethodPost, "/instances/"+esc(id)+"/resize/quote", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResizeInstance is POST /v1/instances/{id}/resize (202; follow the
// operation). A balance that does not cover it is a 402 whose problem says
// what is short (Problem.ShortfallCents).
func (c *Client) ResizeInstance(ctx context.Context, id string, body map[string]any) (*ResizeAccepted, error) {
	var out ResizeAccepted
	if err := c.Do(ctx, http.MethodPost, "/instances/"+esc(id)+"/resize", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Backup schedule ─────────────────────────────────────────────────────────

// BackupSchedule is when an instance is backed up (it needs the backup
// add-on).
type BackupSchedule struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	// sun … sat.
	Days []string `json:"days"`
	// HH:MM, in Timezone (UTC when null).
	Time      string  `json:"time"`
	Timezone  *string `json:"timezone"`
	LastRunAt *string `json:"last_run_at"`
}

// GetBackupSchedule is GET /v1/instances/{id}/backup-schedule: 404 when the
// instance has none.
func (c *Client) GetBackupSchedule(ctx context.Context, instanceID string) (*BackupSchedule, error) {
	var out BackupSchedule
	if err := c.Do(ctx, http.MethodGet, "/instances/"+esc(instanceID)+"/backup-schedule", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetBackupSchedule is PUT /v1/instances/{id}/backup-schedule: made, or the
// one it has replaced.
func (c *Client) SetBackupSchedule(ctx context.Context, instanceID string, body map[string]any) (*BackupSchedule, error) {
	var out BackupSchedule
	if err := c.Do(ctx, http.MethodPut, "/instances/"+esc(instanceID)+"/backup-schedule", nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBackupSchedule is DELETE /v1/instances/{id}/backup-schedule: its
// backups stay.
func (c *Client) DeleteBackupSchedule(ctx context.Context, instanceID string) error {
	return c.Do(ctx, http.MethodDelete, "/instances/"+esc(instanceID)+"/backup-schedule", nil, nil, nil)
}

// ── Orders ──────────────────────────────────────────────────────────────────

// OrderPayment is the invoice an order raised, when it raised one.
type OrderPayment struct {
	InvoiceID   *string `json:"invoice_id"`
	AmountCents *string `json:"amount_cents"`
	Currency    *string `json:"currency"`
	Status      *string `json:"status"`
	// The only time the API gives a link to pay is when the order is placed.
	PayURL *string `json:"pay_url"`
	DueAt  *string `json:"due_at"`
	PaidAt *string `json:"paid_at"`
}

// Order is an instance bought: provisioned straight away from a balance, or
// waiting for an invoice to be paid.
type Order struct {
	ID *string `json:"id"`
	// pending_payment, paid, provisioning, provisioned, failed or cancelled.
	Status         string        `json:"status"`
	SubscriptionID *string       `json:"subscription_id"`
	InstanceIDs    []string      `json:"instance_ids"`
	Payment        *OrderPayment `json:"payment"`
	ExpiresAt      *string       `json:"expires_at"`
	CreatedAt      *string       `json:"created_at"`
}

// CreateOrder is POST /v1/orders: a new instance.
func (c *Client) CreateOrder(ctx context.Context, body map[string]any) (*Order, error) {
	var o Order
	if err := c.Do(ctx, http.MethodPost, "/orders", nil, body, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// GetOrder is GET /v1/orders/{id}.
func (c *Client) GetOrder(ctx context.Context, id string) (*Order, error) {
	var o Order
	if err := c.Do(ctx, http.MethodGet, "/orders/"+esc(id), nil, nil, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ── Operations ──────────────────────────────────────────────────────────────

// Operation is asynchronous work: a firewall commit, a deploy, a snapshot.
type Operation struct {
	ID string `json:"id"`
	// vm_deploy, firewall_commit…
	Type string `json:"type"`
	// pending, in_progress, completed, failed or cancelled.
	Status       string   `json:"status"`
	ResourceType *string  `json:"resource_type"`
	ResourceID   *string  `json:"resource_id"`
	Progress     *float64 `json:"progress"`
	Error        *string  `json:"error"`
	StartedAt    *string  `json:"started_at"`
	CompletedAt  *string  `json:"completed_at"`
	CreatedAt    string   `json:"created_at"`
}

// Done says whether an operation reached a state it does not leave.
func (o *Operation) Done() bool {
	return o.Status == "completed" || o.Status == "failed" || o.Status == "cancelled"
}

// OperationAccepted is what an endpoint that starts asynchronous work
// answers (202): what it started, and the operation to follow when there is
// one.
type OperationAccepted struct {
	Action       string  `json:"action"`
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`
	// The instance the work acts on: the one to watch when there is no
	// operation.
	InstanceID  *string `json:"instance_id"`
	OperationID *string `json:"operation_id"`
}

// GetOperation is GET /v1/operations/{id}.
func (c *Client) GetOperation(ctx context.Context, id string) (*Operation, error) {
	var o Operation
	if err := c.Do(ctx, http.MethodGet, "/operations/"+esc(id), nil, nil, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListActiveOperations is GET /v1/operations?status=active for one
// resource: the work still running on it.
func (c *Client) ListActiveOperations(ctx context.Context, resourceType, resourceID string) ([]Operation, error) {
	q := url.Values{"status": {"active"}, "limit": {"100"}}
	if resourceType != "" {
		q.Set("resource_type", resourceType)
	}
	if resourceID != "" {
		q.Set("resource_id", resourceID)
	}
	return All[Operation](ctx, c, "/operations", q)
}

// numericID is an id the API takes as a number in a body (a region, a VPC).
func numericID(id string) any {
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		return n
	}
	return id
}

// NumericID is numericID for the provider.
func NumericID(id string) any { return numericID(id) }
