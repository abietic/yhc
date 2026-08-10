package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const editWriteHistoryMaxRows = 15

type editWriteToolHistoryRenderer struct{}

type editWriteHistoryInput struct {
	FilePath   string `json:"file_path"`
	Content    string `json:"content"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	parseError bool
}

type editWriteHistoryChanges struct {
	added   int
	removed int
}

func (editWriteToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	input := parseEditWriteHistoryInput(state.Input)
	changes := editWriteChanges(state.Name, input)
	rejected := editWriteRejected(state)
	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderEditWriteTranscript(state, input, changes, rejected)
	}

	header := renderEditWriteHeader(state, input, changes, rejected)
	if state.compact() || (state.Output == "" && (state.Status == ToolRunning || state.Status == ToolPending)) {
		return header
	}

	sections := make([]string, 0, 2)
	if rejected && state.Output != "" {
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			state.Output,
			max(10, state.Context.Width-5),
			ToolError,
		))
	}
	if diff := renderEditWriteDiff(state, input); diff != "" {
		sections = append(sections, diff)
	} else if state.Output != "" && !rejected {
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			state.Output,
			max(10, state.Context.Width-5),
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

func parseEditWriteHistoryInput(input string) editWriteHistoryInput {
	var parsed editWriteHistoryInput
	if input == "" || input == "{}" {
		return parsed
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		parsed.parseError = true
	}
	return parsed
}

func editWriteChanges(name string, input editWriteHistoryInput) editWriteHistoryChanges {
	oldText, newText := input.OldString, input.NewString
	if name == "Write" {
		oldText, newText = "", input.Content
	}
	var changes editWriteHistoryChanges
	for _, hunk := range computeUnifiedDiff(oldText, newText, contextLines) {
		for _, line := range hunk.Lines {
			switch line.Type {
			case diffLineAdd:
				changes.added++
			case diffLineRemove:
				changes.removed++
			}
		}
	}
	return changes
}

func editWriteRejected(state toolHistoryRenderState) bool {
	if state.Status == ToolError {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(state.Output))
	for _, prefix := range []string{
		"file has not been read",
		"no changes to make",
		"file is a jupyter notebook",
		"failed",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func renderEditWriteHeader(
	state toolHistoryRenderState,
	input editWriteHistoryInput,
	changes editWriteHistoryChanges,
	rejected bool,
) string {
	styles := state.Context.Styles
	visualStatus := state.DisplayStatus
	if rejected {
		visualStatus = ToolError
	}
	header := toolIcon(styles, visualStatus, state.SpinnerCount) + " " + toolNameStyled(styles, state.Name)
	label, labelStatus := editWriteStatusLabel(state, changes, rejected)
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
	detail := shortenPath(input.FilePath)
	if input.parseError {
		detail = truncateSingleLineWithProfile(state.profile(), state.Input, 80)
	} else if state.Name == "Edit" && input.ReplaceAll {
		detail += " · replace all"
	}
	if detail != "" {
		header += " " + styles.Subtle.Render("("+detail+")")
	}
	return contentEllipsize(state.profile(), header, state.Context.Width, 0, "…")
}

func editWriteStatusLabel(
	state toolHistoryRenderState,
	changes editWriteHistoryChanges,
	rejected bool,
) (string, ToolStatus) {
	if rejected {
		if state.Status == ToolError {
			return "failed", ToolError
		}
		return "not applied", ToolError
	}
	switch state.DisplayStatus {
	case ToolRunning:
		return "running", ToolRunning
	case ToolPending:
		return "pending", ToolPending
	case ToolError:
		return "failed", ToolError
	}
	if changes.added == 0 && changes.removed == 0 {
		return "done", ToolSuccess
	}
	return fmt.Sprintf("+%d -%d", changes.added, changes.removed), ToolSuccess
}

func renderEditWriteDiff(state toolHistoryRenderState, input editWriteHistoryInput) string {
	if input.parseError {
		return ""
	}
	oldText, newText := input.OldString, input.NewString
	if state.Name == "Write" {
		oldText, newText = "", input.Content
	}
	maxRows := editWriteHistoryMaxRows
	if state.fullOutput() {
		maxRows = 0
	}
	return renderStructuredDiffBoundedWithProfile(
		state.profile(),
		state.Context.Styles,
		"",
		oldText,
		newText,
		state.Context.Width,
		maxRows,
	)
}

func renderEditWriteTranscript(
	state toolHistoryRenderState,
	input editWriteHistoryInput,
	changes editWriteHistoryChanges,
	rejected bool,
) string {
	header := state.Name + " " + input.FilePath
	if input.parseError {
		header = state.Name + " " + state.Input
	}
	parts := []string{strings.TrimSpace(header)}
	if diff := renderEditWriteDiff(state, input); diff != "" {
		parts = append(parts, ansi.Strip(diff))
	}
	if state.Output != "" {
		parts = append(parts, "Result: "+ansi.Strip(state.Output))
	}
	status, _ := editWriteStatusLabel(state, changes, rejected)
	if status != "" {
		parts = append(parts, "["+status+"]")
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func truncateHistoryRenderLinesWithProfile(
	profile DisplayCellProfile,
	rendered string,
	width int,
) string {
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		lines[i] = contentProjectLine(profile, line, width, 0)
	}
	return strings.Join(lines, "\n")
}
