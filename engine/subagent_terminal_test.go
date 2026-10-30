package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestSubAgentIncompleteExecutionPreservesFindings(t *testing.T) {
	for _, background := range []bool{false, true} {
		for _, limit := range []bool{false, true} {
			name := "foreground/upstream-error"
			if background {
				name = "background/upstream-error"
			}
			if limit {
				name = strings.ReplaceAll(name, "upstream-error", "max-turns")
			}
			t.Run(name, func(t *testing.T) {
				registry := tools.NewRegistry()
				registry.Register(tools.ToolImpl{
					Info: &schema.ToolInfo{Name: "Read", Desc: "read"}, IsReadOnly: true,
					ExecuteCtx: func(context.Context, string) (string, error) { return "ok", nil },
				})
				upstream := errors.New("upstream unavailable")
				executor := NewSubAgentExecutor(&failingSubagentProgressModel{err: upstream}, registry, t.TempDir())
				runner := tools.NewAgentRunner(1)
				outputDir := t.TempDir()
				runner.SetOutputDir(outputDir)
				runner.SetExecutor(executor)
				ctx, cancel := context.WithTimeout(tools.WithAgentRunner(context.Background(), runner), 5*time.Second)
				defer cancel()
				opts := tools.AgentExecOptions{Task: "verify both requirements", SubagentType: "verification", MaxTurns: 2}
				wantError := "upstream unavailable"
				if limit {
					opts.MaxTurns = 1
					wantError = string(TerminalMaxTurns)
				}
				var id string
				if background {
					started, err := tools.RunAgentBackground(ctx, runner, opts)
					if err != nil {
						t.Fatal(err)
					}
					id = started.ID
				} else {
					result, err := tools.RunAgent(ctx, runner, opts)
					if err == nil || !strings.Contains(err.Error(), wantError) {
						t.Fatalf("terminal error = %v, want %q", err, wantError)
					}
					if !limit && !errors.Is(err, upstream) {
						t.Fatalf("upstream error identity lost: %v", err)
					}
					if result == nil || !strings.Contains(result.Result, "Requirement B failed") {
						t.Fatalf("partial foreground finding lost: %#v", result)
					}
					if result.Outcome == tools.AgentExecOutcomeCompleted {
						t.Fatal("failed child wait reported completed outcome")
					}
					id = result.AgentID
				}
				failed := waitForAgentStatus(t, runner, id, "failed", 2*time.Second)
				if failed.Error == nil || !strings.Contains(failed.Error.Error(), wantError) {
					t.Fatalf("terminal error = %v", failed.Error)
				}
				if !strings.Contains(failed.Result, "Requirement B failed") {
					t.Fatalf("partial finding lost: %q", failed.Result)
				}
				data, err := os.ReadFile(failed.OutputFile)
				if err != nil || string(data) != failed.Result {
					t.Fatalf("output = %q, err = %v", data, err)
				}
				notifications := runner.PollAgentNotifications()
				if len(notifications) != 1 || notifications[0].Status != "failed" || !strings.Contains(notifications[0].Message, "Requirement B failed") || !strings.Contains(notifications[0].Message, wantError) {
					t.Fatalf("leader notification lost terminal reason or finding: %#v", notifications)
				}
				fresh := tools.NewAgentRunner(1)
				fresh.SetOutputDir(outputDir)
				persisted, err := fresh.LoadPersistedAgentSnapshot(id)
				if err != nil || persisted.Status != "failed" || persisted.Completion == nil || !strings.Contains(persisted.Completion.Message, "Requirement B failed") {
					t.Fatalf("durable failed completion: %#v, err = %v", persisted.Completion, err)
				}
			})
		}
	}
}
