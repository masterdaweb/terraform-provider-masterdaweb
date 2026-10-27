package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func configure(t *testing.T, attrs map[string]any) []*tfprotov6.Diagnostic {
	t.Helper()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	typ := schemas.Provider.ValueType()
	resp, err := server.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: dynamic(t, typ, toValue(t, typ, attrs)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Diagnostics
}

func TestProviderConfiguration(t *testing.T) {
	t.Setenv(EnvToken, "")
	t.Setenv(EnvAPIURL, "")
	expectError(t, configure(t, map[string]any{}), "Set `token` in the provider block or the MW_TOKEN environment variable")
	expectError(t, configure(t, map[string]any{"token": "t", "api_url": "apiv2.masterdaweb.com"}), "The API's address is a full http(s) URL")
	expectError(t, configure(t, map[string]any{"token": unknown}), "Unknown API token")
	failOnDiags(t, "configure", configure(t, map[string]any{"token": "mwk_x"}))

	t.Setenv(EnvToken, "mwk_from_env")
	failOnDiags(t, "configure from the environment", configure(t, map[string]any{}))
}

func TestProviderUsesTheEnvironment(t *testing.T) {
	api := newFakeAPI(t)
	t.Setenv(EnvToken, "mwk_test")
	t.Setenv(EnvAPIURL, api.URL())
	server, err := providerserver.NewProtocol6WithError(New("1.2.3")())()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schemas, _ := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	typ := schemas.Provider.ValueType()
	resp, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
		TerraformVersion: "1.9.8",
		Config:           dynamic(t, typ, tftypes.NewValue(typ, map[string]tftypes.Value{"api_url": tftypes.NewValue(tftypes.String, nil), "token": tftypes.NewValue(tftypes.String, nil)})),
	})
	if err != nil {
		t.Fatal(err)
	}
	failOnDiags(t, "configure", resp.Diagnostics)
	ds := schemas.DataSourceSchemas["masterdaweb_serverless_outbound_addresses"].ValueType()
	read, err := server.ReadDataSource(ctx, &tfprotov6.ReadDataSourceRequest{
		TypeName: "masterdaweb_serverless_outbound_addresses",
		Config:   dynamic(t, ds, toValue(t, ds, map[string]any{})),
	})
	if err != nil {
		t.Fatal(err)
	}
	failOnDiags(t, "read", read.Diagnostics)
	calls := api.callsTo("GET", "/v1/serverless/outbound-addresses")
	if len(calls) != 1 || calls[0].Auth != "Bearer mwk_test" {
		t.Fatalf("calls %+v", calls)
	}
}

var (
	allResources = []string{
		"masterdaweb_project", "masterdaweb_project_resource", "masterdaweb_instance", "masterdaweb_ssh_key",
		"masterdaweb_snapshot", "masterdaweb_backup_schedule", "masterdaweb_firewall", "masterdaweb_vpc", "masterdaweb_vpc_attachment",
		"masterdaweb_reverse_dns", "masterdaweb_dns_zone", "masterdaweb_dns_record",
		"masterdaweb_serverless_service", "masterdaweb_serverless_variable", "masterdaweb_serverless_domain",
		"masterdaweb_serverless_binding", "masterdaweb_serverless_log_drain", "masterdaweb_database",
		"masterdaweb_object_storage_bucket", "masterdaweb_object_storage_key", "masterdaweb_webhook",
		"masterdaweb_vpn_user", "masterdaweb_vpn_gateway_settings", "masterdaweb_security_exception",
		"masterdaweb_security_settings",
	}
	allDataSources = []string{
		"masterdaweb_project", "masterdaweb_regions", "masterdaweb_plans", "masterdaweb_images",
		"masterdaweb_instance", "masterdaweb_instances", "masterdaweb_ssh_key", "masterdaweb_vpc",
		"masterdaweb_firewall", "masterdaweb_firewall_macros", "masterdaweb_dns_zone",
		"masterdaweb_serverless_outbound_addresses", "masterdaweb_vpn_gateway", "masterdaweb_vpn_gateways",
		"masterdaweb_security_overview", "masterdaweb_security_findings",
	}
)

// undescribed are the attributes, nested ones too, with no description.
func undescribed(prefix string, attrs []*tfprotov6.SchemaAttribute) []string {
	var out []string
	for _, a := range attrs {
		if a.Description == "" {
			out = append(out, prefix+a.Name)
		}
		if a.NestedType != nil {
			out = append(out, undescribed(prefix+a.Name+".", a.NestedType.Attributes)...)
		}
	}
	return out
}

func TestEveryResourceAndDataSourceIsServed(t *testing.T) {
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	failOnDiags(t, "schema", schemas.Diagnostics)
	for _, name := range allResources {
		s, ok := schemas.ResourceSchemas[name]
		if !ok {
			t.Errorf("no resource %s", name)
			continue
		}
		if s.Block.Description == "" {
			t.Errorf("resource %s has no description", name)
		}
		for _, a := range undescribed(name+".", s.Block.Attributes) {
			t.Errorf("%s has no description", a)
		}
	}
	for _, name := range allDataSources {
		s, ok := schemas.DataSourceSchemas[name]
		if !ok {
			t.Errorf("no data source %s", name)
			continue
		}
		if s.Block.Description == "" {
			t.Errorf("data source %s has no description", name)
		}
		for _, a := range undescribed("data."+name+".", s.Block.Attributes) {
			t.Errorf("%s has no description", a)
		}
	}
	if len(schemas.ResourceSchemas) != len(allResources) || len(schemas.DataSourceSchemas) != len(allDataSources) {
		t.Errorf("%d resources and %d data sources", len(schemas.ResourceSchemas), len(schemas.DataSourceSchemas))
	}
}

// Every resource can be imported: what Terraform did not create, or lost
// track of, is brought under it without being made again.
func TestEveryResourceImports(t *testing.T) {
	p := New("test")()
	for _, f := range p.Resources(context.Background()) {
		r := f()
		var meta resource.MetadataResponse
		r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "masterdaweb"}, &meta)
		if _, ok := r.(resource.ResourceWithImportState); !ok {
			t.Errorf("%s cannot be imported", meta.TypeName)
		}
	}
}
