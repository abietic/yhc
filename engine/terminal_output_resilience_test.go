package engine

import "testing"

func TestP150RuntimeSnapshotsSurviveSkippedPresentationFrames(t *testing.T) {
	store := NewRuntimeStateStore()
	request := runtimeTestEvent(1, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{
			ToolName:  "Bash",
			ToolUseID: "p150-attention",
			Message:   "confirm terminal stress command",
			Input:     map[string]any{"command": "go test ./..."},
		}
	})
	terminal := runtimeTestEvent(2, "turn-1", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted, TurnCount: 1}
	})
	if err := store.Replay([]QueryEvent{request, terminal}); err != nil {
		t.Fatalf("reduce lossless runtime events: %v", err)
	}

	// The terminal fixture intentionally projects only the final presentation
	// frame. Runtime reduction happened first, so skipped/coalesced frames cannot
	// erase terminal identity or unresolved attention.
	projectedFrames := []QueryEvent{terminal}
	if len(projectedFrames) != 1 || projectedFrames[0].Type != EventTerminal {
		t.Fatalf("unexpected presentation fixture: %#v", projectedFrames)
	}

	snapshot := store.Snapshot("thread-1")
	thread := snapshot.Threads["thread-1"]
	if snapshot.UnresolvedCount != 1 {
		t.Fatalf("unresolved count = %d, want 1", snapshot.UnresolvedCount)
	}
	if _, ok := thread.PendingInteractions["p150-attention"]; !ok {
		t.Fatalf("unresolved attention was lost: %#v", thread.PendingInteractions)
	}
	if thread.LastTerminal == nil ||
		thread.LastTerminal.Reason != TerminalCompleted ||
		thread.LastTerminal.Sequence != 2 {
		t.Fatalf("terminal snapshot = %#v, want completed sequence 2", thread.LastTerminal)
	}
}
