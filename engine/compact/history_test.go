package compact

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCompactionLogRecordAndQuery(t *testing.T) {
	log := NewCompactionLog()

	// Initially empty
	if log.Count() != 0 {
		t.Fatalf("expected 0 events initially, got %d", log.Count())
	}
	if log.Last() != nil {
		t.Fatal("expected nil Last() on empty log")
		return
	}
	if log.TotalTokensSaved() != 0 {
		t.Fatalf("expected 0 total tokens saved, got %d", log.TotalTokensSaved())
	}

	// Record first event
	id1 := log.Record(CompactionEvent{
		Reason:            CompactionReasonAuto,
		MessagesCompacted: 20,
		MessagesAfter:     5,
		TokensBefore:      50000,
		TokensAfter:       10000,
		Strategy:          "llm_full",
		Success:           true,
		Duration:          2 * time.Second,
	})

	if id1 != 1 {
		t.Fatalf("expected first event ID=1, got %d", id1)
	}
	if log.Count() != 1 {
		t.Fatalf("expected 1 event, got %d", log.Count())
	}

	// Record second event
	id2 := log.Record(CompactionEvent{
		Reason:            CompactionReasonPTL,
		MessagesCompacted: 30,
		MessagesAfter:     4,
		TokensBefore:      80000,
		TokensAfter:       15000,
		Strategy:          "deterministic",
		Success:           true,
		Duration:          100 * time.Millisecond,
	})

	if id2 != 2 {
		t.Fatalf("expected second event ID=2, got %d", id2)
	}
	if log.Count() != 2 {
		t.Fatalf("expected 2 events, got %d", log.Count())
	}

	// Record a failed event
	id3 := log.Record(CompactionEvent{
		Reason:       CompactionReasonAuto,
		TokensBefore: 60000,
		TokensAfter:  60000,
		Strategy:     "llm_full",
		Success:      false,
		Error:        "model returned empty response",
		Duration:     5 * time.Second,
	})

	if id3 != 3 {
		t.Fatalf("expected third event ID=3, got %d", id3)
	}

	// Query methods
	last := log.Last()
	if last == nil {
		t.Fatal("expected non-nil Last()")
		return
	}
	if last.ID != 3 {
		t.Fatalf("expected last event ID=3, got %d", last.ID)
	}

	// TotalTokensSaved: (50000-10000) + (80000-15000) + 0 = 40000 + 65000 = 105000
	if log.TotalTokensSaved() != 105000 {
		t.Fatalf("expected 105000 total tokens saved, got %d", log.TotalTokensSaved())
	}

	// SuccessfulEvents
	successful := log.SuccessfulEvents()
	if len(successful) != 2 {
		t.Fatalf("expected 2 successful events, got %d", len(successful))
	}

	// FailedEvents
	failed := log.FailedEvents()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed event, got %d", len(failed))
	}
	if failed[0].Error != "model returned empty response" {
		t.Fatalf("unexpected error message: %s", failed[0].Error)
	}

	// EventsByReason
	autoEvents := log.EventsByReason(CompactionReasonAuto)
	if len(autoEvents) != 2 {
		t.Fatalf("expected 2 auto events, got %d", len(autoEvents))
	}
	ptlEvents := log.EventsByReason(CompactionReasonPTL)
	if len(ptlEvents) != 1 {
		t.Fatalf("expected 1 PTL event, got %d", len(ptlEvents))
	}
	manualEvents := log.EventsByReason(CompactionReasonManual)
	if len(manualEvents) != 0 {
		t.Fatalf("expected 0 manual events, got %d", len(manualEvents))
	}
}

func TestCompactionLogTokensSavedClampedToZero(t *testing.T) {
	log := NewCompactionLog()

	// Record an event where TokensAfter > TokensBefore (edge case)
	log.Record(CompactionEvent{
		Reason:       CompactionReasonAuto,
		TokensBefore: 1000,
		TokensAfter:  2000, // After is larger (e.g., reinjection added tokens)
		Success:      true,
	})

	events := log.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].TokensSaved != 0 {
		t.Fatalf("expected TokensSaved clamped to 0, got %d", events[0].TokensSaved)
	}
	if log.TotalTokensSaved() != 0 {
		t.Fatalf("expected total 0, got %d", log.TotalTokensSaved())
	}
}

func TestCompactionLogTimestampAutoFill(t *testing.T) {
	log := NewCompactionLog()

	before := time.Now()
	log.Record(CompactionEvent{
		Reason:  CompactionReasonManual,
		Success: true,
	})
	after := time.Now()

	events := log.Events()
	if len(events) != 1 {
		t.Fatal("expected 1 event")
	}
	if events[0].Timestamp.Before(before) || events[0].Timestamp.After(after) {
		t.Fatalf("expected timestamp between %v and %v, got %v", before, after, events[0].Timestamp)
	}
}

func TestCompactionLogTimestampPreserved(t *testing.T) {
	log := NewCompactionLog()

	explicit := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	log.Record(CompactionEvent{
		Timestamp: explicit,
		Reason:    CompactionReasonManual,
		Success:   true,
	})

	events := log.Events()
	if !events[0].Timestamp.Equal(explicit) {
		t.Fatalf("expected preserved timestamp %v, got %v", explicit, events[0].Timestamp)
	}
}

func TestCompactionLogFormatHistory(t *testing.T) {
	log := NewCompactionLog()

	// Empty log
	output := log.FormatHistory()
	if output != "No compaction events in this session." {
		t.Fatalf("unexpected empty log output: %q", output)
	}

	// With events
	log.Record(CompactionEvent{
		Timestamp:         time.Date(2025, 6, 1, 14, 30, 0, 0, time.UTC),
		Reason:            CompactionReasonAuto,
		MessagesCompacted: 25,
		TokensBefore:      80000,
		TokensAfter:       12000,
		Strategy:          "llm_full",
		Success:           true,
		Duration:          1500 * time.Millisecond,
	})

	output = log.FormatHistory()
	if output == "" {
		t.Fatal("expected non-empty format output")
	}
	// Should contain the event details
	for _, expected := range []string{"1 event", "1 successful", "68000 tokens saved", "auto", "llm_full"} {
		if !contains(output, expected) {
			t.Errorf("FormatHistory output missing %q:\n%s", expected, output)
		}
	}
}

func TestCompactionLogMarshalJSON(t *testing.T) {
	log := NewCompactionLog()
	log.Record(CompactionEvent{
		Timestamp:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Reason:       CompactionReasonAuto,
		TokensBefore: 50000,
		TokensAfter:  10000,
		Success:      true,
	})

	data, err := log.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
		return
	}

	var decoded struct {
		Events []CompactionEvent `json:"events"`
		Count  int               `json:"count"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
		return
	}
	if decoded.Count != 1 {
		t.Fatalf("expected count=1, got %d", decoded.Count)
	}
	if len(decoded.Events) != 1 {
		t.Fatalf("expected 1 event in JSON, got %d", len(decoded.Events))
	}
	if decoded.Events[0].TokensSaved != 40000 {
		t.Fatalf("expected tokens_saved=40000 in JSON, got %d", decoded.Events[0].TokensSaved)
	}
}

func TestCompactionLogConcurrency(t *testing.T) {
	log := NewCompactionLog()
	done := make(chan struct{})

	// Concurrent writes
	for i := 0; i < 10; i++ {
		go func(n int) {
			log.Record(CompactionEvent{
				Reason:       CompactionReasonAuto,
				TokensBefore: n * 1000,
				TokensAfter:  n * 100,
				Success:      true,
			})
			done <- struct{}{}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 10; i++ {
		go func() {
			_ = log.Count()
			_ = log.Last()
			_ = log.Events()
			_ = log.TotalTokensSaved()
			done <- struct{}{}
		}()
	}

	for i := 0; i < 20; i++ {
		<-done
	}

	if log.Count() != 10 {
		t.Fatalf("expected 10 events after concurrent writes, got %d", log.Count())
	}
}

func TestCompactionLogEventsReturnsCopy(t *testing.T) {
	log := NewCompactionLog()
	log.Record(CompactionEvent{
		Reason:  CompactionReasonAuto,
		Success: true,
	})

	events := log.Events()
	events[0].Reason = CompactionReasonManual // Mutate the copy

	// Original should be unchanged
	original := log.Events()
	if original[0].Reason != CompactionReasonAuto {
		t.Fatal("Events() should return a copy, not a reference to internal state")
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
