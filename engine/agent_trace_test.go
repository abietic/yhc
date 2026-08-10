package engine

import (
	"errors"
	"reflect"
	"testing"
)

func TestAgentParentTraceSnapshotsAreBoundedAndCanonical(t *testing.T) {
	store := NewRuntimeStateStore()
	launch := runtimeTestEvent(1, "agent-launch:agent-1:1", EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{
			Phase: "launched", Name: "explorer", Task: "inspect trace", Status: "running", TranscriptPath: "/tmp/agent-1.jsonl",
		}
	})
	stream := runtimeTestEvent(2, "turn-1", EventStreamRequestStart, nil)
	progress := runtimeTestEvent(3, "turn-1", EventTaskProgress, func(evt *QueryEvent) {
		evt.TaskProgress = &TaskProgressEvent{
			TaskID: "agent-1", ToolUseID: "spawn-call", Summary: "checking parent trace", LastToolName: "Bash",
			Usage: TaskProgressUsage{ToolUses: 5, TotalTokens: 1234},
			RecentActivities: []TaskProgressActivity{
				{ToolName: "Read", Description: "one"},
				{ToolName: "Grep", Description: "two"},
				{ToolName: "Glob", Description: "three"},
				{ToolName: "Edit", Description: "four"},
				{ToolName: "Bash", Description: "five"},
			},
		}
	})
	attention := runtimeTestEvent(4, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{ToolUseID: "approval-1", ToolName: "Bash", Message: "run tests"}
	})
	if err := store.Replay([]QueryEvent{launch, stream, progress, attention}); err != nil {
		t.Fatal(err)
	}

	traces := store.AgentParentTraceSnapshots()
	if len(traces) != 1 {
		t.Fatalf("trace count = %d, want 1", len(traces))
	}
	trace := traces[0]
	if trace.AgentID != "agent-1" || trace.ParentToolUseID != "spawn-call" || trace.Status != "waiting_input" || trace.UnresolvedCount != 1 {
		t.Fatalf("canonical trace identity/status = %#v", trace)
	}
	if trace.ToolUses != 5 || trace.TotalTokens != 1234 || trace.TranscriptPath != "/tmp/agent-1.jsonl" {
		t.Fatalf("canonical trace metadata = %#v", trace)
	}
	wantActivities := []string{"three", "four", "five"}
	gotActivities := make([]string, 0, len(trace.RecentActivities))
	for _, activity := range trace.RecentActivities {
		gotActivities = append(gotActivities, activity.Description)
	}
	if !reflect.DeepEqual(gotActivities, wantActivities) {
		t.Fatalf("bounded activity tail = %#v, want %#v", gotActivities, wantActivities)
	}

	trace.RecentActivities[0].Description = "mutated"
	if after := store.AgentParentTraceSnapshots()[0].RecentActivities[0].Description; after == "mutated" {
		t.Fatal("trace snapshot activity mutation leaked into runtime store")
	}

	resolved := runtimeTestEvent(5, "turn-1", EventPermissionResolved, func(evt *QueryEvent) {
		evt.PermissionResolved = &PermissionResolvedEvent{ToolUseID: "approval-1", Decision: "deny"}
	})
	terminal := runtimeTestEvent(6, "turn-1", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalModelError, Err: errors.New("model failed")}
	})
	if err := store.Replay([]QueryEvent{resolved, terminal}); err != nil {
		t.Fatal(err)
	}
	trace = store.AgentParentTraceSnapshots()[0]
	if trace.Status != "failed" || trace.TerminalReason != TerminalModelError || trace.Error != "model failed" || trace.UnresolvedCount != 0 {
		t.Fatalf("terminal trace did not converge: %#v", trace)
	}
}
