package tui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	charmeditor "github.com/charmbracelet/x/editor"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/identity"
)

func TestYHCPublicProductProjections(t *testing.T) {
	app := newTestApp(100, 30)
	view := app.View()
	if view.WindowTitle != identity.CommandName {
		t.Fatalf("window title = %q, want %q", view.WindowTitle, identity.CommandName)
	}
	header := app.renderHeader()
	if !strings.Contains(header, identity.ProductName) || strings.Contains(header, "Eino Agent") {
		t.Fatalf("header projects a noncanonical product identity: %q", header)
	}

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("SNAP_REVISION", "1")
	if _, err := externalEditorCommand(filepath.Join(t.TempDir(), "prompt.md")); err == nil || !strings.Contains(err.Error(), identity.CommandName) ||
		strings.Contains(err.Error(), identity.LegacyCommandName) {
		t.Fatalf("external editor error projects a noncanonical command: %v", err)
	}

	t.Setenv("SNAP_REVISION", "")
	t.Setenv("VISUAL", filepath.Join(t.TempDir(), "editor"))
	_, path, err := prepareComposerEditor("draft", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if base := filepath.Base(path); !strings.HasPrefix(base, identity.CommandName+"-prompt-") ||
		strings.Contains(base, identity.LegacyCommandName) {
		t.Fatalf("composer temporary file = %q", base)
	}
}

func TestP203ExternalEditorCommandResolution(t *testing.T) {
	t.Run("VISUAL overrides EDITOR and keeps GUI position syntax", func(t *testing.T) {
		editorPath := filepath.Join(t.TempDir(), "code")
		t.Setenv("VISUAL", editorPath+" --wait")
		t.Setenv("EDITOR", filepath.Join(t.TempDir(), "ignored")+" --flag")
		planPath := filepath.Join(t.TempDir(), "计划 with space.md")

		command, err := externalEditorCommand(
			planPath,
			charmeditor.AtPosition(4, 7),
		)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			editorPath,
			"--wait",
			"--goto",
			planPath + ":4:7",
		}
		if !reflect.DeepEqual(command.Args, want) {
			t.Fatalf("VISUAL args = %#v, want %#v", command.Args, want)
		}
	})

	t.Run("EDITOR arguments retain terminal editor position syntax", func(t *testing.T) {
		editorPath := filepath.Join(t.TempDir(), "nvim")
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", editorPath+" -f")
		planPath := filepath.Join(t.TempDir(), "计划.md")

		command, err := externalEditorCommand(
			planPath,
			charmeditor.AtPosition(4, 7),
		)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			editorPath,
			"-f",
			"+call cursor(4,7)",
			planPath,
		}
		if !reflect.DeepEqual(command.Args, want) {
			t.Fatalf("EDITOR args = %#v, want %#v", command.Args, want)
		}
	})

	t.Run("Snap guard remains fail closed", func(t *testing.T) {
		t.Setenv("SNAP_REVISION", "1")
		t.Setenv("VISUAL", filepath.Join(t.TempDir(), "code")+" --wait")
		if _, err := externalEditorCommand(
			filepath.Join(t.TempDir(), "plan.md"),
		); err == nil || !strings.Contains(err.Error(), "Snap") {
			t.Fatalf("Snap error = %v", err)
		}
	})

	t.Run("missing configured editor reports a process error", func(t *testing.T) {
		t.Setenv("VISUAL", filepath.Join(t.TempDir(), "missing-editor"))
		command, err := externalEditorCommand(
			filepath.Join(t.TempDir(), "plan.md"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Run(); err == nil {
			t.Fatal("missing editor unexpectedly succeeded")
		}
	})

	t.Run("unset environment uses the x editor platform default", func(t *testing.T) {
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "")
		want := "nano"
		if runtime.GOOS == "windows" {
			want = "notepad"
		}
		if got := externalEditorDisplayName(); got != want {
			t.Fatalf("display name = %q, want %q", got, want)
		}
	})
}

func TestP203PlanEditorResultRestoresExactPresentation(t *testing.T) {
	app, planPath := p203PlanEditorApp(t)
	dialog := app.planDialog
	dialog.Overlay("", 80, 24)
	dialog.focus = planFocusReview
	dialog.selectedIdx = 2
	dialog.viewport.offset = min(3, dialog.viewport.maxOffset())
	dialog.feedbackEditor.SetValue("keep rollback notes")
	setTextareaRuneCursor(&dialog.feedbackEditor, 5)
	dialog.feedbackUndo = []textEditorSnapshot{{
		Text: "previous rollback notes", CursorOffset: 3,
	}}

	message := p203PlanEditorMessage(dialog)
	message.terminalReleased = true
	want := message.presentation
	dialog.focus = planFocusFeedback
	dialog.selectedIdx = 0
	dialog.viewport.offset = 0
	dialog.feedbackEditor.Reset()
	dialog.feedbackUndo = nil

	edited := "# Updated plan\n\n" + strings.Repeat("review line\n", 30)
	if err := os.WriteFile(planPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := app.applyPlanEditorResult(message)
	if restore == nil {
		t.Fatal("released external editor did not request terminal restoration")
	}
	dialog.Overlay("", 100, 28)
	if dialog.plan != edited ||
		dialog.ReviewedPlanDigest() != engine.PlanBytesDigest([]byte(edited)) ||
		dialog.focus != want.focus ||
		dialog.selectedIdx != want.selectedIdx ||
		dialog.viewport.offset != want.viewportOffset ||
		dialog.Feedback() != want.feedback.Text ||
		dialog.feedbackCursorOffset() != want.feedback.CursorOffset ||
		!reflect.DeepEqual(dialog.feedbackUndo, want.feedbackUndo) {
		t.Fatalf(
			"restored plan=%q digest=%q focus=%s selection=%d offset=%d feedback=%q cursor=%d undo=%#v",
			dialog.plan,
			dialog.ReviewedPlanDigest(),
			dialog.focus,
			dialog.selectedIdx,
			dialog.viewport.offset,
			dialog.Feedback(),
			dialog.feedbackCursorOffset(),
			dialog.feedbackUndo,
		)
	}
	if !dialog.IsVisible() || dialog.EditorActive() {
		t.Fatalf(
			"editor completion visible=%v active=%v",
			dialog.IsVisible(),
			dialog.EditorActive(),
		)
	}
}

func TestP203PlanEditorRejectsStaleIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*planEditorIdentity)
	}{
		{name: "thread", mutate: func(identity *planEditorIdentity) {
			identity.threadID = "other"
		}},
		{name: "request", mutate: func(identity *planEditorIdentity) {
			identity.requestID = "other"
		}},
		{name: "revision", mutate: func(identity *planEditorIdentity) {
			identity.planRevision++
		}},
		{name: "path", mutate: func(identity *planEditorIdentity) {
			identity.planPath += ".other"
		}},
		{name: "generation", mutate: func(identity *planEditorIdentity) {
			identity.generation++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, planPath := p203PlanEditorApp(t)
			before := app.planDialog.plan
			message := p203PlanEditorMessage(app.planDialog)
			test.mutate(&message.identity)
			if err := os.WriteFile(planPath, []byte("# stale replacement"), 0o600); err != nil {
				t.Fatal(err)
			}

			app.applyPlanEditorResult(message)
			notification := p203LatestNotificationMessage(t, app)
			if app.planDialog.plan != before ||
				!app.planDialog.EditorActive() ||
				!strings.Contains(notification, "stale") {
				t.Fatalf(
					"stale result plan=%q active=%v notification=%q",
					app.planDialog.plan,
					app.planDialog.EditorActive(),
					notification,
				)
			}
		})
	}
}

func TestP203ReplacedApprovalClearsFinishedEditorStatus(t *testing.T) {
	app, _ := p203PlanEditorApp(t)
	message := p203PlanEditorMessage(app.planDialog)
	message.terminalReleased = true
	app.externalEditorActive = true

	current, ok := app.threadAttention.get(app.threadAttention.activeID)
	if !ok {
		t.Fatal("active Plan attention missing")
	}
	replacement := *current.PlanApproval
	replacement.RequestID = "replacement-request"
	current.PlanApproval = &replacement
	app.planDialog.Show(
		current.ThreadID,
		current.SessionID,
		current.AgentID,
		current.PlanApproval,
		current.uiResponse,
	)

	restore := app.applyPlanEditorResult(message)
	notification := p203LatestNotificationMessage(t, app)
	if restore == nil ||
		app.externalEditorActive ||
		app.planDialog.EditorActive() ||
		!strings.Contains(notification, "stale") {
		t.Fatalf(
			"replacement restore=%v app active=%v dialog active=%v notification=%q",
			restore,
			app.externalEditorActive,
			app.planDialog.EditorActive(),
			notification,
		)
	}
}

func TestP203PlanEditorFailureKeepsApprovalOpen(t *testing.T) {
	app, _ := p203PlanEditorApp(t)
	before := app.planDialog.plan
	message := p203PlanEditorMessage(app.planDialog)
	message.terminalReleased = true
	message.err = errors.New("exit status 2")

	restore := app.applyPlanEditorResult(message)
	notification := p203LatestNotificationMessage(t, app)
	if restore == nil ||
		app.planDialog.plan != before ||
		!app.planDialog.IsVisible() ||
		app.planDialog.EditorActive() ||
		!strings.Contains(notification, "exit status 2") {
		t.Fatalf(
			"failure restore=%v plan=%q visible=%v active=%v notification=%q",
			restore,
			app.planDialog.plan,
			app.planDialog.IsVisible(),
			app.planDialog.EditorActive(),
			notification,
		)
	}
	if app.threadAttention.activeID == "" {
		t.Fatal("editor failure settled the Plan approval")
	}
}

func p203PlanEditorApp(t *testing.T) (*App, string) {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "计划.md")
	plan := "# Plan\n\n" + strings.Repeat("step\n", 30)
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	app := New(Config{Resumed: true, MouseEnabled: true})
	threadID := app.activeThreadViewID()
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "attention-p20-3", ThreadID: threadID, AgentID: "agent",
		Kind: threadAttentionPlan, Tool: "ExitPlanMode", SessionID: "session",
		Source: "callback", responseCh: make(chan PermissionResponse, 1),
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID:         "request-p20-3",
			PlanRevision:      7,
			PlanFileIdentity:  planPath,
			InitialPlanDigest: engine.PlanBytesDigest([]byte(plan)),
			ReturnMode:        permission.ModeDefault,
		},
	})
	if !app.planDialog.IsVisible() {
		t.Fatal("Plan dialog was not presented")
	}
	return app, planPath
}

func p203LatestNotificationMessage(t *testing.T, app *App) string {
	t.Helper()
	active := app.notifications.Active()
	if len(active) == 0 {
		t.Fatal("expected Plan editor notification")
	}
	return active[len(active)-1].Message
}

func p203PlanEditorMessage(dialog *PlanDialog) planEditorFinishedMsg {
	dialog.editorGeneration++
	dialog.activeEditor = dialog.editorGeneration
	dialog.editorActive = true
	return planEditorFinishedMsg{
		identity: planEditorIdentity{
			threadID:     dialog.ownerThreadID,
			requestID:    dialog.requestID,
			planRevision: dialog.planRevision,
			planPath:     dialog.planPath,
			generation:   dialog.activeEditor,
		},
		presentation: dialog.captureEditorPresentation(),
	}
}
