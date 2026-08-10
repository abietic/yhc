package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	bashHistoryHeadLines = 4
	bashHistoryTailLines = 4
)

type bashToolHistoryRenderer struct{}

type bashHistoryInput struct {
	Command         string `json:"command"`
	Description     string `json:"description"`
	RunInBackground bool   `json:"run_in_background"`
	BashID          string `json:"bash_id"`
	ShellID         string `json:"shell_id"`
	Filter          string `json:"filter"`
	parseError      bool
}

type bashHistoryResult struct {
	body        string
	shellID     string
	description string
	background  bool
	exitCode    *int
	timedOut    bool
	canceled    bool
	noNewOutput bool
}

func (bashToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	input := parseBashHistoryInput(state.Input)
	result := parseBashHistoryResult(state.Name, state.Output, input.Command)
	if input.Description == "" {
		input.Description = result.description
	}
	if input.BashID == "" && input.ShellID == "" {
		input.BashID = result.shellID
	}

	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderBashHistoryTranscript(state, input, result)
	}

	header := renderBashHistoryHeader(state, input, result)
	if state.compact() || result.body == "" {
		return header
	}
	body := result.body
	if !state.fullOutput() {
		body = collapseBashHistoryOutput(body)
	}
	bodyStatus := state.Status
	if bashHistoryFailed(state, result) {
		bodyStatus = ToolError
	}
	body = renderIndentedResultWithProfile(
		state.profile(),
		state.Context.Styles,
		body,
		max(10, state.Context.Width-5),
		bodyStatus,
	)
	return header + "\n" + body
}

func parseBashHistoryInput(input string) bashHistoryInput {
	var parsed bashHistoryInput
	if input == "" || input == "{}" {
		return parsed
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		parsed.parseError = true
	}
	return parsed
}

func parseBashHistoryResult(name, output, command string) bashHistoryResult {
	var result bashHistoryResult
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	body := make([]string, 0, len(lines))
	commandLines := strings.Split(strings.TrimRight(command, "\n"), "\n")
	commandLine := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if command != "" && commandLine < len(commandLines) {
			expected := commandLines[commandLine]
			if commandLine == 0 {
				expected = "$ " + expected
			}
			if line == expected {
				commandLine++
				continue
			}
			if i == 0 || commandLine > 0 {
				commandLine = len(commandLines)
			}
		}
		switch {
		case trimmed == "Command started in background.":
			result.background = true
			continue
		case strings.HasPrefix(trimmed, "Shell ID:"):
			result.shellID = strings.TrimSpace(strings.TrimPrefix(trimmed, "Shell ID:"))
			continue
		case strings.HasPrefix(trimmed, "Description:") && result.background:
			result.description = strings.TrimSpace(strings.TrimPrefix(trimmed, "Description:"))
			continue
		case strings.HasPrefix(trimmed, "Use BashOutput with this ID"):
			continue
		case name == "BashOutput" && strings.HasPrefix(trimmed, "Background shell:"):
			result.shellID = strings.TrimSpace(strings.TrimPrefix(trimmed, "Background shell:"))
			continue
		case name == "BashOutput" && trimmed == "Output:":
			continue
		case trimmed == "(no new output captured)":
			result.noNewOutput = true
			body = append(body, trimmed)
			continue
		case trimmed == "[timeout]":
			result.timedOut = true
			continue
		case trimmed == "[canceled]":
			result.canceled = true
			continue
		case strings.HasPrefix(trimmed, "[exit code:") && strings.HasSuffix(trimmed, "]"):
			rawCode := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "[exit code:"), "]"))
			if code, err := strconv.Atoi(rawCode); err == nil {
				result.exitCode = &code
				continue
			}
		case name == "KillShell" && strings.Contains(trimmed, "terminated successfully"):
			continue
		}
		body = append(body, line)
	}
	result.body = strings.TrimRight(strings.Join(body, "\n"), "\n")
	return result
}

func renderBashHistoryHeader(state toolHistoryRenderState, input bashHistoryInput, result bashHistoryResult) string {
	styles := state.Context.Styles
	visualStatus := state.DisplayStatus
	if bashHistoryFailed(state, result) {
		visualStatus = ToolError
	}
	icon := toolIcon(styles, visualStatus, state.SpinnerCount)
	displayName := "Bash"
	var detail []string
	switch state.Name {
	case "BashOutput":
		displayName = "Shell"
		detail = append(detail, "output")
	case "KillShell":
		displayName = "Shell"
		detail = append(detail, "stop")
	case "Bash":
		if result.background || input.RunInBackground {
			displayName = "Shell"
			detail = append(detail, "start")
		}
	}

	shellID := firstNonEmpty(input.BashID, input.ShellID, result.shellID)
	if shellID != "" {
		detail = append(detail, shellID)
	}
	if state.Name == "Bash" {
		description := firstNonEmpty(input.Description, input.Command)
		if description != "" {
			detail = append(
				detail,
				truncateBashCommandWithProfile(state.profile(), description, 160),
			)
		} else if input.parseError {
			detail = append(detail, truncateSingleLineWithProfile(state.profile(), state.Input, 80))
		}
	} else if input.Filter != "" {
		detail = append(
			detail,
			"filter "+strconv.Quote(
				truncateSingleLineWithProfile(state.profile(), input.Filter, 48),
			),
		)
	}

	header := icon + " " + toolNameStyled(styles, displayName)
	label, labelStatus := bashHistoryStatusLabel(state, result)
	if label != "" {
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
	}
	if len(detail) > 0 {
		header += " " + styles.Subtle.Render("("+strings.Join(detail, " · ")+")")
	}
	return contentEllipsize(state.profile(), header, state.Context.Width, 0, "…")
}

func bashHistoryStatusLabel(state toolHistoryRenderState, result bashHistoryResult) (string, ToolStatus) {
	if result.timedOut {
		return "timed out", ToolError
	}
	if result.canceled {
		return "canceled", ToolError
	}
	if result.exitCode != nil && *result.exitCode != 0 {
		return fmt.Sprintf("exit %d", *result.exitCode), ToolError
	}
	switch state.DisplayStatus {
	case ToolRunning:
		return "running", ToolRunning
	case ToolPending:
		return "pending", ToolPending
	case ToolError:
		return "failed", ToolError
	}
	if result.background {
		return "background", ToolSuccess
	}
	if state.Name == "BashOutput" {
		return "checked", ToolSuccess
	}
	if state.Name == "KillShell" {
		return "stopped", ToolSuccess
	}
	return "done", ToolSuccess
}

func bashHistoryFailed(state toolHistoryRenderState, result bashHistoryResult) bool {
	return state.Status == ToolError || result.timedOut || result.canceled ||
		(result.exitCode != nil && *result.exitCode != 0)
}

func collapseBashHistoryOutput(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	limit := bashHistoryHeadLines + bashHistoryTailLines
	if len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	visible := make([]string, 0, limit+1)
	visible = append(visible, lines[:bashHistoryHeadLines]...)
	visible = append(visible, fmt.Sprintf("… +%d lines (expand for details)", len(lines)-limit))
	visible = append(visible, lines[len(lines)-bashHistoryTailLines:]...)
	return strings.Join(visible, "\n")
}

func renderBashHistoryTranscript(state toolHistoryRenderState, input bashHistoryInput, result bashHistoryResult) string {
	var lines []string
	switch state.Name {
	case "Bash":
		command := input.Command
		if command == "" && input.parseError {
			command = state.Input
		}
		if command != "" {
			commandLines := strings.Split(strings.TrimSpace(command), "\n")
			for i, line := range commandLines {
				if i == 0 {
					lines = append(lines, "$ "+line)
				} else {
					lines = append(lines, "  "+line)
				}
			}
		}
	case "BashOutput":
		lines = append(lines, "BashOutput "+firstNonEmpty(input.BashID, result.shellID))
	case "KillShell":
		lines = append(lines, "KillShell "+firstNonEmpty(input.ShellID, result.shellID))
	}
	if result.body != "" {
		lines = append(lines, result.body)
	}
	if result.background {
		lines = append(lines, "[background shell: "+firstNonEmpty(result.shellID, input.BashID, input.ShellID)+"]")
	} else if result.timedOut {
		lines = append(lines, "[timeout]")
	} else if result.canceled {
		lines = append(lines, "[canceled]")
	} else if result.exitCode != nil {
		lines = append(lines, fmt.Sprintf("[exit code: %d]", *result.exitCode))
	} else if state.DisplayStatus == ToolRunning {
		lines = append(lines, "[running]")
	} else if state.Status == ToolError {
		lines = append(lines, "[failed]")
	} else if state.Name == "Bash" && !result.background {
		lines = append(lines, "[exit code: 0]")
	}
	return ansi.Strip(strings.TrimSpace(strings.Join(lines, "\n")))
}
