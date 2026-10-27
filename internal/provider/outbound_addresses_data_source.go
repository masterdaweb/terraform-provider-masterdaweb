package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

var (
	_ datasource.DataSource              = (*outboundAddressesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*outboundAddressesDataSource)(nil)
)

// NewOutboundAddressesDataSource is masterdaweb_serverless_outbound_addresses.
func NewOutboundAddressesDataSource() datasource.DataSource { return &outboundAddressesDataSource{} }

type outboundAddressesDataSource struct{ client *client.Client }

type outboundAddressesModel struct {
	ID          types.String `tfsdk:"id"`
	Ranges      types.List   `tfsdk:"ranges"`
	Addresses   types.List   `tfsdk:"addresses"`
	AddressesV6 types.List   `tfsdk:"addresses_v6"`
}

func (d *outboundAddressesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_serverless_outbound_addresses"
}

func (d *outboundAddressesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The addresses services reach the internet from, for allowlists at a bank, an ERP or a " +
			"database elsewhere. Put the ranges on an allowlist: they are stable. The addresses in use now change " +
			"when the platform replaces a server, always within the ranges.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Always outbound-addresses."},
			"ranges": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "The ranges (CIDR) every outbound address is drawn from. Stable: what an allowlist should hold.",
			},
			"addresses": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "The IPv4 addresses services reach the internet from right now.",
			},
			"addresses_v6": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "The same, in IPv6, where there are some.",
			},
		},
	}
}

func (d *outboundAddressesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *outboundAddressesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	out, err := d.client.GetOutboundAddresses(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the outbound addresses", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, outboundAddressesModel{
		ID:          types.StringValue("outbound-addresses"),
		Ranges:      stringList(out.Ranges),
		Addresses:   stringList(out.Addresses),
		AddressesV6: stringList(out.AddressesV6),
	})...)
}
