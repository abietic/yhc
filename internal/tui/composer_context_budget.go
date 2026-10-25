package tui

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/compact"
	"github.com/cloudwego/eino/schema"
)

type composerContextTokenEstimate struct {
	tokens int
	status string
	image  bool
}

type composerContextTokenCache struct {
	revision    uint64
	initialized bool
	items       map[string]composerContextTokenEstimate
	total       int
	partial     bool
	images      bool
}

// Cache only numbers/status, never a second copy of attachment payloads. Query,
// selection, resize and repaint reuse this snapshot; load/remove/undo invalidate
// it through the existing draft revision, and opening another draft resets it.
func (a *App) composerContextTokenEstimates(items []threadComposerElement) *composerContextTokenCache {
	cache := &a.contextManager.tokens
	if cache.initialized && cache.revision == a.composerRevision {
		return cache
	}
	*cache = composerContextTokenCache{
		revision: a.composerRevision, initialized: true,
		items: make(map[string]composerContextTokenEstimate, len(items)),
	}
	for _, item := range items {
		estimate := a.estimateComposerContextTokens(item)
		cache.items[item.ID] = estimate
		cache.total += estimate.tokens
		cache.partial = cache.partial || estimate.status != ""
		cache.images = cache.images || estimate.image
	}
	return cache
}

func (a *App) estimateComposerContextTokens(item threadComposerElement) composerContextTokenEstimate {
	message := &schema.Message{Role: schema.User}
	baseline := &schema.Message{Role: schema.User}
	estimate := composerContextTokenEstimate{}
	switch item.Kind {
	case composerElementKindPaste:
		if item.Value == "" {
			return composerContextTokenEstimate{status: "unavailable"}
		}
		// Keep leading/trailing paste whitespace: it becomes internal when
		// inserted into a draft. Aligned non-space guards prevent the shared
		// byte/4 heuristic's TrimSpace from dropping it; subtract both guards.
		const guard = "xxxx"
		message.Content = guard + item.Value + guard
		baseline.Content = guard + guard
	case composerElementKindImage:
		image := a.draftMedia[item.ID]
		if image == nil || len(image.Data) == 0 || image.MIMEType == "" || item.MIMEType != "" && item.MIMEType != image.MIMEType {
			return composerContextTokenEstimate{status: "unavailable"}
		}
		// The existing engine heuristic assigns a generic weight to a media
		// part. Do not encode or tokenize base64, or infer provider image cost.
		message.UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL}}
		estimate.image = true
	case composerElementKindFile, composerElementKindSkill, composerElementKindMCPResource, composerElementKindTerminalOutput:
		if item.Data == "" {
			return composerContextTokenEstimate{status: "pending"}
		}
		envelope, err := encodeComposerContext(item)
		if err != nil {
			return composerContextTokenEstimate{status: "unavailable"}
		}
		message.Content = item.Label + "\n\n" + envelope
	default:
		return composerContextTokenEstimate{status: "unavailable"}
	}
	// Attribute only attachment content: exclude shared message/transport
	// overhead, ordinary draft text, history, system instructions and tools.
	estimate.tokens = compact.EstimateTokenCount([]*schema.Message{message}) -
		compact.EstimateTokenCount([]*schema.Message{baseline})
	return estimate
}

func (e composerContextTokenEstimate) label() string {
	if e.status != "" {
		return e.status
	}
	label := fmt.Sprintf("~%dt", e.tokens)
	if e.image {
		label += "*"
	}
	return label
}

func composerContextWindowShare(tokens, window int) string {
	share := float64(tokens) * 100 / float64(window)
	if tokens > 0 && share < 0.1 {
		return "<0.1%"
	}
	return fmt.Sprintf("%.1f%%", share)
}

func (a *App) composerContextBudgetSnapshot() engine.ContextUsageSnapshot {
	if a.engine == nil || !a.isLeaderThreadView() {
		return engine.ContextUsageSnapshot{}
	}
	return a.engine.GetContextUsageSnapshot()
}

func composerContextBudgetLines(cache *composerContextTokenCache, budget engine.ContextUsageSnapshot) []string {
	total := fmt.Sprintf("Attachments: ~%d tokens", cache.total)
	if cache.partial {
		total += " (partial)"
	}
	if cache.images {
		total += "*"
	}
	window := "Window: unavailable"
	if budget.WindowTokens > 0 {
		shareLabel := "attached"
		if cache.partial {
			shareLabel = "known"
		}
		window = fmt.Sprintf("Window: %s | %s %s", strings.TrimSuffix(humanTokens(budget.WindowTokens), " tokens"), composerContextWindowShare(cache.total, budget.WindowTokens), shareLabel)
	}
	last := "Last input: unavailable"
	if budget.InputKnown {
		last = "Last input: " + humanTokens(budget.InputTokens)
	}
	lines := []string{total, window, last}
	if cache.images {
		lines = append(lines, "* Generic image estimate")
	}
	return lines
}

// Warn only about attachment estimates against the known maximum window.
// Last input can already contain some of these attachments and is not a
// remaining-budget measurement, so it must not be added to this total.
func composerContextBudgetWarning(cache *composerContextTokenCache, budget engine.ContextUsageSnapshot) (string, bool) {
	window := budget.WindowTokens
	if window <= 0 || cache.total < window-window/5 { // ceil(80%); no rounding/overflow
		return "", false
	}
	subject := "attached est."
	if cache.partial {
		subject = "partial est."
	}
	if cache.total >= window {
		return "Alert: " + subject + " >=100% window", true
	}
	return "Warn: " + subject + " >=80% window", false
}

func composerContextTokenDetail(estimate composerContextTokenEstimate, budget engine.ContextUsageSnapshot) string {
	if estimate.status != "" {
		return "Tokens: " + estimate.status
	}
	if estimate.image {
		return fmt.Sprintf("~%d tokens* (generic image)", estimate.tokens)
	}
	label := fmt.Sprintf("~%d tokens", estimate.tokens)
	if budget.WindowTokens > 0 {
		label += " | " + composerContextWindowShare(estimate.tokens, budget.WindowTokens) + " of window"
	}
	return label
}
