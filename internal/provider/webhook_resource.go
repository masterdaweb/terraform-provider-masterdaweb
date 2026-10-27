package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                   = (*webhookResource)(nil)
	_ resource.ResourceWithConfigure      = (*webhookResource)(nil)
	_ resource.ResourceWithImportState    = (*webhookResource)(nil)
	_ resource.ResourceWithValidateConfig = (*webhookResource)(nil)
)

// NewWebhookResource is masterdaweb_webhook.
func NewWebhookResource() resource.Resource { return &webhookResource{} }

type webhookResource struct{ client *client.Client }

type webhookModel struct {
	ID             types.String `tfsdk:"id"`
	Kind           types.String `tfsdk:"kind"`
	URL            types.String `tfsdk:"url"`
	Description    types.String `tfsdk:"description"`
	Events         types.Set    `tfsdk:"events"`
	Enabled        types.Bool   `tfsdk:"enabled"`
	Locale         types.String `tfsdk:"locale"`
	Secret         types.String `tfsdk:"secret"`
	MaskedURL      types.String `tfsdk:"masked_url"`
	DisabledReason types.String `tfsdk:"disabled_reason"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// An event, or a family of them (alert.*): GET /v1/webhooks/events lists
// them.
var webhookEventRe = regexp.MustCompile(`^[a-z][a-z_]*\.([a-z][a-z_]*|\*)$`)

func (r *webhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webhook"
}

func (r *webhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A webhook: the account's events (deployments, alerts, databases, billing) sent to an address of " +
			"yours as signed JSON, or as a message to a Slack or Discord channel.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The webhook's id."),
			"kind": schema.StringAttribute{
				Optional: true, Computed: true,
				Default: stringdefault.StaticString("GENERIC"),
				Description: "GENERIC (your https:// address gets JSON signed with MW-Signature), SLACK or DISCORD (an " +
					"incoming webhook's address). Changing it replaces the webhook.",
				Validators:    []validator.String{stringvalidator.OneOf("GENERIC", "SLACK", "DISCORD")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"url": schema.StringAttribute{
				Required: true, Sensitive: true,
				Description: "Where events go, an https:// address. It is a credential: the API never shows it again " +
					"(masked_url). Changing it replaces the webhook.",
				Validators: []validator.String{
					stringvalidator.LengthBetween(9, 2000),
					stringvalidator.RegexMatches(regexp.MustCompile(`^https://`), "must be an https:// address"),
				},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessImported()},
			},
			"description": schema.StringAttribute{
				Optional: true, Description: "What it is for, up to 255 characters. Changed in place.",
				Validators: []validator.String{stringvalidator.LengthAtMost(255)},
			},
			"events": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "The events it gets, like deployment.failed, or a family like alert.*; every event when " +
					"left out. Changed in place.",
				Validators: []validator.Set{
					setvalidator.SizeAtMost(50),
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(webhookEventRe, "must be an event like deployment.failed, or a family like alert.*")),
				},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(true),
				Description: "Whether it gets events. The platform switches off a webhook that keeps failing " +
					"(disabled_reason): the next apply switches it on again.",
			},
			"locale": schema.StringAttribute{
				Optional: true,
				Description: "The language of a Slack or Discord message, like pt-BR or en. Changing it replaces the " +
					"webhook.",
				Validators:    []validator.String{stringvalidator.LengthAtMost(10)},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessImported()},
			},
			"secret": schema.StringAttribute{
				Computed: true, Sensitive: true,
				Description: "GENERIC: the secret that signs deliveries (MW-Signature: t=…,v1=…, an HMAC-SHA256 of " +
					"<t>.<body>), shown once by the API (not imported).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"masked_url":      computedString("The address as the API shows it: its host and last characters."),
			"disabled_reason": schema.StringAttribute{Computed: true, Description: "Why the platform switched it off: failing or removed."},
			"created_at":      computedString("When it was added (RFC 3339)."),
		},
	}
}

func (r *webhookResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var kind, locale types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("kind"), &kind)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("locale"), &locale)...)
	if known(locale) && (kind.IsNull() || kind.ValueString() == "GENERIC") {
		resp.Diagnostics.AddAttributeError(path.Root("locale"), "A locale is for messages",
			"locale is the language of a Slack or Discord message: a GENERIC webhook gets JSON.")
	}
}

func (r *webhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func webhookFromAPI(prior webhookModel, w *client.Webhook) webhookModel {
	m := prior
	m.ID = types.StringValue(w.ID)
	m.Kind = types.StringValue(w.Kind)
	m.Description = keepEquivalent(prior.Description, w.Description)
	events := append([]string{}, w.Events...)
	sort.Strings(events)
	switch {
	case len(events) == 0 && !known(prior.Events):
		// Every event: as the configuration says it (left out).
		m.Events = types.SetNull(types.StringType)
	default:
		m.Events = stringSet(events)
	}
	m.Enabled = types.BoolValue(w.Enabled)
	m.MaskedURL = types.StringValue(w.URL)
	m.DisabledReason = stringOrNull(w.DisabledReason)
	m.CreatedAt = types.StringValue(w.CreatedAt)
	if m.Secret.IsUnknown() {
		m.Secret = types.StringNull()
	}
	return m
}

// find is the webhook by its id, nil when it is gone: GET /v1/webhooks/{id},
// or the list on an API from before it.
func (r *webhookResource) find(ctx context.Context, id string) (*client.Webhook, error) {
	w, err := r.client.GetWebhook(ctx, id)
	if err == nil {
		return w, nil
	}
	if !client.IsNotFound(err) && !client.HasStatus(err, 405) {
		return nil, err
	}
	all, err := r.client.ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (r *webhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"kind": plan.Kind.ValueString(), "url": strings.TrimSpace(plan.URL.ValueString())}
	if known(plan.Description) && plan.Description.ValueString() != "" {
		body["description"] = plan.Description.ValueString()
	}
	if events := listStrings(ctx, plan.Events); len(events) > 0 {
		sort.Strings(events)
		body["events"] = events
	}
	if known(plan.Locale) {
		body["locale"] = plan.Locale.ValueString()
	}
	created, err := r.client.CreateWebhook(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not add the webhook", err.Error())
		return
	}
	w := &created.Webhook
	if !plan.Enabled.ValueBool() {
		if w, err = r.client.UpdateWebhook(ctx, w.ID, map[string]any{"enabled": false}); err != nil {
			resp.Diagnostics.AddError("Could not switch the webhook off", err.Error())
			return
		}
	}
	m := webhookFromAPI(plan, w)
	m.Secret = stringOrNull(created.Secret)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *webhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w, err := r.find(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not read the webhooks", err.Error())
		return
	}
	if w == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, webhookFromAPI(state, w))...)
}

func (r *webhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state webhookModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := map[string]any{}
	if !plan.Description.Equal(state.Description) {
		if known(plan.Description) && plan.Description.ValueString() != "" {
			patch["description"] = plan.Description.ValueString()
		} else {
			patch["description"] = nil
		}
	}
	if !plan.Events.Equal(state.Events) {
		events := listStrings(ctx, plan.Events)
		if events == nil {
			events = []string{}
		}
		sort.Strings(events)
		patch["events"] = events
	}
	if !plan.Enabled.Equal(state.Enabled) {
		patch["enabled"] = plan.Enabled.ValueBool()
	}
	w := (*client.Webhook)(nil)
	var err error
	if len(patch) > 0 {
		w, err = r.client.UpdateWebhook(ctx, state.ID.ValueString(), patch)
	} else {
		w, err = r.find(ctx, state.ID.ValueString())
	}
	if err == nil && w == nil {
		err = fmt.Errorf("the webhook %s is gone", state.ID.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not update the webhook", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, webhookFromAPI(plan, w))...)
}

func (r *webhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state webhookModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteWebhook(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not remove the webhook", err.Error())
	}
}

func (r *webhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
