package provider

import (
	"context"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// The vocabularies of the Security Center (GET /v1/security/findings).
var (
	securitySeverities    = []string{"critical", "high", "medium", "low", "info"}
	securityStatuses      = []string{"open", "resolved", "excepted", "false_positive"}
	securityResourceTypes = []string{"instance", "serverless_service", "database", "bucket", "storage_key", "account"}
	securityCategories    = []string{"EXPOSED_SERVICE", "AMPLIFICATION_VECTOR", "VULNERABLE_SOFTWARE", "WEAK_ENCRYPTION",
		"CERTIFICATE", "MISCONFIGURATION", "INFORMATION_DISCLOSURE", "DEFAULT_CREDENTIALS", "ACCESS_CONTROL", "NETWORK_POLICY"}
)

// ── masterdaweb_security_overview ──────────────────────────────────────────

type severityCountsData struct {
	Critical types.Int64 `tfsdk:"critical"`
	High     types.Int64 `tfsdk:"high"`
	Medium   types.Int64 `tfsdk:"medium"`
	Low      types.Int64 `tfsdk:"low"`
	Info     types.Int64 `tfsdk:"info"`
}

type securityEngineData struct {
	Engine       types.String       `tfsdk:"engine"`
	Score        types.Int64        `tfsdk:"score"`
	Grade        types.String       `tfsdk:"grade"`
	LastScanAt   types.String       `tfsdk:"last_scan_at"`
	OpenFindings severityCountsData `tfsdk:"open_findings"`
}

type securityInstancesData struct {
	Total             types.Int64 `tfsdk:"total"`
	Assessed          types.Int64 `tfsdk:"assessed"`
	AwaitingFirstScan types.Int64 `tfsdk:"awaiting_first_scan"`
	Excluded          types.Int64 `tfsdk:"excluded"`
	OptedOut          types.Int64 `tfsdk:"opted_out"`
	ExcludedReasons   types.Map   `tfsdk:"excluded_reasons"`
}

type securityResourceCoverageData struct {
	Type       types.String `tfsdk:"type"`
	Total      types.Int64  `tfsdk:"total"`
	Checked    types.Int64  `tfsdk:"checked"`
	ImageScans types.Map    `tfsdk:"image_scans"`
}

type securityResourceScoreData struct {
	Type         types.String `tfsdk:"type"`
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Score        types.Int64  `tfsdk:"score"`
	Grade        types.String `tfsdk:"grade"`
	OpenCritical types.Int64  `tfsdk:"open_critical"`
	OpenHigh     types.Int64  `tfsdk:"open_high"`
}

type securityOverviewData struct {
	ID                types.String                   `tfsdk:"id"`
	Score             types.Int64                    `tfsdk:"score"`
	Grade             types.String                   `tfsdk:"grade"`
	LastScanAt        types.String                   `tfsdk:"last_scan_at"`
	ScanningAvailable types.Bool                     `tfsdk:"scanning_available"`
	OpenFindings      severityCountsData             `tfsdk:"open_findings"`
	Engines           []securityEngineData           `tfsdk:"engines"`
	Instances         securityInstancesData          `tfsdk:"instances"`
	Resources         []securityResourceCoverageData `tfsdk:"resources"`
	WorstInstances    []securityResourceScoreData    `tfsdk:"worst_instances"`
	WorstResources    []securityResourceScoreData    `tfsdk:"worst_resources"`
}

func computedInt64(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, Description: desc}
}

func severityCountsAttribute(desc string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Computed: true, Description: desc,
		Attributes: map[string]schema.Attribute{
			"critical": computedInt64("Critical."),
			"high":     computedInt64("High."),
			"medium":   computedInt64("Medium."),
			"low":      computedInt64("Low."),
			"info":     computedInt64("Informational."),
		},
	}
}

func resourceScoreAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"type":          dsComputed("instance, serverless_service, database, bucket, storage_key or account."),
		"id":            dsComputed("Its id (an instance's id for an instance)."),
		"name":          dsComputed("Its name."),
		"score":         computedInt64("Its score, 0–100; null before its first check."),
		"grade":         dsComputed("Its grade, A to F."),
		"open_critical": computedInt64("Its open critical findings."),
		"open_high":     computedInt64("Its open high findings."),
	}
}

func severityCountsFromAPI(c client.SeverityCounts) severityCountsData {
	return severityCountsData{
		Critical: types.Int64Value(c.Critical), High: types.Int64Value(c.High), Medium: types.Int64Value(c.Medium),
		Low: types.Int64Value(c.Low), Info: types.Int64Value(c.Info),
	}
}

// countsMap is a map of counts, null when the API sends none.
func countsMap(m map[string]int64) types.Map {
	if m == nil {
		return types.MapNull(types.Int64Type)
	}
	vals := make(map[string]attr.Value, len(m))
	for k, v := range m {
		vals[k] = types.Int64Value(v)
	}
	return types.MapValueMust(types.Int64Type, vals)
}

func resourceScoresFromAPI(items []client.SecurityResourceScore) []securityResourceScoreData {
	out := []securityResourceScoreData{}
	for _, r := range items {
		out = append(out, securityResourceScoreData{
			Type: types.StringValue(r.Type), ID: types.StringValue(r.ID), Name: stringOrNull(r.Name),
			Score: int64OrNull(r.Score), Grade: stringOrNull(r.Grade),
			OpenCritical: types.Int64Value(r.OpenCritical), OpenHigh: types.Int64Value(r.OpenHigh),
		})
	}
	return out
}

// NewSecurityOverviewDataSource is masterdaweb_security_overview.
func NewSecurityOverviewDataSource() datasource.DataSource { return &securityOverviewDataSource{} }

type securityOverviewDataSource struct{ client *client.Client }

func (d *securityOverviewDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_overview"
}

func (d *securityOverviewDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The account's security score in the Security Center and what it is made of: open findings by " +
			"severity, the engines behind the score, how much the scans reach, and the worst instances and resources.",
		Attributes: map[string]schema.Attribute{
			"id":                 dsComputed("Always security_overview."),
			"score":              computedInt64("The account's score, 0–100; null before the first check."),
			"grade":              dsComputed("The account's grade, A to F."),
			"last_scan_at":       dsComputed("When anything was last checked (RFC 3339)."),
			"scanning_available": schema.BoolAttribute{Computed: true, Description: "False while the platform has scanning paused: the score is not being refreshed."},
			"open_findings":      severityCountsAttribute("What still counts, by severity: neither resolved nor excepted."),
			"engines": schema.ListNestedAttribute{
				Computed: true, Description: "The parts the score is made of.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"engine":        dsComputed("nmap (exposure), sslyze (TLS), nuclei and openvas (vulnerabilities), posture (configuration checks) or image (container images)."),
					"score":         computedInt64("Its score, 0–100; null until it has run."),
					"grade":         dsComputed("Its grade, A to F."),
					"last_scan_at":  dsComputed("When it last ran (RFC 3339)."),
					"open_findings": severityCountsAttribute("Its open findings, by severity."),
				}},
			},
			"instances": schema.SingleNestedAttribute{
				Computed: true, Description: "How many instances the scans reach.",
				Attributes: map[string]schema.Attribute{
					"total":               computedInt64("Every instance."),
					"assessed":            computedInt64("Scanned at least once."),
					"awaiting_first_scan": computedInt64("Not scanned yet."),
					"excluded":            computedInt64("Not eligible (no public address, for one)."),
					"opted_out":           computedInt64("With scanning turned off."),
					"excluded_reasons": schema.MapAttribute{
						Computed: true, ElementType: types.Int64Type, Description: "The excluded ones, by reason.",
					},
				},
			},
			"resources": schema.ListNestedAttribute{
				Computed: true, Description: "Every other kind of resource: how many there are and how many the checks looked at.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"type":    dsComputed("serverless_service, database, bucket, storage_key or account."),
					"total":   computedInt64("How many there are."),
					"checked": computedInt64("How many the configuration checks have looked at."),
					"image_scans": schema.MapAttribute{
						Computed: true, ElementType: types.Int64Type,
						Description: "Serverless services: their container image scans, by state.",
					},
				}},
			},
			"worst_instances": schema.ListNestedAttribute{
				Computed: true, Description: "Up to five instances, worst first.",
				NestedObject: schema.NestedAttributeObject{Attributes: resourceScoreAttributes()},
			},
			"worst_resources": schema.ListNestedAttribute{
				Computed: true, Description: "Up to five other resources, worst first.",
				NestedObject: schema.NestedAttributeObject{Attributes: resourceScoreAttributes()},
			},
		},
	}
}

func (d *securityOverviewDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *securityOverviewDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	o, err := d.client.GetSecurityOverview(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the security overview", err.Error())
		return
	}
	out := securityOverviewData{
		ID: types.StringValue("security_overview"), Score: int64OrNull(o.Score), Grade: stringOrNull(o.Grade),
		LastScanAt: stringOrNull(o.LastScanAt), ScanningAvailable: types.BoolValue(o.ScanningAvailable),
		OpenFindings: severityCountsFromAPI(o.OpenFindings), Engines: []securityEngineData{},
		Instances: securityInstancesData{
			Total: types.Int64Value(o.Instances.Total), Assessed: types.Int64Value(o.Instances.Assessed),
			AwaitingFirstScan: types.Int64Value(o.Instances.AwaitingFirstScan), Excluded: types.Int64Value(o.Instances.Excluded),
			OptedOut: types.Int64Value(o.Instances.OptedOut), ExcludedReasons: countsMap(orEmpty(o.Instances.ExcludedReasons)),
		},
		Resources:      []securityResourceCoverageData{},
		WorstInstances: resourceScoresFromAPI(o.WorstInstances),
		WorstResources: resourceScoresFromAPI(o.WorstResources),
	}
	for _, e := range o.Engines {
		out.Engines = append(out.Engines, securityEngineData{
			Engine: types.StringValue(e.Engine), Score: int64OrNull(e.Score), Grade: stringOrNull(e.Grade),
			LastScanAt: stringOrNull(e.LastScanAt), OpenFindings: severityCountsFromAPI(e.OpenFindings),
		})
	}
	for _, r := range o.Resources {
		out.Resources = append(out.Resources, securityResourceCoverageData{
			Type: types.StringValue(r.Type), Total: types.Int64Value(r.Total), Checked: types.Int64Value(r.Checked),
			ImageScans: countsMap(r.ImageScans),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}

// orEmpty is a map that is never nil.
func orEmpty(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

// ── masterdaweb_security_findings ──────────────────────────────────────────

type securityFindingData struct {
	ID              types.String `tfsdk:"id"`
	ResourceType    types.String `tfsdk:"resource_type"`
	ResourceID      types.String `tfsdk:"resource_id"`
	ResourceName    types.String `tfsdk:"resource_name"`
	RuleID          types.String `tfsdk:"rule_id"`
	Category        types.String `tfsdk:"category"`
	Severity        types.String `tfsdk:"severity"`
	Status          types.String `tfsdk:"status"`
	RiskScore       types.Int64  `tfsdk:"risk_score"`
	KnownExploited  types.Bool   `tfsdk:"known_exploited"`
	CVEIDs          types.List   `tfsdk:"cve_ids"`
	Port            types.Int64  `tfsdk:"port"`
	Protocol        types.String `tfsdk:"protocol"`
	Title           types.String `tfsdk:"title"`
	Description     types.String `tfsdk:"description"`
	Impact          types.String `tfsdk:"impact"`
	Recommendation  types.String `tfsdk:"recommendation"`
	FirstSeenAt     types.String `tfsdk:"first_seen_at"`
	LastSeenAt      types.String `tfsdk:"last_seen_at"`
	LastVerifiedAt  types.String `tfsdk:"last_verified_at"`
	ResolvedAt      types.String `tfsdk:"resolved_at"`
	OccurrenceCount types.Int64  `tfsdk:"occurrence_count"`
}

type securityFindingsData struct {
	ID           types.String          `tfsdk:"id"`
	Severity     types.String          `tfsdk:"severity"`
	Status       types.String          `tfsdk:"status"`
	ResourceType types.String          `tfsdk:"resource_type"`
	ResourceID   types.String          `tfsdk:"resource_id"`
	InstanceID   types.String          `tfsdk:"instance_id"`
	Category     types.String          `tfsdk:"category"`
	Search       types.String          `tfsdk:"search"`
	Findings     []securityFindingData `tfsdk:"findings"`
}

var _ datasource.DataSourceWithValidateConfig = (*securityFindingsDataSource)(nil)

// NewSecurityFindingsDataSource is masterdaweb_security_findings.
func NewSecurityFindingsDataSource() datasource.DataSource { return &securityFindingsDataSource{} }

type securityFindingsDataSource struct{ client *client.Client }

func (d *securityFindingsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_findings"
}

func (d *securityFindingsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	filter := func(desc string, values []string) schema.StringAttribute {
		a := schema.StringAttribute{Optional: true, Description: desc}
		if values != nil {
			a.Validators = []validator.String{stringvalidator.OneOf(values...)}
		}
		return a
	}
	resp.Schema = schema.Schema{
		Description: "The Security Center's findings, riskiest first — severity, exploitability and exposure " +
			"together —, across instances, serverless services, databases, buckets, access keys and the account. " +
			"By default only what still counts (not resolved, not excepted); the filters narrow the list.",
		Attributes: map[string]schema.Attribute{
			"id":            dsComputed("Always security_findings."),
			"severity":      filter("Only this severity: critical, high, medium, low or info.", securitySeverities),
			"status":        filter("Only this status: open, resolved, excepted or false_positive. Every status but resolved and excepted when left out.", securityStatuses),
			"resource_type": filter("Only this kind of resource: instance, serverless_service, database, bucket, storage_key or account.", securityResourceTypes),
			"resource_id":   filter("With a resource_type other than instance: only that resource.", nil),
			"instance_id":   filter("Only this instance's findings.", nil),
			"category":      filter("Only this category, like EXPOSED_SERVICE or VULNERABLE_SOFTWARE.", securityCategories),
			"search":        filter("Only the findings with this text in their title, check id, address or resource name.", nil),
			"findings": schema.ListNestedAttribute{
				Computed: true, Description: "The findings.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":               dsComputed("The finding's id (for masterdaweb_security_exception's finding_id)."),
					"resource_type":    dsComputed("The kind of resource it is about."),
					"resource_id":      dsComputed("That resource's id."),
					"resource_name":    dsComputed("That resource's name."),
					"rule_id":          dsComputed("The check that raised it, the same across resources (for an exception of scope rule)."),
					"category":         dsComputed("Its category."),
					"severity":         dsComputed("critical, high, medium, low or info."),
					"status":           dsComputed("open, resolved, excepted or false_positive."),
					"risk_score":       computedInt64("How it ranks: severity, exploitability and exposure together."),
					"known_exploited":  schema.BoolAttribute{Computed: true, Description: "Whether it is listed as actively exploited (CISA KEV)."},
					"cve_ids":          schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "The CVEs it is about."},
					"port":             computedInt64("The port, when it is about one."),
					"protocol":         dsComputed("The port's protocol."),
					"title":            dsComputed("What was found."),
					"description":      dsComputed("What it means."),
					"impact":           dsComputed("Why it matters."),
					"recommendation":   dsComputed("What to do about it."),
					"first_seen_at":    dsComputed("When it was first seen (RFC 3339)."),
					"last_seen_at":     dsComputed("When it was last seen (RFC 3339)."),
					"last_verified_at": dsComputed("When a check last confirmed it (RFC 3339)."),
					"resolved_at":      dsComputed("When it was resolved (RFC 3339)."),
					"occurrence_count": computedInt64("How many times it was seen."),
				}},
			},
		},
	}
}

func (d *securityFindingsDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var resourceType, resourceID types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("resource_type"), &resourceType)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("resource_id"), &resourceID)...)
	if resourceID.IsNull() || resourceType.IsUnknown() {
		return
	}
	if resourceType.IsNull() || resourceType.ValueString() == "instance" {
		resp.Diagnostics.AddAttributeError(path.Root("resource_id"), "resource_id needs its kind",
			"resource_id goes with a resource_type other than instance; for an instance, use instance_id.")
	}
}

func (d *securityFindingsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *securityFindingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg securityFindingsData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	q := url.Values{}
	for name, v := range map[string]types.String{
		"severity": cfg.Severity, "status": cfg.Status, "resource_type": cfg.ResourceType,
		"resource_id": cfg.ResourceID, "instance_id": cfg.InstanceID, "category": cfg.Category, "search": cfg.Search,
	} {
		if known(v) && v.ValueString() != "" {
			q.Set(name, v.ValueString())
		}
	}
	all, err := d.client.ListSecurityFindings(ctx, q)
	if err != nil {
		resp.Diagnostics.AddError("Could not list the security findings", err.Error())
		return
	}
	out := cfg
	out.ID = types.StringValue("security_findings")
	out.Findings = []securityFindingData{}
	for _, f := range all {
		cves := f.CVEIDs
		if cves == nil {
			cves = []string{}
		}
		out.Findings = append(out.Findings, securityFindingData{
			ID: types.StringValue(f.ID), ResourceType: types.StringValue(f.Resource.Type),
			ResourceID: types.StringValue(f.Resource.ID), ResourceName: types.StringValue(f.Resource.Name),
			RuleID: types.StringValue(f.RuleID), Category: types.StringValue(f.Category), Severity: types.StringValue(f.Severity),
			Status: types.StringValue(f.Status), RiskScore: types.Int64Value(f.RiskScore),
			KnownExploited: types.BoolValue(f.KnownExploited), CVEIDs: stringList(cves), Port: int64OrNull(f.Port),
			Protocol: stringOrNull(f.Protocol), Title: types.StringValue(f.Title), Description: types.StringValue(f.Description),
			Impact: types.StringValue(f.Impact), Recommendation: types.StringValue(f.Recommendation),
			FirstSeenAt: types.StringValue(f.FirstSeenAt), LastSeenAt: types.StringValue(f.LastSeenAt),
			LastVerifiedAt: stringOrNull(f.LastVerifiedAt), ResolvedAt: stringOrNull(f.ResolvedAt),
			OccurrenceCount: types.Int64Value(f.OccurrenceCount),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, out)...)
}
