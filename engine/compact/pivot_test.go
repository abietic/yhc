package compact

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// --- CreatePivotMessage tests ---

func TestCreatePivotMessageBasic(t *testing.T) {
	pivot := CreatePivotMessage("1. User asked to build a CLI tool.", PivotConfig{
		Trigger:              "auto",
		PreCompactTokenCount: 50000,
	})

	if pivot == nil {
		t.Fatal("expected non-nil pivot")
		return
	}
	if pivot.Boundary == nil {
		t.Fatal("expected non-nil boundary")
		return
	}
	if pivot.Summary == nil {
		t.Fatal("expected non-nil summary")
		return
	}

	// Boundary should be a system message with proper metadata
	if pivot.Boundary.Role != schema.System {
		t.Fatalf("expected system role for boundary, got %v", pivot.Boundary.Role)
	}
	if pivot.Boundary.Extra == nil || pivot.Boundary.Extra["subtype"] != "compact_boundary" {
		t.Fatal("expected compact_boundary subtype on boundary")
		return
	}
	if pivot.Boundary.Extra["trigger"] != "auto" {
		t.Fatalf("expected trigger 'auto', got %v", pivot.Boundary.Extra["trigger"])
	}
	if pivot.Boundary.Extra["pre_compact_tokens"] != 50000 {
		t.Fatalf("expected pre_compact_tokens 50000, got %v", pivot.Boundary.Extra["pre_compact_tokens"])
	}

	// Summary should be a user message with continuation framing
	if pivot.Summary.Role != schema.User {
		t.Fatalf("expected user role for summary, got %v", pivot.Summary.Role)
	}
	if !strings.Contains(pivot.Summary.Content, "being continued from a previous conversation") {
		t.Fatal("expected continuation preamble in summary")
	}
	if !strings.Contains(pivot.Summary.Content, "build a CLI tool") {
		t.Fatal("expected summary content in pivot summary")
	}
}

func TestCreatePivotMessageWithContinuation(t *testing.T) {
	pivot := CreatePivotMessage("Summary text", PivotConfig{
		Trigger:             "auto",
		IncludeContinuation: true,
	})

	if pivot.Continuation == nil {
		t.Fatal("expected continuation message when IncludeContinuation is true")
		return
	}
	if pivot.Continuation.Role != schema.System {
		t.Fatalf("expected system role for continuation, got %v", pivot.Continuation.Role)
	}
	if !strings.Contains(pivot.Continuation.Content, "automatically compacted") {
		t.Fatalf("expected auto-compact continuation text, got %q", pivot.Continuation.Content)
	}
	if pivot.Continuation.Extra == nil || pivot.Continuation.Extra["subtype"] != "continuation_marker" {
		t.Fatal("expected continuation_marker subtype")
		return
	}
}

func TestCreatePivotMessageWithoutContinuation(t *testing.T) {
	pivot := CreatePivotMessage("Summary text", PivotConfig{
		Trigger:             "manual",
		IncludeContinuation: false,
	})

	if pivot.Continuation != nil {
		t.Fatal("expected nil continuation when IncludeContinuation is false")
		return
	}
}

func TestCreatePivotMessageSuppressFollowUp(t *testing.T) {
	pivot := CreatePivotMessage("Summary text", PivotConfig{
		Trigger:          "auto",
		SuppressFollowUp: true,
	})

	if !strings.Contains(pivot.Summary.Content, "without asking the user any further questions") {
		t.Fatal("expected suppress follow-up directive")
	}
	if !strings.Contains(pivot.Summary.Content, "Pick up the last task as if the break never happened") {
		t.Fatal("expected direct continuation directive")
	}
}

func TestCreatePivotMessageWithTranscriptPath(t *testing.T) {
	pivot := CreatePivotMessage("Summary text", PivotConfig{
		Trigger:        "auto",
		TranscriptPath: "/home/user/.session/transcript.jsonl",
	})

	if !strings.Contains(pivot.Summary.Content, "/home/user/.session/transcript.jsonl") {
		t.Fatal("expected transcript path in summary")
	}
}

func TestCreatePivotMessageWithPreservedFacts(t *testing.T) {
	pivot := CreatePivotMessage("Summary text", PivotConfig{
		Trigger: "auto",
		PreservedFacts: []string{
			"Project uses Go 1.25",
			"Main entry point is cmd/main.go",
			"Tests run with `go test ./...`",
		},
	})

	if !strings.Contains(pivot.Summary.Content, "Key Facts Preserved") {
		t.Fatal("expected preserved facts section header")
	}
	if !strings.Contains(pivot.Summary.Content, "Project uses Go 1.25") {
		t.Fatal("expected first preserved fact")
	}
	if !strings.Contains(pivot.Summary.Content, "cmd/main.go") {
		t.Fatal("expected second preserved fact")
	}
}

func TestCreatePivotMessageRecentMessagesPreserved(t *testing.T) {
	pivot := CreatePivotMessage("Summary text", PivotConfig{
		Trigger:                 "auto",
		RecentMessagesPreserved: true,
	})

	if !strings.Contains(pivot.Summary.Content, "Recent messages are preserved verbatim") {
		t.Fatal("expected recent messages preserved notice")
	}
}

func TestPivotMessages(t *testing.T) {
	// Test Messages() ordering: boundary, summary, continuation
	pivot := CreatePivotMessage("test", PivotConfig{
		Trigger:             "auto",
		IncludeContinuation: true,
	})

	msgs := pivot.Messages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0] != pivot.Boundary {
		t.Fatal("first message should be boundary")
	}
	if msgs[1] != pivot.Summary {
		t.Fatal("second message should be summary")
	}
	if msgs[2] != pivot.Continuation {
		t.Fatal("third message should be continuation")
	}

	// Without continuation
	pivot2 := CreatePivotMessage("test", PivotConfig{
		Trigger:             "manual",
		IncludeContinuation: false,
	})
	msgs2 := pivot2.Messages()
	if len(msgs2) != 2 {
		t.Fatalf("expected 2 messages without continuation, got %d", len(msgs2))
	}
}

func TestCreatePivotMessageFormatsAnalysisTags(t *testing.T) {
	rawSummary := `<analysis>
Internal reasoning here.
</analysis>

<summary>
1. Primary Request:
   Build something cool
</summary>`

	pivot := CreatePivotMessage(rawSummary, PivotConfig{Trigger: "auto"})

	// Analysis should be stripped
	if strings.Contains(pivot.Summary.Content, "Internal reasoning") {
		t.Fatal("expected analysis to be stripped from pivot summary")
	}
	// Summary content should be preserved
	if !strings.Contains(pivot.Summary.Content, "Build something cool") {
		t.Fatal("expected summary content to be preserved")
	}
}

func TestCreatePivotMessageReactiveRecovery(t *testing.T) {
	pivot := CreatePivotMessage("Recovery summary", PivotConfig{
		Trigger:             "reactive",
		IncludeContinuation: true,
	})

	if !strings.Contains(pivot.Continuation.Content, "Context recovery") {
		t.Fatalf("expected reactive continuation text, got %q", pivot.Continuation.Content)
	}
}

// --- Pivot identification tests ---

func TestIsPivotBoundary(t *testing.T) {
	if IsPivotBoundary(nil) {
		t.Fatal("nil should not be pivot boundary")
	}
	if IsPivotBoundary(&schema.Message{Role: schema.System}) {
		t.Fatal("message without extra should not be pivot boundary")
	}
	if !IsPivotBoundary(&schema.Message{Role: schema.System, Extra: map[string]any{"subtype": "compact_boundary"}}) {
		t.Fatal("message with compact_boundary should be pivot boundary")
	}
}

func TestIsPivotSummary(t *testing.T) {
	if !IsPivotSummary(&schema.Message{Role: schema.User, Extra: map[string]any{"subtype": "compact_summary"}}) {
		t.Fatal("message with compact_summary should be pivot summary")
	}
	if IsPivotSummary(&schema.Message{Role: schema.User, Extra: map[string]any{"subtype": "other"}}) {
		t.Fatal("message with other subtype should not be pivot summary")
	}
}

func TestIsContinuationMarker(t *testing.T) {
	if !IsContinuationMarker(&schema.Message{Role: schema.System, Extra: map[string]any{"subtype": "continuation_marker"}}) {
		t.Fatal("should identify continuation marker")
	}
	if IsContinuationMarker(&schema.Message{Role: schema.System, Extra: map[string]any{"subtype": "compact_boundary"}}) {
		t.Fatal("compact_boundary should not be continuation marker")
	}
}

func TestExtractPivotMetadata(t *testing.T) {
	msg := &schema.Message{
		Role: schema.System,
		Extra: map[string]any{
			"subtype":            "compact_boundary",
			"trigger":            "auto",
			"pre_compact_tokens": 50000,
		},
	}

	meta := ExtractPivotMetadata(msg)
	if meta == nil {
		t.Fatal("expected metadata from pivot boundary")
		return
	}
	if _, hasSubtype := meta["subtype"]; hasSubtype {
		t.Fatal("subtype should be excluded from metadata")
	}
	if meta["trigger"] != "auto" {
		t.Fatalf("expected trigger 'auto', got %v", meta["trigger"])
	}
	if meta["pre_compact_tokens"] != 50000 {
		t.Fatalf("expected pre_compact_tokens 50000, got %v", meta["pre_compact_tokens"])
	}

	// Non-boundary message returns nil
	nonBoundary := &schema.Message{Role: schema.User, Content: "hello"}
	if ExtractPivotMetadata(nonBoundary) != nil {
		t.Fatal("expected nil for non-boundary message")
		return
	}
}
