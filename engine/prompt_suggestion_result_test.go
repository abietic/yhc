package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/services"
	"github.com/cloudwego/eino/schema"
)

func TestPromptSuggestionResultExplainsAdmissionWithoutProviderCalls(t *testing.T) {
	for _, test := range []struct {
		name     string
		enabled  bool
		mode     permission.Mode
		messages []*schema.Message
		reason   string
	}{
		{"disabled", false, permission.ModeDefault, nil, "disabled"},
		{"plan", true, permission.ModePlan, nil, "plan_mode"},
		{"no context", true, permission.ModeDefault, nil, "no_context"},
		{"early", true, permission.ModeDefault, []*schema.Message{{Role: schema.Assistant, Content: "one"}}, "early_conversation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := &promptSuggestionProbeModel{result: "run the tests"}
			eng := NewQueryEngine(QueryEngineConfig{
				CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: probe,
				Model: "current-model", PermissionMode: test.mode,
				CommandEntrypoint: commands.EntrypointTUI, EnablePromptSuggestions: test.enabled,
			})
			t.Cleanup(eng.Close)
			eng.SetResumedMessages(test.messages)
			result, err := eng.GeneratePromptSuggestionResult(context.Background())
			if err != nil || result.Status != services.PromptSuggestionSuppressed || result.Reason != test.reason || result.Suggestion != "" {
				t.Fatalf("result = %#v, %v", result, err)
			}
			calls, _, _, _, _, _ := probe.snapshot()
			if calls != 0 {
				t.Fatal("ineligible request reached provider")
			}
		})
	}
}
