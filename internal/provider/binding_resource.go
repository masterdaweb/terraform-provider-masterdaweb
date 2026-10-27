package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                   = (*bindingResource)(nil)
	_ resource.ResourceWithConfigure      = (*bindingResource)(nil)
	_ resource.ResourceWithImportState    = (*bindingResource)(nil)
	_ resource.ResourceWithValidateConfig = (*bindingResource)(nil)
)

// NewBindingResource is masterdaweb_serverless_binding.
func NewBindingResource() resource.Resource { return &bindingResource{} }

type bindingResource struct{ client *client.Client }

type bindingModel struct {
	ID           types.String `tfsdk:"id"`
	ServiceID    types.String `tfsdk:"service_id"`
	ResourceKind types.String `tfsdk:"resource_kind"`
	ResourceName types.String `tfsdk:"resource_name"`
	EnvProfile   types.String `tfsdk:"env_profile"`
	EnvPrefix    types.String `tfsdk:"env_prefix"`
	Access       types.String `tfsdk:"access"`
	Deploy       types.Bool   `tfsdk:"deploy"`
	ResourceID   types.String `tfsdk:"resource_id"`
	Variables    types.List   `tfsdk:"variables"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

var envPrefixRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,30}_$`)

// bindingProfiles are the variable layouts each kind of resource takes.
var bindingProfiles = map[string][]string{
	"POSTGRES": {"url", "framework", "libpq", "prefixed", "dotnet"},
	"MYSQL":    {"url", "framework", "prefixed", "dotnet"},
	"REDIS":    {"url", "framework", "prefixed", "dotnet"},
	"BUCKET":   {"prefixed", "framework"},
}

func replaceKeepingState() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}
}

func (r *bindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_serverless_binding"
}

func (r *bindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Gives a service a bucket or a database as environment variables, with a credential of its own " +
			"(never the owner's). Binding and unbinding start a deployment (binding unless deploy is false). Any " +
			"change replaces the binding: the service gets a new credential.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The binding's id."),
			"service_id": schema.StringAttribute{
				Required:      true,
				Description:   "The service that gets the variables.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_kind": schema.StringAttribute{
				Required:      true,
				Description:   "BUCKET, POSTGRES, MYSQL (a MySQL or MariaDB database) or REDIS.",
				Validators:    []validator.String{stringvalidator.OneOf("BUCKET", "POSTGRES", "MYSQL", "REDIS")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"resource_name": schema.StringAttribute{
				Required:      true,
				Description:   "The bucket's or the database's name.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 63)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"env_profile": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The variables' layout. Buckets: prefixed (the default) or framework (AWS_*). PostgreSQL: " +
					"url (DATABASE_URL, the default), framework (DB_*), libpq (PG*), prefixed or dotnet " +
					"(ConnectionStrings__<name>). MySQL, MariaDB and Redis: url, framework, prefixed or dotnet.",
				MarkdownDescription: "The variables' layout. Buckets: `prefixed` (the default) or `framework` (`AWS_*`). PostgreSQL: " +
					"`url` (`DATABASE_URL`, the default), `framework` (`DB_*`), `libpq` (`PG*`), `prefixed` or `dotnet` " +
					"(`ConnectionStrings__<name>`). MySQL, MariaDB and Redis: `url`, `framework`, `prefixed` or `dotnet`.",
				Validators:    []validator.String{stringvalidator.OneOf("url", "framework", "libpq", "prefixed", "dotnet")},
				PlanModifiers: replaceKeepingState(),
			},
			"env_prefix": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The variables' prefix: capitals, digits and underscores, ending in an underscore, like " +
					"MW_STORAGE_ (the prefixed layout's default for buckets). With dotnet it names the connection " +
					"string (SHOP_DB_ is ShopDb; none is DefaultConnection, or Redis for Redis).",
				MarkdownDescription: "The variables' prefix: capitals, digits and underscores, ending in an underscore, like " +
					"`MW_STORAGE_` (the `prefixed` layout's default for buckets). With `dotnet` it names the connection " +
					"string (`SHOP_DB_` is `ShopDb`; none is `DefaultConnection`, or `Redis` for Redis).",
				Validators: []validator.String{stringvalidator.Any(
					stringvalidator.OneOf(""),
					stringvalidator.RegexMatches(envPrefixRe, "must be capitals, digits and underscores, ending in an underscore, like MW_STORAGE_"),
				)},
				PlanModifiers: replaceKeepingState(),
			},
			"access": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Buckets: read_write (the default) or read_only. A database binding always has the " +
					"owner's rights (read_write).",
				Validators:    []validator.String{stringvalidator.OneOf("read_write", "read_only")},
				PlanModifiers: replaceKeepingState(),
			},
			"deploy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether binding starts a deployment that gives the service the variables. False saves it " +
					"for the next deployment. Unbinding always starts one.",
			},
			"resource_id": computedString("The bucket's or the database's id."),
			"variables": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "The names of the variables the binding gives the service (never their values).",
				// An update only ever changes deploy, with no call: they stay.
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"created_at": computedString("When the binding was made (RFC 3339)."),
		},
	}
}

func (r *bindingResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m bindingModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !known(m.ResourceKind) {
		return
	}
	kind := m.ResourceKind.ValueString()
	if known(m.EnvProfile) {
		ok := false
		for _, p := range bindingProfiles[kind] {
			ok = ok || p == m.EnvProfile.ValueString()
		}
		if !ok {
			resp.Diagnostics.AddAttributeError(path.Root("env_profile"), "A layout this resource does not take",
				fmt.Sprintf("A %s binding takes %s.", kind, strings.Join(bindingProfiles[kind], ", ")))
		}
	}
	if known(m.Access) && kind != "BUCKET" && m.Access.ValueString() != "read_write" {
		resp.Diagnostics.AddAttributeError(path.Root("access"), "A database binding has the owner's rights",
			"Leave access out (or read_write) for a database: read_only is for buckets.")
	}
	profile := ""
	if known(m.EnvProfile) {
		profile = m.EnvProfile.ValueString()
	}
	if known(m.EnvPrefix) && m.EnvPrefix.ValueString() != "" && (profile == "framework" || profile == "libpq") {
		resp.Diagnostics.AddAttributeError(path.Root("env_prefix"), "This layout has fixed names",
			"The "+profile+" layout uses fixed names: pick the prefixed layout to add a prefix.")
	}
	if profile == "prefixed" && known(m.EnvPrefix) && m.EnvPrefix.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("env_prefix"), "The prefixed layout needs a prefix",
			"Give env_prefix, like MW_STORAGE_, or leave it out for the default.")
	}
}

func (r *bindingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func bindingFromAPI(prior bindingModel, serviceID string, b *client.Binding) bindingModel {
	m := bindingModel{
		ID:           types.StringValue(b.ID),
		ServiceID:    types.StringValue(serviceID),
		ResourceKind: types.StringValue(b.ResourceKind),
		ResourceName: prior.ResourceName,
		EnvProfile:   types.StringValue(b.EnvProfile),
		EnvPrefix:    types.StringValue(b.EnvPrefix),
		Access:       types.StringValue(b.Access),
		Deploy:       prior.Deploy,
		ResourceID:   types.StringValue(b.ResourceID),
		Variables:    stringList(b.Variables),
		CreatedAt:    types.StringValue(b.CreatedAt),
	}
	// Null once the resource is gone: the name it was made with stays.
	if b.ResourceName != nil {
		m.ResourceName = types.StringValue(*b.ResourceName)
	}
	if !known(m.Deploy) {
		m.Deploy = types.BoolValue(true)
	}
	return m
}

func (r *bindingResource) find(ctx context.Context, serviceID string, match func(*client.Binding) bool) (*client.Binding, error) {
	all, err := r.client.ListBindings(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if match(&all[i]) {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (r *bindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serviceID := plan.ServiceID.ValueString()
	kind, name := plan.ResourceKind.ValueString(), plan.ResourceName.ValueString()
	body := map[string]any{
		"resource_kind": kind,
		"resource":      name,
		"deploy":        plan.Deploy.ValueBool(),
	}
	if known(plan.EnvProfile) {
		body["env_profile"] = plan.EnvProfile.ValueString()
	}
	if known(plan.EnvPrefix) {
		body["env_prefix"] = plan.EnvPrefix.ValueString()
	}
	if known(plan.Access) && kind == "BUCKET" {
		body["access"] = plan.Access.ValueString()
	}
	out, err := r.client.CreateBinding(ctx, serviceID, body)
	var b *client.Binding
	switch {
	case err == nil && out.Binding != nil:
		b = out.Binding
	case err == nil:
		resp.Diagnostics.AddError("Could not bind the resource", "The API answered without the binding it made.")
		return
	case isDeployRefusal(err):
		// Bound, its deployment refused: keep it, say why.
		found, ferr := r.find(ctx, serviceID, func(x *client.Binding) bool {
			return x.ResourceKind == kind && x.ResourceName != nil && *x.ResourceName == name
		})
		if ferr != nil || found == nil {
			resp.Diagnostics.AddError("Could not bind the resource", err.Error())
			return
		}
		b = found
		deployRefusedWarning(&resp.Diagnostics, "The binding of "+name, err)
	default:
		resp.Diagnostics.AddError("Could not bind the resource", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, bindingFromAPI(plan, serviceID, b))...)
}

func (r *bindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serviceID, id := state.ServiceID.ValueString(), state.ID.ValueString()
	b, err := r.find(ctx, serviceID, func(x *client.Binding) bool { return x.ID == id })
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the service's bindings", err.Error())
		return
	}
	if b == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, bindingFromAPI(state, serviceID, b))...)
}

// Update only ever changes deploy: every other argument replaces the binding.
func (r *bindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state bindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Deploy = plan.Deploy
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *bindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteBinding(ctx, state.ServiceID.ValueString(), state.ID.ValueString())
	switch {
	case err == nil, client.IsNotFound(err):
	case client.HasStatus(err, http.StatusBadRequest) && client.HasReason(err, "SERVERLESS_INVALID_SETTING") && isMissingBinding(err):
		// The API says "No such binding on this service" with a 400: gone already.
	case isDeployRefusal(err):
		deployRefusedWarning(&resp.Diagnostics, "Unbinding "+state.ResourceName.ValueString(), err)
	default:
		resp.Diagnostics.AddError("Could not unbind the resource", err.Error())
	}
}

func isMissingBinding(err error) bool {
	p, ok := client.AsProblem(err)
	if !ok {
		return false
	}
	field, _ := p.Params["field"].(string)
	return field == "binding_id"
}

func (r *bindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	serviceID, bindingRef, ok := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !ok || serviceID == "" || bindingRef == "" {
		resp.Diagnostics.AddError("Import a binding as <service_id>/<binding_id>",
			fmt.Sprintf("%q is not <service_id>/<binding_id> (or <service_id>/<resource name>).", req.ID))
		return
	}
	b, err := r.find(ctx, serviceID, func(x *client.Binding) bool {
		return x.ID == bindingRef || (x.ResourceName != nil && *x.ResourceName == bindingRef)
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not read the service's bindings", err.Error())
		return
	}
	if b == nil {
		resp.Diagnostics.AddError("No such binding", fmt.Sprintf("The service %s has no binding %s.", serviceID, bindingRef))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), b.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_id"), serviceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deploy"), true)...)
}
