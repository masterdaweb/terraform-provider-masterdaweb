package provider

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// ── masterdaweb_dns_zone ────────────────────────────────────────────────────

var (
	_ resource.Resource                = (*dnsZoneResource)(nil)
	_ resource.ResourceWithConfigure   = (*dnsZoneResource)(nil)
	_ resource.ResourceWithImportState = (*dnsZoneResource)(nil)
)

// NewDNSZoneResource is masterdaweb_dns_zone.
func NewDNSZoneResource() resource.Resource { return &dnsZoneResource{} }

type dnsZoneResource struct{ client *client.Client }

type dnsZoneModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	NameServers types.List   `tfsdk:"name_servers"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (r *dnsZoneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (r *dnsZoneResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A domain hosted on the platform's nameservers. Point the domain's NS records at them at your " +
			"registrar for its records (masterdaweb_dns_record) to answer. Destroying it removes every record in it.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The zone's id."),
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "The root domain, like example.com (no scheme, no trailing dot). Changing it replaces the zone.",
				Validators:    []validator.String{stringvalidator.LengthBetween(3, 253)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name_servers": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description:   "The platform's nameservers: the NS records to set for the domain at its registrar.",
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"created_at": computedString("When the zone was created (RFC 3339)."),
		},
	}
}

func (r *dnsZoneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func zoneFromAPI(prior dnsZoneModel, z *client.DNSZone) dnsZoneModel {
	m := dnsZoneModel{
		ID: types.StringValue(z.ID), Name: types.StringValue(z.Name), NameServers: prior.NameServers,
		CreatedAt: stringOrNull(z.CreatedAt),
	}
	if known(prior.Name) && normalizeHostname(prior.Name.ValueString()) == normalizeHostname(z.Name) {
		m.Name = prior.Name
	}
	return m
}

// zoneNameServers are the nameservers to set at the registrar, asked once
// (they are the platform's, the same for every zone): null when the API
// cannot say.
func zoneNameServers(ctx context.Context, c *client.Client, m dnsZoneModel) types.List {
	if known(m.NameServers) {
		return m.NameServers
	}
	h, err := c.GetDNSZoneHealth(ctx, m.ID.ValueString())
	if err != nil || len(h.ExpectedNameservers) == 0 {
		return types.ListNull(types.StringType)
	}
	return stringList(h.ExpectedNameservers)
}

func (r *dnsZoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	z, err := r.client.CreateDNSZone(ctx, normalizeHostname(plan.Name.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError("Could not create the DNS zone", err.Error())
		return
	}
	m := zoneFromAPI(plan, z)
	m.NameServers = zoneNameServers(ctx, r.client, m)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *dnsZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	z, err := r.client.GetDNSZone(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the DNS zone", err.Error())
		return
	}
	m := zoneFromAPI(state, z)
	m.NameServers = zoneNameServers(ctx, r.client, m)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *dnsZoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every attribute replaces the zone; nothing changes in place.
	var plan dnsZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *dnsZoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDNSZone(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the DNS zone", err.Error())
	}
}

func (r *dnsZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !isUUID(id) {
		zones, err := r.client.ListDNSZones(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the DNS zones", err.Error())
			return
		}
		found := ""
		for _, z := range zones {
			if normalizeHostname(z.Name) == normalizeHostname(id) {
				found = z.ID
			}
		}
		if found == "" {
			resp.Diagnostics.AddError("No such DNS zone", fmt.Sprintf("The account hosts no zone %q. Import a zone by its id or its name.", id))
			return
		}
		id = found
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ── masterdaweb_dns_record ──────────────────────────────────────────────────

var (
	_ resource.Resource                   = (*dnsRecordResource)(nil)
	_ resource.ResourceWithConfigure      = (*dnsRecordResource)(nil)
	_ resource.ResourceWithImportState    = (*dnsRecordResource)(nil)
	_ resource.ResourceWithValidateConfig = (*dnsRecordResource)(nil)
)

// NewDNSRecordResource is masterdaweb_dns_record.
func NewDNSRecordResource() resource.Resource { return &dnsRecordResource{} }

type dnsRecordResource struct{ client *client.Client }

type dnsRecordModel struct {
	ID      types.String `tfsdk:"id"`
	ZoneID  types.String `tfsdk:"zone_id"`
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	TTL     types.Int64  `tfsdk:"ttl"`
	Records types.Set    `tfsdk:"records"`
	FQDN    types.String `tfsdk:"fqdn"`
}

var dnsRecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA"}

func (r *dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

func (r *dnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A record set: every value at one name and type in a zone (two A records for www are one " +
			"masterdaweb_dns_record with two records). Terraform owns the whole set.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("<zone_id>/<name>/<type>."),
			"zone_id": schema.StringAttribute{
				Required: true, Description: "The zone (masterdaweb_dns_zone). Changing it replaces the record set.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "The name relative to the zone (www), @ for the zone itself, or a fully qualified name in " +
					"the zone. Changing it replaces the record set.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 253)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type": schema.StringAttribute{
				Required: true, Description: "A, AAAA, CNAME, MX, TXT, NS, SRV or CAA. Changing it replaces the record set.",
				Validators:    []validator.String{stringvalidator.OneOf(dnsRecordTypes...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ttl": schema.Int64Attribute{
				Optional: true, Computed: true,
				Default:     int64default.StaticInt64(3600),
				Description: "Seconds resolvers may keep the answer: 60 to 604800.",
				Validators:  []validator.Int64{int64validator.Between(60, 604800)},
			},
			"records": schema.SetAttribute{
				Required: true, ElementType: types.StringType,
				Description: "The values: an address for A/AAAA, a hostname for CNAME/NS, \"<priority> <host>\" for MX, " +
					"\"<priority> <weight> <port> <target>\" for SRV, the text for TXT (quotes optional). A CNAME has one.",
				Validators: []validator.Set{setvalidator.SizeBetween(1, 100), setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 65535))},
			},
			"fqdn": computedString("The fully qualified name, as the nameservers answer it (with its trailing dot)."),
		},
	}
}

func (r *dnsRecordResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg dnsRecordModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !known(cfg.Type) || !known(cfg.Records) {
		return
	}
	if cfg.Type.ValueString() == "CNAME" && len(cfg.Records.Elements()) > 1 {
		resp.Diagnostics.AddAttributeError(path.Root("records"), "A CNAME has one value",
			"A name with a CNAME has nothing else (RFC 1034): give one record.")
	}
	if cfg.Type.ValueString() == "CNAME" && known(cfg.Name) && strings.TrimSpace(cfg.Name.ValueString()) == "@" {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "No CNAME at the zone apex",
			"The zone itself (@) has its SOA and NS records, so it cannot have a CNAME: point it at an A or AAAA record.")
	}
}

func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// recordFQDN is a record name as the nameservers keep it: fully qualified,
// with its trailing dot.
func recordFQDN(name, zone string) string {
	name = strings.TrimSpace(name)
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	switch {
	case name == "" || name == "@":
		return zone + "."
	case strings.HasSuffix(name, "."):
		return strings.ToLower(name)
	case strings.EqualFold(name, zone) || strings.HasSuffix(strings.ToLower(name), "."+zone):
		return strings.ToLower(name) + "."
	}
	return strings.ToLower(name) + "." + zone + "."
}

var txtChunk = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// recordValue is a value compared as the nameservers keep it: hostnames
// fully qualified, TXT text unquoted and joined.
func recordValue(typ, value, zone string) string {
	value = strings.TrimSpace(value)
	host := func(h string) string {
		h = strings.TrimSpace(h)
		switch {
		case strings.HasSuffix(h, "."):
			return strings.ToLower(h)
		case !strings.Contains(h, "."):
			return strings.ToLower(h) + "." + strings.TrimSuffix(strings.ToLower(zone), ".") + "."
		}
		return strings.ToLower(h) + "."
	}
	switch typ {
	case "CNAME", "NS":
		return host(value)
	case "MX":
		if f := strings.Fields(value); len(f) == 2 {
			return f[0] + " " + host(f[1])
		}
	case "SRV":
		if f := strings.Fields(value); len(f) == 4 {
			return strings.Join(f[:3], " ") + " " + host(f[3])
		}
	case "TXT":
		if m := txtChunk.FindAllStringSubmatch(value, -1); len(m) > 0 && strings.HasPrefix(value, "\"") {
			var b strings.Builder
			for _, c := range m {
				b.WriteString(strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(c[1]))
			}
			return b.String()
		}
		return strings.ReplaceAll(value, "\n", "")
	case "A", "AAAA":
		if a, err := netip.ParseAddr(value); err == nil {
			return a.String()
		}
		return strings.ToLower(value)
	}
	return value
}

func recordID(zoneID, name, typ string) string { return zoneID + "/" + name + "/" + typ }

// recordFromAPI is the state from a record set, keeping the configuration's
// spelling of every value that means the same.
func recordFromAPI(prior dnsRecordModel, zone string, rr *client.DNSRecord) dnsRecordModel {
	typ := prior.Type.ValueString()
	m := prior
	m.FQDN = types.StringValue(rr.Name)
	m.TTL = types.Int64Value(rr.TTL)
	m.ID = types.StringValue(recordID(prior.ZoneID.ValueString(), prior.Name.ValueString(), typ))
	configured := listStrings(context.Background(), prior.Records)
	byValue := map[string]string{}
	for _, c := range configured {
		byValue[recordValue(typ, c, zone)] = c
	}
	var out []string
	for _, v := range rr.Values {
		if c, ok := byValue[recordValue(typ, v, zone)]; ok {
			out = append(out, c)
			continue
		}
		out = append(out, v)
	}
	sort.Strings(out)
	m.Records = stringSet(out)
	return m
}

func (r *dnsRecordResource) zoneName(ctx context.Context, zoneID string) (string, error) {
	z, err := r.client.GetDNSZone(ctx, zoneID)
	if err != nil {
		return "", err
	}
	return z.Name, nil
}

func (r *dnsRecordResource) find(ctx context.Context, zoneID, zone, name, typ string) (*client.DNSRecord, error) {
	records, err := r.client.ListDNSRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	want := recordFQDN(name, zone)
	for i := range records {
		if strings.EqualFold(records[i].Name, want) && records[i].Type == typ {
			return &records[i], nil
		}
	}
	return nil, nil
}

// write replaces the record set with the planned values.
func (r *dnsRecordResource) write(ctx context.Context, plan dnsRecordModel) error {
	values := listStrings(ctx, plan.Records)
	sort.Strings(values)
	return r.client.SetDNSRecordSet(ctx, plan.ZoneID.ValueString(), strings.TrimSpace(plan.Name.ValueString()),
		plan.Type.ValueString(), plan.TTL.ValueInt64(), values)
}

func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone, err := r.zoneName(ctx, plan.ZoneID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the DNS zone", err.Error())
		return
	}
	if existing, err := r.find(ctx, plan.ZoneID.ValueString(), zone, plan.Name.ValueString(), plan.Type.ValueString()); err != nil {
		resp.Diagnostics.AddError("Could not read the zone's records", err.Error())
		return
	} else if existing != nil {
		resp.Diagnostics.AddError("The record set already exists",
			fmt.Sprintf("The zone %s already has %s records at %s. Import them (terraform import %s) to manage them, "+
				"or remove them first.", zone, plan.Type.ValueString(), existing.Name,
				recordID(plan.ZoneID.ValueString(), plan.Name.ValueString(), plan.Type.ValueString())))
		return
	}
	if err := r.write(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Could not create the record set", err.Error())
		return
	}
	r.readBack(ctx, plan, zone, &resp.State, &resp.Diagnostics)
}

func (r *dnsRecordResource) readBack(ctx context.Context, plan dnsRecordModel, zone string, state *tfsdkState, diags *diagnostics) {
	rr, err := r.find(ctx, plan.ZoneID.ValueString(), zone, plan.Name.ValueString(), plan.Type.ValueString())
	if err != nil {
		diags.AddError("Could not read the record set back", err.Error())
		return
	}
	if rr == nil {
		diags.AddError("The record set is not there", "The API took the record set but does not list it.")
		return
	}
	diags.Append(state.Set(ctx, recordFromAPI(plan, zone, rr))...)
}

func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone, err := r.zoneName(ctx, state.ZoneID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the DNS zone", err.Error())
		return
	}
	rr, err := r.find(ctx, state.ZoneID.ValueString(), zone, state.Name.ValueString(), state.Type.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the zone's records", err.Error())
		return
	}
	if rr == nil || len(rr.Values) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, recordFromAPI(state, zone, rr))...)
}

func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone, err := r.zoneName(ctx, plan.ZoneID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the DNS zone", err.Error())
		return
	}
	if err := r.write(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Could not update the record set", err.Error())
		return
	}
	r.readBack(ctx, plan, zone, &resp.State, &resp.Diagnostics)
}

func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteDNSRecord(ctx, state.ZoneID.ValueString(), strings.TrimSpace(state.Name.ValueString()), state.Type.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the record set", err.Error())
	}
}

func (r *dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(strings.TrimSpace(req.ID), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Unexpected import id",
			fmt.Sprintf("A record set is imported as <zone_id>/<name>/<type>, like 0190…/www/A, not %q.", req.ID))
		return
	}
	typ := strings.ToUpper(parts[2])
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), recordID(parts[0], parts[1], typ))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("type"), typ)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("records"), types.SetValueMust(types.StringType, []attr.Value{}))...)
}
