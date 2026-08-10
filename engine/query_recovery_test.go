package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type scriptedRecoveryModel struct {
	streams         [][]*schema.Message
	inputs          [][]*schema.Message
	maxOutputTokens []int
	callCount       int
}

func (m *scriptedRecoveryModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *scriptedRecoveryModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	copied := append([]*schema.Message(nil), input...)
	m.inputs = append(m.inputs, copied)
	common := model.GetCommonOptions(nil, opts...)
	maxTokens := 0
	if common.MaxTokens != nil {
		maxTokens = *common.MaxTokens
	}
	m.maxOutputTokens = append(m.maxOutputTokens, maxTokens)

	idx := m.callCount
	m.callCount++
	if idx >= len(m.streams) {
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
	}
	return schema.StreamReaderFromArray(m.streams[idx]), nil
}

func apiErrorMessage(errorType, content string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		Extra: map[string]any{
			"api_error":  true,
			"error_type": errorType,
		},
	}
}

func assistantEventMessage(evt QueryEvent) *schema.Message {
	if evt.AssistantMessage != nil {
		return evt.AssistantMessage
	}
	if evt.Type == EventAssistant {
		return evt.Message
	}
	return nil
}

func lastAssistantEvent(events []QueryEvent) (QueryEvent, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == EventAssistant {
			return events[index], true
		}
	}
	return QueryEvent{}, false
}

func TestQueryBlockingLimitReturnsPreflightError(t *testing.T) {
	ctx := context.Background()
	model := &scriptedRecoveryModel{}
	stopCalled := false
	hookExec := hooks.NewExecutor()
	hookExec.RegisterStop(func(messagesForQuery, assistantMessages []*schema.Message, stopHookActive bool) *hooks.StopHookResult {
		stopCalled = true
		return nil
	})

	largePrompt := strings.Repeat("x", 800000)
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: largePrompt}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "claude-test",
		}},
	})

	if terminal.Reason != TerminalBlockingLimit {
		t.Fatalf("expected blocking limit terminal, got %q", terminal.Reason)
	}
	if model.callCount != 0 {
		t.Fatalf("expected model to be skipped at blocking limit, got %d calls", model.callCount)
	}
	if stopCalled {
		t.Fatal("expected blocking limit to return before stop hooks")
	}
	if len(events) < 2 {
		t.Fatalf("expected preflight events, got %d", len(events))
	}
	last, ok := lastAssistantEvent(events)
	lastMsg := assistantEventMessage(last)
	if !ok || lastMsg == nil {
		t.Fatalf("expected final assistant api error event, got %#v", last)
		return
	}
	if lastMsg.Content != "Prompt is too long" {
		t.Fatalf("unexpected blocking limit error content: %q", lastMsg.Content)
	}
	if lastMsg.Extra == nil || lastMsg.Extra["api_error"] != true {
		t.Fatalf("expected api_error assistant message, got %#v", lastMsg.Extra)
		return
	}
}

func TestQueryMaxOutputTokensEscalatesThenInjectsRecoveryMessage(t *testing.T) {
	ctx := context.Background()
	model := &scriptedRecoveryModel{streams: [][]*schema.Message{
		{apiErrorMessage("max_output_tokens", "first limit")},
		{apiErrorMessage("max_output_tokens", "second limit")},
		{{Role: schema.Assistant, Content: "finished"}},
	}}
	maxTurns := 6

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "do the work"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "claude-test",
		}},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if model.callCount != 3 {
		t.Fatalf("expected 3 model calls (escalate, recover, finish), got %d", model.callCount)
	}
	if len(model.inputs) != 3 {
		t.Fatalf("expected 3 captured inputs, got %d", len(model.inputs))
	}
	if got := model.maxOutputTokens; len(got) != 3 || got[0] != 0 || got[1] != 64000 || got[2] != 0 {
		t.Fatalf("max-output escalation sequence = %#v, want [0 64000 0]", got)
	}
	if got := model.inputs[1][len(model.inputs[1])-1]; got.Extra != nil && got.Extra["is_meta"] == true {
		t.Fatal("did not expect recovery meta message on escalated retry")
		return
	}
	thirdInput := model.inputs[2][len(model.inputs[2])-1]
	if thirdInput.Role != schema.User {
		t.Fatalf("expected recovery message to be a user meta message, got role %q", thirdInput.Role)
	}
	if thirdInput.Extra == nil || thirdInput.Extra["is_meta"] != true {
		t.Fatalf("expected recovery message to be marked meta, got %#v", thirdInput.Extra)
		return
	}
	if !strings.Contains(thirdInput.Content, "Output token limit hit") {
		t.Fatalf("unexpected recovery prompt: %q", thirdInput.Content)
	}
	last, ok := lastAssistantEvent(events)
	if !ok {
		t.Fatalf("expected final assistant success event, got %#v", events)
	}
	finalAssistant := assistantEventMessage(last)
	if finalAssistant == nil || finalAssistant.Content != "finished" {
		t.Fatalf("expected final assistant success payload, got %#v", finalAssistant)
		return
	}
}

func TestQueryMaxOutputTokensExhaustionSurfacesErrorAndCompletes(t *testing.T) {
	ctx := context.Background()
	model := &scriptedRecoveryModel{streams: [][]*schema.Message{
		{apiErrorMessage("max_output_tokens", "limit 1")},
		{apiErrorMessage("max_output_tokens", "limit 2")},
		{apiErrorMessage("max_output_tokens", "limit 3")},
		{apiErrorMessage("max_output_tokens", "limit 4")},
		{apiErrorMessage("max_output_tokens", "limit 5")},
	}}
	maxTurns := 8
	hookExec := hooks.NewExecutor()
	stopFailureCalled := make(chan *schema.Message, 1)
	hookExec.RegisterStopFailure(func(lastMessage *schema.Message) {
		stopFailureCalled <- lastMessage
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "keep going"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "claude-test",
		}},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal after surfaced api error, got %q", terminal.Reason)
	}
	if model.callCount != 5 {
		t.Fatalf("expected 5 model calls through recovery exhaustion, got %d", model.callCount)
	}
	if got := model.maxOutputTokens; len(got) != 5 || got[0] != 0 || got[1] != 64000 || got[2] != 0 || got[3] != 0 || got[4] != 0 {
		t.Fatalf("bounded max-output sequence = %#v", got)
	}
	var surfaced *schema.Message
	for _, evt := range events {
		msg := assistantEventMessage(evt)
		if msg != nil && msg.Extra != nil && msg.Extra["api_error"] == true {
			surfaced = msg
		}
	}
	if surfaced == nil {
		t.Fatal("expected surfaced api error after recovery exhaustion")
		return
	}
	if surfaced.Content != "limit 5" {
		t.Fatalf("expected final exhausted error to surface, got %q", surfaced.Content)
	}
	select {
	case got := <-stopFailureCalled:
		if got == nil || got.Content != "limit 5" {
			t.Fatalf("unexpected stop-failure payload: %#v", got)
			return
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected stop-failure hook to fire for exhausted max_output_tokens")
	}
}

func TestQueryGenericAPIErrorSkipsStopHooksAndCompletes(t *testing.T) {
	ctx := context.Background()
	model := &scriptedRecoveryModel{streams: [][]*schema.Message{{apiErrorMessage("general", "upstream failed")}}}
	maxTurns := 3
	stopCalled := false
	hookExec := hooks.NewExecutor()
	hookExec.RegisterStop(func(messagesForQuery, assistantMessages []*schema.Message, stopHookActive bool) *hooks.StopHookResult {
		stopCalled = true
		return nil
	})
	stopFailureCalled := make(chan *schema.Message, 1)
	hookExec.RegisterStopFailure(func(lastMessage *schema.Message) {
		stopFailureCalled <- lastMessage
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if stopCalled {
		t.Fatal("expected stop hooks to be skipped for api error assistant message")
	}
	var surfaced *schema.Message
	for _, evt := range events {
		if msg := assistantEventMessage(evt); msg != nil {
			surfaced = msg
		}
	}
	if surfaced == nil || surfaced.Content != "upstream failed" {
		t.Fatalf("expected surfaced api error assistant message, got %#v", surfaced)
		return
	}
	select {
	case got := <-stopFailureCalled:
		if got == nil || got.Content != "upstream failed" {
			t.Fatalf("unexpected stop-failure payload: %#v", got)
			return
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected stop-failure hook to fire for generic api error")
	}
}
