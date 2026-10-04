package engine

import (
	"strings"
	"testing"

	promptctx "github.com/abietic/yhc/engine/context"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestBehaviorVerificationPolicyReachesMainAndChildren(t *testing.T) {
	const task = "Fix the request handler while retaining the documented timeout and concurrent request behavior."
	for _, role := range []string{"main", "custom-root", "general-purpose", "verification"} {
		t.Run(role, func(t *testing.T) {
			mdl := &autoCompactRestartCaptureModel{}
			if role == "main" || role == "custom-root" {
				// CLI roots explicitly select the default identity; embedded engines
				// remain free to supply their own system prompt.
				identity := promptctx.BaseIdentityPrompt
				if role == "custom-root" {
					identity = "User-supplied root identity."
				}
				engine := NewQueryEngine(QueryEngineConfig{
					CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: mdl,
					CustomSystemPrompt: identity,
				})
				t.Cleanup(engine.Close)
				events, admission := engine.SubmitMessage(t.Context(), task)
				if admission.Err != nil {
					t.Fatal(admission.Err)
				}
				assertAutoCompactRestartTerminal(t, events)
			} else {
				executor := NewSubAgentExecutor(mdl, tools.NewRegistry(), t.TempDir())
				if _, err := executor.ExecuteAgent(t.Context(), tools.AgentExecOptions{Task: task, SubagentType: role}); err != nil {
					t.Fatal(err)
				}
			}
			inputs := mdl.inputsSnapshot()
			if len(inputs) != 1 {
				t.Fatalf("verification policy introduced extra provider calls: %d", len(inputs))
			}
			var policyCopies, originalRequests int
			for _, message := range inputs[0] {
				if message.Role == schema.System {
					policyCopies += strings.Count(message.Content, promptctx.BehaviorVerificationPolicy)
				}
				if message.Role == schema.User && message.Content == task {
					originalRequests++
				}
			}
			wantCopies := 1
			if role == "custom-root" {
				wantCopies = 0
			}
			if policyCopies != wantCopies || originalRequests != 1 {
				t.Fatalf("policy copies=%d, original requests=%d", policyCopies, originalRequests)
			}
		})
	}
}
