package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestAdmittedAgentInitialHistoryExcludesSeededCurrentTurn(t *testing.T) {
	const task = "Verify the same task without duplicating its current turn."
	const priorAnswer = "The previous verification finished."
	for _, background := range []bool{false, true} {
		for _, history := range []struct {
			name     string
			messages []*schema.Message
			want     int
		}{
			{name: "fresh-nil", want: 1},
			{name: "fresh-empty", messages: []*schema.Message{}, want: 1},
			{name: "intentional-repeat", messages: []*schema.Message{
				{Role: schema.User, Content: task},
				{Role: schema.Assistant, Content: priorAnswer},
			}, want: 2},
		} {
			mode := "foreground/"
			if background {
				mode = "background/"
			}
			t.Run(mode+history.name, func(t *testing.T) {
				mdl := &autoCompactRestartCaptureModel{}
				executor := NewSubAgentExecutor(mdl, tools.NewRegistry(), t.TempDir())
				runner := tools.NewAgentRunner(1)
				runner.SetOutputDir(t.TempDir())
				runner.SetExecutor(executor)
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					if err := runner.Shutdown(ctx); err != nil {
						t.Errorf("join fixture agent: %v", err)
					}
				})
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				opts := tools.AgentExecOptions{Task: task, SubagentType: "verification", ResumeMessages: history.messages}
				var agentID string
				if background {
					started, err := tools.RunAgentBackground(ctx, runner, opts)
					if err != nil {
						t.Fatal(err)
					}
					agentID = started.ID
					waitForAgentStatus(t, runner, agentID, "completed", 2*time.Second)
				} else {
					result, err := tools.RunAgent(ctx, runner, opts)
					if err != nil {
						t.Fatal(err)
					}
					agentID = result.AgentID
				}
				inputs := mdl.inputsSnapshot()
				if len(inputs) != 1 {
					t.Fatalf("provider calls = %d, want 1", len(inputs))
				}
				copies := 0
				oldAnswers := 0
				for _, message := range inputs[0] {
					if message.Role == schema.User {
						copies += strings.Count(message.Content, task)
					}
					if message.Role == schema.Assistant && message.Content == priorAnswer {
						oldAnswers++
					}
				}
				if copies != history.want || oldAnswers != history.want-1 {
					t.Fatalf("model-visible task copies=%d prior answers=%d, want %d/%d", copies, oldAnswers, history.want, history.want-1)
				}
				snapshot, ok := runner.GetAgentSnapshot(agentID)
				if !ok {
					t.Fatal("completed fixture agent missing")
				}
				loaded, err := transcript.NewRecorder(snapshot.SessionID, filepath.Dir(snapshot.TranscriptPath)).LoadFull()
				if err != nil {
					t.Fatal(err)
				}
				durableCopies := 0
				for _, message := range loaded.Messages {
					if message.Role == schema.User && message.Content == task {
						durableCopies++
					}
				}
				if durableCopies != history.want {
					t.Fatalf("durable task copies=%d, want %d", durableCopies, history.want)
				}
				if len(history.messages) > 0 && (history.messages[0].Content != task || history.messages[1].Content != priorAnswer) {
					t.Fatal("caller-owned prior history changed")
				}
			})
		}
	}
}
