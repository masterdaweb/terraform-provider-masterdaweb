package provider

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// attrList is a list or set attribute's elements.
func attrList(t *testing.T, v tftypes.Value, steps ...any) []tftypes.Value {
	t.Helper()
	var items []tftypes.Value
	if err := attrAt(t, v, steps...).As(&items); err != nil {
		t.Fatal(err)
	}
	return items
}

// testPublicKey is an Ed25519 public key line, different for every seed.
func testPublicKey(seed byte, comment string) string {
	wire := func(b []byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
		return append(out, b...)
	}
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = seed + byte(i)
	}
	blob := append(wire([]byte("ssh-ed25519")), wire(pub)...)
	line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
	if comment != "" {
		line += " " + comment
	}
	return line
}

func instanceConfig(extra map[string]any) map[string]any {
	cfg := map[string]any{
		"name": "web-1", "region_id": "3", "plan": "c1m1", "image": "ubuntu-24-04",
		"subscription_id": "sub-9", "payment": "balance",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestInstanceLifecycle(t *testing.T) {
	api := newFakeAPI(t)
	project := api.seedProject("shop")
	h := newHarness(t, api.URL())
	keys := h.resource("masterdaweb_ssh_key")
	key := keys.create(keys.config(map[string]any{"name": "laptop", "public_key": testPublicKey(1, "ana@laptop")}))
	keyID := str(t, key, "id")

	r := h.resource("masterdaweb_instance")
	api.resetCalls()
	cfg := instanceConfig(map[string]any{
		"description": "The shop's web server", "password": "S3cure-pass!", "ssh_key_ids": []string{keyID},
		"user_data": "#cloud-config\npackages: [nginx]\n", "project_id": project.ID,
	})
	st := r.create(r.config(cfg))
	api.expectBody("POST", "/v1/orders", `{"region_id":3,"offering_code":"c1m1","term":"MONTHLY",
		"image":{"type":"template","code":"ubuntu-24-04"},"name":"web-1","password":"S3cure-pass!",
		"subscription_id":"sub-9","payment":"balance","ssh_key_ids":["`+keyID+`"],
		"user_data":"#cloud-config\npackages: [nginx]\n","project_id":"`+project.ID+`"}`)
	id := str(t, st, "id")
	api.expectBody("PATCH", "/v1/instances/"+id, `{"description":"The shop's web server"}`)
	if got := strings.Join(api.mutations(), "\n"); got != "POST /v1/orders\nPATCH /v1/instances/"+id {
		t.Fatalf("calls:\n%s", got)
	}
	// It waited for the build: the status was asked until it was not busy.
	if n := len(api.callsTo("GET", "/v1/instances/"+id+"/status")); n < 3 {
		t.Fatalf("asked the status %d times", n)
	}
	primary := api.infra.instances[id].inst.IPAddresses[0].Address
	if str(t, st, "status") != "running" || str(t, st, "ipv4_address") != primary || str(t, st, "region_name") != "São Paulo" ||
		num(t, st, "vcpus") != 1 || num(t, st, "disk_gb") != 25 || str(t, st, "subscription_id") != "sub-9" ||
		str(t, st, "hostname") != "vps-4002.mwcloud.example" || str(t, st, "ip_addresses", 1, "version") != "ipv6" ||
		isNull(t, st, "order_id") || num(t, st, "main_disk_extra_gb") != 0 || num(t, st, "extra_ipv4") != 0 ||
		len(attrList(t, st, "extra_disks_gb")) != 0 {
		t.Fatalf("state %v", st)
	}
	if m := api.infra.members[project.ID]; len(m) != 1 || m[0].ID != id {
		t.Fatalf("the instance did not join its project: %+v", m)
	}

	// Renamed, stopped and its password reset, in place.
	api.resetCalls()
	cfg["name"], cfg["power_state"], cfg["password"] = "web-01", "stopped", "N3w-pass!!"
	st = r.update(st, r.config(cfg))
	api.expectBody("PATCH", "/v1/instances/"+id, `{"name":"web-01"}`)
	api.expectBody("POST", "/v1/instances/"+id+"/reset-password", `{"password":"N3w-pass!!"}`)
	api.expectBody("POST", "/v1/instances/"+id+"/power", `{"action":"stop"}`)
	if str(t, st, "status") != "stopped" || str(t, st, "name") != "web-01" {
		t.Fatalf("state %v", st)
	}

	// Put in another project elsewhere: the plan says so, and puts it back.
	other := api.seedProject("other")
	api.join(other.ID, api.instanceMember(api.infra.instances[id]))
	refreshed := r.read(st)
	if str(t, refreshed, "project_id") != other.ID {
		t.Fatalf("the project from elsewhere does not show: %v", refreshed)
	}
	api.resetCalls()
	st = r.update(refreshed, r.config(cfg))
	if got := strings.Join(api.mutations(), "\n"); got != "POST /v1/projects/"+project.ID+"/resources" {
		t.Fatalf("calls:\n%s", got)
	}

	// "" takes it out of its project; left out, it is not managed.
	api.resetCalls()
	cfg["project_id"] = ""
	st = r.update(st, r.config(cfg))
	if got := strings.Join(api.mutations(), "\n"); got != "DELETE /v1/projects/"+project.ID+"/resources/instance/"+id {
		t.Fatalf("calls:\n%s", got)
	}
	delete(cfg, "project_id")
	r.expectNoDiff(st, r.config(cfg))

	// Destroyed.
	api.resetCalls()
	failOnDiags(t, "destroy", r.destroy(st))
	del := api.callsTo("DELETE", "/v1/instances/"+id)
	if len(del) != 1 || del[0].Query.Get("confirm") != id {
		t.Fatalf("delete %+v", del)
	}
	if _, ok := api.infra.instances[id]; ok {
		t.Fatal("still there")
	}
}

func TestInstanceOrderWaitsForItsInvoice(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(nil)
	delete(cfg, "subscription_id")
	delete(cfg, "payment")
	st := r.create(r.config(cfg))
	api.expectBody("POST", "/v1/orders", `{"region_id":3,"offering_code":"c1m1","term":"MONTHLY",
		"image":{"type":"template","code":"ubuntu-24-04"},"name":"web-1","payment":"auto"}`)
	order := str(t, st, "order_id")
	if n := len(api.callsTo("GET", "/v1/orders/"+order)); n != 2 {
		t.Fatalf("followed the order %d times, want until it was paid (2)", n)
	}
	if str(t, st, "subscription_id") != "sub-1" || str(t, st, "status") != "running" {
		t.Fatalf("state %v", st)
	}
}

func TestInstanceOrderStillUnpaidSaysWhatToDo(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(map[string]any{"timeouts": map[string]any{"create": "20ms"}})
	delete(cfg, "subscription_id")
	delete(cfg, "payment")
	p := r.plan(r.null(), r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	api.infra.unpaid = true
	st, diags := r.apply(p)
	expectError(t, diags, "is still waiting for its invoice to be paid")
	expectError(t, diags, "Pay it at https://pay.example.com/inv")
	expectError(t, diags, `subscription_id with payment = "balance"`)
	if !st.IsNull() {
		t.Fatalf("an instance not built yet is in the state: %v", st)
	}
}

func TestInstanceReinstallsInPlace(t *testing.T) {
	api := newFakeAPI(t)
	h := newHarness(t, api.URL())
	keys := h.resource("masterdaweb_ssh_key")
	k1 := str(t, keys.create(keys.config(map[string]any{"name": "a", "public_key": testPublicKey(1, "")})), "id")
	k2 := str(t, keys.create(keys.config(map[string]any{"name": "b", "public_key": testPublicKey(2, "")})), "id")
	r := h.resource("masterdaweb_instance")
	cfg := instanceConfig(map[string]any{"password": "S3cure-pass!", "ssh_key_ids": []string{k1}})
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	ip := str(t, st, "ipv4_address")

	// A new image and another key: the plan warns, the apply rebuilds the
	// instance where it is.
	api.resetCalls()
	cfg["image"], cfg["ssh_key_ids"] = "debian-12", []string{k2, k1}
	p := r.plan(st, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	expectWarning(t, p.diags, "Changing image, ssh_key_ids reinstalls the instance "+id+" in place")
	if len(p.requiresReplace) > 0 {
		t.Fatalf("a reinstall replaced the instance: %v", p.requiresReplace)
	}
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	keysWant := []string{k1, k2}
	if k2 < k1 {
		keysWant = []string{k2, k1}
	}
	api.expectBody("POST", "/v1/instances/"+id+"/reinstall", `{"image":"debian-12","password":"S3cure-pass!",
		"ssh_key_ids":["`+keysWant[0]+`","`+keysWant[1]+`"],"user_data":""}`)
	if got := strings.Join(api.mutations(), "\n"); got != "POST /v1/instances/"+id+"/reinstall" {
		t.Fatalf("calls (the password goes with the reinstall, not after it):\n%s", got)
	}
	r.expectNoDiff(st, r.config(cfg))
	fi := api.infra.instances[id]
	if fi.image != "debian-12" || fi.reinstalls != 1 || str(t, st, "ipv4_address") != ip || str(t, st, "id") != id {
		t.Fatalf("instance %+v state %v", fi, st)
	}
	if n := len(api.callsTo("GET", "/v1/operations/"+firstOperation(api))); n < 2 {
		t.Fatalf("followed the reinstall's operation %d times", n)
	}
}

func firstOperation(api *fakeAPI) string {
	for id := range api.infra.operations {
		return id
	}
	return ""
}

func TestInstanceImportAdoptsWhatItWasBuiltWith(t *testing.T) {
	api := newFakeAPI(t)
	fi := api.seedInstance("legacy", "3")
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	for _, ref := range []string{fi.inst.ID, "legacy"} {
		st, diags := r.importState(ref)
		failOnDiags(t, "import "+ref, diags)
		if str(t, st, "id") != fi.inst.ID || str(t, st, "plan") != "c1m1" || str(t, st, "region_id") != "3" {
			t.Fatalf("imported %v", st)
		}
	}
	st, _ := r.importState(fi.inst.ID)
	api.resetCalls()
	// The configuration says what the API cannot show (the image, the
	// password): taken as it is, without reinstalling or resetting anything.
	cfg := r.config(instanceConfig(map[string]any{"name": "legacy", "password": "Wh4tever-it-is"}))
	p := r.plan(st, cfg)
	failOnDiags(t, "plan", p.diags)
	for _, d := range p.diags {
		if strings.Contains(d.Summary, "reinstalled") {
			t.Fatalf("an import plans a reinstall: %s", diagText(p.diags))
		}
	}
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	if got := api.mutations(); len(got) != 0 {
		t.Fatalf("adopting an import changed the instance: %v", got)
	}
	r.expectNoDiff(st, cfg)

	_, diags = r.importState("nope")
	expectError(t, diags, `No instance has the id or name "nope"`)
}

func TestInstanceRefusesWhatTheAPICannotChange(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	st := r.create(r.config(instanceConfig(nil)))
	for attr, v := range map[string]any{"term": "ANNUAL", "subscription_id": "sub-10"} {
		p := r.plan(st, r.config(instanceConfig(map[string]any{attr: v})))
		expectError(t, p.diags, "Cannot change "+attr+" in place")
	}
}

func TestInstanceResizesInPlace(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(map[string]any{"addons": []any{map[string]any{"code": "backup-slot", "quantity": 1}}})
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	ip := str(t, st, "ipv4_address")

	// A bigger plan, a bigger main disk, a data disk, more backup slots and
	// an address: one resize, quoted at plan time.
	api.resetCalls()
	cfg["plan"], cfg["main_disk_extra_gb"], cfg["extra_disks_gb"], cfg["extra_ipv4"] = "c2m4", 20, []any{100}, 1
	cfg["addons"] = []any{map[string]any{"code": "backup-slot", "quantity": 3}}
	p := r.plan(st, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	expectWarning(t, p.diags, "Changing plan, main_disk_extra_gb, extra_disks_gb, addons, extra_ipv4 resizes the instance "+id+" in place")
	expectWarning(t, p.diags, "It powers off and on again")
	expectWarning(t, p.diags, "Charged now: 1.50 BRL")
	if len(p.requiresReplace) > 0 {
		t.Fatalf("a resize replaces the instance: %v", p.requiresReplace)
	}
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	body := `{"offering_code":"c2m4","main_disk_extra_gb":20,"extra_disks":[{"size_gb":100}],
		"addons":[{"code":"backup-slot","quantity":3}],"ipv4_add_count":1}`
	api.expectBody("POST", "/v1/instances/"+id+"/resize", body)
	if quotes := api.callsTo("POST", "/v1/instances/"+id+"/resize/quote"); len(quotes) != 2 {
		t.Fatalf("%d quotes, want one at plan time and one before applying", len(quotes))
	} else {
		assertJSON(t, quotes[1].Body, body)
	}
	if got := strings.Join(api.mutations(), "\n"); !strings.HasSuffix(got, "POST /v1/instances/"+id+"/resize") {
		t.Fatalf("calls:\n%s", got)
	}
	st = r.expectNoDiff(st, r.config(cfg))
	fi := api.infra.instances[id]
	if *fi.inst.OfferingCode != "c2m4" || fi.mainExtra != 20 || len(fi.disks) != 1 || fi.addons["backup-slot"] != 3 ||
		fi.resizes != 1 || str(t, st, "ipv4_address") != ip || str(t, st, "id") != id || num(t, st, "vcpus") != 2 ||
		num(t, st, "disk_gb") != 100 || num(t, st, "extra_ipv4") != 1 || len(attrList(t, st, "ip_addresses")) != 3 {
		t.Fatalf("instance %+v state %v", fi, st)
	}

	// The data disk off, the extra address released: the newest, not the
	// primary. The plan says what goes.
	api.resetCalls()
	extra := fi.assignments[len(fi.assignments)-1]
	cfg["extra_disks_gb"], cfg["extra_ipv4"] = []any{}, 0
	p = r.plan(st, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	expectWarning(t, p.diags, "The last 1 data disk(s) of the instance "+id+" are removed, with ALL the data on them")
	expectWarning(t, p.diags, "Removes Data disk scsi1")
	st, diags = r.apply(p)
	failOnDiags(t, "apply", diags)
	api.expectBody("POST", "/v1/instances/"+id+"/resize", `{"offering_code":"c2m4","main_disk_extra_gb":20,"extra_disks":[],
		"ipv4_remove_assignment_ids":["`+extra+`"]}`)
	r.expectNoDiff(st, r.config(cfg))
	if len(fi.disks) != 0 || str(t, st, "ipv4_address") != ip {
		t.Fatalf("instance %+v state %v", fi, st)
	}

	// Left out, the main disk is what the instance has: a plan change keeps
	// its size.
	api.resetCalls()
	delete(cfg, "main_disk_extra_gb")
	cfg["plan"] = "c4m8"
	st = r.update(st, r.config(cfg))
	api.expectBody("POST", "/v1/instances/"+id+"/resize", `{"offering_code":"c4m8"}`)
	if num(t, st, "main_disk_extra_gb") != 0 || num(t, st, "disk_gb") != 160 {
		t.Fatalf("state %v", st)
	}
}

func TestInstanceResizeRefusals(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(map[string]any{"plan": "c2m4", "main_disk_extra_gb": 20, "extra_disks_gb": []any{50, 60},
		"addons": []any{map[string]any{"code": "managed-support"}}})
	st := r.create(r.config(cfg))
	api.resetCalls()

	shrink := instanceConfig(map[string]any{"plan": "c2m4", "main_disk_extra_gb": 10, "extra_disks_gb": []any{50, 60},
		"addons": []any{map[string]any{"code": "managed-support"}}})
	expectError(t, r.plan(st, r.config(shrink)).diags, "The main disk cannot shrink")

	resized := instanceConfig(map[string]any{"plan": "c2m4", "main_disk_extra_gb": 20, "extra_disks_gb": []any{50, 70},
		"addons": []any{map[string]any{"code": "managed-support"}}})
	expectError(t, r.plan(st, r.config(resized)).diags, "data disk 2 has 60 GB, and a disk's size cannot change")

	// What only the API knows is refused at plan time too: a smaller plan
	// whose disk would shrink, an add-on that is only ever raised.
	smaller := instanceConfig(map[string]any{"plan": "c1m1", "main_disk_extra_gb": 20, "extra_disks_gb": []any{50, 60},
		"addons": []any{map[string]any{"code": "managed-support"}}})
	expectError(t, r.plan(st, r.config(smaller)).diags, "main disk cannot shrink (100GB → 45GB)")
	dropped := instanceConfig(map[string]any{"plan": "c2m4", "main_disk_extra_gb": 20, "extra_disks_gb": []any{50, 60}})
	expectError(t, r.plan(st, r.config(dropped)).diags, "Managed support cannot be removed after purchase")
	if got := api.mutations(); len(got) != 2 {
		// The two quotes only: the first two never reached the API.
		t.Fatalf("calls %v", got)
	}
	for _, m := range api.mutations() {
		if !strings.HasSuffix(m, "/resize/quote") {
			t.Fatalf("a refused plan changed something: %v", api.mutations())
		}
	}
}

func TestInstanceThatCannotBeResized(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(map[string]any{"main_disk_extra_gb": 10})
	st := r.create(r.config(cfg))
	api.infra.instances[str(t, st, "id")].unresizable = true
	// What it shows is not reliable: the state keeps what it knew.
	st = r.expectNoDiff(st, r.config(cfg))
	if num(t, st, "main_disk_extra_gb") != 10 {
		t.Fatalf("state %v", st)
	}
	cfg["plan"] = "c2m4"
	expectError(t, r.plan(st, r.config(cfg)).diags, "cannot be resized now: This instance’s real configuration was never recorded")
}

func TestInstanceResizeShortOfBalance(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(nil)
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	cfg["plan"] = "c2m4"

	// Short at plan time: said then, and refused before anything changes.
	api.infra.resizeShort = "1234"
	p := r.plan(st, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	expectWarning(t, p.diags, "The subscription's balance is short by 12.34 BRL")
	_, diags := r.apply(p)
	expectError(t, diags, "the subscription's balance is short by 12.34 BRL (shortfall_cents: 1234)")
	if n := len(api.callsTo("POST", "/v1/instances/"+id+"/resize")); n != 0 {
		t.Fatalf("%d resizes sent", n)
	}

	// Short only when applying (the balance moved): the API's 402.
	api.infra.resizeShort, api.infra.resizeShortOnApply = "", "990"
	p = r.plan(st, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	_, diags = r.apply(p)
	expectError(t, diags, "The balance does not cover the resize")
	expectError(t, diags, "needs 9.90 BRL more on subscription sub-9 (shortfall_cents: 990)")
	expectError(t, diags, "POST /v1/subscriptions/sub-9/top-ups")
	if *api.infra.instances[id].inst.OfferingCode != "c1m1" {
		t.Fatal("resized without the money")
	}
}

func TestInstanceExtraAddressesAtCreate(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(map[string]any{"extra_ipv4": 2, "extra_ipv6": 1})
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	// The order cannot ask for them: they are added once it is built.
	api.expectBody("POST", "/v1/instances/"+id+"/resize", `{"offering_code":"c1m1","ipv4_add_count":2,"ipv6_add_count":1}`)
	if num(t, st, "extra_ipv4") != 2 || num(t, st, "extra_ipv6") != 1 || len(attrList(t, st, "ip_addresses")) != 5 {
		t.Fatalf("state %v", st)
	}
	imp, diags := r.importState(id)
	failOnDiags(t, "import", diags)
	if num(t, imp, "extra_ipv4") != 2 || num(t, imp, "main_disk_extra_gb") != 0 {
		t.Fatalf("imported %v", imp)
	}
}

func TestInstancePowerWaitsWhileBusy(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(nil)
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	api.infra.instances[id].busyPolls = 2 // a backup, say
	api.resetCalls()
	cfg["power_state"] = "stopped"
	st = r.update(st, r.config(cfg))
	if n := len(api.callsTo("POST", "/v1/instances/"+id+"/power")); n != 3 {
		t.Fatalf("%d power calls, want 2 refused while busy and 1 that went through", n)
	}
	if str(t, st, "power_state") != "stopped" {
		t.Fatalf("state %v", st)
	}
}

func TestInstanceValidation(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	custom := instanceConfig(map[string]any{"custom_image_id": "0193f0e2-0000-7000-8000-00000000abcd", "ssh_key_ids": []string{"k"}})
	delete(custom, "image")
	expectError(t, r.validate(r.config(custom)), "custom_image_id")
	both := instanceConfig(map[string]any{"custom_image_id": "0193f0e2-0000-7000-8000-00000000abcd"})
	expectError(t, r.validate(r.config(both)), "image")
	expectError(t, r.validate(r.config(instanceConfig(map[string]any{"password": "short"}))), "password")
	expectError(t, r.validate(r.config(instanceConfig(map[string]any{"password": "has spaces in it"}))), "password")
	expectError(t, r.validate(r.config(instanceConfig(map[string]any{"name": strings.Repeat("x", 121)}))), "name")
	expectError(t, r.validate(r.config(instanceConfig(map[string]any{"region_id": "sp"}))), "region id")
	expectError(t, r.validate(r.config(instanceConfig(map[string]any{"network_mode": "private"}))), "A private instance needs a VPC")
}

func TestInstanceDataSources(t *testing.T) {
	api := newFakeAPI(t)
	a := api.seedInstance("web-a", "3")
	api.seedInstance("web-b", "5")
	api.seedInstance("db", "3")
	project := api.seedProject("shop")
	api.join(project.ID, api.instanceMember(a))
	h := newHarness(t, api.URL())

	one, diags := h.readDataSource("masterdaweb_instance", map[string]any{"name": "web-a"})
	failOnDiags(t, "by name", diags)
	if str(t, one, "id") != a.inst.ID || str(t, one, "ipv4_address") != a.inst.IPAddresses[0].Address ||
		str(t, one, "plan") != "c1m1" || str(t, one, "power_state") != "running" {
		t.Fatalf("instance %v", one)
	}
	byID, diags := h.readDataSource("masterdaweb_instance", map[string]any{"id": a.inst.ID})
	failOnDiags(t, "by id", diags)
	if str(t, byID, "name") != "web-a" {
		t.Fatalf("instance %v", byID)
	}
	_, diags = h.readDataSource("masterdaweb_instance", map[string]any{"name": "nope"})
	expectError(t, diags, `No instance is named "nope"`)

	names := func(v map[string]any) string {
		st, diags := h.readDataSource("masterdaweb_instances", v)
		failOnDiags(t, "list", diags)
		var out []string
		n := len(attrList(t, st, "instances"))
		for i := 0; i < n; i++ {
			out = append(out, str(t, st, "instances", i, "name"))
		}
		return strings.Join(out, ",")
	}
	if got := names(map[string]any{}); got != "web-a,web-b,db" {
		t.Fatalf("all: %s", got)
	}
	if got := names(map[string]any{"name_regex": "^web-"}); got != "web-a,web-b" {
		t.Fatalf("by name: %s", got)
	}
	if got := names(map[string]any{"region_id": "3"}); got != "web-a,db" {
		t.Fatalf("by region: %s", got)
	}
	if got := names(map[string]any{"project_id": project.ID}); got != "web-a" {
		t.Fatalf("by project: %s", got)
	}
	_, diags = h.readDataSource("masterdaweb_instances", map[string]any{"name_regex": "("})
	expectError(t, diags, "Invalid regular expression")
}

func TestInstanceShowsWhatItWasBuiltWith(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(nil)
	st := r.create(r.config(cfg))
	id := str(t, st, "id")

	// Reinstalled elsewhere: the plan shows it, and reinstalls it back.
	api.infra.instances[id].image = "debian-12"
	refreshed := r.read(st)
	if str(t, refreshed, "image") != "debian-12" {
		t.Fatalf("refreshed %v", refreshed)
	}
	p := r.plan(refreshed, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	expectWarning(t, p.diags, "Changing image reinstalls the instance "+id)

	// An import knows the image and the subscription: a configuration that
	// says another is a change, not an adoption.
	imp, diags := r.importState(id)
	failOnDiags(t, "import", diags)
	if str(t, imp, "image") != "debian-12" || str(t, imp, "subscription_id") != "sub-9" || !isNull(t, imp, "order_id") {
		t.Fatalf("imported %v", imp)
	}
	p = r.plan(imp, r.config(cfg))
	failOnDiags(t, "plan after import", p.diags)
	expectWarning(t, p.diags, "Changing image reinstalls the instance "+id)
	moved := instanceConfig(map[string]any{"image": "debian-12", "subscription_id": "sub-10"})
	expectError(t, r.plan(imp, r.config(moved)).diags, "Cannot change subscription_id in place")
}

func TestInstanceOnAnAPIBeforeImageAndProject(t *testing.T) {
	api := newFakeAPI(t)
	api.infra.legacyFields = true
	fi := api.seedInstance("legacy", "3")
	project := api.seedProject("shop")
	api.join(project.ID, api.instanceMember(fi))
	h := newHarness(t, api.URL())
	r := h.resource("masterdaweb_instance")
	imp, diags := r.importState(fi.inst.ID)
	failOnDiags(t, "import", diags)
	if !isNull(t, imp, "image") || !isNull(t, imp, "project_id") {
		t.Fatalf("imported %v", imp)
	}
	// What the API does not say is taken from the configuration.
	api.resetCalls()
	cfg := r.config(instanceConfig(map[string]any{"name": "legacy"}))
	p := r.plan(imp, cfg)
	failOnDiags(t, "plan", p.diags)
	st, diags := r.apply(p)
	failOnDiags(t, "apply", diags)
	if got := api.mutations(); len(got) != 0 {
		t.Fatalf("calls %v", got)
	}
	r.expectNoDiff(st, cfg)

	// Instances that do not say their project: the project is asked.
	got, diags := h.readDataSource("masterdaweb_instances", map[string]any{"project_id": project.ID})
	failOnDiags(t, "instances", diags)
	if n := len(attrList(t, got, "instances")); n != 1 || len(api.callsTo("GET", "/v1/projects/"+project.ID+"/resources")) != 1 {
		t.Fatalf("instances %v", got)
	}
}

func TestInstanceWritesWaitOnlyForWhatIsBusy(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(nil)
	st := r.create(r.config(cfg))
	id := str(t, st, "id")

	// Rescue mode is no reason to wait: said at once.
	api.infra.instances[id].rescue = true
	cfg["power_state"] = "stopped"
	p := r.plan(st, r.config(cfg))
	failOnDiags(t, "plan", p.diags)
	_, diags := r.apply(p)
	expectError(t, diags, "is in rescue mode: leave it first. (HTTP 409, INSTANCE_IN_RESCUE")
	if n := len(api.callsTo("POST", "/v1/instances/"+id+"/power")); n != 1 {
		t.Fatalf("%d power calls", n)
	}
}

func TestInstanceReleasesTheNewestAddresses(t *testing.T) {
	api := newFakeAPI(t)
	r := newHarness(t, api.URL()).resource("masterdaweb_instance")
	cfg := instanceConfig(map[string]any{"extra_ipv4": 2})
	st := r.create(r.config(cfg))
	id := str(t, st, "id")
	fi := api.infra.instances[id]
	// The newest has the smallest id here: only when it was assigned says so.
	newest := fi.assignments[len(fi.assignments)-1]
	api.resetCalls()
	cfg["extra_ipv4"] = 1
	st = r.update(st, r.config(cfg))
	api.expectBody("POST", "/v1/instances/"+id+"/resize", `{"offering_code":"c1m1","ipv4_remove_assignment_ids":["`+newest+`"]}`)
	if num(t, st, "extra_ipv4") != 1 {
		t.Fatalf("state %v", st)
	}
}
