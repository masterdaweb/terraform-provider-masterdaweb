package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// ── masterdaweb_vpn_gateway / masterdaweb_vpn_gateways ──────────────────────

type vpnGatewayData struct {
	ID                      types.String `tfsdk:"id"`
	InstanceID              types.String `tfsdk:"instance_id"`
	Name                    types.String `tfsdk:"name"`
	Location                types.String `tfsdk:"location"`
	PublicIP                types.String `tfsdk:"public_ip"`
	Running                 types.Bool   `tfsdk:"running"`
	Protocol                types.String `tfsdk:"protocol"`
	RemoteAccessPool        types.String `tfsdk:"remote_access_pool"`
	RemoteAccessPoolIssue   types.String `tfsdk:"remote_access_pool_issue"`
	RouteAllTraffic         types.Bool   `tfsdk:"route_all_traffic"`
	AllowSharedLogins       types.Bool   `tfsdk:"allow_shared_logins"`
	MTU                     types.Int64  `tfsdk:"mtu"`
	WireguardPublicKey      types.String `tfsdk:"wireguard_public_key"`
	WireguardListenPort     types.Int64  `tfsdk:"wireguard_listen_port"`
	PendingChanges          types.Bool   `tfsdk:"pending_changes"`
	ApplyMode               types.String `tfsdk:"apply_mode"`
	ApplyState              types.String `tfsdk:"apply_state"`
	ApplyOperationID        types.String `tfsdk:"apply_operation_id"`
	AppliedAt               types.String `tfsdk:"applied_at"`
	LastApplyErrorCode      types.String `tfsdk:"last_apply_error_code"`
	UpdateAvailable         types.Bool   `tfsdk:"update_available"`
	UsersCount              types.Int64  `tfsdk:"users_count"`
	SiteConnectionsCount    types.Int64  `tfsdk:"site_connections_count"`
	IncludedSiteConnections types.Int64  `tfsdk:"included_site_connections"`
	CreatedAt               types.String `tfsdk:"created_at"`
}

// vpnGatewayDataAttributes are what the VPN gateway data sources say of a
// gateway, all computed.
func vpnGatewayDataAttributes() map[string]schema.Attribute {
	computedBool := func(desc string) schema.BoolAttribute { return schema.BoolAttribute{Computed: true, Description: desc} }
	computedInt := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, Description: desc}
	}
	return map[string]schema.Attribute{
		"id":                        dsComputed("The gateway's id."),
		"instance_id":               dsComputed("The instance it runs on."),
		"name":                      dsComputed("Its name (its instance's)."),
		"location":                  dsComputed("Where it runs."),
		"public_ip":                 dsComputed("The address people and sites connect to."),
		"running":                   computedBool("Whether its instance is powered on."),
		"protocol":                  dsComputed("The VPN app's transport: udp or tcp."),
		"remote_access_pool":        dsComputed("The addresses people connecting get (CIDR)."),
		"remote_access_pool_issue":  dsComputed("Why the pool needs changing, when it overlaps something."),
		"route_all_traffic":         computedBool("Whether all of a connected person's traffic goes through it."),
		"allow_shared_logins":       computedBool("Whether one login may be connected from several devices at once."),
		"mtu":                       computedInt("The tunnel's MTU."),
		"wireguard_public_key":      dsComputed("Its WireGuard public key, for the far end of a WireGuard site connection."),
		"wireguard_listen_port":     computedInt("The UDP port WireGuard site connections reach."),
		"pending_changes":           computedBool("Whether settings or logins changed since its last apply."),
		"apply_mode":                dsComputed("What its next apply does: in_place, or rebuild."),
		"apply_state":               dsComputed("idle, queued, applying or failed."),
		"apply_operation_id":        dsComputed("The apply in flight, when there is one (an operation)."),
		"applied_at":                dsComputed("When it last applied its settings (RFC 3339)."),
		"last_apply_error_code":     dsComputed("Why its last apply failed."),
		"update_available":          computedBool("Whether a newer version of the gateway software is available."),
		"users_count":               computedInt("How many logins it has."),
		"site_connections_count":    computedInt("How many sites are connected to it."),
		"included_site_connections": computedInt("How many site connections its plan includes."),
		"created_at":                dsComputed("When it was made (RFC 3339)."),
	}
}

func vpnGatewayDataFromAPI(g *client.VPNGateway) vpnGatewayData {
	return vpnGatewayData{
		ID: types.StringValue(g.ID), InstanceID: types.StringValue(g.InstanceID), Name: stringOrNull(g.Name),
		Location: stringOrNull(g.Location), PublicIP: stringOrNull(g.PublicIP), Running: types.BoolValue(g.Running),
		Protocol: types.StringValue(g.Protocol), RemoteAccessPool: stringOrNull(g.RemoteAccessPool),
		RemoteAccessPoolIssue: stringOrNull(g.RemoteAccessPoolIssue), RouteAllTraffic: types.BoolValue(g.RouteAllTraffic),
		AllowSharedLogins: types.BoolValue(g.AllowSharedLogins), MTU: types.Int64Value(g.MTU),
		WireguardPublicKey: stringOrNull(g.Wireguard.PublicKey), WireguardListenPort: types.Int64Value(g.Wireguard.ListenPort),
		PendingChanges: types.BoolValue(g.PendingChanges), ApplyMode: types.StringValue(g.ApplyMode),
		ApplyState: types.StringValue(g.ApplyState), ApplyOperationID: stringOrNull(g.ApplyOperationID),
		AppliedAt: stringOrNull(g.AppliedAt), LastApplyErrorCode: stringOrNull(g.LastApplyErrorCode),
		UpdateAvailable: types.BoolValue(g.UpdateAvailable), UsersCount: types.Int64Value(g.UsersCount),
		SiteConnectionsCount: types.Int64Value(g.SiteConnectionsCount), IncludedSiteConnections: types.Int64Value(g.IncludedSiteConnections),
		CreatedAt: stringOrNull(g.CreatedAt),
	}
}

// NewVPNGatewayDataSource is masterdaweb_vpn_gateway.
func NewVPNGatewayDataSource() datasource.DataSource { return &vpnGatewayDataSource{} }

type vpnGatewayDataSource struct{ client *client.Client }

func (d *vpnGatewayDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_gateway"
}

func (d *vpnGatewayDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := vpnGatewayDataAttributes()
	attrs["id"] = dsLookup("The gateway's id. Give the id, the name or the instance.")
	attrs["name"] = dsLookup("The gateway's name, which only it has. Give the id, the name or the instance.")
	attrs["instance_id"] = dsLookup("The instance it runs on (masterdaweb_instance ordered from the VPN gateway image). " +
		"Give the id, the name or the instance.")
	resp.Schema = schema.Schema{
		Description: "Looks up a VPN gateway — an instance ordered from the VPN gateway image — by its id, its name or " +
			"its instance: its address, settings, and whether changes wait to be applied.",
		Attributes: attrs,
	}
}

func (d *vpnGatewayDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupBy("id", "name", "instance_id")
}

func (d *vpnGatewayDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *vpnGatewayDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vpnGatewayData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(cfg.ID) {
		g, err := d.client.GetVPNGateway(ctx, cfg.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Could not read the VPN gateway", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, vpnGatewayDataFromAPI(g))...)
		return
	}
	all, err := d.client.ListVPNGateways(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the VPN gateways", err.Error())
		return
	}
	what, match := "", func(*client.VPNGateway) bool { return false }
	switch {
	case known(cfg.Name):
		what = fmt.Sprintf("named %q", cfg.Name.ValueString())
		match = func(g *client.VPNGateway) bool { return deref(g.Name) == cfg.Name.ValueString() }
	case known(cfg.InstanceID):
		what = "on instance " + cfg.InstanceID.ValueString()
		match = func(g *client.VPNGateway) bool { return g.InstanceID == cfg.InstanceID.ValueString() }
	}
	var found []*client.VPNGateway
	for i := range all {
		if match(&all[i]) {
			found = append(found, &all[i])
		}
	}
	switch len(found) {
	case 0:
		resp.Diagnostics.AddError("No such VPN gateway", "No VPN gateway is "+what+".")
	case 1:
		resp.Diagnostics.Append(resp.State.Set(ctx, vpnGatewayDataFromAPI(found[0]))...)
	default:
		var ids []string
		for _, g := range found {
			ids = append(ids, g.ID)
		}
		resp.Diagnostics.AddError("More than one VPN gateway matches",
			fmt.Sprintf("VPN gateways %s are all %s: look one up by its id.", strings.Join(ids, ", "), what))
	}
}

// NewVPNGatewaysDataSource is masterdaweb_vpn_gateways.
func NewVPNGatewaysDataSource() datasource.DataSource { return &vpnGatewaysDataSource{} }

type vpnGatewaysDataSource struct{ client *client.Client }

type vpnGatewaysData struct {
	ID        types.String     `tfsdk:"id"`
	NameRegex types.String     `tfsdk:"name_regex"`
	Gateways  []vpnGatewayData `tfsdk:"gateways"`
}

func (d *vpnGatewaysDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpn_gateways"
}

func (d *vpnGatewaysDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The account's VPN gateways, newest first. name_regex narrows the list.",
		Attributes: map[string]schema.Attribute{
			"id": dsComputed("Always vpn_gateways."),
			"name_regex": schema.StringAttribute{
				Optional: true, Description: "Only the gateways whose name matches this regular expression (RE2).",
				Validators: []validator.String{validRegex{}},
			},
			"gateways": schema.ListNestedAttribute{
				Computed: true, Description: "The gateways.",
				NestedObject: schema.NestedAttributeObject{Attributes: vpnGatewayDataAttributes()},
			},
		},
	}
}

func (d *vpnGatewaysDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *vpnGatewaysDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vpnGatewaysData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListVPNGateways(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the VPN gateways", err.Error())
		return
	}
	var re *regexp.Regexp
	if known(cfg.NameRegex) {
		re = regexp.MustCompile(cfg.NameRegex.ValueString())
	}
	out := cfg
	out.ID = types.StringValue("vpn_gateways")
	out.Gateways = []vpnGatewayData{}
	for i := range all {
		if re != nil && !re.MatchString(deref(all[i].Name)) {
			continue
		}
		out.Gateways = append(out.Gateways, vpnGatewayDataFromAPI(&all[i]))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}
