package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ErrorSeverity represents the severity of an error for visual presentation.
type ErrorSeverity int

const (
	SeverityInfo    ErrorSeverity = iota // Informational (rate limits with timer, etc.)
	SeverityWarning                      // Recoverable errors (retry possible)
	SeverityError                        // Fatal errors (configuration, auth, etc.)
)

// String returns a human-readable severity string.
func (s ErrorSeverity) String() string {
	switch s {
	case SeverityInfo:
		return "Info"
	case SeverityWarning:
		return "Warning"
	case SeverityError:
		return "Error"
	}
	return "Unknown"
}

// ErrorCategory classifies the type of error for appropriate presentation.
type ErrorCategory int

const (
	CategoryGeneral    ErrorCategory = iota // Unclassified errors
	CategoryRateLimit                       // API rate limits
	CategoryAuth                            // Authentication/authorization failures
	CategoryNetwork                         // Network connectivity issues
	CategoryModel                           // Model-related errors (context too long, etc.)
	CategoryTool                            // Tool execution failures
	CategoryConfig                          // Configuration errors
	CategoryPermission                      // Permission denied (user or system)
)

// String returns a human-readable category string.
func (c ErrorCategory) String() string {
	switch c {
	case CategoryGeneral:
		return "General"
	case CategoryRateLimit:
		return "Rate Limit"
	case CategoryAuth:
		return "Authentication"
	case CategoryNetwork:
		return "Network"
	case CategoryModel:
		return "Model"
	case CategoryTool:
		return "Tool"
	case CategoryConfig:
		return "Configuration"
	case CategoryPermission:
		return "Permission"
	}
	return "Unknown"
}

// SuggestedAction represents an actionable suggestion for resolving an error.
type SuggestedAction struct {
	Label       string // Human-readable action label
	Command     string // Optional command to run (e.g., "/retry", "/model")
	Description string // Explanation of what this action does
}

// ErrorEntry represents a single structured error for display.
type ErrorEntry struct {
	Severity    ErrorSeverity
	Category    ErrorCategory
	Title       string // Short error title
	Message     string // Full error message
	Context     string // Additional context (tool name, file path, etc.)
	Details     string // Verbose details (stack trace, response body, etc.)
	Suggestions []SuggestedAction
	Timestamp   time.Time
	Retryable   bool

	// Rate limit specific
	RetryAfter time.Duration // How long until retry is possible
	RetryAt    time.Time     // When retry becomes available
}

// ErrorDisplay is a component that renders structured errors with severity
// coloring, collapsible details, and suggested actions.
//
// It can function both as an inline chat item (for single errors embedded
// in the conversation) and as a scrollable panel (for the /errors command
// showing error history).
//
// Reference: claude-code-ripe error handling in query.ts — structured error
// types with recovery suggestions.
type ErrorDisplay struct {
	styles Styles

	// Error history
	errors []ErrorEntry

	// Panel state (when showing as overlay via /errors)
	panelVisible bool
	panelOffset  int // scroll offset in panel mode
	panelHeight  int

	// Expanded state: which error details are visible
	expandedIdx map[int]bool
}

// NewErrorDisplay creates a new error display component.
func NewErrorDisplay(styles Styles) *ErrorDisplay {
	return &ErrorDisplay{
		styles:      styles,
		expandedIdx: make(map[int]bool),
	}
}

// AddError adds an error to the history.
func (e *ErrorDisplay) AddError(entry ErrorEntry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	e.errors = append(e.errors, entry)
}

// Clear removes all errors from history.
func (e *ErrorDisplay) Clear() {
	e.errors = nil
	e.expandedIdx = make(map[int]bool)
}

// Errors returns the error history.
func (e *ErrorDisplay) Errors() []ErrorEntry {
	return e.errors
}

// Count returns the number of errors in history.
func (e *ErrorDisplay) Count() int {
	return len(e.errors)
}

// LastError returns the most recent error, or nil if none.
func (e *ErrorDisplay) LastError() *ErrorEntry {
	if len(e.errors) == 0 {
		return nil
	}
	return &e.errors[len(e.errors)-1]
}

// ToggleDetails toggles the expanded state of an error's details section.
func (e *ErrorDisplay) ToggleDetails(idx int) {
	if idx < 0 || idx >= len(e.errors) {
		return
	}
	e.expandedIdx[idx] = !e.expandedIdx[idx]
}

// --- Inline rendering (as ChatItem) ---

// ErrorMessage is a ChatItem that renders a structured error inline in the chat.
type ErrorMessage struct {
	entry   ErrorEntry
	styles  Styles
	version uint64

	// UI state
	expanded bool // whether details are visible
}

// NewErrorMessage creates a new error message for inline display.
func NewErrorMessage(entry ErrorEntry, styles Styles) *ErrorMessage {
	return &ErrorMessage{
		entry:   entry,
		styles:  styles,
		version: 1,
	}
}

// ToggleExpand toggles the details expansion state.
func (m *ErrorMessage) ToggleExpand() {
	m.expanded = !m.expanded
	m.version++
}

// Render implements ChatItem.
func (m *ErrorMessage) Render(width int, styles Styles) string {
	return renderErrorEntry(m.entry, m.expanded, width, styles)
}

func (m *ErrorMessage) RenderWithEnvironment(
	width int,
	env RenderEnvironment,
) string {
	env = env.normalized()
	return renderErrorEntryWithProfile(
		env.profile,
		m.entry,
		m.expanded,
		width,
		env.styles,
	)
}

// Finished implements ChatItem.
func (m *ErrorMessage) Finished() bool {
	return true
}

// Version implements ChatItem.
func (m *ErrorMessage) Version() uint64 {
	return m.version
}

func (m *ErrorMessage) NoSelectPrefix() int { return 0 }
func (m *ErrorMessage) Selectable() bool    { return true }

func (m *ErrorMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	values := []string{
		m.entry.Title,
		m.entry.Message,
		m.entry.Context,
		m.entry.Details,
	}
	for _, suggestion := range m.entry.Suggestions {
		values = append(
			values,
			suggestion.Label,
			suggestion.Command,
			suggestion.Description,
		)
	}
	if selectionAnnotationsCollide(values...) {
		return selectionAnnotatedRender{
			rendered: m.RenderWithEnvironment(ctx.Width, ctx.Environment),
		}
	}
	return selectionAnnotatedRender{
		rendered: renderErrorEntryWithProfileMode(
			ctx.displayCellProfile(),
			m.entry,
			m.expanded,
			ctx.Width,
			ctx.Styles,
			true,
		),
		annotated: true,
	}
}

func (m *ErrorMessage) RenderRaw(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	return ansi.Strip(renderErrorEntryWithProfile(
		ctx.displayCellProfile(),
		m.entry,
		true,
		ctx.Width,
		ctx.Styles,
	))
}

func (m *ErrorMessage) RenderExpanded(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	return renderErrorEntryWithProfile(
		ctx.displayCellProfile(),
		m.entry,
		true,
		ctx.Width,
		ctx.Styles,
	)
}

func (m *ErrorMessage) Expanded() bool { return m.expanded }

func (m *ErrorMessage) ToggleExpanded() bool {
	m.ToggleExpand()
	return m.expanded
}

func (m *ErrorMessage) ExpandedContent() (string, string) {
	content := m.entry.Message
	if m.entry.Details != "" {
		content += "\n\n" + m.entry.Details
	}
	return m.entry.Title, content
}

func (m *ErrorMessage) RenderTranscript(ctx HistoryRenderContext) string {
	return m.RenderRaw(ctx)
}

// --- Panel rendering (for /errors overlay) ---

// ShowPanel opens the error history panel.
func (e *ErrorDisplay) ShowPanel(height int) {
	e.panelVisible = true
	e.panelHeight = height
	e.panelOffset = 0
}

// HidePanel closes the error history panel.
func (e *ErrorDisplay) HidePanel() {
	e.panelVisible = false
}

// IsPanelVisible returns whether the error panel is showing.
func (e *ErrorDisplay) IsPanelVisible() bool {
	return e.panelVisible
}

// ScrollUp scrolls the error panel up.
func (e *ErrorDisplay) ScrollUp(n int) {
	e.panelOffset -= n
	if e.panelOffset < 0 {
		e.panelOffset = 0
	}
}

// ScrollDown scrolls the error panel down.
func (e *ErrorDisplay) ScrollDown(n int) {
	e.panelOffset += n
}

// RenderPanel renders the full error history as a scrollable panel.
func (e *ErrorDisplay) RenderPanel(width, height int) string {
	if len(e.errors) == 0 {
		empty := contentRenderStyleBox(
			DefaultDisplayCellProfile(),
			e.styles.Placeholder.Align(lipgloss.Center, lipgloss.Center),
			width,
			height,
			"No errors recorded this session.",
		)
		return empty
	}

	contentWidth := width - 4
	if contentWidth > 100 {
		contentWidth = 100
	}
	if contentWidth < 30 {
		contentWidth = 30
	}

	// Render all errors
	var allLines []string
	header := e.styles.Bold.Render(fmt.Sprintf("Error History (%d errors)", len(e.errors)))
	allLines = append(allLines, "  "+header)
	allLines = append(allLines, "  "+e.styles.Subtle.Render(strings.Repeat("\u2500", contentWidth-4)))
	allLines = append(allLines, "")

	for i, entry := range e.errors {
		expanded := e.expandedIdx[i]
		rendered := renderErrorEntry(entry, expanded, contentWidth, e.styles)
		entryLines := strings.Split(rendered, "\n")
		allLines = append(allLines, entryLines...)
		allLines = append(allLines, "") // gap between entries
	}

	// Apply scroll offset
	totalLines := len(allLines)
	startLine := e.panelOffset
	if startLine >= totalLines {
		startLine = totalLines - 1
	}
	if startLine < 0 {
		startLine = 0
	}

	endLine := startLine + height
	if endLine > totalLines {
		endLine = totalLines
	}

	visible := allLines[startLine:endLine]

	// Pad to full height
	for len(visible) < height {
		visible = append(visible, "")
	}

	// Footer with scroll indicator
	if totalLines > height {
		scrollPct := 0
		if totalLines-height > 0 {
			scrollPct = startLine * 100 / (totalLines - height)
		}
		footer := e.styles.Subtle.Render(fmt.Sprintf("  \u2191/\u2193 scroll \u00b7 tab expand \u00b7 esc close \u00b7 %d%%", scrollPct))
		visible[len(visible)-1] = footer
	}

	return strings.Join(visible, "\n")
}

// --- Common rendering logic ---

// renderErrorEntry renders a single error entry with appropriate styling.
func renderErrorEntry(entry ErrorEntry, expanded bool, width int, styles Styles) string {
	return renderErrorEntryWithProfile(
		DefaultDisplayCellProfile(),
		entry,
		expanded,
		width,
		styles,
	)
}

func renderErrorEntryWithProfile(
	profile DisplayCellProfile,
	entry ErrorEntry,
	expanded bool,
	width int,
	styles Styles,
) string {
	return renderErrorEntryWithProfileMode(
		profile,
		entry,
		expanded,
		width,
		styles,
		false,
	)
}

func renderErrorEntryWithProfileMode(
	profile DisplayCellProfile,
	entry ErrorEntry,
	expanded bool,
	width int,
	styles Styles,
	selection bool,
) string {
	var lines []string
	contentWidth := max(1, width-4)

	// Icon and title line
	icon := errorIcon(entry.Severity, styles)
	title := errorTitleStyle(entry.Severity, styles).Render(entry.Title)
	categoryTag := styles.Subtle.Render("[" + entry.Category.String() + "]")
	timestamp := styles.Subtle.Render(entry.Timestamp.Format("15:04:05"))

	headerLine := fmt.Sprintf("  %s %s %s  %s", icon, title, categoryTag, timestamp)
	if selection {
		headerLine = selectionPresentation("  "+icon+" ") +
			selectionSemantic(title+" "+categoryTag+"  "+timestamp)
	}
	lines = append(lines, contentProjectLine(profile, headerLine, width, 0))

	// Message
	if entry.Message != "" {
		var msgLines []string
		if selection {
			annotated, ok := selectionAnnotatedContentWrap(
				profile,
				entry.Message,
				contentWidth,
				4,
			)
			if !ok {
				return renderErrorEntryWithProfileMode(
					profile,
					entry,
					expanded,
					width,
					styles,
					false,
				)
			}
			msgLines = strings.Split(annotated, "\n")
		} else {
			msgLines = wrapErrorTextWithProfile(
				profile,
				entry.Message,
				contentWidth,
			)
		}
		for _, ml := range msgLines {
			prefix := "    "
			if selection {
				prefix = selectionPresentation(prefix)
			}
			lines = append(
				lines,
				contentProjectLine(profile, prefix+ml, width, 0),
			)
		}
	}

	// Context (tool name, file, etc.)
	if entry.Context != "" {
		context := styles.Subtle.Render("Context: " + entry.Context)
		ctxLine := "    " + context
		if selection {
			ctxLine = selectionPresentation("    ") +
				selectionSemantic(context)
		}
		lines = append(lines, contentEllipsize(profile, ctxLine, width, 0, "..."))
	}

	// Rate limit countdown
	if entry.Category == CategoryRateLimit && !entry.RetryAt.IsZero() {
		remaining := time.Until(entry.RetryAt)
		if remaining > 0 {
			countdownStyle := styles.Warning
			if remaining < 5*time.Second {
				countdownStyle = styles.ToolSuccess
			}
			countdownText := fmt.Sprintf(
				"    Retry available in %s",
				formatRetryDuration(remaining),
			)
			if selection {
				countdownText = selectionPresentation("    ") +
					selectionSemantic(strings.TrimPrefix(
						countdownText,
						"    ",
					))
			}
			countdown := countdownStyle.Render(countdownText)
			lines = append(lines, countdown)
		} else {
			ready := styles.ToolSuccess.Render("Ready to retry")
			if selection {
				lines = append(
					lines,
					selectionPresentation("    ")+
						selectionSemantic(ready),
				)
			} else {
				lines = append(lines, "    "+ready)
			}
		}
	}

	// Retryable indicator
	if entry.Retryable && entry.Category != CategoryRateLimit {
		retryable := styles.Subtle.Render("(retryable)")
		if selection {
			lines = append(
				lines,
				selectionPresentation("    ")+
					selectionSemantic(retryable),
			)
		} else {
			lines = append(lines, "    "+retryable)
		}
	}

	// Collapsible details
	if entry.Details != "" {
		if expanded {
			detailsLabel := styles.Subtle.Render("\u25bc Details:")
			if selection {
				lines = append(
					lines,
					selectionPresentation("    ")+
						selectionSemantic(detailsLabel),
				)
			} else {
				lines = append(lines, "    "+detailsLabel)
			}
			detailLines := strings.Split(entry.Details, "\n")
			maxDetailLines := 20
			for i, dl := range detailLines {
				if i >= maxDetailLines {
					overflow := styles.Subtle.Render(fmt.Sprintf(
						"... +%d more lines",
						len(detailLines)-maxDetailLines,
					))
					if selection {
						lines = append(
							lines,
							selectionPresentation("      ")+
								selectionSemantic(overflow),
						)
					} else {
						lines = append(lines, "      "+overflow)
					}
					break
				}
				detailWidth := max(1, width-6)
				if selection {
					dl = selectionSemantic(selectionAnnotateTabs(dl))
				}
				dl = contentEllipsize(profile, dl, detailWidth, 6, "...")
				prefix := "      "
				if selection {
					prefix = selectionPresentation(prefix)
				}
				lines = append(lines, prefix+styles.Subtle.Render(dl))
			}
		} else {
			details := styles.Subtle.Render(
				"\u25b6 Details (expand for details)",
			)
			if selection {
				lines = append(
					lines,
					selectionPresentation("    ")+
						selectionSemantic(details),
				)
			} else {
				lines = append(lines, "    "+details)
			}
		}
	}

	// Suggested actions
	if len(entry.Suggestions) > 0 {
		lines = append(lines, "")
		heading := styles.Bold.Render("Suggested actions:")
		if selection {
			lines = append(
				lines,
				selectionPresentation("    ")+
					selectionSemantic(heading),
			)
		} else {
			lines = append(lines, "    "+heading)
		}
		for _, sug := range entry.Suggestions {
			actionLine := "      \u2022 " + sug.Label
			if sug.Command != "" {
				actionLine += " " + styles.Subtle.Render("("+sug.Command+")")
			}
			if selection {
				actionLine = selectionPresentation("      \u2022 ") +
					selectionSemantic(strings.TrimPrefix(
						actionLine,
						"      \u2022 ",
					))
			}
			lines = append(lines, contentEllipsize(profile, actionLine, width, 0, "..."))
			if sug.Description != "" {
				description := styles.Subtle.Render(sug.Description)
				descLine := "        " + description
				if selection {
					descLine = selectionPresentation("        ") +
						selectionSemantic(description)
				}
				lines = append(lines, contentEllipsize(profile, descLine, width, 0, "..."))
			}
		}
	}

	rendered := strings.Join(
		contentProjectRows(profile, lines, width, 0),
		"\n",
	)
	if selection {
		annotated, ok := selectionAnnotateRenderedRows(
			profile,
			rendered,
			0,
		)
		if ok {
			return annotated
		}
		return renderErrorEntryWithProfileMode(
			profile,
			entry,
			expanded,
			width,
			styles,
			false,
		)
	}
	return rendered
}

// --- Error creation helpers ---

// NewRateLimitError creates a structured error entry for rate limit situations.
func NewRateLimitError(retryAfter time.Duration, message string) ErrorEntry {
	return ErrorEntry{
		Severity:   SeverityInfo,
		Category:   CategoryRateLimit,
		Title:      "Rate Limited",
		Message:    message,
		RetryAfter: retryAfter,
		RetryAt:    time.Now().Add(retryAfter),
		Retryable:  true,
		Suggestions: []SuggestedAction{
			{Label: "Wait and retry", Description: fmt.Sprintf("The request will be retried after %s", formatRetryDuration(retryAfter))},
			{Label: "Change model", Command: "/model", Description: "Switch to a different model that may not be rate limited"},
		},
	}
}

// NewNetworkError creates a structured error entry for network failures.
func NewNetworkError(message string) ErrorEntry {
	return ErrorEntry{
		Severity:  SeverityWarning,
		Category:  CategoryNetwork,
		Title:     "Network Error",
		Message:   message,
		Retryable: true,
		Suggestions: []SuggestedAction{
			{Label: "Retry the request", Command: "/retry", Description: "Attempt the request again"},
			{Label: "Check connectivity", Description: "Verify network connection and proxy settings"},
		},
	}
}

// NewAuthError creates a structured error entry for authentication failures.
func NewAuthError(message string) ErrorEntry {
	return ErrorEntry{
		Severity:  SeverityError,
		Category:  CategoryAuth,
		Title:     "Authentication Failed",
		Message:   message,
		Retryable: false,
		Suggestions: []SuggestedAction{
			{Label: "Check API key", Description: "Verify your API key is valid and not expired"},
			{Label: "Reconfigure", Command: "/config", Description: "Update authentication configuration"},
		},
	}
}

// NewModelError creates a structured error entry for model-related issues.
func NewModelError(title, message string) ErrorEntry {
	return ErrorEntry{
		Severity:  SeverityWarning,
		Category:  CategoryModel,
		Title:     title,
		Message:   message,
		Retryable: true,
		Suggestions: []SuggestedAction{
			{Label: "Compact context", Command: "/compact", Description: "Reduce context size by summarizing earlier messages"},
			{Label: "Change model", Command: "/model", Description: "Switch to a model with larger context window"},
			{Label: "Clear and restart", Command: "/clear", Description: "Start a fresh conversation"},
		},
	}
}

// NewToolError creates a structured error entry for tool execution failures.
func NewToolError(toolName, message, details string) ErrorEntry {
	return ErrorEntry{
		Severity:  SeverityWarning,
		Category:  CategoryTool,
		Title:     fmt.Sprintf("Tool Failed: %s", toolName),
		Message:   message,
		Context:   toolName,
		Details:   details,
		Retryable: true,
		Suggestions: []SuggestedAction{
			{Label: "The agent will retry with a different approach"},
		},
	}
}

// NewConfigError creates a structured error entry for configuration issues.
func NewConfigError(message string) ErrorEntry {
	return ErrorEntry{
		Severity:  SeverityError,
		Category:  CategoryConfig,
		Title:     "Configuration Error",
		Message:   message,
		Retryable: false,
		Suggestions: []SuggestedAction{
			{Label: "Check configuration", Command: "/config", Description: "Review and update settings"},
		},
	}
}

// ClassifyError takes a raw error message and creates a structured ErrorEntry
// by analyzing the content for known patterns.
func ClassifyError(errMsg string) ErrorEntry {
	lower := strings.ToLower(errMsg)

	// Rate limit patterns
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "429") ||
		strings.Contains(lower, "too many requests") {
		return NewRateLimitError(30*time.Second, errMsg)
	}

	// Auth patterns
	if strings.Contains(lower, "unauthorized") || strings.Contains(lower, "401") ||
		strings.Contains(lower, "invalid api key") || strings.Contains(lower, "authentication") {
		return NewAuthError(errMsg)
	}

	// Network patterns
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "no such host") || strings.Contains(lower, "network") ||
		strings.Contains(lower, "dns") || strings.Contains(lower, "eof") {
		return NewNetworkError(errMsg)
	}

	// Model/context patterns
	if strings.Contains(lower, "context length") || strings.Contains(lower, "token limit") ||
		strings.Contains(lower, "too long") || strings.Contains(lower, "maximum context") {
		return NewModelError("Context Too Long", errMsg)
	}

	// Config patterns
	if strings.Contains(lower, "missing config") || strings.Contains(lower, "not configured") ||
		strings.Contains(lower, "invalid config") {
		return NewConfigError(errMsg)
	}

	// Default: generic error
	return ErrorEntry{
		Severity:  SeverityWarning,
		Category:  CategoryGeneral,
		Title:     "Error",
		Message:   errMsg,
		Retryable: true,
		Suggestions: []SuggestedAction{
			{Label: "Retry", Description: "The agent may retry automatically"},
		},
	}
}

// --- Style helpers ---

// errorIcon returns the appropriate icon for the severity level.
func errorIcon(severity ErrorSeverity, styles Styles) string {
	switch severity {
	case SeverityInfo:
		return styles.ToolRunning.Render("\u2139") // ℹ
	case SeverityWarning:
		return styles.Warning.Render("\u26a0") // ⚠
	case SeverityError:
		return styles.Error.Render("\u2716") // ✖
	}
	return "\u2022" // bullet
}

// errorTitleStyle returns the appropriate title style for the severity level.
func errorTitleStyle(severity ErrorSeverity, styles Styles) lipgloss.Style {
	switch severity {
	case SeverityInfo:
		return styles.ToolRunning.Bold(true)
	case SeverityWarning:
		return styles.Warning.Bold(true)
	case SeverityError:
		return styles.Error.Bold(true)
	}
	return styles.Bold
}

// wrapErrorText wraps error text to the given width.
func wrapErrorText(text string, width int) []string {
	return wrapErrorTextWithProfile(DefaultDisplayCellProfile(), text, width)
}

func wrapErrorTextWithProfile(
	profile DisplayCellProfile,
	text string,
	width int,
) []string {
	return contentWrapLines(profile, text, width, 4)
}

// formatRetryDuration formats a duration for retry countdown display.
func formatRetryDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}
