package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	_ resource.Resource                = (*domainResource)(nil)
	_ resource.ResourceWithConfigure   = (*domainResource)(nil)
	_ resource.ResourceWithImportState = (*domainResource)(nil)
)

// NewDomainResource is masterdaweb_serverless_domain.
func NewDomainResource() resource.Resource { return &domainResource{} }

type domainResource struct{ client *client.Client }

type domainModel struct {
	ID          types.String `tfsdk:"id"`
	ServiceID   types.String `tfsdk:"service_id"`
	Hostname    types.String `tfsdk:"hostname"`
	Status      types.String `tfsdk:"status"`
	Apex        types.Bool   `tfsdk:"apex"`
	RedirectTo  types.String `tfsdk:"redirect_to"`
	DNSRecords  types.List   `tfsdk:"dns_records"`
	Error       types.String `tfsdk:"error"`
	ActivatedAt types.String `tfsdk:"activated_at"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

var dnsRecordAttrTypes = map[string]attr.Type{
	"type":   types.StringType,
	"name":   types.StringType,
	"value":  types.StringType,
	"option": types.StringType,
	"found":  types.BoolType,
}

func (r *domainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_serverless_domain"
}

func (r *domainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A domain of yours on a web service. Create the DNS records in dns_records at your DNS provider: " +
			"the domain is checked every few minutes and its certificate issued once they are found.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The domain's id."),
			"service_id": schema.StringAttribute{
				Required:      true,
				Description:   "The web service the domain points to. Changing it replaces the domain.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"hostname": schema.StringAttribute{
				Required:      true,
				Description:   "The hostname, like app.example.com, or a root domain like example.com. Changing it replaces the domain.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 253)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "PENDING_DNS (waiting for the records), PENDING_CERTIFICATE, ACTIVE or FAILED, when last read.",
			},
			"apex": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether it is a root domain (no CNAME possible: an ALIAS, or A records to the redirect service).",
			},
			"redirect_to": schema.StringAttribute{
				Computed:    true,
				Description: "A root domain pointed at the redirect service sends its visitors here.",
			},
			"dns_records": schema.ListNestedAttribute{
				Computed: true,
				Description: "The DNS records to create. A root domain has alternatives, told apart by option: alias (if " +
					"your DNS has ALIAS or flattening) or redirect (A records that send visitors to www).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type":   schema.StringAttribute{Computed: true, Description: "TXT, CNAME, ALIAS or A."},
						"name":   schema.StringAttribute{Computed: true, Description: "The record's name."},
						"value":  schema.StringAttribute{Computed: true, Description: "The record's value."},
						"option": schema.StringAttribute{Computed: true, Description: "Root domains: alias or redirect."},
						"found":  schema.BoolAttribute{Computed: true, Description: "Whether the last check found it."},
					},
				},
			},
			"error": schema.StringAttribute{
				Computed:    true,
				Description: "Why the domain is not active yet, when the last check said.",
			},
			"activated_at": schema.StringAttribute{Computed: true, Description: "When the domain became active (RFC 3339)."},
			"created_at":   computedString("When the domain was added (RFC 3339)."),
		},
	}
}

func (r *domainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

func domainFromAPI(prior domainModel, serviceID string, d *client.Domain) domainModel {
	records := make([]attr.Value, 0, len(d.Instructions))
	for _, in := range d.Instructions {
		found := types.BoolNull()
		if in.Found != nil {
			found = types.BoolValue(*in.Found)
		}
		records = append(records, types.ObjectValueMust(dnsRecordAttrTypes, map[string]attr.Value{
			"type":   types.StringValue(in.Type),
			"name":   types.StringValue(in.Name),
			"value":  types.StringValue(in.Value),
			"option": stringOrNull(in.Option),
			"found":  found,
		}))
	}
	m := domainModel{
		ID:          types.StringValue(d.ID),
		ServiceID:   types.StringValue(serviceID),
		Hostname:    types.StringValue(d.Hostname),
		Status:      types.StringValue(d.Status),
		Apex:        types.BoolValue(d.Apex),
		RedirectTo:  stringOrNull(d.RedirectTo),
		DNSRecords:  types.ListValueMust(types.ObjectType{AttrTypes: dnsRecordAttrTypes}, records),
		Error:       types.StringNull(),
		ActivatedAt: stringOrNull(d.ActivatedAt),
		CreatedAt:   types.StringValue(d.CreatedAt),
	}
	if d.Error != nil {
		msg := d.Error.Code
		if d.Error.Message != nil && *d.Error.Message != "" {
			msg = *d.Error.Message
		}
		m.Error = types.StringValue(msg)
	}
	// The API keeps hostnames lowercase, without a trailing dot, in xn-- form.
	if known(prior.Hostname) && normalizeHostname(prior.Hostname.ValueString()) == d.Hostname {
		m.Hostname = prior.Hostname
	}
	return m
}

func (r *domainResource) find(ctx context.Context, serviceID, id string) (*client.Domain, error) {
	all, err := r.client.ListDomains(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id || (!isUUID(id) && all[i].Hostname == normalizeHostname(id)) {
			return &all[i], nil
		}
	}
	return nil, nil
}

func (r *domainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serviceID := plan.ServiceID.ValueString()
	d, err := r.client.AddDomain(ctx, serviceID, normalizeHostname(plan.Hostname.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError("Could not add the domain", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, domainFromAPI(plan, serviceID, d))...)
}

func (r *domainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serviceID := state.ServiceID.ValueString()
	d, err := r.find(ctx, serviceID, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read the service's domains", err.Error())
		return
	}
	if d == nil || d.Status == "REMOVING" {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, domainFromAPI(state, serviceID, d))...)
}

// Update has nothing to change: every argument replaces the domain.
func (r *domainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *domainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDomain(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Could not remove the domain", err.Error())
	}
}

// ImportState takes <service_id>/<domain id or hostname>, or a domain's id
// alone (the account's services are then searched).
func (r *domainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := strings.TrimSpace(req.ID)
	serviceID, domainRef, composite := strings.Cut(ref, "/")
	var found *client.Domain
	if composite {
		d, err := r.find(ctx, serviceID, domainRef)
		if err != nil {
			resp.Diagnostics.AddError("Could not read the service's domains", err.Error())
			return
		}
		found = d
	} else {
		services, err := r.client.ListServices(ctx, "")
		if err != nil {
			resp.Diagnostics.AddError("Could not list the services", err.Error())
			return
		}
		for _, s := range services {
			if s.Kind != "WEB" {
				continue
			}
			d, err := r.find(ctx, s.ID, ref)
			if err != nil {
				resp.Diagnostics.AddError("Could not read the service's domains", err.Error())
				return
			}
			if d != nil {
				found, serviceID = d, s.ID
				break
			}
		}
	}
	if found == nil {
		resp.Diagnostics.AddError("No such domain",
			fmt.Sprintf("No domain matches %q. Import a domain as <service_id>/<hostname>, <service_id>/<domain_id> or by its id.", ref))
		return
	}
	if found.IsDefault {
		resp.Diagnostics.AddError("That is the service's own address",
			found.Hostname+" is the service's default address: it comes with the service and is not a domain resource.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), found.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_id"), serviceID)...)
}
