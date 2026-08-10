package engine

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

const (
	// MaxToolResultsPerMessageChars is the per-message aggregate character budget.
	// Mirrors toolLimits.ts:49.
	MaxToolResultsPerMessageChars = 200_000

	// toolResultPreviewSize is the number of bytes to show in the preview.
	// Mirrors toolResultStorage.ts PREVIEW_SIZE_BYTES.
	toolResultPreviewSize = 2000
)

// toolResultCandidate represents a tool result eligible for budget enforcement.
type toolResultCandidate struct {
	toolUseID string
	content   string
	size      int
	msgIndex  int // index in the message slice
}

// BudgetResult holds the output of tool result budget enforcement.
type BudgetResult struct {
	Messages        []*schema.Message
	NewReplacements []transcript.Replacement // only newly decided (not re-applied)
}

// ApplyToolResultBudget enforces per-message character budgets on tool results.
// Returns a new message slice with oversized results replaced by previews.
// Maintains cross-turn stability via ContentReplacementState.
// Mirrors toolResultStorage.ts:769-909.
func ApplyToolResultBudget(
	messages []*schema.Message,
	state *ContentReplacementState,
	skipToolNames map[string]bool,
) *BudgetResult {
	if state == nil {
		return &BudgetResult{Messages: messages}
	}

	replacementMap := make(map[string]string)
	var newlyReplaced []transcript.Replacement

	// Collect candidates grouped by message
	groups := collectToolResultCandidates(messages, skipToolNames)

	for _, group := range groups {
		if len(group) == 0 {
			continue
		}

		// Partition into mustReapply, frozen, fresh
		var mustReapply []toolResultCandidate
		var frozen []toolResultCandidate
		var fresh []toolResultCandidate

		for _, c := range group {
			if replacement, ok := state.Replacements[c.toolUseID]; ok {
				// Previously replaced → re-apply same replacement
				mustReapply = append(mustReapply, c)
				replacementMap[c.toolUseID] = replacement
			} else if state.SeenIDs[c.toolUseID] {
				// Previously seen but not replaced → frozen (never replace)
				frozen = append(frozen, c)
			} else {
				// Never seen → eligible for replacement
				fresh = append(fresh, c)
			}
		}

		// Re-apply all must-reapply (already added to replacementMap above)
		_ = mustReapply

		// Calculate budget for this message group
		frozenSize := 0
		for _, c := range frozen {
			frozenSize += c.size
		}
		freshSize := 0
		for _, c := range fresh {
			freshSize += c.size
		}

		totalSize := frozenSize + freshSize
		if totalSize <= MaxToolResultsPerMessageChars {
			// Under budget — mark all fresh as seen (frozen, unreplaced)
			for _, c := range fresh {
				state.SeenIDs[c.toolUseID] = true
			}
			continue
		}

		// Over budget — select largest fresh candidates for replacement
		selected := selectFreshToReplace(fresh, frozenSize, MaxToolResultsPerMessageChars)
		selectedSet := make(map[string]bool, len(selected))
		for _, c := range selected {
			selectedSet[c.toolUseID] = true
		}

		// Mark non-selected fresh as seen (frozen)
		for _, c := range fresh {
			if !selectedSet[c.toolUseID] {
				state.SeenIDs[c.toolUseID] = true
			}
		}

		// Generate replacements for selected
		for _, c := range selected {
			preview := buildToolResultPreview(c.content, c.size)
			state.SeenIDs[c.toolUseID] = true
			state.Replacements[c.toolUseID] = preview
			replacementMap[c.toolUseID] = preview
			newlyReplaced = append(newlyReplaced, transcript.Replacement{
				ToolUseID:   c.toolUseID,
				Replacement: preview,
			})
		}
	}

	if len(replacementMap) == 0 {
		return &BudgetResult{Messages: messages}
	}

	return &BudgetResult{
		Messages:        replaceToolResultContents(messages, replacementMap),
		NewReplacements: newlyReplaced,
	}
}

// collectToolResultCandidates groups tool result candidates by their containing message.
func collectToolResultCandidates(messages []*schema.Message, skipToolNames map[string]bool) [][]toolResultCandidate {
	var groups [][]toolResultCandidate

	for i, msg := range messages {
		if msg == nil || msg.Role != schema.Tool {
			continue
		}

		// Check if this tool should be skipped
		toolName := ""
		if msg.ToolName != "" {
			toolName = msg.ToolName
		} else if msg.Extra != nil {
			if tn, ok := msg.Extra["tool_name"].(string); ok {
				toolName = tn
			}
		}
		if skipToolNames != nil && skipToolNames[toolName] {
			continue
		}

		toolUseID := ""
		if msg.Extra != nil {
			if id, ok := msg.Extra["tool_use_id"].(string); ok {
				toolUseID = id
			}
		}
		if toolUseID == "" {
			// Try ToolCallID field
			if msg.ToolCallID != "" {
				toolUseID = msg.ToolCallID
			}
		}
		if toolUseID == "" {
			continue
		}

		content := msg.Content
		size := len(content)
		if size == 0 {
			continue
		}

		candidate := toolResultCandidate{
			toolUseID: toolUseID,
			content:   content,
			size:      size,
			msgIndex:  i,
		}

		// Group candidates by consecutive tool messages (simulating per-user-message grouping)
		if len(groups) == 0 {
			groups = append(groups, []toolResultCandidate{candidate})
		} else {
			lastGroup := groups[len(groups)-1]
			lastCandidate := lastGroup[len(lastGroup)-1]
			// Same group if adjacent tool messages (consecutive indices)
			if i == lastCandidate.msgIndex+1 {
				groups[len(groups)-1] = append(groups[len(groups)-1], candidate)
			} else {
				groups = append(groups, []toolResultCandidate{candidate})
			}
		}
	}

	return groups
}

// selectFreshToReplace picks the largest fresh candidates to bring total under budget.
// Mirrors toolResultStorage.ts:675-692.
func selectFreshToReplace(fresh []toolResultCandidate, frozenSize, limit int) []toolResultCandidate {
	// Sort descending by size (largest first)
	sorted := make([]toolResultCandidate, len(fresh))
	copy(sorted, fresh)
	sortCandidatesDescBySize(sorted)

	freshTotal := 0
	for _, c := range fresh {
		freshTotal += c.size
	}

	var selected []toolResultCandidate
	remaining := frozenSize + freshTotal
	for _, c := range sorted {
		if remaining <= limit {
			break
		}
		selected = append(selected, c)
		remaining -= c.size
	}
	return selected
}

// sortCandidatesDescBySize sorts candidates by size descending (simple insertion sort).
func sortCandidatesDescBySize(candidates []toolResultCandidate) {
	for i := 1; i < len(candidates); i++ {
		j := i
		for j > 0 && candidates[j].size > candidates[j-1].size {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
			j--
		}
	}
}

// buildToolResultPreview creates a truncated preview for an oversized tool result.
// Mirrors toolResultStorage.ts:189-199 (without disk path).
func buildToolResultPreview(content string, originalSize int) string {
	preview := content
	if len(preview) > toolResultPreviewSize {
		// Try to cut at last newline in latter half
		cutoff := toolResultPreviewSize
		halfPoint := toolResultPreviewSize / 2
		if idx := strings.LastIndex(preview[halfPoint:cutoff], "\n"); idx >= 0 {
			cutoff = halfPoint + idx + 1
		}
		preview = preview[:cutoff]
	}

	hasMore := len(content) > toolResultPreviewSize

	var b strings.Builder
	b.WriteString("<tool-result-budget-exceeded>\n")
	fmt.Fprintf(&b, "Output too large (%s). Showing preview (first %s):\n\n",
		formatByteSize(originalSize), formatByteSize(toolResultPreviewSize))
	b.WriteString(preview)
	if hasMore {
		b.WriteString("\n...\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString("</tool-result-budget-exceeded>")
	return b.String()
}

// replaceToolResultContents returns a new message slice with replaced contents.
// Messages that don't need replacement are shared by reference.
// Mirrors toolResultStorage.ts:699-726.
func replaceToolResultContents(messages []*schema.Message, replacementMap map[string]string) []*schema.Message {
	result := make([]*schema.Message, len(messages))
	for i, msg := range messages {
		if msg == nil || msg.Role != schema.Tool {
			result[i] = msg
			continue
		}

		toolUseID := ""
		if msg.ToolCallID != "" {
			toolUseID = msg.ToolCallID
		} else if msg.Extra != nil {
			if id, ok := msg.Extra["tool_use_id"].(string); ok {
				toolUseID = id
			}
		}

		replacement, ok := replacementMap[toolUseID]
		if !ok {
			result[i] = msg
			continue
		}

		// Shallow copy with replaced content
		replaced := *msg
		replaced.Content = replacement
		result[i] = &replaced
	}
	return result
}

func formatByteSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

// ReconstructContentReplacementState rebuilds the content replacement state from
// loaded messages and persisted replacement records. All tool_use_ids present in
// messages are marked as seen (frozen), and records repopulate the replacements map.
// Mirrors reference toolResultStorage.ts:960-988 reconstructContentReplacementState.
func ReconstructContentReplacementState(
	messages []*schema.Message,
	records []transcript.Replacement,
) *ContentReplacementState {
	state := NewContentReplacementState()

	// Collect all candidate tool_use_ids from messages
	candidateIDs := make(map[string]bool)
	for _, msg := range messages {
		if msg == nil || msg.Role != schema.Tool {
			continue
		}
		toolUseID := ""
		if msg.ToolCallID != "" {
			toolUseID = msg.ToolCallID
		} else if msg.Extra != nil {
			if id, ok := msg.Extra["tool_use_id"].(string); ok {
				toolUseID = id
			}
		}
		if toolUseID != "" {
			candidateIDs[toolUseID] = true
		}
	}

	// All candidate IDs are marked "seen" → frozen against new replacement
	for id := range candidateIDs {
		state.SeenIDs[id] = true
	}

	// Records repopulate the replacements map (only for IDs still in messages)
	for _, r := range records {
		if candidateIDs[r.ToolUseID] {
			state.Replacements[r.ToolUseID] = r.Replacement
		}
	}

	return state
}
