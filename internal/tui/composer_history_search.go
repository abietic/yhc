package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/internal/tui/keybindings"
)

type composerHistorySearch struct {
	Active   bool
	Query    string
	Original composerUndoEntry
	Matches  []int
	Selected int
}

func (a *App) startHistorySearch() {
	if a == nil || a.historySearch.Active {
		return
	}
	if a.inputMode != InputNormal {
		a.showNotification("History search is available for ordinary prompts", NotifyWarning)
		return
	}
	a.dismissMentionHints()
	a.commandHints = nil
	a.fileHints = nil
	a.historySearch = composerHistorySearch{
		Active: true, Query: a.textarea.Value(), Original: a.captureComposerUndoEntry(), Selected: 0,
	}
	a.refreshHistorySearch()
	a.updateLayout()
}

func (a *App) handleHistorySearchKey(msg tea.KeyPressMsg) tea.Cmd {
	if !a.historySearch.Active {
		return nil
	}
	if handled, cmd := a.resolveKeyAction(msg, keybindings.ContextHistorySearch); handled {
		return cmd
	}
	switch {
	case msg.Text != "":
		if msg.Mod.Contains(tea.ModAlt) {
			return nil
		}
		a.historySearch.Query += msg.Text
		a.refreshHistorySearch()
	case msg.Code == tea.KeyBackspace || msg.String() == "ctrl+h":
		query := []rune(a.historySearch.Query)
		if len(query) > 0 {
			a.historySearch.Query = string(query[:len(query)-1])
			a.refreshHistorySearch()
		}
	}
	a.updateLayout()
	return nil
}

func (a *App) refreshHistorySearch() {
	query := strings.ToLower(a.historySearch.Query)
	matches := make([]int, 0)
	for index := len(a.history) - 1; index >= 0; index-- {
		if query == "" || strings.Contains(strings.ToLower(a.history[index]), query) {
			matches = append(matches, index)
		}
	}
	a.historySearch.Matches = matches
	a.historySearch.Selected = 0
	if len(matches) == 0 {
		a.applyComposerUndoEntry(a.historySearch.Original)
		return
	}
	a.historySearch.Selected = min(max(0, a.historySearch.Selected), len(matches)-1)
	index := matches[a.historySearch.Selected]
	a.restoreComposerHistoryEntry(a.history[index], a.richHistoryElements[index])
}

func (a *App) advanceHistorySearch() {
	if !a.historySearch.Active {
		return
	}
	if len(a.historySearch.Matches) == 0 {
		a.refreshHistorySearch()
		return
	}
	if a.historySearch.Selected < len(a.historySearch.Matches)-1 {
		a.historySearch.Selected++
	}
	index := a.historySearch.Matches[a.historySearch.Selected]
	a.restoreComposerHistoryEntry(a.history[index], a.richHistoryElements[index])
}

func (a *App) acceptHistorySearch(execute bool) tea.Cmd {
	if !a.historySearch.Active {
		return nil
	}
	matched := len(a.historySearch.Matches) > 0
	if !matched {
		a.applyComposerUndoEntry(a.historySearch.Original)
	} else {
		a.recordComposerUndo(a.historySearch.Original)
		a.historyIdx = a.historySearch.Matches[a.historySearch.Selected]
	}
	a.historySearch = composerHistorySearch{}
	a.syncInputModeFromText()
	a.updateLayout()
	if execute && matched {
		return a.sendMessage()
	}
	return nil
}

func (a *App) cancelHistorySearch() {
	if a == nil || !a.historySearch.Active {
		return
	}
	a.applyComposerUndoEntry(a.historySearch.Original)
	a.historySearch = composerHistorySearch{}
	a.historyIdx = len(a.history)
	a.syncInputModeFromText()
	a.updateLayout()
}

func (a *App) renderHistorySearch() string {
	if a == nil || !a.historySearch.Active {
		return ""
	}
	status := "no match"
	if len(a.historySearch.Matches) > 0 {
		status = fmt.Sprintf("%d/%d", a.historySearch.Selected+1, len(a.historySearch.Matches))
	}
	hints := joinKeyHints(
		keyHint(a.shortcut(keybindings.ContextHistorySearch, keybindings.ActionHistorySearchNext, "ctrl+r"), "older"),
		keyHint(a.shortcut(keybindings.ContextHistorySearch, keybindings.ActionHistorySearchAccept, "enter"), "accept"),
		keyHint(a.shortcut(keybindings.ContextHistorySearch, keybindings.ActionHistorySearchCancel, "esc"), "cancel"),
	)
	line := fmt.Sprintf(" reverse-i-search: %s  %s", a.historySearch.Query, status)
	if hints != "" {
		line += "  " + hints
	}
	line = contentEllipsize(
		a.renderEnvironment.profile,
		line,
		max(20, a.width-8),
		0,
		"…",
	)
	return a.styles.Subtle.Render(line)
}
