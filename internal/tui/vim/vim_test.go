package vim

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func key(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune(s))}
}

func special(t rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: t}
}

func TestNewModel_Disabled(t *testing.T) {
	m := New()
	if m.IsEnabled() {
		t.Error("new model should be disabled")
	}
}

func TestEnable_StartsInNormalMode(t *testing.T) {
	m := New()
	m.Enable()
	if !m.IsEnabled() {
		t.Error("expected enabled")
	}
	if m.GetMode() != ModeNormal {
		t.Errorf("expected NORMAL mode, got %v", m.GetMode())
	}
}

func TestInsertMode_BasicTyping(t *testing.T) {
	m := New()
	m.Enable()
	m.Update(key("i"))
	if m.GetMode() != ModeInsert {
		t.Fatalf("expected INSERT, got %v", m.GetMode())
	}

	m.Update(key("h"))
	m.Update(key("e"))
	m.Update(key("l"))
	m.Update(key("l"))
	m.Update(key("o"))

	if m.Value() != "hello" {
		t.Errorf("value: got %q want %q", m.Value(), "hello")
	}
	if m.Cursor() != 5 {
		t.Errorf("cursor: got %d want 5", m.Cursor())
	}
}

func TestInsertMode_EscReturnsToNormal(t *testing.T) {
	m := New()
	m.Enable()
	m.Update(key("i"))
	m.Update(key("x"))
	m.Update(special(tea.KeyEscape))

	if m.GetMode() != ModeNormal {
		t.Errorf("expected NORMAL after Esc, got %v", m.GetMode())
	}
}

func TestNormalMode_HJKLMovement(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("hello\nworld")

	m.cursor = 2
	m.Update(key("l"))
	if m.Cursor() != 3 {
		t.Errorf("l: got %d want 3", m.Cursor())
	}
	m.Update(key("h"))
	if m.Cursor() != 2 {
		t.Errorf("h: got %d want 2", m.Cursor())
	}
	m.Update(key("j"))
	if m.Cursor() != 8 {
		t.Errorf("j: got %d want 8 (same col on next line), got %d", 8, m.Cursor())
	}
	m.Update(key("k"))
	if m.Cursor() != 2 {
		t.Errorf("k: got %d want 2", m.Cursor())
	}
}

func TestNormalMode_WordMovement(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("hello world foo")

	m.cursor = 0
	m.Update(key("w"))
	if m.Cursor() < 5 {
		t.Errorf("w: expected cursor past first word, got %d", m.Cursor())
	}
	m.Update(key("b"))
	if m.Cursor() != 0 {
		t.Errorf("b: expected cursor at 0, got %d", m.Cursor())
	}
}

func TestNormalMode_DeleteChar(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("abc")
	m.cursor = 1

	m.Update(key("x"))
	if m.Value() != "ac" {
		t.Errorf("x: got %q want %q", m.Value(), "ac")
	}
}

func TestNormalMode_Paste(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("ac")
	m.cursor = 0

	m.Update(key("x"))
	if m.Value() != "c" {
		t.Fatalf("after x: got %q", m.Value())
	}

	m.Update(key("p"))
	if m.Value() != "ca" {
		t.Errorf("after p: got %q want %q", m.Value(), "ca")
	}
}

func TestNormalMode_AppendEnd(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("hi")
	m.cursor = 0

	m.Update(key("A"))
	if m.GetMode() != ModeInsert {
		t.Fatal("A should enter insert")
	}
	if m.Cursor() != 2 {
		t.Errorf("A cursor: got %d want 2", m.Cursor())
	}
}

func TestVisualMode_SelectAndYank(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("hello")
	m.cursor = 1

	m.Update(key("v"))
	if m.GetMode() != ModeVisual {
		t.Fatal("v should enter visual")
	}

	m.Update(key("l"))
	m.Update(key("l"))

	sel := m.Selection()
	if sel != "ell" {
		t.Errorf("selection: got %q want %q", sel, "ell")
	}

	m.Update(key("y"))
	if m.GetMode() != ModeNormal {
		t.Error("y should return to normal")
	}
}

func TestVisualMode_Delete(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("abcdef")
	m.cursor = 1

	m.Update(key("v"))
	m.Update(key("l"))
	m.Update(key("l"))
	m.Update(key("d"))

	if m.Value() != "aef" {
		t.Errorf("after visual delete: got %q want %q", m.Value(), "aef")
	}
	if m.GetMode() != ModeNormal {
		t.Error("d should return to normal")
	}
}

func TestDeleteLine(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("first\nsecond\nthird")
	m.cursor = 7

	m.DeleteLine()
	if m.Value() != "first\nthird" {
		t.Errorf("DeleteLine: got %q want %q", m.Value(), "first\nthird")
	}
}

func TestYankLine(t *testing.T) {
	m := New()
	m.Enable()
	m.SetValue("line1\nline2\nline3")
	m.cursor = 7

	m.YankLine()
	if m.yankBuf != "line2\n" {
		t.Errorf("YankLine: got %q want %q", m.yankBuf, "line2\n")
	}
}

func TestDisabled_DoesNotConsume(t *testing.T) {
	m := New()
	consumed, _ := m.Update(key("i"))
	if consumed {
		t.Error("disabled model should not consume keys")
	}
}

func TestModeString(t *testing.T) {
	if ModeNormal.String() != "NORMAL" {
		t.Errorf("got %q", ModeNormal.String())
	}
	if ModeInsert.String() != "INSERT" {
		t.Errorf("got %q", ModeInsert.String())
	}
	if ModeVisual.String() != "VISUAL" {
		t.Errorf("got %q", ModeVisual.String())
	}
}

func TestStatusLine(t *testing.T) {
	m := New()
	if m.StatusLine() != "" {
		t.Error("disabled should return empty status")
	}
	m.Enable()
	if m.StatusLine() != "-- NORMAL --" {
		t.Errorf("got %q", m.StatusLine())
	}
}
