package engine

import modelcaps "github.com/abietic/yhc/engine/model"

// ContextUsageSnapshot keeps model capacity separate from the latest provider
// input usage. A zero window is unknown; InputKnown distinguishes unknown usage
// from a provider-reported zero. Neither field includes an unsent draft.
type ContextUsageSnapshot struct {
	WindowTokens int
	InputTokens  int
	InputKnown   bool
}

// GetContextUsageSnapshot reads the main-loop binding and usage atomically. It
// performs no route resolution, transcript reads or model calls. Bound metadata
// is authoritative, including when its window is unavailable; only an unbound
// legacy model may use the known capability table. This is capacity, not the
// auto-compaction threshold or remaining input admission budget.
func (e *QueryEngine) GetContextUsageSnapshot() ContextUsageSnapshot {
	if e == nil {
		return ContextUsageSnapshot{}
	}
	e.mu.Lock()
	model := e.config.Model
	binding := e.modelBinding.Clone()
	usage := e.usageSummary
	e.mu.Unlock()
	if model == "" {
		return ContextUsageSnapshot{}
	}
	var result ContextUsageSnapshot
	if usage.CurrentContextUsageKnown && usage.CurrentContextPromptTokens >= 0 {
		result.InputTokens = usage.CurrentContextPromptTokens
		result.InputKnown = true
	}
	if binding != nil {
		if binding.ValidateV1() == nil && binding.ContextWindowTokens != nil && *binding.ContextWindowTokens > 0 {
			result.WindowTokens = *binding.ContextWindowTokens
		}
	} else if window, known := modelcaps.KnownContextWindow(model); known && window > 0 {
		result.WindowTokens = window
	}
	return result
}
