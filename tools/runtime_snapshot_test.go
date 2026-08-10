package tools

import (
	"sync"
	"testing"
	"time"
)

func TestRuntimeTaskSnapshotEmptyState(t *testing.T) {
	snapshot := RuntimeTaskSnapshotFrom(NewAgentRunner(1), NewTaskManager())

	if len(snapshot.Agents) != 0 {
		t.Fatalf("expected no agent snapshots, got %#v", snapshot.Agents)
	}
	if len(snapshot.LocalTasks) != 0 {
		t.Fatalf("expected no local task snapshots, got %#v", snapshot.LocalTasks)
	}
}

func TestRuntimeTaskSnapshotListsBoundedAgentAndLocalTaskState(t *testing.T) {
	runner := NewAgentRunner(2)
	started := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	runner.activeAgents["agent-1"] = &RunningAgent{
		ID:                  "agent-1",
		Type:                "local_agent",
		Task:                "inspect runtime",
		Description:         "Inspect runtime",
		Name:                "runtime-agent",
		ToolUseID:           "toolu_agent",
		Status:              "running",
		StartedAt:           started,
		OutputFile:          "/tmp/agent-1.out",
		PendingMessageCount: 2,
		mu:                  &sync.Mutex{},
		Progress: AgentProgress{
			ToolUseCount: 3,
			TokenCount:   42,
			Summary:      "checking files",
			LastActivity: &ToolActivity{ToolName: "Read", ActivityDescription: "Read README"},
		},
	}

	manager := NewTaskManager()
	task := manager.Create("Write tests", "Add snapshot coverage", "Writing tests", map[string]any{"kind": "local"})
	owner := "executor"
	status := TaskStatusInProgress
	if _, _, err := manager.Update(TaskUpdate{TaskID: task.ID, Status: &status, Owner: &owner}); err != nil {
		t.Fatalf("update local task: %v", err)
		return
	}

	snapshot := RuntimeTaskSnapshotFrom(runner, manager)
	if len(snapshot.Agents) != 1 {
		t.Fatalf("expected one agent snapshot, got %#v", snapshot.Agents)
	}
	agent := snapshot.Agents[0]
	if agent.ID != "agent-1" || agent.Type != "local_agent" || agent.Status != "running" {
		t.Fatalf("unexpected agent identity/status: %#v", agent)
	}
	if agent.Description != "Inspect runtime" || agent.Name != "runtime-agent" || agent.ToolUseID != "toolu_agent" {
		t.Fatalf("unexpected agent descriptive fields: %#v", agent)
	}
	if agent.ToolUseCount != 3 || agent.TokenCount != 42 || agent.LastToolName != "Read" || agent.Summary != "checking files" {
		t.Fatalf("unexpected bounded progress fields: %#v", agent)
	}
	if agent.PendingMessageCount != 2 || agent.OutputFile != "/tmp/agent-1.out" {
		t.Fatalf("unexpected agent queue/output fields: %#v", agent)
	}

	if len(snapshot.LocalTasks) != 1 {
		t.Fatalf("expected one local task snapshot, got %#v", snapshot.LocalTasks)
	}
	local := snapshot.LocalTasks[0]
	if local.ID != "1" || local.Subject != "Write tests" || local.Status != TaskStatusInProgress || local.Owner != "executor" {
		t.Fatalf("unexpected local task snapshot: %#v", local)
	}
	if local.Description != "Add snapshot coverage" || local.ActiveForm != "Writing tests" {
		t.Fatalf("unexpected local task descriptive fields: %#v", local)
	}
}

func TestRuntimeTaskSnapshotDerivesAgentDisplayModeWithoutNewLifecycleState(t *testing.T) {
	runner := NewAgentRunner(3)
	runner.activeAgents["foreground"] = &RunningAgent{
		ID: "foreground", Status: "running", mu: &sync.Mutex{},
		Options:        AgentExecOptions{executionMode: agentExecutionModeForeground},
		foregroundWait: &agentForegroundWait{state: agentForegroundWaitActive},
	}
	runner.activeAgents["background"] = &RunningAgent{
		ID: "background", Status: "running", mu: &sync.Mutex{},
		Options: AgentExecOptions{executionMode: agentExecutionModeBackground},
	}
	runner.activeAgents["backgrounded"] = &RunningAgent{
		ID: "backgrounded", Status: "running", mu: &sync.Mutex{},
		Options:        AgentExecOptions{executionMode: agentExecutionModeForeground},
		foregroundWait: &agentForegroundWait{state: agentForegroundWaitBackgrounded},
	}

	snapshot := RuntimeTaskSnapshotFrom(runner, nil)
	modes := make(map[string]AgentDisplayMode, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		modes[agent.ID] = agent.DisplayMode
	}
	if modes["foreground"] != DisplayModeForeground ||
		modes["background"] != DisplayModeBackground ||
		modes["backgrounded"] != DisplayModeBackgrounded {
		t.Fatalf("derived display modes = %#v", modes)
	}
}
