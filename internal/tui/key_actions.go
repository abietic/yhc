package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/abietic/yhc/internal/tui/vim"
)

func (a *App) resolveKeyAction(msg tea.KeyPressMsg, contexts ...keybindings.Context) (bool, tea.Cmd) {
	if a.keybindResolver == nil {
		return false, nil
	}
	resolution := a.keybindResolver.ResolveEvent(msg, contexts...)
	switch resolution.Kind {
	case keybindings.ResolutionChordStarted:
		a.showToast("Key chord: " + resolution.Pending + " ...")
		return true, nil
	case keybindings.ResolutionChordCancelled:
		a.showToast("Key chord canceled")
		return true, nil
	case keybindings.ResolutionMatch:
		return a.handleKeyAction(resolution.Action, msg)
	default:
		return false, nil
	}
}

func (a *App) resolveEditorKeyAction(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	contexts := make([]keybindings.Context, 0, 3)
	if a.autocompleteOwnsKey(msg) {
		contexts = append(contexts, keybindings.ContextAutocomplete)
	}
	contexts = append(contexts, keybindings.ContextChat)
	contexts = append(contexts, keybindings.ContextScroll)
	return a.resolveKeyAction(msg, contexts...)
}

func (a *App) handleKeyAction(action keybindings.Action, msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch action {
	case keybindings.ActionAppInterrupt:
		return true, a.handleInterrupt()
	case keybindings.ActionAppExit:
		a.quitting = true
		return true, nil
	case keybindings.ActionAppRedraw:
		return true, tea.ClearScreen
	case keybindings.ActionAppToggleTodos:
		if a.state == StateTaskPanel {
			a.state = StateChat
		} else {
			a.closeTransientBaseState()
			return true, a.enterTaskPanel()
		}
		return true, nil
	case keybindings.ActionAppToggleTranscript:
		if a.state == StateExpand {
			a.state = StateChat
		} else {
			a.closeTransientBaseState()
			a.enterExpandView()
		}
		return true, nil
	case keybindings.ActionAppGlobalSearch:
		if a.state == StateExpand {
			a.expandSearch.Show()
			a.expandSearch.UpdateMatches(a.expandLines)
		} else if a.state == StateSearch {
			a.search.Close()
			a.state = StateChat
		} else {
			a.closeTransientBaseState()
			a.openSearch()
		}
		return true, nil
	case keybindings.ActionAppQuickOpen:
		a.openCommandPalette()
		return true, nil
	case keybindings.ActionChatCancel:
		a.cancelEditorInput()
		return true, nil
	case keybindings.ActionChatCycleMode:
		if !a.running {
			a.togglePlanMode()
		}
		return true, nil
	case keybindings.ActionChatModelPicker:
		a.openModelPicker()
		return true, nil
	case keybindings.ActionChatSubmit:
		return true, a.sendMessage()
	case keybindings.ActionChatNewline:
		before := a.captureComposerUndoEntry()
		a.textarea.InsertRune('\n')
		a.reconcileComposerElements(before.Text, a.textarea.Value())
		a.markComposerChanged()
		a.recordComposerUndo(before)
		a.syncInputModeFromText()
		return true, nil
	case keybindings.ActionChatImagePaste:
		return true, a.pasteClipboardImage()
	case keybindings.ActionChatExternalEditor:
		return true, a.openComposerExternalEditor()
	case keybindings.ActionChatUndo:
		a.undoComposerEdit()
		return true, nil
	case keybindings.ActionHistorySearch:
		a.startHistorySearch()
		return true, nil
	case keybindings.ActionHistoryPrevious:
		return true, a.handleHistoryAction(msg, -1)
	case keybindings.ActionHistoryNext:
		return true, a.handleHistoryAction(msg, 1)
	case keybindings.ActionHistorySearchNext:
		a.advanceHistorySearch()
		return true, nil
	case keybindings.ActionHistorySearchAccept:
		return true, a.acceptHistorySearch(false)
	case keybindings.ActionHistorySearchCancel:
		a.cancelHistorySearch()
		return true, nil
	case keybindings.ActionHistorySearchExecute:
		return true, a.acceptHistorySearch(true)
	case keybindings.ActionChatRewriteMessage:
		a.openMessageSelector()
		return true, nil
	case keybindings.ActionChatPreviousAgent:
		return true, a.navigateAgentThread(-1)
	case keybindings.ActionChatNextAgent:
		return true, a.navigateAgentThread(1)
	case keybindings.ActionAutocompletePrev:
		a.moveAutocomplete(-1)
		return true, nil
	case keybindings.ActionAutocompleteNext:
		a.moveAutocomplete(1)
		return true, nil
	case keybindings.ActionAutocompleteAccept:
		return true, a.acceptAutocomplete(msg)
	case keybindings.ActionAutocompleteDismiss:
		if len(a.mentionHints) > 0 {
			a.dismissMentionHints()
		} else {
			a.cancelEditorInput()
		}
		return true, nil
	case keybindings.ActionTaskBackground:
		return true, tea.Batch(a.enterBackgroundTasks(), bgTaskTickCmd())
	case keybindings.ActionScrollPageUp:
		a.chat.ScrollUp(a.layout.chatHeight)
		return true, a.loadOlderActiveAgentTranscript()
	case keybindings.ActionScrollPageDown:
		a.chat.ScrollDown(a.layout.chatHeight)
		return true, nil
	case keybindings.ActionScrollHalfUp:
		a.chat.ScrollUp(max(1, a.layout.chatHeight/2))
		return true, a.loadOlderActiveAgentTranscript()
	case keybindings.ActionScrollHalfDown:
		a.chat.ScrollDown(max(1, a.layout.chatHeight/2))
		return true, nil
	case keybindings.ActionScrollLineUp:
		a.chat.ScrollUp(1)
		return true, a.loadOlderActiveAgentTranscript()
	case keybindings.ActionScrollLineDown:
		a.chat.ScrollDown(1)
		return true, nil
	case keybindings.ActionScrollTop:
		a.chat.ScrollToTop()
		return true, a.loadOlderActiveAgentTranscript()
	case keybindings.ActionScrollBottom:
		a.chat.ResetFollow()
		return true, nil
	case keybindings.ActionSelectionCopy:
		if !a.selection.HasSelection() {
			return false, nil
		}
		selected := a.selection.ExtractTextFromChat(a.chat)
		if selected == "" {
			return true, nil
		}
		cmd := a.requestClipboardCopy(ClipboardCallerKeyboardSelection, selected)
		if cmd == nil {
			return true, nil
		}
		a.selection.Clear()
		return true, cmd
	}
	return false, nil
}

func (a *App) closeTransientBaseState() {
	switch a.state {
	case StateSearch:
		a.search.Close()
	case StateMessageSelect:
		a.msgSelector.Close()
	case StateExpand:
		a.expandSearch.Close()
	}
	if !isDialogState(a.state) {
		a.state = StateChat
	}
}

func (a *App) handleInterrupt() tea.Cmd {
	if handled, continueInterrupt := a.closeActiveDialog(); handled && !continueInterrupt {
		return nil
	}
	if a.cancelComposerAdmission() {
		return nil
	}
	if a.running {
		if a.cancelFn == nil {
			a.showToast("Cancellation in progress")
			return nil
		}
		if a.engine != nil {
			if err := a.engine.RequestStop(engine.RuntimeStopImmediate, "tui_ctrl_c"); err != nil {
				a.showNotification("Cancellation request was not persisted: "+err.Error(), NotifyError)
			}
		}
		a.cancelFn()
		a.cancelFn = nil
		a.chat.FinishAssistant()
		a.appendInterruptionOnce()
		a.hookStatus = "Cancelling..."
		return nil
	}
	if time.Since(a.lastCtrlC) < 500*time.Millisecond {
		a.quitting = true
		return nil
	}
	a.lastCtrlC = time.Now()
	a.showToast("Press Ctrl+C again to exit")
	return nil
}

func (a *App) appendInterruptionOnce() {
	if a == nil || a.chat == nil {
		return
	}
	items := a.chat.Items()
	if len(items) > 0 {
		if _, ok := items[len(items)-1].(*InterruptionMessage); ok {
			return
		}
	}
	a.chat.AppendInterruption()
}

func (a *App) cancelEditorInput() {
	if a.inputMode == InputCommand || a.inputMode == InputShell {
		before := a.captureComposerUndoEntry()
		a.textarea.Reset()
		a.composerElements = nil
		a.markComposerChanged()
		a.setEditorPrompt()
		a.inputMode = InputNormal
		a.commandHints = nil
		a.commandHintIdx = -1
		a.fileHints = nil
		a.fileHintIdx = -1
		a.dismissMentionHints()
		a.recordComposerUndo(before)
		return
	}
	if a.selection.HasSelection() {
		a.selection.Clear()
		return
	}
	if a.textarea.Value() != "" {
		before := a.captureComposerUndoEntry()
		a.textarea.Reset()
		a.composerElements = nil
		a.markComposerChanged()
		a.dismissMentionHints()
		a.recordComposerUndo(before)
	}
}

func (a *App) handleHistoryAction(msg tea.KeyPressMsg, direction int) tea.Cmd {
	if direction < 0 {
		if msg.Code != tea.KeyUp {
			a.navigateHistory(-1)
			return nil
		}
		line := a.textarea.LineInfo()
		atTop := a.textarea.Line() == 0 && line.RowOffset == 0
		if atTop || a.historyIdx < len(a.history) {
			a.navigateHistory(-1)
			return nil
		}
		var cmd tea.Cmd
		a.textarea, cmd = a.textarea.Update(msg)
		return cmd
	}

	if msg.Code != tea.KeyDown {
		a.navigateHistory(1)
		return nil
	}
	if a.historyIdx < len(a.history) {
		a.navigateHistory(1)
		return nil
	}
	line := a.textarea.LineInfo()
	atBottom := a.textarea.Line() == a.textarea.LineCount()-1 && line.RowOffset >= line.Height-1
	if atBottom {
		return nil
	}
	var cmd tea.Cmd
	a.textarea, cmd = a.textarea.Update(msg)
	return cmd
}

func (a *App) moveAutocomplete(direction int) {
	if len(a.mentionHints) > 0 {
		if direction < 0 {
			if a.mentionHintIdx <= 0 {
				a.mentionHintIdx = len(a.mentionHints) - 1
			} else {
				a.mentionHintIdx--
			}
		} else if a.mentionHintIdx >= len(a.mentionHints)-1 {
			a.mentionHintIdx = 0
		} else {
			a.mentionHintIdx++
		}
		return
	}
	if len(a.commandHints) > 0 {
		if direction < 0 {
			if a.commandHintIdx <= 0 {
				a.commandHintIdx = len(a.commandHints) - 1
			} else {
				a.commandHintIdx--
			}
		} else if a.commandHintIdx >= len(a.commandHints)-1 {
			a.commandHintIdx = 0
		} else {
			a.commandHintIdx++
		}
		return
	}
	if len(a.fileHints) > 0 {
		if direction < 0 {
			if a.fileHintIdx <= 0 {
				a.fileHintIdx = len(a.fileHints) - 1
			} else {
				a.fileHintIdx--
			}
		} else if a.fileHintIdx >= len(a.fileHints)-1 {
			a.fileHintIdx = 0
		} else {
			a.fileHintIdx++
		}
	}
}

func (a *App) acceptAutocomplete(msg tea.KeyPressMsg) tea.Cmd {
	if a.mentionHintIdx >= 0 && a.mentionHintIdx < len(a.mentionHints) {
		return a.acceptMentionHint()
	}
	if a.commandHintIdx >= 0 && a.commandHintIdx < len(a.commandHints) {
		a.acceptCommandHint()
		return nil
	}
	if a.fileHintIdx >= 0 && a.fileHintIdx < len(a.fileHints) {
		a.acceptFileHint()
		return nil
	}
	if msg.Code == tea.KeyTab && len(a.fileHints) > 0 {
		a.fileHintIdx = 0
		return nil
	}
	if msg.Code == tea.KeyEnter || msg.Code == tea.KeyReturn {
		return a.sendMessage()
	}
	return nil
}

func (a *App) autocompleteOwnsKey(msg tea.KeyPressMsg) bool {
	if a.suppressingHistoryHints() {
		return false
	}
	mentionActive := len(a.mentionHints) > 0
	commandActive := a.inputMode == InputCommand && (len(a.commandHints) > 0 || len(a.fileHints) > 0)
	if !mentionActive && !commandActive {
		return false
	}
	switch msg.Code {
	case tea.KeyUp, tea.KeyDown, tea.KeyTab, tea.KeyEnter, tea.KeyEscape:
		return true
	default:
		return false
	}
}

func (a *App) handleVimEditorKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if !a.vimModel.IsEnabled() || !vimOwnsKey(a.vimModel.GetMode(), msg) {
		return false, nil
	}
	if a.vimModel.Value() != a.textarea.Value() {
		a.vimModel.SetValue(a.textarea.Value())
		a.vimModel.SetCursor(utf8.RuneCountInString(a.textarea.Value()))
	}
	before := a.captureComposerUndoEntry()
	consumed, cmd := a.vimModel.Update(msg)
	if !consumed {
		return false, cmd
	}
	a.syncTextareaFromVim()
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
	return true, cmd
}

func vimOwnsKey(mode vim.Mode, msg tea.KeyPressMsg) bool {
	if msg.Mod.Contains(tea.ModAlt) {
		return false
	}
	if msg.Code == tea.KeyEscape {
		return true
	}
	if mode == vim.ModeInsert {
		return msg.Text != "" || msg.Code == tea.KeyBackspace
	}
	if msg.Text != "" {
		return len([]rune(msg.Text)) == 1
	}
	switch msg.Code {
	case tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight:
		return true
	default:
		return false
	}
}

func (a *App) syncTextareaFromVim() {
	value := a.vimModel.Value()
	position := a.vimModel.Cursor()
	runes := []rune(value)
	if position < 0 {
		position = 0
	}
	if position > len(runes) {
		position = len(runes)
	}
	line, column := 0, 0
	for _, r := range runes[:position] {
		if r == '\n' {
			line++
			column = 0
		} else {
			column++
		}
	}
	a.textarea.SetValue(value)
	a.markComposerChanged()
	for a.textarea.Line() > line {
		a.textarea.CursorUp()
	}
	a.textarea.SetCursorColumn(column)
}

func (a *App) shortcut(context keybindings.Context, action keybindings.Action, fallback string) string {
	if a.keybindResolver == nil {
		return fallback
	}
	key := a.keybindResolver.GetKeyForAction(context, action)
	if key == "" {
		return ""
	}
	return key
}

func keyHint(key, label string) string {
	if key == "" {
		return ""
	}
	return key + " " + label
}

func joinKeyHints(hints ...string) string {
	filtered := hints[:0]
	for _, hint := range hints {
		if hint != "" {
			filtered = append(filtered, hint)
		}
	}
	return strings.Join(filtered, " · ")
}

func (a *App) keybindingSummary() string {
	rows := []struct {
		context keybindings.Context
		action  keybindings.Action
		label   string
	}{
		{keybindings.ContextChat, keybindings.ActionChatSubmit, "submit"},
		{keybindings.ContextChat, keybindings.ActionChatNewline, "newline"},
		{keybindings.ContextChat, keybindings.ActionHistorySearch, "reverse history search"},
		{keybindings.ContextChat, keybindings.ActionChatExternalEditor, "external editor"},
		{keybindings.ContextChat, keybindings.ActionChatUndo, "undo composer edit"},
		{keybindings.ContextGlobal, keybindings.ActionAppInterrupt, "interrupt / double-press exit"},
		{keybindings.ContextGlobal, keybindings.ActionAppExit, "exit"},
		{keybindings.ContextGlobal, keybindings.ActionAppToggleTranscript, "expanded transcript"},
		{keybindings.ContextGlobal, keybindings.ActionAppToggleTodos, "task panel"},
		{keybindings.ContextGlobal, keybindings.ActionAppGlobalSearch, "conversation search"},
		{keybindings.ContextGlobal, keybindings.ActionAppQuickOpen, "command palette"},
		{keybindings.ContextChat, keybindings.ActionChatCycleMode, "permission mode"},
		{keybindings.ContextChat, keybindings.ActionTaskBackground, "Agent/background tasks"},
		{keybindings.ContextChat, keybindings.ActionChatPreviousAgent, "previous Agent thread"},
		{keybindings.ContextChat, keybindings.ActionChatNextAgent, "next Agent thread"},
	}
	lines := []string{"Key bindings (active TUI):"}
	for _, row := range rows {
		keys := a.keybindResolver.GetKeysForAction(row.context, row.action)
		if len(keys) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %-22s %s", strings.Join(keys, " / "), row.label))
	}
	lines = append(lines, "", "Config: ~/.yhc/keybindings.json")
	if issues := a.keybindResolver.Issues(); len(issues) > 0 {
		lines = append(lines, "", "Config diagnostics:", keybindings.FormatValidationIssues(issues))
	}
	return strings.Join(lines, "\n")
}
