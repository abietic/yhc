package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/schema"
)

// appendRunBudgetReminder projects live invocation facts at the request tail.
// It never mutates canonical history or the stable system/cache prefix. Admission
// remains authoritative: other children can consume budget after this snapshot.
func appendRunBudgetReminder(ctx context.Context, messages []*schema.Message, usage *execution.RunUsage) []*schema.Message {
	if usage == nil {
		return messages
	}
	deadline, hasDeadline := ctx.Deadline()
	if !usage.LimitsEnabled() && !hasDeadline {
		return messages
	}
	snapshot := usage.Snapshot()
	var b strings.Builder
	b.WriteString("<run-budget>\nRuntime snapshot before this request; shared with children and auxiliary calls. This request also consumes budget.\n")
	if limit := snapshot.Limits.MaxProviderCalls; limit > 0 {
		remaining := uint64(limit) - min(uint64(limit), snapshot.ProviderCalls)
		fmt.Fprintf(&b, "provider_calls_remaining=%d; provider_call_limit=%d\n", remaining, limit)
	}
	if limit := snapshot.Limits.MaxTotalTokens; limit > 0 {
		remaining := uint64(limit) - min(uint64(limit), snapshot.TotalTokens)
		fmt.Fprintf(&b, "known_tokens=%d; token_threshold_remaining=%d; token_threshold=%d; unknown_calls=%d; in_flight=%d\n", snapshot.TotalTokens, remaining, limit, snapshot.UnknownCalls, snapshot.InFlight)
		b.WriteString("Token counts are settled usage only; in-flight responses can exceed this threshold.\n")
	}
	if hasDeadline {
		fmt.Fprintf(&b, "time_remaining_seconds=%d (enforced deadline)\n", max(int64(0), int64(time.Until(deadline)/time.Second)))
	}
	b.WriteString("Plan targeted verification and a final response within the remaining budget. Near the limit, avoid broad new exploration or delegation. Report unfinished work and unverified claims explicitly; budget pressure is not evidence of success.\n</run-budget>")
	result := make([]*schema.Message, 0, len(messages)+1)
	result = append(result, messages...)
	return append(result, &schema.Message{Role: schema.User, Content: b.String(), Extra: map[string]any{"is_meta": true, "attachment_kind": "run_budget"}})
}
