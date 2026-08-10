package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type longTurnModel struct {
	mu        sync.Mutex
	toolTurns int
	calls     int
}

func (m *longTurnModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *longTurnModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call <= m.toolTurns {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   fmt.Sprintf("call-%d", call),
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{}`,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "deterministic final response"}}), nil
}

func TestQueryZeroMaxTurnsCompletesAfterOneHundredTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	maxTurns := 0
	mdl := &longTurnModel{toolTurns: 101}
	var final string
	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "keep going"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(context.Context, string, string) (string, error) { return "ok", nil },
	}, func(evt QueryEvent) {
		for _, msg := range []*schema.Message{evt.AssistantMessage, evt.Message, evt.StreamEvent} {
			if msg != nil && msg.Content != "" {
				final += msg.Content
			}
		}
	})
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal reason = %q, want completed", terminal.Reason)
	}
	if mdl.calls != 102 {
		t.Fatalf("model calls = %d, want 102", mdl.calls)
	}
	if final != "deterministic final response" {
		t.Fatalf("final response = %q", final)
	}
}

func TestBuiltInSubagentsDefaultUnlimitedAndExceedOldCaps(t *testing.T) {
	for _, agentType := range []string{"Explore", "Plan", "general-purpose", "verification"} {
		t.Run(agentType, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			registry := tools.NewRegistry()
			registry.Register(tools.ToolImpl{
				Info: &schema.ToolInfo{Name: "Read"},
				ExecuteCtx: func(context.Context, string) (string, error) {
					return "ok", nil
				},
				IsReadOnly: true,
			})
			mdl := &longTurnModel{toolTurns: 101}
			exec := NewSubAgentExecutor(mdl, registry, t.TempDir())
			if got := exec.defaultMaxTurns(agentType); got != 0 {
				t.Fatalf("default max turns = %d, want unlimited (0)", got)
			}
			result, err := exec.ExecuteAgent(ctx, tools.AgentExecOptions{
				Task:         "keep going",
				SubagentType: agentType,
			})
			if err != nil {
				t.Fatalf("ExecuteAgent: %v", err)
				return
			}
			if result.TurnCount != 102 || result.Result != "deterministic final response" {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestSubagentNegativeMaxTurnsRejected(t *testing.T) {
	exec := &SubAgentExecutor{}
	if _, err := exec.ExecuteAgent(context.Background(), tools.AgentExecOptions{Task: "x", MaxTurns: -1}); err == nil {
		t.Fatal("negative max turns was accepted")
		return
	}
}

func TestNewQueryEngineNegativeMaxTurnsRejected(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("negative MaxTurns did not panic")
			return
		}
	}()
	_ = NewQueryEngine(QueryEngineConfig{MaxTurns: -1})
}

func TestQueryNegativeMaxTurnsRejected(t *testing.T) {
	maxTurns := -1
	defer func() {
		if got := recover(); got != "engine: MaxTurns must be zero (unlimited) or positive" {
			t.Fatalf("panic = %v, want negative MaxTurns validation error", got)
		}
	}()
	Query(context.Background(), QueryParams{MaxTurns: &maxTurns}, func(QueryEvent) {})
}
