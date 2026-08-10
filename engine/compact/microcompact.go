package compact

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MicroCompactResult holds the outcome of a fine-grained micro-compaction pass.
type MicroCompactResult struct {
	Messages    []*schema.Message
	TokensFreed int
	Applied     bool
}

// MicroCompact applies targeted trimming to individual messages to reduce
// context size without full compaction. It targets:
// - Long tool results (truncate to first/last N chars with "..." separator)
// - Repeated whitespace in content
// - Large base64 image data (replace with placeholder)
// - Very long assistant reasoning (trim middle)
//
// Strategies are applied in order until targetTokensToFree is reached.
// The input slice is never mutated; a new slice is returned.
func MicroCompact(messages []*schema.Message, targetTokensToFree int) *MicroCompactResult {
	if len(messages) == 0 || targetTokensToFree <= 0 {
		return &MicroCompactResult{
			Messages:    messages,
			TokensFreed: 0,
			Applied:     false,
		}
	}

	totalFreed := 0
	current := cloneMessages(messages)

	// Strategy 1: Trim long tool results
	if totalFreed < targetTokensToFree {
		var freed int
		current, freed = TrimLongToolResults(current, 4000)
		totalFreed += freed
	}

	// Strategy 2: Strip base64 images
	if totalFreed < targetTokensToFree {
		var freed int
		current, freed = StripBase64Images(current)
		totalFreed += freed
	}

	// Strategy 3: Trim long thinking/reasoning content
	if totalFreed < targetTokensToFree {
		var freed int
		current, freed = TrimLongThinking(current, 4000)
		totalFreed += freed
	}

	// Strategy 4: Compress whitespace
	if totalFreed < targetTokensToFree {
		var freed int
		current, freed = CompressWhitespace(current)
		totalFreed += freed
	}

	return &MicroCompactResult{
		Messages:    current,
		TokensFreed: totalFreed,
		Applied:     totalFreed > 0,
	}
}

// TrimLongToolResults truncates tool results longer than maxLen characters.
// It keeps the first 1500 chars + "\n...[truncated]...\n" + last 1500 chars.
// Only Tool role messages are affected. Returns the modified messages and
// an estimate of tokens freed.
func TrimLongToolResults(messages []*schema.Message, maxLen int) ([]*schema.Message, int) {
	if maxLen <= 0 {
		maxLen = 4000
	}

	const keepHead = 1500
	const keepTail = 1500
	const separator = "\n...[truncated]...\n"

	totalFreed := 0
	result := make([]*schema.Message, len(messages))

	for i, msg := range messages {
		if msg == nil || msg.Role != schema.Tool {
			result[i] = msg
			continue
		}

		content := msg.Content
		if len(content) <= maxLen {
			result[i] = msg
			continue
		}

		// Truncate long tool result
		originalLen := len(content)
		head := keepHead
		tail := keepTail
		if head+tail >= originalLen {
			result[i] = msg
			continue
		}

		truncated := content[:head] + separator + content[originalLen-tail:]
		freed := roughTextTokens(content) - roughTextTokens(truncated)
		if freed < 0 {
			freed = 0
		}
		totalFreed += freed

		clone := *msg
		clone.Content = truncated
		if msg.Extra != nil {
			clone.Extra = make(map[string]any, len(msg.Extra))
			for k, v := range msg.Extra {
				clone.Extra[k] = v
			}
		}
		result[i] = &clone
	}

	return result, totalFreed
}

// base64Pattern matches inline base64 image data URIs.
var base64Pattern = regexp.MustCompile(`data:image/[^;]+;base64,[A-Za-z0-9+/=]{100,}`)

// StripBase64Images removes inline base64 image data from all messages,
// replacing matches with a placeholder. Returns the modified messages and
// an estimate of tokens freed.
func StripBase64Images(messages []*schema.Message) ([]*schema.Message, int) {
	const placeholder = "[image content removed for context management]"

	totalFreed := 0
	result := make([]*schema.Message, len(messages))

	for i, msg := range messages {
		if msg == nil {
			result[i] = msg
			continue
		}

		content := msg.Content
		if content == "" || !strings.Contains(content, "data:image/") {
			result[i] = msg
			continue
		}

		replaced := base64Pattern.ReplaceAllString(content, placeholder)
		if replaced == content {
			result[i] = msg
			continue
		}

		freed := roughTextTokens(content) - roughTextTokens(replaced)
		if freed < 0 {
			freed = 0
		}
		totalFreed += freed

		clone := *msg
		clone.Content = replaced
		if msg.Extra != nil {
			clone.Extra = make(map[string]any, len(msg.Extra))
			for k, v := range msg.Extra {
				clone.Extra[k] = v
			}
		}
		result[i] = &clone
	}

	return result, totalFreed
}

// TrimLongThinking trims very long ReasoningContent on assistant messages.
// If ReasoningContent exceeds maxLen, the middle is replaced with a separator
// keeping the first and last portions. Returns the modified messages and
// an estimate of tokens freed.
func TrimLongThinking(messages []*schema.Message, maxLen int) ([]*schema.Message, int) {
	if maxLen <= 0 {
		maxLen = 4000
	}

	const separator = "\n...[reasoning truncated]...\n"

	totalFreed := 0
	result := make([]*schema.Message, len(messages))

	for i, msg := range messages {
		if msg == nil || msg.Role != schema.Assistant {
			result[i] = msg
			continue
		}

		reasoning := msg.ReasoningContent
		if len(reasoning) <= maxLen {
			result[i] = msg
			continue
		}

		// Keep first 40% and last 40% of maxLen
		keepHead := maxLen * 2 / 5
		keepTail := maxLen * 2 / 5
		if keepHead+keepTail >= len(reasoning) {
			result[i] = msg
			continue
		}

		truncated := reasoning[:keepHead] + separator + reasoning[len(reasoning)-keepTail:]
		freed := roughTextTokens(reasoning) - roughTextTokens(truncated)
		if freed < 0 {
			freed = 0
		}
		totalFreed += freed

		clone := *msg
		clone.ReasoningContent = truncated
		if msg.Extra != nil {
			clone.Extra = make(map[string]any, len(msg.Extra))
			for k, v := range msg.Extra {
				clone.Extra[k] = v
			}
		}
		if len(msg.ToolCalls) > 0 {
			clone.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
		}
		result[i] = &clone
	}

	return result, totalFreed
}

// multiNewlinePattern matches runs of 3+ newlines.
var multiNewlinePattern = regexp.MustCompile(`\n{3,}`)

// multiSpacePattern matches runs of 3+ spaces (not newlines).
var multiSpacePattern = regexp.MustCompile(`[^\S\n]{3,}`)

// CompressWhitespace normalizes excessive whitespace in all messages.
// It collapses runs of 3+ newlines to 2 newlines, and runs of 3+ spaces
// to a single space. Returns the modified messages and an estimate of
// tokens freed.
func CompressWhitespace(messages []*schema.Message) ([]*schema.Message, int) {
	totalFreed := 0
	result := make([]*schema.Message, len(messages))

	for i, msg := range messages {
		if msg == nil {
			result[i] = msg
			continue
		}

		contentChanged := false
		newContent := msg.Content
		reasoningChanged := false
		newReasoning := msg.ReasoningContent

		if newContent != "" {
			compressed := compressWS(newContent)
			if compressed != newContent {
				contentChanged = true
				newContent = compressed
			}
		}

		if newReasoning != "" {
			compressed := compressWS(newReasoning)
			if compressed != newReasoning {
				reasoningChanged = true
				newReasoning = compressed
			}
		}

		if !contentChanged && !reasoningChanged {
			result[i] = msg
			continue
		}

		originalTokens := roughTextTokens(msg.Content) + roughTextTokens(msg.ReasoningContent)
		newTokens := roughTextTokens(newContent) + roughTextTokens(newReasoning)
		freed := originalTokens - newTokens
		if freed < 0 {
			freed = 0
		}
		totalFreed += freed

		clone := *msg
		if contentChanged {
			clone.Content = newContent
		}
		if reasoningChanged {
			clone.ReasoningContent = newReasoning
		}
		if msg.Extra != nil {
			clone.Extra = make(map[string]any, len(msg.Extra))
			for k, v := range msg.Extra {
				clone.Extra[k] = v
			}
		}
		if len(msg.ToolCalls) > 0 {
			clone.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
		}
		result[i] = &clone
	}

	return result, totalFreed
}

// compressWS collapses runs of 3+ newlines to 2, and runs of 3+ spaces to 1.
func compressWS(s string) string {
	s = multiNewlinePattern.ReplaceAllString(s, "\n\n")
	s = multiSpacePattern.ReplaceAllString(s, " ")
	return s
}

// Snip performs the lightweight pre-compact pass that trims obvious
// bloat without touching message structure. Called before AutoCompact
// to potentially avoid full compaction.
//
// It applies all micro-compact strategies with a generous target,
// aiming to free as many tokens as possible from low-value content.
func Snip(messages []*schema.Message) (*MicroCompactResult, error) {
	if len(messages) == 0 {
		return &MicroCompactResult{
			Messages:    messages,
			TokensFreed: 0,
			Applied:     false,
		}, nil
	}

	// Apply all strategies unconditionally (use a very large target
	// so we don't short-circuit any strategy).
	const maxTarget = 1<<31 - 1
	result := MicroCompact(messages, maxTarget)
	return result, nil
}

// cloneMessages creates a shallow copy of the message slice. Individual
// messages are NOT cloned here; each strategy clones only messages it modifies.
func cloneMessages(messages []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, len(messages))
	copy(out, messages)
	return out
}

// --- Time-based microcompact ---
// Mirrors reference microCompact.ts maybeTimeBasedMicrocompact.

// compactableTools is the set of tools whose results can be cleared by
// time-based microcompact. Mirrors COMPACTABLE_TOOLS in microCompact.ts.
var compactableTools = map[string]bool{
	"Read":      true,
	"Bash":      true,
	"Grep":      true,
	"Glob":      true,
	"WebSearch": true,
	"WebFetch":  true,
	"Edit":      true,
	"Write":     true,
}

const timeBasedMCClearedMessage = "[Old tool result content cleared]"

// TimeBasedMCConfig holds configuration for time-based microcompact.
type TimeBasedMCConfig struct {
	Enabled             bool
	GapThresholdMinutes int // default 60
	KeepRecent          int // default 5
}

// getTimeBasedMCConfig reads config from environment or returns defaults.
func getTimeBasedMCConfig() TimeBasedMCConfig {
	cfg := TimeBasedMCConfig{
		Enabled:             true,
		GapThresholdMinutes: 60,
		KeepRecent:          5,
	}

	if v := os.Getenv("TIME_BASED_MC_ENABLED"); v == "false" || v == "0" {
		cfg.Enabled = false
	}
	if v := os.Getenv("TIME_BASED_MC_GAP_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.GapThresholdMinutes = n
		}
	}
	if v := os.Getenv("TIME_BASED_MC_KEEP_RECENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			cfg.KeepRecent = n
		}
	}
	return cfg
}

// TimeBasedMicrocompact clears old tool results when the time gap between
// the last assistant message and now exceeds a threshold. Only tool results
// from compactableTools are affected. The most recent N tool results are kept.
// Mirrors reference microCompact.ts maybeTimeBasedMicrocompact.
func TimeBasedMicrocompact(messages []*schema.Message, querySource string) *MicroCompactResult {
	cfg := getTimeBasedMCConfig()
	if !cfg.Enabled {
		return nil
	}

	// Only fire for main REPL threads (mirrors prefix check on "repl_main_thread").
	if !strings.HasPrefix(querySource, "repl_main_thread") && querySource != "main" {
		return nil
	}

	// Find last assistant message timestamp.
	var lastAssistantTime time.Time
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		if msg.Extra != nil {
			if ts, ok := msg.Extra["timestamp"].(float64); ok && ts > 0 {
				lastAssistantTime = time.UnixMilli(int64(ts))
				break
			}
			if ts, ok := msg.Extra["timestamp"].(int64); ok && ts > 0 {
				lastAssistantTime = time.UnixMilli(ts)
				break
			}
		}
		break // found assistant but no timestamp
	}

	if lastAssistantTime.IsZero() {
		return nil
	}

	gapMinutes := time.Since(lastAssistantTime).Minutes()
	if gapMinutes < float64(cfg.GapThresholdMinutes) {
		return nil
	}

	// Collect all compactable tool_use IDs in encounter order.
	var compactableIDs []string
	for _, msg := range messages {
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if compactableTools[tc.Function.Name] {
				compactableIDs = append(compactableIDs, tc.ID)
			}
		}
	}

	if len(compactableIDs) == 0 {
		return nil
	}

	// Keep the most recent N tool IDs.
	keepRecent := cfg.KeepRecent
	if keepRecent < 1 {
		keepRecent = 1
	}
	keepSet := make(map[string]bool)
	startKeep := len(compactableIDs) - keepRecent
	if startKeep < 0 {
		startKeep = 0
	}
	for _, id := range compactableIDs[startKeep:] {
		keepSet[id] = true
	}

	// Build clear set.
	clearSet := make(map[string]bool)
	for _, id := range compactableIDs {
		if !keepSet[id] {
			clearSet[id] = true
		}
	}

	if len(clearSet) == 0 {
		return nil
	}

	// Walk messages and clear tool results in clearSet.
	result := cloneMessages(messages)
	tokensSaved := 0
	for i, msg := range result {
		if msg == nil || msg.Role != schema.Tool {
			continue
		}
		toolCallID := msg.ToolCallID
		if toolCallID == "" {
			// Try to extract from Extra.
			if msg.Extra != nil {
				if id, ok := msg.Extra["tool_call_id"].(string); ok {
					toolCallID = id
				}
			}
		}
		if !clearSet[toolCallID] {
			continue
		}

		oldTokens := roughTextTokens(msg.Content)
		clone := *msg
		clone.Content = timeBasedMCClearedMessage
		if msg.Extra != nil {
			clone.Extra = make(map[string]any, len(msg.Extra)+1)
			for k, v := range msg.Extra {
				clone.Extra[k] = v
			}
		} else {
			clone.Extra = make(map[string]any, 1)
		}
		clone.Extra["time_based_mc_cleared"] = true
		result[i] = &clone
		tokensSaved += oldTokens - roughTextTokens(timeBasedMCClearedMessage)
	}

	if tokensSaved <= 0 {
		return nil
	}

	return &MicroCompactResult{
		Messages:    result,
		TokensFreed: tokensSaved,
		Applied:     true,
	}
}
