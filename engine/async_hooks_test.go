package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
)

func TestQueryEngineAppliesUserPromptShellHookBeforeModelCall(t *testing.T) {
	hookExecutor := hooks.NewExecutor()
	hookExecutor.RegisterShellHooks(&hooks.ShellHookConfig{UserPromptHooks: []hooks.ShellHook{{
		Command: `printf '%s' '{"systemMessage":"prompt checked","hookSpecificOutput":{"updatedInput":{"prompt":"rewritten prompt"},"additionalContext":"policy context"}}'`,
	}}})
	captured := make(chan []*schema.Message, 1)
	chatModel := &funcModel{fn: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		captured <- append([]*schema.Message(nil), messages...)
		return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
	}}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "prompt-hook-session", CWD: t.TempDir(), TranscriptDir: t.TempDir(),
		ChatModel: chatModel, HookExecutor: hookExecutor,
	})
	t.Cleanup(engine.Close)

	events, _ := engine.SubmitMessage(context.Background(), "original prompt")
	collected := drainEngineEvents(t, events)
	var sawHookAttachment bool
	for _, event := range collected {
		if event.Type == EventAttachment && event.AttachmentMessage != nil && event.AttachmentMessage.Content == "prompt checked" {
			sawHookAttachment = true
		}
	}
	if !sawHookAttachment {
		t.Fatal("user-prompt hook attachment was not projected into the query stream")
	}

	select {
	case messages := <-captured:
		contents := make([]string, 0, len(messages))
		for _, message := range messages {
			if message != nil {
				contents = append(contents, message.Content)
			}
		}
		joined := strings.Join(contents, "\n")
		for _, want := range []string{"rewritten prompt", "prompt checked", "policy context"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("model input missing %q: %q", want, joined)
			}
		}
		if strings.Contains(joined, "original prompt") {
			t.Fatalf("model input retained pre-hook prompt: %q", joined)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("model was not called")
	}
}

func TestQueryEngineUserPromptShellHookCanRejectBeforeModelCall(t *testing.T) {
	hookExecutor := hooks.NewExecutor()
	hookExecutor.RegisterShellHooks(&hooks.ShellHookConfig{UserPromptHooks: []hooks.ShellHook{{
		Command: "printf 'blocked by policy' >&2; exit 2",
	}}})
	modelCalls := 0
	chatModel := &funcModel{fn: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		modelCalls++
		return &schema.Message{Role: schema.Assistant, Content: "unexpected"}, nil
	}}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "prompt-reject-session", CWD: t.TempDir(), TranscriptDir: t.TempDir(),
		ChatModel: chatModel, HookExecutor: hookExecutor,
	})
	t.Cleanup(engine.Close)

	events, _ := engine.SubmitMessage(context.Background(), "secret")
	collected := drainEngineEvents(t, events)
	if modelCalls != 0 {
		t.Fatalf("model calls = %d, want 0", modelCalls)
	}
	var terminal *Terminal
	var rejection string
	for _, event := range collected {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
		if event.Type == EventAttachment && event.AttachmentMessage != nil {
			rejection = event.AttachmentMessage.Content
		}
	}
	if terminal == nil || terminal.Reason != TerminalHookStopped {
		t.Fatalf("terminal = %#v, want hook stopped", terminal)
	}
	if rejection != "blocked by policy" {
		t.Fatalf("rejection = %q", rejection)
	}
}

func TestAsyncHookRewakeSubmissionDoesNotReenterUserPromptHooks(t *testing.T) {
	hookExecutor := hooks.NewExecutor()
	userPromptCalls := 0
	hookExecutor.RegisterUserPromptSubmit(func(context.Context, string) *hooks.UserPromptSubmitHookResult {
		userPromptCalls++
		return nil
	})
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "rewake-submit-session", CWD: t.TempDir(), TranscriptDir: t.TempDir(),
		ChatModel: &fixedResponseModel{response: "handled"}, HookExecutor: hookExecutor,
	})
	t.Cleanup(engine.Close)

	events, _ := engine.SubmitMessageWithMetadata(context.Background(), "<async-hook-response/>", map[string]any{
		"is_meta": true, "attachment_kind": "async_hook_response", "async_rewake": true,
	})
	_ = drainEngineEvents(t, events)
	if userPromptCalls != 0 {
		t.Fatalf("rewake re-entered user prompt hooks %d time(s)", userPromptCalls)
	}
}

func TestQueryDrainsCompletedAsyncHookAtNextModelBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command assertion requires a POSIX shell")
	}
	hookExecutor := hooks.NewExecutor()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = hookExecutor.ShutdownAsyncShellHooks(ctx)
	})
	updates := make(chan hooks.AsyncShellHookCompletion, 2)
	hookExecutor.SetAsyncShellCompletionHandler(func(update hooks.AsyncShellHookCompletion) { updates <- update })
	hookExecutor.RegisterShellHooks(&hooks.ShellHookConfig{PreToolHooks: []hooks.ShellHook{{
		Command: "printf 'late hook context'", ToolPattern: "Bash", Async: true,
	}}})
	hookExecutor.ExecutePreTool(hooks.WithHookTurnID(context.Background(), "launch-turn"), "Bash", "tool", map[string]any{"command": "pwd"})
	for range 2 {
		select {
		case <-updates:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for async hook completion")
		}
	}

	var captured []*schema.Message
	var attachments []*schema.Message
	terminal := Query(context.Background(), QueryParams{
		Messages:       []*schema.Message{{Role: schema.User, Content: "continue"}},
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "system"},
		QuerySource:    QuerySourceSDK,
		ChatModel:      &queryInputShapeModel{},
		HookExecutor:   hookExecutor,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{MainLoopModel: "test-model"}},
		Deps: &QueryDeps{UUID: func() string { return "query-turn" }, CallModel: func(_ context.Context, _ model.BaseChatModel, messages []*schema.Message, _ *schema.Message, _ []*schema.ToolInfo, _ execution.CallModelOptions) (*execution.CallModelResult, error) {
			captured = append([]*schema.Message(nil), messages...)
			return &execution.CallModelResult{StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}})}, nil
		}},
	}, func(event QueryEvent) {
		if event.Type == EventAttachment && event.AttachmentMessage != nil {
			attachments = append(attachments, event.AttachmentMessage)
		}
	})
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	joined := ""
	for _, message := range captured {
		joined += message.Content
	}
	if !strings.Contains(joined, "late hook context") {
		t.Fatalf("model input missing async hook result: %q", joined)
	}
	if len(attachments) != 1 || attachments[0].Extra["attachment_kind"] != "async_hook_response" {
		t.Fatalf("async hook attachments = %#v", attachments)
	}
	if messages := hookExecutor.DrainAsyncShellMessages(); len(messages) != 0 {
		t.Fatalf("async hook result remained after query drain: %#v", messages)
	}
}

func TestAsyncHookResponseAfterTerminalDoesNotReopenRuntimeThread(t *testing.T) {
	hookExecutor := hooks.NewExecutor()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "async-runtime-session", CWD: t.TempDir(), TranscriptDir: t.TempDir(),
		ChatModel: &fixedResponseModel{response: "done"}, HookExecutor: hookExecutor,
	})
	t.Cleanup(engine.Close)

	events, _ := engine.SubmitMessage(context.Background(), "hello")
	_ = drainEngineEvents(t, events)
	before := engine.RuntimeSnapshot().Threads[engine.ThreadID()]
	if before.Status != RuntimeThreadCompleted || before.ActiveTurnID == "" {
		t.Fatalf("terminal runtime snapshot = %#v", before)
	}

	hookExecutor.RegisterShellHooks(&hooks.ShellHookConfig{PreToolHooks: []hooks.ShellHook{{
		Command: "printf complete", ToolPattern: "Bash", Async: true,
		StatusMessage: "Background check",
	}}})
	hookEvents := engine.SubscribeAsyncHookEvents()
	hookExecutor.ExecutePreTool(hooks.WithHookTurnID(context.Background(), before.ActiveTurnID), "Bash", "tool-async", map[string]any{"command": "pwd"})
	running := waitForAsyncEngineHookEvent(t, hookEvents)
	completed := waitForAsyncEngineHookEvent(t, hookEvents)
	if running.HookResponse == nil || running.HookResponse.Phase != "running" ||
		completed.HookResponse == nil || completed.HookResponse.Phase != "completed" {
		t.Fatalf("hook events out of order: running=%#v completed=%#v", running.HookResponse, completed.HookResponse)
	}
	after := engine.RuntimeSnapshot().Threads[engine.ThreadID()]
	if after.Status != RuntimeThreadCompleted || after.ActiveTurnID != before.ActiveTurnID {
		t.Fatalf("async completion reopened terminal thread: before=%#v after=%#v", before, after)
	}
	if after.LastSequence <= before.LastSequence {
		t.Fatalf("async response was not retained in runtime state: before=%d after=%d", before.LastSequence, after.LastSequence)
	}
}

func TestQueryEngineCloseCancelsOwnedAsyncHookAndClosesEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process lifecycle assertion requires a POSIX shell")
	}
	hookExecutor := hooks.NewExecutor()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "async-close-session", CWD: t.TempDir(), TranscriptDir: t.TempDir(),
		HookExecutor: hookExecutor,
	})
	startedFile := filepath.Join(t.TempDir(), "started")
	hookExecutor.RegisterShellHooks(&hooks.ShellHookConfig{PreToolHooks: []hooks.ShellHook{{
		Command:     fmt.Sprintf("printf started > %q; sleep 30", startedFile),
		ToolPattern: "Bash", Async: true,
	}}})
	hookEvents := engine.SubscribeAsyncHookEvents()
	hookExecutor.ExecutePreTool(hooks.WithHookTurnID(context.Background(), "close-turn"), "Bash", "tool-close", map[string]any{"command": "pwd"})
	running := waitForAsyncEngineHookEvent(t, hookEvents)
	if running.HookResponse == nil || running.HookResponse.Phase != "running" {
		t.Fatalf("running event = %#v", running.HookResponse)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("async hook process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	closeStarted := time.Now()
	engine.Close()
	if elapsed := time.Since(closeStarted); elapsed > 2*time.Second {
		t.Fatalf("engine close waited %v for cancelled hook", elapsed)
	}
	if _, open := <-hookEvents; open {
		t.Fatal("async hook event stream remained open after engine close")
	}
	engine.Close()
}

func TestAsyncHookCompletionBackpressuresInsteadOfDropping(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{SessionID: "async-backpressure", CWD: t.TempDir(), TranscriptDir: t.TempDir()})
	t.Cleanup(engine.Close)
	events := engine.SubscribeAsyncHookEvents()
	completion := func(index int) hooks.AsyncShellHookCompletion {
		now := time.Now().UTC()
		return hooks.AsyncShellHookCompletion{
			ID: fmt.Sprintf("hook-%d", index), TurnID: "turn-backpressure",
			Event: "PreToolUse", HookName: "check", Phase: "completed",
			Result: hooks.ShellHookResult{ExitCode: 0}, StartedAt: now, CompletedAt: now,
		}
	}
	for index := range 64 {
		engine.handleAsyncShellHookCompletion(completion(index))
	}
	blockedSendDone := make(chan struct{})
	go func() {
		engine.handleAsyncShellHookCompletion(completion(64))
		close(blockedSendDone)
	}()
	deadline := time.Now().Add(time.Second)
	for engine.RuntimeSnapshot().Threads[engine.ThreadID()].LastSequence < 65 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if sequence := engine.RuntimeSnapshot().Threads[engine.ThreadID()].LastSequence; sequence < 65 {
		t.Fatalf("65th completion did not reach publish boundary, sequence=%d", sequence)
	}
	select {
	case <-blockedSendDone:
		t.Fatal("65th completion bypassed bounded backpressure")
	default:
	}

	first := <-events
	if first.HookResponse == nil || first.HookResponse.HookID != "hook-0" {
		t.Fatalf("first completion = %#v", first.HookResponse)
	}
	select {
	case <-blockedSendDone:
	case <-time.After(time.Second):
		t.Fatal("blocked completion did not publish after consumer progress")
	}
	seen := map[string]bool{"hook-0": true}
	for len(seen) < 65 {
		select {
		case event := <-events:
			if event.HookResponse != nil {
				seen[event.HookResponse.HookID] = true
			}
		case <-time.After(time.Second):
			t.Fatalf("received %d/65 hook completions", len(seen))
		}
	}
}

func TestQueryEngineCloseUnblocksBackpressuredHookCompletion(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{SessionID: "async-close-backpressure", CWD: t.TempDir(), TranscriptDir: t.TempDir()})
	_ = engine.SubscribeAsyncHookEvents()
	now := time.Now().UTC()
	completion := func(index int) hooks.AsyncShellHookCompletion {
		return hooks.AsyncShellHookCompletion{
			ID: fmt.Sprintf("blocked-%d", index), TurnID: "turn-close-backpressure",
			Event: "PostToolUse", HookName: "check", Phase: "completed",
			Result: hooks.ShellHookResult{ExitCode: 0}, StartedAt: now, CompletedAt: now,
		}
	}
	for index := range 64 {
		engine.handleAsyncShellHookCompletion(completion(index))
	}
	publishDone := make(chan struct{})
	go func() {
		engine.handleAsyncShellHookCompletion(completion(64))
		close(publishDone)
	}()
	deadline := time.Now().Add(time.Second)
	for engine.RuntimeSnapshot().Threads[engine.ThreadID()].LastSequence < 65 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	closeDone := make(chan struct{})
	go func() {
		engine.Close()
		close(closeDone)
	}()
	for name, done := range map[string]<-chan struct{}{"publish": publishDone, "close": closeDone} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s remained blocked during engine close", name)
		}
	}
}

func waitForAsyncEngineHookEvent(t *testing.T, events <-chan QueryEvent) QueryEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != EventHookResponse {
			t.Fatalf("event type = %q, want hook response", event.Type)
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async hook event")
		return QueryEvent{}
	}
}
