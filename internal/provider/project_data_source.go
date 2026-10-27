package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ datasource.DataSource                     = (*projectDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*projectDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*projectDataSource)(nil)
)

// NewProjectDataSource is the masterdaweb_project data source.
func NewProjectDataSource() datasource.DataSource { return &projectDataSource{} }

type projectDataSource struct{ client *client.Client }

type projectDataModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	Region             types.String `tfsdk:"region"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
	DeploysFrozen      types.Bool   `tfsdk:"deploys_frozen"`
	Paused             types.Bool   `tfsdk:"paused"`
	InstanceCount      types.Int64  `tfsdk:"instance_count"`
	ServiceCount       types.Int64  `tfsdk:"service_count"`
	DatabaseCount      types.Int64  `tfsdk:"database_count"`
	BucketCount        types.Int64  `tfsdk:"bucket_count"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a project by its id or its name, with how much it groups.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The project's id. Give the id or the name.",
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The project's name. Give the id or the name.",
			},
			"description":         schema.StringAttribute{Computed: true, Description: "What the project is for."},
			"region":              schema.StringAttribute{Computed: true, Description: "Where the project's serverless services run."},
			"deletion_protection": schema.BoolAttribute{Computed: true, Description: "Whether the project is protected from deletion."},
			"deploys_frozen":      schema.BoolAttribute{Computed: true, Description: "Whether new deployments are refused until the freeze is lifted."},
			"paused":              schema.BoolAttribute{Computed: true, Description: "Whether the project is paused (its services stopped)."},
			"instance_count":      schema.Int64Attribute{Computed: true, Description: "How many instances are in it."},
			"service_count":       schema.Int64Attribute{Computed: true, Description: "How many serverless services it has."},
			"database_count":      schema.Int64Attribute{Computed: true, Description: "How many databases it has: made for it, or bound to its services."},
			"bucket_count":        schema.Int64Attribute{Computed: true, Description: "How many buckets it has: put in it, or bound to its services."},
			"created_at":          schema.StringAttribute{Computed: true, Description: "When the project was created (RFC 3339)."},
		},
	}
}

func (d *projectDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *projectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg projectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The list says how much each project groups; one project read alone
	// does not.
	all, err := d.client.ListProjects(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the projects", err.Error())
		return
	}
	var p *client.Project
	if known(cfg.ID) {
		for i := range all {
			if all[i].ID == cfg.ID.ValueString() {
				p = &all[i]
			}
		}
		if p == nil {
			got, err := d.client.GetProject(ctx, cfg.ID.ValueString())
			if err != nil {
				resp.Diagnostics.AddError("Could not read the project", err.Error())
				return
			}
			p = got
		}
	} else {
		want := normalizeProjectName(cfg.Name.ValueString())
		for i := range all {
			if all[i].Name == want && !all[i].Deleting {
				p = &all[i]
			}
		}
		if p == nil {
			resp.Diagnostics.AddError("No such project", fmt.Sprintf("The account has no project named %q.", want))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, projectDataModel{
		ID:                 types.StringValue(p.ID),
		Name:               types.StringValue(p.Name),
		Description:        stringOrNull(p.Description),
		Region:             types.StringValue(p.Region),
		DeletionProtection: types.BoolValue(p.DeletionProtection),
		DeploysFrozen:      types.BoolValue(p.DeploysFrozen),
		Paused:             types.BoolValue(p.Paused),
		InstanceCount:      int64OrNull(p.InstanceCount),
		ServiceCount:       int64OrNull(p.ServiceCount),
		DatabaseCount:      int64OrNull(p.DatabaseCount),
		BucketCount:        int64OrNull(p.BucketCount),
		CreatedAt:          types.StringValue(p.CreatedAt),
	})...)
}
