package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
)

func TestSideQuestionDialogActionsAndPaste(t *testing.T) {
	d := NewSideQuestionDialog(Styles{})
	d.Show("")
	if action, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); action != sideQuestionDialogNone {
		t.Fatalf("empty Enter action = %v", action)
	}
	d.HandlePaste("what changed?")
	if action, question := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); action != sideQuestionDialogAsk || question != "what changed?" {
		t.Fatalf("Ask = (%v, %q)", action, question)
	}
	d.HandlePaste("\x1b[31munsafe")
	if !strings.Contains(d.feedback, "one line") {
		t.Fatalf("feedback = %q", d.feedback)
	}
	d.SetSnapshot(engine.SideQuestionSnapshot{Question: "what changed?", Status: engine.SideQuestionRunning})
	if action, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); action != sideQuestionDialogCancel || d.IsVisible() {
		t.Fatalf("running Esc = (%v, visible=%v)", action, d.IsVisible())
	}
}

func TestSideQuestionDialogAnswerScrollAndStage(t *testing.T) {
	d := NewSideQuestionDialog(Styles{})
	d.Show("why")
	d.SetSnapshot(engine.SideQuestionSnapshot{Question: "why", Answer: strings.Repeat("answer ", 50), Status: engine.SideQuestionAnswered})
	d.Overlay("base", 40, 12)
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if d.scroll == 0 {
		t.Fatal("end did not move answer scroll")
	}
	if action, payload := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); action != sideQuestionDialogStage || payload != "" {
		t.Fatalf("stage = (%v, %q)", action, payload)
	}
	d.SetSnapshot(engine.SideQuestionSnapshot{Question: "why", Status: engine.SideQuestionFailed})
	if action, question := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); action != sideQuestionDialogAsk || question != "why" {
		t.Fatalf("retry = (%v, %q)", action, question)
	}
}

func TestSideQuestionDialogGoldenWidths(t *testing.T) {
	d := NewSideQuestionDialog(Styles{})
	d.Show("question\x1b[31m")
	d.SetSnapshot(engine.SideQuestionSnapshot{Question: "question", Answer: "A short answer with control \x1b[31m and bidi \u202e bytes.", Status: engine.SideQuestionAnswered, ContextEntries: 3, ContextTruncated: true, StartedAt: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)})
	var frames []string
	for _, width := range []int{40, 80, 120, 180} {
		frame := d.Overlay("base", width, 12)
		plain := ansi.Strip(frame)
		if strings.Contains(plain, "\x1b") {
			t.Fatalf("width %d leaked terminal control: %q", width, frame)
		}
		if strings.Contains(plain, "\u202e") {
			t.Fatalf("width %d leaked bidi override: %q", width, frame)
		}
		if got := len(strings.Split(plain, "\n")); got != 12 {
			t.Fatalf("width %d rows = %d", width, got)
		}
		frames = append(frames, "-- width "+itoa(width)+" --\n"+plain)
	}
	got := strings.Join(frames, "\n")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile("testdata/side_question_dialog.golden", []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/side_question_dialog.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(frames, "\n"); strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
		t.Fatalf("golden mismatch:\n%s", got)
	}
}

func TestSideQuestionDialogLongAnswerKeepsControlsAndFeedback(t *testing.T) {
	d := NewSideQuestionDialog(defaultStyles())
	d.Show("why")
	d.SetSnapshot(engine.SideQuestionSnapshot{Status: engine.SideQuestionAnswered, Answer: strings.Repeat("long answer\n", 200)})
	d.SetFeedback("Use a normal draft first")
	frame := ansi.Strip(d.Overlay("", 40, 12))
	if !strings.Contains(frame, "Enter add") || !strings.Contains(frame, "Esc close") || !strings.Contains(frame, "Use a normal draft first") {
		t.Fatalf("clipped controls or feedback: %s", frame)
	}
	if d.answerBody == nil {
		t.Fatal("answer body was not cached")
	}
	d.SetRenderEnvironment(defaultRenderEnvironment(defaultStyles()))
	if d.answerBody == nil {
		t.Fatal("unchanged environment discarded wrapping")
	}
	d.SetRenderEnvironment(defaultRenderEnvironment(defaultStyles()).withGeometry())
	if d.answerBody != nil {
		t.Fatal("environment change retained stale wrapping")
	}
}
