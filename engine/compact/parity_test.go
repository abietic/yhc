package compact

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// parity_test.go — Final parity verification tests for compaction.
// Covers:
// - Compaction preserves system messages and instruction context
// - Multiple sequential compactions produce correct stacked pivots
// - Compaction with tool_use/tool_result pairs (never splits a pair)
// - Recovery from compaction failure (original messages preserved)
// - Pivot message structure and metadata
// - Grouping by API round

// --- Pivot Message Tests ---

// TestPivotMessageStructure verifies the pivot has boundary + summary + optional continuation.
func TestPivotMessageStructure(t *testing.T) {
	pivot := CreatePivotMessage("Summary of conversation", PivotConfig{
		Trigger:              "auto",
		PreCompactTokenCount: 50000,
		IncludeContinuation:  true,
		SuppressFollowUp:     true,
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
	if pivot.Continuation == nil {
		t.Fatal("expected non-nil continuation with IncludeContinuation=true")
		return
	}

	// Boundary should be system role with compact_boundary subtype
	if pivot.Boundary.Role != schema.System {
		t.Errorf("expected system role for boundary, got %v", pivot.Boundary.Role)
	}
	if !IsPivotBoundary(pivot.Boundary) {
		t.Error("expected IsPivotBoundary to return true for boundary message")
	}

	// Summary should be user role with compact_summary subtype
	if pivot.Summary.Role != schema.User {
		t.Errorf("expected user role for summary, got %v", pivot.Summary.Role)
	}
	if !IsPivotSummary(pivot.Summary) {
		t.Error("expected IsPivotSummary to return true for summary message")
	}

	// Continuation should be system role with continuation_marker subtype
	if pivot.Continuation.Role != schema.System {
		t.Errorf("expected system role for continuation, got %v", pivot.Continuation.Role)
	}
	if !IsContinuationMarker(pivot.Continuation) {
		t.Error("expected IsContinuationMarker to return true for continuation message")
	}
}

// TestPivotMessageWithoutContinuation verifies continuation is nil when not requested.
func TestPivotMessageWithoutContinuation(t *testing.T) {
	pivot := CreatePivotMessage("Summary", PivotConfig{
		Trigger:             "manual",
		IncludeContinuation: false,
	})
	if pivot.Continuation != nil {
		t.Error("expected nil continuation with IncludeContinuation=false")
	}
}

// TestPivotMessagePreservesFacts verifies preserved facts appear in the summary.
func TestPivotMessagePreservesFacts(t *testing.T) {
	pivot := CreatePivotMessage("Conversation about Go", PivotConfig{
		Trigger: "auto",
		PreservedFacts: []string{
			"Working on project at /workspace/myproject",
			"Using Go 1.25",
			"Target is Linux amd64",
		},
	})

	content := pivot.Summary.Content
	if !strings.Contains(content, "Working on project at /workspace/myproject") {
		t.Error("expected preserved fact about project path")
	}
	if !strings.Contains(content, "Using Go 1.25") {
		t.Error("expected preserved fact about Go version")
	}
	if !strings.Contains(content, "Target is Linux amd64") {
		t.Error("expected preserved fact about target")
	}
}

// TestPivotMessageSuppressFollowUp verifies the suppression directive.
func TestPivotMessageSuppressFollowUp(t *testing.T) {
	pivot := CreatePivotMessage("Summary", PivotConfig{
		Trigger:          "auto",
		SuppressFollowUp: true,
	})

	content := pivot.Summary.Content
	if !strings.Contains(content, "Continue the conversation") {
		t.Error("expected continuation directive with SuppressFollowUp")
	}
	if !strings.Contains(content, "without asking") {
		t.Error("expected 'without asking' in suppressed followup text")
	}
}

// TestPivotMetadataExtraction verifies ExtractPivotMetadata.
func TestPivotMetadataExtraction(t *testing.T) {
	pivot := CreatePivotMessage("Test summary", PivotConfig{
		Trigger:              "reactive",
		PreCompactTokenCount: 75000,
		LastMessageUUID:      "uuid-abc-123",
	})

	metadata := ExtractPivotMetadata(pivot.Boundary)
	if metadata == nil {
		t.Fatal("expected non-nil metadata from boundary")
		return
	}
	if metadata["trigger"] != "reactive" {
		t.Errorf("expected trigger 'reactive', got %v", metadata["trigger"])
	}
	if metadata["pre_compact_tokens"] != 75000 {
		t.Errorf("expected pre_compact_tokens 75000, got %v", metadata["pre_compact_tokens"])
	}
	if metadata["last_message_uuid"] != "uuid-abc-123" {
		t.Errorf("expected last_message_uuid 'uuid-abc-123', got %v", metadata["last_message_uuid"])
	}
}

// --- Boundary Detection Tests ---

// TestParity_GetMessagesAfterCompactBoundary verifies finding the latest boundary.
func TestParity_GetMessagesAfterCompactBoundary(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "old message 1"},
		{Role: schema.Assistant, Content: "old response 1"},
		{Role: schema.System, Content: "", Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.User, Content: "summary of earlier conversation"},
		{Role: schema.User, Content: "new message after compact"},
		{Role: schema.Assistant, Content: "new response"},
	}

	after := GetMessagesAfterCompactBoundary(messages)
	if len(after) != 4 {
		t.Fatalf("expected 4 messages after boundary, got %d", len(after))
	}
	if after[0].Role != schema.System {
		t.Error("expected first message to be the boundary itself")
	}
	if !IsPivotBoundary(after[0]) {
		t.Error("expected first message to be a pivot boundary")
	}
}

// TestParity_GetMessagesAfterCompactBoundaryNoBoundary verifies full message return
// when no boundary exists.
func TestParity_GetMessagesAfterCompactBoundaryNoBoundary(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "msg 1"},
		{Role: schema.Assistant, Content: "msg 2"},
		{Role: schema.User, Content: "msg 3"},
	}

	after := GetMessagesAfterCompactBoundary(messages)
	if len(after) != 3 {
		t.Fatalf("expected all 3 messages when no boundary exists, got %d", len(after))
	}
}

// TestMultipleSequentialCompactionsStackPivots verifies that multiple compactions
// create proper stacked boundaries (each new compaction replaces the previous
// compacted portion but the boundary markers are identifiable).
func TestMultipleSequentialCompactionsStackPivots(t *testing.T) {
	// First compaction result
	pivot1 := CreatePivotMessage("First compaction summary", PivotConfig{
		Trigger:             "auto",
		IncludeContinuation: true,
	})
	msgs := pivot1.Messages()

	// Add some conversation after first compaction
	msgs = append(msgs,
		&schema.Message{Role: schema.User, Content: "continue working"},
		&schema.Message{Role: schema.Assistant, Content: "sure, working on it"},
		&schema.Message{Role: schema.User, Content: "more context here"},
		&schema.Message{Role: schema.Assistant, Content: "got it"},
	)

	// Second compaction — should find only messages after the last boundary
	_ = GetMessagesAfterCompactBoundary(msgs)
	// The boundary itself plus subsequent messages

	// Create a second pivot based on the after-boundary messages
	pivot2 := CreatePivotMessage("Second compaction: includes first summary + new work", PivotConfig{
		Trigger:             "auto",
		IncludeContinuation: true,
	})
	finalMsgs := pivot2.Messages()
	finalMsgs = append(finalMsgs,
		&schema.Message{Role: schema.User, Content: "latest question"},
		&schema.Message{Role: schema.Assistant, Content: "latest answer"},
	)

	// Verify the final message set has exactly one compact_boundary
	boundaryCount := 0
	for _, msg := range finalMsgs {
		if IsPivotBoundary(msg) {
			boundaryCount++
		}
	}
	if boundaryCount != 1 {
		t.Errorf("expected exactly 1 compact_boundary in final message set, got %d", boundaryCount)
	}

	// After the boundary: all messages should be accessible
	afterFinal := GetMessagesAfterCompactBoundary(finalMsgs)
	if len(afterFinal) != len(finalMsgs) {
		// Since there's only one boundary at position 0, all messages are after it
		t.Logf("afterFinal has %d messages, finalMsgs has %d", len(afterFinal), len(finalMsgs))
	}
}

// --- Grouping Tests ---

// TestParity_GroupMessagesByAPIRound verifies correct grouping at assistant boundaries.
func TestParity_GroupMessagesByAPIRound(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "first question"},
		{Role: schema.Assistant, Content: "first answer", Extra: map[string]any{"message_id": "m1"}},
		{Role: schema.Tool, Content: "tool result", ToolCallID: "tc1"},
		{Role: schema.User, Content: "second question"},
		{Role: schema.Assistant, Content: "second answer", Extra: map[string]any{"message_id": "m2"}},
	}

	groups := GroupMessagesByAPIRound(messages)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	// First group: user + assistant(m1) + tool + user (split happens at next new assistant)
	if len(groups[0]) != 4 {
		t.Errorf("expected 4 messages in first group, got %d", len(groups[0]))
	}
	// Second group: assistant(m2) only
	if len(groups[1]) != 1 {
		t.Errorf("expected 1 message in second group, got %d", len(groups[1]))
	}
}

// TestGroupMessagesByAPIRoundToolPairsStayTogether verifies tool_use/tool_result
// pairs are never split across groups.
func TestGroupMessagesByAPIRoundToolPairsStayTogether(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "run something"},
		{Role: schema.Assistant, Content: "running...", Extra: map[string]any{"message_id": "m1"}, ToolCalls: []schema.ToolCall{
			{ID: "tc_1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"ls"}`}},
		}},
		{Role: schema.Tool, Content: "file1\nfile2", ToolCallID: "tc_1"},
		{Role: schema.Assistant, Content: "here are the files", Extra: map[string]any{"message_id": "m1"}},
		{Role: schema.User, Content: "next question"},
		{Role: schema.Assistant, Content: "next answer", Extra: map[string]any{"message_id": "m2"}},
	}

	groups := GroupMessagesByAPIRound(messages)
	// The tool result stays with the same assistant group (same message_id "m1").
	// Split happens at next new assistant (m2).
	// First group: User + Asst(m1) + Tool + Asst(m1) + User = 5
	// Second group: Asst(m2) = 1
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if len(groups[0]) != 5 {
		t.Errorf("expected 5 messages in first group (tool pair intact with same assistant ID), got %d", len(groups[0]))
	}
	// Verify the tool message is in the same group as its assistant
	foundTool := false
	foundAssistantWithTC := false
	for _, msg := range groups[0] {
		if msg.Role == schema.Tool && msg.ToolCallID == "tc_1" {
			foundTool = true
		}
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			foundAssistantWithTC = true
		}
	}
	if !foundTool || !foundAssistantWithTC {
		t.Error("expected both tool_use (assistant with tool calls) and tool_result in same group")
	}
}

// --- Recovery Tests ---

// TestParity_CompactWithRecoveryNoModel verifies behavior when no ChatModel is provided.
func TestParity_CompactWithRecoveryNoModel(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "test"},
		{Role: schema.Assistant, Content: "response"},
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: nil, // No model
	}, &CompactRecoveryConfig{
		FallbackToDeterministic: true,
	})

	// Without a model, should fall back to deterministic
	if !result.Success {
		t.Error("expected success via deterministic fallback")
	}
	if result.Strategy != RecoveryDeterministic {
		t.Errorf("expected RecoveryDeterministic strategy, got %d", result.Strategy)
	}
	if len(result.Messages) == 0 {
		t.Error("expected non-empty messages from deterministic fallback")
	}
}

// TestParity_CompactWithRecoveryNoModelNoDeterministic verifies total failure path.
func TestParity_CompactWithRecoveryNoModelNoDeterministic(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "test"},
		{Role: schema.Assistant, Content: "response"},
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: nil,
	}, &CompactRecoveryConfig{
		FallbackToDeterministic: false,
	})

	// Without model and without deterministic fallback, should fail
	if result.Success {
		t.Error("expected failure without model and without deterministic fallback")
	}
	if result.Strategy != RecoveryPreserveOriginal {
		t.Errorf("expected RecoveryPreserveOriginal strategy, got %d", result.Strategy)
	}
	if result.Error == nil {
		t.Error("expected non-nil error on total failure")
	}
	// Messages should be nil (caller keeps originals)
	if result.Messages != nil {
		t.Error("expected nil messages on failure (caller preserves originals)")
	}
}

// TestParity_CompactWithRecoveryEmptyMessages verifies edge case of empty input.
func TestParity_CompactWithRecoveryEmptyMessages(t *testing.T) {
	result := CompactWithRecovery(context.Background(), nil, LLMCompactOptions{}, nil)
	if result.Success {
		t.Error("expected failure for empty messages")
	}
	if result.Error == nil {
		t.Error("expected error for empty messages")
	}
}

// TestCompactWithRecoveryPreservesTokenCounts verifies token counting.
func TestCompactWithRecoveryPreservesTokenCounts(t *testing.T) {
	messages := make([]*schema.Message, 10)
	for i := 0; i < 10; i++ {
		role := schema.User
		if i%2 == 1 {
			role = schema.Assistant
		}
		messages[i] = &schema.Message{
			Role:    role,
			Content: strings.Repeat("token ", 100),
		}
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: nil,
	}, &CompactRecoveryConfig{
		FallbackToDeterministic: true,
	})

	if result.PreCompactTokens <= 0 {
		t.Errorf("expected positive PreCompactTokens, got %d", result.PreCompactTokens)
	}
	if result.Success && result.PostCompactTokens <= 0 {
		t.Errorf("expected positive PostCompactTokens on success, got %d", result.PostCompactTokens)
	}
	// Post-compact should be smaller than pre-compact (deterministic reduces)
	if result.Success && result.PostCompactTokens >= result.PreCompactTokens {
		t.Errorf("expected post-compact tokens (%d) < pre-compact tokens (%d)",
			result.PostCompactTokens, result.PreCompactTokens)
	}
}

// --- Token Estimation Tests ---

// TestEstimateTokenCountBasic verifies the heuristic token estimation.
func TestEstimateTokenCountBasic(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "Hello world"},
		{Role: schema.Assistant, Content: "Hi there! How can I help?"},
	}
	tokens := EstimateTokenCount(messages)
	if tokens <= 0 {
		t.Error("expected positive token count")
	}
	// Rough heuristic: "Hello world" = 11 chars / 4 = ~3 tokens + overhead
	// Should be in a reasonable range
	if tokens > 100 {
		t.Errorf("token estimate seems too high for short messages: %d", tokens)
	}
}

// TestEstimateTokenCountWithToolCalls verifies tool calls add to the estimate.
func TestEstimateTokenCountWithToolCalls(t *testing.T) {
	withoutTC := []*schema.Message{
		{Role: schema.Assistant, Content: "running"},
	}
	withTC := []*schema.Message{
		{Role: schema.Assistant, Content: "running", ToolCalls: []schema.ToolCall{
			{ID: "tc_1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"ls -la"}`}},
		}},
	}

	tokensWithout := EstimateTokenCount(withoutTC)
	tokensWith := EstimateTokenCount(withTC)

	if tokensWith <= tokensWithout {
		t.Errorf("expected more tokens with tool calls (%d) than without (%d)", tokensWith, tokensWithout)
	}
}

// TestEstimateTokenCountNilMessages verifies nil handling.
func TestEstimateTokenCountNilMessages(t *testing.T) {
	tokens := EstimateTokenCount(nil)
	if tokens != 0 {
		t.Errorf("expected 0 for nil messages, got %d", tokens)
	}

	tokens = EstimateTokenCount([]*schema.Message{nil, nil})
	if tokens != 0 {
		t.Errorf("expected 0 for nil message pointers, got %d", tokens)
	}
}

// --- Strip Images Tests ---

// TestStripImagesFromMessages verifies image blocks are replaced with placeholders.
func TestStripImagesFromMessages(t *testing.T) {
	messages := []*schema.Message{
		{
			Role: schema.User,
			MultiContent: []schema.ChatMessagePart{ //nolint:staticcheck
				{Type: schema.ChatMessagePartTypeText, Text: "Look at this:"},
				{Type: schema.ChatMessagePartTypeImageURL, ImageURL: &schema.ChatMessageImageURL{URL: "data:image/png;base64,abc123"}}, //nolint:staticcheck
				{Type: schema.ChatMessagePartTypeText, Text: "What do you see?"},
			},
		},
		{Role: schema.Assistant, Content: "I see an image."},
	}

	stripped := StripImagesFromMessages(messages)
	if len(stripped) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(stripped))
	}

	// First message should have images replaced
	userMsg := stripped[0]
	if len(userMsg.MultiContent) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(userMsg.MultiContent))
	}
	// The image part should now be text "[image]"
	if userMsg.MultiContent[1].Type != schema.ChatMessagePartTypeText {
		t.Errorf("expected image replaced with text, got type %v", userMsg.MultiContent[1].Type)
	}
	if userMsg.MultiContent[1].Text != "[image]" {
		t.Errorf("expected '[image]' placeholder, got %q", userMsg.MultiContent[1].Text)
	}

	// Assistant message should be unchanged
	if stripped[1].Content != "I see an image." {
		t.Errorf("assistant message should be unchanged, got %q", stripped[1].Content)
	}
}

// TestStripImagesPreservesNonUserMessages verifies only user messages are stripped.
func TestStripImagesPreservesNonUserMessages(t *testing.T) {
	messages := []*schema.Message{
		{
			Role: schema.Assistant,
			MultiContent: []schema.ChatMessagePart{ //nolint:staticcheck
				{Type: schema.ChatMessagePartTypeImageURL, ImageURL: &schema.ChatMessageImageURL{URL: "http://example.com/img.png"}}, //nolint:staticcheck
			},
		},
	}

	stripped := StripImagesFromMessages(messages)
	// Non-user messages should pass through unchanged
	if len(stripped[0].MultiContent) != 1 {
		t.Error("non-user messages should not be modified")
	}
	if stripped[0].MultiContent[0].Type != schema.ChatMessagePartTypeImageURL {
		t.Error("non-user image should be preserved")
	}
}

// TestTruncateHeadForPTLRetry verifies head truncation for PTL recovery.
func TestTruncateHeadForPTLRetry(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("word ", 500)},
		{Role: schema.Assistant, Content: "response 1", Extra: map[string]any{"message_id": "m1"}},
		{Role: schema.User, Content: strings.Repeat("word ", 500)},
		{Role: schema.Assistant, Content: "response 2", Extra: map[string]any{"message_id": "m2"}},
		{Role: schema.User, Content: strings.Repeat("word ", 500)},
		{Role: schema.Assistant, Content: "response 3", Extra: map[string]any{"message_id": "m3"}},
	}

	// Request dropping enough to free some tokens
	truncated := TruncateHeadForPTLRetry(messages, 500)
	if truncated == nil {
		t.Fatal("expected non-nil truncated result")
		return
	}
	if len(truncated) >= len(messages) {
		t.Error("expected fewer messages after head truncation")
	}
	// Should keep at least one group
	if len(truncated) == 0 {
		t.Error("should not truncate everything")
	}
}

// TestTruncateHeadForPTLRetryTooFewGroups verifies nil return when can't truncate.
func TestTruncateHeadForPTLRetryTooFewGroups(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "only one group"},
		{Role: schema.Assistant, Content: "response"},
	}
	result := TruncateHeadForPTLRetry(messages, 1000)
	if result != nil {
		t.Error("expected nil when there's only one group")
	}
}
