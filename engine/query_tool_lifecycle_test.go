package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type singleToolCallModel struct {
	toolName string
	args     string
	called   bool
}

func (m *singleToolCallModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *singleToolCallModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if !m.called {
		m.called = true
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      m.toolName,
					Arguments: m.args,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func collectEvents(ctx context.Context, params QueryParams) ([]QueryEvent, Terminal) {
	var events []QueryEvent
	terminal := Query(ctx, params, func(evt QueryEvent) {
		events = append(events, evt)
	})
	return events, terminal
}

func TestQueryPermissionDeniedSkipsToolExecutor(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	var executed bool
	maxTurns := 4
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolRegistry: registry,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "policy rejected"
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			executed = true
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if executed {
		t.Fatal("expected tool executor to be skipped on permission denial")
	}
	var toolMsg *schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult {
			toolMsg = evt.ToolResultMessage
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool_result event for denied permission")
		return
	}
	if toolMsg.Extra == nil || toolMsg.Extra["is_error"] != true {
		t.Fatalf("expected denied tool result to be marked error, got %#v", toolMsg.Extra)
		return
	}
	if !strings.Contains(toolMsg.Content, "permission denied") || !strings.Contains(toolMsg.Content, "policy rejected") {
		t.Fatalf("unexpected denial content: %q", toolMsg.Content)
	}
}

func TestQueryPermissionAllowExecutesToolOnce(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	calls := 0
	maxTurns := 4
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolRegistry: registry,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			if toolName != "Bash" {
				t.Fatalf("unexpected tool %q", toolName)
			}
			if input["command"] != "pwd" {
				t.Fatalf("unexpected input: %#v", input)
			}
			return true, ""
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			calls++
			if jsonInput != `{"command":"pwd"}` {
				t.Fatalf("unexpected tool json: %q", jsonInput)
			}
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one tool execution, got %d", calls)
	}
}

func TestQueryMalformedToolArgsYieldsErrorToolResult(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":`}
	var executed bool
	maxTurns := 4

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run malformed"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return true, ""
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			executed = true
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if executed {
		t.Fatal("expected malformed tool args to skip executor")
	}
	var toolMsg *schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult {
			toolMsg = evt.ToolResultMessage
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool_result event")
		return
	}
	if toolMsg.Extra == nil || toolMsg.Extra["is_error"] != true {
		t.Fatalf("expected malformed args result to be error, got %#v", toolMsg.Extra)
		return
	}
	if !strings.Contains(toolMsg.Content, "invalid tool input JSON") {
		t.Fatalf("unexpected malformed args message: %q", toolMsg.Content)
	}
}

func TestQueryPreAndPostToolHooksWrapExecution(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	maxTurns := 4
	order := make([]string, 0)
	hookExec := hooks.NewExecutor()
	hookExec.RegisterPreTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any) *hooks.PreToolHookResult {
		order = append(order, "pre")
		updated := make(map[string]any, len(input)+1)
		for k, v := range input {
			updated[k] = v
		}
		updated["command"] = "echo from-hook"
		return &hooks.PreToolHookResult{UpdatedInput: updated}
	})
	hookExec.RegisterPostTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any, result string) *hooks.PostToolHookResult {
		order = append(order, "post")
		if result != "tool-ok" {
			t.Fatalf("unexpected raw post-tool result: %q", result)
		}
		return &hooks.PostToolHookResult{UpdatedResult: "tool-ok-post", ReplaceResult: true}
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			order = append(order, "exec:"+jsonInput)
			if jsonInput != `{"command":"echo from-hook"}` {
				t.Fatalf("expected pre-hook mutated input, got %q", jsonInput)
			}
			return "tool-ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if len(order) != 3 || order[0] != "pre" || !strings.HasPrefix(order[1], "exec:") || order[2] != "post" {
		t.Fatalf("unexpected hook/execution order: %#v", order)
	}
	var toolMsg *schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult {
			toolMsg = evt.ToolResultMessage
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool_result event")
		return
	}
	if toolMsg.Content != "tool-ok-post" {
		t.Fatalf("expected post-hook rewritten result, got %q", toolMsg.Content)
	}
}

func TestQueryPreToolHookBlockingSkipsExecutor(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	maxTurns := 4
	var executed bool
	hookExec := hooks.NewExecutor()
	hookExec.RegisterPreTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any) *hooks.PreToolHookResult {
		return &hooks.PreToolHookResult{DenyReason: "blocked by pre-hook"}
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			executed = true
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if executed {
		t.Fatal("expected executor to be skipped when pre-tool hook blocks")
	}
	var toolMsg *schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult {
			toolMsg = evt.ToolResultMessage
			break
		}
	}
	if toolMsg == nil || toolMsg.Content != "blocked by pre-hook" {
		t.Fatalf("expected pre-hook denial tool result, got %#v", toolMsg)
		return
	}
}

func TestQueryPreToolShellHookTimeoutIsObservableAndNonBlocking(t *testing.T) {
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	maxTurns := 4
	var executed bool
	hookExec := hooks.NewExecutor()
	hookExec.RegisterShellHooks(&hooks.ShellHookConfig{PreToolHooks: []hooks.ShellHook{{
		ToolPattern: "Bash",
		Command:     "sleep 5",
		Timeout:     50 * time.Millisecond,
	}}})

	events, terminal := collectEvents(context.Background(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executed = true
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal reason = %q, want completed", terminal.Reason)
	}
	if !executed {
		t.Fatal("tool did not execute after non-blocking hook timeout")
	}
	for _, evt := range events {
		if evt.Type != EventAttachment || evt.AttachmentMessage == nil || evt.AttachmentMessage.Extra == nil {
			continue
		}
		if evt.AttachmentMessage.Extra["attachment_kind"] == "hook_non_blocking_error" {
			if evt.AttachmentMessage.Extra["timed_out"] != true {
				t.Fatalf("timeout attachment metadata = %#v", evt.AttachmentMessage.Extra)
			}
			return
		}
	}
	t.Fatal("missing hook_non_blocking_error attachment event")
}

func TestQueryPostToolFailureHookRunsAfterError(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	maxTurns := 4
	order := make([]string, 0)
	hookExec := hooks.NewExecutor()
	hookExec.RegisterPostToolFailure(func(ctx context.Context, toolName, toolUseID string, input map[string]any, err error) *hooks.PostToolFailureHookResult {
		order = append(order, "failure:"+err.Error())
		return nil
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			order = append(order, "exec")
			return "", errors.New("boom")
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if len(order) != 2 || order[0] != "exec" || order[1] != "failure:boom" {
		t.Fatalf("unexpected failure hook order: %#v", order)
	}
	var toolMsg *schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult {
			toolMsg = evt.ToolResultMessage
			break
		}
	}
	if toolMsg == nil || toolMsg.Content != "boom" {
		t.Fatalf("expected tool error result, got %#v", toolMsg)
		return
	}
}

func TestQueryPreToolPreventContinuationEmitsHookStoppedAttachment(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	maxTurns := 4
	hookExec := hooks.NewExecutor()
	hookExec.RegisterPreTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any) *hooks.PreToolHookResult {
		return &hooks.PreToolHookResult{PreventContinuation: true, StopReason: "pre stop reason"}
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalHookStopped {
		t.Fatalf("expected terminal hook_stopped, got %q", terminal.Reason)
	}
	var toolMsg *schema.Message
	var attachment *schema.Message
	for _, evt := range events {
		switch evt.Type {
		case EventToolResult:
			toolMsg = evt.ToolResultMessage
		case EventAttachment:
			if evt.AttachmentMessage != nil && evt.AttachmentMessage.Extra != nil && evt.AttachmentMessage.Extra["attachment_kind"] == "hook_stopped_continuation" {
				attachment = evt.AttachmentMessage
			}
		}
	}
	if toolMsg == nil || toolMsg.Content != "ok" {
		t.Fatalf("expected tool result before hook-stopped attachment, got %#v", toolMsg)
		return
	}
	if attachment == nil {
		t.Fatal("expected hook-stopped continuation attachment")
		return
	}
	if attachment.Content != "pre stop reason" {
		t.Fatalf("expected pre-tool stop reason, got %#v", attachment)
	}
	if attachment.Extra["hook_name"] != "PreToolUse:Bash" || attachment.Extra["hook_event"] != "PreToolUse" || attachment.Extra["tool_use_id"] != "call_1" {
		t.Fatalf("unexpected hook-stopped continuation metadata: %#v", attachment.Extra)
	}
}

func TestQueryPostToolPreventContinuationEmitsHookStoppedAttachment(t *testing.T) {
	ctx := context.Background()
	model := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	maxTurns := 4
	hookExec := hooks.NewExecutor()
	hookExec.RegisterPostTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any, result string) *hooks.PostToolHookResult {
		return &hooks.PostToolHookResult{PreventContinuation: true, StopReason: "post stop reason"}
	})

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalHookStopped {
		t.Fatalf("expected terminal hook_stopped, got %q", terminal.Reason)
	}
	var toolMsg *schema.Message
	var attachment *schema.Message
	for _, evt := range events {
		switch evt.Type {
		case EventToolResult:
			toolMsg = evt.ToolResultMessage
		case EventAttachment:
			if evt.AttachmentMessage != nil && evt.AttachmentMessage.Extra != nil && evt.AttachmentMessage.Extra["attachment_kind"] == "hook_stopped_continuation" {
				attachment = evt.AttachmentMessage
			}
		}
	}
	if toolMsg == nil || toolMsg.Content != "ok" {
		t.Fatalf("expected tool result before hook-stopped attachment, got %#v", toolMsg)
		return
	}
	if attachment == nil {
		t.Fatal("expected hook-stopped continuation attachment")
		return
	}
	if attachment.Content != "post stop reason" {
		t.Fatalf("expected post-tool stop reason, got %#v", attachment)
	}
	if attachment.Extra["hook_name"] != "PostToolUse:Bash" || attachment.Extra["hook_event"] != "PostToolUse" || attachment.Extra["tool_use_id"] != "call_1" {
		t.Fatalf("unexpected hook-stopped continuation metadata: %#v", attachment.Extra)
	}
}

func TestFormatToolErrorShortUnchanged(t *testing.T) {
	msg := "short error"
	if got := formatToolError(msg); got != msg {
		t.Errorf("expected short error unchanged, got %q", got)
	}
}

func TestFormatToolErrorExactlyAtLimit(t *testing.T) {
	msg := strings.Repeat("x", maxToolErrorLength)
	if got := formatToolError(msg); got != msg {
		t.Errorf("expected message at limit unchanged, len(got)=%d", len(got))
	}
}

func TestFormatToolErrorTruncatesLong(t *testing.T) {
	msg := strings.Repeat("A", 7000) + strings.Repeat("B", 7000) + strings.Repeat("C", 6000)
	got := formatToolError(msg)

	if len(got) >= len(msg) {
		t.Fatalf("expected truncated output shorter than input, got len=%d vs input len=%d", len(got), len(msg))
	}
	// Head is first 5000 chars.
	if !strings.HasPrefix(got, msg[:halfToolErrorLength]) {
		t.Error("expected output to start with first 5000 chars of input")
	}
	// Tail is last 5000 chars.
	if !strings.HasSuffix(got, msg[len(msg)-halfToolErrorLength:]) {
		t.Error("expected output to end with last 5000 chars of input")
	}
	// Truncation notice present.
	if !strings.Contains(got, "characters truncated") {
		t.Error("expected truncation notice in output")
	}
	// Truncated count correct.
	if !strings.Contains(got, "10000 characters truncated") {
		t.Errorf("expected '10000 characters truncated' in output, got truncation section: %q",
			got[halfToolErrorLength:len(got)-halfToolErrorLength])
	}
}

func TestQueryEmptyToolResultInjection(t *testing.T) {
	ctx := context.Background()
	mdl := &singleToolCallModel{toolName: "Bash", args: `{"command":"true"}`}
	maxTurns := 4

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run true"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "", nil // empty result
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	var toolMsg *schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult {
			toolMsg = evt.ToolResultMessage
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool result event")
		return
	}
	if !strings.Contains(toolMsg.Content, "completed with no output") {
		t.Errorf("expected empty result injection, got %q", toolMsg.Content)
	}
}

func TestQueryUnknownToolError(t *testing.T) {
	ctx := context.Background()
	mdl := &singleToolCallModel{toolName: "NonexistentTool", args: `{"x":"y"}`}
	maxTurns := 4
	registry := tools.NewRegistry() // empty registry — no tools registered

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "call tool"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolRegistry: registry,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			t.Fatal("should not execute unknown tool")
			return "", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	var toolMsg *schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult {
			toolMsg = evt.ToolResultMessage
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a tool result event for unknown tool")
		return
	}
	if !strings.Contains(toolMsg.Content, "is not registered") {
		t.Errorf("expected unknown tool error message, got %q", toolMsg.Content)
	}
}

func TestQueryRetiredWorktreeToolsFailBeforeExecution(t *testing.T) {
	for _, toolName := range []string{"EnterWorktree", "ExitWorktree"} {
		t.Run(toolName, func(t *testing.T) {
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			maxTurns := 4
			events, terminal := collectEvents(context.Background(), QueryParams{
				Messages:     []*schema.Message{{Role: schema.User, Content: "replay old tool call"}},
				SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
				QuerySource:  QuerySourceSDK,
				MaxTurns:     &maxTurns,
				ChatModel:    &singleToolCallModel{toolName: toolName, args: `{malformed-old-input`},
				ToolRegistry: registry,
				ToolExecutor: func(context.Context, string, string) (string, error) {
					t.Fatal("retired worktree tool reached executor")
					return "", nil
				},
			})
			if terminal.Reason != TerminalCompleted {
				t.Fatalf("terminal reason = %q, want completed", terminal.Reason)
			}
			var toolMessage *schema.Message
			for _, event := range events {
				if event.Type == EventToolResult {
					toolMessage = event.ToolResultMessage
					break
				}
			}
			if toolMessage == nil || !strings.Contains(toolMessage.Content, "Tool unavailable: "+toolName) {
				t.Fatalf("tool result = %#v, want explicit unavailable", toolMessage)
			}

			engine := &QueryEngine{toolRegistry: registry}
			result, err := engine.toolExecutor(context.Background(), toolName, `{}`)
			if err != nil || !strings.Contains(result, "tool unavailable: "+toolName) {
				t.Fatalf("direct executor result = %q, err = %v", result, err)
			}
		})
	}
}
