package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/masterdaweb/terraform-provider-masterdaweb/internal/client"
)

// waitForOperation follows an operation (GET /v1/operations/{id}) until it
// completes; a failed or cancelled one is an error saying why.
func waitForOperation(ctx context.Context, c *client.Client, id string, timeout time.Duration) error {
	return waitUntil(ctx, timeout, func() (bool, error) {
		op, err := c.GetOperation(ctx, id)
		if err != nil {
			return false, err
		}
		switch op.Status {
		case "completed":
			return true, nil
		case "failed", "cancelled":
			why := ""
			if op.Error != nil && strings.TrimSpace(*op.Error) != "" {
				why = ": " + strings.TrimSpace(*op.Error)
			}
			return false, fmt.Errorf("the %s operation %s %s%s", op.Type, op.ID, op.Status, why)
		}
		tflog.Debug(ctx, "Waiting for an operation", map[string]any{"id": op.ID, "type": op.Type, "status": op.Status})
		return false, nil
	})
}

// waitForAccepted follows what an asynchronous endpoint started: its
// operation when the API names one, else until done() says so.
func waitForAccepted(ctx context.Context, c *client.Client, accepted *client.OperationAccepted, timeout time.Duration, done func() (bool, error)) error {
	if accepted != nil && accepted.OperationID != nil && *accepted.OperationID != "" {
		if err := waitForOperation(ctx, c, *accepted.OperationID, timeout); err != nil {
			return err
		}
	}
	if done == nil {
		return nil
	}
	return waitUntil(ctx, timeout, done)
}

// busyReason is the API's reason for refusing a write while another
// operation holds the instance.
const busyReason = "VM_OPERATION_IN_PROGRESS"

// whileBusy does an instance write again while another operation (a backup,
// a snapshot being taken, a deploy) holds the instance: the API refuses it
// then with a 409 whose reason says so (an API from before reasons says
// "busy"). Other refusals stand — an instance in rescue mode among them.
func whileBusy(ctx context.Context, timeout time.Duration, write func() error) error {
	return waitUntil(ctx, timeout, func() (bool, error) {
		err := write()
		if p, ok := client.AsProblem(err); ok && p.Status == 409 &&
			(p.Reason == busyReason || (p.Reason == "" && strings.Contains(strings.ToLower(p.Detail), "busy"))) {
			tflog.Debug(ctx, "The instance is busy: asking again", map[string]any{"detail": p.Detail})
			return false, nil
		}
		return err == nil, err
	})
}

// timeoutsAttribute is the timeouts attribute, described.
func timeoutsAttribute(ctx context.Context, opts timeouts.Opts) schema.Attribute {
	a := timeouts.Attributes(ctx, opts).(schema.SingleNestedAttribute)
	a.Description = "How long Terraform waits, per operation, as a duration like \"30m\" or \"1h\"."
	return a
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// stringValueOrNull is a text the API may leave out, as null when empty.
func stringValueOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// emptySet is an empty set of strings.
func emptySet() types.Set { return types.SetValueMust(types.StringType, []attr.Value{}) }

// Short names for what a read-back helper writes into.
type (
	tfsdkState  = tfsdk.State
	diagnostics = diag.Diagnostics
)
