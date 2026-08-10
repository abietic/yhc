package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type pairingCaptureModel struct {
	inputs [][]*schema.Message
}

func (m *pairingCaptureModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *pairingCaptureModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func writeTranscriptMessages(t *testing.T, dir, sessionID string, messages []*schema.Message) {
	t.Helper()
	rec := transcript.NewRecorder(sessionID, filepath.Join(dir, "transcripts"))
	if err := rec.Replace(messages); err != nil {
		t.Fatalf("replace transcript: %v", err)
		return
	}
	writeProjectGraphRootTestMetadata(t, rec, nil)
	if err := rec.Flush(); err != nil {
		t.Fatalf("flush transcript: %v", err)
		return
	}
}

func TestQueryEngineReloadRepairsMissingToolResult(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptMessages(t, dir, "session-pairing-missing", []*schema.Message{
		{Role: schema.User, Content: "run a command"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID:   "call_missing_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Bash",
				Arguments: `{"command":"pwd"}`,
			},
		}}},
	})

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session-pairing-missing",
		TranscriptDir: filepath.Join(dir, "transcripts"),
		CWD:           dir,
	})

	msgs := eng.GetMessages()
	// 4 messages: user, assistant+tool_call, synthesized tool result, continuation prompt
	if len(msgs) != 4 {
		t.Fatalf("expected missing tool result to be synthesized on reload with continuation prompt, got %#v", msgs)
	}
	if msgs[1].Role != schema.Assistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call to survive reload repair, got %#v", msgs[1])
	}
	if msgs[2].Role != schema.Tool || msgs[2].ToolCallID != "call_missing_1" {
		t.Fatalf("expected synthesized tool result for missing call, got %#v", msgs[2])
	}
	if msgs[2].ToolName != "Bash" {
		t.Fatalf("expected synthesized tool result to keep tool name, got %#v", msgs[2])
	}
	if msgs[2].Content != syntheticToolResultPlaceholder {
		t.Fatalf("expected synthetic placeholder content, got %q", msgs[2].Content)
	}
	if msgs[2].Extra == nil || msgs[2].Extra["is_error"] != true {
		t.Fatalf("expected synthesized tool result to be marked error, got %#v", msgs[2].Extra)
		return
	}
	// Continuation prompt injected because last real message was a tool result (interrupted turn)
	if msgs[3].Role != schema.User || msgs[3].Content != ContinuationPrompt {
		t.Fatalf("expected continuation prompt after interrupted turn, got %#v", msgs[3])
	}
}

func TestQueryEngineReloadStripsOrphanAndDuplicateToolResults(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptMessages(t, dir, "session-pairing-dedup", []*schema.Message{
		{Role: schema.Tool, ToolCallID: "call_orphan_1", ToolName: "Bash", Content: "orphan"},
		{Role: schema.User, Content: "run a command"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID:   "call_ok_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Bash",
				Arguments: `{"command":"echo hi"}`,
			},
		}}},
		{Role: schema.Tool, ToolCallID: "call_ok_1", ToolName: "Bash", Content: "ok"},
		{Role: schema.Tool, ToolCallID: "call_ok_1", ToolName: "Bash", Content: "duplicate"},
		{Role: schema.Tool, ToolCallID: "call_orphan_2", ToolName: "Bash", Content: "orphan"},
	})

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session-pairing-dedup",
		TranscriptDir: filepath.Join(dir, "transcripts"),
		CWD:           dir,
	})

	msgs := eng.GetMessages()
	// 4 messages: user, assistant+tool_call, tool_result, continuation prompt
	if len(msgs) != 4 {
		t.Fatalf("expected orphan and duplicate tool results to be stripped with continuation prompt, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[1].Role != schema.Assistant || msgs[2].Role != schema.Tool {
		t.Fatalf("unexpected repaired transcript shape: %#v", msgs)
	}
	if msgs[2].ToolCallID != "call_ok_1" || msgs[2].Content != "ok" {
		t.Fatalf("expected only the first matching tool result to survive, got %#v", msgs[2])
	}
	if msgs[3].Role != schema.User || msgs[3].Content != ContinuationPrompt {
		t.Fatalf("expected continuation prompt after interrupted turn, got %#v", msgs[3])
	}
}

func TestQueryEngineReloadDedupesAssistantToolCallsAndSynthesizesOneResult(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptMessages(t, dir, "session-pairing-assistant-dedup", []*schema.Message{
		{Role: schema.User, Content: "run a command"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{
				ID:   "call_same_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"pwd"}`,
				},
			},
			{
				ID:   "call_same_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"pwd"}`,
				},
			},
		}},
	})

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "session-pairing-assistant-dedup",
		TranscriptDir: filepath.Join(dir, "transcripts"),
		CWD:           dir,
	})

	msgs := eng.GetMessages()
	// 4 messages: user, assistant+tool_call(deduped), synthesized tool_result, continuation prompt
	if len(msgs) != 4 {
		t.Fatalf("expected duplicate tool use ids to collapse to one synthesized pair with continuation prompt, got %#v", msgs)
	}
	if len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool calls to dedupe to one entry, got %#v", msgs[1])
	}
	if msgs[2].ToolCallID != "call_same_1" {
		t.Fatalf("expected only one synthesized tool result for deduped tool call, got %#v", msgs[2])
	}
	if msgs[3].Role != schema.User || msgs[3].Content != ContinuationPrompt {
		t.Fatalf("expected continuation prompt after interrupted turn, got %#v", msgs[3])
	}
}

func TestQueryEngineNextTurnUsesReloadRepairedPairing(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptMessages(t, dir, "session-pairing-next-turn", []*schema.Message{
		{Role: schema.User, Content: "run a command"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID:   "call_resume_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Bash",
				Arguments: `{"command":"pwd"}`,
			},
		}}},
	})

	model := &pairingCaptureModel{}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-pairing-next-turn",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
	})

	events, _ := eng.SubmitMessage(context.Background(), "next")
	_ = drainEngineEvents(t, events)

	if len(model.inputs) != 1 {
		t.Fatalf("expected one captured model input, got %d", len(model.inputs))
	}
	input := model.inputs[0]
	// After normalizeMessagesForAPI, consecutive user messages (continuation_prompt + "next") are merged.
	// system + user("run a command") + assistant(tool_call) + tool(synthesized) + user(continuation+next)
	if len(input) != 5 {
		t.Fatalf("expected system prompt plus repaired history, continuation prompt, and new prompt, got %#v", input)
	}
	if input[0].Role != schema.System {
		t.Fatalf("expected system prompt first in captured model input, got %#v", input[0])
	}
	if input[1].Role != schema.User || input[1].Content != "run a command" {
		t.Fatalf("unexpected first repaired input message: %#v", input[1])
	}
	if input[2].Role != schema.Assistant || len(input[2].ToolCalls) != 1 {
		t.Fatalf("expected repaired assistant tool call before continuation, got %#v", input[2])
	}
	if input[3].Role != schema.Tool || input[3].ToolCallID != "call_resume_1" || input[3].Content != syntheticToolResultPlaceholder {
		t.Fatalf("expected repaired synthetic tool result before continuation, got %#v", input[3])
	}
	// Continuation prompt and "next" are merged into one user message by normalization.
	mergedContent := ContinuationPrompt + "\n\n" + "next"
	if input[4].Role != schema.User || input[4].Content != mergedContent {
		t.Fatalf("expected merged continuation+user prompt, got %#v", input[4])
	}
}

func TestMaxTurnsRepairsLiveHistoryBeforeNextTurn(t *testing.T) {
	history := newConversationHistory([]*schema.Message{{Role: schema.User, Content: "run it"}})
	history.Observe(QueryEvent{
		Type: EventAssistant,
		AssistantMessage: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"pwd"}`},
			}},
		},
	})
	history.Observe(QueryEvent{
		Type: EventToolResult,
		ToolResultMessage: &schema.Message{
			Role: schema.Tool, Content: "result without copied id", ToolName: "Bash",
		},
	})
	history.Observe(QueryEvent{
		Type:         EventMaxTurnsReached,
		MaxTurnsInfo: &MaxTurnsInfo{MaxTurns: 1, TurnCount: 2},
	})

	messages := history.Messages()
	if len(messages) != 4 {
		t.Fatalf("expected user, assistant, paired tool result, and max-turn marker; got %#v", messages)
	}
	if got := messages[1].ToolCalls[0].ID; got != "Bash" {
		t.Fatalf("normalized tool call ID = %q, want %q", got, "Bash")
	}
	if got := messages[2].ToolCallID; got != "Bash" {
		t.Fatalf("repaired tool result ID = %q, want %q", got, "Bash")
	}
	if messages[3].Extra["attachment_kind"] != "max_turns_reached" {
		t.Fatalf("expected max-turn marker after the repaired pair, got %#v", messages[3])
	}
}

func TestQueryEngineUsesDefaultMaxTurnsWhenUnset(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir()})
	if eng.config.MaxTurns != 0 {
		t.Fatalf("MaxTurns = %d, want unlimited (0)", eng.config.MaxTurns)
	}
}

func TestDetectTurnInterruptionEmpty(t *testing.T) {
	kind := detectTurnInterruption(nil)
	if kind != InterruptionNone {
		t.Fatalf("expected InterruptionNone for nil, got %d", kind)
	}
	kind = detectTurnInterruption([]*schema.Message{})
	if kind != InterruptionNone {
		t.Fatalf("expected InterruptionNone for empty, got %d", kind)
	}
}

func TestDetectTurnInterruptionAssistantLast(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hi"},
		{Role: schema.Assistant, Content: "hello"},
	}
	kind := detectTurnInterruption(msgs)
	if kind != InterruptionNone {
		t.Fatalf("expected InterruptionNone when assistant is last, got %d", kind)
	}
}

func TestDetectTurnInterruptionToolLast(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "run"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "tc1"}}},
		{Role: schema.Tool, Content: "result", ToolCallID: "tc1"},
	}
	kind := detectTurnInterruption(msgs)
	if kind != InterruptionTurn {
		t.Fatalf("expected InterruptionTurn when tool is last, got %d", kind)
	}
}

func TestDetectTurnInterruptionUserLastPlain(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.Assistant, Content: "reply"},
		{Role: schema.User, Content: "second"},
	}
	kind := detectTurnInterruption(msgs)
	if kind != InterruptionPrompt {
		t.Fatalf("expected InterruptionPrompt when plain user is last, got %d", kind)
	}
}

func TestDetectTurnInterruptionUserWithToolCallID(t *testing.T) {
	// A user message with ToolCallID is a synthetic repair result — treated as interrupted turn
	msgs := []*schema.Message{
		{Role: schema.User, Content: "synthetic", ToolCallID: "tc1"},
	}
	kind := detectTurnInterruption(msgs)
	if kind != InterruptionTurn {
		t.Fatalf("expected InterruptionTurn for user message with ToolCallID, got %d", kind)
	}
}

func TestRepairLoadedMessagesWithInterruptionInjectsContinuation(t *testing.T) {
	// Transcript ends with assistant tool call but no result → repair synthesizes result → InterruptionTurn
	messages := []*schema.Message{
		{Role: schema.User, Content: "do something"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID:       "tc1",
			Type:     "function",
			Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"pwd"}`},
		}}},
	}

	repaired, kind := repairLoadedMessagesWithInterruption(messages)
	if kind != InterruptionTurn {
		t.Fatalf("expected InterruptionTurn, got %d", kind)
	}
	// Should be: user, assistant, synthesized tool result, continuation prompt
	if len(repaired) != 4 {
		t.Fatalf("expected 4 messages after repair with continuation, got %d", len(repaired))
	}
	if repaired[3].Role != schema.User || repaired[3].Content != ContinuationPrompt {
		t.Fatalf("expected continuation prompt as last message, got %#v", repaired[3])
	}
}

func TestRepairLoadedMessagesWithInterruptionNoContinuationForCompletedTurn(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hi"},
		{Role: schema.Assistant, Content: "hello"},
	}

	repaired, kind := repairLoadedMessagesWithInterruption(messages)
	if kind != InterruptionNone {
		t.Fatalf("expected InterruptionNone, got %d", kind)
	}
	if len(repaired) != 2 {
		t.Fatalf("expected 2 messages (no continuation), got %d", len(repaired))
	}
}

func TestRepairLoadedMessagesWithInterruptionPromptNoInjection(t *testing.T) {
	// User sent a prompt but got no response — no continuation injected
	messages := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.Assistant, Content: "reply"},
		{Role: schema.User, Content: "second"},
	}

	repaired, kind := repairLoadedMessagesWithInterruption(messages)
	if kind != InterruptionPrompt {
		t.Fatalf("expected InterruptionPrompt, got %d", kind)
	}
	if len(repaired) != 3 {
		t.Fatalf("expected 3 messages (no continuation injected for prompt interruption), got %d", len(repaired))
	}
}

func TestFilterThinkingOnlyAssistantMessages(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, ReasoningContent: "thinking only", Content: ""},
		{Role: schema.Assistant, Content: "real response"},
	}

	filtered := filterThinkingOnlyAssistantMessages(messages)
	if len(filtered) != 2 {
		t.Fatalf("expected thinking-only message to be removed, got %d messages", len(filtered))
	}
	if filtered[0].Role != schema.User || filtered[0].Content != "hello" {
		t.Fatalf("unexpected first message: %#v", filtered[0])
	}
	if filtered[1].Role != schema.Assistant || filtered[1].Content != "real response" {
		t.Fatalf("unexpected second message: %#v", filtered[1])
	}
}

func TestFilterThinkingOnlyKeepsMessagesWithToolCalls(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: "thinking", Content: "", ToolCalls: []schema.ToolCall{{
			ID: "tc1", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: "{}"},
		}}},
	}

	filtered := filterThinkingOnlyAssistantMessages(messages)
	if len(filtered) != 1 {
		t.Fatalf("expected message with tool calls to be kept, got %d messages", len(filtered))
	}
}

func TestFilterThinkingOnlyKeepsMessagesWithVisibleContent(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: "thinking", Content: "also has visible text"},
	}

	filtered := filterThinkingOnlyAssistantMessages(messages)
	if len(filtered) != 1 {
		t.Fatalf("expected message with visible content to be kept, got %d messages", len(filtered))
	}
}

func TestFilterWhitespaceOnlyAssistantMessages(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "  \n\n  "},
		{Role: schema.Assistant, Content: "real response"},
	}

	filtered := filterWhitespaceOnlyAssistantMessages(messages)
	if len(filtered) != 2 {
		t.Fatalf("expected whitespace-only message to be removed, got %d messages", len(filtered))
	}
	if filtered[1].Content != "real response" {
		t.Fatalf("unexpected second message: %#v", filtered[1])
	}
}

func TestFilterWhitespaceOnlyKeepsEmptyContent(t *testing.T) {
	// Empty content string is NOT whitespace-only (it's truly empty, handled elsewhere)
	messages := []*schema.Message{
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{{
			ID: "tc1", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: "{}"},
		}}},
	}

	filtered := filterWhitespaceOnlyAssistantMessages(messages)
	if len(filtered) != 1 {
		t.Fatalf("expected empty-content message with tool calls to be kept, got %d messages", len(filtered))
	}
}

func TestFilterWhitespaceOnlyKeepsNonAssistant(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "   "},
	}

	filtered := filterWhitespaceOnlyAssistantMessages(messages)
	if len(filtered) != 1 {
		t.Fatalf("expected user message with whitespace to be kept, got %d messages", len(filtered))
	}
}

func TestRepairWithInterruptionFiltersThinkingAndWhitespace(t *testing.T) {
	// Transcript: user → thinking-only assistant → whitespace-only assistant → user prompt
	// After filtering both bad assistants, the last relevant message is the second user → InterruptionPrompt
	messages := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.Assistant, ReasoningContent: "thinking", Content: ""},
		{Role: schema.Assistant, Content: "\n\n"},
		{Role: schema.User, Content: "second"},
	}

	repaired, kind := repairLoadedMessagesWithInterruption(messages)
	if kind != InterruptionPrompt {
		t.Fatalf("expected InterruptionPrompt after filtering bad assistants, got %d", kind)
	}
	// Should be: first user, second user (bad assistants removed)
	if len(repaired) != 2 {
		t.Fatalf("expected 2 messages after filtering, got %d: %#v", len(repaired), repaired)
	}
	if repaired[0].Content != "first" || repaired[1].Content != "second" {
		t.Fatalf("unexpected messages after filtering: %#v", repaired)
	}
}
