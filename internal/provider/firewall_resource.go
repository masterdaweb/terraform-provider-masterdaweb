package provider

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                = (*firewallResource)(nil)
	_ resource.ResourceWithConfigure   = (*firewallResource)(nil)
	_ resource.ResourceWithImportState = (*firewallResource)(nil)
)

// NewFirewallResource is masterdaweb_firewall.
func NewFirewallResource() resource.Resource { return &firewallResource{} }

type firewallResource struct{ client *client.Client }

type firewallModel struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	Description       types.String   `tfsdk:"description"`
	Enabled           types.Bool     `tfsdk:"enabled"`
	PolicyIn          types.String   `tfsdk:"policy_in"`
	PolicyOut         types.String   `tfsdk:"policy_out"`
	Rules             types.List     `tfsdk:"rules"`
	InstanceIDs       types.Set      `tfsdk:"instance_ids"`
	HasPendingChanges types.Bool     `tfsdk:"has_pending_changes"`
	CreatedAt         types.String   `tfsdk:"created_at"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

type firewallRuleModel struct {
	Direction   types.String `tfsdk:"direction"`
	Action      types.String `tfsdk:"action"`
	Macro       types.String `tfsdk:"macro"`
	Protocol    types.String `tfsdk:"protocol"`
	Source      types.String `tfsdk:"source"`
	Destination types.String `tfsdk:"destination"`
	SourcePort  types.String `tfsdk:"source_port"`
	DestPort    types.String `tfsdk:"dest_port"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Comment     types.String `tfsdk:"comment"`
}

var firewallRuleType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"direction":   types.StringType,
	"action":      types.StringType,
	"macro":       types.StringType,
	"protocol":    types.StringType,
	"source":      types.StringType,
	"destination": types.StringType,
	"source_port": types.StringType,
	"dest_port":   types.StringType,
	"enabled":     types.BoolType,
	"comment":     types.StringType,
}}

const defaultFirewallTimeout = 10 * time.Minute

var policies = []string{"ACCEPT", "DROP", "REJECT"}

func (r *firewallResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall"
}

func (r *firewallResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A firewall: rules evaluated in order, and default policies for what no rule matches, applied " +
			"to the instances listed in instance_ids. Every change is committed onto exactly those instances, and " +
			"Terraform waits for the commit to finish.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The firewall's id."),
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The firewall's name, up to 255 characters.",
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "What the firewall is for, up to 500 characters.",
				Validators:  []validator.String{stringvalidator.LengthAtMost(500)},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the firewall filters at all.",
			},
			"policy_in": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "What happens to inbound traffic no rule matches: ACCEPT, DROP or REJECT. DROP or REJECT " +
					"also stop your own SSH unless a rule lets it in.",
				Validators:    []validator.String{stringvalidator.OneOf(policies...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"policy_out": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "What happens to outbound traffic no rule matches: ACCEPT, DROP or REJECT.",
				Validators:    []validator.String{stringvalidator.OneOf(policies...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"rules": schema.ListNestedAttribute{
				Optional: true, Computed: true,
				Default:     listdefault.StaticValue(types.ListValueMust(firewallRuleType, []attr.Value{})),
				Description: "The rules, evaluated in this order. Rules made outside Terraform are removed.",
				Validators:  []validator.List{listvalidator.SizeAtMost(200)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"direction": schema.StringAttribute{
						Required: true, Description: "in or out.",
						Validators: []validator.String{stringvalidator.OneOf("in", "out")},
					},
					"action": schema.StringAttribute{
						Required: true, Description: "ACCEPT, DROP or REJECT.",
						Validators: []validator.String{stringvalidator.OneOf(policies...)},
					},
					"macro": schema.StringAttribute{
						Optional:    true,
						Description: "A named service (HTTP, HTTPS, SSH…; the masterdaweb_firewall_macros list): it sets the protocol and ports.",
					},
					"protocol": schema.StringAttribute{
						Optional:    true,
						Description: "The protocol, lower-case as in /etc/protocols: tcp, udp, icmp… Only tcp, udp, sctp, dccp and udplite carry ports.",
					},
					"source": schema.StringAttribute{
						Optional:    true,
						Description: "Where the traffic comes from: an address, a CIDR, a FROM-TO range or a comma-separated list. Anywhere when left out.",
						Validators:  []validator.String{stringvalidator.LengthAtMost(512)},
					},
					"destination": schema.StringAttribute{
						Optional:    true,
						Description: "Where the traffic goes, in the same forms as source.",
						Validators:  []validator.String{stringvalidator.LengthAtMost(512)},
					},
					"source_port": schema.StringAttribute{
						Optional:    true,
						Description: "Source port, a range (1024:65535) or a comma-separated list. Needs protocol.",
					},
					"dest_port": schema.StringAttribute{
						Optional:    true,
						Description: "Destination port, a range (8000:8100) or a comma-separated list, like 443. Needs protocol.",
					},
					"enabled": schema.BoolAttribute{
						Optional: true, Computed: true,
						Default:     booldefault.StaticBool(true),
						Description: "A disabled rule is kept but not applied.",
					},
					"comment": schema.StringAttribute{
						Optional:    true,
						Description: "A note on the rule, up to 500 characters.",
						Validators:  []validator.String{stringvalidator.LengthAtMost(500)},
					},
				}},
			},
			"instance_ids": schema.SetAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Default:     setdefault.StaticValue(emptySet()),
				Description: "The instances the firewall protects: exactly these (one left out is detached).",
			},
			"has_pending_changes": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether rules were changed outside Terraform without being committed.",
			},
			"created_at": computedString("When the firewall was created (RFC 3339)."),
			"timeouts": timeoutsAttribute(ctx, timeouts.Opts{
				Create: true, Update: true,
				CreateDescription: "How long to wait for the first commit (default 10m).",
				UpdateDescription: "How long to wait for a commit (default 10m).",
			}),
		},
	}
}

func (r *firewallResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// ── Rules: what the API keeps, and what means the same ─────────────────────

// normPorts is a port list as the API keeps it: FROM-TO stored as FROM:TO,
// no spaces.
func normPorts(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	return strings.ReplaceAll(s, "-", ":")
}

// normAddresses is an address list compared item by item: CIDRs by their
// network, single addresses as written.
func normAddresses(s string) string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			if p.Bits() == p.Addr().BitLen() {
				part = p.Addr().String()
			} else {
				part = p.Masked().String()
			}
		} else if a, err := netip.ParseAddr(part); err == nil {
			part = a.String()
		}
		out = append(out, strings.ToLower(part))
	}
	return strings.Join(out, ",")
}

func sameText(cfg types.String, api *string, norm func(string) string) bool {
	got := ""
	if api != nil {
		got = *api
	}
	want := ""
	if known(cfg) {
		want = cfg.ValueString()
	}
	if norm != nil {
		got, want = norm(got), norm(want)
	}
	return strings.TrimSpace(got) == strings.TrimSpace(want)
}

// ruleMatches says whether the API's rule is what the configuration's says.
// A macro's protocol and ports, filled in by the API, match a rule that
// gives only the macro.
func ruleMatches(cfg firewallRuleModel, api client.FirewallRule) bool {
	lower := strings.ToLower
	upper := strings.ToUpper
	macroSet := known(cfg.Macro) && cfg.Macro.ValueString() != ""
	if lower(cfg.Direction.ValueString()) != lower(api.Direction) || upper(cfg.Action.ValueString()) != upper(api.Action) {
		return false
	}
	if !sameText(cfg.Macro, api.Macro, upper) {
		return false
	}
	if !(macroSet && !known(cfg.Protocol)) && !sameText(cfg.Protocol, api.Protocol, lower) {
		return false
	}
	if !(macroSet && !known(cfg.DestPort)) && !sameText(cfg.DestPort, api.DestPort, normPorts) {
		return false
	}
	if !sameText(cfg.SourcePort, api.SourcePort, normPorts) || !sameText(cfg.Source, api.Source, normAddresses) ||
		!sameText(cfg.Destination, api.Destination, normAddresses) || !sameText(cfg.Comment, api.Comment, nil) {
		return false
	}
	enabled := !known(cfg.Enabled) || cfg.Enabled.ValueBool()
	return enabled == api.Enabled
}

// ruleBody is a rule as the API takes it: every field, a cleared one null
// (a rule update merges over the stored rule).
func ruleBody(m firewallRuleModel) map[string]any {
	opt := func(v types.String) any {
		if !known(v) || strings.TrimSpace(v.ValueString()) == "" {
			return nil
		}
		return v.ValueString()
	}
	body := map[string]any{
		"direction":   m.Direction.ValueString(),
		"action":      m.Action.ValueString(),
		"macro":       opt(m.Macro),
		"protocol":    opt(m.Protocol),
		"source":      opt(m.Source),
		"destination": opt(m.Destination),
		"source_port": opt(m.SourcePort),
		"dest_port":   opt(m.DestPort),
		"enabled":     !known(m.Enabled) || m.Enabled.ValueBool(),
		"comment":     opt(m.Comment),
	}
	return body
}

// newRuleBody is a rule to add: the fields given only.
func newRuleBody(m firewallRuleModel) map[string]any {
	body := ruleBody(m)
	for k, v := range body {
		if v == nil {
			delete(body, k)
		}
	}
	return body
}

func ruleFromAPI(prior *firewallRuleModel, api client.FirewallRule) firewallRuleModel {
	if prior != nil && ruleMatches(*prior, api) {
		return *prior
	}
	return firewallRuleModel{
		Direction:   types.StringValue(api.Direction),
		Action:      types.StringValue(api.Action),
		Macro:       stringOrNull(api.Macro),
		Protocol:    stringOrNull(api.Protocol),
		Source:      stringOrNull(api.Source),
		Destination: stringOrNull(api.Destination),
		SourcePort:  stringOrNull(api.SourcePort),
		DestPort:    stringOrNull(api.DestPort),
		Enabled:     types.BoolValue(api.Enabled),
		Comment:     stringOrNull(api.Comment),
	}
}

func sortedRules(f *client.Firewall) []client.FirewallRule {
	rules := append([]client.FirewallRule(nil), f.Rules...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Position < rules[j].Position })
	return rules
}

func firewallFromAPI(ctx context.Context, prior firewallModel, f *client.Firewall) (firewallModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	var priorRules []firewallRuleModel
	if known(prior.Rules) {
		diags.Append(prior.Rules.ElementsAs(ctx, &priorRules, false)...)
	}
	m := prior
	m.ID = types.StringValue(f.ID)
	m.Name = types.StringValue(f.Name)
	if known(prior.Name) && strings.TrimSpace(prior.Name.ValueString()) == f.Name {
		m.Name = prior.Name
	}
	m.Description = keepEquivalent(prior.Description, f.Description)
	m.Enabled = types.BoolValue(f.Enabled)
	m.PolicyIn = types.StringValue(f.PolicyIn)
	m.PolicyOut = types.StringValue(f.PolicyOut)
	m.HasPendingChanges = types.BoolValue(f.HasPendingChanges)
	m.CreatedAt = stringOrNull(f.CreatedAt)
	rules := []attr.Value{}
	for i, api := range sortedRules(f) {
		var p *firewallRuleModel
		if i < len(priorRules) {
			p = &priorRules[i]
		}
		obj, d := types.ObjectValueFrom(ctx, firewallRuleType.AttrTypes, ruleFromAPI(p, api))
		diags.Append(d...)
		rules = append(rules, obj)
	}
	list, d := types.ListValue(firewallRuleType, rules)
	diags.Append(d...)
	m.Rules = list
	ids := append([]string{}, f.InstanceIDs...)
	m.InstanceIDs = stringSet(ids)
	return m, diags
}

// ── Applying ────────────────────────────────────────────────────────────────

// apply brings the firewall to the plan — its settings, its rules in order —
// and commits it onto the planned instances.
func (r *firewallResource) apply(ctx context.Context, f *client.Firewall, plan, prior firewallModel, timeout time.Duration) (*client.Firewall, error) {
	id := f.ID
	patch := map[string]any{}
	if !plan.Name.Equal(prior.Name) && known(plan.Name) {
		patch["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(prior.Description) {
		patch["description"] = plan.Description.ValueString()
	}
	if known(plan.Enabled) && plan.Enabled.ValueBool() != f.Enabled {
		patch["enabled"] = plan.Enabled.ValueBool()
	}
	if known(plan.PolicyIn) && plan.PolicyIn.ValueString() != f.PolicyIn {
		patch["policy_in"] = plan.PolicyIn.ValueString()
	}
	if known(plan.PolicyOut) && plan.PolicyOut.ValueString() != f.PolicyOut {
		patch["policy_out"] = plan.PolicyOut.ValueString()
	}
	if len(patch) > 0 {
		updated, err := r.client.UpdateFirewall(ctx, id, patch)
		if err != nil {
			return nil, fmt.Errorf("could not update the firewall: %w", err)
		}
		f = updated
	}

	var want []firewallRuleModel
	if known(plan.Rules) {
		if d := plan.Rules.ElementsAs(ctx, &want, false); d.HasError() {
			return nil, fmt.Errorf("the rules could not be read from the plan")
		}
	}
	current := sortedRules(f)
	for i, rule := range want {
		var err error
		switch {
		case i < len(current) && ruleMatches(rule, current[i]):
			continue
		case i < len(current):
			_, err = r.client.UpdateFirewallRule(ctx, id, current[i].ID, ruleBody(rule))
		default:
			_, err = r.client.AddFirewallRule(ctx, id, newRuleBody(rule))
		}
		if err != nil {
			return nil, fmt.Errorf("could not set rule %d: %w", i+1, err)
		}
	}
	for i := len(want); i < len(current); i++ {
		if err := r.client.DeleteFirewallRule(ctx, id, current[i].ID); err != nil && !client.IsNotFound(err) {
			return nil, fmt.Errorf("could not remove rule %d: %w", i+1, err)
		}
	}

	commit, err := r.client.CommitFirewall(ctx, id, listStrings(ctx, plan.InstanceIDs))
	if err != nil {
		return nil, fmt.Errorf("could not commit the firewall: %w", err)
	}
	if err := waitForOperation(ctx, r.client, commit.OperationID, timeout); err != nil {
		return nil, fmt.Errorf("the firewall's commit did not finish: %w", err)
	}
	return r.client.GetFirewall(ctx, id)
}

func (r *firewallResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan firewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultFirewallTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString()}
	if known(plan.Description) && plan.Description.ValueString() != "" {
		body["description"] = plan.Description.ValueString()
	}
	f, err := r.client.CreateFirewall(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not create the firewall", err.Error())
		return
	}
	// The description went with the create.
	prior := plan
	prior.Description = stringOrNull(f.Description)
	if !plan.Description.IsNull() && plan.Description.ValueString() == "" {
		prior.Description = plan.Description
	}
	prior.Name = types.StringValue(f.Name)
	applied, err := r.apply(ctx, f, plan, prior, timeout)
	if err != nil {
		// In the state anyway, tainted: the next apply replaces it.
		m, d := firewallFromAPI(ctx, plan, f)
		resp.Diagnostics.Append(d...)
		resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
		resp.Diagnostics.AddError("Could not set up the firewall", err.Error())
		return
	}
	m, d := firewallFromAPI(ctx, plan, applied)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *firewallResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f, err := r.client.GetFirewall(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the firewall", err.Error())
		return
	}
	m, d := firewallFromAPI(ctx, state, f)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *firewallResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state firewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, defaultFirewallTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	f, err := r.client.GetFirewall(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the firewall", err.Error())
		return
	}
	applied, err := r.apply(ctx, f, plan, state, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Could not update the firewall", err.Error())
		return
	}
	m, d := firewallFromAPI(ctx, plan, applied)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *firewallResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteFirewall(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the firewall", err.Error())
	}
}

func (r *firewallResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !isUUID(id) {
		all, err := r.client.ListFirewalls(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the firewalls", err.Error())
			return
		}
		var found []string
		for _, f := range all {
			if f.Name == id {
				found = append(found, f.ID)
			}
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("No single firewall by that name",
				fmt.Sprintf("%d firewalls are named %q. Import a firewall by its id, or by a name only it has.", len(found), id))
			return
		}
		id = found[0]
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
