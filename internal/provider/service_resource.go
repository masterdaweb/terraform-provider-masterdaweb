package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ resource.Resource                     = (*serviceResource)(nil)
	_ resource.ResourceWithConfigure        = (*serviceResource)(nil)
	_ resource.ResourceWithImportState      = (*serviceResource)(nil)
	_ resource.ResourceWithValidateConfig   = (*serviceResource)(nil)
	_ resource.ResourceWithConfigValidators = (*serviceResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*serviceResource)(nil)
)

// NewServiceResource is masterdaweb_serverless_service.
func NewServiceResource() resource.Resource { return &serviceResource{} }

type serviceResource struct{ client *client.Client }

type serviceModel struct {
	ID                      types.String `tfsdk:"id"`
	ProjectID               types.String `tfsdk:"project_id"`
	Name                    types.String `tfsdk:"name"`
	Kind                    types.String `tfsdk:"kind"`
	Git                     types.Object `tfsdk:"git"`
	Image                   types.Object `tfsdk:"image"`
	Size                    types.String `tfsdk:"size"`
	Port                    types.Int64  `tfsdk:"port"`
	HealthCheckPath         types.String `tfsdk:"health_check_path"`
	StartCommand            types.String `tfsdk:"start_command"`
	BuildCommand            types.String `tfsdk:"build_command"`
	ReleaseCommand          types.String `tfsdk:"release_command"`
	RootDir                 types.String `tfsdk:"root_dir"`
	DockerfilePath          types.String `tfsdk:"dockerfile_path"`
	WatchPaths              types.List   `tfsdk:"watch_paths"`
	PHPVersion              types.String `tfsdk:"php_version"`
	PHPServer               types.String `tfsdk:"php_server"`
	PHPExtensions           types.List   `tfsdk:"php_extensions"`
	DocumentRoot            types.String `tfsdk:"document_root"`
	NodeVersion             types.String `tfsdk:"node_version"`
	SystemPackages          types.List   `tfsdk:"system_packages"`
	StartupTimeoutSeconds   types.Int64  `tfsdk:"startup_timeout_seconds"`
	MinInstances            types.Int64  `tfsdk:"min_instances"`
	MaxInstances            types.Int64  `tfsdk:"max_instances"`
	Concurrency             types.Int64  `tfsdk:"concurrency"`
	IdleTimeoutSeconds      types.Int64  `tfsdk:"idle_timeout_seconds"`
	RequestTimeoutSeconds   types.Int64  `tfsdk:"request_timeout_seconds"`
	GracefulShutdownSeconds types.Int64  `tfsdk:"graceful_shutdown_seconds"`
	RolloutPercent          types.Int64  `tfsdk:"rollout_percent"`
	RolloutMinutes          types.Int64  `tfsdk:"rollout_minutes"`
	Instances               types.Int64  `tfsdk:"instances"`
	CronSchedule            types.String `tfsdk:"cron_schedule"`
	CronTimezone            types.String `tfsdk:"cron_timezone"`
	CronTimeoutSeconds      types.Int64  `tfsdk:"cron_timeout_seconds"`
	AutoDeploy              types.Bool   `tfsdk:"auto_deploy"`
	WaitForCI               types.Bool   `tfsdk:"wait_for_ci"`
	DeletionProtection      types.Bool   `tfsdk:"deletion_protection"`
	Deploy                  types.Bool   `tfsdk:"deploy"`
	URL                     types.String `tfsdk:"url"`
	InternalURL             types.String `tfsdk:"internal_url"`
	Status                  types.String `tfsdk:"status"`
	VCPUMillis              types.Int64  `tfsdk:"vcpu_millis"`
	MemoryMiB               types.Int64  `tfsdk:"memory_mib"`
	RepositorySettings      types.List   `tfsdk:"repository_settings"`
	CreatedAt               types.String `tfsdk:"created_at"`
}

type gitModel struct {
	RepoURL      types.String `tfsdk:"repo_url"`
	Repo         types.String `tfsdk:"repo"`
	Provider     types.String `tfsdk:"provider"`
	ConnectionID types.String `tfsdk:"connection_id"`
	Branch       types.String `tfsdk:"branch"`
	TagPattern   types.String `tfsdk:"tag_pattern"`
}

type imageModel struct {
	Ref                  types.String `tfsdk:"ref"`
	RegistryCredentialID types.String `tfsdk:"registry_credential_id"`
}

var gitAttrTypes = map[string]attr.Type{
	"repo_url":      types.StringType,
	"repo":          types.StringType,
	"provider":      types.StringType,
	"connection_id": types.StringType,
	"branch":        types.StringType,
	"tag_pattern":   types.StringType,
}

var imageAttrTypes = map[string]attr.Type{
	"ref":                    types.StringType,
	"registry_credential_id": types.StringType,
}

var serviceNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// settingKind is how a setting travels.
type settingKind int

const (
	settingInt       settingKind = iota
	settingText                  // a text the API always has ("" allowed)
	settingOptional              // a text the API keeps as null when unset: "" in state
	settingList                  // a list of texts
	settingRollout               // rollout_percent: 0 and null both mean "all at once"
	settingSizeLabel             // size: a code from the sizes on offer
)

type settingSpec struct {
	name    string
	kind    settingKind
	build   bool // takes a new build to apply
	rollout bool // shapes how the next deployment takes traffic: never deploys by itself
}

// serviceSettings are the settings the API takes, by their API name (the
// attribute has the same name).
var serviceSettings = []settingSpec{
	{name: "size", kind: settingSizeLabel},
	{name: "port", kind: settingInt},
	{name: "health_check_path", kind: settingOptional},
	{name: "start_command", kind: settingOptional},
	{name: "build_command", kind: settingOptional, build: true},
	{name: "release_command", kind: settingOptional},
	{name: "root_dir", kind: settingText, build: true},
	{name: "dockerfile_path", kind: settingOptional, build: true},
	{name: "watch_paths", kind: settingList},
	{name: "php_version", kind: settingOptional, build: true},
	{name: "php_server", kind: settingOptional, build: true},
	{name: "php_extensions", kind: settingList, build: true},
	{name: "document_root", kind: settingOptional, build: true},
	{name: "node_version", kind: settingOptional, build: true},
	{name: "system_packages", kind: settingList, build: true},
	{name: "startup_timeout_seconds", kind: settingInt},
	{name: "min_instances", kind: settingInt},
	{name: "max_instances", kind: settingInt},
	{name: "concurrency", kind: settingInt},
	{name: "idle_timeout_seconds", kind: settingInt},
	{name: "request_timeout_seconds", kind: settingInt},
	{name: "graceful_shutdown_seconds", kind: settingInt},
	{name: "rollout_percent", kind: settingRollout, rollout: true},
	{name: "rollout_minutes", kind: settingInt, rollout: true},
	{name: "instances", kind: settingInt},
	{name: "cron_schedule", kind: settingOptional},
	{name: "cron_timezone", kind: settingText},
	{name: "cron_timeout_seconds", kind: settingInt},
}

func (m *serviceModel) setting(name string) attr.Value {
	switch name {
	case "size":
		return m.Size
	case "port":
		return m.Port
	case "health_check_path":
		return m.HealthCheckPath
	case "start_command":
		return m.StartCommand
	case "build_command":
		return m.BuildCommand
	case "release_command":
		return m.ReleaseCommand
	case "root_dir":
		return m.RootDir
	case "dockerfile_path":
		return m.DockerfilePath
	case "watch_paths":
		return m.WatchPaths
	case "php_version":
		return m.PHPVersion
	case "php_server":
		return m.PHPServer
	case "php_extensions":
		return m.PHPExtensions
	case "document_root":
		return m.DocumentRoot
	case "node_version":
		return m.NodeVersion
	case "system_packages":
		return m.SystemPackages
	case "startup_timeout_seconds":
		return m.StartupTimeoutSeconds
	case "min_instances":
		return m.MinInstances
	case "max_instances":
		return m.MaxInstances
	case "concurrency":
		return m.Concurrency
	case "idle_timeout_seconds":
		return m.IdleTimeoutSeconds
	case "request_timeout_seconds":
		return m.RequestTimeoutSeconds
	case "graceful_shutdown_seconds":
		return m.GracefulShutdownSeconds
	case "rollout_percent":
		return m.RolloutPercent
	case "rollout_minutes":
		return m.RolloutMinutes
	case "instances":
		return m.Instances
	case "cron_schedule":
		return m.CronSchedule
	case "cron_timezone":
		return m.CronTimezone
	case "cron_timeout_seconds":
		return m.CronTimeoutSeconds
	}
	panic("unknown setting " + name)
}

// settingJSON is a planned setting as the API takes it.
func settingJSON(ctx context.Context, spec settingSpec, v attr.Value) any {
	switch spec.kind {
	case settingRollout:
		// 0 and null both mean "all at once": the API is sent null.
		if n := v.(types.Int64); known(n) && n.ValueInt64() > 0 {
			return n.ValueInt64()
		}
		return nil
	case settingInt:
		return v.(types.Int64).ValueInt64()
	case settingList:
		out := listStrings(ctx, v.(types.List))
		if out == nil {
			out = []string{}
		}
		return out
	case settingOptional:
		if s := v.(types.String).ValueString(); s != "" {
			return s
		}
		return nil
	default:
		return v.(types.String).ValueString()
	}
}

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_serverless_service"
}

func computedString(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		Computed: true, Description: desc,
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func settingString(desc string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true, Description: desc, Validators: validators,
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

// withMarkdown gives an attribute its description as the docs render it.
func withMarkdown(a schema.StringAttribute, markdown string) schema.StringAttribute {
	a.MarkdownDescription = markdown
	return a
}

func settingInt64(desc string, min, max int64) schema.Int64Attribute {
	return schema.Int64Attribute{
		Optional: true, Computed: true, Description: desc,
		Validators:    []validator.Int64{int64validator.Between(min, max)},
		PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
	}
}

func (r *serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	noBlank := stringvalidator.LengthAtLeast(1)
	resp.Schema = schema.Schema{
		Description: "A serverless service: a web app, a private service, a worker or a cron job, built from a Git " +
			"repository or run from an image. Creating it starts its first deployment, and changes start a new one, " +
			"unless deploy is false; Terraform does not wait for deployments to finish.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The service's id."),
			"project_id": schema.StringAttribute{
				Required:      true,
				Description:   "The project the service belongs to. Changing it replaces the service.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "The service's name: up to 40 lowercase letters, digits and hyphens, starting with a letter. " +
					"Other services of the project reach a private service at http://<name>. Changing it replaces the service.",
				MarkdownDescription: "The service's name: up to 40 lowercase letters, digits and hyphens, starting with a letter. " +
					"Other services of the project reach a private service at `http://<name>`. Changing it replaces the service.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(serviceNameRe, "must be up to 40 lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"kind": schema.StringAttribute{
				Required: true,
				Description: "WEB (a public address), PRIVATE (reached only by the project's other services), WORKER " +
					"(runs all the time, no address) or CRON (runs on a schedule). Changing it replaces the service.",
				Validators:    []validator.String{stringvalidator.OneOf("WEB", "PRIVATE", "WORKER", "CRON")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"git": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Build the service from a Git repository: a public one by its https address, or one connected " +
					"in the console (Settings › Integrations), which deploys on every push. Give git or image.",
				PlanModifiers: []planmodifier.Object{requiresReplaceOnSwitch()},
				Attributes: map[string]schema.Attribute{
					"repo_url": schema.StringAttribute{
						Optional: true,
						Description: "A public repository's https address, like https://github.com/acme/shop. " +
							"Give repo_url or repo.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(regexp.MustCompile(`^https://[^\s/@]+(:\d+)?/[^\s@]+$`), "must be a repository's https address"),
						},
					},
					"repo": schema.StringAttribute{
						Optional: true,
						Description: "A repository connected in the console, as owner/name (acme/shop). " +
							"Give repo_url or repo.",
						Validators: []validator.String{
							stringvalidator.RegexMatches(regexp.MustCompile(`^[^\s/]+(/[^\s/]+)+$`), "must be owner/name"),
						},
					},
					"provider": schema.StringAttribute{
						Optional: true, Computed: true,
						Description: "GITHUB, GITLAB or BITBUCKET: the connected account repo is on, when more than one is " +
							"connected. PUBLIC_URL for a repository given by repo_url.",
						Validators:    []validator.String{stringvalidator.OneOf("GITHUB", "GITLAB", "BITBUCKET")},
						PlanModifiers: []planmodifier.String{useStateIfUnchanged(path.Root("git").AtName("repo"), path.Root("git").AtName("repo_url"))},
					},
					"connection_id": schema.StringAttribute{
						Optional: true, Computed: true,
						Description: "The connected account's id, when one provider has several. " +
							"Found by itself when only one fits.",
						PlanModifiers: []planmodifier.String{useStateIfUnchanged(path.Root("git").AtName("repo"), path.Root("git").AtName("repo_url"))},
					},
					"branch": schema.StringAttribute{
						Optional: true, Computed: true,
						Description:   "The branch to deploy. Defaults to the repository's default branch (main for a public URL).",
						Validators:    []validator.String{noBlank},
						PlanModifiers: []planmodifier.String{useStateIfUnchanged(path.Root("git").AtName("repo"), path.Root("git").AtName("repo_url"))},
					},
					"tag_pattern": schema.StringAttribute{
						Optional:    true,
						Description: "Deploy only tags matching this pattern, like v*.",
						Validators:  []validator.String{noBlank, stringvalidator.LengthAtMost(100)},
					},
				},
			},
			"image": schema.SingleNestedAttribute{
				Optional:      true,
				Description:   "Run an image as it is, like nginx:1.27 or ghcr.io/acme/api:1.4. Give git or image.",
				PlanModifiers: []planmodifier.Object{requiresReplaceOnSwitch()},
				Attributes: map[string]schema.Attribute{
					"ref": schema.StringAttribute{
						Required:    true,
						Description: "The image, like ghcr.io/acme/api:1.4.",
						Validators:  []validator.String{noBlank, stringvalidator.LengthAtMost(500)},
					},
					"registry_credential_id": schema.StringAttribute{
						Optional:    true,
						Description: "The credential a private image is pulled with (created with `mw registry` or in the console).",
					},
				},
			},
			"size": settingString("The instance size: x-small, small, medium or large (x-large on request).",
				stringvalidator.OneOf("x-small", "small", "medium", "large", "x-large")),
			"port":              settingInt64("Web and private services: the port the app listens on.", 1, 65535),
			"health_check_path": settingString("Web and private services: a path that answers 200 when the app is ready, like /health. \"\" for none."),
			"start_command":     settingString("The command that starts the app, when the build's own is not the one. \"\" for none."),
			"build_command":     settingString("The command that builds the app, when the platform's detection is not enough. \"\" for none."),
			"release_command": settingString("A command run once per deployment, before the new version takes traffic " +
				"(migrations). \"\" for none."),
			"root_dir":        settingString("The folder of the repository the app is in, like apps/api. \"\" for the root."),
			"dockerfile_path": settingString("The Dockerfile to build with, when it is not ./Dockerfile. \"\" for the default."),
			"watch_paths": schema.ListAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Description:   "Only pushes that change these paths (patterns) deploy. Empty: every push.",
				Validators:    []validator.List{listvalidator.SizeAtMost(20)},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"php_version": settingString("Builds without a Dockerfile: the PHP release (7.4 to 8.5). \"\" for what composer.json allows, preferring 8.4.",
				stringvalidator.OneOf("", "7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5")),
			"php_server": settingString("Builds without a Dockerfile: apache (honours .htaccess, every release) or frankenphp (8.2 on). \"\" to detect.",
				stringvalidator.OneOf("", "apache", "frankenphp")),
			"php_extensions": schema.ListAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Description:   "PHP extensions beyond the database drivers every PHP build has and composer.json's ext-*, like gd or redis-6.1.0.",
				Validators:    []validator.List{listvalidator.SizeAtMost(30)},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"document_root": settingString("PHP: the folder served, relative to root_dir (\".\" for the root). \"\" to detect the folder with index.php."),
			"node_version":  settingString("Builds without a Dockerfile: the Node.js release, like 22, 20.11 or lts. \"\" for package.json's engines or .nvmrc."),
			"system_packages": schema.ListAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				Description:   "Builds without a Dockerfile: Debian packages in the image and at build, like ffmpeg.",
				Validators:    []validator.List{listvalidator.SizeAtMost(30)},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"startup_timeout_seconds":   settingInt64("How long a new version has to answer its health check, in seconds.", 10, 900),
			"min_instances":             settingInt64("Web and private services: the fewest instances. 0 sleeps when idle.", 0, 100),
			"max_instances":             settingInt64("Web and private services: the most instances it scales to.", 1, 100),
			"concurrency":               settingInt64("Web and private services: the requests one instance takes at once.", 1, 250),
			"idle_timeout_seconds":      settingInt64("Web and private services: how long an instance stays without requests before it stops.", 60, 3600),
			"request_timeout_seconds":   settingInt64("Web and private services: the longest a request may take, in seconds (up to 600). On the public address the app must also send something at least every 100 s.", 1, 600),
			"graceful_shutdown_seconds": settingInt64("How long an instance has to finish its work when it stops, in seconds.", 1, 30),
			"rollout_percent": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "Web and private services, a gradual rollout: the share of the requests (1 to 50%) a new " +
					"version takes first, for rollout_minutes, before it takes them all — or none, if its error rate " +
					"climbs. Left out, or 0: all at once. It shapes the next deployment and never starts one by itself.",
				Validators:    []validator.Int64{int64validator.Between(0, 50)},
				PlanModifiers: []planmodifier.Int64{offUnlessRepositoryManaged("rollout_percent")},
			},
			"rollout_minutes": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(10),
				Description: "Web and private services: how long a gradual rollout lasts, in minutes (1 to 60, 10 by " +
					"default). It shapes the next deployment and never starts one by itself.",
				Validators:    []validator.Int64{int64validator.Between(1, 60)},
				PlanModifiers: []planmodifier.Int64{offUnlessRepositoryManaged("rollout_minutes")},
			},
			"instances": settingInt64("Workers: how many instances run.", 0, 100),
			"cron_schedule": withMarkdown(settingString("Cron jobs: when it runs, in cron syntax, like */5 * * * *.",
				stringvalidator.LengthAtMost(100)), "Cron jobs: when it runs, in cron syntax, like `*/5 * * * *`."),
			"cron_timezone":        settingString("Cron jobs: the time zone of the schedule, like America/Sao_Paulo or UTC."),
			"cron_timeout_seconds": settingInt64("Cron jobs: the longest a run may take, in seconds.", 60, 3600),
			"auto_deploy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether every push to the branch (or a new image digest) deploys by itself.",
			},
			"wait_for_ci": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether a push waits for the repository's checks to pass before it deploys.",
			},
			"deletion_protection": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether the service is protected from deletion. To destroy a protected service, set it to " +
					"false and apply first.",
			},
			"deploy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether creating the service, and the changes Terraform makes to it, start a deployment. " +
					"False saves them for the next deployment. Terraform never waits for a deployment to finish.",
			},
			"url":          computedString("The public address (web services)."),
			"internal_url": computedString("The address inside the project (private services)."),
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "NEW, DEPLOYING, LIVE, SLEEPING, FAILED, SUSPENDED or PAUSED, when last read.",
			},
			"vcpu_millis": schema.Int64Attribute{
				Computed: true, Description: "The vCPU of one instance, in thousandths.",
				PlanModifiers: []planmodifier.Int64{sizeDerived(false), int64planmodifier.UseStateForUnknown()},
			},
			"memory_mib": schema.Int64Attribute{
				Computed: true, Description: "The memory of one instance, in MiB.",
				PlanModifiers: []planmodifier.Int64{sizeDerived(true), int64planmodifier.UseStateForUnknown()},
			},
			"repository_settings": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "Settings the repository's mw.toml defines. They are read-only here: change them in the " +
					"file and push, and leave them out of this resource.",
			},
			"created_at": computedString("When the service was created (RFC 3339)."),
		},
	}
}

func (r *serviceResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("git"), path.MatchRoot("image")),
	}
}

func (r *serviceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var gitObj types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("git"), &gitObj)...)
	if known(gitObj) {
		var g gitModel
		resp.Diagnostics.Append(gitObj.As(ctx, &g, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			return
		}
		switch {
		case g.RepoURL.IsNull() && g.Repo.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("git"), "Which repository?",
				"Give git.repo_url (a public repository's https address) or git.repo (owner/name of a repository connected in the console).")
		case !g.RepoURL.IsNull() && !g.Repo.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root("git"), "Two repositories",
				"Give git.repo_url or git.repo, not both.")
		case !g.RepoURL.IsNull() && (!g.Provider.IsNull() || !g.ConnectionID.IsNull()):
			resp.Diagnostics.AddAttributeError(path.Root("git"), "A public repository has no provider",
				"git.provider and git.connection_id go with git.repo (a connected repository), not with git.repo_url.")
		}
	}
	var kind, schedule types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("kind"), &kind)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("cron_schedule"), &schedule)...)
	if known(kind) && kind.ValueString() == "CRON" && (schedule.IsNull() || (known(schedule) && schedule.ValueString() == "")) {
		resp.Diagnostics.AddAttributeError(path.Root("cron_schedule"), "A cron job needs a schedule",
			"Set cron_schedule, like \"*/5 * * * *\" (every five minutes).")
	}
}

// serviceComputed are the attributes only the API sets.
var serviceComputed = map[string]bool{
	"id": true, "url": true, "internal_url": true, "status": true, "vcpu_millis": true, "memory_mib": true,
	"repository_settings": true, "created_at": true,
}

// ModifyPlan settles what the attribute plan modifiers leave open. A
// default can differ from a value the repository's mw.toml keeps (and a
// plan modifier then restores it): with nothing really changing, the plan is
// the state, not an update of values only the API sets. A plan modifier can
// also bring a change of its own (a rollout left out is turned off): then
// what an update may move is not known until it is applied.
func (r *serviceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var planned, prior map[string]tftypes.Value
	if req.Plan.Raw.As(&planned) != nil || req.State.Raw.As(&prior) != nil {
		return
	}
	changes := false
	for name, v := range planned {
		if !serviceComputed[name] && !v.Equal(prior[name]) {
			changes = true
		}
	}
	if !changes {
		resp.Plan.Raw = req.State.Raw.Copy()
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	// A Git service's update may build another commit, with another mw.toml.
	// An image service has no repository: its settings from one stay empty,
	// and "(known after apply)" on every plan that changed its size said
	// otherwise (production check, 2026-11-25).
	if git, ok := planned["git"]; ok && !git.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("repository_settings"), types.ListUnknown(types.StringType))...)
		return
	}
	// The framework marks every computed value unknown when anything
	// changes: the state's empty list is put back.
	var settings types.List
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("repository_settings"), &settings)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("repository_settings"), settings)...)
}

func (r *serviceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// ── From the API ────────────────────────────────────────────────────────────

func serviceFromAPI(ctx context.Context, prior serviceModel, s *client.Service, diags *diag.Diagnostics) serviceModel {
	st := s.Settings
	m := serviceModel{
		ID:                      types.StringValue(s.ID),
		ProjectID:               types.StringValue(s.ProjectID),
		Name:                    types.StringValue(s.Name),
		Kind:                    types.StringValue(s.Kind),
		Git:                     types.ObjectNull(gitAttrTypes),
		Image:                   types.ObjectNull(imageAttrTypes),
		Size:                    stringOrNull(st.Size),
		Port:                    types.Int64Value(st.Port),
		HealthCheckPath:         stringOrEmpty(st.HealthCheckPath),
		StartCommand:            stringOrEmpty(st.StartCommand),
		BuildCommand:            stringOrEmpty(st.BuildCommand),
		ReleaseCommand:          stringOrEmpty(st.ReleaseCommand),
		RootDir:                 types.StringValue(st.RootDir),
		DockerfilePath:          stringOrEmpty(st.DockerfilePath),
		WatchPaths:              stringList(st.WatchPaths),
		PHPVersion:              stringOrEmpty(st.PHPVersion),
		PHPServer:               stringOrEmpty(st.PHPServer),
		PHPExtensions:           stringList(st.PHPExtensions),
		DocumentRoot:            stringOrEmpty(st.DocumentRoot),
		NodeVersion:             stringOrEmpty(st.NodeVersion),
		SystemPackages:          stringList(st.SystemPackages),
		StartupTimeoutSeconds:   types.Int64Value(st.StartupTimeoutSeconds),
		MinInstances:            types.Int64Value(st.MinInstances),
		MaxInstances:            types.Int64Value(st.MaxInstances),
		Concurrency:             types.Int64Value(st.Concurrency),
		IdleTimeoutSeconds:      types.Int64Value(st.IdleTimeoutSeconds),
		RequestTimeoutSeconds:   types.Int64Value(st.RequestTimeoutSeconds),
		GracefulShutdownSeconds: types.Int64Value(st.GracefulShutdownSeconds),
		RolloutPercent:          rolloutPercent(prior.RolloutPercent, st.RolloutPercent),
		RolloutMinutes:          rolloutMinutes(prior.RolloutMinutes, st.RolloutMinutes),
		Instances:               types.Int64Value(st.Instances),
		CronSchedule:            stringOrEmpty(st.CronSchedule),
		CronTimezone:            types.StringValue(st.CronTimezone),
		CronTimeoutSeconds:      types.Int64Value(st.CronTimeoutSeconds),
		AutoDeploy:              types.BoolValue(s.AutoDeploy),
		WaitForCI:               types.BoolValue(s.WaitForCI),
		DeletionProtection:      types.BoolValue(s.DeletionProtection),
		Deploy:                  prior.Deploy,
		URL:                     stringOrNull(s.URL),
		InternalURL:             stringOrNull(s.InternalURL),
		Status:                  types.StringValue(s.Status),
		VCPUMillis:              types.Int64Value(st.VCPUMillis),
		MemoryMiB:               types.Int64Value(st.MemoryMiB),
		RepositorySettings:      stringList(s.FileManagedFields),
		CreatedAt:               types.StringValue(s.CreatedAt),
	}
	if !known(m.Deploy) {
		m.Deploy = types.BoolValue(true)
	}
	// Texts the API trims: the configuration's spelling stays.
	for _, keep := range []struct {
		dst   *types.String
		prior types.String
	}{
		{&m.HealthCheckPath, prior.HealthCheckPath}, {&m.StartCommand, prior.StartCommand},
		{&m.BuildCommand, prior.BuildCommand}, {&m.ReleaseCommand, prior.ReleaseCommand},
		{&m.DockerfilePath, prior.DockerfilePath}, {&m.CronSchedule, prior.CronSchedule},
	} {
		if known(keep.prior) && strings.TrimSpace(keep.prior.ValueString()) == keep.dst.ValueString() {
			*keep.dst = keep.prior
		}
	}

	switch s.Source.Kind {
	case "GIT":
		if g := s.Source.Git; g != nil {
			var prev gitModel
			if known(prior.Git) {
				diags.Append(prior.Git.As(ctx, &prev, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
			}
			gm := gitModel{
				RepoURL:      types.StringNull(),
				Repo:         types.StringNull(),
				Provider:     types.StringValue(g.Provider),
				ConnectionID: stringOrNull(g.ConnectionID),
				Branch:       stringOrNull(g.Branch),
				TagPattern:   stringOrNull(g.TagPattern),
			}
			if g.Provider == "PUBLIC_URL" {
				gm.RepoURL = keepEquivalent(prev.RepoURL, g.RepoURL)
			} else if g.RepoFullName != nil {
				gm.Repo = types.StringValue(*g.RepoFullName)
				// The provider answers with its own casing of owner/name.
				if known(prev.Repo) && strings.EqualFold(prev.Repo.ValueString(), *g.RepoFullName) {
					gm.Repo = prev.Repo
				}
			}
			if known(prev.Branch) && strings.TrimSpace(prev.Branch.ValueString()) == gm.Branch.ValueString() {
				gm.Branch = prev.Branch
			}
			if known(prev.TagPattern) && strings.TrimSpace(prev.TagPattern.ValueString()) == gm.TagPattern.ValueString() {
				gm.TagPattern = prev.TagPattern
			}
			obj, d := types.ObjectValueFrom(ctx, gitAttrTypes, gm)
			diags.Append(d...)
			m.Git = obj
		}
	case "IMAGE":
		if img := s.Source.Image; img != nil {
			var prev imageModel
			if known(prior.Image) {
				diags.Append(prior.Image.As(ctx, &prev, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
			}
			ref := img.Ref
			im := imageModel{
				Ref:                  keepEquivalent(prev.Ref, &ref),
				RegistryCredentialID: stringOrNull(img.RegistryCredentialID),
			}
			obj, d := types.ObjectValueFrom(ctx, imageAttrTypes, im)
			diags.Append(d...)
			m.Image = obj
		}
	}
	return m
}

// rolloutPercent is the share a new version takes first. Off is null or 0,
// as the configuration says it: the API's null (or 0) reads back the same
// way, so neither spelling ever shows up as a change.
func rolloutPercent(prior types.Int64, api *int64) types.Int64 {
	if api != nil && *api > 0 {
		return types.Int64Value(*api)
	}
	if known(prior) && prior.ValueInt64() == 0 {
		return prior
	}
	return types.Int64Null()
}

// rolloutMinutes is how long a gradual rollout lasts. An API without
// gradual rollouts does not say: the setting is then its default.
func rolloutMinutes(prior types.Int64, api *int64) types.Int64 {
	if api != nil {
		return types.Int64Value(*api)
	}
	if known(prior) {
		return prior
	}
	return types.Int64Value(10)
}

// rolloutOff says whether a planned rollout_percent means "all at once".
func rolloutOff(v types.Int64) bool { return v.IsNull() || (known(v) && v.ValueInt64() == 0) }

// ── To the API ──────────────────────────────────────────────────────────────

// gitSource is a planned Git source as the API takes it, with the connected
// account found when the configuration leaves it out.
func (r *serviceResource) gitSource(ctx context.Context, g gitModel, update bool) (map[string]any, error) {
	spec := map[string]any{}
	if known(g.RepoURL) {
		spec["provider"] = "PUBLIC_URL"
		spec["repo_url"] = strings.TrimSpace(g.RepoURL.ValueString())
		if update {
			spec["connection_id"] = nil
			spec["repo_full_name"] = nil
		}
	} else {
		provider, connection := "", ""
		if known(g.Provider) && g.Provider.ValueString() != "PUBLIC_URL" {
			provider = g.Provider.ValueString()
		}
		if known(g.ConnectionID) {
			connection = g.ConnectionID.ValueString()
		}
		if provider == "" || connection == "" {
			found, err := r.findConnection(ctx, provider, connection)
			if err != nil {
				return nil, err
			}
			provider, connection = found.Provider, found.ID
		}
		spec["provider"] = provider
		spec["connection_id"] = connection
		spec["repo_full_name"] = strings.TrimSpace(g.Repo.ValueString())
	}
	if known(g.Branch) {
		spec["branch"] = g.Branch.ValueString()
	}
	if known(g.TagPattern) {
		spec["tag_pattern"] = g.TagPattern.ValueString()
	} else if update {
		spec["tag_pattern"] = nil
	}
	return spec, nil
}

// findConnection is the one connected Git account that fits, as `mw` finds it.
func (r *serviceResource) findConnection(ctx context.Context, provider, id string) (*client.GitConnection, error) {
	all, err := r.client.ListGitConnections(ctx)
	if err != nil {
		return nil, err
	}
	var fit []client.GitConnection
	for _, c := range all {
		if c.Status != "ACTIVE" || (provider != "" && !strings.EqualFold(c.Provider, provider)) || (id != "" && c.ID != id) {
			continue
		}
		fit = append(fit, c)
	}
	describe := func(list []client.GitConnection) string {
		var out []string
		for _, c := range list {
			s := strings.ToLower(c.Provider) + " " + c.AccountLogin
			if c.Status != "ACTIVE" {
				s += " (" + strings.ToLower(strings.ReplaceAll(c.Status, "_", " ")) + ")"
			}
			out = append(out, s+" ["+c.ID+"]")
		}
		return strings.Join(out, ", ")
	}
	switch {
	case len(fit) == 1:
		return &fit[0], nil
	case len(all) == 0:
		return nil, fmt.Errorf("no Git account is connected: connect GitHub, GitLab or Bitbucket in the console " +
			"(Settings › Integrations), or give a public repository's https address in git.repo_url")
	case len(fit) == 0:
		return nil, fmt.Errorf("no connected Git account fits: %s", describe(all))
	default:
		return nil, fmt.Errorf("more than one Git account is connected (%s): set git.provider or git.connection_id", describe(fit))
	}
}

func imageSource(im imageModel, update bool) map[string]any {
	spec := map[string]any{"ref": strings.TrimSpace(im.Ref.ValueString())}
	if known(im.RegistryCredentialID) {
		spec["registry_credential_id"] = im.RegistryCredentialID.ValueString()
	} else if update {
		spec["registry_credential_id"] = nil
	}
	return spec
}

// ── CRUD ────────────────────────────────────────────────────────────────────

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"name":                plan.Name.ValueString(),
		"kind":                plan.Kind.ValueString(),
		"auto_deploy":         plan.AutoDeploy.ValueBool(),
		"wait_for_ci":         plan.WaitForCI.ValueBool(),
		"deletion_protection": plan.DeletionProtection.ValueBool(),
		"deploy":              plan.Deploy.ValueBool(),
	}
	if known(plan.Git) {
		var g gitModel
		resp.Diagnostics.Append(plan.Git.As(ctx, &g, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
		if resp.Diagnostics.HasError() {
			return
		}
		spec, err := r.gitSource(ctx, g, false)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("git"), "Could not find the repository's account", err.Error())
			return
		}
		body["source"] = map[string]any{"kind": "GIT", "git": spec}
	} else {
		var im imageModel
		resp.Diagnostics.Append(plan.Image.As(ctx, &im, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			return
		}
		body["source"] = map[string]any{"kind": "IMAGE", "image": imageSource(im, false)}
	}
	settings := map[string]any{}
	for _, spec := range serviceSettings {
		v := plan.setting(spec.name)
		if !known(v) {
			continue
		}
		if spec.kind == settingOptional && v.(types.String).ValueString() == "" {
			continue // none is the default
		}
		if spec.kind == settingRollout && rolloutOff(v.(types.Int64)) {
			continue // all at once is the default
		}
		if spec.name == "rollout_minutes" {
			// Its default (10) is the API's own: sent only when configured.
			var configured types.Int64
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("rollout_minutes"), &configured)...)
			if configured.IsNull() {
				continue
			}
		}
		settings[spec.name] = settingJSON(ctx, spec, v)
	}
	if len(settings) > 0 {
		body["settings"] = settings
	}

	projectID := plan.ProjectID.ValueString()
	out, err := r.client.CreateService(ctx, projectID, body)
	var svc *client.Service
	switch {
	case err == nil:
		svc = &out.Service
	case isDeployRefusal(err):
		// Created, its first deployment refused: keep it, say why.
		found, ferr := r.findByName(ctx, projectID, plan.Name.ValueString())
		if ferr != nil || found == nil {
			resp.Diagnostics.AddError("Could not create the service", err.Error())
			return
		}
		svc = found
		deployRefusedWarning(&resp.Diagnostics, "The service "+svc.Name, err)
	default:
		resp.Diagnostics.AddError("Could not create the service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceFromAPI(ctx, plan, svc, &resp.Diagnostics))...)
}

func (r *serviceResource) findByName(ctx context.Context, projectID, name string) (*client.Service, error) {
	all, err := r.client.ListServices(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name && all[i].ProjectID == projectID {
			return r.client.GetService(ctx, all[i].ID)
		}
	}
	return nil, nil
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.client.GetService(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the service", err.Error())
		return
	}
	if s.Status == "DELETING" {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceFromAPI(ctx, state, s, &resp.Diagnostics))...)
}

func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	deploy := plan.Deploy.ValueBool()
	changed := false
	// fail records what is true now before saying what went wrong, so the next
	// plan starts from it.
	fail := func(summary string, err error) {
		if changed {
			if s, gerr := r.client.GetService(ctx, id); gerr == nil {
				resp.Diagnostics.Append(resp.State.Set(ctx, serviceFromAPI(ctx, plan, s, &resp.Diagnostics))...)
			}
		}
		resp.Diagnostics.AddError(summary, err.Error())
	}

	// The source: repository, branch, image, auto-deploy.
	sourcePatch := map[string]any{}
	sourceChanged := !plan.Git.Equal(state.Git) || !plan.Image.Equal(state.Image)
	if sourceChanged && known(plan.Git) {
		var g gitModel
		resp.Diagnostics.Append(plan.Git.As(ctx, &g, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})...)
		if resp.Diagnostics.HasError() {
			return
		}
		spec, err := r.gitSource(ctx, g, true)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("git"), "Could not find the repository's account", err.Error())
			return
		}
		sourcePatch["git"] = spec
	}
	if sourceChanged && known(plan.Image) {
		var im imageModel
		resp.Diagnostics.Append(plan.Image.As(ctx, &im, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			return
		}
		sourcePatch["image"] = imageSource(im, true)
	}
	if !plan.AutoDeploy.Equal(state.AutoDeploy) {
		sourcePatch["auto_deploy"] = plan.AutoDeploy.ValueBool()
	}
	if !plan.WaitForCI.Equal(state.WaitForCI) {
		sourcePatch["wait_for_ci"] = plan.WaitForCI.ValueBool()
	}
	if len(sourcePatch) > 0 {
		if _, err := r.client.UpdateServiceSource(ctx, id, sourcePatch); err != nil {
			fail("Could not change the service's source", err)
			return
		}
		changed = true
	}

	// The settings, only those that change: the ones the repository's mw.toml
	// defines are refused when changed from here.
	settings := map[string]any{}
	needsBuild := sourceChanged
	onlyRollout := true
	for _, spec := range serviceSettings {
		v := plan.setting(spec.name)
		if v.IsUnknown() || v.Equal(state.setting(spec.name)) {
			continue
		}
		if spec.kind == settingRollout {
			if rolloutOff(v.(types.Int64)) && rolloutOff(state.RolloutPercent) {
				continue // 0 and null: all at once, both
			}
		} else if v.IsNull() {
			continue
		}
		settings[spec.name] = settingJSON(ctx, spec, v)
		onlyRollout = onlyRollout && spec.rollout
		if spec.build && known(plan.Git) {
			needsBuild = true
		}
	}
	rebuild := deploy && needsBuild
	if len(settings) > 0 {
		// A rollout setting shapes the next deployment: alone, it starts none.
		_, err := r.client.UpdateServiceSettings(ctx, id, settings, deploy && !rebuild && !onlyRollout)
		switch {
		case err == nil:
			changed = true
		case isDeployRefusal(err):
			changed = true
			deployRefusedWarning(&resp.Diagnostics, "The settings change", err)
		default:
			fail("Could not change the service's settings", err)
			return
		}
	}

	if !plan.DeletionProtection.Equal(state.DeletionProtection) {
		if _, err := r.client.SetServiceDeletionProtection(ctx, id, plan.DeletionProtection.ValueBool()); err != nil {
			fail("Could not change the service's deletion protection", err)
			return
		}
		changed = true
	}

	// A new source, or a setting the build reads, takes a build: the latest
	// commit (or the image's current digest), built afresh.
	if rebuild {
		if _, err := r.client.DeployService(ctx, id, "latest"); err != nil {
			if isDeployRefusal(err) {
				deployRefusedWarning(&resp.Diagnostics, "The change", err)
			} else {
				fail("Saved, but the deployment did not start", err)
				return
			}
		}
	}

	s, err := r.client.GetService(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, serviceFromAPI(ctx, plan, s, &resp.Diagnostics))...)
}

func (r *serviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Deletion protection is on",
			fmt.Sprintf("The service %s is protected from deletion. Set deletion_protection = false, run terraform apply, "+
				"and then destroy it.", state.Name.ValueString()))
		return
	}
	id := state.ID.ValueString()
	if err := r.client.DeleteService(ctx, id, state.Name.ValueString()); err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Could not delete the service", err.Error())
		return
	}
	// Deleting is two-phase: the name is free once the service is gone.
	err := waitUntil(ctx, deleteTimeout, func() (bool, error) {
		_, err := r.client.GetService(ctx, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddWarning("The service is still being deleted",
			fmt.Sprintf("The service %s is being deleted and finishes on its own (%s). Its name is free again once it is gone.",
				state.Name.ValueString(), err))
	}
}

func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if project, name, ok := strings.Cut(id, "/"); ok {
		s, err := r.findByName(ctx, project, name)
		if err != nil {
			resp.Diagnostics.AddError("Could not list the project's services", err.Error())
			return
		}
		if s == nil {
			resp.Diagnostics.AddError("No such service", fmt.Sprintf("The project %s has no service named %s.", project, name))
			return
		}
		id = s.ID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deploy"), true)...)
}
