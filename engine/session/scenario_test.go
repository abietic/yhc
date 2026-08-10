package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

// --- Cross-entrypoint Consistency Tests ---
// These tests verify that session operations produce identical results
// regardless of the access pattern (direct API, resume, branch).

func TestConsistency_ListingMatchesResume(t *testing.T) {
	dir := t.TempDir()

	// Create a session with rich metadata.
	rec := transcript.NewRecorder("consistency-session", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "start a project", Extra: map[string]any{
			"timestamp": "2026-06-10T10:00:00Z",
			"model":     "claude-sonnet-4-20250514",
			"provider":  "claude",
		}},
		{Role: schema.Assistant, Content: "I'll help you start a project."},
	}
	if err := rec.Record(msgs, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("git_branch", "main"); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("cwd", "/workspace/project"); err != nil {
		t.Fatal(err)
		return
	}
	metaFull := &SessionMetadataFull{
		SessionID: "consistency-session",
		Model:     "claude-sonnet-4-20250514",
		Provider:  "claude",
		GitBranch: "main",
		CWD:       "/workspace/project",
		IsLeaf:    true,
	}
	if err := WriteSessionMetadata(rec, metaFull); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Access via listing.
	sessions, err := ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		t.Fatalf("list: %v", err)
		return
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	listedSession := sessions[0]

	// Access via resume.
	resumed, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:  "consistency-session",
		SessionDir: dir,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
		return
	}

	// Cross-check: session ID matches.
	if listedSession.SessionID != resumed.SessionID {
		t.Fatalf("session ID mismatch: list=%q resume=%q",
			listedSession.SessionID, resumed.SessionID)
	}

	// Cross-check: git branch matches.
	if listedSession.GitBranch != resumed.Metadata.GitBranch {
		t.Fatalf("git branch mismatch: list=%q resume=%q",
			listedSession.GitBranch, resumed.Metadata.GitBranch)
	}

	// Cross-check: CWD matches.
	if listedSession.CWD != resumed.Metadata.CWD {
		t.Fatalf("CWD mismatch: list=%q resume=%q",
			listedSession.CWD, resumed.Metadata.CWD)
	}
}

func TestConsistency_BranchPreservesParentMetadata(t *testing.T) {
	dir := t.TempDir()

	// Create parent session.
	parentRec := transcript.NewRecorder("parent-consistent", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi there"},
		{Role: schema.User, Content: "continue"},
		{Role: schema.Assistant, Content: "continuing"},
	}
	if err := parentRec.Record(msgs, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := parentRec.RecordMetadata("git_branch", "feature/x"); err != nil {
		t.Fatal(err)
		return
	}
	if err := parentRec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Branch from parent at message 2.
	branchResult, err := BranchSession(BranchOptions{
		SourceSessionID: "parent-consistent",
		MessageIndex:    2,
		NewSessionID:    "child-consistent",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Resume the branched session.
	resumed, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:  branchResult.NewSessionID,
		SessionDir: dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Verify lineage is consistent.
	if resumed.Metadata.ParentSessionID != "parent-consistent" {
		t.Fatalf("parent mismatch: %q", resumed.Metadata.ParentSessionID)
	}
	if resumed.Metadata.BranchPoint != 2 {
		t.Fatalf("branch point: %d", resumed.Metadata.BranchPoint)
	}
	if resumed.Metadata.MessageCount != 2 {
		t.Fatalf("message count: %d", resumed.Metadata.MessageCount)
	}

	// Verify listing also shows the child.
	sessions, err := ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Find child in listing.
	var childListed *SessionInfo
	for i := range sessions {
		if sessions[i].SessionID == "child-consistent" {
			childListed = &sessions[i]
			break
		}
	}
	if childListed == nil {
		t.Fatal("child session not found in listing")
		return
	}
	if childListed.ParentSessionID != "parent-consistent" {
		t.Fatalf("listed parent mismatch: %q", childListed.ParentSessionID)
	}
}

func TestConsistency_ExportMatchesResume(t *testing.T) {
	dir := t.TempDir()

	// Create a session.
	rec := transcript.NewRecorder("export-consistent", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "question one"},
		{Role: schema.Assistant, Content: "answer one"},
		{Role: schema.User, Content: "question two"},
		{Role: schema.Assistant, Content: "answer two"},
	}
	if err := rec.Record(msgs, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Resume.
	resumed, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:  "export-consistent",
		SessionDir: dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Export as JSON.
	exportResult, err := ExportSession(ExportOptions{
		SessionID:        "export-consistent",
		Dir:              dir,
		Format:           ExportJSON,
		IncludeToolCalls: true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	var exported ExportedSession
	if err := json.Unmarshal([]byte(exportResult.Content), &exported); err != nil {
		t.Fatal(err)
		return
	}

	// Message count must match.
	if len(exported.Messages) != len(resumed.Messages) {
		t.Fatalf("message count: export=%d resume=%d",
			len(exported.Messages), len(resumed.Messages))
	}

	// Content must match.
	for i, msg := range resumed.Messages {
		if exported.Messages[i].Content != msg.Content {
			t.Fatalf("message[%d] content: export=%q resume=%q",
				i, exported.Messages[i].Content, msg.Content)
		}
		if exported.Messages[i].Role != string(msg.Role) {
			t.Fatalf("message[%d] role: export=%q resume=%q",
				i, exported.Messages[i].Role, msg.Role)
		}
	}
}

func TestConsistency_FilteredListingIsDeterministic(t *testing.T) {
	dir := t.TempDir()

	// Create multiple sessions.
	for _, id := range []string{"alpha", "beta", "gamma"} {
		rec := transcript.NewRecorder(id, dir)
		if err := rec.Record([]*schema.Message{
			{Role: schema.User, Content: "msg from " + id},
		}, false); err != nil {
			t.Fatal(err)
			return
		}
		if err := rec.Flush(); err != nil {
			t.Fatal(err)
			return
		}
	}

	// Run listing multiple times with the same filter.
	var results [][]SessionInfo
	for i := 0; i < 3; i++ {
		sessions, err := ListSessions(ListOptions{
			TranscriptDir: dir,
			Filter:        &ListFilter{Search: "msg from"},
		})
		if err != nil {
			t.Fatal(err)
			return
		}
		results = append(results, sessions)
	}

	// All runs must produce identical results.
	for i := 1; i < len(results); i++ {
		if len(results[i]) != len(results[0]) {
			t.Fatalf("run %d has %d results vs run 0 has %d",
				i, len(results[i]), len(results[0]))
		}
		for j := range results[0] {
			if results[i][j].SessionID != results[0][j].SessionID {
				t.Fatalf("run %d result[%d] differs: %q vs %q",
					i, j, results[i][j].SessionID, results[0][j].SessionID)
			}
		}
	}
}

// --- Full Lifecycle Scenario Tests ---
// These test complete session lifecycles: create -> use -> branch -> resume -> delete.

func TestScenario_FullLifecycle_CreateResumeBranchDelete(t *testing.T) {
	dir := t.TempDir()

	// Phase 1: Create a session.
	rec := transcript.NewRecorder("lifecycle-root", dir)
	if err := rec.Record([]*schema.Message{
		{Role: schema.User, Content: "Start coding session"},
		{Role: schema.Assistant, Content: "Ready to help!"},
		{Role: schema.User, Content: "Read main.go"},
		{Role: schema.Assistant, Content: "Here's the content of main.go."},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("git_branch", "main"); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("cwd", "/project"); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Phase 2: Resume the session.
	resumed, err := ResumeSession(context.Background(), ResumeOptions{
		SessionID:        "lifecycle-root",
		SessionDir:       dir,
		ValidateMessages: true,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
		return
	}
	if len(resumed.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(resumed.Messages))
	}
	if len(resumed.Warnings) != 0 {
		t.Fatalf("unexpected validation warnings: %v", resumed.Warnings)
	}

	// Phase 3: Branch from the session at message 2.
	branchResult, err := BranchSession(BranchOptions{
		SourceSessionID: "lifecycle-root",
		MessageIndex:    2,
		NewSessionID:    "lifecycle-branch",
		Dir:             dir,
	})
	if err != nil {
		t.Fatalf("branch: %v", err)
		return
	}
	if branchResult.MessagesCopied != 2 {
		t.Fatalf("messages copied: %d", branchResult.MessagesCopied)
	}

	// Verify listing shows both sessions.
	sessions, err := ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Phase 4: Verify lineage.
	lineage, err := GetSessionLineage("lifecycle-root", dir)
	if err != nil {
		t.Fatal(err)
		return
	}
	if lineage.IsLeaf {
		t.Fatal("root should not be a leaf (has child)")
	}
	if len(lineage.Children) != 1 || lineage.Children[0] != "lifecycle-branch" {
		t.Fatalf("unexpected children: %v", lineage.Children)
	}

	// Phase 5: Delete the branch.
	deleteResult, err := DeleteSession(DeleteOptions{
		SessionID: "lifecycle-branch",
		Dir:       dir,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
		return
	}
	if !deleteResult.TranscriptRemoved {
		t.Fatal("transcript should be removed")
	}
	if !deleteResult.ParentUpdated {
		t.Fatal("parent should be updated (became leaf)")
	}

	// Phase 6: Verify root is now a leaf again.
	lineage, err = GetSessionLineage("lifecycle-root", dir)
	if err != nil {
		t.Fatal(err)
		return
	}
	if !lineage.IsLeaf {
		t.Fatal("root should be a leaf after child deletion")
	}

	// Phase 7: Verify listing shows only root.
	sessions, err = ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(sessions) != 1 || sessions[0].SessionID != "lifecycle-root" {
		t.Fatalf("expected only lifecycle-root, got %v", sessions)
	}
}

func TestScenario_MultiBranch_DeleteOne(t *testing.T) {
	dir := t.TempDir()

	// Create parent with messages.
	rec := transcript.NewRecorder("multi-parent", dir)
	if err := rec.Record([]*schema.Message{
		{Role: schema.User, Content: "msg1"},
		{Role: schema.Assistant, Content: "reply1"},
		{Role: schema.User, Content: "msg2"},
		{Role: schema.Assistant, Content: "reply2"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Create two branches.
	_, err := BranchSession(BranchOptions{
		SourceSessionID: "multi-parent",
		MessageIndex:    2,
		NewSessionID:    "branch-a",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	_, err = BranchSession(BranchOptions{
		SourceSessionID: "multi-parent",
		MessageIndex:    4,
		NewSessionID:    "branch-b",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Verify parent has 2 children.
	branches, _ := ListBranches("multi-parent", dir)
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(branches))
	}

	// Delete one branch.
	result, err := DeleteSession(DeleteOptions{SessionID: "branch-a", Dir: dir})
	if err != nil {
		t.Fatal(err)
		return
	}
	// Parent should NOT be updated because branch-b still exists.
	if result.ParentUpdated {
		t.Fatal("parent should not be updated (still has branch-b)")
	}

	// Verify parent still has one child.
	branches, _ = ListBranches("multi-parent", dir)
	if len(branches) != 1 || branches[0] != "branch-b" {
		t.Fatalf("expected [branch-b], got %v", branches)
	}
}

func TestScenario_ExportAfterBranch(t *testing.T) {
	dir := t.TempDir()

	// Create parent.
	rec := transcript.NewRecorder("export-parent", dir)
	if err := rec.Record([]*schema.Message{
		{Role: schema.User, Content: "original question"},
		{Role: schema.Assistant, Content: "original answer"},
		{Role: schema.User, Content: "follow up"},
		{Role: schema.Assistant, Content: "follow up answer"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Branch at message 2.
	_, err := BranchSession(BranchOptions{
		SourceSessionID: "export-parent",
		MessageIndex:    2,
		NewSessionID:    "export-child",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Export the branch.
	result, err := ExportSession(ExportOptions{
		SessionID:        "export-child",
		Dir:              dir,
		Format:           ExportJSON,
		IncludeToolCalls: true,
		IncludeMetadata:  true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	var exported ExportedSession
	if err := json.Unmarshal([]byte(result.Content), &exported); err != nil {
		t.Fatal(err)
		return
	}

	// Branch should have 2 messages.
	if len(exported.Messages) != 2 {
		t.Fatalf("expected 2 messages in branch export, got %d", len(exported.Messages))
	}
	if exported.Messages[0].Content != "original question" {
		t.Fatalf("unexpected first message: %q", exported.Messages[0].Content)
	}

	// Metadata should show parent.
	if exported.Metadata == nil {
		t.Fatal("metadata should be present")
		return
	}
	if exported.Metadata.ParentSessionID != "export-parent" {
		t.Fatalf("parent: %q", exported.Metadata.ParentSessionID)
	}
	if exported.Metadata.BranchPoint != 2 {
		t.Fatalf("branch point: %d", exported.Metadata.BranchPoint)
	}
}

// --- Listing Filter Tests ---

func TestListSessions_FilterBySearch(t *testing.T) {
	dir := t.TempDir()

	// Create sessions with different content.
	createSimpleSession(t, dir, "search-match", "implement dark mode feature")
	createSimpleSession(t, dir, "search-nomatch", "fix a typo in readme")

	sessions, err := ListSessions(ListOptions{
		TranscriptDir: dir,
		Filter:        &ListFilter{Search: "dark mode"},
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	if len(sessions) != 1 || sessions[0].SessionID != "search-match" {
		t.Fatalf("expected only search-match, got %v", sessionIDs(sessions))
	}
}

func TestListSessions_FilterBySearch_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()

	createSimpleSession(t, dir, "upper-case", "Implement DARK MODE")
	createSimpleSession(t, dir, "lower-case", "implement dark mode")

	sessions, err := ListSessions(ListOptions{
		TranscriptDir: dir,
		Filter:        &ListFilter{Search: "Dark Mode"},
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	if len(sessions) != 2 {
		t.Fatalf("expected 2 matches (case-insensitive), got %d", len(sessions))
	}
}

func TestListSessions_FilterByGitBranch(t *testing.T) {
	dir := t.TempDir()

	createSessionWithGitBranch(t, dir, "on-main", "main")
	createSessionWithGitBranch(t, dir, "on-feature", "feature/dark-mode")

	sessions, err := ListSessions(ListOptions{
		TranscriptDir: dir,
		Filter:        &ListFilter{GitBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	if len(sessions) != 1 || sessions[0].SessionID != "on-main" {
		t.Fatalf("expected on-main, got %v", sessionIDs(sessions))
	}
}

func TestListSessions_FilterByTimeRange(t *testing.T) {
	dir := t.TempDir()

	// Create two sessions.
	createSimpleSession(t, dir, "recent-session", "recent work")
	createSimpleSession(t, dir, "old-session", "old work")

	// Make one session old.
	oldPath := filepath.Join(dir, "old-session.jsonl")
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
		return
	}

	// Filter: only sessions after 7 days ago.
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	sessions, err := ListSessions(ListOptions{
		TranscriptDir: dir,
		Filter:        &ListFilter{After: cutoff},
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	if len(sessions) != 1 || sessions[0].SessionID != "recent-session" {
		t.Fatalf("expected recent-session, got %v", sessionIDs(sessions))
	}
}

func TestListSessions_SortOptions(t *testing.T) {
	dir := t.TempDir()

	// Create sessions with different sizes and times.
	createSimpleSession(t, dir, "small-session", "x")
	createSimpleSession(t, dir, "large-session", strings.Repeat("long content ", 100))

	// Make small-session older.
	smallPath := filepath.Join(dir, "small-session.jsonl")
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(smallPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
		return
	}

	// Sort newest first (default).
	sessions, err := ListSessions(ListOptions{
		TranscriptDir: dir,
		Sort:          SortNewestFirst,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(sessions) >= 2 && sessions[0].SessionID != "large-session" {
		t.Fatalf("newest first: expected large-session first, got %s", sessions[0].SessionID)
	}

	// Sort oldest first.
	sessions, err = ListSessions(ListOptions{
		TranscriptDir: dir,
		Sort:          SortOldestFirst,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(sessions) >= 2 && sessions[0].SessionID != "small-session" {
		t.Fatalf("oldest first: expected small-session first, got %s", sessions[0].SessionID)
	}

	// Sort by most messages (using file size as proxy).
	sessions, err = ListSessions(ListOptions{
		TranscriptDir: dir,
		Sort:          SortMostMessages,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(sessions) >= 2 && sessions[0].SessionID != "large-session" {
		t.Fatalf("most messages: expected large-session first, got %s", sessions[0].SessionID)
	}
}

func TestListSessions_Pagination(t *testing.T) {
	dir := t.TempDir()

	// Create 5 sessions.
	for i := 0; i < 5; i++ {
		id := "page-session-" + string(rune('a'+i))
		createSimpleSession(t, dir, id, "message "+string(rune('a'+i)))
	}

	// Page 1: first 2.
	result, err := ListSessionsWithResult(ListOptions{
		TranscriptDir: dir,
		Limit:         2,
		Offset:        0,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if result.Total != 5 {
		t.Fatalf("total: %d", result.Total)
	}
	if len(result.Sessions) != 2 {
		t.Fatalf("page 1 size: %d", len(result.Sessions))
	}
	if !result.HasMore {
		t.Fatal("should have more")
	}

	// Page 2: next 2.
	result2, err := ListSessionsWithResult(ListOptions{
		TranscriptDir: dir,
		Limit:         2,
		Offset:        2,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(result2.Sessions) != 2 {
		t.Fatalf("page 2 size: %d", len(result2.Sessions))
	}
	if !result2.HasMore {
		t.Fatal("page 2 should have more")
	}

	// Page 3: last 1.
	result3, err := ListSessionsWithResult(ListOptions{
		TranscriptDir: dir,
		Limit:         2,
		Offset:        4,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(result3.Sessions) != 1 {
		t.Fatalf("page 3 size: %d", len(result3.Sessions))
	}
	if result3.HasMore {
		t.Fatal("page 3 should not have more")
	}
}

func TestListSessions_FilterCombination(t *testing.T) {
	dir := t.TempDir()

	// Create sessions with various attributes.
	createSessionWithGitBranch(t, dir, "match-both", "feature/x")
	createSessionWithGitBranch(t, dir, "match-branch-only", "feature/x")
	createSimpleSession(t, dir, "match-search-only", "special keyword feature/x")

	// Overwrite match-both with content that includes the search term.
	rec := transcript.NewRecorder("match-both", dir)
	if err := rec.Replace([]*schema.Message{
		{Role: schema.User, Content: "special keyword"},
	}); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("git_branch", "feature/x"); err != nil {
		t.Fatal(err)
		return
	}
	// Write gitBranch in lite-readable format.
	appendLiteMetadata(t, dir, "match-both", "feature/x")
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Filter by both git branch AND search.
	sessions, err := ListSessions(ListOptions{
		TranscriptDir: dir,
		Filter: &ListFilter{
			GitBranch: "feature/x",
			Search:    "special keyword",
		},
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Only match-both should match both criteria.
	if len(sessions) != 1 || sessions[0].SessionID != "match-both" {
		t.Fatalf("expected only match-both, got %v", sessionIDs(sessions))
	}
}

// --- Helper functions for scenario tests ---

func createSimpleSession(t *testing.T, dir, sessionID, content string) {
	t.Helper()
	rec := transcript.NewRecorder(sessionID, dir)
	if err := rec.Record([]*schema.Message{
		{Role: schema.User, Content: content},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}
}

func appendLiteMetadata(t *testing.T, dir, sessionID, gitBranch string) {
	t.Helper()
	entry := map[string]interface{}{
		"type":      "metadata",
		"gitBranch": gitBranch,
	}
	data, _ := json.Marshal(entry)
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}

func sessionIDs(sessions []SessionInfo) []string {
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.SessionID)
	}
	return ids
}
