package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// StreamingRenderer manages the rendering of an in-progress assistant message,
// showing a cursor indicator at the end, a word/token count during streaming,
// and graceful handling of partial markdown.
//
// This component does not own the message data; it augments the rendering of
// an AssistantMessage while it is streaming. The ChatView integrates this
// renderer by calling its methods when rendering the current streaming message.
//
// Reference: claude-code-ripe FullscreenLayout.tsx — streaming assistant output
// with cursor, auto-scroll, and partial markdown handling.
type StreamingRenderer struct {
	// State tracking
	streaming     bool
	startTime     time.Time
	lastDelta     time.Time
	charCount     int
	wordCount     int
	tokenEstimate int // rough token estimate (~4 chars per token)

	// Cursor animation
	cursorVisible bool
	cursorBlink   int // incremented by spinner tick

	// Content tracking for partial markdown detection
	inCodeBlock    bool   // currently inside an unclosed fenced code block
	codeBlockFence string // the fence string (e.g., "```" or "~~~")
}

// NewStreamingRenderer creates a new streaming renderer.
func NewStreamingRenderer() *StreamingRenderer {
	return &StreamingRenderer{}
}

// StartStreaming begins a new streaming session.
func (s *StreamingRenderer) StartStreaming() {
	s.streaming = true
	s.startTime = time.Now()
	s.lastDelta = time.Now()
	s.charCount = 0
	s.wordCount = 0
	s.tokenEstimate = 0
	s.cursorVisible = true
	s.cursorBlink = 0
	s.inCodeBlock = false
	s.codeBlockFence = ""
}

// StopStreaming ends the current streaming session.
func (s *StreamingRenderer) StopStreaming() {
	s.streaming = false
}

// IsStreaming returns whether the renderer is currently in streaming mode.
func (s *StreamingRenderer) IsStreaming() bool {
	return s.streaming
}

// OnDelta processes a new streaming delta, updating word/char/token counts.
func (s *StreamingRenderer) OnDelta(delta string) {
	if delta == "" {
		return
	}
	s.lastDelta = time.Now()
	s.charCount += utf8.RuneCountInString(delta)
	s.tokenEstimate = (s.charCount + 3) / 4

	// Count words in delta (simplistic: split on whitespace)
	words := strings.Fields(delta)
	s.wordCount += len(words)

	// Track code block state for partial markdown awareness
	s.updateCodeBlockState(delta)
}

// OnTick advances cursor blink state. Called on spinner tick.
func (s *StreamingRenderer) OnTick() {
	if !s.streaming {
		return
	}
	s.cursorBlink++
	// Blink every 4 ticks (at 120ms/tick = ~480ms cycle)
	s.cursorVisible = (s.cursorBlink/4)%2 == 0
}

// updateCodeBlockState tracks whether we're inside a fenced code block.
func (s *StreamingRenderer) updateCodeBlockState(delta string) {
	// We need to check for fence markers in the accumulated content.
	// Rather than tracking full content, we look for fence patterns in the delta.
	lines := strings.Split(delta, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if s.inCodeBlock {
			// Check if this line closes the code block
			if strings.HasPrefix(trimmed, s.codeBlockFence) &&
				strings.TrimSpace(strings.TrimPrefix(trimmed, s.codeBlockFence)) == "" {
				s.inCodeBlock = false
				s.codeBlockFence = ""
			}
		} else {
			// Check if this line opens a code block
			if fence := extractFence(trimmed); fence != "" {
				s.inCodeBlock = true
				s.codeBlockFence = fence
			}
		}
	}
}

// extractFence returns the fence string if the line opens a fenced code block.
// Returns empty string if not a fence opener.
func extractFence(line string) string {
	if len(line) < 3 {
		return ""
	}
	ch := line[0]
	if ch != '`' && ch != '~' {
		return ""
	}
	run := 0
	for run < len(line) && line[run] == ch {
		run++
	}
	if run < 3 {
		return ""
	}
	return strings.Repeat(string(ch), run)
}

// RenderCursor returns the cursor indicator string to append to the last line
// of the streaming message. Uses a block cursor that blinks.
func (s *StreamingRenderer) RenderCursor(styles Styles) string {
	if !s.streaming {
		return ""
	}
	if s.cursorVisible {
		return styles.ToolRunning.Render("\u2588") // full block in amber
	}
	return " " // space to maintain width when cursor is "off"
}

// RenderStreamingIndicator returns the streaming status indicator line shown
// below the message content. Displays word count, token estimate, and elapsed time.
func (s *StreamingRenderer) RenderStreamingIndicator(styles Styles, width int) string {
	return s.renderStreamingIndicator(
		DefaultDisplayCellProfile(),
		styles,
		width,
	)
}

func (s *StreamingRenderer) renderStreamingIndicator(
	profile DisplayCellProfile,
	styles Styles,
	width int,
) string {
	if !s.streaming {
		return ""
	}

	elapsed := time.Since(s.startTime)
	elapsedStr := formatStreamingDuration(elapsed)

	// Build status parts
	var parts []string
	if s.wordCount > 0 {
		parts = append(parts, fmt.Sprintf("%d words", s.wordCount))
	}
	if s.tokenEstimate > 0 {
		parts = append(parts, fmt.Sprintf("~%s tokens", formatTokensShort(s.tokenEstimate)))
	}
	if elapsedStr != "" {
		parts = append(parts, elapsedStr)
	}

	if len(parts) == 0 {
		return ""
	}

	// Show partial markdown warning if inside an unclosed code block
	var warning string
	if s.inCodeBlock {
		warning = styles.Subtle.Render(" (code block streaming...)")
	}

	indicator := "  " + styles.Subtle.Render(strings.Join(parts, " \u00b7 ")) + warning

	return profile.truncate(indicator, width)
}

// InCodeBlock returns whether the stream is currently inside an unclosed code block.
// Used by the chat view to decide whether to close a partial fence for rendering.
func (s *StreamingRenderer) InCodeBlock() bool {
	return s.inCodeBlock
}

// CodeBlockFence returns the current open fence string (e.g., "```").
func (s *StreamingRenderer) CodeBlockFence() string {
	return s.codeBlockFence
}

// ElapsedTime returns the duration since streaming started.
func (s *StreamingRenderer) ElapsedTime() time.Duration {
	if s.startTime.IsZero() {
		return 0
	}
	return time.Since(s.startTime)
}

// WordCount returns the current word count.
func (s *StreamingRenderer) WordCount() int {
	return s.wordCount
}

// TokenEstimate returns the estimated token count.
func (s *StreamingRenderer) TokenEstimate() int {
	return s.tokenEstimate
}

// formatStreamingDuration formats elapsed time for the streaming indicator.
func formatStreamingDuration(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// --- StreamingMessage ---

// StreamingMessage wraps an AssistantMessage with streaming-aware rendering
// that includes cursor indicator, partial markdown handling, and statistics.
// It implements ChatItem so it can be placed directly in the chat view.
type StreamingMessage struct {
	content  string
	renderer *StreamingRenderer
	styles   Styles
	version  uint64
	finished bool

	// Underlying markdown renderer with prefix caching
	streamingMd StreamingMarkdown
}

// NewStreamingMessage creates a new streaming message item.
func NewStreamingMessage(styles Styles) *StreamingMessage {
	return &StreamingMessage{
		renderer: NewStreamingRenderer(),
		styles:   styles,
		version:  1,
	}
}

// Start begins the streaming session.
func (m *StreamingMessage) Start() {
	m.renderer.StartStreaming()
}

// AppendContent adds streaming content.
func (m *StreamingMessage) AppendContent(delta string) {
	m.content += delta
	m.renderer.OnDelta(delta)
	m.version++
}

// Finish marks the message as complete.
func (m *StreamingMessage) Finish() {
	m.renderer.StopStreaming()
	m.finished = true
	m.version++
}

// Tick advances cursor animation.
func (m *StreamingMessage) Tick() {
	m.renderer.OnTick()
	if m.renderer.IsStreaming() {
		m.version++ // force re-render on tick for cursor animation
	}
}

// Render implements ChatItem.
func (m *StreamingMessage) Render(width int, styles Styles) string {
	return m.RenderWithEnvironment(width, defaultRenderEnvironment(styles))
}

func (m *StreamingMessage) RenderWithEnvironment(width int, env RenderEnvironment) string {
	lines := m.RenderLinesWithEnvironment(width, env)
	return strings.Join(lines, "\n")
}

// RenderLines renders the streaming message with cursor and indicator.
func (m *StreamingMessage) RenderLines(width int, styles Styles) []string {
	return m.RenderLinesWithEnvironment(width, defaultRenderEnvironment(styles))
}

func (m *StreamingMessage) RenderLinesWithEnvironment(width int, env RenderEnvironment) []string {
	env = env.normalized()
	styles := env.styles
	profile := env.profile
	if m.content == "" && m.renderer.IsStreaming() {
		return []string{"  " + styles.Subtle.Render("...") + m.renderer.RenderCursor(styles)}
	}
	if m.content == "" {
		return []string{"  " + styles.Subtle.Render("...")}
	}

	contentWidth := width - 4
	if contentWidth > 120 {
		contentWidth = 120
	}
	if contentWidth < 10 {
		contentWidth = 10
	}

	// Handle partial markdown: if inside an unclosed code block, temporarily
	// close it for rendering purposes to prevent broken output.
	renderContent := m.content
	if m.renderer.InCodeBlock() && !m.finished {
		renderContent += "\n" + m.renderer.CodeBlockFence()
	}

	// Use the streaming markdown renderer for efficient prefix-cached rendering
	rendered := m.streamingMd.renderWithEnvironment(renderContent, contentWidth, env)

	// Split into lines and format with prefix
	renderedLines := strings.Split(rendered, "\n")
	result := make([]string, 0, len(renderedLines)+2)

	for i, line := range renderedLines {
		line = profile.truncate(line, contentWidth)
		if i == 0 {
			result = append(result, styles.AssistantPrefix.Render(assistantIdentityGlyph)+" "+line)
		} else {
			result = append(result, "  "+line)
		}
	}

	// Append cursor to last line if streaming
	if m.renderer.IsStreaming() && len(result) > 0 {
		lastIdx := len(result) - 1
		result[lastIdx] += m.renderer.RenderCursor(styles)
	}

	// Append streaming indicator line
	if indicator := m.renderer.renderStreamingIndicator(profile, styles, width); indicator != "" {
		result = append(result, indicator)
	}

	return result
}

// Finished implements ChatItem.
func (m *StreamingMessage) Finished() bool {
	return m.finished
}

// Version implements ChatItem.
func (m *StreamingMessage) Version() uint64 {
	return m.version
}

func (m *StreamingMessage) NoSelectPrefix() int { return 0 }
func (m *StreamingMessage) Selectable() bool    { return true }

func (m *StreamingMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	env := ctx.Environment
	styles, profile := env.styles, env.profile
	if m.content == "" {
		rendered := selectionPresentation("  ") +
			styles.Subtle.Render(selectionSemantic("..."))
		if m.renderer.IsStreaming() {
			rendered += selectionPresentation(m.renderer.RenderCursor(styles))
		}
		return selectionAnnotatedRender{
			rendered: rendered, annotated: true,
		}
	}
	contentWidth := ctx.Width - 4
	if contentWidth > 120 {
		contentWidth = 120
	}
	if contentWidth < 10 {
		contentWidth = 10
	}
	renderContent := m.content
	if m.renderer.InCodeBlock() && !m.finished {
		renderContent += "\n" + m.renderer.CodeBlockFence()
	}
	rendered, annotated := m.streamingMd.renderSelectionWithEnvironment(
		renderContent,
		contentWidth,
		env,
	)
	renderedLines := strings.Split(rendered, "\n")
	result := make([]string, 0, len(renderedLines)+2)
	for index, line := range renderedLines {
		line = selectionTruncateAnnotatedLine(
			profile,
			line,
			contentWidth,
		)
		if index == 0 {
			result = append(
				result,
				selectionPresentation(
					styles.AssistantPrefix.Render(
						assistantIdentityGlyph,
					)+" ",
				)+line,
			)
		} else {
			result = append(result, selectionPresentation("  ")+line)
		}
	}
	if m.renderer.IsStreaming() && len(result) > 0 {
		last := len(result) - 1
		result[last] += selectionPresentation(
			m.renderer.RenderCursor(styles),
		)
	}
	if indicator := m.renderer.renderStreamingIndicator(
		profile,
		styles,
		ctx.Width,
	); indicator != "" {
		result = append(result, selectionPresentation(indicator))
	}
	return selectionAnnotatedRender{
		rendered:  strings.Join(result, "\n"),
		annotated: annotated,
	}
}

func (m *StreamingMessage) RenderRaw(HistoryRenderContext) string {
	return m.content
}

func (m *StreamingMessage) RenderTranscript(HistoryRenderContext) string {
	return m.content
}

// --- Integration helpers for ChatView ---

// RenderAssistantWithCursor augments an existing assistant message render
// with a streaming cursor at the end of the last content line.
// This is used by the chat view's render path when the current assistant
// message is still streaming.
func RenderAssistantWithCursor(lines []string, streaming *StreamingRenderer, styles Styles) []string {
	if streaming == nil || !streaming.IsStreaming() {
		return lines
	}
	if len(lines) == 0 {
		return lines
	}

	// Append cursor to the last line
	result := make([]string, len(lines))
	copy(result, lines)
	lastIdx := len(result) - 1
	result[lastIdx] += streaming.RenderCursor(styles)

	// Add streaming indicator as an extra line
	if indicator := streaming.RenderStreamingIndicator(styles, 120); indicator != "" {
		result = append(result, indicator)
	}

	return result
}

// PreparePartialMarkdown handles incomplete markdown in streaming content.
// If the content ends with an unclosed code fence, it appends the closing
// fence so that glamour can render it without producing broken output.
func PreparePartialMarkdown(content string) string {
	if content == "" {
		return content
	}

	// Quick check: does the content contain any code fences?
	if !strings.Contains(content, "```") && !strings.Contains(content, "~~~") {
		return content
	}

	// Count fence opens/closes
	lines := strings.Split(content, "\n")
	var openFence string
	inFence := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inFence {
			// Check close
			if strings.HasPrefix(trimmed, openFence) {
				rest := strings.TrimSpace(trimmed[len(openFence):])
				if rest == "" {
					inFence = false
					openFence = ""
				}
			}
		} else {
			if fence := extractFence(trimmed); fence != "" {
				inFence = true
				openFence = fence
			}
		}
	}

	// If we ended inside a fence, close it
	if inFence && openFence != "" {
		return content + "\n" + openFence
	}
	return content
}

// StreamingStats holds statistics about the current streaming session.
type StreamingStats struct {
	Words         int
	Chars         int
	TokenEstimate int
	Elapsed       time.Duration
	InCodeBlock   bool
}

// GetStats returns current streaming statistics.
func (s *StreamingRenderer) GetStats() StreamingStats {
	return StreamingStats{
		Words:         s.wordCount,
		Chars:         s.charCount,
		TokenEstimate: s.tokenEstimate,
		Elapsed:       s.ElapsedTime(),
		InCodeBlock:   s.inCodeBlock,
	}
}

// --- Streaming cursor styles ---

// StreamingCursorStyle returns the lipgloss style for the streaming cursor.
func StreamingCursorStyle() lipgloss.Style {
	return defaultStyles().ToolRunning
}
