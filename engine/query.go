package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

// queryEventValidator is an optional event order validator attached to a query
// execution. When non-nil, the yield wrapper will validate event ordering.
// This is package-level state for test introspection only; production code
// uses the validator embedded in the canonical ProjectGraph runtime.
var debugEventValidator atomic.Pointer[EventOrderValidator]

// Query runs the agent loop. Events are yielded via the callback.
// Returns a Terminal when the loop ends.
// Mirrors query.ts:219-239.
func Query(ctx context.Context, params QueryParams, yieldFn func(QueryEvent)) Terminal {
	kernel := productionQueryKernel()
	if fixtureKernel := fixtureQueryKernelFromContext(ctx); fixtureKernel != nil {
		kernel = fixtureKernel
	}
	return queryWithKernel(ctx, params, yieldFn, kernel)
}

func queryWithKernel(
	ctx context.Context,
	params QueryParams,
	yieldFn func(QueryEvent),
	kernel queryKernel,
) Terminal {
	if params.MaxTurns != nil && *params.MaxTurns < 0 {
		panic("engine: MaxTurns must be zero (unlimited) or positive")
	}
	if yieldFn == nil {
		yieldFn = func(QueryEvent) {}
	}
	if kernel == nil {
		return Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("engine: query kernel is required"),
		}
	}
	params.repeatedToolGuard = newRepeatedToolCallGuard()

	deps := params.Deps
	defaults := defaultDeps()
	if deps == nil {
		deps = defaults
	} else {
		if deps.UUID == nil {
			deps.UUID = defaults.UUID
		}
		if deps.CallModel == nil {
			deps.CallModel = defaults.CallModel
		}
	}

	queryCtx := ctx
	if queryCtx == nil {
		queryCtx = context.Background()
	}
	queryCtx, cancelProjection := context.WithCancel(queryCtx)
	defer cancelProjection()
	projectionEmitter := newAssistantProjectionEmitter(
		cancelProjection,
		deps.UUID,
		yieldFn,
	)

	consumedCommandUUIDs := make([]string, 0)
	terminal := kernel.run(queryCtx, queryKernelRequest{
		params:               params,
		deps:                 deps,
		consumedCommandUUIDs: &consumedCommandUUIDs,
		yield:                projectionEmitter.Emit,
	})
	if projectionErr := projectionEmitter.Err(); projectionErr != nil {
		terminal = Terminal{
			Reason:    TerminalModelError,
			TurnCount: terminal.TurnCount,
			MaxTurns:  terminal.MaxTurns,
			Err:       projectionErr,
		}
	}

	// Emit lifecycle completed for all commands consumed during this query.
	// Moved from within the loop (step 24) to post-terminal to match
	// the TS reference's query.ts:1600-1643 timing.
	for _, uuid := range consumedCommandUUIDs {
		projectionEmitter.Emit(QueryEvent{
			Type: EventCommandLifecycle,
			CommandLifecycle: &CommandLifecycleEvent{
				CommandUUID: uuid,
				Phase:       CommandLifecycleCompleted,
			},
		})
	}
	if params.InputCoordinator != nil {
		_ = params.InputCoordinator.settleStopRequests(
			runtimeInputScopeForQuery(params, params.ToolUseContext),
		)
		if !params.InputCoordinator.Durable() {
			_ = params.InputCoordinator.Settle(consumedCommandUUIDs...)
		}
	}

	return terminal
}

func defaultDeps() *QueryDeps {
	return &QueryDeps{
		UUID:      generateUUID,
		CallModel: execution.CallModel,
	}
}

// generateUUID produces a UUID v4 string.
func generateUUID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func newRecoveryBoundaryMessage(message *schema.Message, reason string) *schema.Message {
	if message == nil {
		return nil
	}
	cloned := *message
	cloned.Extra = cloneMessageExtra(message.Extra)
	if cloned.Extra == nil {
		cloned.Extra = make(map[string]any)
	}
	cloned.Extra["subtype"] = "recovery_boundary"
	cloned.Extra["recovery_reason"] = reason
	return &cloned
}

func getAgentID(ctx *ToolUseContext) string {
	if ctx == nil {
		return ""
	}
	return ctx.AgentID
}

func getSessionID(params QueryParams, ctx *ToolUseContext) string {
	if ctx != nil {
		if sessionID := strings.TrimSpace(ctx.SessionID); sessionID != "" {
			return sessionID
		}
	}
	return strings.TrimSpace(params.SessionID)
}

func toExecutionQueryTracking(tracking *QueryTracking) *execution.QueryTracking {
	if tracking == nil {
		return nil
	}
	return &execution.QueryTracking{
		ChainID: tracking.ChainID,
		Depth:   tracking.Depth,
	}
}

func toExecutionThinkingConfig(config *ThinkingConfig) *execution.ThinkingConfig {
	if config == nil {
		return nil
	}
	return &execution.ThinkingConfig{
		Type:         config.Type,
		BudgetTokens: config.BudgetTokens,
	}
}

// buildSkipToolNames builds the set of tool names whose results should skip budget enforcement.
// In the reference, tools with maxResultSizeChars=Infinity (like Read, Bash) are skipped.
// We derive this from ToolRegistry.SkipResultBudget or fall back to a known allowlist.
func buildSkipToolNames(toolInfos []*schema.ToolInfo, registry *tools.Registry) map[string]bool {
	skip := make(map[string]bool)
	if registry != nil {
		for _, info := range toolInfos {
			if info == nil {
				continue
			}
			if impl, ok := registry.Get(info.Name); ok && impl.SkipResultBudget {
				skip[info.Name] = true
			}
		}
	}
	if len(skip) == 0 {
		// Fallback allowlist matching reference behavior
		for _, name := range []string{"Read", "Bash", "Write"} {
			skip[name] = true
		}
	}
	return skip
}

func emitStreamingToolResult(
	yield func(QueryEvent),
	toolResults *[]*schema.Message,
	completed *execution.ToolResult,
) error {
	if completed == nil {
		return nil
	}
	for _, msg := range completed.BeforeMessages {
		if msg == nil {
			continue
		}
		yield(QueryEvent{Type: EventAttachment, AttachmentMessage: msg})
		if toolResults != nil {
			*toolResults = append(*toolResults, msg)
		}
	}
	terminal, err := buildCanonicalToolTerminalProjection(completed)
	if err != nil {
		return fmt.Errorf("project canonical tool terminal: %w", err)
	}
	yield(terminal)
	if completed.Message != nil {
		yield(QueryEvent{Type: EventToolResult, ToolResultMessage: completed.Message})
		if toolResults != nil {
			*toolResults = append(*toolResults, completed.Message)
		}
	}
	for _, msg := range completed.AfterMessages {
		if msg == nil {
			continue
		}
		yield(QueryEvent{Type: EventAttachment, AttachmentMessage: msg})
		if toolResults != nil {
			*toolResults = append(*toolResults, msg)
		}
	}
	if completed.ContextPublisher != nil {
		completed.ContextPublisher()
		completed.ContextPublisher = nil
	}
	return nil
}

func toEngineQueryEvent(evt execution.QueryEvent) QueryEvent {
	engineEvt := QueryEvent{Type: QueryEventType(evt.Type), Message: evt.Message}
	switch evt.Type {
	case execution.QueryEventType("tool_result"):
		engineEvt.ToolResultMessage = evt.Message
	case execution.QueryEventType("attachment"):
		engineEvt.AttachmentMessage = evt.Message
	}
	return engineEvt
}

func newAPIErrorMessage(content, errorType string) *schema.Message {
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		Extra: map[string]any{
			"api_error": true,
		},
	}
	if errorType != "" {
		msg.Extra["error_type"] = errorType
	}
	return msg
}

func isAPIErrorMessage(msg *schema.Message) bool {
	if msg == nil || msg.Extra == nil {
		return false
	}
	_, ok := msg.Extra["api_error"]
	return ok
}

func lastAssistantMessage(messages []*schema.Message) *schema.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role == schema.Assistant {
			return messages[i]
		}
	}
	return nil
}

func isTerminalAgentNotificationStatus(status string) bool {
	switch status {
	case "completed", "failed", "aborted", "killed":
		return true
	default:
		return false
	}
}

func drainAgentProgressEvents(drain func() []tools.AgentProgressEvent) []*TaskProgressEvent {
	if drain == nil {
		return nil
	}
	snapshots := drain()
	if len(snapshots) == 0 {
		return nil
	}
	events := make([]*TaskProgressEvent, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Type != "system" || snapshot.Subtype != "task_progress" || strings.TrimSpace(snapshot.TaskID) == "" {
			continue
		}
		recent := make([]TaskProgressActivity, 0, len(snapshot.RecentActivities))
		for _, activity := range snapshot.RecentActivities {
			recent = append(recent, TaskProgressActivity{
				ToolName:    activity.ToolName,
				Description: activity.ActivityDescription,
				IsSearch:    activity.IsSearch,
				IsRead:      activity.IsRead,
			})
		}
		events = append(events, &TaskProgressEvent{
			Type:        snapshot.Type,
			Subtype:     snapshot.Subtype,
			TaskID:      snapshot.TaskID,
			ToolUseID:   snapshot.ToolUseID,
			Description: snapshot.Description,
			Usage: TaskProgressUsage{
				TotalTokens: snapshot.Usage.TotalTokens,
				ToolUses:    snapshot.Usage.ToolUses,
				DurationMS:  snapshot.Usage.DurationMS,
			},
			LastToolName:     snapshot.LastToolName,
			Summary:          snapshot.Summary,
			RecentActivities: recent,
		})
	}
	return events
}

func emitTaskLifecycleEvents(yield func(QueryEvent), drain func() []tools.TaskLifecycleEvent) {
	if yield == nil || drain == nil {
		return
	}
	for _, evt := range drain() {
		if evt.Task == nil {
			continue
		}
		yield(QueryEvent{
			Type: EventTaskLifecycle,
			TaskLifecycle: &TaskLifecycleEvent{
				Phase:         string(evt.Phase),
				TaskID:        evt.Task.ID,
				Subject:       evt.Task.Subject,
				Description:   evt.Task.Description,
				ActiveForm:    evt.Task.ActiveForm,
				Status:        string(evt.Task.Status),
				Owner:         evt.Task.Owner,
				UpdatedFields: append([]string(nil), evt.UpdatedFields...),
				UpdatedAt:     evt.Task.UpdatedAt,
			},
		})
	}
}

// getRuntimeMainLoopModel selects the model for this iteration based on
// the user-specified model setting and permission mode.
// Mirrors src/utils/model/model.ts:getRuntimeMainLoopModel.
func getRuntimeMainLoopModel(opts *ToolUseOptions, messages []*schema.Message) string {
	if opts == nil {
		return ""
	}
	mainLoopModel := opts.MainLoopModel
	modelSetting := opts.ModelSetting
	permMode := opts.PermissionMode

	if modelSetting == "" {
		return mainLoopModel
	}

	// "opusplan": use Opus in plan mode unless context exceeds 200k tokens.
	if modelSetting == "opusplan" && permMode == permission.ModePlan {
		if !doesMostRecentAssistantMessageExceed200k(messages) {
			return getDefaultOpusModel()
		}
	}

	// "haiku" in plan mode: upgrade to Sonnet ("sonnetplan" by default).
	if modelSetting == "haiku" && permMode == permission.ModePlan {
		return getDefaultSonnetModel()
	}

	return mainLoopModel
}

// getDefaultOpusModel returns the default Opus model name.
// Mirrors src/utils/model/model.ts:getDefaultOpusModel.
func getDefaultOpusModel() string {
	if v := os.Getenv("ANTHROPIC_DEFAULT_OPUS_MODEL"); v != "" {
		return v
	}
	return "claude-3-opus-20240229"
}

// getDefaultSonnetModel returns the default Sonnet model name.
// Mirrors src/utils/model/model.ts:getDefaultSonnetModel.
func getDefaultSonnetModel() string {
	if v := os.Getenv("ANTHROPIC_DEFAULT_SONNET_MODEL"); v != "" {
		return v
	}
	return "claude-sonnet-4-20250514"
}

// doesMostRecentAssistantMessageExceed200k checks whether the most recent
// assistant message's estimated token usage exceeds 200,000 tokens.
// Mirrors src/utils/tokens.ts:doesMostRecentAssistantMessageExceed200k.
func doesMostRecentAssistantMessageExceed200k(messages []*schema.Message) bool {
	const threshold = 200000
	lastAsst := lastAssistantMessage(messages)
	if lastAsst == nil {
		return false
	}
	return compact.EstimateTokenCount([]*schema.Message{lastAsst}) > threshold
}

// stripSignatureBlocks removes provider-bound assistant state before replaying
// history to a different profile. Reasoning text and structured signatures are
// bound to the model/API key that generated them; message-level Extra may also
// contain an adapter's private self-generated marker. None of those values may
// authorize or reach a different route.
// Mirrors src/utils/messages.ts:stripSignatureBlocks.
func stripSignatureBlocks(messages []*schema.Message) []*schema.Message {
	changed := false
	result := make([]*schema.Message, len(messages))
	for i, msg := range messages {
		if msg == nil || msg.Role != schema.Assistant {
			result[i] = msg
			continue
		}
		hasReasoningPart := false
		for _, part := range msg.AssistantGenMultiContent {
			if part.Type == schema.ChatMessagePartTypeReasoning {
				hasReasoningPart = true
				break
			}
		}
		if msg.ReasoningContent == "" && !hasReasoningPart && len(msg.Extra) == 0 {
			result[i] = msg
			continue
		}
		// Clone the attempt-local message without private provider state. The
		// caller's canonical history remains byte-for-byte unchanged.
		clone := *msg
		clone.ReasoningContent = ""
		clone.Extra = nil
		if hasReasoningPart {
			clone.AssistantGenMultiContent = make(
				[]schema.MessageOutputPart,
				0,
				len(msg.AssistantGenMultiContent),
			)
			for _, part := range msg.AssistantGenMultiContent {
				if part.Type == schema.ChatMessagePartTypeReasoning {
					continue
				}
				clone.AssistantGenMultiContent = append(
					clone.AssistantGenMultiContent,
					part,
				)
			}
		}
		result[i] = &clone
		changed = true
	}
	if !changed {
		return messages
	}
	return result
}

// normalizeMessagesForAPI prepares the message list for API submission.
// Implements the full normalization pipeline ported from
// src/utils/messages.ts:normalizeMessagesForAPI:
//   - Filters nil entries and virtual (display-only) messages
//   - Converts system-role messages to user-role with "[system] " prefix
//   - Filters empty-content assistant messages
//   - Merges consecutive user messages
//   - Merges consecutive assistant messages
//   - Strips trailing thinking/reasoning from the last assistant message
//   - Ensures non-empty assistant content when ToolCalls are present
//   - Sanitizes error tool result content
func normalizeMessagesForAPI(messages []*schema.Message) []*schema.Message {
	if len(messages) == 0 {
		return messages
	}

	// hasSpecialSubtype returns true if a message carries metadata that should
	// prevent it from being merged with adjacent messages of the same role.
	hasSpecialSubtype := func(msg *schema.Message) bool {
		if msg == nil || msg.Extra == nil {
			return false
		}
		if _, ok := msg.Extra["subtype"]; ok {
			return true
		}
		if _, ok := msg.Extra["is_meta"]; ok {
			return true
		}
		return false
	}

	result := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		// Step 1: Filter nil messages.
		if msg == nil {
			continue
		}

		// Step 2: Filter virtual (display-only) messages.
		if msg.Extra != nil {
			if v, ok := msg.Extra["virtual"]; ok {
				if bv, isBool := v.(bool); isBool && bv {
					continue
				}
			}
		}

		// Step 3: Convert system-role messages to user-role with prefix.
		// Many APIs don't support system role mid-conversation.
		if msg.Role == schema.System {
			converted := &schema.Message{
				Role:    schema.User,
				Content: "[system] " + msg.Content,
				Extra:   msg.Extra,
			}
			// Merge with previous user message if present, but skip merging
			// when either message has special metadata (compact boundaries, etc.).
			if len(result) > 0 && result[len(result)-1].Role == schema.User &&
				!hasSpecialSubtype(result[len(result)-1]) && !hasSpecialSubtype(converted) {
				last := result[len(result)-1]
				merged := *last
				if merged.Content != "" && converted.Content != "" {
					merged.Content = merged.Content + "\n\n" + converted.Content
				} else if converted.Content != "" {
					merged.Content = converted.Content
				}
				result[len(result)-1] = &merged
			} else {
				result = append(result, converted)
			}
			continue
		}

		// Step 4: Filter assistant messages with no Content and no ToolCalls.
		// These are orphaned thinking-only messages left by compaction,
		// fallback retry, or stream interruptions. An assistant message
		// with only ReasoningContent is illegal for the API — stripping
		// reasoning in Step 7 would leave an empty message that causes:
		// "Invalid assistant message: content or tool_calls must be set".
		if msg.Role == schema.Assistant && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}

		// Step 5: Merge consecutive user messages (required by Bedrock,
		// harmless for 1P API which merges automatically).
		// Skip merging if either message has special metadata (e.g., compact boundaries).
		if msg.Role == schema.User && len(result) > 0 {
			last := result[len(result)-1]
			if last.Role == schema.User && !hasSpecialSubtype(last) && !hasSpecialSubtype(msg) {
				merged := *last
				if merged.Content != "" && msg.Content != "" {
					merged.Content = merged.Content + "\n\n" + msg.Content
				} else if msg.Content != "" {
					merged.Content = msg.Content
				}
				result[len(result)-1] = &merged
				continue
			}
		}

		// Step 6: Merge consecutive assistant messages.
		// Skip merging if either message has special metadata.
		if msg.Role == schema.Assistant && len(result) > 0 {
			last := result[len(result)-1]
			if last.Role == schema.Assistant && !hasSpecialSubtype(last) && !hasSpecialSubtype(msg) {
				merged := *last
				// Merge Content.
				if merged.Content != "" && msg.Content != "" {
					merged.Content = merged.Content + "\n\n" + msg.Content
				} else if msg.Content != "" {
					merged.Content = msg.Content
				}
				// Merge ToolCalls.
				if len(msg.ToolCalls) > 0 {
					merged.ToolCalls = append(merged.ToolCalls, msg.ToolCalls...)
				}
				// Merge ReasoningContent.
				if merged.ReasoningContent != "" && msg.ReasoningContent != "" {
					merged.ReasoningContent = merged.ReasoningContent + "\n\n" + msg.ReasoningContent
				} else if msg.ReasoningContent != "" {
					merged.ReasoningContent = msg.ReasoningContent
				}
				result[len(result)-1] = &merged
				continue
			}
		}

		result = append(result, msg)
	}

	// Step 7: Strip trailing thinking/reasoning from the last assistant message.
	// If the last assistant has ReasoningContent, strip it to avoid confusing
	// the next API call. Covers both cases: when Content is present (partial
	// thinking left from streaming) and when only thinking remains.
	if len(result) > 0 {
		last := result[len(result)-1]
		if last.Role == schema.Assistant && last.ReasoningContent != "" {
			clone := *last
			clone.ReasoningContent = ""
			result[len(result)-1] = &clone
		}
	}

	// Step 8: Ensure non-empty assistant content.
	// If an assistant message has ToolCalls but empty Content, set Content to
	// a single space (API requirement for some providers).
	for i, msg := range result {
		if msg.Role == schema.Assistant && msg.Content == "" && len(msg.ToolCalls) > 0 {
			clone := *msg
			clone.Content = " "
			result[i] = &clone
		}
	}

	// Step 9: Sanitize error tool results.
	// Tool messages with is_error=true that have empty Content should get
	// a placeholder to prevent API rejection.
	for i, msg := range result {
		if msg.Role == schema.Tool && msg.Extra != nil {
			if v, ok := msg.Extra["is_error"]; ok {
				if bv, isBool := v.(bool); isBool && bv {
					if strings.TrimSpace(msg.Content) == "" {
						clone := *msg
						clone.Content = "[Tool execution error]"
						result[i] = &clone
					}
				}
			}
		}
	}

	// Step 10: Ensure every assistant tool_call has a matching tool result.
	// After interruptions, compaction, or other message pipeline operations,
	// an assistant message with ToolCalls may lack corresponding tool result
	// messages. The API requires each tool_call_id to have a following tool
	// message. Synthesize placeholders for any that are missing.
	result = ensureToolCallResultPairing(result)

	return result
}

// ensureToolCallResultPairing checks that every tool call in assistant messages
// has a corresponding tool result message IMMEDIATELY following it (before any
// non-tool message). The API requires tool results to be adjacent to their
// assistant message. If any are missing or misplaced (due to interruption,
// compaction, message reordering, or other pipeline operations), synthetic
// placeholder results are inserted immediately after the assistant message's
// tool result group. Orphaned tool messages (those not adjacent to a matching
// assistant) are removed to prevent API rejection.
func ensureToolCallResultPairing(messages []*schema.Message) []*schema.Message {
	// Quick check: scan for any assistant with tool_calls that might need repair.
	hasToolCalls := false
	for _, msg := range messages {
		if msg != nil && msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			hasToolCalls = true
			break
		}
	}
	if !hasToolCalls {
		return messages
	}

	// Rebuild: adjacent tool results are consumed by the inner loop after each
	// assistant message. Any tool message encountered in the OUTER loop is
	// therefore non-adjacent (orphaned) and gets dropped.
	out := make([]*schema.Message, 0, len(messages)+4)
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}

		// Drop orphaned tool results — they are not adjacent to any assistant
		// with matching tool_calls. Adjacent tool results are consumed by the
		// inner loop below, so they never reach this check.
		if msg.Role == schema.Tool {
			continue
		}

		out = append(out, msg)

		if msg.Role != schema.Assistant || len(msg.ToolCalls) == 0 {
			continue
		}

		// Collect tool_call_ids that need results for this assistant.
		needed := make(map[string]string, len(msg.ToolCalls)) // id → function name
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				needed[tc.ID] = tc.Function.Name
			}
		}

		// Consume immediately adjacent tool result messages that belong to this
		// assistant. Streaming persistence can leave delayed or duplicate results
		// next to a later assistant message; forwarding those results makes the
		// provider see a tool_call_id that was never issued by that assistant.
		for i+1 < len(messages) && messages[i+1] != nil && messages[i+1].Role == schema.Tool {
			i++
			toolResult := messages[i]
			toolName, expected := needed[toolResult.ToolCallID]
			if !expected {
				continue
			}
			if toolResult.ToolName == "" && toolName != "" {
				clone := *toolResult
				clone.ToolName = toolName
				toolResult = &clone
			}
			out = append(out, toolResult)
			delete(needed, toolResult.ToolCallID)
		}

		// Synthesize placeholders for tool_calls not answered in the
		// immediate group (even if they exist elsewhere in the conversation).
		for _, tc := range msg.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if _, still := needed[tc.ID]; !still {
				continue
			}
			out = append(out, &schema.Message{
				Role:       schema.Tool,
				Content:    "[Tool result unavailable - execution was interrupted]",
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			})
			delete(needed, tc.ID)
		}
	}
	return out
}

// checkSleepRan returns true if any tool result in this turn was from the Sleep tool.
func checkSleepRan(toolResults []*schema.Message) bool {
	for _, msg := range toolResults {
		if msg != nil && msg.ToolName == "Sleep" {
			return true
		}
	}
	return false
}

// countSyntheticOutputCalls counts how many times the SyntheticOutput tool
// was called across all messages. Used for the structured output retry limit.
// Reference: QueryEngine.ts:1004-1010 (countToolCalls with SYNTHETIC_OUTPUT_TOOL_NAME)
func countSyntheticOutputCalls(messages []*schema.Message) int {
	count := 0
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "SyntheticOutput" {
				count++
			}
		}
	}
	return count
}
