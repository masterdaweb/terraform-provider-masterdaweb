package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                     = (*instanceResource)(nil)
	_ resource.ResourceWithConfigure        = (*instanceResource)(nil)
	_ resource.ResourceWithImportState      = (*instanceResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*instanceResource)(nil)
	_ resource.ResourceWithConfigValidators = (*instanceResource)(nil)
	_ resource.ResourceWithValidateConfig   = (*instanceResource)(nil)
)

// NewInstanceResource is masterdaweb_instance.
func NewInstanceResource() resource.Resource { return &instanceResource{} }

type instanceResource struct{ client *client.Client }

type instanceModel struct {
	ID                 types.String   `tfsdk:"id"`
	Name               types.String   `tfsdk:"name"`
	Description        types.String   `tfsdk:"description"`
	RegionID           types.String   `tfsdk:"region_id"`
	Plan               types.String   `tfsdk:"plan"`
	Image              types.String   `tfsdk:"image"`
	CustomImageID      types.String   `tfsdk:"custom_image_id"`
	Term               types.String   `tfsdk:"term"`
	SubscriptionID     types.String   `tfsdk:"subscription_id"`
	Payment            types.String   `tfsdk:"payment"`
	InitialCreditCents types.String   `tfsdk:"initial_credit_cents"`
	Password           types.String   `tfsdk:"password"`
	SSHKeyIDs          types.Set      `tfsdk:"ssh_key_ids"`
	UserData           types.String   `tfsdk:"user_data"`
	AppInputs          types.Map      `tfsdk:"app_inputs"`
	ProjectID          types.String   `tfsdk:"project_id"`
	MainDiskExtraGB    types.Int64    `tfsdk:"main_disk_extra_gb"`
	ExtraDisksGB       types.List     `tfsdk:"extra_disks_gb"`
	Addons             types.Set      `tfsdk:"addons"`
	ExtraIPv4          types.Int64    `tfsdk:"extra_ipv4"`
	ExtraIPv6          types.Int64    `tfsdk:"extra_ipv6"`
	NetworkMode        types.String   `tfsdk:"network_mode"`
	VPCID              types.String   `tfsdk:"vpc_id"`
	PowerState         types.String   `tfsdk:"power_state"`
	Status             types.String   `tfsdk:"status"`
	IPv4Address        types.String   `tfsdk:"ipv4_address"`
	IPv6Address        types.String   `tfsdk:"ipv6_address"`
	IPAddresses        types.List     `tfsdk:"ip_addresses"`
	Hostname           types.String   `tfsdk:"hostname"`
	VCPUs              types.Int64    `tfsdk:"vcpus"`
	RAMMB              types.Int64    `tfsdk:"ram_mb"`
	DiskGB             types.Int64    `tfsdk:"disk_gb"`
	RegionName         types.String   `tfsdk:"region_name"`
	OrderID            types.String   `tfsdk:"order_id"`
	CreatedAt          types.String   `tfsdk:"created_at"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

var instanceIPType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"address": types.StringType,
	"version": types.StringType,
	"primary": types.BoolType,
}}

var instanceAddonType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"code":     types.StringType,
	"quantity": types.Int64Type,
}}

// The passwords the API takes (src/vm/dto/vm-password.ts): letters, digits
// and the symbols of a US keyboard, no spaces or accents.
var instancePasswordRe = regexp.MustCompile(`^[!-~]{8,200}$`)

const (
	defaultInstanceCreateTimeout = 60 * time.Minute
	defaultInstanceUpdateTimeout = 30 * time.Minute
	defaultInstanceDeleteTimeout = 20 * time.Minute
)

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	createOnly := "Used when the instance is created; changing it later changes nothing."
	reinstalls := "Changing it reinstalls the instance in place: everything on its disk is replaced (its addresses, " +
		"plan and billing stay)."
	resizes := "Changing it resizes the instance in place, quoted first: the plan says what it costs and removes."
	resp.Schema = schema.Schema{
		Description: "A cloud instance (VPS), ordered through the API like in the checkout: from a subscription's " +
			"balance (subscription_id with payment = \"balance\" is the unattended way), or with an invoice Terraform " +
			"waits for. Terraform waits until the instance is built and running. Changing its image, SSH keys, user " +
			"data or app inputs reinstalls it in place (its disk is replaced; its addresses, plan and billing stay), " +
			"and the plan warns about it.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The instance's id."),
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "The instance's display name, up to 120 characters. Changed in place.",
				Validators:    []validator.String{stringvalidator.LengthAtMost(120)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "A note on the instance, up to 500 characters. Changed in place.",
				Validators:  []validator.String{stringvalidator.LengthAtMost(500)},
			},
			"region_id": schema.StringAttribute{
				Required:      true,
				Description:   "The region, by id (the masterdaweb_regions data source). Changing it replaces the instance.",
				Validators:    []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[0-9]+$`), "must be a region id, like \"3\"")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"plan": schema.StringAttribute{
				Required: true,
				Description: "The plan, by its code (the masterdaweb_plans data source). " + resizes + " A CPU or memory " +
					"change powers it off and on again.",
			},
			"image": schema.StringAttribute{
				Optional: true,
				Description: "The operating system or app to install, by its code (the masterdaweb_images data source). " +
					"Give image or custom_image_id. " + reinstalls,
			},
			"custom_image_id": schema.StringAttribute{
				Optional: true,
				Description: "One of your own uploaded disk images, by id, instead of image. It brings its own " +
					"cloud-init: ssh_key_ids, user_data and app_inputs do not go with it. " + reinstalls,
			},
			"term": schema.StringAttribute{
				Optional: true, Computed: true,
				Default: stringdefault.StaticString("MONTHLY"),
				Description: "The billing term: MONTHLY, QUARTERLY, SEMI_ANNUAL, ANNUAL or ON_DEMAND (hourly, from prepaid " +
					"credit). An existing subscription's term wins. It cannot change after the instance is created.",
				Validators: []validator.String{stringvalidator.OneOf("MONTHLY", "QUARTERLY", "SEMI_ANNUAL", "ANNUAL", "ON_DEMAND")},
			},
			"subscription_id": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "An existing subscription to add the instance to. Without one, the order starts a new " +
					"subscription, which is always paid by invoice. It cannot change after the instance is created.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"payment": schema.StringAttribute{
				Optional: true, Computed: true,
				Default: stringdefault.StaticString("auto"),
				Description: "How the order is paid: auto (the subscription's balance when it covers it, else an invoice), " +
					"balance (never an invoice: refused when the balance is short; needs subscription_id) or invoice. " +
					createOnly,
				Validators: []validator.String{stringvalidator.OneOf("auto", "balance", "invoice")},
			},
			"initial_credit_cents": schema.StringAttribute{
				Optional: true,
				Description: "A new ON_DEMAND subscription's first prepaid credit, in cents (at least the minimum " +
					"recharge, its default). " + createOnly,
				Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[1-9][0-9]{0,13}$`), "must be a whole number of cents, like \"5000\"")},
			},
			"password": schema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "root's (Administrator's) password, for images that take one: 8 to 200 letters, digits and " +
					"US-keyboard symbols. Changing it resets the password on the running instance (through its guest " +
					"agent); a reinstall builds the instance with it.",
				Validators: []validator.String{stringvalidator.RegexMatches(instancePasswordRe, "must be 8 to 200 letters, digits and US-keyboard symbols (no spaces or accents)")},
			},
			"ssh_key_ids": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Up to 20 SSH keys (masterdaweb_ssh_key) root accepts, put on the instance when it is " +
					"built, on Linux catalog images. " + reinstalls,
				Validators: []validator.Set{setvalidator.SizeAtMost(20)},
			},
			"user_data": schema.StringAttribute{
				Optional: true,
				Description: "User data for the first boot, on Linux catalog images: a #cloud-config document or a #! " +
					"script, up to 16 KiB, merged into the image's own cloud-init (the root password, the hostname and " +
					"disk growth are the platform's, and refused). Never stored in the clear. " + reinstalls,
				Validators: []validator.String{stringvalidator.LengthAtMost(16384)},
			},
			"app_inputs": schema.MapAttribute{
				Optional: true, Sensitive: true, ElementType: types.StringType,
				Description: "Answers to the app's setup questions, by key (the image's inputs in the masterdaweb_images " +
					"data source). " + reinstalls,
			},
			"project_id": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The project the instance is in (masterdaweb_project): it joins it when it is created, and " +
					"changing it moves it; \"\" takes it out of its project. The instance itself is not touched. Left " +
					"out, it is the project the instance is in (managed elsewhere, like with masterdaweb_project_resource).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"main_disk_extra_gb": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "GB on the main disk beyond the plan's. Raising it grows the disk in place (then grow the " +
					"partition inside the guest); the main disk never shrinks, and a smaller value is refused at plan " +
					"time. Left out, it is what the instance has.",
				Validators:    []validator.Int64{int64validator.AtLeast(0)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"extra_disks_gb": schema.ListAttribute{
				Optional: true, Computed: true, ElementType: types.Int64Type,
				Description: "Extra data disks, by size in GB, in order. Disks added at the end of the list are added in " +
					"place; disks taken off the end are removed with ALL their data; a disk's size never changes. Left " +
					"out, they are what the instance has (a disk added elsewhere shows as 0, its size unknown); [] " +
					"removes them all.",
				Validators:    []validator.List{listvalidator.ValueInt64sAre(int64validator.AtLeast(1))},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"addons": schema.SetNestedAttribute{
				Optional: true,
				Description: "The add-ons Terraform manages (the catalog's, like backup slots), changed in place: one " +
					"taken out of the list is removed where it may be. Add-ons the list never had are left as they are.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"code":     schema.StringAttribute{Required: true, Description: "The add-on's code."},
					"quantity": schema.Int64Attribute{Optional: true, Description: "How many; the add-on's minimum when left out.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
				}},
			},
			"extra_ipv4": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "Public IPv4 addresses beyond the plan's. Raising it adds addresses in place; lowering it " +
					"releases the newest ones that are not the primary (gone for good). Left out, it is what the instance has.",
				Validators:    []validator.Int64{int64validator.AtLeast(0)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"extra_ipv6": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description:   "Public IPv6 addresses beyond the plan's, as extra_ipv4.",
				Validators:    []validator.Int64{int64validator.AtLeast(0)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"network_mode": schema.StringAttribute{
				Optional: true, Computed: true,
				Default: stringdefault.StaticString("public"),
				Description: "public (public addresses) or private (no public address: on vpc_id only). Changing it " +
					"replaces the instance.",
				Validators:    []validator.String{stringvalidator.OneOf("public", "private")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"vpc_id": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The VPC the instance is on. Set it to create the instance on that VPC (required with " +
					"network_mode = \"private\"); left out, it shows a VPC the instance was put on afterwards " +
					"(masterdaweb_vpc_attachment). Changing a set value replaces the instance.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()},
			},
			"power_state": schema.StringAttribute{
				Optional: true, Computed: true,
				Default:     stringdefault.StaticString("running"),
				Description: "running or stopped. Changing it starts or stops the instance.",
				Validators:  []validator.String{stringvalidator.OneOf("running", "stopped")},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "The live power state (running, stopped…), or suspended while suspended for non-payment.",
			},
			"ipv4_address": schema.StringAttribute{
				Computed: true, Description: "The primary public IPv4 address, when it has one.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ipv6_address": schema.StringAttribute{
				Computed: true, Description: "The primary IPv6 address, when it has one.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ip_addresses": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every address of the instance.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"address": schema.StringAttribute{Computed: true, Description: "The address."},
					"version": schema.StringAttribute{Computed: true, Description: "ipv4 or ipv6."},
					"primary": schema.BoolAttribute{Computed: true, Description: "Whether it is the primary address."},
				}},
			},
			"hostname":    schema.StringAttribute{Computed: true, Description: "The instance's hostname.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"vcpus":       schema.Int64Attribute{Computed: true, Description: "Its vCPUs.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"ram_mb":      schema.Int64Attribute{Computed: true, Description: "Its memory, in MB.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"disk_gb":     schema.Int64Attribute{Computed: true, Description: "Its main disk, in GB.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"region_name": schema.StringAttribute{Computed: true, Description: "The region's name.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"order_id":    computedString("The order the instance was bought with (null for an imported one)."),
			"created_at":  computedString("When the instance was created (RFC 3339)."),
			"timeouts": timeoutsAttribute(ctx, timeouts.Opts{
				Create: true, Update: true, Delete: true,
				CreateDescription: "How long to wait for the order to be paid and the instance built and running (default 60m).",
				UpdateDescription: "How long to wait for a resize, a reinstall or a power change (default 30m).",
				DeleteDescription: "How long to wait for the instance to be gone (default 20m).",
			}),
		},
	}
}

func (r *instanceResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	// A custom image brings its own cloud-init: the API takes none of these
	// with it.
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("image"), path.MatchRoot("custom_image_id")),
		resourcevalidator.Conflicting(path.MatchRoot("custom_image_id"), path.MatchRoot("ssh_key_ids")),
		resourcevalidator.Conflicting(path.MatchRoot("custom_image_id"), path.MatchRoot("user_data")),
		resourcevalidator.Conflicting(path.MatchRoot("custom_image_id"), path.MatchRoot("app_inputs")),
	}
}

func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// ValidateConfig checks that a private instance names its VPC.
func (r *instanceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var mode, vpc types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("network_mode"), &mode)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("vpc_id"), &vpc)...)
	if mode.ValueString() == "private" && vpc.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("vpc_id"), "A private instance needs a VPC",
			"With network_mode = \"private\" the instance has no public address: set vpc_id to the VPC it goes on.")
	}
}

// ModifyPlan refuses at plan time what the API cannot change on an existing
// instance — instead of replacing a server, with its data, over a billing
// detail — quotes a resize, and warns before a change that reinstalls it.
func (r *instanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.State.Raw.IsNull() {
		return
	}
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	refuse := func(attr string, prior, planned attr.Value, why string) {
		if prior.IsNull() || prior.IsUnknown() || planned.IsUnknown() || prior.Equal(planned) {
			return
		}
		resp.Diagnostics.AddAttributeError(path.Root(attr), "Cannot change "+attr+" in place",
			fmt.Sprintf("The instance %s has %s = %s and the configuration says %s. %s", state.ID.ValueString(), attr,
				prior.String(), planned.String(), why))
	}
	if changed := reinstallChanges(state, plan); len(changed) > 0 {
		resp.Diagnostics.AddWarning("The instance will be reinstalled",
			fmt.Sprintf("Changing %s reinstalls the instance %s in place: everything on its disk is replaced by a "+
				"fresh install. Its addresses, plan and billing stay. Revert the change to keep the disk.",
				strings.Join(changed, ", "), state.ID.ValueString()))
	}
	refuse("term", state.Term, plan.Term, "The billing term belongs to the subscription: change it in the console.")
	refuse("subscription_id", state.SubscriptionID, plan.SubscriptionID, "An instance stays in the subscription it was ordered in.")

	// A resize: what cannot be is refused here, what can is quoted, and what
	// it changes is known after it.
	resizeRefusals(ctx, state, plan, &resp.Diagnostics)
	if changed := resizeChanges(ctx, state, plan); len(changed) > 0 && !resp.Diagnostics.HasError() && !state.ID.IsNull() {
		var mainDisk types.Int64
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("main_disk_extra_gb"), &mainDisk)...)
		r.quotePlan(ctx, state, plan, !mainDisk.IsNull(), changed, &resp.Diagnostics)
		for _, a := range []string{"vcpus", "ram_mb", "disk_gb"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(a), types.Int64Unknown())...)
		}
		if mainDisk.IsNull() && !plan.Plan.Equal(state.Plan) {
			// Left out, it is what keeps the disk's size on the new plan.
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("main_disk_extra_gb"), types.Int64Unknown())...)
		}
	}
}

// imported says whether the instance came into the state by an import: an
// instance Terraform ordered has its order on record.
func (m instanceModel) imported() bool { return m.OrderID.IsNull() }

// reinstallChanges are the attributes the plan changes that the instance is
// built with — changing any of them reinstalls it. On an imported instance,
// what the API cannot show (its SSH keys, user data, app inputs) is taken
// from the configuration as it is the first time it is given.
func reinstallChanges(state, plan instanceModel) []string {
	var changed []string
	for _, a := range []struct {
		name           string
		prior, planned attr.Value
	}{
		{"image", state.Image, plan.Image},
		{"custom_image_id", state.CustomImageID, plan.CustomImageID},
		{"ssh_key_ids", state.SSHKeyIDs, plan.SSHKeyIDs},
		{"user_data", state.UserData, plan.UserData},
		{"app_inputs", state.AppInputs, plan.AppInputs},
	} {
		if sameBuildInput(a.prior, a.planned) || (state.imported() && a.prior.IsNull()) {
			continue
		}
		changed = append(changed, a.name)
	}
	return changed
}

// sameBuildInput compares what an instance is built with, nothing (null) and
// empty ("", no keys) being the same.
func sameBuildInput(prior, planned attr.Value) bool {
	if planned.IsUnknown() {
		return false
	}
	empty := func(v attr.Value) bool {
		switch t := v.(type) {
		case types.String:
			return t.IsNull() || t.ValueString() == ""
		case types.Set:
			return t.IsNull() || len(t.Elements()) == 0
		case types.Map:
			return t.IsNull() || len(t.Elements()) == 0
		}
		return v.IsNull()
	}
	if empty(prior) && empty(planned) {
		return true
	}
	return prior.Equal(planned)
}

// ── Converting ──────────────────────────────────────────────────────────────

// instanceFromAPI is the state from the API's instance and live status,
// keeping what the API never shows again from prior.
func instanceFromAPI(ctx context.Context, prior instanceModel, i *client.Instance, s *client.InstanceStatus) (instanceModel, diag.Diagnostics) {
	m := prior
	var diags diag.Diagnostics
	m.ID = types.StringValue(i.ID)
	m.Name = keepEquivalent(prior.Name, i.Name)
	if m.Name.IsNull() {
		m.Name = types.StringValue("")
	}
	m.Description = keepEquivalent(prior.Description, i.Description)
	if i.Region != nil {
		m.RegionID = types.StringValue(i.Region.ID)
		m.RegionName = types.StringValue(i.Region.Name)
	} else if m.RegionName.IsUnknown() {
		m.RegionName = types.StringNull()
	}
	if i.OfferingCode != nil && *i.OfferingCode != "" {
		m.Plan = types.StringValue(*i.OfferingCode)
	}
	m.VCPUs, m.RAMMB, m.DiskGB = int64OrNull(i.Specs.VCPUs), int64OrNull(i.Specs.RAMMB), int64OrNull(i.Specs.DiskGB)
	m.Hostname = stringOrNull(i.Hostname)
	m.VPCID = stringOrNull(i.VPCID)
	m.CreatedAt = stringOrNull(i.CreatedAt)
	// What it was built from, its project and its subscription, as the API
	// says them (an API from before these fields leaves the state as it was).
	if i.Has("image") || i.Has("custom_image_id") {
		m.Image, m.CustomImageID = stringOrNull(i.Image), stringOrNull(i.CustomImageID)
	}
	if i.Has("project_id") && !(prior.ProjectID.ValueString() == "" && i.ProjectID == nil && known(prior.ProjectID)) {
		// "" in the configuration is no project, as null is from the API.
		m.ProjectID = stringOrNull(i.ProjectID)
	}
	if i.SubscriptionID != nil {
		m.SubscriptionID = types.StringValue(*i.SubscriptionID)
	}

	m.IPv4Address, m.IPv6Address = types.StringNull(), types.StringNull()
	ips := make([]attr.Value, 0, len(i.IPAddresses))
	for _, version := range []string{"ipv4", "ipv6"} {
		var first, primary string
		for _, a := range i.IPAddresses {
			if a.Version != version {
				continue
			}
			if first == "" {
				first = a.Address
			}
			if a.Primary && primary == "" {
				primary = a.Address
			}
		}
		if primary == "" {
			primary = first
		}
		if primary != "" {
			if version == "ipv4" {
				m.IPv4Address = types.StringValue(primary)
			} else {
				m.IPv6Address = types.StringValue(primary)
			}
		}
	}
	for _, a := range i.IPAddresses {
		obj, d := types.ObjectValue(instanceIPType.AttrTypes, map[string]attr.Value{
			"address": types.StringValue(a.Address),
			"version": types.StringValue(a.Version),
			"primary": types.BoolValue(a.Primary),
		})
		diags.Append(d...)
		ips = append(ips, obj)
	}
	list, d := types.ListValue(instanceIPType, ips)
	diags.Append(d...)
	m.IPAddresses = list

	power := i.PowerStateCached
	if s != nil && s.PowerState != "" {
		power = s.PowerState
	}
	switch {
	case i.Suspended:
		m.Status = types.StringValue("suspended")
	case power != "":
		m.Status = types.StringValue(power)
	default:
		m.Status = types.StringValue("unknown")
	}
	if power == "running" || power == "stopped" {
		m.PowerState = types.StringValue(power)
	} else if m.PowerState.IsNull() || m.PowerState.IsUnknown() {
		m.PowerState = types.StringValue("running")
	}
	if m.OrderID.IsUnknown() {
		m.OrderID = types.StringNull()
	}
	if m.SubscriptionID.IsUnknown() {
		m.SubscriptionID = types.StringNull()
	}
	if m.ProjectID.IsUnknown() {
		m.ProjectID = types.StringNull()
	}
	return m, diags
}

// orderBody is the order the plan describes.
func (r *instanceResource) orderBody(ctx context.Context, plan instanceModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	region, _ := strconv.ParseInt(plan.RegionID.ValueString(), 10, 64)
	body := map[string]any{
		"region_id":     region,
		"offering_code": plan.Plan.ValueString(),
		"term":          plan.Term.ValueString(),
	}
	if known(plan.CustomImageID) {
		body["image"] = map[string]any{"type": "client_image", "image_id": plan.CustomImageID.ValueString()}
	} else {
		body["image"] = map[string]any{"type": "template", "code": plan.Image.ValueString()}
	}
	if known(plan.Name) && plan.Name.ValueString() != "" {
		body["name"] = plan.Name.ValueString()
	}
	if known(plan.Password) {
		body["password"] = plan.Password.ValueString()
	}
	if known(plan.SubscriptionID) {
		body["subscription_id"] = plan.SubscriptionID.ValueString()
	}
	if known(plan.Payment) {
		body["payment"] = plan.Payment.ValueString()
	}
	if known(plan.InitialCreditCents) {
		body["initial_credit_cents"] = plan.InitialCreditCents.ValueString()
	}
	if n := plan.MainDiskExtraGB.ValueInt64(); n > 0 {
		body["main_disk_extra_gb"] = n
	}
	if known(plan.ExtraDisksGB) {
		var sizes []int64
		diags.Append(plan.ExtraDisksGB.ElementsAs(ctx, &sizes, false)...)
		disks := []map[string]any{}
		for _, s := range sizes {
			disks = append(disks, map[string]any{"size_gb": s})
		}
		if len(disks) > 0 {
			body["extra_disks"] = disks
		}
	}
	if known(plan.Addons) {
		var addons []struct {
			Code     types.String `tfsdk:"code"`
			Quantity types.Int64  `tfsdk:"quantity"`
		}
		diags.Append(plan.Addons.ElementsAs(ctx, &addons, false)...)
		out := []map[string]any{}
		for _, a := range addons {
			item := map[string]any{"code": a.Code.ValueString()}
			if known(a.Quantity) {
				item["quantity"] = a.Quantity.ValueInt64()
			}
			out = append(out, item)
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["code"].(string) < out[j]["code"].(string) })
		if len(out) > 0 {
			body["addons"] = out
		}
	}
	if known(plan.AppInputs) {
		inputs := map[string]string{}
		diags.Append(plan.AppInputs.ElementsAs(ctx, &inputs, false)...)
		if len(inputs) > 0 {
			body["app_inputs"] = inputs
		}
	}
	if plan.NetworkMode.ValueString() == "private" || known(plan.VPCID) {
		net := map[string]any{"mode": plan.NetworkMode.ValueString()}
		if known(plan.VPCID) {
			net["vpc_id"] = client.NumericID(plan.VPCID.ValueString())
		}
		body["networking"] = net
	}
	if keys := listStrings(ctx, plan.SSHKeyIDs); len(keys) > 0 {
		sort.Strings(keys)
		body["ssh_key_ids"] = keys
	}
	if known(plan.UserData) && plan.UserData.ValueString() != "" {
		body["user_data"] = plan.UserData.ValueString()
	}
	if known(plan.ProjectID) && plan.ProjectID.ValueString() != "" {
		body["project_id"] = plan.ProjectID.ValueString()
	}
	return body, diags
}

// ── Waits ───────────────────────────────────────────────────────────────────

// waitForOrder follows an order until it has made its instance. An order
// waiting for its invoice says where to pay it, once.
func (r *instanceResource) waitForOrder(ctx context.Context, order *client.Order, timeout time.Duration) (string, error) {
	if len(order.InstanceIDs) > 0 {
		return order.InstanceIDs[0], nil
	}
	if order.ID == nil || *order.ID == "" {
		return "", fmt.Errorf("the order was not placed (status %s)", order.Status)
	}
	orderID := *order.ID
	if order.Status == "pending_payment" {
		amount, link := "", ""
		if order.Payment != nil {
			amount, link = deref(order.Payment.AmountCents), deref(order.Payment.PayURL)
		}
		tflog.Warn(ctx, "The order raised an invoice: the instance is built once it is paid", map[string]any{
			"order_id": orderID, "amount_cents": amount, "pay_url": link,
		})
	}
	instanceID := ""
	last := order
	err := waitUntil(ctx, timeout, func() (bool, error) {
		o, err := r.client.GetOrder(ctx, orderID)
		if err != nil {
			return false, err
		}
		last = o
		switch o.Status {
		case "failed":
			return false, fmt.Errorf("the order %s failed after it was paid (support has been alerted and the money is not lost)", orderID)
		case "cancelled":
			return false, fmt.Errorf("the order %s was cancelled (it expired unpaid, or was cancelled)", orderID)
		}
		if len(o.InstanceIDs) > 0 {
			instanceID = o.InstanceIDs[0]
			return true, nil
		}
		return false, nil
	})
	if err != nil && last != nil && last.Status == "pending_payment" {
		link := ""
		if order.Payment != nil && order.Payment.PayURL != nil {
			link = " Pay it at " + *order.Payment.PayURL + "."
		}
		return "", fmt.Errorf("the order %s is still waiting for its invoice to be paid (%s).%s Once it is paid the "+
			"instance is built: import it then (terraform import), as applying again would order another. "+
			"For unattended orders, give subscription_id with payment = \"balance\"", orderID, err, link)
	}
	return instanceID, err
}

// waitForReady waits until the instance is built: not busy, and in the power
// state wanted.
func (r *instanceResource) waitForReady(ctx context.Context, id, power string, timeout time.Duration) error {
	return waitUntil(ctx, timeout, func() (bool, error) {
		s, err := r.client.GetInstanceStatus(ctx, id, true)
		if client.IsNotFound(err) {
			// Built a moment ago: the record may not be readable yet.
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if s.Busy || (s.DeploymentProgress != nil && *s.DeploymentProgress < 100) {
			return false, nil
		}
		return power == "" || s.PowerState == power, nil
	})
}

func (r *instanceResource) setPower(ctx context.Context, id, want string, timeout time.Duration) error {
	action := "start"
	if want == "stopped" {
		action = "stop"
	}
	err := whileBusy(ctx, timeout, func() error {
		_, err := r.client.PowerInstance(ctx, id, action)
		return err
	})
	if err != nil {
		return err
	}
	return r.waitForReady(ctx, id, want, timeout)
}

// reinstall rebuilds the instance from the plan's image, with its keys, user
// data and app inputs (POST /v1/instances/{id}/reinstall), and waits until
// it is built and running again.
func (r *instanceResource) reinstall(ctx context.Context, id string, plan instanceModel, timeout time.Duration) error {
	body := map[string]any{}
	if known(plan.CustomImageID) {
		// A custom image brings its own cloud-init: alone.
		body["custom_image_id"] = plan.CustomImageID.ValueString()
	} else {
		body["image"] = plan.Image.ValueString()
		if known(plan.Password) {
			body["password"] = plan.Password.ValueString()
		}
		// What the configuration says, all of it: no keys or no user data
		// removes what the instance had.
		keys := listStrings(ctx, plan.SSHKeyIDs)
		if keys == nil {
			keys = []string{}
		}
		sort.Strings(keys)
		body["ssh_key_ids"] = keys
		body["user_data"] = plan.UserData.ValueString()
		if known(plan.AppInputs) {
			inputs := map[string]string{}
			if d := plan.AppInputs.ElementsAs(ctx, &inputs, false); d.HasError() {
				return fmt.Errorf("could not read app_inputs")
			}
			if len(inputs) > 0 {
				body["app_inputs"] = inputs
			}
		}
	}
	var accepted *client.OperationAccepted
	err := whileBusy(ctx, timeout, func() error {
		a, err := r.client.ReinstallInstance(ctx, id, body)
		accepted = a
		return err
	})
	if err != nil {
		return err
	}
	if accepted.OperationID != nil && *accepted.OperationID != "" {
		if err := waitForOperation(ctx, r.client, *accepted.OperationID, timeout); err != nil {
			return err
		}
	} else {
		// Not tracked as an operation: the rebuild shows in the status. Wait
		// for it to start (briefly: it may be over already), then to end.
		_ = waitUntil(ctx, 2*time.Minute, func() (bool, error) {
			s, err := r.client.GetInstanceStatus(ctx, id, true)
			if err != nil {
				return false, nil
			}
			return s.Busy || (s.DeploymentProgress != nil && *s.DeploymentProgress < 100), nil
		})
	}
	return r.waitForReady(ctx, id, "running", timeout)
}

// ── CRUD ────────────────────────────────────────────────────────────────────

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultInstanceCreateTimeout)
	resp.Diagnostics.Append(d...)
	body, d := r.orderBody(ctx, plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	order, err := r.client.CreateOrder(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not order the instance", err.Error())
		return
	}
	plan.OrderID = stringOrNull(order.ID)
	if order.SubscriptionID != nil {
		plan.SubscriptionID = types.StringValue(*order.SubscriptionID)
	}
	id, err := r.waitForOrder(ctx, order, timeout)
	if err != nil {
		resp.Diagnostics.AddError("The instance was not built", err.Error())
		return
	}

	// In the state from here on: a failure below leaves it there (tainted)
	// instead of a server Terraform no longer knows about.
	partial := plan
	partial.ID = types.StringValue(id)
	partial.Status = types.StringValue("deploying")
	for _, v := range []*types.String{&partial.IPv4Address, &partial.IPv6Address, &partial.Hostname, &partial.RegionName, &partial.CreatedAt} {
		if v.IsUnknown() {
			*v = types.StringNull()
		}
	}
	for _, v := range []*types.Int64{&partial.VCPUs, &partial.RAMMB, &partial.DiskGB} {
		if v.IsUnknown() {
			*v = types.Int64Null()
		}
	}
	if partial.IPAddresses.IsUnknown() {
		partial.IPAddresses = types.ListNull(instanceIPType)
	}
	if partial.VPCID.IsUnknown() {
		partial.VPCID = types.StringNull()
	}
	if partial.SubscriptionID.IsUnknown() {
		partial.SubscriptionID = types.StringNull()
	}
	if partial.ProjectID.IsUnknown() {
		partial.ProjectID = types.StringNull()
	}
	if partial.Name.IsUnknown() {
		partial.Name = types.StringValue("")
	}
	settleExtras(&partial)
	resp.Diagnostics.Append(resp.State.Set(ctx, partial)...)

	if err := r.waitForReady(ctx, id, "running", timeout); err != nil {
		resp.Diagnostics.AddError("The instance is not ready", fmt.Sprintf("The instance %s was ordered but is not built and running yet: %s", id, err))
		return
	}
	if known(plan.Description) && plan.Description.ValueString() != "" {
		if _, err := r.client.UpdateInstance(ctx, id, map[string]any{"description": plan.Description.ValueString()}); err != nil {
			resp.Diagnostics.AddError("Could not describe the instance", err.Error())
			return
		}
	}
	if plan.PowerState.ValueString() == "stopped" {
		if err := r.setPower(ctx, id, "stopped", timeout); err != nil {
			resp.Diagnostics.AddError("Could not stop the instance", err.Error())
			return
		}
	}
	// Extra addresses are not ordered: they are added once it is built.
	partial.ExtraIPv4, partial.ExtraIPv6 = types.Int64Unknown(), types.Int64Unknown()
	r.readInto(ctx, partial, &resp.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || (!known(plan.ExtraIPv4) && !known(plan.ExtraIPv6)) {
		return
	}
	var built instanceModel
	resp.Diagnostics.Append(resp.State.Get(ctx, &built)...)
	want := built
	want.ExtraIPv4, want.ExtraIPv6 = plan.ExtraIPv4, plan.ExtraIPv6
	if len(resizeChanges(ctx, built, want)) == 0 {
		return
	}
	if _, err := r.resize(ctx, id, built, want, false, timeout); err != nil {
		// Not a reason to replace the instance: the next apply asks again.
		resp.Diagnostics.AddWarning("The extra addresses were not added",
			fmt.Sprintf("The instance %s is built, but its extra addresses could not be added: %s The next apply tries again.", id, err))
		return
	}
	r.readInto(ctx, built, &resp.State, &resp.Diagnostics)
}

// readInto reads the instance and sets the state from it.
func (r *instanceResource) readInto(ctx context.Context, prior instanceModel, state interface {
	Set(context.Context, any) diag.Diagnostics
}, diags *diag.Diagnostics) {
	i, err := r.client.GetInstance(ctx, prior.ID.ValueString())
	if err != nil {
		diags.AddError("Could not read the instance", err.Error())
		return
	}
	s, err := r.client.GetInstanceStatus(ctx, i.ID, false)
	if err != nil && !client.IsNotFound(err) {
		diags.AddError("Could not read the instance's status", err.Error())
		return
	}
	m, d := instanceFromAPI(ctx, prior, i, s)
	diags.Append(d...)
	r.readExtras(ctx, &m)
	diags.Append(state.Set(ctx, m)...)
}

// readExtras reads what the instance has beyond its plan — its main disk,
// data disks, add-ons and addresses — from its resize options. Best effort:
// what cannot be read keeps what the state had.
func (r *instanceResource) readExtras(ctx context.Context, m *instanceModel) {
	opts, err := r.client.GetResizeOptions(ctx, m.ID.ValueString())
	if err != nil {
		tflog.Debug(ctx, "Could not read the instance's resize options", map[string]any{"id": m.ID.ValueString(), "error": err.Error()})
	} else {
		extrasFromOptions(ctx, m, opts)
	}
	settleExtras(m)
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	i, err := r.client.GetInstance(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the instance", err.Error())
		return
	}
	s, err := r.client.GetInstanceStatus(ctx, i.ID, false)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not read the instance's status", err.Error())
		return
	}
	m, d := instanceFromAPI(ctx, state, i, s)
	resp.Diagnostics.Append(d...)
	r.readExtras(ctx, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, defaultInstanceUpdateTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id := state.ID.ValueString()

	patch := map[string]any{}
	if known(plan.Name) && !plan.Name.Equal(state.Name) {
		patch["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(state.Description) {
		patch["description"] = plan.Description.ValueString()
	}
	if len(patch) > 0 {
		if _, err := r.client.UpdateInstance(ctx, id, patch); err != nil {
			resp.Diagnostics.AddError("Could not update the instance", err.Error())
			return
		}
	}

	// Resized first: a reinstall may need the bigger plan.
	if changed := resizeChanges(ctx, state, plan); len(changed) > 0 {
		var mainDisk types.Int64
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("main_disk_extra_gb"), &mainDisk)...)
		tflog.Info(ctx, "Resizing the instance", map[string]any{"id": id, "changed": changed})
		resized, err := r.resize(ctx, id, state, plan, !mainDisk.IsNull(), timeout)
		if err != nil {
			resizeError(&resp.Diagnostics, id, err)
			return
		}
		if resized {
			// A resize may power it off and on: the power state is what it is now.
			if st, err := r.client.GetInstanceStatus(ctx, id, true); err == nil && (st.PowerState == "running" || st.PowerState == "stopped") {
				state.PowerState = types.StringValue(st.PowerState)
			}
		}
	}

	// Rebuilt from a catalog image, the instance has the planned password.
	rebuiltWithPassword := false
	if changed := reinstallChanges(state, plan); len(changed) > 0 {
		tflog.Info(ctx, "Reinstalling the instance", map[string]any{"id": id, "changed": changed})
		if err := r.reinstall(ctx, id, plan, timeout); err != nil {
			resp.Diagnostics.AddError("Could not reinstall the instance",
				fmt.Sprintf("Changing %s reinstalls the instance %s, which did not finish: %s", strings.Join(changed, ", "), id, err))
			return
		}
		rebuiltWithPassword = !known(plan.CustomImageID)
		state.PowerState = types.StringValue("running")
	}

	// A password an import brings in is taken as it is: the instance's own
	// is not known, and resetting it was not asked for.
	passwordChanged := known(plan.Password) && !plan.Password.Equal(state.Password) &&
		!(state.imported() && state.Password.IsNull())
	if passwordChanged && !rebuiltWithPassword {
		_, err := r.client.ResetInstancePassword(ctx, id, plan.Password.ValueString())
		switch {
		case client.HasStatus(err, 404) || client.HasStatus(err, 405):
			if _, gone := r.client.GetInstance(ctx, id); client.IsNotFound(gone) {
				resp.Diagnostics.AddError("Could not reset the password", err.Error())
				return
			}
			resp.Diagnostics.AddWarning("The password was not reset",
				"This API cannot reset an instance's password yet: the new password is kept in the state, but the "+
					"instance keeps the one it had. Reset it in the console.")
		case err != nil:
			resp.Diagnostics.AddError("Could not reset the password", err.Error())
			return
		}
	}

	if known(plan.ProjectID) && plan.ProjectID.ValueString() != state.ProjectID.ValueString() {
		switch {
		case plan.ProjectID.ValueString() != "":
			// Putting it in a project moves it from the one it was in.
			if _, err := r.client.AddProjectResource(ctx, plan.ProjectID.ValueString(), "INSTANCE", id); err != nil {
				resp.Diagnostics.AddError("Could not put the instance in the project", err.Error())
				return
			}
		case state.ProjectID.ValueString() != "":
			err := r.client.RemoveProjectResource(ctx, state.ProjectID.ValueString(), "INSTANCE", id)
			if err != nil && !client.IsNotFound(err) {
				resp.Diagnostics.AddError("Could not take the instance out of its project", err.Error())
				return
			}
		}
	}

	if known(plan.PowerState) && !plan.PowerState.Equal(state.PowerState) {
		if err := r.setPower(ctx, id, plan.PowerState.ValueString(), timeout); err != nil {
			resp.Diagnostics.AddError("Could not change the power state", err.Error())
			return
		}
	}
	r.readInto(ctx, plan, &resp.State, &resp.Diagnostics)
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, defaultInstanceDeleteTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id := state.ID.ValueString()
	err := waitUntil(ctx, timeout, func() (bool, error) {
		out, err := r.client.DeleteInstance(ctx, id)
		switch {
		case client.IsNotFound(err):
			return true, nil
		case client.HasStatus(err, 409):
			// Another operation holds it (a backup, a resize): again once it ends.
			return false, nil
		case err != nil:
			return false, err
		case !out.Deleted:
			return false, fmt.Errorf("the API did not destroy the instance: %s", out.Message)
		}
		return true, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not destroy the instance", err.Error())
		return
	}
	err = waitUntil(ctx, timeout, func() (bool, error) {
		_, err := r.client.GetInstance(ctx, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddWarning("The instance is still being destroyed",
			fmt.Sprintf("The instance %s is being destroyed and finishes on its own (%s).", id, err))
	}
}

func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		all, err := r.client.ListInstances(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the instances", err.Error())
			return
		}
		var found []string
		for _, i := range all {
			if i.Name != nil && *i.Name == id {
				found = append(found, i.ID)
			}
		}
		switch len(found) {
		case 0:
			resp.Diagnostics.AddError("No such instance", fmt.Sprintf("No instance has the id or name %q. Import an instance by its id or its name.", id))
			return
		case 1:
			id = found[0]
		default:
			resp.Diagnostics.AddError("More than one instance has that name",
				fmt.Sprintf("Instances %s are all named %q: import one by its id.", strings.Join(found, ", "), id))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	// What an import cannot read back starts as the defaults say, so the
	// first plan after it is quiet about them.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("payment"), "auto")...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("network_mode"), "public")...)
}
