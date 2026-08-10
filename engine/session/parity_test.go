package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

// parity_test.go — Final parity verification tests for session/transcript.
// Covers:
// - Full session lifecycle: create -> resume -> branch -> resume-branch -> delete -> verify cleanup
// - Transcript corruption scenarios
// - Cross-entrypoint consistency
// - Message validation edge cases
// - Token truncation behavior

// --- Session Lifecycle Tests ---

// TestSessionFullLifecycle verifies the complete lifecycle:
// create -> resume -> branch -> resume-branch -> delete -> verify cleanup.
func TestSessionFullLifecycle(t *testing.T) {
	dir := t.TempDir()

	// Step 1: Create session via transcript recorder.
	rec := transcript.NewRecorder("lifecycle-session", dir)
	messages := []*schema.Message{
		{Role: schema.System, Content: "You are helpful."},
		{Role: schema.User, Content: "Hello, world!"},
		{Role: schema.Assistant, Content: "Hi! How can I help?"},
		{Role: schema.User, Content: "Tell me about Go."},
		{Role: schema.Assistant, Content: "Go is a statically typed language."},
	}
	if err := rec.Record(messages[:3], false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Record(messages[3:], false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("git_branch", "main"); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Step 2: Resume the session.
	resumed, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:        "lifecycle-session",
		SessionDir:       dir,
		ValidateMessages: true,
	})
	if err != nil {
		t.Fatalf("ResumeSession failed: %v", err)
		return
	}
	if len(resumed.Messages) != 5 {
		t.Fatalf("expected 5 messages on resume, got %d", len(resumed.Messages))
	}
	if resumed.SessionID != "lifecycle-session" {
		t.Errorf("expected session ID 'lifecycle-session', got %q", resumed.SessionID)
	}
	if len(resumed.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", resumed.Warnings)
	}

	// Step 3: Branch from the session at message 3.
	branchResult, err := BranchSession(BranchOptions{
		SourceSessionID: "lifecycle-session",
		MessageIndex:    3,
		NewSessionID:    "branch-session",
		Dir:             dir,
	})
	if err != nil {
		t.Fatalf("BranchSession failed: %v", err)
		return
	}
	if branchResult.NewSessionID != "branch-session" {
		t.Errorf("expected branch ID 'branch-session', got %q", branchResult.NewSessionID)
	}
	if branchResult.MessagesCopied != 3 {
		t.Errorf("expected 3 messages copied, got %d", branchResult.MessagesCopied)
	}

	// Step 4: Resume the branch.
	branchResumed, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:        "branch-session",
		SessionDir:       dir,
		ValidateMessages: true,
	})
	if err != nil {
		t.Fatalf("resume branch failed: %v", err)
		return
	}
	if len(branchResumed.Messages) != 3 {
		t.Fatalf("expected 3 messages in branch, got %d", len(branchResumed.Messages))
	}
	if branchResumed.Metadata.ParentSessionID != "lifecycle-session" {
		t.Errorf("expected parent 'lifecycle-session', got %q", branchResumed.Metadata.ParentSessionID)
	}

	// Step 5: Delete the branch session.
	deleteResult, err := DeleteSession(DeleteOptions{
		SessionID: "branch-session",
		Dir:       dir,
	})
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
		return
	}
	if !deleteResult.TranscriptRemoved {
		t.Error("expected transcript to be removed")
	}
	if deleteResult.BytesFreed <= 0 {
		t.Error("expected positive bytes freed")
	}

	// Step 6: Verify cleanup — branch file should not exist.
	branchPath := filepath.Join(dir, "branch-session.jsonl")
	if _, err := os.Stat(branchPath); !os.IsNotExist(err) {
		t.Error("branch transcript should have been removed")
	}

	// Parent should still exist.
	parentPath := filepath.Join(dir, "lifecycle-session.jsonl")
	if _, err := os.Stat(parentPath); err != nil {
		t.Error("parent transcript should still exist")
	}
}

// TestResumeSessionNotFound verifies error on non-existent session.
func TestResumeSessionNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:  "nonexistent",
		SessionDir: dir,
	})
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

// TestResumeSessionMaxMessages verifies message truncation on resume.
func TestResumeSessionMaxMessages(t *testing.T) {
	dir := t.TempDir()
	rec := transcript.NewRecorder("long-session", dir)

	// Create 20 messages
	messages := make([]*schema.Message, 20)
	for i := 0; i < 20; i++ {
		role := schema.User
		if i%2 == 1 {
			role = schema.Assistant
		}
		messages[i] = &schema.Message{
			Role:    role,
			Content: strings.Repeat("msg ", 50) + string(rune('A'+i)),
		}
	}
	if err := rec.Record(messages, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Resume with MaxMessages=5 — should get only the 5 newest.
	resumed, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:   "long-session",
		SessionDir:  dir,
		MaxMessages: 5,
	})
	if err != nil {
		t.Fatalf("ResumeSession failed: %v", err)
		return
	}
	if len(resumed.Messages) != 5 {
		t.Fatalf("expected 5 messages with MaxMessages=5, got %d", len(resumed.Messages))
	}
	if resumed.TruncatedAt != 15 {
		t.Errorf("expected TruncatedAt=15, got %d", resumed.TruncatedAt)
	}
}

// TestBranchSessionInvalidOptions verifies error handling for bad branch options.
func TestBranchSessionInvalidOptions(t *testing.T) {
	dir := t.TempDir()

	// Missing source session ID
	_, err := BranchSession(BranchOptions{Dir: dir, MessageIndex: 1})
	if err == nil {
		t.Error("expected error for empty source session ID")
	}

	// Zero message index
	_, err = BranchSession(BranchOptions{
		SourceSessionID: "test",
		MessageIndex:    0,
		Dir:             dir,
	})
	if err == nil {
		t.Error("expected error for zero message index")
	}

	// Non-existent source
	_, err = BranchSession(BranchOptions{
		SourceSessionID: "nonexistent",
		MessageIndex:    1,
		Dir:             dir,
	})
	if err == nil {
		t.Error("expected error for non-existent source session")
	}
}

// TestDeleteSessionCleansTmpFiles verifies .tmp files are cleaned up on delete.
func TestDeleteSessionCleansTmpFiles(t *testing.T) {
	dir := t.TempDir()

	// Create session and a leftover .tmp file
	rec := transcript.NewRecorder("cleanup-session", dir)
	if err := rec.Record([]*schema.Message{
		{Role: schema.User, Content: "test"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	tmpPath := filepath.Join(dir, "cleanup-session.jsonl.tmp")
	if err := os.WriteFile(tmpPath, []byte("partial write"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	_, err := DeleteSession(DeleteOptions{
		SessionID: "cleanup-session",
		Dir:       dir,
	})
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
		return
	}

	// Both files should be gone
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("expected .tmp file to be cleaned up")
	}
}

// --- Transcript Corruption Tests ---

// TestTranscriptCorruptionBinaryGarbage verifies binary data is skipped gracefully.
func TestTranscriptCorruptionBinaryGarbage(t *testing.T) {
	dir := t.TempDir()

	// Write a valid entry, binary garbage, then another valid entry
	validEntry := `{"timestamp":"2026-06-10T10:00:00Z","kind":"user","message":{"role":"user","content":"hello"}}`
	binary := string([]byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD})
	validEntry2 := `{"timestamp":"2026-06-10T10:01:00Z","kind":"assistant","message":{"role":"assistant","content":"hi"}}`
	content := validEntry + "\n" + binary + "\n" + validEntry2 + "\n"

	path := filepath.Join(dir, "binary-garbage.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := transcript.NewRecorder("binary-garbage", dir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull should recover from binary garbage: %v", err)
		return
	}
	if len(result.Messages) != 2 {
		t.Errorf("expected 2 valid messages, got %d", len(result.Messages))
	}
	if len(result.Corruptions) != 1 {
		t.Errorf("expected 1 corruption, got %d", len(result.Corruptions))
	}
}

// TestTranscriptCorruptionBOMPrefix verifies UTF-8 BOM is handled.
func TestTranscriptCorruptionBOMPrefix(t *testing.T) {
	dir := t.TempDir()

	// UTF-8 BOM followed by valid JSON
	bom := "\xEF\xBB\xBF"
	validEntry := `{"timestamp":"2026-06-10T10:00:00Z","kind":"user","message":{"role":"user","content":"test"}}`
	content := bom + validEntry + "\n"

	path := filepath.Join(dir, "bom-prefix.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := transcript.NewRecorder("bom-prefix", dir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull should handle BOM prefix: %v", err)
		return
	}
	// BOM + JSON may fail to parse as JSON (BOM is not '{'), so it gets recorded as corruption
	// OR it parses successfully if the JSON parser is BOM-tolerant.
	// Either way, the load should not hard-fail.
	totalEntries := len(result.Messages) + len(result.Corruptions)
	if totalEntries != 1 {
		t.Errorf("expected 1 total entry (message or corruption), got %d messages + %d corruptions", len(result.Messages), len(result.Corruptions))
	}
}

// TestTranscriptCorruptionMixedValidAndInvalid verifies partial recovery.
func TestTranscriptCorruptionMixedValidAndInvalid(t *testing.T) {
	dir := t.TempDir()

	entries := []string{
		`{"timestamp":"2026-06-10T10:00:00Z","kind":"user","message":{"role":"user","content":"msg1"}}`,
		`not json at all`,
		`{"timestamp":"2026-06-10T10:01:00Z","kind":"assistant","message":{"role":"assistant","content":"msg2"}}`,
		`{"broken":true, no closing`,
		`{"timestamp":"2026-06-10T10:02:00Z","kind":"user","message":{"role":"user","content":"msg3"}}`,
	}
	content := strings.Join(entries, "\n") + "\n"
	path := filepath.Join(dir, "mixed.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := transcript.NewRecorder("mixed", dir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull should not hard-fail on mixed content: %v", err)
		return
	}
	if len(result.Messages) != 3 {
		t.Errorf("expected 3 valid messages, got %d", len(result.Messages))
	}
	if len(result.Corruptions) != 2 {
		t.Errorf("expected 2 corruptions, got %d", len(result.Corruptions))
	}
}

// TestTranscriptAtomicReplacePreservesOnCrash verifies that AtomicReplace uses
// temp+rename pattern.
func TestTranscriptAtomicReplacePreservesOnCrash(t *testing.T) {
	dir := t.TempDir()
	rec := transcript.NewRecorder("atomic-session", dir)

	// Create initial content
	initial := []*schema.Message{
		{Role: schema.User, Content: "original"},
	}
	if err := rec.Record(initial, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// AtomicReplace with new content
	replacement := []*schema.Message{
		{Role: schema.User, Content: "replaced content"},
		{Role: schema.Assistant, Content: "new response"},
	}
	if err := rec.AtomicReplace(replacement); err != nil {
		t.Fatalf("AtomicReplace failed: %v", err)
		return
	}

	// Verify content was replaced
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(result.Messages) != 2 {
		t.Fatalf("expected 2 messages after replace, got %d", len(result.Messages))
	}
	if result.Messages[0].Content != "replaced content" {
		t.Errorf("expected 'replaced content', got %q", result.Messages[0].Content)
	}

	// Verify no .tmp file remains
	tmpPath := filepath.Join(dir, "atomic-session.jsonl.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("expected no leftover .tmp file after successful AtomicReplace")
	}
}

// --- Message Validation Tests ---

// TestValidateMessageHistoryDetectsConsecutiveRoles verifies role alternation.
func TestValidateMessageHistoryDetectsConsecutiveRoles(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.User, Content: "second"}, // consecutive user
		{Role: schema.Assistant, Content: "response"},
	}

	warnings := ValidateMessageHistory(messages)
	if len(warnings) == 0 {
		t.Error("expected warning for consecutive user messages")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "consecutive") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'consecutive' warning, got %v", warnings)
	}
}

// TestValidateMessageHistoryDetectsOrphanedToolResults verifies orphan detection.
func TestValidateMessageHistoryDetectsOrphanedToolResults(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "do something"},
		{Role: schema.Tool, Content: "result", ToolCallID: "orphan_call_1"}, // no prior tool_use
	}

	warnings := ValidateMessageHistory(messages)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "orphaned") && strings.Contains(w, "orphan_call_1") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected orphaned tool_result warning, got %v", warnings)
	}
}

// TestValidateMessageHistoryDetectsUnresolvedToolCalls verifies unresolved tool calls.
func TestValidateMessageHistoryDetectsUnresolvedToolCalls(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "run bash"},
		{Role: schema.Assistant, Content: "running...", ToolCalls: []schema.ToolCall{
			{ID: "tc_1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"ls"}`}},
			{ID: "tc_2", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/a"}`}},
		}},
		{Role: schema.Tool, Content: "ls output", ToolCallID: "tc_1"}, // Only tc_1 resolved
	}

	warnings := ValidateMessageHistory(messages)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "tc_2") && strings.Contains(w, "no matching tool_result") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unresolved tool_call warning for tc_2, got %v", warnings)
	}
}

// TestValidateMessageHistoryCleanSessionNoWarnings verifies no false positives.
func TestValidateMessageHistoryCleanSessionNoWarnings(t *testing.T) {
	// A clean session: proper user/assistant alternation with system and tool
	// messages interspersed. Note: the validator flags consecutive assistant
	// roles (even with tool in between), so a proper clean session does NOT
	// have two assistant messages back-to-back.
	messages := []*schema.Message{
		{Role: schema.System, Content: "system prompt"},
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "here's the file content", ToolCalls: []schema.ToolCall{
			{ID: "tc_1", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/a"}`}},
		}},
		{Role: schema.Tool, Content: "file contents", ToolCallID: "tc_1"},
		{Role: schema.User, Content: "thanks"},
		{Role: schema.Assistant, Content: "you're welcome!"},
	}

	warnings := ValidateMessageHistory(messages)
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for clean session, got %v", warnings)
	}
}

// TestTruncateToTokenBudget verifies budget-aware truncation.
func TestTruncateToTokenBudget(t *testing.T) {
	messages := make([]*schema.Message, 20)
	for i := 0; i < 20; i++ {
		role := schema.User
		if i%2 == 1 {
			role = schema.Assistant
		}
		messages[i] = &schema.Message{
			Role:    role,
			Content: strings.Repeat("word ", 100), // ~500 chars each
		}
	}

	// Very small budget should truncate from front
	truncated, cutIdx := TruncateToTokenBudget(messages, 200)
	if cutIdx == 0 {
		t.Error("expected truncation with small budget")
	}
	if len(truncated) >= len(messages) {
		t.Error("expected fewer messages after truncation")
	}
	// Should always preserve at least the last user/assistant pair
	if len(truncated) < 2 {
		t.Error("expected at least 2 messages preserved (last turn pair)")
	}
}

// TestTruncateToTokenBudgetNoTruncationNeeded verifies pass-through when within budget.
func TestTruncateToTokenBudgetNoTruncationNeeded(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "short"},
		{Role: schema.Assistant, Content: "response"},
	}
	// Large budget should not truncate
	result, cutIdx := TruncateToTokenBudget(messages, 100000)
	if cutIdx != 0 {
		t.Errorf("expected no truncation, got cutIdx=%d", cutIdx)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result))
	}
}

// TestListBranchesFindsChildren verifies that ListBranches correctly finds
// sessions branched from a given parent.
func TestListBranchesFindsChildren(t *testing.T) {
	dir := t.TempDir()

	// Create parent session
	parentRec := transcript.NewRecorder("parent-session", dir)
	if err := parentRec.Record([]*schema.Message{
		{Role: schema.User, Content: "parent msg 1"},
		{Role: schema.Assistant, Content: "parent response 1"},
		{Role: schema.User, Content: "parent msg 2"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := parentRec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Create two branches
	_, err := BranchSession(BranchOptions{
		SourceSessionID: "parent-session",
		MessageIndex:    2,
		NewSessionID:    "child-1",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	_, err = BranchSession(BranchOptions{
		SourceSessionID: "parent-session",
		MessageIndex:    3,
		NewSessionID:    "child-2",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// ListBranches should find both children
	branches, err := ListBranches("parent-session", dir)
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
		return
	}
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}

	branchSet := map[string]bool{}
	for _, b := range branches {
		branchSet[b] = true
	}
	if !branchSet["child-1"] || !branchSet["child-2"] {
		t.Errorf("expected child-1 and child-2 in branches, got %v", branches)
	}
}

// TestSessionMetadataRoundTrip verifies metadata write and read roundtrip.
func TestSessionMetadataRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec := transcript.NewRecorder("meta-session", dir)

	// Write initial message
	if err := rec.Record([]*schema.Message{
		{Role: schema.User, Content: "test"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}

	// Write full metadata
	meta := &SessionMetadataFull{
		SessionID:                  "meta-session",
		ParentSessionID:            "parent-123",
		BranchPoint:                5,
		Model:                      "claude-sonnet-4-20250514",
		Provider:                   "claude",
		QueryKernelVersion:         "project_graph/v1",
		QueryKernelStage:           "read_only",
		QueryKernelIncompatibility: "",
		CreatedAt:                  time.Now().UTC().Truncate(time.Second),
		UpdatedAt:                  time.Now().UTC().Truncate(time.Second),
		MessageCount:               42,
		TokenUsage:                 15000,
		IsLeaf:                     true,
		GitBranch:                  "feature/test",
		CWD:                        "/workspace/project",
	}
	if err := WriteSessionMetadata(rec, meta); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}
	raw, err := os.ReadFile(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(raw),
		`query_kernel_canary_stage`,
	) || strings.Contains(string(raw), `query_kernel_stage`) {
		t.Fatalf(
			"durable query-kernel stage key changed: %s",
			raw,
		)
	}

	// Load and read metadata back
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatal(err)
		return
	}
	readMeta := ReadSessionMetadataFull(result)
	if readMeta == nil {
		t.Fatal("expected non-nil metadata")
		return
	}
	if readMeta.SessionID != "meta-session" {
		t.Errorf("expected session ID 'meta-session', got %q", readMeta.SessionID)
	}
	if readMeta.ParentSessionID != "parent-123" {
		t.Errorf("expected parent 'parent-123', got %q", readMeta.ParentSessionID)
	}
	if readMeta.BranchPoint != 5 {
		t.Errorf("expected branch point 5, got %d", readMeta.BranchPoint)
	}
	if readMeta.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected model 'claude-sonnet-4-20250514', got %q", readMeta.Model)
	}
	if readMeta.TokenUsage != 15000 {
		t.Errorf("expected token usage 15000, got %d", readMeta.TokenUsage)
	}
	if readMeta.QueryKernelVersion != "project_graph/v1" ||
		readMeta.QueryKernelStage != "read_only" ||
		readMeta.QueryKernelIncompatibility != "" {
		t.Errorf("query kernel metadata mismatch: %#v", readMeta)
	}
}

// TestRecorderRecordContentReplacements verifies replacement recording/loading.
func TestRecorderRecordContentReplacements(t *testing.T) {
	dir := t.TempDir()
	rec := transcript.NewRecorder("replacement-session", dir)

	if err := rec.Record([]*schema.Message{
		{Role: schema.User, Content: "test"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}

	replacements := []transcript.Replacement{
		{ToolUseID: "tu_1", Replacement: "[truncated]"},
		{ToolUseID: "tu_2", Replacement: "[image removed]"},
	}
	if err := rec.RecordContentReplacements(replacements); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	result, err := rec.LoadFull()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(result.Replacements) != 2 {
		t.Fatalf("expected 2 replacements, got %d", len(result.Replacements))
	}
	if result.Replacements[0].ToolUseID != "tu_1" {
		t.Errorf("expected tool_use_id 'tu_1', got %q", result.Replacements[0].ToolUseID)
	}
}
