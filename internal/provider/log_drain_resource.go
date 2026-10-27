package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                   = (*logDrainResource)(nil)
	_ resource.ResourceWithConfigure      = (*logDrainResource)(nil)
	_ resource.ResourceWithImportState    = (*logDrainResource)(nil)
	_ resource.ResourceWithValidateConfig = (*logDrainResource)(nil)
)

// NewLogDrainResource is masterdaweb_serverless_log_drain.
func NewLogDrainResource() resource.Resource { return &logDrainResource{} }

type logDrainResource struct{ client *client.Client }

type logDrainModel struct {
	ID          types.String `tfsdk:"id"`
	ProjectID   types.String `tfsdk:"project_id"`
	Kind        types.String `tfsdk:"kind"`
	URL         types.String `tfsdk:"url"`
	Token       types.String `tfsdk:"token"`
	Site        types.String `tfsdk:"site"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Destination types.String `tfsdk:"destination"`
	Status      types.String `tfsdk:"status"`
	LastError   types.String `tfsdk:"last_error"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// datadogSites are Datadog's sites by the host of their log intake.
var datadogSites = map[string]string{
	"http-intake.logs.datadoghq.com":     "us1",
	"http-intake.logs.us3.datadoghq.com": "us3",
	"http-intake.logs.us5.datadoghq.com": "us5",
	"http-intake.logs.datadoghq.eu":      "eu1",
	"http-intake.logs.ap1.datadoghq.com": "ap1",
	"http-intake.logs.ap2.datadoghq.com": "ap2",
}

func (r *logDrainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_serverless_log_drain"
}

func (r *logDrainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Sends a project's logs to an HTTPS endpoint of yours, Datadog or Better Stack, near real time " +
			"(about 30 seconds behind): every service's output from when it is made. A project takes up to 3. " +
			"Making or changing one takes an administrator's token.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The log drain's id."),
			"project_id": schema.StringAttribute{
				Required:      true,
				Description:   "The project whose logs are sent.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"kind": schema.StringAttribute{
				Required: true,
				Description: "HTTPS (your endpoint takes NDJSON lines: timestamp, message, project, service, revision, " +
					"instance), DATADOG or BETTERSTACK.",
				Validators:    []validator.String{stringvalidator.OneOf("HTTPS", "DATADOG", "BETTERSTACK")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"url": schema.StringAttribute{
				Optional: true,
				Description: "HTTPS: the endpoint's https:// address. BETTERSTACK: the source's ingesting host (default " +
					"in.logs.betterstack.com). Not used for Datadog. The API shows it shortened afterwards (destination); " +
					"changing it replaces the log drain.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 500)},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessImported()},
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "DATADOG: the API key. BETTERSTACK: the source token. HTTPS: the Authorization header to " +
					"send, like \"Bearer …\" (optional). Kept encrypted and never shown again; changing it replaces the log drain.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 1000)},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessImported()},
			},
			"site": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "DATADOG: the account's site, us1 (the default), us3, us5, eu1, ap1 or ap2.",
				Validators:    []validator.String{stringvalidator.OneOf("us1", "us3", "us5", "eu1", "ap1", "ap2")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether logs are sent. Turned back on, it resumes from where it stopped (within the 6 days " +
					"the logs are kept).",
			},
			"destination": computedString("Where the logs go, as the API shows it: the host and the last characters of the address."),
			"status": schema.StringAttribute{
				Computed: true,
				Description: "ok (sending), failing (retrying after errors), stopped (turned off after too many failed " +
					"sends in a row: set enabled to resume) or off, when last read.",
			},
			"last_error": schema.StringAttribute{Computed: true, Description: "What the destination last answered, when it failed."},
			"created_at": computedString("When the log drain was made (RFC 3339)."),
		},
	}
}

func (r *logDrainResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m logDrainModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !known(m.Kind) {
		return
	}
	switch m.Kind.ValueString() {
	case "HTTPS":
		if m.URL.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "Which endpoint?", "An HTTPS log drain needs url, an https:// address.")
		} else if known(m.URL) && !strings.HasPrefix(m.URL.ValueString(), "https://") {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "Logs are only sent over HTTPS", "Give url as an https:// address.")
		}
		if !m.Site.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("site"), "site is Datadog's", "Leave site out for an HTTPS log drain.")
		}
	case "DATADOG":
		if m.Token.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("token"), "The Datadog API key", "A Datadog log drain needs token: the account's API key.")
		}
		if !m.URL.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "Datadog's address comes from its site",
				"Leave url out for Datadog, and set site when the account is not on us1.")
		}
	case "BETTERSTACK":
		if m.Token.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("token"), "The Better Stack source token", "A Better Stack log drain needs token: the source's token.")
		}
		if !m.Site.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("site"), "site is Datadog's", "Leave site out for Better Stack.")
		}
	}
}

func (r *logDrainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// drainHost is the host of a destination as the API shows it.
func drainHost(display string) string {
	rest := strings.TrimPrefix(display, "https://")
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	return strings.ToLower(rest)
}

// drainDisplay is how the API would show an address: its host, and the
// last four characters of the rest when that is long.
func drainDisplay(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimSuffix(host, ":443")
	tail := u.EscapedPath()
	if tail == "" {
		tail = "/"
	}
	if u.RawQuery != "" {
		tail += "?" + u.RawQuery
	}
	if len(tail) > 8 {
		tail = "/…" + tail[len(tail)-4:]
	}
	return u.Scheme + "://" + host + tail, true
}

func logDrainFromAPI(prior logDrainModel, d *client.LogDrain) logDrainModel {
	m := logDrainModel{
		ID:          types.StringValue(d.ID),
		ProjectID:   types.StringValue(d.ProjectID),
		Kind:        types.StringValue(d.Kind),
		URL:         prior.URL,
		Token:       prior.Token,
		Site:        types.StringNull(),
		Enabled:     types.BoolValue(d.Enabled),
		Destination: types.StringValue(d.URL),
		Status:      types.StringValue(d.Status),
		LastError:   stringOrNull(d.LastError),
		CreatedAt:   types.StringValue(d.CreatedAt),
	}
	if d.Kind == "DATADOG" {
		if site, ok := datadogSites[drainHost(d.URL)]; ok {
			m.Site = types.StringValue(site)
		} else if known(prior.Site) {
			m.Site = prior.Site
		}
	}
	return m
}

func (r *logDrainResource) find(ctx context.Context, projectID, id string) (*client.LogDrain, error) {
	all, err := r.client.ListLogDrains(ctx, projectID)
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

func (r *logDrainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan logDrainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"kind": plan.Kind.ValueString()}
	if known(plan.URL) {
		body["url"] = plan.URL.ValueString()
	}
	if known(plan.Token) {
		body["token"] = plan.Token.ValueString()
	}
	if known(plan.Site) {
		body["site"] = plan.Site.ValueString()
	}
	d, err := r.client.CreateLogDrain(ctx, plan.ProjectID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Could not create the log drain", err.Error())
		return
	}
	if !plan.Enabled.ValueBool() && d.Enabled {
		off, err := r.client.SetLogDrainEnabled(ctx, d.ID, false)
		if err != nil {
			resp.Diagnostics.AddWarning("The log drain is on",
				"The log drain was created, but could not be turned off: "+err.Error()+" The next apply tries again.")
		} else {
			d = off
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, logDrainFromAPI(plan, d))...)
}

func (r *logDrainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state logDrainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.find(ctx, state.ProjectID.ValueString(), state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the project's log drains", err.Error())
		return
	}
	if d == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, logDrainFromAPI(state, d))...)
}

func (r *logDrainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state logDrainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	// After an import the address is only known shortened: the
	// configuration's is taken when it looks like the same one.
	if known(plan.URL) && state.URL.IsNull() && plan.Kind.ValueString() == "HTTPS" {
		if want, ok := drainDisplay(plan.URL.ValueString()); ok && want != state.Destination.ValueString() {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "Not this log drain's address",
				fmt.Sprintf("The log drain sends to %s, which does not look like %s. Fix url, or replace the log drain.",
					state.Destination.ValueString(), plan.URL.ValueString()))
			return
		}
	}
	var d *client.LogDrain
	var err error
	if !plan.Enabled.Equal(state.Enabled) {
		d, err = r.client.SetLogDrainEnabled(ctx, id, plan.Enabled.ValueBool())
	} else {
		d, err = r.find(ctx, plan.ProjectID.ValueString(), id)
		if err == nil && d == nil {
			err = fmt.Errorf("the log drain %s is gone", id)
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not update the log drain", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, logDrainFromAPI(plan, d))...)
}

func (r *logDrainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state logDrainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteLogDrain(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the log drain", err.Error())
	}
}

// ImportState takes <project_id>/<log drain id>, or the log drain's id alone
// (the account's projects are then searched).
func (r *logDrainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := strings.TrimSpace(req.ID)
	projectID, id, composite := strings.Cut(ref, "/")
	if !composite {
		id, projectID = ref, ""
		projects, err := r.client.ListProjects(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the projects", err.Error())
			return
		}
		for _, p := range projects {
			d, err := r.find(ctx, p.ID, id)
			if err != nil && !client.IsNotFound(err) {
				resp.Diagnostics.AddError("Could not read the project's log drains", err.Error())
				return
			}
			if d != nil {
				projectID = p.ID
				break
			}
		}
		if projectID == "" {
			resp.Diagnostics.AddError("No such log drain",
				fmt.Sprintf("No project has the log drain %q. Import a log drain as <project_id>/<id> or by its id.", ref))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
}
