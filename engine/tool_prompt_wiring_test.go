package engine

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type toolPromptCaptureModel struct {
	autoCompactRestartCaptureModel
	descriptions []map[string]string
}

func TestToolInstructionsSkipPlanAndGoalFilteredCallbacks(t *testing.T) {
	for _, role := range []string{"root", "child"} {
		for _, plan := range []bool{false, true} {
			name := role + "/ordinary"
			if plan {
				name = role + "/plan"
			}
			t.Run(name, func(t *testing.T) {
				var excludedCalls atomic.Int64
				registry := tools.NewRegistry()
				registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{Name: "Read"}})
				for _, toolName := range []string{"Bash", "mcp__sample__lookup", tools.GetGoalToolName} {
					registry.Register(tools.ToolImpl{
						Info: &schema.ToolInfo{Name: toolName},
						Prompt: func() string {
							if plan || toolName == tools.GetGoalToolName {
								excludedCalls.Add(1)
							}
							return "Tool-specific instructions."
						},
					})
				}
				mdl := &toolPromptCaptureModel{}
				if role == "root" {
					eng := NewQueryEngine(QueryEngineConfig{
						CWD: t.TempDir(), TranscriptDir: t.TempDir(),
						ChatModel: mdl, ToolRegistry: registry,
					})
					t.Cleanup(eng.Close)
					if plan {
						if err := eng.SetPermissionMode(permission.ModePlan); err != nil {
							t.Fatal(err)
						}
						excludedCalls.Store(0) // Only observe the now-active Plan turn.
					}
					events, admission := eng.SubmitMessage(t.Context(), "Inspect the implementation.")
					if admission.Err != nil {
						t.Fatal(admission.Err)
					}
					assertAutoCompactRestartTerminal(t, events)
				} else {
					executor := NewSubAgentExecutor(mdl, registry, t.TempDir())
					mode := "default"
					if plan {
						mode = "plan"
					}
					if _, err := executor.ExecuteAgent(t.Context(), tools.AgentExecOptions{
						Task: "Inspect the implementation.", SubagentType: "general-purpose", Mode: mode,
					}); err != nil {
						t.Fatal(err)
					}
				}
				if excludedCalls.Load() != 0 {
					t.Fatalf("filtered tool callbacks ran %d times", excludedCalls.Load())
				}
			})
		}
	}
}

func (m *toolPromptCaptureModel) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := make(map[string]string, len(infos))
	for _, info := range infos {
		row[info.Name] = info.Desc
	}
	m.descriptions = append(m.descriptions, row)
	return m, nil
}

func TestToolInstructionsReachRootAndChildModelRequests(t *testing.T) {
	const instruction = "Read the full target before replacing existing contents."
	for _, role := range []string{"root", "general-purpose", "verification"} {
		t.Run(role, func(t *testing.T) {
			registry := tools.NewRegistry()
			registry.Register(tools.ToolImpl{
				Info:   &schema.ToolInfo{Name: "Read", Desc: "Read a file."},
				Prompt: func() string { return instruction },
			})
			registry.Register(tools.ToolImpl{
				Info:     &schema.ToolInfo{Name: "HiddenTool"},
				IsHidden: true,
				Prompt: func() string {
					t.Fatal("hidden tool instructions were evaluated")
					return "unreachable"
				},
			})
			mdl := &toolPromptCaptureModel{}
			if role == "root" {
				eng := NewQueryEngine(QueryEngineConfig{
					CWD: t.TempDir(), TranscriptDir: t.TempDir(),
					ChatModel: mdl, ToolRegistry: registry,
					CustomSystemPrompt: "Custom root identity.",
				})
				t.Cleanup(eng.Close)
				events, admission := eng.SubmitMessage(t.Context(), "Inspect the current implementation.")
				if admission.Err != nil {
					t.Fatal(admission.Err)
				}
				assertAutoCompactRestartTerminal(t, events)
			} else {
				executor := NewSubAgentExecutor(mdl, registry, t.TempDir())
				if _, err := executor.ExecuteAgent(t.Context(), tools.AgentExecOptions{
					Task: "Inspect the current implementation.", SubagentType: role,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if len(mdl.inputsSnapshot()) != 1 {
				t.Fatal("tool instructions introduced extra model calls")
			}
			mdl.mu.Lock()
			defer mdl.mu.Unlock()
			if len(mdl.descriptions) != 1 || strings.Count(mdl.descriptions[0]["Read"], instruction) != 1 {
				t.Fatalf("provider-bound tool descriptions omitted instructions: %#v", mdl.descriptions)
			}
			if _, exists := mdl.descriptions[0]["HiddenTool"]; exists {
				t.Fatal("hidden tool reached provider")
			}
		})
	}
}
