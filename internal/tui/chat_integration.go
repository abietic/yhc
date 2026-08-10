package tui

import (
	"fmt"
	"strings"
	"time"
)

// --- Streaming Integration ---
// StreamingState tracks the streaming lifecycle within the chat loop.
type StreamingState int

const (
	StreamingIdle     StreamingState = iota // No active streaming
	StreamingActive                         // Assistant is currently streaming
	StreamingComplete                       // Stream finished, content finalized
)

// ChatStreamingContext holds streaming state for the chat view integration.
// Wires the StreamingRenderer into the main chat loop by tracking streaming
// state transitions and providing cursor/indicator rendering.
type ChatStreamingContext struct {
	state    StreamingState
	renderer *StreamingRenderer

	// Token tracking during streaming
	totalTokens int
	startTime   time.Time
}

// NewChatStreamingContext creates a new streaming integration context.
func NewChatStreamingContext() *ChatStreamingContext {
	return &ChatStreamingContext{
		state:    StreamingIdle,
		renderer: NewStreamingRenderer(),
	}
}

// BeginStreaming transitions to active streaming state.
func (c *ChatStreamingContext) BeginStreaming() {
	c.state = StreamingActive
	c.renderer.StartStreaming()
	c.startTime = time.Now()
	c.totalTokens = 0
}

// OnStreamDelta processes an incoming streaming delta.
func (c *ChatStreamingContext) OnStreamDelta(delta string) {
	if c.state != StreamingActive {
		return
	}
	c.renderer.OnDelta(delta)
	c.totalTokens = c.renderer.TokenEstimate()
}

// EndStreaming transitions to complete state.
func (c *ChatStreamingContext) EndStreaming() {
	c.state = StreamingComplete
	c.renderer.StopStreaming()
}

// Reset returns to idle state.
func (c *ChatStreamingContext) Reset() {
	c.state = StreamingIdle
	c.totalTokens = 0
	c.startTime = time.Time{}
}

// IsStreaming returns whether streaming is currently active.
func (c *ChatStreamingContext) IsStreaming() bool {
	return c.state == StreamingActive
}

// State returns the current streaming state.
func (c *ChatStreamingContext) State() StreamingState {
	return c.state
}

// Tick advances the cursor blink animation.
func (c *ChatStreamingContext) Tick() {
	c.renderer.OnTick()
}

// RenderCursor returns the cursor string for the streaming message.
func (c *ChatStreamingContext) RenderCursor(styles Styles) string {
	return c.renderer.RenderCursor(styles)
}

// RenderIndicator returns the streaming stats indicator line.
func (c *ChatStreamingContext) RenderIndicator(styles Styles, width int) string {
	return c.renderer.RenderStreamingIndicator(styles, width)
}

// TokenCount returns the estimated token count during streaming.
func (c *ChatStreamingContext) TokenCount() int {
	return c.totalTokens
}

// ElapsedTime returns duration since streaming began.
func (c *ChatStreamingContext) ElapsedTime() time.Duration {
	if c.startTime.IsZero() {
		return 0
	}
	return time.Since(c.startTime)
}

// --- Permission Queue Integration ---
// PermissionQueue manages concurrent permission requests, ensuring only one
// is shown at a time while others wait in a FIFO queue.
// Reference: claude-code-ripe serializes permission prompts to prevent
// response channel overwrites.
type PermissionQueue struct {
	pending []permissionQueueEntry
	active  *permissionQueueEntry
	prompt  *PermissionPrompt
}

type permissionQueueEntry struct {
	tool         string
	input        string
	sessionScope string
	responseCh   chan<- PermissionResponse
	enqueuedAt   time.Time
}

// NewPermissionQueue creates a permission queue with an associated prompt.
func NewPermissionQueue(styles Styles) *PermissionQueue {
	return &PermissionQueue{
		prompt: NewPermissionPrompt(styles),
	}
}

func (q *PermissionQueue) SetStyles(styles Styles) {
	q.prompt.SetStyles(styles)
}

// Enqueue adds a permission request to the queue.
// If no request is currently active, it immediately activates.
func (q *PermissionQueue) Enqueue(tool, input, sessionScope string, responseCh chan<- PermissionResponse) {
	entry := permissionQueueEntry{
		tool:         tool,
		input:        input,
		sessionScope: sessionScope,
		responseCh:   responseCh,
		enqueuedAt:   time.Now(),
	}

	if q.active == nil {
		q.activate(entry)
	} else {
		q.pending = append(q.pending, entry)
	}
}

// activate shows the prompt for the given entry.
func (q *PermissionQueue) activate(entry permissionQueueEntry) {
	q.active = &entry
	q.prompt.Show(entry.tool, entry.input, entry.sessionScope, entry.responseCh)
}

// AdvanceQueue moves to the next pending request after the current one is resolved.
// Returns true if another request was activated.
func (q *PermissionQueue) AdvanceQueue() bool {
	q.active = nil
	if len(q.pending) == 0 {
		return false
	}
	next := q.pending[0]
	q.pending = q.pending[1:]
	q.activate(next)
	return true
}

// PendingCount returns the number of queued (not yet active) requests.
func (q *PermissionQueue) PendingCount() int {
	return len(q.pending)
}

// IsActive returns whether a permission prompt is currently displayed.
func (q *PermissionQueue) IsActive() bool {
	return q.active != nil && q.prompt.IsVisible()
}

// ForceCloseAll denies all pending and active requests.
func (q *PermissionQueue) ForceCloseAll() {
	if q.prompt.IsVisible() {
		q.prompt.ForceClose()
	}
	for _, entry := range q.pending {
		if entry.responseCh != nil {
			entry.responseCh <- PermissionDeny
		}
	}
	q.pending = nil
	q.active = nil
}

// Prompt returns the underlying PermissionPrompt for rendering and key handling.
func (q *PermissionQueue) Prompt() *PermissionPrompt {
	return q.prompt
}

// --- Multiline Input State ---
// MultilineState tracks the state of multiline input editing.
type MultilineState struct {
	// Whether the input is currently multiline (has newlines)
	isMultiline bool
	// Number of lines in the current input
	lineCount int
}

// NewMultilineState creates a new multiline input state tracker.
func NewMultilineState() *MultilineState {
	return &MultilineState{}
}

// Update recalculates multiline state from the current input value.
func (m *MultilineState) Update(value string) {
	m.lineCount = strings.Count(value, "\n") + 1
	m.isMultiline = m.lineCount > 1
}

// IsMultiline returns whether the input currently spans multiple lines.
func (m *MultilineState) IsMultiline() bool {
	return m.isMultiline
}

// LineCount returns the number of lines in the input.
func (m *MultilineState) LineCount() int {
	return m.lineCount
}

// ShouldSubmitOnEnter reports the editor's Enter behavior.
func (m *MultilineState) ShouldSubmitOnEnter() bool {
	return true
}

// RenderLineIndicator renders the line count indicator for the editor.
// Only shown when input is multiline.
func (m *MultilineState) RenderLineIndicator(styles Styles) string {
	if !m.isMultiline {
		return ""
	}
	return styles.Subtle.Render(fmt.Sprintf("[%d lines]", m.lineCount))
}

// RenderSubmitHint returns the submit key hint.
func (m *MultilineState) RenderSubmitHint(styles Styles) string {
	return styles.Subtle.Render("enter send")
}

// --- Thinking Indicator ---
// ThinkingIndicator provides an animated thinking/reasoning indicator
// displayed during extended model reasoning (thinking tokens).
// Matches reference Spinner.tsx: shows "Thinking..." while active,
// then "Thought for Xs" for minimum 2s after completion.
type ThinkingIndicator struct {
	active    bool
	startTime time.Time
	tick      int
	// Post-thinking display: shows duration after thinking finishes
	showingResult  bool
	resultDuration time.Duration
	resultShownAt  time.Time

	// Animation frames for thinking (matches reference ellipsis animation)
	frames []string
}

// NewThinkingIndicator creates a new thinking indicator.
func NewThinkingIndicator() *ThinkingIndicator {
	return &ThinkingIndicator{
		frames: []string{"   ", ".  ", ".. ", "..."},
	}
}

// Start begins the thinking animation.
func (t *ThinkingIndicator) Start() {
	t.active = true
	t.startTime = time.Now()
	t.tick = 0
	t.showingResult = false
}

// Stop ends the thinking animation and transitions to showing duration.
// The duration display persists for minimum 2s (matching reference).
func (t *ThinkingIndicator) Stop() {
	if t.active {
		t.resultDuration = time.Since(t.startTime)
		t.resultShownAt = time.Now()
		t.showingResult = true
	}
	t.active = false
}

// IsActive returns whether the indicator is actively thinking.
func (t *ThinkingIndicator) IsActive() bool {
	return t.active
}

// IsVisible returns whether anything should be rendered (active or post-result).
func (t *ThinkingIndicator) IsVisible() bool {
	if t.active {
		return true
	}
	if t.showingResult {
		if time.Since(t.resultShownAt) < 2*time.Second {
			return true
		}
		t.showingResult = false
	}
	return false
}

// Tick advances the animation frame.
func (t *ThinkingIndicator) Tick() {
	if !t.active {
		return
	}
	t.tick++
}

// Elapsed returns the duration since thinking started.
func (t *ThinkingIndicator) Elapsed() time.Duration {
	if t.startTime.IsZero() {
		return 0
	}
	if t.active {
		return time.Since(t.startTime)
	}
	return t.resultDuration
}

// Render renders the thinking indicator with animation and elapsed time.
// During thinking: "* Thinking... Xs"
// After thinking: "\u2713 Thought for Xs" (persists 2s)
func (t *ThinkingIndicator) Render(styles Styles) string {
	if !t.active && !t.showingResult {
		return ""
	}

	if t.showingResult {
		if time.Since(t.resultShownAt) >= 2*time.Second {
			t.showingResult = false
			return ""
		}
		icon := styles.ToolSuccess.Render("\u2713")
		durStr := formatDurationShort(t.resultDuration)
		text := styles.Subtle.Render("Thought for " + durStr)
		return "  " + icon + " " + text
	}

	frame := t.frames[t.tick%len(t.frames)]
	elapsed := t.Elapsed()

	var elapsedStr string
	if elapsed >= time.Second {
		elapsedStr = " " + formatDurationShort(elapsed)
	}

	icon := styles.ToolRunning.Render("*")
	text := styles.Subtle.Render("Thinking" + frame)
	timeStr := styles.Subtle.Render(elapsedStr)

	return "  " + icon + " " + text + timeStr
}

// --- Tool Progress Display ---
// ToolProgressEntry represents a single tool's execution progress.
type ToolProgressEntry struct {
	ToolCallID string
	AttemptID  string
	Name       string
	Desc       string
	StartTime  time.Time
	Output     string // live output (last few lines for Bash)
	Expanded   bool   // whether live output is expanded
	Status     ToolStatus
}

// ToolProgressDisplay manages the display of actively running tools
// with progress indicators, elapsed time, and live output preview.
type ToolProgressDisplay struct {
	entries map[string]*ToolProgressEntry
	order   []string // insertion order
}

// NewToolProgressDisplay creates a new tool progress display.
func NewToolProgressDisplay() *ToolProgressDisplay {
	return &ToolProgressDisplay{
		entries: make(map[string]*ToolProgressEntry),
	}
}

// StartTool records a new tool execution.
func (d *ToolProgressDisplay) StartTool(toolCallID, name, input string) {
	d.StartToolAttempt(toolCallID, name, input, "")
}

// StartToolAttempt records one attempt-owned model tool projection. A reused
// call ID from a later attempt replaces only the progress owner for that ID.
func (d *ToolProgressDisplay) StartToolAttempt(
	toolCallID, name, input, attemptID string,
) {
	if toolCallID == "" {
		return
	}
	_, exists := d.entries[toolCallID]
	if existing := d.entries[toolCallID]; existing != nil &&
		(attemptID == "" || existing.AttemptID == attemptID) {
		return
	}
	entry := &ToolProgressEntry{
		ToolCallID: toolCallID,
		AttemptID:  attemptID,
		Name:       name,
		Desc:       getToolDescription(name, input, ToolRunning),
		StartTime:  time.Now(),
		Status:     ToolRunning,
	}
	d.entries[toolCallID] = entry
	if !exists {
		d.order = append(d.order, toolCallID)
	}
}

// UpdateProgress updates the live output for a running tool.
func (d *ToolProgressDisplay) UpdateProgress(toolCallID, content string) {
	if entry, ok := d.entries[toolCallID]; ok && entry.Status == ToolRunning {
		entry.Output = content
	}
}

// CompleteTool marks a tool as finished and removes it from display.
func (d *ToolProgressDisplay) CompleteTool(toolCallID string, status ToolStatus) {
	d.CompleteToolAttempt(toolCallID, status, "")
}

// CompleteToolAttempt removes progress only when the tombstoned attempt still
// owns the call ID. An empty attempt ID retains the canonical completion path.
func (d *ToolProgressDisplay) CompleteToolAttempt(
	toolCallID string,
	_ ToolStatus,
	attemptID string,
) {
	entry, ok := d.entries[toolCallID]
	if !ok || (attemptID != "" && entry.AttemptID != attemptID) {
		return
	}
	delete(d.entries, toolCallID)
	for i, id := range d.order {
		if id == toolCallID {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
}

// ToggleExpand toggles the expanded state of the most recent running tool.
func (d *ToolProgressDisplay) ToggleExpand(toolCallID string) {
	if entry, ok := d.entries[toolCallID]; ok {
		entry.Expanded = !entry.Expanded
	}
}

// HasRunning returns whether any tools are currently running.
func (d *ToolProgressDisplay) HasRunning() bool {
	return len(d.entries) > 0
}

// Count returns the number of running tools.
func (d *ToolProgressDisplay) Count() int {
	return len(d.entries)
}

// Reset clears all tracked tools.
func (d *ToolProgressDisplay) Reset() {
	d.entries = make(map[string]*ToolProgressEntry)
	d.order = nil
}

// Render renders the tool progress display with elapsed times and live output.
func (d *ToolProgressDisplay) Render(styles Styles, spinnerCount, width int) string {
	if len(d.entries) == 0 {
		return ""
	}

	var lines []string
	maxShow := 5 // Cap visible tools

	shown := 0
	for _, id := range d.order {
		entry, ok := d.entries[id]
		if !ok {
			continue
		}
		if shown >= maxShow {
			remaining := len(d.order) - shown
			lines = append(lines, fmt.Sprintf("    %s", styles.Subtle.Render(fmt.Sprintf("+%d more tools running", remaining))))
			break
		}

		// Tool name with spinner icon
		icon := toolIcon(styles, ToolRunning, spinnerCount+shown)
		elapsed := time.Since(entry.StartTime)
		elapsedStr := formatToolElapsed(elapsed)

		desc := entry.Name
		if entry.Desc != "" {
			desc += " " + styles.Subtle.Render(entry.Desc)
		}
		if elapsedStr != "" {
			desc += " " + styles.Dim.Render(elapsedStr)
		}

		lines = append(lines, fmt.Sprintf("    %s %s", icon, desc))

		// Live output preview for Bash tools (last 3 lines)
		if entry.Name == "Bash" && entry.Output != "" && entry.Expanded {
			outputLines := strings.Split(entry.Output, "\n")
			maxPreview := 3
			start := len(outputLines) - maxPreview
			if start < 0 {
				start = 0
			}
			preview := outputLines[start:]
			maxWidth := width - 8
			if maxWidth < 20 {
				maxWidth = 20
			}
			for _, ol := range preview {
				if len(ol) > maxWidth {
					ol = ol[:maxWidth-3] + "..."
				}
				lines = append(lines, "      "+styles.Dim.Render(ol))
			}
		} else if entry.Name == "Bash" && entry.Output != "" && !entry.Expanded {
			// Show collapsed hint
			outLines := strings.Count(entry.Output, "\n") + 1
			if outLines > 0 {
				lines = append(lines, "      "+styles.Dim.Render(fmt.Sprintf("(%d lines, tab to expand)", outLines)))
			}
		}

		shown++
	}

	return strings.Join(lines, "\n")
}

// formatToolElapsed formats elapsed time for tool progress display.
func formatToolElapsed(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
