// Package provider is the Master da Web Terraform provider: instances (VPS)
// and what goes with them — SSH keys, firewalls, VPCs, snapshots, reverse
// DNS —, VPN gateways' logins and settings, DNS zones and records, projects,
// serverless services with their variables, domains, bindings and log drains,
// managed databases, Object Storage buckets and keys, the Security Center's
// exceptions and settings, and webhooks, all through the public API (v1).
package provider

import (
	"context"
	"net/url"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// The environment variables the provider reads, the same as the `mw` CLI's.
const (
	EnvAPIURL = "MW_API_URL"
	EnvToken  = "MW_TOKEN"
)

var _ provider.Provider = (*masterdawebProvider)(nil)

type masterdawebProvider struct {
	version string
}

type providerModel struct {
	APIURL types.String `tfsdk:"api_url"`
	Token  types.String `tfsdk:"token"`
}

// New returns the provider, for providerserver.Serve and tests.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &masterdawebProvider{version: version}
	}
}

func (p *masterdawebProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "masterdaweb"
	resp.Version = p.version
}

func (p *masterdawebProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Master da Web cloud instances, SSH keys, firewalls, VPCs, snapshots, reverse DNS, " +
			"VPN gateways, DNS zones and records, projects, serverless services, managed databases, Object Storage, " +
			"the Security Center and webhooks.",
		MarkdownDescription: "Manage [Master da Web](https://www.masterdaweb.com) cloud instances, SSH keys, firewalls, " +
			"VPCs, snapshots, reverse DNS, VPN gateways, DNS zones and records, projects, serverless services, managed " +
			"databases, Object Storage, the Security Center and webhooks.",
		Attributes: map[string]schema.Attribute{
			"api_url": schema.StringAttribute{
				Optional: true,
				Description: "The API's address. Defaults to the " + EnvAPIURL + " environment variable, " +
					"or " + client.DefaultBaseURL + ".",
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "An API token, created in the console under Settings → API tokens. Defaults to the " +
					EnvToken + " environment variable.",
			},
		},
	}
}

func (p *masterdawebProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.APIURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_url"), "Unknown API address",
			"The API's address is not known until apply. Give it as a fixed value, or set "+EnvAPIURL+".")
	}
	if cfg.Token.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Unknown API token",
			"The token is not known until apply. Give it as a fixed value or variable, or set "+EnvToken+".")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	apiURL := strings.TrimSpace(os.Getenv(EnvAPIURL))
	if !cfg.APIURL.IsNull() {
		apiURL = strings.TrimSpace(cfg.APIURL.ValueString())
	}
	if apiURL == "" {
		apiURL = client.DefaultBaseURL
	}
	if u, err := url.Parse(apiURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_url"), "Invalid API address",
			"The API's address is a full http(s) URL, like "+client.DefaultBaseURL+", not \""+apiURL+"\".")
		return
	}

	token := strings.TrimSpace(os.Getenv(EnvToken))
	if !cfg.Token.IsNull() {
		token = strings.TrimSpace(cfg.Token.ValueString())
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing API token",
			"Set `token` in the provider block or the "+EnvToken+" environment variable. Tokens are created in "+
				"the console under Settings → API tokens, with the scopes of what Terraform manages: vm:read, "+
				"vm:write, vm:power, order:write and billing:read for instances (vm:delete to destroy them), "+
				"network:read and network:write for firewalls, VPCs, reverse DNS and VPN gateways, "+
				"security:read and security:write for the Security Center, dns:read and dns:write, "+
				"serverless:read and serverless:write for projects and services, databases:write, storage:write, "+
				"account:read and webhooks:write for webhooks, and the :delete scopes to destroy things.")
		return
	}

	ua := "terraform-provider-masterdaweb/" + p.version
	if req.TerraformVersion != "" {
		ua += " terraform/" + req.TerraformVersion
	}
	c := client.New(apiURL, token, ua)
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *masterdawebProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewProjectResource,
		NewProjectMembershipResource,
		NewInstanceResource,
		NewSSHKeyResource,
		NewSnapshotResource,
		NewBackupScheduleResource,
		NewFirewallResource,
		NewVPCResource,
		NewVPCAttachmentResource,
		NewReverseDNSResource,
		NewDNSZoneResource,
		NewDNSRecordResource,
		NewServiceResource,
		NewVariableResource,
		NewDomainResource,
		NewBindingResource,
		NewLogDrainResource,
		NewDatabaseResource,
		NewBucketResource,
		NewStorageKeyResource,
		NewWebhookResource,
		NewVPNUserResource,
		NewVPNGatewaySettingsResource,
		NewSecurityExceptionResource,
		NewSecuritySettingsResource,
	}
}

func (p *masterdawebProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewProjectDataSource,
		NewRegionsDataSource,
		NewPlansDataSource,
		NewImagesDataSource,
		NewInstanceDataSource,
		NewInstancesDataSource,
		NewSSHKeyDataSource,
		NewVPCDataSource,
		NewFirewallDataSource,
		NewFirewallMacrosDataSource,
		NewDNSZoneDataSource,
		NewOutboundAddressesDataSource,
		NewVPNGatewayDataSource,
		NewVPNGatewaysDataSource,
		NewSecurityOverviewDataSource,
		NewSecurityFindingsDataSource,
	}
}
