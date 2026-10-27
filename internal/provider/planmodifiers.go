package provider

import (
	"context"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ── Keep the state's value while what it depends on stays ─────────────────

// useStateIfUnchanged is UseStateForUnknown, but only while the attributes
// the value follows from are unchanged: a repository's default branch is the
// state's own as long as the repository is the same one.
func useStateIfUnchanged(siblings ...path.Path) planmodifier.String {
	return useStateIfUnchangedModifier{siblings: siblings}
}

type useStateIfUnchangedModifier struct{ siblings []path.Path }

func (m useStateIfUnchangedModifier) Description(context.Context) string {
	var names []string
	for _, p := range m.siblings {
		names = append(names, p.String())
	}
	return "Keeps the value from the state while " + strings.Join(names, " and ") + " stay the same."
}

func (m useStateIfUnchangedModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m useStateIfUnchangedModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// As UseStateForUnknown: a null prior value is kept too (a public
	// repository has no connection).
	if req.State.Raw.IsNull() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if !siblingsUnchanged(ctx, req.Plan, req.State, m.siblings) {
		return
	}
	resp.PlanValue = req.StateValue
}

// useStateIfUnchangedInt64 is useStateIfUnchanged for a number: a database's
// disk is the state's own while its size stays (a bigger size grows it).
func useStateIfUnchangedInt64(siblings ...path.Path) planmodifier.Int64 {
	return useStateIfUnchangedModifier{siblings: siblings}
}

func (m useStateIfUnchangedModifier) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if !siblingsUnchanged(ctx, req.Plan, req.State, m.siblings) {
		return
	}
	resp.PlanValue = req.StateValue
}

func siblingsUnchanged(ctx context.Context, plan tfsdk.Plan, state tfsdk.State, siblings []path.Path) bool {
	for _, p := range siblings {
		var planned, prior types.String
		if d := plan.GetAttribute(ctx, p, &planned); d.HasError() {
			return false
		}
		if d := state.GetAttribute(ctx, p, &prior); d.HasError() {
			return false
		}
		if !planned.Equal(prior) {
			return false
		}
	}
	return true
}

// ── A service's CPU and memory follow from its size ───────────────────────

// serviceSizes are the instance sizes on offer: vCPU (thousandths) and MiB.
var serviceSizes = map[string][2]int64{
	"x-small": {250, 512},
	"small":   {500, 1024},
	"medium":  {1000, 2048},
	"large":   {2000, 4096},
	"x-large": {4000, 8192},
}

func sizeDerived(memory bool) planmodifier.Int64 { return sizeDerivedModifier{memory: memory} }

type sizeDerivedModifier struct{ memory bool }

func (m sizeDerivedModifier) Description(context.Context) string {
	return "Follows from the planned size."
}

func (m sizeDerivedModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m sizeDerivedModifier) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.PlanValue.IsUnknown() {
		return
	}
	var size types.String
	if d := req.Plan.GetAttribute(ctx, path.Root("size"), &size); d.HasError() || !known(size) {
		return
	}
	s, ok := serviceSizes[size.ValueString()]
	if !ok {
		return
	}
	if m.memory {
		resp.PlanValue = types.Int64Value(s[1])
	} else {
		resp.PlanValue = types.Int64Value(s[0])
	}
}

// ── Replacements ───────────────────────────────────────────────────────────

// requiresReplaceWhenTurnedOff replaces a variable that stops being a secret:
// the API keeps a secret write-only for good, so only a new variable can be
// a plain one.
func requiresReplaceWhenTurnedOff() planmodifier.Bool {
	return boolplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = req.StateValue.ValueBool() && known(req.PlanValue) && !req.PlanValue.ValueBool()
		},
		"Changing it from true to false replaces the variable: a secret stays a secret.",
		"Changing it from `true` to `false` replaces the variable: a secret stays a secret.",
	)
}

// requiresReplaceUnlessImported replaces the resource when a value the API
// never shows again changes — except right after an import, when the state
// has no value to compare and the configuration's is taken as it is.
func requiresReplaceUnlessImported() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull()
		},
		"Changing it replaces the resource (after an import, the configuration's value is taken as it is).",
		"Changing it replaces the resource (after an import, the configuration's value is taken as it is).",
	)
}

// requiresReplaceOnSwitch replaces a service whose source moves between Git
// and an image: the API changes a source within its kind only.
func requiresReplaceOnSwitch() planmodifier.Object {
	return objectplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = req.StateValue.IsNull() != req.PlanValue.IsNull()
		},
		"Moving a service between a Git source and an image replaces it.",
		"Moving a service between a `git` source and an `image` replaces it.",
	)
}

// ── Refusals said at plan time ────────────────────────────────────────────

// diskOnlyGrows refuses a smaller disk: a database's disk never shrinks.
func diskOnlyGrows() planmodifier.Int64 { return diskOnlyGrowsModifier{} }

type diskOnlyGrowsModifier struct{}

func (diskOnlyGrowsModifier) Description(context.Context) string {
	return "A disk only grows."
}

func (m diskOnlyGrowsModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (diskOnlyGrowsModifier) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || !known(req.StateValue) || !known(req.PlanValue) {
		return
	}
	if req.PlanValue.ValueInt64() < req.StateValue.ValueInt64() {
		resp.Diagnostics.AddAttributeError(req.Path, "A disk only grows",
			"The database's disk is "+itoa(req.StateValue.ValueInt64())+" GB and never shrinks. "+
				"To use a smaller one, restore the database into a new, smaller database.")
	}
}

// extensionsOnlyAdded refuses taking an extension away: the API never drops
// one (its objects are the customer's data).
func extensionsOnlyAdded() planmodifier.Set { return extensionsOnlyAddedModifier{} }

type extensionsOnlyAddedModifier struct{}

func (extensionsOnlyAddedModifier) Description(context.Context) string {
	return "Extensions are only ever added."
}

func (m extensionsOnlyAddedModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (extensionsOnlyAddedModifier) PlanModifySet(ctx context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	if req.State.Raw.IsNull() || !known(req.StateValue) || !known(req.PlanValue) {
		return
	}
	planned := map[string]bool{}
	for _, e := range listStrings(ctx, req.PlanValue) {
		planned[e] = true
	}
	var removed []string
	for _, e := range listStrings(ctx, req.StateValue) {
		if !planned[e] {
			removed = append(removed, e)
		}
	}
	if len(removed) > 0 {
		sort.Strings(removed)
		resp.Diagnostics.AddAttributeError(req.Path, "Extensions are never removed from here",
			"Keep "+strings.Join(removed, ", ")+" in extensions: an extension's objects are your data, so it is "+
				"only removed by running DROP EXTENSION on the database yourself, knowing what it takes with it.")
	}
}

// ── Gradual rollouts: left out is off ──────────────────────────────────────

// offUnlessRepositoryManaged plans a rollout setting the configuration
// leaves out as off — null for rollout_percent, the default (planned
// already) for rollout_minutes — unless the repository's mw.toml defines it:
// it is then the repository's, and the state's value stays.
func offUnlessRepositoryManaged(field string) planmodifier.Int64 {
	return offUnlessRepositoryManagedModifier{field: field}
}

type offUnlessRepositoryManagedModifier struct{ field string }

func (m offUnlessRepositoryManagedModifier) Description(context.Context) string {
	return "Left out, it is off (or its default), unless the repository's mw.toml defines it."
}

func (m offUnlessRepositoryManagedModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m offUnlessRepositoryManagedModifier) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.ConfigValue.IsNull() {
		return // configured, or not known yet: the configuration's
	}
	if !req.State.Raw.IsNull() && repositoryManaged(ctx, req.State, m.field) {
		resp.PlanValue = req.StateValue
		return
	}
	if m.field == "rollout_percent" {
		resp.PlanValue = types.Int64Null()
	}
}

// repositoryManaged says whether the repository's mw.toml defines a setting.
func repositoryManaged(ctx context.Context, state tfsdk.State, field string) bool {
	var managed types.List
	if d := state.GetAttribute(ctx, path.Root("repository_settings"), &managed); d.HasError() {
		return false
	}
	for _, f := range listStrings(ctx, managed) {
		if f == field {
			return true
		}
	}
	return false
}
