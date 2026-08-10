package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// renderToolHeader renders a Claude-Code-like tool invocation row.
func renderToolHeader(styles Styles, name string, status ToolStatus, input string, spinnerCount int) string {
	icon := toolIcon(styles, status, spinnerCount)
	styledName := toolNameStyled(styles, name)
	args := formatToolArgs(name, input)
	if args == "" {
		return fmt.Sprintf("%s %s", icon, styledName)
	}
	return fmt.Sprintf("%s %s (%s)", icon, styledName, args)
}

// toolNameStyled renders known tool categories as semantic foregrounds on the
// neutral element surface. Unknown tools retain the plain ToolName style.
func toolNameStyled(styles Styles, name string) string {
	foreground, known := toolCategoryForeground(styles, name)
	if !known {
		return styles.ToolName.Render(name)
	}

	style := styles.ToolName
	if foreground != nil {
		style = style.Foreground(foreground)
	}
	if background := styles.Element.GetBackground(); background != nil {
		style = style.Background(background)
	}
	return style.Render(name)
}

// toolCategoryForeground maps the existing tool categories onto Revontuli
// semantic accents. Task and To-Do retain their established sky and green
// category meanings while sharing the neutral element surface.
func toolCategoryForeground(styles Styles, name string) (tuiColor, bool) {
	switch name {
	case "Bash", "BashOutput", "KillShell", "Shell", "MCP":
		return styles.AssistantPrefix.GetForeground(), true
	case "Read", "Write", "Edit", "To-Do":
		return styles.ToolSuccess.GetForeground(), true
	case "Grep", "Glob", "LS", "Explore", "Task", "Web", "WebFetch", "WebSearch":
		return styles.AuroraSky.GetForeground(), true
	case "Agent", "Plan":
		return styles.DialogTitle.GetForeground(), true
	default:
		return nil, false
	}
}

func toolIcon(styles Styles, status ToolStatus, spinnerCount int) string {
	switch status {
	case ToolRunning:
		if spinnerCount%2 == 0 {
			return styles.ToolRunning.Render("●")
		}
		return styles.ToolRunning.Render(" ")
	case ToolSuccess:
		return styles.ToolSuccess.Render("●")
	case ToolError:
		return styles.ToolError.Render("●")
	default:
		return styles.Subtle.Render("●")
	}
}

func formatToolArgs(toolName, input string) string {
	return formatToolArgsWithProfile(DefaultDisplayCellProfile(), toolName, input)
}

func formatToolArgsWithProfile(profile DisplayCellProfile, toolName, input string) string {
	if input == "" || input == "{}" {
		return ""
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return truncateSingleLineWithProfile(profile, input, 80)
	}

	switch toolName {
	case "Bash":
		if cmd, ok := params["command"].(string); ok {
			return truncateBashCommandWithProfile(profile, cmd, 160)
		}
	case "Read":
		if fp, ok := params["file_path"].(string); ok {
			return shortenPath(fp)
		}
	case "Write":
		if fp, ok := params["file_path"].(string); ok {
			return shortenPath(fp)
		}
	case "Edit":
		if fp, ok := params["file_path"].(string); ok {
			return shortenPath(fp)
		}
	case "Agent":
		if desc, ok := params["description"].(string); ok {
			return truncateSingleLineWithProfile(profile, desc, 80)
		}
		if prompt, ok := params["prompt"].(string); ok {
			return truncateSingleLineWithProfile(profile, prompt, 80)
		}
	case "Grep":
		return formatSearchArgsWithProfile(profile, params, "pattern", "path")
	case "Glob":
		return formatSearchArgsWithProfile(profile, params, "pattern", "path")
	}

	// Fallback: key-value format for unknown tools
	return formatKeyValueArgsWithProfile(profile, toolName, params)
}

func truncateBashCommandWithProfile(profile DisplayCellProfile, cmd string, max int) string {
	cmd = strings.TrimSpace(cmd)
	lines := strings.Split(cmd, "\n")
	if len(lines) > 2 {
		cmd = strings.Join(lines[:2], " && ") + "…"
	} else if len(lines) == 2 {
		cmd = strings.Join(lines, " && ")
	}
	cmd = strings.ReplaceAll(cmd, "\t", " ")
	return contentEllipsize(profile, cmd, max, 0, "…")
}

func shortenPath(fp string) string {
	// Show last 2 path components for brevity
	parts := strings.Split(fp, "/")
	if len(parts) <= 3 {
		return fileHyperlink(fp, fp)
	}
	display := "…/" + strings.Join(parts[len(parts)-2:], "/")
	return fileHyperlink(fp, display)
}

// fileHyperlink wraps text in an OSC 8 terminal hyperlink pointing to a file path.
// Terminals that support OSC 8 (iTerm2, WezTerm, etc.) make it clickable.
func fileHyperlink(path, text string) string {
	if path == "" || text == "" {
		return text
	}
	// Only create hyperlinks for absolute paths
	if !strings.HasPrefix(path, "/") {
		return text
	}
	destination := (&url.URL{Scheme: "file", Path: path}).String()
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", destination, text)
}

func formatSearchArgsWithProfile(
	profile DisplayCellProfile,
	params map[string]any,
	patternKey, pathKey string,
) string {
	var parts []string
	if p, ok := params[patternKey].(string); ok && p != "" {
		parts = append(parts, fmt.Sprintf(
			"pattern: %q",
			truncateSingleLineWithProfile(profile, p, 40),
		))
	}
	if p, ok := params[pathKey].(string); ok && p != "" {
		parts = append(parts, fmt.Sprintf("path: %q", shortenPath(p)))
	}
	return strings.Join(parts, ", ")
}

func formatKeyValueArgsWithProfile(
	profile DisplayCellProfile,
	toolName string,
	params map[string]any,
) string {
	args := orderedToolArgs(toolName, params)
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for _, key := range args {
		value, ok := params[key]
		if !ok || value == nil {
			continue
		}
		rendered := "[redacted]"
		redactConfigValue := key == "value" && mcpHistorySensitiveKey(fmt.Sprint(params["key"]))
		if !mcpHistorySensitiveKey(key) && !redactConfigValue {
			rendered = formatArgValueWithProfile(profile, value)
		}
		parts = append(parts, fmt.Sprintf("%s: %s", key, rendered))
	}
	if len(parts) == 0 {
		return ""
	}
	return truncateSingleLineWithProfile(profile, strings.Join(parts, ", "), 120)
}

func orderedToolArgs(toolName string, params map[string]any) []string {
	preferred := map[string][]string{
		"Read":  {"file_path", "limit", "offset", "line"},
		"Write": {"file_path"},
		"Edit":  {"file_path", "old_string", "new_string"},
		"Bash":  {"command", "description"},
		"Grep":  {"pattern", "path", "glob", "output_mode", "head_limit"},
		"Glob":  {"pattern", "path"},
		"Agent": {"agent_type", "description", "prompt"},
	}
	seen := make(map[string]bool, len(params))
	var out []string
	for _, key := range preferred[toolName] {
		if _, ok := params[key]; ok {
			out = append(out, key)
			seen[key] = true
		}
	}
	var rest []string
	for key := range params {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	out = append(out, rest...)
	return out
}

func formatArgValueWithProfile(profile DisplayCellProfile, value any) string {
	switch v := value.(type) {
	case string:
		return strconv.Quote(truncateSingleLineWithProfile(profile, v, 96))
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return truncateSingleLineWithProfile(profile, string(encoded), 96)
	}
}

func truncateSingleLineWithProfile(profile DisplayCellProfile, s string, max int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	return contentEllipsize(profile, s, max, 0, "…")
}

// renderToolBody renders a compact output preview with a Claude-Code-like gutter.
func renderToolBody(styles Styles, name, output string, expanded bool, status ToolStatus, width int) string {
	if output == "" {
		return ""
	}
	output = expandTabsForRender(output, 4)
	bodyWidth := width - 5 // account for "  ⎿  " prefix (5 visible chars)
	if bodyWidth < 10 {
		bodyWidth = 10
	}
	formatted := formatToolOutput(name, output, expanded, bodyWidth)
	return renderIndentedResult(styles, formatted, bodyWidth, status)
}

func expandTabsForRender(s string, tabWidth int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, ch := range s {
		switch ch {
		case '\t':
			spaces := tabWidth - (col % tabWidth)
			for j := 0; j < spaces; j++ {
				b.WriteByte(' ')
			}
			col += spaces
		case '\n':
			b.WriteByte('\n')
			col = 0
		default:
			b.WriteRune(ch)
			col++
		}
	}
	return b.String()
}

func renderIndentedResult(styles Styles, content string, width int, status ToolStatus) string {
	return renderIndentedResultWithProfile(
		DefaultDisplayCellProfile(),
		styles,
		content,
		width,
		status,
	)
}

func renderIndentedResultWithProfile(
	profile DisplayCellProfile,
	styles Styles,
	content string,
	width int,
	status ToolStatus,
) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	gutter := "  " + styles.Subtle.Render("⎿") + "  "
	indent := "     " // 5 spaces to match gutter visual width
	for i, line := range lines {
		line = contentProjectLine(profile, line, width, 5)
		var styledLine string
		if status == ToolError {
			styledLine = styles.Error.Render(line)
		} else {
			styledLine = styles.Subtle.Render(line)
		}
		if i == 0 {
			out = append(out, gutter+styledLine)
		} else {
			out = append(out, indent+styledLine)
		}
	}
	return strings.Join(out, "\n")
}

// formatToolOutput formats tool output based on tool type.
func formatToolOutput(toolName, output string, expanded bool, width int) string {
	switch toolName {
	case "Bash":
		return formatCollapsedLines(output, expanded, 10, "line(s)")
	case "Read":
		return formatReadOutput(output, expanded)
	case "Write":
		return formatWriteOutput(output, expanded, width)
	case "Glob":
		return formatSearchOutput(strings.TrimSpace(output), expanded, "file(s)")
	case "Grep":
		return formatSearchOutput(strings.TrimSpace(output), expanded, "line(s)")
	case "Agent", "Explore", "Plan":
		return formatAgentOutput(output, expanded)
	default:
		if !expanded && len(output) > 500 {
			return truncate(output, 500) + "\n... (truncated, expand for details)"
		}
		return formatCollapsedLines(output, expanded, 8, "line(s)")
	}
}

func formatReadOutput(output string, expanded bool) string {
	lines := splitOutputLines(output)
	n := len(lines)
	if expanded || n <= 8 {
		return strings.Join(lines, "\n")
	}
	label := "lines"
	if n == 1 {
		label = "line"
	}
	return fmt.Sprintf("Read %d %s (expand for details)", n, label)
}

func formatWriteOutput(output string, expanded bool, _ int) string {
	lines := splitOutputLines(output)
	n := len(lines)
	if expanded {
		return strings.Join(lines, "\n")
	}
	if n <= 10 {
		return strings.Join(lines, "\n")
	}
	// Show first 10 lines + overflow
	visible := append([]string(nil), lines[:10]...)
	plusLines := n - 10
	label := "lines"
	if plusLines == 1 {
		label = "line"
	}
	visible = append(visible, fmt.Sprintf("… +%d %s (expand for details)", plusLines, label))
	return strings.Join(visible, "\n")
}

func formatSearchOutput(output string, expanded bool, unit string) string {
	lines := splitOutputLines(output)
	n := len(lines)
	if expanded || n <= 10 {
		return strings.Join(lines, "\n")
	}
	// Singular/plural
	label := unit
	if n == 1 {
		label = strings.TrimSuffix(label, "(s)")
	} else {
		label = strings.Replace(label, "(s)", "s", 1)
	}
	return fmt.Sprintf("Found %d %s (expand for details)", n, label)
}

func formatCollapsedLines(output string, expanded bool, maxLines int, unit string) string {
	lines := splitOutputLines(output)
	if expanded || len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	visible := append([]string(nil), lines[:maxLines]...)
	hidden := len(lines) - maxLines
	visible = append(visible, fmt.Sprintf("... (+%d %s) (expand for details)", hidden, unit))
	return strings.Join(visible, "\n")
}

func splitOutputLines(output string) []string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return []string{"Done"}
	}
	return strings.Split(output, "\n")
}

func formatAgentOutput(output string, expanded bool) string {
	if expanded {
		return output
	}
	lines := splitOutputLines(output)
	toolUses := countToolUseMentions(output)
	bytes := humanBytes(len(output))
	if toolUses > 0 {
		return fmt.Sprintf("Done (%d tool uses, ↓ %s) (expand for details)", toolUses, bytes)
	}
	if len(lines) > 4 {
		return fmt.Sprintf("Done (↓ %s) (expand for details)", bytes)
	}
	return strings.Join(lines, "\n")
}

func countToolUseMentions(output string) int {
	return strings.Count(output, "●") + strings.Count(output, "Tool:") + strings.Count(output, "tool_use")
}

func humanBytes(n int) string {
	if n >= 1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	if n >= 1024 {
		return fmt.Sprintf("%.1f kB", float64(n)/1024)
	}
	return fmt.Sprintf("%d B", n)
}

func humanTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM tokens", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk tokens", float64(n)/1000)
	}
	return fmt.Sprintf("%d tokens", n)
}

// highlightCode applies syntax highlighting using chroma.
// Returns the highlighted string or empty if highlighting fails/not applicable.
func highlightCode(code, filename string) string {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		return ""
	}
	lexer = chroma.Coalesce(lexer)
	style := styles.Get("monokai")
	formatter := formatters.Get("terminal256")
	if formatter == nil || style == nil {
		return ""
	}
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return ""
	}
	return strings.TrimRight(buf.String(), "\n")
}

// renderHighlightedRead renders Read tool output with syntax highlighting.
// Returns empty string if highlighting is not applicable.
func renderHighlightedReadWithProfile(
	profile DisplayCellProfile,
	tStyles Styles,
	input, output string,
	expanded bool,
	width int,
) string {
	// Parse file_path from input
	var params struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil || params.FilePath == "" {
		return ""
	}

	// Check if output should be shown (not collapsed)
	lines := splitOutputLines(output)
	if !expanded && len(lines) > 8 {
		return "" // collapsed — use default summary
	}

	// Try to highlight
	ext := filepath.Ext(params.FilePath)
	if ext == "" {
		return ""
	}

	// Strip line-number prefixes (e.g. "     1→") before highlighting
	prefixes := make([]string, len(lines))
	codeLines := make([]string, len(lines))
	for i, line := range lines {
		if idx := strings.Index(line, "→"); idx >= 0 && idx <= 7 {
			prefixes[i] = line[:idx+len("→")]
			codeLines[i] = line[idx+len("→"):]
		} else {
			codeLines[i] = line
		}
	}

	highlighted := highlightCode(strings.Join(codeLines, "\n"), params.FilePath)
	if highlighted == "" {
		return ""
	}

	// Render with gutter (like renderIndentedResult but pre-styled)
	bodyWidth := width - 5
	if bodyWidth < 10 {
		bodyWidth = 10
	}
	hLines := strings.Split(highlighted, "\n")
	out := make([]string, 0, len(hLines))
	gutter := "  " + tStyles.Subtle.Render("⎿") + "  "
	indent := "     "
	for i, line := range hLines {
		// Re-prepend the line-number prefix with subtle styling
		var prefix string
		if i < len(prefixes) && prefixes[i] != "" {
			prefix = tStyles.Subtle.Render(prefixes[i])
		}
		fullLine := prefix + line
		fullLine = contentProjectLine(profile, fullLine, bodyWidth, 5)
		if i == 0 {
			out = append(out, gutter+fullLine)
		} else {
			out = append(out, indent+fullLine)
		}
	}
	return strings.Join(out, "\n")
}

// getToolDescription is retained for compatibility with existing ToolMessage state.
func getToolDescription(toolName, input string, _ ToolStatus) string {
	return formatToolArgs(toolName, input)
}
