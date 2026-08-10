package tui

import (
	"testing"

	"github.com/abietic/yhc/engine"
)

func TestSetEngineBindsScopedRuntimeManagers(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(),
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})

	if app.taskExplorerSnapshotSource == nil || app.taskExplorerActionProvider == nil {
		t.Fatal("canonical explorer providers were not bound")
	}
	if app.mentionIndex.listMCP == nil || app.mentionIndex.readMCP == nil {
		t.Fatal("composer MCP providers were not bound from the engine")
	}
}

func TestBackgroundTaskPanelStopsAgentsThroughEngineProvider(t *testing.T) {
	aborted := make([]string, 0, 1)

	background := NewBackgroundTasksPanel(defaultStyles())
	background.SetActionProvider(func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		aborted = append(aborted, request.AgentID)
		return engine.TaskExplorerActionResult{RequestID: request.RequestID, BoardID: request.BoardID, BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision, AgentID: request.AgentID, Generation: request.Generation, Action: request.Action, Outcome: "accepted"}
	})
	key := engine.RuntimeExecutionKey{
		AgentID: "background-agent", Generation: 1,
	}
	background.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return engine.TaskExplorerSnapshot{
			Available: true, BoardID: "scope", Revision: engine.TaskExplorerRevision{Board: 1, Runtime: 1},
			Executions: []engine.TaskExplorerExecution{{Key: key}},
		}
	})
	background.items = []bgTaskItem{{
		id: "background-agent", kind: "agent", status: "running",
		execution: engine.TaskExplorerExecution{Key: key}, compatible: true,
	}}
	background.stopSelectedTask()

	if len(aborted) != 1 || aborted[0] != "background-agent" {
		t.Fatalf("engine-scoped abort calls = %#v", aborted)
	}
}
