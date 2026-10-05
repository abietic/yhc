package engine

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
)

func newCommandArgumentSuggestionEngine(t *testing.T, probe *promptSuggestionProbeModel, mode permission.Mode) *QueryEngine {
	t.Helper()
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		ChatModel:         probe,
		CommandEntrypoint: commands.EntrypointTUI,
		PermissionMode:    mode,
	})
	t.Cleanup(eng.Close)
	return eng
}

func commandArgumentRequest(input string) commands.CompletionRequest {
	return commands.CompletionRequest{Input: input, Cursor: len([]rune(input))}
}

func TestCommandArgumentSuggestionUsesAuxiliaryProviderOnlyForEligibleArguments(t *testing.T) {
	probe := &promptSuggestionProbeModel{result: `{"argument":"because the context is stale"}`}
	eng := newCommandArgumentSuggestionEngine(t, probe, permission.ModeDefault)
	for _, request := range []commands.CompletionRequest{
		commandArgumentRequest("/diff st"),
		commandArgumentRequest("/model gpt"),
	} {
		if got := eng.GenerateCommandArgumentSuggestion(context.Background(), request); got != "" {
			t.Fatalf("static argument suggestion = %q", got)
		}
	}
	if calls, _, _, _, _, _ := probe.snapshot(); calls != 0 {
		t.Fatalf("static argument provider calls = %d", calls)
	}
	eng.observeProviderUsageMessage(&schema.Message{
		Role: schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		}},
	})

	request := commandArgumentRequest("/compact because")
	if got := eng.GenerateCommandArgumentSuggestion(context.Background(), request); got != "because the context is stale" {
		t.Fatalf("GenerateCommandArgumentSuggestion() = %q", got)
	}
	calls, messages, _, tools, _, generateCalls := probe.snapshot()
	if calls != 1 || generateCalls != 0 || tools != 0 {
		t.Fatalf("auxiliary provider controls calls=%d generate=%d tools=%d", calls, generateCalls, tools)
	}
	if len(messages) != 2 || messages[0].Role != schema.System || messages[1].Role != schema.User {
		t.Fatalf("provider messages = %#v", messages)
	}
	if len(eng.GetMessages()) != 0 {
		t.Fatal("argument suggestion mutated conversation")
	}
	usage := eng.providerUsageSummary()
	if usage.TotalTokens != 132 || !usage.CurrentContextUsageKnown || usage.CurrentContextPromptTokens != 100 {
		t.Fatalf("auxiliary usage changed active context or lost billing: %+v", usage)
	}
}

func TestCommandArgumentSuggestionFailsClosedForPlanAndMalformedOutput(t *testing.T) {
	planProbe := &promptSuggestionProbeModel{result: `{"argument":"because x"}`}
	plan := newCommandArgumentSuggestionEngine(t, planProbe, permission.ModePlan)
	if got := plan.GenerateCommandArgumentSuggestion(context.Background(), commandArgumentRequest("/compact because")); got != "" {
		t.Fatalf("plan suggestion = %q", got)
	}
	if calls, _, _, _, _, _ := planProbe.snapshot(); calls != 0 {
		t.Fatalf("plan provider calls = %d", calls)
	}

	malformedProbe := &promptSuggestionProbeModel{result: `{"argument":"because x"} {"argument":"because y"}`}
	eng := newCommandArgumentSuggestionEngine(t, malformedProbe, permission.ModeDefault)
	if got := eng.GenerateCommandArgumentSuggestion(context.Background(), commandArgumentRequest("/compact because")); got != "" {
		t.Fatalf("malformed suggestion = %q", got)
	}
}

func TestCommandArgumentSuggestionHonorsCancellation(t *testing.T) {
	probe := &promptSuggestionProbeModel{wait: true}
	eng := newCommandArgumentSuggestionEngine(t, probe, permission.ModeDefault)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan string, 1)
	go func() {
		done <- eng.GenerateCommandArgumentSuggestion(ctx, commandArgumentRequest("/compact because"))
	}()
	deadline := time.Now().Add(time.Second)
	for {
		calls, _, _, _, _, _ := probe.snapshot()
		if calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("argument suggestion provider did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("cancelled suggestion = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled suggestion did not return")
	}
}

func TestCommandArgumentSuggestionRechecksEligibilityAfterProvider(t *testing.T) {
	probe := &promptSuggestionProbeModel{result: `{"argument":"because x"}`}
	eng := newCommandArgumentSuggestionEngine(t, probe, permission.ModeDefault)
	probe.onCall = func() {
		eng.planMu.Lock()
		eng.planActiveTurnID = "turn-active"
		eng.planMu.Unlock()
	}
	if got := eng.GenerateCommandArgumentSuggestion(context.Background(), commandArgumentRequest("/compact because")); got != "" {
		t.Fatalf("stale eligible suggestion = %q", got)
	}
	if calls, _, _, _, _, _ := probe.snapshot(); calls != 1 {
		t.Fatalf("provider never reached post-call eligibility check: calls=%d", calls)
	}
}
