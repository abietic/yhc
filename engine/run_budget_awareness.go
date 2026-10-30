package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/schema"
)

// prependRunBudgetPolicy projects invocation limits before conversation history.
// Settled usage and a relative countdown would change this prefix every round;
// keep those in telemetry and let admission enforce the shared remaining budget.
// This projection never becomes canonical history or durable budget authority.
func prependRunBudgetPolicy(ctx context.Context, messages []*schema.Message, usage *execution.RunUsage) []*schema.Message {
	if usage == nil {
		return messages
	}
	deadline, hasDeadline := ctx.Deadline()
	if !usage.LimitsEnabled() && !hasDeadline {
		return messages
	}
	snapshot := usage.Snapshot()
	var b strings.Builder
	b.WriteString("<run-budget>\nRuntime-enforced limits for this invocation, shared with children and auxiliary calls. These are total limits, not remaining allowances; earlier calls may have consumed them.\n")
	if limit := snapshot.Limits.MaxProviderCalls; limit > 0 {
		fmt.Fprintf(&b, "provider_call_limit=%d\n", limit)
	}
	if limit := snapshot.Limits.MaxTotalTokens; limit > 0 {
		fmt.Fprintf(&b, "token_threshold=%d\n", limit)
		b.WriteString("The token threshold uses settled usage; in-flight responses can exceed it.\n")
	}
	if hasDeadline {
		fmt.Fprintf(&b, "deadline_utc=%s (enforced deadline)\n", deadline.UTC().Format(time.RFC3339Nano))
	}
	b.WriteString("Plan targeted verification and a final response within the remaining budget. Near the limit, avoid broad new exploration or delegation. Report unfinished work and unverified claims explicitly; budget pressure is not evidence of success.\n</run-budget>")
	result := make([]*schema.Message, 0, len(messages)+1)
	result = append(result, &schema.Message{Role: schema.User, Content: b.String(), Extra: map[string]any{"is_meta": true, "attachment_kind": "run_budget"}})
	return append(result, messages...)
}
