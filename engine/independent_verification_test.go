package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func verificationResponse(text string) canonicalModelResponse {
	return canonicalModelResponse{chunks: []*schema.Message{{Role: schema.Assistant, Content: text, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 2}}}}}
}

const (
	verificationPass = `{"verdict":"PASS","coverage_complete":true,"checks":[{"requirement":"original contract","tool_call_id":"verify-command","command":"independent check","expected":"ok","observed":"ok","status":"PASS"}]}`
	verificationFail = `{"verdict":"FAIL","coverage_complete":true,"checks":[{"requirement":"original contract","tool_call_id":"verify-command","command":"independent check","expected":"ok","observed":"broken","status":"FAIL"}]}`
)

func TestIndependentVerificationGate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		config    IndependentVerificationConfig
		limit     int64
		responses []string
		calls     uint64
		failed    bool
	}{
		{name: "default-off", responses: []string{"done"}, calls: 1},
		{name: "pass", config: IndependentVerificationConfig{MaxTurns: 2}, limit: 5, responses: []string{"done", verificationPass}, calls: 3},
		{name: "fail-no-repair", config: IndependentVerificationConfig{MaxTurns: 2}, limit: 5, responses: []string{"done", verificationFail}, calls: 3, failed: true},
		{name: "repair-and-recheck", config: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}, limit: 8, responses: []string{"done", verificationFail, "repaired", verificationPass}, calls: 6},
		{name: "repair-limit", config: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}, limit: 8, responses: []string{"done", verificationFail, "repaired", verificationFail}, calls: 6, failed: true},
		{name: "partial", config: IndependentVerificationConfig{MaxTurns: 2}, limit: 5, responses: []string{"done", `{"verdict":"PARTIAL","coverage_complete":false,"checks":[],"missing":["blocked concurrency check"]}`}, calls: 2, failed: true},
		{name: "malformed", config: IndependentVerificationConfig{MaxTurns: 1}, limit: 5, responses: []string{"done", "PASS"}, calls: 2, failed: true},
		{name: "shared-budget", config: IndependentVerificationConfig{MaxTurns: 2}, limit: 1, responses: []string{"done"}, calls: 1, failed: true},
		{name: "unbounded-rejected", config: IndependentVerificationConfig{MaxTurns: 2}, responses: nil, calls: 0, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: tc.limit})
			mdl := &canonicalScriptModel{}
			for _, r := range tc.responses {
				if r == verificationPass || r == verificationFail {
					tool := verificationResponse("")
					tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
					mdl.responses = append(mdl.responses, tool)
				}
				mdl.responses = append(mdl.responses, verificationResponse(r))
			}
			_, terminal := collectEvents(context.Background(), QueryParams{Messages: []*schema.Message{{Role: schema.User, Content: "original contract"}}, ChatModel: mdl, ToolRegistry: registry, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage}, ToolExecutor: func(context.Context, string, string) (string, error) { return "observed evidence", nil }, IndependentVerification: tc.config})
			if (terminal.Err != nil) != tc.failed || usage.Snapshot().ProviderCalls != tc.calls {
				t.Fatalf("terminal=%+v usage=%+v", terminal, usage.Snapshot())
			}
			if tc.name == "fail-no-repair" && !errors.Is(terminal.Err, ErrIndependentVerification) {
				t.Fatal(terminal.Err)
			}
		})
	}
}

func TestIndependentVerificationReportFailsClosed(t *testing.T) {
	for _, text := range []string{`{"verdict":"PASS","coverage_complete":true,"checks":[]}`, strings.Replace(verificationPass, `"status":"PASS"`, `"status":"FAIL"`, 1), verificationPass + verificationPass, strings.Replace(verificationPass, `"tool_call_id":"verify-command","command":"independent check"`, `"command":""`, 1)} {
		if _, err := parseIndependentVerificationReport(text); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}

func TestIndependentVerificationRuntimeOwnedCommandReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, id, command string
		background        bool
		toolFailed        bool
		largeCommand      bool
		failed            bool
	}{
		{name: "runtime-command", id: "verify-command"},
		{name: "legacy-exact-command", id: "verify-command", command: "independent check"},
		{name: "unknown-receipt", id: "solver-old-command", failed: true},
		{name: "command-mismatch", id: "verify-command", command: "invented check", failed: true},
		{name: "background-launch", id: "verify-command", background: true, failed: true},
		{name: "failed-tool", id: "verify-command", toolFailed: true, failed: true},
		{name: "resolved-report-bound", id: "verify-command", largeCommand: true, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
			tool := verificationResponse("")
			command := "independent check"
			if tc.largeCommand {
				command = strings.Repeat("x", 128*1024)
			}
			args, _ := json.Marshal(map[string]any{"command": command, "run_in_background": tc.background})
			tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: string(args)}}}
			report := strings.Replace(verificationPass, `"tool_call_id":"verify-command"`, `"tool_call_id":"`+tc.id+`"`, 1)
			if tc.command == "" {
				report = strings.Replace(report, `,"command":"independent check"`, "", 1)
			} else {
				report = strings.Replace(report, `"command":"independent check"`, `"command":"`+tc.command+`"`, 1)
			}
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, verificationResponse(report)}}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			params := QueryParams{Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2}, ToolExecutor: func(context.Context, string, string) (string, error) {
				if tc.toolFailed {
					return "", errors.New("tool failed")
				}
				return "observed evidence", nil
			}}
			params.Deps = &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				if opts.QuerySource == "independent_verification" && opts.ToolChoice == "none" && !tc.background && !tc.toolFailed && !strings.Contains(messages[len(messages)-1].Content, `"tool_call_id":"verify-command"`) {
					t.Error("final report round lacks runtime-owned command receipts")
				}
				return execution.CallModel(ctx, chatModel, messages, system, infos, opts)
			}}
			events, terminal := collectEvents(t.Context(), params)
			if (terminal.Err != nil) != tc.failed || usage.Snapshot().ProviderCalls != 3 {
				t.Fatalf("terminal=%+v usage=%+v", terminal, usage.Snapshot())
			}
			if tc.failed {
				return
			}
			found := false
			for _, e := range events {
				if e.AttachmentMessage != nil && e.AttachmentMessage.Extra["attachment_kind"] == "independent_verification" {
					found = strings.Contains(e.AttachmentMessage.Content, `"command":"independent check"`)
				}
			}
			if !found {
				t.Error("accepted report lacks canonical executed command")
			}
		})
	}
}

func TestVerificationReceiptCatalogIsBoundedAndInvocationLocal(t *testing.T) {
	commands := map[string]string{"b": "second", "a": "first", "pending": "not finished", "failed": "not evidence"}
	results := map[string]*schema.Message{"a": {ToolName: "Bash", Content: "private stdout"}, "b": {ToolName: "Bash"}, "failed": {ToolName: "Bash", Extra: map[string]any{"is_error": true}}, "old-solver": {}}
	value := verificationReceiptCatalog(commands, results)
	want := `[{"tool_call_id":"a","command_preview":"first"},{"tool_call_id":"b","command_preview":"second"}]`
	if value != want {
		t.Fatalf("catalog = %s", value)
	}
	for i := range 200 {
		id := "unicode-" + strconv.Itoa(i)
		commands[id] = strings.Repeat("验证", 1000)
		results[id] = &schema.Message{ToolName: "Bash"}
	}
	value = verificationReceiptCatalog(commands, results)
	var receipts []struct {
		ID      string `json:"tool_call_id"`
		Preview string `json:"command_preview"`
	}
	if len(value) > 16*1024 || json.Unmarshal([]byte(value), &receipts) != nil || len(receipts) > 128 || len(receipts) < 2 {
		t.Fatal("receipt catalog is unbounded or malformed")
	}
	for _, receipt := range receipts {
		if len([]rune(receipt.Preview)) > 256 {
			t.Fatal("command preview is unbounded")
		}
	}
}

func TestIndependentVerificationRejectsReusedReceiptIDs(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	bash := verificationResponse("")
	bash.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	read := verificationResponse("")
	read.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/receipt-fixture"}`}}}
	report := strings.Replace(verificationPass, `,"command":"independent check"`, "", 1)
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), bash, read, verificationResponse(report)}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	executions := 0
	_, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage}, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 3}, ToolExecutor: func(context.Context, string, string) (string, error) {
		executions++
		return "observed evidence", nil
	}})
	if !errors.Is(terminal.Err, ErrIndependentVerification) || executions != 1 || usage.Snapshot().ProviderCalls != 4 {
		t.Fatalf("mixed reused-ID evidence accepted: terminal=%+v executions=%d usage=%+v", terminal, executions, usage.Snapshot())
	}
}

func TestIndependentVerificationOriginalContextAndToolBoundary(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 8})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("private solver claims")}}
	// The verifier attempts a prohibited write. Even if the model hallucinates a
	// PASS, no operation and no executable evidence can authorize completion.
	bad := verificationResponse("")
	bad.chunks[0].ToolCalls = []schema.ToolCall{{ID: "forbidden", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{"file_path":"/tmp/forbidden","content":"bad"}`}}}
	mdl.responses = append(mdl.responses, bad, verificationResponse(verificationPass))
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	executions := 0
	verifierCalls := 0
	params := QueryParams{Messages: []*schema.Message{{Role: schema.User, Content: "ORIGINAL immutable request"}, {Role: schema.Assistant, Content: "private old solver history"}}, SystemPrompt: &schema.Message{Role: schema.System, Content: "private solver system"}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2}, ToolExecutor: func(context.Context, string, string) (string, error) { executions++; return "", nil }}
	params.Deps = &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
		if opts.QuerySource == "independent_verification" {
			verifierCalls++
			text := ""
			for _, m := range messages {
				text += m.Content
			}
			if !strings.Contains(text, "ORIGINAL immutable request") || strings.Contains(text, "private old solver history") || strings.Contains(text, "private solver claims") || strings.Contains(system.Content, "private solver system") {
				t.Errorf("verifier received contaminated or missing context")
			}
		}
		return execution.CallModel(ctx, chatModel, messages, system, infos, opts)
	}}
	_, terminal := collectEvents(context.Background(), params)
	if terminal.Err == nil || executions != 0 || verifierCalls != 2 {
		t.Fatalf("terminal=%+v executions=%d verifier=%d", terminal, executions, verifierCalls)
	}
}

type cancelledVerificationModel struct {
	canonicalScriptModel
	entered chan struct{}
}

func (m *cancelledVerificationModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	for _, message := range input {
		if message.Role == schema.System && strings.Contains(message.Content, "You independently verify") {
			close(m.entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return m.canonicalScriptModel.Stream(ctx, input, opts...)
}

func TestIndependentVerificationCancellation(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	mdl := &cancelledVerificationModel{canonicalScriptModel: canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done")}}, entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan Terminal, 1)
	go func() {
		_, terminal := collectEvents(ctx, QueryParams{Messages: []*schema.Message{{Role: schema.User, Content: "original contract"}}, ChatModel: mdl, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage}, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}})
		done <- terminal
	}()
	select {
	case <-mdl.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("verifier did not start")
	}
	cancel()
	select {
	case terminal := <-done:
		if !errors.Is(terminal.Err, context.Canceled) {
			t.Fatalf("terminal=%+v", terminal)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("verifier cancellation did not return")
	}
	if usage.Snapshot().ProviderCalls > 2 {
		t.Fatal("cancelled verifier triggered a repair or another request")
	}
}

func TestIndependentVerificationPreservesStopHookAuthority(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done")}}
	hook := hooks.NewExecutor()
	hook.RegisterStop(func([]*schema.Message, []*schema.Message, bool) *hooks.StopHookResult {
		return &hooks.StopHookResult{PreventContinuation: true}
	})
	_, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{{Role: schema.User, Content: "original task"}}, ChatModel: mdl, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage}, HookExecutor: hook, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2}})
	if terminal.Reason != TerminalStopHookPrevented || usage.Snapshot().ProviderCalls != 1 {
		t.Fatalf("terminal=%+v usage=%+v", terminal, usage.Snapshot())
	}
}

func TestIndependentVerificationDoesNotBypassToolHooks(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 5})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done")}}
	tool := verificationResponse("")
	tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	mdl.responses = append(mdl.responses, tool, verificationResponse(verificationPass))
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	hook := hooks.NewExecutor()
	denials := 0
	executions := 0
	hook.RegisterPreTool(func(context.Context, string, string, map[string]any) *hooks.PreToolHookResult {
		denials++
		return &hooks.PreToolHookResult{DenyReason: "project policy"}
	})
	_, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{{Role: schema.User, Content: "original contract"}}, ChatModel: mdl, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage}, ToolRegistry: registry, ToolExecutor: func(context.Context, string, string) (string, error) { executions++; return "", nil }, HookExecutor: hook, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2}})
	if terminal.Err == nil || executions != 0 || denials != 1 {
		t.Fatalf("terminal=%+v executions=%d denials=%d", terminal, executions, denials)
	}
}

func TestIndependentVerificationCannotCompleteThroughAPIErrorBypass(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	response := verificationResponse("provider API error")
	response.chunks[0].Extra = map[string]any{"api_error": true}
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{response}}
	_, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{{Role: schema.User, Content: "original task"}}, ChatModel: mdl, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage}, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2}})
	if terminal.Reason == TerminalCompleted || !errors.Is(terminal.Err, ErrIndependentVerification) || usage.Snapshot().ProviderCalls != 1 {
		t.Fatalf("terminal=%+v usage=%+v", terminal, usage.Snapshot())
	}
}

func TestIndependentVerificationReservesFinalRoundForReport(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	tool := verificationResponse("")
	tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	secondTool := verificationResponse("")
	secondTool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command-2", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, secondTool, verificationResponse(verificationPass)}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	verifierCalls := 0
	params := QueryParams{Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 3}, ToolExecutor: func(context.Context, string, string) (string, error) { return "observed evidence", nil }}
	params.Deps = &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
		if opts.QuerySource == "independent_verification" {
			verifierCalls++
			last := messages[len(messages)-1]
			if !strings.Contains(last.Content, "verification round") || last.Extra["is_meta"] != true {
				t.Error("checker cannot see its bounded remaining rounds")
			}
			if verifierCalls == 3 && opts.ToolChoice != "none" {
				t.Error("last admitted round must request a report without tools")
			}
		}
		return execution.CallModel(ctx, chatModel, messages, system, infos, opts)
	}}
	_, terminal := collectEvents(t.Context(), params)
	if terminal.Err != nil || verifierCalls != 3 || usage.Snapshot().ProviderCalls != 4 {
		t.Fatalf("terminal=%+v checker=%d usage=%+v", terminal, verifierCalls, usage.Snapshot())
	}
}

func TestIndependentVerificationRejectsToolsInFinalReportRound(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
	tool := verificationResponse("")
	tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, tool}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	executions := 0
	_, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage}, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2}, ToolExecutor: func(context.Context, string, string) (string, error) { executions++; return "observed evidence", nil }})
	if terminal.Err == nil || executions != 1 || usage.Snapshot().ProviderCalls != 3 {
		t.Fatalf("terminal=%+v executions=%d usage=%+v", terminal, executions, usage.Snapshot())
	}
}
