package compact

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const collapsePreservedTailMessages = 2

// CollapseStatus represents whether a collapse entry is staged or committed.
type CollapseStatus string

const (
	// CollapseStaged means the entry has been identified for collapse but not yet applied.
	CollapseStaged CollapseStatus = "staged"
	// CollapseCommitted means the entry has been collapsed and replaced with a summary.
	CollapseCommitted CollapseStatus = "committed"
)

// CollapseEntry tracks a group of messages that can be collapsed together.
// A collapse entry represents a consecutive tool-use group: an assistant message
// with tool_calls followed by one or more tool result messages.
type CollapseEntry struct {
	// StartIndex is the index of the first message in this collapse group
	// within the original message slice.
	StartIndex int
	// EndIndex is the exclusive end index of messages in this collapse group.
	EndIndex int
	// Messages holds the actual messages in this group (for summary generation).
	Messages []*schema.Message
	// Status indicates whether this entry is staged or committed.
	Status CollapseStatus
	// Summary is the generated summary text for the collapsed content.
	Summary string
	// TokensSaved is the estimated token count freed by collapsing this group.
	TokensSaved int
}

// CollapseState holds the full collapse lifecycle state across calls.
// It tracks staged entries (identified but not yet applied) and committed
// entries (already replaced with summaries). This enables incremental
// collapse as context pressure increases.
type CollapseState struct {
	// Staged holds entries identified for collapse but not yet committed.
	Staged []*CollapseEntry
	// Committed holds entries that have been collapsed and replaced with summaries.
	Committed []*CollapseEntry
	// TotalTokensFreed is the cumulative token count freed by all committed collapses.
	TotalTokensFreed int
}

// NewCollapseState creates a fresh empty collapse state.
func NewCollapseState() *CollapseState {
	return &CollapseState{
		Staged:    make([]*CollapseEntry, 0),
		Committed: make([]*CollapseEntry, 0),
	}
}

// CollapseResult holds the result of context collapse application.
type CollapseResult struct {
	Messages []*schema.Message
	// State is the updated collapse state after this operation.
	// Nil when called via the backward-compatible signature without state.
	State *CollapseState
}

// ApplyCollapsesIfNeeded projects the collapsed context view and commits more collapses.
// This is the backward-compatible entry point. It detects collapsible tool-use groups
// and applies collapses without persistent state tracking.
// Mirrors query.ts:440-447.
func ApplyCollapsesIfNeeded(
	messages []*schema.Message,
	querySource string,
) *CollapseResult {
	return ApplyCollapsesWithState(messages, querySource, "", nil)
}

// ApplyCollapsesWithState is the full-featured collapse function that maintains
// state across calls. It identifies consecutive tool-use groups, stages them for
// potential collapse, and commits oldest staged entries when above threshold.
//
// Parameters:
//   - messages: current conversation messages
//   - querySource: the query source identifier (skips collapse for "compact")
//   - modelName: model name for threshold calculation (empty uses default)
//   - state: existing collapse state (nil creates ephemeral state)
//
// Returns a CollapseResult with the projected message view and updated state.
func ApplyCollapsesWithState(
	messages []*schema.Message,
	querySource string,
	modelName string,
	state *CollapseState,
) *CollapseResult {
	// Skip collapse during compaction queries
	if querySource == "compact" {
		return &CollapseResult{Messages: messages, State: state}
	}
	if len(messages) == 0 {
		return &CollapseResult{Messages: messages, State: state}
	}

	// Initialize state if not provided
	if state == nil {
		state = NewCollapseState()
	}

	// Detect collapsible tool-use groups
	groups := detectToolUseGroups(messages)
	if len(groups) == 0 {
		return &CollapseResult{Messages: messages, State: state}
	}

	// Stage new collapsible groups that aren't already tracked
	stageNewGroups(groups, state)

	// Check if we're above the collapse commit threshold
	tokenCount := EstimateTokenCount(messages)
	threshold := getCollapseThreshold(modelName)

	if tokenCount >= threshold && len(state.Staged) > 0 {
		// Commit oldest staged entries to reduce context pressure
		commitCount := determineCommitCount(state, tokenCount, threshold)
		commitOldestStaged(state, commitCount)
	}

	// Project the message view with committed collapses applied
	projected := projectCollapsedView(messages, state)

	return &CollapseResult{Messages: projected, State: state}
}

// CommitStagedCollapses commits all currently staged entries by producing
// summary messages. It updates the state and returns the modified message array.
func CommitStagedCollapses(
	messages []*schema.Message,
	state *CollapseState,
) *CollapseResult {
	if state == nil {
		return &CollapseResult{Messages: messages, State: nil}
	}
	if len(state.Staged) == 0 {
		return &CollapseResult{Messages: messages, State: state}
	}

	commitOldestStaged(state, len(state.Staged))
	projected := projectCollapsedView(messages, state)

	return &CollapseResult{Messages: projected, State: state}
}

// DrainResult holds the result of a collapse drain recovery.
type DrainResult struct {
	Committed int
	Messages  []*schema.Message
	// State is the updated collapse state after drain. Nil for stateless calls.
	State *CollapseState
}

// RecoverFromOverflow drains staged collapses on a REAL API 413 error.
// This is the backward-compatible entry point.
// Mirrors query.ts:1089-1117.
func RecoverFromOverflow(
	messages []*schema.Message,
	querySource string,
) *DrainResult {
	return RecoverFromOverflowWithState(messages, querySource, nil)
}

// RecoverFromOverflowWithState drains staged collapses on a REAL API 413 error,
// working with persistent collapse state when available.
func RecoverFromOverflowWithState(
	messages []*schema.Message,
	querySource string,
	state *CollapseState,
) *DrainResult {
	if querySource == "compact" {
		return &DrainResult{Committed: 0, Messages: messages, State: state}
	}
	if len(messages) <= collapsePreservedTailMessages {
		return &DrainResult{Committed: 0, Messages: messages, State: state}
	}

	// If we have state with staged entries, commit them first
	if state != nil && len(state.Staged) > 0 {
		commitOldestStaged(state, len(state.Staged))
		projected := projectCollapsedView(messages, state)
		totalCommitted := 0
		for _, entry := range state.Committed {
			totalCommitted += len(entry.Messages)
		}
		return &DrainResult{Committed: totalCommitted, Messages: projected, State: state}
	}

	// Stateless path: fall back to the original drain logic
	if hasCommittedCompact(messages) || hasCollapseStaged(messages) {
		return &DrainResult{Committed: 0, Messages: messages, State: state}
	}

	cut := len(messages) - collapsePreservedTailMessages
	if cut <= 0 {
		return &DrainResult{Committed: 0, Messages: messages, State: state}
	}

	prefix := messages[:cut]
	preserved := messages[cut:]
	committed := countDrainableMessages(prefix)
	if committed == 0 {
		return &DrainResult{Committed: 0, Messages: messages, State: state}
	}

	summary := &schema.Message{
		Role:    schema.System,
		Content: buildCollapseDrainSummary(prefix),
		Extra: map[string]any{
			"subtype":   "collapse_staged",
			"trigger":   "collapse_drain",
			"committed": committed,
		},
	}

	out := make([]*schema.Message, 0, 1+len(preserved))
	out = append(out, summary)
	out = append(out, preserved...)
	return &DrainResult{Committed: committed, Messages: out, State: state}
}

// --- Tool-use group detection ---

// toolUseGroup represents a consecutive sequence of an assistant message with
// tool_calls followed by one or more tool result messages.
type toolUseGroup struct {
	startIndex int
	endIndex   int // exclusive
	messages   []*schema.Message
}

// detectToolUseGroups scans messages for consecutive tool-use patterns:
// assistant message with ToolCalls > 0 followed by Tool role messages.
func detectToolUseGroups(messages []*schema.Message) []toolUseGroup {
	var groups []toolUseGroup
	i := 0
	for i < len(messages) {
		msg := messages[i]
		if msg == nil {
			i++
			continue
		}

		// Look for assistant message with tool calls
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			start := i
			i++

			// Collect consecutive tool result messages
			for i < len(messages) && messages[i] != nil && messages[i].Role == schema.Tool {
				i++
			}

			// Only form a group if we found at least one tool result
			if i > start+1 {
				groupMsgs := make([]*schema.Message, i-start)
				copy(groupMsgs, messages[start:i])
				groups = append(groups, toolUseGroup{
					startIndex: start,
					endIndex:   i,
					messages:   groupMsgs,
				})
			}
			continue
		}
		i++
	}
	return groups
}

// --- Staging logic ---

// stageNewGroups adds newly detected groups to the staged list if they aren't
// already tracked (by index range overlap).
func stageNewGroups(groups []toolUseGroup, state *CollapseState) {
	for _, g := range groups {
		if isGroupAlreadyTracked(g, state) {
			continue
		}
		tokensSaved := estimateGroupTokens(g.messages)
		entry := &CollapseEntry{
			StartIndex:  g.startIndex,
			EndIndex:    g.endIndex,
			Messages:    g.messages,
			Status:      CollapseStaged,
			Summary:     "", // generated on commit
			TokensSaved: tokensSaved,
		}
		state.Staged = append(state.Staged, entry)
	}
}

// isGroupAlreadyTracked checks if a tool-use group overlaps with any existing
// staged or committed entry.
func isGroupAlreadyTracked(g toolUseGroup, state *CollapseState) bool {
	for _, entry := range state.Staged {
		if rangesOverlap(g.startIndex, g.endIndex, entry.StartIndex, entry.EndIndex) {
			return true
		}
	}
	for _, entry := range state.Committed {
		if rangesOverlap(g.startIndex, g.endIndex, entry.StartIndex, entry.EndIndex) {
			return true
		}
	}
	return false
}

func rangesOverlap(aStart, aEnd, bStart, bEnd int) bool {
	return aStart < bEnd && bStart < aEnd
}

// estimateGroupTokens estimates the token count of a tool-use group.
func estimateGroupTokens(messages []*schema.Message) int {
	return EstimateTokenCount(messages)
}

// --- Commit logic ---

// getCollapseThreshold returns the token count above which staged collapses
// should be committed. Uses 80% of the auto-compact threshold to give
// collapse a chance to reduce pressure before full compaction kicks in.
func getCollapseThreshold(modelName string) int {
	autoThreshold := GetAutoCompactThreshold(modelName)
	// Collapse commits at 80% of the auto-compact threshold
	return int(float64(autoThreshold) * 0.80)
}

// determineCommitCount decides how many staged entries to commit based on
// how far above the threshold we are.
func determineCommitCount(state *CollapseState, tokenCount, threshold int) int {
	if len(state.Staged) == 0 {
		return 0
	}

	excess := tokenCount - threshold
	if excess <= 0 {
		return 1 // commit at least one when triggered
	}

	// Commit enough staged entries to cover the excess
	accumulated := 0
	count := 0
	for _, entry := range state.Staged {
		accumulated += entry.TokensSaved
		count++
		if accumulated >= excess {
			break
		}
	}
	return count
}

// commitOldestStaged commits up to n oldest staged entries by generating
// summaries and moving them to the committed list.
func commitOldestStaged(state *CollapseState, n int) {
	if n <= 0 || len(state.Staged) == 0 {
		return
	}
	if n > len(state.Staged) {
		n = len(state.Staged)
	}

	toCommit := state.Staged[:n]
	state.Staged = state.Staged[n:]

	for _, entry := range toCommit {
		entry.Status = CollapseCommitted
		entry.Summary = buildCollapseEntrySummary(entry)
		state.TotalTokensFreed += entry.TokensSaved
		state.Committed = append(state.Committed, entry)
	}
}

// buildCollapseEntrySummary generates a compact summary for a committed
// collapse entry describing the tool calls that were collapsed.
func buildCollapseEntrySummary(entry *CollapseEntry) string {
	if entry == nil || len(entry.Messages) == 0 {
		return "Tool use context collapsed."
	}

	var toolNames []string
	for _, msg := range entry.Messages {
		if msg == nil {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name != "" {
				toolNames = append(toolNames, tc.Function.Name)
			}
		}
	}

	var parts []string
	parts = append(parts, "[Collapsed tool use]")
	if len(toolNames) > 0 {
		unique := uniqueStrings(toolNames)
		if len(unique) <= 3 {
			parts = append(parts, fmt.Sprintf("Tools: %s", strings.Join(unique, ", ")))
		} else {
			parts = append(parts, fmt.Sprintf("Tools: %s (+%d more)", strings.Join(unique[:3], ", "), len(unique)-3))
		}
	}

	// Include brief results preview
	resultCount := 0
	for _, msg := range entry.Messages {
		if msg != nil && msg.Role == schema.Tool {
			resultCount++
		}
	}
	if resultCount > 0 {
		parts = append(parts, fmt.Sprintf("Results: %d tool result(s) received", resultCount))
	}

	return strings.Join(parts, " | ")
}

// --- Projection logic ---

// projectCollapsedView produces the message array with committed collapses
// replaced by their summary messages.
func projectCollapsedView(messages []*schema.Message, state *CollapseState) []*schema.Message {
	if state == nil || len(state.Committed) == 0 {
		return messages
	}

	// Build a set of message indices that are covered by committed entries
	type indexRange struct {
		start   int
		end     int
		summary string
	}
	ranges := make([]indexRange, 0, len(state.Committed))
	for _, entry := range state.Committed {
		// Only apply ranges that are within the current message bounds
		if entry.StartIndex < len(messages) {
			end := entry.EndIndex
			if end > len(messages) {
				end = len(messages)
			}
			ranges = append(ranges, indexRange{
				start:   entry.StartIndex,
				end:     end,
				summary: entry.Summary,
			})
		}
	}

	if len(ranges) == 0 {
		return messages
	}

	// Sort ranges by start index (they should already be in order from staged progression)
	// Build the projected view
	projected := make([]*schema.Message, 0, len(messages))
	i := 0
	rangeIdx := 0
	for i < len(messages) {
		// Check if current position matches a committed range
		if rangeIdx < len(ranges) && i == ranges[rangeIdx].start {
			// Replace with summary message
			summaryMsg := &schema.Message{
				Role:    schema.System,
				Content: ranges[rangeIdx].summary,
				Extra: map[string]any{
					"subtype": "collapse_committed",
					"trigger": "collapse_apply",
				},
			}
			projected = append(projected, summaryMsg)
			i = ranges[rangeIdx].end
			rangeIdx++
		} else {
			projected = append(projected, messages[i])
			i++
		}
	}

	return projected
}

// --- Utility functions ---

func uniqueStrings(ss []string) []string {
	seen := make(map[string]struct{}, len(ss))
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			result = append(result, s)
		}
	}
	return result
}

func hasCommittedCompact(messages []*schema.Message) bool {
	for _, msg := range messages {
		if msg == nil || msg.Extra == nil {
			continue
		}
		subtype, _ := msg.Extra["subtype"].(string)
		if subtype == "compact_boundary" || subtype == "compact_summary" {
			return true
		}
	}
	return false
}

func hasCollapseStaged(messages []*schema.Message) bool {
	for _, msg := range messages {
		if msg == nil || msg.Extra == nil {
			continue
		}
		if subtype, _ := msg.Extra["subtype"].(string); subtype == "collapse_staged" {
			return true
		}
	}
	return false
}

func countDrainableMessages(messages []*schema.Message) int {
	count := 0
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		count++
	}
	return count
}

func buildCollapseDrainSummary(messages []*schema.Message) string {
	lines := []string{"Earlier context was collapsed after a prompt-too-long error.", "Collapsed context:"}
	previews := make([]string, 0, 4)
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		preview := collapsePreview(msg)
		if preview == "" {
			continue
		}
		previews = append(previews, fmt.Sprintf("- %s: %s", msg.Role, preview))
		if len(previews) == 4 {
			break
		}
	}
	if len(previews) == 0 {
		previews = append(previews, "- earlier context omitted")
	}
	lines = append(lines, previews...)
	lines = append(lines, "Continue from the preserved recent context.")
	return strings.Join(lines, "\n")
}

func collapsePreview(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	text := strings.TrimSpace(msg.Content)
	if text == "" && len(msg.UserInputMultiContent) > 0 {
		parts := make([]string, 0, len(msg.UserInputMultiContent))
		for _, part := range msg.UserInputMultiContent {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
		text = strings.Join(parts, " ")
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 120 {
		text = text[:117] + "..."
	}
	return text
}
