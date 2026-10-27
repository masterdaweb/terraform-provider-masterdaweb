package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                = (*bucketResource)(nil)
	_ resource.ResourceWithConfigure   = (*bucketResource)(nil)
	_ resource.ResourceWithImportState = (*bucketResource)(nil)
)

// NewBucketResource is masterdaweb_object_storage_bucket.
func NewBucketResource() resource.Resource { return &bucketResource{} }

type bucketResource struct{ client *client.Client }

type bucketModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Public             types.Bool   `tfsdk:"public"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
	QuotaBytes         types.Int64  `tfsdk:"quota_bytes"`
	QuotaMode          types.String `tfsdk:"quota_mode"`
	Cors               types.List   `tfsdk:"cors"`
	LifecycleRules     types.List   `tfsdk:"lifecycle_rules"`
	AbortMultipartDays types.Int64  `tfsdk:"abort_multipart_days"`
	Location           types.String `tfsdk:"location"`
	PublicURL          types.String `tfsdk:"public_url"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

type corsModel struct {
	AllowedOrigins types.List  `tfsdk:"allowed_origins"`
	AllowedMethods types.List  `tfsdk:"allowed_methods"`
	AllowedHeaders types.List  `tfsdk:"allowed_headers"`
	ExposeHeaders  types.List  `tfsdk:"expose_headers"`
	MaxAgeSeconds  types.Int64 `tfsdk:"max_age_seconds"`
}

type lifecycleModel struct {
	ID         types.String `tfsdk:"id"`
	Prefix     types.String `tfsdk:"prefix"`
	ExpireDays types.Int64  `tfsdk:"expire_days"`
	Enabled    types.Bool   `tfsdk:"enabled"`
}

var corsAttrTypes = map[string]attr.Type{
	"allowed_origins": types.ListType{ElemType: types.StringType},
	"allowed_methods": types.ListType{ElemType: types.StringType},
	"allowed_headers": types.ListType{ElemType: types.StringType},
	"expose_headers":  types.ListType{ElemType: types.StringType},
	"max_age_seconds": types.Int64Type,
}

var lifecycleAttrTypes = map[string]attr.Type{
	"id":          types.StringType,
	"prefix":      types.StringType,
	"expire_days": types.Int64Type,
	"enabled":     types.BoolType,
}

var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func emptyStringList() basetypes.ListValue {
	return types.ListValueMust(types.StringType, []attr.Value{})
}

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_object_storage_bucket"
}

func (r *bucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An Object Storage bucket, read and written with any S3 tool through the endpoint " +
			"`mw storage info` names, or bound to a service. Private and protected from deletion unless said " +
			"otherwise. A deleted bucket can be restored for a few days, and keeps its name until then.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The bucket's id."),
			"name": schema.StringAttribute{
				Required: true,
				Description: "The bucket's name: 3 to 63 lowercase letters, digits and single hyphens, unique on the " +
					"whole platform. Changing it replaces the bucket.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(bucketNameRe, "must be 3 to 63 lowercase letters, digits and hyphens, starting and ending with a letter or digit"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"public": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether anyone may read its files (never write them), at public_url. Making an existing " +
					"bucket public takes a token with storage:delete, of an administrator.",
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the bucket is protected from deletion. On by default: to destroy it, set it to false " +
					"and apply first.",
			},
			"quota_bytes": schema.Int64Attribute{
				Optional:    true,
				Description: "A size limit in bytes, from 1 MB (1000000) up. None when left out.",
				Validators:  []validator.Int64{int64validator.Between(1_000_000, 1_000_000_000_000_000)},
			},
			"quota_mode": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("alert"),
				Description: "What reaching the size limit does: alert (an e-mail, the default) or block (uploads refused).",
				Validators:  []validator.String{stringvalidator.OneOf("alert", "block")},
			},
			"cors": schema.ListNestedAttribute{
				Optional: true, Computed: true,
				Default:     listdefault.StaticValue(types.ListValueMust(types.ObjectType{AttrTypes: corsAttrTypes}, []attr.Value{})),
				Description: "CORS rules, for browsers that read or upload files from another site.",
				Validators:  []validator.List{listvalidator.SizeAtMost(100)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"allowed_origins": schema.ListAttribute{
							Required: true, ElementType: types.StringType,
							Description: "Sites allowed, like https://shop.example.com (one * wildcard at most).",
							Validators:  []validator.List{listvalidator.SizeBetween(1, 100)},
						},
						"allowed_methods": schema.ListAttribute{
							Required: true, ElementType: types.StringType,
							Description: "GET, PUT, POST, DELETE or HEAD.",
							Validators: []validator.List{
								listvalidator.SizeBetween(1, 5),
								listvalidator.ValueStringsAre(stringvalidator.OneOf("GET", "PUT", "POST", "DELETE", "HEAD")),
							},
						},
						"allowed_headers": schema.ListAttribute{
							Optional: true, Computed: true, ElementType: types.StringType,
							Default:     listdefault.StaticValue(emptyStringList()),
							Description: "Request headers allowed, like content-type.",
						},
						"expose_headers": schema.ListAttribute{
							Optional: true, Computed: true, ElementType: types.StringType,
							Default:     listdefault.StaticValue(emptyStringList()),
							Description: "Response headers the browser may read, like ETag.",
						},
						"max_age_seconds": schema.Int64Attribute{
							Optional:    true,
							Description: "How long a browser may keep the answer to its preflight request, up to a week.",
							Validators:  []validator.Int64{int64validator.Between(0, 604800)},
						},
					},
				},
			},
			"lifecycle_rules": schema.ListNestedAttribute{
				Optional: true, Computed: true,
				Default:     listdefault.StaticValue(types.ListValueMust(types.ObjectType{AttrTypes: lifecycleAttrTypes}, []attr.Value{})),
				Description: "Rules that delete files some days after they were written.",
				Validators:  []validator.List{listvalidator.SizeAtMost(50)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Required:    true,
							Description: "The rule's name: up to 64 letters, digits, dots, dashes or underscores, unique in the bucket.",
							Validators: []validator.String{
								stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`), "must be up to 64 letters, digits, dots, dashes or underscores"),
							},
						},
						"prefix": schema.StringAttribute{
							Optional: true, Computed: true, Default: stringdefault.StaticString(""),
							Description: "Files whose names start with it, like tmp/. Empty: every file (an expiry of the whole " +
								"bucket takes a token with storage:delete, of an administrator).",
							Validators: []validator.String{stringvalidator.LengthAtMost(1024)},
						},
						"expire_days": schema.Int64Attribute{
							Required:    true,
							Description: "Days after which a file is deleted, from 1 to 36500.",
							Validators:  []validator.Int64{int64validator.Between(1, 36500)},
						},
						"enabled": schema.BoolAttribute{
							Optional: true, Computed: true, Default: booldefault.StaticBool(true),
							Description: "Whether the rule applies.",
						},
					},
				},
			},
			"abort_multipart_days": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(7),
				Description: "Unfinished uploads are discarded after this many days (7 by default). 0: never.",
				Validators:  []validator.Int64{int64validator.Between(0, 365)},
			},
			"location":   computedString("Where the bucket keeps its data."),
			"public_url": schema.StringAttribute{Computed: true, Description: "Where a public bucket serves its files."},
			"created_at": computedString("When the bucket was created (RFC 3339)."),
		},
	}
}

func (r *bucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// ── Rules, both ways ───────────────────────────────────────────────────────

func trimmed(items []string) []string {
	out := []string{}
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// normalCors is a rule as the API keeps it: texts trimmed, empty ones
// dropped, methods in capitals once each.
func normalCors(c client.CorsRule) client.CorsRule {
	var methods []string
	seen := map[string]bool{}
	for _, m := range trimmed(c.AllowedMethods) {
		m = strings.ToUpper(m)
		if !seen[m] {
			seen[m] = true
			methods = append(methods, m)
		}
	}
	return client.CorsRule{
		AllowedOrigins: trimmed(c.AllowedOrigins),
		AllowedMethods: methods,
		AllowedHeaders: trimmed(c.AllowedHeaders),
		ExposeHeaders:  trimmed(c.ExposeHeaders),
		MaxAgeSeconds:  c.MaxAgeSeconds,
	}
}

func sameCors(a, b []client.CorsRule) bool {
	if len(a) != len(b) {
		return false
	}
	eq := func(x, y []string) bool { return strings.Join(x, "\x00") == strings.Join(y, "\x00") }
	for i := range a {
		x, y := normalCors(a[i]), normalCors(b[i])
		if !eq(x.AllowedOrigins, y.AllowedOrigins) || !eq(x.AllowedMethods, y.AllowedMethods) ||
			!eq(x.AllowedHeaders, y.AllowedHeaders) || !eq(x.ExposeHeaders, y.ExposeHeaders) {
			return false
		}
		if (x.MaxAgeSeconds == nil) != (y.MaxAgeSeconds == nil) ||
			(x.MaxAgeSeconds != nil && *x.MaxAgeSeconds != *y.MaxAgeSeconds) {
			return false
		}
	}
	return true
}

func corsFromList(ctx context.Context, l types.List, diags *diag.Diagnostics) ([]client.CorsRule, bool) {
	if !known(l) {
		return nil, false
	}
	var rules []corsModel
	diags.Append(l.ElementsAs(ctx, &rules, false)...)
	out := []client.CorsRule{}
	for _, r := range rules {
		c := client.CorsRule{
			AllowedOrigins: listStrings(ctx, r.AllowedOrigins),
			AllowedMethods: listStrings(ctx, r.AllowedMethods),
			AllowedHeaders: listStrings(ctx, r.AllowedHeaders),
			ExposeHeaders:  listStrings(ctx, r.ExposeHeaders),
		}
		if known(r.MaxAgeSeconds) {
			n := r.MaxAgeSeconds.ValueInt64()
			c.MaxAgeSeconds = &n
		}
		for _, l := range []*[]string{&c.AllowedOrigins, &c.AllowedMethods, &c.AllowedHeaders, &c.ExposeHeaders} {
			if *l == nil {
				*l = []string{}
			}
		}
		out = append(out, c)
	}
	return out, true
}

func corsToList(rules []client.CorsRule) types.List {
	vals := make([]attr.Value, 0, len(rules))
	for _, c := range rules {
		vals = append(vals, types.ObjectValueMust(corsAttrTypes, map[string]attr.Value{
			"allowed_origins": stringList(c.AllowedOrigins),
			"allowed_methods": stringList(c.AllowedMethods),
			"allowed_headers": stringList(c.AllowedHeaders),
			"expose_headers":  stringList(c.ExposeHeaders),
			"max_age_seconds": int64OrNull(c.MaxAgeSeconds),
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: corsAttrTypes}, vals)
}

func lifecycleFromList(ctx context.Context, l types.List, diags *diag.Diagnostics) ([]client.LifecycleRule, bool) {
	if !known(l) {
		return nil, false
	}
	var rules []lifecycleModel
	diags.Append(l.ElementsAs(ctx, &rules, false)...)
	out := []client.LifecycleRule{}
	for _, r := range rules {
		out = append(out, client.LifecycleRule{
			ID:         strings.TrimSpace(r.ID.ValueString()),
			Prefix:     r.Prefix.ValueString(),
			ExpireDays: r.ExpireDays.ValueInt64(),
			Enabled:    r.Enabled.IsNull() || r.Enabled.ValueBool(),
		})
	}
	return out, true
}

func lifecycleToList(rules []client.LifecycleRule) types.List {
	vals := make([]attr.Value, 0, len(rules))
	for _, r := range rules {
		vals = append(vals, types.ObjectValueMust(lifecycleAttrTypes, map[string]attr.Value{
			"id":          types.StringValue(r.ID),
			"prefix":      types.StringValue(r.Prefix),
			"expire_days": types.Int64Value(r.ExpireDays),
			"enabled":     types.BoolValue(r.Enabled),
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: lifecycleAttrTypes}, vals)
}

func sameLifecycle(a, b []client.LifecycleRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i].ID) != strings.TrimSpace(b[i].ID) || a[i].Prefix != b[i].Prefix ||
			a[i].ExpireDays != b[i].ExpireDays || a[i].Enabled != b[i].Enabled {
			return false
		}
	}
	return true
}

func bucketFromAPI(ctx context.Context, prior bucketModel, b *client.Bucket, diags *diag.Diagnostics) bucketModel {
	m := bucketModel{
		ID:                 types.StringValue(b.ID),
		Name:               types.StringValue(b.Name),
		Public:             types.BoolValue(b.Public),
		DeletionProtection: types.BoolValue(b.DeletionProtection),
		QuotaBytes:         types.Int64Null(),
		QuotaMode:          types.StringValue(b.QuotaMode),
		Cors:               corsToList(b.Cors),
		LifecycleRules:     lifecycleToList(b.Lifecycle),
		AbortMultipartDays: types.Int64Value(0),
		Location:           types.StringValue(b.Location),
		PublicURL:          stringOrNull(b.PublicURL),
		CreatedAt:          types.StringValue(b.CreatedAt),
	}
	if b.QuotaBytes != nil {
		if n, err := strconv.ParseInt(*b.QuotaBytes, 10, 64); err == nil {
			m.QuotaBytes = types.Int64Value(n)
		}
	}
	if b.AbortMultipartDays != nil {
		m.AbortMultipartDays = types.Int64Value(*b.AbortMultipartDays)
	}
	// The configuration's spelling of a rule stays while it means the same.
	if prev, ok := corsFromList(ctx, prior.Cors, diags); ok && sameCors(prev, b.Cors) {
		m.Cors = prior.Cors
	}
	if prev, ok := lifecycleFromList(ctx, prior.LifecycleRules, diags); ok && sameLifecycle(prev, b.Lifecycle) {
		m.LifecycleRules = prior.LifecycleRules
	}
	return m
}

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"name":                plan.Name.ValueString(),
		"public":              plan.Public.ValueBool(),
		"deletion_protection": plan.DeletionProtection.ValueBool(),
		"quota_mode":          plan.QuotaMode.ValueString(),
	}
	if known(plan.QuotaBytes) {
		body["quota_bytes"] = strconv.FormatInt(plan.QuotaBytes.ValueInt64(), 10)
	}
	if cors, ok := corsFromList(ctx, plan.Cors, &resp.Diagnostics); ok && len(cors) > 0 {
		body["cors"] = cors
	}
	if rules, ok := lifecycleFromList(ctx, plan.LifecycleRules, &resp.Diagnostics); ok && len(rules) > 0 {
		body["lifecycle"] = rules
	}
	if known(plan.AbortMultipartDays) {
		if n := plan.AbortMultipartDays.ValueInt64(); n > 0 {
			body["abort_multipart_days"] = n
		} else {
			body["abort_multipart_days"] = nil
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.CreateBucket(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not create the bucket", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, bucketFromAPI(ctx, plan, b, &resp.Diagnostics))...)
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.GetBucket(ctx, state.Name.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the bucket", err.Error())
		return
	}
	// Deleted (restorable for a few days), or the name went to another bucket.
	if b.DeletedAt != nil || b.Status == "deleting" || (known(state.ID) && state.ID.ValueString() != b.ID) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, bucketFromAPI(ctx, state, b, &resp.Diagnostics))...)
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := map[string]any{}
	if !plan.Public.Equal(state.Public) {
		patch["public"] = plan.Public.ValueBool()
	}
	if !plan.DeletionProtection.Equal(state.DeletionProtection) {
		patch["deletion_protection"] = plan.DeletionProtection.ValueBool()
	}
	if !plan.QuotaBytes.Equal(state.QuotaBytes) {
		if known(plan.QuotaBytes) {
			patch["quota_bytes"] = strconv.FormatInt(plan.QuotaBytes.ValueInt64(), 10)
		} else {
			patch["quota_bytes"] = nil
		}
	}
	if !plan.QuotaMode.Equal(state.QuotaMode) {
		patch["quota_mode"] = plan.QuotaMode.ValueString()
	}
	if !plan.Cors.Equal(state.Cors) {
		if cors, ok := corsFromList(ctx, plan.Cors, &resp.Diagnostics); ok {
			patch["cors"] = cors
		}
	}
	if !plan.LifecycleRules.Equal(state.LifecycleRules) {
		if rules, ok := lifecycleFromList(ctx, plan.LifecycleRules, &resp.Diagnostics); ok {
			patch["lifecycle"] = rules
		}
	}
	if !plan.AbortMultipartDays.Equal(state.AbortMultipartDays) {
		if n := plan.AbortMultipartDays.ValueInt64(); n > 0 {
			patch["abort_multipart_days"] = n
		} else {
			patch["abort_multipart_days"] = nil
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()
	var b *client.Bucket
	var err error
	if len(patch) > 0 {
		b, err = r.client.UpdateBucket(ctx, name, patch)
	} else {
		b, err = r.client.GetBucket(ctx, name)
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not update the bucket", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, bucketFromAPI(ctx, plan, b, &resp.Diagnostics))...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()
	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Deletion protection is on",
			fmt.Sprintf("The bucket %s is protected from deletion. Set deletion_protection = false, run terraform apply, "+
				"and then destroy it. A deleted bucket can be restored for a few days.", name))
		return
	}
	if err := r.client.DeleteBucket(ctx, name); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not delete the bucket", err.Error())
	}
}

// ImportState takes the bucket's name (how the API addresses it) or its id.
func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := strings.TrimSpace(req.ID)
	name := ref
	if isUUID(ref) {
		all, err := r.client.ListBuckets(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the buckets", err.Error())
			return
		}
		name = ""
		for _, b := range all {
			if b.ID == ref && b.DeletedAt == nil {
				name = b.Name
			}
		}
		if name == "" {
			resp.Diagnostics.AddError("No such bucket", fmt.Sprintf("No bucket has the id %s.", ref))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}
