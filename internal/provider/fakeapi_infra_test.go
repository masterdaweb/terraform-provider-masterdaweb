package provider

// The infrastructure half of the fake API: the catalog, orders and
// operations, instances (power, reinstall, password, snapshots), SSH keys,
// firewalls, VPCs, DNS zones and records, reverse DNS, Object Storage access
// keys, and what projects group besides services. As in the rest of the
// fake, what the real API normalises is normalised here, its validation
// refuses what it refuses, and the slow parts (a deploy, a payment, an
// attachment) take a few polls.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

type fakeInstance struct {
	inst client.Instance
	// running or stopped.
	power string
	// Fresh status polls (or refused writes) the instance stays busy for:
	// a deploy, a reinstall.
	busyPolls   int
	image       string
	customImage string
	sshKeys     []string
	userData    string
	password    string
	reinstalls  int
	// Beyond the plan: GB on the main disk, data disks (GB, in order),
	// add-ons, and an assignment id for each address (in IPAddresses' order).
	mainExtra   int64
	disks       []int64
	addons      map[string]int64
	assignments []string
	resizes     int
	// Its configuration was never recorded: it cannot be resized.
	unresizable bool
	// The subscription it is billed on.
	subscription string
	// In rescue mode: writes are refused for it.
	rescue bool
}

type fakeInfra struct {
	regions   []client.Region
	offerings []client.Offering
	images    []client.CatalogImage

	instances  map[string]*fakeInstance
	orders     map[string]*client.Order
	orderSpecs map[string]map[string]any
	// GETs an order stays pending_payment for before it is paid.
	orderPolls map[string]int
	operations map[string]*client.Operation
	opPolls    map[string]int
	sshKeys    map[string]*client.SSHKey
	snapshots  map[string][]*client.Snapshot
	firewalls  map[string]*client.Firewall
	vpcs       map[string]*client.VPC
	vpcRegion  map[string]string
	// Instances attached but not listed yet, by VPC: GETs to go.
	vpcJoining map[string]map[string]int
	zones      map[string]*client.DNSZone
	records    map[string][]*client.DNSRecord
	ptr        map[string]string
	accessKeys map[string]*client.AccessKey
	members    map[string][]client.ProjectResource
	webhooks   map[string]*client.Webhook
	num        int

	// The API before the record set PUT: one value per POST.
	noRecordSetPut bool
	// Orders whose invoice is never paid.
	unpaid bool
	// The API from before instances showed their image, project and
	// subscription, VPCs their region, and webhooks could be read one by one.
	legacyFields bool
	// Writes refused with 409 "busy" before they go through, by "METHOD path".
	busy map[string]int
	// What a resize is short of, in cents (the subscription's balance does
	// not cover it); resizeShortOnApply is the same found only on applying.
	resizeShort        string
	resizeShortOnApply string
	// Backup schedules, by instance; private addresses, by VPC and instance.
	schedules map[string]*client.BackupSchedule
	vpcIPs    map[string]map[string]string
}

func newFakeInfra() *fakeInfra {
	brsp, brbh, usmia := "br-sp", "br-bh", "us-mia"
	desc := func(s string) *string { return &s }
	rate := func(term, r string) client.TermRate { return client.TermRate{Term: term, RateMicroCentsPerHour: &r} }
	notOffered := client.TermRate{Term: "ANNUAL"}
	min := func(n int64) *int64 { return &n }
	return &fakeInfra{
		regions: []client.Region{
			{ID: "3", Name: "São Paulo", RegionName: &brsp, Available: true, IPv4Available: true, IPv6Available: true},
			{ID: "5", Name: "Belo Horizonte", RegionName: &brbh, Available: true, IPv4Available: true, IPv6Available: false},
			{ID: "7", Name: "Miami", RegionName: &usmia, Available: false, IPv4Available: false, IPv6Available: true},
		},
		offerings: []client.Offering{
			{Code: "c4m8", Name: "4 vCPU / 8 GB", Kind: "VM", VCPUs: 4, RAMMB: 8192, BaseDiskGB: 160, Currency: "BRL",
				Rates: []client.TermRate{rate("MONTHLY", "5555555"), rate("ON_DEMAND", "6944444"), notOffered}},
			{Code: "c1m1", Name: "1 vCPU / 1 GB", Description: desc("Small sites and tests"), Kind: "VM", VCPUs: 1, RAMMB: 1024,
				BaseDiskGB: 25, Currency: "BRL", Rates: []client.TermRate{rate("MONTHLY", "1388888"), rate("ON_DEMAND", "1736111")}},
			{Code: "c2m4", Name: "2 vCPU / 4 GB", Kind: "VM", VCPUs: 2, RAMMB: 4096, BaseDiskGB: 80, Currency: "BRL",
				Rates: []client.TermRate{rate("MONTHLY", "2777777"), rate("ANNUAL", "2361111")}},
		},
		images: []client.CatalogImage{
			{Code: "ubuntu-24-04", Name: "Ubuntu 24.04", OSFamily: "linux", Type: "OPERATING_SYSTEM", Category: "Ubuntu",
				SupportsPassword: true, MinDiskGB: min(10)},
			{Code: "debian-12", Name: "Debian 12", OSFamily: "linux", Type: "OPERATING_SYSTEM", Category: "Debian",
				SupportsPassword: true, MinDiskGB: min(10)},
			{Code: "windows-2022", Name: "Windows Server 2022", OSFamily: "windows", Type: "OPERATING_SYSTEM",
				Category: "Windows", SupportsPassword: true, MinDiskGB: min(40), MinRAMMB: min(2048)},
			{Code: "wordpress", Name: "WordPress", OSFamily: "linux", Type: "MARKETPLACE_APP", Category: "CMS",
				Description: desc("WordPress on OpenLiteSpeed"), MinRAMMB: min(1024), MinVCPU: min(1),
				Inputs: []client.AppInput{fakeAppInput("admin_email", "email", "required", "Admin e-mail", nil),
					fakeAppInput("cache", "toggle", "optional", "Page cache", true)}},
		},
		instances:  map[string]*fakeInstance{},
		orders:     map[string]*client.Order{},
		orderSpecs: map[string]map[string]any{},
		orderPolls: map[string]int{},
		operations: map[string]*client.Operation{},
		opPolls:    map[string]int{},
		sshKeys:    map[string]*client.SSHKey{},
		snapshots:  map[string][]*client.Snapshot{},
		firewalls:  map[string]*client.Firewall{},
		vpcs:       map[string]*client.VPC{},
		vpcRegion:  map[string]string{},
		vpcJoining: map[string]map[string]int{},
		zones:      map[string]*client.DNSZone{},
		records:    map[string][]*client.DNSRecord{},
		ptr:        map[string]string{},
		accessKeys: map[string]*client.AccessKey{},
		members:    map[string][]client.ProjectResource{},
		webhooks:   map[string]*client.Webhook{},
		busy:       map[string]int{},
		schedules:  map[string]*client.BackupSchedule{},
		vpcIPs:     map[string]map[string]string{},
	}
}

func fakeAppInput(key, typ, need, label string, def any) client.AppInput {
	in := client.AppInput{Key: key, Type: typ, Need: need, Default: def}
	in.Fallback.Label = label
	return in
}

func (f *fakeAPI) infraRoutes() []fakeRoute {
	return []fakeRoute{
		{"GET", "regions", f.listRegions},
		{"GET", "catalog/offerings", f.listOfferings},
		{"GET", "catalog/images", f.listImages},
		{"POST", "orders", f.createOrder},
		{"GET", "orders/:id", f.getOrder},
		{"GET", "operations/:id", f.getOperation},
		{"GET", "instances", f.listInstances},
		{"GET", "instances/:id", f.getInstance},
		{"PATCH", "instances/:id", f.updateInstance},
		{"DELETE", "instances/:id", f.deleteInstance},
		{"GET", "instances/:id/status", f.instanceStatus},
		{"POST", "instances/:id/power", f.powerInstance},
		{"POST", "instances/:id/reinstall", f.reinstallInstance},
		{"POST", "instances/:id/reset-password", f.resetPassword},
		{"GET", "instances/:id/resize-options", f.resizeOptions},
		{"POST", "instances/:id/resize/quote", f.quoteResize},
		{"POST", "instances/:id/resize", f.resizeInstance},
		{"GET", "instances/:id/backup-schedule", f.getSchedule},
		{"PUT", "instances/:id/backup-schedule", f.setSchedule},
		{"DELETE", "instances/:id/backup-schedule", f.deleteSchedule},
		{"GET", "instances/:id/snapshots", f.listSnapshots},
		{"POST", "instances/:id/snapshots", f.createSnapshot},
		{"DELETE", "instances/:id/snapshots/:sid", f.deleteSnapshot},
		{"GET", "ssh-keys", f.listSSHKeys},
		{"POST", "ssh-keys", f.createSSHKey},
		{"GET", "ssh-keys/:id", f.getSSHKey},
		{"PATCH", "ssh-keys/:id", f.renameSSHKey},
		{"DELETE", "ssh-keys/:id", f.deleteSSHKey},
		{"GET", "firewalls/macros", f.listMacros},
		{"GET", "firewalls", f.listFirewalls},
		{"POST", "firewalls", f.createFirewall},
		{"GET", "firewalls/:id", f.getFirewall},
		{"PATCH", "firewalls/:id", f.updateFirewall},
		{"DELETE", "firewalls/:id", f.deleteFirewall},
		{"POST", "firewalls/:id/rules", f.addRule},
		{"PATCH", "firewalls/:id/rules/:rid", f.updateRule},
		{"DELETE", "firewalls/:id/rules/:rid", f.deleteRule},
		{"POST", "firewalls/:id/commit", f.commitFirewall},
		{"GET", "vpcs", f.listVPCs},
		{"POST", "vpcs", f.createVPC},
		{"GET", "vpcs/:id", f.getVPC},
		{"PATCH", "vpcs/:id", f.updateVPC},
		{"DELETE", "vpcs/:id", f.deleteVPC},
		{"POST", "vpcs/:id/instances", f.attachVPC},
		{"DELETE", "vpcs/:id/instances/:iid", f.detachVPC},
		{"GET", "dns/zones", f.listZones},
		{"POST", "dns/zones", f.createZone},
		{"GET", "dns/zones/:id", f.getZone},
		{"DELETE", "dns/zones/:id", f.deleteZone},
		{"GET", "dns/zones/:id/health", f.zoneHealth},
		{"GET", "dns/zones/:id/records", f.listRecords},
		{"POST", "dns/zones/:id/records", f.upsertRecord},
		{"PUT", "dns/zones/:id/records", f.setRecordSet},
		{"DELETE", "dns/zones/:id/records", f.deleteRecordSet},
		{"GET", "reverse-dns", f.listReverseDNS},
		{"PUT", "reverse-dns/:ip", f.setReverseDNS},
		{"POST", "reverse-dns/:ip/reset", f.resetReverseDNS},
		{"GET", "object-storage/keys", f.listAccessKeys},
		{"POST", "object-storage/keys", f.createAccessKey},
		{"DELETE", "object-storage/keys/:id", f.revokeAccessKey},
		{"GET", "webhooks", f.listWebhooks},
		{"GET", "webhooks/:id", f.getWebhook},
		{"POST", "webhooks", f.createWebhook},
		{"PATCH", "webhooks/:id", f.updateWebhook},
		{"DELETE", "webhooks/:id", f.deleteWebhook},
		{"GET", "projects/:id/resources", f.listMembers},
		{"POST", "projects/:id/resources", f.addMembers},
		{"DELETE", "projects/:id/resources/:kind/:rid", f.removeMember},
	}
}

// busyRefusal answers a write the test made wait for the instance (409
// "busy"), or one that comes while the instance is still busy.
func (f *fakeAPI) busyRefusal(w http.ResponseWriter, r *http.Request, fi *fakeInstance) bool {
	key := r.Method + " " + r.URL.Path
	if f.infra.busy[key] > 0 {
		f.infra.busy[key]--
		problem(w, 409, "", "Instance is busy: a snapshot is being taken. Wait for it to finish, then try again.", nil)
		return true
	}
	if fi != nil && fi.rescue {
		problem(w, 409, "INSTANCE_IN_RESCUE", "Instance "+fi.inst.ID+" is in rescue mode: leave it first.", nil)
		return true
	}
	if fi != nil && fi.busyPolls > 0 {
		fi.busyPolls--
		problem(w, 409, "VM_OPERATION_IN_PROGRESS", "Another operation (deploy) holds instance "+fi.inst.ID+". Retry once it finishes.", nil)
		return true
	}
	return false
}

func (f *fakeAPI) newOperation(typ, resourceType, resourceID string) *client.Operation {
	op := &client.Operation{ID: f.newID(), Type: typ, Status: "pending", ResourceType: &resourceType,
		ResourceID: &resourceID, CreatedAt: fakeNow}
	f.infra.operations[op.ID] = op
	f.infra.opPolls[op.ID] = 1
	return op
}

func (f *fakeAPI) getOperation(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	op, ok := f.infra.operations[p["id"]]
	if !ok {
		notFound(w, "", "Operation")
		return
	}
	if f.infra.opPolls[op.ID] > 0 {
		f.infra.opPolls[op.ID]--
		op.Status = "in_progress"
	} else if op.Status != "failed" {
		op.Status = "completed"
	}
	writeJSON(w, 200, op)
}

// ── Catalog ─────────────────────────────────────────────────────────────────

func (f *fakeAPI) listRegions(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	writeJSON(w, 200, page(f.infra.regions))
}

func (f *fakeAPI) listOfferings(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	writeJSON(w, 200, page(f.infra.offerings))
}

func (f *fakeAPI) listImages(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	writeJSON(w, 200, page(f.infra.images))
}

func (f *fakeAPI) region(id string) *client.Region {
	for i := range f.infra.regions {
		if f.infra.regions[i].ID == id {
			return &f.infra.regions[i]
		}
	}
	return nil
}

func (f *fakeAPI) offering(code string) *client.Offering {
	for i := range f.infra.offerings {
		if f.infra.offerings[i].Code == code {
			return &f.infra.offerings[i]
		}
	}
	return nil
}

func (f *fakeAPI) catalogImage(code string) *client.CatalogImage {
	for i := range f.infra.images {
		if f.infra.images[i].Code == code {
			return &f.infra.images[i]
		}
	}
	return nil
}

// ── Orders ──────────────────────────────────────────────────────────────────

// fieldProblem is a refusal of one field, as the API's validation (422) or
// a service (400) names it.
func fieldProblem(w http.ResponseWriter, status int, field, detail string) {
	codes := map[int]string{400: "invalid_request", 404: "not_found", 409: "conflict", 422: "validation_failed"}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": codes[status], "detail": detail,
		"request_id": "req-fake", "errors": []any{map[string]any{"field": field, "issues": []string{detail}, "code": "invalid"}}})
}

func (f *fakeAPI) createOrder(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "region_id", "offering_code", "term", "image", "name", "password", "subscription_id", "payment",
		"initial_credit_cents", "main_disk_extra_gb", "extra_disks", "addons", "app_inputs", "networking", "ssh_key_ids",
		"user_data", "project_id", "coupon_code") {
		return
	}
	regionID, ok := asInt(body["region_id"])
	if !ok || f.region(strconv.FormatInt(regionID, 10)) == nil {
		fieldProblem(w, 400, "region_id", "No such region (GET /v1/regions).")
		return
	}
	if f.offering(fmt.Sprint(body["offering_code"])) == nil {
		fieldProblem(w, 400, "offering_code", "No such offering (GET /v1/catalog/offerings).")
		return
	}
	image, _ := body["image"].(map[string]any)
	custom := image["type"] == "client_image"
	switch {
	case image["type"] == "template":
		if f.catalogImage(fmt.Sprint(image["code"])) == nil {
			problem(w, 404, "", fmt.Sprintf("No image with code `%v` (GET /v1/catalog/images).", image["code"]), nil)
			return
		}
	case custom:
		if !isUUID(fmt.Sprint(image["image_id"])) {
			fieldProblem(w, 400, "image.image_id", "Not one of your images.")
			return
		}
	default:
		fieldProblem(w, 422, "image.type", "type must be one of the following values: template, byo_iso, client_image")
		return
	}
	if custom && (body["ssh_key_ids"] != nil || body["user_data"] != nil || body["app_inputs"] != nil) {
		problem(w, 400, "", "SSH keys, user data and app inputs are for catalog images: a custom image brings its own.", nil)
		return
	}
	for _, k := range strList(body["ssh_key_ids"]) {
		if _, ok := f.infra.sshKeys[k]; !ok {
			fieldProblem(w, 400, "ssh_key_ids", "No such SSH key in this account: "+k+".")
			return
		}
	}
	if pid, ok := body["project_id"].(string); ok {
		if _, ok := f.projects[pid]; !ok {
			notFound(w, "SERVERLESS_PROJECT_NOT_FOUND", "Project")
			return
		}
	}
	if net, ok := body["networking"].(map[string]any); ok {
		if net["mode"] == "private" && net["vpc_id"] == nil {
			fieldProblem(w, 400, "networking.vpc_id", "A private instance needs vpc_id.")
			return
		}
		if v, ok := asInt(net["vpc_id"]); ok {
			if _, ok := f.infra.vpcs[strconv.FormatInt(v, 10)]; !ok {
				fieldProblem(w, 400, "networking.vpc_id", "No such VPC.")
				return
			}
		}
	}
	sub, _ := body["subscription_id"].(string)
	payment, _ := body["payment"].(string)
	if payment == "balance" && sub == "" {
		fieldProblem(w, 400, "payment", "payment = balance needs a subscription_id: a new subscription is always invoiced.")
		return
	}
	f.infra.num++
	orderID := f.newID()
	o := &client.Order{ID: &orderID, CreatedAt: optString(fakeNow)}
	f.infra.orders[orderID] = o
	f.infra.orderSpecs[orderID] = body
	if sub != "" && payment != "invoice" {
		o.Status = "provisioned"
		o.SubscriptionID = &sub
		o.InstanceIDs = []string{f.provision(body).inst.ID}
		writeJSON(w, 201, o)
		return
	}
	newSub := fmt.Sprintf("sub-%d", f.infra.num)
	if sub != "" {
		newSub = sub
	}
	invoice, amount, link := fmt.Sprintf("inv-%d", f.infra.num), "5000", "https://pay.example.com/inv"
	o.Status, o.SubscriptionID, o.InstanceIDs = "pending_payment", &newSub, []string{}
	spec := map[string]any{}
	for k, v := range body {
		spec[k] = v
	}
	spec["subscription_id"] = newSub
	f.infra.orderSpecs[orderID] = spec
	o.Payment = &client.OrderPayment{InvoiceID: &invoice, AmountCents: &amount, PayURL: &link}
	f.infra.orderPolls[orderID] = 1
	writeJSON(w, 202, o)
}

func (f *fakeAPI) getOrder(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	o, ok := f.infra.orders[p["id"]]
	if !ok {
		notFound(w, "", "Order")
		return
	}
	if o.Status == "pending_payment" && !f.infra.unpaid {
		if f.infra.orderPolls[*o.ID] > 0 {
			f.infra.orderPolls[*o.ID]--
		} else {
			// Paid: built.
			o.Status = "provisioned"
			o.InstanceIDs = []string{f.provision(f.infra.orderSpecs[*o.ID]).inst.ID}
		}
	}
	answer := *o
	answer.Payment = nil
	if o.Payment != nil {
		pay := *o.Payment
		pay.PayURL = nil // shown only when the order is placed
		answer.Payment = &pay
	}
	writeJSON(w, 200, answer)
}

// provision builds the instance an order describes.
func (f *fakeAPI) provision(body map[string]any) *fakeInstance {
	f.infra.num++
	n := f.infra.num
	id := strconv.Itoa(4000 + n)
	regionID, _ := asInt(body["region_id"])
	region := f.region(strconv.FormatInt(regionID, 10))
	off := f.offering(fmt.Sprint(body["offering_code"]))
	extra, _ := asInt(body["main_disk_extra_gb"])
	vcpus, ram, disk := off.VCPUs, off.RAMMB, off.BaseDiskGB+extra
	code := off.Code
	hostname := "vps-" + id + ".mwcloud.example"
	inst := client.Instance{ID: id, Name: trimOrNull(body["name"]), PowerStateCached: "running",
		Region:   &client.InstanceRegion{ID: region.ID, Name: region.Name, RegionName: region.RegionName},
		Specs:    client.InstanceSpecs{VCPUs: &vcpus, RAMMB: &ram, DiskGB: &disk},
		Hostname: &hostname, OfferingCode: &code, CreatedAt: optString(fakeNow), IPAddresses: []client.InstanceIP{}}
	private := false
	if net, ok := body["networking"].(map[string]any); ok {
		private = net["mode"] == "private"
		if v, ok := asInt(net["vpc_id"]); ok {
			vpcID := strconv.FormatInt(v, 10)
			inst.VPCID = &vpcID
			vpc := f.infra.vpcs[vpcID]
			vpc.InstanceIDs = append(vpc.InstanceIDs, id)
		}
	}
	image, _ := body["image"].(map[string]any)
	fi := &fakeInstance{inst: inst, power: "running", busyPolls: 2, sshKeys: strList(body["ssh_key_ids"]),
		mainExtra: extra, addons: map[string]int64{}}
	if !private {
		f.addAddress(fi, "ipv4", true)
		f.addAddress(fi, "ipv6", false)
	}
	for _, d := range asList(body["extra_disks"]) {
		size, _ := asInt(d["size_gb"])
		fi.disks = append(fi.disks, size)
	}
	for _, a := range asList(body["addons"]) {
		q, ok := asInt(a["quantity"])
		if !ok {
			q = 1
		}
		fi.addons[fmt.Sprint(a["code"])] = q
	}
	if image["type"] == "client_image" {
		fi.customImage = fmt.Sprint(image["image_id"])
	} else {
		fi.image = fmt.Sprint(image["code"])
	}
	fi.userData, _ = body["user_data"].(string)
	fi.subscription, _ = body["subscription_id"].(string)
	fi.password, _ = body["password"].(string)
	f.infra.instances[id] = fi
	if pid, ok := body["project_id"].(string); ok {
		f.join(pid, f.instanceMember(fi))
	}
	return fi
}

// ── Instances ───────────────────────────────────────────────────────────────

func (f *fakeAPI) instance(w http.ResponseWriter, id string) *fakeInstance {
	fi, ok := f.infra.instances[id]
	if !ok {
		problem(w, 404, "", "Instance not found.", nil)
		return nil
	}
	return fi
}

func (f *fakeAPI) instanceView(fi *fakeInstance) any {
	i := fi.inst
	i.PowerStateCached = fi.power
	i.IPAddresses = append([]client.InstanceIP{}, fi.inst.IPAddresses...)
	i.Image, i.CustomImageID, i.SubscriptionID = optNonEmpty(fi.image), optNonEmpty(fi.customImage), optNonEmpty(fi.subscription)
	for pid, members := range f.infra.members {
		for _, m := range members {
			if m.Kind == "INSTANCE" && m.ID == fi.inst.ID {
				project := pid
				i.ProjectID = &project
			}
		}
	}
	if !f.infra.legacyFields {
		return i
	}
	raw, _ := json.Marshal(i)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	for _, k := range []string{"image", "custom_image_id", "project_id", "subscription_id"} {
		delete(out, k)
	}
	return out
}

func optNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (f *fakeAPI) listInstances(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	var ids []int
	for id := range f.infra.instances {
		n, _ := strconv.Atoi(id)
		ids = append(ids, n)
	}
	sort.Ints(ids)
	out := []any{}
	for _, n := range ids {
		out = append(out, f.instanceView(f.infra.instances[strconv.Itoa(n)]))
	}
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) getInstance(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if fi := f.instance(w, p["id"]); fi != nil {
		writeJSON(w, 200, f.instanceView(fi))
	}
}

func (f *fakeAPI) updateInstance(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description") {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	if v, ok := body["name"]; ok {
		if s, _ := v.(string); len(s) > 120 {
			fieldProblem(w, 422, "name", "name must be shorter than or equal to 120 characters")
			return
		}
		fi.inst.Name = trimOrNull(v)
	}
	if v, ok := body["description"]; ok {
		fi.inst.Description = trimOrNull(v)
	}
	writeJSON(w, 200, f.instanceView(fi))
}

func (f *fakeAPI) instanceStatus(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	fresh := r.URL.Query().Get("fresh") == "true"
	busy := fi.busyPolls > 0
	if busy && fresh {
		fi.busyPolls--
	}
	progress := 100.0
	if busy {
		progress = 40
	}
	writeJSON(w, 200, map[string]any{"object": "instance_status", "instance_id": fi.inst.ID, "power_state": fi.power,
		"suspended": false, "busy": busy, "deployment_progress": progress, "cached": !fresh})
}

func (f *fakeAPI) powerInstance(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "action") {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil || f.busyRefusal(w, r, fi) {
		return
	}
	switch body["action"] {
	case "start", "reboot":
		fi.power = "running"
	case "stop":
		fi.power = "stopped"
	default:
		fieldProblem(w, 422, "action", "action must be one of the following values: start, stop, reboot")
		return
	}
	writeJSON(w, 202, map[string]any{"object": "task", "instance_id": fi.inst.ID, "action": body["action"], "task_id": "task_x"})
}

func (f *fakeAPI) reinstallInstance(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "image", "custom_image_id", "password", "ssh_key_ids", "user_data", "app_inputs") {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	custom, _ := body["custom_image_id"].(string)
	if custom != "" {
		if body["image"] != nil || body["ssh_key_ids"] != nil || body["user_data"] != nil || body["password"] != nil {
			problem(w, 400, "", "A custom image brings its own cloud-init configuration: send `custom_image_id` alone.", nil)
			return
		}
	} else if f.catalogImage(fmt.Sprint(body["image"])) == nil {
		problem(w, 404, "", fmt.Sprintf("No image with code `%v` (GET /v1/catalog/images).", body["image"]), nil)
		return
	}
	if f.busyRefusal(w, r, fi) {
		return
	}
	fi.reinstalls++
	fi.busyPolls, fi.power = 2, "running"
	if custom != "" {
		fi.customImage, fi.image = custom, ""
	} else {
		fi.image, fi.customImage = fmt.Sprint(body["image"]), ""
		if pw, ok := body["password"].(string); ok {
			fi.password = pw
		}
		if _, ok := body["ssh_key_ids"]; ok {
			fi.sshKeys = strList(body["ssh_key_ids"])
		}
		if v, ok := body["user_data"].(string); ok {
			fi.userData = v
		}
	}
	op := f.newOperation("vm_deploy", "instance", fi.inst.ID)
	writeJSON(w, 202, map[string]any{"object": "operation_accepted", "action": "reinstall", "resource_type": "instance",
		"resource_id": fi.inst.ID, "instance_id": fi.inst.ID, "operation_id": op.ID})
}

func (f *fakeAPI) resetPassword(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "username", "password") {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	if fi.power != "running" {
		problem(w, 409, "", "The instance must be running for its guest agent to reset a password.", nil)
		return
	}
	fi.password, _ = body["password"].(string)
	writeJSON(w, 200, map[string]any{"object": "password_reset", "instance_id": fi.inst.ID, "username": "root", "password": nil})
}

func (f *fakeAPI) deleteInstance(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	if r.URL.Query().Get("confirm") != fi.inst.ID {
		problem(w, 400, "", "Pass ?confirm=<instance id> to destroy it.", nil)
		return
	}
	id := fi.inst.ID
	delete(f.infra.instances, id)
	delete(f.infra.snapshots, id)
	for _, v := range f.infra.vpcs {
		v.InstanceIDs = without(v.InstanceIDs, id)
	}
	for _, fw := range f.infra.firewalls {
		fw.InstanceIDs = without(fw.InstanceIDs, id)
	}
	f.leave("INSTANCE", id)
	writeJSON(w, 200, map[string]any{"object": "instance_deletion", "instance_id": id, "deleted": true, "message": "Destroyed."})
}

func without(items []string, drop string) []string {
	out := []string{}
	for _, s := range items {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

// ── Snapshots ───────────────────────────────────────────────────────────────

var fakeSnapshotName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,39}$`)

func (f *fakeAPI) listSnapshots(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.instance(w, p["id"]) == nil {
		return
	}
	data := []client.Snapshot{}
	for _, s := range f.infra.snapshots[p["id"]] {
		data = append(data, *s)
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data, "max_snapshots": 3, "remaining": 3 - len(data)})
}

func (f *fakeAPI) createSnapshot(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description", "include_ram") {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	name, _ := body["name"].(string)
	if !fakeSnapshotName.MatchString(name) {
		fieldProblem(w, 422, "name", "name must start with a letter and use letters, digits, - or _ (2 to 40)")
		return
	}
	if f.busyRefusal(w, r, nil) {
		return
	}
	list := f.infra.snapshots[fi.inst.ID]
	for _, s := range list {
		if s.Name == name {
			problem(w, 400, "", fmt.Sprintf("Snapshot with name '%s' already exists for this VM", name), nil)
			return
		}
	}
	if len(list) >= 3 {
		problem(w, 403, "", "The plan allows 3 snapshots: delete one first.", nil)
		return
	}
	ram, _ := body["include_ram"].(bool)
	s := &client.Snapshot{ID: f.newID(), InstanceID: fi.inst.ID, Name: name, Description: trimOrNull(body["description"]),
		IncludeRAM: ram, CreatedAt: optString(fakeNow), ExpiresAt: optString("2026-11-25T12:00:00.000Z")}
	f.infra.snapshots[fi.inst.ID] = append(list, s)
	writeJSON(w, 201, s)
}

func (f *fakeAPI) deleteSnapshot(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	if f.instance(w, p["id"]) == nil || f.busyRefusal(w, r, nil) {
		return
	}
	list := f.infra.snapshots[p["id"]]
	for i, s := range list {
		if s.ID == p["sid"] {
			f.infra.snapshots[p["id"]] = append(list[:i:i], list[i+1:]...)
			w.WriteHeader(204)
			return
		}
	}
	problem(w, 404, "", "No such snapshot on this instance.", nil)
}

// ── SSH keys ────────────────────────────────────────────────────────────────

var fakeKeyTypes = map[string]bool{"ssh-ed25519": true, "ssh-rsa": true, "ecdsa-sha2-nistp256": true,
	"ecdsa-sha2-nistp384": true, "ecdsa-sha2-nistp521": true, "sk-ssh-ed25519@openssh.com": true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true}

func (f *fakeAPI) listSSHKeys(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.SSHKey{}
	for _, k := range f.infra.sshKeys {
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) createSSHKey(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name", "public_key") {
		return
	}
	name := strings.TrimSpace(fmt.Sprint(body["name"]))
	if name == "" || len(name) > 64 {
		fieldProblem(w, 400, "name", "Name the key (up to 64 characters).")
		return
	}
	fields := strings.Fields(fmt.Sprint(body["public_key"]))
	if len(fields) < 2 || !fakeKeyTypes[fields[0]] {
		fieldProblem(w, 400, "public_key", "Key type is not accepted. Use ssh-ed25519 (recommended), ssh-rsa or ecdsa-sha2-nistp256/384/521.")
		return
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		fieldProblem(w, 400, "public_key", "The key is not valid base64 after its type.")
		return
	}
	sum := sha256.Sum256(blob)
	fp := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	for _, k := range f.infra.sshKeys {
		if k.Fingerprint == fp {
			problem(w, 409, "", fmt.Sprintf("This key is already saved as %q (%s).", k.Name, fp), nil)
			return
		}
	}
	k := &client.SSHKey{ID: f.newID(), Name: name, Type: fields[0], Fingerprint: fp,
		PublicKey: strings.Join(fields, " "), CreatedAt: optString(fakeNow)}
	f.infra.sshKeys[k.ID] = k
	writeJSON(w, 201, k)
}

func (f *fakeAPI) getSSHKey(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	k, ok := f.infra.sshKeys[p["id"]]
	if !ok {
		problem(w, 404, "", "SSH key not found.", nil)
		return
	}
	writeJSON(w, 200, k)
}

func (f *fakeAPI) renameSSHKey(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name") {
		return
	}
	k, ok := f.infra.sshKeys[p["id"]]
	if !ok {
		problem(w, 404, "", "SSH key not found.", nil)
		return
	}
	k.Name = strings.TrimSpace(fmt.Sprint(body["name"]))
	writeJSON(w, 200, k)
}

func (f *fakeAPI) deleteSSHKey(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if _, ok := f.infra.sshKeys[p["id"]]; !ok {
		problem(w, 404, "", "SSH key not found.", nil)
		return
	}
	delete(f.infra.sshKeys, p["id"])
	w.WriteHeader(204)
}

// ── Firewalls ───────────────────────────────────────────────────────────────

var fakeMacros = map[string][2]string{"SSH": {"tcp", "22"}, "HTTP": {"tcp", "80"}, "HTTPS": {"tcp", "443"}}

func (f *fakeAPI) listMacros(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	writeJSON(w, 200, []client.FirewallMacro{
		{Name: "HTTP", Description: "Web traffic (TCP 80)"},
		{Name: "HTTPS", Description: "Secure web traffic (TCP 443)"},
		{Name: "SSH", Description: "Secure shell (TCP 22)"},
	})
}

func (f *fakeAPI) firewall(w http.ResponseWriter, id string) *client.Firewall {
	fw, ok := f.infra.firewalls[id]
	if !ok {
		problem(w, 404, "", "Firewall not found.", nil)
		return nil
	}
	return fw
}

func (f *fakeAPI) listFirewalls(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.Firewall{}
	for _, fw := range f.infra.firewalls {
		out = append(out, *fw)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) createFirewall(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description") {
		return
	}
	fw := &client.Firewall{ID: f.newID(), Name: strings.TrimSpace(fmt.Sprint(body["name"])), Description: trimOrNull(body["description"]),
		Enabled: true, PolicyIn: "DROP", PolicyOut: "ACCEPT", Rules: []client.FirewallRule{}, InstanceIDs: []string{},
		CreatedAt: optString(fakeNow)}
	f.infra.firewalls[fw.ID] = fw
	writeJSON(w, 201, fw)
}

func (f *fakeAPI) getFirewall(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if fw := f.firewall(w, p["id"]); fw != nil {
		writeJSON(w, 200, fw)
	}
}

func (f *fakeAPI) updateFirewall(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description", "enabled", "policy_in", "policy_out") {
		return
	}
	fw := f.firewall(w, p["id"])
	if fw == nil {
		return
	}
	if v, ok := body["name"]; ok {
		fw.Name = strings.TrimSpace(fmt.Sprint(v))
	}
	if v, ok := body["description"]; ok {
		fw.Description = trimOrNull(v)
	}
	if v, ok := body["enabled"].(bool); ok {
		fw.Enabled = v
	}
	if v, ok := body["policy_in"].(string); ok {
		fw.PolicyIn = v
	}
	if v, ok := body["policy_out"].(string); ok {
		fw.PolicyOut = v
	}
	fw.HasPendingChanges = true
	writeJSON(w, 200, fw)
}

func (f *fakeAPI) deleteFirewall(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.firewall(w, p["id"]) == nil {
		return
	}
	delete(f.infra.firewalls, p["id"])
	w.WriteHeader(204)
}

var ruleFields = []string{"direction", "action", "macro", "protocol", "source", "destination", "source_port", "dest_port",
	"enabled", "comment"}

// setRuleFields writes the fields a body has onto a rule (null clears one),
// as the API keeps them: a macro fills its protocol and port, ports keep
// FROM:TO.
func setRuleFields(rule *client.FirewallRule, body map[string]any) {
	for k, v := range body {
		s := trimOrNull(v)
		switch k {
		case "direction":
			rule.Direction = strings.ToLower(fmt.Sprint(v))
		case "action":
			rule.Action = strings.ToUpper(fmt.Sprint(v))
		case "macro":
			if s != nil {
				up := strings.ToUpper(*s)
				s = &up
			}
			rule.Macro = s
		case "protocol":
			if s != nil {
				low := strings.ToLower(*s)
				s = &low
			}
			rule.Protocol = s
		case "source":
			rule.Source = s
		case "destination":
			rule.Destination = s
		case "source_port":
			if s != nil {
				n := normPorts(*s)
				s = &n
			}
			rule.SourcePort = s
		case "dest_port":
			if s != nil {
				n := normPorts(*s)
				s = &n
			}
			rule.DestPort = s
		case "enabled":
			b, ok := v.(bool)
			rule.Enabled = !ok || b
		case "comment":
			rule.Comment = s
		}
	}
	if rule.Macro != nil {
		if m, ok := fakeMacros[*rule.Macro]; ok {
			if rule.Protocol == nil {
				rule.Protocol = &m[0]
			}
			if rule.DestPort == nil {
				rule.DestPort = &m[1]
			}
		}
	}
}

func (f *fakeAPI) addRule(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, ruleFields...) {
		return
	}
	fw := f.firewall(w, p["id"])
	if fw == nil {
		return
	}
	if body["direction"] == nil || body["action"] == nil {
		fieldProblem(w, 422, "direction", "direction and action are required")
		return
	}
	rule := client.FirewallRule{ID: f.newID(), Position: int64(len(fw.Rules) + 1), Enabled: true}
	setRuleFields(&rule, body)
	fw.Rules = append(fw.Rules, rule)
	fw.HasPendingChanges = true
	writeJSON(w, 201, fw)
}

func (f *fakeAPI) updateRule(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, ruleFields...) {
		return
	}
	fw := f.firewall(w, p["id"])
	if fw == nil {
		return
	}
	for i := range fw.Rules {
		if fw.Rules[i].ID == p["rid"] {
			setRuleFields(&fw.Rules[i], body)
			fw.HasPendingChanges = true
			writeJSON(w, 200, fw)
			return
		}
	}
	problem(w, 404, "", "Rule not found.", nil)
}

func (f *fakeAPI) deleteRule(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	fw := f.firewall(w, p["id"])
	if fw == nil {
		return
	}
	for i := range fw.Rules {
		if fw.Rules[i].ID == p["rid"] {
			fw.Rules = append(fw.Rules[:i:i], fw.Rules[i+1:]...)
			for j := range fw.Rules {
				fw.Rules[j].Position = int64(j + 1)
			}
			fw.HasPendingChanges = true
			w.WriteHeader(204)
			return
		}
	}
	problem(w, 404, "", "Rule not found.", nil)
}

func (f *fakeAPI) commitFirewall(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "instance_ids") {
		return
	}
	fw := f.firewall(w, p["id"])
	if fw == nil {
		return
	}
	if _, ok := body["instance_ids"].([]any); !ok {
		fieldProblem(w, 422, "instance_ids", "instance_ids must be an array")
		return
	}
	ids := strList(body["instance_ids"])
	for _, id := range ids {
		if _, ok := f.infra.instances[id]; !ok {
			problem(w, 404, "", "Instance "+id+" not found.", nil)
			return
		}
	}
	sort.Strings(ids)
	fw.InstanceIDs = ids
	fw.HasPendingChanges = false
	op := f.newOperation("firewall_commit", "firewall", fw.ID)
	writeJSON(w, 202, map[string]any{"object": "firewall_commit", "firewall_id": fw.ID, "operation_id": op.ID})
}

// ── VPCs ────────────────────────────────────────────────────────────────────

func (f *fakeAPI) vpc(w http.ResponseWriter, id string) *client.VPC {
	v, ok := f.infra.vpcs[id]
	if !ok {
		problem(w, 404, "", "VPC not found.", nil)
		return nil
	}
	return v
}

// vpcView is a VPC as a GET shows it: an instance attached a moment ago
// shows once its NIC is in, with its address.
func (f *fakeAPI) vpcView(v *client.VPC) client.VPC {
	out := *v
	out.InstanceIDs, out.Members = []string{}, []client.VPCMember{}
	if region := f.infra.vpcRegion[v.ID]; region != "" && !f.infra.legacyFields {
		out.RegionID = &region
	}
	for _, id := range v.InstanceIDs {
		if f.infra.vpcJoining[v.ID][id] > 0 {
			f.infra.vpcJoining[v.ID][id]--
			continue
		}
		out.InstanceIDs = append(out.InstanceIDs, id)
		m := client.VPCMember{InstanceID: id, Status: "online"}
		if ip, ok := f.infra.vpcIPs[v.ID][id]; ok {
			m.PrivateIP = &ip
		}
		out.Members = append(out.Members, m)
	}
	return out
}

// vpcAddress is the private address an instance gets on a VPC: the one
// asked for, or the next free one in its subnet.
func (f *fakeAPI) vpcAddress(w http.ResponseWriter, v *client.VPC, instanceID, asked string) (string, bool) {
	pfx := netip.MustParsePrefix(*v.Subnet)
	taken := map[string]bool{}
	for id, ip := range f.infra.vpcIPs[v.ID] {
		if id != instanceID {
			taken[ip] = true
		}
	}
	if asked != "" {
		a, err := netip.ParseAddr(asked)
		if err != nil || !pfx.Contains(a) {
			problem(w, 400, "", asked+" is not in the VPC's subnet "+*v.Subnet+".", nil)
			return "", false
		}
		if taken[a.String()] {
			problem(w, 409, "", a.String()+" is taken on this VPC.", nil)
			return "", false
		}
		return a.String(), true
	}
	for a := pfx.Addr().Next().Next(); pfx.Contains(a); a = a.Next() {
		if !taken[a.String()] {
			return a.String(), true
		}
	}
	problem(w, 409, "", "The VPC's subnet is full.", nil)
	return "", false
}

func (f *fakeAPI) listVPCs(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.VPC{}
	for _, v := range f.infra.vpcs {
		out = append(out, f.vpcView(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) createVPC(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description", "region", "subnet") {
		return
	}
	region, ok := asInt(body["region"])
	if !ok || region < 1 {
		fieldProblem(w, 422, "region", "region must be an integer number")
		return
	}
	rg := f.region(strconv.FormatInt(region, 10))
	if rg == nil || !rg.Available {
		problem(w, 400, "", "Private networks (VPC) are not available in this region.", nil)
		return
	}
	subnet := "10.200.0.0/24"
	if s, ok := body["subnet"].(string); ok {
		pfx, err := netip.ParsePrefix(s)
		if err != nil || !privateRange(pfx) {
			problem(w, 400, "", "VPC subnet is not within an accepted private subnet", nil)
			return
		}
		subnet = pfx.Masked().String()
	}
	f.infra.num++
	id := strconv.Itoa(100 + f.infra.num)
	v := &client.VPC{ID: id, Name: strings.TrimSpace(fmt.Sprint(body["name"])), Description: trimOrNull(body["description"]),
		Subnet: &subnet, InstanceIDs: []string{}, CreatedAt: optString(fakeNow)}
	f.infra.vpcs[id] = v
	f.infra.vpcRegion[id] = rg.ID
	writeJSON(w, 201, f.vpcView(v))
}

func (f *fakeAPI) getVPC(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if v := f.vpc(w, p["id"]); v != nil {
		writeJSON(w, 200, f.vpcView(v))
	}
}

func (f *fakeAPI) updateVPC(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "description") {
		return
	}
	v := f.vpc(w, p["id"])
	if v == nil {
		return
	}
	if len(body) == 0 {
		problem(w, 400, "", "Provide `name` or `description`.", nil)
		return
	}
	if n, ok := body["name"]; ok {
		v.Name = strings.TrimSpace(fmt.Sprint(n))
	}
	if d, ok := body["description"]; ok {
		v.Description = trimOrNull(d)
	}
	writeJSON(w, 200, f.vpcView(v))
}

func (f *fakeAPI) deleteVPC(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	v := f.vpc(w, p["id"])
	if v == nil {
		return
	}
	if v.Default {
		problem(w, 400, "", "Default VPCs cannot be deleted", nil)
		return
	}
	for _, id := range v.InstanceIDs {
		if fi, ok := f.infra.instances[id]; ok {
			fi.inst.VPCID = nil
		}
	}
	delete(f.infra.vpcs, v.ID)
	w.WriteHeader(204)
}

func (f *fakeAPI) attachVPC(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "instance_id", "restart", "private_ip") {
		return
	}
	v := f.vpc(w, p["id"])
	if v == nil {
		return
	}
	id, _ := body["instance_id"].(string)
	if !regexp.MustCompile(`^\d{1,10}$`).MatchString(id) {
		fieldProblem(w, 422, "instance_id", "instance_id must match /^\\d{1,10}$/ regular expression")
		return
	}
	fi := f.instance(w, id)
	if fi == nil {
		return
	}
	if fi.inst.Region == nil || fi.inst.Region.ID != f.infra.vpcRegion[v.ID] {
		problem(w, 400, "", "The instance and the VPC must be in the same region.", nil)
		return
	}
	if fi.inst.VPCID != nil && *fi.inst.VPCID != v.ID {
		problem(w, 409, "", "The instance is in another VPC: take it out of that one first.", nil)
		return
	}
	if f.busyRefusal(w, r, fi) {
		return
	}
	asked, _ := body["private_ip"].(string)
	ip, ok := f.vpcAddress(w, v, id, asked)
	if !ok {
		return
	}
	if f.infra.vpcIPs[v.ID] == nil {
		f.infra.vpcIPs[v.ID] = map[string]string{}
	}
	f.infra.vpcIPs[v.ID][id] = ip
	if fi.inst.VPCID == nil {
		v.InstanceIDs = append(v.InstanceIDs, id)
		if f.infra.vpcJoining[v.ID] == nil {
			f.infra.vpcJoining[v.ID] = map[string]int{}
		}
		f.infra.vpcJoining[v.ID][id] = 1
		vpcID := v.ID
		fi.inst.VPCID = &vpcID
	}
	op := f.newOperation("vpc_refresh", "vpc", v.ID)
	writeJSON(w, 202, map[string]any{"object": "operation_accepted", "action": "vpc_attach", "resource_type": "vpc",
		"resource_id": v.ID, "instance_id": id, "operation_id": op.ID})
}

func (f *fakeAPI) detachVPC(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	v := f.vpc(w, p["id"])
	if v == nil {
		return
	}
	fi := f.instance(w, p["iid"])
	if fi == nil {
		return
	}
	if fi.inst.VPCID == nil || *fi.inst.VPCID != v.ID {
		problem(w, 400, "", "The instance is not in this VPC.", nil)
		return
	}
	if f.busyRefusal(w, r, fi) {
		return
	}
	v.InstanceIDs = without(v.InstanceIDs, fi.inst.ID)
	delete(f.infra.vpcIPs[v.ID], fi.inst.ID)
	fi.inst.VPCID = nil
	writeJSON(w, 202, map[string]any{"object": "operation_accepted", "action": "vpc_detach", "resource_type": "vpc",
		"resource_id": v.ID, "instance_id": fi.inst.ID, "operation_id": nil})
}

// ── DNS ─────────────────────────────────────────────────────────────────────

func (f *fakeAPI) zone(w http.ResponseWriter, id string) *client.DNSZone {
	z, ok := f.infra.zones[id]
	if !ok {
		problem(w, 404, "", "DNS zone not found.", nil)
		return nil
	}
	return z
}

func (f *fakeAPI) listZones(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.DNSZone{}
	for _, z := range f.infra.zones {
		out = append(out, *z)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, page(out))
}

func (f *fakeAPI) createZone(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name") {
		return
	}
	name := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["name"])))
	if strings.HasSuffix(name, ".") || !strings.Contains(name, ".") {
		fieldProblem(w, 422, "name", "name must be a domain such as example.com (no scheme, path or trailing dot)")
		return
	}
	for _, z := range f.infra.zones {
		if z.Name == name {
			writeJSON(w, 200, z)
			return
		}
	}
	z := &client.DNSZone{ID: f.newID(), Name: name, CreatedAt: optString(fakeNow)}
	f.infra.zones[z.ID] = z
	f.infra.records[z.ID] = []*client.DNSRecord{
		{Name: name + ".", Type: "NS", TTL: 3600, Values: []string{"ns1.masterdaweb.net.", "ns2.masterdaweb.net."}, DisabledValues: []string{}},
	}
	writeJSON(w, 201, z)
}

func (f *fakeAPI) getZone(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if z := f.zone(w, p["id"]); z != nil {
		writeJSON(w, 200, z)
	}
}

func (f *fakeAPI) deleteZone(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.zone(w, p["id"]) == nil {
		return
	}
	delete(f.infra.zones, p["id"])
	delete(f.infra.records, p["id"])
	w.WriteHeader(204)
}

func (f *fakeAPI) zoneHealth(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	z := f.zone(w, p["id"])
	if z == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"object": "dns_zone_health", "zone_id": z.ID, "delegated": false,
		"nameservers": []string{}, "expected_nameservers": []string{"ns1.masterdaweb.net", "ns2.masterdaweb.net"}})
}

func (f *fakeAPI) listRecords(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.zone(w, p["id"]) == nil {
		return
	}
	out := []client.DNSRecord{}
	for _, rr := range f.infra.records[p["id"]] {
		out = append(out, *rr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name+out[i].Type < out[j].Name+out[j].Type })
	writeJSON(w, 200, page(out))
}

// pdnsName is a record name as PowerDNS keeps it: fully qualified, with its
// trailing dot.
func pdnsName(name, zone string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case name == "@" || name == "" || name == zone || name == zone+".":
		return zone + "."
	case strings.HasSuffix(name, "."):
		return name
	case strings.HasSuffix(name, "."+zone):
		return name + "."
	}
	return name + "." + zone + "."
}

// pdnsValue is a value as PowerDNS keeps it: targets fully qualified (a
// one-label target is in the zone), TXT quoted (in 255-character strings),
// AAAA in its short form.
func pdnsValue(typ, v, zone string) string {
	v = strings.TrimSpace(v)
	fqdn := func(h string) string {
		h = strings.ToLower(h)
		switch {
		case strings.HasSuffix(h, "."):
		case !strings.Contains(h, "."):
			h += "." + zone + "."
		default:
			h += "."
		}
		return h
	}
	switch typ {
	case "CNAME", "NS":
		return fqdn(v)
	case "MX":
		f := strings.Fields(v)
		if len(f) == 2 {
			return f[0] + " " + fqdn(f[1])
		}
	case "SRV":
		f := strings.Fields(v)
		if len(f) == 4 {
			return strings.Join(f[:3], " ") + " " + fqdn(f[3])
		}
	case "AAAA":
		if a, err := netip.ParseAddr(v); err == nil {
			return a.String()
		}
	case "TXT":
		if strings.HasPrefix(v, `"`) {
			return v
		}
		var parts []string
		for len(v) > 255 {
			parts = append(parts, `"`+v[:255]+`"`)
			v = v[255:]
		}
		return strings.Join(append(parts, `"`+v+`"`), " ")
	}
	return v
}

func (f *fakeAPI) putRecordSet(zoneID, name, typ string, ttl int64, values []string) {
	z := f.infra.zones[zoneID]
	fq := pdnsName(name, z.Name)
	norm := []string{}
	for _, v := range values {
		norm = append(norm, pdnsValue(typ, v, z.Name))
	}
	for _, rr := range f.infra.records[zoneID] {
		if rr.Name == fq && rr.Type == typ {
			rr.TTL, rr.Values = ttl, norm
			return
		}
	}
	f.infra.records[zoneID] = append(f.infra.records[zoneID],
		&client.DNSRecord{Name: fq, Type: typ, TTL: ttl, Values: norm, DisabledValues: []string{}})
}

func recordTTL(w http.ResponseWriter, body map[string]any) (int64, bool) {
	ttl := int64(3600)
	if v, ok := body["ttl"]; ok {
		n, ok := asInt(v)
		if !ok || n < 60 || n > 604800 {
			fieldProblem(w, 422, "ttl", "ttl must not be less than 60 and not greater than 604800")
			return 0, false
		}
		ttl = n
	}
	return ttl, true
}

func (f *fakeAPI) upsertRecord(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "type", "content", "ttl", "comment") {
		return
	}
	if f.zone(w, p["id"]) == nil {
		return
	}
	ttl, ok := recordTTL(w, body)
	if !ok {
		return
	}
	f.putRecordSet(p["id"], fmt.Sprint(body["name"]), fmt.Sprint(body["type"]), ttl, []string{fmt.Sprint(body["content"])})
	f.listRecords(w, r, p, nil)
}

func (f *fakeAPI) setRecordSet(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if f.infra.noRecordSetPut {
		problem(w, 404, "", "Cannot PUT "+r.URL.Path, nil)
		return
	}
	if !strict(w, body, "name", "type", "records", "ttl", "comment") {
		return
	}
	if f.zone(w, p["id"]) == nil {
		return
	}
	values := strList(body["records"])
	if len(values) < 1 || len(values) > 100 {
		fieldProblem(w, 422, "records", "records must contain between 1 and 100 elements")
		return
	}
	ttl, ok := recordTTL(w, body)
	if !ok {
		return
	}
	f.putRecordSet(p["id"], fmt.Sprint(body["name"]), fmt.Sprint(body["type"]), ttl, values)
	f.listRecords(w, r, p, nil)
}

func (f *fakeAPI) deleteRecordSet(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "name", "type") {
		return
	}
	z := f.zone(w, p["id"])
	if z == nil {
		return
	}
	fq := pdnsName(fmt.Sprint(body["name"]), z.Name)
	out := []*client.DNSRecord{}
	for _, rr := range f.infra.records[z.ID] {
		if !(rr.Name == fq && rr.Type == body["type"]) {
			out = append(out, rr)
		}
	}
	f.infra.records[z.ID] = out
	w.WriteHeader(204)
}

// resolves says whether a name has an A or AAAA record with this address in
// one of the account's zones: the forward half of a PTR.
func (f *fakeAPI) resolves(hostname, ip string) bool {
	want := strings.ToLower(strings.TrimSuffix(hostname, ".")) + "."
	for _, rrs := range f.infra.records {
		for _, rr := range rrs {
			if rr.Name != want || (rr.Type != "A" && rr.Type != "AAAA") {
				continue
			}
			for _, v := range rr.Values {
				if normalizeIP(v) == normalizeIP(ip) {
					return true
				}
			}
		}
	}
	return false
}

// ── Reverse DNS ─────────────────────────────────────────────────────────────

func defaultPTR(ip string) string {
	return strings.NewReplacer(".", "-", ":", "-").Replace(ip) + ".static.mwcloud.example"
}

func (f *fakeAPI) reverseEntry(fi *fakeInstance, a client.InstanceIP) map[string]any {
	version, host := 4, defaultPTR(a.Address)
	if a.Version == "ipv6" {
		version = 6
	}
	if h, ok := f.infra.ptr[a.Address]; ok {
		host = h
	}
	return map[string]any{"object": "reverse_dns", "ip": a.Address, "version": version, "hostname": host,
		"default_hostname": defaultPTR(a.Address), "instance_id": fi.inst.ID, "editable": true, "editable_at": nil}
}

func (f *fakeAPI) address(w http.ResponseWriter, raw string) (*fakeInstance, client.InstanceIP, bool) {
	for _, fi := range f.infra.instances {
		for _, a := range fi.inst.IPAddresses {
			if normalizeIP(a.Address) == normalizeIP(raw) {
				return fi, a, true
			}
		}
	}
	problem(w, 404, "", raw+" is not one of your addresses.", nil)
	return nil, client.InstanceIP{}, false
}

func (f *fakeAPI) listReverseDNS(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	data := []map[string]any{}
	var ids []string
	for id := range f.infra.instances {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fi := f.infra.instances[id]
		for _, a := range fi.inst.IPAddresses {
			data = append(data, f.reverseEntry(fi, a))
		}
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (f *fakeAPI) setReverseDNS(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "hostname") {
		return
	}
	fi, a, ok := f.address(w, p["ip"])
	if !ok {
		return
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fmt.Sprint(body["hostname"])), "."))
	if len(host) < 3 {
		fieldProblem(w, 422, "hostname", "hostname must be longer than or equal to 3 characters")
		return
	}
	if !f.resolves(host, a.Address) {
		problem(w, 400, "", fmt.Sprintf("%s does not resolve to %s: add its A or AAAA record first.", host, a.Address), nil)
		return
	}
	f.infra.ptr[a.Address] = host
	writeJSON(w, 200, f.reverseEntry(fi, a))
}

func (f *fakeAPI) resetReverseDNS(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	fi, a, ok := f.address(w, p["ip"])
	if !ok {
		return
	}
	delete(f.infra.ptr, a.Address)
	writeJSON(w, 200, f.reverseEntry(fi, a))
}

// ── Object Storage access keys ──────────────────────────────────────────────

func (f *fakeAPI) listAccessKeys(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.AccessKey{}
	for _, k := range f.infra.accessKeys {
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, out)
}

func (f *fakeAPI) createAccessKey(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "name", "access", "buckets", "expires_at") {
		return
	}
	access := "read_write"
	if a, ok := body["access"].(string); ok {
		access = a
	}
	var buckets []string
	if _, ok := body["buckets"]; ok {
		buckets = strList(body["buckets"])
		for _, b := range buckets {
			if _, ok := f.buckets[b]; !ok {
				notFound(w, "STORAGE_BUCKET_NOT_FOUND", "Bucket "+b)
				return
			}
		}
		sort.Strings(buckets)
	}
	f.infra.num++
	k := &client.AccessKey{ID: f.newID(), Name: strings.TrimSpace(fmt.Sprint(body["name"])),
		AccessKeyID: fmt.Sprintf("MWAK%06d", f.infra.num), Access: access, Buckets: buckets,
		ExpiresAt: optString(fmt.Sprint(body["expires_at"])), CreatedAt: fakeNow}
	if body["expires_at"] == nil {
		k.ExpiresAt = nil
	}
	f.infra.accessKeys[k.ID] = k
	writeJSON(w, 201, map[string]any{"key": k, "secret_access_key": fmt.Sprintf("mw-secret-%d", f.infra.num)})
}

func (f *fakeAPI) revokeAccessKey(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if _, ok := f.infra.accessKeys[p["id"]]; !ok {
		notFound(w, "", "Access key")
		return
	}
	delete(f.infra.accessKeys, p["id"])
	w.WriteHeader(204)
}

// ── What projects group ─────────────────────────────────────────────────────

func (f *fakeAPI) instanceMember(fi *fakeInstance) client.ProjectResource {
	status := "RUNNING"
	if fi.power == "stopped" {
		status = "STOPPED"
	}
	return client.ProjectResource{Kind: "INSTANCE", ID: fi.inst.ID, Name: deref(fi.inst.Name), Status: status,
		AddedAt: optString(fakeNow)}
}

// join puts a resource in a project, out of the one it was in.
func (f *fakeAPI) join(projectID string, m client.ProjectResource) {
	f.leave(m.Kind, m.ID)
	f.infra.members[projectID] = append(f.infra.members[projectID], m)
}

func (f *fakeAPI) leave(kind, id string) {
	for pid, list := range f.infra.members {
		out := []client.ProjectResource{}
		for _, m := range list {
			if !(m.Kind == kind && m.ID == id) {
				out = append(out, m)
			}
		}
		f.infra.members[pid] = out
	}
}

func (f *fakeAPI) project(w http.ResponseWriter, id string) *client.Project {
	pr, ok := f.projects[id]
	if !ok || pr.Deleting {
		notFound(w, "SERVERLESS_PROJECT_NOT_FOUND", "Project")
		return nil
	}
	return pr
}

func (f *fakeAPI) memberList(projectID string) []client.ProjectResource {
	out := []client.ProjectResource{}
	for _, m := range f.infra.members[projectID] {
		if m.Kind == "INSTANCE" {
			if fi, ok := f.infra.instances[m.ID]; ok {
				now := f.instanceMember(fi)
				m.Name, m.Status = now.Name, now.Status
			}
		}
		out = append(out, m)
	}
	return out
}

func (f *fakeAPI) listMembers(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.project(w, p["id"]) == nil {
		return
	}
	writeJSON(w, 200, f.memberList(p["id"]))
}

func (f *fakeAPI) addMembers(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "resources") {
		return
	}
	if f.project(w, p["id"]) == nil {
		return
	}
	items, _ := body["resources"].([]any)
	var add []client.ProjectResource
	for _, it := range items {
		m, _ := it.(map[string]any)
		kind, ref := fmt.Sprint(m["kind"]), fmt.Sprint(m["id"])
		switch kind {
		case "INSTANCE":
			fi, ok := f.infra.instances[ref]
			if !ok {
				notFound(w, "SERVERLESS_PROJECT_RESOURCE_NOT_FOUND", "Instance "+ref)
				return
			}
			add = append(add, f.instanceMember(fi))
		case "BUCKET":
			var found *client.Bucket
			for _, b := range f.buckets {
				if b.ID == ref || b.Name == ref {
					found = b
				}
			}
			if found == nil {
				notFound(w, "SERVERLESS_PROJECT_RESOURCE_NOT_FOUND", "Bucket "+ref)
				return
			}
			add = append(add, client.ProjectResource{Kind: kind, ID: found.ID, Name: found.Name, Status: "ACTIVE",
				Detail: &found.Location, AddedAt: optString(fakeNow)})
		case "DATABASE":
			var found *client.Database
			for _, d := range f.databases {
				if d.ID == ref || d.Name == ref {
					found = d
				}
			}
			if found == nil {
				notFound(w, "SERVERLESS_PROJECT_RESOURCE_NOT_FOUND", "Database "+ref)
				return
			}
			add = append(add, client.ProjectResource{Kind: kind, ID: found.ID, Name: found.Name, Status: found.Status,
				AddedAt: optString(fakeNow)})
		default:
			fieldProblem(w, 422, "resources.0.kind", "kind must be one of the following values: INSTANCE, BUCKET, DATABASE")
			return
		}
	}
	for _, m := range add {
		f.join(p["id"], m)
	}
	writeJSON(w, 200, f.memberList(p["id"]))
}

func (f *fakeAPI) removeMember(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.project(w, p["id"]) == nil {
		return
	}
	kind := strings.ToUpper(p["kind"])
	for _, m := range f.infra.members[p["id"]] {
		if m.Kind == kind && (m.ID == p["rid"] || (kind != "INSTANCE" && m.Name == p["rid"])) {
			f.leave(kind, m.ID)
			w.WriteHeader(204)
			return
		}
	}
	notFound(w, "SERVERLESS_PROJECT_RESOURCE_NOT_FOUND", "Resource")
}

// projectCounts fills in what a project list says each groups.
func (f *fakeAPI) projectCounts(p *client.Project) client.Project {
	out := *p
	var instances, buckets, databases, services int64
	for _, m := range f.infra.members[p.ID] {
		switch m.Kind {
		case "INSTANCE":
			instances++
		case "BUCKET":
			buckets++
		case "DATABASE":
			databases++
		}
	}
	for _, s := range f.services {
		if s.ProjectID == p.ID {
			services++
		}
	}
	out.InstanceCount, out.BucketCount, out.DatabaseCount, out.ServiceCount = &instances, &buckets, &databases, &services
	return out
}

// ── Seeding (for tests) ─────────────────────────────────────────────────────

// seedInstance is an instance made outside Terraform, built and running.
func (f *fakeAPI) seedInstance(name, regionID string) *fakeInstance {
	f.mu.Lock()
	defer f.mu.Unlock()
	region, _ := strconv.Atoi(regionID)
	fi := f.provision(map[string]any{"region_id": float64(region), "offering_code": "c1m1", "name": name,
		"image": map[string]any{"type": "template", "code": "ubuntu-24-04"}, "subscription_id": "sub-9"})
	fi.busyPolls = 0
	return fi
}

// seedProject is a project made outside Terraform.
func (f *fakeAPI) seedProject(name string) *client.Project {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &client.Project{ID: f.newID(), Name: name, Region: "bh", CreatedAt: fakeNow, UpdatedAt: fakeNow}
	f.projects[p.ID] = p
	return p
}

// ── Webhooks ────────────────────────────────────────────────────────────────

func (f *fakeAPI) listWebhooks(w http.ResponseWriter, _ *http.Request, _ map[string]string, _ map[string]any) {
	out := []client.Webhook{}
	for _, h := range f.infra.webhooks {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, out)
}

func (f *fakeAPI) createWebhook(w http.ResponseWriter, _ *http.Request, _ map[string]string, body map[string]any) {
	if !strict(w, body, "kind", "url", "description", "events", "locale") {
		return
	}
	kind := "GENERIC"
	if k, ok := body["kind"].(string); ok {
		kind = k
	}
	raw := strings.TrimSpace(fmt.Sprint(body["url"]))
	if !strings.HasPrefix(raw, "https://") {
		fieldProblem(w, 400, "url", "The address must be https://.")
		return
	}
	host := strings.SplitN(strings.TrimPrefix(raw, "https://"), "/", 2)[0]
	h := &client.Webhook{ID: f.newID(), Kind: kind, URL: host + "/…" + raw[len(raw)-4:], Description: trimOrNull(body["description"]),
		Events: strList(body["events"]), Enabled: true, CreatedAt: fakeNow}
	f.infra.webhooks[h.ID] = h
	var secret *string
	if kind == "GENERIC" {
		s := "whsec_" + h.ID[len(h.ID)-6:]
		secret = &s
	}
	writeJSON(w, 201, map[string]any{"webhook": h, "secret": secret})
}

func (f *fakeAPI) updateWebhook(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "events", "description", "enabled") {
		return
	}
	h, ok := f.infra.webhooks[p["id"]]
	if !ok {
		notFound(w, "", "Webhook")
		return
	}
	if _, ok := body["events"]; ok {
		h.Events = strList(body["events"])
	}
	if v, ok := body["description"]; ok {
		h.Description = trimOrNull(v)
	}
	if v, ok := body["enabled"].(bool); ok {
		h.Enabled = v
		if v {
			h.DisabledReason, h.ConsecutiveFailures = nil, 0
		}
	}
	writeJSON(w, 200, h)
}

func (f *fakeAPI) deleteWebhook(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if _, ok := f.infra.webhooks[p["id"]]; !ok {
		notFound(w, "", "Webhook")
		return
	}
	delete(f.infra.webhooks, p["id"])
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func asList(v any) []map[string]any {
	items, _ := v.([]any)
	var out []map[string]any
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// addAddress gives an instance a new public address of a version.
func (f *fakeAPI) addAddress(fi *fakeInstance, version string, primary bool) {
	f.infra.num++
	addr := fmt.Sprintf("203.0.113.%d", f.infra.num)
	if version == "ipv6" {
		addr = fmt.Sprintf("2001:db8::%x", f.infra.num)
	}
	fi.inst.IPAddresses = append(fi.inst.IPAddresses, client.InstanceIP{Address: addr, Version: version, Primary: primary})
	// Ids that do not grow with time: only assigned_at says which is newest.
	fi.assignments = append(fi.assignments, strconv.Itoa(99999-f.infra.num))
}

// assignedAt is when an instance's address n was assigned: in order.
func assignedAt(n int) string {
	return fmt.Sprintf("2026-10-%02dT12:00:00.000Z", 1+n)
}

// ── Resize ──────────────────────────────────────────────────────────────────

// The add-ons a resize can change: a backup slot (removable) and a managed
// support plan (only ever raised).
var fakeResizeAddons = []client.ResizeAddon{
	{Code: "backup-slot", Name: "Backup slot", Type: "BACKUP", MinQuantity: 1, QuantityStep: 1, Removable: true},
	{Code: "managed-support", Name: "Managed support", Type: "SUPPORT", MinQuantity: 1, QuantityStep: 1, Removable: false},
}

func (f *fakeAPI) extraCount(fi *fakeInstance, version string) int64 {
	n := int64(-1) // one of each is the plan's
	for _, a := range fi.inst.IPAddresses {
		if a.Version == version {
			n++
		}
	}
	return max(n, 0)
}

func (f *fakeAPI) resizeOptions(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	off := f.offering(*fi.inst.OfferingCode)
	opts := client.ResizeOptions{Resizable: !fi.unresizable, CommitmentType: "COMMITTED",
		Current: client.ResizeCurrent{OfferingCode: off.Code, OfferingOnSale: true, VCPU: off.VCPUs, RAMMB: off.RAMMB,
			MainDiskGB: off.BaseDiskGB + fi.mainExtra, ExtraIPv4: f.extraCount(fi, "ipv4"), ExtraIPv6: f.extraCount(fi, "ipv6"),
			HourlyRateMicroCents: "1388888"},
		Capabilities: client.ResizeCapabilities{Addons: true, ExtraIPs: true, Storage: true},
		Offerings:    []client.ResizeOffering{}, Addons: []client.ResizeAddon{}, IPAssignments: []client.IPAssignment{},
		ExtraDisks: []client.ResizeDisk{}, Currency: "BRL"}
	if fi.unresizable {
		reason := "This instance’s real configuration was never recorded, so it cannot be priced: ask support to resize it."
		opts.Reason = &reason
		opts.Current = client.ResizeCurrent{}
	}
	for _, o := range f.infra.offerings {
		opts.Offerings = append(opts.Offerings, client.ResizeOffering{Code: o.Code, Name: o.Name, VCPU: o.VCPUs, RAMMB: o.RAMMB,
			MainDiskBaseGB: o.BaseDiskGB, IncludedIPv4: 1, IncludedIPv6: 1, Current: o.Code == off.Code})
	}
	for _, a := range fakeResizeAddons {
		a.CurrentQuantity = fi.addons[a.Code]
		opts.Addons = append(opts.Addons, a)
	}
	for i, a := range fi.inst.IPAddresses {
		version, addr := int64(4), a.Address
		if a.Version == "ipv6" {
			version = 6
		}
		at := assignedAt(i)
		opts.IPAssignments = append(opts.IPAssignments, client.IPAssignment{AssignmentID: fi.assignments[i], Version: version,
			Address: &addr, Primary: a.Primary, AssignedAt: &at})
	}
	for i := range fi.disks {
		opts.ExtraDisks = append(opts.ExtraDisks, client.ResizeDisk{ID: strconv.Itoa(700 + i), Label: fmt.Sprintf("Disco adicional (scsi%d)", i+1)})
	}
	writeJSON(w, 200, opts)
}

// fakeResize is what a resize body would do to an instance.
type fakeResize struct {
	off        *client.Offering
	mainExtra  int64
	disks      []int64
	addons     map[string]int64
	addV4      int64
	addV6      int64
	release    map[string]bool
	violations []client.ResizeNotice
	removes    []map[string]any
	changes    bool
	restart    bool
}

func (f *fakeAPI) planResize(fi *fakeInstance, body map[string]any) *fakeResize {
	cur := f.offering(*fi.inst.OfferingCode)
	rz := &fakeResize{off: f.offering(fmt.Sprint(body["offering_code"])), disks: fi.disks, addons: map[string]int64{},
		release: map[string]bool{}}
	notice := func(code, msg string) {
		rz.violations = append(rz.violations, client.ResizeNotice{Code: code, Message: msg})
	}
	if rz.off == nil {
		notice("offering_unknown", fmt.Sprintf("No plan %v.", body["offering_code"]))
		return rz
	}
	curTotal := cur.BaseDiskGB + fi.mainExtra
	rz.mainExtra = max(0, curTotal-rz.off.BaseDiskGB)
	if v, ok := asInt(body["main_disk_extra_gb"]); ok {
		rz.mainExtra = v
	}
	if total := rz.off.BaseDiskGB + rz.mainExtra; total < curTotal {
		notice("main_disk_shrink", fmt.Sprintf("main disk cannot shrink (%dGB → %dGB); choose a plan/expansion with at least %dGB",
			curTotal, total, curTotal))
	}
	if _, ok := body["extra_disks"]; ok {
		want := asList(body["extra_disks"])
		var disks []int64
		for i, d := range want {
			size, _ := asInt(d["size_gb"])
			if i < len(fi.disks) {
				size = fi.disks[i] // kept disks keep their size
			} else if size < 10 {
				notice("extra_disk_size", fmt.Sprintf("new data disk %d (GB): %d is below the minimum of 10", i-len(fi.disks)+1, size))
			}
			disks = append(disks, size)
		}
		for i := len(want); i < len(fi.disks); i++ {
			rz.removes = append(rz.removes, map[string]any{"type": "extra_disk", "label": fmt.Sprintf("Data disk scsi%d", i+1),
				"warning": "This additional data disk and ALL data on it will be permanently destroyed."})
		}
		rz.disks = disks
	}
	for k, v := range fi.addons {
		rz.addons[k] = v
	}
	for _, a := range asList(body["addons"]) {
		code := fmt.Sprint(a["code"])
		q, _ := asInt(a["quantity"])
		if code == "managed-support" && q < fi.addons[code] {
			notice("not_removable", "Managed support cannot be removed after purchase")
		}
		rz.addons[code] = q
	}
	rz.addV4, _ = asInt(body["ipv4_add_count"])
	rz.addV6, _ = asInt(body["ipv6_add_count"])
	for _, key := range []string{"ipv4_remove_assignment_ids", "ipv6_remove_assignment_ids"} {
		for _, id := range strList(body[key]) {
			found := false
			for i, a := range fi.assignments {
				if a == id {
					found = true
					if fi.inst.IPAddresses[i].Primary {
						notice("primary_ip", "The primary address cannot be released without another primary.")
					}
					rz.removes = append(rz.removes, map[string]any{"type": fi.inst.IPAddresses[i].Version,
						"label": fi.inst.IPAddresses[i].Address, "warning": "This address is released for good."})
				}
			}
			if !found {
				notice("ip_unknown", "No address with assignment id "+id+".")
			}
			rz.release[id] = true
		}
	}
	sameAddons := len(rz.addons) == len(fi.addons)
	for k, v := range rz.addons {
		if fi.addons[k] != v {
			sameAddons = false
		}
	}
	rz.changes = rz.off.Code != cur.Code || rz.mainExtra != fi.mainExtra || len(rz.disks) != len(fi.disks) || !sameAddons ||
		rz.addV4 > 0 || rz.addV6 > 0 || len(rz.release) > 0
	rz.restart = rz.off.VCPUs != cur.VCPUs || rz.off.RAMMB != cur.RAMMB || rz.off.BaseDiskGB+rz.mainExtra != curTotal ||
		len(rz.disks) != len(fi.disks)
	return rz
}

var resizeFields = []string{"offering_code", "main_disk_extra_gb", "extra_disks", "addons", "ipv4_add_count",
	"ipv4_remove_assignment_ids", "ipv4_primary_assignment_id", "ipv6_add_count", "ipv6_remove_assignment_ids"}

func (f *fakeAPI) quoteResize(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, resizeFields...) {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	rz := f.planResize(fi, body)
	var short *string
	if f.infra.resizeShort != "" {
		short = &f.infra.resizeShort
	}
	removes := rz.removes
	if removes == nil {
		removes = []map[string]any{}
	}
	violations := rz.violations
	if violations == nil {
		violations = []client.ResizeNotice{}
	}
	writeJSON(w, 200, map[string]any{"object": "resize_quote", "instance_id": fi.inst.ID, "subscription_id": optNonEmpty(fi.subscription),
		"has_changes": rz.changes,
		"can_apply":   rz.changes && len(rz.violations) == 0 && short == nil, "current_hourly_rate_micro_cents": "1388888",
		"new_hourly_rate_micro_cents": "2777777", "new_cycle_cents": "2000", "charge_now_cents": "150", "credit_cents": "0",
		"shortfall_cents": short, "requires_restart": rz.restart, "removes": removes, "violations": violations,
		"warnings": []any{}, "currency": "BRL"})
}

func (f *fakeAPI) resizeInstance(w http.ResponseWriter, r *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, resizeFields...) {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil || f.busyRefusal(w, r, fi) {
		return
	}
	rz := f.planResize(fi, body)
	if len(rz.violations) > 0 {
		problem(w, 400, "", rz.violations[0].Message, nil)
		return
	}
	if short := f.infra.resizeShort + f.infra.resizeShortOnApply; short != "" {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(402)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "https://apiv2.masterdaweb.com/v1/errors/insufficient_balance",
			"title": "Insufficient balance", "status": 402, "code": "insufficient_balance",
			"detail":          "The subscription’s balance does not cover this resize. Add credit and retry with a new Idempotency-Key.",
			"subscription_id": "sub-9", "shortfall_cents": short, "currency": "BRL",
			"next": map[string]any{"top_up": "POST /v1/subscriptions/sub-9/top-ups"}, "request_id": "req-fake"})
		return
	}
	code := rz.off.Code
	fi.inst.OfferingCode = &code
	vcpus, ram, disk := rz.off.VCPUs, rz.off.RAMMB, rz.off.BaseDiskGB+rz.mainExtra
	fi.inst.Specs = client.InstanceSpecs{VCPUs: &vcpus, RAMMB: &ram, DiskGB: &disk}
	fi.mainExtra, fi.disks = rz.mainExtra, rz.disks
	fi.addons = map[string]int64{}
	for k, v := range rz.addons {
		if v > 0 {
			fi.addons[k] = v
		}
	}
	var ips []client.InstanceIP
	var ids []string
	for i, id := range fi.assignments {
		if !rz.release[id] {
			ips, ids = append(ips, fi.inst.IPAddresses[i]), append(ids, id)
		}
	}
	fi.inst.IPAddresses, fi.assignments = ips, ids
	for i := int64(0); i < rz.addV4; i++ {
		f.addAddress(fi, "ipv4", false)
	}
	for i := int64(0); i < rz.addV6; i++ {
		f.addAddress(fi, "ipv6", false)
	}
	fi.resizes++
	fi.busyPolls = 2
	op := f.newOperation("vm_resize", "instance", fi.inst.ID)
	writeJSON(w, 202, map[string]any{"object": "operation_accepted", "action": "resize", "resource_type": "instance",
		"resource_id": fi.inst.ID, "instance_id": fi.inst.ID, "operation_id": op.ID, "new_hourly_rate_micro_cents": "2777777", "credit_cents": "0",
		"planned": map[string]any{"requires_restart": rz.restart, "main_disk_grow_gb": 0, "disks_added": 0, "disks_removed": 0,
			"ipv4_added": rz.addV4, "ipv4_removed": 0, "ipv6_added": rz.addV6, "ipv6_removed": 0, "reboot_required": false}})
}

// ── Backup schedule ─────────────────────────────────────────────────────────

var (
	fakeDays     = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	fakeTime     = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	fakeTimezone = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+-]+)*$`)
)

func (f *fakeAPI) getSchedule(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.instance(w, p["id"]) == nil {
		return
	}
	s, ok := f.infra.schedules[p["id"]]
	if !ok {
		problem(w, 404, "", "This instance has no backup schedule.", nil)
		return
	}
	writeJSON(w, 200, s)
}

func (f *fakeAPI) setSchedule(w http.ResponseWriter, _ *http.Request, p map[string]string, body map[string]any) {
	if !strict(w, body, "days", "time", "timezone") {
		return
	}
	fi := f.instance(w, p["id"])
	if fi == nil {
		return
	}
	want := map[string]bool{}
	for _, d := range strList(body["days"]) {
		want[d] = true
	}
	var days []string
	for _, d := range fakeDays {
		if want[d] {
			days = append(days, d)
			delete(want, d)
		}
	}
	if len(days) == 0 || len(want) > 0 {
		fieldProblem(w, 422, "days", "each value in days must be one of the following values: sun, mon, tue, wed, thu, fri, sat")
		return
	}
	t, _ := body["time"].(string)
	if !fakeTime.MatchString(t) {
		fieldProblem(w, 422, "time", "time must be HH:MM (24h)")
		return
	}
	var tz *string
	if v, ok := body["timezone"].(string); ok {
		if !fakeTimezone.MatchString(v) {
			fieldProblem(w, 422, "timezone", "timezone must be an IANA zone such as America/Sao_Paulo")
			return
		}
		tz = &v
	}
	if fi.addons["backup-slot"] == 0 {
		problem(w, 400, "", "Backups need the backup add-on: add it to the instance first.", nil)
		return
	}
	s, ok := f.infra.schedules[fi.inst.ID]
	if !ok {
		f.infra.num++
		s = &client.BackupSchedule{ID: strconv.Itoa(300 + f.infra.num), InstanceID: fi.inst.ID}
		f.infra.schedules[fi.inst.ID] = s
	}
	s.Days, s.Time, s.Timezone = days, t, tz
	writeJSON(w, 200, s)
}

func (f *fakeAPI) deleteSchedule(w http.ResponseWriter, _ *http.Request, p map[string]string, _ map[string]any) {
	if f.instance(w, p["id"]) == nil {
		return
	}
	if _, ok := f.infra.schedules[p["id"]]; !ok {
		problem(w, 404, "", "This instance has no backup schedule.", nil)
		return
	}
	delete(f.infra.schedules, p["id"])
	w.WriteHeader(204)
}

func (f *fakeAPI) getWebhook(w http.ResponseWriter, r *http.Request, p map[string]string, _ map[string]any) {
	if f.infra.legacyFields {
		problem(w, 404, "", "Cannot GET "+r.URL.Path, nil)
		return
	}
	h, ok := f.infra.webhooks[p["id"]]
	if !ok {
		notFound(w, "", "Webhook")
		return
	}
	writeJSON(w, 200, h)
}
