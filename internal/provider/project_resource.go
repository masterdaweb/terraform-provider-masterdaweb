package provider

import (
	"context"
	"fmt"
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
	_ resource.Resource                = (*projectResource)(nil)
	_ resource.ResourceWithConfigure   = (*projectResource)(nil)
	_ resource.ResourceWithImportState = (*projectResource)(nil)
)

// NewProjectResource is masterdaweb_project.
func NewProjectResource() resource.Resource { return &projectResource{} }

type projectResource struct{ client *client.Client }

type projectModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
	Region             types.String `tfsdk:"region"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A project: what groups instances, databases, buckets and serverless services under one name. " +
			"Instances, databases and buckets join it with masterdaweb_project_resource (or project_id on an " +
			"instance or a database); services are made in it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The project's id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The project's name: 1 to 64 characters, unique in the account. It can be changed.",
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "What the project is for, up to 500 characters.",
				Validators:  []validator.String{stringvalidator.LengthAtMost(500)},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				Description: "Whether the project is protected from deletion. On by default: to destroy the project, " +
					"set it to false and apply first.",
			},
			"region": schema.StringAttribute{
				Computed:      true,
				Description:   "Where the project's serverless services run.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "When the project was created (RFC 3339).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// normalizeProjectName is a project name as the API keeps it: trimmed, its
// runs of spaces made one.
func normalizeProjectName(s string) string { return strings.Join(strings.Fields(s), " ") }

func projectFromAPI(prior projectModel, p *client.Project) projectModel {
	m := projectModel{
		ID:                 types.StringValue(p.ID),
		Name:               types.StringValue(p.Name),
		Description:        keepEquivalent(prior.Description, p.Description),
		DeletionProtection: types.BoolValue(p.DeletionProtection),
		Region:             types.StringValue(p.Region),
		CreatedAt:          types.StringValue(p.CreatedAt),
	}
	if known(prior.Name) && normalizeProjectName(prior.Name.ValueString()) == p.Name {
		m.Name = prior.Name
	}
	return m
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString()}
	if known(plan.Description) && plan.Description.ValueString() != "" {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.DeletionProtection.ValueBool() {
		body["deletion_protection"] = false
	}
	p, err := r.client.CreateProject(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not create the project", err.Error())
		return
	}
	if !plan.DeletionProtection.ValueBool() && p.DeletionProtection {
		updated, err := r.client.UpdateProject(ctx, p.ID, map[string]any{"deletion_protection": false})
		if err != nil {
			resp.Diagnostics.AddWarning("Deletion protection is still on",
				fmt.Sprintf("The project %s was created, but its deletion protection could not be switched off: %s "+
					"The next apply tries again.", p.Name, err))
		} else {
			p = updated
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(plan, p))...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.GetProject(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the project", err.Error())
		return
	}
	if p.Deleting {
		// On its way out: as good as gone.
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(state, p))...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		patch["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(state.Description) {
		if plan.Description.IsNull() || plan.Description.ValueString() == "" {
			patch["description"] = nil
		} else {
			patch["description"] = plan.Description.ValueString()
		}
	}
	if !plan.DeletionProtection.Equal(state.DeletionProtection) {
		patch["deletion_protection"] = plan.DeletionProtection.ValueBool()
	}
	var p *client.Project
	var err error
	if len(patch) > 0 {
		p, err = r.client.UpdateProject(ctx, state.ID.ValueString(), patch)
	} else {
		p, err = r.client.GetProject(ctx, state.ID.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not update the project", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectFromAPI(plan, p))...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Deletion protection is on",
			fmt.Sprintf("The project %s is protected from deletion. Set deletion_protection = false, run terraform apply, "+
				"and then destroy it.", state.Name.ValueString()))
		return
	}
	id := state.ID.ValueString()
	// The API confirms with the name as it keeps it.
	name := normalizeProjectName(state.Name.ValueString())
	if err := r.client.DeleteProject(ctx, id, name); err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Could not delete the project", err.Error())
		return
	}
	err := waitUntil(ctx, deleteTimeout, func() (bool, error) {
		_, err := r.client.GetProject(ctx, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddWarning("The project is still being deleted",
			fmt.Sprintf("The project %s is being deleted and finishes on its own (%s). Its name is free again once it is gone.",
				name, err))
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !isUUID(id) {
		all, err := r.client.ListProjects(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the projects", err.Error())
			return
		}
		found := ""
		for _, p := range all {
			if p.Name == normalizeProjectName(id) && !p.Deleting {
				found = p.ID
			}
		}
		if found == "" {
			resp.Diagnostics.AddError("No such project",
				fmt.Sprintf("No project has the id or name %q. Import a project by its id or its name.", id))
			return
		}
		id = found
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
