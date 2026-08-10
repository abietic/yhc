package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
)

type taskExplorerSection int

const (
	taskExplorerLogical taskExplorerSection = iota
	taskExplorerExecutions
	taskExplorerMixed
)

type taskExplorerFilter uint8

const (
	taskExplorerFilterAll taskExplorerFilter = iota
	taskExplorerFilterActive
	taskExplorerFilterAttention
	taskExplorerFilterTerminal
)

var taskExplorerFilters = [...]taskExplorerFilter{
	taskExplorerFilterAll,
	taskExplorerFilterActive,
	taskExplorerFilterAttention,
	taskExplorerFilterTerminal,
}

func (f taskExplorerFilter) String() string {
	switch f {
	case taskExplorerFilterActive:
		return "active"
	case taskExplorerFilterAttention:
		return "attention"
	case taskExplorerFilterTerminal:
		return "terminal"
	default:
		return "all"
	}
}

type taskExplorerFocus uint8

const (
	taskExplorerFocusControls taskExplorerFocus = iota
	taskExplorerFocusList
	taskExplorerFocusDetail
)

func (f taskExplorerFocus) String() string {
	switch f {
	case taskExplorerFocusList:
		return "list"
	case taskExplorerFocusDetail:
		return "detail"
	default:
		return "controls"
	}
}

type taskExplorerDetailTab uint8

const (
	taskExplorerDetailOverview taskExplorerDetailTab = iota
	taskExplorerDetailActivity
	taskExplorerDetailTranscript
	taskExplorerDetailOutput
	taskExplorerDetailLineage
)

type taskExplorerCommand uint8

const (
	taskExplorerCommandInspect taskExplorerCommand = iota
	taskExplorerCommandSwitch
	taskExplorerCommandSend
	taskExplorerCommandPauseResume
	taskExplorerCommandCancel
	taskExplorerCommandContinue
	taskExplorerCommandRefresh
	taskExplorerCommandFilter
	taskExplorerCommandSearch
	taskExplorerCommandFocus
	taskExplorerCommandPreviousDetailTab
	taskExplorerCommandNextDetailTab
)

type taskExplorerBinding struct {
	command  taskExplorerCommand
	keys     []string
	keyLabel string
	label    string
}

// taskExplorerBindings is the panel-local owner for both command resolution
// and rendered hints. Engine-declared AllowedActions still own whether an
// execution action is enabled for the selected exact generation.
var taskExplorerBindings = [...]taskExplorerBinding{
	{taskExplorerCommandInspect, []string{"enter", "ctrl+o"}, "Enter", "inspect"},
	{taskExplorerCommandSwitch, []string{"x"}, "x", "switch"},
	{taskExplorerCommandSend, []string{"s"}, "s", "send"},
	{taskExplorerCommandPauseResume, []string{"p"}, "p", "pause/resume"},
	{taskExplorerCommandCancel, []string{"c"}, "c", "cancel"},
	{taskExplorerCommandContinue, []string{"n"}, "n", "continue"},
	{taskExplorerCommandRefresh, []string{"r"}, "r", "refresh"},
	{taskExplorerCommandFilter, []string{"f"}, "f", "filter"},
	{taskExplorerCommandSearch, []string{"/"}, "/", "search"},
	{
		taskExplorerCommandFocus,
		[]string{"tab", "shift+tab"},
		"Tab",
		"focus (Shift+Tab reverse)",
	},
	{
		taskExplorerCommandPreviousDetailTab,
		[]string{"left", "h"},
		"h/Left",
		"previous tab",
	},
	{
		taskExplorerCommandNextDetailTab,
		[]string{"right", "l"},
		"l/Right",
		"next tab",
	},
}

type taskExplorerGeometry struct {
	controls    layoutRect
	filterRects map[taskExplorerFilter]layoutRect
	search      layoutRect
	list        layoutRect
	detail      layoutRect
	detailRows  int
	listStart   int
	listCount   int
}

type taskExplorerSelection struct {
	boardID    string
	workID     string
	agentID    string
	generation int64
}

type taskExplorerActionPrompt uint8

const (
	taskExplorerActionPromptNone taskExplorerActionPrompt = iota
	taskExplorerActionPromptInput
	taskExplorerActionPromptConfirm
)

type taskExplorerActionIntent struct {
	RequestID       string
	BoardID         string
	BoardRevision   uint64
	RuntimeRevision uint64
	AgentID         string
	Generation      int64
	MessageID       string
	Action          engine.TaskExplorerAction
	DisplayLabel    string
}

type taskExplorerNavigationIntent struct {
	Target          engine.TaskExplorerNavigationTarget
	RuntimeRevision uint64
}

func (s taskExplorerSelection) valid() bool {
	workItem := s.boardID != "" && s.workID != "" &&
		s.agentID == "" && s.generation == 0
	execution := s.boardID == "" && s.workID == "" &&
		s.agentID != "" && s.generation > 0
	return workItem || execution
}

type taskExplorerRow struct {
	selection  taskExplorerSelection
	searchText string
	work       *engine.TaskExplorerWorkItem
	execution  *engine.TaskExplorerExecution
}

type taskExplorerSummary struct {
	done        int
	total       int
	live        int
	attention   int
	hidden      int
	activity    string
	unavailable string
}

func summarizeTaskExplorer(snapshot engine.TaskExplorerSnapshot) taskExplorerSummary {
	if !snapshot.Available {
		return taskExplorerSummary{unavailable: snapshot.UnavailableReason}
	}
	summary := taskExplorerSummary{
		total:     len(snapshot.WorkItems),
		attention: len(snapshot.Attention),
	}
	for _, item := range snapshot.WorkItems {
		switch strings.TrimSpace(item.Status) {
		case "completed", "cancelled":
			summary.done++
		}
	}
	for _, execution := range snapshot.Executions {
		switch execution.Phase {
		case engine.TaskExplorerExecutionRunning,
			engine.TaskExplorerExecutionWaitingInput,
			engine.TaskExplorerExecutionPaused:
			summary.live++
			if summary.activity == "" {
				summary.activity = firstNonEmptyTUIText(
					execution.Activity,
					execution.Task,
					execution.Description,
					execution.Name,
					execution.Key.AgentID,
				)
			}
		}
	}
	for status, count := range snapshot.Hidden.WorkItems {
		summary.hidden += count
		summary.total += count
		switch strings.TrimSpace(status) {
		case "completed", "cancelled":
			summary.done += count
		}
	}
	for _, count := range snapshot.Hidden.Executions {
		summary.hidden += count
	}
	for _, count := range snapshot.Hidden.Attention {
		summary.hidden += count
	}
	summary.hidden += snapshot.Hidden.Links +
		snapshot.Hidden.WorkBoardOutsidePrimary +
		int(snapshot.Hidden.RuntimeEventsDropped) +
		int(snapshot.Hidden.ExecutionGenerationsEvicted) +
		int(snapshot.Hidden.HiddenLiveExecutions)
	return summary
}

// TaskExplorerPanel owns only presentation-local state. Runtime ordering,
// bounded facts, identity, and action vocabulary remain engine-owned.
type TaskExplorerPanel struct {
	styles                  Styles
	environment             RenderEnvironment
	provider                func() engine.TaskExplorerSnapshot
	transcriptProvider      agentTranscriptPageProvider
	executionDetailProvider taskExplorerExecutionDetailProvider

	section         taskExplorerSection
	teamOnly        bool
	filter          taskExplorerFilter
	focus           taskExplorerFocus
	search          string
	searchFocus     bool
	detail          bool
	detailTab       taskExplorerDetailTab
	detailOffset    int
	transcript      agentTranscriptPager
	executionDetail taskExplorerExecutionDetailState
	cursor          int
	offset          int
	selection       taskExplorerSelection
	snapshot        engine.TaskExplorerSnapshot
	rows            []taskExplorerRow
	action          func(engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult
	actionIntent    *taskExplorerActionIntent
	actionPrompt    taskExplorerActionPrompt
	actionText      string
	notice          string
	switchTarget    *taskExplorerNavigationIntent
	geometry        taskExplorerGeometry
	lastWidth       int
	lastHeight      int
}

func NewTaskExplorerPanel(styles Styles) *TaskExplorerPanel {
	panel := &TaskExplorerPanel{
		styles: styles, cursor: -1,
		filter: taskExplorerFilterAll,
		focus:  taskExplorerFocusList,
	}
	panel.SetRenderEnvironment(defaultRenderEnvironment(styles))
	return panel
}

func (a *App) taskExplorerSnapshot() engine.TaskExplorerSnapshot {
	if a == nil || a.taskExplorer == nil || a.taskExplorer.provider == nil {
		return engine.TaskExplorerSnapshot{
			UnavailableReason: "selector_unavailable",
		}
	}
	return a.taskExplorer.provider()
}

func (p *TaskExplorerPanel) SetRenderEnvironment(env RenderEnvironment) {
	p.environment = env.normalized()
	p.styles = p.environment.styles
}

func (p *TaskExplorerPanel) SetSnapshotProvider(
	provider func() engine.TaskExplorerSnapshot,
) {
	p.provider = provider
}

func (p *TaskExplorerPanel) SetActionProvider(
	action func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult,
) {
	p.action = action
}

func (p *TaskExplorerPanel) SetTranscriptProvider(
	provider agentTranscriptPageProvider,
) {
	p.transcriptProvider = provider
}

func (p *TaskExplorerPanel) SetExecutionDetailProvider(
	provider taskExplorerExecutionDetailProvider,
) {
	p.executionDetailProvider = provider
}

func (p *TaskExplorerPanel) Show(section taskExplorerSection, teamOnly bool) {
	p.section = section
	p.teamOnly = teamOnly
	p.searchFocus = false
	p.focus = taskExplorerFocusList
	p.detail = false
	p.clearActionIntent()
	p.notice = ""
	p.switchTarget = nil
	p.offset = 0
	p.resetDetailProjection()
	p.Refresh()
}

func (p *TaskExplorerPanel) Refresh() {
	p.invalidateGeometry()
	previous := p.selection
	previousIndex := p.cursor
	if p.provider == nil {
		p.snapshot = engine.TaskExplorerSnapshot{
			UnavailableReason: "selector_unavailable",
		}
	} else {
		p.snapshot = cloneTaskExplorerSnapshotForPanel(p.provider())
	}
	p.rows = p.filteredRows()
	p.restoreSelection(previous, previousIndex)
	p.bindSelectedExecutionDetail()
}

func cloneTaskExplorerSnapshotForPanel(
	snapshot engine.TaskExplorerSnapshot,
) engine.TaskExplorerSnapshot {
	out := snapshot
	out.WorkItems = make(
		[]engine.TaskExplorerWorkItem,
		len(snapshot.WorkItems),
	)
	for index, item := range snapshot.WorkItems {
		item.Blocks = append([]string(nil), item.Blocks...)
		item.BlockedBy = append([]string(nil), item.BlockedBy...)
		item.Attention = append([]string(nil), item.Attention...)
		item.ExecutionKeys = append(
			[]engine.RuntimeExecutionKey(nil),
			item.ExecutionKeys...,
		)
		item.AllowedActions = append(
			[]engine.TaskExplorerAction(nil),
			item.AllowedActions...,
		)
		out.WorkItems[index] = item
	}
	out.Executions = make(
		[]engine.TaskExplorerExecution,
		len(snapshot.Executions),
	)
	for index, execution := range snapshot.Executions {
		execution.Attention = append([]string(nil), execution.Attention...)
		execution.AllowedActions = append(
			[]engine.TaskExplorerAction(nil),
			execution.AllowedActions...,
		)
		out.Executions[index] = execution
	}
	out.Links = make([]engine.TaskExplorerLink, len(snapshot.Links))
	for index, link := range snapshot.Links {
		link.AllowedActions = append(
			[]engine.TaskExplorerAction(nil),
			link.AllowedActions...,
		)
		out.Links[index] = link
	}
	out.Attention = append(
		[]engine.TaskExplorerAttention(nil),
		snapshot.Attention...,
	)
	out.Diagnostics = append(
		[]engine.TaskExplorerDiagnostic(nil),
		snapshot.Diagnostics...,
	)
	out.Hidden.WorkItems = cloneTaskExplorerHiddenMap(snapshot.Hidden.WorkItems)
	out.Hidden.Executions = cloneTaskExplorerHiddenMap(snapshot.Hidden.Executions)
	out.Hidden.Attention = cloneTaskExplorerHiddenMap(snapshot.Hidden.Attention)
	return out
}

func cloneTaskExplorerHiddenMap(source map[string]int) map[string]int {
	if source == nil {
		return nil
	}
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func (p *TaskExplorerPanel) resetDetailProjection() {
	p.detailTab = taskExplorerDetailOverview
	p.detailOffset = 0
	p.transcript.reset()
	p.executionDetail.reset()
}

func (p *TaskExplorerPanel) restoreSelection(
	previous taskExplorerSelection,
	previousIndex int,
) {
	p.cursor = -1
	if previous.valid() {
		for index := range p.rows {
			if p.rows[index].selection == previous {
				p.cursor = index
				break
			}
		}
	}
	if p.cursor < 0 && len(p.rows) > 0 {
		p.cursor = min(max(previousIndex, 0), len(p.rows)-1)
	}
	if p.cursor >= 0 {
		next := p.rows[p.cursor].selection
		if next != previous {
			p.resetDetailProjection()
		}
		p.selection = next
	} else {
		p.selection = taskExplorerSelection{}
		p.offset = 0
		p.resetDetailProjection()
		p.detail = false
		if p.focus == taskExplorerFocusDetail {
			p.focus = taskExplorerFocusList
		}
	}
}

func (p *TaskExplorerPanel) filteredRows() []taskExplorerRow {
	needle := strings.ToLower(strings.TrimSpace(p.search))
	capacity := len(p.snapshot.WorkItems) + len(p.snapshot.Executions)
	if p.section == taskExplorerLogical {
		capacity = len(p.snapshot.WorkItems)
	} else if p.section == taskExplorerExecutions {
		capacity = len(p.snapshot.Executions)
	}
	rows := make([]taskExplorerRow, 0, capacity)
	if p.section != taskExplorerExecutions {
		for index := range p.snapshot.WorkItems {
			item := &p.snapshot.WorkItems[index]
			if !taskExplorerWorkItemMatchesFilter(*item, p.filter) {
				continue
			}
			searchText := strings.Join([]string{
				"WorkItem",
				item.BoardID,
				item.WorkItemID,
				item.Title,
				item.ActiveForm,
				item.Description,
				item.Owner,
				item.Status,
			}, "\n")
			if needle != "" &&
				!strings.Contains(strings.ToLower(searchText), needle) {
				continue
			}
			rows = append(rows, taskExplorerRow{
				selection: taskExplorerSelection{
					boardID: item.BoardID,
					workID:  item.WorkItemID,
				},
				searchText: searchText,
				work:       item,
			})
		}
	}
	if p.section != taskExplorerLogical {
		for index := range p.snapshot.Executions {
			execution := &p.snapshot.Executions[index]
			if !taskExplorerExecutionMatchesFilter(*execution, p.filter) {
				continue
			}
			searchText := strings.Join([]string{
				"Execution",
				execution.Key.AgentID,
				fmt.Sprintf("%d", execution.Key.Generation),
				execution.Name,
				execution.Task,
				execution.Description,
				execution.Activity,
				execution.Status,
				string(execution.Phase),
			}, "\n")
			if needle != "" &&
				!strings.Contains(strings.ToLower(searchText), needle) {
				continue
			}
			rows = append(rows, taskExplorerRow{
				selection: taskExplorerSelection{
					agentID:    execution.Key.AgentID,
					generation: execution.Key.Generation,
				},
				searchText: searchText,
				execution:  execution,
			})
		}
	}
	return rows
}

func taskExplorerWorkItemMatchesFilter(
	item engine.TaskExplorerWorkItem,
	filter taskExplorerFilter,
) bool {
	switch filter {
	case taskExplorerFilterActive:
		return strings.TrimSpace(item.Status) == "in_progress"
	case taskExplorerFilterAttention:
		return len(item.Attention) > 0
	case taskExplorerFilterTerminal:
		switch strings.TrimSpace(item.Status) {
		case "completed", "failed", "cancelled":
			return true
		default:
			return false
		}
	default:
		return true
	}
}

func taskExplorerExecutionMatchesFilter(
	execution engine.TaskExplorerExecution,
	filter taskExplorerFilter,
) bool {
	switch filter {
	case taskExplorerFilterActive:
		switch execution.Phase {
		case engine.TaskExplorerExecutionRunning,
			engine.TaskExplorerExecutionWaitingInput,
			engine.TaskExplorerExecutionPaused:
			return true
		default:
			return false
		}
	case taskExplorerFilterAttention:
		return len(execution.Attention) > 0
	case taskExplorerFilterTerminal:
		switch execution.Phase {
		case engine.TaskExplorerExecutionCompleted,
			engine.TaskExplorerExecutionFailed,
			engine.TaskExplorerExecutionCancelled:
			return true
		default:
			return false
		}
	default:
		return true
	}
}

func (p *TaskExplorerPanel) localHiddenCount() int {
	total := 0
	if p.section != taskExplorerExecutions {
		total += len(p.snapshot.WorkItems)
	}
	if p.section != taskExplorerLogical {
		total += len(p.snapshot.Executions)
	}
	return max(0, total-len(p.rows))
}

func (p *TaskExplorerPanel) refilter() {
	p.invalidateGeometry()
	previous := p.selection
	previousIndex := p.cursor
	p.rows = p.filteredRows()
	p.restoreSelection(previous, previousIndex)
}

func (p *TaskExplorerPanel) selectedRow() (taskExplorerRow, bool) {
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return taskExplorerRow{}, false
	}
	return p.rows[p.cursor], true
}

func (p *TaskExplorerPanel) move(delta, height int) {
	if len(p.rows) == 0 {
		return
	}
	p.invalidateGeometry()
	previous := p.selection
	p.cursor = min(max(p.cursor+delta, 0), len(p.rows)-1)
	p.selection = p.rows[p.cursor].selection
	if p.selection != previous {
		p.resetDetailProjection()
	}
	p.ensureVisible(max(1, height-7))
}

func (p *TaskExplorerPanel) detailBodyLines(width int) []string {
	row, ok := p.selectedRow()
	if !ok {
		return []string{"No selection"}
	}
	if row.work != nil {
		if p.detailTab == taskExplorerDetailActivity {
			return p.workItemActivityLines(*row.work)
		}
		return taskExplorerWorkItemOverviewLines(*row.work)
	}
	if row.execution != nil {
		switch p.detailTab {
		case taskExplorerDetailActivity:
			return p.executionActivityLines(*row.execution)
		case taskExplorerDetailTranscript:
			if !p.transcript.selection.valid() && !p.transcript.initialized {
				return []string{"Execution transcript is unavailable for this exact generation"}
			}
			return buildAgentTranscriptPageLinesWithProfile(
				p.environment.profile,
				p.transcript,
				max(12, width),
			)
		case taskExplorerDetailOutput, taskExplorerDetailLineage:
			return p.executionDetailLines(p.detailTab, width)
		default:
			return taskExplorerExecutionOverviewLines(*row.execution)
		}
	}
	return []string{"No selection"}
}

func (p *TaskExplorerPanel) executionDetailLines(
	tab taskExplorerDetailTab,
	width int,
) []string {
	state := p.executionDetail.tabState(tab)
	label := taskExplorerDetailTabLabel(tab)
	if state.loading && !state.initialized {
		return []string{"Loading " + label + "..."}
	}
	if !state.initialized || state.unavailable {
		return []string{fmt.Sprintf(
			"Execution %s is unavailable for this exact generation",
			label,
		)}
	}
	detail := engine.AgentDetailSnapshot{
		Revision:        state.detail.Revision,
		Agent:           state.detail.Agent,
		Output:          state.detail.Output,
		OutputTruncated: state.detail.OutputTruncated,
		LoadError:       state.detail.LoadError,
	}
	var lines []string
	if tab == taskExplorerDetailOutput {
		lines = buildAgentOutputLines(p.environment.profile, detail, max(12, width))
	} else {
		lines = buildAgentLineageLines(p.environment.profile, detail, max(12, width))
	}
	if detail.LoadError != "" {
		lines = append(lines, "", "Read warning: "+detail.LoadError)
	}
	return lines
}

func taskExplorerDetailTabLabel(tab taskExplorerDetailTab) string {
	switch tab {
	case taskExplorerDetailActivity:
		return "activity"
	case taskExplorerDetailTranscript:
		return "transcript"
	case taskExplorerDetailOutput:
		return "output"
	case taskExplorerDetailLineage:
		return "lineage"
	default:
		return "overview"
	}
}

func (p *TaskExplorerPanel) detailViewportRows(height int) int {
	if p.geometry.detailRows > 0 {
		return p.geometry.detailRows
	}
	return max(1, height-12)
}

func (p *TaskExplorerPanel) moveDetail(delta, height int) {
	rows := p.detailBodyLines(p.detailBodyWidth())
	visible := p.detailViewportRows(height)
	maxOffset := max(0, len(rows)-visible)
	p.detailOffset = min(max(p.detailOffset+delta, 0), maxOffset)
	if delta != 0 {
		p.invalidateGeometry()
	}
}

func (p *TaskExplorerPanel) moveDetailToEnd(height int) {
	rows := p.detailBodyLines(p.detailBodyWidth())
	visible := p.detailViewportRows(height)
	p.detailOffset = max(0, len(rows)-visible)
	p.invalidateGeometry()
}

func (p *TaskExplorerPanel) detailBodyWidth() int {
	if p.geometry.detail.Width > 0 {
		return max(12, p.geometry.detail.Width)
	}
	return max(12, p.lastWidth-4)
}

func (p *TaskExplorerPanel) maxDetailTab() taskExplorerDetailTab {
	if _, ok := p.selectedExecution(); ok {
		return taskExplorerDetailLineage
	}
	return taskExplorerDetailActivity
}

func (p *TaskExplorerPanel) switchDetailTab(
	tab taskExplorerDetailTab,
) tea.Cmd {
	maximum := p.maxDetailTab()
	if tab > maximum {
		tab = maximum
	}
	if tab == p.detailTab {
		return nil
	}
	switch p.detailTab {
	case taskExplorerDetailTranscript:
		p.transcript.invalidate()
	case taskExplorerDetailOutput, taskExplorerDetailLineage:
		p.executionDetail.invalidate(p.detailTab)
	}
	p.detailTab = tab
	p.detailOffset = 0
	p.invalidateGeometry()
	return p.ensureLazyDetail(false)
}

func (p *TaskExplorerPanel) handleDetailNavigation(
	msg tea.KeyPressMsg,
	height int,
) (bool, tea.Cmd) {
	command, ok := resolveTaskExplorerCommand(msg)
	if ok {
		switch command {
		case taskExplorerCommandPreviousDetailTab:
			previous := p.detailTab
			if previous > taskExplorerDetailOverview {
				previous--
			}
			return true, p.switchDetailTab(previous)
		case taskExplorerCommandNextDetailTab:
			next := p.detailTab
			if next < p.maxDetailTab() {
				next++
			}
			return true, p.switchDetailTab(next)
		}
	}
	page := p.detailViewportRows(height)
	switch msg.String() {
	case "up", "k":
		p.moveDetail(-1, height)
		if p.detailTab == taskExplorerDetailTranscript && p.detailOffset == 0 {
			return true, p.requestOlderTranscript()
		}
	case "down", "j":
		p.moveDetail(1, height)
	case "pgup":
		p.moveDetail(-page, height)
		if p.detailTab == taskExplorerDetailTranscript && p.detailOffset == 0 {
			return true, p.requestOlderTranscript()
		}
	case "pgdown":
		p.moveDetail(page, height)
	case "home", "g":
		p.detailOffset = 0
		p.invalidateGeometry()
		if p.detailTab == taskExplorerDetailTranscript {
			return true, p.requestOlderTranscript()
		}
	case "end", "G":
		p.moveDetailToEnd(height)
	default:
		return false, nil
	}
	return true, nil
}

func (p *TaskExplorerPanel) ensureLazyDetail(force bool) tea.Cmd {
	if _, ok := p.selectedExecution(); !ok {
		return nil
	}
	p.bindSelectedExecutionDetail()
	switch p.detailTab {
	case taskExplorerDetailTranscript:
		if force {
			p.transcript.invalidate()
		}
		request, started := p.transcript.begin(
			agentTranscriptSurfaceTaskExplorer,
			force,
		)
		if !started {
			return nil
		}
		request.tab = taskExplorerDetailTranscript
		if command := agentTranscriptPageCmd(p.transcriptProvider, request); command != nil {
			return command
		}
		return func() tea.Msg {
			return agentTranscriptPageLoadedMsg{request: request}
		}
	case taskExplorerDetailOutput, taskExplorerDetailLineage:
		request, started := p.executionDetail.begin(p.detailTab, force)
		if !started {
			return nil
		}
		return taskExplorerExecutionDetailCmd(
			p.executionDetailProvider,
			request,
		)
	default:
		return nil
	}
}

func (p *TaskExplorerPanel) requestOlderTranscript() tea.Cmd {
	if p.detailTab != taskExplorerDetailTranscript {
		return nil
	}
	request, started := p.transcript.older(agentTranscriptSurfaceTaskExplorer)
	if !started {
		return nil
	}
	request.tab = taskExplorerDetailTranscript
	if command := agentTranscriptPageCmd(p.transcriptProvider, request); command != nil {
		return command
	}
	return func() tea.Msg {
		return agentTranscriptPageLoadedMsg{request: request}
	}
}

func (p *TaskExplorerPanel) applyTranscriptPage(
	msg agentTranscriptPageLoadedMsg,
) bool {
	execution, ok := p.selectedExecution()
	if !ok || p.detailTab != taskExplorerDetailTranscript ||
		msg.request.surface != agentTranscriptSurfaceTaskExplorer ||
		msg.request.tab != taskExplorerDetailTranscript ||
		msg.request.selection.AgentID != execution.Key.AgentID ||
		msg.request.selection.Generation != execution.Key.Generation ||
		msg.request.selection.ThreadID != execution.ThreadID {
		p.transcript.discard(msg)
		return false
	}
	if msg.err == nil && msg.found &&
		(msg.page.SessionID != execution.SessionID ||
			msg.page.ThreadID != execution.ThreadID) {
		msg.page = engine.AgentTranscriptPage{}
		msg.err = engine.ErrAgentTranscriptSelectionChanged
	}
	if !p.transcript.apply(msg) {
		return false
	}
	if msg.err != nil || !msg.found || p.transcript.err != "" {
		p.transcript.err = "Agent transcript is unavailable for this exact generation"
	}
	p.detailOffset = min(
		p.detailOffset,
		max(0, len(p.detailBodyLines(p.detailBodyWidth()))-p.detailViewportRows(p.lastHeight)),
	)
	p.invalidateGeometry()
	return true
}

func (p *TaskExplorerPanel) applyExecutionDetail(
	msg taskExplorerExecutionDetailLoadedMsg,
) bool {
	execution, ok := p.selectedExecution()
	if !ok || p.detailTab != msg.request.tab ||
		p.selection != msg.request.selection ||
		execution.SessionID != msg.request.sessionID ||
		execution.ThreadID != msg.request.threadID {
		return false
	}
	if !p.executionDetail.apply(msg) {
		return false
	}
	p.detailOffset = 0
	p.invalidateGeometry()
	return true
}

func (p *TaskExplorerPanel) ensureVisible(visible int) {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+visible {
		p.offset = p.cursor - visible + 1
	}
	maxOffset := max(0, len(p.rows)-visible)
	p.offset = min(max(p.offset, 0), maxOffset)
}

func (p *TaskExplorerPanel) selectedExecution() (
	*engine.TaskExplorerExecution,
	bool,
) {
	row, ok := p.selectedRow()
	if !ok || row.execution == nil {
		return nil, false
	}
	return row.execution, true
}

func (p *TaskExplorerPanel) bindSelectedExecutionDetail() {
	execution, ok := p.selectedExecution()
	if !ok {
		return
	}
	mode := engine.ThreadModeLiveAttach
	if execution.ReplayOnly ||
		execution.Phase == engine.TaskExplorerExecutionReplayOnly {
		mode = engine.ThreadModeReplayOnly
	}
	p.transcript.bind(agentTranscriptSelectionFromExecution(*execution, mode))
	p.executionDetail.bind(taskExplorerExecutionDetailIdentity{
		selection: p.selection,
		sessionID: execution.SessionID,
		threadID:  execution.ThreadID,
	})
}

func (p *TaskExplorerPanel) actionAllowed(
	action engine.TaskExplorerAction,
) bool {
	execution, ok := p.selectedExecution()
	if !ok {
		return false
	}
	for _, candidate := range execution.AllowedActions {
		if candidate == action {
			return true
		}
	}
	return false
}

func (p *TaskExplorerPanel) captureActionIntent(
	action engine.TaskExplorerAction,
) (*taskExplorerActionIntent, bool) {
	execution, ok := p.selectedExecution()
	if !ok || !p.actionAllowed(action) {
		p.notice = "Action unavailable for this exact generation"
		return nil, false
	}
	return &taskExplorerActionIntent{
		RequestID:       uuid.NewString(),
		BoardID:         p.snapshot.BoardID,
		BoardRevision:   p.snapshot.Revision.Board,
		RuntimeRevision: p.snapshot.Revision.Runtime,
		AgentID:         execution.Key.AgentID,
		Generation:      execution.Key.Generation,
		Action:          action,
		DisplayLabel: fmt.Sprintf(
			"%s@g%d",
			execution.Key.AgentID,
			execution.Key.Generation,
		),
	}, true
}

func (p *TaskExplorerPanel) clearActionIntent() {
	p.actionIntent = nil
	p.actionPrompt = taskExplorerActionPromptNone
	p.actionText = ""
}

func (p *TaskExplorerPanel) submitActionIntent(
	intent *taskExplorerActionIntent,
	payload string,
) {
	if intent != nil && intent.Action == engine.TaskExplorerActionSwitch {
		p.switchTarget = nil
	}
	if intent == nil || p.action == nil {
		p.notice = "Action unavailable for this exact generation"
		return
	}
	result := p.action(engine.TaskExplorerActionRequest{
		RequestID:       intent.RequestID,
		BoardID:         intent.BoardID,
		BoardRevision:   intent.BoardRevision,
		RuntimeRevision: intent.RuntimeRevision,
		AgentID:         intent.AgentID,
		Generation:      intent.Generation,
		MessageID:       intent.MessageID,
		Action:          intent.Action,
		Payload:         payload,
	})
	p.notice = firstNonEmptyTUIText(
		result.Message,
		result.Outcome,
		result.Conflict,
	)
	expectedMessageID := intent.MessageID
	if intent.Action == engine.TaskExplorerActionSend {
		expectedMessageID = intent.RequestID
	}
	if result.RequestID != intent.RequestID ||
		result.BoardID != intent.BoardID ||
		result.BoardRevision != intent.BoardRevision ||
		result.AgentID != intent.AgentID ||
		result.Generation != intent.Generation ||
		result.MessageID != expectedMessageID ||
		result.Action != intent.Action ||
		result.Conflict != "" {
		return
	}
	if intent.Action == engine.TaskExplorerActionSwitch {
		target := result.NavigationTarget
		if result.Outcome != "switched" || target == nil ||
			!p.navigationTargetMatches(intent, result, *target) {
			p.notice = "Exact navigation target changed before activation"
			return
		}
		p.switchTarget = &taskExplorerNavigationIntent{
			Target:          *target,
			RuntimeRevision: result.RuntimeRevision,
		}
	}
	p.Refresh()
}

func (p *TaskExplorerPanel) navigationTargetMatches(
	intent *taskExplorerActionIntent,
	result engine.TaskExplorerActionResult,
	target engine.TaskExplorerNavigationTarget,
) bool {
	if intent == nil || target.AgentID != intent.AgentID ||
		target.Generation != intent.Generation ||
		strings.TrimSpace(target.SessionID) == "" ||
		strings.TrimSpace(target.ThreadID) == "" ||
		target.Mode != engine.ThreadModeLiveAttach ||
		result.SessionID != target.SessionID ||
		result.ThreadID != target.ThreadID {
		return false
	}
	matches := 0
	for _, row := range p.snapshot.Executions {
		if row.Key.AgentID != target.AgentID ||
			row.Key.Generation != target.Generation {
			continue
		}
		matches++
		if strings.TrimSpace(row.SessionID) != target.SessionID ||
			strings.TrimSpace(row.ThreadID) != target.ThreadID ||
			strings.TrimSpace(row.TranscriptPath) == "" ||
			row.Predispatch || row.ReplayOnly ||
			row.Phase == engine.TaskExplorerExecutionReplayOnly {
			return false
		}
	}
	return matches == 1
}

func (p *TaskExplorerPanel) submitAction(
	action engine.TaskExplorerAction,
) {
	intent, ok := p.captureActionIntent(action)
	if !ok {
		return
	}
	p.submitActionIntent(intent, "")
}

func (p *TaskExplorerPanel) takeSwitchTarget() (
	taskExplorerNavigationIntent,
	bool,
) {
	if p.switchTarget == nil {
		return taskExplorerNavigationIntent{}, false
	}
	intent := *p.switchTarget
	p.switchTarget = nil
	return intent, true
}

func resolveTaskExplorerCommand(msg tea.KeyPressMsg) (
	taskExplorerCommand,
	bool,
) {
	key := msg.String()
	for _, binding := range &taskExplorerBindings {
		for _, candidate := range binding.keys {
			if key == candidate {
				return binding.command, true
			}
		}
	}
	return 0, false
}

func taskExplorerBindingFor(
	command taskExplorerCommand,
) (taskExplorerBinding, bool) {
	for _, binding := range &taskExplorerBindings {
		if binding.command == command {
			return binding, true
		}
	}
	return taskExplorerBinding{}, false
}

func (p *TaskExplorerPanel) cycleFilter() {
	index := 0
	for candidate := range taskExplorerFilters {
		if taskExplorerFilters[candidate] == p.filter {
			index = candidate
			break
		}
	}
	p.filter = taskExplorerFilters[(index+1)%len(taskExplorerFilters)]
	p.refilter()
}

func (p *TaskExplorerPanel) setFocus(focus taskExplorerFocus) {
	p.invalidateGeometry()
	p.searchFocus = false
	if focus == taskExplorerFocusDetail && !p.selection.valid() {
		focus = taskExplorerFocusList
	}
	p.focus = focus
	p.detail = focus == taskExplorerFocusDetail
}

func (p *TaskExplorerPanel) cycleFocus(reverse bool) {
	switch {
	case reverse && p.focus == taskExplorerFocusControls:
		if p.selection.valid() {
			p.setFocus(taskExplorerFocusDetail)
		} else {
			p.setFocus(taskExplorerFocusList)
		}
	case reverse && p.focus == taskExplorerFocusList:
		p.setFocus(taskExplorerFocusControls)
	case reverse:
		p.setFocus(taskExplorerFocusList)
	case p.focus == taskExplorerFocusControls:
		p.setFocus(taskExplorerFocusList)
	case p.focus == taskExplorerFocusList:
		if p.selection.valid() {
			p.setFocus(taskExplorerFocusDetail)
		} else {
			p.setFocus(taskExplorerFocusControls)
		}
	default:
		p.setFocus(taskExplorerFocusControls)
	}
}

func (p *TaskExplorerPanel) inspectSelection() {
	if !p.selection.valid() {
		return
	}
	if p.actionAllowed(engine.TaskExplorerActionInspect) {
		p.submitAction(engine.TaskExplorerActionInspect)
	}
	p.setFocus(taskExplorerFocusDetail)
}

func (p *TaskExplorerPanel) handleCommand(command taskExplorerCommand) tea.Cmd {
	switch command {
	case taskExplorerCommandInspect:
		p.inspectSelection()
	case taskExplorerCommandSwitch:
		p.submitAction(engine.TaskExplorerActionSwitch)
	case taskExplorerCommandSend:
		if intent, ok := p.captureActionIntent(
			engine.TaskExplorerActionSend,
		); ok {
			p.actionIntent = intent
			p.actionPrompt = taskExplorerActionPromptInput
		}
	case taskExplorerCommandPauseResume:
		switch {
		case p.actionAllowed(engine.TaskExplorerActionPause):
			p.submitAction(engine.TaskExplorerActionPause)
		case p.actionAllowed(engine.TaskExplorerActionResume):
			p.submitAction(engine.TaskExplorerActionResume)
		}
	case taskExplorerCommandCancel:
		if intent, ok := p.captureActionIntent(
			engine.TaskExplorerActionCancel,
		); ok {
			p.actionIntent = intent
			p.actionPrompt = taskExplorerActionPromptConfirm
		}
	case taskExplorerCommandContinue:
		if intent, ok := p.captureActionIntent(
			engine.TaskExplorerActionContinue,
		); ok {
			p.actionIntent = intent
			p.actionPrompt = taskExplorerActionPromptInput
		}
	case taskExplorerCommandRefresh:
		p.Refresh()
		return p.ensureLazyDetail(true)
	case taskExplorerCommandFilter:
		p.setFocus(taskExplorerFocusControls)
		p.cycleFilter()
	case taskExplorerCommandSearch:
		p.setFocus(taskExplorerFocusControls)
		p.searchFocus = true
	case taskExplorerCommandFocus:
		// The caller retains the key event so Shift+Tab can choose direction.
	case taskExplorerCommandPreviousDetailTab:
		if p.focus == taskExplorerFocusDetail {
			previous := p.detailTab
			if previous > taskExplorerDetailOverview {
				previous--
			}
			return p.switchDetailTab(previous)
		}
	case taskExplorerCommandNextDetailTab:
		if p.focus == taskExplorerFocusDetail {
			next := p.detailTab
			if next < p.maxDetailTab() {
				next++
			}
			return p.switchDetailTab(next)
		}
	}
	return nil
}

func (p *TaskExplorerPanel) HandleKey(
	msg tea.KeyPressMsg,
	height int,
) (dismissed bool) {
	dismissed, command := p.HandleKeyWithCmd(msg, height)
	if command != nil {
		switch p.detailTab {
		case taskExplorerDetailTranscript:
			p.transcript.invalidate()
		case taskExplorerDetailOutput, taskExplorerDetailLineage:
			p.executionDetail.invalidate(p.detailTab)
		}
	}
	return dismissed
}

func (p *TaskExplorerPanel) HandleKeyWithCmd(
	msg tea.KeyPressMsg,
	height int,
) (dismissed bool, command tea.Cmd) {
	if p.actionPrompt == taskExplorerActionPromptInput &&
		p.actionIntent != nil {
		switch msg.String() {
		case "esc":
			p.clearActionIntent()
		case "enter":
			intent := p.actionIntent
			payload := p.actionText
			p.clearActionIntent()
			p.submitActionIntent(intent, payload)
		case "backspace":
			runes := []rune(p.actionText)
			if len(runes) > 0 {
				p.actionText = string(runes[:len(runes)-1])
			}
		case "ctrl+u":
			p.actionText = ""
		default:
			if msg.Text != "" {
				p.actionText += msg.Text
			}
		}
		return false, nil
	}
	if p.actionPrompt == taskExplorerActionPromptConfirm &&
		p.actionIntent != nil {
		switch msg.String() {
		case "y", "enter":
			intent := p.actionIntent
			p.clearActionIntent()
			p.submitActionIntent(intent, "")
		case "n", "esc":
			p.clearActionIntent()
			p.notice = "Action cancelled"
		}
		return false, nil
	}
	if p.searchFocus {
		switch msg.String() {
		case "esc":
			p.searchFocus = false
		case "enter":
			p.setFocus(taskExplorerFocusList)
		case "tab":
			p.cycleFocus(false)
		case "shift+tab":
			p.cycleFocus(true)
		case "backspace":
			runes := []rune(p.search)
			if len(runes) > 0 {
				p.search = string(runes[:len(runes)-1])
				p.refilter()
			}
		case "ctrl+u":
			p.search = ""
			p.refilter()
		default:
			if msg.Text != "" {
				p.search += msg.Text
				p.refilter()
			}
		}
		return false, nil
	}

	switch msg.String() {
	case "esc":
		if p.focus != taskExplorerFocusList {
			p.setFocus(taskExplorerFocusList)
			return false, nil
		}
		p.resetDetailProjection()
		return true, nil
	case "q", "ctrl+t":
		p.resetDetailProjection()
		return true, nil
	}
	if p.focus == taskExplorerFocusDetail {
		if handled, detailCommand := p.handleDetailNavigation(msg, height); handled {
			return false, detailCommand
		}
	}

	switch msg.String() {
	case "up", "k":
		p.setFocus(taskExplorerFocusList)
		p.move(-1, height)
	case "down", "j":
		p.setFocus(taskExplorerFocusList)
		p.move(1, height)
	case "pgup":
		p.setFocus(taskExplorerFocusList)
		p.move(-max(1, height-7), height)
	case "pgdown":
		p.setFocus(taskExplorerFocusList)
		p.move(max(1, height-7), height)
	case "home", "g":
		p.setFocus(taskExplorerFocusList)
		p.move(-len(p.rows), height)
	case "end", "G":
		p.setFocus(taskExplorerFocusList)
		p.move(len(p.rows), height)
	default:
		command, ok := resolveTaskExplorerCommand(msg)
		if !ok {
			return false, nil
		}
		if command == taskExplorerCommandFocus {
			p.cycleFocus(msg.String() == "shift+tab")
			return false, nil
		}
		return false, p.handleCommand(command)
	}
	return false, nil
}

func (p *TaskExplorerPanel) Render(width, height int) string {
	width = max(width, 1)
	height = max(height, 1)
	p.lastWidth = width
	p.lastHeight = height
	p.geometry = taskExplorerGeometry{
		filterRects: make(map[taskExplorerFilter]layoutRect),
	}
	profile := p.environment.profile
	controls := p.controlLine()
	p.captureControlGeometry(width, 2)
	lines := []string{
		profile.truncateAt(p.title(), width, 0),
		profile.truncateAt(p.summaryLine(), width, 0),
		profile.truncateAt(controls, width, 0),
		profile.truncateAt(p.controlHintLine(), width, 0),
	}
	if p.searchFocus {
		lines = append(lines, profile.truncateAt(
			"Search input: /"+p.search,
			width,
			0,
		))
	}
	switch {
	case p.actionPrompt == taskExplorerActionPromptInput &&
		p.actionIntent != nil:
		lines = append(
			lines,
			profile.truncateAt(
				fmt.Sprintf(
					"%s %s> %s",
					p.actionIntent.Action,
					p.actionIntent.DisplayLabel,
					p.actionText,
				),
				width,
				0,
			),
		)
	case p.actionPrompt == taskExplorerActionPromptConfirm &&
		p.actionIntent != nil:
		lines = append(
			lines,
			profile.truncateAt(
				fmt.Sprintf(
					"Confirm %s %s? y/N",
					p.actionIntent.Action,
					p.actionIntent.DisplayLabel,
				),
				width,
				0,
			),
		)
	case p.notice != "":
		lines = append(lines, profile.truncateAt(p.notice, width, 0))
	}
	help := p.helpLines(width)
	if !p.snapshot.Available {
		p.invalidateGeometry()
		lines = append(lines, profile.truncateAt(
			"Explorer unavailable: "+p.snapshot.UnavailableReason,
			width,
			0,
		))
		lines = append(lines, help...)
		return boundedExplorerLines(profile, lines, width, height)
	}

	wide := width >= 150 && height >= 24
	compact := width < 80 || height < 24
	bodyStart := len(lines)
	// Keep one terminal row of slack so the list never consumes the footer's
	// last repaint row during resize and preserves the prior viewport budget.
	bodyBudget := max(1, height-bodyStart-len(help)-1)
	if wide {
		body := p.renderSplit(width, bodyBudget)
		lines = append(lines, body...)
		p.captureWideBodyGeometry(width, bodyStart, bodyBudget)
	} else if p.detail && compact {
		body := p.renderDetail(width, bodyBudget)
		lines = append(lines, body...)
		p.geometry.detail = layoutRect{
			Y: bodyStart, Width: width, Height: len(body),
		}
		p.geometry.detailRows = p.renderedDetailBodyRows(width, len(body))
	} else {
		inline := []string(nil)
		if !compact {
			inline = p.renderInlineDetail(width)
		}
		listBudget := max(1, bodyBudget-len(inline))
		list := p.renderList(width, listBudget)
		lines = append(lines, list...)
		p.captureListGeometry(0, bodyStart, width, listBudget)
		remaining := max(0, bodyBudget-len(list))
		if len(inline) > remaining {
			inline = inline[:remaining]
		}
		if len(inline) > 0 {
			detailY := bodyStart + len(list) + 2
			detailHeight := max(0, len(inline)-2)
			p.geometry.detail = layoutRect{
				Y: detailY, Width: width, Height: detailHeight,
			}
			p.geometry.detailRows = p.renderedDetailBodyRows(width, detailHeight)
			lines = append(lines, inline...)
		}
	}
	lines = append(lines, help...)
	return boundedExplorerLines(profile, lines, width, height)
}

func (p *TaskExplorerPanel) title() string {
	label := "Logical work"
	switch p.section {
	case taskExplorerExecutions:
		label = "Executions"
	case taskExplorerMixed:
		label = "WorkItems and executions"
	}
	if p.teamOnly {
		label = "Team executions"
	}
	return p.styles.Bold.Render("Task Explorer · " + label)
}

func (p *TaskExplorerPanel) controlLine() string {
	filters := make([]string, 0, len(taskExplorerFilters))
	for _, filter := range taskExplorerFilters {
		label := filter.String()
		if filter == p.filter {
			label = "[" + label + "]"
		}
		filters = append(filters, label)
	}
	search := "empty"
	if p.searchFocus {
		search = "editing"
		if p.search != "" {
			search += " /" + p.search
		}
	} else if p.search != "" {
		search = "/" + p.search
	}
	return fmt.Sprintf(
		"Focus: %s · Filter: %s · Search: %s",
		p.focus,
		strings.Join(filters, " "),
		search,
	)
}

func (p *TaskExplorerPanel) captureControlGeometry(width, y int) {
	profile := p.environment.profile
	p.geometry.controls = layoutRect{Y: y, Width: width, Height: 1}
	x := profile.width(fmt.Sprintf("Focus: %s · Filter: ", p.focus))
	for _, filter := range taskExplorerFilters {
		label := filter.String()
		if filter == p.filter {
			label = "[" + label + "]"
		}
		labelWidth := profile.width(label)
		p.geometry.filterRects[filter] = layoutRect{
			X: x, Y: y, Width: min(labelWidth, max(0, width-x)), Height: 1,
		}
		x += labelWidth + 1
	}
	x--
	x += profile.width(" · ")
	searchWidth := profile.width("Search: ")
	searchState := "empty"
	if p.searchFocus {
		searchState = "editing"
		if p.search != "" {
			searchState += " /" + p.search
		}
	} else if p.search != "" {
		searchState = "/" + p.search
	}
	searchWidth += profile.width(searchState)
	p.geometry.search = layoutRect{
		X: x, Y: y, Width: min(searchWidth, max(0, width-x)), Height: 1,
	}
}

func (p *TaskExplorerPanel) controlHintLine() string {
	commands := [...]taskExplorerCommand{
		taskExplorerCommandFilter,
		taskExplorerCommandSearch,
		taskExplorerCommandFocus,
	}
	parts := make([]string, 0, len(commands))
	for _, command := range commands {
		binding, ok := taskExplorerBindingFor(command)
		if !ok {
			continue
		}
		parts = append(parts, binding.keyLabel+" "+binding.label)
	}
	return strings.Join(parts, " · ")
}

func (p *TaskExplorerPanel) commandEnabled(command taskExplorerCommand) bool {
	switch command {
	case taskExplorerCommandSwitch:
		return p.actionAllowed(engine.TaskExplorerActionSwitch)
	case taskExplorerCommandSend:
		return p.actionAllowed(engine.TaskExplorerActionSend)
	case taskExplorerCommandPauseResume:
		return p.actionAllowed(engine.TaskExplorerActionPause) ||
			p.actionAllowed(engine.TaskExplorerActionResume)
	case taskExplorerCommandCancel:
		return p.actionAllowed(engine.TaskExplorerActionCancel)
	case taskExplorerCommandContinue:
		return p.actionAllowed(engine.TaskExplorerActionContinue)
	default:
		return true
	}
}

func (p *TaskExplorerPanel) helpLines(width int) []string {
	switch {
	case p.actionPrompt == taskExplorerActionPromptInput &&
		p.actionIntent != nil:
		return packTaskExplorerHints(
			p.environment.profile,
			[]string{"Type payload", "Enter submit", "Esc cancel", "Ctrl+U clear"},
			width,
		)
	case p.actionPrompt == taskExplorerActionPromptConfirm &&
		p.actionIntent != nil:
		return packTaskExplorerHints(
			p.environment.profile,
			[]string{"y/Enter confirm", "n/Esc cancel"},
			width,
		)
	case p.searchFocus:
		return packTaskExplorerHints(
			p.environment.profile,
			[]string{"Search", "Enter apply", "Esc leave search", "Ctrl+U clear"},
			width,
		)
	}

	parts := make([]string, 0, 12)
	if p.focus == taskExplorerFocusDetail {
		for _, command := range []taskExplorerCommand{
			taskExplorerCommandPreviousDetailTab,
			taskExplorerCommandNextDetailTab,
		} {
			binding, ok := taskExplorerBindingFor(command)
			if ok {
				parts = append(parts, binding.keyLabel+" "+binding.label)
			}
		}
		parts = append(
			parts,
			"Up/Down scroll",
			"PgUp/PgDown page",
			"Home/End bounds",
		)
	}

	commands := [...]taskExplorerCommand{
		taskExplorerCommandInspect,
		taskExplorerCommandSwitch,
		taskExplorerCommandSend,
		taskExplorerCommandPauseResume,
		taskExplorerCommandCancel,
		taskExplorerCommandContinue,
		taskExplorerCommandRefresh,
	}
	for _, command := range commands {
		binding, ok := taskExplorerBindingFor(command)
		if !ok {
			continue
		}
		label := binding.keyLabel + " " + binding.label
		if !p.commandEnabled(command) {
			label += " disabled"
		}
		parts = append(parts, label)
	}
	return packTaskExplorerHints(p.environment.profile, parts, width)
}

func packTaskExplorerHints(
	profile DisplayCellProfile,
	parts []string,
	width int,
) []string {
	width = max(1, width)
	lines := make([]string, 0, len(parts))
	current := ""
	for _, part := range parts {
		candidate := part
		if current != "" {
			candidate = current + " · " + part
		}
		if current == "" || profile.width(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = part
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func (p *TaskExplorerPanel) summaryLine() string {
	summary := summarizeTaskExplorer(p.snapshot)
	if summary.unavailable != "" {
		return "read-only · unavailable"
	}
	parts := []string{
		fmt.Sprintf("work %d/%d", summary.done, summary.total),
		fmt.Sprintf("live %d", summary.live),
		fmt.Sprintf("attention %d", summary.attention),
	}
	if summary.hidden > 0 {
		parts = append(parts, fmt.Sprintf("hidden %d", summary.hidden))
	}
	if hidden := p.localHiddenCount(); hidden > 0 {
		parts = append(parts, fmt.Sprintf("local hidden %d", hidden))
	}
	if len(p.snapshot.Links) > 0 {
		parts = append(parts, fmt.Sprintf("links %d", len(p.snapshot.Links)))
	}
	if len(p.snapshot.Diagnostics) > 0 {
		parts = append(
			parts,
			fmt.Sprintf("diagnostics %d", len(p.snapshot.Diagnostics)),
		)
	}
	if summary.activity != "" {
		parts = append(parts, summary.activity)
	}
	return strings.Join(parts, " · ")
}

func (p *TaskExplorerPanel) renderList(width, visible int) []string {
	if len(p.rows) == 0 {
		if p.filter != taskExplorerFilterAll {
			return []string{fmt.Sprintf(
				"No rows match %s filter",
				p.filter,
			)}
		}
		if p.section == taskExplorerMixed {
			return []string{"No matching WorkItems or executions"}
		}
		return []string{"No matching work"}
	}
	start, end, truncated := p.listWindow(visible)
	lines := make([]string, 0, end-p.offset+1)
	for index := start; index < end; index++ {
		prefix := "  "
		if index == p.cursor {
			prefix = "> "
		}
		lines = append(lines, prefix+p.rowLabel(p.rows[index], max(1, width-2)))
	}
	if truncated {
		lines = append(lines, fmt.Sprintf(
			"%d-%d of %d",
			start+1,
			end,
			len(p.rows),
		))
	}
	return lines
}

func (p *TaskExplorerPanel) listWindow(lineBudget int) (
	start, end int,
	truncated bool,
) {
	lineBudget = max(1, lineBudget)
	rowBudget := lineBudget
	if len(p.rows) > rowBudget {
		truncated = true
	}
	p.ensureVisible(rowBudget)
	start = p.offset
	end = min(len(p.rows), start+rowBudget)
	return start, end, truncated
}

func (p *TaskExplorerPanel) captureListGeometry(
	x, y, width, lineBudget int,
) {
	if len(p.rows) == 0 {
		return
	}
	start, end, _ := p.listWindow(lineBudget)
	p.geometry.list = layoutRect{
		X: x, Y: y, Width: width, Height: max(0, end-start),
	}
	p.geometry.listStart = start
	p.geometry.listCount = max(0, end-start)
}

func (p *TaskExplorerPanel) rowLabel(row taskExplorerRow, width int) string {
	text := ""
	if row.work != nil {
		kind := ""
		if p.section == taskExplorerMixed {
			kind = "WorkItem "
		}
		text = fmt.Sprintf(
			"%s%-11s %s",
			kind,
			firstNonEmptyTUIText(row.work.Status, "unknown"),
			firstNonEmptyTUIText(
				row.work.Title,
				row.work.ActiveForm,
				row.work.WorkItemID,
			),
		)
		if len(row.work.Attention) > 0 {
			text += fmt.Sprintf(" !%d", len(row.work.Attention))
		}
	} else if row.execution != nil {
		kind := ""
		if p.section == taskExplorerMixed {
			kind = "Execution "
		}
		text = fmt.Sprintf(
			"%s%-13s %s@g%d %s",
			kind,
			firstNonEmptyTUIText(
				string(row.execution.Phase),
				row.execution.Status,
				"unknown",
			),
			row.execution.Key.AgentID,
			row.execution.Key.Generation,
			firstNonEmptyTUIText(
				row.execution.Name,
				row.execution.Task,
				row.execution.Activity,
			),
		)
		if len(row.execution.Attention) > 0 {
			text += fmt.Sprintf(" !%d", len(row.execution.Attention))
		}
	}
	return modalEllipsize(p.environment.profile, text, width, 0, "...")
}

func (p *TaskExplorerPanel) renderInlineDetail(width int) []string {
	detail := p.renderDetail(width, 6)
	if len(detail) == 0 {
		return nil
	}
	return append([]string{"", "Selected"}, detail...)
}

func (p *TaskExplorerPanel) renderDetail(width, visible int) []string {
	visible = max(1, visible)
	body := p.detailBodyLines(width)
	bodyRows, showRange := taskExplorerDetailBodyCapacity(visible, len(body))
	maxOffset := max(0, len(body)-bodyRows)
	start := min(max(p.detailOffset, 0), maxOffset)
	end := min(len(body), start+bodyRows)
	lines := []string{p.detailHeader(width)}
	if bodyRows > 0 {
		lines = append(lines, body[start:end]...)
	}
	if showRange {
		lines = append(lines, fmt.Sprintf(
			"Rows %d-%d of %d",
			start+1,
			end,
			len(body),
		))
	}
	for index := range lines {
		lines[index] = modalEllipsize(
			p.environment.profile,
			lines[index],
			max(1, width),
			0,
			"...",
		)
	}
	return lines
}

func (p *TaskExplorerPanel) detailHeader(width int) string {
	kind := "Selection"
	if row, ok := p.selectedRow(); ok {
		switch {
		case row.work != nil:
			kind = "WorkItem"
		case row.execution != nil:
			kind = "Execution"
		}
	}
	maximum := p.maxDetailTab()
	tabLabels := make([]string, 0, int(maximum)+1)
	for tab := taskExplorerDetailOverview; tab <= maximum; tab++ {
		label := taskExplorerDetailTabLabel(tab)
		if tab == p.detailTab {
			label = "[" + label + "]"
		}
		tabLabels = append(tabLabels, label)
	}
	header := fmt.Sprintf(
		"Detail · %s · Tabs: %s",
		kind,
		strings.Join(tabLabels, " "),
	)
	if p.environment.profile.measure(header, 0) <= width {
		return header
	}
	compactTabs := "[" + taskExplorerDetailTabLabel(p.detailTab) + "]"
	switch p.detailTab {
	case taskExplorerDetailOverview:
		compactTabs = "[overview] activity"
	case taskExplorerDetailActivity:
		compactTabs = "overview [activity]"
	}
	return fmt.Sprintf("Detail · %s · Tabs: %s", kind, compactTabs)
}

func taskExplorerDetailBodyCapacity(visible, bodyLength int) (
	rows int,
	showRange bool,
) {
	if visible <= 1 || bodyLength == 0 {
		return 0, false
	}
	if bodyLength <= visible-1 {
		return bodyLength, false
	}
	if visible == 2 {
		return 1, false
	}
	return max(1, visible-2), true
}

func taskExplorerWorkItemOverviewLines(
	item engine.TaskExplorerWorkItem,
) []string {
	lines := []string{
		fmt.Sprintf("WorkItem %s/%s", item.BoardID, item.WorkItemID),
		fmt.Sprintf(
			"status %s · owner %s · revision %d · order %d",
			firstNonEmptyTUIText(item.Status, "unknown"),
			firstNonEmptyTUIText(item.Owner, "unassigned"),
			item.Revision,
			item.Order,
		),
	}
	lines = appendTaskExplorerDetailValue(lines, "description", item.Description)
	lines = appendTaskExplorerDetailValue(lines, "title", item.Title)
	lines = appendTaskExplorerDetailValue(lines, "active", item.ActiveForm)
	if len(item.BlockedBy) > 0 {
		lines = append(lines, "blocked by "+strings.Join(item.BlockedBy, ", "))
	}
	if len(item.Blocks) > 0 {
		lines = append(lines, "blocks "+strings.Join(item.Blocks, ", "))
	}
	if len(item.Attention) > 0 {
		lines = append(lines, "attention "+strings.Join(item.Attention, ", "))
	}
	lines = appendTaskExplorerDetailValue(lines, "terminal", item.TerminalReason)
	lines = appendTaskExplorerDetailValue(lines, "result", item.ResultSummary)
	return lines
}

func taskExplorerExecutionOverviewLines(
	execution engine.TaskExplorerExecution,
) []string {
	lines := []string{
		fmt.Sprintf(
			"Execution %s@g%d",
			execution.Key.AgentID,
			execution.Key.Generation,
		),
		fmt.Sprintf(
			"phase %s · status %s",
			firstNonEmptyTUIText(string(execution.Phase), "unknown"),
			firstNonEmptyTUIText(execution.Status, "unknown"),
		),
	}
	lines = appendTaskExplorerDetailValue(lines, "task", execution.Task)
	lines = appendTaskExplorerDetailValue(lines, "thread", execution.ThreadID)
	lines = appendTaskExplorerDetailValue(lines, "name", execution.Name)
	lines = appendTaskExplorerDetailValue(lines, "description", execution.Description)
	lines = appendTaskExplorerDetailValue(lines, "session", execution.SessionID)
	lines = appendTaskExplorerDetailValue(lines, "parent session", execution.ParentSessionID)
	lines = appendTaskExplorerDetailValue(lines, "parent thread", execution.ParentThreadID)
	lines = appendTaskExplorerDetailValue(lines, "parent agent", execution.ParentAgentID)
	lines = appendTaskExplorerDetailValue(lines, "parent tool", execution.ParentToolUseID)
	lines = appendTaskExplorerDetailValue(lines, "display", execution.DisplayMode)
	if execution.ReplayOnly {
		lines = append(lines, "read-only replay")
	}
	if execution.Predispatch {
		lines = append(lines, "predispatch")
	}
	return lines
}

func (p *TaskExplorerPanel) workItemActivityLines(
	item engine.TaskExplorerWorkItem,
) []string {
	lines := make([]string, 0)
	if item.LinkedLive {
		lines = append(lines, "linked live: yes")
	}
	for _, key := range item.ExecutionKeys {
		if strings.TrimSpace(key.AgentID) == "" || key.Generation <= 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"execution %s@g%d",
			key.AgentID,
			key.Generation,
		))
	}
	for _, link := range p.snapshot.Links {
		if link.BoardID != item.BoardID || link.WorkItemID != item.WorkItemID {
			continue
		}
		line := fmt.Sprintf(
			"link %s@g%d %s",
			link.AgentID,
			link.Generation,
			link.State,
		)
		if link.UnavailableReason != "" {
			line += " " + link.UnavailableReason
		}
		lines = append(lines, line)
	}
	// Attention and diagnostics do not carry BoardID. Fail closed when the
	// cached snapshot contains the same WorkItemID on more than one board;
	// only links have enough identity to remain attributable in that case.
	if p.workItemIDUnambiguous(item.WorkItemID) {
		for _, attention := range p.snapshot.Attention {
			if attention.WorkItemID == "" ||
				attention.WorkItemID != item.WorkItemID {
				continue
			}
			lines = append(lines, taskExplorerAttentionLine(attention))
		}
		for _, diagnostic := range p.snapshot.Diagnostics {
			if diagnostic.ItemID == "" || diagnostic.ItemID != item.WorkItemID {
				continue
			}
			lines = append(lines, fmt.Sprintf(
				"diagnostic %s %s",
				diagnostic.Kind,
				diagnostic.Message,
			))
		}
	}
	if len(lines) == 0 {
		return []string{"No cached execution activity for this WorkItem"}
	}
	return lines
}

func (p *TaskExplorerPanel) workItemIDUnambiguous(workItemID string) bool {
	if workItemID == "" {
		return false
	}
	matches := 0
	for _, candidate := range p.snapshot.WorkItems {
		if candidate.WorkItemID != workItemID {
			continue
		}
		matches++
		if matches > 1 {
			return false
		}
	}
	return matches == 1
}

func (p *TaskExplorerPanel) executionActivityLines(
	execution engine.TaskExplorerExecution,
) []string {
	lines := make([]string, 0)
	lines = appendTaskExplorerDetailValue(lines, "activity", execution.Activity)
	lines = appendTaskExplorerDetailValue(lines, "last tool", execution.LastToolName)
	if execution.ToolUseCount > 0 || execution.TokenCount > 0 {
		lines = append(lines, fmt.Sprintf(
			"tool uses %d · tokens %d",
			execution.ToolUseCount,
			execution.TokenCount,
		))
	}
	if execution.OrdinalPresent {
		lines = append(lines, fmt.Sprintf(
			"observation ordinal %d",
			execution.ObservationOrdinal,
		))
	}
	for _, attention := range execution.Attention {
		if attention != "" {
			lines = append(lines, "attention "+attention)
		}
	}
	for _, attention := range p.snapshot.Attention {
		if attention.AgentID == "" || attention.Generation <= 0 ||
			attention.AgentID != execution.Key.AgentID ||
			attention.Generation != execution.Key.Generation {
			continue
		}
		lines = append(lines, taskExplorerAttentionLine(attention))
	}
	for _, link := range p.snapshot.Links {
		if link.AgentID != execution.Key.AgentID ||
			link.Generation != execution.Key.Generation {
			continue
		}
		line := fmt.Sprintf("link %s/%s %s", link.BoardID, link.WorkItemID, link.State)
		if link.UnavailableReason != "" {
			line += " " + link.UnavailableReason
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []string{"No cached activity for this exact execution"}
	}
	return lines
}

func taskExplorerAttentionLine(attention engine.TaskExplorerAttention) string {
	prefix := "attention"
	if attention.Category != "" {
		prefix += " " + attention.Category
	}
	if attention.Reason != "" {
		return prefix + ": " + attention.Reason
	}
	return prefix
}

func appendTaskExplorerDetailValue(
	lines []string,
	label, value string,
) []string {
	if value == "" {
		return lines
	}
	return append(lines, label+" "+value)
}

func (p *TaskExplorerPanel) renderSplit(width, visible int) []string {
	leftWidth := min(72, max(40, width/2))
	rightWidth := max(1, width-leftWidth-3)
	visible = max(1, visible)
	left := p.renderList(leftWidth, visible)
	right := p.renderDetail(rightWidth, visible)
	count := max(len(left), len(right))
	lines := make([]string, 0, count)
	profile := p.environment.profile
	for index := 0; index < count; index++ {
		leftLine := ""
		if index < len(left) {
			leftLine = left[index]
		}
		rightLine := ""
		if index < len(right) {
			rightLine = right[index]
		}
		lines = append(
			lines,
			profile.padAligned(leftLine, leftWidth, "left", 0)+
				" │ "+
				profile.truncateAt(rightLine, rightWidth, leftWidth+3),
		)
	}
	return lines
}

func (p *TaskExplorerPanel) captureWideBodyGeometry(
	width, y, visible int,
) {
	leftWidth := min(72, max(40, width/2))
	p.captureListGeometry(0, y, leftWidth, visible)
	detail := p.renderDetail(max(1, width-leftWidth-3), visible)
	p.geometry.detail = layoutRect{
		X:      leftWidth + 3,
		Y:      y,
		Width:  max(1, width-leftWidth-3),
		Height: len(detail),
	}
	p.geometry.detailRows = p.renderedDetailBodyRows(
		max(1, width-leftWidth-3),
		len(detail),
	)
}

func (p *TaskExplorerPanel) renderedDetailBodyRows(width, visible int) int {
	rows, _ := taskExplorerDetailBodyCapacity(
		visible,
		len(p.detailBodyLines(width)),
	)
	return rows
}

func taskExplorerRectContains(rect layoutRect, x, y int) bool {
	return rect.Width > 0 && rect.Height > 0 &&
		x >= rect.X && x < rect.X+rect.Width &&
		y >= rect.Y && y < rect.Y+rect.Height
}

func (p *TaskExplorerPanel) invalidateGeometry() {
	p.geometry = taskExplorerGeometry{}
}

// HandleMouse consumes Task Explorer pointer input using geometry published by
// the latest render. Pointer input changes only panel-local presentation state;
// engine actions remain explicit keyboard commands.
func (p *TaskExplorerPanel) HandleMouse(msg tuiMouseMsg) bool {
	if p == nil {
		return false
	}
	if p.actionPrompt != taskExplorerActionPromptNone {
		return true
	}
	if msg.Action != mouseActionPress {
		return true
	}
	if msg.Button == tea.MouseWheelUp || msg.Button == tea.MouseWheelDown {
		delta := -3
		if msg.Button == tea.MouseWheelDown {
			delta = 3
		}
		switch {
		case taskExplorerRectContains(p.geometry.detail, msg.X, msg.Y):
			p.moveDetail(delta, max(1, p.lastHeight))
			p.setFocus(taskExplorerFocusDetail)
		case taskExplorerRectContains(p.geometry.list, msg.X, msg.Y):
			p.setFocus(taskExplorerFocusList)
			p.move(delta, max(1, p.lastHeight))
		}
		return true
	}
	if msg.Button != tea.MouseLeft {
		return true
	}
	for _, filter := range taskExplorerFilters {
		if !taskExplorerRectContains(
			p.geometry.filterRects[filter],
			msg.X,
			msg.Y,
		) {
			continue
		}
		p.filter = filter
		p.setFocus(taskExplorerFocusControls)
		p.refilter()
		return true
	}
	if taskExplorerRectContains(p.geometry.search, msg.X, msg.Y) {
		p.setFocus(taskExplorerFocusControls)
		p.searchFocus = true
		return true
	}
	if taskExplorerRectContains(p.geometry.list, msg.X, msg.Y) {
		index := p.geometry.listStart + msg.Y - p.geometry.list.Y
		if index >= p.geometry.listStart &&
			index < p.geometry.listStart+p.geometry.listCount &&
			index >= 0 && index < len(p.rows) {
			previous := p.selection
			p.cursor = index
			p.selection = p.rows[index].selection
			if p.selection != previous {
				p.resetDetailProjection()
			}
			p.setFocus(taskExplorerFocusList)
		}
		return true
	}
	if taskExplorerRectContains(p.geometry.detail, msg.X, msg.Y) {
		p.setFocus(taskExplorerFocusDetail)
		return true
	}
	if taskExplorerRectContains(p.geometry.controls, msg.X, msg.Y) {
		p.setFocus(taskExplorerFocusControls)
	}
	return true
}

func boundedExplorerLines(
	profile DisplayCellProfile,
	lines []string,
	width, height int,
) string {
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = profile.truncateAt(lines[index], width, 0)
	}
	return strings.Join(lines, "\n")
}

type taskExplorerTickMsg struct{}

func taskExplorerTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return taskExplorerTickMsg{}
	})
}
