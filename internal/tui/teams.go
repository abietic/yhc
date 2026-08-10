package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

// teamsSubView tracks whether the user is on the member list or viewing detail.
type teamsSubView int

const (
	teamsViewList   teamsSubView = iota
	teamsViewDetail              // expanded detail for a selected member
)

// TeamsPanel is the read-only multi-Agent monitor and transcript peek exposed
// by /team. Runtime mutation controls remain on their existing explicit
// surfaces; this component only inspects canonical selectors and requests an
// existing thread-navigation action.
type TeamsPanel struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	geometry    modalFrameGeometry

	subView teamsSubView

	// List view state
	items  []teamMemberItem // flat list of team members
	cursor int              // currently highlighted item index
	offset int              // scroll offset for list view

	// Detail view state (expanded)
	detailLines      []string
	detailOffset     int
	detailTitle      string // title for the detail view (member name)
	detail           engine.AgentDetailSnapshot
	detailAgent      string
	detailKey        engine.RuntimeExecutionKey
	detailCompatible bool
	detailLoaded     bool
	detailWidth      int
	transcript       agentTranscriptPager
	switchThread     string

	explorerProvider            func() engine.TaskExplorerSnapshot
	detailProvider              func(string) (engine.AgentDetailSnapshot, bool)
	transcriptProvider          agentTranscriptPageProvider
	transcriptSelectionProvider func(string) (agentTranscriptSelection, bool)
	snapshotRevision            uint64
}

// teamMemberItem represents a single team member entry in the panel list.
type teamMemberItem struct {
	id          string
	name        string
	description string // role/description
	status      string
	displayMode string
	threadID    string
	attention   int
	attachMode  engine.ThreadAttachmentMode
	errorText   string
	startedAt   time.Time
	completedAt time.Time

	// Activity info
	lastToolName string
	toolUseCount int
	tokenCount   int
	summary      string
	task         string // current task or last completed task
	execution    engine.TaskExplorerExecution
	compatible   bool
}

// NewTeamsPanel creates a new teams panel.
func NewTeamsPanel(styles Styles) *TeamsPanel {
	panel := &TeamsPanel{styles: styles}
	panel.SetRenderEnvironment(defaultRenderEnvironment(styles))
	return panel
}

func (p *TeamsPanel) SetStyles(styles Styles) {
	p.SetRenderEnvironment(p.environment.withStyles(styles))
}

func (p *TeamsPanel) SetRenderEnvironment(env RenderEnvironment) {
	p.environment = env.normalized()
	p.styles = p.environment.styles
}

// SetExplorerSnapshotProvider installs the only production list selector.
func (p *TeamsPanel) SetExplorerSnapshotProvider(
	provider func() engine.TaskExplorerSnapshot,
) {
	p.explorerProvider = provider
}

// SetDetailProvider installs the same engine-owned detail reader as Ctrl+B.
func (p *TeamsPanel) SetDetailProvider(provider func(string) (engine.AgentDetailSnapshot, bool)) {
	p.detailProvider = provider
}

func (p *TeamsPanel) SetTranscriptProvider(provider agentTranscriptPageProvider) {
	p.transcriptProvider = provider
}

func (p *TeamsPanel) SetTranscriptSelectionProvider(provider func(string) (agentTranscriptSelection, bool)) {
	p.transcriptSelectionProvider = provider
}

// Show makes the panel visible and refreshes team member data.
func (p *TeamsPanel) Show() {
	p.visible = true
	p.subView = teamsViewList
	p.cursor = 0
	p.offset = 0
	p.detailLines = nil
	p.detailOffset = 0
	p.detail = engine.AgentDetailSnapshot{}
	p.detailAgent = ""
	p.detailKey = engine.RuntimeExecutionKey{}
	p.detailCompatible = false
	p.detailLoaded = false
	p.transcript = agentTranscriptPager{}
	p.switchThread = ""
	p.Refresh()
}

// Close hides the panel.
func (p *TeamsPanel) Close() {
	p.visible = false
}

// Visible returns whether the panel is currently shown.
func (p *TeamsPanel) Visible() bool {
	return p.visible
}

// Refresh reloads team member data from the runtime snapshot.
func (p *TeamsPanel) Refresh() {
	var explorer engine.TaskExplorerSnapshot
	if p.explorerProvider != nil {
		explorer = p.explorerProvider()
	}
	selected := taskExplorerSelection{}
	previousIndex := p.cursor
	if p.cursor >= 0 && p.cursor < len(p.items) {
		selected = p.items[p.cursor].selection()
	}
	p.snapshotRevision = explorer.Revision.Runtime
	p.items = p.buildItems(explorer)

	p.cursor = -1
	for index := range p.items {
		if p.items[index].selection() == selected {
			p.cursor = index
			break
		}
	}
	if p.cursor < 0 {
		if len(p.items) == 0 {
			p.cursor = 0
		} else {
			p.cursor = min(max(previousIndex, 0), len(p.items)-1)
		}
	}
	if p.subView == teamsViewDetail && p.detailAgent != "" {
		current, ok := p.currentCompatibility(p.detailKey)
		p.detailCompatible = ok
		if !ok {
			p.detail = explorerOnlyAgentDetail(p.selectedExecution())
			p.detailLoaded = true
			p.rebuildAgentDetailLines()
		} else if p.transcriptProvider == nil {
			if p.cursor >= 0 && p.cursor < len(p.items) {
				p.items[p.cursor].execution = current
			}
			p.refreshAgentDetail(false)
		} else {
			p.refreshAgentSummary()
		}
	}
}

// HandleKey processes key events for the teams panel.
// Returns true if the panel was dismissed (should return to StateChat).
func (p *TeamsPanel) HandleKey(msg tea.KeyPressMsg, viewHeight int) (dismissed bool) {
	dismissed, _ = p.HandleKeyWithCmd(msg, viewHeight)
	return dismissed
}

// HandleKeyWithCmd preserves text-input cursor commands for the App event loop.
func (p *TeamsPanel) HandleKeyWithCmd(msg tea.KeyPressMsg, viewHeight int) (dismissed bool, cmd tea.Cmd) {
	if p.subView == teamsViewDetail {
		cmd = p.handleDetailKey(msg, viewHeight)
		return !p.visible, cmd
	}
	dismissed, cmd = p.handleListKey(msg, viewHeight)
	return dismissed, cmd
}

func (p *TeamsPanel) handleListKey(msg tea.KeyPressMsg, viewHeight int) (bool, tea.Cmd) {
	maxVisible := viewHeight - 10 // room for chrome
	if maxVisible < 3 {
		maxVisible = 3
	}

	switch msg.String() {
	case "esc", "q":
		p.Close()
		return true, nil

	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
			p.adjustListScroll(maxVisible)
		}
		return false, nil

	case "down", "j":
		if p.cursor < len(p.items)-1 {
			p.cursor++
			p.adjustListScroll(maxVisible)
		}
		return false, nil

	case "enter":
		if len(p.items) > 0 && p.cursor < len(p.items) {
			return p.selectCurrentThread()
		}
		return false, nil

	case "tab", "ctrl+o":
		if len(p.items) > 0 && p.cursor < len(p.items) {
			return false, p.enterDetailView()
		}
		return false, nil

	case "r":
		p.Refresh()
		return false, nil

	case "pgup":
		p.cursor -= maxVisible
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.adjustListScroll(maxVisible)
		return false, nil

	case "pgdown":
		p.cursor += maxVisible
		if p.cursor >= len(p.items) {
			p.cursor = len(p.items) - 1
		}
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.adjustListScroll(maxVisible)
		return false, nil

	case "home", "g":
		p.cursor = 0
		p.offset = 0
		return false, nil

	case "end", "G":
		p.cursor = len(p.items) - 1
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.adjustListScroll(maxVisible)
		return false, nil
	}

	return false, nil
}

func (p *TeamsPanel) handleDetailKey(msg tea.KeyPressMsg, viewHeight int) tea.Cmd {
	maxLines := viewHeight - 8
	if maxLines < 3 {
		maxLines = 3
	}
	maxOffset := len(p.detailLines) - maxLines
	if maxOffset < 0 {
		maxOffset = 0
	}

	switch msg.String() {
	case "esc", "q":
		p.subView = teamsViewList
		p.detailLines = nil
		p.detailOffset = 0
		p.detailAgent = ""
		p.detailKey = engine.RuntimeExecutionKey{}
		p.detailCompatible = false
		p.detailLoaded = false
		p.transcript = agentTranscriptPager{}
		return nil

	case "enter":
		_, cmd := p.selectCurrentThread()
		return cmd

	case "r":
		p.refreshAgentDetail(true)
		return p.requestTranscript(true)

	case "up", "k":
		p.detailOffset--
		if p.detailOffset < 0 {
			p.detailOffset = 0
		}
		if p.detailOffset == 0 {
			return p.requestOlderTranscript()
		}
		return nil

	case "down", "j":
		p.detailOffset++
		if p.detailOffset > maxOffset {
			p.detailOffset = maxOffset
		}
		return nil

	case "pgup":
		p.detailOffset -= maxLines
		if p.detailOffset < 0 {
			p.detailOffset = 0
		}
		if p.detailOffset == 0 {
			return p.requestOlderTranscript()
		}
		return nil

	case "pgdown":
		p.detailOffset += maxLines
		if p.detailOffset > maxOffset {
			p.detailOffset = maxOffset
		}
		return nil

	case "home", "g":
		p.detailOffset = 0
		return p.requestOlderTranscript()

	case "end", "G":
		p.detailOffset = maxOffset
		return nil
	}
	return nil
}

func (p *TeamsPanel) adjustListScroll(maxVisible int) {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+maxVisible {
		p.offset = p.cursor - maxVisible + 1
	}
}

func (p *TeamsPanel) enterDetailView() tea.Cmd {
	p.Refresh()
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return nil
	}
	item := p.items[p.cursor]
	p.subView = teamsViewDetail
	p.detailOffset = 0
	p.detailTitle = item.name
	if p.detailTitle == "" {
		p.detailTitle = fmt.Sprintf("Member %s", item.id)
	}

	p.detailAgent = item.id
	p.detailKey = item.execution.Key
	p.detailLoaded = false
	current, compatible := p.currentCompatibility(item.execution.Key)
	p.detailCompatible = compatible
	if !compatible {
		p.detail = explorerOnlyAgentDetail(item.execution)
		p.detailLoaded = true
		p.rebuildAgentDetailLines()
		return nil
	}
	item.execution = current
	selection, ok := p.transcriptSelection(item.execution.Key)
	if !ok {
		selection = agentTranscriptSelectionFromExecution(
			item.execution,
			item.attachMode,
		)
	}
	p.transcript.bind(selection)
	p.detail = agentDetailSnapshotFromExecution(
		item.execution,
		p.snapshotRevision,
		selection.Mode,
	)
	p.detail.UnresolvedCount = item.attention
	if !ok {
		p.detail.LoadError = "Agent transcript selection is unavailable"
	}
	p.detailLoaded = true
	p.refreshAgentDetail(true)
	p.rebuildAgentDetailLines()
	return p.requestTranscript(false)
}

func (p *TeamsPanel) selectCurrentThread() (bool, tea.Cmd) {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return false, nil
	}
	item := p.items[p.cursor]
	if _, ok := p.currentCompatibility(item.execution.Key); !ok {
		return false, nil
	}
	threadID := strings.TrimSpace(item.execution.ThreadID)
	if threadID == "" {
		return false, nil
	}
	p.Close()
	p.switchThread = threadID
	return true, nil
}

func (p *TeamsPanel) takeSwitchThread() string {
	threadID := p.switchThread
	p.switchThread = ""
	return threadID
}

func (p *TeamsPanel) transcriptSelection(
	key engine.RuntimeExecutionKey,
) (agentTranscriptSelection, bool) {
	if p.transcriptSelectionProvider == nil {
		return agentTranscriptSelection{}, false
	}
	selection, ok := p.transcriptSelectionProvider(key.AgentID)
	if !ok ||
		selection.AgentID != key.AgentID ||
		selection.Generation != key.Generation {
		return agentTranscriptSelection{}, false
	}
	return selection, true
}

func (p *TeamsPanel) requestTranscript(force bool) tea.Cmd {
	if _, ok := p.currentCompatibility(p.detailKey); !ok {
		return nil
	}
	request, started := p.transcript.begin(agentTranscriptSurfaceTeams, force)
	if !started {
		return nil
	}
	return agentTranscriptPageCmd(p.transcriptProvider, request)
}

func (p *TeamsPanel) ensureTranscriptPage() tea.Cmd {
	if !p.visible || p.subView != teamsViewDetail || p.detailAgent == "" {
		return nil
	}
	return p.requestTranscript(false)
}

func (p *TeamsPanel) requestOlderTranscript() tea.Cmd {
	if _, ok := p.currentCompatibility(p.detailKey); !ok {
		return nil
	}
	request, started := p.transcript.older(agentTranscriptSurfaceTeams)
	if !started {
		return nil
	}
	return agentTranscriptPageCmd(p.transcriptProvider, request)
}

func (p *TeamsPanel) applyTranscriptPage(msg agentTranscriptPageLoadedMsg) bool {
	if !p.visible || p.subView != teamsViewDetail || p.detailAgent != msg.request.selection.AgentID {
		p.transcript.discard(msg)
		return false
	}
	if _, ok := p.currentCompatibility(p.detailKey); !ok {
		p.transcript.discard(msg)
		return false
	}
	current, ok := p.transcriptSelection(p.detailKey)
	if !ok || current != msg.request.selection {
		p.transcript.bind(current)
		p.rebuildAgentDetailLines()
		return false
	}
	if !p.transcript.apply(msg) {
		return false
	}
	p.detail.Storage = p.transcript.storage
	p.rebuildAgentDetailLines()
	return true
}

func (p *TeamsPanel) refreshAgentSummary() {
	if p.detailAgent == "" || p.cursor < 0 || p.cursor >= len(p.items) {
		return
	}
	item := p.items[p.cursor]
	if item.id != p.detailAgent {
		return
	}
	current, ok := p.currentCompatibility(item.execution.Key)
	p.detailCompatible = ok
	if !ok {
		p.detail = explorerOnlyAgentDetail(item.execution)
		p.detailLoaded = true
		p.rebuildAgentDetailLines()
		return
	}
	item.execution = current
	if selection, ok := p.transcriptSelection(item.execution.Key); ok {
		p.transcript.bind(selection)
	}
	summary := agentDetailSnapshotFromExecution(
		item.execution,
		p.snapshotRevision,
		p.transcript.selection.Mode,
	)
	summary.UnresolvedCount = item.attention
	p.detail = mergeAgentDetailProjection(p.detail, summary)
	p.detailLoaded = true
	p.rebuildAgentDetailLines()
}

func (p *TeamsPanel) refreshAgentDetail(force bool) {
	if p.detailAgent == "" || p.detailProvider == nil {
		return
	}
	if _, ok := p.currentCompatibility(p.detailKey); !ok {
		p.detail = explorerOnlyAgentDetail(p.selectedExecution())
		p.detailCompatible = false
		p.detailLoaded = true
		p.rebuildAgentDetailLines()
		return
	}
	if !force && p.detailLoaded && p.detail.Revision == p.snapshotRevision {
		return
	}
	detail, ok := p.detailProvider(p.detailAgent)
	if !ok ||
		detail.Agent.AgentID != p.detailKey.AgentID ||
		detail.Agent.Generation != p.detailKey.Generation {
		p.detail = engine.AgentDetailSnapshot{Revision: p.snapshotRevision, LoadError: "Agent detail is no longer available"}
	} else {
		p.detail = detail
	}
	p.detailLoaded = true
	p.rebuildAgentDetailLines()
}

func (p *TeamsPanel) rebuildAgentDetailLines() {
	if p.detailAgent == "" {
		return
	}
	width := p.detailWidth
	if width <= 0 {
		width = 68
	}
	if p.transcriptProvider != nil && p.detailCompatible {
		p.detailLines = buildAgentTranscriptPageLinesWithProfile(p.environment.profile, p.transcript, width)
	} else {
		p.detailLines = buildAgentDetailLinesWithProfile(
			p.environment.profile,
			p.detail,
			agentDetailTranscript,
			width,
			time.Now(),
		)
	}
}

// Overlay renders the teams panel on top of the base view.
func (p *TeamsPanel) Overlay(base string, width, height int) string {
	p.geometry = modalFrameGeometry{}
	if !p.visible {
		return base
	}

	dialogWidth := width - 8
	if dialogWidth > 112 {
		dialogWidth = 112
	}
	if dialogWidth < 28 {
		dialogWidth = 28
	}
	if width > 0 && dialogWidth > width {
		dialogWidth = width
	}

	var dialog string
	if p.subView == teamsViewDetail {
		dialog = p.renderDetailView(dialogWidth, height)
	} else {
		dialog = p.renderListView(dialogWidth, height)
	}

	rendered, geometry := modalCenteredOverlay(
		p.environment.profile,
		base,
		dialog,
		width,
		height,
	)
	p.geometry = geometry
	return rendered
}

func (p *TeamsPanel) renderListView(dialogWidth, height int) string {
	maxContentHeight := height - 8
	if maxContentHeight < 3 {
		maxContentHeight = 3
	}
	compact := dialogWidth < 64
	linesPerItem := 2
	if compact {
		linesPerItem = 1
	}
	maxVisible := max(1, maxContentHeight/linesPerItem)
	p.adjustListScroll(maxVisible)

	title := p.styles.DialogTitle.Render("  Multi-Agent monitor")
	if len(p.items) > 0 {
		title += p.styles.Subtle.Render(fmt.Sprintf(" (%d)", len(p.items)))
	}
	parts := []string{title, ""}
	content := make([]string, 0, maxContentHeight)
	if len(p.items) == 0 {
		content = append(content, p.styles.Subtle.Render("  No Agent threads"))
	} else {
		end := min(len(p.items), p.offset+maxVisible)
		content = append(content, p.renderListItems(p.offset, end, dialogWidth, compact)...)
		if len(p.items) > maxVisible && len(content) < maxContentHeight {
			content = append(content, p.styles.Subtle.Render(fmt.Sprintf("  %d-%d of %d", p.offset+1, end, len(p.items))))
		}
	}
	for len(content) < maxContentHeight {
		content = append(content, "")
	}
	if len(content) > maxContentHeight {
		content = content[:maxContentHeight]
	}
	parts = append(parts, content...)
	parts = append(parts, strings.Repeat("\u2500", max(1, dialogWidth-4)))
	help := "  \u2191\u2193 move \u00b7 Tab peek \u00b7 Enter switch \u00b7 r refresh \u00b7 Esc close"
	if compact {
		help = "  Tab peek \u00b7 Enter switch \u00b7 Esc close"
	}
	parts = append(parts, p.styles.DialogHelp.Render(p.truncateText(help, max(1, dialogWidth-4))))

	return contentRenderStyleWidth(
		p.environment.normalized().profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
}

func (p *TeamsPanel) renderDetailView(dialogWidth, height int) string {
	maxContentHeight := height - 11
	if maxContentHeight < 3 {
		maxContentHeight = 3
	}
	p.detailWidth = max(12, dialogWidth-6)
	p.rebuildAgentDetailLines()

	title := p.styles.DialogTitle.Render("  Read-only peek \u00b7 @" + p.truncateText(p.detailTitle, max(1, dialogWidth-25)))
	parts := []string{title}
	if item, ok := p.selectedMonitorItem(); ok {
		parts = append(parts, "  "+p.styles.Subtle.Render(p.truncateText(p.monitorMeta(item, time.Now()), max(1, dialogWidth-6))))
		if activity := p.formatMemberDetail(item); activity != "" {
			parts = append(parts, "  "+p.styles.Subtle.Render(p.truncateText(activity, max(1, dialogWidth-6))))
		} else {
			parts = append(parts, "")
		}
	} else {
		parts = append(parts, "", "")
	}
	parts = append(parts, "")
	content := make([]string, 0, maxContentHeight)
	start := min(max(0, p.detailOffset), len(p.detailLines))
	end := min(len(p.detailLines), start+maxContentHeight)
	for _, line := range p.detailLines[start:end] {
		content = append(content, "  "+p.truncateText(line, max(1, dialogWidth-6)))
	}
	for len(content) < maxContentHeight {
		content = append(content, "")
	}
	parts = append(parts, content...)
	parts = append(parts, strings.Repeat("\u2500", max(1, dialogWidth-4)))
	help := "  \u2191\u2193 scroll \u00b7 PgUp/PgDn history \u00b7 r refresh \u00b7 Enter switch \u00b7 Esc monitor"
	if dialogWidth < 64 {
		help = "  Enter switch \u00b7 Esc monitor"
	}
	parts = append(parts, p.styles.DialogHelp.Render(p.truncateText(help, max(1, dialogWidth-4))))

	return contentRenderStyleWidth(
		p.environment.normalized().profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
}

func (p *TeamsPanel) renderListItems(start, end, dialogWidth int, compact bool) []string {
	var lines []string
	now := time.Now()

	for i := start; i < end; i++ {
		item := p.items[i]
		isCursor := i == p.cursor
		icon := p.statusIcon(item.status)
		name := "@" + item.name
		if name == "@" {
			name = fmt.Sprintf("@%s", item.id)
		}
		meta := p.monitorMeta(item, now)
		firstLine := fmt.Sprintf("  %s %s  %s", icon, name, meta)
		firstLine = p.truncateText(firstLine, max(1, dialogWidth-4))
		if isCursor {
			firstLine = p.styles.Selected.Render(firstLine)
		}
		lines = append(lines, firstLine)
		if !compact {
			detail := firstNonEmptyTUIText(p.formatMemberDetail(item), "(no current activity)")
			lines = append(lines, "    "+p.styles.Subtle.Render(p.truncateText(detail, max(1, dialogWidth-8))))
		}
	}

	return lines
}

func (p *TeamsPanel) statusIcon(status string) string {
	switch status {
	case "running":
		return p.styles.ToolRunning.Render("\u25cf") // filled circle (green)
	case "waiting_input":
		return p.styles.Warning.Render("!")
	case "paused":
		return p.styles.Subtle.Render("\u2016") // pause mark
	case "completed":
		return p.styles.ToolSuccess.Render("\u2713") // checkmark
	case "failed", "aborted", "killed":
		return p.styles.ToolError.Render("\u2717") // X mark
	case "idle", "pending":
		return p.styles.Subtle.Render("\u25cb") // open circle
	default:
		return p.styles.Subtle.Render("\u25cb")
	}
}

func (p *TeamsPanel) formatTimeInfo(item teamMemberItem, now time.Time) string {
	if item.status == "running" || item.status == "waiting_input" || item.status == "paused" {
		if !item.startedAt.IsZero() {
			elapsed := now.Sub(item.startedAt)
			return formatDurationShort(elapsed)
		}
		return ""
	}

	// Terminal state -- show relative completion time
	if !item.completedAt.IsZero() {
		return formatRelativeTime(item.completedAt, now)
	}
	if !item.startedAt.IsZero() {
		return formatRelativeTime(item.startedAt, now)
	}
	return ""
}

func (p *TeamsPanel) formatMemberDetail(item teamMemberItem) string {
	switch {
	case (item.status == "failed" || item.status == "aborted" || item.status == "killed") && item.errorText != "":
		return "outcome: " + item.errorText
	case item.status == "completed" && item.summary != "":
		return "outcome: " + item.summary
	case item.lastToolName != "" && item.summary != "":
		return fmt.Sprintf("current: %s \u00b7 %s", item.lastToolName, item.summary)
	case item.lastToolName != "":
		return fmt.Sprintf("current: %s", item.lastToolName)
	case item.summary != "":
		return "current: " + item.summary
	case item.task != "":
		return item.task
	case item.description != "":
		return item.description
	}
	return ""
}

func (p *TeamsPanel) monitorMeta(item teamMemberItem, now time.Time) string {
	parts := []string{monitorStateText(item)}
	if mode := monitorModeText(item); mode != "" && mode != parts[0] {
		parts = append(parts, mode)
	}
	if item.attention > 0 {
		parts = append(parts, fmt.Sprintf("attention:%d", item.attention))
	}
	if elapsed := p.formatTimeInfo(item, now); elapsed != "" {
		parts = append(parts, elapsed)
	}
	return strings.Join(parts, " \u00b7 ")
}

func (p *TeamsPanel) selectedMonitorItem() (teamMemberItem, bool) {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return teamMemberItem{}, false
	}
	return p.items[p.cursor], true
}

func monitorStateText(item teamMemberItem) string {
	switch item.status {
	case "running":
		if item.displayMode != "" {
			return item.displayMode
		}
		return "running"
	case "waiting_input":
		return "waiting-input"
	case "paused", "completed", "failed":
		return item.status
	case "aborted", "killed":
		return "aborted"
	case "pending", "idle":
		return "idle"
	default:
		return firstNonEmptyTUIText(item.status, "idle")
	}
}

func monitorModeText(item teamMemberItem) string {
	if item.displayMode != "" {
		return item.displayMode
	}
	switch item.attachMode {
	case engine.ThreadModeReplayOnly:
		return "replay"
	case engine.ThreadModeEvictedTranscript:
		return "disk"
	default:
		return ""
	}
}

func (item teamMemberItem) selection() taskExplorerSelection {
	return taskExplorerSelection{
		agentID:    item.execution.Key.AgentID,
		generation: item.execution.Key.Generation,
	}
}

func (p *TeamsPanel) selectedExecution() engine.TaskExplorerExecution {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return engine.TaskExplorerExecution{}
	}
	return p.items[p.cursor].execution
}

func (p *TeamsPanel) currentCompatibility(
	key engine.RuntimeExecutionKey,
) (engine.TaskExplorerExecution, bool) {
	if strings.TrimSpace(key.AgentID) == "" {
		return engine.TaskExplorerExecution{}, false
	}
	if p.explorerProvider == nil {
		return engine.TaskExplorerExecution{}, false
	}
	explorer := p.explorerProvider()
	for _, execution := range explorer.Executions {
		if execution.Key == key {
			return execution, true
		}
	}
	return engine.TaskExplorerExecution{}, false
}

func (p *TeamsPanel) buildItems(
	explorer engine.TaskExplorerSnapshot,
) []teamMemberItem {
	var items []teamMemberItem

	for _, execution := range explorer.Executions {
		attachMode := engine.ThreadModeLiveAttach
		if execution.ReplayOnly ||
			execution.Phase == engine.TaskExplorerExecutionReplayOnly {
			attachMode = engine.ThreadModeReplayOnly
		}
		items = append(items, teamMemberItem{
			id: execution.Key.AgentID,
			name: firstNonEmptyTUIText(
				execution.Name,
				execution.Key.AgentID,
			),
			description: execution.Description,
			status: firstNonEmptyTUIText(
				execution.Status,
				string(execution.Phase),
			),
			displayMode: execution.DisplayMode,
			threadID:    execution.ThreadID,
			attention:   len(execution.Attention),
			attachMode:  attachMode,
			errorText: func() string {
				if execution.Phase == engine.TaskExplorerExecutionFailed {
					return firstNonEmptyTUIText(
						execution.Activity,
						execution.Status,
					)
				}
				return ""
			}(),
			lastToolName: execution.LastToolName,
			toolUseCount: execution.ToolUseCount,
			tokenCount:   execution.TokenCount,
			summary:      execution.Activity,
			task: firstNonEmptyTUIText(
				execution.Task,
				execution.Description,
			),
			execution:  execution,
			compatible: true,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftPriority := monitorItemPriority(items[i])
		rightPriority := monitorItemPriority(items[j])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if items[i].startedAt.Equal(items[j].startedAt) {
			return items[i].id < items[j].id
		}
		return items[i].startedAt.Before(items[j].startedAt)
	})
	return items
}

func monitorItemPriority(item teamMemberItem) int {
	if item.attention > 0 {
		return 0
	}
	switch item.status {
	case "running", "waiting_input", "paused":
		return 1
	default:
		return 2
	}
}

func (p *TeamsPanel) truncateText(text string, maxWidth int) string {
	return modalEllipsize(p.environment.profile, text, maxWidth, 0, "...")
}

// teamsTickMsg is sent periodically to refresh the panel while visible.
type teamsTickMsg struct{}

func teamsTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return teamsTickMsg{}
	})
}
