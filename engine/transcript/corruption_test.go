package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// --- Corruption Recovery Tests ---

func TestLoadFull_HandlesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	// Create an empty file.
	if err := os.WriteFile(filepath.Join(sessionDir, "empty.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := NewRecorder("empty", sessionDir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull on empty file should not error: %v", err)
		return
	}
	if len(result.Messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result.Messages))
	}
	if len(result.Corruptions) != 0 {
		t.Fatalf("expected 0 corruptions, got %d", len(result.Corruptions))
	}
}

func TestLoadFull_HandlesTruncatedJSON(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Write a valid line followed by a truncated line (simulating crash mid-write).
	validEntry := recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      "user",
		Message:   &schema.Message{Role: schema.User, Content: "hello"},
	}
	validJSON, _ := json.Marshal(validEntry)
	content := string(validJSON) + "\n" + `{"timestamp":"2026-01-01T00:00:00Z","kind":"assistant","message":{"role":"ass` + "\n"

	if err := os.WriteFile(filepath.Join(sessionDir, "truncated.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := NewRecorder("truncated", sessionDir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull should not return hard error on truncated JSON: %v", err)
		return
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 valid message, got %d", len(result.Messages))
	}
	if result.Messages[0].Content != "hello" {
		t.Fatalf("unexpected message content: %q", result.Messages[0].Content)
	}
	if len(result.Corruptions) != 1 {
		t.Fatalf("expected 1 corruption, got %d", len(result.Corruptions))
	}
	if result.Corruptions[0].Line != 2 {
		t.Fatalf("expected corruption on line 2, got line %d", result.Corruptions[0].Line)
	}
}

func TestLoadFull_HandlesNullBytes(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Write valid lines with a null-byte garbage line in between.
	validEntry1 := recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      "user",
		Message:   &schema.Message{Role: schema.User, Content: "first"},
	}
	validEntry2 := recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      "assistant",
		Message:   &schema.Message{Role: schema.Assistant, Content: "second"},
	}
	json1, _ := json.Marshal(validEntry1)
	json2, _ := json.Marshal(validEntry2)
	content := string(json1) + "\n" + "\x00\x00\x00garbage\x00\n" + string(json2) + "\n"

	if err := os.WriteFile(filepath.Join(sessionDir, "nullbytes.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := NewRecorder("nullbytes", sessionDir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull should not error on null bytes: %v", err)
		return
	}
	if len(result.Messages) != 2 {
		t.Fatalf("expected 2 valid messages, got %d", len(result.Messages))
	}
	if len(result.Corruptions) != 1 {
		t.Fatalf("expected 1 corruption, got %d", len(result.Corruptions))
	}
	if result.Corruptions[0].Line != 2 {
		t.Fatalf("expected corruption on line 2, got line %d", result.Corruptions[0].Line)
	}
}

func TestLoadFull_HandlesInvalidJSONLines(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Write a mix of valid and invalid lines.
	validEntry := recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      "user",
		Message:   &schema.Message{Role: schema.User, Content: "good"},
	}
	validJSON, _ := json.Marshal(validEntry)
	content := string(validJSON) + "\n" +
		"not json at all\n" +
		"{invalid json}\n" +
		`{"timestamp":"bad","kind":"user","message":null}` + "\n" // invalid timestamp causes unmarshal error

	if err := os.WriteFile(filepath.Join(sessionDir, "invalid.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := NewRecorder("invalid", sessionDir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull should not error on invalid JSON lines: %v", err)
		return
	}
	// Only the first line has a valid message.
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 valid message, got %d", len(result.Messages))
	}
	// Lines 2, 3, and 4 are all corrupt (line 4 has invalid timestamp that fails unmarshal).
	if len(result.Corruptions) != 3 {
		t.Fatalf("expected 3 corruptions, got %d: %+v", len(result.Corruptions), result.Corruptions)
	}
}

func TestLoadFull_HandlesCompletelyCorruptFile(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	content := "this is not json\nneither is this\n\x00\x01\x02\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "corrupt.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := NewRecorder("corrupt", sessionDir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull should not panic/error even on fully corrupt file: %v", err)
		return
	}
	if len(result.Messages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result.Messages))
	}
	if len(result.Corruptions) != 3 {
		t.Fatalf("expected 3 corruptions, got %d", len(result.Corruptions))
	}
}

func TestLoadFull_RecoversMostMessagesFromPartialCorruption(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// 10 valid messages with 2 corrupt lines interspersed.
	var lines []string
	for i := 0; i < 10; i++ {
		entry := recordEntry{
			Timestamp: time.Now().UTC(),
			Kind:      "user",
			Message:   &schema.Message{Role: schema.User, Content: strings.Repeat("x", i+1)},
		}
		j, _ := json.Marshal(entry)
		lines = append(lines, string(j))
		if i == 3 || i == 7 {
			lines = append(lines, "CORRUPT LINE HERE")
		}
	}
	content := strings.Join(lines, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(sessionDir, "partial.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := NewRecorder("partial", sessionDir)
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if len(result.Messages) != 10 {
		t.Fatalf("expected 10 recovered messages, got %d", len(result.Messages))
	}
	if len(result.Corruptions) != 2 {
		t.Fatalf("expected 2 corruptions, got %d", len(result.Corruptions))
	}
}

// --- Branch Tests ---

func TestBranch_CreatesNewSessionFromPrefix(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")

	// Create a source session with 5 messages.
	rec := NewRecorder("source-session", sessionDir)
	for i := 0; i < 5; i++ {
		role := schema.User
		if i%2 == 1 {
			role = schema.Assistant
		}
		if err := rec.Record([]*schema.Message{{Role: role, Content: strings.Repeat("msg", i+1)}}, i%2 == 1); err != nil {
			t.Fatal(err)
			return
		}
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Branch at message 3 (first 3 messages).
	newRec, err := rec.Branch("branch-session", 3)
	if err != nil {
		t.Fatalf("branch failed: %v", err)
		return
	}

	// Load the branch and verify.
	branchResult, err := newRec.LoadFull()
	if err != nil {
		t.Fatalf("load branch: %v", err)
		return
	}
	if len(branchResult.Messages) != 3 {
		t.Fatalf("expected 3 messages in branch, got %d", len(branchResult.Messages))
	}

	// Check parent metadata was recorded.
	foundParent := false
	foundBranchPoint := false
	for _, meta := range branchResult.Metadata {
		if meta.Key == "parent_session_id" && meta.Value == "source-session" {
			foundParent = true
		}
		if meta.Key == "branch_point" && meta.Value == "3" {
			foundBranchPoint = true
		}
	}
	if !foundParent {
		t.Fatal("parent_session_id metadata not found in branch")
	}
	if !foundBranchPoint {
		t.Fatal("branch_point metadata not found in branch")
	}

	// Verify original session is unchanged.
	origResult, err := rec.LoadFull()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(origResult.Messages) != 5 {
		t.Fatalf("original session should still have 5 messages, got %d", len(origResult.Messages))
	}
}

func TestBranch_MessageCountExceedsAvailable(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")

	// Create a source session with 3 messages.
	rec := NewRecorder("small-session", sessionDir)
	for i := 0; i < 3; i++ {
		if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "x"}}, false); err != nil {
			t.Fatal(err)
			return
		}
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Branch asking for 100 messages (more than available).
	newRec, err := rec.Branch("over-branch", 100)
	if err != nil {
		t.Fatalf("branch should succeed with clamped count: %v", err)
		return
	}

	result, err := newRec.LoadFull()
	if err != nil {
		t.Fatal(err)
		return
	}
	// Should copy all 3 available messages.
	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages (clamped), got %d", len(result.Messages))
	}
}

func TestBranch_EmptySourceFails(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Create an empty session file.
	if err := os.WriteFile(filepath.Join(sessionDir, "empty-src.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	rec := NewRecorder("empty-src", sessionDir)
	_, err := rec.Branch("new-branch", 5)
	if err == nil {
		t.Fatal("expected error branching from empty session")
		return
	}
}

func TestBranch_InvalidInputs(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")

	rec := NewRecorder("some-session", sessionDir)

	// Empty new session ID.
	_, err := rec.Branch("", 5)
	if err == nil {
		t.Fatal("expected error for empty new session ID")
		return
	}

	// Negative message count.
	_, err = rec.Branch("new-id", -1)
	if err == nil {
		t.Fatal("expected error for negative message count")
		return
	}
}

func TestBranch_AtomicOnDisk(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")

	// Create source with messages.
	rec := NewRecorder("atomic-src", sessionDir)
	if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "data"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Branch and verify no .tmp file remains.
	_, err := rec.Branch("atomic-dest", 1)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Check no .tmp file exists.
	entries, _ := os.ReadDir(sessionDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// --- isLikelyJSON helper tests ---

func TestIsLikelyJSON(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{`{"key":"value"}`, true},
		{`  {"key":"value"}`, true},
		{`[1,2,3]`, true},
		{`not json`, false},
		{"\x00\x00", false},
		{"", false},
		{" \t\n{", true},
		{"123", false},
	}
	for _, tt := range tests {
		got := isLikelyJSON([]byte(tt.input))
		if got != tt.expected {
			t.Errorf("isLikelyJSON(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}
