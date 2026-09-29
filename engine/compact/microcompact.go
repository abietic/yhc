package compact

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	enginemessages "github.com/abietic/yhc/engine/messages"
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
// - Large base64 image data (replace with placeholder)
// - Very long assistant reasoning (trim middle)
//
// Strategies are applied in order until targetTokensToFree is reached.
// The latest assistant tool round has not yet been consumed by a subsequent
// assistant response, so opportunistic trimming leaves that entire suffix intact.
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
	protectedStart := unconsumedToolRoundStart(messages)
	current := cloneMessages(messages[:protectedStart])

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

	return &MicroCompactResult{
		Messages:    append(current, cloneMessages(messages[protectedStart:])...),
		TokensFreed: totalFreed,
		Applied:     totalFreed > 0,
	}
}

// unconsumedToolRoundStart returns the latest assistant's index when it calls
// tools, or len(messages) once a later assistant has consumed those results.
// Protecting the whole round also covers parallel results and slow tools whose
// owning assistant timestamp predates the idle-compaction threshold.
func unconsumedToolRoundStart(messages []*schema.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if msg := messages[i]; msg != nil && msg.Role == schema.Assistant {
			if len(msg.ToolCalls) > 0 {
				return i
			}
			break
		}
	}
	return len(messages)
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

// TrimLongThinking trims unsigned reasoning in the representation sent to the
// provider and updates its flat mirror. Stream deltas are joined before trimming
// each logical block. Messages containing signed blocks remain intact: private
// continuation bindings may cover the entire message, including unsigned siblings.
func TrimLongThinking(messages []*schema.Message, maxLen int) ([]*schema.Message, int) {
	if maxLen <= 0 {
		maxLen = 4000
	}
	result := append([]*schema.Message(nil), messages...)
	totalFreed := 0
	for i, msg := range messages {
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		parts, err := enginemessages.ConcatAssistantOutputParts(msg.AssistantGenMultiContent)
		if err != nil {
			continue
		}
		hasReasoning, signed := false, false
		for _, part := range parts {
			if part.Type == schema.ChatMessagePartTypeReasoning {
				hasReasoning = true
				if part.Reasoning == nil || part.Reasoning.Signature != "" {
					signed = true
				}
			}
		}
		if signed {
			continue
		}
		before, after := msg.ReasoningContent, trimReasoningText(msg.ReasoningContent, maxLen)
		var trimmedParts []schema.MessageOutputPart
		if hasReasoning {
			trimmedParts = append([]schema.MessageOutputPart(nil), parts...)
			var original, trimmed strings.Builder
			for j, part := range parts {
				if part.Type != schema.ChatMessagePartTypeReasoning {
					continue
				}
				reasoning := *part.Reasoning
				original.WriteString(reasoning.Text)
				reasoning.Text = trimReasoningText(reasoning.Text, maxLen)
				trimmed.WriteString(reasoning.Text)
				trimmedParts[j].Reasoning = &reasoning
			}
			before, after = original.String(), trimmed.String()
		}
		if before == after {
			continue
		}
		clone := *msg
		clone.ReasoningContent = after
		if hasReasoning {
			clone.AssistantGenMultiContent = trimmedParts
		}
		result[i] = &clone
		totalFreed += max(0, roughTextTokens(before)-roughTextTokens(after))
	}
	return result, totalFreed
}

func trimReasoningText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	const separator = "\n...[reasoning truncated]...\n"
	head, tail := maxLen*2/5, len(text)-maxLen*2/5
	// Keep valid UTF-8 even when a byte budget splits a multibyte rune.
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	for tail < len(text) && !utf8.RuneStart(text[tail]) {
		tail++
	}
	if head+len(separator)+len(text)-tail >= len(text) {
		return text
	}
	return text[:head] + separator + text[tail:]
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
	if start := unconsumedToolRoundStart(messages); start < len(messages) {
		for _, call := range messages[start].ToolCalls {
			keepSet[call.ID] = true
		}
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
