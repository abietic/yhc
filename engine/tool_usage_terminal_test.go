package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestQueryToolUsageFailureStopsBeforeNextModel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		stop bool
	}{
		{name: "budget", err: &execution.ProviderUsageTerminalError{Err: execution.ErrRunBudgetExceeded}, stop: true},
		{name: "wrapped unknown", err: fmt.Errorf("lookup: %w", &execution.ProviderUsageTerminalError{Err: execution.ErrRunUsageUnknown}), stop: true},
		{name: "ordinary failure", err: errors.New("local lookup failed")},
		{name: "lookalike text", err: errors.New("provider usage accounting failed closed: run usage budget exceeded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mdl := toolUsageFailureModel()
			var results []*schema.Message
			terminal := Query(t.Context(), QueryParams{
				Messages:     []*schema.Message{{Role: schema.User, Content: "perform lookup"}},
				SystemPrompt: &schema.Message{Role: schema.System, Content: "Local fixture"},
				QuerySource:  QuerySourceSDK,
				ChatModel:    mdl,
				ToolExecutor: func(context.Context, string, string) (string, error) { return "", tc.err },
			}, func(event QueryEvent) {
				if event.Type == EventToolResult {
					results = append(results, event.ToolResultMessage)
				}
			})
			if len(results) != 1 || results[0] == nil || results[0].ToolCallID != "lookup-once" || results[0].Extra["is_error"] != true {
				t.Fatalf("failed tool must still publish exactly one result: %#v", results)
			}
			if tc.stop {
				if mdl.callCount != 1 || terminal.Reason != TerminalModelError || !errors.Is(terminal.Err, tc.err) || !execution.IsProviderUsageTerminalError(terminal.Err) {
					t.Fatalf("calls=%d terminal=%+v", mdl.callCount, terminal)
				}
			} else if mdl.callCount != 2 || terminal.Reason != TerminalCompleted || terminal.Err != nil {
				t.Fatalf("ordinary tool errors must remain recoverable: calls=%d terminal=%+v", mdl.callCount, terminal)
			}
		})
	}
}

func TestQueryToolUsageFailureKeepsCommittedResults(t *testing.T) {
	t.Parallel()
	mdl := toolUsageFailureModel()
	mdl.responses[0].chunks[0].ToolCalls = append([]schema.ToolCall{{
		ID: "local-proof", Type: "function", Function: schema.FunctionCall{Name: "LocalProof", Arguments: "{}"},
	}}, mdl.responses[0].chunks[0].ToolCalls...)
	var dispatched, published []string
	terminal := Query(t.Context(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "perform local check then lookup"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "Local fixture"},
		QuerySource:  QuerySourceSDK,
		ChatModel:    mdl,
		ToolExecutor: func(_ context.Context, name, _ string) (string, error) {
			dispatched = append(dispatched, name)
			if name == "PaidLookup" {
				return "", &execution.ProviderUsageTerminalError{Err: execution.ErrRunBudgetExceeded}
			}
			return "completed local proof", nil
		},
	}, func(event QueryEvent) {
		if event.Type == EventToolResult && event.ToolResultMessage != nil {
			published = append(published, event.ToolResultMessage.ToolCallID)
		}
	})
	if !errors.Is(terminal.Err, execution.ErrRunBudgetExceeded) || mdl.callCount != 1 {
		t.Fatalf("calls=%d terminal=%+v", mdl.callCount, terminal)
	}
	if !reflect.DeepEqual(dispatched, []string{"LocalProof", "PaidLookup"}) || !reflect.DeepEqual(published, []string{"local-proof", "lookup-once"}) {
		t.Fatalf("committed results lost or repeated: dispatched=%v published=%v", dispatched, published)
	}
}

func TestQueryEngineToolUsageFailureCheckpointsBeforeResume(t *testing.T) {
	var executed int
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "PaidLookup"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executed++
			return "", &execution.ProviderUsageTerminalError{Err: execution.ErrRunBudgetExceeded}
		},
	})
	mdl := toolUsageFailureModel()
	cfg := projectGraphEngineConfig(t, t.TempDir(), "tool-usage-checkpoint", mdl, registry, &tools.ToolSelection{})
	cfg.PermissionMode = permission.ModeBypassPermissions
	eng := NewQueryEngine(cfg)
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(t.Context(), "perform lookup")
	var terminal *Terminal
	for event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil || !errors.Is(terminal.Err, execution.ErrRunBudgetExceeded) || mdl.callCount != 1 || executed != 1 {
		t.Fatalf("calls=%d executed=%d terminal=%+v", mdl.callCount, executed, terminal)
	}
	eng.Close()
	// Reopen from durable state with a fresh model. The completed failed call
	// must be paired in history, and cannot be dispatched again on resume.
	resumedModel := &canonicalScriptModel{}
	cfg.ChatModel = resumedModel
	resumed := NewQueryEngine(cfg)
	t.Cleanup(resumed.Close)
	var assistantCalls, toolResults int
	for _, message := range resumed.GetMessages() {
		for _, call := range message.ToolCalls {
			if call.ID == "lookup-once" {
				assistantCalls++
			}
		}
		if message.Role == schema.Tool && message.ToolCallID == "lookup-once" {
			toolResults++
			if message.Extra["is_error"] != true {
				t.Fatal("checkpoint lost failed tool outcome")
			}
		}
	}
	if assistantCalls != 1 || toolResults != 1 {
		t.Fatalf("unpaired checkpoint: assistant calls=%d tool results=%d", assistantCalls, toolResults)
	}
	events, _ = resumed.SubmitMessage(t.Context(), "continue with a new finite allowance")
	for range events {
	}
	if resumedModel.callCount != 1 || executed != 1 {
		t.Fatalf("resume replayed a completed lookup: model=%d tool=%d", resumedModel.callCount, executed)
	}
}

func toolUsageFailureModel() *canonicalScriptModel {
	return &canonicalScriptModel{responses: []canonicalModelResponse{{chunks: []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{ID: "lookup-once", Type: "function", Function: schema.FunctionCall{
			Name: "PaidLookup", Arguments: "{}",
		}}},
	}}}}}
}
