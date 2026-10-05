package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type runBudgetProbe struct{ inputs [][]*schema.Message }

func (m *runBudgetProbe) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	panic("unexpected Generate")
}

func (m *runBudgetProbe) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 2}}}}), nil
}

func budgetNotice(input []*schema.Message) string {
	var notices []string
	for _, m := range input {
		if strings.Contains(m.Content, "<run-budget>") {
			notices = append(notices, m.Content)
		}
	}
	return strings.Join(notices, "\nDUPLICATE\n")
}

func TestRunBudgetAwarenessSharedAndFresh(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3, MaxTotalTokens: 100})
	probe := &runBudgetProbe{}
	cfg := projectGraphEngineConfig(t, t.TempDir(), "budget-awareness", nil, tools.NewRegistry(), &tools.ToolSelection{})
	cfg.ChatModel = probe
	cfg.RunUsage = usage
	eng := NewQueryEngine(cfg)
	defer eng.Close()
	submit := func() {
		events, _ := eng.SubmitMessage(context.Background(), "respond")
		for range events {
		}
	}
	submit()
	_, err := eng.subagentExecutor.ExecuteAgent(context.Background(), tools.AgentExecOptions{Task: "respond", SubagentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	submit()
	if len(probe.inputs) != 3 {
		t.Fatalf("calls=%d", len(probe.inputs))
	}
	for i, want := range []string{"provider_calls_remaining=3", "provider_calls_remaining=2", "provider_calls_remaining=1"} {
		notice := budgetNotice(probe.inputs[i])
		if !strings.Contains(notice, want) || strings.Contains(notice, "DUPLICATE") {
			t.Fatalf("round %d: %q", i, notice)
		}
	}
	if !strings.Contains(budgetNotice(probe.inputs[2]), "known_tokens=24") {
		t.Fatal("child usage missing from parent reminder")
	}
	if usage.Snapshot().ProviderCalls != 3 {
		t.Fatal("reminder changed admission accounting")
	}
}

func TestRunBudgetAwarenessDefaultOffAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "deadline"}[deadline], func(t *testing.T) {
			probe := &runBudgetProbe{}
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{})
			cfg := projectGraphEngineConfig(t, t.TempDir(), "budget-default", nil, tools.NewRegistry(), &tools.ToolSelection{})
			cfg.ChatModel = probe
			cfg.RunUsage = usage
			eng := NewQueryEngine(cfg)
			defer eng.Close()
			ctx := context.Background()
			if deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
			}
			events, _ := eng.SubmitMessage(ctx, "respond")
			for range events {
			}
			if len(probe.inputs) != 1 {
				t.Fatalf("calls=%d", len(probe.inputs))
			}
			notice := budgetNotice(probe.inputs[0])
			if deadline && !strings.Contains(notice, "time_remaining_seconds=") {
				t.Fatalf("missing enforced deadline: %q", notice)
			}
			if !deadline && notice != "" {
				t.Fatalf("default enabled reminder: %q", notice)
			}
		})
	}
}

func TestRunBudgetAwarenessDetachedChildRetainsInvocationDeadline(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{})
	probe := &runBudgetProbe{}
	cfg := projectGraphEngineConfig(t, t.TempDir(), "budget-detach", nil, tools.NewRegistry(), &tools.ToolSelection{})
	cfg.ChatModel = probe
	cfg.RunUsage = usage
	eng := NewQueryEngine(cfg)
	defer eng.Close()
	ctx, cancel := execution.WithRunDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	_, err := eng.subagentExecutor.ExecuteAgent(context.WithoutCancel(ctx), tools.AgentExecOptions{Task: "respond", SubagentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.inputs) != 1 || !strings.Contains(budgetNotice(probe.inputs[0]), "time_remaining_seconds=") {
		t.Fatal("detached child lost invocation cutoff")
	}
}

func TestRunBudgetAwarenessResumedChildUsesConfiguredDeadline(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{})
	probe := &runBudgetProbe{}
	cfg := projectGraphEngineConfig(t, t.TempDir(), "budget-resume", nil, tools.NewRegistry(), &tools.ToolSelection{})
	cfg.ChatModel = probe
	cfg.RunUsage = usage
	cfg.RunDeadline = time.Now().Add(time.Minute)
	eng := NewQueryEngine(cfg)
	defer eng.Close()
	// AgentRunner resumes use a fresh background context, without launch values.
	_, err := eng.subagentExecutor.ExecuteAgent(context.Background(), tools.AgentExecOptions{Task: "respond", SubagentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.inputs) != 1 || !strings.Contains(budgetNotice(probe.inputs[0]), "time_remaining_seconds=") {
		t.Fatal("fresh child context escaped configured invocation cutoff")
	}
}
