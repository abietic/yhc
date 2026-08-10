package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/budget"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type interruptibleTurnModel struct {
	callCount        int
	firstChunkSent   chan struct{}
	releaseFirstTurn chan struct{}
}

func (m *interruptibleTurnModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *interruptibleTurnModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		sr, sw := schema.Pipe[*schema.Message](2)
		go func() {
			defer sw.Close()
			sw.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
			close(m.firstChunkSent)
			<-m.releaseFirstTurn
		}()
		return sr, nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func waitForEngineEvent(t *testing.T, events <-chan QueryEvent, match func(QueryEvent) bool, timeoutMessage string) QueryEvent {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before expected event")
			}
			if match(evt) {
				return evt
			}
		case <-timeout:
			t.Fatal(timeoutMessage)
		}
	}
}

func drainEngineEvents(t *testing.T, events <-chan QueryEvent) []QueryEvent {
	t.Helper()
	collected := make([]QueryEvent, 0)
	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return collected
			}
			collected = append(collected, evt)
		case <-timeout:
			t.Fatal("timed out draining query engine events")
		}
	}
}

func numericExtraEquals(v any, want int) bool {
	switch n := v.(type) {
	case int:
		return n == want
	case int64:
		return n == int64(want)
	case float64:
		return n == float64(want)
	default:
		return false
	}
}

func TestConversationHistoryDropsPendingAssistantOnUserInterruption(t *testing.T) {
	h := newConversationHistory([]*schema.Message{{Role: schema.User, Content: "hello"}})
	h.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{Role: schema.Assistant, Content: "partial"}})
	h.Observe(QueryEvent{Type: EventUserInterruption})

	msgs := h.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected only the original user message after interruption, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "hello" {
		t.Fatalf("unexpected stored message after interruption: %#v", msgs[0])
	}
}

func TestQueryEngineInterruptDropsPartialAssistantAndAllowsNextTurn(t *testing.T) {
	dir := t.TempDir()
	model := &interruptibleTurnModel{
		firstChunkSent:   make(chan struct{}),
		releaseFirstTurn: make(chan struct{}),
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-resume-a",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
	})

	firstEvents, _ := eng.SubmitMessage(context.Background(), "hello")
	select {
	case <-model.firstChunkSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first streamed chunk")
	}

	eng.Interrupt()
	close(model.releaseFirstTurn)
	firstCollected := drainEngineEvents(t, firstEvents)

	hasInterruption := false
	for _, evt := range firstCollected {
		if evt.Type == EventUserInterruption {
			hasInterruption = true
			break
		}
	}
	if !hasInterruption {
		t.Fatalf("expected first turn to emit a user interruption event, got %#v", firstCollected)
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-resume-a",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected interrupted transcript reload to keep only the user message, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "hello" {
		t.Fatalf("unexpected interrupted transcript contents: %#v", msgs[0])
	}

	secondEvents, _ := eng.SubmitMessage(context.Background(), "next")
	_ = drainEngineEvents(t, secondEvents)
	finalMsgs := eng.GetMessages()
	if len(finalMsgs) != 3 {
		t.Fatalf("expected interrupted turn to be followed by a normal second turn, got %#v", finalMsgs)
	}
	if finalMsgs[0].Role != schema.User || finalMsgs[0].Content != "hello" {
		t.Fatalf("unexpected first persisted message after second turn: %#v", finalMsgs[0])
	}
	if finalMsgs[1].Role != schema.User || finalMsgs[1].Content != "next" {
		t.Fatalf("unexpected second-turn user message: %#v", finalMsgs[1])
	}
	if finalMsgs[2].Role != schema.Assistant || finalMsgs[2].Content != "done" {
		t.Fatalf("expected second turn to complete normally, got %#v", finalMsgs[2])
	}
}

type checkpointToolThenBlockModel struct {
	streamCalls int
	called      chan struct{}
	release     chan struct{}
}

func (m *checkpointToolThenBlockModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *checkpointToolThenBlockModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	if m.called == nil {
		m.called = make(chan struct{})
	}
	if m.release == nil {
		m.release = make(chan struct{})
	}
	if m.streamCalls == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_checkpoint_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"echo hi"}`,
				},
			}},
		}}), nil
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		close(m.called)
		<-m.release
	}()
	return sr, nil
}

func TestQueryEngineCheckpointsToolResultBeforeTurnFinishes(t *testing.T) {
	dir := t.TempDir()
	model := &checkpointToolThenBlockModel{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-checkpoint-tool",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
	})
	eng.toolRegistry = nil

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "run a command")
	select {
	case <-model.called:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second-turn stream to block")
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-checkpoint-tool",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	// 4 messages: user, assistant+tool_call, tool_result, continuation prompt
	if len(msgs) != 4 {
		t.Fatalf("expected mid-turn checkpoint to persist user, assistant tool call, tool result, and continuation prompt, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "run a command" {
		t.Fatalf("unexpected checkpointed user message: %#v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected checkpointed assistant tool call, got %#v", msgs[1])
	}
	if msgs[2].Role != schema.Tool || msgs[2].ToolCallID != "call_checkpoint_1" {
		t.Fatalf("expected checkpointed tool result, got %#v", msgs[2])
	}
	if msgs[3].Role != schema.User || msgs[3].Content != ContinuationPrompt {
		t.Fatalf("expected continuation prompt after interrupted turn, got %#v", msgs[3])
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}

func TestQueryEngineCheckpointsPostToolAttachmentsBeforeTurnFinishes(t *testing.T) {
	dir := t.TempDir()
	model := &checkpointToolThenBlockModel{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	hookExec := hooks.NewExecutor()
	hookExec.RegisterPostTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any, result string) *hooks.PostToolHookResult {
		return &hooks.PostToolHookResult{Attachments: []*schema.Message{{
			Role:    schema.User,
			Content: "post-tool attachment",
			Extra:   map[string]any{"is_meta": true, "attachment_kind": "hook"},
		}}}
	})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-checkpoint-attachment",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
		HookExecutor:       hookExec,
	})
	eng.toolRegistry = nil

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "run a command")
	waitForEngineEvent(t, events, func(evt QueryEvent) bool {
		return evt.Type == EventAttachment && evt.AttachmentMessage != nil && evt.AttachmentMessage.Content == "post-tool attachment"
	}, "timed out waiting for post-tool attachment event")
	select {
	case <-model.called:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second-turn stream to block after attachment")
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-checkpoint-attachment",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 4 {
		t.Fatalf("expected mid-turn checkpoint to persist user, assistant tool call, tool result, and post-tool attachment, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "run a command" {
		t.Fatalf("unexpected checkpointed user message: %#v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected checkpointed assistant tool call, got %#v", msgs[1])
	}
	if msgs[2].Role != schema.Tool || msgs[2].ToolCallID != "call_checkpoint_1" {
		t.Fatalf("expected checkpointed tool result before attachment, got %#v", msgs[2])
	}
	if msgs[3].Content != "post-tool attachment" {
		t.Fatalf("expected checkpointed post-tool attachment, got %#v", msgs[3])
	}
	if msgs[3].Extra == nil || msgs[3].Extra["attachment_kind"] != "hook" {
		t.Fatalf("expected attachment metadata to survive checkpoint reload, got %#v", msgs[3])
		return
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}

type autoCompactCheckpointModel struct {
	release chan struct{}
}

func (m *autoCompactCheckpointModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	// Check if this is a compact summarization call
	for _, msg := range input {
		if msg.Role == schema.System && strings.Contains(msg.Content, "summar") {
			return &schema.Message{Role: schema.Assistant, Content: "[Summary of conversation so far]"}, nil
		}
	}
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *autoCompactCheckpointModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "partial after compact"}, nil)
		<-m.release
	}()
	return sr, nil
}

func TestQueryEngineCheckpointsCompactBoundaryBatchBeforePartialAssistant(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	dir := t.TempDir()
	model := &autoCompactCheckpointModel{
		release: make(chan struct{}),
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-checkpoint-compact-batch",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
	})
	eng.messages = []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("older question ", 220)},
		{Role: schema.Assistant, Content: strings.Repeat("older answer ", 200)},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "new prompt")
	waitForEngineEvent(t, events, func(evt QueryEvent) bool {
		return evt.Type == EventAssistant && evt.Message != nil && evt.Message.Content == "partial after compact"
	}, "timed out waiting for post-compact partial assistant event")

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-checkpoint-compact-batch",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 4 {
		t.Fatalf("expected compact batch checkpoint to persist boundary, summary, and preserved tail, got %#v", msgs)
	}
	if msgs[0].Extra == nil || msgs[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected compact boundary at checkpoint start, got %#v", msgs[0])
		return
	}
	if msgs[1].Extra == nil || msgs[1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected compact summary after boundary, got %#v", msgs[1])
		return
	}
	if msgs[2].Role != schema.Assistant || msgs[2].Content != "latest answer" {
		t.Fatalf("expected preserved assistant tail in compact checkpoint, got %#v", msgs[2])
	}
	if msgs[3].Role != schema.User || msgs[3].Content != "new prompt" {
		t.Fatalf("expected latest user prompt in compact checkpoint, got %#v", msgs[3])
	}
	for _, msg := range msgs {
		if msg != nil && msg.Content == "partial after compact" {
			t.Fatalf("did not expect partial assistant content in compact checkpoint: %#v", msgs)
			return
		}
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}

type maxOutputRecoveryThenBlockModel struct {
	callCount int
	release   chan struct{}
}

func (m *maxOutputRecoveryThenBlockModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *maxOutputRecoveryThenBlockModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.release == nil {
		m.release = make(chan struct{})
	}
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:    schema.Assistant,
			Content: "limit hit",
			Extra: map[string]any{
				"api_error":  true,
				"error_type": "max_output_tokens",
			},
		}}), nil
	}
	if m.callCount == 2 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:    schema.Assistant,
			Content: "second limit hit",
			Extra: map[string]any{
				"api_error":  true,
				"error_type": "max_output_tokens",
			},
		}}), nil
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		<-m.release
	}()
	return sr, nil
}

func TestQueryEngineCheckpointsMaxOutputRecoveryMessageBeforeContinuationFinishes(t *testing.T) {
	dir := t.TempDir()
	model := &maxOutputRecoveryThenBlockModel{release: make(chan struct{})}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-max-output-recovery",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           6,
		ChatModel:          model,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "do the work")
	recoveryEvt := waitForEngineEvent(t, events, func(evt QueryEvent) bool {
		return evt.Type == EventAttachment && evt.AttachmentMessage != nil && strings.Contains(evt.AttachmentMessage.Content, "Output token limit hit")
	}, "timed out waiting for max-output-token recovery message")
	if recoveryEvt.AttachmentMessage.Extra == nil || recoveryEvt.AttachmentMessage.Extra["is_meta"] != true {
		t.Fatalf("expected recovery message to be meta, got %#v", recoveryEvt.AttachmentMessage)
		return
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-max-output-recovery",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected reload to include user prompt and recovery meta message, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "do the work" {
		t.Fatalf("unexpected persisted initial user message: %#v", msgs[0])
	}
	if msgs[1].Extra == nil || msgs[1].Extra["is_meta"] != true {
		t.Fatalf("expected persisted recovery message to remain meta, got %#v", msgs[1])
		return
	}
	if !strings.Contains(msgs[1].Content, "Output token limit hit") {
		t.Fatalf("expected persisted recovery prompt after reload, got %#v", msgs[1])
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}

type tokenBudgetContinueThenBlockModel struct {
	callCount  int
	secondCall chan struct{}
	release    chan struct{}
}

func (m *tokenBudgetContinueThenBlockModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *tokenBudgetContinueThenBlockModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.secondCall == nil {
		m.secondCall = make(chan struct{})
	}
	if m.release == nil {
		m.release = make(chan struct{})
	}
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "first assistant"}}), nil
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		close(m.secondCall)
		<-m.release
	}()
	return sr, nil
}

func TestQueryEngineCheckpointsTokenBudgetNudgeBeforeContinuationFinishes(t *testing.T) {
	dir := t.TempDir()
	model := &tokenBudgetContinueThenBlockModel{
		secondCall: make(chan struct{}),
		release:    make(chan struct{}),
	}
	tracker := budget.NewTokenBudget(100000)
	tracker.RecordInput(60000)
	tracker.RecordOutput(40000)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-token-budget-nudge",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
		TokenBudgetTracker: tracker,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "hello")
	nudgeEvt := waitForEngineEvent(t, events, func(evt QueryEvent) bool {
		return evt.Type == EventAttachment && evt.AttachmentMessage != nil && evt.AttachmentMessage.Content == "Output token budget consumed. Break remaining work into smaller pieces."
	}, "timed out waiting for token-budget nudge")
	if nudgeEvt.AttachmentMessage.Extra == nil || nudgeEvt.AttachmentMessage.Extra["attachment_kind"] != "token_budget_continuation" {
		t.Fatalf("expected token-budget nudge metadata, got %#v", nudgeEvt.AttachmentMessage)
		return
	}
	select {
	case <-model.secondCall:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for continuation turn to start after token-budget nudge")
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-token-budget-nudge",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("expected reload to include token-budget continuation state, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "hello" {
		t.Fatalf("unexpected persisted user message: %#v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant || msgs[1].Content != "first assistant" {
		t.Fatalf("expected first assistant before continuation, got %#v", msgs[1])
	}
	if msgs[2].Extra == nil || msgs[2].Extra["attachment_kind"] != "token_budget_continuation" {
		t.Fatalf("expected token-budget continuation metadata after reload, got %#v", msgs[2])
		return
	}
	if msgs[2].Content != "Output token budget consumed. Break remaining work into smaller pieces." {
		t.Fatalf("expected persisted token-budget nudge after reload, got %#v", msgs[2])
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}

type stopHookBlockingThenBlockModel struct {
	streamCalls int
	secondCall  chan struct{}
	release     chan struct{}
}

func (m *stopHookBlockingThenBlockModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *stopHookBlockingThenBlockModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	if m.secondCall == nil {
		m.secondCall = make(chan struct{})
	}
	if m.release == nil {
		m.release = make(chan struct{})
	}
	if m.streamCalls == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "first assistant"}}), nil
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		close(m.secondCall)
		<-m.release
	}()
	return sr, nil
}

func TestQueryEngineCheckpointsStopHookBlockingMessagesBeforeRetryTurnFinishes(t *testing.T) {
	dir := t.TempDir()
	model := &stopHookBlockingThenBlockModel{
		secondCall: make(chan struct{}),
		release:    make(chan struct{}),
	}
	hookExec := hooks.NewExecutor()
	hookExec.RegisterStop(func(messagesForQuery, assistantMessages []*schema.Message, stopHookActive bool) *hooks.StopHookResult {
		if stopHookActive {
			return nil
		}
		return &hooks.StopHookResult{BlockingErrors: []*schema.Message{{
			Role:    schema.User,
			Content: "stop hook blocked",
			Extra:   map[string]any{"is_meta": true, "attachment_kind": "stop_hook_blocking"},
		}}}
	})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-stop-hook-blocking",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
		HookExecutor:       hookExec,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "hello")
	waitForEngineEvent(t, events, func(evt QueryEvent) bool {
		return evt.Type == EventAttachment && evt.AttachmentMessage != nil && evt.AttachmentMessage.Content == "stop hook blocked"
	}, "timed out waiting for stop-hook blocking message")
	select {
	case <-model.secondCall:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry turn to start after stop-hook blocking")
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-stop-hook-blocking",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("expected reload to include stop-hook blocking checkpoint state, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "hello" {
		t.Fatalf("unexpected checkpointed user message: %#v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant || msgs[1].Content != "first assistant" {
		t.Fatalf("expected first assistant before retry, got %#v", msgs[1])
	}
	if msgs[2].Content != "stop hook blocked" {
		t.Fatalf("expected stop-hook blocking marker after assistant, got %#v", msgs[2])
	}
	if msgs[2].Extra == nil || msgs[2].Extra["attachment_kind"] != "stop_hook_blocking" {
		t.Fatalf("expected stop-hook metadata to survive reload, got %#v", msgs[2])
		return
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}

func TestQueryEngineCheckpointsHookStoppedContinuationAttachmentBeforeTurnEnds(t *testing.T) {
	dir := t.TempDir()
	model := &checkpointToolThenBlockModel{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	hookExec := hooks.NewExecutor()
	hookExec.RegisterPostTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any, result string) *hooks.PostToolHookResult {
		return &hooks.PostToolHookResult{PreventContinuation: true, StopReason: "post tool requested stop"}
	})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-hook-stopped-continuation",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           4,
		ChatModel:          model,
		HookExecutor:       hookExec,
	})
	eng.toolRegistry = nil

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "run a command")
	attachmentEvt := waitForEngineEvent(t, events, func(evt QueryEvent) bool {
		return evt.Type == EventAttachment && evt.AttachmentMessage != nil && evt.AttachmentMessage.Extra != nil && evt.AttachmentMessage.Extra["attachment_kind"] == "hook_stopped_continuation"
	}, "timed out waiting for hook-stopped continuation attachment")
	if attachmentEvt.AttachmentMessage.Content != "post tool requested stop" {
		t.Fatalf("expected hook-stopped attachment content, got %#v", attachmentEvt.AttachmentMessage)
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-hook-stopped-continuation",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 4 {
		t.Fatalf("expected reload to include hook-stopped continuation checkpoint state, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "run a command" {
		t.Fatalf("unexpected checkpointed user message: %#v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected checkpointed assistant tool call, got %#v", msgs[1])
	}
	if msgs[2].Role != schema.Tool || msgs[2].ToolCallID != "call_checkpoint_1" {
		t.Fatalf("expected checkpointed tool result before hook-stopped attachment, got %#v", msgs[2])
	}
	if msgs[3].Extra == nil || msgs[3].Extra["attachment_kind"] != "hook_stopped_continuation" {
		t.Fatalf("expected hook-stopped metadata after reload, got %#v", msgs[3])
		return
	}
	if msgs[3].Extra["hook_name"] != "PostToolUse:Bash" || msgs[3].Extra["hook_event"] != "PostToolUse" || msgs[3].Extra["tool_use_id"] != "call_checkpoint_1" {
		t.Fatalf("expected hook-stopped metadata to survive reload, got %#v", msgs[3].Extra)
	}
	if msgs[3].Content != "post tool requested stop" {
		t.Fatalf("expected persisted hook-stopped continuation message after reload, got %#v", msgs[3])
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}

type maxTurnsToolThenBlockModel struct {
	streamCalls int
	release     chan struct{}
}

func (m *maxTurnsToolThenBlockModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *maxTurnsToolThenBlockModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	if m.release == nil {
		m.release = make(chan struct{})
	}
	if m.streamCalls == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_max_turns_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"echo hi"}`,
				},
			}},
		}}), nil
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		<-m.release
	}()
	return sr, nil
}

func TestQueryEngineReloadPersistsMaxTurnsAttachmentState(t *testing.T) {
	dir := t.TempDir()
	model := &maxTurnsToolThenBlockModel{release: make(chan struct{})}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-max-turns-reload",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           1,
		ChatModel:          model,
	})
	eng.toolRegistry = nil

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := eng.SubmitMessage(ctx, "run a command")
	maxTurnsEvt := waitForEngineEvent(t, events, func(evt QueryEvent) bool {
		return evt.Type == EventMaxTurnsReached && evt.MaxTurnsInfo != nil
	}, "timed out waiting for max turns event")
	if maxTurnsEvt.MaxTurnsInfo.MaxTurns != 1 || maxTurnsEvt.MaxTurnsInfo.TurnCount != 2 {
		t.Fatalf("unexpected max turns event payload: %#v", maxTurnsEvt.MaxTurnsInfo)
	}

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-max-turns-reload",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "You are helpful.",
	})
	msgs := reloaded.GetMessages()
	if len(msgs) != 4 {
		t.Fatalf("expected reload to include max-turns resumable attachment, got %#v", msgs)
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "run a command" {
		t.Fatalf("unexpected checkpointed user message: %#v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected checkpointed assistant tool call, got %#v", msgs[1])
	}
	if msgs[2].Role != schema.Tool || msgs[2].ToolCallID != "call_max_turns_1" {
		t.Fatalf("expected checkpointed tool result before max-turns marker, got %#v", msgs[2])
	}
	if msgs[3].Extra == nil || msgs[3].Extra["attachment_kind"] != "max_turns_reached" {
		t.Fatalf("expected max-turns marker metadata after reload, got %#v", msgs[3])
		return
	}
	if !numericExtraEquals(msgs[3].Extra["max_turns"], 1) || !numericExtraEquals(msgs[3].Extra["turn_count"], 2) {
		t.Fatalf("expected max-turns metadata to survive reload, got %#v", msgs[3].Extra)
	}

	close(model.release)
	cancel()
	_ = drainEngineEvents(t, events)
}
