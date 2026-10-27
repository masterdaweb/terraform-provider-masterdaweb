package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// VPN gateways are instances ordered from the VPN gateway image; what
// Terraform manages of them is who can connect (masterdaweb_vpn_user) and
// their settings (masterdaweb_vpn_gateway_settings). Both change the record
// first and the running gateway on an apply (POST …/apply), as the console
// does: each resource applies after its own change, in place.

// How long a change waits for the gateway to apply it. A rebuild reinstalls
// the machine, so the settings wait longer.
const (
	defaultVPNUserTimeout     = 15 * time.Minute
	defaultVPNSettingsTimeout = 30 * time.Minute
)

var (
	vpnUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9]{1,64}$`)
	emailRe       = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	cidrRe        = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}/\d{1,2}$`)
)

// applyVPNChanges puts a gateway's pending changes in place and waits for the
// operation carrying them. rebuildRequired says the changes need a rebuild
// (minutes offline, everyone disconnected) and allowRebuild did not allow
// one: nothing was applied.
func applyVPNChanges(ctx context.Context, c *client.Client, gatewayID string, allowRebuild bool, timeout time.Duration) (rebuildRequired bool, err error) {
	var applied *client.VPNGatewayApply
	start := func(rebuild bool) error {
		// Another operation holding the gateway's instance (a backup, a
		// snapshot) makes the API answer 409 VM_OPERATION_IN_PROGRESS.
		return whileBusy(ctx, timeout, func() error {
			a, err := c.ApplyVPNGateway(ctx, gatewayID, rebuild)
			applied = a
			return err
		})
	}
	err = start(false)
	if client.HasReason(err, client.VPNGatewayRebuildRequired) {
		if !allowRebuild {
			return true, nil
		}
		err = start(true)
	}
	if err != nil {
		return false, err
	}
	if applied != nil && applied.OperationID != nil && *applied.OperationID != "" {
		return false, waitForOperation(ctx, c, *applied.OperationID, timeout)
	}
	return false, nil
}

// applyAfterLoginChange applies a change to a gateway's logins. The change is
// saved already, so a gateway that does not take it is a warning: an error
// would taint the login, and its replacement would lose a generated password.
func applyAfterLoginChange(ctx context.Context, c *client.Client, gatewayID string, timeout time.Duration, diags *diag.Diagnostics, what string) {
	rebuild, err := applyVPNChanges(ctx, c, gatewayID, false, timeout)
	switch {
	case rebuild:
		diags.AddWarning("Saved, but not applied",
			what+" was saved, but the gateway needs a rebuild to apply it — a few minutes offline, everyone "+
				"disconnected —, which a change to a login never starts. Apply it from the gateway's page in the "+
				"console, or with masterdaweb_vpn_gateway_settings and allow_rebuild = true.")
	case err != nil:
		diags.AddWarning("Saved, but not applied",
			what+" was saved, but the gateway did not apply it: "+err.Error()+". It reaches the gateway with its "+
				"next apply: masterdaweb_vpn_gateway_settings applies pending changes on every run, as does the "+
				"console's Apply.")
	}
}

// ── masterdaweb_vpn_user ────────────────────────────────────────────────────

var (
	_ resource.Resource                = (*vpnUserResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpnUserResource)(nil)
	_ resource.ResourceWithImportState = (*vpnUserResource)(nil)
)

// NewVPNUserResource is masterdaweb_vpn_user.
func NewVPNUserResource() resource.Resource { return &vpnUserResource{} }

type vpnUserResource struct{ client *client.Client }

type vpnUserModel struct {
	ID              types.String   `tfsdk:"id"`
	GatewayID       types.String   `tfsdk:"gateway_id"`
	Username        types.String   `tfsdk:"username"`
	Email           types.String   `tfsdk:"email"`
	Password        types.String   `tfsdk:"password"`
	Status          types.String   `tfsdk:"status"`
	SendCredentials types.Bool     `tfsdk:"send_credentials"`
	CreatedAt       types.String   `tfsdk:"created_at"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *vpnUserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_user"
}

func (r *vpnUserResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A login on a VPN gateway: a person who connects with the VPN app, with their own username and " +
			"password. Every change is applied to the gateway in place right after it is saved (the gateway's " +
			"other pending changes go with it); when applying needs a rebuild, the change stays saved and a warning " +
			"says how to apply it.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The login's id."),
			"gateway_id": schema.StringAttribute{
				Required: true, Description: "The VPN gateway (masterdaweb_vpn_gateway). Changing it replaces the login.",
				Validators:    []validator.String{stringvalidator.RegexMatches(uuidRe, "must be a VPN gateway's id (a UUID)")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"username": schema.StringAttribute{
				Required: true, Description: "What the person signs in with: letters and digits, unique on the gateway. Changed in place.",
				Validators: []validator.String{stringvalidator.RegexMatches(vpnUsernameRe, "must be letters and digits only, up to 64")},
			},
			"email": schema.StringAttribute{
				Required: true, Description: "The person's email, unique on the gateway: where send_credentials sends their access. Changed in place.",
				Validators: []validator.String{
					stringvalidator.LengthAtMost(255),
					stringvalidator.RegexMatches(emailRe, "must be an email address"),
				},
			},
			"password": schema.StringAttribute{
				Optional: true, Computed: true, Sensitive: true,
				Description: "The login's password, 8 to 128 characters. Left out, the platform generates one and it is " +
					"kept here, in the state — the API shows it once and never again (not imported). Changing it sets " +
					"a new one in place.",
				Validators:    []validator.String{stringvalidator.LengthBetween(8, 128)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:     stringdefault.StaticString("active"),
				Description: "active, or inactive: the login is kept and refused. Changed in place.",
				Validators:  []validator.String{stringvalidator.OneOf("active", "inactive")},
			},
			"send_credentials": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
				Description: "Whether the person is emailed their username, password and how to connect: when the login " +
					"is made, and when its username, email or password changes.",
			},
			"created_at": computedString("When the login was made (RFC 3339)."),
			"timeouts": timeoutsAttribute(ctx, timeouts.Opts{
				Create: true, Update: true, Delete: true,
				CreateDescription: "How long to wait for the gateway to apply the new login (default 15m).",
				UpdateDescription: "How long to wait for the gateway to apply a change (default 15m).",
				DeleteDescription: "How long to wait for the gateway to apply the removal (default 15m).",
			}),
		},
	}
}

func (r *vpnUserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func vpnUserFromAPI(prior vpnUserModel, u *client.VPNUser) vpnUserModel {
	m := prior
	m.ID = types.StringValue(u.ID)
	m.GatewayID = types.StringValue(u.GatewayID)
	m.Username = types.StringValue(u.Username)
	if !(known(prior.Email) && strings.EqualFold(prior.Email.ValueString(), u.Email)) {
		m.Email = types.StringValue(u.Email)
	}
	m.Status = types.StringValue(u.Status)
	m.CreatedAt = stringOrNull(u.CreatedAt)
	if m.Password.IsUnknown() {
		m.Password = types.StringNull()
	}
	if m.SendCredentials.IsNull() || m.SendCredentials.IsUnknown() {
		m.SendCredentials = types.BoolValue(false)
	}
	return m
}

func (r *vpnUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpnUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultVPNUserTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	gatewayID := plan.GatewayID.ValueString()
	body := map[string]any{
		"username": plan.Username.ValueString(),
		"email":    strings.TrimSpace(plan.Email.ValueString()),
		"status":   plan.Status.ValueString(),
	}
	if known(plan.Password) {
		body["password"] = plan.Password.ValueString()
	}
	if plan.SendCredentials.ValueBool() {
		body["send_credentials"] = true
	}
	u, err := r.client.CreateVPNUser(ctx, gatewayID, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not add the VPN login", err.Error())
		return
	}
	m := vpnUserFromAPI(plan, u)
	if u.Password != nil {
		m.Password = types.StringValue(*u.Password)
	}
	// Saved first: the login exists whatever the gateway does next.
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
	applyAfterLoginChange(ctx, r.client, gatewayID, timeout, &resp.Diagnostics, "The login "+u.Username)
}

func (r *vpnUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpnUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.client.GetVPNUser(ctx, state.GatewayID.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		// Removed, or gone with its gateway.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the VPN login", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpnUserFromAPI(state, u))...)
}

func (r *vpnUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vpnUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, defaultVPNUserTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := map[string]any{}
	credentials := false
	if !plan.Username.Equal(state.Username) {
		patch["username"], credentials = plan.Username.ValueString(), true
	}
	if !plan.Email.Equal(state.Email) {
		patch["email"], credentials = strings.TrimSpace(plan.Email.ValueString()), true
	}
	if known(plan.Password) && !plan.Password.Equal(state.Password) {
		patch["password"], credentials = plan.Password.ValueString(), true
	}
	if !plan.Status.Equal(state.Status) {
		patch["status"] = plan.Status.ValueString()
	}
	if len(patch) == 0 {
		// Only send_credentials or the timeouts: how the next change is made.
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		return
	}
	if credentials && plan.SendCredentials.ValueBool() {
		patch["send_credentials"] = true
	}
	gatewayID := state.GatewayID.ValueString()
	u, err := r.client.UpdateVPNUser(ctx, gatewayID, state.ID.ValueString(), patch)
	if err != nil {
		resp.Diagnostics.AddError("Could not change the VPN login", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpnUserFromAPI(plan, u))...)
	applyAfterLoginChange(ctx, r.client, gatewayID, timeout, &resp.Diagnostics, "The change to the login "+u.Username)
}

func (r *vpnUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpnUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, defaultVPNUserTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	gatewayID := state.GatewayID.ValueString()
	err := r.client.DeleteVPNUser(ctx, gatewayID, state.ID.ValueString())
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not remove the VPN login", err.Error())
		return
	}
	applyAfterLoginChange(ctx, r.client, gatewayID, timeout, &resp.Diagnostics, "The removal of the login "+state.Username.ValueString())
}

func (r *vpnUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(strings.TrimSpace(req.ID), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Unexpected import id",
			fmt.Sprintf("A VPN login is imported as <gateway_id>/<login_id>, not %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("send_credentials"), false)...)
}

// ── masterdaweb_vpn_gateway_settings ───────────────────────────────────────

var (
	_ resource.Resource                = (*vpnGatewaySettingsResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpnGatewaySettingsResource)(nil)
	_ resource.ResourceWithImportState = (*vpnGatewaySettingsResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*vpnGatewaySettingsResource)(nil)
)

// NewVPNGatewaySettingsResource is masterdaweb_vpn_gateway_settings.
func NewVPNGatewaySettingsResource() resource.Resource { return &vpnGatewaySettingsResource{} }

type vpnGatewaySettingsResource struct{ client *client.Client }

type vpnGatewaySettingsModel struct {
	ID                  types.String   `tfsdk:"id"`
	GatewayID           types.String   `tfsdk:"gateway_id"`
	Name                types.String   `tfsdk:"name"`
	Protocol            types.String   `tfsdk:"protocol"`
	RemoteAccessPool    types.String   `tfsdk:"remote_access_pool"`
	RouteAllTraffic     types.Bool     `tfsdk:"route_all_traffic"`
	AllowSharedLogins   types.Bool     `tfsdk:"allow_shared_logins"`
	MTU                 types.Int64    `tfsdk:"mtu"`
	AllowRebuild        types.Bool     `tfsdk:"allow_rebuild"`
	PendingChanges      types.Bool     `tfsdk:"pending_changes"`
	InstanceID          types.String   `tfsdk:"instance_id"`
	PublicIP            types.String   `tfsdk:"public_ip"`
	ApplyMode           types.String   `tfsdk:"apply_mode"`
	WireguardPublicKey  types.String   `tfsdk:"wireguard_public_key"`
	WireguardListenPort types.Int64    `tfsdk:"wireguard_listen_port"`
	Timeouts            timeouts.Value `tfsdk:"timeouts"`
}

func (r *vpnGatewaySettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_gateway_settings"
}

func (r *vpnGatewaySettingsResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The settings of an existing VPN gateway — the instance ordered from the VPN gateway image —, " +
			"applied to it: Terraform saves them and applies them in place, waiting for the gateway. Whenever the " +
			"gateway has changes waiting to be applied (made in the console, or a login whose apply did not go " +
			"through), the next apply applies them too. A setting left out is the gateway's own. Destroying this " +
			"resource stops managing the settings; the gateway keeps them.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The gateway's id."),
			"gateway_id": schema.StringAttribute{
				Required: true, Description: "The VPN gateway (masterdaweb_vpn_gateway). Changing it manages another gateway.",
				Validators:    []validator.String{stringvalidator.RegexMatches(uuidRe, "must be a VPN gateway's id (a UUID)")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "The gateway's name: its instance's, renamed at once (no apply).",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 255)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"protocol": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "The VPN app's transport: udp, or tcp for networks that block UDP.",
				Validators:    []validator.String{stringvalidator.OneOf("udp", "tcp")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"remote_access_pool": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The addresses people connecting get, an IPv4 network like 10.8.0.0/24 that overlaps " +
					"neither the gateway's private network nor a connected site. Changing it renumbers connected " +
					"people; the profiles they hold keep working.",
				Validators:    []validator.String{stringvalidator.RegexMatches(cidrRe, "must be an IPv4 network like 10.8.0.0/24")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"route_all_traffic": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description: "Whether all of a connected person's traffic goes through the gateway, not only the " +
					"private network's. Changing it needs a rebuild (allow_rebuild).",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"allow_shared_logins": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description:   "Whether one login may be connected from several devices at once.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"mtu": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description:   "The tunnel's MTU, 576 to 1500 (1420 by default, for networks behind carrier-grade NAT).",
				Validators:    []validator.Int64{int64validator.Between(576, 1500)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"allow_rebuild": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
				Description: "Whether an apply that needs a rebuild — a few minutes offline, everyone disconnected — " +
					"may rebuild the gateway. Left false, such an apply fails, saying so, and the changes stay saved.",
			},
			"pending_changes": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the gateway has changes waiting to be applied. When it has, the next plan " +
					"applies them.",
			},
			"instance_id": computedString("The instance the gateway runs on."),
			"public_ip": schema.StringAttribute{
				Computed: true, Description: "The address people and sites connect to.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"apply_mode": schema.StringAttribute{
				Computed: true, Description: "What the next apply does: in_place, or rebuild.",
			},
			"wireguard_public_key": schema.StringAttribute{
				Computed: true, Description: "The gateway's WireGuard public key, for the far end of a WireGuard site connection.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"wireguard_listen_port": schema.Int64Attribute{
				Computed: true, Description: "The UDP port WireGuard site connections reach.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"timeouts": timeoutsAttribute(ctx, timeouts.Opts{
				Create: true, Update: true,
				CreateDescription: "How long to wait for the gateway to apply the settings (default 30m).",
				UpdateDescription: "How long to wait for the gateway to apply a change (default 30m).",
			}),
		},
	}
}

func (r *vpnGatewaySettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan plans an apply while the gateway has changes waiting for one.
func (r *vpnGatewaySettingsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var pending types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("pending_changes"), &pending)...)
	if pending.ValueBool() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("pending_changes"), false)...)
	}
}

func gatewaySettingsFromAPI(prior vpnGatewaySettingsModel, g *client.VPNGateway) vpnGatewaySettingsModel {
	m := prior
	m.ID = types.StringValue(g.ID)
	m.GatewayID = types.StringValue(g.ID)
	m.Name = keepEquivalent(prior.Name, g.Name)
	m.Protocol = types.StringValue(g.Protocol)
	switch {
	case g.RemoteAccessPool == nil:
		m.RemoteAccessPool = types.StringNull()
	case known(prior.RemoteAccessPool) && normalizeCIDR(prior.RemoteAccessPool.ValueString()) == normalizeCIDR(*g.RemoteAccessPool):
	default:
		m.RemoteAccessPool = types.StringValue(*g.RemoteAccessPool)
	}
	m.RouteAllTraffic = types.BoolValue(g.RouteAllTraffic)
	m.AllowSharedLogins = types.BoolValue(g.AllowSharedLogins)
	m.MTU = types.Int64Value(g.MTU)
	if m.AllowRebuild.IsNull() || m.AllowRebuild.IsUnknown() {
		m.AllowRebuild = types.BoolValue(false)
	}
	m.PendingChanges = types.BoolValue(g.PendingChanges)
	m.InstanceID = types.StringValue(g.InstanceID)
	m.PublicIP = stringOrNull(g.PublicIP)
	m.ApplyMode = types.StringValue(g.ApplyMode)
	m.WireguardPublicKey = stringOrNull(g.Wireguard.PublicKey)
	m.WireguardListenPort = types.Int64Value(g.Wireguard.ListenPort)
	return m
}

// settingsPatch is what the configuration asks of the gateway that it does
// not have yet.
func settingsPatch(plan vpnGatewaySettingsModel, g *client.VPNGateway) map[string]any {
	patch := map[string]any{}
	if known(plan.Name) && strings.TrimSpace(plan.Name.ValueString()) != deref(g.Name) {
		patch["name"] = strings.TrimSpace(plan.Name.ValueString())
	}
	if known(plan.Protocol) && plan.Protocol.ValueString() != g.Protocol {
		patch["protocol"] = plan.Protocol.ValueString()
	}
	if known(plan.RemoteAccessPool) && normalizeCIDR(plan.RemoteAccessPool.ValueString()) != normalizeCIDR(deref(g.RemoteAccessPool)) {
		patch["remote_access_pool"] = strings.TrimSpace(plan.RemoteAccessPool.ValueString())
	}
	if known(plan.RouteAllTraffic) && plan.RouteAllTraffic.ValueBool() != g.RouteAllTraffic {
		patch["route_all_traffic"] = plan.RouteAllTraffic.ValueBool()
	}
	if known(plan.AllowSharedLogins) && plan.AllowSharedLogins.ValueBool() != g.AllowSharedLogins {
		patch["allow_shared_logins"] = plan.AllowSharedLogins.ValueBool()
	}
	if known(plan.MTU) && plan.MTU.ValueInt64() != g.MTU {
		patch["mtu"] = plan.MTU.ValueInt64()
	}
	return patch
}

// converge saves what the configuration asks, applies it (and whatever else
// waits) to the gateway, and answers the gateway as it then is.
func (r *vpnGatewaySettingsResource) converge(ctx context.Context, plan vpnGatewaySettingsModel, timeout time.Duration, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	gatewayID := plan.GatewayID.ValueString()
	g, err := r.client.GetVPNGateway(ctx, gatewayID)
	if err != nil {
		diags.AddError("Could not read the VPN gateway", err.Error())
		return
	}
	if patch := settingsPatch(plan, g); len(patch) > 0 {
		if g, err = r.client.UpdateVPNGateway(ctx, gatewayID, patch); err != nil {
			diags.AddError("Could not change the VPN gateway's settings", err.Error())
			return
		}
	}
	if g.PendingChanges {
		// Saved first: what was changed stays changed whatever the apply does.
		saved := gatewaySettingsFromAPI(plan, g)
		saved.PendingChanges = types.BoolValue(true)
		diags.Append(state.Set(ctx, saved)...)
		rebuild, err := applyVPNChanges(ctx, r.client, gatewayID, plan.AllowRebuild.ValueBool(), timeout)
		switch {
		case rebuild:
			diags.AddError("Applying needs a rebuild",
				"The settings are saved, but applying them rebuilds the gateway: a few minutes offline, everyone "+
					"connected disconnected. Set allow_rebuild = true to let Terraform do it, or apply it from the "+
					"gateway's page in the console.")
			return
		case err != nil:
			diags.AddError("The VPN gateway did not apply the settings", err.Error())
			return
		}
		if g, err = r.applied(ctx, gatewayID, timeout); err != nil {
			diags.AddError("The VPN gateway did not apply the settings", err.Error())
			return
		}
	}
	diags.Append(state.Set(ctx, gatewaySettingsFromAPI(plan, g))...)
}

// applied waits until the gateway says it has nothing waiting: the operation
// finishing and the gateway's own record can be a moment apart.
func (r *vpnGatewaySettingsResource) applied(ctx context.Context, gatewayID string, timeout time.Duration) (*client.VPNGateway, error) {
	var g *client.VPNGateway
	err := waitUntil(ctx, timeout, func() (bool, error) {
		got, err := r.client.GetVPNGateway(ctx, gatewayID)
		if err != nil {
			return false, err
		}
		g = got
		if got.ApplyState == "failed" {
			why := deref(got.LastApplyErrorCode)
			if why == "" {
				why = "no reason given"
			}
			return false, fmt.Errorf("the gateway's last apply failed (%s)", why)
		}
		return !got.PendingChanges, nil
	})
	return g, err
}

func (r *vpnGatewaySettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpnGatewaySettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultVPNSettingsTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.converge(ctx, plan, timeout, &resp.State, &resp.Diagnostics)
}

func (r *vpnGatewaySettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpnGatewaySettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.GetVPNGateway(ctx, state.GatewayID.ValueString())
	if client.IsNotFound(err) {
		// The gateway (its instance) is gone.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the VPN gateway", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, gatewaySettingsFromAPI(state, g))...)
}

func (r *vpnGatewaySettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpnGatewaySettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, defaultVPNSettingsTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.converge(ctx, plan, timeout, &resp.State, &resp.Diagnostics)
}

// Delete leaves the gateway as it is: its settings are its own, and stay.
func (r *vpnGatewaySettingsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *vpnGatewaySettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("allow_rebuild"), false)...)
}
