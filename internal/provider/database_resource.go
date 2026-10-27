package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                   = (*databaseResource)(nil)
	_ resource.ResourceWithConfigure      = (*databaseResource)(nil)
	_ resource.ResourceWithImportState    = (*databaseResource)(nil)
	_ resource.ResourceWithValidateConfig = (*databaseResource)(nil)
)

// NewDatabaseResource is masterdaweb_database.
func NewDatabaseResource() resource.Resource { return &databaseResource{} }

type databaseResource struct{ client *client.Client }

type databaseModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Engine             types.String `tfsdk:"engine"`
	Version            types.String `tfsdk:"version"`
	Size               types.String `tfsdk:"size"`
	DiskGB             types.Int64  `tfsdk:"disk_gb"`
	DiskAutogrowMaxGB  types.Int64  `tfsdk:"disk_autogrow_max_gb"`
	ProjectID          types.String `tfsdk:"project_id"`
	AllowedCIDRs       types.Set    `tfsdk:"allowed_cidrs"`
	Extensions         types.Set    `tfsdk:"extensions"`
	EvictionPolicy     types.String `tfsdk:"eviction_policy"`
	Backups            types.Bool   `tfsdk:"backups"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
	MaintenanceDay     types.String `tfsdk:"maintenance_day"`
	MaintenanceHourUTC types.Int64  `tfsdk:"maintenance_hour_utc"`
	Status             types.String `tfsdk:"status"`
	Host               types.String `tfsdk:"host"`
	Port               types.Int64  `tfsdk:"port"`
	PoolerPort         types.Int64  `tfsdk:"pooler_port"`
	DatabaseName       types.String `tfsdk:"database_name"`
	Username           types.String `tfsdk:"username"`
	URI                types.String `tfsdk:"uri"`
	DirectURI          types.String `tfsdk:"direct_uri"`
	Password           types.String `tfsdk:"password"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

var databaseNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)

var weekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func (r *databaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (r *databaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A managed PostgreSQL, MySQL, MariaDB or Redis (Valkey) database, billed by the hour. It is ready " +
			"a few minutes after it is created (status running); Terraform does not wait, and a bound service's " +
			"deployment waits for it. Protected from deletion unless deletion_protection is false.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The database's id."),
			"name": schema.StringAttribute{
				Required: true,
				Description: "The database's name: 3 to 63 lowercase letters, digits and single hyphens, starting with a " +
					"letter, unique in the account. Changing it replaces the database.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(databaseNameRe, "must be 3 to 63 lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit"),
					stringvalidator.RegexMatches(regexp.MustCompile(`^(?:[^-]|-[^-])*-?$`), "must not have two hyphens in a row"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"engine": schema.StringAttribute{
				Required:      true,
				Description:   "postgres, mysql, mariadb or valkey (Redis). Changing it replaces the database.",
				Validators:    []validator.String{stringvalidator.OneOf("postgres", "mysql", "mariadb", "valkey")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"version": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The release series, like \"18\" (PostgreSQL), \"8.4\" (MySQL) or \"11.8\" (MariaDB). The " +
					"newest on offer when left out. Changing it replaces the database.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"size": schema.StringAttribute{
				Required: true,
				Description: "The size, like pg-1-1, my-1-1 or kv-1-1 (`mw db sizes` lists them). Changing it resizes " +
					"the database with a short restart, once it is running.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 32)},
			},
			"disk_gb": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "The disk, in GB. It starts at the size's own and only grows: set it once the database " +
					"runs to grow it (up to 1000).",
				Validators:    []validator.Int64{int64validator.Between(1, 1000)},
				PlanModifiers: []planmodifier.Int64{useStateIfUnchangedInt64(path.Root("size")), diskOnlyGrows()},
			},
			"disk_autogrow_max_gb": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description:   "Grow the disk by itself past 85% used, up to this many GB. 0: off.",
				Validators:    []validator.Int64{int64validator.Between(0, 1000)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				Optional:    true,
				Description: "The serverless project it belongs to (where the console shows it).",
			},
			"allowed_cidrs": schema.SetAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Default:     setdefault.StaticValue(types.SetValueMust(types.StringType, nil)),
				Description: "Addresses or networks allowed to connect besides your services, like 203.0.113.7 or 198.51.100.0/24.",
				Validators:  []validator.Set{setvalidator.SizeAtMost(50)},
			},
			"extensions": schema.SetAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Default: setdefault.StaticValue(types.SetValueMust(types.StringType, nil)),
				Description: "PostgreSQL: the extensions to enable, like vector or pg_trgm. They are only ever added: " +
					"removing one is done on the database itself (DROP EXTENSION).",
				Validators:    []validator.Set{setvalidator.SizeAtMost(30)},
				PlanModifiers: []planmodifier.Set{extensionsOnlyAdded()},
			},
			"eviction_policy": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Redis: what happens when memory is full. noeviction (the default, for queues) or an LRU/LFU " +
					"policy like allkeys-lru (for a cache).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"backups": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description: "PostgreSQL, MySQL, MariaDB: continuous backups, for restoring to any point in time. On " +
					"by default.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the database is protected from deletion. On by default: to destroy it, set it to " +
					"false and apply first.",
			},
			"maintenance_day": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "The day of the weekly maintenance window (sunday … saturday), or daily.",
				Validators:    []validator.String{stringvalidator.OneOf(append([]string{"daily"}, weekdays...)...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"maintenance_hour_utc": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description:   "The hour of the maintenance window, UTC (0 to 23).",
				Validators:    []validator.Int64{int64validator.Between(0, 23)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "provisioning, running, updating, failed, hibernating or hibernated, when last read.",
			},
			"host": computedString("Where the database answers."),
			"port": schema.Int64Attribute{
				Computed: true, Description: "PostgreSQL: the direct port; MySQL, MariaDB, Redis: their port.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"pooler_port": schema.Int64Attribute{
				Computed: true, Description: "PostgreSQL: the pooler's port (use it from apps).",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"database_name": computedString("The database (schema) to connect to; none for Redis."),
			"username":      computedString("The owner's user name."),
			"uri":           computedString("The address to connect to, without the password."),
			"direct_uri":    computedString("PostgreSQL: the direct address, for migrations, without the password."),
			"password": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The owner's password, as the API gave it when the database was created. Null after an " +
					"import, or for a developer's token. Services get logins of their own through bindings.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": computedString("When the database was created (RFC 3339)."),
		},
	}
}

func (r *databaseResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var engine, eviction types.String
	var backups types.Bool
	var extensions types.Set
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("engine"), &engine)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("eviction_policy"), &eviction)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("backups"), &backups)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("extensions"), &extensions)...)
	if resp.Diagnostics.HasError() || !known(engine) {
		return
	}
	e := engine.ValueString()
	if e != "postgres" && len(listStrings(ctx, extensions)) > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("extensions"), "Extensions are for PostgreSQL",
			"Leave extensions out for a "+e+" database.")
	}
	if e != "valkey" && !eviction.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("eviction_policy"), "An eviction policy is for Redis",
			"Leave eviction_policy out for a "+e+" database.")
	}
	if e == "valkey" && known(backups) && backups.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("backups"), "Backups are for SQL databases",
			"Redis keeps its data on disk: leave backups out.")
	}
}

func (r *databaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func databaseFromAPI(prior databaseModel, d *client.Database) databaseModel {
	m := databaseModel{
		ID:                 types.StringValue(d.ID),
		Name:               types.StringValue(d.Name),
		Engine:             types.StringValue(d.Engine),
		Version:            types.StringValue(string(d.Version)),
		Size:               types.StringValue(d.Size.Code),
		DiskGB:             types.Int64Value(d.Disk.SizeGB),
		DiskAutogrowMaxGB:  types.Int64Value(0),
		ProjectID:          stringOrNull(d.ProjectID),
		AllowedCIDRs:       stringSet(d.AllowedCIDRs),
		Extensions:         stringSet(d.Extensions),
		EvictionPolicy:     stringOrNull(d.EvictionPolicy),
		Backups:            types.BoolValue(d.BackupsEnabled),
		DeletionProtection: types.BoolValue(d.DeletionProtection),
		MaintenanceDay:     types.StringValue("daily"),
		MaintenanceHourUTC: types.Int64Value(d.Maintenance.HourUTC),
		Status:             types.StringValue(d.Status),
		Host:               types.StringValue(d.Connection.Host),
		Port:               types.Int64Value(d.Connection.Port),
		PoolerPort:         int64OrNull(d.Connection.PoolerPort),
		DatabaseName:       stringOrNull(d.Connection.Database),
		Username:           types.StringValue(d.Connection.Username),
		URI:                types.StringValue(d.Connection.URI),
		DirectURI:          stringOrNull(d.Connection.DirectURI),
		Password:           prior.Password,
		CreatedAt:          types.StringValue(d.CreatedAt),
	}
	if d.Disk.AutogrowMaxGB != nil {
		m.DiskAutogrowMaxGB = types.Int64Value(*d.Disk.AutogrowMaxGB)
	}
	if day := d.Maintenance.Day; day != nil && *day >= 0 && int(*day) < len(weekdays) {
		m.MaintenanceDay = types.StringValue(weekdays[*day])
	}
	if !known(m.Password) {
		m.Password = types.StringNull()
	}
	// The API keeps allowed sources normalised (an address as a /32): the
	// configuration's spelling stays while it means the same.
	if prior := listStrings(context.Background(), prior.AllowedCIDRs); prior != nil &&
		sameStringSets(prior, d.AllowedCIDRs, normalizeCIDR) {
		m.AllowedCIDRs = stringSet(prior)
	}
	return m
}

// maintenancePatch is what a planned database changes in the settings
// the API takes only after creation (PATCH).
func maintenancePatch(plan, state databaseModel, creating bool) map[string]any {
	patch := map[string]any{}
	if known(plan.MaintenanceDay) && (creating || !plan.MaintenanceDay.Equal(state.MaintenanceDay)) {
		day := plan.MaintenanceDay.ValueString()
		patch["maintenance_day"] = nil // daily
		for i, w := range weekdays {
			if w == day {
				patch["maintenance_day"] = i
			}
		}
	}
	if known(plan.MaintenanceHourUTC) && (creating || !plan.MaintenanceHourUTC.Equal(state.MaintenanceHourUTC)) {
		patch["maintenance_hour_utc"] = plan.MaintenanceHourUTC.ValueInt64()
	}
	if known(plan.DiskAutogrowMaxGB) && (creating || !plan.DiskAutogrowMaxGB.Equal(state.DiskAutogrowMaxGB)) {
		if n := plan.DiskAutogrowMaxGB.ValueInt64(); n > 0 {
			patch["disk_autogrow_max_gb"] = n
		} else if !creating {
			patch["disk_autogrow_max_gb"] = nil // off
		}
	}
	return patch
}

// sortedStrings is a set's strings in order, so a request is always the same.
func sortedStrings(ctx context.Context, v types.Set) []string {
	out := listStrings(ctx, v)
	if out == nil {
		out = []string{}
	}
	sort.Strings(out)
	return out
}

func (r *databaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	engine := plan.Engine.ValueString()
	// A database starts with its size's disk: a bigger one is grown once it runs.
	if known(plan.DiskGB) {
		info, err := r.client.GetDatabasesInfo(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not read the sizes on offer", err.Error())
			return
		}
		for _, e := range info.Engines {
			for _, s := range e.Sizes {
				if e.Engine == engine && s.Code == plan.Size.ValueString() && s.DiskGB != plan.DiskGB.ValueInt64() {
					resp.Diagnostics.AddAttributeError(path.Root("disk_gb"), "The disk starts at the size's own",
						fmt.Sprintf("A %s database starts with %d GB. Leave disk_gb out to create it, and set it once "+
							"the database is running to grow the disk.", s.Code, s.DiskGB))
					return
				}
			}
		}
	}
	body := map[string]any{
		"name":                plan.Name.ValueString(),
		"engine":              engine,
		"size":                plan.Size.ValueString(),
		"deletion_protection": plan.DeletionProtection.ValueBool(),
	}
	if known(plan.Version) {
		body["version"] = plan.Version.ValueString()
	}
	if known(plan.ProjectID) {
		body["project_id"] = plan.ProjectID.ValueString()
	}
	if cidrs := sortedStrings(ctx, plan.AllowedCIDRs); len(cidrs) > 0 {
		body["allowed_cidrs"] = cidrs
	}
	if ext := sortedStrings(ctx, plan.Extensions); len(ext) > 0 {
		body["extensions"] = ext
	}
	if known(plan.EvictionPolicy) {
		body["eviction_policy"] = plan.EvictionPolicy.ValueString()
	}
	if known(plan.Backups) {
		body["backups"] = plan.Backups.ValueBool()
	}
	made, err := r.client.CreateDatabase(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not create the database", err.Error())
		return
	}
	d := &made.Database
	plan.Password = stringOrNull(made.Password)
	if patch := maintenancePatch(plan, databaseModel{}, true); len(patch) > 0 {
		updated, err := r.client.UpdateDatabase(ctx, d.Name, patch)
		if err != nil {
			resp.Diagnostics.AddWarning("The maintenance settings were not saved",
				"The database was created, but its maintenance window or disk growth could not be set: "+err.Error()+
					" The next apply tries again.")
		} else {
			d = updated
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, databaseFromAPI(plan, d))...)
}

func (r *databaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.GetDatabase(ctx, state.Name.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the database", err.Error())
		return
	}
	// The name now belongs to another database: this one is gone.
	if d.DeletedAt != nil || (known(state.ID) && state.ID.ValueString() != d.ID) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, databaseFromAPI(state, d))...)
}

func (r *databaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state databaseModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()
	changed := false
	fail := func(summary string, err error) {
		if changed {
			if d, gerr := r.client.GetDatabase(ctx, name); gerr == nil {
				resp.Diagnostics.Append(resp.State.Set(ctx, databaseFromAPI(plan, d))...)
			}
		}
		resp.Diagnostics.AddError(summary, err.Error())
	}

	patch := maintenancePatch(plan, state, false)
	if !plan.AllowedCIDRs.Equal(state.AllowedCIDRs) {
		patch["allowed_cidrs"] = sortedStrings(ctx, plan.AllowedCIDRs)
	}
	if !plan.Extensions.Equal(state.Extensions) && plan.Engine.ValueString() == "postgres" {
		patch["extensions"] = sortedStrings(ctx, plan.Extensions)
	}
	if known(plan.EvictionPolicy) && !plan.EvictionPolicy.Equal(state.EvictionPolicy) {
		patch["eviction_policy"] = plan.EvictionPolicy.ValueString()
	}
	if known(plan.Backups) && !plan.Backups.Equal(state.Backups) {
		patch["backups"] = plan.Backups.ValueBool()
	}
	if !plan.DeletionProtection.Equal(state.DeletionProtection) {
		patch["deletion_protection"] = plan.DeletionProtection.ValueBool()
	}
	if !plan.ProjectID.Equal(state.ProjectID) {
		if known(plan.ProjectID) {
			patch["project_id"] = plan.ProjectID.ValueString()
		} else {
			patch["project_id"] = nil
		}
	}
	if len(patch) > 0 {
		if _, err := r.client.UpdateDatabase(ctx, name, patch); err != nil {
			fail("Could not change the database", err)
			return
		}
		changed = true
	}

	resize := map[string]any{}
	if !plan.Size.Equal(state.Size) {
		resize["size"] = plan.Size.ValueString()
	}
	if known(plan.DiskGB) && !plan.DiskGB.Equal(state.DiskGB) {
		resize["disk_gb"] = plan.DiskGB.ValueInt64()
	}
	if len(resize) > 0 {
		resize["when"] = "now"
		if _, err := r.client.ResizeDatabase(ctx, name, resize); err != nil {
			fail("Could not resize the database", err)
			return
		}
		changed = true
	}

	d, err := r.client.GetDatabase(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the database", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, databaseFromAPI(plan, d))...)
}

func (r *databaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()
	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Deletion protection is on",
			fmt.Sprintf("The database %s is protected from deletion. Set deletion_protection = false, run terraform apply, "+
				"and then destroy it. Its backups are kept for a few days after.", name))
		return
	}
	if err := r.client.DeleteDatabase(ctx, name); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the database", err.Error())
	}
}

// ImportState takes the database's name (how the API addresses it) or its id.
func (r *databaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := strings.TrimSpace(req.ID)
	name := ref
	if isUUID(ref) {
		all, err := r.client.ListDatabases(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the databases", err.Error())
			return
		}
		name = ""
		for _, d := range all {
			if d.ID == ref && d.DeletedAt == nil {
				name = d.Name
			}
		}
		if name == "" {
			resp.Diagnostics.AddError("No such database", fmt.Sprintf("No database has the id %s.", ref))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}
