package analytics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrackerAggregatesEventsAndStats(t *testing.T) {
	tracker := NewTracker("session-1", "claude-initial")
	tracker.Track(EventSessionStart, map[string]any{"source": "test"})
	tracker.TrackToolCall("Read", 12, true)
	tracker.TrackToolCall("Read", 8, false)
	tracker.TrackToolCall("Write", 20, true)
	tracker.TrackModelCall(1200, 3400, "claude-final", 55)
	tracker.Track(EventCompaction, nil)
	tracker.Track(EventError, map[string]any{"kind": "boom"})

	stats := tracker.GetStats()
	if stats.SessionID != "session-1" {
		t.Fatalf("session id = %q", stats.SessionID)
	}
	if stats.ToolCallCount != 3 || stats.ToolCallsByName["Read"] != 2 || stats.ToolCallsByName["Write"] != 1 {
		t.Fatalf("unexpected tool stats: %#v", stats)
	}
	if stats.TurnCount != 1 || stats.InputTokens != 1200 || stats.OutputTokens != 3400 {
		t.Fatalf("unexpected model stats: %#v", stats)
	}
	if stats.CompactionCount != 1 || stats.ErrorCount != 1 || stats.Model != "claude-final" {
		t.Fatalf("unexpected aggregate stats: %#v", stats)
	}

	events := tracker.GetEvents()
	if len(events) != 7 {
		t.Fatalf("expected 7 events, got %#v", events)
	}
	if events[1].Type != EventToolCall || events[1].Data["tool_name"] != "Read" {
		t.Fatalf("unexpected tool event: %#v", events[1])
	}
	if events[4].Type != EventModelCall || events[4].Data["model"] != "claude-final" {
		t.Fatalf("unexpected model event: %#v", events[4])
	}
}

func TestStatsAndEventsAreDefensiveCopies(t *testing.T) {
	tracker := NewTracker("session-copy", "claude")
	tracker.TrackToolCall("Read", 1, true)

	stats := tracker.GetStats()
	stats.ToolCallsByName["Read"] = 99
	if got := tracker.GetStats().ToolCallsByName["Read"]; got != 1 {
		t.Fatalf("stats copy mutated tracker state: %d", got)
	}

	events := tracker.GetEvents()
	events[0].Type = EventError
	if got := tracker.GetEvents()[0].Type; got != EventToolCall {
		t.Fatalf("events copy mutated tracker state: %s", got)
	}
}

func TestDisableStopsFutureTracking(t *testing.T) {
	tracker := NewTracker("session-disabled", "claude")
	tracker.TrackToolCall("Read", 1, true)
	tracker.Disable()
	tracker.TrackToolCall("Write", 1, true)
	tracker.TrackModelCall(10, 20, "ignored", 3)
	tracker.Track(EventError, nil)

	stats := tracker.GetStats()
	if stats.ToolCallCount != 1 || stats.TurnCount != 0 || stats.ErrorCount != 0 || stats.Model != "claude" {
		t.Fatalf("disabled tracker recorded events: %#v", stats)
	}
	if len(tracker.GetEvents()) != 1 {
		t.Fatalf("disabled tracker recorded extra events: %#v", tracker.GetEvents())
	}
}

func TestExportWritesStatsAndEventsJSON(t *testing.T) {
	tracker := NewTracker("session-export", "claude")
	tracker.TrackToolCall("Read", 5, true)
	tracker.TrackModelCall(1000, 2000, "", 10)

	path := filepath.Join(t.TempDir(), "analytics.json")
	if err := tracker.Export(path); err != nil {
		t.Fatalf("Export failed: %v", err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
		return
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("export file mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
		return
	}
	var payload struct {
		Stats  SessionStats     `json:"stats"`
		Events []AnalyticsEvent `json:"events"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
		return
	}
	if payload.Stats.SessionID != "session-export" || payload.Stats.ToolCallCount != 1 || payload.Stats.InputTokens != 1000 {
		t.Fatalf("unexpected exported stats: %#v", payload.Stats)
	}
	if len(payload.Events) != 2 || payload.Events[0].Type != EventToolCall || payload.Events[1].Type != EventModelCall {
		t.Fatalf("unexpected exported events: %#v", payload.Events)
	}
}

func TestSummaryAndFormatters(t *testing.T) {
	tracker := NewTracker("session-summary", "claude")
	tracker.stats.StartTime = time.Now().Add(-75 * time.Second)
	tracker.TrackToolCall("Write", 1, true)
	tracker.TrackToolCall("Read", 1, true)
	tracker.TrackToolCall("Read", 1, true)
	tracker.TrackModelCall(1234567, 8901, "claude", 1)
	tracker.Track(EventCompaction, nil)
	tracker.Track(EventError, nil)

	summary := tracker.Summary()
	for _, want := range []string{
		"Session Summary:",
		"Duration: 1m 15s",
		"Turns: 1",
		"Tool calls: 3 (Read: 2, Write: 1)",
		"Tokens: 1,234,567 input / 8,901 output",
		"Compactions: 1",
		"Errors: 1",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}

	if got := formatNumber(1000000); got != "1,000,000" {
		t.Fatalf("formatNumber = %q", got)
	}
	if got := formatDuration(4*time.Second + 400*time.Millisecond); got != "4s" {
		t.Fatalf("formatDuration = %q", got)
	}
}
