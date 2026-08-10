package compact

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

// TranscriptCompactionEntry is the metadata written to the transcript when
// a compaction boundary is created. This enables session resume to detect
// compaction points and reconstruct context correctly.
type TranscriptCompactionEntry struct {
	// Kind is always "compaction_boundary" for transcript entries.
	Kind string `json:"kind"`
	// Timestamp is when the compaction occurred.
	Timestamp time.Time `json:"timestamp"`
	// Reason identifies why the compaction was triggered.
	Reason CompactionReason `json:"reason"`
	// MessagesCompacted is how many messages were summarized away.
	MessagesCompacted int `json:"messages_compacted"`
	// TokensBefore is the estimated token count before compaction.
	TokensBefore int `json:"tokens_before"`
	// TokensAfter is the estimated token count after compaction.
	TokensAfter int `json:"tokens_after"`
	// Strategy is the compaction method used.
	Strategy string `json:"strategy"`
	// SummaryPreview is the first 200 chars of the summary for quick inspection.
	SummaryPreview string `json:"summary_preview,omitempty"`
	// EventID links back to the CompactionLog event ID within the session.
	EventID int `json:"event_id"`
}

// TranscriptRecorder is the interface required for writing compaction metadata
// to the transcript. This decouples the compact package from the transcript
// package to avoid circular imports.
type TranscriptRecorder interface {
	// RecordMetadata appends a metadata key-value entry to the transcript.
	RecordMetadata(key, value string) error
}

// RecordCompactionInTranscript writes a compaction boundary metadata entry
// to the transcript. This enables session resume to detect that a compaction
// happened at this point in the conversation history.
//
// The entry is written as a "metadata" record with key "compaction_boundary"
// and a JSON-encoded value containing the compaction details.
func RecordCompactionInTranscript(recorder TranscriptRecorder, entry TranscriptCompactionEntry) error {
	if recorder == nil {
		return nil
	}

	if entry.Kind == "" {
		entry.Kind = "compaction_boundary"
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal compaction transcript entry: %w", err)
	}

	return recorder.RecordMetadata("compaction_boundary", string(data))
}

// BuildTranscriptCompactionEntry creates a TranscriptCompactionEntry from
// a CompactionEvent and optional summary text. This is the bridge between
// the CompactionLog event and the transcript metadata entry.
func BuildTranscriptCompactionEntry(event CompactionEvent, summary string) TranscriptCompactionEntry {
	preview := summary
	if len(preview) > 200 {
		preview = preview[:200]
	}

	return TranscriptCompactionEntry{
		Kind:              "compaction_boundary",
		Timestamp:         event.Timestamp,
		Reason:            event.Reason,
		MessagesCompacted: event.MessagesCompacted,
		TokensBefore:      event.TokensBefore,
		TokensAfter:       event.TokensAfter,
		Strategy:          event.Strategy,
		SummaryPreview:    preview,
		EventID:           event.ID,
	}
}

// DetectCompactionBoundaries scans transcript metadata entries and returns
// all compaction boundary entries found. This is used during session resume
// to understand the compaction history of a resumed session.
//
// metadataEntries should be the Metadata field from a transcript LoadResult.
// Each entry with Key == "compaction_boundary" is parsed and returned.
func DetectCompactionBoundaries(metadataEntries []TranscriptMetadataEntry) []TranscriptCompactionEntry {
	var boundaries []TranscriptCompactionEntry

	for _, entry := range metadataEntries {
		if entry.Key != "compaction_boundary" {
			continue
		}
		var compEntry TranscriptCompactionEntry
		if err := json.Unmarshal([]byte(entry.Value), &compEntry); err != nil {
			// Skip malformed entries (corruption tolerance)
			continue
		}
		boundaries = append(boundaries, compEntry)
	}

	return boundaries
}

// TranscriptMetadataEntry represents a metadata entry from a loaded transcript.
// This mirrors the MetadataEntry from the transcript package without importing it.
type TranscriptMetadataEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// ReconstructPostCompactionContext handles the case where a session is resumed
// after a compaction occurred mid-session. It:
//  1. Detects compaction boundaries in the transcript metadata
//  2. Verifies messages contain proper boundary markers
//  3. Returns information about the compaction state for the resume handler
//
// This enables the resume path to understand that messages have been compacted
// and to set up the CompactionLog and tracking state correctly.
type ResumeCompactionState struct {
	// HasCompaction indicates at least one compaction boundary was found.
	HasCompaction bool
	// CompactionCount is the total number of compactions in the session history.
	CompactionCount int
	// LastCompaction is the most recent compaction entry, or nil if none found.
	LastCompaction *TranscriptCompactionEntry
	// Boundaries is the full list of compaction boundaries in order.
	Boundaries []TranscriptCompactionEntry
	// MessagesHaveBoundaryMarker indicates whether the loaded messages contain
	// at least one message with subtype "compact_boundary" in Extra.
	MessagesHaveBoundaryMarker bool
}

// AnalyzeResumeCompactionState inspects both transcript metadata and loaded
// messages to build a complete picture of the compaction state for resume.
func AnalyzeResumeCompactionState(metadataEntries []TranscriptMetadataEntry, messages []*schema.Message) *ResumeCompactionState {
	state := &ResumeCompactionState{}

	// Detect boundaries from transcript metadata
	boundaries := DetectCompactionBoundaries(metadataEntries)
	state.Boundaries = boundaries
	state.CompactionCount = len(boundaries)
	state.HasCompaction = len(boundaries) > 0

	if len(boundaries) > 0 {
		last := boundaries[len(boundaries)-1]
		state.LastCompaction = &last
	}

	// Check if loaded messages contain boundary markers
	for _, msg := range messages {
		if IsPivotBoundary(msg) {
			state.MessagesHaveBoundaryMarker = true
			break
		}
	}

	return state
}

// RebuildCompactionLog reconstructs a CompactionLog from transcript metadata
// entries found during session resume. This allows the resumed session to have
// an accurate history of past compactions for display and decision-making.
func RebuildCompactionLog(metadataEntries []TranscriptMetadataEntry) *CompactionLog {
	log := NewCompactionLog()
	boundaries := DetectCompactionBoundaries(metadataEntries)

	for _, b := range boundaries {
		log.Record(CompactionEvent{
			Timestamp:         b.Timestamp,
			Reason:            b.Reason,
			MessagesCompacted: b.MessagesCompacted,
			TokensBefore:      b.TokensBefore,
			TokensAfter:       b.TokensAfter,
			Strategy:          b.Strategy,
			Success:           true, // Only successful compactions are recorded in transcript
		})
	}

	return log
}

// CountCompactionBoundariesInMessages counts the number of compaction boundary
// markers present in a message slice. This is useful for verifying that multiple
// compactions have stacked correctly (each adds exactly one boundary marker).
func CountCompactionBoundariesInMessages(messages []*schema.Message) int {
	count := 0
	for _, msg := range messages {
		if IsPivotBoundary(msg) {
			count++
		}
	}
	return count
}
