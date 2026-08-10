package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	readHistoryHeadLines   = 3
	readHistoryTailLines   = 3
	searchHistoryHeadLines = 4
	searchHistoryTailLines = 4
)

type readSearchToolHistoryRenderer struct{}

type readSearchHistoryInput struct {
	FilePath   string `json:"file_path"`
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	OutputMode string `json:"output_mode"`
	Limit      *int   `json:"limit"`
	Offset     *int   `json:"offset"`
	Line       *int   `json:"line"`
	HeadLimit  *int   `json:"head_limit"`
	parseError bool
}

type readSearchHistoryResult struct {
	body      string
	count     int
	unit      string
	noResults bool
	truncated bool
}

func (readSearchToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	input := parseReadSearchHistoryInput(state.Input)
	result := parseReadSearchHistoryResult(state.Name, state.Output, input)
	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderReadSearchTranscript(state, input, result)
	}

	header := renderReadSearchHeader(state, input, result)
	if state.compact() || result.body == "" {
		return header
	}
	body := result.body
	if !state.fullOutput() {
		body = collapseReadSearchOutput(state.Name, body)
	}
	if state.Name == "Read" && state.Status == ToolSuccess {
		if highlighted := renderHighlightedReadWithProfile(
			state.profile(),
			state.Context.Styles,
			state.Input,
			body,
			true,
			state.Context.Width,
		); highlighted != "" {
			return header + "\n" + highlighted
		}
	}
	body = renderIndentedResultWithProfile(
		state.profile(),
		state.Context.Styles,
		body,
		max(10, state.Context.Width-5),
		state.Status,
	)
	return header + "\n" + body
}

func parseReadSearchHistoryInput(input string) readSearchHistoryInput {
	var parsed readSearchHistoryInput
	if input == "" || input == "{}" {
		return parsed
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		parsed.parseError = true
	}
	return parsed
}

func parseReadSearchHistoryResult(name, output string, input readSearchHistoryInput) readSearchHistoryResult {
	body := strings.TrimRight(output, "\n")
	result := readSearchHistoryResult{body: body}
	if body == "" {
		result.noResults = true
	}
	switch name {
	case "Read":
		result.unit = "lines"
		result.count = len(nonEmptyHistoryLines(body))
	case "Glob":
		result.unit = "files"
		result.count, result.noResults, result.truncated = countSearchHistoryResults(body, "files")
	case "Grep":
		result.unit = "matches"
		if input.OutputMode == "files_with_matches" || input.OutputMode == "" {
			result.unit = "files"
		}
		result.count, result.noResults, result.truncated = countSearchHistoryResults(body, result.unit)
	}
	return result
}

func countSearchHistoryResults(output, _ string) (count int, noResults, truncated bool) {
	lines := nonEmptyHistoryLines(output)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "no matches") || strings.HasPrefix(lower, "no files") {
			return 0, true, false
		}
		if strings.Contains(lower, "truncated") || strings.Contains(lower, "pagination") {
			truncated = true
		}
		if strings.HasPrefix(trimmed, "Found ") {
			fields := strings.Fields(trimmed)
			if len(fields) > 1 {
				if parsed, err := strconv.Atoi(fields[1]); err == nil {
					return parsed, parsed == 0, truncated
				}
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "--" || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "(") {
			continue
		}
		count++
	}
	return count, count == 0, truncated
}

func nonEmptyHistoryLines(output string) []string {
	if output == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}
	return result
}

func renderReadSearchHeader(state toolHistoryRenderState, input readSearchHistoryInput, result readSearchHistoryResult) string {
	styles := state.Context.Styles
	header := toolIcon(styles, state.DisplayStatus, state.SpinnerCount) + " " + toolNameStyled(styles, state.Name)
	label, labelStatus := readSearchStatusLabel(state, result)
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
	if detail := readSearchHeaderDetail(state, input); detail != "" {
		header += " " + styles.Subtle.Render("("+detail+")")
	}
	return contentEllipsize(state.profile(), header, state.Context.Width, 0, "…")
}

func readSearchStatusLabel(state toolHistoryRenderState, result readSearchHistoryResult) (string, ToolStatus) {
	switch state.DisplayStatus {
	case ToolRunning:
		return "running", ToolRunning
	case ToolPending:
		return "pending", ToolPending
	case ToolError:
		return "failed", ToolError
	}
	if result.noResults {
		return "0 " + result.unit, ToolSuccess
	}
	label := fmt.Sprintf("%d %s", result.count, result.unit)
	if result.truncated {
		label += "+"
	}
	return label, ToolSuccess
}

func readSearchHeaderDetail(state toolHistoryRenderState, input readSearchHistoryInput) string {
	if input.parseError {
		return truncateSingleLineWithProfile(state.profile(), state.Input, 80)
	}
	switch state.Name {
	case "Read":
		parts := []string{shortenPath(input.FilePath)}
		if input.Line != nil {
			parts = append(parts, fmt.Sprintf("line %d", *input.Line))
		} else {
			if input.Offset != nil {
				parts = append(parts, fmt.Sprintf("offset %d", *input.Offset))
			}
			if input.Limit != nil {
				parts = append(parts, fmt.Sprintf("limit %d", *input.Limit))
			}
		}
		return strings.Join(nonEmptyStrings(parts), " · ")
	case "Grep":
		parts := []string{strconv.Quote(
			truncateSingleLineWithProfile(state.profile(), input.Pattern, 48),
		)}
		if input.Path != "" {
			parts = append(parts, shortenPath(input.Path))
		}
		if input.Glob != "" {
			parts = append(parts, input.Glob)
		}
		return strings.Join(nonEmptyStrings(parts), " · ")
	case "Glob":
		parts := []string{strconv.Quote(
			truncateSingleLineWithProfile(state.profile(), input.Pattern, 48),
		)}
		if input.Path != "" {
			parts = append(parts, shortenPath(input.Path))
		}
		return strings.Join(nonEmptyStrings(parts), " · ")
	}
	return ""
}

func nonEmptyStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func collapseReadSearchOutput(name, output string) string {
	head, tail := searchHistoryHeadLines, searchHistoryTailLines
	if name == "Read" {
		head, tail = readHistoryHeadLines, readHistoryTailLines
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) <= head+tail {
		return strings.Join(lines, "\n")
	}
	visible := make([]string, 0, head+tail+1)
	visible = append(visible, lines[:head]...)
	visible = append(visible, fmt.Sprintf("… +%d lines (expand for details)", len(lines)-head-tail))
	visible = append(visible, lines[len(lines)-tail:]...)
	return strings.Join(visible, "\n")
}

func renderReadSearchTranscript(state toolHistoryRenderState, input readSearchHistoryInput, result readSearchHistoryResult) string {
	var header string
	switch state.Name {
	case "Read":
		header = "Read " + input.FilePath
	case "Grep":
		header = "Grep " + strconv.Quote(input.Pattern)
		if input.Path != "" {
			header += " in " + input.Path
		}
	case "Glob":
		header = "Glob " + strconv.Quote(input.Pattern)
		if input.Path != "" {
			header += " in " + input.Path
		}
	}
	if input.parseError {
		header = state.Name + " " + state.Input
	}
	parts := []string{strings.TrimSpace(header)}
	if result.body != "" {
		parts = append(parts, result.body)
	}
	status, _ := readSearchStatusLabel(state, result)
	if status != "" {
		parts = append(parts, "["+status+"]")
	}
	return ansi.Strip(strings.TrimSpace(strings.Join(parts, "\n")))
}

func renderReadSearchGroupSummary(tools []*ToolMessage, styles Styles) string {
	reads, searches := 0, 0
	for _, tool := range tools {
		switch tool.name {
		case "Read":
			reads++
		case "Grep", "Glob":
			searches++
		}
	}
	parts := []string{fmt.Sprintf("%d operations", len(tools))}
	if reads > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", reads, historyCountWord(reads, "read", "reads")))
	}
	if searches > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", searches, historyCountWord(searches, "search", "searches")))
	}
	return styles.ToolSuccess.Render("●") + " " + toolNameStyled(styles, "Explore") +
		" " + styles.Subtle.Render("("+strings.Join(parts, " · ")+")")
}

func historyCountWord(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func isReadSearchToolGroup(tools []*ToolMessage) bool {
	if len(tools) == 0 {
		return false
	}
	for _, tool := range tools {
		if !isCollapsibleTool(tool.name) {
			return false
		}
	}
	return true
}
