package provider

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/net/idna"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// clientFrom is the API client the provider configured, for a resource or
// data source's Configure.
func clientFrom(data any, diags *diag.Diagnostics) *client.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data",
			fmt.Sprintf("Expected the API client, got %T. This is a bug in the provider: please report it.", data))
		return nil
	}
	return c
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

// deployRefusals are the reasons the API refuses a deployment after saving
// the change that asked for it: the change is made, the deployment is not.
var deployRefusals = []string{
	"SERVERLESS_DEPLOYS_FROZEN",
	"SERVERLESS_PROJECT_PAUSED",
	"SERVERLESS_SERVICE_SUSPENDED",
}

func isDeployRefusal(err error) bool { return client.HasReason(err, deployRefusals...) }

// deployRefusedWarning says a change was saved but its deployment refused.
func deployRefusedWarning(diags *diag.Diagnostics, what string, err error) {
	diags.AddWarning("Saved, but not deployed",
		what+" was saved, but the deployment that applies it was refused: "+err.Error()+
			" It reaches the service with its next deployment.")
}

// pollInterval is how often a wait asks the API again. Tests shorten it.
var pollInterval = 5 * time.Second

// waitUntil asks done() every pollInterval until it says so, it fails, or
// the timeout passes — its own, or the context's, which is the same timeout
// (a resource's timeouts) seen from inside a request.
func waitUntil(ctx context.Context, timeout time.Duration, done func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	expired := fmt.Errorf("still not done after %s", timeout)
	for {
		ok, err := done()
		if errors.Is(err, context.DeadlineExceeded) {
			return expired
		}
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return expired
		}
		t := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return expired
			}
			return ctx.Err()
		case <-t.C:
		}
	}
}

// deleteTimeout is how long a destroy waits for a project or a service to be
// gone.
var deleteTimeout = 10 * time.Minute

// ── Value conversions ───────────────────────────────────────────────────────

func stringOrNull(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

// stringOrEmpty is an optional text the API keeps as null, as "" in state:
// "" is how a configuration says "none".
func stringOrEmpty(p *string) types.String {
	if p == nil {
		return types.StringValue("")
	}
	return types.StringValue(*p)
}

func int64OrNull(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

func stringList(items []string) types.List {
	vals := make([]attr.Value, 0, len(items))
	for _, s := range items {
		vals = append(vals, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, vals)
}

func stringSet(items []string) types.Set {
	vals := make([]attr.Value, 0, len(items))
	for _, s := range items {
		vals = append(vals, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, vals)
}

// listStrings is a list or set's known strings (nil when null or unknown).
func listStrings(ctx context.Context, v interface {
	IsNull() bool
	IsUnknown() bool
	ElementsAs(context.Context, any, bool) diag.Diagnostics
}) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	_ = v.ElementsAs(ctx, &out, false)
	return out
}

// known says whether a planned value was given (not null, not unknown).
func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

// keepEquivalent keeps the configured text when the API's answer means the
// same (the API trims, or keeps "" as null), so a cosmetic difference never
// shows up as a change.
func keepEquivalent(prior types.String, api *string) types.String {
	got := ""
	if api != nil {
		got = *api
	}
	if known(prior) && strings.TrimSpace(prior.ValueString()) == got {
		return prior
	}
	if api == nil {
		return types.StringNull()
	}
	return types.StringValue(got)
}

// ── Normalisation (as the API does it) ─────────────────────────────────────

// normalizeHostname is a hostname as the API keeps it: lowercase, without a
// trailing dot, an international name in its xn-- form.
func normalizeHostname(s string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if ascii, err := idna.Lookup.ToASCII(h); err == nil {
		return ascii
	}
	return h
}

// normalizeCIDR is an allowed source as the API keeps it: an address as a /32
// or /128, a network by its network address, IPv6 in its RFC 5952 form.
func normalizeCIDR(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return s
		}
		return p.Masked().String()
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return s
	}
	return netip.PrefixFrom(a, a.BitLen()).String()
}

// sameStringSets says whether two lists hold the same strings once
// normalised, in any order.
func sameStringSets(a, b []string, norm func(string) string) bool {
	set := func(items []string) []string {
		seen := map[string]bool{}
		var out []string
		for _, s := range items {
			if norm != nil {
				s = norm(s)
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		sort.Strings(out)
		return out
	}
	x, y := set(a), set(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
