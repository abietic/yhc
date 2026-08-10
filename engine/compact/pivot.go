package compact

import (
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// PivotMessage is the structured boundary marker inserted between compacted
// history and the live conversation. It contains the summary, preserved key
// facts, and a continuation marker that tells the model this is a continuation.
//
// This corresponds to the reference's CompactBoundaryMessage + the summary
// UserMessage combination from compact/compact.ts buildPostCompactMessages.
type PivotMessage struct {
	// Boundary is the system-role marker that signals a compaction boundary.
	// It carries metadata about the compaction event.
	Boundary *schema.Message

	// Summary is the user-role message containing the formatted summary of
	// compacted content plus continuation instructions.
	Summary *schema.Message

	// Continuation is an optional system-role message that provides additional
	// context to help the model understand it's continuing a conversation.
	// This is used for long-session continuation semantics.
	Continuation *schema.Message
}

// PivotConfig controls how the pivot message is constructed.
type PivotConfig struct {
	// Trigger identifies what caused the compaction (e.g., "auto", "manual",
	// "reactive", "api_error").
	Trigger string

	// PreCompactTokenCount is the estimated token count before compaction.
	PreCompactTokenCount int

	// SuppressFollowUp instructs the model to continue without asking questions.
	SuppressFollowUp bool

	// TranscriptPath is the path to the full transcript file for reference.
	TranscriptPath string

	// RecentMessagesPreserved indicates whether recent messages are kept verbatim
	// after the pivot (changes the summary message framing).
	RecentMessagesPreserved bool

	// PreservedFacts are key facts extracted from the compacted portion that
	// should be explicitly called out in the pivot for continuity.
	PreservedFacts []string

	// LastMessageUUID is the UUID of the last message before compaction, used
	// for transcript linking. Empty string if unavailable.
	LastMessageUUID string

	// IncludeContinuation controls whether a continuation system message is
	// generated. Set to true for auto-compact and long sessions.
	IncludeContinuation bool
}

// CreatePivotMessage constructs the structured pivot that separates compacted
// history from the live conversation. The pivot consists of:
//  1. A boundary marker (system message with compaction metadata)
//  2. A summary message (user message with the compaction summary + continuation semantics)
//  3. An optional continuation marker (system message for long-session context)
//
// Mirrors the reference's createCompactBoundaryMessage + getCompactUserSummaryMessage
// from compact/compact.ts.
func CreatePivotMessage(summary string, config PivotConfig) *PivotMessage {
	if config.Trigger == "" {
		config.Trigger = "auto"
	}

	// 1. Build the boundary marker
	boundaryExtra := map[string]any{
		"subtype":              "compact_boundary",
		"trigger":              config.Trigger,
		"pre_compact_tokens":   config.PreCompactTokenCount,
		"compaction_timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if config.LastMessageUUID != "" {
		boundaryExtra["last_message_uuid"] = config.LastMessageUUID
	}

	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra:   boundaryExtra,
	}

	// 2. Build the summary message with continuation semantics
	summaryContent := formatPivotSummary(summary, config)

	summaryExtra := map[string]any{
		"subtype": "compact_summary",
		"trigger": config.Trigger,
	}

	summaryMsg := &schema.Message{
		Role:    schema.User,
		Content: summaryContent,
		Extra:   summaryExtra,
	}

	// 3. Optionally build the continuation marker
	var continuation *schema.Message
	if config.IncludeContinuation {
		continuation = buildContinuationMessage(config)
	}

	return &PivotMessage{
		Boundary:     boundary,
		Summary:      summaryMsg,
		Continuation: continuation,
	}
}

// Messages returns the pivot messages in the correct order for insertion
// into the conversation history.
func (p *PivotMessage) Messages() []*schema.Message {
	if p == nil {
		return nil
	}
	msgs := make([]*schema.Message, 0, 3)
	if p.Boundary != nil {
		msgs = append(msgs, p.Boundary)
	}
	if p.Summary != nil {
		msgs = append(msgs, p.Summary)
	}
	if p.Continuation != nil {
		msgs = append(msgs, p.Continuation)
	}
	return msgs
}

// formatPivotSummary builds the summary content with proper continuation framing.
// This uses the same approach as the reference's getCompactUserSummaryMessage.
func formatPivotSummary(rawSummary string, config PivotConfig) string {
	// Format the summary (strip analysis, format tags)
	formatted := FormatCompactSummary(rawSummary)

	var b strings.Builder

	// Continuation preamble
	b.WriteString("This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.\n\n")

	// Main summary content
	b.WriteString(formatted)

	// Preserved facts section (if any)
	if len(config.PreservedFacts) > 0 {
		b.WriteString("\n\n## Key Facts Preserved Across Compaction\n")
		for _, fact := range config.PreservedFacts {
			fmt.Fprintf(&b, "- %s\n", fact)
		}
	}

	// Transcript reference
	if config.TranscriptPath != "" {
		fmt.Fprintf(&b, "\n\nIf you need specific details from before compaction (like exact code snippets, error messages, or content you generated), read the full transcript at: %s", config.TranscriptPath)
	}

	// Recent messages preserved notice
	if config.RecentMessagesPreserved {
		b.WriteString("\n\nRecent messages are preserved verbatim.")
	}

	// Continuation directive
	if config.SuppressFollowUp {
		b.WriteString("\nContinue the conversation from where it left off without asking the user any further questions. Resume directly — do not acknowledge the summary, do not recap what was happening, do not preface with \"I'll continue\" or similar. Pick up the last task as if the break never happened.")
	}

	return b.String()
}

// buildContinuationMessage creates a system-level continuation marker that
// provides the model with orientation context for long-running sessions.
// This helps the model understand it's not starting fresh but continuing
// work that was in progress.
func buildContinuationMessage(config PivotConfig) *schema.Message {
	var content string

	switch config.Trigger {
	case "auto":
		content = "Context window management: This conversation was automatically compacted to continue within the available context. The summary above captures the essential state. Continue working on the current task seamlessly."
	case "reactive", "api_error":
		content = "Context recovery: The conversation was compacted after approaching context limits. The summary preserves the key context. Resume the current task without interruption."
	default:
		content = "Context compacted: The conversation history above this point has been summarized. Continue the current work based on the summary provided."
	}

	return &schema.Message{
		Role:    schema.System,
		Content: content,
		Extra: map[string]any{
			"subtype": "continuation_marker",
			"trigger": config.Trigger,
		},
	}
}

// IsPivotBoundary checks whether a message is a compaction pivot boundary marker.
func IsPivotBoundary(msg *schema.Message) bool {
	if msg == nil || msg.Extra == nil {
		return false
	}
	subtype, _ := msg.Extra["subtype"].(string)
	return subtype == "compact_boundary"
}

// IsPivotSummary checks whether a message is a compaction pivot summary.
func IsPivotSummary(msg *schema.Message) bool {
	if msg == nil || msg.Extra == nil {
		return false
	}
	subtype, _ := msg.Extra["subtype"].(string)
	return subtype == "compact_summary"
}

// IsContinuationMarker checks whether a message is a continuation marker.
func IsContinuationMarker(msg *schema.Message) bool {
	if msg == nil || msg.Extra == nil {
		return false
	}
	subtype, _ := msg.Extra["subtype"].(string)
	return subtype == "continuation_marker"
}

// ExtractPivotMetadata extracts compaction metadata from a pivot boundary message.
// Returns nil if the message is not a valid pivot boundary.
func ExtractPivotMetadata(msg *schema.Message) map[string]any {
	if !IsPivotBoundary(msg) {
		return nil
	}
	// Return a copy of the extra map (minus subtype which is structural)
	result := make(map[string]any, len(msg.Extra))
	for k, v := range msg.Extra {
		if k == "subtype" {
			continue
		}
		result[k] = v
	}
	return result
}
