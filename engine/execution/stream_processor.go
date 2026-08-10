package execution

import (
	"context"
	"errors"
	"io"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/messages"
)

// StreamResult holds the result of processing a model stream.
// Mirrors query.ts:551-557.
type StreamResult struct {
	AssistantMessages   []*schema.Message
	ToolUseBlocks       []*schema.ToolCall
	ToolResults         []*schema.Message
	NeedsFollowUp       bool
	ToolCallsCommitted  bool
	PreventContinuation bool
	Withheld            *schema.Message
	WithheldReason      string // "413", "max_output_tokens", "media_size"
}

// ProcessStream reads the model's streaming output, yielding events and
// collecting a finalized assistant message for history/tool execution.
//
// Important: streamed chunks are display events, not durable transcript
// messages. Tool-call chunks in particular may arrive incrementally; if we
// treat every chunk as a standalone assistant message we can execute partial
// tool calls (for example with empty arguments) and then send malformed
// assistant/tool-result history back to the model on the follow-up turn.
// Mirrors query.ts:826-863.
func ProcessStream(
	ctx context.Context,
	sr *schema.StreamReader[*schema.Message],
	executor *StreamingToolExecutor,
	yieldFn func(QueryEvent),
) (*StreamResult, error) {
	result := &StreamResult{}
	accumulated := &schema.Message{Role: schema.Assistant}
	toolOrder := make([]string, 0)
	toolIndex := make(map[string]int)
	cleanEOF := false
	var streamErr error

	for {
		msg, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			cleanEOF = true
			break
		}
		if err != nil {
			streamErr = err
			break
		}

		if msg == nil {
			continue
		}
		if isAPIError(msg) {
			result.Withheld = msg
			result.WithheldReason = classifyError(msg)
			continue
		}

		mergeAssistantChunk(accumulated, msg, &toolOrder, toolIndex)
		if executor != nil {
			for _, tc := range msg.ToolCalls {
				toolCall := tc
				executor.AddTool(&toolCall, msg)
			}
		}

		yieldFn(QueryEvent{Type: QueryEventType("assistant"), Message: msg})

		if executor != nil {
			for _, completed := range executor.GetCompleted() {
				appendCompletedToolResult(result, completed, yieldFn)
			}
		}

	}

	if accumulated.Content != "" || accumulated.ReasoningContent != "" || len(accumulated.AssistantGenMultiContent) > 0 || len(accumulated.ToolCalls) > 0 {
		if merged, err := messages.ConcatAssistantOutputParts(accumulated.AssistantGenMultiContent); err == nil {
			accumulated.AssistantGenMultiContent = merged
		}
		result.AssistantMessages = append(result.AssistantMessages, accumulated)
		if len(accumulated.ToolCalls) > 0 {
			result.NeedsFollowUp = true
			result.ToolUseBlocks = make([]*schema.ToolCall, 0, len(accumulated.ToolCalls))
			for i := range accumulated.ToolCalls {
				result.ToolUseBlocks = append(result.ToolUseBlocks, &accumulated.ToolCalls[i])
			}
		}
	}
	if result.Withheld != nil {
		result.NeedsFollowUp = false
	}

	decision := classifyStreamTerminal(
		assistantFinishReason(accumulated),
		result.Withheld != nil,
		result.WithheldReason,
		cleanEOF,
		streamErr,
		ctx.Err(),
	)
	if executor != nil {
		switch decision.Disposition {
		case streamTerminalCommit:
			if !executor.commit(ctx) && ctx.Err() != nil {
				decision = streamTerminalDecision{
					Disposition: streamTerminalCancel,
					Err:         ctx.Err(),
				}
			}
		case streamTerminalRejectTruncated, streamTerminalCancel, streamTerminalModelError:
			executor.rejectPending(streamTerminalToolError(decision))
		}
		for _, completed := range executor.GetCompleted() {
			appendCompletedToolResult(result, completed, yieldFn)
		}
	}
	result.ToolCallsCommitted = decision.Disposition == streamTerminalCommit
	if decision.Disposition == streamTerminalModelError && result.Withheld == nil {
		return nil, decision.Err
	}

	return result, nil
}

func assistantFinishReason(message *schema.Message) string {
	if message == nil || message.ResponseMeta == nil {
		return ""
	}
	return message.ResponseMeta.FinishReason
}

func appendCompletedToolResult(result *StreamResult, completed *ToolResult, yieldFn func(QueryEvent)) {
	if result == nil || completed == nil {
		return
	}
	for _, msg := range completed.BeforeMessages {
		if msg == nil {
			continue
		}
		yieldFn(QueryEvent{Type: QueryEventType("attachment"), Message: msg})
		result.ToolResults = append(result.ToolResults, msg)
	}
	if completed.Message != nil {
		yieldFn(QueryEvent{Type: QueryEventType("tool_result"), Message: completed.Message})
		result.ToolResults = append(result.ToolResults, completed.Message)
	}
	for _, msg := range completed.AfterMessages {
		if msg == nil {
			continue
		}
		yieldFn(QueryEvent{Type: QueryEventType("attachment"), Message: msg})
		result.ToolResults = append(result.ToolResults, msg)
	}
	if completed.PreventContinuation {
		result.PreventContinuation = true
	}
}

func mergeAssistantChunk(dst, chunk *schema.Message, toolOrder *[]string, toolIndex map[string]int) {
	if dst == nil || chunk == nil {
		return
	}
	if chunk.Role != "" {
		dst.Role = chunk.Role
	}
	if chunk.Content != "" {
		dst.Content += chunk.Content
	}
	if chunk.ReasoningContent != "" {
		dst.ReasoningContent += chunk.ReasoningContent
	}
	if len(chunk.AssistantGenMultiContent) > 0 {
		dst.AssistantGenMultiContent = append(dst.AssistantGenMultiContent, chunk.AssistantGenMultiContent...)
	}
	mergeAssistantResponseMeta(dst, chunk)
	for _, tc := range chunk.ToolCalls {
		mergeToolCall(dst, tc, toolOrder, toolIndex)
	}
}

func mergeAssistantResponseMeta(dst, chunk *schema.Message) {
	if dst == nil || chunk == nil || chunk.ResponseMeta == nil {
		return
	}
	if dst.ResponseMeta == nil {
		dst.ResponseMeta = &schema.ResponseMeta{}
	}
	if chunk.ResponseMeta.FinishReason != "" {
		dst.ResponseMeta.FinishReason = chunk.ResponseMeta.FinishReason
	}
	usage := chunk.ResponseMeta.Usage
	if usage == nil {
		return
	}
	if dst.ResponseMeta.Usage == nil {
		copied := *usage
		dst.ResponseMeta.Usage = &copied
		return
	}
	current := dst.ResponseMeta.Usage
	current.PromptTokens = max(current.PromptTokens, usage.PromptTokens)
	current.CompletionTokens = max(current.CompletionTokens, usage.CompletionTokens)
	current.TotalTokens = max(current.TotalTokens, usage.TotalTokens)
	current.PromptTokenDetails.CachedTokens = max(current.PromptTokenDetails.CachedTokens, usage.PromptTokenDetails.CachedTokens)
	current.CompletionTokensDetails.ReasoningTokens = max(
		current.CompletionTokensDetails.ReasoningTokens,
		usage.CompletionTokensDetails.ReasoningTokens,
	)
}

func mergeToolCall(dst *schema.Message, incoming schema.ToolCall, toolOrder *[]string, toolIndex map[string]int) {
	key := toolCallKey(incoming)
	if key == "" {
		return
	}

	if idx, ok := toolIndex[key]; ok {
		existing := dst.ToolCalls[idx]
		if incoming.ID != "" {
			existing.ID = incoming.ID
		}
		if incoming.Type != "" {
			existing.Type = incoming.Type
		}
		if incoming.Function.Name != "" {
			existing.Function.Name = incoming.Function.Name
		}
		if incoming.Function.Arguments != "" && incoming.Function.Arguments != "{}" {
			existing.Function.Arguments = incoming.Function.Arguments
		} else if existing.Function.Arguments == "" {
			existing.Function.Arguments = incoming.Function.Arguments
		}
		if existing.ID == "" {
			existing.ID = existing.Function.Name
		}
		if existing.Type == "" {
			existing.Type = "function"
		}
		if existing.Function.Arguments == "" {
			existing.Function.Arguments = "{}"
		}
		dst.ToolCalls[idx] = existing
		return
	}

	if incoming.ID == "" {
		incoming.ID = incoming.Function.Name
	}
	if incoming.Type == "" {
		incoming.Type = "function"
	}
	if incoming.Function.Arguments == "" {
		incoming.Function.Arguments = "{}"
	}
	toolIndex[key] = len(dst.ToolCalls)
	*toolOrder = append(*toolOrder, key)
	dst.ToolCalls = append(dst.ToolCalls, incoming)
}

func toolCallKey(tc schema.ToolCall) string {
	if tc.ID != "" {
		return tc.ID
	}
	if tc.Function.Name != "" {
		return "name:" + tc.Function.Name
	}
	return ""
}

// QueryEventType is the event type string.
type QueryEventType string

// QueryEvent is a generic event wrapper.
type QueryEvent struct {
	Type    QueryEventType
	Message *schema.Message
}

func isAPIError(msg *schema.Message) bool {
	if msg.Extra == nil {
		return false
	}
	_, ok := msg.Extra["api_error"]
	return ok
}

func classifyError(msg *schema.Message) string {
	if msg.Extra == nil {
		return ""
	}
	if t, ok := msg.Extra["error_type"].(string); ok {
		return t
	}
	return ""
}
