package tui

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	webHistoryHeadLines  = 4
	webHistoryTailLines  = 3
	webHistoryMaxSources = 5
)

type webToolHistoryRenderer struct{}

type webHistoryInput struct {
	url            string
	prompt         string
	rawMode        bool
	query          string
	allowedDomains []string
	blockedDomains []string
	parseError     bool
}

type webFetchHistoryResult struct {
	content     string
	originalURL string
	redirectURL string
	aiError     string
	bytes       int64
	code        int
	codeText    string
	redirected  bool
	truncated   bool
}

type webSearchHistoryEntry struct {
	title   string
	url     string
	snippet string
}

type webSearchHistoryResult struct {
	query      string
	entries    []webSearchHistoryEntry
	commentary []string
	duration   float64
	noResults  bool
}

func (webToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	input := parseWebHistoryInput(state.Name, state.Input)
	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderWebHistoryTranscript(state)
	}
	if state.Name == "WebFetch" {
		return renderWebFetchHistory(state, input)
	}
	return renderWebSearchHistory(state, input)
}

func parseWebHistoryInput(name, input string) webHistoryInput {
	var parsed webHistoryInput
	if input == "" || input == "{}" {
		return parsed
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		parsed.parseError = true
		return parsed
	}
	if name == "WebFetch" {
		parsed.url = historyStringValue(params, "url")
		parsed.prompt = historyStringValue(params, "prompt")
		parsed.rawMode, _ = params["raw_mode"].(bool)
		return parsed
	}
	parsed.query = historyStringValue(params, "query")
	parsed.allowedDomains = webHistoryStringSlice(params["allowed_domains"])
	parsed.blockedDomains = webHistoryStringSlice(params["blocked_domains"])
	return parsed
}

func webHistoryStringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func renderWebFetchHistory(state toolHistoryRenderState, input webHistoryInput) string {
	result := parseWebFetchHistoryResult(state.Output)
	if input.url == "" {
		input.url = result.originalURL
	}
	label, labelStatus, visualStatus := webFetchHistoryStatus(state, result)
	details := make([]string, 0, 6)
	if input.url != "" {
		details = append(details, webHistoryURLLink(state.profile(), input.url))
	} else if input.parseError {
		details = append(details, "invalid input")
	}
	if input.rawMode {
		details = append(details, "raw")
	} else if input.prompt != "" {
		details = append(details, "AI")
	}
	if result.bytes > 0 {
		details = append(details, formatWebHistoryBytes(result.bytes))
	}
	if result.code > 0 {
		code := strconv.Itoa(result.code)
		if result.codeText != "" {
			code += " " + result.codeText
		}
		details = append(details, code)
	}
	if result.aiError != "" {
		details = append(details, "AI fallback")
	}
	if result.truncated {
		details = append(details, "truncated")
	}
	header := renderWebHistoryHeader(state, label, labelStatus, visualStatus, details)
	if state.compact() {
		return header
	}
	if state.fullOutput() {
		return renderWebHistoryExpanded(state, header, visualStatus)
	}

	body := renderWebFetchHistoryBody(state, result)
	if body == "" {
		return header
	}
	bodyWidth := max(10, state.Context.Width-5)
	body = boundWebHistoryPreview(
		state.profile(),
		body,
		webHistoryHeadLines,
		webHistoryTailLines,
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

func parseWebFetchHistoryResult(output string) webFetchHistoryResult {
	var result webFetchHistoryResult
	if parseStructuredWebFetchHistoryResult(output, &result) {
		return result
	}
	result.redirected = strings.Contains(output, "The URL redirected to a different host.")
	if result.redirected {
		result.originalURL = historyLineValue(output, "Original URL:")
		result.redirectURL = historyLineValue(output, "Redirect URL:")
		result.bytes = int64(len(output))
		return result
	}
	result.originalURL = historyLineValue(output, "Content fetched from:")
	result.aiError = webHistoryParentheticalValue(output, "(AI processing failed: ")
	if marker := "--- Page Content ---"; strings.Contains(output, marker) {
		result.content = historySectionValue(output, marker, "")
	} else {
		result.content = output
	}
	result.content = sanitizeWebHistoryText(result.content)
	result.bytes = int64(len(result.content))
	result.truncated = strings.Contains(result.content, "[Content truncated at 100KB]") ||
		strings.Contains(result.content, "[...content truncated for processing...]")
	return result
}

func parseStructuredWebFetchHistoryResult(output string, result *webFetchHistoryResult) bool {
	var value map[string]any
	if json.Unmarshal([]byte(output), &value) != nil {
		return false
	}
	rawResult, hasResult := value["result"]
	_, hasBytes := value["bytes"]
	_, hasCode := value["code"]
	if !hasResult && !hasBytes && !hasCode {
		return false
	}
	if text, ok := rawResult.(string); ok {
		result.content = sanitizeWebHistoryText(text)
	}
	result.bytes = webHistoryInt64(value["bytes"])
	if result.bytes == 0 {
		result.bytes = int64(len(result.content))
	}
	result.code = webHistoryHTTPStatus(value["code"])
	result.codeText, _ = value["codeText"].(string)
	result.codeText = sanitizeWebHistoryText(result.codeText)
	result.truncated = strings.Contains(strings.ToLower(result.content), "truncated")
	return true
}

func webFetchHistoryStatus(
	state toolHistoryRenderState,
	result webFetchHistoryResult,
) (string, ToolStatus, ToolStatus) {
	if state.Status == ToolError || state.DisplayStatus == ToolError || result.code >= 400 {
		return "failed", ToolError, ToolError
	}
	if state.DisplayStatus == ToolRunning {
		return "fetching", ToolRunning, ToolRunning
	}
	if state.DisplayStatus == ToolPending {
		return "pending", ToolPending, ToolPending
	}
	if result.redirected {
		return "redirected", ToolSuccess, ToolSuccess
	}
	return "fetched", ToolSuccess, ToolSuccess
}

func renderWebFetchHistoryBody(state toolHistoryRenderState, result webFetchHistoryResult) string {
	if state.Status == ToolError || state.DisplayStatus == ToolError || result.code >= 400 {
		return sanitizeWebHistoryText(state.Output)
	}
	if result.redirected {
		redirect := firstNonEmpty(result.redirectURL, result.originalURL)
		if redirect == "" {
			return "Redirected to a different host; fetch the new URL explicitly."
		}
		return "Redirect: " + webHistoryURLLink(state.profile(), redirect) + "\nFetch the redirect URL explicitly."
	}
	parts := make([]string, 0, 3)
	if result.aiError != "" {
		parts = append(parts, "AI processing failed: "+sanitizeWebHistoryText(result.aiError))
	}
	if strings.TrimSpace(result.content) != "" {
		parts = append(parts, result.content)
	} else if state.DisplayStatus != ToolRunning && state.DisplayStatus != ToolPending {
		parts = append(parts, "(no content)")
	}
	return strings.Join(parts, "\n")
}

func renderWebSearchHistory(state toolHistoryRenderState, input webHistoryInput) string {
	result := parseWebSearchHistoryResult(state.Output)
	if input.query == "" {
		input.query = result.query
	}
	label, labelStatus, visualStatus := webSearchHistoryStatus(state, result)
	details := make([]string, 0, 5)
	if input.query != "" {
		details = append(details, strconv.Quote(
			truncateSingleLineWithProfile(
				state.profile(),
				sanitizeWebHistoryText(input.query),
				100,
			),
		))
	} else if input.parseError {
		details = append(details, "invalid input")
	}
	if len(result.entries) > 0 {
		details = append(details, fmt.Sprintf("%d results", len(result.entries)))
	}
	if result.duration > 0 {
		details = append(details, formatWebHistoryDuration(result.duration))
	}
	if len(input.allowedDomains) > 0 {
		details = append(
			details,
			"only "+truncateSingleLineWithProfile(
				state.profile(),
				sanitizeWebHistoryText(strings.Join(input.allowedDomains, ", ")),
				64,
			),
		)
	}
	if len(input.blockedDomains) > 0 {
		details = append(
			details,
			"blocked "+truncateSingleLineWithProfile(
				state.profile(),
				sanitizeWebHistoryText(strings.Join(input.blockedDomains, ", ")),
				64,
			),
		)
	}
	header := renderWebHistoryHeader(state, label, labelStatus, visualStatus, details)
	if state.compact() {
		return header
	}
	if state.fullOutput() {
		return renderWebHistoryExpanded(state, header, visualStatus)
	}

	body := renderWebSearchHistoryBody(state, result)
	if body == "" {
		return header
	}
	bodyWidth := max(10, state.Context.Width-5)
	body = boundWebHistoryPreview(
		state.profile(),
		body,
		webHistoryHeadLines,
		webHistoryTailLines,
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

func parseWebSearchHistoryResult(output string) webSearchHistoryResult {
	var result webSearchHistoryResult
	if parseStructuredWebSearchHistoryResult(output, &result) {
		return result
	}
	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "No search results found for query:") {
		result.noResults = true
		result.query = strings.TrimSpace(strings.TrimPrefix(trimmed, "No search results found for query:"))
		return result
	}
	result.query = historyLineValue(output, "Search results for:")
	var current *webSearchHistoryEntry
	for _, line := range strings.Split(output, "\n") {
		if entry, ok := parseWebSearchHistoryEntry(line); ok {
			result.entries = append(result.entries, entry)
			current = &result.entries[len(result.entries)-1]
			continue
		}
		if current != nil && strings.HasPrefix(line, "   ") && strings.TrimSpace(line) != "" {
			current.snippet = sanitizeWebHistoryText(strings.TrimSpace(line))
		}
	}
	return result
}

func parseStructuredWebSearchHistoryResult(output string, result *webSearchHistoryResult) bool {
	var value struct {
		Query           string            `json:"query"`
		Results         []json.RawMessage `json:"results"`
		DurationSeconds float64           `json:"durationSeconds"`
	}
	if json.Unmarshal([]byte(output), &value) != nil ||
		(value.Query == "" && value.Results == nil && value.DurationSeconds == 0) {
		return false
	}
	result.query = value.Query
	result.duration = value.DurationSeconds
	for _, raw := range value.Results {
		var commentary string
		if json.Unmarshal(raw, &commentary) == nil {
			if strings.TrimSpace(commentary) != "" {
				result.commentary = append(result.commentary, sanitizeWebHistoryText(commentary))
			}
			continue
		}
		var search struct {
			Content []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &search) != nil {
			continue
		}
		for _, hit := range search.Content {
			result.entries = append(result.entries, webSearchHistoryEntry{
				title: sanitizeWebHistoryText(hit.Title),
				url:   hit.URL,
			})
		}
	}
	result.noResults = len(result.entries) == 0
	return true
}

func parseWebSearchHistoryEntry(line string) (webSearchHistoryEntry, bool) {
	trimmed := strings.TrimSpace(line)
	dot := strings.Index(trimmed, ". ")
	if dot <= 0 {
		return webSearchHistoryEntry{}, false
	}
	if _, err := strconv.Atoi(trimmed[:dot]); err != nil {
		return webSearchHistoryEntry{}, false
	}
	link := trimmed[dot+2:]
	if !strings.HasPrefix(link, "[") {
		return webSearchHistoryEntry{}, false
	}
	separator := strings.LastIndex(link, "](")
	if separator <= 1 || !strings.HasSuffix(link, ")") {
		return webSearchHistoryEntry{}, false
	}
	return webSearchHistoryEntry{
		title: sanitizeWebHistoryText(link[1:separator]),
		url:   strings.TrimSpace(link[separator+2 : len(link)-1]),
	}, true
}

func webSearchHistoryStatus(
	state toolHistoryRenderState,
	result webSearchHistoryResult,
) (string, ToolStatus, ToolStatus) {
	if state.Status == ToolError || state.DisplayStatus == ToolError {
		return "failed", ToolError, ToolError
	}
	if state.DisplayStatus == ToolRunning {
		return "searching", ToolRunning, ToolRunning
	}
	if state.DisplayStatus == ToolPending {
		return "pending", ToolPending, ToolPending
	}
	if result.noResults {
		return "no results", ToolSuccess, ToolSuccess
	}
	return "searched", ToolSuccess, ToolSuccess
}

func renderWebSearchHistoryBody(state toolHistoryRenderState, result webSearchHistoryResult) string {
	if state.Status == ToolError || state.DisplayStatus == ToolError {
		return sanitizeWebHistoryText(state.Output)
	}
	if result.noResults {
		if len(result.commentary) > 0 {
			return strings.Join(result.commentary, "\n")
		}
		return "No search results matched the current domain filters."
	}
	if len(result.entries) == 0 {
		return sanitizeWebHistoryText(state.Output)
	}
	visible := min(len(result.entries), webHistoryMaxSources)
	lines := make([]string, 0, visible+1)
	for index, entry := range result.entries[:visible] {
		title := entry.title
		if title == "" {
			title = entry.url
		}
		line := fmt.Sprintf("%d. %s", index+1, webHistoryHyperlink(entry.url, title))
		if host := webHistoryHost(entry.url); host != "" && host != title {
			line += " (" + host + ")"
		}
		lines = append(lines, line)
	}
	if len(result.entries) > visible {
		lines = append(lines, fmt.Sprintf("… +%d results (expand for details)", len(result.entries)-visible))
	}
	return strings.Join(lines, "\n")
}

func renderWebHistoryHeader(
	state toolHistoryRenderState,
	label string,
	labelStatus ToolStatus,
	visualStatus ToolStatus,
	details []string,
) string {
	styles := state.Context.Styles
	header := toolIcon(styles, visualStatus, state.SpinnerCount) + " " + toolNameStyled(styles, "Web")
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

func renderWebHistoryExpanded(state toolHistoryRenderState, header string, visualStatus ToolStatus) string {
	bodyWidth := max(10, state.Context.Width-5)
	sections := make([]string, 0, 2)
	if state.Input != "" && state.Input != "{}" {
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			wrapWebHistoryContent(
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
		result := prettyPlanTaskHistoryJSONText(sanitizeWebHistoryText(state.Output))
		sections = append(sections, renderIndentedResultWithProfile(
			state.profile(),
			state.Context.Styles,
			wrapWebHistoryContent(
				state.profile(),
				"Result:\n"+result,
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

func renderWebHistoryTranscript(state toolHistoryRenderState) string {
	parts := []string{state.Name, "Status: " + genericHistoryToolStatus(state)}
	if state.Input != "" {
		parts = append(parts, "Input:\n"+prettyPlanTaskHistoryJSONText(state.Input))
	}
	if state.Output != "" {
		parts = append(parts, "Result:\n"+sanitizeWebHistoryText(state.Output))
	}
	return ansi.Strip(strings.TrimSpace(strings.Join(parts, "\n")))
}

func boundWebHistoryPreview(
	profile DisplayCellProfile,
	content string,
	head, tail, width int,
) string {
	content = sanitizeWebHistoryText(content)
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

func wrapWebHistoryContent(
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

func webHistoryURLLink(profile DisplayCellProfile, rawURL string) string {
	return webHistoryHyperlink(rawURL, webHistoryURLDisplay(profile, rawURL))
}

func webHistoryURLDisplay(profile DisplayCellProfile, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return truncateSingleLineWithProfile(profile, sanitizeWebHistoryText(rawURL), 120)
	}
	display := parsed.Host + parsed.EscapedPath()
	if parsed.RawQuery != "" {
		display += "?" + parsed.RawQuery
	}
	return truncateSingleLineWithProfile(profile, display, 120)
}

func webHistoryHyperlink(rawURL, display string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		!webHistoryURLControlSafe(rawURL) {
		return sanitizeWebHistoryText(display)
	}
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", rawURL, sanitizeWebHistoryText(display))
}

func webHistoryHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func sanitizeWebHistoryText(value string) string {
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

func webHistoryURLControlSafe(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func webHistoryParentheticalValue(content, prefix string) string {
	start := strings.Index(content, prefix)
	if start < 0 {
		return ""
	}
	value := content[start+len(prefix):]
	if end := strings.Index(value, ")"); end >= 0 {
		return strings.TrimSpace(value[:end])
	}
	return ""
}

func webHistoryInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		result, _ := typed.Int64()
		return result
	case string:
		result, _ := strconv.ParseInt(typed, 10, 64)
		return result
	default:
		return 0
	}
}

func webHistoryHTTPStatus(value any) int {
	const (
		minHTTPStatus = 100
		maxHTTPStatus = 599
	)

	var code int64
	switch typed := value.(type) {
	case float64:
		if math.Trunc(typed) != typed || typed < minHTTPStatus || typed > maxHTTPStatus {
			return 0
		}
		return int(typed)
	case json.Number:
		var err error
		code, err = typed.Int64()
		if err != nil {
			return 0
		}
	case string:
		var err error
		code, err = strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0
		}
	default:
		return 0
	}
	if code < minHTTPStatus || code > maxHTTPStatus {
		return 0
	}
	return int(code)
}

func formatWebHistoryBytes(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func formatWebHistoryDuration(seconds float64) string {
	if seconds >= 1 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	return fmt.Sprintf("%dms", int(seconds*1000+0.5))
}
