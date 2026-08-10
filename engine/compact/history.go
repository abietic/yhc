package compact

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// CompactionReason identifies why a compaction was triggered.
type CompactionReason string

const (
	// CompactionReasonAuto is an automatic compaction triggered by token threshold.
	CompactionReasonAuto CompactionReason = "auto"
	// CompactionReasonManual is a user-initiated /compact command.
	CompactionReasonManual CompactionReason = "manual"
	// CompactionReasonPTL is a compaction triggered by a prompt-too-long error.
	CompactionReasonPTL CompactionReason = "ptl"
	// CompactionReasonReactive is a compaction triggered by reactive context pressure.
	CompactionReasonReactive CompactionReason = "reactive"
)

// CompactionEvent records a single compaction occurrence with full metadata.
// This provides the data backing for `/compact history` and debugging.
type CompactionEvent struct {
	// ID is a monotonically increasing identifier within the session.
	ID int `json:"id"`
	// Timestamp is when the compaction occurred.
	Timestamp time.Time `json:"timestamp"`
	// Reason identifies why the compaction was triggered.
	Reason CompactionReason `json:"reason"`
	// MessagesCompacted is the number of messages that were summarized.
	MessagesCompacted int `json:"messages_compacted"`
	// MessagesAfter is the number of messages remaining after compaction.
	MessagesAfter int `json:"messages_after"`
	// TokensBefore is the estimated token count before compaction.
	TokensBefore int `json:"tokens_before"`
	// TokensAfter is the estimated token count after compaction.
	TokensAfter int `json:"tokens_after"`
	// TokensSaved is TokensBefore - TokensAfter.
	TokensSaved int `json:"tokens_saved"`
	// Strategy describes the compaction method used (e.g., "llm_full", "llm_partial", "deterministic").
	Strategy string `json:"strategy"`
	// Success indicates whether the compaction completed without error.
	Success bool `json:"success"`
	// Error contains the error message if Success is false.
	Error string `json:"error,omitempty"`
	// Duration is how long the compaction took.
	Duration time.Duration `json:"duration"`
	// TurnID is the conversation turn during which compaction occurred.
	TurnID string `json:"turn_id,omitempty"`
}

// CompactionLog is a thread-safe, in-memory log of compaction events for a session.
// It provides append-only recording and multiple query methods for inspection.
type CompactionLog struct {
	mu     sync.RWMutex
	events []CompactionEvent
	nextID int
}

// NewCompactionLog creates an empty CompactionLog.
func NewCompactionLog() *CompactionLog {
	return &CompactionLog{
		events: make([]CompactionEvent, 0),
		nextID: 1,
	}
}

// Record adds a compaction event to the log and returns the assigned event ID.
func (l *CompactionLog) Record(event CompactionEvent) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	event.ID = l.nextID
	l.nextID++

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	event.TokensSaved = event.TokensBefore - event.TokensAfter
	if event.TokensSaved < 0 {
		event.TokensSaved = 0
	}

	l.events = append(l.events, event)
	return event.ID
}

// Events returns a copy of all recorded compaction events in chronological order.
func (l *CompactionLog) Events() []CompactionEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]CompactionEvent, len(l.events))
	copy(result, l.events)
	return result
}

// Count returns the total number of compaction events recorded.
func (l *CompactionLog) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.events)
}

// Last returns the most recent compaction event, or nil if none exist.
func (l *CompactionLog) Last() *CompactionEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.events) == 0 {
		return nil
	}
	event := l.events[len(l.events)-1]
	return &event
}

// TotalTokensSaved returns the sum of tokens saved across all compaction events.
func (l *CompactionLog) TotalTokensSaved() int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	total := 0
	for _, e := range l.events {
		total += e.TokensSaved
	}
	return total
}

// SuccessfulEvents returns only events where Success is true.
func (l *CompactionLog) SuccessfulEvents() []CompactionEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var result []CompactionEvent
	for _, e := range l.events {
		if e.Success {
			result = append(result, e)
		}
	}
	return result
}

// FailedEvents returns only events where Success is false.
func (l *CompactionLog) FailedEvents() []CompactionEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var result []CompactionEvent
	for _, e := range l.events {
		if !e.Success {
			result = append(result, e)
		}
	}
	return result
}

// EventsByReason returns all events matching the given reason.
func (l *CompactionLog) EventsByReason(reason CompactionReason) []CompactionEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var result []CompactionEvent
	for _, e := range l.events {
		if e.Reason == reason {
			result = append(result, e)
		}
	}
	return result
}

// FormatHistory returns a human-readable summary of the compaction history
// suitable for display in a status command.
func (l *CompactionLog) FormatHistory() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.events) == 0 {
		return "No compaction events in this session."
	}

	totalSaved := 0
	successCount := 0
	for _, e := range l.events {
		totalSaved += e.TokensSaved
		if e.Success {
			successCount++
		}
	}

	header := fmt.Sprintf("Compaction History: %d event(s), %d successful, ~%d tokens saved total\n",
		len(l.events), successCount, totalSaved)

	lines := header + "\n"
	for _, e := range l.events {
		status := "OK"
		if !e.Success {
			status = "FAILED"
		}
		lines += fmt.Sprintf("  #%d [%s] %s reason=%s strategy=%s tokens=%d->%d (-%d) duration=%s\n",
			e.ID,
			e.Timestamp.Format("15:04:05"),
			status,
			e.Reason,
			e.Strategy,
			e.TokensBefore,
			e.TokensAfter,
			e.TokensSaved,
			e.Duration.Round(time.Millisecond),
		)
		if e.Error != "" {
			lines += fmt.Sprintf("       error: %s\n", e.Error)
		}
	}
	return lines
}

// MarshalJSON provides JSON serialization for the compaction log.
func (l *CompactionLog) MarshalJSON() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return json.Marshal(struct {
		Events []CompactionEvent `json:"events"`
		Count  int               `json:"count"`
	}{
		Events: l.events,
		Count:  len(l.events),
	})
}
