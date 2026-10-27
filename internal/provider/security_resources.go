package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// The Security Center: the risks the account accepted
// (masterdaweb_security_exception) and how it is scanned
// (masterdaweb_security_settings).

// The justification an exception needs, as the API's policy says it.
const (
	minJustification = 30
	maxJustification = 2000
)

// rfc3339Time checks a date-time, like 2026-12-31T23:59:59Z.
type rfc3339Time struct{}

func (rfc3339Time) Description(context.Context) string {
	return "must be an RFC 3339 date-time, like 2026-12-31T23:59:59Z"
}
func (v rfc3339Time) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v rfc3339Time) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ConfigValue.ValueString())); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid date-time",
			fmt.Sprintf("%q %s.", req.ConfigValue.ValueString(), v.Description(ctx)))
	}
}

// requiresReplaceUnlessEquivalent replaces the resource when a value changes
// in substance, not in spelling: an imported exception's date written
// 2026-12-01T00:00:00Z in the configuration and 2026-12-01T00:00:00.000Z by
// the API is the same date, taken in place.
func requiresReplaceUnlessEquivalent(same func(a, b string) bool, desc string) planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !(known(req.StateValue) && known(req.PlanValue) &&
				same(req.StateValue.ValueString(), req.PlanValue.ValueString()))
		},
		desc, desc,
	)
}

func sameTrimmed(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }

// ── masterdaweb_security_exception ─────────────────────────────────────────

var (
	_ resource.Resource                   = (*securityExceptionResource)(nil)
	_ resource.ResourceWithConfigure      = (*securityExceptionResource)(nil)
	_ resource.ResourceWithImportState    = (*securityExceptionResource)(nil)
	_ resource.ResourceWithValidateConfig = (*securityExceptionResource)(nil)
)

// NewSecurityExceptionResource is masterdaweb_security_exception.
func NewSecurityExceptionResource() resource.Resource { return &securityExceptionResource{} }

type securityExceptionResource struct{ client *client.Client }

type securityExceptionModel struct {
	ID                 types.String `tfsdk:"id"`
	Scope              types.String `tfsdk:"scope"`
	FindingID          types.String `tfsdk:"finding_id"`
	RuleID             types.String `tfsdk:"rule_id"`
	Port               types.Int64  `tfsdk:"port"`
	Protocol           types.String `tfsdk:"protocol"`
	InstanceID         types.String `tfsdk:"instance_id"`
	Justification      types.String `tfsdk:"justification"`
	ExpiresAt          types.String `tfsdk:"expires_at"`
	EffectiveExpiresAt types.String `tfsdk:"effective_expires_at"`
	Status             types.String `tfsdk:"status"`
	StartsAt           types.String `tfsdk:"starts_at"`
	ResourceType       types.String `tfsdk:"resource_type"`
	ResourceID         types.String `tfsdk:"resource_id"`
}

func (r *securityExceptionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_exception"
}

func (r *securityExceptionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "A risk the account accepts in the Security Center: one finding, a check (everywhere or on one " +
			"instance) or a port, with a justification kept for the audit and an end date. Its findings stop " +
			"lowering the security score until it lapses. An exception cannot be changed: changing anything " +
			"replaces it, and destroying it revokes it (its findings count again at once). Once it lapses or is " +
			"revoked in the console it leaves the state, and the next plan proposes it again — with a later " +
			"expires_at, to renew it.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The exception's id."),
			"scope": schema.StringAttribute{
				Required: true,
				Description: "finding (one finding, finding_id), rule (a check, rule_id, everywhere or on " +
					"instance_id) or port (port and protocol, everywhere or on instance_id).",
				Validators:    []validator.String{stringvalidator.OneOf("finding", "rule", "port")},
				PlanModifiers: replace,
			},
			"finding_id": schema.StringAttribute{
				Optional: true,
				Description: "With scope finding: the finding (masterdaweb_security_findings). An import takes it " +
					"from the API (the configuration's, from an API that does not show it).",
				Validators:    []validator.String{stringvalidator.RegexMatches(uuidRe, "must be a finding's id (a UUID)")},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessImported()},
			},
			"rule_id": schema.StringAttribute{
				Optional: true, Description: "With scope rule: the check's id (a finding's rule_id).",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 128)},
				PlanModifiers: replace,
			},
			"port": schema.Int64Attribute{
				Optional: true, Description: "With scope port: the port, 1 to 65535.",
				Validators:    []validator.Int64{int64validator.Between(1, 65535)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"protocol": schema.StringAttribute{
				Optional: true, Description: "With scope port: tcp or udp; both when left out.",
				Validators:    []validator.String{stringvalidator.OneOf("tcp", "udp")},
				PlanModifiers: replace,
			},
			"instance_id": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "With scope rule or port: only on this instance; everywhere when left out. With scope " +
					"finding, the instance the finding is about.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"justification": schema.StringAttribute{
				Required: true,
				Description: fmt.Sprintf("Why the risk is accepted, %d to %d characters, kept for the audit.",
					minJustification, maxJustification),
				Validators: []validator.String{stringvalidator.LengthBetween(minJustification, maxJustification)},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessEquivalent(sameTrimmed,
					"Changing it replaces the exception.")},
			},
			"expires_at": schema.StringAttribute{
				Required: true,
				Description: "When it lapses, an RFC 3339 date-time in the future. The platform caps how long an " +
					"exception lasts: effective_expires_at is the date that applies.",
				Validators: []validator.String{rfc3339Time{}},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessEquivalent(sameInstant,
					"Changing it to another date replaces the exception.")},
			},
			"effective_expires_at": computedString("When it lapses: expires_at, or the platform's maximum when that " +
				"comes first (RFC 3339)."),
			"status":    computedString("active while it is in force."),
			"starts_at": computedString("When it came into force (RFC 3339)."),
			"resource_type": computedString("With scope finding, the kind of resource the finding is about when it " +
				"is not an instance: serverless_service, database, bucket, storage_key or account."),
			"resource_id": computedString("With scope finding, that resource's id."),
		},
	}
}

func (r *securityExceptionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m securityExceptionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !known(m.Scope) {
		return
	}
	scope := m.Scope.ValueString()
	given := map[string]bool{
		"finding_id":  !m.FindingID.IsNull(),
		"rule_id":     !m.RuleID.IsNull(),
		"port":        !m.Port.IsNull(),
		"protocol":    !m.Protocol.IsNull(),
		"instance_id": !m.InstanceID.IsNull(),
	}
	need := map[string]string{"finding": "finding_id", "rule": "rule_id", "port": "port"}[scope]
	if need == "" {
		return // the scope's own validator says what is wrong
	}
	allowed := map[string][]string{
		"finding": {"finding_id"},
		"rule":    {"rule_id", "instance_id"},
		"port":    {"port", "protocol", "instance_id"},
	}[scope]
	if !given[need] {
		resp.Diagnostics.AddAttributeError(path.Root(need), "Missing "+need,
			"An exception of scope "+scope+" names its "+strings.ReplaceAll(need, "_id", "")+" with "+need+".")
	}
	for _, attr := range []string{"finding_id", "rule_id", "port", "protocol", "instance_id"} {
		ok := false
		for _, a := range allowed {
			ok = ok || a == attr
		}
		if given[attr] && !ok {
			resp.Diagnostics.AddAttributeError(path.Root(attr), "Not for this scope",
				attr+" is not for an exception of scope "+scope+": it takes "+strings.Join(allowed, ", ")+".")
		}
	}
}

func (r *securityExceptionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func securityExceptionFromAPI(prior securityExceptionModel, e *client.SecurityException) securityExceptionModel {
	m := prior
	m.ID = types.StringValue(e.ID)
	m.Scope = types.StringValue(e.Scope)
	if !known(prior.FindingID) && e.FindingID != nil {
		// An import. Otherwise the id it was made with stays: the API names
		// the newest finding of the same fingerprint, the same risk.
		m.FindingID = types.StringValue(*e.FindingID)
	}
	m.RuleID = stringOrNull(e.RuleID)
	m.Port = int64OrNull(e.Port)
	m.Protocol = stringOrNull(e.Protocol)
	m.InstanceID = stringOrNull(e.InstanceID)
	if !(known(prior.Justification) && strings.TrimSpace(prior.Justification.ValueString()) == e.Justification) {
		m.Justification = types.StringValue(e.Justification)
	}
	if !known(prior.ExpiresAt) {
		// An import. Otherwise the configuration's date stays as written: an
		// exception never changes, and a cap shows in effective_expires_at.
		m.ExpiresAt = types.StringValue(e.ExpiresAt)
	}
	m.EffectiveExpiresAt = types.StringValue(e.ExpiresAt)
	m.Status = types.StringValue(e.Status)
	m.StartsAt = types.StringValue(e.StartsAt)
	m.ResourceType, m.ResourceID = types.StringNull(), types.StringNull()
	if e.Resource != nil {
		m.ResourceType, m.ResourceID = types.StringValue(e.Resource.Type), types.StringValue(e.Resource.ID)
	}
	return m
}

// find is the exception by its id while it is in force, nil otherwise:
// GET /v1/security/exceptions/{id}, or the list on an API from before it.
func (r *securityExceptionResource) find(ctx context.Context, id string) (*client.SecurityException, error) {
	e, err := r.client.GetSecurityException(ctx, id)
	switch {
	case client.RouteMissing(err):
		all, err := r.client.ListSecurityExceptions(ctx, true)
		if err != nil {
			return nil, err
		}
		e = nil
		for i := range all {
			if all[i].ID == id {
				e = &all[i]
			}
		}
	case client.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	if e == nil {
		return nil, nil
	}
	if e.Status != "active" {
		// Lapsed or revoked: it no longer stands, so it leaves the state and
		// the next plan proposes it again, as for a resource deleted outside
		// Terraform.
		tflog.Info(ctx, "The security exception is no longer in force", map[string]any{"id": id, "status": e.Status})
		return nil, nil
	}
	return e, nil
}

func (r *securityExceptionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan securityExceptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"scope":         plan.Scope.ValueString(),
		"justification": strings.TrimSpace(plan.Justification.ValueString()),
		"expires_at":    strings.TrimSpace(plan.ExpiresAt.ValueString()),
	}
	if known(plan.FindingID) {
		body["finding_id"] = plan.FindingID.ValueString()
	}
	if known(plan.RuleID) {
		body["rule_id"] = plan.RuleID.ValueString()
	}
	if known(plan.Port) {
		body["port"] = plan.Port.ValueInt64()
	}
	if known(plan.Protocol) {
		body["protocol"] = plan.Protocol.ValueString()
	}
	if known(plan.InstanceID) {
		body["instance_id"] = plan.InstanceID.ValueString()
	}
	e, err := r.client.CreateSecurityException(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not accept the risk", err.Error())
		return
	}
	m := securityExceptionFromAPI(plan, e)
	if !sameInstant(plan.ExpiresAt.ValueString(), e.ExpiresAt) {
		resp.Diagnostics.AddWarning("Shorter than asked",
			"The platform caps how long a risk can be accepted: this exception lapses on "+e.ExpiresAt+
				" (effective_expires_at), not on "+plan.ExpiresAt.ValueString()+". When it lapses it leaves the "+
				"state, and the next apply accepts it again.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *securityExceptionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state securityExceptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := r.find(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the security exceptions", err.Error())
		return
	}
	if e == nil {
		// Lapsed or revoked: no longer in force.
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, securityExceptionFromAPI(state, e))...)
}

func (r *securityExceptionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Everything replaces an exception but what only an import leaves
	// different: a finding_id an older API does not show, and the spelling of
	// a date or a justification. The configuration's is taken.
	var plan securityExceptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *securityExceptionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state securityExceptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.RevokeSecurityException(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not revoke the security exception", err.Error())
	}
}

func (r *securityExceptionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ── masterdaweb_security_settings ──────────────────────────────────────────

var (
	_ resource.Resource                = (*securitySettingsResource)(nil)
	_ resource.ResourceWithConfigure   = (*securitySettingsResource)(nil)
	_ resource.ResourceWithImportState = (*securitySettingsResource)(nil)
)

// securitySettingsID is the settings' id: an account has one set.
const securitySettingsID = "security_settings"

// NewSecuritySettingsResource is masterdaweb_security_settings.
func NewSecuritySettingsResource() resource.Resource { return &securitySettingsResource{} }

type securitySettingsResource struct{ client *client.Client }

type securitySettingsModel struct {
	ID                  types.String `tfsdk:"id"`
	Enabled             types.Bool   `tfsdk:"enabled"`
	DefaultFrequency    types.String `tfsdk:"default_frequency"`
	NotifyOnNewCritical types.Bool   `tfsdk:"notify_on_new_critical"`
	NotifyOnScoreDrop   types.Bool   `tfsdk:"notify_on_score_drop"`
}

func (r *securitySettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_settings"
}

func (r *securitySettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "How the Security Center scans the account and who is told: one set per account. A setting left " +
			"out is the account's own. Destroying this resource stops managing the settings; the account keeps them.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("Always " + securitySettingsID + "."),
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description:   "Whether the account's instances and resources are scanned (on by default).",
				PlanModifiers: keepBool,
			},
			"default_frequency": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "How often instances are scanned: weekly or monthly. A change applies to every instance at once.",
				Validators:    []validator.String{stringvalidator.OneOf("weekly", "monthly")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"notify_on_new_critical": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description:   "Whether a new critical finding is emailed.",
				PlanModifiers: keepBool,
			},
			"notify_on_score_drop": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description:   "Whether a drop of the security score is emailed.",
				PlanModifiers: keepBool,
			},
		},
	}
}

func (r *securitySettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func securitySettingsFromAPI(s *client.SecuritySettings) securitySettingsModel {
	return securitySettingsModel{
		ID:                  types.StringValue(securitySettingsID),
		Enabled:             types.BoolValue(s.Enabled),
		DefaultFrequency:    types.StringValue(s.DefaultFrequency),
		NotifyOnNewCritical: types.BoolValue(s.NotifyOnNewCritical),
		NotifyOnScoreDrop:   types.BoolValue(s.NotifyOnScoreDrop),
	}
}

// save sends what the plan sets that the prior settings do not have; with
// nothing to send, it reads them.
func (r *securitySettingsResource) save(ctx context.Context, plan securitySettingsModel, prior *securitySettingsModel, state interface {
	Set(context.Context, any) diagnostics
}, diags *diagnostics) {
	patch := map[string]any{}
	if known(plan.Enabled) && (prior == nil || !plan.Enabled.Equal(prior.Enabled)) {
		patch["enabled"] = plan.Enabled.ValueBool()
	}
	if known(plan.DefaultFrequency) && (prior == nil || !plan.DefaultFrequency.Equal(prior.DefaultFrequency)) {
		patch["default_frequency"] = plan.DefaultFrequency.ValueString()
	}
	if known(plan.NotifyOnNewCritical) && (prior == nil || !plan.NotifyOnNewCritical.Equal(prior.NotifyOnNewCritical)) {
		patch["notify_on_new_critical"] = plan.NotifyOnNewCritical.ValueBool()
	}
	if known(plan.NotifyOnScoreDrop) && (prior == nil || !plan.NotifyOnScoreDrop.Equal(prior.NotifyOnScoreDrop)) {
		patch["notify_on_score_drop"] = plan.NotifyOnScoreDrop.ValueBool()
	}
	var s *client.SecuritySettings
	var err error
	if len(patch) > 0 {
		s, err = r.client.UpdateSecuritySettings(ctx, patch)
	} else {
		s, err = r.client.GetSecuritySettings(ctx)
	}
	if err != nil {
		diags.AddError("Could not set the security settings", err.Error())
		return
	}
	diags.Append(state.Set(ctx, securitySettingsFromAPI(s))...)
}

func (r *securitySettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan securitySettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.save(ctx, plan, nil, &resp.State, &resp.Diagnostics)
}

func (r *securitySettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	s, err := r.client.GetSecuritySettings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the security settings", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, securitySettingsFromAPI(s))...)
}

func (r *securitySettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state securitySettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.save(ctx, plan, &state, &resp.State, &resp.Diagnostics)
}

// Delete leaves the settings as they are: turning scanning off because a
// configuration stopped managing it would be a surprise nobody asked for.
func (r *securitySettingsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *securitySettingsResource) ImportState(ctx context.Context, _ resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), securitySettingsID)...)
}
