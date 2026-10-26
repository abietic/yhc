package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

type sideQuestionUIModel struct{ calls atomic.Int32 }

func (m *sideQuestionUIModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("side questions require the streaming boundary")
}

func (m *sideQuestionUIModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls.Add(1)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "A literal answer"}}), nil
}

func newSideQuestionApp(t *testing.T) (*App, *sideQuestionUIModel) {
	t.Helper()
	m := &sideQuestionUIModel{}
	e := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: m, Model: "fixture", CommandEntrypoint: commands.EntrypointTUI})
	t.Cleanup(e.Close)
	return New(Config{Engine: e, Resumed: true, KeybindingsConfigDir: t.TempDir()}), m
}

func TestSideQuestionAppMainTurnAndDraftStayIndependent(t *testing.T) {
	a, m := newSideQuestionApp(t)
	a.running = true
	a.queryID = 17
	a.textarea.SetValue("Existing draft")
	beforeMessages := len(a.engine.GetMessages())
	cmd := a.openSideQuestion("Explain the error", nil)
	if cmd == nil || m.calls.Load() != 0 || !a.hasDialog(StateSideQuestion) {
		t.Fatal("opening dispatched eagerly or missed modal")
	}
	a.handleSideQuestionResult(cmd().(sideQuestionResultMsg))
	if !a.running || a.queryID != 17 || a.textarea.Value() != "Existing draft" || len(a.engine.GetMessages()) != beforeMessages {
		t.Fatal("answer altered main execution or draft")
	}
	a.handleSideQuestionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.hasDialog(StateSideQuestion) || m.calls.Load() != 1 || !a.running {
		t.Fatal("stage did not close, or submitted/interrupted main")
	}
	if got := a.textarea.Value(); !strings.HasPrefix(got, "Existing draft\n\nSide question context (captured ") {
		t.Fatal("stage lost draft")
	}
	snapshot, err := a.captureComposerSubmission()
	if err != nil || snapshot.Text == "" {
		t.Fatalf("staged text is not submit-ready: %v", err)
	}
	if !containsAll(snapshot.Text, "Existing draft", "Explain the error", "A literal answer") {
		t.Fatalf("wrong staged content %q", snapshot.Text)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func TestSideQuestionAppCancelBeforeDispatchAndLateDelivery(t *testing.T) {
	a, m := newSideQuestionApp(t)
	a.running = true
	a.textarea.SetValue("unchanged")
	cmd := a.openSideQuestion("Explain", nil)
	a.handleSideQuestionKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	a.handleSideQuestionResult(cmd().(sideQuestionResultMsg))
	if m.calls.Load() != 0 || a.hasDialog(StateSideQuestion) || !a.running || a.textarea.Value() != "unchanged" {
		t.Fatal("cancelled side question leaked or cancelled the leader")
	}
}

func TestSideQuestionAppPasteAndEngineReplacementFence(t *testing.T) {
	a, m := newSideQuestionApp(t)
	a.textarea.SetValue("retained draft")
	a.openSideQuestion("", nil)
	a.Update(tea.PasteMsg{Content: "Explain pasted question"})
	if a.textarea.Value() != "retained draft" || m.calls.Load() != 0 {
		t.Fatal("modal paste escaped into draft or provider")
	}
	cmd := a.handleSideQuestionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("question was not accepted")
	}
	a.SetEngine(nil)
	a.handleSideQuestionResult(cmd().(sideQuestionResultMsg))
	if m.calls.Load() != 0 || a.hasDialog(StateSideQuestion) || a.textarea.Value() != "retained draft" {
		t.Fatal("replaced engine accepted a stale answer")
	}
}

func TestSideQuestionSlashDoesNotAppendMainHistory(t *testing.T) {
	a, m := newSideQuestionApp(t)
	a.running = true
	a.textarea.SetValue("/btw Explain")
	before := len(a.chat.items)
	cmd := a.sendMessage()
	if cmd == nil || len(a.chat.items) != before || a.textarea.Value() != "" || !a.running {
		t.Fatal("slash question changed main history or execution")
	}
	a.handleSideQuestionResult(cmd().(sideQuestionResultMsg))
	if m.calls.Load() != 1 {
		t.Fatal("side question not dispatched once")
	}
}

func TestSideQuestionPalettePreservesRichDraftAndRecordsOnlyAnswer(t *testing.T) {
	a, m := newSideQuestionApp(t)
	a.handleComposerPaste(tea.PasteMsg{Content: strings.Repeat("original context\n", 100)})
	before := a.textarea.Value()
	openPaletteForm(t, a, "btw")
	a.commandPalette.form.inputs[0].SetValue("Explain")
	_, cmd := a.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || a.textarea.Value() != before || len(a.composerElements) != 1 || len(a.commandPalette.recent) != 0 {
		t.Fatal("palette changed the rich draft or recorded eager recency")
	}
	a.handleSideQuestionResult(cmd().(sideQuestionResultMsg))
	if m.calls.Load() != 1 || len(a.commandPalette.recent) != 1 || a.commandPalette.recent[0] != "btw" {
		t.Fatal("answer did not settle recency")
	}
	a.handleSideQuestionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	snapshot, err := a.captureComposerSubmission()
	if err != nil || len(a.composerElements) != 1 || !containsAll(snapshot.Text, "original context", "A literal answer") {
		t.Fatal("stage lost the existing context")
	}
}

func TestSideQuestionUnavailableAndCommandDraftKeepTheirInput(t *testing.T) {
	unavailable := New(Config{})
	unavailable.textarea.SetValue("/btw explain")
	if cmd := unavailable.sendMessage(); cmd != nil || unavailable.textarea.Value() != "/btw explain" {
		t.Fatal("unavailable question consumed its draft")
	}
	a, _ := newSideQuestionApp(t)
	cmd := a.openSideQuestion("Explain", nil)
	a.handleSideQuestionResult(cmd().(sideQuestionResultMsg))
	a.textarea.SetValue("/model another")
	a.inputMode = InputCommand
	a.handleSideQuestionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.textarea.Value() != "/model another" || !a.hasDialog(StateSideQuestion) {
		t.Fatal("staging modified a command")
	}
	if !strings.Contains(stripANSIForTest(a.sideQuestionDialog.Overlay("", 80, 12)), "normal message draft") {
		t.Fatal("staging rejection was not visible")
	}
}
