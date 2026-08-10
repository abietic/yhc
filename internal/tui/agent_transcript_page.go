package tui

import (
	"fmt"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

const agentTranscriptPageLimit = 32

type agentTranscriptSurface string

const (
	agentTranscriptSurfaceThread       agentTranscriptSurface = "thread"
	agentTranscriptSurfaceBackground   agentTranscriptSurface = "background_tasks"
	agentTranscriptSurfaceTeams        agentTranscriptSurface = "teams"
	agentTranscriptSurfaceTaskExplorer agentTranscriptSurface = "task_explorer"
)

type agentTranscriptSelection struct {
	AgentID    string
	ThreadID   string
	Generation int64
	Mode       engine.ThreadAttachmentMode
}

func agentTranscriptSelectionFromExecution(
	execution engine.TaskExplorerExecution,
	mode engine.ThreadAttachmentMode,
) agentTranscriptSelection {
	if mode == "" {
		mode = engine.ThreadModeLiveAttach
	}
	return agentTranscriptSelection{
		AgentID:    execution.Key.AgentID,
		ThreadID:   execution.ThreadID,
		Generation: execution.Key.Generation,
		Mode:       mode,
	}
}

func agentDetailSnapshotFromExecution(
	execution engine.TaskExplorerExecution,
	revision uint64,
	mode engine.ThreadAttachmentMode,
) engine.AgentDetailSnapshot {
	storage := "runtime"
	if mode == engine.ThreadModeReplayOnly || mode == engine.ThreadModeEvictedTranscript {
		storage = "durable"
	}
	return engine.AgentDetailSnapshot{
		Revision: revision,
		Storage:  storage,
		Agent: engine.RuntimeAgentSnapshot{
			AgentID:         execution.Key.AgentID,
			SessionID:       execution.SessionID,
			ThreadID:        execution.ThreadID,
			ParentSessionID: execution.ParentSessionID,
			ParentThreadID:  execution.ParentThreadID,
			ParentAgentID:   execution.ParentAgentID,
			ParentToolUseID: execution.ParentToolUseID,
			Name:            execution.Name,
			Task:            execution.Task,
			TranscriptPath:  execution.TranscriptPath,
			Description:     execution.Description,
			Status:          execution.Status,
			Generation:      execution.Key.Generation,
			Progress: engine.RuntimeAgentProgressSnapshot{
				Summary: execution.Activity,
			},
		},
	}
}

func mergeAgentDetailProjection(current, summary engine.AgentDetailSnapshot) engine.AgentDetailSnapshot {
	if current.Agent.AgentID != summary.Agent.AgentID ||
		(current.Agent.Generation > 0 && current.Agent.Generation != summary.Agent.Generation) {
		return summary
	}
	summary.Thread = current.Thread
	summary.Output = current.Output
	summary.OutputTruncated = current.OutputTruncated
	summary.PendingMessageCount = current.PendingMessageCount
	summary.UnresolvedCount = current.UnresolvedCount
	summary.SteeringState = current.SteeringState
	summary.TotalPausedMS = current.TotalPausedMS
	if current.LoadError != "" {
		summary.LoadError = current.LoadError
	}
	return summary
}

func (s agentTranscriptSelection) valid() bool {
	return strings.TrimSpace(s.AgentID) != "" && strings.TrimSpace(s.ThreadID) != "" && s.Generation > 0
}

func (s agentTranscriptSelection) writable() bool {
	return s.Mode == "" || s.Mode == engine.ThreadModeLiveAttach
}

type agentTranscriptPageRequestMsg struct {
	surface                agentTranscriptSurface
	selection              agentTranscriptSelection
	requestGeneration      uint64
	cursor                 string
	tab                    taskExplorerDetailTab
	taskExplorerNavigation *taskExplorerNavigationIntent
}

type agentTranscriptPageLoadedMsg struct {
	request agentTranscriptPageRequestMsg
	page    engine.AgentTranscriptPage
	found   bool
	err     error
}

type agentTranscriptPageProvider func(engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error)

type agentTranscriptPager struct {
	selection              agentTranscriptSelection
	taskExplorerNavigation *taskExplorerNavigationIntent
	requestGeneration      uint64
	loading                bool
	initialized            bool
	requestedCursor        string
	nextCursor             string
	hasMore                bool
	messages               []engine.AgentTranscriptMessage
	revision               uint64
	storage                string
	err                    string
}

func (p *agentTranscriptPager) bind(selection agentTranscriptSelection) bool {
	if p.selection == selection {
		return false
	}
	p.requestGeneration++
	p.selection = selection
	p.taskExplorerNavigation = nil
	p.loading = false
	p.initialized = false
	p.requestedCursor = ""
	p.nextCursor = ""
	p.hasMore = false
	p.messages = nil
	p.revision = 0
	p.storage = ""
	p.err = ""
	return true
}

func (p *agentTranscriptPager) reset() {
	p.requestGeneration++
	p.selection = agentTranscriptSelection{}
	p.taskExplorerNavigation = nil
	p.loading = false
	p.initialized = false
	p.requestedCursor = ""
	p.nextCursor = ""
	p.hasMore = false
	p.messages = nil
	p.revision = 0
	p.storage = ""
	p.err = ""
}

func (p *agentTranscriptPager) invalidate() {
	if !p.loading {
		return
	}
	p.requestGeneration++
	p.loading = false
	p.requestedCursor = ""
}

func (p *agentTranscriptPager) bindTaskExplorerNavigation(
	intent *taskExplorerNavigationIntent,
) {
	p.taskExplorerNavigation = nil
	if intent == nil {
		return
	}
	copy := *intent
	p.taskExplorerNavigation = &copy
}

func (p *agentTranscriptPager) begin(surface agentTranscriptSurface, force bool) (agentTranscriptPageRequestMsg, bool) {
	if !p.selection.valid() || p.loading || (!force && p.initialized) {
		return agentTranscriptPageRequestMsg{}, false
	}
	if force {
		p.requestGeneration++
		p.initialized = false
		p.nextCursor = ""
		p.hasMore = false
		p.messages = nil
		p.err = ""
	}
	return p.request(surface, "")
}

func (p *agentTranscriptPager) older(surface agentTranscriptSurface) (agentTranscriptPageRequestMsg, bool) {
	if !p.selection.valid() || p.loading || !p.initialized || !p.hasMore || strings.TrimSpace(p.nextCursor) == "" {
		return agentTranscriptPageRequestMsg{}, false
	}
	return p.request(surface, p.nextCursor)
}

func (p *agentTranscriptPager) request(surface agentTranscriptSurface, cursor string) (agentTranscriptPageRequestMsg, bool) {
	p.requestGeneration++
	p.loading = true
	p.requestedCursor = cursor
	request := agentTranscriptPageRequestMsg{
		surface:           surface,
		selection:         p.selection,
		requestGeneration: p.requestGeneration,
		cursor:            cursor,
	}
	if p.taskExplorerNavigation != nil {
		intent := *p.taskExplorerNavigation
		request.taskExplorerNavigation = &intent
	}
	return request, true
}

func (p *agentTranscriptPager) apply(msg agentTranscriptPageLoadedMsg) bool {
	request := msg.request
	if request.selection != p.selection || request.requestGeneration != p.requestGeneration ||
		request.cursor != p.requestedCursor || !p.loading {
		return false
	}
	p.loading = false
	p.initialized = true
	if msg.err != nil {
		p.err = msg.err.Error()
		return true
	}
	if !msg.found {
		p.err = "Agent transcript is no longer available"
		return true
	}
	page := msg.page
	if page.AgentID != p.selection.AgentID || page.ThreadID != p.selection.ThreadID ||
		page.Generation != p.selection.Generation {
		p.err = "Agent transcript selection changed while loading"
		return true
	}
	if request.cursor == "" {
		merged, err := prependAgentTranscriptMessages(page.Messages, nil)
		if err != nil {
			p.err = err.Error()
			return true
		}
		p.messages = merged
	} else {
		merged, err := prependAgentTranscriptMessages(page.Messages, p.messages)
		if err != nil {
			p.err = err.Error()
			return true
		}
		p.messages = merged
	}
	p.selection.Mode = page.AttachMode
	p.nextCursor = page.NextCursor
	p.hasMore = page.HasMore
	p.revision = page.Revision
	p.storage = page.Storage
	p.err = ""
	return true
}

func (p *agentTranscriptPager) discard(msg agentTranscriptPageLoadedMsg) {
	request := msg.request
	if request.selection != p.selection || request.requestGeneration != p.requestGeneration ||
		request.cursor != p.requestedCursor || !p.loading {
		return
	}
	p.loading = false
}

func agentTranscriptPageCmd(provider agentTranscriptPageProvider, request agentTranscriptPageRequestMsg) tea.Cmd {
	if provider == nil || !request.selection.valid() {
		return nil
	}
	return func() tea.Msg {
		page, found, err := provider(engine.AgentTranscriptPageRequest{
			AgentID:    request.selection.AgentID,
			Generation: request.selection.Generation,
			Cursor:     request.cursor,
			Limit:      agentTranscriptPageLimit,
		})
		return agentTranscriptPageLoadedMsg{request: request, page: page, found: found, err: err}
	}
}

func cloneAgentTranscriptMessages(messages []engine.AgentTranscriptMessage) []engine.AgentTranscriptMessage {
	cloned := append([]engine.AgentTranscriptMessage(nil), messages...)
	for i := range cloned {
		cloned[i].ToolCalls = append([]engine.RuntimeToolCallSnapshot(nil), cloned[i].ToolCalls...)
	}
	return cloned
}

func prependAgentTranscriptMessages(older, current []engine.AgentTranscriptMessage) ([]engine.AgentTranscriptMessage, error) {
	seen := make(map[string]engine.AgentTranscriptMessage, len(older)+len(current))
	merged := make([]engine.AgentTranscriptMessage, 0, len(older)+len(current))
	appendUnique := func(message engine.AgentTranscriptMessage) error {
		identity := agentTranscriptMessageIdentity(message)
		if identity == "" {
			return fmt.Errorf("agent transcript row has no stable identity")
		}
		if existing, ok := seen[identity]; ok {
			if !reflect.DeepEqual(existing, message) {
				return fmt.Errorf("agent transcript identity %q has conflicting rows", identity)
			}
			return nil
		}
		seen[identity] = message
		merged = append(merged, message)
		return nil
	}
	for _, message := range older {
		if err := appendUnique(message); err != nil {
			return nil, err
		}
	}
	for _, message := range current {
		if err := appendUnique(message); err != nil {
			return nil, err
		}
	}
	return cloneAgentTranscriptMessages(merged), nil
}

func agentTranscriptMessageIdentity(message engine.AgentTranscriptMessage) string {
	if identity := strings.TrimSpace(message.TranscriptEntryID); identity != "" {
		return identity
	}
	return strings.TrimSpace(message.ID)
}

func buildAgentTranscriptPageLinesWithProfile(
	profile DisplayCellProfile,
	pager agentTranscriptPager,
	width int,
) []string {
	width = max(12, width)
	lines := make([]string, 0, len(pager.messages)*3+3)
	if pager.loading && len(pager.messages) == 0 {
		lines = append(lines, "Loading transcript...")
	}
	for i, message := range pager.messages {
		lines = append(lines, agentTranscriptMessageLinesWithProfile(
			profile,
			message,
			width,
			false,
		)...)
		if i < len(pager.messages)-1 {
			lines = append(lines, "")
		}
	}
	if pager.err != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Read warning: "+pager.err)
	}
	if pager.hasMore {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "[more history available: press PgUp at the top]")
	}
	if len(lines) == 0 {
		return []string{"(no messages yet)"}
	}
	return lines
}

type agentTranscriptHistoryItem struct {
	id      string
	message engine.AgentTranscriptMessage
}

func newAgentTranscriptHistoryItem(message engine.AgentTranscriptMessage) *agentTranscriptHistoryItem {
	return &agentTranscriptHistoryItem{id: "agent-transcript:" + agentTranscriptMessageIdentity(message), message: message}
}

func (i *agentTranscriptHistoryItem) ID() string          { return i.id }
func (i *agentTranscriptHistoryItem) Version() uint64     { return 1 }
func (i *agentTranscriptHistoryItem) Finished() bool      { return true }
func (i *agentTranscriptHistoryItem) Selectable() bool    { return true }
func (i *agentTranscriptHistoryItem) NoSelectPrefix() int { return 0 }

func (i *agentTranscriptHistoryItem) Render(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	lines := agentTranscriptMessageLinesWithProfile(
		ctx.Environment.profile,
		i.message,
		ctx.Width,
		true,
	)
	if len(lines) == 0 {
		return ""
	}
	lines[0] = ctx.Styles.Subtle.Render(lines[0])
	return strings.Join(lines, "\n")
}

func (i *agentTranscriptHistoryItem) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	message := i.message
	values := []string{
		message.Role,
		message.ToolName,
		message.ReasoningContent,
		message.Content,
	}
	for _, call := range message.ToolCalls {
		values = append(
			values,
			call.Name,
			call.ID,
			call.InputPreview,
		)
	}
	if selectionAnnotationsCollide(values...) {
		return selectionAnnotatedRender{rendered: i.Render(ctx)}
	}

	profile := ctx.displayCellProfile()
	label := strings.ToUpper(firstNonEmptyTUIText(message.Role, "message"))
	if message.ToolName != "" {
		label += " " + message.ToolName
	}
	if !message.Completed {
		label += " (live)"
	}
	parts := []string{modalProjectLine(
		profile,
		selectionSemantic(label),
		ctx.Width,
		0,
	)}
	appendWrapped := func(value string) bool {
		if value == "" {
			return true
		}
		annotated, ok := selectionAnnotatedProfileWrap(
			profile,
			value,
			ctx.Width,
		)
		if !ok {
			return false
		}
		parts = append(parts, annotated)
		return true
	}
	if !appendWrapped(func() string {
		if message.ReasoningContent == "" {
			return ""
		}
		return "Thinking: " + message.ReasoningContent
	}()) || !appendWrapped(message.Content) {
		return selectionAnnotatedRender{rendered: i.Render(ctx)}
	}
	for _, call := range message.ToolCalls {
		callText := "Tool: " + firstNonEmptyTUIText(call.Name, call.ID)
		if call.InputPreview != "" {
			callText += " " + call.InputPreview
		}
		if !appendWrapped(callText) {
			return selectionAnnotatedRender{rendered: i.Render(ctx)}
		}
	}
	if len(parts) == 1 {
		parts = append(parts, modalProjectLine(
			profile,
			selectionSemantic("(empty transcript row)"),
			ctx.Width,
			0,
		))
	}
	rendered := strings.Join(parts, selectionHardBreak())
	lines := strings.Split(rendered, "\n")
	lines[0] = ctx.Styles.Subtle.Render(lines[0])
	return selectionAnnotatedRender{
		rendered: strings.Join(lines, "\n"), annotated: true,
	}
}

func (i *agentTranscriptHistoryItem) Raw(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	return strings.Join(
		agentTranscriptMessageLinesWithProfile(
			ctx.Environment.profile,
			i.message,
			ctx.Width,
			false,
		),
		"\n",
	)
}

func (i *agentTranscriptHistoryItem) Height(ctx HistoryRenderContext) int {
	return historyRenderedHeight(i.Render(ctx))
}

func agentTranscriptMessageLinesWithProfile(
	profile DisplayCellProfile,
	message engine.AgentTranscriptMessage,
	width int,
	includeState bool,
) []string {
	label := strings.ToUpper(firstNonEmptyTUIText(message.Role, "message"))
	if message.ToolName != "" {
		label += " " + message.ToolName
	}
	if includeState && !message.Completed {
		label += " (live)"
	}
	lines := []string{modalProjectLine(profile, label, width, 0)}
	if message.ReasoningContent != "" {
		lines = appendAgentDetailWrappedWithProfile(
			profile,
			lines,
			width,
			"Thinking: "+message.ReasoningContent,
		)
	}
	if message.Content != "" {
		lines = appendAgentDetailWrappedWithProfile(profile, lines, width, message.Content)
	}
	for _, call := range message.ToolCalls {
		callText := "Tool: " + firstNonEmptyTUIText(call.Name, call.ID)
		if call.InputPreview != "" {
			callText += " " + call.InputPreview
		}
		lines = appendAgentDetailWrappedWithProfile(profile, lines, width, callText)
	}
	if len(lines) == 1 {
		lines = append(lines, modalProjectLine(profile, "(empty transcript row)", width, 0))
	}
	return lines
}
