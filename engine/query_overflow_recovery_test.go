package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type scriptedOverflowModel struct {
	streams   [][]*schema.Message
	inputs    [][]*schema.Message
	callCount int
}

func (m *scriptedOverflowModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *scriptedOverflowModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	copied := append([]*schema.Message(nil), input...)
	m.inputs = append(m.inputs, copied)
	idx := m.callCount
	m.callCount++
	if idx >= len(m.streams) {
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
	}
	return schema.StreamReaderFromArray(m.streams[idx]), nil
}

func overflowAPIError(errorType, content string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		Extra: map[string]any{
			"api_error":  true,
			"error_type": errorType,
		},
	}
}

func TestQueryRecoveryCascadeRunsPreparationAndOverflowStagesInOrder(t *testing.T) {
	ctx := context.Background()
	model := &scriptedOverflowModel{streams: [][]*schema.Message{
		{overflowAPIError("413", "Prompt is too long")},
		{overflowAPIError("413", "Prompt still too long after drain")},
		{{Role: schema.Assistant, Content: "resumed after recovery"}},
	}}
	messages := make([]*schema.Message, 0, 41)
	for i := 0; i < 40; i++ {
		role := schema.User
		if i%2 == 1 {
			role = schema.Assistant
		}
		messages = append(messages, &schema.Message{Role: role, Content: "history"})
	}
	messages[1].ReasoningContent = strings.Repeat("reasoning ", 1200)
	messages = append(messages, &schema.Message{Role: schema.User, Content: "latest question"})
	maxTurns := 6

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     messages,
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "claude-test",
		}},
	})

	if terminal.Reason != TerminalCompleted || model.callCount != 3 {
		t.Fatalf("terminal = %q, model calls = %d", terminal.Reason, model.callCount)
	}
	if !strings.Contains(model.inputs[0][2].ReasoningContent, "reasoning truncated") {
		t.Fatalf("initial API input was not snipped before the first call: %#v", model.inputs[0][2])
	}
	if !hasMessageSubtype(model.inputs[1], "collapse_staged") {
		t.Fatalf("second API input did not contain collapse drain: %#v", model.inputs[1])
	}
	if !hasMessageSubtype(model.inputs[2], "compact_boundary") || !hasMessageSubtype(model.inputs[2], "compact_summary") {
		t.Fatalf("third API input did not contain reactive compact output: %#v", model.inputs[2])
	}

	boundarySubtypes := make([]string, 0, 3)
	for _, evt := range events {
		if evt.Type != EventCompactBoundary || evt.CompactBoundaryMessage == nil || evt.CompactBoundaryMessage.Extra == nil {
			continue
		}
		if subtype, _ := evt.CompactBoundaryMessage.Extra["subtype"].(string); subtype != "" {
			boundarySubtypes = append(boundarySubtypes, subtype)
		}
	}
	if len(boundarySubtypes) == 0 || boundarySubtypes[0] != "snip_boundary" {
		t.Fatalf("expected snip boundary before overflow recovery events, got %#v", boundarySubtypes)
	}
	if messages[1].ReasoningContent != strings.Repeat("reasoning ", 1200) {
		t.Fatal("query preparation mutated the original history")
	}
}

func TestQueryPreparationMicrocompactsBelowSnipThreshold(t *testing.T) {
	model := &scriptedOverflowModel{}
	messages := []*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer", ReasoningContent: strings.Repeat("thought ", 1200)},
		{Role: schema.User, Content: "continue"},
	}

	events, terminal := collectEvents(context.Background(), QueryParams{
		Messages:     messages,
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		QuerySource:  QuerySourceSDK,
		ChatModel:    model,
	})
	if terminal.Reason != TerminalCompleted || model.callCount != 1 {
		t.Fatalf("terminal = %q, model calls = %d", terminal.Reason, model.callCount)
	}
	if !strings.Contains(model.inputs[0][2].ReasoningContent, "reasoning truncated") {
		t.Fatalf("expected pre-request microcompact, got %#v", model.inputs[0])
	}
	for _, evt := range events {
		if evt.Type == EventCompactBoundary && evt.CompactBoundaryMessage != nil && evt.CompactBoundaryMessage.Extra["subtype"] == "snip_boundary" {
			t.Fatal("did not expect snip below the history threshold")
		}
	}
	if messages[1].ReasoningContent != strings.Repeat("thought ", 1200) {
		t.Fatal("microcompact mutated the original message")
	}
}

func hasMessageSubtype(messages []*schema.Message, want string) bool {
	for _, msg := range messages {
		if msg != nil && msg.Extra != nil && msg.Extra["subtype"] == want {
			return true
		}
	}
	return false
}

func TestQueryPromptTooLongRetriesWithReactiveCompact(t *testing.T) {
	ctx := context.Background()
	model := &scriptedOverflowModel{streams: [][]*schema.Message{
		{overflowAPIError("413", "Prompt is too long")},
		{overflowAPIError("413", "Prompt still too long after drain")},
		{{Role: schema.Assistant, Content: "resumed after compact"}},
	}}
	maxTurns := 5

	var events []QueryEvent
	terminal := Query(ctx, QueryParams{
		Messages: []*schema.Message{
			{Role: schema.User, Content: strings.Repeat("old context ", 2000)},
			{Role: schema.Assistant, Content: "older answer"},
			{Role: schema.User, Content: "latest question"},
		},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "claude-test",
		}},
	}, func(evt QueryEvent) {
		events = append(events, evt)
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if model.callCount != 3 {
		t.Fatalf("expected drain retry then reactive compact retry, got %d model calls", model.callCount)
	}
	if len(model.inputs) != 3 {
		t.Fatalf("expected 3 captured inputs, got %d", len(model.inputs))
	}
	drainInput := model.inputs[1]
	foundCollapseStaged := false
	for _, msg := range drainInput {
		if msg != nil && msg.Extra != nil && msg.Extra["subtype"] == "collapse_staged" {
			foundCollapseStaged = true
		}
	}
	if !foundCollapseStaged {
		t.Fatalf("expected collapse drain summary in first retry input, got %#v", drainInput)
	}
	secondInput := model.inputs[2]
	if len(secondInput) < 3 {
		t.Fatalf("expected reactively compacted retry input, got %#v", secondInput)
	}
	foundBoundary := false
	foundSummary := false
	for _, msg := range secondInput {
		if msg == nil || msg.Extra == nil {
			continue
		}
		if msg.Extra["subtype"] == "compact_boundary" {
			foundBoundary = true
		}
		if msg.Extra["subtype"] == "compact_summary" {
			foundSummary = true
		}
	}
	if !foundBoundary || !foundSummary {
		t.Fatalf("expected reactive compact boundary and summary in retry input, got %#v", secondInput)
	}
	var compactBoundaryEvent bool
	for _, evt := range events {
		if evt.Type == EventCompactBoundary && evt.CompactBoundaryMessage != nil && evt.CompactBoundaryMessage.Content == "Prompt is too long" {
			compactBoundaryEvent = true
		}
	}
	if !compactBoundaryEvent {
		t.Fatal("expected compact boundary event for withheld prompt-too-long")
	}
}

func TestQueryPromptTooLongSecondFailureSurfacesTerminalError(t *testing.T) {
	ctx := context.Background()
	model := &scriptedOverflowModel{streams: [][]*schema.Message{
		{overflowAPIError("413", "Prompt is too long")},
		{overflowAPIError("413", "Prompt still too long after drain")},
		{overflowAPIError("413", "Prompt is still too long")},
	}}
	maxTurns := 5
	hookExec := hooks.NewExecutor()
	stopFailureCalled := make(chan *schema.Message, 1)
	hookExec.RegisterStopFailure(func(lastMessage *schema.Message) {
		stopFailureCalled <- lastMessage
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages: []*schema.Message{
			{Role: schema.User, Content: strings.Repeat("older prompt ", 600)},
			{Role: schema.Assistant, Content: "older answer"},
			{Role: schema.User, Content: strings.Repeat("big prompt ", 1500)},
		},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "claude-test",
		}},
	})

	if terminal.Reason != TerminalPromptTooLong {
		t.Fatalf("expected prompt_too_long terminal, got %q", terminal.Reason)
	}
	if model.callCount != 3 {
		t.Fatalf("expected drain retry and reactive compact retry before surfacing prompt-too-long, got %d calls", model.callCount)
	}
	var surfaced *schema.Message
	for _, evt := range events {
		if msg := assistantEventMessage(evt); msg != nil && strings.Contains(msg.Content, "still too long") {
			surfaced = msg
		}
	}
	if surfaced == nil {
		t.Fatal("expected final prompt-too-long error to surface")
		return
	}
	select {
	case got := <-stopFailureCalled:
		if got == nil || !strings.Contains(got.Content, "still too long") {
			t.Fatalf("unexpected stop-failure payload: %#v", got)
			return
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected stop-failure hook for surfaced prompt-too-long")
	}
}

func TestQueryMediaSizeWithoutExactBindingReturnsRedactedImageError(
	t *testing.T,
) {
	ctx := context.Background()
	model := &scriptedOverflowModel{streams: [][]*schema.Message{
		{overflowAPIError("media_size", "media too large")},
		{{Role: schema.Assistant, Content: "continued without media"}},
	}}
	maxTurns := 5
	hookExec := hooks.NewExecutor()
	stopFailureCalled := make(chan *schema.Message, 2)
	hookExec.RegisterStopFailure(func(lastMessage *schema.Message) {
		stopFailureCalled <- lastMessage
	})

	userWithMedia := &schema.Message{
		Role:    schema.User,
		Content: "Please inspect these files",
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "Please inspect these files"},
			{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{}},
		},
	}
	unrelated := &schema.Message{Role: schema.Assistant, Content: "keep me", Extra: map[string]any{"stable": true}}

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{unrelated, userWithMedia},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
	})

	if terminal.Reason != TerminalImageError {
		t.Fatalf("expected image_error terminal, got %q", terminal.Reason)
	}
	if model.callCount != 1 {
		t.Fatalf("expected one model call without media retry, got %d", model.callCount)
	}
	var redactedCount int
	for _, evt := range events {
		if evt.Type == EventCompactBoundary {
			t.Fatalf("media failure emitted compact boundary: %#v", evt)
		}
		if msg := assistantEventMessage(evt); msg != nil {
			if msg.Content == "continued without media" {
				t.Fatal("media failure emitted a successful answer after terminalization")
			}
			if msg.Content == "media too large" {
				t.Fatal("raw media-size provider body reached an event")
			}
			if msg.Content ==
				"Image input could not be accepted after bounded media recovery." {
				redactedCount++
			}
		}
	}
	if redactedCount != 1 {
		t.Fatalf("expected one redacted media error, got %d events: %#v", redactedCount, events)
	}
	if len(userWithMedia.UserInputMultiContent) != 2 || userWithMedia.UserInputMultiContent[1].File == nil {
		t.Fatalf("media recovery mutated the original media message: %#v", userWithMedia)
	}
	if unrelated.Content != "keep me" || unrelated.Extra["stable"] != true {
		t.Fatalf("media recovery mutated unrelated context: %#v", unrelated)
	}
	select {
	case got := <-stopFailureCalled:
		if got == nil ||
			got.Content !=
				"Image input could not be accepted after bounded media recovery." {
			t.Fatalf("unexpected stop-failure payload: %#v", got)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected stop-failure hook for media error")
	}
	select {
	case got := <-stopFailureCalled:
		t.Fatalf("stop-failure hook ran more than once: %#v", got)
	default:
	}
}

func TestP300MediaRecoveryStripsHistoricalAndCurrentTurnWithoutMutatingSource(t *testing.T) {
	// Retain the P30.0 characterization until P30.3 replaces this helper with
	// historical-only recovery. P30.1a no longer wires it into media_size
	// production recovery because it cannot distinguish the current turn.
	historical := &schema.Message{
		Role:    schema.User,
		Content: "historical media",
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "historical media"},
			{Type: schema.ChatMessagePartTypeImageURL},
		},
	}
	current := &schema.Message{
		Role:    schema.User,
		Content: "current turn media",
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "current turn media"},
			{Type: schema.ChatMessagePartTypeImageURL},
		},
	}

	result := compact.TryReactiveCompact(
		[]*schema.Message{historical, current},
		string(QuerySourceSDK),
		"media_size",
	)
	if result == nil {
		t.Fatal("media recovery returned nil")
	}

	stripped := map[string]bool{}
	for _, msg := range result.Messages {
		if msg == nil || msg.Extra == nil || msg.Extra["media_stripped"] != true {
			continue
		}
		if len(msg.UserInputMultiContent) != 0 {
			t.Fatalf("stripped message retained media: %#v", msg)
		}
		stripped[msg.Content] = true
	}
	if !stripped["historical media"] || !stripped["current turn media"] {
		t.Fatalf("recovery did not strip both historical and current media: %#v", result.Messages)
	}
	if len(historical.UserInputMultiContent) != 2 ||
		len(current.UserInputMultiContent) != 2 {
		t.Fatalf("source messages mutated: historical=%#v current=%#v", historical, current)
	}
}

func TestP301aMediaSizeHistoricalOnlyFailsClosed(t *testing.T) {
	ctx := context.Background()
	model := &scriptedOverflowModel{streams: [][]*schema.Message{
		{overflowAPIError("media_size", "media too large")},
		{{Role: schema.Assistant, Content: "continued after historical omission"}},
	}}
	maxTurns := 5
	terminal := Query(ctx, QueryParams{
		Messages: []*schema.Message{
			{
				Role:                  schema.User,
				Content:               "historical attachment",
				UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL}},
			},
			{Role: schema.Assistant, Content: "historical answer"},
			{Role: schema.User, Content: "current text-only question"},
		},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalImageError {
		t.Fatalf("expected image_error terminal, got %q", terminal.Reason)
	}
	if model.callCount != 1 {
		t.Fatalf("expected historical-only media failure to terminalize without retry, got %d calls", model.callCount)
	}
}
