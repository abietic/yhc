package tui

import (
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/abietic/yhc/tools"
)

func TestPlanDialogFeedbackBackspacesUnicodeByRune(t *testing.T) {
	dialog, _ := p202PlanDialog(
		t,
		keybindings.NewResolver(),
		true,
		permission.ModeDefault,
		24,
	)
	enterP202Feedback(t, dialog)
	typeP202Feedback(dialog, "你好")

	if done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyBackspace}); done {
		t.Fatal("backspace unexpectedly closed the dialog")
	}
	if got := dialog.feedbackEditor.Value(); got != "你" || !utf8.ValidString(got) {
		t.Fatalf("feedback after backspace = %q", got)
	}
}

func TestQuestionDialogOtherAnswerBackspacesUnicodeByRune(t *testing.T) {
	dialog := NewQuestionDialog(StylesForTheme(ThemePolarNight))
	dialog.inOther = true
	dialog.otherText = "你好"

	if done, _ := dialog.handleOtherInput(tea.KeyPressMsg{Code: tea.KeyBackspace}); done {
		t.Fatal("backspace unexpectedly closed the dialog")
	}
	if dialog.otherText != "你" || !utf8.ValidString(dialog.otherText) {
		t.Fatalf("other answer after backspace = %q", dialog.otherText)
	}
}

func TestQuestionDialogNarrowOverlayDoesNotPanic(t *testing.T) {
	dialog := NewQuestionDialog(StylesForTheme(ThemePolarNight))
	dialog.questions = []tools.UserQuestion{{
		Question: "一个很长的问题",
		Options:  []tools.QuestionOption{{Label: "答案"}},
	}}
	dialog.answers["一个很长的问题"] = "答案"
	dialog.view = viewSubmit

	if got := dialog.Overlay("", 3, 8); got == "" {
		t.Fatal("narrow overlay rendered empty output")
	}
	if got := dialog.Overlay("", 3, 0); got != "" {
		t.Fatalf("zero-height overlay = %q, want empty output", got)
	}
}
