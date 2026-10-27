package provider

import (
	"context"
	"fmt"
	"regexp"
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
	_ resource.Resource                = (*variableResource)(nil)
	_ resource.ResourceWithConfigure   = (*variableResource)(nil)
	_ resource.ResourceWithImportState = (*variableResource)(nil)
)

// NewVariableResource is masterdaweb_serverless_variable.
func NewVariableResource() resource.Resource { return &variableResource{} }

type variableResource struct{ client *client.Client }

type variableModel struct {
	ID               types.String `tfsdk:"id"`
	ServiceID        types.String `tfsdk:"service_id"`
	Key              types.String `tfsdk:"key"`
	Value            types.String `tfsdk:"value"`
	Secret           types.Bool   `tfsdk:"secret"`
	AvailableAtBuild types.Bool   `tfsdk:"available_at_build"`
	Deploy           types.Bool   `tfsdk:"deploy"`
	Version          types.Int64  `tfsdk:"version"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

var variableKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (r *variableResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_serverless_variable"
}

func (r *variableResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A service's environment variable. A secret is write-only: once saved, the API never shows its " +
			"value again, so Terraform keeps the configured one. Each change starts a deployment unless deploy is " +
			"false; removing a variable always does.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				Description:         "<service_id>/<key>.",
				MarkdownDescription: "`<service_id>/<key>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"service_id": schema.StringAttribute{
				Required:      true,
				Description:   "The service the variable belongs to.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"key": schema.StringAttribute{
				Required: true,
				Description: "The variable's name: letters, digits and underscores, not starting with a digit. PORT, " +
					"K_* and MW_* are the platform's.",
				MarkdownDescription: "The variable's name: letters, digits and underscores, not starting with a digit. `PORT`, " +
					"`K_*` and `MW_*` are the platform's.",
				Validators: []validator.String{
					stringvalidator.LengthAtMost(255),
					stringvalidator.RegexMatches(variableKeyRe, "must be letters, digits and underscores, not starting with a digit"),
					reservedKeyValidator{},
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"value": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "The value. A secret's value is never read back: Terraform keeps the configured one.",
			},
			"secret": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether the value is write-only once saved. A secret stays a secret: setting it back to " +
					"false replaces the variable.",
				PlanModifiers: []planmodifier.Bool{requiresReplaceWhenTurnedOff()},
			},
			"available_at_build": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether the build gets it too (NEXT_PUBLIC_* and the like).",
			},
			"deploy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether setting or changing the variable starts a deployment. False saves it for the " +
					"next deployment. Removing a variable always starts one.",
			},
			"version": schema.Int64Attribute{
				Computed:    true,
				Description: "Goes up each time the value changes. A secret changed elsewhere shows up as a change to apply.",
			},
			"updated_at": schema.StringAttribute{
				Computed:    true,
				Description: "When the variable last changed (RFC 3339).",
			},
		},
	}
}

func (r *variableResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// reservedKeyValidator refuses the names the platform sets.
type reservedKeyValidator struct{}

func (reservedKeyValidator) Description(context.Context) string {
	return "PORT, K_SERVICE, K_REVISION, K_CONFIGURATION and names starting with MW_ are the platform's."
}

func (v reservedKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (reservedKeyValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	k := req.ConfigValue.ValueString()
	switch {
	case k == "PORT" || k == "K_SERVICE" || k == "K_REVISION" || k == "K_CONFIGURATION":
		resp.Diagnostics.AddAttributeError(req.Path, "A name the platform sets",
			k+" is set by the platform and cannot be defined.")
	case strings.HasPrefix(strings.ToUpper(k), "MW_"):
		resp.Diagnostics.AddAttributeError(req.Path, "A name the platform sets",
			"Names starting with MW_ are the platform's. A binding's variables (MW_STORAGE_…) come from the binding.")
	}
}

func variableID(serviceID, key string) string { return serviceID + "/" + key }

// variableFromAPI is the state of a variable as the API has it. A secret's
// value never comes back: the configured one stays, unless the variable's
// version moved — then it was changed elsewhere, and the value is left
// empty so the next plan sets it again.
func variableFromAPI(prior variableModel, serviceID string, v client.Variable) variableModel {
	m := variableModel{
		ID:               types.StringValue(variableID(serviceID, v.Key)),
		ServiceID:        types.StringValue(serviceID),
		Key:              types.StringValue(v.Key),
		Value:            prior.Value,
		Secret:           types.BoolValue(v.IsSecret),
		AvailableAtBuild: types.BoolValue(v.AvailableAtBuild),
		Deploy:           prior.Deploy,
		Version:          types.Int64Value(v.Version),
		UpdatedAt:        types.StringValue(v.UpdatedAt),
	}
	if !known(m.Deploy) {
		m.Deploy = types.BoolValue(true)
	}
	switch {
	case v.Value != nil:
		m.Value = types.StringValue(*v.Value)
	case known(prior.Version) && prior.Version.ValueInt64() != v.Version:
		m.Value = types.StringNull()
	}
	return m
}

func (r *variableResource) find(ctx context.Context, serviceID, key string) (*client.Variable, error) {
	all, err := r.client.ListVariables(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Key == key {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (r *variableResource) set(ctx context.Context, m variableModel, withValue bool) error {
	in := client.VariableInput{Key: m.Key.ValueString()}
	if withValue {
		v := m.Value.ValueString()
		in.Value = &v
	}
	secret := m.Secret.ValueBool()
	build := m.AvailableAtBuild.ValueBool()
	in.IsSecret = &secret
	in.AvailableAtBuild = &build
	_, err := r.client.SetVariables(ctx, m.ServiceID.ValueString(), []client.VariableInput{in}, m.Deploy.ValueBool())
	return err
}

func (r *variableResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan variableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serviceID, key := plan.ServiceID.ValueString(), plan.Key.ValueString()
	existing, err := r.find(ctx, serviceID, key)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the service's variables", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddError("The variable already exists",
			fmt.Sprintf("The service already has a variable %s. Import it to manage it here: terraform import <address> %s.",
				key, variableID(serviceID, key)))
		return
	}
	err = r.set(ctx, plan, true)
	switch {
	case err == nil:
	case isDeployRefusal(err):
		deployRefusedWarning(&resp.Diagnostics, "The variable "+key, err)
	default:
		resp.Diagnostics.AddError("Could not set the variable", err.Error())
		return
	}
	v, err := r.find(ctx, serviceID, key)
	if err != nil || v == nil {
		msg := "The variable was set, but did not come back when read."
		if err != nil {
			msg = err.Error()
		}
		resp.Diagnostics.AddError("Could not read the variable", msg)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, variableFromAPI(plan, serviceID, *v))...)
}

func (r *variableResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state variableModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serviceID, key := state.ServiceID.ValueString(), state.Key.ValueString()
	v, err := r.find(ctx, serviceID, key)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the service's variables", err.Error())
		return
	}
	if v == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, variableFromAPI(state, serviceID, *v))...)
}

func (r *variableResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state variableModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serviceID, key := plan.ServiceID.ValueString(), plan.Key.ValueString()
	if !plan.Value.Equal(state.Value) || !plan.Secret.Equal(state.Secret) || !plan.AvailableAtBuild.Equal(state.AvailableAtBuild) {
		err := r.set(ctx, plan, !plan.Value.Equal(state.Value))
		switch {
		case err == nil:
		case isDeployRefusal(err):
			deployRefusedWarning(&resp.Diagnostics, "The variable "+key, err)
		default:
			resp.Diagnostics.AddError("Could not change the variable", err.Error())
			return
		}
	}
	v, err := r.find(ctx, serviceID, key)
	if err != nil || v == nil {
		msg := "The variable did not come back when read."
		if err != nil {
			msg = err.Error()
		}
		resp.Diagnostics.AddError("Could not read the variable", msg)
		return
	}
	// The version the change made is the one to compare with from now on.
	resp.Diagnostics.Append(resp.State.Set(ctx, variableFromAPI(variableModel{
		Value: plan.Value, Deploy: plan.Deploy,
	}, serviceID, *v))...)
}

func (r *variableResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state variableModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.DeleteVariables(ctx, state.ServiceID.ValueString(), []string{state.Key.ValueString()})
	switch {
	case err == nil, client.IsNotFound(err):
	case isDeployRefusal(err):
		deployRefusedWarning(&resp.Diagnostics, "Removing "+state.Key.ValueString(), err)
	default:
		resp.Diagnostics.AddError("Could not remove the variable", err.Error())
	}
}

func (r *variableResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	serviceID, key, ok := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !ok || serviceID == "" || key == "" {
		resp.Diagnostics.AddError("Import a variable as <service_id>/<key>",
			fmt.Sprintf("%q is not <service_id>/<key>, like 0193f0e2-…/DATABASE_URL.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), variableID(serviceID, key))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_id"), serviceID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deploy"), true)...)
}
