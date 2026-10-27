package provider

// A small stand-in for Terraform core, so every resource is tested the way
// Terraform drives it — validate, plan, apply, refresh, plan again — through
// the plugin protocol, without a Terraform binary. It builds the "proposed
// new state" as core does (internal/plans/objchange.ProposedNew), checks an
// applied state against its plan as core does, and fails a test whose
// second plan is not empty: the perpetual diff these resources must not have.

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

type harness struct {
	t           *testing.T
	server      tfprotov6.ProviderServer
	resources   map[string]*tfprotov6.Schema
	dataSources map[string]*tfprotov6.Schema
}

// unknown marks a configuration value that is not known until apply.
type unknownValue struct{}

var unknown = unknownValue{}

func newHarness(t *testing.T, apiURL string) *harness {
	t.Helper()
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	failOnDiags(t, "GetProviderSchema", schemas.Diagnostics)
	h := &harness{t: t, server: server, resources: schemas.ResourceSchemas, dataSources: schemas.DataSourceSchemas}

	typ := schemas.Provider.ValueType()
	cfg := tftypes.NewValue(typ, map[string]tftypes.Value{
		"api_url": tftypes.NewValue(tftypes.String, apiURL),
		"token":   tftypes.NewValue(tftypes.String, "mwk_test"),
	})
	resp, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
		TerraformVersion: "1.9.8",
		Config:           dynamic(t, typ, cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	failOnDiags(t, "ConfigureProvider", resp.Diagnostics)
	return h
}

func dynamic(t *testing.T, typ tftypes.Type, v tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dv, err := tfprotov6.NewDynamicValue(typ, v)
	if err != nil {
		t.Fatalf("encoding %v: %v", v, err)
	}
	return &dv
}

func undynamic(t *testing.T, typ tftypes.Type, dv *tfprotov6.DynamicValue) tftypes.Value {
	t.Helper()
	if dv == nil {
		return tftypes.NewValue(typ, nil)
	}
	v, err := dv.Unmarshal(typ)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return v
}

func diagText(diags []*tfprotov6.Diagnostic) string {
	var out []string
	for _, d := range diags {
		sev := "warning"
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			sev = "error"
		}
		at := ""
		if d.Attribute != nil {
			at = " at " + d.Attribute.String()
		}
		out = append(out, fmt.Sprintf("%s%s: %s: %s", sev, at, d.Summary, d.Detail))
	}
	return strings.Join(out, "\n")
}

func hasError(diags []*tfprotov6.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

func failOnDiags(t *testing.T, step string, diags []*tfprotov6.Diagnostic) {
	t.Helper()
	if hasError(diags) {
		t.Fatalf("%s failed:\n%s", step, diagText(diags))
	}
}

// ── Values from Go ─────────────────────────────────────────────────────────

// toValue builds a value of a type from Go: nil is null, unknown is unknown,
// maps are objects (missing attributes null), slices are lists or sets.
func toValue(t *testing.T, typ tftypes.Type, v any) tftypes.Value {
	t.Helper()
	if v == nil {
		return tftypes.NewValue(typ, nil)
	}
	if _, ok := v.(unknownValue); ok {
		return tftypes.NewValue(typ, tftypes.UnknownValue)
	}
	switch {
	case typ.Is(tftypes.String):
		return tftypes.NewValue(tftypes.String, v.(string))
	case typ.Is(tftypes.Bool):
		return tftypes.NewValue(tftypes.Bool, v.(bool))
	case typ.Is(tftypes.Number):
		switch n := v.(type) {
		case int:
			return tftypes.NewValue(tftypes.Number, new(big.Float).SetInt64(int64(n)))
		case int64:
			return tftypes.NewValue(tftypes.Number, new(big.Float).SetInt64(n))
		case float64:
			return tftypes.NewValue(tftypes.Number, big.NewFloat(n))
		}
	}
	switch tt := typ.(type) {
	case tftypes.Map:
		out := map[string]tftypes.Value{}
		switch m := v.(type) {
		case map[string]string:
			for k, e := range m {
				out[k] = toValue(t, tt.ElementType, e)
			}
		case map[string]any:
			for k, e := range m {
				out[k] = toValue(t, tt.ElementType, e)
			}
		default:
			t.Fatalf("a map needs a map, got %T", v)
		}
		return tftypes.NewValue(tt, out)
	case tftypes.List:
		return tftypes.NewValue(tt, elements(t, tt.ElementType, v))
	case tftypes.Set:
		return tftypes.NewValue(tt, elements(t, tt.ElementType, v))
	case tftypes.Object:
		in, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("an object needs a map, got %T", v)
		}
		attrs := map[string]tftypes.Value{}
		for name, at := range tt.AttributeTypes {
			attrs[name] = toValue(t, at, in[name])
		}
		for name := range in {
			if _, ok := tt.AttributeTypes[name]; !ok {
				t.Fatalf("no attribute %q in %s", name, tt)
			}
		}
		return tftypes.NewValue(tt, attrs)
	}
	t.Fatalf("cannot build a %s from %T", typ, v)
	return tftypes.Value{}
}

func elements(t *testing.T, elem tftypes.Type, v any) []tftypes.Value {
	t.Helper()
	var out []tftypes.Value
	switch items := v.(type) {
	case []string:
		for _, s := range items {
			out = append(out, toValue(t, elem, s))
		}
	case []any:
		for _, it := range items {
			out = append(out, toValue(t, elem, it))
		}
	case []map[string]any:
		for _, it := range items {
			out = append(out, toValue(t, elem, it))
		}
	default:
		t.Fatalf("a collection needs a slice, got %T", v)
	}
	if out == nil {
		out = []tftypes.Value{}
	}
	return out
}

// ── Reading values ─────────────────────────────────────────────────────────

func attrAt(t *testing.T, v tftypes.Value, steps ...any) tftypes.Value {
	t.Helper()
	cur := v
	for _, s := range steps {
		switch k := s.(type) {
		case string:
			var m map[string]tftypes.Value
			if err := cur.As(&m); err != nil {
				t.Fatalf("%v is not an object: %v", cur, err)
			}
			next, ok := m[k]
			if !ok {
				t.Fatalf("no attribute %q", k)
			}
			cur = next
		case int:
			var l []tftypes.Value
			if err := cur.As(&l); err != nil {
				t.Fatalf("%v is not a list: %v", cur, err)
			}
			if k >= len(l) {
				t.Fatalf("no element %d in %v", k, cur)
			}
			cur = l[k]
		}
	}
	return cur
}

func str(t *testing.T, v tftypes.Value, steps ...any) string {
	t.Helper()
	a := attrAt(t, v, steps...)
	if a.IsNull() || !a.IsKnown() {
		t.Fatalf("%v at %v is null or unknown", a, steps)
	}
	var s string
	if err := a.As(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func num(t *testing.T, v tftypes.Value, steps ...any) int64 {
	t.Helper()
	a := attrAt(t, v, steps...)
	var f big.Float
	if err := a.As(&f); err != nil {
		t.Fatal(err)
	}
	n, _ := f.Int64()
	return n
}

func boolean(t *testing.T, v tftypes.Value, steps ...any) bool {
	t.Helper()
	var b bool
	if err := attrAt(t, v, steps...).As(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func isNull(t *testing.T, v tftypes.Value, steps ...any) bool {
	t.Helper()
	return attrAt(t, v, steps...).IsNull()
}

func strs(t *testing.T, v tftypes.Value, steps ...any) []string {
	t.Helper()
	var items []tftypes.Value
	if err := attrAt(t, v, steps...).As(&items); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, it := range items {
		var s string
		if err := it.As(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// ── Core's proposed new state ──────────────────────────────────────────────

func allNull(typ tftypes.Type) tftypes.Value {
	obj := typ.(tftypes.Object)
	attrs := map[string]tftypes.Value{}
	for name, at := range obj.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	return tftypes.NewValue(typ, attrs)
}

func proposedNew(s *tfprotov6.Schema, prior, config tftypes.Value) tftypes.Value {
	if config.IsNull() || !config.IsKnown() {
		return prior
	}
	if prior.IsNull() {
		prior = allNull(s.ValueType())
	}
	return tftypes.NewValue(s.ValueType(), proposedNewAttributes(s.Block.Attributes, prior, config))
}

func objectAttrs(v tftypes.Value) map[string]tftypes.Value {
	m := map[string]tftypes.Value{}
	if v.IsNull() || !v.IsKnown() {
		return m
	}
	_ = v.As(&m)
	return m
}

func proposedNewAttributes(attrs []*tfprotov6.SchemaAttribute, prior, config tftypes.Value) map[string]tftypes.Value {
	priorAttrs, configAttrs := objectAttrs(prior), objectAttrs(config)
	out := map[string]tftypes.Value{}
	for _, a := range attrs {
		typ := a.ValueType()
		priorV, ok := priorAttrs[a.Name]
		if !ok {
			priorV = tftypes.NewValue(typ, nil)
		}
		configV, ok := configAttrs[a.Name]
		if !ok {
			configV = tftypes.NewValue(typ, nil)
		}
		var newV tftypes.Value
		switch {
		case !a.Computed:
			if a.NestedType == nil {
				newV = configV
			} else {
				newV = proposedNewNested(a.NestedType, priorV, configV)
			}
		case configV.IsNull():
			newV = priorV
			if optionalValueNotComputable(a, priorV) {
				newV = configV
			}
		case a.NestedType != nil:
			newV = proposedNewNested(a.NestedType, priorV, configV)
		default:
			newV = configV
		}
		out[a.Name] = newV
	}
	return out
}

func proposedNewNested(obj *tfprotov6.SchemaObject, prior, config tftypes.Value) tftypes.Value {
	if !config.IsKnown() || config.IsNull() {
		return config
	}
	switch obj.Nesting {
	case tfprotov6.SchemaObjectNestingModeSingle:
		return tftypes.NewValue(config.Type(), proposedNewAttributes(obj.Attributes, prior, config))
	case tfprotov6.SchemaObjectNestingModeList:
		var cfg, pri []tftypes.Value
		_ = config.As(&cfg)
		if len(cfg) == 0 {
			return config
		}
		if prior.IsKnown() && !prior.IsNull() {
			_ = prior.As(&pri)
		}
		var out []tftypes.Value
		for i, ce := range cfg {
			if i >= len(pri) {
				out = append(out, ce)
				continue
			}
			out = append(out, tftypes.NewValue(ce.Type(), proposedNewAttributes(obj.Attributes, pri[i], ce)))
		}
		return tftypes.NewValue(config.Type(), out)
	case tfprotov6.SchemaObjectNestingModeSet:
		// Set elements are not matched with prior ones: as core does for a
		// set of attributes only the configuration sets, the config is it.
		return config
	default:
		panic("the harness does not model " + obj.Nesting.String() + " nesting")
	}
}

// optionalValueNotComputable: a prior nested value holding attributes that
// only configuration sets must have come from configuration.
func optionalValueNotComputable(a *tfprotov6.SchemaAttribute, v tftypes.Value) bool {
	if !a.Optional || a.NestedType == nil {
		return false
	}
	return holdsConfigured(a.NestedType, v)
}

func holdsConfigured(obj *tfprotov6.SchemaObject, v tftypes.Value) bool {
	if v.IsNull() || !v.IsKnown() {
		return false
	}
	check := func(elem tftypes.Value) bool {
		attrs := objectAttrs(elem)
		for _, a := range obj.Attributes {
			av, ok := attrs[a.Name]
			if !ok || av.IsNull() {
				continue
			}
			if !a.Computed {
				return true
			}
			if a.NestedType != nil && holdsConfigured(a.NestedType, av) {
				return true
			}
		}
		return false
	}
	if obj.Nesting == tfprotov6.SchemaObjectNestingModeSingle {
		return check(v)
	}
	var elems []tftypes.Value
	_ = v.As(&elems)
	for _, e := range elems {
		if check(e) {
			return true
		}
	}
	return false
}

// ── Core's checks ──────────────────────────────────────────────────────────

// differences lists where two values differ, for failure messages.
func differences(path string, a, b tftypes.Value) []string {
	if a.Equal(b) {
		return nil
	}
	if !a.IsKnown() || !b.IsKnown() || a.IsNull() || b.IsNull() {
		return []string{fmt.Sprintf("%s: %v → %v", path, a, b)}
	}
	switch a.Type().(type) {
	case tftypes.Object:
		am, bm := objectAttrs(a), objectAttrs(b)
		var names []string
		for n := range am {
			names = append(names, n)
		}
		sort.Strings(names)
		var out []string
		for _, n := range names {
			out = append(out, differences(path+"."+n, am[n], bm[n])...)
		}
		return out
	case tftypes.List:
		var al, bl []tftypes.Value
		_ = a.As(&al)
		_ = b.As(&bl)
		if len(al) != len(bl) {
			return []string{fmt.Sprintf("%s: %v → %v", path, a, b)}
		}
		var out []string
		for i := range al {
			out = append(out, differences(fmt.Sprintf("%s[%d]", path, i), al[i], bl[i])...)
		}
		return out
	}
	return []string{fmt.Sprintf("%s: %v → %v", path, a, b)}
}

// compatible fails where an applied value contradicts what was planned: a
// known planned value must come back exactly, and nothing may stay unknown.
func compatible(path string, planned, applied tftypes.Value) []string {
	if !applied.IsFullyKnown() {
		return []string{path + ": unknown after apply"}
	}
	if !planned.IsKnown() {
		return nil
	}
	if planned.IsNull() || applied.IsNull() || planned.IsFullyKnown() {
		if planned.Equal(applied) {
			return nil
		}
		return []string{fmt.Sprintf("%s: planned %v, applied %v", path, planned, applied)}
	}
	switch planned.Type().(type) {
	case tftypes.Object:
		pm, am := objectAttrs(planned), objectAttrs(applied)
		var out []string
		for n := range pm {
			out = append(out, compatible(path+"."+n, pm[n], am[n])...)
		}
		return out
	case tftypes.List:
		var pl, al []tftypes.Value
		_ = planned.As(&pl)
		_ = applied.As(&al)
		if len(pl) != len(al) {
			return []string{fmt.Sprintf("%s: planned %d elements, applied %d", path, len(pl), len(al))}
		}
		var out []string
		for i := range pl {
			out = append(out, compatible(fmt.Sprintf("%s[%d]", path, i), pl[i], al[i])...)
		}
		return out
	}
	return nil // a set with unknowns in it: core is lenient, so are we
}

// ── Resources ──────────────────────────────────────────────────────────────

type resourceUnderTest struct {
	h      *harness
	name   string
	schema *tfprotov6.Schema
}

func (h *harness) resource(name string) *resourceUnderTest {
	h.t.Helper()
	s, ok := h.resources[name]
	if !ok {
		h.t.Fatalf("no resource %s", name)
	}
	return &resourceUnderTest{h: h, name: name, schema: s}
}

func (r *resourceUnderTest) typ() tftypes.Type { return r.schema.ValueType() }

func (r *resourceUnderTest) null() tftypes.Value { return tftypes.NewValue(r.typ(), nil) }

// config builds a configuration from Go values.
func (r *resourceUnderTest) config(attrs map[string]any) tftypes.Value {
	r.h.t.Helper()
	return toValue(r.h.t, r.typ(), attrs)
}

func (r *resourceUnderTest) validate(config tftypes.Value) []*tfprotov6.Diagnostic {
	r.h.t.Helper()
	resp, err := r.h.server.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: r.name,
		Config:   dynamic(r.h.t, r.typ(), config),
	})
	if err != nil {
		r.h.t.Fatal(err)
	}
	return resp.Diagnostics
}

type planned struct {
	state           tftypes.Value
	private         []byte
	requiresReplace []string
	diags           []*tfprotov6.Diagnostic
	prior, config   tftypes.Value
}

// plan is PlanResourceChange with core's proposed new state; a null config
// plans the resource's destruction.
func (r *resourceUnderTest) plan(prior, config tftypes.Value) planned {
	r.h.t.Helper()
	proposed := tftypes.NewValue(r.typ(), nil)
	if !config.IsNull() {
		proposed = proposedNew(r.schema, prior, config)
	}
	resp, err := r.h.server.PlanResourceChange(context.Background(), &tfprotov6.PlanResourceChangeRequest{
		TypeName:         r.name,
		PriorState:       dynamic(r.h.t, r.typ(), prior),
		ProposedNewState: dynamic(r.h.t, r.typ(), proposed),
		Config:           dynamic(r.h.t, r.typ(), config),
	})
	if err != nil {
		r.h.t.Fatal(err)
	}
	p := planned{
		state:   undynamic(r.h.t, r.typ(), resp.PlannedState),
		private: resp.PlannedPrivate,
		diags:   resp.Diagnostics,
		prior:   prior,
		config:  config,
	}
	for _, rr := range resp.RequiresReplace {
		p.requiresReplace = append(p.requiresReplace, rr.String())
	}
	sort.Strings(p.requiresReplace)
	return p
}

func (p planned) noop() bool { return p.state.Equal(p.prior) }

// apply is ApplyResourceChange; it returns the new state and diagnostics.
func (r *resourceUnderTest) apply(p planned) (tftypes.Value, []*tfprotov6.Diagnostic) {
	r.h.t.Helper()
	resp, err := r.h.server.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       r.name,
		PriorState:     dynamic(r.h.t, r.typ(), p.prior),
		PlannedState:   dynamic(r.h.t, r.typ(), p.state),
		Config:         dynamic(r.h.t, r.typ(), p.config),
		PlannedPrivate: p.private,
	})
	if err != nil {
		r.h.t.Fatal(err)
	}
	newState := undynamic(r.h.t, r.typ(), resp.NewState)
	if !hasError(resp.Diagnostics) && !p.state.IsNull() {
		if diffs := compatible(r.name, p.state, newState); len(diffs) > 0 {
			r.h.t.Fatalf("provider produced an inconsistent result after apply:\n%s", strings.Join(diffs, "\n"))
		}
	}
	return newState, resp.Diagnostics
}

// read is ReadResource: the refreshed state (null when the resource is gone).
func (r *resourceUnderTest) read(state tftypes.Value) tftypes.Value {
	r.h.t.Helper()
	s, diags := r.readDiags(state)
	failOnDiags(r.h.t, "refresh", diags)
	return s
}

func (r *resourceUnderTest) readDiags(state tftypes.Value) (tftypes.Value, []*tfprotov6.Diagnostic) {
	r.h.t.Helper()
	resp, err := r.h.server.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName:     r.name,
		CurrentState: dynamic(r.h.t, r.typ(), state),
	})
	if err != nil {
		r.h.t.Fatal(err)
	}
	return undynamic(r.h.t, r.typ(), resp.NewState), resp.Diagnostics
}

// expectNoDiff refreshes the state and plans the configuration again: the
// plan must change nothing.
func (r *resourceUnderTest) expectNoDiff(state, config tftypes.Value) tftypes.Value {
	r.h.t.Helper()
	refreshed := r.read(state)
	if refreshed.IsNull() {
		r.h.t.Fatalf("%s disappeared on refresh", r.name)
	}
	p := r.plan(refreshed, config)
	failOnDiags(r.h.t, "plan after refresh", p.diags)
	if !p.noop() {
		r.h.t.Fatalf("perpetual diff: the plan after refresh changes\n%s", strings.Join(differences(r.name, refreshed, p.state), "\n"))
	}
	if len(p.requiresReplace) > 0 {
		r.h.t.Fatalf("an empty plan requires replacing %v", p.requiresReplace)
	}
	return refreshed
}

// create validates, plans, applies and checks the result: no diff after.
func (r *resourceUnderTest) create(config tftypes.Value) tftypes.Value {
	r.h.t.Helper()
	failOnDiags(r.h.t, "validate", r.validate(config))
	p := r.plan(r.null(), config)
	failOnDiags(r.h.t, "plan", p.diags)
	state, diags := r.apply(p)
	failOnDiags(r.h.t, "apply", diags)
	return r.expectNoDiff(state, config)
}

// update plans a new configuration over a state, applies it and checks the
// result. It returns the new state and the attributes that would have
// required a replacement (the update then fails the test: use replace).
func (r *resourceUnderTest) update(state, config tftypes.Value) tftypes.Value {
	r.h.t.Helper()
	failOnDiags(r.h.t, "validate", r.validate(config))
	p := r.plan(state, config)
	failOnDiags(r.h.t, "plan", p.diags)
	if len(p.requiresReplace) > 0 {
		r.h.t.Fatalf("the change requires a replacement (%v)", p.requiresReplace)
	}
	if p.noop() {
		r.h.t.Fatalf("the change plans nothing")
	}
	newState, diags := r.apply(p)
	failOnDiags(r.h.t, "apply", diags)
	return r.expectNoDiff(newState, config)
}

// destroy plans and applies the resource's destruction.
func (r *resourceUnderTest) destroy(state tftypes.Value) []*tfprotov6.Diagnostic {
	r.h.t.Helper()
	p := r.plan(state, r.null())
	failOnDiags(r.h.t, "plan destroy", p.diags)
	newState, diags := r.apply(p)
	if !hasError(diags) && !newState.IsNull() {
		r.h.t.Fatalf("the state after destroy is %v", newState)
	}
	return diags
}

// importState is terraform import: ImportResourceState, then a refresh.
func (r *resourceUnderTest) importState(id string) (tftypes.Value, []*tfprotov6.Diagnostic) {
	r.h.t.Helper()
	resp, err := r.h.server.ImportResourceState(context.Background(), &tfprotov6.ImportResourceStateRequest{
		TypeName: r.name,
		ID:       id,
	})
	if err != nil {
		r.h.t.Fatal(err)
	}
	if hasError(resp.Diagnostics) {
		return r.null(), resp.Diagnostics
	}
	if len(resp.ImportedResources) != 1 {
		r.h.t.Fatalf("%d imported resources", len(resp.ImportedResources))
	}
	state := undynamic(r.h.t, r.typ(), resp.ImportedResources[0].State)
	return r.readDiags(state)
}

// ── Data sources ───────────────────────────────────────────────────────────

func (h *harness) readDataSource(name string, attrs map[string]any) (tftypes.Value, []*tfprotov6.Diagnostic) {
	h.t.Helper()
	s, ok := h.dataSources[name]
	if !ok {
		h.t.Fatalf("no data source %s", name)
	}
	cfg := toValue(h.t, s.ValueType(), attrs)
	ctx := context.Background()
	v, err := h.server.ValidateDataResourceConfig(ctx, &tfprotov6.ValidateDataResourceConfigRequest{
		TypeName: name, Config: dynamic(h.t, s.ValueType(), cfg),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	if hasError(v.Diagnostics) {
		return tftypes.NewValue(s.ValueType(), nil), v.Diagnostics
	}
	resp, err := h.server.ReadDataSource(ctx, &tfprotov6.ReadDataSourceRequest{
		TypeName: name, Config: dynamic(h.t, s.ValueType(), cfg),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return undynamic(h.t, s.ValueType(), resp.State), resp.Diagnostics
}

func expectError(t *testing.T, diags []*tfprotov6.Diagnostic, contains string) {
	t.Helper()
	if !hasError(diags) {
		t.Fatalf("expected an error containing %q, got:\n%s", contains, diagText(diags))
	}
	if !strings.Contains(diagText(diags), contains) {
		t.Fatalf("expected an error containing %q, got:\n%s", contains, diagText(diags))
	}
}

func expectWarning(t *testing.T, diags []*tfprotov6.Diagnostic, contains string) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityWarning && strings.Contains(d.Summary+": "+d.Detail, contains) {
			return
		}
	}
	t.Fatalf("expected a warning containing %q, got:\n%s", contains, diagText(diags))
}
