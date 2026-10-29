package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestRunUsageSharedWithChildAndAuxiliary(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{{chunks: []*schema.Message{{Role: schema.Assistant, Content: "done", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 2}}}}}}}
	mdl.responses = append(mdl.responses, mdl.responses[0])
	cfg := projectGraphEngineConfig(t, t.TempDir(), "run-usage-root", mdl, tools.NewRegistry(), &tools.ToolSelection{})
	cfg.RunUsage = usage
	eng := NewQueryEngine(cfg)
	defer eng.Close()
	events, _ := eng.SubmitMessage(context.Background(), "one response")
	for range events {
	}
	if s := usage.Snapshot(); s.ProviderCalls != 1 || s.TotalTokens != 12 {
		t.Fatalf("root=%+v", s)
	}
	// Child construction must receive the root pointer, not a fresh per-child limit.
	if eng.subagentExecutor.RunUsage != usage {
		t.Fatal("child executor lost run collector")
	}
	child, err := eng.subagentExecutor.ExecuteAgent(context.Background(), tools.AgentExecOptions{Task: "one response", SubagentType: "Explore"})
	if err != nil || child == nil {
		t.Fatalf("child=%+v err=%v", child, err)
	}
	if s := usage.Snapshot(); s.ProviderCalls != 2 || s.TotalTokens != 24 {
		t.Fatalf("child=%+v", s)
	}
	// Background Generate shares admission even though it bypasses Stream.
	_, err = eng.callBackgroundProvider(context.Background(), mdl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := usage.Snapshot(); s.ProviderCalls != 3 {
		t.Fatalf("background=%+v", s)
	}
	_, err = eng.callBackgroundProvider(context.Background(), mdl, nil)
	if !errors.Is(err, execution.ErrRunBudgetExceeded) {
		t.Fatalf("background exceeded shared limit: %v", err)
	}
}

func TestRunUsagePromptSuggestionAndPendingCancellation(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 1})
	probe := &promptSuggestionProbeModel{result: "run tests"}
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: probe, Model: "test-model", RunUsage: usage})
	defer eng.Close()
	if _, err := eng.generatePromptSuggestionProvider(context.Background(), []string{"done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.generatePromptSuggestionProvider(context.Background(), []string{"done"}); !errors.Is(err, execution.ErrRunBudgetExceeded) {
		t.Fatal(err)
	}
	s := usage.Snapshot()
	if probe.calls != 1 || s.TotalTokens != 12 || len(s.Routes) != 1 || s.Routes[0].Source != "prompt_suggestion_generation" {
		t.Fatalf("usage=%+v", s)
	}
	usage, _ = execution.NewRunUsage(execution.RunUsageLimits{})
	entered := make(chan struct{})
	probe = &promptSuggestionProbeModel{wait: true, onCall: func() { close(entered) }}
	eng2 := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: probe, Model: "test-model", RunUsage: usage})
	defer eng2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := eng2.generatePromptSuggestionProvider(ctx, []string{"done"}); done <- err }()
	<-entered
	usage.Seal()
	if s := usage.Snapshot(); s.InFlight != 1 || s.Complete {
		t.Fatalf("pending=%+v", s)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s := usage.Snapshot(); s.InFlight != 0 || s.UnknownCalls != 1 || s.Complete {
		t.Fatalf("cancelled=%+v", s)
	}
	if _, err := eng2.generatePromptSuggestionProvider(context.Background(), []string{"late"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("late call=%v", err)
	}
	if usage.Snapshot().ProviderCalls != 1 {
		t.Fatal("late async request admitted after seal")
	}
}
