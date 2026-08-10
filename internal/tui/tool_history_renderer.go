package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	genericHistoryHeadLines      = 4
	genericHistoryTailLines      = 3
	genericHistoryMaxLineColumns = 240
)

type toolHistoryRenderState struct {
	Context       HistoryRenderContext
	Name          string
	Input         string
	Output        string
	Status        ToolStatus
	DisplayStatus ToolStatus
	Expanded      bool
	SpinnerCount  int
	AgentTrace    *agentToolTrace
}

func (s toolHistoryRenderState) normalized() toolHistoryRenderState {
	s.Context = s.Context.normalized()
	return s
}

func (s toolHistoryRenderState) profile() DisplayCellProfile {
	return s.Context.displayCellProfile()
}

func (s toolHistoryRenderState) fullOutput() bool {
	return s.Expanded || s.Context.Mode == HistoryRenderExpanded ||
		s.Context.Mode == HistoryRenderRaw || s.Context.Mode == HistoryRenderTranscript
}

func (s toolHistoryRenderState) compact() bool {
	return s.Context.Mode == HistoryRenderCompact
}

type toolHistoryRenderer interface {
	Render(toolHistoryRenderState) string
}

type genericToolHistoryRenderer struct{}

func (genericToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderGenericHistoryTranscript(state)
	}
	header := renderGenericHistoryHeader(state)
	if state.compact() {
		return header
	}
	if state.fullOutput() {
		return renderGenericHistoryExpanded(state, header)
	}
	content := state.Output
	if content == "" && state.DisplayStatus != ToolRunning && state.DisplayStatus != ToolPending {
		content = "(no content)"
	}
	if content == "" {
		return header
	}
	bodyWidth := max(10, state.Context.Width-5)
	body := genericHistoryPreview(state.profile(), content, bodyWidth)
	bodyStatus := state.Status
	if state.DisplayStatus == ToolError {
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

func renderGenericHistoryHeader(state toolHistoryRenderState) string {
	styles := state.Context.Styles
	visualStatus := state.DisplayStatus
	if state.Status == ToolError {
		visualStatus = ToolError
	}
	label := genericHistoryToolStatus(state)
	header := toolIcon(styles, visualStatus, state.SpinnerCount) + " " + toolNameStyled(styles, state.Name)
	labelStyle := styles.Subtle
	switch visualStatus {
	case ToolRunning:
		labelStyle = styles.ToolRunning
	case ToolSuccess:
		labelStyle = styles.ToolSuccess
	case ToolError:
		labelStyle = styles.ToolError
	}
	header += " " + labelStyle.Render(label)
	if args := formatToolArgsWithProfile(state.profile(), state.Name, state.Input); args != "" {
		header += " " + styles.Subtle.Render("("+args+")")
	}
	return contentEllipsize(state.profile(), header, state.Context.Width, 0, "…")
}

func renderGenericHistoryExpanded(state toolHistoryRenderState, header string) string {
	bodyWidth := max(10, state.Context.Width-5)
	sections := make([]string, 0, 2)
	if state.Input != "" && state.Input != "{}" {
		input := sanitizeGenericHistoryText(prettyPlanTaskHistoryJSONText(state.Input))
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			wrapGenericHistoryContent(
				state.profile(),
				"Input:\n"+input,
				bodyWidth,
				state.Context.selection,
			),
			bodyWidth,
			ToolSuccess,
		))
	}
	if state.Output != "" {
		status := state.Status
		if state.DisplayStatus == ToolError {
			status = ToolError
		}
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			wrapGenericHistoryContent(
				state.profile(),
				"Result:\n"+sanitizeGenericHistoryText(state.Output),
				bodyWidth,
				state.Context.selection,
			),
			bodyWidth,
			status,
		))
	} else if state.DisplayStatus != ToolRunning && state.DisplayStatus != ToolPending {
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			"Result: (no content)",
			bodyWidth,
			state.Status,
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

func renderGenericHistoryTranscript(state toolHistoryRenderState) string {
	parts := []string{state.Name, "Status: " + genericHistoryToolStatus(state)}
	if state.Input != "" {
		parts = append(parts, "Input:\n"+sanitizeGenericHistoryText(prettyPlanTaskHistoryJSONText(state.Input)))
	}
	if state.Output != "" {
		parts = append(parts, "Result:\n"+sanitizeGenericHistoryText(state.Output))
	} else if state.DisplayStatus != ToolRunning && state.DisplayStatus != ToolPending {
		parts = append(parts, "Result: (no content)")
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func genericHistoryPreview(
	profile DisplayCellProfile,
	content string,
	width int,
) string {
	content = sanitizeGenericHistoryText(content)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	limit := genericHistoryHeadLines + genericHistoryTailLines
	if len(lines) > limit {
		bounded := make([]string, 0, limit+1)
		bounded = append(bounded, lines[:genericHistoryHeadLines]...)
		bounded = append(bounded, "… +"+strconv.Itoa(len(lines)-limit)+" lines (expand for details)")
		bounded = append(bounded, lines[len(lines)-genericHistoryTailLines:]...)
		lines = bounded
	}
	lineWidth := min(width, genericHistoryMaxLineColumns)
	clipped := false
	for index, line := range lines {
		if profile.measure(line, 0) > lineWidth {
			lines[index] = contentEllipsize(profile, line, lineWidth, 0, "…")
			clipped = true
		}
	}
	if clipped {
		lines = append(lines, "… content clipped (expand for details)")
	}
	return strings.Join(lines, "\n")
}

func wrapGenericHistoryContent(
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

func sanitizeGenericHistoryText(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, ansi.Strip(value))
}

var (
	defaultToolHistoryRenderer toolHistoryRenderer = genericToolHistoryRenderer{}
	bashHistoryRenderer        toolHistoryRenderer = bashToolHistoryRenderer{}
	readSearchHistoryRenderer  toolHistoryRenderer = readSearchToolHistoryRenderer{}
	editWriteHistoryRenderer   toolHistoryRenderer = editWriteToolHistoryRenderer{}
	agentHistoryRenderer       toolHistoryRenderer = agentToolHistoryRenderer{}
	mcpHistoryRenderer         toolHistoryRenderer = mcpToolHistoryRenderer{}
	planTaskHistoryRenderer    toolHistoryRenderer = planTaskTodoToolHistoryRenderer{}
	webHistoryRenderer         toolHistoryRenderer = webToolHistoryRenderer{}
)

func toolHistoryRendererFor(name string) toolHistoryRenderer {
	switch name {
	case "Bash", "BashOutput", "KillShell":
		return bashHistoryRenderer
	case "Read", "Grep", "Glob":
		return readSearchHistoryRenderer
	case "Edit", "Write":
		return editWriteHistoryRenderer
	case "Agent":
		return agentHistoryRenderer
	case "EnterPlanMode", "ExitPlanMode", "Task", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskStop", "TaskOutput", "TodoWrite":
		return planTaskHistoryRenderer
	case "WebFetch", "WebSearch":
		return webHistoryRenderer
	default:
		if isMCPHistoryToolName(name) {
			return mcpHistoryRenderer
		}
		return defaultToolHistoryRenderer
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
