package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
)

type composerSuggestionState struct {
	Text       string
	ThreadID   string
	Revision   uint64
	QueryID    uint64
	Generation uint64
}

type composerSuggestionRequest struct {
	Engine     *engine.QueryEngine
	ThreadID   string
	Revision   uint64
	QueryID    uint64
	Generation uint64
	Cancel     context.CancelFunc
}

type composerSuggestionMsg struct {
	Engine     *engine.QueryEngine
	ThreadID   string
	Revision   uint64
	QueryID    uint64
	Generation uint64
	Suggestion string
}

func (a *App) composerSuggestionEligible() bool {
	if a == nil || a.state != StateChat || a.running || !a.isLeaderThreadView() ||
		a.inputMode != InputNormal || a.textarea.Value() != "" ||
		a.composerInputBlocked() || a.composerImageLoadPending != nil ||
		a.externalEditorActive || a.historySearch.Active ||
		a.suppressingHistoryHints() || !a.chat.Following() ||
		a.permissionMode() == permission.ModePlan {
		return false
	}
	return len(a.commandHints) == 0 && len(a.fileHints) == 0 &&
		len(a.mentionHints) == 0
}

func (a *App) requestComposerSuggestion() tea.Cmd {
	if a == nil || a.engine == nil || !a.composerSuggestionEligible() {
		return nil
	}
	a.dismissComposerSuggestion()
	a.composerSuggestionSerial++
	if a.composerSuggestionSerial == 0 {
		a.composerSuggestionSerial++
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &composerSuggestionRequest{
		Engine:     a.engine,
		ThreadID:   a.activeThreadViewID(),
		Revision:   a.composerRevision,
		QueryID:    a.queryID,
		Generation: a.composerSuggestionSerial,
		Cancel:     cancel,
	}
	a.composerSuggestionRequest = request
	return func() tea.Msg {
		return composerSuggestionMsg{
			Engine:     request.Engine,
			ThreadID:   request.ThreadID,
			Revision:   request.Revision,
			QueryID:    request.QueryID,
			Generation: request.Generation,
			Suggestion: request.Engine.GeneratePromptSuggestion(ctx),
		}
	}
}

func (a *App) handleComposerSuggestion(msg composerSuggestionMsg) {
	if a == nil {
		return
	}
	request := a.composerSuggestionRequest
	if request == nil || request.Engine != msg.Engine ||
		request.ThreadID != msg.ThreadID || request.Revision != msg.Revision ||
		request.QueryID != msg.QueryID || request.Generation != msg.Generation {
		return
	}
	if request.Cancel != nil {
		request.Cancel()
	}
	a.composerSuggestionRequest = nil
	suggestion := strings.TrimSpace(msg.Suggestion)
	if suggestion == "" || !a.composerSuggestionEligible() ||
		msg.Engine != a.engine || msg.ThreadID != a.activeThreadViewID() ||
		msg.Revision != a.composerRevision || msg.QueryID != a.queryID {
		return
	}
	a.composerSuggestion = composerSuggestionState{
		Text:       suggestion,
		ThreadID:   msg.ThreadID,
		Revision:   msg.Revision,
		QueryID:    msg.QueryID,
		Generation: msg.Generation,
	}
}

func (a *App) visibleComposerSuggestion() string {
	if a == nil || !a.composerSuggestionEligible() {
		return ""
	}
	suggestion := a.composerSuggestion
	if suggestion.Text == "" || suggestion.ThreadID != a.activeThreadViewID() ||
		suggestion.Revision != a.composerRevision ||
		suggestion.QueryID != a.queryID {
		return ""
	}
	return suggestion.Text
}

func (a *App) hasComposerSuggestionActivity() bool {
	return a != nil && (a.composerSuggestionRequest != nil ||
		a.composerSuggestion.Text != "")
}

func (a *App) dismissComposerSuggestion() {
	if a == nil {
		return
	}
	if request := a.composerSuggestionRequest; request != nil && request.Cancel != nil {
		request.Cancel()
	}
	a.composerSuggestionRequest = nil
	a.composerSuggestion = composerSuggestionState{}
}

func (a *App) acceptComposerSuggestion() tea.Cmd {
	suggestion := a.visibleComposerSuggestion()
	if suggestion == "" {
		return nil
	}
	before := a.captureComposerUndoEntry()
	a.dismissComposerSuggestion()
	a.textarea.SetValue(suggestion)
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
	return a.ensureMentionIndex()
}
