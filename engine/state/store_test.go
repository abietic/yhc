package state

import (
	"fmt"
	"sync"
	"testing"
)

type change struct {
	field string
	old   any
	new   any
}

func TestAppStateDefaultsAndBasicMutations(t *testing.T) {
	state := NewAppState("session-1", "/repo", "claude")
	if state.SessionID != "session-1" || state.CWD != "/repo" || state.GetModel() != "claude" {
		t.Fatalf("unexpected initial state: %#v", state)
	}
	if state.GetPermissionMode() != "default" || state.GetPlanMode() {
		t.Fatalf("unexpected initial modes: permission=%q plan=%v", state.GetPermissionMode(), state.GetPlanMode())
	}
	if state.StartTime.IsZero() || state.DisabledTools == nil || state.ActiveAgents == nil || state.Extra == nil {
		t.Fatalf("initial maps/start time not initialized: %#v", state)
		return
	}

	state.SetModel("claude-new")
	state.SetPermissionMode("strict")
	state.SetPlanMode(true)
	state.SetProcessing(true)
	state.IncrementTurn()
	state.IncrementTurn()
	state.AddTokenUsage(100, 200)
	state.AddTokenUsage(50, 25)

	input, output := state.GetTokenUsage()
	if state.GetModel() != "claude-new" || state.GetPermissionMode() != "strict" || !state.GetPlanMode() {
		t.Fatalf("mutations not reflected in getters")
	}
	if state.GetTurnCount() != 2 || !state.IsCurrentlyProcessing() || input != 150 || output != 225 {
		t.Fatalf("unexpected runtime counters: turns=%d processing=%v tokens=%d/%d", state.GetTurnCount(), state.IsCurrentlyProcessing(), input, output)
	}
}

func TestChangeHandlersReceiveOldAndNewValues(t *testing.T) {
	state := NewAppState("session-1", "/repo", "claude")
	var got []change
	state.OnChange(func(field string, oldValue, newValue any) {
		got = append(got, change{field: field, old: oldValue, new: newValue})
	})

	state.SetModel("claude-new")
	state.SetPermissionMode("permissive")
	state.SetPlanMode(true)
	state.IncrementTurn()
	state.SetProcessing(true)
	state.RegisterAgent("agent-1", "worker", "task-1")
	state.SetAgentStatus("agent-1", "idle")
	state.SetAgentStatus("missing", "idle")
	state.UnregisterAgent("agent-1")

	want := []change{
		{field: "Model", old: "claude", new: "claude-new"},
		{field: "PermissionMode", old: "default", new: "permissive"},
		{field: "PlanMode", old: false, new: true},
		{field: "TurnCount", old: 0, new: 1},
		{field: "IsProcessing", old: false, new: true},
		{field: "ActiveAgents", old: nil, new: "agent-1"},
		{field: "AgentStatus:agent-1", old: "running", new: "idle"},
		{field: "ActiveAgents", old: "agent-1", new: nil},
	}
	if len(got) != len(want) {
		t.Fatalf("changes len=%d want=%d got=%#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("change[%d] = %#v want %#v", i, got[i], want[i])
		}
	}
}

func TestAgentRegistryCopiesAndSnapshot(t *testing.T) {
	state := NewAppState("session-1", "/repo", "claude")
	state.RegisterAgent("agent-1", "worker", "task-1")
	state.RegisterAgent("agent-2", "helper", "task-2")
	state.SetAgentStatus("agent-2", "idle")
	state.SetPlanMode(true)
	state.SetProcessing(true)
	state.IncrementTurn()
	state.AddTokenUsage(12, 34)

	agents := state.GetActiveAgents()
	if len(agents) != 2 || agents["agent-1"].Status != "running" || agents["agent-2"].Status != "idle" {
		t.Fatalf("unexpected agents: %#v", agents)
	}
	agents["agent-1"].Status = "mutated"
	delete(agents, "agent-2")
	fresh := state.GetActiveAgents()
	if len(fresh) != 2 || fresh["agent-1"].Status != "running" {
		t.Fatalf("GetActiveAgents did not return defensive copies: %#v", fresh)
	}

	snapshot := state.Snapshot()
	if snapshot.SessionID != "session-1" || snapshot.Model != "claude" || snapshot.PermissionMode != "default" {
		t.Fatalf("unexpected snapshot identity: %#v", snapshot)
	}
	if !snapshot.PlanMode || !snapshot.IsProcessing || snapshot.TurnCount != 1 || snapshot.InputTokens != 12 || snapshot.OutputTokens != 34 || snapshot.ActiveAgentCount != 2 {
		t.Fatalf("unexpected snapshot counters: %#v", snapshot)
	}

	state.UnregisterAgent("agent-1")
	state.UnregisterAgent("agent-2")
	if len(state.GetActiveAgents()) != 0 || state.Snapshot().ActiveAgentCount != 0 {
		t.Fatalf("agents not removed")
	}
}

func TestConcurrentStateUpdates(t *testing.T) {
	state := NewAppState("session-concurrent", "/repo", "claude")
	const workers = 20
	const loops = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < loops; j++ {
				state.IncrementTurn()
				state.AddTokenUsage(1, 2)
				state.SetModel(fmt.Sprintf("claude-%d", i))
				state.RegisterAgent(fmt.Sprintf("agent-%d", i), "worker", "task")
				state.SetAgentStatus(fmt.Sprintf("agent-%d", i), "idle")
			}
		}(i)
	}
	wg.Wait()

	input, output := state.GetTokenUsage()
	if state.GetTurnCount() != workers*loops || input != workers*loops || output != workers*loops*2 {
		t.Fatalf("unexpected concurrent counters: turns=%d tokens=%d/%d", state.GetTurnCount(), input, output)
	}
	if len(state.GetActiveAgents()) != workers {
		t.Fatalf("expected one agent per worker, got %d", len(state.GetActiveAgents()))
	}
}
