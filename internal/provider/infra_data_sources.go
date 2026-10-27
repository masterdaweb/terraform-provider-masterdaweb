package provider

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// The data sources of the infrastructure API: the catalog (regions, plans,
// images), instances, SSH keys, VPCs, firewalls and DNS zones.

// lookupBy is the "give one of these" rule of a data source that finds one
// thing.
func lookupBy(attrs ...string) []datasource.ConfigValidator {
	exprs := make([]path.Expression, 0, len(attrs))
	for _, a := range attrs {
		exprs = append(exprs, path.MatchRoot(a))
	}
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(exprs...)}
}

func dsComputed(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: desc}
}

func dsLookup(desc string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, Description: desc}
}

// ── masterdaweb_regions ─────────────────────────────────────────────────────

// NewRegionsDataSource is masterdaweb_regions.
func NewRegionsDataSource() datasource.DataSource { return &regionsDataSource{} }

type regionsDataSource struct{ client *client.Client }

type regionData struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	RegionName    types.String `tfsdk:"region_name"`
	Available     types.Bool   `tfsdk:"available"`
	IPv4Available types.Bool   `tfsdk:"ipv4_available"`
	IPv6Available types.Bool   `tfsdk:"ipv6_available"`
}

type regionsData struct {
	ID        types.String `tfsdk:"id"`
	Available types.Bool   `tfsdk:"available"`
	Regions   []regionData `tfsdk:"regions"`
}

func (d *regionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}

func (d *regionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The regions (datacenters) instances are placed in: region_id of masterdaweb_instance and " +
			"masterdaweb_vpc.",
		Attributes: map[string]schema.Attribute{
			"id": dsComputed("Always regions."),
			"available": schema.BoolAttribute{
				Optional:    true,
				Description: "true: only the regions new instances can be placed in right now.",
			},
			"regions": schema.ListNestedAttribute{
				Computed: true, Description: "The regions, by id.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":          dsComputed("The region's id (region_id)."),
					"name":        dsComputed("Its name, like São Paulo."),
					"region_name": dsComputed("Its short name, like br-sp."),
					"available": schema.BoolAttribute{
						Computed:    true,
						Description: "Whether new instances can be placed in it right now (false: full for now, not gone).",
					},
					"ipv4_available": schema.BoolAttribute{Computed: true, Description: "Whether a public IPv4 address can be had in it."},
					"ipv6_available": schema.BoolAttribute{Computed: true, Description: "Whether an IPv6 address can be had in it."},
				}},
			},
		},
	}
}

func (d *regionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *regionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg regionsData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListRegions(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the regions", err.Error())
		return
	}
	out := regionsData{ID: types.StringValue("regions"), Available: cfg.Available, Regions: []regionData{}}
	for _, r := range all {
		if cfg.Available.ValueBool() && !r.Available {
			continue
		}
		out.Regions = append(out.Regions, regionData{
			ID: types.StringValue(r.ID), Name: types.StringValue(r.Name), RegionName: stringOrNull(r.RegionName),
			Available: types.BoolValue(r.Available), IPv4Available: types.BoolValue(r.IPv4Available),
			IPv6Available: types.BoolValue(r.IPv6Available),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}

// ── masterdaweb_plans ───────────────────────────────────────────────────────

// NewPlansDataSource is masterdaweb_plans.
func NewPlansDataSource() datasource.DataSource { return &plansDataSource{} }

type plansDataSource struct{ client *client.Client }

type planData struct {
	Code        types.String `tfsdk:"code"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Kind        types.String `tfsdk:"kind"`
	VCPUs       types.Int64  `tfsdk:"vcpus"`
	RAMMB       types.Int64  `tfsdk:"ram_mb"`
	BaseDiskGB  types.Int64  `tfsdk:"base_disk_gb"`
	Currency    types.String `tfsdk:"currency"`
	HourlyRates types.Map    `tfsdk:"hourly_rates"`
}

type plansData struct {
	ID        types.String `tfsdk:"id"`
	Kind      types.String `tfsdk:"kind"`
	Term      types.String `tfsdk:"term"`
	MinVCPUs  types.Int64  `tfsdk:"min_vcpus"`
	MinRAMMB  types.Int64  `tfsdk:"min_ram_mb"`
	MinDiskGB types.Int64  `tfsdk:"min_disk_gb"`
	Plans     []planData   `tfsdk:"plans"`
}

func (d *plansDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plans"
}

func (d *plansDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The plans instances are ordered on (the plan of masterdaweb_instance), smallest first, with " +
			"their rates per billing term. The filters narrow the list.",
		Attributes: map[string]schema.Attribute{
			"id": dsComputed("Always plans."),
			"kind": schema.StringAttribute{
				Optional: true, Description: "Only the plans of this kind, like VM.",
			},
			"term": schema.StringAttribute{
				Optional:    true,
				Description: "Only the plans offered on this billing term: MONTHLY, QUARTERLY, SEMI_ANNUAL, ANNUAL or ON_DEMAND.",
				Validators:  []validator.String{stringvalidator.OneOf("MONTHLY", "QUARTERLY", "SEMI_ANNUAL", "ANNUAL", "ON_DEMAND")},
			},
			"min_vcpus":   schema.Int64Attribute{Optional: true, Description: "Only the plans with at least this many vCPUs."},
			"min_ram_mb":  schema.Int64Attribute{Optional: true, Description: "Only the plans with at least this much memory, in MB."},
			"min_disk_gb": schema.Int64Attribute{Optional: true, Description: "Only the plans with at least this much disk included, in GB."},
			"plans": schema.ListNestedAttribute{
				Computed: true, Description: "The plans: fewest vCPUs, then least memory and disk, first.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"code":         dsComputed("The plan's code (masterdaweb_instance.plan)."),
					"name":         dsComputed("Its name."),
					"description":  dsComputed("What it is for."),
					"kind":         dsComputed("What it provisions, like VM."),
					"vcpus":        schema.Int64Attribute{Computed: true, Description: "Its vCPUs."},
					"ram_mb":       schema.Int64Attribute{Computed: true, Description: "Its memory, in MB."},
					"base_disk_gb": schema.Int64Attribute{Computed: true, Description: "The disk it includes, in GB (more is ordered with main_disk_extra_gb)."},
					"currency":     dsComputed("The rates' currency, like BRL."),
					"hourly_rates": schema.MapAttribute{
						Computed: true, ElementType: types.StringType,
						Description: "Its rate per hour in micro-cents (millionths of a cent), by billing term; a term it " +
							"is not offered on is left out.",
					},
				}},
			},
		},
	}
}

func (d *plansDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *plansDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg plansData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListOfferings(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the plans", err.Error())
		return
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.VCPUs != b.VCPUs {
			return a.VCPUs < b.VCPUs
		}
		if a.RAMMB != b.RAMMB {
			return a.RAMMB < b.RAMMB
		}
		if a.BaseDiskGB != b.BaseDiskGB {
			return a.BaseDiskGB < b.BaseDiskGB
		}
		return a.Code < b.Code
	})
	out := cfg
	out.ID = types.StringValue("plans")
	out.Plans = []planData{}
	for _, o := range all {
		rates := map[string]string{}
		for _, r := range o.Rates {
			if r.RateMicroCentsPerHour != nil {
				rates[r.Term] = *r.RateMicroCentsPerHour
			}
		}
		_, onTerm := rates[cfg.Term.ValueString()]
		switch {
		case known(cfg.Kind) && !strings.EqualFold(o.Kind, cfg.Kind.ValueString()),
			known(cfg.Term) && !onTerm,
			known(cfg.MinVCPUs) && o.VCPUs < cfg.MinVCPUs.ValueInt64(),
			known(cfg.MinRAMMB) && o.RAMMB < cfg.MinRAMMB.ValueInt64(),
			known(cfg.MinDiskGB) && o.BaseDiskGB < cfg.MinDiskGB.ValueInt64():
			continue
		}
		m, diags := types.MapValueFrom(ctx, types.StringType, rates)
		resp.Diagnostics.Append(diags...)
		out.Plans = append(out.Plans, planData{
			Code: types.StringValue(o.Code), Name: types.StringValue(o.Name), Description: stringOrNull(o.Description),
			Kind: types.StringValue(o.Kind), VCPUs: types.Int64Value(o.VCPUs), RAMMB: types.Int64Value(o.RAMMB),
			BaseDiskGB: types.Int64Value(o.BaseDiskGB), Currency: types.StringValue(o.Currency), HourlyRates: m,
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}

// ── masterdaweb_images ──────────────────────────────────────────────────────

// NewImagesDataSource is masterdaweb_images.
func NewImagesDataSource() datasource.DataSource { return &imagesDataSource{} }

type imagesDataSource struct{ client *client.Client }

type imageInputData struct {
	Key     types.String `tfsdk:"key"`
	Type    types.String `tfsdk:"type"`
	Need    types.String `tfsdk:"need"`
	Label   types.String `tfsdk:"label"`
	Default types.String `tfsdk:"default"`
}

type imageData struct {
	Code             types.String     `tfsdk:"code"`
	Name             types.String     `tfsdk:"name"`
	OSFamily         types.String     `tfsdk:"os_family"`
	Type             types.String     `tfsdk:"type"`
	Category         types.String     `tfsdk:"category"`
	Description      types.String     `tfsdk:"description"`
	SupportsPassword types.Bool       `tfsdk:"supports_password"`
	MinDiskGB        types.Int64      `tfsdk:"min_disk_gb"`
	MinRAMMB         types.Int64      `tfsdk:"min_ram_mb"`
	MinVCPU          types.Int64      `tfsdk:"min_vcpu"`
	Inputs           []imageInputData `tfsdk:"inputs"`
}

type imagesData struct {
	ID       types.String `tfsdk:"id"`
	OSFamily types.String `tfsdk:"os_family"`
	Type     types.String `tfsdk:"type"`
	Category types.String `tfsdk:"category"`
	Images   []imageData  `tfsdk:"images"`
}

func (d *imagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The operating systems and ready-made apps an instance is installed with (the image of " +
			"masterdaweb_instance), with what each needs. The filters narrow the list.",
		Attributes: map[string]schema.Attribute{
			"id": dsComputed("Always images."),
			"os_family": schema.StringAttribute{
				Optional: true, Description: "Only linux, windows or other images.",
				Validators: []validator.String{stringvalidator.OneOf("linux", "windows", "other")},
			},
			"type": schema.StringAttribute{
				Optional: true, Description: "Only operating systems (OPERATING_SYSTEM) or apps (MARKETPLACE_APP).",
				Validators: []validator.String{stringvalidator.OneOf("OPERATING_SYSTEM", "MARKETPLACE_APP")},
			},
			"category": schema.StringAttribute{Optional: true, Description: "Only the images of this category."},
			"images": schema.ListNestedAttribute{
				Computed: true, Description: "The images.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"code":              dsComputed("The image's code (masterdaweb_instance.image)."),
					"name":              dsComputed("Its name."),
					"os_family":         dsComputed("linux, windows or other."),
					"type":              dsComputed("OPERATING_SYSTEM or MARKETPLACE_APP."),
					"category":          dsComputed("Its category."),
					"description":       dsComputed("What it is."),
					"supports_password": schema.BoolAttribute{Computed: true, Description: "Whether masterdaweb_instance.password applies to it."},
					"min_disk_gb":       schema.Int64Attribute{Computed: true, Description: "The least disk it needs, in GB."},
					"min_ram_mb":        schema.Int64Attribute{Computed: true, Description: "The least memory it needs, in MB."},
					"min_vcpu":          schema.Int64Attribute{Computed: true, Description: "The fewest vCPUs it needs."},
					"inputs": schema.ListNestedAttribute{
						Computed:    true,
						Description: "What the app asks before it can run, answered in masterdaweb_instance.app_inputs.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"key":     dsComputed("The answer's key in app_inputs."),
							"type":    dsComputed("What kind of answer it takes."),
							"need":    dsComputed("required (an order is refused without it), recommended or optional."),
							"label":   dsComputed("The question, in English."),
							"default": dsComputed("What it is when not answered, as text."),
						}},
					},
				}},
			},
		},
	}
}

func (d *imagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg imagesData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListCatalogImages(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the images", err.Error())
		return
	}
	out := cfg
	out.ID = types.StringValue("images")
	out.Images = []imageData{}
	for _, im := range all {
		switch {
		case known(cfg.OSFamily) && im.OSFamily != cfg.OSFamily.ValueString(),
			known(cfg.Type) && im.Type != cfg.Type.ValueString(),
			known(cfg.Category) && !strings.EqualFold(im.Category, cfg.Category.ValueString()):
			continue
		}
		inputs := []imageInputData{}
		for _, in := range im.Inputs {
			def := types.StringNull()
			if in.Default != nil {
				def = types.StringValue(fmt.Sprint(in.Default))
			}
			inputs = append(inputs, imageInputData{
				Key: types.StringValue(in.Key), Type: types.StringValue(in.Type), Need: types.StringValue(in.Need),
				Label: stringValueOrNull(in.Fallback.Label), Default: def,
			})
		}
		out.Images = append(out.Images, imageData{
			Code: types.StringValue(im.Code), Name: types.StringValue(im.Name), OSFamily: types.StringValue(im.OSFamily),
			Type: types.StringValue(im.Type), Category: types.StringValue(im.Category), Description: stringOrNull(im.Description),
			SupportsPassword: types.BoolValue(im.SupportsPassword), MinDiskGB: int64OrNull(im.MinDiskGB),
			MinRAMMB: int64OrNull(im.MinRAMMB), MinVCPU: int64OrNull(im.MinVCPU), Inputs: inputs,
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}

// ── masterdaweb_instance / masterdaweb_instances ────────────────────────────

type instanceIPData struct {
	Address types.String `tfsdk:"address"`
	Version types.String `tfsdk:"version"`
	Primary types.Bool   `tfsdk:"primary"`
}

type instanceData struct {
	ID             types.String     `tfsdk:"id"`
	Name           types.String     `tfsdk:"name"`
	Description    types.String     `tfsdk:"description"`
	PowerState     types.String     `tfsdk:"power_state"`
	Suspended      types.Bool       `tfsdk:"suspended"`
	RegionID       types.String     `tfsdk:"region_id"`
	RegionName     types.String     `tfsdk:"region_name"`
	Plan           types.String     `tfsdk:"plan"`
	Hostname       types.String     `tfsdk:"hostname"`
	IPv4Address    types.String     `tfsdk:"ipv4_address"`
	IPv6Address    types.String     `tfsdk:"ipv6_address"`
	IPAddresses    []instanceIPData `tfsdk:"ip_addresses"`
	VCPUs          types.Int64      `tfsdk:"vcpus"`
	RAMMB          types.Int64      `tfsdk:"ram_mb"`
	DiskGB         types.Int64      `tfsdk:"disk_gb"`
	VPCID          types.String     `tfsdk:"vpc_id"`
	Image          types.String     `tfsdk:"image"`
	CustomImageID  types.String     `tfsdk:"custom_image_id"`
	ProjectID      types.String     `tfsdk:"project_id"`
	SubscriptionID types.String     `tfsdk:"subscription_id"`
	CreatedAt      types.String     `tfsdk:"created_at"`
}

// instanceDataAttributes are what the instance data sources say of an
// instance, all computed.
func instanceDataAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":           dsComputed("The instance's id."),
		"name":         dsComputed("Its display name."),
		"description":  dsComputed("Its note."),
		"power_state":  dsComputed("The last power state on record: running or stopped."),
		"suspended":    schema.BoolAttribute{Computed: true, Description: "Whether it is suspended for non-payment."},
		"region_id":    dsComputed("Its region's id."),
		"region_name":  dsComputed("Its region's name."),
		"plan":         dsComputed("Its plan's code."),
		"hostname":     dsComputed("Its hostname."),
		"ipv4_address": dsComputed("Its primary public IPv4 address."),
		"ipv6_address": dsComputed("Its primary IPv6 address."),
		"ip_addresses": schema.ListNestedAttribute{
			Computed: true, Description: "Every address of it.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"address": dsComputed("The address."),
				"version": dsComputed("ipv4 or ipv6."),
				"primary": schema.BoolAttribute{Computed: true, Description: "Whether it is the primary address."},
			}},
		},
		"vcpus":           schema.Int64Attribute{Computed: true, Description: "Its vCPUs."},
		"ram_mb":          schema.Int64Attribute{Computed: true, Description: "Its memory, in MB."},
		"disk_gb":         schema.Int64Attribute{Computed: true, Description: "Its main disk, in GB."},
		"vpc_id":          dsComputed("The VPC it is on."),
		"image":           dsComputed("The catalog image it was built or last reinstalled from; null for a custom image."),
		"custom_image_id": dsComputed("The custom image it was built from, when it was."),
		"project_id":      dsComputed("The project it is in."),
		"subscription_id": dsComputed("The subscription it is billed on."),
		"created_at":      dsComputed("When it was created (RFC 3339)."),
	}
}

func instanceDataFromAPI(i *client.Instance) instanceData {
	m := instanceData{
		ID: types.StringValue(i.ID), Name: stringOrNull(i.Name), Description: stringOrNull(i.Description),
		PowerState: stringValueOrNull(i.PowerStateCached), Suspended: types.BoolValue(i.Suspended),
		RegionID: types.StringNull(), RegionName: types.StringNull(), Plan: stringOrNull(i.OfferingCode),
		Hostname: stringOrNull(i.Hostname), IPv4Address: types.StringNull(), IPv6Address: types.StringNull(),
		IPAddresses: []instanceIPData{}, VCPUs: int64OrNull(i.Specs.VCPUs), RAMMB: int64OrNull(i.Specs.RAMMB),
		DiskGB: int64OrNull(i.Specs.DiskGB), VPCID: stringOrNull(i.VPCID), Image: stringOrNull(i.Image),
		CustomImageID: stringOrNull(i.CustomImageID), ProjectID: stringOrNull(i.ProjectID),
		SubscriptionID: stringOrNull(i.SubscriptionID), CreatedAt: stringOrNull(i.CreatedAt),
	}
	if i.Region != nil {
		m.RegionID, m.RegionName = types.StringValue(i.Region.ID), types.StringValue(i.Region.Name)
	}
	for _, a := range i.IPAddresses {
		m.IPAddresses = append(m.IPAddresses, instanceIPData{
			Address: types.StringValue(a.Address), Version: types.StringValue(a.Version), Primary: types.BoolValue(a.Primary),
		})
	}
	m.IPv4Address = primaryAddress(i.IPAddresses, "ipv4")
	m.IPv6Address = primaryAddress(i.IPAddresses, "ipv6")
	return m
}

// primaryAddress is the primary address of a version, else its first one.
func primaryAddress(ips []client.InstanceIP, version string) types.String {
	first := ""
	for _, a := range ips {
		if a.Version != version {
			continue
		}
		if a.Primary {
			return types.StringValue(a.Address)
		}
		if first == "" {
			first = a.Address
		}
	}
	return stringValueOrNull(first)
}

// NewInstanceDataSource is masterdaweb_instance.
func NewInstanceDataSource() datasource.DataSource { return &instanceDataSource{} }

type instanceDataSource struct{ client *client.Client }

func (d *instanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (d *instanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := instanceDataAttributes()
	attrs["id"] = dsLookup("The instance's id. Give the id or the name.")
	attrs["name"] = dsLookup("The instance's display name, which only it has. Give the id or the name.")
	resp.Schema = schema.Schema{
		Description: "Looks up an instance by its id or its name.",
		Attributes:  attrs,
	}
}

func (d *instanceDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupBy("id", "name")
}

func (d *instanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *instanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg instanceData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var found *client.Instance
	if known(cfg.ID) {
		i, err := d.client.GetInstance(ctx, cfg.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Could not read the instance", err.Error())
			return
		}
		found = i
	} else {
		all, err := d.client.ListInstances(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the instances", err.Error())
			return
		}
		var ids []string
		for i := range all {
			if deref(all[i].Name) == cfg.Name.ValueString() {
				found = &all[i]
				ids = append(ids, all[i].ID)
			}
		}
		switch {
		case len(ids) == 0:
			resp.Diagnostics.AddError("No such instance", fmt.Sprintf("No instance is named %q.", cfg.Name.ValueString()))
			return
		case len(ids) > 1:
			resp.Diagnostics.AddError("More than one instance has that name",
				fmt.Sprintf("Instances %s are all named %q: look one up by its id.", strings.Join(ids, ", "), cfg.Name.ValueString()))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, instanceDataFromAPI(found))...)
}

// NewInstancesDataSource is masterdaweb_instances.
func NewInstancesDataSource() datasource.DataSource { return &instancesDataSource{} }

type instancesDataSource struct{ client *client.Client }

type instancesData struct {
	ID        types.String   `tfsdk:"id"`
	NameRegex types.String   `tfsdk:"name_regex"`
	RegionID  types.String   `tfsdk:"region_id"`
	ProjectID types.String   `tfsdk:"project_id"`
	Instances []instanceData `tfsdk:"instances"`
}

func (d *instancesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instances"
}

func (d *instancesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The account's instances, oldest first. The filters narrow the list.",
		Attributes: map[string]schema.Attribute{
			"id": dsComputed("Always instances."),
			"name_regex": schema.StringAttribute{
				Optional: true, Description: "Only the instances whose name matches this regular expression (RE2).",
				Validators: []validator.String{validRegex{}},
			},
			"region_id":  schema.StringAttribute{Optional: true, Description: "Only the instances in this region."},
			"project_id": schema.StringAttribute{Optional: true, Description: "Only the instances in this project (masterdaweb_project)."},
			"instances": schema.ListNestedAttribute{
				Computed: true, Description: "The instances.",
				NestedObject: schema.NestedAttributeObject{Attributes: instanceDataAttributes()},
			},
		},
	}
}

func (d *instancesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *instancesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg instancesData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListInstances(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the instances", err.Error())
		return
	}
	var re *regexp.Regexp
	if known(cfg.NameRegex) {
		re = regexp.MustCompile(cfg.NameRegex.ValueString())
	}
	// Instances say their project; an API from before that is asked for the
	// project's members.
	var inProject []string
	if known(cfg.ProjectID) && len(all) > 0 && !all[0].Has("project_id") {
		members, err := d.client.ListProjectResources(ctx, cfg.ProjectID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Could not read the project's resources", err.Error())
			return
		}
		inProject = []string{}
		for _, m := range members {
			if m.Kind == "INSTANCE" {
				inProject = append(inProject, m.ID)
			}
		}
	}
	out := cfg
	out.ID = types.StringValue("instances")
	out.Instances = []instanceData{}
	for i := range all {
		in := &all[i]
		switch {
		case re != nil && !re.MatchString(deref(in.Name)),
			known(cfg.RegionID) && (in.Region == nil || in.Region.ID != cfg.RegionID.ValueString()),
			inProject != nil && !slices.Contains(inProject, in.ID),
			inProject == nil && known(cfg.ProjectID) && deref(in.ProjectID) != cfg.ProjectID.ValueString():
			continue
		}
		out.Instances = append(out.Instances, instanceDataFromAPI(in))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}

// validRegex checks a regular expression (RE2, as Go reads it).
type validRegex struct{}

func (validRegex) Description(context.Context) string {
	return "must be a valid regular expression (RE2)"
}
func (v validRegex) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v validRegex) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !known(req.ConfigValue) {
		return
	}
	if _, err := regexp.Compile(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid regular expression", err.Error())
	}
}

// ── masterdaweb_ssh_key ─────────────────────────────────────────────────────

// NewSSHKeyDataSource is masterdaweb_ssh_key.
func NewSSHKeyDataSource() datasource.DataSource { return &sshKeyDataSource{} }

type sshKeyDataSource struct{ client *client.Client }

type sshKeyData struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	PublicKey   types.String `tfsdk:"public_key"`
	Type        types.String `tfsdk:"type"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (d *sshKeyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (d *sshKeyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an SSH key of the account by its id, its name or its fingerprint, for " +
			"masterdaweb_instance.ssh_key_ids.",
		Attributes: map[string]schema.Attribute{
			"id":          dsLookup("The key's id. Give the id, the name or the fingerprint."),
			"name":        dsLookup("The key's name. Give the id, the name or the fingerprint."),
			"fingerprint": dsLookup("The key's SHA256 fingerprint, as ssh-keygen -lf shows it (SHA256:…). Give the id, the name or the fingerprint."),
			"public_key":  dsComputed("The public key, one OpenSSH line."),
			"type":        dsComputed("The key's type, like ssh-ed25519."),
			"created_at":  dsComputed("When the key was added (RFC 3339)."),
		},
	}
}

func (d *sshKeyDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupBy("id", "name", "fingerprint")
}

func (d *sshKeyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *sshKeyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg sshKeyData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var k *client.SSHKey
	if known(cfg.ID) {
		got, err := d.client.GetSSHKey(ctx, cfg.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Could not read the SSH key", err.Error())
			return
		}
		k = got
	} else {
		all, err := d.client.ListSSHKeys(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the SSH keys", err.Error())
			return
		}
		for i := range all {
			if (known(cfg.Name) && all[i].Name == strings.TrimSpace(cfg.Name.ValueString())) ||
				(known(cfg.Fingerprint) && all[i].Fingerprint == strings.TrimSpace(cfg.Fingerprint.ValueString())) {
				k = &all[i]
				break
			}
		}
		if k == nil {
			resp.Diagnostics.AddError("No such SSH key", "The account has no SSH key with that name or fingerprint.")
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, sshKeyData{
		ID: types.StringValue(k.ID), Name: types.StringValue(k.Name), Fingerprint: types.StringValue(k.Fingerprint),
		PublicKey: types.StringValue(k.PublicKey), Type: types.StringValue(k.Type), CreatedAt: stringOrNull(k.CreatedAt),
	})...)
}

// ── masterdaweb_vpc ─────────────────────────────────────────────────────────

// NewVPCDataSource is masterdaweb_vpc.
func NewVPCDataSource() datasource.DataSource { return &vpcDataSource{} }

type vpcDataSource struct{ client *client.Client }

type vpcData struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Default     types.Bool   `tfsdk:"default"`
	Description types.String `tfsdk:"description"`
	RegionID    types.String `tfsdk:"region_id"`
	Subnet      types.String `tfsdk:"subnet"`
	InstanceIDs types.Set    `tfsdk:"instance_ids"`
	Members     types.List   `tfsdk:"members"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (d *vpcDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (d *vpcDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a VPC by its id or its name, or the account's default VPC.",
		Attributes: map[string]schema.Attribute{
			"id":   dsLookup("The VPC's id. Give the id, the name, or default = true."),
			"name": dsLookup("The VPC's name, which only it has. Give the id, the name, or default = true."),
			"default": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description: "true: the account's default VPC. Give the id, the name, or default = true.",
			},
			"description": dsComputed("What the VPC is for."),
			"region_id":   dsComputed("The region it is in."),
			"subnet":      dsComputed("Its private subnet (CIDR)."),
			"instance_ids": schema.SetAttribute{
				Computed: true, ElementType: types.StringType, Description: "The instances on it.",
			},
			"members": schema.ListNestedAttribute{
				Computed: true, Description: vpcMembersDescription,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"instance_id": dsComputed("The instance."),
					"private_ip":  dsComputed("Its address on the VPC; null until its private network interface is set up."),
					"status":      dsComputed("Its private network interface, as last seen."),
				}},
			},
			"created_at": dsComputed("When it was created (RFC 3339)."),
		},
	}
}

func (d *vpcDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupBy("id", "name", "default")
}

func (d *vpcDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *vpcDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vpcData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(cfg.Default) && !cfg.Default.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("default"), "default = false finds nothing",
			"Set default = true for the account's default VPC, or look a VPC up by its id or name.")
		return
	}
	var v *client.VPC
	if known(cfg.ID) {
		got, err := d.client.GetVPC(ctx, cfg.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Could not read the VPC", err.Error())
			return
		}
		v = got
	} else {
		all, err := d.client.ListVPCs(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the VPCs", err.Error())
			return
		}
		var ids []string
		for i := range all {
			if (known(cfg.Name) && all[i].Name == cfg.Name.ValueString()) || (known(cfg.Default) && all[i].Default) {
				v = &all[i]
				ids = append(ids, all[i].ID)
			}
		}
		switch {
		case len(ids) == 0:
			resp.Diagnostics.AddError("No such VPC", "The account has no VPC by that name, or no default VPC.")
			return
		case len(ids) > 1 && known(cfg.Name):
			resp.Diagnostics.AddError("More than one VPC has that name",
				fmt.Sprintf("VPCs %s are all named %q: look one up by its id.", strings.Join(ids, ", "), cfg.Name.ValueString()))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vpcData{
		ID: types.StringValue(v.ID), Name: types.StringValue(v.Name), Default: types.BoolValue(v.Default),
		Description: stringOrNull(v.Description), RegionID: stringOrNull(v.RegionID), Subnet: stringOrNull(v.Subnet),
		InstanceIDs: stringSet(append([]string{}, v.InstanceIDs...)), Members: vpcMembers(v), CreatedAt: stringOrNull(v.CreatedAt),
	})...)
}

// ── masterdaweb_firewall ────────────────────────────────────────────────────

// NewFirewallDataSource is masterdaweb_firewall.
func NewFirewallDataSource() datasource.DataSource { return &firewallDataSource{} }

type firewallDataSource struct{ client *client.Client }

type firewallRuleData struct {
	Direction   types.String `tfsdk:"direction"`
	Action      types.String `tfsdk:"action"`
	Macro       types.String `tfsdk:"macro"`
	Protocol    types.String `tfsdk:"protocol"`
	Source      types.String `tfsdk:"source"`
	Destination types.String `tfsdk:"destination"`
	SourcePort  types.String `tfsdk:"source_port"`
	DestPort    types.String `tfsdk:"dest_port"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Comment     types.String `tfsdk:"comment"`
}

type firewallData struct {
	ID                types.String       `tfsdk:"id"`
	Name              types.String       `tfsdk:"name"`
	Description       types.String       `tfsdk:"description"`
	Enabled           types.Bool         `tfsdk:"enabled"`
	PolicyIn          types.String       `tfsdk:"policy_in"`
	PolicyOut         types.String       `tfsdk:"policy_out"`
	Rules             []firewallRuleData `tfsdk:"rules"`
	InstanceIDs       types.Set          `tfsdk:"instance_ids"`
	HasPendingChanges types.Bool         `tfsdk:"has_pending_changes"`
	CreatedAt         types.String       `tfsdk:"created_at"`
}

func (d *firewallDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall"
}

func (d *firewallDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a firewall by its id or its name, with its rules in the order they apply.",
		Attributes: map[string]schema.Attribute{
			"id":          dsLookup("The firewall's id. Give the id or the name."),
			"name":        dsLookup("The firewall's name, which only it has. Give the id or the name."),
			"description": dsComputed("What it is for."),
			"enabled":     schema.BoolAttribute{Computed: true, Description: "Whether it filters traffic."},
			"policy_in":   dsComputed("What happens to inbound traffic no rule matches: ACCEPT, DROP or REJECT."),
			"policy_out":  dsComputed("What happens to outbound traffic no rule matches."),
			"rules": schema.ListNestedAttribute{
				Computed: true, Description: "Its rules, in the order they apply.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"direction":   dsComputed("in or out."),
					"action":      dsComputed("ACCEPT, DROP or REJECT."),
					"macro":       dsComputed("A named service, like SSH (masterdaweb_firewall_macros)."),
					"protocol":    dsComputed("tcp, udp, icmp…"),
					"source":      dsComputed("Where the traffic comes from (addresses, networks)."),
					"destination": dsComputed("Where it goes to."),
					"source_port": dsComputed("The source ports."),
					"dest_port":   dsComputed("The destination ports."),
					"enabled":     schema.BoolAttribute{Computed: true, Description: "Whether the rule applies."},
					"comment":     dsComputed("A note on the rule."),
				}},
			},
			"instance_ids": schema.SetAttribute{
				Computed: true, ElementType: types.StringType, Description: "The instances it applies to.",
			},
			"has_pending_changes": schema.BoolAttribute{Computed: true, Description: "Whether it has edits not applied yet."},
			"created_at":          dsComputed("When it was created (RFC 3339)."),
		},
	}
}

func (d *firewallDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupBy("id", "name")
}

func (d *firewallDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *firewallDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg firewallData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := cfg.ID.ValueString()
	if !known(cfg.ID) {
		all, err := d.client.ListFirewalls(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the firewalls", err.Error())
			return
		}
		var ids []string
		for _, f := range all {
			if f.Name == cfg.Name.ValueString() {
				ids = append(ids, f.ID)
			}
		}
		if len(ids) != 1 {
			resp.Diagnostics.AddError("No single firewall by that name",
				fmt.Sprintf("%d firewalls are named %q: look one up by its id, or by a name only it has.", len(ids), cfg.Name.ValueString()))
			return
		}
		id = ids[0]
	}
	f, err := d.client.GetFirewall(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the firewall", err.Error())
		return
	}
	rules := []firewallRuleData{}
	for _, r := range sortedRules(f) {
		rules = append(rules, firewallRuleData{
			Direction: types.StringValue(r.Direction), Action: types.StringValue(r.Action), Macro: stringOrNull(r.Macro),
			Protocol: stringOrNull(r.Protocol), Source: stringOrNull(r.Source), Destination: stringOrNull(r.Destination),
			SourcePort: stringOrNull(r.SourcePort), DestPort: stringOrNull(r.DestPort), Enabled: types.BoolValue(r.Enabled),
			Comment: stringOrNull(r.Comment),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, firewallData{
		ID: types.StringValue(f.ID), Name: types.StringValue(f.Name), Description: stringOrNull(f.Description),
		Enabled: types.BoolValue(f.Enabled), PolicyIn: types.StringValue(f.PolicyIn), PolicyOut: types.StringValue(f.PolicyOut),
		Rules: rules, InstanceIDs: stringSet(append([]string{}, f.InstanceIDs...)),
		HasPendingChanges: types.BoolValue(f.HasPendingChanges), CreatedAt: stringOrNull(f.CreatedAt),
	})...)
}

// ── masterdaweb_firewall_macros ─────────────────────────────────────────────

// NewFirewallMacrosDataSource is masterdaweb_firewall_macros.
func NewFirewallMacrosDataSource() datasource.DataSource { return &firewallMacrosDataSource{} }

type firewallMacrosDataSource struct{ client *client.Client }

type firewallMacroData struct {
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

type firewallMacrosData struct {
	ID     types.String        `tfsdk:"id"`
	Macros []firewallMacroData `tfsdk:"macros"`
}

func (d *firewallMacrosDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_macros"
}

func (d *firewallMacrosDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The named services a firewall rule can name instead of a protocol and ports (macro), like SSH or HTTPS.",
		Attributes: map[string]schema.Attribute{
			"id": dsComputed("Always firewall-macros."),
			"macros": schema.ListNestedAttribute{
				Computed: true, Description: "The macros.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":        dsComputed("The macro's name (masterdaweb_firewall rules[].macro)."),
					"description": dsComputed("What traffic it covers."),
				}},
			},
		},
	}
}

func (d *firewallMacrosDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *firewallMacrosDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	all, err := d.client.ListFirewallMacros(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the firewall macros", err.Error())
		return
	}
	out := firewallMacrosData{ID: types.StringValue("firewall-macros"), Macros: []firewallMacroData{}}
	for _, m := range all {
		out.Macros = append(out.Macros, firewallMacroData{Name: types.StringValue(m.Name), Description: stringValueOrNull(m.Description)})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}

// ── masterdaweb_dns_zone ────────────────────────────────────────────────────

// NewDNSZoneDataSource is masterdaweb_dns_zone.
func NewDNSZoneDataSource() datasource.DataSource { return &dnsZoneDataSource{} }

type dnsZoneDataSource struct{ client *client.Client }

func (d *dnsZoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (d *dnsZoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a DNS zone by its id or its name (the domain), for masterdaweb_dns_record.zone_id.",
		Attributes: map[string]schema.Attribute{
			"id":   dsLookup("The zone's id. Give the id or the name."),
			"name": dsLookup("The domain, like example.com. Give the id or the name."),
			"name_servers": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "The platform's nameservers: the NS records to set for the domain at its registrar.",
			},
			"created_at": dsComputed("When the zone was created (RFC 3339)."),
		},
	}
}

func (d *dnsZoneDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return lookupBy("id", "name")
}

func (d *dnsZoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *dnsZoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg dnsZoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var z *client.DNSZone
	if known(cfg.ID) {
		got, err := d.client.GetDNSZone(ctx, cfg.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Could not read the DNS zone", err.Error())
			return
		}
		z = got
	} else {
		all, err := d.client.ListDNSZones(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the DNS zones", err.Error())
			return
		}
		for i := range all {
			if normalizeHostname(all[i].Name) == normalizeHostname(cfg.Name.ValueString()) {
				z = &all[i]
			}
		}
		if z == nil {
			resp.Diagnostics.AddError("No such DNS zone", fmt.Sprintf("The account hosts no zone %q.", cfg.Name.ValueString()))
			return
		}
	}
	m := dnsZoneModel{
		ID: types.StringValue(z.ID), Name: types.StringValue(z.Name), NameServers: types.ListNull(types.StringType),
		CreatedAt: stringOrNull(z.CreatedAt),
	}
	m.NameServers = zoneNameServers(ctx, d.client, m)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
