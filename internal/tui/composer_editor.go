package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	charmeditor "github.com/charmbracelet/x/editor"

	"github.com/abietic/yhc/internal/identity"
)

type composerEditorFinishedMsg struct {
	threadID         string
	content          string
	terminalReleased bool
	err              error
}

func (a *App) openComposerExternalEditor() tea.Cmd {
	if a == nil {
		return nil
	}
	if a.inputMode != InputNormal {
		a.showNotification("External editor is available for ordinary prompts", NotifyWarning)
		return nil
	}
	if a.externalEditorActive {
		a.showNotification("External editor is already active", NotifyWarning)
		return nil
	}
	seed := expandComposerElements(a.textarea.Value(), a.composerElements)
	line := a.textarea.LineInfo()
	command, path, err := prepareComposerEditor(seed, a.textarea.Line()+1, line.StartColumn+line.ColumnOffset+1)
	if err != nil {
		a.showNotification("Cannot open external editor: "+err.Error(), NotifyError)
		return nil
	}
	a.externalEditorActive = true
	a.showToast("External editor active")
	threadID := a.activeThreadViewID()
	return tea.ExecProcess(command, func(processErr error) tea.Msg {
		return readComposerEditorResult(threadID, path, processErr)
	})
}

func prepareComposerEditor(seed string, line, column int) (*exec.Cmd, string, error) {
	temp, err := os.CreateTemp("", identity.CommandName+"-prompt-*.md")
	if err != nil {
		return nil, "", err
	}
	path := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(path)
	}
	if _, err := temp.WriteString(seed); err != nil {
		cleanup()
		return nil, "", err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return nil, "", err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return nil, "", err
	}
	command, err := externalEditorCommand(
		path,
		charmeditor.AtPosition(line, column),
	)
	if err != nil {
		_ = os.Remove(path)
		return nil, "", err
	}
	return command, path, nil
}

func readComposerEditorResult(threadID, path string, processErr error) composerEditorFinishedMsg {
	defer os.Remove(path) //nolint:errcheck
	if processErr != nil {
		return composerEditorFinishedMsg{
			threadID: threadID, terminalReleased: true, err: processErr,
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return composerEditorFinishedMsg{
			threadID: threadID, terminalReleased: true, err: err,
		}
	}
	return composerEditorFinishedMsg{
		threadID:         threadID,
		content:          strings.TrimRightFunc(string(content), unicode.IsSpace),
		terminalReleased: true,
	}
}

func (a *App) applyComposerEditorResult(message composerEditorFinishedMsg) {
	a.externalEditorActive = false
	if message.err != nil {
		a.showNotification(fmt.Sprintf("External editor failed: %v", message.err), NotifyError)
		return
	}
	if message.threadID != a.activeThreadViewID() {
		a.showNotification("External editor target thread changed; draft was not replaced", NotifyWarning)
		return
	}
	before := a.captureComposerUndoEntry()
	a.textarea.SetValue(message.content)
	a.reconcileComposerElements(before.Text, message.content)
	a.markComposerChanged()
	a.textarea.CursorEnd()
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
	a.dismissMentionHints()
	a.updateLayout()
}
