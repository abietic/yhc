package session

import (
	"sync"
	"testing"
)

func TestStateMachineInitialState(t *testing.T) {
	sm := NewStateMachine()
	if got := sm.GetState(); got != StateIdle {
		t.Errorf("initial state = %q, want %q", got, StateIdle)
	}
}

func TestStateMachineTransitions(t *testing.T) {
	sm := NewStateMachine()

	sm.TransitionToRunning()
	if got := sm.GetState(); got != StateRunning {
		t.Errorf("state = %q, want running", got)
	}

	sm.TransitionToRequiresAction(RequiresActionDetails{
		ToolName:          "Bash",
		ActionDescription: "Running npm test",
		ToolUseID:         "tu-1",
		RequestID:         "req-1",
	})
	if got := sm.GetState(); got != StateRequiresAction {
		t.Errorf("state = %q, want requires_action", got)
	}

	sm.TransitionToIdle()
	if got := sm.GetState(); got != StateIdle {
		t.Errorf("state = %q, want idle", got)
	}
}

func TestStateChangedListener(t *testing.T) {
	sm := NewStateMachine()

	var received []State
	sm.OnStateChanged(func(state State, details *RequiresActionDetails) {
		received = append(received, state)
	})

	sm.TransitionToRunning()
	sm.TransitionToIdle()

	if len(received) != 2 {
		t.Fatalf("got %d state changes, want 2", len(received))
	}
	if received[0] != StateRunning || received[1] != StateIdle {
		t.Errorf("states = %v, want [running idle]", received)
	}
}

func TestRequiresActionDetails(t *testing.T) {
	sm := NewStateMachine()

	var capturedDetails *RequiresActionDetails
	sm.OnStateChanged(func(state State, details *RequiresActionDetails) {
		capturedDetails = details
	})

	sm.TransitionToRequiresAction(RequiresActionDetails{
		ToolName:          "Edit",
		ActionDescription: "Editing main.go",
		ToolUseID:         "tu-2",
		RequestID:         "req-2",
	})

	if capturedDetails == nil {
		t.Fatal("details should not be nil")
		return
	}
	if capturedDetails.ToolName != "Edit" {
		t.Errorf("ToolName = %q, want Edit", capturedDetails.ToolName)
	}
}

func TestMetadataListenerOnRequiresAction(t *testing.T) {
	sm := NewStateMachine()

	var metadataUpdates []ExternalMetadata
	sm.OnMetadataChanged(func(md ExternalMetadata) {
		metadataUpdates = append(metadataUpdates, md)
	})

	sm.TransitionToRequiresAction(RequiresActionDetails{
		ToolName:  "Bash",
		ToolUseID: "tu-3",
		RequestID: "req-3",
	})
	if len(metadataUpdates) != 1 {
		t.Fatalf("got %d metadata updates, want 1", len(metadataUpdates))
	}
	if metadataUpdates[0].PendingAction == nil {
		t.Error("PendingAction should be set on requires_action")
	}

	sm.TransitionToRunning()
	if len(metadataUpdates) != 2 {
		t.Fatalf("got %d metadata updates, want 2", len(metadataUpdates))
	}
	if metadataUpdates[1].PendingAction != nil {
		t.Error("PendingAction should be cleared on non-requires_action transition")
	}
}

func TestIdleClearsTaskSummary(t *testing.T) {
	sm := NewStateMachine()

	var metadataUpdates []ExternalMetadata
	sm.OnMetadataChanged(func(md ExternalMetadata) {
		metadataUpdates = append(metadataUpdates, md)
	})

	sm.TransitionToRunning()
	sm.TransitionToIdle()

	found := false
	for _, md := range metadataUpdates {
		if md.TaskSummary == nil && md.PendingAction == nil {
			found = true
		}
	}
	if !found {
		t.Error("idle transition should emit metadata with cleared task_summary")
	}
}

func TestPermissionModeChangedListener(t *testing.T) {
	sm := NewStateMachine()

	var modes []string
	sm.OnPermissionModeChanged(func(mode string) {
		modes = append(modes, mode)
	})

	sm.NotifyPermissionModeChanged("auto")
	sm.NotifyPermissionModeChanged("default")

	if len(modes) != 2 {
		t.Fatalf("got %d mode changes, want 2", len(modes))
	}
	if modes[0] != "auto" || modes[1] != "default" {
		t.Errorf("modes = %v", modes)
	}
}

func TestNotifyMetadataChanged(t *testing.T) {
	sm := NewStateMachine()

	var received []ExternalMetadata
	sm.OnMetadataChanged(func(md ExternalMetadata) {
		received = append(received, md)
	})

	model := "claude-sonnet-4-6"
	sm.NotifyMetadataChanged(ExternalMetadata{Model: &model})

	if len(received) != 1 {
		t.Fatalf("got %d metadata events, want 1", len(received))
	}
	if received[0].Model == nil || *received[0].Model != "claude-sonnet-4-6" {
		t.Error("model not propagated")
	}
}

func TestConcurrentAccess(t *testing.T) {
	sm := NewStateMachine()
	var wg sync.WaitGroup

	sm.OnStateChanged(func(state State, details *RequiresActionDetails) {})

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sm.TransitionToRunning()
			sm.GetState()
			sm.TransitionToIdle()
		}()
	}
	wg.Wait()

	if got := sm.GetState(); got != StateIdle {
		t.Errorf("final state = %q, want idle", got)
	}
}

func TestMultipleListeners(t *testing.T) {
	sm := NewStateMachine()
	var count1, count2 int

	sm.OnStateChanged(func(State, *RequiresActionDetails) { count1++ })
	sm.OnStateChanged(func(State, *RequiresActionDetails) { count2++ })

	sm.TransitionToRunning()

	if count1 != 1 || count2 != 1 {
		t.Errorf("counts = %d, %d; want 1, 1", count1, count2)
	}
}

func TestNoPendingActionClearWithoutPrior(t *testing.T) {
	sm := NewStateMachine()

	var metadataCount int
	sm.OnMetadataChanged(func(md ExternalMetadata) {
		metadataCount++
	})

	sm.TransitionToRunning()
	sm.TransitionToIdle()

	if metadataCount != 1 {
		t.Errorf("got %d metadata events, want 1 (just idle clear)", metadataCount)
	}
}
