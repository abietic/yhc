package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	planTaskHistoryHeadLines = 4
	planTaskHistoryTailLines = 3
)

type planTaskTodoToolHistoryRenderer struct{}

func (planTaskTodoToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	if state.Name == "Task" && isLegacyAgentTaskHistoryInput(state.Input) {
		state.Name = "Agent"
		return agentHistoryRenderer.Render(state)
	}
	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderPlanTaskTodoTranscript(state)
	}
	switch state.Name {
	case "EnterPlanMode", "ExitPlanMode":
		return renderPlanHistory(state)
	case "TodoWrite":
		return renderTodoHistory(state)
	default:
		return renderTaskHistory(state)
	}
}

type planHistoryView struct {
	label           string
	labelStatus     ToolStatus
	visualStatus    ToolStatus
	path            string
	plan            string
	note            string
	requestID       string
	permissionCount int
}

func renderPlanHistory(state toolHistoryRenderState) string {
	view := parsePlanHistoryView(state)
	details := make([]string, 0, 2)
	if view.path != "" {
		details = append(details, shortenPath(view.path))
	}
	if view.permissionCount > 0 {
		details = append(details, fmt.Sprintf("%d permissions", view.permissionCount))
	}
	header := renderPlanTaskHeader(state, "Plan", view.label, view.labelStatus, view.visualStatus, details)
	if state.compact() {
		return header
	}
	if state.fullOutput() {
		return renderPlanTaskTodoExpanded(state, header, view.visualStatus)
	}

	body := renderPlanHistoryBody(state, view)
	if body == "" {
		return header
	}
	bodyWidth := max(10, state.Context.Width-5)
	body = boundPlanTaskHistoryPreview(
		state.profile(),
		body,
		planTaskHistoryHeadLines,
		planTaskHistoryTailLines,
		bodyWidth,
	)
	bodyStatus := ToolSuccess
	if view.visualStatus == ToolError {
		bodyStatus = ToolError
	}
	return truncateHistoryRenderLinesWithProfile(
		state.profile(),
		header+"\n"+renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			body,
			bodyWidth,
			bodyStatus,
		),
		state.Context.Width,
	)
}

func parsePlanHistoryView(state toolHistoryRenderState) planHistoryView {
	view := planHistoryView{visualStatus: state.DisplayStatus, labelStatus: state.DisplayStatus}
	view.permissionCount = planHistoryPermissionCount(state.Input)
	if state.Status == ToolError || state.DisplayStatus == ToolError {
		view.label = "failed"
		view.labelStatus = ToolError
		view.visualStatus = ToolError
		return view
	}

	if state.Name == "EnterPlanMode" {
		view.path = historyLineValue(state.Output, "You should create or edit your plan at:")
		switch state.DisplayStatus {
		case ToolRunning:
			view.label = "entering"
			view.labelStatus = ToolRunning
		case ToolPending:
			view.label = "pending"
			view.labelStatus = ToolPending
		default:
			view.label = "entered"
			view.labelStatus = ToolSuccess
			view.visualStatus = ToolSuccess
			view.note = "Exploring and designing an implementation approach."
		}
		return view
	}

	if state.DisplayStatus == ToolRunning || state.DisplayStatus == ToolPending {
		view.label = "requesting approval"
		view.labelStatus = state.DisplayStatus
		return view
	}

	if parseStructuredPlanHistoryOutput(state.Output, &view) {
		return view
	}
	lower := strings.ToLower(state.Output)
	switch {
	case strings.Contains(lower, "submitted to the team lead for approval"):
		view.label = "submitted"
		view.note = "Waiting for team lead approval."
		view.path = historyLineValue(state.Output, "Plan file:")
		view.requestID = historyLineValue(state.Output, "Request ID:")
	case strings.Contains(lower, "approved your plan"):
		view.label = "approved"
		view.path = historyLineValue(state.Output, "Your plan has been saved to:")
		view.plan = historySectionValue(state.Output, "## Approved Plan:", "## Granted Permissions:")
	case strings.Contains(lower, "approved exiting plan mode"):
		view.label = "exited"
	case strings.Contains(lower, "declined") || strings.Contains(lower, "rejected"):
		view.label = "rejected"
		view.labelStatus = ToolError
		view.visualStatus = ToolError
	default:
		view.label = "exited"
	}
	if view.labelStatus != ToolError {
		view.labelStatus = ToolSuccess
		view.visualStatus = ToolSuccess
	}
	return view
}

func planHistoryPermissionCount(input string) int {
	var params struct {
		AllowedPrompts []any `json:"allowedPrompts"`
	}
	if json.Unmarshal([]byte(input), &params) != nil {
		return 0
	}
	return len(params.AllowedPrompts)
}

func parseStructuredPlanHistoryOutput(output string, view *planHistoryView) bool {
	var value struct {
		Plan                   string `json:"plan"`
		FilePath               string `json:"filePath"`
		AwaitingLeaderApproval bool   `json:"awaitingLeaderApproval"`
		RequestID              string `json:"requestId"`
	}
	if json.Unmarshal([]byte(output), &value) != nil ||
		(value.Plan == "" && value.FilePath == "" && value.RequestID == "" && !value.AwaitingLeaderApproval) {
		return false
	}
	view.plan = value.Plan
	view.path = value.FilePath
	view.requestID = value.RequestID
	view.labelStatus = ToolSuccess
	view.visualStatus = ToolSuccess
	if value.AwaitingLeaderApproval {
		view.label = "submitted"
		view.note = "Waiting for team lead approval."
	} else if strings.TrimSpace(value.Plan) == "" {
		view.label = "exited"
	} else {
		view.label = "approved"
	}
	return true
}

func renderPlanHistoryBody(state toolHistoryRenderState, view planHistoryView) string {
	if view.visualStatus == ToolError {
		return state.Output
	}
	parts := make([]string, 0, 4)
	if view.note != "" {
		parts = append(parts, view.note)
	}
	if view.path != "" {
		parts = append(parts, "Plan file: "+shortenPath(view.path))
	}
	if strings.TrimSpace(view.plan) != "" {
		parts = append(parts, strings.TrimSpace(view.plan))
	}
	if view.requestID != "" {
		parts = append(parts, "Request: "+view.requestID)
	}
	return strings.Join(parts, "\n")
}

type taskHistoryInput struct {
	action      string
	id          string
	subject     string
	description string
	activeForm  string
	status      string
	owner       string
	filter      string
	block       *bool
	parseError  bool
}

type taskHistoryListEntry struct {
	id      string
	status  string
	subject string
	blocked bool
}

type taskHistoryResult struct {
	id          string
	subject     string
	status      string
	taskType    string
	retrieval   string
	description string
	owner       string
	output      string
	error       string
	fields      string
	entries     []taskHistoryListEntry
	noTasks     bool
}

func renderTaskHistory(state toolHistoryRenderState) string {
	input := parseTaskHistoryInput(state.Name, state.Input)
	result := parseTaskHistoryResult(input.action, state.Output)
	if input.id == "" {
		input.id = result.id
	}
	label, labelStatus, visualStatus := taskHistoryStatus(state, input, result)
	details := taskHistoryHeaderDetails(state.profile(), input, result)
	header := renderPlanTaskHeader(state, "Task", label, labelStatus, visualStatus, details)
	if state.compact() {
		return header
	}
	if state.fullOutput() {
		return renderPlanTaskTodoExpanded(state, header, visualStatus)
	}
	body := renderTaskHistoryBody(state, input, result)
	if body == "" {
		return header
	}
	bodyWidth := max(10, state.Context.Width-5)
	body = boundPlanTaskHistoryPreview(
		state.profile(),
		body,
		planTaskHistoryHeadLines,
		planTaskHistoryTailLines,
		bodyWidth,
	)
	bodyStatus := ToolSuccess
	if visualStatus == ToolError {
		bodyStatus = ToolError
	}
	return truncateHistoryRenderLinesWithProfile(
		state.profile(),
		header+"\n"+renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			body,
			bodyWidth,
			bodyStatus,
		),
		state.Context.Width,
	)
}

func parseTaskHistoryInput(name, input string) taskHistoryInput {
	parsed := taskHistoryInput{action: taskHistoryActionForName(name)}
	if input == "" || input == "{}" {
		return parsed
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		parsed.parseError = true
		return parsed
	}
	if name == "Task" {
		parsed.action = strings.ToLower(historyStringValue(params, "action"))
		switch parsed.action {
		case "cancel":
			parsed.action = "stop"
		case "monitor":
			parsed.action = "output"
		}
	}
	parsed.id = firstNonEmpty(
		historyStringValue(params, "task_id"),
		historyStringValue(params, "taskId"),
		historyStringValue(params, "shell_id"),
	)
	parsed.subject = historyStringValue(params, "subject")
	parsed.description = historyStringValue(params, "description")
	parsed.activeForm = firstNonEmpty(historyStringValue(params, "active_form"), historyStringValue(params, "activeForm"))
	parsed.status = historyStringValue(params, "status")
	parsed.owner = historyStringValue(params, "owner")
	if parsed.action == "list" {
		parsed.filter = parsed.status
	}
	if block, ok := params["block"].(bool); ok {
		parsed.block = &block
	}
	return parsed
}

func taskHistoryActionForName(name string) string {
	switch name {
	case "TaskCreate":
		return "create"
	case "TaskGet":
		return "get"
	case "TaskList":
		return "list"
	case "TaskUpdate":
		return "update"
	case "TaskStop":
		return "stop"
	case "TaskOutput":
		return "output"
	default:
		return ""
	}
}

func isLegacyAgentTaskHistoryInput(input string) bool {
	var params map[string]any
	if json.Unmarshal([]byte(input), &params) != nil || historyStringValue(params, "action") != "" {
		return false
	}
	return historyStringValue(params, "prompt") != "" ||
		historyStringValue(params, "subagent_type") != "" ||
		historyStringValue(params, "agent_type") != ""
}

func parseTaskHistoryResult(action, output string) taskHistoryResult {
	var result taskHistoryResult
	switch action {
	case "create":
		const prefix = "Task #"
		const separator = " created successfully: "
		if strings.HasPrefix(output, prefix) {
			parts := strings.SplitN(strings.TrimPrefix(output, prefix), separator, 2)
			if len(parts) == 2 {
				result.id, result.subject = parts[0], parts[1]
			}
		}
	case "update":
		parseTaskUpdateHistoryResult(output, &result)
	case "get":
		parseTaskGetHistoryResult(output, &result)
	case "list":
		parseTaskListHistoryResult(output, &result)
	case "stop":
		result.id = historyLineValue(output, "task_id:")
		result.taskType = historyLineValue(output, "task_type:")
	case "output":
		result.retrieval = historyXMLValue(output, "retrieval_status")
		result.id = historyXMLValue(output, "task_id")
		result.taskType = historyXMLValue(output, "task_type")
		result.status = historyXMLValue(output, "status")
		result.output = historyXMLValue(output, "output")
		result.error = historyXMLValue(output, "error")
	}
	return result
}

func parseTaskUpdateHistoryResult(output string, result *taskHistoryResult) {
	const prefix = "Task #"
	if !strings.HasPrefix(output, prefix) {
		return
	}
	rest := strings.TrimPrefix(output, prefix)
	for _, separator := range []string{" updated: ", " unchanged"} {
		if index := strings.Index(rest, separator); index >= 0 {
			result.id = rest[:index]
			if separator == " updated: " {
				result.fields = rest[index+len(separator):]
			}
			return
		}
	}
}

func parseTaskGetHistoryResult(output string, result *taskHistoryResult) {
	for index, line := range strings.Split(output, "\n") {
		if index == 0 && strings.HasPrefix(line, "Task #") {
			rest := strings.TrimPrefix(line, "Task #")
			parts := strings.SplitN(rest, ": ", 2)
			if len(parts) == 2 {
				result.id, result.subject = parts[0], parts[1]
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "Status: "):
			result.status = strings.TrimPrefix(line, "Status: ")
		case strings.HasPrefix(line, "Description: "):
			result.description = strings.TrimPrefix(line, "Description: ")
		case strings.HasPrefix(line, "Owner: "):
			result.owner = strings.TrimPrefix(line, "Owner: ")
		}
	}
	if strings.TrimSpace(output) == "Task not found" {
		result.noTasks = true
	}
}

func parseTaskListHistoryResult(output string, result *taskHistoryResult) {
	if strings.HasPrefix(strings.TrimSpace(output), "No tasks found") {
		result.noTasks = true
		return
	}
	for _, line := range strings.Split(output, "\n") {
		entry, ok := parseTaskListHistoryEntry(line)
		if ok {
			result.entries = append(result.entries, entry)
		}
	}
}

func parseTaskListHistoryEntry(line string) (taskHistoryListEntry, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#") {
		return taskHistoryListEntry{}, false
	}
	space := strings.IndexByte(line, ' ')
	if space <= 1 {
		return taskHistoryListEntry{}, false
	}
	entry := taskHistoryListEntry{id: line[1:space]}
	rest := strings.TrimSpace(line[space+1:])
	if strings.HasPrefix(rest, "[") {
		if closeIndex := strings.Index(rest, "]"); closeIndex > 1 {
			entry.status = rest[1:closeIndex]
			rest = strings.TrimSpace(rest[closeIndex+1:])
		}
	}
	entry.subject = rest
	entry.blocked = strings.Contains(rest, "[blocked by ")
	return entry, true
}

func taskHistoryStatus(
	state toolHistoryRenderState,
	input taskHistoryInput,
	result taskHistoryResult,
) (string, ToolStatus, ToolStatus) {
	if state.Status == ToolError || state.DisplayStatus == ToolError {
		return "failed", ToolError, ToolError
	}
	if state.DisplayStatus == ToolRunning || state.DisplayStatus == ToolPending {
		label := map[string]string{
			"create": "creating", "get": "reading", "list": "listing", "update": "updating",
			"stop": "stopping", "output": "waiting",
		}[input.action]
		if input.action == "output" && input.block != nil && !*input.block {
			label = "checking"
		}
		if label == "" {
			label = "running"
		}
		return label, state.DisplayStatus, state.DisplayStatus
	}
	if input.action != "stop" && (result.status == "failed" || result.status == "killed" || result.error != "") {
		return historyTaskStatusLabel(result.status), ToolError, ToolError
	}
	switch input.action {
	case "create":
		return "created", ToolSuccess, ToolSuccess
	case "get":
		if result.noTasks {
			return "not found", ToolError, ToolError
		}
		if result.status != "" {
			return historyTaskStatusLabel(result.status), ToolSuccess, ToolSuccess
		}
		return "read", ToolSuccess, ToolSuccess
	case "list":
		if result.noTasks {
			return "no tasks", ToolSuccess, ToolSuccess
		}
		return fmt.Sprintf("%d tasks", len(result.entries)), ToolSuccess, ToolSuccess
	case "update":
		if input.status != "" {
			return historyTaskStatusLabel(input.status), ToolSuccess, ToolSuccess
		}
		return "updated", ToolSuccess, ToolSuccess
	case "stop":
		return "stopped", ToolSuccess, ToolSuccess
	case "output":
		switch result.retrieval {
		case "timeout":
			return "timed out", ToolPending, ToolPending
		case "not_ready":
			return "still running", ToolRunning, ToolRunning
		}
		if result.status != "" {
			return historyTaskStatusLabel(result.status), ToolSuccess, ToolSuccess
		}
		return "checked", ToolSuccess, ToolSuccess
	default:
		return "done", ToolSuccess, ToolSuccess
	}
}

func taskHistoryHeaderDetails(
	profile DisplayCellProfile,
	input taskHistoryInput,
	result taskHistoryResult,
) []string {
	details := make([]string, 0, 3)
	id := firstNonEmpty(input.id, result.id)
	if id != "" {
		if !strings.HasPrefix(id, "#") {
			id = "#" + id
		}
		details = append(details, id)
	}
	if input.action == "list" {
		if input.filter != "" {
			details = append(details, "filter "+historyTaskStatusLabel(input.filter))
		}
		if summary := taskHistoryListSummary(result.entries); summary != "" {
			details = append(details, summary)
		}
	} else if subject := firstNonEmpty(input.subject, result.subject); subject != "" {
		details = append(details, truncateSingleLineWithProfile(profile, subject, 80))
	}
	if input.parseError {
		details = append(details, "invalid input")
	}
	return details
}

func taskHistoryListSummary(entries []taskHistoryListEntry) string {
	if len(entries) == 0 {
		return ""
	}
	active, done, blocked := 0, 0, 0
	for _, entry := range entries {
		switch entry.status {
		case "in_progress", "running":
			active++
		case "completed":
			done++
		}
		if entry.blocked {
			blocked++
		}
	}
	parts := make([]string, 0, 3)
	if active > 0 {
		parts = append(parts, fmt.Sprintf("%d active", active))
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d done", done))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	return strings.Join(parts, " · ")
}

func renderTaskHistoryBody(state toolHistoryRenderState, input taskHistoryInput, result taskHistoryResult) string {
	if state.Status == ToolError || state.DisplayStatus == ToolError {
		return state.Output
	}
	switch input.action {
	case "create":
		parts := make([]string, 0, 2)
		if input.description != "" {
			parts = append(parts, input.description)
		}
		if input.activeForm != "" {
			parts = append(parts, "Active: "+input.activeForm)
		}
		return strings.Join(parts, "\n")
	case "get":
		return state.Output
	case "list":
		if result.noTasks {
			return state.Output
		}
		lines := make([]string, 0, len(result.entries))
		for _, entry := range result.entries {
			lines = append(lines, taskHistoryListMarker(entry.status)+" #"+entry.id+" "+entry.subject)
		}
		return strings.Join(lines, "\n")
	case "update":
		return state.Output
	case "stop":
		return ""
	case "output":
		switch result.retrieval {
		case "timeout":
			return "Timed out waiting for task; current status: " + firstNonEmpty(result.status, "unknown")
		case "not_ready":
			return "Task is still running."
		}
		if result.taskType == "local_agent" && result.output != "" {
			return "Agent output available (expand for details)"
		}
		parts := make([]string, 0, 2)
		if result.output != "" {
			parts = append(parts, result.output)
		}
		if result.error != "" {
			parts = append(parts, "Error: "+result.error)
		}
		if len(parts) == 0 && result.status != "" {
			parts = append(parts, "Status: "+historyTaskStatusLabel(result.status))
		}
		return strings.Join(parts, "\n")
	default:
		return state.Output
	}
}

func taskHistoryListMarker(status string) string {
	switch status {
	case "completed":
		return "[x]"
	case "in_progress", "running":
		return "[~]"
	case "failed":
		return "[!]"
	case "killed":
		return "[-]"
	default:
		return "[ ]"
	}
}

func historyTaskStatusLabel(status string) string {
	status = strings.TrimSpace(strings.ToLower(status))
	if status == "" {
		return "failed"
	}
	return strings.ReplaceAll(status, "_", " ")
}

type todoHistoryItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

type todoHistoryInput struct {
	items      []todoHistoryItem
	parseError bool
}

func renderTodoHistory(state toolHistoryRenderState) string {
	input := parseTodoHistoryInput(state.Input)
	done, active := 0, ""
	for _, item := range input.items {
		if item.Status == "completed" {
			done++
		}
		if item.Status == "in_progress" && active == "" {
			active = firstNonEmpty(item.ActiveForm, item.Content)
		}
	}
	visualStatus := state.DisplayStatus
	labelStatus := state.DisplayStatus
	label := "updated"
	if state.Status == ToolError || state.DisplayStatus == ToolError {
		label, labelStatus, visualStatus = "failed", ToolError, ToolError
	} else if state.DisplayStatus == ToolRunning || state.DisplayStatus == ToolPending {
		label = "updating"
	} else if len(input.items) > 0 && done == len(input.items) {
		label, labelStatus, visualStatus = "completed all", ToolSuccess, ToolSuccess
	} else {
		labelStatus, visualStatus = ToolSuccess, ToolSuccess
	}
	details := []string{fmt.Sprintf("%d/%d", done, len(input.items))}
	if active != "" {
		details = append(details, truncateSingleLineWithProfile(state.profile(), active, 80))
	}
	if input.parseError {
		details = append(details, "invalid input")
	}
	header := renderPlanTaskHeader(state, "To-Do", label, labelStatus, visualStatus, details)
	if state.compact() {
		return header
	}
	if state.fullOutput() {
		return renderPlanTaskTodoExpanded(state, header, visualStatus)
	}

	var body string
	if visualStatus == ToolError {
		body = state.Output
	} else if input.parseError {
		body = state.Input
	} else {
		body = renderTodoHistoryItems(input.items)
	}
	if body == "" {
		return header
	}
	bodyWidth := max(10, state.Context.Width-5)
	body = boundPlanTaskHistoryPreview(state.profile(), body, 4, 4, bodyWidth)
	bodyStatus := ToolSuccess
	if visualStatus == ToolError {
		bodyStatus = ToolError
	}
	return truncateHistoryRenderLinesWithProfile(
		state.profile(),
		header+"\n"+renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			body,
			bodyWidth,
			bodyStatus,
		),
		state.Context.Width,
	)
}

func parseTodoHistoryInput(input string) todoHistoryInput {
	var params struct {
		Todos []todoHistoryItem `json:"todos"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return todoHistoryInput{parseError: true}
	}
	return todoHistoryInput{items: params.Todos}
}

func renderTodoHistoryItems(items []todoHistoryItem) string {
	if len(items) == 0 {
		return "Todo list is empty."
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		text := item.Content
		if item.Status == "in_progress" && item.ActiveForm != "" {
			text = item.ActiveForm
		}
		lines = append(lines, taskHistoryListMarker(item.Status)+" "+text)
	}
	return strings.Join(lines, "\n")
}

func renderPlanTaskHeader(
	state toolHistoryRenderState,
	displayName string,
	label string,
	labelStatus ToolStatus,
	visualStatus ToolStatus,
	details []string,
) string {
	styles := state.Context.Styles
	header := toolIcon(styles, visualStatus, state.SpinnerCount) + " " + toolNameStyled(styles, displayName)
	labelStyle := styles.Subtle
	switch labelStatus {
	case ToolRunning:
		labelStyle = styles.ToolRunning
	case ToolSuccess:
		labelStyle = styles.ToolSuccess
	case ToolError:
		labelStyle = styles.ToolError
	}
	header += " " + labelStyle.Render(label)
	if len(details) > 0 {
		header += " " + styles.Subtle.Render("("+strings.Join(details, " · ")+")")
	}
	return contentEllipsize(state.profile(), header, state.Context.Width, 0, "…")
}

func renderPlanTaskTodoExpanded(state toolHistoryRenderState, header string, visualStatus ToolStatus) string {
	bodyWidth := max(10, state.Context.Width-5)
	sections := make([]string, 0, 2)
	if state.Input != "" && state.Input != "{}" {
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			wrapPlanTaskHistoryContent(
				state.profile(),
				"Input:\n"+prettyPlanTaskHistoryJSONText(state.Input),
				bodyWidth,
				state.Context.selection,
			),
			bodyWidth,
			ToolSuccess,
		))
	}
	if state.Output != "" {
		status := ToolSuccess
		if visualStatus == ToolError {
			status = ToolError
		}
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			wrapPlanTaskHistoryContent(
				state.profile(),
				"Result:\n"+state.Output,
				bodyWidth,
				state.Context.selection,
			),
			bodyWidth,
			status,
		))
	}
	if len(sections) == 0 {
		return header
	}
	return truncateHistoryRenderLinesWithProfile(
		state.profile(),
		header+"\n"+strings.Join(sections, "\n"),
		state.Context.Width,
	)
}

func renderPlanTaskTodoTranscript(state toolHistoryRenderState) string {
	parts := []string{state.Name, "Status: " + genericHistoryToolStatus(state)}
	if state.Input != "" {
		parts = append(parts, "Input:\n"+prettyPlanTaskHistoryJSONText(state.Input))
	}
	if state.Output != "" {
		parts = append(parts, "Result:\n"+state.Output)
	}
	return ansi.Strip(strings.TrimSpace(strings.Join(parts, "\n")))
}

func genericHistoryToolStatus(state toolHistoryRenderState) string {
	switch state.DisplayStatus {
	case ToolRunning:
		return "running"
	case ToolPending:
		return "pending"
	case ToolError:
		return "failed"
	default:
		return "completed"
	}
}

func boundPlanTaskHistoryPreview(
	profile DisplayCellProfile,
	content string,
	head, tail, width int,
) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	limit := head + tail
	if len(lines) > limit {
		bounded := make([]string, 0, limit+1)
		bounded = append(bounded, lines[:head]...)
		bounded = append(bounded, fmt.Sprintf("… +%d lines (expand for details)", len(lines)-limit))
		bounded = append(bounded, lines[len(lines)-tail:]...)
		lines = bounded
	}
	clipped := false
	for i, line := range lines {
		if profile.measure(line, 0) > width {
			lines[i] = contentEllipsize(profile, line, width, 0, "…")
			clipped = true
		}
	}
	if clipped {
		lines = append(lines, "… content clipped (expand for details)")
	}
	return strings.Join(lines, "\n")
}

func wrapPlanTaskHistoryContent(
	profile DisplayCellProfile,
	content string,
	width int,
	selection bool,
) string {
	if selection {
		if annotated, ok := selectionAnnotatedContentWrap(
			profile,
			content,
			width,
			5,
		); ok {
			return annotated
		}
	}
	lines := strings.Split(content, "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			wrapped = append(wrapped, "")
			continue
		}
		wrapped = append(wrapped, contentWrapLines(profile, line, width, 5)...)
	}
	return strings.Join(wrapped, "\n")
}

func prettyPlanTaskHistoryJSONText(value string) string {
	var decoded any
	if json.Unmarshal([]byte(value), &decoded) != nil {
		return value
	}
	encoded, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return value
	}
	return string(encoded)
}

func historyStringValue(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

func historyLineValue(content, prefix string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return ""
}

func historySectionValue(content, startMarker, endMarker string) string {
	start := strings.Index(content, startMarker)
	if start < 0 {
		return ""
	}
	section := content[start+len(startMarker):]
	if endMarker != "" {
		if end := strings.Index(section, endMarker); end >= 0 {
			section = section[:end]
		}
	}
	return strings.TrimSpace(section)
}

func historyXMLValue(content, tag string) string {
	startTag, endTag := "<"+tag+">", "</"+tag+">"
	start := strings.Index(content, startTag)
	if start < 0 {
		return ""
	}
	value := content[start+len(startTag):]
	end := strings.Index(value, endTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(value[:end])
}
