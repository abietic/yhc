package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/engine"
)

const (
	defaultThreadViewLimit       = 128
	maxThreadComposerElements    = 32
	maxThreadQueuedInputPreviews = 32
	maxThreadElementValueRunes   = 1024
	maxThreadQueuedInputRunes    = 4096
	fallbackLeaderThreadID       = "__leader__"
)

// threadComposerElement owns only a typed placeholder and its rune range.
// Image bytes live once in App.draftMedia and are addressed by ID.
type threadComposerElement struct {
	ID       string
	Kind     string
	Label    string
	Name     string
	Value    string
	MIMEType string
	Data     string
	Start    int
	End      int
}

// threadQueuedInput is a non-dispatchable projection of an engine-owned
// pending input. It never carries prompt media bytes, paths, refs, or IDs.
type threadQueuedInput struct {
	ID            string
	Content       string
	Parts         []engine.QueuedPromptPart
	EnqueuedAt    time.Time
	State         engine.RuntimeItemState
	Unavailable   bool
	BoardID       string
	BoardRevision uint64
	AgentID       string
	Generation    int64
	RequestID     string
}

type threadEditorState struct {
	Draft                string
	CursorLine           int
	CursorColumn         int
	InputMode            InputMode
	HistoryIndex         int
	HistoryDraft         string
	HistoryDraftElements []threadComposerElement
	Undo                 []composerUndoEntry
	CommandHint          int
	FileHint             int
	MentionHint          int
}

// threadViewState contains presentation state only. Chat items are projections
// and can be rebuilt from RuntimeStateStore/transcripts after clean eviction.
type threadViewState struct {
	ThreadID string
	Mode     engine.ThreadAttachmentMode

	Chat      *ChatView
	Search    *SearchOverlay
	Selection *Selection
	Editor    threadEditorState

	ComposerElements  []threadComposerElement
	QueuePreview      []threadQueuedInput
	DetailTab         agentDetailTab
	Surface           AppState
	transcript        agentTranscriptPager
	projectedRevision uint64
	projectedAgentID  string
	projectedIDs      []string
	displayLabel      string
	refreshTicks      int

	lastUsed uint64
}

type threadViewStore struct {
	limit          int
	leaderThreadID string
	activeThreadID string
	nextUse        uint64
	styles         Styles
	environment    RenderEnvironment
	views          map[string]*threadViewState
}

func newThreadViewStore(limit int, leaderThreadID string, styles Styles) *threadViewStore {
	return newThreadViewStoreWithRenderEnvironment(limit, leaderThreadID, defaultRenderEnvironment(styles))
}

func newThreadViewStoreWithRenderEnvironment(limit int, leaderThreadID string, env RenderEnvironment) *threadViewStore {
	if limit <= 0 {
		limit = defaultThreadViewLimit
	}
	env = env.normalized()
	leaderThreadID = normalizeThreadViewID(leaderThreadID)
	store := &threadViewStore{
		limit:          limit,
		leaderThreadID: leaderThreadID,
		activeThreadID: leaderThreadID,
		styles:         env.styles,
		environment:    env,
		views:          make(map[string]*threadViewState),
	}
	view := store.newView(leaderThreadID, engine.ThreadModeLiveAttach)
	store.views[leaderThreadID] = view
	return store
}

func normalizeThreadViewID(threadID string) string {
	if threadID = strings.TrimSpace(threadID); threadID != "" {
		return threadID
	}
	return fallbackLeaderThreadID
}

func normalizeThreadAttachmentMode(mode engine.ThreadAttachmentMode) (engine.ThreadAttachmentMode, error) {
	switch mode {
	case "", engine.ThreadModeLiveAttach:
		return engine.ThreadModeLiveAttach, nil
	case engine.ThreadModeReplayOnly, engine.ThreadModeEvictedTranscript:
		return mode, nil
	default:
		return "", fmt.Errorf("thread view: unsupported attachment mode %q", mode)
	}
}

func (s *threadViewStore) newView(threadID string, mode engine.ThreadAttachmentMode) *threadViewState {
	s.nextUse++
	return &threadViewState{
		ThreadID: threadID,
		Mode:     mode,
		Chat:     newChatViewWithRenderEnvironment(s.environment),
		Search: func() *SearchOverlay {
			search := NewSearchOverlay(s.styles)
			search.SetRenderEnvironment(s.environment)
			return search
		}(),
		Selection: &Selection{},
		Editor: threadEditorState{
			HistoryIndex: -1,
			CommandHint:  -1,
			FileHint:     -1,
		},
		DetailTab: agentDetailOverview,
		Surface:   StateChat,
		lastUsed:  s.nextUse,
	}
}

// SetStyles updates both existing thread views and the constructor source used
// by future views.
func (s *threadViewStore) SetStyles(styles Styles) {
	if s == nil {
		return
	}
	s.SetRenderEnvironment(s.environment.withStyles(styles))
}

func (s *threadViewStore) SetRenderEnvironment(env RenderEnvironment) {
	if s == nil {
		return
	}
	s.environment = env.normalized()
	s.styles = s.environment.styles
	for _, view := range s.views {
		if view.Chat != nil {
			view.Chat.SetRenderEnvironment(s.environment)
		}
		if view.Search != nil {
			view.Search.SetRenderEnvironment(s.environment)
		}
	}
}

func (s *threadViewStore) active() *threadViewState {
	if s == nil {
		return nil
	}
	return s.views[s.activeThreadID]
}

func (s *threadViewStore) activate(threadID string, mode engine.ThreadAttachmentMode) (*threadViewState, error) {
	if s == nil {
		return nil, fmt.Errorf("thread view: nil store")
	}
	threadID = normalizeThreadViewID(threadID)
	mode, err := normalizeThreadAttachmentMode(mode)
	if err != nil {
		return nil, err
	}
	view := s.views[threadID]
	if view == nil {
		if err := s.ensureCapacity(); err != nil {
			return nil, err
		}
		view = s.newView(threadID, mode)
		s.views[threadID] = view
	} else {
		view.Mode = mode
	}
	s.activeThreadID = threadID
	s.touch(view)
	return view, nil
}

func (s *threadViewStore) touch(view *threadViewState) {
	if s == nil || view == nil {
		return
	}
	s.nextUse++
	view.lastUsed = s.nextUse
}

func (s *threadViewStore) ensureCapacity() error {
	if len(s.views) < s.limit {
		return nil
	}
	var candidate *threadViewState
	for _, view := range s.views {
		if view.ThreadID == s.activeThreadID || view.ThreadID == s.leaderThreadID || !view.cleanReplayView() {
			continue
		}
		if candidate == nil || view.lastUsed < candidate.lastUsed ||
			(view.lastUsed == candidate.lastUsed && view.ThreadID < candidate.ThreadID) {
			candidate = view
		}
	}
	if candidate == nil {
		return fmt.Errorf("thread view: capacity %d reached with no clean replay view to evict", s.limit)
	}
	delete(s.views, candidate.ThreadID)
	return nil
}

func (v *threadViewState) cleanReplayView() bool {
	if v == nil || v.Mode == engine.ThreadModeLiveAttach || strings.TrimSpace(v.Editor.Draft) != "" ||
		len(v.ComposerElements) > 0 || len(v.QueuePreview) > 0 {
		return false
	}
	if v.Selection != nil && (v.Selection.HasSelection() || v.Selection.HasExpandSelection() || v.Selection.IsDragging()) {
		return false
	}
	return v.Search == nil || (!v.Search.Visible() && strings.TrimSpace(v.Search.Query()) == "")
}

func (s *threadViewStore) rebindLeader(threadID string) {
	if s == nil {
		return
	}
	threadID = normalizeThreadViewID(threadID)
	oldID := s.leaderThreadID
	if oldID == threadID {
		return
	}
	oldView := s.views[oldID]
	if oldView != nil {
		if _, exists := s.views[threadID]; !exists || s.activeThreadID == oldID {
			delete(s.views, oldID)
			oldView.ThreadID = threadID
			oldView.Mode = engine.ThreadModeLiveAttach
			s.views[threadID] = oldView
		} else {
			delete(s.views, oldID)
		}
	}
	if s.activeThreadID == oldID {
		s.activeThreadID = threadID
	}
	s.leaderThreadID = threadID
}

func (a *App) initializeThreadViews() {
	if a == nil {
		return
	}
	leaderThreadID := fallbackLeaderThreadID
	if a.engine != nil {
		leaderThreadID = normalizeThreadViewID(a.engine.ThreadID())
	}
	a.threadViews = newThreadViewStoreWithRenderEnvironment(defaultThreadViewLimit, leaderThreadID, a.renderEnvironment)
	a.captureActiveThreadView()
}

func (a *App) captureActiveThreadView() {
	if a == nil || a.threadViews == nil {
		return
	}
	view := a.threadViews.active()
	if view == nil {
		return
	}
	view.Chat = a.chat
	view.Search = a.search
	view.Selection = a.selection
	view.Editor = a.captureThreadEditorState()
	view.ComposerElements = cloneThreadComposerElements(a.composerElements)
	view.QueuePreview = cloneThreadQueuedInputs(a.queuedInputPreview)
	view.DetailTab = a.threadDetailTab
	if a.state == StateSearch {
		view.Surface = StateSearch
	} else {
		view.Surface = StateChat
	}
	a.threadViews.touch(view)
}

func (a *App) switchThreadView(threadID string, mode engine.ThreadAttachmentMode) error {
	if a == nil || a.threadViews == nil {
		return fmt.Errorf("thread view: app is not initialized")
	}
	if a.composerAdmissionPending != nil {
		return fmt.Errorf("thread view: wait for the composer submission to finish")
	}
	if _, err := normalizeThreadAttachmentMode(mode); err != nil {
		return err
	}
	previousThreadID := a.activeThreadViewID()
	a.cancelHistorySearch()
	a.captureActiveThreadView()
	view, err := a.threadViews.activate(threadID, mode)
	if err != nil {
		return err
	}
	a.suspendThreadAttentionPresentation(previousThreadID, view.ThreadID)
	a.restoreThreadView(view)
	a.markComposerChanged()
	a.gcDraftMedia()
	return nil
}

func (a *App) restoreThreadView(view *threadViewState) {
	if a == nil || view == nil {
		return
	}
	if view.Chat == nil {
		view.Chat = newChatViewWithRenderEnvironment(a.renderEnvironment)
	}
	view.Chat.SetRenderEnvironment(a.renderEnvironment)
	if view.Search == nil {
		view.Search = NewSearchOverlay(a.styles)
	}
	view.Search.SetRenderEnvironment(a.renderEnvironment)
	if view.Selection == nil {
		view.Selection = &Selection{}
	}
	a.chat = view.Chat
	a.search = view.Search
	a.selection = view.Selection
	a.composerElements = cloneThreadComposerElements(view.ComposerElements)
	a.queuedInputPreview = cloneThreadQueuedInputs(view.QueuePreview)
	a.threadDetailTab = view.DetailTab
	a.restoreThreadEditorState(view.Editor)
	a.chat.SetSpinnerCount(a.spinnerCount)
	a.chat.SetSize(a.layout.width, a.layout.chatHeight)
	if view.Surface == StateSearch && a.search.Visible() {
		a.state = StateSearch
	} else {
		a.state = StateChat
	}
	a.updateLayout()
}

func (a *App) captureThreadEditorState() threadEditorState {
	lineInfo := a.textarea.LineInfo()
	return threadEditorState{
		Draft:                a.textarea.Value(),
		CursorLine:           a.textarea.Line(),
		CursorColumn:         lineInfo.StartColumn + lineInfo.ColumnOffset,
		InputMode:            a.inputMode,
		HistoryIndex:         a.historyIdx,
		HistoryDraft:         a.draft,
		HistoryDraftElements: cloneThreadComposerElements(a.draftElements),
		Undo:                 cloneComposerUndoEntries(a.composerUndo),
		CommandHint:          a.commandHintIdx,
		FileHint:             a.fileHintIdx,
		MentionHint:          a.mentionHintIdx,
	}
}

func (a *App) restoreThreadEditorState(editor threadEditorState) {
	a.textarea.SetValue(editor.Draft)
	maxMoves := len([]rune(editor.Draft)) + a.textarea.LineCount() + 1
	for moves := 0; a.textarea.Line() > editor.CursorLine && moves < maxMoves; moves++ {
		a.textarea.CursorUp()
	}
	for moves := 0; a.textarea.Line() < editor.CursorLine && moves < maxMoves; moves++ {
		a.textarea.CursorDown()
	}
	a.textarea.SetCursorColumn(editor.CursorColumn)
	a.inputMode = editor.InputMode
	a.historyIdx = editor.HistoryIndex
	if a.historyIdx < 0 {
		a.historyIdx = 0
	}
	if a.historyIdx > len(a.history) {
		a.historyIdx = len(a.history)
	}
	a.draft = editor.HistoryDraft
	a.draftElements = cloneThreadComposerElements(editor.HistoryDraftElements)
	a.composerUndo = cloneComposerUndoEntries(editor.Undo)
	a.commandHintIdx = -1
	a.fileHintIdx = -1
	a.commandHints = nil
	a.fileHints = nil
	a.setEditorPrompt()
	if a.inputMode == InputCommand {
		a.updateCommandHints()
		a.commandHintIdx = clampThreadViewIndex(editor.CommandHint, len(a.commandHints))
		a.fileHintIdx = clampThreadViewIndex(editor.FileHint, len(a.fileHints))
	} else {
		a.updateMentionHints()
		a.mentionHintIdx = clampThreadViewIndex(editor.MentionHint, len(a.mentionHints))
	}
}

func clampThreadViewIndex(index, length int) int {
	if index < 0 || length <= 0 {
		return -1
	}
	if index >= length {
		return length - 1
	}
	return index
}

func (a *App) rebindLeaderThreadView(threadID string) {
	if a == nil || a.threadViews == nil || strings.TrimSpace(threadID) == "" {
		return
	}
	a.captureActiveThreadView()
	a.threadViews.rebindLeader(threadID)
}

func (a *App) activeThreadViewID() string {
	if a == nil || a.threadViews == nil {
		return ""
	}
	return a.threadViews.activeThreadID
}

func (a *App) activeThreadViewMode() engine.ThreadAttachmentMode {
	if a == nil || a.threadViews == nil || a.threadViews.active() == nil {
		return engine.ThreadModeLiveAttach
	}
	return a.threadViews.active().Mode
}

func cloneThreadComposerElements(elements []threadComposerElement) []threadComposerElement {
	if len(elements) > maxThreadComposerElements {
		elements = elements[len(elements)-maxThreadComposerElements:]
	}
	cloned := append([]threadComposerElement(nil), elements...)
	for i := range cloned {
		cloned[i].ID = truncateThreadViewText(cloned[i].ID, maxThreadElementValueRunes)
		cloned[i].Kind = truncateThreadViewText(cloned[i].Kind, maxThreadElementValueRunes)
		cloned[i].Label = truncateThreadViewText(cloned[i].Label, maxThreadElementValueRunes)
		cloned[i].Name = truncateThreadViewText(cloned[i].Name, maxThreadElementValueRunes)
		cloned[i].MIMEType = truncateThreadViewText(cloned[i].MIMEType, maxThreadElementValueRunes)
		if cloned[i].Kind == composerElementKindImage {
			cloned[i].Value = ""
			cloned[i].Data = ""
		}
	}
	return cloned
}

func cloneThreadQueuedInputs(queued []threadQueuedInput) []threadQueuedInput {
	if len(queued) > maxThreadQueuedInputPreviews {
		queued = queued[len(queued)-maxThreadQueuedInputPreviews:]
	}
	cloned := append([]threadQueuedInput(nil), queued...)
	for i := range cloned {
		cloned[i].ID = truncateThreadViewText(cloned[i].ID, maxThreadElementValueRunes)
		cloned[i].Content = truncateThreadViewText(cloned[i].Content, maxThreadQueuedInputRunes)
		cloned[i].Parts = cloneQueuedPromptParts(cloned[i].Parts)
	}
	return cloned
}

func cloneQueuedPromptParts(parts []engine.QueuedPromptPart) []engine.QueuedPromptPart {
	cloned := append([]engine.QueuedPromptPart(nil), parts...)
	for i := range cloned {
		cloned[i].Text = truncateThreadViewText(cloned[i].Text, maxThreadQueuedInputRunes)
		if cloned[i].Image != nil {
			descriptor := *cloned[i].Image
			cloned[i].Image = &descriptor
		}
	}
	return cloned
}

func truncateThreadViewText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
