package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

func installComposerSuggestion(app *App, text string) {
	app.composerSuggestion = composerSuggestionState{
		Text:       text,
		ThreadID:   app.activeThreadViewID(),
		Revision:   app.composerRevision,
		QueryID:    app.queryID,
		Generation: 1,
	}
}

func TestComposerSuggestionIsRenderOnlyUntilExplicitAccept(t *testing.T) {
	app := newTestApp(80, 24)
	installComposerSuggestion(app, "\u8fd0\u884c focused tests")

	beforeRevision := app.composerRevision
	beforeUndo := len(app.composerUndo)
	rendered := xansi.Strip(app.renderEditor())
	if !strings.Contains(rendered, "\u8fd0\u884c focused tests") {
		t.Fatalf("ghost suggestion is not visible: %q", rendered)
	}
	if got := app.textarea.Value(); got != "" {
		t.Fatalf("render mutated textarea = %q", got)
	}
	if app.composerRevision != beforeRevision || len(app.composerUndo) != beforeUndo {
		t.Fatal("render mutated composer revision or undo")
	}

	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := app.textarea.Value(); got != "\u8fd0\u884c focused tests" {
		t.Fatalf("Tab accepted value = %q", got)
	}
	if app.visibleComposerSuggestion() != "" {
		t.Fatal("accepted suggestion remained visible")
	}
	if len(app.composerUndo) != beforeUndo+1 {
		t.Fatalf("accept undo entries = %d, want %d", len(app.composerUndo), beforeUndo+1)
	}
}

func TestComposerSuggestionRightAcceptsButEnterDoesNot(t *testing.T) {
	app := newTestApp(80, 24)
	installComposerSuggestion(app, "run the tests")
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := app.textarea.Value(); got != "" {
		t.Fatalf("Enter accepted ghost text = %q", got)
	}
	if app.visibleComposerSuggestion() != "" {
		t.Fatal("Enter did not dismiss the unaccepted suggestion")
	}

	installComposerSuggestion(app, "run the tests")
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyRight})
	if got := app.textarea.Value(); got != "run the tests" {
		t.Fatalf("Right accepted value = %q", got)
	}
}

func TestRightArrowDoesNotAcceptOrdinaryAutocomplete(t *testing.T) {
	app := newTestApp(80, 24)
	app.inputMode = InputCommand
	app.textarea.SetValue("/")
	app.commandHints = app.commandRegistry.List()
	app.commandHintIdx = 0
	if len(app.commandHints) == 0 {
		t.Fatal("test requires at least one command hint")
	}

	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyRight})
	if got := app.textarea.Value(); got != "/" {
		t.Fatalf("Right accepted ordinary autocomplete = %q", got)
	}
}

func TestComposerSuggestionTypingCancelsAndStaleResultCannotReturn(t *testing.T) {
	app := newTestApp(80, 24)
	request := &composerSuggestionRequest{
		Engine:     app.engine,
		Generation: 1,
		ThreadID:   app.activeThreadViewID(),
		Revision:   app.composerRevision,
		QueryID:    app.queryID,
		Cancel:     func() {},
	}
	app.composerSuggestionRequest = request
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyExtended, Text: "x"})
	if app.composerSuggestionRequest != nil {
		t.Fatal("typing did not cancel the pending suggestion")
	}

	updateAppSilent(app, composerSuggestionMsg{
		Engine:     request.Engine,
		Generation: request.Generation,
		ThreadID:   request.ThreadID,
		Revision:   request.Revision,
		QueryID:    request.QueryID,
		Suggestion: "stale text",
	})
	if got := app.visibleComposerSuggestion(); got != "" {
		t.Fatalf("stale suggestion became visible = %q", got)
	}
	if got := app.textarea.Value(); got != "x" {
		t.Fatalf("stale result changed input = %q", got)
	}
}

func TestComposerSuggestionIsSuppressedOutsideIdleLeaderNormalInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*App)
	}{
		{name: "nonempty", mutate: func(app *App) { app.textarea.SetValue("draft") }},
		{name: "running", mutate: func(app *App) { app.running = true }},
		{name: "command", mutate: func(app *App) { app.inputMode = InputCommand }},
		{name: "history search", mutate: func(app *App) { app.historySearch.Active = true }},
		{name: "command hints", mutate: func(app *App) { app.commandHints = app.commandRegistry.List() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := newTestApp(80, 24)
			installComposerSuggestion(app, "run the tests")
			test.mutate(app)
			if got := app.visibleComposerSuggestion(); got != "" {
				t.Fatalf("suppressed suggestion = %q", got)
			}
		})
	}
}

func TestComposerSuggestionRenderStaysWithinEditorWidths(t *testing.T) {
	for _, width := range []int{40, 80, 120, 180} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			app := newTestApp(width, 24)
			installComposerSuggestion(app, "继续修复输入框并运行 focused tests 🚀")
			rendered := app.renderEditor()
			if !strings.Contains(xansi.Strip(rendered), "继续") {
				t.Fatalf("width %d lost the ghost prefix: %q", width, rendered)
			}
			for _, line := range strings.Split(rendered, "\n") {
				if got := xansi.StringWidth(line); got > app.layout.width {
					t.Fatalf("width %d rendered %d cells: %q", width, got, line)
				}
			}
		})
	}
}

func TestCompletedTerminalRequestsSuggestionButFailureDoesNot(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD:                     t.TempDir(),
		TranscriptDir:           t.TempDir(),
		CommandEntrypoint:       commands.EntrypointTUI,
		EnablePromptSuggestions: true,
	})
	t.Cleanup(eng.Close)
	app := newTestApp(80, 24)
	app.SetEngine(eng)
	app.running = true
	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventAssistant,
		AssistantMessage: &schema.Message{
			Role:    schema.Assistant,
			Content: "done",
		},
	})

	completed := app.handleEngineEvent(engine.QueryEvent{
		Type:         engine.EventTerminal,
		TerminalInfo: &engine.Terminal{Reason: engine.TerminalCompleted},
	})
	if completed == nil || app.composerSuggestionRequest == nil {
		t.Fatal("completed terminal did not start a suggestion request")
	}
	app.dismissComposerSuggestion()
	app.running = true
	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventAssistant,
		AssistantMessage: &schema.Message{
			Role:    schema.Assistant,
			Content: "failed",
		},
	})

	failed := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalModelError,
		},
	})
	if failed != nil || app.composerSuggestionRequest != nil {
		t.Fatal("failed terminal started a suggestion request")
	}

	app.running = true
	commandOnly := app.handleEngineEvent(engine.QueryEvent{
		Type:         engine.EventTerminal,
		TerminalInfo: &engine.Terminal{Reason: engine.TerminalCompleted},
	})
	if commandOnly != nil || app.composerSuggestionRequest != nil {
		t.Fatal("completed turn without an assistant response started a suggestion request")
	}
}
