package tui

import (
	"os"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/services"
)

func TestComposerSuggestionManualShortcutKeepsPendingRequestAndShowsFeedback(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), CommandEntrypoint: commands.EntrypointTUI,
	})
	t.Cleanup(eng.Close)
	app := newTestApp(80, 24)
	app.SetEngine(eng)
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	request := app.composerSuggestionRequest
	if request == nil || !request.Manual || !strings.Contains(app.renderHintSection(), "Thinking") {
		t.Fatal("manual request missing or cancelled by editor action dispatch")
	}
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	if app.composerSuggestionRequest != request {
		t.Fatal("duplicate shortcut replaced the in-flight request")
	}
	app.handleComposerSuggestion(composerSuggestionMsg{
		Engine: request.Engine, ThreadID: request.ThreadID, Revision: request.Revision,
		QueryID: request.QueryID, Generation: request.Generation,
		Result: services.PromptSuggestionResult{Status: services.PromptSuggestionSuppressed, Reason: "disabled"},
	})
	if app.textarea.Value() != "" || !strings.Contains(app.renderHintSection(), "disabled") {
		t.Fatal("explicit suppression did not preserve draft and explain outcome")
	}
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.hasComposerSuggestionActivity() {
		t.Fatal("escape did not dismiss feedback")
	}
}

func TestComposerSuggestionManualFeedbackIsEphemeralAndFenced(t *testing.T) {
	for _, status := range []services.PromptSuggestionStatus{
		services.PromptSuggestionSuppressed, services.PromptSuggestionEmpty,
		services.PromptSuggestionFiltered, services.PromptSuggestionTimedOut,
		services.PromptSuggestionFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			app := newTestApp(40, 24)
			request := &composerSuggestionRequest{
				Engine: app.engine, ThreadID: app.activeThreadViewID(),
				Revision: app.composerRevision, QueryID: app.queryID, Generation: 1,
				Manual: true, Cancel: func() {},
			}
			app.composerSuggestionRequest = request
			before := len(app.chat.Items())
			app.handleComposerSuggestion(composerSuggestionMsg{
				Engine: request.Engine, ThreadID: request.ThreadID, Revision: request.Revision,
				QueryID: request.QueryID, Generation: request.Generation,
				Result: services.PromptSuggestionResult{Status: status, Reason: "private error details"},
			})
			if app.composerSuggestion.Feedback == "" || strings.Contains(app.composerSuggestion.Feedback, "private") {
				t.Fatal("feedback is missing or contains untrusted details")
			}
			if len(app.chat.Items()) != before || app.textarea.Value() != "" {
				t.Fatal("feedback mutated transcript projection or draft")
			}
			updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyExtended, Text: "x"})
			if app.hasComposerSuggestionActivity() || app.renderComposerSuggestionFeedback() != "" {
				t.Fatal("editing retained old feedback")
			}
		})
	}
}

func TestComposerSuggestionAutomaticSuppressionStaysQuiet(t *testing.T) {
	app := newTestApp(80, 24)
	app.composerSuggestionRequest = &composerSuggestionRequest{
		Engine: app.engine, ThreadID: app.activeThreadViewID(), Revision: app.composerRevision,
		QueryID: app.queryID, Generation: 1, Cancel: func() {},
	}
	request := app.composerSuggestionRequest
	app.handleComposerSuggestion(composerSuggestionMsg{
		Engine: request.Engine, ThreadID: request.ThreadID, Revision: request.Revision,
		QueryID: request.QueryID, Generation: request.Generation,
		Result: services.PromptSuggestionResult{Status: services.PromptSuggestionFailed},
	})
	if app.hasComposerSuggestionActivity() {
		t.Fatal("automatic failure produced feedback")
	}
}

func TestComposerSuggestionFeedbackWidthGolden(t *testing.T) {
	var got strings.Builder
	for _, width := range []int{40, 80, 120, 180} {
		for _, test := range []struct {
			reason string
			token  string
		}{
			{"goal_active", "Goal"},
			{"early_conversation", "messages"},
			{"disabled", "disabled"},
		} {
			app := newTestApp(width, 24)
			app.composerSuggestion = composerSuggestionState{
				ThreadID: app.activeThreadViewID(), Revision: app.composerRevision, QueryID: app.queryID,
				Feedback: composerSuggestionFeedback(services.PromptSuggestionResult{Status: services.PromptSuggestionSuppressed, Reason: test.reason}),
			}
			rendered := normalizeAppLayoutGolden(app.renderHintSection())
			if !strings.Contains(rendered, test.token) {
				t.Fatalf("width %d hides %s cause: %s", width, test.reason, rendered)
			}
			for _, row := range strings.Split(rendered, "\n") {
				if app.renderEnvironment.profile.measure(row, 0) > width {
					t.Fatalf("feedback overflows width %d", width)
				}
			}
			got.WriteString("== " + strconv.Itoa(width) + " " + test.reason + " ==\n" + rendered + "\n")
		}
	}
	path := "testdata/composer_suggestion_feedback.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("suggestion feedback golden mismatch:\n%s", got.String())
	}
}
