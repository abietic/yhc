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

func TestRunBudgetPolicyStableWithSharedUsage(t *testing.T) {
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
	for i := range probe.inputs {
		notice := budgetNotice(probe.inputs[i])
		if !strings.Contains(notice, "provider_call_limit=3") ||
			!strings.Contains(notice, "token_threshold=100") ||
			strings.Contains(notice, "DUPLICATE") || notice != budgetNotice(probe.inputs[0]) {
			t.Fatalf("round %d: %q", i, notice)
		}
	}
	if strings.Contains(budgetNotice(probe.inputs[2]), "remaining=") ||
		strings.Contains(budgetNotice(probe.inputs[2]), "known_tokens=") {
		t.Fatal("live usage must not rewrite the provider prefix")
	}
	if snapshot := usage.Snapshot(); snapshot.ProviderCalls != 3 || snapshot.TotalTokens != 36 {
		t.Fatal("reminder changed admission accounting")
	}
	if budgetNotice(eng.GetMessages()) != "" {
		t.Fatal("invocation policy became canonical conversation history")
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
			if cutoff, ok := ctx.Deadline(); deadline && (!ok ||
				!strings.Contains(notice, "deadline_utc="+cutoff.UTC().Format(time.RFC3339Nano))) {
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
	if len(probe.inputs) != 1 || !strings.Contains(budgetNotice(probe.inputs[0]), "deadline_utc=") {
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
	if len(probe.inputs) != 1 || !strings.Contains(budgetNotice(probe.inputs[0]), "deadline_utc=") {
		t.Fatal("fresh child context escaped configured invocation cutoff")
	}
}
