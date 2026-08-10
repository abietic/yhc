package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
)

func (a *App) leaderThreadViewID() string {
	if a != nil && a.threadViews != nil {
		return a.threadViews.leaderThreadID
	}
	if a != nil && a.engine != nil {
		return normalizeThreadViewID(a.engine.ThreadID())
	}
	return fallbackLeaderThreadID
}

func (a *App) isLeaderThreadView() bool {
	return a == nil || a.activeThreadViewID() == "" || a.activeThreadViewID() == a.leaderThreadViewID()
}

func (a *App) threadNavigationEntries() []engine.RuntimeThreadCatalogEntry {
	if a == nil {
		return nil
	}
	leaderID := a.leaderThreadViewID()
	var items []engine.RuntimeThreadCatalogEntry
	if a.threadCatalogProvider != nil {
		items = a.threadCatalogProvider().Threads
	}
	seen := make(map[string]struct{}, len(items)+1)
	deduplicated := make([]engine.RuntimeThreadCatalogEntry, 0, len(items)+1)
	for _, item := range items {
		item.ThreadID = strings.TrimSpace(item.ThreadID)
		if item.ThreadID == "" {
			continue
		}
		if _, exists := seen[item.ThreadID]; exists {
			continue
		}
		item.IsActive = item.ThreadID == a.activeThreadViewID()
		seen[item.ThreadID] = struct{}{}
		deduplicated = append(deduplicated, item)
	}
	if _, exists := seen[leaderID]; !exists {
		sessionID := ""
		if a.engine != nil {
			sessionID = a.engine.SessionID()
		}
		deduplicated = append(deduplicated, engine.RuntimeThreadCatalogEntry{
			ThreadID:  leaderID,
			SessionID: sessionID,
			Status:    engine.RuntimeThreadRunning,
			Mode:      engine.ThreadModeLiveAttach,
			IsActive:  leaderID == a.activeThreadViewID(),
		})
	}
	return stableThreadCatalogOrder(deduplicated, leaderID)
}

func (a *App) threadNavigationEntry(threadID string) (engine.RuntimeThreadCatalogEntry, bool) {
	if a == nil {
		return engine.RuntimeThreadCatalogEntry{}, false
	}
	if a.threadCatalogProvider != nil {
		for _, item := range a.threadCatalogProvider().Threads {
			if item.ThreadID == threadID {
				item.IsActive = item.ThreadID == a.activeThreadViewID()
				return item, true
			}
		}
	}
	if threadID == a.leaderThreadViewID() {
		return engine.RuntimeThreadCatalogEntry{
			ThreadID: threadID, Status: engine.RuntimeThreadRunning,
			Mode: engine.ThreadModeLiveAttach, IsActive: a.isLeaderThreadView(),
		}, true
	}
	return engine.RuntimeThreadCatalogEntry{}, false
}

func (a *App) openAgentThreadPicker() {
	if a == nil || a.agentPicker == nil {
		return
	}
	a.agentPicker.Show(a.threadNavigationEntries(), a.activeThreadViewID(), a.leaderThreadViewID())
	a.pushDialog(StateAgentPicker)
}

func (a *App) navigateAgentThread(delta int) tea.Cmd {
	items := a.threadNavigationEntries()
	if len(items) <= 1 {
		a.showToast("No other Agent threads")
		return nil
	}
	active := a.activeThreadViewID()
	index := 0
	for i, item := range items {
		if item.ThreadID == active {
			index = i
			break
		}
	}
	target := items[(index+delta+len(items))%len(items)]
	pageCmd, err := a.activateThreadEntry(target)
	if err != nil {
		a.showNotification(err.Error(), NotifyError)
		return nil
	}
	return tea.Batch(pageCmd, a.ensureSpinnerTick())
}

func (a *App) activateThreadByID(threadID string) error {
	cmd, err := a.activateThreadByIDWithCmd(threadID)
	if err != nil || cmd == nil {
		return err
	}
	if msg := cmd(); msg != nil {
		if loaded, ok := msg.(agentTranscriptPageLoadedMsg); ok {
			a.applyAgentTranscriptPage(loaded)
		}
	}
	return nil
}

func (a *App) activateThreadByIDWithCmd(threadID string) (tea.Cmd, error) {
	entry, ok := a.threadNavigationEntry(threadID)
	if !ok {
		return nil, fmt.Errorf("agent thread %q is no longer available", threadID)
	}
	return a.activateThreadEntry(entry)
}

func (a *App) activateThreadEntry(entry engine.RuntimeThreadCatalogEntry) (tea.Cmd, error) {
	if a == nil {
		return nil, fmt.Errorf("agent thread navigation is unavailable")
	}
	if entry.ThreadID == a.leaderThreadViewID() || entry.AgentID == "" {
		if err := a.switchThreadView(a.leaderThreadViewID(), engine.ThreadModeLiveAttach); err != nil {
			return nil, err
		}
		a.syncRuntimeThreadAttention()
		a.presentNextThreadAttention()
		return nil, nil
	}
	// Standalone TUI fixtures may install only the legacy detail reader. The
	// production binding always supplies AgentTranscriptPage and never enters
	// this compatibility path.
	if a.agentTranscriptProvider == nil && a.agentDetailProvider != nil {
		detail, ok := a.agentDetailProvider(entry.AgentID)
		if !ok {
			return nil, fmt.Errorf("agent thread %q has no readable transcript", entry.ThreadID)
		}
		if err := a.switchThreadView(entry.ThreadID, entry.Mode); err != nil {
			return nil, err
		}
		if view := a.threadViews.active(); view != nil {
			view.displayLabel = agentThreadLabel(entry, a.leaderThreadViewID())
		}
		a.projectAgentThread(detail)
		a.syncRuntimeThreadAttention()
		a.presentNextThreadAttention()
		return nil, nil
	}
	selection, _, ok := a.agentTranscriptSelection(entry)
	if !ok {
		return nil, fmt.Errorf("agent thread %q has no current generation", entry.ThreadID)
	}
	return a.activateResolvedThreadEntry(entry, selection)
}

func (a *App) activateTaskExplorerNavigationTarget(
	intent taskExplorerNavigationIntent,
) (tea.Cmd, error) {
	entry, selection, _, err := a.resolveTaskExplorerNavigationIntent(intent, true)
	if err != nil {
		return nil, err
	}
	return a.activateResolvedThreadEntryWithNavigation(
		entry,
		selection,
		&intent,
	)
}

func (a *App) resolveTaskExplorerNavigationIntent(
	intent taskExplorerNavigationIntent,
	requireIntentRevision bool,
) (engine.RuntimeThreadCatalogEntry, agentTranscriptSelection, uint64, error) {
	if a == nil || a.taskExplorerSnapshotSource == nil ||
		a.threadCatalogProvider == nil {
		return engine.RuntimeThreadCatalogEntry{}, agentTranscriptSelection{}, 0,
			fmt.Errorf("exact Task Explorer navigation is unavailable")
	}
	target := intent.Target
	if strings.TrimSpace(target.SessionID) == "" ||
		strings.TrimSpace(target.ThreadID) == "" ||
		strings.TrimSpace(target.AgentID) == "" ||
		target.Generation <= 0 || target.Mode != engine.ThreadModeLiveAttach {
		return engine.RuntimeThreadCatalogEntry{}, agentTranscriptSelection{}, 0,
			fmt.Errorf("exact Task Explorer navigation target is invalid")
	}
	snapshot := a.taskExplorerSnapshotSource()
	catalog := a.threadCatalogProvider()
	if !snapshot.Available || snapshot.Revision.Runtime != catalog.Revision ||
		(requireIntentRevision &&
			snapshot.Revision.Runtime != intent.RuntimeRevision) {
		return engine.RuntimeThreadCatalogEntry{}, agentTranscriptSelection{}, 0,
			fmt.Errorf("exact Task Explorer navigation target is stale")
	}
	var execution *engine.TaskExplorerExecution
	for index := range snapshot.Executions {
		candidate := &snapshot.Executions[index]
		if candidate.Key.AgentID != target.AgentID ||
			candidate.Key.Generation != target.Generation {
			continue
		}
		if execution != nil {
			return engine.RuntimeThreadCatalogEntry{}, agentTranscriptSelection{}, 0,
				fmt.Errorf("exact Task Explorer execution is duplicated")
		}
		execution = candidate
	}
	if execution == nil || strings.TrimSpace(execution.SessionID) != target.SessionID ||
		strings.TrimSpace(execution.ThreadID) != target.ThreadID ||
		strings.TrimSpace(execution.TranscriptPath) == "" ||
		execution.Predispatch || execution.ReplayOnly ||
		execution.Phase == engine.TaskExplorerExecutionReplayOnly ||
		!taskExplorerExecutionAllows(
			*execution,
			engine.TaskExplorerActionSwitch,
		) {
		return engine.RuntimeThreadCatalogEntry{}, agentTranscriptSelection{}, 0,
			fmt.Errorf("exact Task Explorer execution changed before activation")
	}
	var entry *engine.RuntimeThreadCatalogEntry
	for index := range catalog.Threads {
		candidate := &catalog.Threads[index]
		if strings.TrimSpace(candidate.ThreadID) != target.ThreadID {
			continue
		}
		if entry != nil {
			return engine.RuntimeThreadCatalogEntry{}, agentTranscriptSelection{}, 0,
				fmt.Errorf("exact Task Explorer thread is duplicated")
		}
		entry = candidate
	}
	if entry == nil || strings.TrimSpace(entry.SessionID) != target.SessionID ||
		strings.TrimSpace(entry.AgentID) != target.AgentID ||
		strings.TrimSpace(entry.TranscriptPath) == "" ||
		entry.Mode != target.Mode {
		return engine.RuntimeThreadCatalogEntry{}, agentTranscriptSelection{}, 0,
			fmt.Errorf("exact Task Explorer thread changed before activation")
	}
	selection := agentTranscriptSelection{
		AgentID: target.AgentID, ThreadID: target.ThreadID,
		Generation: target.Generation, Mode: target.Mode,
	}
	return *entry, selection, snapshot.Revision.Runtime, nil
}

func (a *App) activateResolvedThreadEntry(
	entry engine.RuntimeThreadCatalogEntry,
	selection agentTranscriptSelection,
) (tea.Cmd, error) {
	return a.activateResolvedThreadEntryWithNavigation(entry, selection, nil)
}

func (a *App) activateResolvedThreadEntryWithNavigation(
	entry engine.RuntimeThreadCatalogEntry,
	selection agentTranscriptSelection,
	navigation *taskExplorerNavigationIntent,
) (tea.Cmd, error) {
	if entry.Mode == "" {
		entry.Mode = engine.ThreadModeLiveAttach
	}
	if !selection.valid() || selection.ThreadID != entry.ThreadID ||
		selection.AgentID != entry.AgentID || selection.Mode != entry.Mode {
		return nil, fmt.Errorf("agent thread navigation target changed")
	}
	if a.agentTranscriptProvider == nil {
		return nil, fmt.Errorf("agent thread transcript is unavailable")
	}
	if err := a.switchThreadView(entry.ThreadID, entry.Mode); err != nil {
		return nil, err
	}
	if view := a.threadViews.active(); view != nil {
		view.displayLabel = agentThreadLabel(entry, a.leaderThreadViewID())
		view.transcript.bind(selection)
		view.transcript.bindTaskExplorerNavigation(navigation)
		a.projectActiveAgentTranscript()
		request, started := view.transcript.begin(agentTranscriptSurfaceThread, false)
		if started {
			a.syncRuntimeThreadAttention()
			a.presentNextThreadAttention()
			return agentTranscriptPageCmd(a.agentTranscriptProvider, request), nil
		}
	}
	a.syncRuntimeThreadAttention()
	a.presentNextThreadAttention()
	return nil, nil
}

func (a *App) agentTranscriptSelection(entry engine.RuntimeThreadCatalogEntry) (agentTranscriptSelection, uint64, bool) {
	if a == nil || a.taskExplorerSnapshotSource == nil {
		return agentTranscriptSelection{}, 0, false
	}
	snapshot := a.taskExplorerSnapshotSource()
	for _, execution := range snapshot.Executions {
		if execution.Key.AgentID != entry.AgentID ||
			execution.ThreadID != entry.ThreadID {
			continue
		}
		selection := agentTranscriptSelectionFromExecution(execution, entry.Mode)
		return selection, snapshot.Revision.Runtime, selection.valid()
	}
	return agentTranscriptSelection{}, snapshot.Revision.Runtime, false
}

func (a *App) agentTranscriptSelectionByAgent(agentID string) (agentTranscriptSelection, bool) {
	if a == nil || a.threadCatalogProvider == nil {
		return agentTranscriptSelection{}, false
	}
	for _, entry := range a.threadCatalogProvider().Threads {
		if entry.AgentID == agentID {
			selection, _, found := a.agentTranscriptSelection(entry)
			return selection, found
		}
	}
	return agentTranscriptSelection{}, false
}

func (a *App) projectActiveAgentTranscript() {
	if a == nil || a.threadViews == nil || a.chat == nil {
		return
	}
	view := a.threadViews.active()
	if view == nil || view.ThreadID == a.leaderThreadViewID() {
		return
	}
	follow := a.chat.Following()
	offsetIdx := a.chat.offsetIdx
	offsetLine := a.chat.offsetLine
	anchor := ""
	if !follow && offsetIdx >= 0 && offsetIdx < len(view.projectedIDs) {
		anchor = view.projectedIDs[offsetIdx]
	}
	a.chat.Reset()
	identities := make([]string, 0, len(view.transcript.messages))
	a.chat.withHydrationIntent(func() {
		for _, message := range view.transcript.messages {
			identity := agentTranscriptMessageIdentity(message)
			if identity == "" {
				continue
			}
			identities = append(identities, identity)
			a.chat.appendHydratedHistoryItem(newAgentTranscriptHistoryItem(message))
		}
		if view.transcript.err != "" {
			a.chat.AppendSystem("Agent transcript: " + view.transcript.err)
		}
	})
	if follow {
		a.chat.ResetFollow()
	} else {
		a.chat.restoreAway()
		a.chat.offsetIdx = min(max(0, offsetIdx), max(0, len(a.chat.items)-1))
		if anchor != "" {
			for index, identity := range identities {
				if identity == anchor {
					a.chat.offsetIdx = index
					break
				}
			}
		}
		a.chat.offsetLine = max(0, offsetLine)
		a.chat.viewDirty = true
	}
	if a.selection != nil {
		a.selection.Clear()
	}
	view.projectedAgentID = view.transcript.selection.AgentID
	view.projectedRevision = view.transcript.revision
	view.projectedIDs = identities
}

func (a *App) applyAgentTranscriptPage(msg agentTranscriptPageLoadedMsg) bool {
	if a == nil {
		return false
	}
	switch msg.request.surface {
	case agentTranscriptSurfaceThread:
		if a.threadViews == nil {
			return false
		}
		view := a.threadViews.views[msg.request.selection.ThreadID]
		if view == nil || view.ThreadID != a.activeThreadViewID() {
			if view != nil {
				view.transcript.discard(msg)
			}
			return false
		}
		intent := msg.request.taskExplorerNavigation
		if intent == nil && view.transcript.taskExplorerNavigation != nil &&
			view.transcript.selection == msg.request.selection {
			intent = view.transcript.taskExplorerNavigation
		}
		if intent != nil {
			if err := a.validateTaskExplorerTranscriptSelection(
				*intent,
				msg.request.selection,
			); err != nil {
				view.transcript.discard(msg)
				a.rejectTaskExplorerTranscriptSelection(view, err)
				return false
			}
			if !view.transcript.apply(msg) {
				return false
			}
			view.Mode = view.transcript.selection.Mode
			a.projectActiveAgentTranscript()
			return true
		}
		entry, exists := a.threadNavigationEntry(view.ThreadID)
		if !exists {
			view.transcript.discard(msg)
			return false
		}
		current, _, exists := a.agentTranscriptSelection(entry)
		if !exists || current != msg.request.selection {
			view.transcript.bind(current)
			a.projectActiveAgentTranscript()
			return false
		}
		if !view.transcript.apply(msg) {
			return false
		}
		view.Mode = view.transcript.selection.Mode
		a.projectActiveAgentTranscript()
		return true
	case agentTranscriptSurfaceBackground:
		return a.backgroundTasks != nil && a.backgroundTasks.applyTranscriptPage(msg)
	case agentTranscriptSurfaceTeams:
		return a.teamsPanel != nil && a.teamsPanel.applyTranscriptPage(msg)
	case agentTranscriptSurfaceTaskExplorer:
		if a.state != StateTaskPanel || a.taskExplorer == nil {
			if a.taskExplorer != nil {
				a.taskExplorer.transcript.discard(msg)
			}
			return false
		}
		return a.taskExplorer.applyTranscriptPage(msg)
	default:
		return false
	}
}

func (a *App) loadOlderActiveAgentTranscript() tea.Cmd {
	if a == nil || a.isLeaderThreadView() || a.threadViews == nil || a.agentTranscriptProvider == nil {
		return nil
	}
	view := a.threadViews.active()
	if view == nil || a.chat == nil || a.chat.offsetIdx > 0 || a.chat.offsetLine > 0 {
		return nil
	}
	if intent := view.transcript.taskExplorerNavigation; intent != nil {
		if err := a.validateTaskExplorerTranscriptSelection(
			*intent,
			view.transcript.selection,
		); err != nil {
			a.rejectTaskExplorerTranscriptSelection(view, err)
			return nil
		}
	}
	request, started := view.transcript.older(agentTranscriptSurfaceThread)
	if !started {
		return nil
	}
	return agentTranscriptPageCmd(a.agentTranscriptProvider, request)
}

func (a *App) validateTaskExplorerTranscriptSelection(
	intent taskExplorerNavigationIntent,
	selection agentTranscriptSelection,
) error {
	_, current, _, err := a.resolveTaskExplorerNavigationIntent(intent, false)
	if err != nil {
		return err
	}
	if current != selection {
		return fmt.Errorf("exact Task Explorer navigation target changed while loading")
	}
	return nil
}

func (a *App) rejectTaskExplorerTranscriptSelection(
	view *threadViewState,
	err error,
) {
	if a == nil || view == nil || err == nil {
		return
	}
	message := err.Error()
	alreadyVisible := view.transcript.err == message
	view.transcript.err = message
	a.projectActiveAgentTranscript()
	if !alreadyVisible {
		a.showNotification(message, NotifyError)
	}
}

func (a *App) projectAgentThread(detail engine.AgentDetailSnapshot) {
	if a == nil || a.threadViews == nil || a.chat == nil {
		return
	}
	view := a.threadViews.active()
	if view == nil || view.ThreadID == a.leaderThreadViewID() {
		return
	}
	if detail.PendingMessageCount < len(a.queuedInputPreview) {
		removeCount := len(a.queuedInputPreview) - max(0, detail.PendingMessageCount)
		a.queuedInputPreview = append([]threadQueuedInput(nil), a.queuedInputPreview[removeCount:]...)
	}
	if view.projectedAgentID == detail.Agent.AgentID && view.projectedRevision == detail.Revision && len(a.chat.Items()) > 0 {
		return
	}
	follow := a.chat.Following()
	offsetIdx := a.chat.offsetIdx
	offsetLine := a.chat.offsetLine
	a.chat.Reset()
	a.chat.withHydrationIntent(func() {
		for _, message := range detail.Messages {
			a.appendAgentDetailMessage(message)
		}
		if detail.LoadError != "" {
			a.chat.AppendSystem("Agent detail: " + detail.LoadError)
		}
	})
	if isThreadTerminalStatus(detail.Thread.Status) {
		a.chat.FinishAssistant()
	}
	if follow {
		a.chat.ResetFollow()
	} else {
		a.chat.restoreAway()
		a.chat.offsetIdx = min(max(0, offsetIdx), max(0, len(a.chat.items)-1))
		a.chat.offsetLine = max(0, offsetLine)
		a.chat.viewDirty = true
	}
	view.projectedAgentID = detail.Agent.AgentID
	view.projectedRevision = detail.Revision
}

func (a *App) appendAgentDetailMessage(message engine.AgentDetailMessage) {
	role := strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case string(schema.User):
		a.chat.AppendUser(message.Content)
	case string(schema.Assistant):
		if message.ReasoningContent != "" {
			a.chat.StreamThinkingDelta(message.ReasoningContent)
			if message.Completed {
				a.chat.FinishThinking()
			}
		}
		if message.Content != "" {
			a.chat.AppendOrUpdateAssistant(message.Content)
			if message.Completed {
				a.chat.FinishAssistant()
			}
		}
		for _, call := range message.ToolCalls {
			a.chat.AppendOrUpdateTool(call.ID, call.Name, call.InputPreview)
		}
	case string(schema.Tool):
		if message.ToolCallID != "" || message.ToolName != "" {
			a.chat.UpdateToolResult(message.ToolCallID, message.ToolName, message.Content)
		} else if message.Content != "" {
			a.chat.AppendSystem(message.Content)
		}
	case string(schema.System):
		a.chat.AppendSystem(message.Content)
	default:
		if strings.TrimSpace(message.Content) != "" {
			a.chat.AppendSystem(message.Content)
		}
	}
}

func (a *App) refreshActiveThreadProjection() bool {
	running, cmd := a.refreshActiveThreadProjectionWithCmd()
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if loaded, ok := msg.(agentTranscriptPageLoadedMsg); ok {
				a.applyAgentTranscriptPage(loaded)
			}
		}
	}
	return running
}

func (a *App) refreshActiveThreadProjectionWithCmd() (bool, tea.Cmd) {
	if a == nil || a.isLeaderThreadView() {
		return false, nil
	}
	view := a.threadViews.active()
	if view != nil && view.transcript.taskExplorerNavigation != nil {
		intent := *view.transcript.taskExplorerNavigation
		entry, selection, revision, err := a.resolveTaskExplorerNavigationIntent(
			intent,
			false,
		)
		if err == nil && selection != view.transcript.selection {
			err = fmt.Errorf("exact Task Explorer navigation target changed while loading")
		}
		if err != nil {
			a.rejectTaskExplorerTranscriptSelection(view, err)
			return false, nil
		}
		view.Mode = entry.Mode
		view.displayLabel = agentThreadLabel(entry, a.leaderThreadViewID())
		var pageCmd tea.Cmd
		if a.agentTranscriptProvider == nil {
			a.rejectTaskExplorerTranscriptSelection(
				view,
				fmt.Errorf("agent thread transcript is unavailable"),
			)
			return false, nil
		}
		force := view.transcript.initialized && selection.writable() &&
			a.chat.Following() && revision > view.transcript.revision
		if request, started := view.transcript.begin(
			agentTranscriptSurfaceThread,
			force,
		); started {
			pageCmd = agentTranscriptPageCmd(a.agentTranscriptProvider, request)
		}
		if isThreadActiveStatus(entry.Status) {
			view.refreshTicks = 0
			return true, pageCmd
		}
		if view.refreshTicks > 0 {
			view.refreshTicks--
			return true, pageCmd
		}
		return false, pageCmd
	}
	entry, ok := a.threadNavigationEntry(a.activeThreadViewID())
	if !ok {
		leaderID := a.leaderThreadViewID()
		if err := a.switchThreadView(leaderID, engine.ThreadModeLiveAttach); err == nil {
			a.showNotification("Active Agent thread closed; returned to main", NotifyWarning)
			a.syncRuntimeThreadAttention()
			a.presentNextThreadAttention()
		}
		return false, nil
	}
	if view != nil {
		view.Mode = entry.Mode
		view.displayLabel = agentThreadLabel(entry, a.leaderThreadViewID())
	}
	var pageCmd tea.Cmd
	if view != nil && entry.AgentID != "" {
		if a.agentTranscriptProvider == nil && a.agentDetailProvider != nil {
			if detail, exists := a.agentDetailProvider(entry.AgentID); exists {
				a.projectAgentThread(detail)
			}
		} else if selection, revision, found := a.agentTranscriptSelection(entry); found {
			changed := view.transcript.bind(selection)
			if changed {
				a.projectActiveAgentTranscript()
			}
			force := !changed && view.transcript.initialized && selection.writable() &&
				a.chat.Following() && revision > view.transcript.revision
			if request, started := view.transcript.begin(agentTranscriptSurfaceThread, force); started {
				pageCmd = agentTranscriptPageCmd(a.agentTranscriptProvider, request)
			}
		}
	}
	if isThreadActiveStatus(entry.Status) {
		if view != nil {
			view.refreshTicks = 0
		}
		return true, pageCmd
	}
	if view != nil && view.refreshTicks > 0 {
		view.refreshTicks--
		return true, pageCmd
	}
	return false, pageCmd
}

func (a *App) sendActiveAgentThreadMessage(content string) tea.Cmd {
	entry, ok := a.threadNavigationEntry(a.activeThreadViewID())
	if !ok || entry.AgentID == "" {
		a.showNotification("Active Agent thread is no longer available", NotifyError)
		return nil
	}
	if entry.Mode == engine.ThreadModeReplayOnly || entry.Mode == engine.ThreadModeEvictedTranscript {
		a.showNotification("Replay and evicted Agent views are read-only", NotifyWarning)
		return nil
	}
	if a.taskExplorerSnapshotSource == nil ||
		a.taskExplorerActionProvider == nil {
		a.showNotification("Agent messaging is unavailable", NotifyError)
		return nil
	}
	snapshot := a.taskExplorerSnapshotSource()
	execution, ok := taskExplorerExecutionForThread(snapshot, entry)
	if !ok {
		a.showNotification(
			"Active Agent generation is no longer available",
			NotifyError,
		)
		return nil
	}
	action := engine.TaskExplorerActionSend
	if !taskExplorerExecutionAllows(execution, action) {
		action = engine.TaskExplorerActionContinue
	}
	if !taskExplorerExecutionAllows(execution, action) {
		a.showNotification(
			"Agent messaging is unavailable for this exact generation",
			NotifyWarning,
		)
		return nil
	}
	requestID := uuid.NewString()
	result := a.taskExplorerActionProvider(engine.TaskExplorerActionRequest{
		RequestID:       requestID,
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         execution.Key.AgentID,
		Generation:      execution.Key.Generation,
		Action:          action,
		Payload:         content,
	})
	if result.RequestID != requestID ||
		result.BoardID != snapshot.BoardID ||
		result.BoardRevision != snapshot.Revision.Board ||
		result.AgentID != execution.Key.AgentID ||
		result.Generation != execution.Key.Generation ||
		result.Action != action ||
		result.Conflict != "" {
		a.showNotification(
			firstNonEmptyTUIText(
				result.Message,
				result.Conflict,
				"Agent messaging conflicted with refreshed state",
			),
			NotifyError,
		)
		return nil
	}
	display := strings.TrimSpace(a.textarea.Value())
	if display == "" {
		display = content
	}
	if action == engine.TaskExplorerActionSend &&
		result.Outcome == "sent" {
		a.queuedInputPreview = cloneThreadQueuedInputs(append(a.queuedInputPreview, threadQueuedInput{
			ID:            result.MessageID,
			Content:       display,
			BoardID:       result.BoardID,
			BoardRevision: result.BoardRevision,
			AgentID:       result.AgentID,
			Generation:    result.Generation,
			RequestID:     result.RequestID,
			Parts:         []engine.QueuedPromptPart{{Kind: engine.QueuedPromptPartText, Text: content}},
		}))
	} else {
		a.chat.ResetFollow()
		a.chat.AppendUserWithComposer(display, a.composerDisplayElements())
	}
	if view := a.threadViews.active(); view != nil {
		view.projectedRevision = 0
		view.refreshTicks = 50
	}
	a.clearInputAfterSubmit(content)
	if action == engine.TaskExplorerActionContinue {
		a.showToast("Agent resumed")
	} else {
		a.showToast("Message queued")
	}
	return a.ensureSpinnerTick()
}

func taskExplorerExecutionForThread(
	snapshot engine.TaskExplorerSnapshot,
	entry engine.RuntimeThreadCatalogEntry,
) (engine.TaskExplorerExecution, bool) {
	var selected engine.TaskExplorerExecution
	found := false
	for _, execution := range snapshot.Executions {
		if execution.Key.AgentID != entry.AgentID ||
			execution.ThreadID != entry.ThreadID {
			continue
		}
		if !found || execution.Key.Generation > selected.Key.Generation {
			selected = execution
			found = true
		}
	}
	return selected, found
}

func taskExplorerExecutionAllows(
	execution engine.TaskExplorerExecution,
	action engine.TaskExplorerAction,
) bool {
	for _, candidate := range execution.AllowedActions {
		if candidate == action {
			return true
		}
	}
	return false
}

func (a *App) activeThreadDisplayLabel() string {
	if a == nil || a.isLeaderThreadView() {
		return "thread:main"
	}
	label := ""
	if a.threadViews != nil && a.threadViews.active() != nil {
		label = a.threadViews.active().displayLabel
	}
	if strings.TrimSpace(label) == "" {
		label = a.activeThreadViewID()
	}
	return "thread:@" + contentEllipsize(
		a.renderEnvironment.profile,
		label,
		20,
		len("thread:@"),
		"…",
	)
}

func isThreadActiveStatus(status engine.RuntimeThreadStatus) bool {
	switch status {
	case engine.RuntimeThreadRunning, engine.RuntimeThreadPaused, engine.RuntimeThreadWaitingInput:
		return true
	default:
		return false
	}
}

func isThreadTerminalStatus(status engine.RuntimeThreadStatus) bool {
	switch status {
	case engine.RuntimeThreadCompleted, engine.RuntimeThreadFailed, engine.RuntimeThreadAborted:
		return true
	default:
		return false
	}
}
