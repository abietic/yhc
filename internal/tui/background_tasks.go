package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

// bgTaskSubView tracks whether the user is on the list or viewing expanded output.
type bgTaskSubView int

const (
	bgTaskViewList   bgTaskSubView = iota
	bgTaskViewOutput               // expanded output for a selected task
)

// BackgroundTasksPanel is a modal overlay that shows all background tasks
// (running, completed, failed, stopped) with management controls.
// Triggered by Ctrl+B. It uses the same canonical Task Explorer snapshot as
// the Ctrl+T surface.
type BackgroundTasksPanel struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	geometry    modalFrameGeometry

	subView bgTaskSubView

	// List view state
	items  []bgTaskItem // flat list of tasks
	cursor int          // currently highlighted item index
	offset int          // scroll offset for list view

	// Output view state (expanded)
	outputLines  []string
	outputOffset int
	outputTitle  string // title for the output view (task description)
	detail       engine.AgentDetailSnapshot
	detailAgent  string
	detailTab    agentDetailTab
	detailLoaded bool
	detailWidth  int
	control      agentDetailControl
	transcript   agentTranscriptPager

	explorerProvider            func() engine.TaskExplorerSnapshot
	detailProvider              func(string) (engine.AgentDetailSnapshot, bool)
	transcriptProvider          agentTranscriptPageProvider
	transcriptSelectionProvider func(string) (agentTranscriptSelection, bool)
	snapshotRevision            uint64
}

// bgTaskItem represents a single task entry in the panel list.
type bgTaskItem struct {
	// Common fields
	id          string
	kind        string // "agent" or "task"
	description string
	status      string // "running", "completed", "failed", "aborted", "killed", etc.
	startedAt   time.Time
	completedAt time.Time

	// Agent-specific
	lastToolName string
	toolUseCount int
	tokenCount   int
	summary      string

	// Local task-specific
	activeForm string
	output     string
	owner      string
	execution  engine.TaskExplorerExecution
	compatible bool
}

// NewBackgroundTasksPanel creates a new background tasks panel.
func NewBackgroundTasksPanel(styles Styles) *BackgroundTasksPanel {
	panel := &BackgroundTasksPanel{styles: styles, control: newAgentDetailControl()}
	panel.SetRenderEnvironment(defaultRenderEnvironment(styles))
	return panel
}

func (p *BackgroundTasksPanel) SetStyles(styles Styles) {
	p.SetRenderEnvironment(p.environment.withStyles(styles))
}

func (p *BackgroundTasksPanel) SetRenderEnvironment(env RenderEnvironment) {
	p.environment = env.normalized()
	p.styles = p.environment.styles
}

// SetExplorerSnapshotProvider installs the only production list selector.
func (p *BackgroundTasksPanel) SetExplorerSnapshotProvider(
	provider func() engine.TaskExplorerSnapshot,
) {
	p.explorerProvider = provider
}

// SetDetailProvider installs the engine-owned Agent detail reader.
func (p *BackgroundTasksPanel) SetDetailProvider(provider func(string) (engine.AgentDetailSnapshot, bool)) {
	p.detailProvider = provider
}

func (p *BackgroundTasksPanel) SetTranscriptProvider(provider agentTranscriptPageProvider) {
	p.transcriptProvider = provider
}

func (p *BackgroundTasksPanel) SetTranscriptSelectionProvider(provider func(string) (agentTranscriptSelection, bool)) {
	p.transcriptSelectionProvider = provider
}

// SetActionProvider installs the exact-generation explorer dispatcher.
func (p *BackgroundTasksPanel) SetActionProvider(
	action func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult,
) {
	p.control.setActionProvider(action)
}

// Show makes the panel visible and refreshes task data.
func (p *BackgroundTasksPanel) Show() {
	p.visible = true
	p.subView = bgTaskViewList
	p.cursor = 0
	p.offset = 0
	p.outputLines = nil
	p.outputOffset = 0
	p.detail = engine.AgentDetailSnapshot{}
	p.detailAgent = ""
	p.detailTab = agentDetailOverview
	p.detailLoaded = false
	p.transcript = agentTranscriptPager{}
	p.control.reset()
	p.Refresh()
}

// ShowExecution opens one exact current-generation Agent detail directly. Esc
// returns to the background list, preserving Ctrl+B as the broader task entrypoint.
func (p *BackgroundTasksPanel) ShowExecution(
	key engine.RuntimeExecutionKey,
) (bool, tea.Cmd) {
	p.Show()
	for i, item := range p.items {
		if item.kind == "agent" &&
			item.execution.Key == key &&
			item.compatible {
			p.cursor = i
			return true, p.enterOutputView()
		}
	}
	p.Close()
	return false, nil
}

// ShowAgent is retained for standalone compatibility fixtures. Production
// navigation must use ShowExecution so an AgentID cannot cross generations.
func (p *BackgroundTasksPanel) ShowAgent(agentID string) (bool, tea.Cmd) {
	p.Show()
	for _, item := range p.items {
		if item.kind == "agent" && item.id == agentID && item.compatible {
			return p.ShowExecution(item.execution.Key)
		}
	}
	p.Close()
	return false, nil
}

// Close hides the panel.
func (p *BackgroundTasksPanel) Close() {
	p.visible = false
	p.control.reset()
}

// Visible returns whether the panel is currently shown.
func (p *BackgroundTasksPanel) Visible() bool {
	return p.visible
}

// Refresh reloads task data from the runtime snapshot.
func (p *BackgroundTasksPanel) Refresh() {
	var explorer engine.TaskExplorerSnapshot
	if p.explorerProvider != nil {
		explorer = p.explorerProvider()
	}
	selected := p.selectedIdentity()
	previousIndex := p.cursor
	p.snapshotRevision = explorer.Revision.Runtime
	p.items = p.buildItems(explorer)

	p.cursor = -1
	for index := range p.items {
		if p.itemIdentity(p.items[index]) == selected {
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
	if p.subView == bgTaskViewOutput && p.detailAgent != "" {
		if p.transcriptProvider == nil {
			p.refreshAgentDetail(false)
		} else {
			p.refreshAgentSummary()
		}
	}
}

// HandleKey processes key events for the background tasks panel.
// Returns true if the panel was dismissed (should return to StateChat).
func (p *BackgroundTasksPanel) HandleKey(msg tea.KeyPressMsg, viewHeight int) (dismissed bool) {
	dismissed, _ = p.HandleKeyWithCmd(msg, viewHeight)
	return dismissed
}

// HandleKeyWithCmd preserves text-input cursor commands for the App event loop.
func (p *BackgroundTasksPanel) HandleKeyWithCmd(msg tea.KeyPressMsg, viewHeight int) (dismissed bool, cmd tea.Cmd) {
	if p.subView == bgTaskViewOutput && p.detailAgent != "" {
		execution, ok := p.currentCompatibility(p.selectedExecutionKey())
		if !ok {
			p.control.reset()
			p.detail.LoadError = "Execution generation is no longer current"
			p.rebuildAgentDetailLines()
			return false, p.handleOutputKey(msg, viewHeight)
		}
		var snapshot engine.TaskExplorerSnapshot
		if p.explorerProvider != nil {
			snapshot = p.explorerProvider()
		}
		handled, changed, controlCmd := p.control.handleKey(
			execution,
			snapshot,
			p.transcript.selection.writable(),
			msg,
		)
		if handled {
			if changed {
				if p.transcriptProvider == nil {
					p.refreshAgentDetail(true)
				} else {
					p.refreshAgentSummary()
				}
			}
			return false, controlCmd
		}
	}
	if p.subView == bgTaskViewOutput {
		return false, p.handleOutputKey(msg, viewHeight)
	}
	dismissed, cmd = p.handleListKey(msg, viewHeight)
	return dismissed, cmd
}

func (p *BackgroundTasksPanel) handleListKey(msg tea.KeyPressMsg, viewHeight int) (bool, tea.Cmd) {
	maxVisible := viewHeight - 10 // room for chrome
	if maxVisible < 3 {
		maxVisible = 3
	}

	switch msg.String() {
	case "esc", "q", "ctrl+b":
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
			return false, p.enterOutputView()
		}
		return false, nil

	case "s":
		if len(p.items) > 0 && p.cursor < len(p.items) {
			p.stopSelectedTask()
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

func (p *BackgroundTasksPanel) handleOutputKey(msg tea.KeyPressMsg, viewHeight int) tea.Cmd {
	maxLines := viewHeight - 8
	if maxLines < 3 {
		maxLines = 3
	}
	maxOffset := len(p.outputLines) - maxLines
	if maxOffset < 0 {
		maxOffset = 0
	}

	switch msg.String() {
	case "esc", "q":
		p.subView = bgTaskViewList
		p.outputLines = nil
		p.outputOffset = 0
		p.detailAgent = ""
		p.detailLoaded = false
		p.transcript = agentTranscriptPager{}
		p.control.reset()
		return nil

	case "left", "h", "shift+tab":
		if p.detailAgent != "" {
			p.detailTab = p.detailTab.previous()
			p.outputOffset = 0
			if p.transcriptProvider != nil && (p.detailTab == agentDetailOutput || p.detailTab == agentDetailLineage) {
				p.refreshAgentDetail(true)
			}
			p.rebuildAgentDetailLines()
		}
		return nil

	case "right", "l", "tab":
		if p.detailAgent != "" {
			p.detailTab = p.detailTab.next()
			p.outputOffset = 0
			if p.transcriptProvider != nil && (p.detailTab == agentDetailOutput || p.detailTab == agentDetailLineage) {
				p.refreshAgentDetail(true)
			}
			p.rebuildAgentDetailLines()
		}
		return nil

	case "r":
		if p.detailAgent != "" {
			if p.transcriptProvider == nil || p.detailTab == agentDetailOutput || p.detailTab == agentDetailLineage {
				p.refreshAgentDetail(true)
			} else {
				p.refreshAgentSummary()
			}
			return p.requestTranscript(true)
		}
		return nil

	case "up", "k":
		p.outputOffset--
		if p.outputOffset < 0 {
			p.outputOffset = 0
		}
		if p.outputOffset == 0 && p.detailTab == agentDetailTranscript {
			return p.requestOlderTranscript()
		}
		return nil

	case "down", "j":
		p.outputOffset++
		if p.outputOffset > maxOffset {
			p.outputOffset = maxOffset
		}
		return nil

	case "pgup":
		p.outputOffset -= maxLines
		if p.outputOffset < 0 {
			p.outputOffset = 0
		}
		if p.outputOffset == 0 && p.detailTab == agentDetailTranscript {
			return p.requestOlderTranscript()
		}
		return nil

	case "pgdown":
		p.outputOffset += maxLines
		if p.outputOffset > maxOffset {
			p.outputOffset = maxOffset
		}
		return nil

	case "home", "g":
		p.outputOffset = 0
		if p.detailTab == agentDetailTranscript {
			return p.requestOlderTranscript()
		}
		return nil

	case "end", "G":
		p.outputOffset = maxOffset
		return nil
	}
	return nil
}

func (p *BackgroundTasksPanel) adjustListScroll(maxVisible int) {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+maxVisible {
		p.offset = p.cursor - maxVisible + 1
	}
}

func (p *BackgroundTasksPanel) enterOutputView() tea.Cmd {
	p.Refresh()
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return nil
	}
	item := p.items[p.cursor]
	p.control.reset()
	p.subView = bgTaskViewOutput
	p.outputOffset = 0
	p.outputTitle = item.description
	if p.outputTitle == "" {
		p.outputTitle = fmt.Sprintf("Task %s", item.id)
	}

	p.detailAgent = ""
	p.detailLoaded = false
	p.detailTab = agentDetailOverview
	// Get output based on task kind
	var output string
	if item.kind == "agent" {
		if _, ok := p.currentCompatibility(item.execution.Key); !ok {
			p.detail = explorerOnlyAgentDetail(item.execution)
			p.detailLoaded = true
			p.outputLines = buildAgentDetailLinesWithProfile(
				p.environment.profile,
				p.detail,
				agentDetailOverview,
				max(12, p.detailWidth),
				time.Now(),
			)
			return nil
		}
		p.detailAgent = item.id
		if p.transcriptProvider == nil && p.detailProvider != nil {
			p.transcript.bind(agentTranscriptSelectionFromExecution(
				item.execution,
				engine.ThreadModeLiveAttach,
			))
			p.refreshAgentDetail(true)
			return nil
		}
		selection, ok := p.transcriptSelection(item.execution.Key)
		if !ok {
			selection = agentTranscriptSelectionFromExecution(
				item.execution,
				engine.ThreadModeReplayOnly,
			)
		}
		p.transcript.bind(selection)
		p.detail = agentDetailSnapshotFromExecution(
			item.execution,
			p.snapshotRevision,
			selection.Mode,
		)
		if !ok {
			p.detail.LoadError = "Agent transcript selection is unavailable"
		}
		p.detailLoaded = true
		p.rebuildAgentDetailLines()
		return p.requestTranscript(false)
	}
	// Local task — use Output field.
	if item.output != "" {
		output = item.output
	} else {
		output = fmt.Sprintf("Task %s — Status: %s\nNo output available.", item.id, item.status)
	}

	p.outputLines = strings.Split(output, "\n")
	return nil
}

func (p *BackgroundTasksPanel) transcriptSelection(
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

func (p *BackgroundTasksPanel) requestTranscript(force bool) tea.Cmd {
	if _, ok := p.currentCompatibility(p.selectedExecutionKey()); !ok {
		return nil
	}
	request, started := p.transcript.begin(agentTranscriptSurfaceBackground, force)
	if !started {
		return nil
	}
	return agentTranscriptPageCmd(p.transcriptProvider, request)
}

func (p *BackgroundTasksPanel) ensureTranscriptPage() tea.Cmd {
	if !p.visible || p.subView != bgTaskViewOutput || p.detailAgent == "" {
		return nil
	}
	return p.requestTranscript(false)
}

func (p *BackgroundTasksPanel) requestOlderTranscript() tea.Cmd {
	if _, ok := p.currentCompatibility(p.selectedExecutionKey()); !ok {
		return nil
	}
	request, started := p.transcript.older(agentTranscriptSurfaceBackground)
	if !started {
		return nil
	}
	return agentTranscriptPageCmd(p.transcriptProvider, request)
}

func (p *BackgroundTasksPanel) applyTranscriptPage(msg agentTranscriptPageLoadedMsg) bool {
	if !p.visible || p.subView != bgTaskViewOutput || p.detailAgent != msg.request.selection.AgentID {
		p.transcript.discard(msg)
		return false
	}
	if _, ok := p.currentCompatibility(p.selectedExecutionKey()); !ok {
		p.transcript.discard(msg)
		return false
	}
	current, ok := p.transcriptSelection(p.selectedExecutionKey())
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

func (p *BackgroundTasksPanel) refreshAgentSummary() {
	if p.detailAgent == "" || p.cursor < 0 || p.cursor >= len(p.items) {
		return
	}
	item := p.items[p.cursor]
	if item.kind != "agent" || item.id != p.detailAgent {
		return
	}
	current, ok := p.currentCompatibility(item.execution.Key)
	if !ok {
		p.detail = explorerOnlyAgentDetail(item.execution)
		p.detailLoaded = true
		p.control.reset()
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
	p.detail = mergeAgentDetailProjection(p.detail, summary)
	p.detailLoaded = true
	p.rebuildAgentDetailLines()
}

func (p *BackgroundTasksPanel) refreshAgentDetail(force bool) {
	if p.detailAgent == "" || p.detailProvider == nil {
		return
	}
	if _, ok := p.currentCompatibility(p.selectedExecutionKey()); !ok {
		p.detail = engine.AgentDetailSnapshot{
			Revision:  p.snapshotRevision,
			LoadError: "Execution generation is no longer current",
		}
		p.detailLoaded = true
		p.control.reset()
		p.rebuildAgentDetailLines()
		return
	}
	if !force && p.detailLoaded && p.detail.Revision == p.snapshotRevision {
		return
	}
	key := p.selectedExecutionKey()
	detail, ok := p.detailProvider(p.detailAgent)
	if !ok ||
		detail.Agent.AgentID != key.AgentID ||
		detail.Agent.Generation != key.Generation {
		p.detail = engine.AgentDetailSnapshot{Revision: p.snapshotRevision, LoadError: "Agent detail is no longer available"}
	} else {
		p.detail = detail
	}
	p.detailLoaded = true
	p.rebuildAgentDetailLines()
}

func (p *BackgroundTasksPanel) rebuildAgentDetailLines() {
	if p.detailAgent == "" {
		return
	}
	width := p.detailWidth
	if width <= 0 {
		width = 68
	}
	if p.detailTab == agentDetailTranscript && p.transcriptProvider != nil {
		p.outputLines = buildAgentTranscriptPageLinesWithProfile(p.environment.profile, p.transcript, width)
	} else {
		p.outputLines = buildAgentDetailLinesWithProfile(
			p.environment.profile,
			p.detail,
			p.detailTab,
			width,
			time.Now(),
		)
	}
}

func (p *BackgroundTasksPanel) stopSelectedTask() {
	if p.cursor >= len(p.items) {
		return
	}
	item := p.items[p.cursor]

	// Only stop running tasks
	if item.status != "running" {
		return
	}

	if item.kind == "agent" {
		execution, ok := p.currentCompatibility(item.execution.Key)
		if ok && p.explorerProvider != nil {
			_, _ = p.control.submitAction(
				execution,
				p.explorerProvider(),
				engine.TaskExplorerActionCancel,
				"",
			)
		}
	} else {
		return
	}

	// Refresh to show updated state
	p.Refresh()
}

// Overlay renders the background tasks panel on top of the base view.
func (p *BackgroundTasksPanel) Overlay(base string, width, height int) string {
	p.geometry = modalFrameGeometry{}
	if !p.visible {
		return base
	}

	dialogWidth := agentPanelDialogWidth(width)

	var dialog string
	if p.subView == bgTaskViewOutput {
		dialog = p.renderOutputView(dialogWidth, height)
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

func (p *BackgroundTasksPanel) renderListView(dialogWidth, height int) string {
	maxContentHeight := height - 8
	if maxContentHeight < 5 {
		maxContentHeight = 5
	}

	var parts []string

	// Title
	title := p.styles.DialogTitle.Render("  Background Tasks")
	if len(p.items) > 0 {
		title += p.styles.Subtle.Render(fmt.Sprintf(" (%d)", len(p.items)))
	}
	parts = append(parts, title)
	parts = append(parts, "")

	// Content
	if len(p.items) == 0 {
		parts = append(parts, p.styles.Subtle.Render("  No background tasks"))
		parts = append(parts, "")
	} else {
		// Build all item lines
		allLines := p.renderListItems(dialogWidth)

		// Compute scroll window (each item is ~3 lines)
		// Adjust offset based on cursor
		p.adjustListScroll(maxContentHeight)

		end := p.offset + maxContentHeight
		if end > len(allLines) {
			end = len(allLines)
		}
		start := p.offset
		if start > len(allLines) {
			start = len(allLines)
		}

		visible := allLines[start:end]
		parts = append(parts, visible...)

		// Scroll indicator
		if len(allLines) > maxContentHeight {
			pct := 0
			denom := len(allLines) - maxContentHeight
			if denom > 0 {
				pct = (p.offset * 100) / denom
			}
			parts = append(parts, p.styles.Subtle.Render(fmt.Sprintf("  (%d%%)", pct)))
		}
	}

	// Footer
	parts = append(parts, "")
	parts = append(parts, strings.Repeat("\u2500", dialogWidth-4))
	helpText := p.styles.DialogHelp.Render(
		"  \u2191\u2193 navigate \u00b7 Enter view \u00b7 s cancel Agent \u00b7 r refresh \u00b7 Esc close",
	)
	parts = append(parts, helpText)

	return contentRenderStyleWidth(
		p.environment.normalized().profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
}

func (p *BackgroundTasksPanel) renderOutputView(dialogWidth, height int) string {
	maxContentHeight := height - 8
	if p.detailAgent != "" {
		maxContentHeight -= 2
		if p.control.visible() {
			maxContentHeight -= 2
		}
		p.detailWidth = max(12, dialogWidth-6)
		p.rebuildAgentDetailLines()
	}
	if maxContentHeight < 5 {
		maxContentHeight = 5
	}

	var parts []string

	// Title
	title := p.styles.DialogTitle.Render("  " + p.truncateText(p.outputTitle, dialogWidth-8))
	parts = append(parts, title)
	if p.detailAgent != "" {
		parts = append(parts, renderAgentDetailTabsWithProfile(
			p.environment.profile,
			p.styles,
			p.detailTab,
			dialogWidth-4,
		))
	}
	parts = append(parts, "")

	// Output content
	if len(p.outputLines) == 0 {
		parts = append(parts, p.styles.Subtle.Render("  (no output)"))
	} else {
		end := p.outputOffset + maxContentHeight
		if end > len(p.outputLines) {
			end = len(p.outputLines)
		}
		start := p.outputOffset
		if start < 0 {
			start = 0
		}

		for _, line := range p.outputLines[start:end] {
			line = p.truncateText(line, dialogWidth-6)
			parts = append(parts, "  "+line)
		}

		// Scroll indicator
		if len(p.outputLines) > maxContentHeight {
			pct := 0
			denom := len(p.outputLines) - maxContentHeight
			if denom > 0 {
				pct = (p.outputOffset * 100) / denom
			}
			parts = append(parts, p.styles.Subtle.Render(fmt.Sprintf("  (%d%%)", pct)))
		}
	}

	if p.detailAgent != "" {
		if controlView := p.control.viewWithProfile(p.environment.profile, p.styles, dialogWidth-6); controlView != "" {
			parts = append(parts, "", "  "+controlView)
		}
	}

	// Footer
	parts = append(parts, "")
	parts = append(parts, strings.Repeat("\u2500", dialogWidth-4))
	help := "  \u2191\u2193 scroll \u00b7 Esc back to list"
	if p.detailAgent != "" {
		help = agentDetailControlHelpWithProfile(
			p.environment.profile,
			p.detail,
			p.transcript.selection.Mode,
			dialogWidth-4,
		)
	}
	helpText := p.styles.DialogHelp.Render(help)
	parts = append(parts, helpText)

	return contentRenderStyleWidth(
		p.environment.normalized().profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
}

func (p *BackgroundTasksPanel) renderListItems(dialogWidth int) []string {
	var lines []string
	now := time.Now()

	for i, item := range p.items {
		isCursor := i == p.cursor

		// Status icon
		icon := p.statusIcon(item.status)

		// Description
		desc := item.description
		if desc == "" {
			desc = fmt.Sprintf("%s %s", item.kind, item.id)
		}

		// Time info
		timeInfo := p.formatTimeInfo(item, now)

		// First line: icon + description + time
		firstLine := fmt.Sprintf("  %s %s", icon, p.truncateText(desc, dialogWidth-12))
		if timeInfo != "" {
			firstLine += " " + p.styles.Subtle.Render(timeInfo)
		}

		// Apply cursor highlight
		if isCursor {
			firstLine = p.styles.Selected.Render(firstLine)
		}
		lines = append(lines, firstLine)

		// Second line: detail/preview
		detail := p.formatDetail(item)
		if detail != "" {
			detailLine := "    " + p.styles.Subtle.Render(p.truncateText(detail, dialogWidth-8))
			lines = append(lines, detailLine)
		}

		// Blank line between entries
		lines = append(lines, "")
	}

	return lines
}

func (p *BackgroundTasksPanel) statusIcon(status string) string {
	switch status {
	case "running":
		return p.styles.ToolRunning.Render("\u2847") // braille spinner char
	case "paused":
		return p.styles.Subtle.Render("\u2016") // pause mark
	case "completed":
		return p.styles.ToolSuccess.Render("\u2713") // checkmark
	case "failed":
		return p.styles.ToolError.Render("\u2717") // X mark
	case "aborted", "killed":
		return p.styles.Subtle.Render("\u25a0") // filled square (stopped)
	case "pending", "in_progress":
		return p.styles.ToolRunning.Render("\u25cb") // open circle
	default:
		return p.styles.Subtle.Render("\u25cb")
	}
}

func (p *BackgroundTasksPanel) formatTimeInfo(item bgTaskItem, now time.Time) string {
	if item.status == "running" || item.status == "paused" {
		if !item.startedAt.IsZero() {
			elapsed := now.Sub(item.startedAt)
			if item.status == "paused" {
				return fmt.Sprintf("(paused \u00b7 %s)", formatDurationShort(elapsed))
			}
			return fmt.Sprintf("(%s)", formatDurationShort(elapsed))
		}
		return ""
	}

	// Terminal state — show relative completion time
	if !item.completedAt.IsZero() {
		return fmt.Sprintf("(%s)", formatRelativeTime(item.completedAt, now))
	}
	if !item.startedAt.IsZero() {
		return fmt.Sprintf("(%s)", formatRelativeTime(item.startedAt, now))
	}
	return ""
}

func (p *BackgroundTasksPanel) formatDetail(item bgTaskItem) string {
	switch {
	case item.status == "paused":
		return "Paused at safe boundary"
	case item.status == "running" && item.lastToolName != "":
		return fmt.Sprintf("Last: %s", item.lastToolName)
	case item.status == "running" && item.activeForm != "":
		return item.activeForm
	case item.status == "completed" && item.summary != "":
		return item.summary
	case item.status == "failed" && item.summary != "":
		return fmt.Sprintf("Error: %s", item.summary)
	case item.output != "":
		// Show last line of output as preview
		lines := strings.Split(strings.TrimSpace(item.output), "\n")
		if len(lines) > 0 {
			return lines[len(lines)-1]
		}
	}
	return ""
}

func (p *BackgroundTasksPanel) selectedIdentity() taskExplorerSelection {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return taskExplorerSelection{}
	}
	return p.itemIdentity(p.items[p.cursor])
}

func (p *BackgroundTasksPanel) itemIdentity(
	item bgTaskItem,
) taskExplorerSelection {
	if item.kind == "agent" {
		return taskExplorerSelection{
			agentID:    item.execution.Key.AgentID,
			generation: item.execution.Key.Generation,
		}
	}
	return taskExplorerSelection{workID: "local:" + item.id}
}

func (p *BackgroundTasksPanel) selectedExecutionKey() engine.RuntimeExecutionKey {
	if p.cursor < 0 || p.cursor >= len(p.items) ||
		p.items[p.cursor].kind != "agent" {
		return engine.RuntimeExecutionKey{}
	}
	return p.items[p.cursor].execution.Key
}

func (p *BackgroundTasksPanel) currentCompatibility(
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

func explorerOnlyAgentDetail(
	execution engine.TaskExplorerExecution,
) engine.AgentDetailSnapshot {
	agent := engine.RuntimeAgentSnapshot{
		AgentID:         execution.Key.AgentID,
		Generation:      execution.Key.Generation,
		SessionID:       execution.SessionID,
		ThreadID:        execution.ThreadID,
		ParentSessionID: execution.ParentSessionID,
		ParentThreadID:  execution.ParentThreadID,
		ParentAgentID:   execution.ParentAgentID,
		ParentToolUseID: execution.ParentToolUseID,
		TranscriptPath:  execution.TranscriptPath,
		Name:            execution.Name,
		Task:            execution.Task,
		Description:     execution.Description,
		Status:          execution.Status,
		Progress: engine.RuntimeAgentProgressSnapshot{
			Summary: execution.Activity,
		},
	}
	return engine.AgentDetailSnapshot{
		Agent:     agent,
		LoadError: "Retained execution is read-only; current-generation readers are fenced",
	}
}

func (p *BackgroundTasksPanel) buildItems(
	explorer engine.TaskExplorerSnapshot,
) []bgTaskItem {
	var items []bgTaskItem

	for _, execution := range explorer.Executions {
		items = append(items, bgTaskItem{
			id:   execution.Key.AgentID,
			kind: "agent",
			description: firstNonEmptyTUIText(
				execution.Task,
				execution.Description,
				execution.Name,
				execution.Key.AgentID,
			),
			status:       firstNonEmptyTUIText(execution.Status, string(execution.Phase)),
			lastToolName: execution.LastToolName,
			toolUseCount: execution.ToolUseCount,
			tokenCount:   execution.TokenCount,
			summary:      execution.Activity,
			execution:    execution,
			compatible:   true,
		})
	}

	for _, task := range explorer.WorkItems {
		desc := firstNonEmptyTUIText(
			task.Title,
			task.Description,
			task.WorkItemID,
		)
		if desc == "" {
			desc = fmt.Sprintf("task %s", task.WorkItemID)
		}

		items = append(items, bgTaskItem{
			id:          task.WorkItemID,
			kind:        "task",
			description: desc,
			status:      task.Status,
			activeForm:  task.ActiveForm,
			owner:       task.Owner,
			output:      task.ResultSummary,
		})
	}

	return items
}

func (p *BackgroundTasksPanel) truncateText(text string, maxWidth int) string {
	return modalEllipsize(p.environment.profile, text, maxWidth, 0, "...")
}

// bgTaskTickMsg is sent periodically to refresh the panel while visible.
type bgTaskTickMsg struct{}

func bgTaskTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return bgTaskTickMsg{}
	})
}
