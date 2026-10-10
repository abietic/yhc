package execution

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// MaxRunUsageRecords bounds retained admission records independently of totals.
// Once full, new calls still settle normally and increment DroppedRecords.
const MaxRunUsageRecords = 1024

// RunUsageCallTokens is known cumulative response usage. Cached input and
// reasoning output are subsets; UncachedPromptTokens is input minus cache.
type RunUsageCallTokens struct {
	PromptTokens         uint64 `json:"prompt_tokens"`
	CompletionTokens     uint64 `json:"completion_tokens"`
	TotalTokens          uint64 `json:"total_tokens"`
	CachedPromptTokens   uint64 `json:"cached_prompt_tokens"`
	UncachedPromptTokens uint64 `json:"uncached_prompt_tokens"`
	ReasoningTokens      uint64 `json:"reasoning_tokens"`
}

// RunUsageRecord contains only bounded routing labels, opaque UUIDs and numbers.
// State is in_flight, known, unknown, or released; nil Tokens never means zero.
type RunUsageRecord struct {
	Ordinal                uint64              `json:"ordinal"`
	CallID                 string              `json:"call_id"`
	LogicalRoundID         string              `json:"logical_round_id"`
	LogicalRequestID       string              `json:"logical_request_id"`
	ModelAttemptID         string              `json:"model_attempt_id"`
	AttemptIndex           int                 `json:"attempt_index"`
	RetryIndex             int                 `json:"retry_index"`
	Provider               string              `json:"provider"`
	RequestedModel         string              `json:"requested_model"`
	ResolvedModel          string              `json:"resolved_model"`
	Source                 string              `json:"source"`
	Role                   string              `json:"role"`
	Effort                 string              `json:"effort"`
	State                  string              `json:"state"`
	StartedOffsetMillis    int64               `json:"started_offset_ms"`
	ProviderDurationMillis uint64              `json:"provider_duration_ms"`
	Tokens                 *RunUsageCallTokens `json:"tokens"`
}

// RunUsageLedger is one process invocation's bounded incremental history. It is
// not persisted into the durable Goal ledger or reconstructed from transcripts.
type RunUsageLedger struct {
	Version        int              `json:"version"`
	SegmentID      string           `json:"segment_id"`
	Records        []RunUsageRecord `json:"records"`
	DroppedRecords uint64           `json:"dropped_records"`
}

func (r *RunUsage) appendCallRecord(d ProviderUsageDescriptor, call *runUsageCall) {
	call.recordIndex = -1
	if len(r.records) == MaxRunUsageRecords {
		r.droppedRecords++
		return
	}
	call.recordIndex = len(r.records)
	r.records = append(r.records, RunUsageRecord{
		Ordinal: r.totals.ProviderCalls + r.totals.ReleasedCalls, CallID: call.id,
		LogicalRoundID: runUsageUUID(d.LogicalRoundID), LogicalRequestID: runUsageUUID(d.LogicalRequestID), ModelAttemptID: runUsageUUID(d.ModelAttemptID),
		AttemptIndex: max(0, d.ModelAttemptIndex), RetryIndex: max(0, d.ModelRetryIndex),
		Provider: runUsageProvider(d.Provider), RequestedModel: runUsageModelLabel(d.Model), ResolvedModel: "unknown",
		Source: runUsageSource(d.QuerySource), Role: runUsageRole(d.ModelRole), Effort: runUsageEffort(d.ReasoningEffort),
		State: "in_flight", StartedOffsetMillis: call.started.Sub(r.started).Milliseconds(),
	})
}

func runUsageUUID(value string) string {
	if len(value) == 36 {
		if id, err := uuid.Parse(value); err == nil && id != uuid.Nil {
			return id.String()
		}
	}
	return "unknown"
}

func runUsageModelLabel(value string) string {
	// Configuration/response model identifiers only, never arbitrary content,
	// endpoints, profile bodies, or response IDs. Reject rather than truncate.
	if len(value) == 0 || len(value) > 128 {
		return "unknown"
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:/[]", c)) {
			return "unknown"
		}
	}
	if strings.Contains(value, "://") {
		return "unknown"
	}
	return value
}

func runUsageProvider(value string) string {
	switch value {
	case "agenticdeepseek", "agenticglm", "agenticclaude", "agenticgemini", "agenticopenai", "agenticark", "agenticqwen":
		return value
	}
	return "unknown"
}

func runUsageSource(value string) string {
	switch value {
	case "repl_main_thread", "sdk", "agent", "compact", "independent_verification", "independent_verification_coverage", "prompt_suggestion_generation", "tool_use_summary_generation", "yolo_classifier", "permission_explainer", "approval_review", "long_session_background":
		return value
	}
	return "other"
}

func runUsageRole(value string) string {
	switch value {
	case "main", "explore", "plan", "general", "summary":
		return value
	}
	return "other"
}

func runUsageEffort(value string) string {
	switch value {
	case "none", "low", "medium", "high", "max", "xhigh":
		return value
	}
	return "unknown"
}

// ProviderResponseModelObserver is optional. Existing durable owners and their
// settlement interfaces remain unchanged; combined owners forward observations.
type (
	ProviderResponseModelObserver    interface{ ObserveProviderResponseModel(string) }
	providerUsageResponseObserverKey struct{}
)

func withProviderUsageResponseObserver(ctx context.Context, call ProviderUsageCall) context.Context {
	if observer, ok := call.(ProviderResponseModelObserver); ok {
		return context.WithValue(ctx, providerUsageResponseObserverKey{}, observer)
	}
	return ctx
}

// ObserveProviderResponseModel accepts only model identity from typed response
// metadata. Producers must not derive it from requested model or text output.
func ObserveProviderResponseModel(ctx context.Context, model string) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(providerUsageResponseObserverKey{}).(ProviderResponseModelObserver); ok {
		observer.ObserveProviderResponseModel(model)
	}
}

func (c *runUsageCall) ObserveProviderResponseModel(model string) {
	r := c.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.settled || c.recordIndex < 0 || model == "" {
		return
	}
	label := runUsageModelLabel(model)
	if c.responseModelSeen && r.records[c.recordIndex].ResolvedModel != label {
		c.responseModelConflict = true
	}
	c.responseModelSeen = true
	if c.responseModelConflict {
		label = "unknown"
	}
	r.records[c.recordIndex].ResolvedModel = label
}
