package tui

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/attachments"
)

const composerElementKindTerminalOutput = "terminal_output"

var (
	errSelectedOutputTooLarge    = errors.New("selected output exceeds the 5 MiB context limit")
	errSelectedOutputUnavailable = errors.New("select current visible output again; the selection is empty or changed")
)

func (a *App) selectedOutputContext(maxBytes int) (string, string, error) {
	if a.selection == nil || a.selection.IsDragging() {
		return "", "", errSelectedOutputUnavailable
	}
	if a.state == StateExpand {
		if !a.expandConversation {
			return "", "", errors.New("attach output from the current transcript, not a Session preview")
		}
		text, err := a.selection.extractExpandText(a.expandLines, a.renderEnvironment.normalized().profile, maxBytes)
		return text, "expanded_transcript_selection", err
	}
	if a.state != StateChat || !a.selection.HasSelection() || !a.selection.ensureCompatible(a.chat) {
		return "", "", errSelectedOutputUnavailable
	}
	start, end := a.selection.getItemBounds()
	text, err := a.chat.renderItemRange(start.itemIdx, start.lineInItem, start.col, end.itemIdx, end.lineInItem, end.col, maxBytes)
	return text, "chat_selection", err
}

// attachSelectedOutputContext captures rendered selection data as literal
// context. It neither replays a command nor reads raw tool/transcript storage.
func (a *App) attachSelectedOutputContext() {
	if a.composerInputBlocked() || a.composerImageLoadPending != nil || a.externalEditorActive || a.historySearch.Active || a.inputMode != InputNormal || a.activeThreadViewMode() != engine.ThreadModeLiveAttach {
		a.showNotification("Output context requires an editable ordinary draft", NotifyWarning)
		return
	}
	if _, active := a.activeDialogState(); active {
		return
	}
	if len(a.composerElements) >= maxThreadComposerElements {
		a.showNotification("Composer attachment limit reached (32)", NotifyError)
		return
	}
	text, source, err := a.selectedOutputContext(attachments.MaxAttachmentBytes)
	if err == nil && (strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0) {
		err = errSelectedOutputUnavailable
	}
	if err != nil {
		a.showNotification(err.Error(), NotifyWarning)
		return
	}
	if a.composerPayloadBytes()+len(text) > maxComposerPayloadBytes {
		a.showNotification("Composer attachments exceed the 10 MiB draft limit", NotifyError)
		return
	}
	before := a.captureComposerUndoEntry()
	anchor := before.CursorOffset
	for _, element := range a.composerElements {
		if anchor > element.Start && anchor < element.End {
			a.showNotification("Move the cursor outside the existing attachment reference", NotifyWarning)
			return
		}
	}
	a.nextComposerElementID++
	label := fmt.Sprintf("[Output #%d]", a.nextComposerElementID)
	a.textarea.InsertString(label + " ")
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.composerElements = append(a.composerElements, threadComposerElement{
		ID: fmt.Sprintf("output-%d", a.nextComposerElementID), Kind: composerElementKindTerminalOutput,
		Label: label, Name: "Selected terminal output", Value: source, MIMEType: "text/plain", Data: text,
		Start: anchor, End: anchor + utf8.RuneCountInString(label),
	})
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.dismissMentionHints()
	a.syncInputModeFromText()
	a.selection.Clear()
	a.selection.clearExpandSelection()
	if a.state == StateExpand {
		a.leaveExpandView()
	}
	a.chat.ResetFollow()
	a.updateLayout()
	a.showToast("Selected output attached to draft")
}
