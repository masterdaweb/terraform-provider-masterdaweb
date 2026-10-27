package provider

// Resizing an instance in place — its plan, main disk, extra disks, add-ons
// and public addresses — like DigitalOcean's size or Vultr's plan. The API
// quotes a resize first (what it costs and removes, and why it would be
// refused), then applies it as an operation. What the instance has now is
// read from its resize options: the instance itself does not show it.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// instanceAddon is one of the add-ons the configuration manages.
type instanceAddon struct {
	Code     types.String `tfsdk:"code"`
	Quantity types.Int64  `tfsdk:"quantity"`
}

func addonsOf(ctx context.Context, v types.Set) []instanceAddon {
	if !known(v) {
		return nil
	}
	var out []instanceAddon
	_ = v.ElementsAs(ctx, &out, false)
	return out
}

func int64sOf(ctx context.Context, v types.List) []int64 {
	if !known(v) {
		return nil
	}
	var out []int64
	_ = v.ElementsAs(ctx, &out, false)
	return out
}

func int64List(items []int64) types.List {
	vals := make([]attr.Value, 0, len(items))
	for _, n := range items {
		vals = append(vals, types.Int64Value(n))
	}
	return types.ListValueMust(types.Int64Type, vals)
}

// diskChange is what a planned list of extra disks does to the instance's:
// disks added at the end, or the last ones removed. Kept disks are matched
// by their place in the list, and keep their size; a size of 0 in the state
// is one the API does not show (a disk added elsewhere), which the
// configuration's is taken for.
func diskChange(prior, planned []int64) (add []int64, remove int, err error) {
	for i := 0; i < len(prior) && i < len(planned); i++ {
		if prior[i] != 0 && planned[i] != prior[i] {
			return nil, 0, fmt.Errorf("data disk %d has %d GB, and a disk's size cannot change: add a disk at the end "+
				"of the list, or take the last ones off it (they are removed with their data)", i+1, prior[i])
		}
	}
	if len(planned) > len(prior) {
		add = planned[len(prior):]
	}
	if len(planned) < len(prior) {
		remove = len(prior) - len(planned)
	}
	return add, remove, nil
}

// addonChanges are the add-ons to set: the planned ones whose quantity
// differs from the state's, and 0 for the ones the configuration stopped
// listing. Add-ons it never listed are left as they are.
func addonChanges(prior, planned []instanceAddon) []instanceAddon {
	before := map[string]instanceAddon{}
	for _, a := range prior {
		before[a.Code.ValueString()] = a
	}
	var out []instanceAddon
	seen := map[string]bool{}
	for _, a := range planned {
		code := a.Code.ValueString()
		seen[code] = true
		if b, ok := before[code]; ok && b.Quantity.Equal(a.Quantity) {
			continue
		}
		out = append(out, a)
	}
	for _, a := range prior {
		if !seen[a.Code.ValueString()] {
			out = append(out, instanceAddon{Code: a.Code, Quantity: types.Int64Value(0)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code.ValueString() < out[j].Code.ValueString() })
	return out
}

// resizeChanges names what the plan resizes on an existing instance.
func resizeChanges(ctx context.Context, state, plan instanceModel) []string {
	var changed []string
	if known(plan.Plan) && known(state.Plan) && plan.Plan.ValueString() != state.Plan.ValueString() {
		changed = append(changed, "plan")
	}
	if known(plan.MainDiskExtraGB) && known(state.MainDiskExtraGB) && !plan.MainDiskExtraGB.Equal(state.MainDiskExtraGB) {
		changed = append(changed, "main_disk_extra_gb")
	}
	if known(plan.ExtraDisksGB) && known(state.ExtraDisksGB) {
		if add, remove, err := diskChange(int64sOf(ctx, state.ExtraDisksGB), int64sOf(ctx, plan.ExtraDisksGB)); err != nil || len(add) > 0 || remove > 0 {
			changed = append(changed, "extra_disks_gb")
		}
	}
	if !plan.Addons.IsUnknown() && len(addonChanges(addonsOf(ctx, state.Addons), addonsOf(ctx, plan.Addons))) > 0 {
		changed = append(changed, "addons")
	}
	for _, ip := range []struct {
		name           string
		prior, planned types.Int64
	}{{"extra_ipv4", state.ExtraIPv4, plan.ExtraIPv4}, {"extra_ipv6", state.ExtraIPv6, plan.ExtraIPv6}} {
		if known(ip.planned) && known(ip.prior) && !ip.planned.Equal(ip.prior) {
			changed = append(changed, ip.name)
		}
	}
	return changed
}

// releasable are the addresses of a version a resize releases first: the
// newest ones that are not the primary, by when they were assigned (the API
// lists them oldest first, which decides between those it does not date).
func releasable(opts *client.ResizeOptions, version int64, n int64) []string {
	type cand struct {
		id    string
		at    time.Time
		index int
	}
	var cands []cand
	for i, a := range opts.IPAssignments {
		if a.Version != version || a.Primary {
			continue
		}
		c := cand{id: a.AssignmentID, index: i}
		if a.AssignedAt != nil {
			c.at, _ = time.Parse(time.RFC3339, *a.AssignedAt)
		}
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if !cands[i].at.Equal(cands[j].at) {
			return cands[i].at.After(cands[j].at)
		}
		return cands[i].index > cands[j].index
	})
	var out []string
	for i := int64(0); i < n && int(i) < len(cands); i++ {
		out = append(out, cands[i].id)
	}
	return out
}

// resizeBody is the resize the plan asks for, against what the instance has
// now (opts): the full set of extra disks when they change, the add-ons to
// set, and the addresses to add or release. The main disk's extra is sent
// when the configuration gives it; left out, the API keeps the disk's size.
func resizeBody(ctx context.Context, state, plan instanceModel, sendMainDisk bool, opts *client.ResizeOptions) (map[string]any, error) {
	body := map[string]any{"offering_code": plan.Plan.ValueString()}
	if sendMainDisk && known(plan.MainDiskExtraGB) {
		body["main_disk_extra_gb"] = plan.MainDiskExtraGB.ValueInt64()
	}
	if known(plan.ExtraDisksGB) && known(state.ExtraDisksGB) {
		planned := int64sOf(ctx, plan.ExtraDisksGB)
		add, remove, err := diskChange(int64sOf(ctx, state.ExtraDisksGB), planned)
		if err != nil {
			return nil, err
		}
		if len(add) > 0 || remove > 0 {
			// The full set: a disk missing from it is removed.
			disks := []map[string]any{}
			for _, size := range planned {
				disks = append(disks, map[string]any{"size_gb": size})
			}
			body["extra_disks"] = disks
		}
	}
	if !plan.Addons.IsUnknown() {
		var addons []map[string]any
		for _, a := range addonChanges(addonsOf(ctx, state.Addons), addonsOf(ctx, plan.Addons)) {
			quantity := a.Quantity.ValueInt64()
			if !known(a.Quantity) {
				// The add-on's minimum, or what it has.
				quantity = 1
				for _, o := range opts.Addons {
					if o.Code == a.Code.ValueString() {
						quantity = max(o.CurrentQuantity, o.MinQuantity, 1)
					}
				}
			}
			addons = append(addons, map[string]any{"code": a.Code.ValueString(), "quantity": quantity})
		}
		if len(addons) > 0 {
			body["addons"] = addons
		}
	}
	for _, ip := range []struct {
		version int64
		planned types.Int64
		current int64
	}{{4, plan.ExtraIPv4, opts.Current.ExtraIPv4}, {6, plan.ExtraIPv6, opts.Current.ExtraIPv6}} {
		if !known(ip.planned) {
			continue
		}
		v := strconv.FormatInt(ip.version, 10)
		switch want := ip.planned.ValueInt64(); {
		case want > ip.current:
			body["ipv"+v+"_add_count"] = want - ip.current
		case want < ip.current:
			ids := releasable(opts, ip.version, ip.current-want)
			if int64(len(ids)) < ip.current-want {
				return nil, fmt.Errorf("the instance has %d extra IPv%s addresses but only %d can be released (not the "+
					"primary one)", ip.current, v, len(ids))
			}
			body["ipv"+v+"_remove_assignment_ids"] = ids
		}
	}
	return body, nil
}

// quoteRefusal says why a quoted resize cannot be applied.
func quoteRefusal(q *client.ResizeQuote) error {
	var why []string
	for _, v := range q.Violations {
		why = append(why, v.Message)
	}
	if q.ShortfallCents != nil {
		sub := "the subscription"
		if q.SubscriptionID != nil {
			sub = "subscription " + *q.SubscriptionID
		}
		why = append(why, fmt.Sprintf("the subscription's balance is short by %s (shortfall_cents: %s): top up %s, then "+
			"apply again", client.FormatCents(*q.ShortfallCents, q.Currency), *q.ShortfallCents, sub))
	}
	if len(why) == 0 {
		why = append(why, "the API cannot apply it now")
	}
	return fmt.Errorf("the API refuses this resize: %s", strings.Join(why, "; "))
}

// quoteSummary is what a resize does, as the plan's warning says it.
func quoteSummary(changed []string, id string, q *client.ResizeQuote) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Changing %s resizes the instance %s in place.", strings.Join(changed, ", "), id)
	if q.RequiresRestart {
		b.WriteString(" It powers off and on again (grow the partition inside the guest after a disk grows).")
	}
	if q.ChargeNowCents != "" && q.ChargeNowCents != "0" {
		fmt.Fprintf(&b, " Charged now: %s.", client.FormatCents(q.ChargeNowCents, q.Currency))
	}
	if q.CreditCents != "" && q.CreditCents != "0" {
		fmt.Fprintf(&b, " Credit back: %s.", client.FormatCents(q.CreditCents, q.Currency))
	}
	for _, rm := range q.Removes {
		fmt.Fprintf(&b, " Removes %s: %s", rm.Label, rm.Warning)
	}
	for _, w := range q.Warnings {
		fmt.Fprintf(&b, " %s", w.Message)
	}
	if q.ShortfallCents != nil {
		fmt.Fprintf(&b, " The subscription's balance is short by %s: top it up before applying.",
			client.FormatCents(*q.ShortfallCents, q.Currency))
	}
	return b.String()
}

// resizeRefusals refuses at plan time the resizes that cannot be: a main
// disk that would shrink on the same plan, a disk that would change size.
// Taking disks off the end is warned about: their data goes with them.
func resizeRefusals(ctx context.Context, state, plan instanceModel, diags *diag.Diagnostics) {
	if known(plan.MainDiskExtraGB) && known(state.MainDiskExtraGB) && known(plan.Plan) &&
		plan.Plan.Equal(state.Plan) && plan.MainDiskExtraGB.ValueInt64() < state.MainDiskExtraGB.ValueInt64() {
		diags.AddAttributeError(path.Root("main_disk_extra_gb"), "The main disk cannot shrink",
			fmt.Sprintf("The instance %s has %d GB beyond its plan's on its main disk, and the configuration says %d: a "+
				"main disk only grows. Keep at least %d (or move to a plan whose disk is that big).", state.ID.ValueString(),
				state.MainDiskExtraGB.ValueInt64(), plan.MainDiskExtraGB.ValueInt64(), state.MainDiskExtraGB.ValueInt64()))
	}
	if known(plan.ExtraDisksGB) && known(state.ExtraDisksGB) {
		_, remove, err := diskChange(int64sOf(ctx, state.ExtraDisksGB), int64sOf(ctx, plan.ExtraDisksGB))
		switch {
		case err != nil:
			diags.AddAttributeError(path.Root("extra_disks_gb"), "A data disk cannot change size", "On the instance "+
				state.ID.ValueString()+", "+err.Error()+".")
		case remove > 0:
			diags.AddAttributeWarning(path.Root("extra_disks_gb"), "Data disks will be removed",
				fmt.Sprintf("The last %d data disk(s) of the instance %s are removed, with ALL the data on them. Back up "+
					"what you need first, or put them back in the list.", remove, state.ID.ValueString()))
		}
	}
}

// quotePlan prices the planned resize at plan time, to say what it does and
// refuse what the API would: best effort, as the apply quotes it again.
func (r *instanceResource) quotePlan(ctx context.Context, state, plan instanceModel, sendMainDisk bool, changed []string, diags *diag.Diagnostics) {
	id := state.ID.ValueString()
	opts, err := r.client.GetResizeOptions(ctx, id)
	if err != nil {
		tflog.Debug(ctx, "Could not read the resize options at plan time", map[string]any{"id": id, "error": err.Error()})
		return
	}
	if !opts.Resizable {
		diags.AddError("The instance cannot be resized", fmt.Sprintf("Changing %s resizes the instance %s, which cannot be "+
			"resized now: %s", strings.Join(changed, ", "), id, notResizable(opts)))
		return
	}
	body, err := resizeBody(ctx, state, plan, sendMainDisk, opts)
	if err != nil {
		diags.AddError("The resize cannot be done", fmt.Sprintf("On the instance %s, %s.", id, err))
		return
	}
	q, err := r.client.QuoteResize(ctx, id, body)
	if err != nil {
		tflog.Debug(ctx, "Could not quote the resize at plan time", map[string]any{"id": id, "error": err.Error()})
		return
	}
	switch {
	case !q.HasChanges:
	case len(q.Violations) > 0:
		var why []string
		for _, v := range q.Violations {
			why = append(why, v.Message)
		}
		diags.AddError("The API would refuse this resize",
			fmt.Sprintf("Resizing the instance %s (%s) is refused: %s.", id, strings.Join(changed, ", "), strings.Join(why, "; ")))
	default:
		diags.AddWarning("The instance will be resized", quoteSummary(changed, id, q))
	}
}

// resize quotes and applies the planned resize, and waits until the
// instance is built again. It says whether anything changed.
func (r *instanceResource) resize(ctx context.Context, id string, state, plan instanceModel, sendMainDisk bool, timeout time.Duration) (bool, error) {
	opts, err := r.client.GetResizeOptions(ctx, id)
	if err != nil {
		return false, err
	}
	if !opts.Resizable {
		return false, fmt.Errorf("it cannot be resized now: %s", notResizable(opts))
	}
	body, err := resizeBody(ctx, state, plan, sendMainDisk, opts)
	if err != nil {
		return false, err
	}
	q, err := r.client.QuoteResize(ctx, id, body)
	if err != nil {
		return false, err
	}
	if !q.HasChanges {
		return false, nil
	}
	if !q.CanApply {
		return false, quoteRefusal(q)
	}
	var accepted *client.ResizeAccepted
	err = whileBusy(ctx, timeout, func() error {
		a, err := r.client.ResizeInstance(ctx, id, body)
		accepted = a
		return err
	})
	if err != nil {
		return false, err
	}
	if accepted.OperationID != nil && *accepted.OperationID != "" {
		if err := waitForOperation(ctx, r.client, *accepted.OperationID, timeout); err != nil {
			return true, err
		}
	}
	return true, r.waitForReady(ctx, id, "", timeout)
}

// notResizable says why an instance cannot be resized now.
func notResizable(opts *client.ResizeOptions) string {
	if opts.Reason != nil && strings.TrimSpace(*opts.Reason) != "" {
		return strings.TrimSpace(*opts.Reason)
	}
	return "the API does not say why"
}

// resizeError is the diagnostic for a resize that failed: a balance that
// does not cover it says what is short.
func resizeError(diags *diag.Diagnostics, id string, err error) {
	if p, ok := client.AsProblem(err); ok && p.Status == 402 && p.ShortfallCents != nil {
		sub := deref(p.SubscriptionID)
		diags.AddError("The balance does not cover the resize",
			fmt.Sprintf("Resizing the instance %s needs %s more on subscription %s (shortfall_cents: %s). Top it up (in "+
				"the console, or POST /v1/subscriptions/%s/top-ups), then apply again. Nothing was changed. %s", id,
				client.FormatCents(*p.ShortfallCents, p.Currency), sub, *p.ShortfallCents, sub, err))
		return
	}
	diags.AddError("Could not resize the instance", fmt.Sprintf("The instance %s: %s", id, err))
}

// extrasFromOptions is the state's main disk, extra disks, add-ons and
// addresses from what the instance has now. An instance whose configuration
// the platform never recorded (not resizable) shows nothing reliable: the
// state keeps what it had.
func extrasFromOptions(ctx context.Context, m *instanceModel, opts *client.ResizeOptions) {
	if !opts.Resizable {
		return
	}
	for _, o := range opts.Offerings {
		if o.Current || o.Code == opts.Current.OfferingCode {
			m.MainDiskExtraGB = types.Int64Value(max(0, opts.Current.MainDiskGB-o.MainDiskBaseGB))
			break
		}
	}
	m.ExtraIPv4 = types.Int64Value(opts.Current.ExtraIPv4)
	m.ExtraIPv6 = types.Int64Value(opts.Current.ExtraIPv6)

	// The API shows how many data disks there are, not their sizes: the
	// state keeps the sizes it knows, 0 for one added elsewhere.
	disks := int64sOf(ctx, m.ExtraDisksGB)
	switch n := len(opts.ExtraDisks); {
	case n < len(disks):
		disks = disks[:n]
	case n > len(disks):
		for len(disks) < n {
			disks = append(disks, 0)
		}
	}
	m.ExtraDisksGB = int64List(disks)

	// The add-ons the configuration lists, as the instance has them.
	if managed := addonsOf(ctx, m.Addons); managed != nil {
		have := map[string]int64{}
		for _, o := range opts.Addons {
			have[o.Code] = o.CurrentQuantity
		}
		var out []attr.Value
		for _, a := range managed {
			q, ok := have[a.Code.ValueString()]
			if !ok || q == 0 {
				continue // removed elsewhere: the plan puts it back
			}
			// A quantity the configuration leaves to the add-on's minimum
			// stays unsaid.
			quantity := a.Quantity
			if known(a.Quantity) {
				quantity = types.Int64Value(q)
			}
			obj, _ := types.ObjectValue(instanceAddonType.AttrTypes, map[string]attr.Value{"code": a.Code, "quantity": quantity})
			out = append(out, obj)
		}
		m.Addons = types.SetValueMust(instanceAddonType, out)
	}
}

// settleExtras gives the extras a value when the API could not be asked:
// what the plan says, or nothing.
func settleExtras(m *instanceModel) {
	for _, v := range []*types.Int64{&m.MainDiskExtraGB, &m.ExtraIPv4, &m.ExtraIPv6} {
		if v.IsUnknown() {
			*v = types.Int64Value(0)
		}
	}
	if m.ExtraDisksGB.IsUnknown() {
		m.ExtraDisksGB = int64List(nil)
	}
}
