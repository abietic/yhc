package analytics

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// EventType represents the category of an analytics event.
type EventType string

const (
	EventToolCall     EventType = "tool_call"
	EventModelCall    EventType = "model_call"
	EventSessionStart EventType = "session_start"
	EventSessionEnd   EventType = "session_end"
	EventCompaction   EventType = "compaction"
	EventError        EventType = "error"
	EventPermission   EventType = "permission"
	EventCommand      EventType = "command"
)

// AnalyticsEvent represents a single tracked event.
type AnalyticsEvent struct {
	Type      EventType      `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	SessionID string         `json:"session_id"`
	Data      map[string]any `json:"data,omitempty"`
}

// SessionStats holds aggregated statistics for a session.
type SessionStats struct {
	SessionID       string         `json:"session_id"`
	StartTime       time.Time      `json:"start_time"`
	Duration        time.Duration  `json:"duration"`
	TurnCount       int            `json:"turn_count"`
	ToolCallCount   int            `json:"tool_call_count"`
	ToolCallsByName map[string]int `json:"tool_calls_by_name"`
	InputTokens     int            `json:"input_tokens"`
	OutputTokens    int            `json:"output_tokens"`
	CompactionCount int            `json:"compaction_count"`
	ErrorCount      int            `json:"error_count"`
	Model           string         `json:"model"`
}

// Tracker records analytics events and session statistics.
type Tracker struct {
	mu       sync.Mutex
	events   []AnalyticsEvent
	stats    *SessionStats
	disabled bool
}

// NewTracker creates a new analytics tracker for the given session.
func NewTracker(sessionID, model string) *Tracker {
	now := time.Now()
	return &Tracker{
		events: make([]AnalyticsEvent, 0),
		stats: &SessionStats{
			SessionID:       sessionID,
			StartTime:       now,
			ToolCallsByName: make(map[string]int),
			Model:           model,
		},
	}
}

// Track records an analytics event.
func (t *Tracker) Track(eventType EventType, data map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.disabled {
		return
	}

	event := AnalyticsEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		SessionID: t.stats.SessionID,
		Data:      data,
	}
	t.events = append(t.events, event)

	switch eventType {
	case EventCompaction:
		t.stats.CompactionCount++
	case EventError:
		t.stats.ErrorCount++
	case EventModelCall:
		t.stats.TurnCount++
	}
}

// TrackToolCall is a convenience method for tool call events.
func (t *Tracker) TrackToolCall(toolName string, durationMs int64, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.disabled {
		return
	}

	event := AnalyticsEvent{
		Type:      EventToolCall,
		Timestamp: time.Now(),
		SessionID: t.stats.SessionID,
		Data: map[string]any{
			"tool_name":   toolName,
			"duration_ms": durationMs,
			"success":     success,
		},
	}
	t.events = append(t.events, event)

	t.stats.ToolCallCount++
	t.stats.ToolCallsByName[toolName]++
}

// TrackModelCall records a model API call with usage.
func (t *Tracker) TrackModelCall(inputTokens, outputTokens int, model string, durationMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.disabled {
		return
	}

	event := AnalyticsEvent{
		Type:      EventModelCall,
		Timestamp: time.Now(),
		SessionID: t.stats.SessionID,
		Data: map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"model":         model,
			"duration_ms":   durationMs,
		},
	}
	t.events = append(t.events, event)

	t.stats.TurnCount++
	t.stats.InputTokens += inputTokens
	t.stats.OutputTokens += outputTokens
	if model != "" {
		t.stats.Model = model
	}
}

// GetStats returns the current session statistics.
func (t *Tracker) GetStats() *SessionStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stats.Duration = time.Since(t.stats.StartTime)
	statsCopy := *t.stats
	statsCopy.ToolCallsByName = make(map[string]int, len(t.stats.ToolCallsByName))
	for k, v := range t.stats.ToolCallsByName {
		statsCopy.ToolCallsByName[k] = v
	}
	return &statsCopy
}

// GetEvents returns all recorded events.
func (t *Tracker) GetEvents() []AnalyticsEvent {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make([]AnalyticsEvent, len(t.events))
	copy(result, t.events)
	return result
}

// Disable turns off tracking (for privacy).
func (t *Tracker) Disable() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.disabled = true
}

// Export writes analytics to a JSON file.
func (t *Tracker) Export(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stats.Duration = time.Since(t.stats.StartTime)

	payload := struct {
		Stats  *SessionStats    `json:"stats"`
		Events []AnalyticsEvent `json:"events"`
	}{
		Stats:  t.stats,
		Events: t.events,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal analytics: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write analytics file: %w", err)
	}

	return nil
}

// Summary returns a human-readable summary of the session.
func (t *Tracker) Summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	duration := time.Since(t.stats.StartTime)

	var b strings.Builder
	b.WriteString("Session Summary:\n")
	fmt.Fprintf(&b, "  Duration: %s\n", formatDuration(duration))
	fmt.Fprintf(&b, "  Turns: %d\n", t.stats.TurnCount)
	fmt.Fprintf(&b, "  Tool calls: %d", t.stats.ToolCallCount)

	if len(t.stats.ToolCallsByName) > 0 {
		b.WriteString(" (")
		b.WriteString(formatToolBreakdown(t.stats.ToolCallsByName))
		b.WriteString(")")
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "  Tokens: %s input / %s output\n",
		formatNumber(t.stats.InputTokens), formatNumber(t.stats.OutputTokens))
	fmt.Fprintf(&b, "  Compactions: %d\n", t.stats.CompactionCount)
	fmt.Fprintf(&b, "  Errors: %d\n", t.stats.ErrorCount)

	return b.String()
}

// formatDuration formats a duration as "Xm Ys" or "Xs" for short durations.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60

	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// formatNumber formats an integer with comma separators.
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	var result strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		result.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if result.Len() > 0 {
			result.WriteByte(',')
		}
		result.WriteString(s[i : i+3])
	}
	return result.String()
}

// formatToolBreakdown formats tool call counts sorted by frequency descending.
func formatToolBreakdown(tools map[string]int) string {
	type toolCount struct {
		name  string
		count int
	}

	counts := make([]toolCount, 0, len(tools))
	for name, count := range tools {
		counts = append(counts, toolCount{name: name, count: count})
	}
	sort.Slice(counts, func(i, j int) bool {
		return counts[i].count > counts[j].count
	})

	parts := make([]string, len(counts))
	for i, tc := range counts {
		parts[i] = fmt.Sprintf("%s: %d", tc.name, tc.count)
	}
	return strings.Join(parts, ", ")
}
