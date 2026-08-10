package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type callModelOptionsModel struct{}

func (m *callModelOptionsModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *callModelOptionsModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryPassesExpandedCallModelOptions(t *testing.T) {
	ctx := context.Background()
	abortCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	toolCtx := &ToolUseContext{
		AbortController: &AbortController{Ctx: abortCtx, Cancel: cancel},
		Options: &ToolUseOptions{
			MainLoopModel:           "main-model",
			ThinkingConfig:          &ThinkingConfig{Type: "enabled"},
			ToolChoice:              "forced",
			ForcedToolName:          "Bash",
			IsNonInteractiveSession: true,
			Tools: []*schema.ToolInfo{{
				Name: "Bash",
			}},
		},
	}

	var capturedCtx context.Context
	var capturedOpts execution.CallModelOptions
	terminal := Query(ctx, QueryParams{
		Messages:       []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "You are helpful."},
		SessionID:      "session-query-test",
		UserContext:    map[string]string{"cwd": "/tmp/project"},
		ToolUseContext: toolCtx,
		FallbackModel:  "fallback-model",
		SkipCacheWrite: true,
		QuerySource:    QuerySourceSDK,
		ChatModel:      &callModelOptionsModel{},
		Deps: &QueryDeps{
			UUID: func() string { return "chain-1" },
			CallModel: func(callCtx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				capturedCtx = callCtx
				capturedOpts = opts
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}),
					Model:        opts.Model,
				}, nil
			},
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if capturedCtx != abortCtx {
		t.Fatal("expected query to call the model with the abort-controller context")
	}
	if capturedOpts.Signal != abortCtx {
		t.Fatal("expected call-model options to include the abort-controller signal")
	}
	if capturedOpts.UserContext["cwd"] != "/tmp/project" {
		t.Fatalf("expected user context to be forwarded, got %#v", capturedOpts.UserContext)
	}
	if capturedOpts.ThinkingConfig == nil || capturedOpts.ThinkingConfig.Type != "enabled" {
		t.Fatalf("expected thinking config to be forwarded, got %#v", capturedOpts.ThinkingConfig)
		return
	}
	if capturedOpts.Model != "main-model" {
		t.Fatalf("expected main-loop model to be forwarded, got %q", capturedOpts.Model)
	}
	if capturedOpts.ToolChoice != "forced" {
		t.Fatalf("expected tool choice to be forwarded, got %q", capturedOpts.ToolChoice)
	}
	if capturedOpts.ForcedToolName != "Bash" {
		t.Fatalf("expected forced tool name to be forwarded, got %q", capturedOpts.ForcedToolName)
	}
	if !capturedOpts.IsNonInteractive {
		t.Fatal("expected non-interactive session flag to be forwarded")
	}
	if capturedOpts.FallbackModel != "" {
		t.Fatalf(
			"legacy fallback escaped the canonical coordinator: %q",
			capturedOpts.FallbackModel,
		)
	}
	if capturedOpts.QuerySource != string(QuerySourceSDK) {
		t.Fatalf("expected query source to be forwarded, got %q", capturedOpts.QuerySource)
	}
	if capturedOpts.QueryTracking == nil {
		t.Fatal("expected query tracking to be forwarded")
		return
	}
	if capturedOpts.QueryTracking.ChainID != "chain-1" || capturedOpts.QueryTracking.Depth != 0 {
		t.Fatalf("expected query tracking chain-1/depth-0, got %#v", capturedOpts.QueryTracking)
	}
	if !capturedOpts.SkipCacheWrite {
		t.Fatal("expected skip-cache-write flag to be forwarded")
	}
	if capturedOpts.AgentID != "" {
		t.Fatalf("expected empty agent id for top-level turn, got %q", capturedOpts.AgentID)
	}
	if capturedOpts.SessionID != "session-query-test" {
		t.Fatalf("expected session id to be forwarded, got %q", capturedOpts.SessionID)
	}
	if len(capturedOpts.Tools) != 1 || capturedOpts.Tools[0].Name != "Bash" {
		t.Fatalf("expected tool infos to be forwarded, got %#v", capturedOpts.Tools)
	}
}

func TestQueryPrefersToolContextSessionIDWhenPresent(t *testing.T) {
	ctx := context.Background()
	toolCtx := &ToolUseContext{
		SessionID: "session-from-toolctx",
		Options: &ToolUseOptions{
			MainLoopModel: "main-model",
		},
	}

	var capturedOpts execution.CallModelOptions
	terminal := Query(ctx, QueryParams{
		Messages:       []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "You are helpful."},
		SessionID:      "session-from-params",
		ToolUseContext: toolCtx,
		QuerySource:    QuerySourceSDK,
		ChatModel:      &callModelOptionsModel{},
		Deps: &QueryDeps{
			UUID: func() string { return "chain-2" },
			CallModel: func(callCtx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				capturedOpts = opts
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}),
					Model:        opts.Model,
				}, nil
			},
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if capturedOpts.SessionID != "session-from-toolctx" {
		t.Fatalf("expected tool-context session id to win, got %q", capturedOpts.SessionID)
	}
}

func TestQueryRefreshesToolsBetweenTurns(t *testing.T) {
	ctx := context.Background()

	originalTools := []*schema.ToolInfo{{Name: "Bash"}}
	refreshedTools := []*schema.ToolInfo{{Name: "Bash"}, {Name: "NewMCPTool"}}
	refreshCalled := 0

	toolCtx := &ToolUseContext{
		Options: &ToolUseOptions{
			MainLoopModel: "test-model",
			Tools:         originalTools,
			RefreshTools: func() []*schema.ToolInfo {
				refreshCalled++
				return refreshedTools
			},
		},
	}

	callCount := 0
	var capturedToolCounts []int
	maxTurns := 2
	terminal := Query(ctx, QueryParams{
		Messages:       []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "sys"},
		ToolUseContext: toolCtx,
		QuerySource:    QuerySourceSDK,
		ChatModel:      &callModelOptionsModel{},
		MaxTurns:       &maxTurns,
		Deps: &QueryDeps{
			UUID: func() string { return "chain-1" },
			CallModel: func(callCtx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				callCount++
				capturedToolCounts = append(capturedToolCounts, len(tools))
				if callCount == 1 {
					// First call: return tool use to force a follow-up turn
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray([]*schema.Message{{
							Role:    schema.Assistant,
							Content: "using tool",
							ToolCalls: []schema.ToolCall{{
								ID:       "tc1",
								Type:     "function",
								Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"echo hi"}`},
							}},
						}}),
						Model: opts.Model,
					}, nil
				}
				// Second call: just complete
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}),
					Model:        opts.Model,
				}, nil
			},
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 model calls (multi-turn), got %d", callCount)
	}
	if refreshCalled != 1 {
		t.Fatalf("expected RefreshTools called once between turns, got %d", refreshCalled)
	}
	// First call should have original 1 tool, second call should have refreshed 2 tools
	if capturedToolCounts[0] != 1 {
		t.Fatalf("expected first call to have 1 tool, got %d", capturedToolCounts[0])
	}
	if capturedToolCounts[1] != 2 {
		t.Fatalf("expected second call to have 2 tools (refreshed), got %d", capturedToolCounts[1])
	}
}
