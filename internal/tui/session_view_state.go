package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
)

const sessionViewSaveDelay = 500 * time.Millisecond

type sessionViewSaveDueMsg struct {
	generation uint64
}

func (a *App) scheduleSessionViewSave() tea.Cmd {
	if a == nil || a.engine == nil || a.sessionRestorePending {
		return nil
	}
	a.sessionViewSaveGeneration++
	generation := a.sessionViewSaveGeneration
	return tea.Tick(sessionViewSaveDelay, func(time.Time) tea.Msg {
		return sessionViewSaveDueMsg{generation: generation}
	})
}

func (a *App) invalidateSessionViewSave() {
	if a != nil {
		a.sessionViewSaveGeneration++
	}
}

func (a *App) persistSessionViewState() error {
	if a == nil || a.engine == nil || a.threadViews == nil {
		return nil
	}
	a.captureActiveThreadView()
	state := session.PersistedSessionViewState{
		SessionID:      a.engine.SessionID(),
		ActiveThreadID: a.threadViews.activeThreadID,
		UpdatedAt:      time.Now().UTC(),
	}
	threadIDs := make([]string, 0, len(a.threadViews.views))
	for threadID := range a.threadViews.views {
		threadIDs = append(threadIDs, threadID)
	}
	sort.Strings(threadIDs)
	for _, threadID := range threadIDs {
		view := a.threadViews.views[threadID]
		if view == nil {
			continue
		}
		persisted := session.PersistedThreadViewState{
			ThreadID:     view.ThreadID,
			Mode:         string(view.Mode),
			Draft:        view.Editor.Draft,
			CursorLine:   view.Editor.CursorLine,
			CursorColumn: view.Editor.CursorColumn,
			InputMode:    int(view.Editor.InputMode),
			DetailTab:    int(view.DetailTab),
			Follow:       true,
		}
		if view.Chat != nil {
			persisted.ScrollItem = view.Chat.offsetIdx
			persisted.ScrollLine = view.Chat.offsetLine
			persisted.Follow = view.Chat.Following()
		}
		state.Threads = append(state.Threads, persisted)
	}
	return session.SaveSessionViewState(a.engine.GetTranscriptDir(), state.SessionID, state)
}

func (a *App) resetAndRestoreSessionViews() error {
	if a == nil || a.engine == nil {
		return nil
	}
	leaderID := normalizeThreadViewID(a.engine.ThreadID())
	a.cancelHistorySearch()
	a.chat = newChatViewWithRenderEnvironment(a.renderEnvironment)
	a.search = NewSearchOverlay(a.styles)
	a.selection = &Selection{}
	a.composerElements = nil
	a.queuedInputPreview = nil
	a.composerUndo = nil
	a.draftElements = nil
	a.draft = ""
	a.textarea.SetValue("")
	a.inputMode = InputNormal
	a.threadDetailTab = agentDetailOverview
	a.threadViews = newThreadViewStoreWithRenderEnvironment(defaultThreadViewLimit, leaderID, a.renderEnvironment)
	a.markComposerChanged()
	a.gcDraftMedia()
	leader := a.threadViews.active()
	leader.Chat = a.chat
	leader.Search = a.search
	leader.Selection = a.selection
	a.reloadChatFromEngine()

	persisted, err := session.LoadSessionViewState(a.engine.GetTranscriptDir(), a.engine.SessionID())
	if err != nil {
		a.restoreThreadView(leader)
		return err
	}
	catalog := a.engine.ThreadCatalogSnapshot()
	available := make(map[string]engine.ThreadAttachmentMode, len(catalog.Threads)+1)
	available[leaderID] = engine.ThreadModeLiveAttach
	for _, entry := range catalog.Threads {
		if strings.TrimSpace(entry.ThreadID) != "" {
			available[entry.ThreadID] = entry.Mode
		}
	}
	for _, saved := range persisted.Threads {
		threadID := normalizeThreadViewID(saved.ThreadID)
		mode, ok := available[threadID]
		if !ok {
			continue
		}
		view, activateErr := a.threadViews.activate(threadID, mode)
		if activateErr != nil {
			continue
		}
		if threadID == leaderID {
			view.Chat = a.chat
			view.Search = a.search
			view.Selection = a.selection
		}
		view.Editor = threadEditorState{
			Draft: saved.Draft, CursorLine: saved.CursorLine, CursorColumn: saved.CursorColumn,
			InputMode: restoredInputMode(saved.InputMode), HistoryIndex: len(a.history),
			CommandHint: -1, FileHint: -1, MentionHint: -1,
		}
		if saved.DetailTab >= int(agentDetailOverview) && saved.DetailTab < int(agentDetailTabCount) {
			view.DetailTab = agentDetailTab(saved.DetailTab)
		}
		if view.Chat != nil {
			view.Chat.offsetIdx = saved.ScrollItem
			view.Chat.offsetLine = saved.ScrollLine
			if saved.Follow {
				view.Chat.followState.follow()
			} else {
				// Durable view state deliberately does not persist append baselines.
				view.Chat.restoreAway()
			}
			view.Chat.viewDirty = true
		}
	}
	activeID := leaderID
	if _, ok := available[persisted.ActiveThreadID]; ok {
		if _, exists := a.threadViews.views[persisted.ActiveThreadID]; exists {
			activeID = persisted.ActiveThreadID
		}
	}
	view, activateErr := a.threadViews.activate(activeID, available[activeID])
	if activateErr != nil {
		return fmt.Errorf("restore active thread view: %w", activateErr)
	}
	a.restoreThreadView(view)
	if activeID != leaderID {
		a.refreshActiveThreadProjection()
	}
	a.model = a.engine.GetModelName()
	a.permMode = a.engine.PermissionMode()
	return nil
}

func restoredInputMode(value int) InputMode {
	mode := InputMode(value)
	switch mode {
	case InputNormal, InputCommand, InputShell:
		return mode
	default:
		return InputNormal
	}
}
