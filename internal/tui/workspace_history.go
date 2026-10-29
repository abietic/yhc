package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/tui/attachments"
)

type changeReviewTarget struct {
	engine                *engine.QueryEngine
	session, thread, view string
	generation            uint64
}

func (a *App) changeReviewTargetMatches() bool {
	target := a.changeReviewTarget
	if a.engine == nil || target.engine != a.engine || target.view != a.activeThreadViewID() {
		return false
	}
	session, thread, generation := a.engine.WorkspaceHistoryIdentity()
	return target.session == session && target.thread == thread && target.generation == generation
}

func (a *App) openChangeReview() {
	a.dismissWorkspaceRestoreUI()
	a.dismissComposerSuggestion()
	snapshot := a.engine.WorkspaceChanges()
	a.changeReviewTarget = changeReviewTarget{engine: a.engine, session: snapshot.SessionID, thread: snapshot.ThreadID, view: a.activeThreadViewID(), generation: snapshot.OwnerGeneration}
	a.changeReviewPanel.Show(snapshot, a.engine.WorkspaceChange)
	a.pushDialog(StateChangeReview)
}

func (a *App) refreshChangeReview() {
	if !a.hasDialog(StateChangeReview) {
		return
	}
	if !a.changeReviewTargetMatches() {
		a.dismissChangeReview()
		a.showNotification("History owner changed; reopen the view.", NotifyWarning)
		return
	}
	snapshot := a.engine.WorkspaceChanges()
	oldRestore, newRestore := a.changeReviewPanel.snapshot.Restore, snapshot.Restore
	restoreChanged := (oldRestore == nil) != (newRestore == nil)
	if oldRestore != nil && newRestore != nil {
		restoreChanged = *oldRestore != *newRestore
	}
	if restoreChanged || snapshot.Revision != a.changeReviewPanel.snapshot.Revision || snapshot.Warning != a.changeReviewPanel.snapshot.Warning {
		a.changeReviewPanel.Refresh(snapshot)
	}
}

func (a *App) dismissChangeReview() {
	if a == nil {
		return
	}
	a.dismissWorkspaceRestoreUI()
	if a.changeReviewPanel != nil {
		a.changeReviewPanel.Close()
	}
	a.changeReviewTarget = changeReviewTarget{}
	a.removeDialog(StateChangeReview)
}

func (a *App) handleChangeReviewKey(msg tea.KeyPressMsg) {
	if !a.changeReviewTargetMatches() {
		a.dismissChangeReview()
		a.showNotification("History owner changed; reopen the view.", NotifyWarning)
		return
	}
	if msg.Code == tea.KeyF5 {
		a.refreshChangeReview()
		return
	}
	action, selection := a.changeReviewPanel.HandleKey(msg)
	switch action {
	case ChangeReviewClose:
		a.dismissChangeReview()
	case ChangeReviewStage:
		record, ok := a.engine.WorkspaceChange(selection.RevisionID)
		if !ok || record.SessionID != a.changeReviewTarget.session || record.TurnID != selection.TurnID {
			a.changeReviewPanel.SetFeedback("The selected source is no longer available; refresh.")
			return
		}
		content, err := changeReviewContext(record, selection)
		if err != nil {
			a.changeReviewPanel.SetFeedback(err.Error())
			return
		}
		body := fmt.Sprintf("Captured change context\nSession: %s\nTurn: %s\nTool: %s (%s)\nCaptured: %s\n%s", changeReviewDisplay(record.SessionID), changeReviewDisplay(record.TurnID), changeReviewDisplay(record.ToolName), changeReviewDisplay(record.Outcome), record.Timestamp.UTC().Format("2006-01-02T15:04:05Z"), content)
		if reason := a.appendCapturedReviewContext(body, record.Path); reason != "" {
			a.changeReviewPanel.SetFeedback(reason)
			return
		}
		a.dismissChangeReview()
	}
}

// A captured context element preserves raw source bytes (including CRLF), while
// the existing paste element projection owns its inert preview and submission.
func (a *App) appendCapturedReviewContext(body, path string) string {
	if a.composerInputBlocked() || a.composerImageLoadPending != nil || a.externalEditorActive {
		return "Wait for the current draft operation to finish."
	}
	if a.inputMode != InputNormal && strings.TrimSpace(a.textarea.Value()) != "" {
		return "Use a normal message draft before adding review context."
	}
	if len(body) > attachments.MaxAttachmentBytes || a.composerPayloadBytes()+len(body) > maxComposerPayloadBytes {
		return "Captured context exceeds the draft attachment budget."
	}
	if len(a.composerElements) >= maxThreadComposerElements {
		return "The draft attachment limit is reached."
	}
	before := a.captureComposerUndoEntry()
	label := a.nextLargePastePlaceholder(utf8.RuneCountInString(body))
	prefix := ""
	if a.textarea.Value() != "" {
		prefix = "\n\n"
	}
	start := utf8.RuneCountInString(a.textarea.Value() + prefix)
	a.textarea.CursorEnd()
	a.textarea.InsertString(prefix + label)
	a.nextComposerElementID++
	a.composerElements = append(a.composerElements, threadComposerElement{ID: fmt.Sprintf("review-%d", a.nextComposerElementID), Kind: composerElementKindPaste, Label: label, Name: "Captured change: " + changeReviewDisplay(path), Value: body, Start: start, End: start + utf8.RuneCountInString(label)})
	a.inputMode = InputNormal
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.updateLayout()
	return ""
}

func (a *App) dispatchHistoryView(value string, cmdCtx *commands.CommandContext, palette *commandPaletteSubmission, preserveDraft bool) tea.Cmd {
	result, err := a.commandRegistry.Dispatch(context.Background(), commands.EntrypointTUI, cmdCtx, value)
	if err != nil {
		a.showNotification(err.Error(), NotifyWarning)
		return nil
	}
	if result == nil || (result.Action != commands.ActionWorkspaceChanges && result.Action != commands.ActionExecutionRecap) {
		reason := "History view is unavailable."
		if result != nil && result.Output != "" {
			reason = result.Output
		}
		a.showNotification(reason, NotifyWarning)
		return nil
	}
	if !preserveDraft {
		a.clearInputAfterSubmit("")
	}
	if result.Action == commands.ActionWorkspaceChanges {
		a.openChangeReview()
	} else {
		a.enterExpandContent(renderExecutionRecap(a.engine.ExecutionSummary()), StateChat)
	}
	a.commitCommandPaletteLocalSubmission(palette)
	return nil
}
