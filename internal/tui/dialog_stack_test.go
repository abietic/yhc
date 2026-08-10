package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestDialogStackPushPop(t *testing.T) {
	ds := &DialogStack{}
	if !ds.IsEmpty() {
		t.Error("new stack should be empty")
	}

	ds.Push(&mockDialog{id: "test1"})
	if ds.IsEmpty() {
		t.Error("stack should not be empty after push")
	}
	if ds.Front().DialogID() != "test1" {
		t.Errorf("front = %s, want test1", ds.Front().DialogID())
	}

	ds.Push(&mockDialog{id: "test2"})
	if ds.Front().DialogID() != "test2" {
		t.Errorf("front = %s, want test2", ds.Front().DialogID())
	}
	if ds.Len() != 2 {
		t.Errorf("len = %d, want 2", ds.Len())
	}

	ds.Pop()
	if ds.Front().DialogID() != "test1" {
		t.Errorf("front after pop = %s, want test1", ds.Front().DialogID())
	}

	ds.Pop()
	if !ds.IsEmpty() {
		t.Error("stack should be empty after all pops")
	}
}

func TestDialogStackContains(t *testing.T) {
	ds := &DialogStack{}
	ds.Push(&mockDialog{id: "alpha"})
	ds.Push(&mockDialog{id: "beta"})

	if !ds.Contains("alpha") {
		t.Error("should contain alpha")
	}
	if !ds.Contains("beta") {
		t.Error("should contain beta")
	}
	if ds.Contains("gamma") {
		t.Error("should not contain gamma")
	}
}

func TestDialogStackPopByID(t *testing.T) {
	ds := &DialogStack{}
	ds.Push(&mockDialog{id: "a"})
	ds.Push(&mockDialog{id: "b"})
	ds.Push(&mockDialog{id: "c"})

	ds.PopByID("b")
	if ds.Contains("b") {
		t.Error("b should be removed")
	}
	if ds.Len() != 2 {
		t.Errorf("len = %d, want 2", ds.Len())
	}
	if ds.Front().DialogID() != "c" {
		t.Errorf("front = %s, want c", ds.Front().DialogID())
	}
}

func TestDialogStackGracePeriod(t *testing.T) {
	ds := &DialogStack{}
	ds.PushWithGrace(&mockDialog{id: "grace"})

	msg := tea.KeyPressMsg{Code: tea.KeyEnter}

	// Immediately after push, key should be absorbed
	action, consumed := ds.HandleKey(msg)
	if !consumed {
		t.Error("key should be consumed during grace period")
	}
	if _, ok := action.(DialogActionNone); !ok {
		t.Errorf("action should be DialogActionNone during grace, got %T", action)
	}

	// After grace period expires, key should reach the dialog
	time.Sleep(250 * time.Millisecond)
	action, consumed = ds.HandleKey(msg)
	if !consumed {
		t.Error("key should still be consumed (by dialog)")
	}
	if _, ok := action.(DialogActionClose); !ok {
		t.Errorf("expected DialogActionClose from mock, got %T", action)
	}
}

// --- Mock dialog for testing ---

type mockDialog struct {
	id string
}

func (m *mockDialog) DialogID() string { return m.id }

func (m *mockDialog) HandleDialogKey(_ tea.KeyPressMsg) DialogAction {
	return DialogActionClose{}
}

func (m *mockDialog) RenderOverlay(base string, _, _ int) string {
	return base + "\n[DIALOG:" + m.id + "]"
}
