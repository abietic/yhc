package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestPreToolShellHookJSONProtocol(t *testing.T) {
	executor := NewExecutor()
	executor.RegisterShellHooks(&ShellHookConfig{PreToolHooks: []ShellHook{{
		ToolPattern: "Bash",
		Command: shellPrintfJSONCommand(`{
			"continue": false,
			"stopReason": "policy stop",
			"decision": "block",
			"reason": "unsafe command",
			"systemMessage": "reviewed by policy",
			"hookSpecificOutput": {
				"updatedInput": {"command": "go test ./engine/hooks", "description": "safe"},
				"permissionDecision": "ask"
			}
		}`),
	}}})

	result := executor.ExecutePreTool(context.Background(), "Bash", "toolu_1", map[string]any{
		"command": "rm -rf /tmp/nope",
	})

	if !result.PreventContinuation {
		t.Fatal("expected continue=false to prevent continuation")
	}
	if result.StopReason != "policy stop" {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, "policy stop")
	}
	if result.DenyReason != "unsafe command" {
		t.Fatalf("DenyReason = %q, want %q", result.DenyReason, "unsafe command")
	}
	if got := result.UpdatedInput["command"]; got != "go test ./engine/hooks" {
		t.Fatalf("UpdatedInput[command] = %v, want rewritten command", got)
	}
	if got := result.UpdatedInput["description"]; got != "safe" {
		t.Fatalf("UpdatedInput[description] = %v, want %q", got, "safe")
	}
	if result.PermissionBehavior != HookPermissionAsk {
		t.Fatalf("PermissionBehavior = %q, want %q", result.PermissionBehavior, HookPermissionAsk)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Content != "reviewed by policy" {
		t.Fatalf("Attachments = %#v, want hook system message", result.Attachments)
	}
	if got := result.Attachments[0].Extra["attachment_kind"]; got != "hook_system_message" {
		t.Fatalf("attachment_kind = %v, want hook_system_message", got)
	}
}

func TestPreToolShellHookPermissionDecisionPrecedence(t *testing.T) {
	executor := NewExecutor()
	executor.RegisterPreTool(func(context.Context, string, string, map[string]any) *PreToolHookResult {
		return &PreToolHookResult{PermissionBehavior: HookPermissionAllow}
	})
	executor.RegisterShellHooks(&ShellHookConfig{PreToolHooks: []ShellHook{
		{ToolPattern: "Bash", Command: shellPrintfJSONCommand(`{"hookSpecificOutput":{"permissionDecision":"ask"}}`)},
		{ToolPattern: "Bash", Command: shellPrintfJSONCommand(`{"hookSpecificOutput":{"permissionDecision":"deny"}}`)},
	}})

	result := executor.ExecutePreTool(context.Background(), "Bash", "toolu_1", map[string]any{"command": "pwd"})
	if result.PermissionBehavior != HookPermissionDeny {
		t.Fatalf("PermissionBehavior = %q, want deny precedence over ask/allow", result.PermissionBehavior)
	}
	if result.DenyReason != "denied by hook" {
		t.Fatalf("DenyReason = %q, want default permission denial reason", result.DenyReason)
	}
}

func TestPreToolShellHooksPatternAndConditionFiltering(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		input     map[string]any
		hooks     []ShellHook
		wantLines []string
	}{
		{
			name:     "tool patterns and command conditions",
			toolName: "Bash",
			input:    map[string]any{"command": "go test ./engine/hooks"},
			hooks: []ShellHook{
				{ToolPattern: "Bash", Command: shellEchoCommand("exact")},
				{ToolPattern: "Write|Edit", Command: shellEchoCommand("pipe-miss")},
				{ToolPattern: "Ba*", Command: shellEchoCommand("prefix-glob")},
				{ToolPattern: "*sh", Command: shellEchoCommand("suffix-glob")},
				{ToolPattern: "*", If: &ShellHookCondition{ToolName: "Bash|Read"}, Command: shellEchoCommand("tool-name-condition")},
				{ToolPattern: "*", If: &ShellHookCondition{CommandPattern: `go test`}, Command: shellEchoCommand("command-condition")},
				{ToolPattern: "*", If: &ShellHookCondition{CommandPattern: `npm test`}, Command: shellEchoCommand("command-miss")},
				{ToolPattern: "*", If: &ShellHookCondition{FilePattern: `\.go$`}, Command: shellEchoCommand("file-miss")},
			},
			wantLines: []string{"exact", "prefix-glob", "suffix-glob", "tool-name-condition", "command-condition"},
		},
		{
			name:     "pipe pattern and file condition",
			toolName: "Read",
			input:    map[string]any{"file_path": "/tmp/main.go"},
			hooks: []ShellHook{
				{ToolPattern: "Read|Write", Command: shellEchoCommand("pipe-match")},
				{ToolPattern: "*", If: &ShellHookCondition{FilePattern: `\.go$`}, Command: shellEchoCommand("file-condition")},
				{ToolPattern: "*", If: &ShellHookCondition{ToolName: "Bash"}, Command: shellEchoCommand("tool-name-miss")},
			},
			wantLines: []string{"pipe-match", "file-condition"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := RunPreToolHooks(context.Background(), &ShellHookConfig{PreToolHooks: tc.hooks}, tc.toolName, tc.input)
			if err != nil {
				t.Fatalf("RunPreToolHooks returned error: %v", err)
				return
			}
			var got []string
			for _, result := range results {
				got = append(got, strings.TrimSpace(result.Stdout))
			}
			if !reflect.DeepEqual(got, tc.wantLines) {
				t.Fatalf("executed hooks = %#v, want %#v", got, tc.wantLines)
			}
		})
	}
}

func TestPreToolShellHookExitCode2Blocks(t *testing.T) {
	executor := NewExecutor()
	executor.RegisterShellHooks(&ShellHookConfig{PreToolHooks: []ShellHook{{
		ToolPattern: "Bash",
		Command:     "printf 'blocked by shell policy' >&2; exit 2",
	}}})

	result := executor.ExecutePreTool(context.Background(), "Bash", "toolu_1", map[string]any{"command": "rm file"})
	if result.DenyReason != "blocked by shell policy" {
		t.Fatalf("DenyReason = %q, want stderr from exit-code-2 hook", result.DenyReason)
	}
}

func TestPostToolShellHookRewritesResult(t *testing.T) {
	executor := NewExecutor()
	executor.RegisterShellHooks(&ShellHookConfig{PostToolHooks: []ShellHook{{
		ToolPattern: "Read",
		Command: shellPrintfJSONCommand(`{
			"systemMessage": "post processed",
			"hookSpecificOutput": {"updatedMCPToolOutput": "rewritten output"}
		}`),
	}}})

	result := executor.ExecutePostTool(context.Background(), "Read", "toolu_1", map[string]any{"file_path": "a.txt"}, "original output")
	if !result.ReplaceResult {
		t.Fatal("expected updatedMCPToolOutput to request result replacement")
	}
	if result.UpdatedResult != "rewritten output" {
		t.Fatalf("UpdatedResult = %q, want rewritten output", result.UpdatedResult)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Content != "post processed" {
		t.Fatalf("Attachments = %#v, want post-tool system message", result.Attachments)
	}
}

func TestUserPromptShellHooksRewriteContextAndReject(t *testing.T) {
	t.Run("rewrite prompt and add context", func(t *testing.T) {
		executor := NewExecutor()
		executor.RegisterShellHooks(&ShellHookConfig{UserPromptHooks: []ShellHook{{
			Command: shellPrintfJSONCommand(`{
				"systemMessage": "prompt checked",
				"hookSpecificOutput": {
					"updatedInput": {"prompt": "rewritten prompt"},
					"additionalContext": "extra policy context"
				}
			}`),
		}}})

		result := executor.ExecuteUserPromptSubmit(context.Background(), "original prompt")
		if result.Reject {
			t.Fatalf("Reject = true, want prompt accepted")
		}
		if result.UpdatedPrompt != "rewritten prompt" {
			t.Fatalf("UpdatedPrompt = %q, want rewritten prompt", result.UpdatedPrompt)
		}
		if result.AdditionalContext != "extra policy context" {
			t.Fatalf("AdditionalContext = %q, want extra context", result.AdditionalContext)
		}
		if len(result.Attachments) != 1 || result.Attachments[0].Content != "prompt checked" {
			t.Fatalf("Attachments = %#v, want prompt system message", result.Attachments)
		}
	})

	t.Run("reject with decision block", func(t *testing.T) {
		executor := NewExecutor()
		executor.RegisterShellHooks(&ShellHookConfig{UserPromptHooks: []ShellHook{{
			Command: shellPrintfJSONCommand(`{"decision":"block","reason":"prompt rejected"}`),
		}}})

		result := executor.ExecuteUserPromptSubmit(context.Background(), "secret prompt")
		if !result.Reject {
			t.Fatal("Reject = false, want rejection")
		}
		if result.RejectReason != "prompt rejected" {
			t.Fatalf("RejectReason = %q, want prompt rejected", result.RejectReason)
		}
	})
}

func TestAsyncPreToolShellHookFiresAndDoesNotContributeResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "async.out")
	config := &ShellHookConfig{PreToolHooks: []ShellHook{{
		ToolPattern: "Bash",
		Async:       true,
		Command:     fmt.Sprintf("sleep 0.05; printf async > %s", shellQuote(path)),
	}}}

	results, err := RunPreToolHooks(context.Background(), config, "Bash", map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("RunPreToolHooks returned error: %v", err)
		return
	}
	if len(results) != 0 {
		t.Fatalf("async hook contributed %d result(s), want none", len(results))
	}

	if !eventually(2*time.Second, 10*time.Millisecond, func() bool {
		data, err := os.ReadFile(path)
		return err == nil && string(data) == "async"
	}) {
		data, _ := os.ReadFile(path)
		t.Fatalf("async hook did not write expected file content; got %q", string(data))
	}
}

func TestHookStatusEmitterInvokedAroundShellHook(t *testing.T) {
	previous := HookStatusEmitter
	defer func() { HookStatusEmitter = previous }()

	var events []string
	HookStatusEmitter = func(hookCommand, statusMessage, phase string) {
		events = append(events, fmt.Sprintf("%s|%s|%s", hookCommand, statusMessage, phase))
	}

	hook := ShellHook{
		ToolPattern:   "Bash",
		Command:       shellPrintfJSONCommand(`{}`),
		StatusMessage: "checking policy",
	}
	_, err := RunPreToolHooks(context.Background(), &ShellHookConfig{PreToolHooks: []ShellHook{hook}}, "Bash", map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("RunPreToolHooks returned error: %v", err)
		return
	}

	want := []string{
		fmt.Sprintf("%s|checking policy|running", hook.Command),
		fmt.Sprintf("%s|checking policy|completed", hook.Command),
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("status events = %#v, want %#v", events, want)
	}
}

func TestLifecycleHookAggregation(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterSessionStart(func(context.Context, string, bool) *SessionStartHookResult {
		return &SessionStartHookResult{Attachments: []*schema.Message{{Role: schema.User, Content: "session-a"}}}
	})
	executor.RegisterSessionStart(func(context.Context, string, bool) *SessionStartHookResult {
		return &SessionStartHookResult{Attachments: []*schema.Message{{Role: schema.User, Content: "session-b"}}, SkipDefault: true}
	})
	sessionStart := executor.ExecuteSessionStart(ctx, "session-1", true)
	if !sessionStart.SkipDefault || messageContents(sessionStart.Attachments) != "session-a,session-b" {
		t.Fatalf("sessionStart = %#v, want aggregated attachments and SkipDefault", sessionStart)
	}

	executor.RegisterPreCompact(func(context.Context, int, int) *PreCompactHookResult {
		return &PreCompactHookResult{MemoryEntries: []string{"keep-a"}}
	})
	executor.RegisterPreCompact(func(context.Context, int, int) *PreCompactHookResult {
		return &PreCompactHookResult{MemoryEntries: []string{"keep-b"}, Cancel: true}
	})
	preCompact := executor.ExecutePreCompact(ctx, 10, 1000)
	if !preCompact.Cancel || !reflect.DeepEqual(preCompact.MemoryEntries, []string{"keep-a", "keep-b"}) {
		t.Fatalf("preCompact = %#v, want aggregated memory and cancel", preCompact)
	}

	executor.RegisterPostCompact(func(context.Context, int, int) *PostCompactHookResult {
		return &PostCompactHookResult{Attachments: []*schema.Message{{Role: schema.User, Content: "compact-a"}}}
	})
	executor.RegisterPostCompact(func(context.Context, int, int) *PostCompactHookResult {
		return &PostCompactHookResult{Attachments: []*schema.Message{{Role: schema.User, Content: "compact-b"}}}
	})
	postCompact := executor.ExecutePostCompact(ctx, 1000, 400)
	if messageContents(postCompact.Attachments) != "compact-a,compact-b" {
		t.Fatalf("postCompact attachments = %q, want both attachments", messageContents(postCompact.Attachments))
	}

	var commandCalls []string
	executor.RegisterCommand(func(context.Context, string, string) *CommandHookResult {
		commandCalls = append(commandCalls, "first")
		return &CommandHookResult{Output: "intermediate"}
	})
	executor.RegisterCommand(func(context.Context, string, string) *CommandHookResult {
		commandCalls = append(commandCalls, "second")
		return &CommandHookResult{Output: "handled", Handled: true}
	})
	executor.RegisterCommand(func(context.Context, string, string) *CommandHookResult {
		commandCalls = append(commandCalls, "third")
		return &CommandHookResult{Output: "should not run", Handled: true}
	})
	command := executor.ExecuteCommand(ctx, "help", "")
	if !command.Handled || command.Output != "handled" || !reflect.DeepEqual(commandCalls, []string{"first", "second"}) {
		t.Fatalf("command = %#v calls=%#v, want first handled hook to stop aggregation", command, commandCalls)
	}

	executor.RegisterTurnStart(func(context.Context, int, string) *TurnStartHookResult {
		return &TurnStartHookResult{Attachments: []*schema.Message{{Role: schema.User, Content: "turn-a"}}}
	})
	executor.RegisterTurnStart(func(context.Context, int, string) *TurnStartHookResult {
		return &TurnStartHookResult{Attachments: []*schema.Message{{Role: schema.User, Content: "turn-b"}}}
	})
	turnStart := executor.ExecuteTurnStart(ctx, 2, "hello")
	if messageContents(turnStart.Attachments) != "turn-a,turn-b" {
		t.Fatalf("turnStart attachments = %q, want both attachments", messageContents(turnStart.Attachments))
	}

	var observed []string
	executor.RegisterSessionEnd(func(_ context.Context, sessionID, reason string) {
		observed = append(observed, "sessionEnd:"+sessionID+":"+reason)
	})
	executor.RegisterNotification(func(_ context.Context, level, message string, data map[string]any) {
		observed = append(observed, fmt.Sprintf("notification:%s:%s:%v", level, message, data["code"]))
	})
	executor.RegisterTurnEnd(func(_ context.Context, turnNumber int, assistantMessage string) {
		observed = append(observed, fmt.Sprintf("turnEnd:%d:%s", turnNumber, assistantMessage))
	})
	executor.ExecuteSessionEnd(ctx, "session-1", "done")
	executor.ExecuteNotification(ctx, "info", "ready", map[string]any{"code": 7})
	executor.ExecuteTurnEnd(ctx, 3, "assistant done")
	wantObserved := []string{"sessionEnd:session-1:done", "notification:info:ready:7", "turnEnd:3:assistant done"}
	if !reflect.DeepEqual(observed, wantObserved) {
		t.Fatalf("observed lifecycle callbacks = %#v, want %#v", observed, wantObserved)
	}
}

func TestStopHooksExtractMemoriesAndContinueOnToolError(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "We will use focused hook tests for this phase."},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			Function: schema.FunctionCall{Name: "Write", Arguments: `{"file_path":"engine/hooks/hooks_integration_test.go"}`},
		}}},
		{Role: schema.Tool, ToolName: "Bash", Content: "--- FAIL: TestHooks (0.01s)\nError: command exited with status 1\nFAIL\tgithub.com/abietic/yhc/engine/hooks\t0.01s"},
		{Role: schema.Tool, ToolName: "Bash", Content: "ok\tgithub.com/abietic/yhc/engine/hooks\t0.02s\nPASS\n1 passed"},
	}

	result, err := RunStopHooks(context.Background(), &StopHookContext{
		Reason:        StopReasonToolError,
		Messages:      messages,
		TurnCount:     3,
		ModelName:     "test-model",
		SessionID:     "session-1",
		FinalResponse: "There are remaining next steps after that.",
	})
	if err != nil {
		t.Fatalf("RunStopHooks returned error: %v", err)
		return
	}
	if !result.ShouldContinue {
		t.Fatal("ShouldContinue = false, want tool error retry")
	}
	if !strings.Contains(result.ContinuationPrompt, "previous tool call resulted in an error") {
		t.Fatalf("ContinuationPrompt = %q, want tool-error retry guidance", result.ContinuationPrompt)
	}

	memories := memoryContents(result.MemoryEntries)
	for _, want := range []string{
		"File modified: engine/hooks/hooks_integration_test.go (via Write)",
		"Test failure: --- FAIL: TestHooks (0.01s)",
		"Tests passed",
		"Decision: We will use focused hook tests for this phase.",
		"Error encountered: command exited with status 1",
	} {
		if !containsString(memories, want) {
			t.Fatalf("memories = %#v, missing %q", memories, want)
		}
	}

	for _, want := range []string{
		"Run tests to verify the changes",
		"Explain what went wrong and suggest a fix",
		"Continue with the remaining tasks",
		"Fix any failing tests",
	} {
		if !containsString(result.PromptSuggestions, want) {
			t.Fatalf("PromptSuggestions = %#v, missing %q", result.PromptSuggestions, want)
		}
	}
}

func shellPrintfJSONCommand(jsonText string) string {
	return fmt.Sprintf("printf %%s %s", shellQuote(strings.TrimSpace(jsonText)))
}

func shellEchoCommand(line string) string {
	return fmt.Sprintf("printf '%%s\\n' %s", shellQuote(line))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func eventually(timeout, interval time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(interval)
	}
	return check()
}

func messageContents(messages []*schema.Message) string {
	contents := make([]string, 0, len(messages))
	for _, msg := range messages {
		contents = append(contents, msg.Content)
	}
	return strings.Join(contents, ",")
}

func memoryContents(entries []MemoryEntry) []string {
	contents := make([]string, 0, len(entries))
	for _, entry := range entries {
		contents = append(contents, entry.Content)
	}
	return contents
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestShellHookOutcomeMapping(t *testing.T) {
	cases := []struct {
		name   string
		config *ShellHookConfig
		act    func(*Executor) *ShellHookOutcomeResult
		assert func(*testing.T, *ShellHookOutcomeResult)
	}{
		{
			name: "ExecutePreTool_NonBlockingErrorThenSuccess",
			config: &ShellHookConfig{PreToolHooks: []ShellHook{
				{ToolPattern: "Bash", Command: "printf 'non-fatal warning' >&2; exit 1"},
				{ToolPattern: "Bash", Command: shellPrintfJSONCommand(`{"hookSpecificOutput":{"updatedInput":{"second_hook_ran":"yes"}}}`)},
			}},
			act: func(executor *Executor) *ShellHookOutcomeResult {
				return &ShellHookOutcomeResult{
					PreToolResult: executor.ExecutePreTool(context.Background(), "Bash", "toolu_1", map[string]any{"command": "pwd"}),
				}
			},
			assert: func(t *testing.T, r *ShellHookOutcomeResult) {
				result := r.PreToolResult
				if result.DenyReason != "" {
					t.Fatalf("DenyReason = %q, want empty", result.DenyReason)
				}
				if result.PreventContinuation {
					t.Fatal("PreventContinuation = true, want false")
				}
				if got := result.UpdatedInput["second_hook_ran"]; got != "yes" {
					t.Fatalf("second hook did not run: UpdatedInput[second_hook_ran] = %v, want %q", got, "yes")
				}
				errs := attachmentsByKind(result.Attachments, "hook_non_blocking_error")
				if len(errs) != 1 {
					t.Fatalf("hook_non_blocking_error attachments = %d, want exactly 1", len(errs))
				}
				if errs[0].Content != "non-fatal warning" {
					t.Fatalf("attachment content = %q, want %q", errs[0].Content, "non-fatal warning")
				}
			},
		},
		{
			name: "ExecutePostTool_ExitCode2Blocking",
			config: &ShellHookConfig{PostToolHooks: []ShellHook{{
				ToolPattern: "Read",
				Command:     "printf 'post tool blocked' >&2; exit 2",
			}}},
			act: func(executor *Executor) *ShellHookOutcomeResult {
				return &ShellHookOutcomeResult{
					PostToolResult: executor.ExecutePostTool(context.Background(), "Read", "toolu_1", map[string]any{"file_path": "a.txt"}, "result"),
				}
			},
			assert: func(t *testing.T, r *ShellHookOutcomeResult) {
				result := r.PostToolResult
				if !result.PreventContinuation {
					t.Fatal("PreventContinuation = false, want true")
				}
				if result.StopReason != "post tool blocked" {
					t.Fatalf("StopReason = %q, want %q", result.StopReason, "post tool blocked")
				}
			},
		},
		{
			name: "ExecuteUserPromptSubmit_ContinueFalse",
			config: &ShellHookConfig{UserPromptHooks: []ShellHook{{
				Command: shellPrintfJSONCommand(`{"continue":false,"stopReason":"prompt policy stop"}`),
			}}},
			act: func(executor *Executor) *ShellHookOutcomeResult {
				return &ShellHookOutcomeResult{
					UserPromptSubmitResult: executor.ExecuteUserPromptSubmit(context.Background(), "hello"),
				}
			},
			assert: func(t *testing.T, r *ShellHookOutcomeResult) {
				result := r.UserPromptSubmitResult
				if !result.Reject {
					t.Fatal("Reject = false, want true")
				}
				if result.RejectReason != "prompt policy stop" {
					t.Fatalf("RejectReason = %q, want %q", result.RejectReason, "prompt policy stop")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := NewExecutor()
			executor.RegisterShellHooks(tc.config)
			tc.assert(t, tc.act(executor))
		})
	}
}

// ShellHookOutcomeResult is a small union carrier for the table-driven outcome
// mapping tests above. Only the field relevant to the scenario under test is set.
type ShellHookOutcomeResult struct {
	PreToolResult          *PreToolHookResult
	PostToolResult         *PostToolHookResult
	UserPromptSubmitResult *UserPromptSubmitHookResult
}

func attachmentsByKind(messages []*schema.Message, kind string) []*schema.Message {
	var matches []*schema.Message
	for _, msg := range messages {
		if msg.Extra["attachment_kind"] == kind {
			matches = append(matches, msg)
		}
	}
	return matches
}
