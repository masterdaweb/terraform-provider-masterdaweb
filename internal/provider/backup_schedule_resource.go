package provider

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                = (*backupScheduleResource)(nil)
	_ resource.ResourceWithConfigure   = (*backupScheduleResource)(nil)
	_ resource.ResourceWithImportState = (*backupScheduleResource)(nil)
)

// NewBackupScheduleResource is masterdaweb_backup_schedule.
func NewBackupScheduleResource() resource.Resource { return &backupScheduleResource{} }

type backupScheduleResource struct{ client *client.Client }

type backupScheduleModel struct {
	ID         types.String `tfsdk:"id"`
	InstanceID types.String `tfsdk:"instance_id"`
	Days       types.Set    `tfsdk:"days"`
	Time       types.String `tfsdk:"time"`
	Timezone   types.String `tfsdk:"timezone"`
	LastRunAt  types.String `tfsdk:"last_run_at"`
}

// The days, times and zones the API takes (SetBackupScheduleV1Dto).
var (
	backupDays       = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	backupTimeRe     = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	backupTimezoneRe = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+-]+)*$`)
)

func (r *backupScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup_schedule"
}

func (r *backupScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "When an instance is backed up, on the days and at the time given. The instance needs the backup " +
			"add-on (its addons, like backup-slot), whose slots keep the backups. An instance has one schedule; " +
			"destroying it stops the scheduled backups and keeps the ones made.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The instance's id: an instance has one schedule."),
			"instance_id": schema.StringAttribute{
				Required: true, Description: "The instance. Changing it moves the schedule to another instance.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"days": schema.SetAttribute{
				Required: true, ElementType: types.StringType,
				Description: "The days it is backed up: sun, mon, tue, wed, thu, fri or sat.",
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.OneOf(backupDays...)),
				},
			},
			"time": schema.StringAttribute{
				Required: true, Description: "The time of day it is backed up, HH:MM (24h), in timezone.",
				Validators: []validator.String{stringvalidator.RegexMatches(backupTimeRe, "must be HH:MM (24h), like 03:30")},
			},
			"timezone": schema.StringAttribute{
				Optional: true, Description: "The time zone of time, an IANA name like America/Sao_Paulo; UTC when left out.",
				Validators: []validator.String{stringvalidator.RegexMatches(backupTimezoneRe, "must be an IANA time zone, like America/Sao_Paulo")},
			},
			"last_run_at": schema.StringAttribute{Computed: true, Description: "When it last made a backup (RFC 3339)."},
		},
	}
}

func (r *backupScheduleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func scheduleFromAPI(prior backupScheduleModel, s *client.BackupSchedule) backupScheduleModel {
	m := prior
	m.ID = types.StringValue(s.InstanceID)
	m.InstanceID = types.StringValue(s.InstanceID)
	days := append([]string{}, s.Days...)
	sort.Strings(days)
	m.Days = stringSet(days)
	m.Time = types.StringValue(s.Time)
	m.Timezone = keepEquivalent(prior.Timezone, s.Timezone)
	m.LastRunAt = stringOrNull(s.LastRunAt)
	return m
}

func (r *backupScheduleResource) set(ctx context.Context, plan backupScheduleModel, state interface {
	Set(context.Context, any) diagnostics
}, diags *diagnostics) {
	days := listStrings(ctx, plan.Days)
	sort.Slice(days, func(i, j int) bool { return dayIndex(days[i]) < dayIndex(days[j]) })
	body := map[string]any{"days": days, "time": plan.Time.ValueString()}
	if known(plan.Timezone) && strings.TrimSpace(plan.Timezone.ValueString()) != "" {
		body["timezone"] = strings.TrimSpace(plan.Timezone.ValueString())
	}
	s, err := r.client.SetBackupSchedule(ctx, plan.InstanceID.ValueString(), body)
	if err != nil {
		diags.AddError("Could not set the backup schedule", err.Error())
		return
	}
	diags.Append(state.Set(ctx, scheduleFromAPI(plan, s))...)
}

func dayIndex(d string) int {
	for i, day := range backupDays {
		if day == d {
			return i
		}
	}
	return len(backupDays)
}

func (r *backupScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan backupScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.set(ctx, plan, &resp.State, &resp.Diagnostics)
}

func (r *backupScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state backupScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.client.GetBackupSchedule(ctx, state.InstanceID.ValueString())
	if client.IsNotFound(err) {
		// No schedule any more, or no instance.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the backup schedule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, scheduleFromAPI(state, s))...)
}

func (r *backupScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan backupScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.set(ctx, plan, &resp.State, &resp.Diagnostics)
}

func (r *backupScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state backupScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteBackupSchedule(ctx, state.InstanceID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the backup schedule", err.Error())
	}
}

func (r *backupScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), id)...)
}
