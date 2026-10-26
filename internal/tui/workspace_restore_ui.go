package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"
)

// workspaceRestoreUIState is an inert projection of one engine-owned restore
// preview. It deliberately contains no file-write authority: confirmation is
// always delegated back to the engine with the preview ID it issued.
type workspaceRestoreUIState struct {
	target     changeReviewTarget
	preview    engine.WorkspaceRestorePreview
	status     engine.WorkspaceRestoreStatus
	preparing  bool
	applying   bool
	selected   int
	lineOffset int
	feedback   string
	request    *workspaceRestoreRequest
}

type workspaceRestoreRequest struct {
	ui      *workspaceRestoreUIState
	target  changeReviewTarget
	engine  *engine.QueryEngine
	preview string
	prepare bool
	cancel  context.CancelFunc
}

type workspaceRestoreResultMsg struct {
	request *workspaceRestoreRequest
	preview engine.WorkspaceRestorePreview
	status  engine.WorkspaceRestoreStatus
	err     error
}

func (a *App) workspaceRestoreTargetMatches(target changeReviewTarget) bool {
	if a == nil || !a.changeReviewTargetMatches() {
		return false
	}
	return target.engine == a.changeReviewTarget.engine && target.session == a.changeReviewTarget.session && target.thread == a.changeReviewTarget.thread && target.view == a.changeReviewTarget.view && target.generation == a.changeReviewTarget.generation
}

func (a *App) workspaceRestoreSelection() (string, string, bool) {
	panel := a.changeReviewPanel
	if panel == nil {
		return "", "", false
	}
	if panel.preview != nil {
		return panel.preview.TurnID, panel.preview.ID, true
	}
	if panel.selected < 0 || panel.selected >= len(panel.rows) {
		return "", "", false
	}
	row := panel.rows[panel.selected]
	if row.kind == changeReviewTurnRow {
		return row.turn.TurnID, "", true
	}
	if row.kind != changeReviewFileRow {
		return "", "", false
	}
	return row.header.TurnID, row.header.ID, true
}

func (a *App) prepareWorkspaceRestore(turnID, revisionID, direction string) tea.Cmd {
	if a == nil || !a.changeReviewTargetMatches() {
		return nil
	}
	if a.workspaceRestoreUI != nil && (a.workspaceRestoreUI.preparing || a.workspaceRestoreUI.applying) {
		a.workspaceRestoreUI.feedback = "A restore operation is already in progress."
		return nil
	}
	ui := &workspaceRestoreUIState{target: a.changeReviewTarget, preparing: true}
	a.workspaceRestoreUI = ui
	request := &workspaceRestoreRequest{ui: ui, target: ui.target, engine: a.engine, prepare: true}
	ui.request = request
	return func() tea.Msg {
		preview, err := request.engine.PrepareWorkspaceRestore(turnID, revisionID, direction)
		return workspaceRestoreResultMsg{request: request, preview: preview, err: err}
	}
}

func (a *App) confirmWorkspaceRestore(ui *workspaceRestoreUIState) tea.Cmd {
	if a == nil || ui == nil || ui.preparing || ui.applying || ui.preview.ID == "" || !a.workspaceRestoreTargetMatches(ui.target) {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &workspaceRestoreRequest{ui: ui, target: ui.target, engine: a.engine, preview: ui.preview.ID, cancel: cancel}
	ui.applying, ui.feedback, ui.request = true, "Requesting a fresh allow-once permission.", request
	return func() tea.Msg {
		status, err := request.engine.ConfirmWorkspaceRestore(ctx, request.preview, true)
		return workspaceRestoreResultMsg{request: request, status: status, err: err}
	}
}

// handleWorkspaceRestoreKey owns only the restore projection. App integration
// routes it while StateChangeReview is frontmost.
func (a *App) handleWorkspaceRestoreKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if a == nil || !a.hasDialog(StateChangeReview) {
		return false, nil
	}
	ui := a.workspaceRestoreUI
	key := msg.String()
	if ui == nil {
		switch key {
		case "t", "b":
			status := a.changeReviewPanel.snapshot.Restore
			if status == nil || (status.State != "applying" && status.State != "partial") {
				a.changeReviewPanel.SetFeedback("No interrupted restore to recover.")
				return true, nil
			}
			direction := "restore"
			if key == "b" || status.Direction == "rollback" {
				direction = "rollback"
			}
			return true, a.prepareWorkspaceRestore("", "", direction)
		case "r":
			turnID, revisionID, ok := a.workspaceRestoreSelection()
			if !ok {
				a.changeReviewPanel.SetFeedback("Select a completed turn or captured file before preparing a restore.")
				return true, nil
			}
			return true, a.prepareWorkspaceRestore(turnID, revisionID, "")
		default:
			return false, nil
		}
	}
	if !a.workspaceRestoreTargetMatches(ui.target) {
		a.dismissWorkspaceRestoreUI()
		a.showNotification("History owner changed; reopen the view.", NotifyWarning)
		return true, nil
	}
	if ui.applying {
		switch key {
		case "esc", "ctrl+c":
			if ui.request != nil && ui.request.cancel != nil {
				ui.request.cancel()
			}
			a.engine.CancelWorkspaceRestore()
			ui.feedback = "Cancellation requested; waiting for the restore boundary."
		}
		return true, nil
	}
	if ui.preparing {
		if key == "esc" || key == "ctrl+c" {
			a.dismissWorkspaceRestoreUI()
		}
		return true, nil
	}
	switch key {
	case "esc":
		a.dismissWorkspaceRestoreUI()
		return true, nil
	case "enter":
		return true, a.confirmWorkspaceRestore(ui)
	case "tab":
		if len(ui.preview.Files) > 0 {
			ui.selected = (ui.selected + 1) % len(ui.preview.Files)
			ui.lineOffset = 0
		}
	case "up", "k":
		ui.lineOffset = max(0, ui.lineOffset-1)
	case "down", "j":
		ui.lineOffset++
	case "pgup":
		ui.lineOffset = max(0, ui.lineOffset-5)
	case "pgdown":
		ui.lineOffset += 5
	case "home":
		ui.lineOffset = 0
	case "end":
		ui.lineOffset = 1 << 30
	case "t", "b":
		if ui.status.State != "applying" && ui.status.State != "partial" {
			return true, nil
		}
		direction := "restore"
		if key == "b" || ui.status.Direction == "rollback" {
			direction = "rollback"
		}
		return true, a.prepareWorkspaceRestore("", "", direction)
	}
	return true, nil
}

func (a *App) handleWorkspaceRestoreMessage(msg tea.Msg) (bool, tea.Cmd) {
	result, ok := msg.(workspaceRestoreResultMsg)
	if !ok {
		return false, nil
	}
	request := result.request
	if request == nil || a == nil || a.workspaceRestoreUI != request.ui || request.ui.request != request || !a.workspaceRestoreTargetMatches(request.target) || !a.hasDialog(StateChangeReview) {
		if request != nil && request.cancel != nil {
			request.cancel()
		}
		if request != nil && request.prepare && result.preview.ID != "" && request.engine != nil {
			request.engine.DiscardWorkspaceRestorePreview(result.preview.ID)
		}
		return true, nil
	}
	ui := request.ui
	if request.cancel != nil {
		request.cancel()
	}
	ui.request = nil
	if request.prepare {
		ui.preparing = false
		if result.err != nil {
			ui.feedback = "Restore preview unavailable: " + changeReviewDisplay(result.err.Error())
			return true, nil
		}
		if result.preview.ID == "" || result.preview.SessionID != request.target.session || result.preview.ThreadID != request.target.thread || result.preview.OwnerGeneration != request.target.generation {
			ui.feedback = "Restore preview owner changed; prepare again."
			return true, nil
		}
		ui.preview, ui.selected, ui.lineOffset = result.preview, 0, 0
		ui.feedback = "Review the captured transition, then press Enter to request a fresh allow-once permission."
		return true, nil
	}
	ui.applying = false
	ui.preview = engine.WorkspaceRestorePreview{}
	ui.status = result.status
	a.refreshChangeReview()
	if result.err != nil {
		ui.feedback = "Restore result: " + changeReviewDisplay(result.err.Error())
		return true, nil
	}
	ui.preview = engine.WorkspaceRestorePreview{}
	ui.feedback = fmt.Sprintf("%s %d/%d paths finished.", changeReviewDisplay(result.status.State), result.status.Finished, result.status.Paths)
	a.refreshChangeReview()
	return true, nil
}

// dismissWorkspaceRestoreUI revokes only the ephemeral preview or pending
// request. It never changes the transcript, draft, or a completed journal.
func (a *App) dismissWorkspaceRestoreUI() {
	if a == nil || a.workspaceRestoreUI == nil {
		return
	}
	ui := a.workspaceRestoreUI
	if ui.request != nil && ui.request.cancel != nil {
		ui.request.cancel()
	}
	if ui.applying && ui.target.engine != nil {
		ui.target.engine.CancelWorkspaceRestore()
	}
	if !ui.applying && ui.preview.ID != "" && ui.target.engine != nil {
		ui.target.engine.DiscardWorkspaceRestorePreview(ui.preview.ID)
	}
	a.workspaceRestoreUI = nil
}

func (a *App) renderWorkspaceRestoreOverlay(base string, width, height int) string {
	if a == nil || a.workspaceRestoreUI == nil {
		if a != nil && a.changeReviewPanel != nil {
			return a.changeReviewPanel.Overlay(base, width, height)
		}
		return base
	}
	ui := a.workspaceRestoreUI
	lines := a.workspaceRestoreLines(ui, width, height)
	view, _ := modalCenteredOverlay(a.renderEnvironment.normalized().profile, base, strings.Join(lines, "\n"), width, height)
	return view
}

func (a *App) workspaceRestoreLines(ui *workspaceRestoreUIState, width, height int) []string {
	profile := a.renderEnvironment.normalized().profile
	if width <= 0 || height <= 0 {
		return nil
	}
	lines := []string{a.styles.DialogTitle.Render("Workspace restore")}
	if ui.preparing {
		lines = append(lines, a.styles.DialogHelp.Render("Preparing a read-only restore preview…"))
	} else if ui.applying {
		lines = append(lines, a.styles.DialogHelp.Render("Restore request in progress; Esc cancels."))
	} else if ui.preview.ID != "" {
		lines = append(lines, a.styles.DialogHelp.Render("Captured files only; conversation and draft are unchanged."))
		lines = append(lines, a.workspaceRestorePreviewLines(ui, max(1, width-2), max(1, height-4))...)
	} else if ui.status.State != "" {
		lines = append(lines, fmt.Sprintf("%s: %d/%d paths finished", changeReviewDisplay(ui.status.State), ui.status.Finished, ui.status.Paths))
	}
	if ui.feedback != "" {
		lines = append(lines, a.styles.DialogHelp.Render(changeReviewDisplay(ui.feedback)))
	}
	footer := "Esc return"
	if ui.preparing {
		footer = "Esc cancel preview"
	} else if ui.applying {
		footer = "Esc/Ctrl+C cancel"
	} else if ui.preview.ID != "" {
		footer = "Tab file ↑↓ scroll Enter confirm Esc"
	} else if ui.status.State == "applying" || ui.status.State == "partial" {
		footer = "t restore  b rollback  Esc return"
	}
	lines = append(lines, a.styles.DialogHelp.Render(footer))
	if len(lines) > height {
		lines = lines[:height]
		lines[len(lines)-1] = a.styles.DialogHelp.Render(footer)
	}
	for i := range lines {
		lines[i] = modalEllipsize(profile, lines[i], width, 0, "…")
	}
	return lines
}

func (a *App) workspaceRestorePreviewLines(ui *workspaceRestoreUIState, width, capacity int) []string {
	if len(ui.preview.Files) == 0 {
		return []string{"No captured files are available."}
	}
	ui.selected = min(max(0, ui.selected), len(ui.preview.Files)-1)
	file := ui.preview.Files[ui.selected]
	record := filehistory.RevisionRecord{Path: file.Path, Before: file.Current, After: file.Target, Outcome: "succeeded"}
	body := []string{
		fmt.Sprintf("%d/%d %s [%s]", ui.selected+1, len(ui.preview.Files), changeReviewSafeLabel(file.Path), changeReviewDisplay(file.Status)),
		changeReviewLiteralState("current", file.Path, file.Current),
		changeReviewLiteralState("target", file.Path, file.Target),
	}
	hunks, err := changeReviewHunks(record)
	if err != nil {
		body = append(body, "Diff unavailable: "+changeReviewDisplay(err.Error()))
	} else if len(hunks) == 0 {
		body = append(body, "No textual diff; metadata above is the captured transition.")
	} else {
		diff := changeReviewPreviewText(strings.Join(hunks, "\n"))
		body = append(body, a.renderEnvironment.normalized().profile.wrap(diff, max(1, width), true)...)
	}
	available := max(1, capacity)
	ui.lineOffset = min(ui.lineOffset, max(0, len(body)-available))
	return body[ui.lineOffset:min(len(body), ui.lineOffset+available)]
}
