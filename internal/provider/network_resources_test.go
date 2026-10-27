package provider

import (
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestFirewallLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	web := api.seedInstance("web", "3")
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_firewall")
	cfg := map[string]any{
		"name": "web", "description": "Web servers", "policy_in": "DROP",
		"rules": []any{
			map[string]any{"direction": "in", "action": "ACCEPT", "macro": "SSH", "source": "203.0.113.0/24"},
			map[string]any{"direction": "in", "action": "ACCEPT", "protocol": "tcp", "dest_port": "8000-8100", "comment": "app"},
		},
		"instance_ids": []string{web.inst.ID},
	}
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	want := []string{
		"POST /v1/firewalls",
		"POST /v1/firewalls/" + id + "/rules",
		"POST /v1/firewalls/" + id + "/rules",
		"POST /v1/firewalls/" + id + "/commit",
	}
	if got := strings.Join(api.mutations(), "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", got)
	}
	api.expectBody("POST", "/v1/firewalls", `{"name":"web","description":"Web servers"}`)
	api.expectBody("POST", "/v1/firewalls/"+id+"/commit", `{"instance_ids":["`+web.inst.ID+`"]}`)
	fw := api.infra.firewalls[id]
	// The macro filled in its protocol and port; the range is kept as 8000:8100.
	if fw.HasPendingChanges || *fw.Rules[0].DestPort != "22" || *fw.Rules[1].DestPort != "8000:8100" {
		t.Fatalf("firewall %+v", fw)
	}
	if str(t, st, "policy_out") != "ACCEPT" || str(t, st, "rules", 1, "dest_port") != "8000-8100" || boolean(t, st, "has_pending_changes") {
		t.Fatalf("state %v", st)
	}

	// A rule out, another changed, a policy set: in place, in order, committed.
	api.resetCalls()
	cfg["policy_out"] = "DROP"
	cfg["rules"] = []any{map[string]any{"direction": "in", "action": "ACCEPT", "protocol": "tcp", "dest_port": "443", "comment": "https"}}
	st = r.update(st, r.config(cfg))
	r1 := fw.Rules[0].ID
	want = []string{
		"PATCH /v1/firewalls/" + id,
		"PATCH /v1/firewalls/" + id + "/rules/" + r1,
		"DELETE /v1/firewalls/" + id + "/rules/",
		"POST /v1/firewalls/" + id + "/commit",
	}
	got := api.mutations()
	if len(got) != 4 || got[0] != want[0] || got[1] != want[1] || !strings.HasPrefix(got[2], want[2]) || got[3] != want[3] {
		t.Fatalf("calls:\n%s", strings.Join(got, "\n"))
	}
	api.expectBody("PATCH", "/v1/firewalls/"+id+"/rules/"+r1, `{"direction":"in","action":"ACCEPT","macro":null,"protocol":"tcp",
		"source":null,"destination":null,"source_port":null,"dest_port":"443","enabled":true,"comment":"https"}`)
	if len(attrList(t, st, "rules")) != 1 || str(t, st, "policy_out") != "DROP" {
		t.Fatalf("state %v", st)
	}

	// A rule added outside Terraform shows up, and is removed.
	api.infra.firewalls[id].Rules = append(api.infra.firewalls[id].Rules, fw.Rules[0])
	api.infra.firewalls[id].Rules[1].ID, api.infra.firewalls[id].Rules[1].Position = "extra", 2
	refreshed := r.read(st)
	if len(attrList(t, refreshed, "rules")) != 2 {
		t.Fatalf("refreshed %v", refreshed)
	}
	p := r.plan(refreshed, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	if p.noop() {
		t.Fatal("a rule made outside Terraform was left alone")
	}

	for _, ref := range []string{id, "web"} {
		imp, diags := r.importState(ref)
		failOnDiags(t, "import "+ref, diags)
		if str(t, imp, "id") != id {
			t.Fatalf("imported %v", imp)
		}
	}
	failOnDiags(t, "destroy", r.destroy(st))
	if _, ok := api.infra.firewalls[id]; ok {
		t.Fatal("still there")
	}
}

func TestFirewallDataSources(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_firewall")
	r.create(r.config(map[string]any{"name": "edge", "rules": []any{
		map[string]any{"direction": "in", "action": "ACCEPT", "macro": "HTTPS"},
	}}))
	st, diags := h.readDataSource("masterdaweb_firewall", map[string]any{"name": "edge"})
	failOnDiags(t, "firewall", diags)
	if str(t, st, "rules", 0, "macro") != "HTTPS" || str(t, st, "rules", 0, "dest_port") != "443" || str(t, st, "policy_in") != "DROP" {
		t.Fatalf("firewall %v", st)
	}
	_, diags = h.readDataSource("masterdaweb_firewall", map[string]any{"name": "nope"})
	expectError(t, diags, `0 firewalls are named "nope"`)
	macros, diags := h.readDataSource("masterdaweb_firewall_macros", map[string]any{})
	failOnDiags(t, "macros", diags)
	if str(t, macros, "macros", 2, "name") != "SSH" {
		t.Fatalf("macros %v", macros)
	}
}

func TestVPCAndAttachment(t *testing.T) {
	api := newFakeAPI(t)
	web := api.seedInstance("web", "3")
	far := api.seedInstance("far", "5")
	h := newHarness(t, api.URL())
	vpcs := h.resource("masterdaweb_vpc")
	vcfg := map[string]any{"name": "backend", "region_id": "3", "subnet": "10.20.0.0/24", "description": "Private"}
	vpc := vpcs.create(vpcs.config(vcfg))
	vpcID := str(t, vpc, "id")
	api.expectBody("POST", "/v1/vpcs", `{"name":"backend","region":3,"subnet":"10.20.0.0/24","description":"Private"}`)
	if boolean(t, vpc, "default") || len(attrList(t, vpc, "instance_ids")) != 0 {
		t.Fatalf("vpc %v", vpc)
	}

	att := h.resource("masterdaweb_vpc_attachment")
	api.resetCalls()
	a := att.create(att.config(map[string]any{"vpc_id": vpcID, "instance_id": web.inst.ID}))
	api.expectBody("POST", "/v1/vpcs/"+vpcID+"/instances", `{"instance_id":"`+web.inst.ID+`"}`)
	if str(t, a, "id") != vpcID+"/"+web.inst.ID {
		t.Fatalf("attachment %v", a)
	}
	// It waited until the VPC listed the instance.
	if n := len(api.callsTo("GET", "/v1/vpcs/"+vpcID)); n < 2 {
		t.Fatalf("read the VPC %d times", n)
	}
	vpc = vpcs.expectNoDiff(vpc, vpcs.config(vcfg))
	if got := strs(t, vpc, "instance_ids"); len(got) != 1 || got[0] != web.inst.ID {
		t.Fatalf("instance_ids %v", got)
	}

	// Another region: the API refuses it, and says why.
	p := att.plan(att.null(), att.config(map[string]any{"vpc_id": vpcID, "instance_id": far.inst.ID}))
	failOnDiags(t, "plan", p.diags)
	_, diags := att.apply(p)
	expectError(t, diags, "The instance and the VPC must be in the same region.")

	imp, diags := att.importState(vpcID + "/" + web.inst.ID)
	failOnDiags(t, "import attachment", diags)
	att.expectNoDiff(imp, att.config(map[string]any{"vpc_id": vpcID, "instance_id": web.inst.ID}))

	vcfg["name"] = "backend-2"
	api.resetCalls()
	vpc = vpcs.update(vpc, vpcs.config(vcfg))
	api.expectBody("PATCH", "/v1/vpcs/"+vpcID, `{"name":"backend-2"}`)

	api.resetCalls()
	failOnDiags(t, "detach", att.destroy(a))
	if got := strings.Join(api.mutations(), "\n"); got != "DELETE /v1/vpcs/"+vpcID+"/instances/"+web.inst.ID {
		t.Fatalf("calls:\n%s", got)
	}
	if web.inst.VPCID != nil {
		t.Fatal("still on the VPC")
	}
	// Gone already: destroying it again asks nothing.
	api.resetCalls()
	failOnDiags(t, "detach again", att.destroy(a))
	if got := api.mutations(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}

	for _, ref := range []string{vpcID, "backend-2"} {
		imp, diags := vpcs.importState(ref)
		failOnDiags(t, "import "+ref, diags)
		if str(t, imp, "subnet") != "10.20.0.0/24" || str(t, imp, "region_id") != "3" {
			t.Fatalf("imported %v", imp)
		}
		vpcs.expectNoDiff(imp, vpcs.config(vcfg))
		// Another region is another VPC.
		other := map[string]any{"name": "backend-2", "region_id": "5", "subnet": "10.20.0.0/24", "description": "Private"}
		if p := vpcs.plan(imp, vpcs.config(other)); len(p.requiresReplace) == 0 {
			t.Fatal("a VPC moved region in place")
		}
	}
	failOnDiags(t, "destroy", vpcs.destroy(vpc))
	if _, ok := api.infra.vpcs[vpcID]; ok {
		t.Fatal("still there")
	}
}

func TestVPCValidationAndDataSource(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	vpcs := h.resource("masterdaweb_vpc")
	for _, subnet := range []string{"8.8.8.0/24", "10.0.0.0/31", "172.32.0.0/16", "fd00::/64", "10.0.0.0"} {
		expectError(t, vpcs.validate(vpcs.config(map[string]any{"name": "x", "region_id": "3", "subnet": subnet})), "Invalid subnet")
	}
	failOnDiags(t, "valid", vpcs.validate(vpcs.config(map[string]any{"name": "x", "region_id": "3", "subnet": "192.168.10.0/24"})))
	// The default subnet is the API's.
	st := vpcs.create(vpcs.config(map[string]any{"name": "lan", "region_id": "3"}))
	if str(t, st, "subnet") != "10.200.0.0/24" {
		t.Fatalf("vpc %v", st)
	}
	api.infra.vpcs[str(t, st, "id")].Default = true
	ds, diags := h.readDataSource("masterdaweb_vpc", map[string]any{"default": true})
	failOnDiags(t, "default vpc", diags)
	if str(t, ds, "name") != "lan" {
		t.Fatalf("vpc %v", ds)
	}
	_, diags = h.readDataSource("masterdaweb_vpc", map[string]any{"default": false})
	expectError(t, diags, "default = false finds nothing")
	expectError(t, vpcs.destroy(vpcs.read(st)), "The default VPC is never deleted")
}

func TestReverseDNS(t *testing.T) {
	api := newFakeAPI(t)
	web := api.seedInstance("mail", "3")
	ip := web.inst.IPAddresses[0].Address
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_reverse_dns")

	// The name must resolve to the address first.
	p := r.plan(r.null(), r.config(map[string]any{"ip": ip, "hostname": "mail.example.com"}))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "mail.example.com does not resolve to "+ip)

	zones, records := h.resource("masterdaweb_dns_zone"), h.resource("masterdaweb_dns_record")
	z := zones.create(zones.config(map[string]any{"name": "example.com"}))
	records.create(records.config(map[string]any{"zone_id": str(t, z, "id"), "name": "mail", "type": "A", "records": []string{ip}}))

	api.resetCalls()
	cfg := r.config(map[string]any{"ip": ip, "hostname": "Mail.Example.com."})
	st := r.create(cfg)
	api.expectBody("PUT", "/v1/reverse-dns/"+ip, `{"hostname":"mail.example.com"}`)
	if str(t, st, "hostname") != "Mail.Example.com." || str(t, st, "instance_id") != web.inst.ID || num(t, st, "version") != 4 {
		t.Fatalf("state %v", st)
	}
	imp, diags := r.importState(ip)
	failOnDiags(t, "import", diags)
	r.expectNoDiff(imp, r.config(map[string]any{"ip": ip, "hostname": "mail.example.com"}))

	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	if got := strings.Join(api.mutations(), "\n"); got != "POST /v1/reverse-dns/"+ip+"/reset" {
		t.Fatalf("calls:\n%s", got)
	}
	if _, ok := api.infra.ptr[ip]; ok {
		t.Fatal("not reset")
	}
	// IPv6, written any way.
	v6 := web.inst.IPAddresses[1].Address
	imp, diags = r.importState(strings.ToUpper(v6))
	failOnDiags(t, "import v6", diags)
	if str(t, imp, "ip") != v6 || num(t, imp, "version") != 6 {
		t.Fatalf("imported %v", imp)
	}
	expectError(t, r.validate(r.config(map[string]any{"ip": "mail", "hostname": "x.example.com"})), "Invalid address")
}

func TestDNSZoneAndRecords(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	zones, records := h.resource("masterdaweb_dns_zone"), h.resource("masterdaweb_dns_record")
	z := zones.create(zones.config(map[string]any{"name": "Example.com"}))
	zoneID := str(t, z, "id")
	api.expectBody("POST", "/v1/dns/zones", `{"name":"example.com"}`)
	if got := strings.Join(strs(t, z, "name_servers"), ","); got != "ns1.masterdaweb.net,ns2.masterdaweb.net" || str(t, z, "name") != "Example.com" {
		t.Fatalf("zone %v", z)
	}

	api.resetCalls()
	www := records.config(map[string]any{"zone_id": zoneID, "name": "www", "type": "A", "records": []string{"203.0.113.11", "203.0.113.10"}})
	st := records.create(www)
	api.expectBody("PUT", "/v1/dns/zones/"+zoneID+"/records", `{"name":"www","type":"A","ttl":3600,"records":["203.0.113.10","203.0.113.11"]}`)
	if str(t, st, "fqdn") != "www.example.com." || str(t, st, "id") != zoneID+"/www/A" {
		t.Fatalf("state %v", st)
	}

	// What the nameservers keep differently means the same: no diff.
	for _, c := range []map[string]any{
		{"zone_id": zoneID, "name": "blog", "type": "CNAME", "records": []string{"www"}},
		{"zone_id": zoneID, "name": "@", "type": "MX", "records": []string{"10 mail.example.com", "20 backup.example.net."}},
		{"zone_id": zoneID, "name": "@", "type": "TXT", "records": []string{"v=spf1 include:_spf.example.com ~all", strings.Repeat("k", 300)}},
		{"zone_id": zoneID, "name": "v6", "type": "AAAA", "records": []string{"2001:DB8:0::1"}, "ttl": 300},
	} {
		records.create(records.config(c))
	}
	rrs := map[string][]string{}
	for _, rr := range api.infra.records[zoneID] {
		rrs[rr.Name+" "+rr.Type] = rr.Values
	}
	if got := rrs["blog.example.com. CNAME"]; len(got) != 1 || got[0] != "www.example.com." {
		t.Fatalf("CNAME %v", got)
	}
	if got := rrs["example.com. TXT"]; len(got) != 2 || !strings.Contains(strings.Join(got, "|"), `"`+strings.Repeat("k", 255)+`" "kkk`) {
		t.Fatalf("TXT %v", got)
	}

	// More values, in one step.
	api.resetCalls()
	www = records.config(map[string]any{"zone_id": zoneID, "name": "www", "type": "A", "ttl": 600,
		"records": []string{"203.0.113.10", "203.0.113.11", "203.0.113.12"}})
	st = records.update(st, www)
	if got := strings.Join(api.mutations(), "\n"); got != "PUT /v1/dns/zones/"+zoneID+"/records" {
		t.Fatalf("calls:\n%s", got)
	}

	// An existing record set is not taken over silently.
	p := records.plan(records.null(), records.config(map[string]any{"zone_id": zoneID, "name": "www", "type": "A", "records": []string{"192.0.2.1"}}))
	_, diags := records.apply(p)
	expectError(t, diags, "Import them (terraform import "+zoneID+"/www/A)")

	imp, diags := records.importState(zoneID + "/www/a")
	failOnDiags(t, "import", diags)
	records.expectNoDiff(imp, www)

	expectError(t, records.validate(records.config(map[string]any{"zone_id": zoneID, "name": "x", "type": "CNAME",
		"records": []string{"a.example.com", "b.example.com"}})), "A CNAME has one value")
	expectError(t, records.validate(records.config(map[string]any{"zone_id": zoneID, "name": "@", "type": "CNAME",
		"records": []string{"www.example.com"}})), "No CNAME at the zone apex")

	api.resetCalls()
	failOnDiags(t, "destroy", records.destroy(st))
	api.expectBody("DELETE", "/v1/dns/zones/"+zoneID+"/records", `{"name":"www","type":"A"}`)

	// Removed outside Terraform: gone from the state.
	api.infra.records[zoneID] = nil
	if got := records.read(imp); !got.IsNull() {
		t.Fatalf("a missing record set stayed: %v", got)
	}

	for _, ref := range []string{zoneID, "example.com"} {
		imp, diags := zones.importState(ref)
		failOnDiags(t, "import zone "+ref, diags)
		zones.expectNoDiff(imp, zones.config(map[string]any{"name": "example.com"}))
	}
	ds, diags := h.readDataSource("masterdaweb_dns_zone", map[string]any{"name": "EXAMPLE.com"})
	failOnDiags(t, "zone data source", diags)
	if str(t, ds, "id") != zoneID || len(strs(t, ds, "name_servers")) != 2 {
		t.Fatalf("zone %v", ds)
	}
	failOnDiags(t, "destroy zone", zones.destroy(z))
}

func TestDNSRecordOnAnAPIWithoutRecordSets(t *testing.T) {
	api := newFakeAPI(t)
	api.infra.noRecordSetPut = true
	h := newHarness(t, api.URL())
	zones, records := h.resource("masterdaweb_dns_zone"), h.resource("masterdaweb_dns_record")
	zoneID := str(t, zones.create(zones.config(map[string]any{"name": "example.com"})), "id")

	api.resetCalls()
	records.create(records.config(map[string]any{"zone_id": zoneID, "name": "www", "type": "A", "records": []string{"203.0.113.10"}}))
	api.expectBody("POST", "/v1/dns/zones/"+zoneID+"/records", `{"name":"www","type":"A","content":"203.0.113.10","ttl":3600}`)

	p := records.plan(records.null(), records.config(map[string]any{"zone_id": zoneID, "name": "api", "type": "A",
		"records": []string{"203.0.113.10", "203.0.113.11"}}))
	_, diags := records.apply(p)
	expectError(t, diags, "a set of 2 values needs its PUT endpoint")
	// It asked once whether the API has record sets.
	if n := len(api.callsTo("PUT", "/v1/dns/zones/"+zoneID+"/records")); n != 1 {
		t.Fatalf("asked %d times", n)
	}
}

func TestCatalogDataSources(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())

	regions, diags := h.readDataSource("masterdaweb_regions", map[string]any{})
	failOnDiags(t, "regions", diags)
	if n := len(attrList(t, regions, "regions")); n != 3 || str(t, regions, "regions", 0, "region_name") != "br-sp" {
		t.Fatalf("regions %v", regions)
	}
	open, diags := h.readDataSource("masterdaweb_regions", map[string]any{"available": true})
	failOnDiags(t, "open regions", diags)
	if n := len(attrList(t, open, "regions")); n != 2 {
		t.Fatalf("available regions %v", open)
	}

	codes := func(attrs map[string]any) string {
		st, diags := h.readDataSource("masterdaweb_plans", attrs)
		failOnDiags(t, "plans", diags)
		var out []string
		for i := range attrList(t, st, "plans") {
			out = append(out, str(t, st, "plans", i, "code"))
		}
		return strings.Join(out, ",")
	}
	if got := codes(map[string]any{}); got != "c1m1,c2m4,c4m8" {
		t.Fatalf("plans sorted %s", got)
	}
	if got := codes(map[string]any{"min_ram_mb": 4096, "term": "MONTHLY"}); got != "c2m4,c4m8" {
		t.Fatalf("plans filtered %s", got)
	}
	if got := codes(map[string]any{"term": "ANNUAL"}); got != "c2m4" {
		t.Fatalf("annual plans %s", got)
	}
	st, _ := h.readDataSource("masterdaweb_plans", map[string]any{"min_vcpus": 4})
	rates := map[string]string{}
	var m map[string]tftypes.Value
	if err := attrAt(t, st, "plans", 0, "hourly_rates").As(&m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		var s string
		_ = v.As(&s)
		rates[k] = s
	}
	keys := []string{}
	for k := range rates {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "MONTHLY,ON_DEMAND" || rates["MONTHLY"] != "5555555" {
		t.Fatalf("rates %v", rates)
	}

	images, diags := h.readDataSource("masterdaweb_images", map[string]any{"type": "MARKETPLACE_APP"})
	failOnDiags(t, "images", diags)
	if n := len(attrList(t, images, "images")); n != 1 || str(t, images, "images", 0, "code") != "wordpress" ||
		str(t, images, "images", 0, "inputs", 0, "need") != "required" || str(t, images, "images", 0, "inputs", 1, "default") != "true" {
		t.Fatalf("apps %v", images)
	}
	linux, diags := h.readDataSource("masterdaweb_images", map[string]any{"os_family": "linux", "type": "OPERATING_SYSTEM"})
	failOnDiags(t, "linux", diags)
	if n := len(attrList(t, linux, "images")); n != 2 || num(t, linux, "images", 0, "min_disk_gb") != 10 {
		t.Fatalf("linux %v", linux)
	}
	_, diags = h.readDataSource("masterdaweb_images", map[string]any{"os_family": "macos"})
	expectError(t, diags, "os_family")
}

func TestVPCAttachmentPrivateAddress(t *testing.T) {
	api := newFakeAPI(t)
	web, db := api.seedInstance("web", "3"), api.seedInstance("db", "3")
	h := newHarness(t, api.URL())
	vpcs, att := h.resource("masterdaweb_vpc"), h.resource("masterdaweb_vpc_attachment")
	vpc := vpcs.create(vpcs.config(map[string]any{"name": "backend", "region_id": "3", "subnet": "10.20.0.0/24"}))
	vpcID := str(t, vpc, "id")

	api.resetCalls()
	a := att.create(att.config(map[string]any{"vpc_id": vpcID, "instance_id": web.inst.ID, "private_ip": "10.20.0.50"}))
	api.expectBody("POST", "/v1/vpcs/"+vpcID+"/instances", `{"instance_id":"`+web.inst.ID+`","private_ip":"10.20.0.50"}`)
	// It waited for the attachment's operation.
	if ops := api.mutations(); len(ops) != 1 {
		t.Fatalf("calls %v", ops)
	}
	if n := len(api.callsTo("GET", "/v1/operations/"+firstOperation(api))); n < 2 {
		t.Fatalf("followed the attachment's operation %d times", n)
	}
	if str(t, a, "private_ip") != "10.20.0.50" {
		t.Fatalf("attachment %v", a)
	}
	// Left out, DHCP picks it, and it shows.
	b := att.create(att.config(map[string]any{"vpc_id": vpcID, "instance_id": db.inst.ID}))
	if str(t, b, "private_ip") != "10.20.0.2" {
		t.Fatalf("attachment %v", b)
	}
	vpc = vpcs.read(vpc)
	if n := len(attrList(t, vpc, "members")); n != 2 || str(t, vpc, "members", 0, "private_ip") != "10.20.0.50" ||
		str(t, vpc, "members", 0, "status") != "online" {
		t.Fatalf("members %v", vpc)
	}
	ds, diags := h.readDataSource("masterdaweb_vpc", map[string]any{"id": vpcID})
	failOnDiags(t, "vpc data source", diags)
	if str(t, ds, "members", 1, "instance_id") != db.inst.ID {
		t.Fatalf("data source %v", ds)
	}

	// Another address is another attachment.
	if p := att.plan(a, att.config(map[string]any{"vpc_id": vpcID, "instance_id": web.inst.ID, "private_ip": "10.20.0.60"})); len(p.requiresReplace) == 0 {
		t.Fatal("a new address did not replace the attachment")
	}
	imp, diags := att.importState(vpcID + "/" + web.inst.ID)
	failOnDiags(t, "import", diags)
	if str(t, imp, "private_ip") != "10.20.0.50" {
		t.Fatalf("imported %v", imp)
	}
	att.expectNoDiff(imp, att.config(map[string]any{"vpc_id": vpcID, "instance_id": web.inst.ID, "private_ip": "10.20.0.50"}))

	// Taken, or outside the subnet: the API says so.
	other := api.seedInstance("other", "3")
	for ip, want := range map[string]string{"10.20.0.50": "10.20.0.50 is taken on this VPC.", "10.99.0.1": "is not in the VPC's subnet"} {
		p := att.plan(att.null(), att.config(map[string]any{"vpc_id": vpcID, "instance_id": other.inst.ID, "private_ip": ip}))
		_, diags := att.apply(p)
		expectError(t, diags, want)
	}
	expectError(t, att.validate(att.config(map[string]any{"vpc_id": vpcID, "instance_id": "1", "private_ip": "fd00::1"})), "must be an IPv4 address")
}
