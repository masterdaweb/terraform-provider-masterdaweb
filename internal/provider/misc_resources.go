package provider

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// ── masterdaweb_ssh_key ─────────────────────────────────────────────────────

var (
	_ resource.Resource                = (*sshKeyResource)(nil)
	_ resource.ResourceWithConfigure   = (*sshKeyResource)(nil)
	_ resource.ResourceWithImportState = (*sshKeyResource)(nil)
)

// NewSSHKeyResource is masterdaweb_ssh_key.
func NewSSHKeyResource() resource.Resource { return &sshKeyResource{} }

type sshKeyResource struct{ client *client.Client }

type sshKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	Type        types.String `tfsdk:"type"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// The key types the API takes (src/ssh-keys/ssh-public-key.ts), as sshd
// reads authorized_keys.
var sshKeyRe = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)[ \t]+[A-Za-z0-9+/]+={0,2}([ \t].*)?$`)

// sshPublicKey checks one OpenSSH public key line, around which a file()
// leaves its newline.
type sshPublicKey struct{}

func (sshPublicKey) Description(context.Context) string {
	return "must be one OpenSSH public key line, like \"ssh-ed25519 AAAA… you@laptop\""
}
func (v sshPublicKey) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v sshPublicKey) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	key := strings.TrimSpace(req.ConfigValue.ValueString())
	switch {
	case len(key) > 8192:
		resp.Diagnostics.AddAttributeError(req.Path, "Public key too long",
			"A public key line is at most 8192 characters (an 8192-bit RSA key is about 1.4 KiB).")
	case strings.Contains(key, "PRIVATE KEY"):
		resp.Diagnostics.AddAttributeError(req.Path, "A private key",
			"That is a PRIVATE key. Give the public one (the .pub file), and keep the private key to yourself.")
	case strings.ContainsAny(key, "\r\n") || !sshKeyRe.MatchString(key):
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid public key",
			"The public key "+v.Description(ctx)+": ssh-ed25519, ssh-rsa (2048 bits or more), ecdsa-sha2-nistp256/384/521 "+
				"or a security key's, with its base64 after the type.")
	}
}

func (r *sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An SSH public key of the account, put on Linux instances for root when they are created " +
			"(masterdaweb_instance.ssh_key_ids).",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The key's id."),
			"name": schema.StringAttribute{
				Required: true, Description: "The key's name, up to 64 characters. Changed in place.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"public_key": schema.StringAttribute{
				Required: true,
				Description: "The public key, one OpenSSH line (ssh-ed25519 AAAA… comment), like " +
					"file(\"~/.ssh/id_ed25519.pub\"): Ed25519, RSA of 2048 bits or more, ECDSA, or a security key's. " +
					"An account keeps a key once. Changing it replaces the key.",
				Validators:    []validator.String{sshPublicKey{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type":        computedString("The key's type, like ssh-ed25519."),
			"fingerprint": computedString("The key's SHA256 fingerprint, as ssh-keygen -lf shows it."),
			"created_at":  computedString("When the key was added (RFC 3339)."),
		},
	}
}

func (r *sshKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// sameKey says whether two public keys are the same key (type and data;
// the comment aside).
func sameKey(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	return len(fa) >= 2 && len(fb) >= 2 && fa[0] == fb[0] && fa[1] == fb[1]
}

func sshKeyFromAPI(prior sshKeyModel, k *client.SSHKey) sshKeyModel {
	m := sshKeyModel{
		ID: types.StringValue(k.ID), Name: types.StringValue(k.Name), PublicKey: types.StringValue(k.PublicKey),
		Type: types.StringValue(k.Type), Fingerprint: types.StringValue(k.Fingerprint), CreatedAt: stringOrNull(k.CreatedAt),
	}
	if known(prior.PublicKey) && sameKey(prior.PublicKey.ValueString(), k.PublicKey) {
		m.PublicKey = prior.PublicKey
	}
	if known(prior.Name) && strings.TrimSpace(prior.Name.ValueString()) == k.Name {
		m.Name = prior.Name
	}
	return m
}

func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := r.client.CreateSSHKey(ctx, plan.Name.ValueString(), strings.TrimSpace(plan.PublicKey.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError("Could not add the SSH key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyFromAPI(plan, k))...)
}

func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := r.client.GetSSHKey(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the SSH key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyFromAPI(state, k))...)
}

func (r *sshKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := r.client.RenameSSHKey(ctx, state.ID.ValueString(), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not rename the SSH key", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyFromAPI(plan, k))...)
}

func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSSHKey(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the SSH key", err.Error())
	}
}

func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ── masterdaweb_reverse_dns ─────────────────────────────────────────────────

var (
	_ resource.Resource                = (*reverseDNSResource)(nil)
	_ resource.ResourceWithConfigure   = (*reverseDNSResource)(nil)
	_ resource.ResourceWithImportState = (*reverseDNSResource)(nil)
)

// NewReverseDNSResource is masterdaweb_reverse_dns.
func NewReverseDNSResource() resource.Resource { return &reverseDNSResource{} }

type reverseDNSResource struct{ client *client.Client }

type reverseDNSModel struct {
	ID              types.String `tfsdk:"id"`
	IP              types.String `tfsdk:"ip"`
	Hostname        types.String `tfsdk:"hostname"`
	Version         types.Int64  `tfsdk:"version"`
	InstanceID      types.String `tfsdk:"instance_id"`
	DefaultHostname types.String `tfsdk:"default_hostname"`
}

func (r *reverseDNSResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_reverse_dns"
}

func (r *reverseDNSResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The reverse DNS (PTR) of one of the account's addresses. The hostname must resolve to the " +
			"address first (forward-confirmed): create its A/AAAA record before. Destroying it puts the default back.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The address."),
			"ip": schema.StringAttribute{
				Required: true, Description: "The IPv4 or IPv6 address, one of the account's. Changing it replaces the resource.",
				Validators:    []validator.String{ipAddress{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"hostname": schema.StringAttribute{
				Required: true,
				Description: "The fully-qualified name the address answers with, like mail.example.com. It must " +
					"resolve to the address. Changed in place.",
				Validators: []validator.String{stringvalidator.LengthBetween(3, 253)},
			},
			"version":          schema.Int64Attribute{Computed: true, Description: "4 or 6."},
			"instance_id":      schema.StringAttribute{Computed: true, Description: "The instance the address is on."},
			"default_hostname": schema.StringAttribute{Computed: true, Description: "The name it answers with by default."},
		},
	}
}

// ipAddress checks an IPv4 or IPv6 address.
type ipAddress struct{}

func (ipAddress) Description(context.Context) string { return "must be an IPv4 or IPv6 address" }
func (v ipAddress) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v ipAddress) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	if _, err := netip.ParseAddr(strings.TrimSpace(req.ConfigValue.ValueString())); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid address", fmt.Sprintf("%q %s.", req.ConfigValue.ValueString(), v.Description(ctx)))
	}
}

func (r *reverseDNSResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func normalizeIP(s string) string {
	if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
		return a.String()
	}
	return strings.TrimSpace(s)
}

func reverseDNSFromAPI(prior reverseDNSModel, rr *client.ReverseDNS) reverseDNSModel {
	m := prior
	m.ID = types.StringValue(normalizeIP(rr.IP))
	if !(known(prior.IP) && normalizeIP(prior.IP.ValueString()) == normalizeIP(rr.IP)) {
		m.IP = types.StringValue(rr.IP)
	}
	m.Hostname = stringOrNull(rr.Hostname)
	if known(prior.Hostname) && rr.Hostname != nil && normalizeHostname(prior.Hostname.ValueString()) == normalizeHostname(*rr.Hostname) {
		m.Hostname = prior.Hostname
	}
	m.Version = types.Int64Value(rr.Version)
	m.InstanceID = stringOrNull(rr.InstanceID)
	m.DefaultHostname = stringOrNull(rr.DefaultHostname)
	return m
}

func (r *reverseDNSResource) find(ctx context.Context, ip string) (*client.ReverseDNS, error) {
	all, err := r.client.ListReverseDNS(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if normalizeIP(all[i].IP) == normalizeIP(ip) {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (r *reverseDNSResource) set(ctx context.Context, plan reverseDNSModel, state *tfsdkState, diags *diagnostics) {
	rr, err := r.client.SetReverseDNS(ctx, normalizeIP(plan.IP.ValueString()), normalizeHostname(plan.Hostname.ValueString()))
	if err != nil {
		diags.AddError("Could not set the reverse DNS", err.Error())
		return
	}
	diags.Append(state.Set(ctx, reverseDNSFromAPI(plan, rr))...)
}

func (r *reverseDNSResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan reverseDNSModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.set(ctx, plan, &resp.State, &resp.Diagnostics)
}

func (r *reverseDNSResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state reverseDNSModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rr, err := r.find(ctx, state.IP.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the reverse DNS", err.Error())
		return
	}
	if rr == nil {
		// The address left the account.
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, reverseDNSFromAPI(state, rr))...)
}

func (r *reverseDNSResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan reverseDNSModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.set(ctx, plan, &resp.State, &resp.Diagnostics)
}

func (r *reverseDNSResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state reverseDNSModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.ResetReverseDNS(ctx, normalizeIP(state.IP.ValueString())); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not reset the reverse DNS", err.Error())
	}
}

func (r *reverseDNSResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ip := normalizeIP(req.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), ip)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ip"), ip)...)
}

// ── masterdaweb_snapshot ────────────────────────────────────────────────────

var (
	_ resource.Resource                = (*snapshotResource)(nil)
	_ resource.ResourceWithConfigure   = (*snapshotResource)(nil)
	_ resource.ResourceWithImportState = (*snapshotResource)(nil)
)

// NewSnapshotResource is masterdaweb_snapshot.
func NewSnapshotResource() resource.Resource { return &snapshotResource{} }

type snapshotResource struct{ client *client.Client }

type snapshotModel struct {
	ID          types.String   `tfsdk:"id"`
	InstanceID  types.String   `tfsdk:"instance_id"`
	Name        types.String   `tfsdk:"name"`
	Description types.String   `tfsdk:"description"`
	IncludeRAM  types.Bool     `tfsdk:"include_ram"`
	CreatedAt   types.String   `tfsdk:"created_at"`
	ExpiresAt   types.String   `tfsdk:"expires_at"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

const defaultSnapshotTimeout = 20 * time.Minute

// The names the API takes (CreateSnapshotV1Dto).
var snapshotNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,39}$`)

func (r *snapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot"
}

func (r *snapshotResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A snapshot of an instance, to restore it to this point, within the plan's snapshot quota. " +
			"Snapshots expire 30 days after they are taken: one that expired is gone from the state, and the next " +
			"apply takes it again. Restoring is done in the console or with the API (it deletes the snapshots newer " +
			"than the one restored).",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The snapshot's id."),
			"instance_id": schema.StringAttribute{
				Required: true, Description: "The instance. Changing it takes a new snapshot.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "The snapshot's name: a letter, then 1 to 39 letters, digits, - or _, unique on the " +
					"instance. Changing it takes a new snapshot.",
				Validators:    []validator.String{stringvalidator.RegexMatches(snapshotNameRe, "must start with a letter and have 2 to 40 letters, digits, - or _")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional: true, Description: "A note on the snapshot, up to 255 characters. Changing it takes a new snapshot.",
				Validators:    []validator.String{stringvalidator.LengthAtMost(255)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"include_ram": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:       booldefault.StaticBool(false),
				Description:   "Whether the memory is in the snapshot too (restoring brings the instance back running). Changing it takes a new snapshot.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"created_at": computedString("When the snapshot was taken (RFC 3339)."),
			"expires_at": computedString("When the snapshot expires (RFC 3339), 30 days after it was taken."),
			"timeouts": timeoutsAttribute(ctx, timeouts.Opts{
				Create: true, Delete: true,
				CreateDescription: "How long to keep asking while another operation holds the instance (default 20m).",
				DeleteDescription: "How long to keep asking while another operation holds the instance (default 20m).",
			}),
		},
	}
}

func (r *snapshotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *snapshotResource) find(ctx context.Context, instanceID, id string) (*client.Snapshot, error) {
	list, err := r.client.ListSnapshots(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	for i := range list.Data {
		if list.Data[i].ID == id {
			return &list.Data[i], nil
		}
	}
	return nil, nil
}

func snapshotFromAPI(prior snapshotModel, s *client.Snapshot) snapshotModel {
	m := prior
	m.ID = types.StringValue(s.ID)
	m.InstanceID = types.StringValue(s.InstanceID)
	m.Name = types.StringValue(s.Name)
	m.Description = keepEquivalent(prior.Description, s.Description)
	m.IncludeRAM = types.BoolValue(s.IncludeRAM)
	m.CreatedAt = stringOrNull(s.CreatedAt)
	m.ExpiresAt = stringOrNull(s.ExpiresAt)
	return m
}

func (r *snapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan snapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultSnapshotTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "include_ram": plan.IncludeRAM.ValueBool()}
	if known(plan.Description) && plan.Description.ValueString() != "" {
		body["description"] = plan.Description.ValueString()
	}
	var snap *client.Snapshot
	err := whileBusy(ctx, timeout, func() error {
		s, err := r.client.CreateSnapshot(ctx, plan.InstanceID.ValueString(), body)
		snap = s
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not take the snapshot", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, snapshotFromAPI(plan, snap))...)
}

func (r *snapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state snapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.find(ctx, state.InstanceID.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) || (err == nil && s == nil) {
		// Expired, deleted, or its instance is gone.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the snapshot", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, snapshotFromAPI(state, s))...)
}

func (r *snapshotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every attribute replaces the snapshot but the timeouts.
	var plan snapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *snapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state snapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, defaultSnapshotTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := whileBusy(ctx, timeout, func() error {
		return r.client.DeleteSnapshot(ctx, state.InstanceID.ValueString(), state.ID.ValueString())
	})
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the snapshot", err.Error())
	}
}

func (r *snapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(strings.TrimSpace(req.ID), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Unexpected import id",
			fmt.Sprintf("A snapshot is imported as <instance_id>/<snapshot_id>, not %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

// ── masterdaweb_object_storage_key ──────────────────────────────────────────

var (
	_ resource.Resource                = (*storageKeyResource)(nil)
	_ resource.ResourceWithConfigure   = (*storageKeyResource)(nil)
	_ resource.ResourceWithImportState = (*storageKeyResource)(nil)
)

// NewStorageKeyResource is masterdaweb_object_storage_key.
func NewStorageKeyResource() resource.Resource { return &storageKeyResource{} }

type storageKeyResource struct{ client *client.Client }

type storageKeyModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Access          types.String `tfsdk:"access"`
	Buckets         types.Set    `tfsdk:"buckets"`
	ExpiresAt       types.String `tfsdk:"expires_at"`
	AccessKeyID     types.String `tfsdk:"access_key_id"`
	SecretAccessKey types.String `tfsdk:"secret_access_key"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

func (r *storageKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_object_storage_key"
}

func (r *storageKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An S3 access key to the account's buckets, for tools outside the platform (a service gets its " +
			"own through a binding). Its secret is shown once, when it is made: Terraform keeps it in the state " +
			"(sensitive). Every change makes a new key.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The key's id."),
			"name": schema.StringAttribute{
				Required: true, Description: "The key's name, up to 64 characters.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 64)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"access": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:       stringdefault.StaticString("read_write"),
				Description:   "read_write or read_only.",
				Validators:    []validator.String{stringvalidator.OneOf("read_write", "read_only")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"buckets": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				Description:   "The buckets it reaches, by name; every bucket of the account (present and future) when left out.",
				Validators:    []validator.Set{setvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			},
			"expires_at": schema.StringAttribute{
				Optional: true, Description: "When the key stops working (RFC 3339); never when left out.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"access_key_id": computedString("The access key id (the S3 user)."),
			"secret_access_key": schema.StringAttribute{
				Computed: true, Sensitive: true,
				Description:   "The secret, shown once by the API (not imported).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": computedString("When the key was made (RFC 3339)."),
		},
	}
}

func (r *storageKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func storageKeyFromAPI(prior storageKeyModel, k *client.AccessKey) storageKeyModel {
	m := prior
	m.ID = types.StringValue(k.ID)
	m.Name = types.StringValue(k.Name)
	if known(prior.Name) && strings.TrimSpace(prior.Name.ValueString()) == k.Name {
		m.Name = prior.Name
	}
	m.Access = types.StringValue(k.Access)
	if k.Buckets == nil {
		m.Buckets = types.SetNull(types.StringType)
	} else {
		m.Buckets = stringSet(append([]string{}, k.Buckets...))
	}
	if !(known(prior.ExpiresAt) && sameInstant(prior.ExpiresAt.ValueString(), deref(k.ExpiresAt))) {
		m.ExpiresAt = stringOrNull(k.ExpiresAt)
	}
	m.AccessKeyID = types.StringValue(k.AccessKeyID)
	m.CreatedAt = types.StringValue(k.CreatedAt)
	if m.SecretAccessKey.IsUnknown() {
		m.SecretAccessKey = types.StringNull()
	}
	return m
}

// sameInstant says whether two RFC 3339 times are the same moment.
func sameInstant(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339, a)
	tb, err2 := time.Parse(time.RFC3339, b)
	return err1 == nil && err2 == nil && ta.Equal(tb)
}

func (r *storageKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan storageKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "access": plan.Access.ValueString()}
	if buckets := listStrings(ctx, plan.Buckets); buckets != nil {
		body["buckets"] = buckets
	}
	if known(plan.ExpiresAt) {
		body["expires_at"] = plan.ExpiresAt.ValueString()
	}
	k, err := r.client.CreateAccessKey(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not make the access key", err.Error())
		return
	}
	m := storageKeyFromAPI(plan, &k.Key)
	m.SecretAccessKey = types.StringValue(k.SecretAccessKey)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *storageKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state storageKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	keys, err := r.client.ListAccessKeys(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the access keys", err.Error())
		return
	}
	for i := range keys {
		if keys[i].ID == state.ID.ValueString() {
			resp.Diagnostics.Append(resp.State.Set(ctx, storageKeyFromAPI(state, &keys[i]))...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *storageKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan storageKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *storageKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state storageKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.RevokeAccessKey(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not revoke the access key", err.Error())
	}
}

func (r *storageKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ── masterdaweb_project_resource ────────────────────────────────────────────

var (
	_ resource.Resource                = (*projectMembershipResource)(nil)
	_ resource.ResourceWithConfigure   = (*projectMembershipResource)(nil)
	_ resource.ResourceWithImportState = (*projectMembershipResource)(nil)
)

// NewProjectMembershipResource is masterdaweb_project_resource.
func NewProjectMembershipResource() resource.Resource { return &projectMembershipResource{} }

type projectMembershipResource struct{ client *client.Client }

type projectMembershipModel struct {
	ID         types.String `tfsdk:"id"`
	ProjectID  types.String `tfsdk:"project_id"`
	Kind       types.String `tfsdk:"kind"`
	ResourceID types.String `tfsdk:"resource_id"`
	Name       types.String `tfsdk:"name"`
	Status     types.String `tfsdk:"status"`
}

func (r *projectMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_resource"
}

func (r *projectMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An instance, a database or a bucket in a project (masterdaweb_project). A resource is in one " +
			"project at most: putting it in one moves it from the other. Destroying this takes the resource out of " +
			"the project; the resource itself is not touched.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("<project_id>/<kind>/<resource_id>."),
			"project_id": schema.StringAttribute{
				Required: true, Description: "The project. Changing it moves the resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"kind": schema.StringAttribute{
				Required: true, Description: "INSTANCE, BUCKET or DATABASE.",
				Validators:    []validator.String{stringvalidator.OneOf("INSTANCE", "BUCKET", "DATABASE")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_id": schema.StringAttribute{
				Required:      true,
				Description:   "The instance's id, or the bucket's or the database's id or name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name":   computedString("The resource's name."),
			"status": schema.StringAttribute{Computed: true, Description: "The resource's status."},
		},
	}
}

func (r *projectMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// findMember is the resource in the project's list: by its id, or by its
// name for a bucket or a database given by name.
func findMember(list []client.ProjectResource, kind, ref string) *client.ProjectResource {
	for i := range list {
		if list[i].Kind == kind && (list[i].ID == ref || (kind != "INSTANCE" && list[i].Name == ref)) {
			return &list[i]
		}
	}
	return nil
}

func membershipFromAPI(prior projectMembershipModel, pr *client.ProjectResource) projectMembershipModel {
	m := prior
	m.ID = types.StringValue(prior.ProjectID.ValueString() + "/" + prior.Kind.ValueString() + "/" + prior.ResourceID.ValueString())
	m.Name = types.StringValue(pr.Name)
	m.Status = types.StringValue(pr.Status)
	return m
}

func (r *projectMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := r.client.AddProjectResource(ctx, plan.ProjectID.ValueString(), plan.Kind.ValueString(), plan.ResourceID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not put the resource in the project", err.Error())
		return
	}
	pr := findMember(list, plan.Kind.ValueString(), plan.ResourceID.ValueString())
	if pr == nil {
		resp.Diagnostics.AddError("The resource is not in the project",
			"The API took the resource but its project does not list it.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, membershipFromAPI(plan, pr))...)
}

func (r *projectMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := r.client.ListProjectResources(ctx, state.ProjectID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the project's resources", err.Error())
		return
	}
	pr := findMember(list, state.Kind.ValueString(), state.ResourceID.ValueString())
	if pr == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, membershipFromAPI(state, pr))...)
}

func (r *projectMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan projectMembershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *projectMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectMembershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.RemoveProjectResource(ctx, state.ProjectID.ValueString(), state.Kind.ValueString(), state.ResourceID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not take the resource out of the project", err.Error())
	}
}

func (r *projectMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(strings.TrimSpace(req.ID), "/")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Unexpected import id",
			fmt.Sprintf("A project resource is imported as <project_id>/<kind>/<resource_id>, like 0190…/INSTANCE/1234, not %q.", req.ID))
		return
	}
	kind := strings.ToUpper(parts[1])
	if kind != "INSTANCE" && kind != "BUCKET" && kind != "DATABASE" {
		resp.Diagnostics.AddError("Unexpected kind", fmt.Sprintf("The kind is INSTANCE, BUCKET or DATABASE, not %q.", parts[1]))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+kind+"/"+parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("kind"), kind)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_id"), parts[2])...)
}
