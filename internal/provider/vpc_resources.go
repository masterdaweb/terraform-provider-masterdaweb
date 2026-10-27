package provider

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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

// ── masterdaweb_vpc ─────────────────────────────────────────────────────────

var (
	_ resource.Resource                = (*vpcResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpcResource)(nil)
	_ resource.ResourceWithImportState = (*vpcResource)(nil)
)

// NewVPCResource is masterdaweb_vpc.
func NewVPCResource() resource.Resource { return &vpcResource{} }

type vpcResource struct{ client *client.Client }

type vpcModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	RegionID    types.String `tfsdk:"region_id"`
	Subnet      types.String `tfsdk:"subnet"`
	Default     types.Bool   `tfsdk:"default"`
	InstanceIDs types.Set    `tfsdk:"instance_ids"`
	Members     types.List   `tfsdk:"members"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

var vpcMemberType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"instance_id": types.StringType,
	"private_ip":  types.StringType,
	"status":      types.StringType,
}}

// vpcMembers are a VPC's instances with their private addresses.
func vpcMembers(v *client.VPC) types.List {
	vals := []attr.Value{}
	for _, m := range v.Members {
		vals = append(vals, types.ObjectValueMust(vpcMemberType.AttrTypes, map[string]attr.Value{
			"instance_id": types.StringValue(m.InstanceID),
			"private_ip":  stringOrNull(m.PrivateIP),
			"status":      types.StringValue(m.Status),
		}))
	}
	return types.ListValueMust(vpcMemberType, vals)
}

// vpcMembersAttribute describes members, for the resource and the data
// source.
var vpcMembersDescription = "The instances on it, with their private addresses."

func (r *vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (r *vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A VPC: a private network between instances in one region. Instances join it with " +
			"masterdaweb_vpc_attachment, or when they are created (masterdaweb_instance.vpc_id). VPCs connect " +
			"instances only (not serverless services).",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The VPC's id."),
			"name": schema.StringAttribute{
				Required: true, Description: "The VPC's name, up to 120 characters. Changed in place.",
				Validators: []validator.String{stringvalidator.LengthBetween(1, 120)},
			},
			"description": schema.StringAttribute{
				Optional: true, Description: "What the VPC is for, up to 500 characters. Changed in place.",
				Validators: []validator.String{stringvalidator.LengthAtMost(500)},
			},
			"region_id": schema.StringAttribute{
				Required: true, Description: "The region, by id (masterdaweb_regions). Changing it replaces the VPC.",
				// An API from before VPCs showed their region cannot say it after an import.
				Validators:    []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[0-9]+$`), "must be a region id, like \"3\"")},
				PlanModifiers: []planmodifier.String{requiresReplaceUnlessImported()},
			},
			"subnet": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The private subnet handed out inside the VPC, a CIDR within 10.0.0.0/8, 172.16.0.0/12 or " +
					"192.168.0.0/16, like 10.20.0.0/24. 10.200.0.0/24 when left out. Changing it replaces the VPC.",
				Validators:    []validator.String{privateSubnet{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()},
			},
			"default": schema.BoolAttribute{Computed: true, Description: "Whether it is the account's default VPC."},
			"instance_ids": schema.SetAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "The instances on it.",
			},
			"members": schema.ListNestedAttribute{
				Computed: true, Description: vpcMembersDescription,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"instance_id": schema.StringAttribute{Computed: true, Description: "The instance."},
					"private_ip":  schema.StringAttribute{Computed: true, Description: "Its address on the VPC; null until its private network interface is set up."},
					"status":      schema.StringAttribute{Computed: true, Description: "Its private network interface, as last seen."},
				}},
			},
			"created_at": computedString("When the VPC was created (RFC 3339)."),
		},
	}
}

// privateSubnet checks a VPC subnet: an IPv4 network within the private
// ranges (RFC 1918), with room for instances.
type privateSubnet struct{}

func (privateSubnet) Description(context.Context) string {
	return "must be an IPv4 network within 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16, from /8 to /30, like 10.20.0.0/24"
}
func (v privateSubnet) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v privateSubnet) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(req.ConfigValue.ValueString()))
	if err != nil || !p.Addr().Is4() || !p.Masked().Addr().IsPrivate() || p.Bits() < 8 || p.Bits() > 30 ||
		!privateRange(p) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid subnet", fmt.Sprintf("%q %s.", req.ConfigValue.ValueString(), v.Description(ctx)))
	}
}

// privateRange says whether a network lies wholly inside one RFC 1918 range.
func privateRange(p netip.Prefix) bool {
	for _, r := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		rp := netip.MustParsePrefix(r)
		if p.Bits() >= rp.Bits() && rp.Contains(p.Masked().Addr()) {
			return true
		}
	}
	return false
}

func (r *vpcResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func vpcFromAPI(prior vpcModel, v *client.VPC) vpcModel {
	m := prior
	m.ID = types.StringValue(v.ID)
	m.Name = types.StringValue(v.Name)
	if known(prior.Name) && strings.TrimSpace(prior.Name.ValueString()) == v.Name {
		m.Name = prior.Name
	}
	m.Description = keepEquivalent(prior.Description, v.Description)
	m.Subnet = stringOrNull(v.Subnet)
	if known(prior.Subnet) && v.Subnet != nil && normalizeCIDR(prior.Subnet.ValueString()) == normalizeCIDR(*v.Subnet) {
		m.Subnet = prior.Subnet
	}
	m.Default = types.BoolValue(v.Default)
	if v.RegionID != nil {
		m.RegionID = types.StringValue(*v.RegionID)
	}
	ids := append([]string{}, v.InstanceIDs...)
	m.InstanceIDs = stringSet(ids)
	m.Members = vpcMembers(v)
	m.CreatedAt = stringOrNull(v.CreatedAt)
	return m
}

func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "region": client.NumericID(plan.RegionID.ValueString())}
	if known(plan.Description) && plan.Description.ValueString() != "" {
		body["description"] = plan.Description.ValueString()
	}
	if known(plan.Subnet) {
		body["subnet"] = strings.TrimSpace(plan.Subnet.ValueString())
	}
	v, err := r.client.CreateVPC(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not create the VPC", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpcFromAPI(plan, v))...)
}

func (r *vpcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetVPC(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the VPC", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpcFromAPI(state, v))...)
}

func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vpcModel
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
		patch["description"] = plan.Description.ValueString()
	}
	var v *client.VPC
	var err error
	if len(patch) > 0 {
		v, err = r.client.UpdateVPC(ctx, state.ID.ValueString(), patch)
	} else {
		v, err = r.client.GetVPC(ctx, state.ID.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not update the VPC", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpcFromAPI(plan, v))...)
}

func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Its instances leave it with it (their private NICs are removed in the
	// background).
	err := r.client.DeleteVPC(ctx, state.ID.ValueString())
	if err == nil || client.IsNotFound(err) {
		return
	}
	if state.Default.ValueBool() {
		resp.Diagnostics.AddError("The default VPC is never deleted",
			err.Error()+" Remove it from the configuration with a removed block (or terraform state rm) instead.")
		return
	}
	resp.Diagnostics.AddError("Could not delete the VPC", err.Error())
}

func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(id) {
		vpcs, err := r.client.ListVPCs(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the VPCs", err.Error())
			return
		}
		var found []string
		for _, v := range vpcs {
			if v.Name == id {
				found = append(found, v.ID)
			}
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("No single VPC by that name",
				fmt.Sprintf("%d VPCs are named %q. Import a VPC by its id, or by a name only it has.", len(found), id))
			return
		}
		id = found[0]
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ── masterdaweb_vpc_attachment ──────────────────────────────────────────────

var (
	_ resource.Resource                = (*vpcAttachmentResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpcAttachmentResource)(nil)
	_ resource.ResourceWithImportState = (*vpcAttachmentResource)(nil)
)

// NewVPCAttachmentResource is masterdaweb_vpc_attachment.
func NewVPCAttachmentResource() resource.Resource { return &vpcAttachmentResource{} }

type vpcAttachmentResource struct{ client *client.Client }

type vpcAttachmentModel struct {
	ID         types.String   `tfsdk:"id"`
	VPCID      types.String   `tfsdk:"vpc_id"`
	InstanceID types.String   `tfsdk:"instance_id"`
	PrivateIP  types.String   `tfsdk:"private_ip"`
	Restart    types.Bool     `tfsdk:"restart"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

const defaultVPCAttachmentTimeout = 15 * time.Minute

func (r *vpcAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_attachment"
}

func (r *vpcAttachmentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An instance on a VPC of its region. Attaching plugs a private NIC into the instance (Terraform " +
			"waits until the VPC lists it); an instance is on one VPC at a time. Destroying it takes the private NIC " +
			"out and keeps the public one.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("<vpc_id>/<instance_id>."),
			"vpc_id": schema.StringAttribute{
				Required: true, Description: "The VPC. Changing it moves the instance (replaces the attachment).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"instance_id": schema.StringAttribute{
				Required: true, Description: "The instance. Changing it replaces the attachment.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"private_ip": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "The instance's address on the VPC, inside its subnet and free; DHCP picks one when left " +
					"out. Changing it makes a new attachment.",
				Validators: []validator.String{ipv4Address{}},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"restart": schema.BoolAttribute{
				Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
				Description: "Whether the instance restarts when it joins or leaves, so the change to its private NIC " +
					"applies at once; otherwise it applies at its next restart.",
			},
			"timeouts": timeoutsAttribute(ctx, timeouts.Opts{
				Create: true, Delete: true,
				CreateDescription: "How long to wait for the instance to be on the VPC (default 15m).",
				DeleteDescription: "How long to wait for it to leave (default 15m).",
			}),
		},
	}
}

func (r *vpcAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *vpcAttachmentResource) onVPC(ctx context.Context, vpcID, instanceID string) (bool, error) {
	m, err := r.member(ctx, vpcID, instanceID)
	return m != nil, err
}

// member is the instance as the VPC lists it, nil when it is not on it.
func (r *vpcAttachmentResource) member(ctx context.Context, vpcID, instanceID string) (*client.VPCMember, error) {
	v, err := r.client.GetVPC(ctx, vpcID)
	if err != nil {
		return nil, err
	}
	for i := range v.Members {
		if v.Members[i].InstanceID == instanceID {
			return &v.Members[i], nil
		}
	}
	if slices.Contains(v.InstanceIDs, instanceID) {
		// An API from before members: on it, its address unknown.
		return &client.VPCMember{InstanceID: instanceID}, nil
	}
	return nil, nil
}

// withAddress is the attachment with the address the VPC gave the instance:
// the configuration's spelling while it is the same, the planned one while
// the API does not show it yet.
func withAddress(m vpcAttachmentModel, member *client.VPCMember) vpcAttachmentModel {
	switch {
	case member != nil && member.PrivateIP != nil:
		if !(known(m.PrivateIP) && normalizeIP(m.PrivateIP.ValueString()) == normalizeIP(*member.PrivateIP)) {
			m.PrivateIP = types.StringValue(*member.PrivateIP)
		}
	case m.PrivateIP.IsUnknown():
		m.PrivateIP = types.StringNull()
	}
	return m
}

// ipv4Address checks an IPv4 address.
type ipv4Address struct{}

func (ipv4Address) Description(context.Context) string {
	return "must be an IPv4 address, like 10.20.0.15"
}
func (v ipv4Address) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v ipv4Address) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(req.ConfigValue.ValueString())); err != nil || !a.Is4() {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid address", fmt.Sprintf("%q %s.", req.ConfigValue.ValueString(), v.Description(ctx)))
	}
}

func (r *vpcAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultVPCAttachmentTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	vpcID, instanceID := plan.VPCID.ValueString(), plan.InstanceID.ValueString()
	var accepted *client.OperationAccepted
	err := whileBusy(ctx, timeout, func() error {
		a, err := r.client.AttachVPCInstance(ctx, vpcID, instanceID, plan.PrivateIP.ValueString(), plan.Restart.ValueBool())
		accepted = a
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not attach the instance to the VPC", err.Error())
		return
	}
	var member *client.VPCMember
	err = waitForAccepted(ctx, r.client, accepted, timeout, func() (bool, error) {
		m, err := r.member(ctx, vpcID, instanceID)
		member = m
		return m != nil, err
	})
	if err != nil {
		resp.Diagnostics.AddError("The instance did not join the VPC", err.Error())
		return
	}
	plan.ID = types.StringValue(vpcID + "/" + instanceID)
	resp.Diagnostics.Append(resp.State.Set(ctx, withAddress(plan, member))...)
}

func (r *vpcAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	member, err := r.member(ctx, state.VPCID.ValueString(), state.InstanceID.ValueString())
	if client.IsNotFound(err) || (err == nil && member == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the VPC", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, withAddress(state, member))...)
}

func (r *vpcAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every attribute replaces the attachment but restart and the timeouts,
	// which only say how the next change is made.
	var plan vpcAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vpcAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, defaultVPCAttachmentTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	vpcID, instanceID := state.VPCID.ValueString(), state.InstanceID.ValueString()
	// Gone with its VPC or its instance, or taken off already.
	on, err := r.onVPC(ctx, vpcID, instanceID)
	if client.IsNotFound(err) || (err == nil && !on) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the VPC", err.Error())
		return
	}
	var accepted *client.OperationAccepted
	err = whileBusy(ctx, timeout, func() error {
		a, err := r.client.DetachVPCInstance(ctx, vpcID, instanceID, state.Restart.ValueBool())
		accepted = a
		return err
	})
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not take the instance off the VPC", err.Error())
		return
	}
	err = waitForAccepted(ctx, r.client, accepted, timeout, func() (bool, error) {
		on, err := r.onVPC(ctx, vpcID, instanceID)
		if client.IsNotFound(err) {
			return true, nil
		}
		return !on, err
	})
	if err != nil {
		resp.Diagnostics.AddError("The instance did not leave the VPC", err.Error())
	}
}

func (r *vpcAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(strings.TrimSpace(req.ID), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Unexpected import id",
			fmt.Sprintf("An attachment is imported as <vpc_id>/<instance_id>, like 12/3456, not %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("restart"), false)...)
}
