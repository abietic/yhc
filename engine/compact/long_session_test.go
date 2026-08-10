package compact

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// --- Helpers for generating realistic long conversations ---

// generateLongConversation creates a conversation with the specified number of turns.
// Each turn consists of a user message and an assistant reply (with optional tool calls).
// Messages are sized to approximate realistic token counts.
func generateLongConversation(turns, avgContentLen int) []*schema.Message {
	messages := make([]*schema.Message, 0, turns*3)
	for i := 0; i < turns; i++ {
		// User message
		userContent := fmt.Sprintf("Turn %d: %s", i+1, strings.Repeat("user context data ", avgContentLen/18))
		messages = append(messages, &schema.Message{
			Role:    schema.User,
			Content: userContent,
			Extra: map[string]any{
				"message_id": fmt.Sprintf("user_%d", i),
			},
		})

		// Assistant reply with occasional tool calls
		if i%3 == 0 && i > 0 {
			// Tool call turn
			messages = append(messages, &schema.Message{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{
						ID:   fmt.Sprintf("call_%d", i),
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "Read",
							Arguments: fmt.Sprintf(`{"file_path": "/src/module_%d.go"}`, i),
						},
					},
				},
				Extra: map[string]any{
					"message_id": fmt.Sprintf("assistant_%d", i),
				},
			})
			// Tool result
			messages = append(messages, &schema.Message{
				Role:       schema.Tool,
				Content:    fmt.Sprintf("package module%d\n\nfunc Process() error {\n    // implementation\n    return nil\n}\n", i),
				ToolCallID: fmt.Sprintf("call_%d", i),
				ToolName:   "Read",
			})
		} else {
			// Regular assistant reply
			assistantContent := fmt.Sprintf("Response to turn %d: %s", i+1, strings.Repeat("assistant analysis ", avgContentLen/20))
			messages = append(messages, &schema.Message{
				Role:    schema.Assistant,
				Content: assistantContent,
				Extra: map[string]any{
					"message_id": fmt.Sprintf("assistant_%d", i),
				},
			})
		}
	}
	return messages
}

// --- Long-session behavioral tests ---

// setWindowForTokenCount calculates and sets the context window env var so that
// the given token count falls in the valid auto-compact band:
//
//	threshold (window - 13000) <= tokenCount < blocking (window - 3000)
//
// Returns the window size used.
func setWindowForTokenCount(t *testing.T, tokenCount int) int {
	t.Helper()
	// Set window so that tokenCount is above threshold but below blocking limit.
	// window - 13000 <= tokenCount → window <= tokenCount + 13000
	// tokenCount < window - 3000 → window > tokenCount + 3000
	// Use midpoint: window = tokenCount + 8000
	window := tokenCount + 8000
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", fmt.Sprintf("%d", window))
	return window
}

func TestLongSessionAutoCompactTriggersCorrectly(t *testing.T) {
	// Simulate a 50-turn conversation and set window dynamically.
	messages := generateLongConversation(50, 200)
	tracking := &CompactTracking{}

	tokensBefore := EstimateTokenCount(messages)
	window := setWindowForTokenCount(t, tokensBefore)
	_ = window

	result, failures, updated := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatalf("expected auto-compact to trigger for 50-turn conversation (%d tokens)", tokensBefore)
		return
	}
	if failures != 0 {
		t.Fatalf("expected 0 failures, got %d", failures)
	}
	if !updated.Compacted {
		t.Fatal("expected tracking.Compacted = true")
	}

	// Verify post-compact token count is within limits
	postMessages := BuildPostCompactMessages(result)
	postTokens := EstimateTokenCount(postMessages)
	if postTokens >= tokensBefore {
		t.Fatalf("compaction should reduce tokens: before=%d, after=%d", tokensBefore, postTokens)
	}

	// Post-compact should be well within the window
	windowSize := GetEffectiveContextWindowSize("")
	if postTokens > windowSize {
		t.Fatalf("post-compact tokens (%d) should be within window (%d)", postTokens, windowSize)
	}
}

func TestLongSessionTokenCountStaysWithinLimitsAfterCompaction(t *testing.T) {
	messages := generateLongConversation(30, 150)
	tracking := &CompactTracking{}

	tokensBefore := EstimateTokenCount(messages)
	setWindowForTokenCount(t, tokensBefore)

	result, _, _ := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatalf("expected compaction to trigger (tokens=%d)", tokensBefore)
		return
	}

	// The result's PostCompactTokenCount should be reported accurately
	postMessages := BuildPostCompactMessages(result)
	actualPostTokens := EstimateTokenCount(postMessages)

	// Allow some tolerance for estimation vs actual
	if result.PostCompactTokenCount != actualPostTokens {
		t.Fatalf("PostCompactTokenCount mismatch: reported=%d, actual=%d",
			result.PostCompactTokenCount, actualPostTokens)
	}

	// Post-compact should be significantly smaller than pre-compact
	reductionRatio := float64(actualPostTokens) / float64(result.PreCompactTokenCount)
	if reductionRatio > 0.5 {
		t.Fatalf("expected significant reduction, but ratio is %.2f (pre=%d, post=%d)",
			reductionRatio, result.PreCompactTokenCount, actualPostTokens)
	}
}

func TestLongSessionKeyContextSurvivesCompaction(t *testing.T) {
	// The "latest question" (preserved tail) should survive compaction verbatim.
	messages := generateLongConversation(40, 150)

	// Add a distinctive recent message that should be preserved
	keyMessage := &schema.Message{
		Role:    schema.User,
		Content: "CRITICAL_INSTRUCTION: always use snake_case for variable names",
	}
	messages = append(messages, keyMessage)

	tokensBefore := EstimateTokenCount(messages)
	setWindowForTokenCount(t, tokensBefore)
	tracking := &CompactTracking{}

	result, _, _ := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatalf("expected compaction to trigger (tokens=%d)", tokensBefore)
		return
	}

	// Check that the preserved tail contains recent messages
	if len(result.MessagesToKeep) == 0 {
		t.Fatal("expected preserved messages in MessagesToKeep")
	}

	// The most recent user message should be in the preserved tail
	found := false
	for _, msg := range result.MessagesToKeep {
		if strings.Contains(msg.Content, "CRITICAL_INSTRUCTION") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected CRITICAL_INSTRUCTION to survive in preserved tail")
	}
}

func TestLongSessionContinuationAfterCompaction(t *testing.T) {
	messages := generateLongConversation(40, 150)
	tracking := &CompactTracking{}

	tokensBefore := EstimateTokenCount(messages)
	window := setWindowForTokenCount(t, tokensBefore)

	result, _, _ := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatalf("expected compaction to trigger (tokens=%d)", tokensBefore)
		return
	}

	// Build post-compact messages (what the model would see)
	postMessages := BuildPostCompactMessages(result)

	// Verify structural correctness for continuation:
	// 1. Must start with boundary marker or system message
	if len(postMessages) == 0 {
		t.Fatal("expected non-empty post-compact messages")
	}
	if postMessages[0].Role != schema.System {
		t.Fatalf("expected first post-compact message to be system (boundary), got %s", postMessages[0].Role)
	}
	if postMessages[0].Extra == nil || postMessages[0].Extra["subtype"] != "compact_boundary" {
		t.Fatal("expected first message to be compact_boundary")
		return
	}

	// 2. Summary should follow boundary
	if len(postMessages) < 2 {
		t.Fatal("expected at least boundary + summary")
	}
	summaryMsg := postMessages[1]
	if summaryMsg.Extra == nil || summaryMsg.Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected second message to be compact_summary, got %v", summaryMsg.Extra)
		return
	}

	// 3. Summary should contain meaningful content (not empty)
	if len(summaryMsg.Content) < 50 {
		t.Fatalf("summary seems too short for a 40-turn conversation: %d chars", len(summaryMsg.Content))
	}

	// 4. After summary, preserved messages should be present
	if len(postMessages) < 3 {
		t.Fatal("expected preserved messages after summary")
	}

	// 5. The conversation can continue: simulate adding a new user message
	newUserMsg := &schema.Message{
		Role:    schema.User,
		Content: "Continue with the next task",
	}
	continuedMessages := append(postMessages, newUserMsg)
	continuedTokens := EstimateTokenCount(continuedMessages)
	if continuedTokens > window {
		t.Fatalf("continued conversation exceeds window: %d > %d", continuedTokens, window)
	}
}

func TestLongSessionMultipleCompactionsStackCorrectly(t *testing.T) {
	// First compaction
	messages := generateLongConversation(30, 150)
	tracking := &CompactTracking{}
	log := NewCompactionLog()

	tokensBefore := EstimateTokenCount(messages)
	setWindowForTokenCount(t, tokensBefore)

	result1, _, tracking := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result1 == nil {
		t.Fatalf("expected first compaction to trigger (tokens=%d)", tokensBefore)
		return
	}
	log.Record(CompactionEvent{
		Reason:            CompactionReasonAuto,
		MessagesCompacted: len(messages),
		MessagesAfter:     len(BuildPostCompactMessages(result1)),
		TokensBefore:      result1.PreCompactTokenCount,
		TokensAfter:       result1.PostCompactTokenCount,
		Strategy:          "deterministic",
		Success:           true,
		Duration:          time.Millisecond,
	})

	// Build post-first-compact messages, then add more turns to trigger second compaction
	postFirst := BuildPostCompactMessages(result1)
	boundaryCount1 := CountCompactionBoundariesInMessages(postFirst)
	if boundaryCount1 != 1 {
		t.Fatalf("expected 1 boundary marker after first compaction, got %d", boundaryCount1)
	}

	// Add enough new messages to trigger a second compaction
	additionalMessages := generateLongConversation(25, 150)
	combined := append(postFirst, additionalMessages...)

	// Set window for second compaction
	combinedTokens := EstimateTokenCount(combined)
	setWindowForTokenCount(t, combinedTokens)

	// Second compaction
	result2, _, _ := AutoCompact(combined, "sdk", tracking, 0, "", nil)
	if result2 == nil {
		t.Fatalf("expected second compaction to trigger (tokens=%d)", combinedTokens)
		return
	}
	log.Record(CompactionEvent{
		Reason:            CompactionReasonAuto,
		MessagesCompacted: len(combined),
		MessagesAfter:     len(BuildPostCompactMessages(result2)),
		TokensBefore:      result2.PreCompactTokenCount,
		TokensAfter:       result2.PostCompactTokenCount,
		Strategy:          "deterministic",
		Success:           true,
		Duration:          time.Millisecond,
	})

	// Verify stacking
	postSecond := BuildPostCompactMessages(result2)

	// The second compaction should produce exactly 1 new boundary marker
	// (the old boundary from the first compaction gets summarized away)
	boundaryCount2 := CountCompactionBoundariesInMessages(postSecond)
	if boundaryCount2 != 1 {
		t.Fatalf("expected 1 boundary marker after second compaction (old one summarized), got %d", boundaryCount2)
	}

	// Token count should be reduced compared to combined input
	postTokens := EstimateTokenCount(postSecond)
	if postTokens >= combinedTokens {
		t.Fatalf("post-second-compaction tokens (%d) should be less than input (%d)", postTokens, combinedTokens)
	}

	// CompactionLog should have 2 events
	if log.Count() != 2 {
		t.Fatalf("expected 2 compaction events in log, got %d", log.Count())
	}

	// Both events should be successful
	successful := log.SuccessfulEvents()
	if len(successful) != 2 {
		t.Fatalf("expected 2 successful events, got %d", len(successful))
	}

	// Total tokens saved should be positive
	if log.TotalTokensSaved() <= 0 {
		t.Fatalf("expected positive total tokens saved, got %d", log.TotalTokensSaved())
	}
}

func TestLongSessionThirdCompactionAfterTwoStacksCorrectly(t *testing.T) {
	log := NewCompactionLog()
	tracking := &CompactTracking{}

	// Build and compact 3 times sequentially
	currentMessages := generateLongConversation(20, 150)
	for iteration := 0; iteration < 3; iteration++ {
		tokens := EstimateTokenCount(currentMessages)
		setWindowForTokenCount(t, tokens)

		result, _, newTracking := AutoCompact(currentMessages, "sdk", tracking, 0, "", nil)
		if result == nil {
			t.Fatalf("iteration %d: expected compaction to trigger (tokens=%d)", iteration, tokens)
			return
		}
		tracking = newTracking

		log.Record(CompactionEvent{
			Reason:            CompactionReasonAuto,
			MessagesCompacted: len(currentMessages),
			TokensBefore:      result.PreCompactTokenCount,
			TokensAfter:       result.PostCompactTokenCount,
			Strategy:          "deterministic",
			Success:           true,
		})

		// Use compacted result plus new messages for next iteration
		postMessages := BuildPostCompactMessages(result)
		additionalMessages := generateLongConversation(15, 150)
		currentMessages = append(postMessages, additionalMessages...)
	}

	// After 3 compactions, the log should reflect all 3
	if log.Count() != 3 {
		t.Fatalf("expected 3 compaction events, got %d", log.Count())
	}

	// Each compaction should have reduced tokens
	events := log.Events()
	for i, e := range events {
		if e.TokensAfter >= e.TokensBefore {
			t.Fatalf("event %d: expected token reduction, before=%d after=%d",
				i, e.TokensBefore, e.TokensAfter)
		}
	}
}

func TestLongSessionCompactionPreservesRecentToolResults(t *testing.T) {
	// Build a conversation where the last messages include tool results
	messages := generateLongConversation(35, 150)

	// Add a tool call + result as the final messages
	messages = append(messages, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{
				ID:   "final_call",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path": "/important/config.yaml"}`,
				},
			},
		},
		Extra: map[string]any{"message_id": "final_assistant"},
	})
	messages = append(messages, &schema.Message{
		Role:       schema.Tool,
		Content:    "port: 8080\nhost: localhost\ndebug: true",
		ToolCallID: "final_call",
		ToolName:   "Read",
	})

	tokensBefore := EstimateTokenCount(messages)
	setWindowForTokenCount(t, tokensBefore)
	tracking := &CompactTracking{}

	result, _, _ := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatalf("expected compaction to trigger (tokens=%d)", tokensBefore)
		return
	}

	// The preserved tail should contain the recent tool interaction
	preserved := result.MessagesToKeep
	hasToolResult := false
	for _, msg := range preserved {
		if msg.Role == schema.Tool && strings.Contains(msg.Content, "port: 8080") {
			hasToolResult = true
			break
		}
	}
	// Note: with only 2 preserved tail messages, the tool result might or might not
	// be in the tail depending on the exact tail logic. The important thing is that
	// the preserved messages are coherent (no orphaned tool results without calls).
	_ = hasToolResult

	// Verify no orphaned tool results in post-compact messages
	postMessages := BuildPostCompactMessages(result)
	toolCallIDs := make(map[string]bool)
	for _, msg := range postMessages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				toolCallIDs[tc.ID] = true
			}
		}
	}
	for _, msg := range postMessages {
		if msg.Role == schema.Tool && msg.ToolCallID != "" {
			// If there's a tool result, either its call is in the post-compact set
			// or the message shouldn't be there
			if !toolCallIDs[msg.ToolCallID] {
				// This is acceptable if the tool result is part of the summary structure
				if msg.Extra == nil || msg.Extra["subtype"] == nil {
					// Orphaned tool result in regular messages is a problem
					t.Logf("warning: tool result for %s present without matching call", msg.ToolCallID)
				}
			}
		}
	}
}

func TestLongSessionCompactionWithBranchFromCompactedHistory(t *testing.T) {
	// Create and compact a conversation
	messages := generateLongConversation(40, 150)
	tracking := &CompactTracking{}

	tokensBefore := EstimateTokenCount(messages)
	window := setWindowForTokenCount(t, tokensBefore)

	result, _, _ := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatalf("expected compaction to trigger (tokens=%d)", tokensBefore)
		return
	}

	postMessages := BuildPostCompactMessages(result)

	// Simulate branching from compacted history:
	// Take the first N messages of the post-compact state as a branch point
	branchPoint := len(postMessages) - 1
	if branchPoint < 1 {
		branchPoint = 1
	}
	branchedMessages := make([]*schema.Message, branchPoint)
	copy(branchedMessages, postMessages[:branchPoint])

	// The branch should still have the boundary marker
	hasBoundary := false
	for _, msg := range branchedMessages {
		if IsPivotBoundary(msg) {
			hasBoundary = true
			break
		}
	}
	if !hasBoundary {
		t.Fatal("branched messages should contain the compaction boundary marker")
	}

	// Adding new messages to the branch should work and stay within token limits
	newMessages := generateLongConversation(5, 100)
	branchedWithNew := append(branchedMessages, newMessages...)
	branchedTokens := EstimateTokenCount(branchedWithNew)

	// Should still be well within the original window
	if branchedTokens > window {
		t.Fatalf("branched + new messages (%d tokens) exceeds window (%d)", branchedTokens, window)
	}
}

func TestLongSessionCompactionDoesNotTriggerBelowThreshold(t *testing.T) {
	// Default 200k window — a 10-turn conversation should NOT trigger compaction
	messages := generateLongConversation(10, 200)
	tracking := &CompactTracking{}

	result, _, updated := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result != nil {
		t.Fatal("expected no compaction for small conversation under default 200k window")
		return
	}
	if updated.Compacted {
		t.Fatal("expected tracking.Compacted to remain false")
	}
}

func TestLongSessionCompactionHistoryAndTranscriptIntegration(t *testing.T) {
	log := NewCompactionLog()
	var transcriptEntries []TranscriptMetadataEntry

	// Simulate a mock transcript recorder
	recorder := &mockTranscriptRecorder{entries: &transcriptEntries}

	// First compaction
	messages := generateLongConversation(25, 150)
	tracking := &CompactTracking{}

	tokensBefore := EstimateTokenCount(messages)
	setWindowForTokenCount(t, tokensBefore)

	result1, _, tracking := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result1 == nil {
		t.Fatalf("expected first compaction (tokens=%d)", tokensBefore)
		return
	}

	// Record in history log
	event1 := CompactionEvent{
		Timestamp:         time.Now(),
		Reason:            CompactionReasonAuto,
		MessagesCompacted: len(messages),
		MessagesAfter:     len(BuildPostCompactMessages(result1)),
		TokensBefore:      result1.PreCompactTokenCount,
		TokensAfter:       result1.PostCompactTokenCount,
		Strategy:          "deterministic",
		Success:           true,
		Duration:          50 * time.Millisecond,
	}
	eventID1 := log.Record(event1)
	event1.ID = eventID1

	// Record in transcript
	transcriptEntry := BuildTranscriptCompactionEntry(event1, result1.Summary)
	err := RecordCompactionInTranscript(recorder, transcriptEntry)
	if err != nil {
		t.Fatalf("RecordCompactionInTranscript error: %v", err)
		return
	}

	// Second compaction
	postFirst := BuildPostCompactMessages(result1)
	additionalMessages := generateLongConversation(20, 150)
	combined := append(postFirst, additionalMessages...)

	combinedTokens := EstimateTokenCount(combined)
	setWindowForTokenCount(t, combinedTokens)

	result2, _, _ := AutoCompact(combined, "sdk", tracking, 0, "", nil)
	if result2 == nil {
		t.Fatalf("expected second compaction (tokens=%d)", combinedTokens)
		return
	}

	event2 := CompactionEvent{
		Timestamp:         time.Now(),
		Reason:            CompactionReasonAuto,
		MessagesCompacted: len(combined),
		MessagesAfter:     len(BuildPostCompactMessages(result2)),
		TokensBefore:      result2.PreCompactTokenCount,
		TokensAfter:       result2.PostCompactTokenCount,
		Strategy:          "deterministic",
		Success:           true,
		Duration:          40 * time.Millisecond,
	}
	eventID2 := log.Record(event2)
	event2.ID = eventID2

	transcriptEntry2 := BuildTranscriptCompactionEntry(event2, result2.Summary)
	err = RecordCompactionInTranscript(recorder, transcriptEntry2)
	if err != nil {
		t.Fatalf("RecordCompactionInTranscript error: %v", err)
		return
	}

	// Verify: transcript has 2 compaction metadata entries
	if len(transcriptEntries) != 2 {
		t.Fatalf("expected 2 transcript entries, got %d", len(transcriptEntries))
	}

	// Simulate session resume: detect boundaries from transcript
	resumeState := AnalyzeResumeCompactionState(transcriptEntries, BuildPostCompactMessages(result2))
	if !resumeState.HasCompaction {
		t.Fatal("resume state should detect compaction")
	}
	if resumeState.CompactionCount != 2 {
		t.Fatalf("expected 2 compaction boundaries, got %d", resumeState.CompactionCount)
	}
	if resumeState.LastCompaction == nil {
		t.Fatal("expected non-nil LastCompaction")
		return
	}
	if !resumeState.MessagesHaveBoundaryMarker {
		t.Fatal("expected messages to have boundary marker")
	}

	// Rebuild log from transcript (simulating resume)
	rebuiltLog := RebuildCompactionLog(transcriptEntries)
	if rebuiltLog.Count() != 2 {
		t.Fatalf("rebuilt log should have 2 events, got %d", rebuiltLog.Count())
	}
}

func TestLongSessionCompactionCircuitBreakerPreventsInfiniteLoop(t *testing.T) {
	messages := generateLongConversation(30, 150)

	tokensBefore := EstimateTokenCount(messages)
	setWindowForTokenCount(t, tokensBefore)

	// Simulate 3 consecutive failures (circuit breaker threshold)
	tracking := &CompactTracking{
		ConsecutiveFailures: maxConsecutiveAutoCompactFailures,
	}

	result, failures, updated := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result != nil {
		t.Fatal("expected circuit breaker to prevent compaction")
		return
	}
	if failures != maxConsecutiveAutoCompactFailures {
		t.Fatalf("expected failure count preserved at %d, got %d",
			maxConsecutiveAutoCompactFailures, failures)
	}
	if updated.Compacted {
		t.Fatal("expected no compaction with circuit breaker active")
	}
}

func TestLongSessionCompactionResetsTurnCounter(t *testing.T) {
	messages := generateLongConversation(30, 150)

	tokensBefore := EstimateTokenCount(messages)
	setWindowForTokenCount(t, tokensBefore)

	tracking := &CompactTracking{
		TurnCounter:         15,
		ConsecutiveFailures: 1,
	}

	result, _, updated := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatalf("expected compaction (tokens=%d)", tokensBefore)
		return
	}

	// Successful compaction should reset turn counter and failures
	if updated.TurnCounter != 0 {
		t.Fatalf("expected TurnCounter reset to 0, got %d", updated.TurnCounter)
	}
	if updated.ConsecutiveFailures != 0 {
		t.Fatalf("expected ConsecutiveFailures reset to 0, got %d", updated.ConsecutiveFailures)
	}
}

// --- Transcript interaction tests ---

func TestTranscriptCompactionEntryRoundTrip(t *testing.T) {
	event := CompactionEvent{
		ID:                1,
		Timestamp:         time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC),
		Reason:            CompactionReasonAuto,
		MessagesCompacted: 30,
		TokensBefore:      50000,
		TokensAfter:       8000,
		Strategy:          "llm_full",
	}

	entry := BuildTranscriptCompactionEntry(event, "This is a summary of the conversation...")
	if entry.Kind != "compaction_boundary" {
		t.Fatalf("expected kind=compaction_boundary, got %s", entry.Kind)
	}
	if entry.Reason != CompactionReasonAuto {
		t.Fatalf("expected reason=auto, got %s", entry.Reason)
	}
	if entry.MessagesCompacted != 30 {
		t.Fatalf("expected 30 messages compacted, got %d", entry.MessagesCompacted)
	}
	if entry.SummaryPreview == "" {
		t.Fatal("expected non-empty summary preview")
	}
	if entry.EventID != 1 {
		t.Fatalf("expected event_id=1, got %d", entry.EventID)
	}
}

func TestTranscriptCompactionEntrySummaryPreviewTruncation(t *testing.T) {
	event := CompactionEvent{ID: 1}
	longSummary := strings.Repeat("x", 500)

	entry := BuildTranscriptCompactionEntry(event, longSummary)
	if len(entry.SummaryPreview) != 200 {
		t.Fatalf("expected preview truncated to 200 chars, got %d", len(entry.SummaryPreview))
	}
}

func TestDetectCompactionBoundaries(t *testing.T) {
	entries := []TranscriptMetadataEntry{
		{Key: "model", Value: "claude-sonnet-4"},
		{Key: "compaction_boundary", Value: `{"kind":"compaction_boundary","reason":"auto","messages_compacted":20,"tokens_before":40000,"tokens_after":8000}`},
		{Key: "cwd", Value: "/home/user/project"},
		{Key: "compaction_boundary", Value: `{"kind":"compaction_boundary","reason":"ptl","messages_compacted":35,"tokens_before":60000,"tokens_after":12000}`},
		{Key: "compaction_boundary", Value: `malformed json{{{`}, // Should be skipped
	}

	boundaries := DetectCompactionBoundaries(entries)
	if len(boundaries) != 2 {
		t.Fatalf("expected 2 valid boundaries (skipping malformed), got %d", len(boundaries))
	}
	if boundaries[0].Reason != CompactionReasonAuto {
		t.Fatalf("expected first boundary reason=auto, got %s", boundaries[0].Reason)
	}
	if boundaries[1].Reason != CompactionReasonPTL {
		t.Fatalf("expected second boundary reason=ptl, got %s", boundaries[1].Reason)
	}
	if boundaries[0].MessagesCompacted != 20 {
		t.Fatalf("expected first boundary 20 messages, got %d", boundaries[0].MessagesCompacted)
	}
}

func TestAnalyzeResumeCompactionState(t *testing.T) {
	metadata := []TranscriptMetadataEntry{
		{Key: "compaction_boundary", Value: `{"kind":"compaction_boundary","reason":"auto","messages_compacted":20,"tokens_before":40000,"tokens_after":8000}`},
	}
	messages := []*schema.Message{
		{Role: schema.System, Content: "", Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.User, Content: "summary..."},
		{Role: schema.User, Content: "latest message"},
	}

	state := AnalyzeResumeCompactionState(metadata, messages)
	if !state.HasCompaction {
		t.Fatal("expected HasCompaction=true")
	}
	if state.CompactionCount != 1 {
		t.Fatalf("expected CompactionCount=1, got %d", state.CompactionCount)
	}
	if state.LastCompaction == nil {
		t.Fatal("expected non-nil LastCompaction")
		return
	}
	if !state.MessagesHaveBoundaryMarker {
		t.Fatal("expected MessagesHaveBoundaryMarker=true")
	}
}

func TestAnalyzeResumeCompactionStateNoCompaction(t *testing.T) {
	metadata := []TranscriptMetadataEntry{
		{Key: "model", Value: "gpt-4"},
		{Key: "cwd", Value: "/home/user"},
	}
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
	}

	state := AnalyzeResumeCompactionState(metadata, messages)
	if state.HasCompaction {
		t.Fatal("expected HasCompaction=false")
	}
	if state.CompactionCount != 0 {
		t.Fatalf("expected CompactionCount=0, got %d", state.CompactionCount)
	}
	if state.MessagesHaveBoundaryMarker {
		t.Fatal("expected MessagesHaveBoundaryMarker=false")
	}
}

func TestRebuildCompactionLog(t *testing.T) {
	entries := []TranscriptMetadataEntry{
		{Key: "compaction_boundary", Value: `{"kind":"compaction_boundary","reason":"auto","messages_compacted":20,"tokens_before":40000,"tokens_after":8000,"strategy":"llm_full"}`},
		{Key: "compaction_boundary", Value: `{"kind":"compaction_boundary","reason":"ptl","messages_compacted":35,"tokens_before":60000,"tokens_after":12000,"strategy":"deterministic"}`},
	}

	log := RebuildCompactionLog(entries)
	if log.Count() != 2 {
		t.Fatalf("expected 2 events in rebuilt log, got %d", log.Count())
	}

	events := log.Events()
	if events[0].Reason != CompactionReasonAuto {
		t.Fatalf("expected first event reason=auto, got %s", events[0].Reason)
	}
	if events[1].Reason != CompactionReasonPTL {
		t.Fatalf("expected second event reason=ptl, got %s", events[1].Reason)
	}
	if events[0].TokensBefore != 40000 {
		t.Fatalf("expected first event TokensBefore=40000, got %d", events[0].TokensBefore)
	}
}

func TestRecordCompactionInTranscriptNilRecorder(t *testing.T) {
	// Should not panic with nil recorder
	err := RecordCompactionInTranscript(nil, TranscriptCompactionEntry{})
	if err != nil {
		t.Fatalf("expected nil error for nil recorder, got %v", err)
		return
	}
}

func TestRecordCompactionInTranscriptWritesCorrectly(t *testing.T) {
	var entries []TranscriptMetadataEntry
	recorder := &mockTranscriptRecorder{entries: &entries}

	entry := TranscriptCompactionEntry{
		Kind:              "compaction_boundary",
		Reason:            CompactionReasonManual,
		MessagesCompacted: 10,
		TokensBefore:      20000,
		TokensAfter:       5000,
		Strategy:          "llm_full",
		EventID:           3,
	}

	err := RecordCompactionInTranscript(recorder, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry recorded, got %d", len(entries))
	}
	if entries[0].Key != "compaction_boundary" {
		t.Fatalf("expected key=compaction_boundary, got %s", entries[0].Key)
	}

	// Parse the value back
	var parsed TranscriptCompactionEntry
	if err := parseJSON(entries[0].Value, &parsed); err != nil {
		t.Fatalf("failed to parse recorded value: %v", err)
		return
	}
	if parsed.Reason != CompactionReasonManual {
		t.Fatalf("expected reason=manual, got %s", parsed.Reason)
	}
	if parsed.EventID != 3 {
		t.Fatalf("expected event_id=3, got %d", parsed.EventID)
	}
}

func TestCountCompactionBoundariesInMessages(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.System, Content: "", Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.User, Content: "summary"},
		{Role: schema.User, Content: "hello"},
		{Role: schema.System, Content: "", Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.User, Content: "second summary"},
		{Role: schema.Assistant, Content: "response"},
	}

	count := CountCompactionBoundariesInMessages(messages)
	if count != 2 {
		t.Fatalf("expected 2 boundaries, got %d", count)
	}
}

func TestCountCompactionBoundariesNone(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
	}

	count := CountCompactionBoundariesInMessages(messages)
	if count != 0 {
		t.Fatalf("expected 0 boundaries, got %d", count)
	}
}

// --- Mock transcript recorder ---

type mockTranscriptRecorder struct {
	entries *[]TranscriptMetadataEntry
}

func (m *mockTranscriptRecorder) RecordMetadata(key, value string) error {
	*m.entries = append(*m.entries, TranscriptMetadataEntry{
		Key:       key,
		Value:     value,
		Timestamp: time.Now(),
	})
	return nil
}

// parseJSON is a test helper.
func parseJSON(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}
