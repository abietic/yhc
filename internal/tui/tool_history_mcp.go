package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	mcpHistoryHeadLines              = 4
	mcpHistoryTailLines              = 3
	mcpHistoryMaxJSONParseBytes      = 200_000
	mcpHistoryMaxFlatJSONBytes       = 5_000
	mcpHistoryMaxFlatJSONKeys        = 12
	mcpHistoryDominantTextMinBytes   = 200
	mcpHistoryLargeResponseThreshold = 40_000
	mcpHistoryMaxHeaderArgs          = 6
	mcpHistoryMaxHeaderValueBytes    = 80
)

type mcpToolHistoryRenderer struct{}

type mcpHistoryInvocation struct {
	server       string
	tool         string
	arguments    any
	hasArguments bool
	parseError   bool
	rawInput     string
}

type mcpHistoryOutput struct {
	preview       string
	protocolError bool
	noContent     bool
}

func (mcpToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	invocation := parseMCPHistoryInvocation(state.Name, state.Input)
	result := parseMCPHistoryOutput(state.profile(), state.Output)
	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderMCPHistoryTranscript(state, invocation, result)
	}

	header := renderMCPHistoryHeader(state, invocation, result)
	if state.compact() {
		return header
	}

	bodyWidth := max(10, state.Context.Width-5)
	sections := make([]string, 0, 3)
	if state.fullOutput() && invocation.hasArguments {
		arguments := prettyMCPHistoryJSON(invocation.arguments)
		sections = append(sections, renderMCPHistorySection(
			state,
			"Arguments:\n"+arguments,
			bodyWidth,
			ToolSuccess,
			true,
		))
	}

	if len(state.Output) >= mcpHistoryLargeResponseThreshold {
		estimatedTokens := (len(state.Output) + 3) / 4
		warning := fmt.Sprintf("Large MCP response (~%d tokens); expand deliberately", estimatedTokens)
		sections = append(sections, renderMCPHistorySection(
			state,
			warning,
			bodyWidth,
			ToolSuccess,
			false,
		))
	}

	bodyStatus := state.Status
	if result.protocolError {
		bodyStatus = ToolError
	}
	content := result.preview
	if state.fullOutput() && state.Output != "" {
		content = prettyMCPHistoryJSONText(state.Output)
	}
	if content == "" && state.DisplayStatus != ToolRunning && state.DisplayStatus != ToolPending {
		if bodyStatus == ToolError {
			content = "(no error detail)"
		} else {
			content = "(no content)"
		}
	}
	if content != "" {
		if state.fullOutput() {
			content = "Result:\n" + content
		} else {
			content = collapseMCPHistoryOutput(content)
			content = boundMCPHistoryPreviewWidth(state.profile(), content, bodyWidth)
		}
		sections = append(sections, renderMCPHistorySection(
			state,
			content,
			bodyWidth,
			bodyStatus,
			state.fullOutput(),
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

func isMCPHistoryToolName(name string) bool {
	if name == "mcp_tool" {
		return true
	}
	_, _, ok := parseMCPHistoryToolName(name)
	return ok
}

func parseMCPHistoryToolName(name string) (string, string, bool) {
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], true
		}
		return "", "", false
	}
	if strings.HasPrefix(name, "mcp_") && name != "mcp_tool" {
		parts := strings.SplitN(strings.TrimPrefix(name, "mcp_"), "_", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

func parseMCPHistoryInvocation(name, input string) mcpHistoryInvocation {
	server, tool, _ := parseMCPHistoryToolName(name)
	invocation := mcpHistoryInvocation{server: server, tool: tool, rawInput: input}
	if input == "" {
		return invocation
	}

	var decoded any
	if err := json.Unmarshal([]byte(input), &decoded); err != nil {
		invocation.parseError = true
		return invocation
	}
	if name != "mcp_tool" {
		invocation.arguments = decoded
		invocation.hasArguments = !mcpHistoryEmptyValue(decoded)
		return invocation
	}

	params, ok := decoded.(map[string]any)
	if !ok {
		invocation.parseError = true
		return invocation
	}
	if value, ok := params["server"].(string); ok {
		invocation.server = value
	}
	if value, ok := params["tool"].(string); ok {
		invocation.tool = value
	}
	if value, ok := params["arguments"]; ok {
		invocation.arguments = value
		invocation.hasArguments = !mcpHistoryEmptyValue(value)
	}
	return invocation
}

func mcpHistoryEmptyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	case string:
		return typed == ""
	default:
		return false
	}
}

func renderMCPHistoryHeader(
	state toolHistoryRenderState,
	invocation mcpHistoryInvocation,
	result mcpHistoryOutput,
) string {
	styles := state.Context.Styles
	visualStatus := state.DisplayStatus
	if result.protocolError {
		visualStatus = ToolError
	}
	header := toolIcon(styles, visualStatus, state.SpinnerCount) + " " + toolNameStyled(styles, "MCP")
	label, labelStatus := mcpHistoryStatusLabel(state, result)
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

	details := make([]string, 0, 2)
	if identity := mcpHistoryInvocationLabel(state.Name, invocation); identity != "" {
		details = append(details, identity)
	}
	if args := compactMCPHistoryArguments(state.profile(), invocation); args != "" {
		details = append(details, args)
	}
	if len(details) > 0 {
		header += " " + styles.Subtle.Render("("+strings.Join(details, " · ")+")")
	}
	return contentEllipsize(state.profile(), header, state.Context.Width, 0, "…")
}

func mcpHistoryStatusLabel(state toolHistoryRenderState, result mcpHistoryOutput) (string, ToolStatus) {
	if result.protocolError || state.Status == ToolError {
		return "failed", ToolError
	}
	switch state.DisplayStatus {
	case ToolRunning:
		return "calling", ToolRunning
	case ToolPending:
		return "pending", ToolPending
	case ToolError:
		return "failed", ToolError
	default:
		return "called", ToolSuccess
	}
}

func mcpHistoryInvocationLabel(name string, invocation mcpHistoryInvocation) string {
	switch {
	case invocation.server != "" && invocation.tool != "":
		return invocation.server + "." + invocation.tool
	case invocation.tool != "":
		return invocation.tool
	case invocation.server != "":
		return invocation.server
	case name != "mcp_tool":
		return name
	default:
		return "mcp_tool"
	}
}

func compactMCPHistoryArguments(
	profile DisplayCellProfile,
	invocation mcpHistoryInvocation,
) string {
	if invocation.parseError {
		return truncateSingleLineWithProfile(profile, invocation.rawInput, 80)
	}
	if !invocation.hasArguments {
		return ""
	}
	params, ok := invocation.arguments.(map[string]any)
	if !ok {
		return truncateSingleLineWithProfile(
			profile,
			prettyMCPHistoryCompactJSON(invocation.arguments),
			120,
		)
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, min(len(keys), mcpHistoryMaxHeaderArgs)+1)
	for _, key := range keys[:min(len(keys), mcpHistoryMaxHeaderArgs)] {
		value := "[redacted]"
		if !mcpHistorySensitiveKey(key) {
			value = truncateSingleLineWithProfile(
				profile,
				prettyMCPHistoryCompactJSON(params[key]),
				mcpHistoryMaxHeaderValueBytes,
			)
		}
		parts = append(parts, key+": "+value)
	}
	if len(keys) > mcpHistoryMaxHeaderArgs {
		parts = append(parts, fmt.Sprintf("+%d args", len(keys)-mcpHistoryMaxHeaderArgs))
	}
	return strings.Join(parts, ", ")
}

func mcpHistorySensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, marker := range []string{
		"api_key", "apikey", "authorization", "client_secret", "cookie", "credential",
		"credentials", "password", "passwd", "private_key", "secret", "token",
		"access_token", "refresh_token",
	} {
		if normalized == marker || strings.HasSuffix(normalized, "_"+marker) {
			return true
		}
	}
	return false
}

func parseMCPHistoryOutput(
	profile DisplayCellProfile,
	output string,
) mcpHistoryOutput {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return mcpHistoryOutput{noContent: true}
	}
	result := mcpHistoryOutput{preview: output}
	if len(trimmed) > mcpHistoryMaxJSONParseBytes {
		return result
	}

	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return result
	}
	switch typed := decoded.(type) {
	case map[string]any:
		result.protocolError = mcpHistoryProtocolError(typed)
		if content, ok := typed["content"].([]any); ok {
			if preview, recognized := renderMCPHistoryContentBlocks(profile, content); recognized {
				result.preview = preview
				result.noContent = preview == ""
				return result
			}
		}
		if preview, ok := unwrapMCPHistoryDominantText(typed); ok {
			result.preview = preview
			return result
		}
		if preview, ok := flattenMCPHistoryJSON(profile, typed, len(trimmed)); ok {
			result.preview = preview
			return result
		}
		result.preview = prettyMCPHistoryJSON(typed)
	case []any:
		if preview, recognized := renderMCPHistoryContentBlocks(profile, typed); recognized {
			result.preview = preview
			result.noContent = preview == ""
			return result
		}
		result.preview = prettyMCPHistoryJSON(typed)
	case string:
		result.preview = typed
	default:
		result.preview = prettyMCPHistoryJSON(typed)
	}
	return result
}

func mcpHistoryProtocolError(value map[string]any) bool {
	for _, key := range []string{"isError", "is_error"} {
		if flag, ok := value[key].(bool); ok && flag {
			return true
		}
	}
	return false
}

func renderMCPHistoryContentBlocks(
	profile DisplayCellProfile,
	blocks []any,
) (string, bool) {
	lines := make([]string, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			return "", false
		}
		kind, ok := block["type"].(string)
		if !ok || kind == "" {
			return "", false
		}
		switch strings.ToLower(kind) {
		case "text", "inputtext", "outputtext":
			if text, ok := block["text"].(string); ok && text != "" {
				lines = append(lines, text)
			}
		case "image", "inputimage", "outputimage":
			lines = append(lines, "[image content]")
		case "audio":
			lines = append(lines, "[audio content]")
		case "resource":
			uri := mcpHistoryResourceURI(block)
			if uri == "" {
				lines = append(lines, "[embedded resource]")
			} else {
				lines = append(lines, "embedded resource: "+uri)
			}
		case "resource_link", "resourcelink", "link":
			uri := mcpHistoryResourceURI(block)
			if uri == "" {
				lines = append(lines, "[resource link]")
			} else {
				lines = append(lines, "link: "+uri)
			}
		default:
			lines = append(
				lines,
				truncateSingleLineWithProfile(
					profile,
					prettyMCPHistoryCompactJSON(block),
					200,
				),
			)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), true
}

func mcpHistoryResourceURI(block map[string]any) string {
	if uri, ok := block["uri"].(string); ok {
		return uri
	}
	if resource, ok := block["resource"].(map[string]any); ok {
		if uri, ok := resource["uri"].(string); ok {
			return uri
		}
	}
	return ""
}

func unwrapMCPHistoryDominantText(value map[string]any) (string, bool) {
	if len(value) == 0 || len(value) > 4 {
		return "", false
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		if key != "isError" && key != "is_error" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	body := ""
	extras := make([]string, 0, len(keys)-1)
	for _, key := range keys {
		raw := value[key]
		switch typed := raw.(type) {
		case string:
			text := strings.TrimRight(typed, "\n")
			dominant := len(text) > mcpHistoryDominantTextMinBytes ||
				(strings.Contains(text, "\n") && len(text) > 50)
			if dominant {
				if body != "" {
					return "", false
				}
				body = text
				continue
			}
			if len(text) > 150 {
				return "", false
			}
			extras = append(extras, key+": "+strings.Join(strings.Fields(text), " "))
		case nil, float64, bool:
			extras = append(extras, key+": "+fmt.Sprint(typed))
		default:
			return "", false
		}
	}
	if body == "" {
		return "", false
	}
	if len(extras) == 0 {
		return body, true
	}
	return strings.Join(extras, " · ") + "\n" + body, true
}

func flattenMCPHistoryJSON(
	profile DisplayCellProfile,
	value map[string]any,
	sourceBytes int,
) (string, bool) {
	if len(value) == 0 || len(value) > mcpHistoryMaxFlatJSONKeys || sourceBytes > mcpHistoryMaxFlatJSONBytes {
		return "", false
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		if key != "isError" && key != "is_error" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", false
	}
	maxKeyWidth := 0
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		raw := value[key]
		var rendered string
		switch typed := raw.(type) {
		case string:
			rendered = typed
		case nil, float64, bool:
			rendered = fmt.Sprint(typed)
		case map[string]any, []any:
			rendered = prettyMCPHistoryCompactJSON(typed)
			if len(rendered) > 120 {
				return "", false
			}
		default:
			return "", false
		}
		values[key] = rendered
		maxKeyWidth = max(maxKeyWidth, profile.measure(key, 0))
	}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		padding := strings.Repeat(" ", maxKeyWidth-profile.measure(key, 0))
		lines = append(lines, key+padding+": "+values[key])
	}
	return strings.Join(lines, "\n"), true
}

func collapseMCPHistoryOutput(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	limit := mcpHistoryHeadLines + mcpHistoryTailLines
	if len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	visible := make([]string, 0, limit+1)
	visible = append(visible, lines[:mcpHistoryHeadLines]...)
	visible = append(visible, fmt.Sprintf("… +%d lines (expand for details)", len(lines)-limit))
	visible = append(visible, lines[len(lines)-mcpHistoryTailLines:]...)
	return strings.Join(visible, "\n")
}

func boundMCPHistoryPreviewWidth(
	profile DisplayCellProfile,
	output string,
	width int,
) string {
	lines := strings.Split(output, "\n")
	clipped := false
	for i, line := range lines {
		if profile.measure(line, 0) <= width {
			continue
		}
		lines[i] = contentEllipsize(profile, line, width, 0, "…")
		clipped = true
	}
	if clipped {
		lines = append(lines, "… content clipped (expand for details)")
	}
	return strings.Join(lines, "\n")
}

func renderMCPHistorySection(
	state toolHistoryRenderState,
	content string,
	width int,
	status ToolStatus,
	wrap bool,
) string {
	if wrap {
		content = wrapMCPHistoryContent(
			state.profile(),
			content,
			width,
			state.Context.selection,
		)
	}
	return renderIndentedResultWithProfile(
		state.profile(),
		state.Context.Styles,
		content,
		width,
		status,
	)
}

func wrapMCPHistoryContent(
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

func renderMCPHistoryTranscript(
	state toolHistoryRenderState,
	invocation mcpHistoryInvocation,
	result mcpHistoryOutput,
) string {
	parts := []string{"MCP " + mcpHistoryInvocationLabel(state.Name, invocation)}
	status, _ := mcpHistoryStatusLabel(state, result)
	parts = append(parts, "Status: "+status)
	if state.Input != "" {
		parts = append(parts, "Input:\n"+prettyMCPHistoryJSONText(state.Input))
	}
	if state.Output != "" {
		parts = append(parts, "Result:\n"+state.Output)
	} else if state.DisplayStatus != ToolRunning && state.DisplayStatus != ToolPending {
		parts = append(parts, "Result: (no content)")
	}
	return ansi.Strip(strings.TrimSpace(strings.Join(parts, "\n")))
}

func prettyMCPHistoryJSON(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func prettyMCPHistoryCompactJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func prettyMCPHistoryJSONText(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > mcpHistoryMaxJSONParseBytes {
		return value
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return value
	}
	return prettyMCPHistoryJSON(decoded)
}
