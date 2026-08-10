package commands

import (
	"strings"
	"testing"
)

type fakeRuntimeSnapshotEngine struct {
	snapshot TaskExplorerInspectionSnapshot
}

func (f fakeRuntimeSnapshotEngine) RuntimeInspectionSnapshot() RuntimeInspectionSnapshot {
	return RuntimeInspectionSnapshot{TaskExplorer: f.snapshot}
}

func TestExecuteTasksReportsEmptyRuntimeSnapshot(t *testing.T) {
	result, err := executeTasks(
		&CommandContext{Engine: fakeRuntimeSnapshotEngine{
			snapshot: TaskExplorerInspectionSnapshot{
				Available:     true,
				BoardID:       "board",
				BoardRevision: 3,
			},
		}},
		"",
	)
	if err != nil {
		t.Fatalf("executeTasks returned error: %v", err)
		return
	}
	if result == nil {
		t.Fatal("expected command result")
		return
	}
	if !strings.Contains(result.Output, "No task explorer rows.") {
		t.Fatalf("expected empty runtime snapshot output, got %q", result.Output)
	}
	for _, contract := range []string{
		"durability=durable-session-workboard",
		"control=read-only-command",
	} {
		if !strings.Contains(result.Output, contract) {
			t.Fatalf("missing /tasks contract %q: %q", contract, result.Output)
		}
	}
}

func TestExecuteTasksRejectsMutationWithoutRuntimeSideEffect(t *testing.T) {
	result, err := executeTasks(
		&CommandContext{Engine: fakeRuntimeSnapshotEngine{}},
		"kill task-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil ||
		!strings.Contains(result.Output, "Task inspection is read-only") {
		t.Fatalf("read-only result = %#v", result)
	}
}

func TestExecuteTasksReportsRuntimeAgentAndLocalTaskRecords(t *testing.T) {
	result, err := executeTasks(
		&CommandContext{Engine: fakeRuntimeSnapshotEngine{
			snapshot: TaskExplorerInspectionSnapshot{
				Available:       true,
				BoardID:         "board",
				BoardRevision:   7,
				RuntimeRevision: 9,
				WorkItems: []TaskExplorerInspectionWorkItem{{
					WorkItemID: "todo:1",
					Status:     "in_progress",
					ActiveForm: "Writing tests",
					Owner:      "executor",
				}},
				Executions: []TaskExplorerInspectionExecution{{
					AgentID:    "agent-1",
					Generation: 4,
					Status:     "running",
					Name:       "runtime-agent",
					Activity:   "Inspect runtime",
				}},
				Links: []TaskExplorerInspectionLink{{
					WorkItemID: "todo:1",
					AgentID:    "agent-1",
					Generation: 4,
					State:      "valid",
				}},
			},
		}},
		"",
	)
	if err != nil {
		t.Fatalf("executeTasks returned error: %v", err)
		return
	}

	output := result.Output
	for _, want := range []string{
		"Task Explorer",
		"durability=durable-session-workboard",
		"control=read-only-command",
		"board=board revision=7 runtime_revision=9",
		"WorkItems (1)",
		"[todo:1] in_progress Writing tests owner=executor",
		"Executions (1)",
		"[agent-1/4] running Inspect runtime",
		"Links (1)",
		"todo:1 -> agent-1/4 state=valid",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, output)
		}
	}
}
