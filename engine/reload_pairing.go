package engine

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	syntheticToolResultPlaceholder = "[Tool result missing due to internal error]"
	toolUseInterruptedPlaceholder  = "[Tool use interrupted]"

	// ContinuationPrompt is injected when a resumed session had an interrupted turn.
	// Mirrors reference conversationRecovery.ts:232.
	ContinuationPrompt = "Continue from where you left off. Do not repeat previous work."
)

// InterruptionKind classifies how a session ended before resume.
// Mirrors reference conversationRecovery.ts:272-290.
type InterruptionKind int

const (
	// InterruptionNone means the turn completed normally.
	InterruptionNone InterruptionKind = iota
	// InterruptionTurn means the model was mid-tool-execution when interrupted.
	InterruptionTurn
	// InterruptionPrompt means the user sent a prompt but got no response.
	InterruptionPrompt
)

func repairLoadedMessages(messages []*schema.Message) []*schema.Message {
	if len(messages) == 0 {
		return nil
	}

	repaired := make([]*schema.Message, 0, len(messages))
	seenToolUseIDs := make(map[string]struct{})
	pendingToolCalls := make([]schema.ToolCall, 0)
	pendingToolCallByID := make(map[string]schema.ToolCall)

	flushPending := func() {
		if len(pendingToolCallByID) == 0 {
			return
		}
		for _, toolCall := range pendingToolCalls {
			toolCallID := repairedToolCallID(toolCall)
			if toolCallID == "" {
				continue
			}
			pending, ok := pendingToolCallByID[toolCallID]
			if !ok {
				continue
			}
			repaired = append(repaired, newToolResultMessage(&pending, syntheticToolResultPlaceholder, true))
			delete(pendingToolCallByID, toolCallID)
		}
		pendingToolCalls = pendingToolCalls[:0]
	}

	for _, msg := range messages {
		if msg == nil {
			continue
		}

		if shouldFlushPendingToolResults(msg, pendingToolCallByID) {
			flushPending()
		}

		switch msg.Role {
		case schema.Assistant:
			assistant, toolCalls := repairAssistantToolCallsForReload(msg, seenToolUseIDs)
			if assistant != nil {
				repaired = append(repaired, assistant)
			}
			for _, toolCall := range toolCalls {
				toolCallID := repairedToolCallID(toolCall)
				if toolCallID == "" {
					continue
				}
				pendingToolCalls = append(pendingToolCalls, toolCall)
				pendingToolCallByID[toolCallID] = toolCall
			}
		case schema.Tool:
			toolResult, ok := repairToolResultForReload(msg, pendingToolCallByID)
			if ok {
				repaired = append(repaired, toolResult)
			}
		default:
			if msg.Role == schema.User &&
				len(msg.UserInputMultiContent) > 0 {
				repaired = append(repaired, msg)
			} else {
				repaired = append(repaired, cloneMessage(msg))
			}
		}
	}

	flushPending()
	return repaired
}

func shouldFlushPendingToolResults(msg *schema.Message, pending map[string]schema.ToolCall) bool {
	if msg == nil || len(pending) == 0 {
		return false
	}
	switch msg.Role {
	case schema.User, schema.Assistant, schema.System:
		return true
	default:
		return false
	}
}

func repairAssistantToolCallsForReload(msg *schema.Message, seenToolUseIDs map[string]struct{}) (*schema.Message, []schema.ToolCall) {
	if msg == nil {
		return nil, nil
	}

	cloned := cloneMessage(msg)
	if cloned == nil {
		return nil, nil
	}

	if len(cloned.ToolCalls) == 0 {
		if assistantMessageEmpty(cloned) {
			return nil, nil
		}
		return cloned, nil
	}

	keptToolCalls := make([]schema.ToolCall, 0, len(cloned.ToolCalls))
	seenLocal := make(map[string]struct{})
	removedToolCall := false

	for _, toolCall := range cloned.ToolCalls {
		toolCall = normalizeToolCallForReload(toolCall)
		toolCallID := repairedToolCallID(toolCall)
		if toolCallID == "" {
			removedToolCall = true
			continue
		}
		if _, ok := seenLocal[toolCallID]; ok {
			removedToolCall = true
			continue
		}
		if _, ok := seenToolUseIDs[toolCallID]; ok {
			removedToolCall = true
			continue
		}
		seenLocal[toolCallID] = struct{}{}
		seenToolUseIDs[toolCallID] = struct{}{}
		keptToolCalls = append(keptToolCalls, toolCall)
	}

	cloned.ToolCalls = keptToolCalls
	if removedToolCall && len(keptToolCalls) == 0 && assistantVisibleContentEmpty(cloned) {
		cloned.Content = toolUseInterruptedPlaceholder
	}
	if assistantMessageEmpty(cloned) {
		return nil, nil
	}
	return cloned, keptToolCalls
}

func repairToolResultForReload(msg *schema.Message, pending map[string]schema.ToolCall) (*schema.Message, bool) {
	if msg == nil {
		return nil, false
	}
	if msg.ToolCallID == "" {
		// Some interrupted streaming paths can produce a result before the
		// provider's tool-call ID has been copied onto it. Pair it only when
		// there is exactly one unambiguous pending call; otherwise discard it
		// and let the caller synthesize correctly identified results.
		if len(pending) != 1 {
			return nil, false
		}
		for toolCallID, toolCall := range pending {
			cloned := cloneMessage(msg)
			cloned.ToolCallID = toolCallID
			if cloned.ToolName == "" {
				cloned.ToolName = toolCall.Function.Name
			}
			delete(pending, toolCallID)
			return cloned, true
		}
	}
	toolCall, ok := pending[msg.ToolCallID]
	if !ok {
		return nil, false
	}
	delete(pending, msg.ToolCallID)

	cloned := cloneMessage(msg)
	if cloned.ToolName == "" {
		cloned.ToolName = toolCall.Function.Name
	}
	return cloned, true
}

func repairedToolCallID(toolCall schema.ToolCall) string {
	if toolCall.ID != "" {
		return toolCall.ID
	}
	return toolCall.Function.Name
}

func normalizeToolCallForReload(toolCall schema.ToolCall) schema.ToolCall {
	if toolCall.ID == "" {
		toolCall.ID = toolCall.Function.Name
	}
	if toolCall.Type == "" {
		toolCall.Type = "function"
	}
	if toolCall.Function.Arguments == "" {
		toolCall.Function.Arguments = "{}"
	}
	return toolCall
}

func assistantVisibleContentEmpty(msg *schema.Message) bool {
	if msg == nil {
		return true
	}
	return msg.Content == "" && msg.ReasoningContent == "" && len(msg.AssistantGenMultiContent) == 0
}

func assistantMessageEmpty(msg *schema.Message) bool {
	if msg == nil {
		return true
	}
	return assistantVisibleContentEmpty(msg) && len(msg.ToolCalls) == 0
}

func cloneMessage(msg *schema.Message) *schema.Message {
	if msg == nil {
		return nil
	}
	cloned := *msg
	if len(msg.MultiContent) > 0 {
		cloned.MultiContent = append([]schema.ChatMessagePart(nil), msg.MultiContent...) //nolint:staticcheck
	}
	if len(msg.UserInputMultiContent) > 0 {
		cloned.UserInputMultiContent = append([]schema.MessageInputPart(nil), msg.UserInputMultiContent...)
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		cloned.AssistantGenMultiContent = append([]schema.MessageOutputPart(nil), msg.AssistantGenMultiContent...)
	}
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
	}
	return &cloned
}

// detectTurnInterruption examines repaired messages and classifies the interruption type.
// Mirrors reference conversationRecovery.ts detectTurnInterruption.
//
// Logic:
//   - If last message is assistant → completed turn (no interruption)
//   - If last message is tool → interrupted turn (was mid-tool-execution)
//   - If last message is user → interrupted prompt (user sent but got no reply)
//   - Otherwise → no interruption
func detectTurnInterruption(messages []*schema.Message) InterruptionKind {
	if len(messages) == 0 {
		return InterruptionNone
	}

	// Walk backwards past nil messages
	var last *schema.Message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil {
			last = messages[i]
			break
		}
	}
	if last == nil {
		return InterruptionNone
	}

	switch last.Role {
	case schema.Assistant:
		// Completed turn — the model finished responding
		return InterruptionNone
	case schema.Tool:
		// Tool result is last → the model issued tool calls, results came back,
		// but the model never got to continue. This is an interrupted turn.
		return InterruptionTurn
	case schema.User:
		// User message is last → the user sent a prompt but got no response.
		// Check if it's a tool_result (has ToolCallID) or plain text.
		if last.ToolCallID != "" {
			// Synthetic tool result from repair → interrupted turn
			return InterruptionTurn
		}
		return InterruptionPrompt
	default:
		return InterruptionNone
	}
}

// filterThinkingOnlyAssistantMessages removes assistant messages that contain only
// reasoning/thinking content with no visible text or tool calls. These can occur when
// streaming yields separate messages per content block and interleaved user messages
// prevent proper merging by message.id.
// Mirrors reference messages.ts filterOrphanedThinkingOnlyMessages.
func filterThinkingOnlyAssistantMessages(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Assistant &&
			msg.Content == "" &&
			len(msg.ToolCalls) == 0 &&
			len(msg.AssistantGenMultiContent) == 0 &&
			msg.ReasoningContent != "" {
			// Orphaned thinking-only message — skip it
			continue
		}
		result = append(result, msg)
	}
	return result
}

// filterWhitespaceOnlyAssistantMessages removes assistant messages where the visible
// content is only whitespace. This can happen when the model outputs "\n\n" before
// thinking and the user cancels mid-stream.
// Mirrors reference messages.ts filterWhitespaceOnlyAssistantMessages.
func filterWhitespaceOnlyAssistantMessages(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Assistant &&
			len(msg.ToolCalls) == 0 &&
			len(msg.AssistantGenMultiContent) == 0 &&
			msg.ReasoningContent == "" &&
			msg.Content != "" &&
			strings.TrimSpace(msg.Content) == "" {
			// Whitespace-only assistant message — skip it
			continue
		}
		result = append(result, msg)
	}
	return result
}

// repairLoadedMessagesWithInterruption repairs messages and handles interruption state.
// Returns the repaired messages (with continuation prompt appended if interrupted_turn).
// Mirrors the reference flow: repair → filter thinking → filter whitespace → detect → inject continuation.
func repairLoadedMessagesWithInterruption(messages []*schema.Message) ([]*schema.Message, InterruptionKind) {
	repaired := repairLoadedMessages(messages)
	repaired = filterThinkingOnlyAssistantMessages(repaired)
	repaired = filterWhitespaceOnlyAssistantMessages(repaired)
	kind := detectTurnInterruption(repaired)

	switch kind {
	case InterruptionTurn:
		// Mid-tool-execution: append a continuation user message so the model resumes.
		repaired = append(repaired, &schema.Message{
			Role:    schema.User,
			Content: ContinuationPrompt,
		})
	case InterruptionPrompt:
		// The user's message is already there — the model just never responded.
		// No action needed; the next SubmitMessage will naturally continue.
	}

	return repaired, kind
}
