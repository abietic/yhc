package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"
)

func TestWorkspaceRestoreOverlayPreservesFooterAtResponsiveWidths(t *testing.T) {
	app := New(Config{})
	app.workspaceRestoreUI = &workspaceRestoreUIState{preview: engine.WorkspaceRestorePreview{ID: "preview", Files: []engine.WorkspaceRestoreFile{{Path: "note.txt", Status: "ready", Current: filehistory.RevisionFileState{Captured: true, Exists: true, Mode: 0o600, Data: []byte("old\n")}, Target: filehistory.RevisionFileState{Captured: true, Exists: true, Mode: 0o644, Data: []byte("new\n")}}}}}
	for _, width := range []int{40, 80, 120, 180} {
		rendered := app.renderWorkspaceRestoreOverlay("", width, 20)
		if !strings.Contains(rendered, "Enter confirm") || !strings.Contains(rendered, "exists=true") {
			t.Fatalf("width %d omitted actionable footer or concrete state: %q", width, rendered)
		}
	}
}

func TestWorkspaceRestoreEnterStartsExactConfirmedEngineActionOnly(t *testing.T) {
	app := newG27EngineApp(t)
	app.textarea.SetValue("keep draft")
	app.openChangeReview()
	target := app.changeReviewTarget
	app.workspaceRestoreUI = &workspaceRestoreUIState{target: target, preview: engine.WorkspaceRestorePreview{ID: "preview"}}
	handled, cmd := app.handleWorkspaceRestoreKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd == nil || !app.workspaceRestoreUI.applying || app.textarea.Value() != "keep draft" {
		t.Fatalf("Enter did not start the exact confirmed action: handled=%v cmd=%v applying=%v draft=%q", handled, cmd != nil, app.workspaceRestoreUI.applying, app.textarea.Value())
	}
}

func TestWorkspaceRestorePrepareDoesNotAutoConfirmOrAlterDraft(t *testing.T) {
	app := newCapturedHistoryApp(t)
	app.textarea.SetValue("keep draft")
	app.openChangeReview()
	app.handleChangeReviewKey(tea.KeyPressMsg{Code: tea.KeyDown})
	handled, cmd := app.handleWorkspaceRestoreKey(tea.KeyPressMsg{Code: 'r'})
	if !handled || cmd == nil || app.workspaceRestoreUI == nil || !app.workspaceRestoreUI.preparing {
		t.Fatalf("r did not begin an inert preview request: handled=%v cmd=%v ui=%#v", handled, cmd != nil, app.workspaceRestoreUI)
	}
	if !app.handleWorkspaceRestoreMessage(cmd()) || app.workspaceRestoreUI.applying || app.textarea.Value() != "keep draft" || len(app.engine.GetMessages()) != 0 {
		t.Fatalf("preparing a preview auto-confirmed or altered conversation/draft: applying=%v draft=%q messages=%d", app.workspaceRestoreUI.applying, app.textarea.Value(), len(app.engine.GetMessages()))
	}
}

func TestWorkspaceRestoreStaleResultCannotReplaceNewProjection(t *testing.T) {
	app := New(Config{})
	oldTarget := changeReviewTarget{engine: app.engine, view: "old"}
	ui := &workspaceRestoreUIState{target: oldTarget, preparing: true}
	request := &workspaceRestoreRequest{ui: ui, target: oldTarget, engine: app.engine, prepare: true}
	ui.request = request
	app.workspaceRestoreUI = ui
	app.changeReviewTarget = changeReviewTarget{engine: app.engine, view: "new"}
	handled := app.handleWorkspaceRestoreMessage(workspaceRestoreResultMsg{request: request, preview: engine.WorkspaceRestorePreview{ID: "stale"}})
	if !handled || app.workspaceRestoreUI != ui || app.workspaceRestoreUI.preview.ID != "" {
		t.Fatalf("stale result replaced current projection: handled=%v ui=%#v", handled, app.workspaceRestoreUI)
	}
}

func TestWorkspaceRestoreReviewGolden(t *testing.T) {
	app := New(Config{})
	ui := &workspaceRestoreUIState{preview: engine.WorkspaceRestorePreview{ID: "preview", Direction: "restore", Files: []engine.WorkspaceRestoreFile{{Path: "note.txt", Status: "pending", Current: filehistory.RevisionFileState{Captured: true, Exists: true, Mode: 0o640, Data: []byte("old\r\nlast")}, Target: filehistory.RevisionFileState{Captured: true, Exists: true, Mode: 0o640, Data: []byte("new\r\nLAST")}}}}}
	var out strings.Builder
	for _, width := range []int{40, 80, 120, 180} {
		lines := app.workspaceRestoreLines(ui, width, 20)
		text := stripANSIForTest(strings.Join(lines, "\n"))
		for _, want := range []string{"Enter confirm", "Esc", "+new", "-old", "exists=true"} {
			if !strings.Contains(text, want) {
				t.Fatalf("width %d missing %q: %s", width, want, text)
			}
		}
		fmt.Fprintf(&out, "== preview %d ==\n%s\n", width, changeReviewGoldenNormalize(text))
	}
	path := "testdata/workspace_restore.golden"
	actual := strings.TrimSpace(out.String()) + "\n"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != actual {
		t.Fatalf("restore golden mismatch:\n%s", actual)
	}
}
