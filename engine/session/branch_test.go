package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

func TestBranchSession_Success(t *testing.T) {
	dir := t.TempDir()

	// Create a source session with messages.
	srcRec := transcript.NewRecorder("src-session-abc", dir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi there"},
		{Role: schema.User, Content: "how are you"},
		{Role: schema.Assistant, Content: "good"},
		{Role: schema.User, Content: "bye"},
	}
	if err := srcRec.Record(messages[:2], false); err != nil {
		t.Fatal(err)
		return
	}
	if err := srcRec.Record(messages[2:4], true); err != nil {
		t.Fatal(err)
		return
	}
	if err := srcRec.Record(messages[4:], false); err != nil {
		t.Fatal(err)
		return
	}
	if err := srcRec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Branch at message 3.
	result, err := BranchSession(BranchOptions{
		SourceSessionID: "src-session-abc",
		MessageIndex:    3,
		Dir:             dir,
	})
	if err != nil {
		t.Fatalf("branch failed: %v", err)
		return
	}

	if result.ParentSessionID != "src-session-abc" {
		t.Fatalf("unexpected parent: %s", result.ParentSessionID)
	}
	if result.MessagesCopied != 3 {
		t.Fatalf("expected 3 copied, got %d", result.MessagesCopied)
	}
	if result.NewSessionID == "" {
		t.Fatal("new session ID should not be empty")
	}

	// Verify the branch file exists and has correct content.
	branchRec := transcript.NewRecorder(result.NewSessionID, dir)
	branchResult, err := branchRec.LoadFull()
	if err != nil {
		t.Fatalf("load branch: %v", err)
		return
	}
	if len(branchResult.Messages) != 3 {
		t.Fatalf("expected 3 messages in branch, got %d", len(branchResult.Messages))
	}
	if branchResult.Messages[0].Content != "hello" {
		t.Fatalf("first message should be 'hello', got %q", branchResult.Messages[0].Content)
	}
}

func TestBranchSession_CustomNewID(t *testing.T) {
	dir := t.TempDir()

	srcRec := transcript.NewRecorder("src-custom", dir)
	if err := srcRec.Record([]*schema.Message{{Role: schema.User, Content: "test"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := srcRec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	result, err := BranchSession(BranchOptions{
		SourceSessionID: "src-custom",
		MessageIndex:    1,
		NewSessionID:    "my-custom-branch-id",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if result.NewSessionID != "my-custom-branch-id" {
		t.Fatalf("expected custom ID, got %q", result.NewSessionID)
	}
}

func TestBranchSessionCommitsFullChildMetadataAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sourceID := "fork-metadata-source"
	source := transcript.NewRecorder(sourceID, dir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}
	if err := source.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		[]transcript.Replacement{{ToolUseID: "tool-1", Replacement: "preview"}},
		map[string]transcript.FileState{
			"/tmp/source.go": {Path: "/tmp/source.go", WasRead: true},
		},
	); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 7, 20, 7, 8, 9, 0, time.UTC)
	sourcePlanPath := filepath.Join(dir, "fork-metadata-source.md")
	childPlanPath := filepath.Join(dir, "fork-metadata-child.md")
	sourceMetadata := &SessionMetadataFull{
		SessionID:                  sourceID,
		ThreadID:                   "source-thread",
		AgentID:                    "source-agent",
		AgentName:                  "source name",
		AgentRole:                  "worker",
		Status:                     "running",
		Model:                      "model-a",
		Provider:                   "provider-a",
		PermissionMode:             "acceptEdits",
		QueryKernelVersion:         "project_graph/v1",
		QueryKernelStage:           "read_only",
		QueryKernelIncompatibility: "none",
		PlanState: &PersistedPlanState{
			Version: 1, Phase: "awaiting_approval",
			PlanFileIdentity:  sourcePlanPath,
			ApprovalRequestID: "source-approval",
			Revision:          3,
		},
		GoalState: &PersistedGoalState{
			Version:           PersistedGoalStateVersion,
			GoalID:            "source-goal",
			Objective:         "must remain on source root",
			ObjectiveRevision: 1,
			Status:            "paused",
			Revision:          1,
			TokenBudget:       func() *uint64 { value := uint64(10); return &value }(),
			CreatedAt:         fixed.Add(-time.Hour),
			UpdatedAt:         fixed.Add(-time.Minute),
		},
		WorktreePath:      "/tmp/source-worktree",
		WorktreeBranch:    "source-branch",
		AdditionalDirs:    []string{"/tmp/additional"},
		AgentIDs:          []string{"child-agent"},
		PendingRequestIDs: []string{"pending"},
		RuntimeRevision:   42,
		CreatedAt:         fixed.Add(-time.Hour),
		UpdatedAt:         fixed.Add(-time.Minute),
		MessageCount:      len(messages),
		TokenUsage:        123,
		IsLeaf:            false,
		CWD:               "/tmp/project",
	}
	if err := WriteSessionMetadata(source, sourceMetadata); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(source.Path())
	if err != nil {
		t.Fatal(err)
	}
	opts := BranchOptions{
		SourceSessionID:  sourceID,
		MessageIndex:     len(messages),
		NewSessionID:     "fork-metadata-child",
		Dir:              dir,
		BranchName:       "review",
		OperationID:      "operation-1",
		Metadata:         sourceMetadata,
		PlanFileIdentity: childPlanPath,
		Clock:            func() time.Time { return fixed },
	}
	result, err := BranchSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	child, err := transcript.NewRecorder(result.NewSessionID, dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := ReadSessionMetadataFull(child)
	if metadata == nil {
		t.Fatal("child is missing full session metadata")
	}
	if metadata.SessionID != result.NewSessionID ||
		metadata.ParentSessionID != sourceID ||
		metadata.ParentThreadID != "source-thread" ||
		metadata.ParentAgentID != "source-agent" ||
		metadata.ThreadID != result.NewSessionID ||
		metadata.AgentID != "" ||
		metadata.Status != "idle" ||
		metadata.PermissionMode != "acceptEdits" ||
		metadata.QueryKernelVersion != "project_graph/v1" ||
		metadata.PlanState == nil ||
		metadata.PlanState.Phase != "active" ||
		metadata.PlanState.PlanFileIdentity != childPlanPath ||
		metadata.PlanState.ApprovalRequestID != "" ||
		metadata.PlanState.Revision != 4 ||
		metadata.GoalState != nil ||
		metadata.WorktreePath != "" ||
		metadata.WorktreeBranch != "" ||
		len(metadata.AgentIDs) != 0 ||
		len(metadata.PendingRequestIDs) != 0 ||
		metadata.RuntimeRevision != 0 ||
		metadata.MessageCount != len(messages) ||
		metadata.TokenUsage != sourceMetadata.TokenUsage {
		t.Fatalf("child full metadata = %#v", metadata)
	}
	if len(child.Replacements) != 1 || len(child.FileSnapshots) != 1 {
		t.Fatalf(
			"child auxiliary state = replacements %#v files %#v",
			child.Replacements,
			child.FileSnapshots,
		)
	}
	reused, err := BranchSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.NewSessionID != result.NewSessionID {
		t.Fatalf("idempotent retry = %#v", reused)
	}
	childBeforeConflict, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := opts
	conflicting.OperationID = "operation-2"
	if _, err := BranchSession(conflicting); err == nil {
		t.Fatal("conflicting operation reused an existing child")
	}
	childAfterConflict, err := os.ReadFile(result.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(childAfterConflict) != string(childBeforeConflict) {
		t.Fatal("conflicting retry mutated committed child")
	}
	sourceAfter, err := os.ReadFile(source.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("branch mutated source transcript")
	}
}

func TestBranchSession_SourceNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := BranchSession(BranchOptions{
		SourceSessionID: "nonexistent",
		MessageIndex:    1,
		Dir:             dir,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent source")
		return
	}
}

func TestBranchSession_ValidationErrors(t *testing.T) {
	dir := t.TempDir()

	// Empty source ID.
	_, err := BranchSession(BranchOptions{MessageIndex: 1, Dir: dir})
	if err == nil {
		t.Fatal("expected error for empty source ID")
		return
	}

	// Non-positive message index.
	_, err = BranchSession(BranchOptions{SourceSessionID: "x", MessageIndex: 0, Dir: dir})
	if err == nil {
		t.Fatal("expected error for zero message index")
		return
	}
}

func TestListBranches_FindsChildren(t *testing.T) {
	dir := t.TempDir()

	// Create a parent session.
	parentRec := transcript.NewRecorder("parent-id", dir)
	if err := parentRec.Record([]*schema.Message{{Role: schema.User, Content: "parent msg"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := parentRec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Create two branch sessions.
	_, err := parentRec.Branch("child-1", 1)
	if err != nil {
		t.Fatal(err)
		return
	}
	_, err = parentRec.Branch("child-2", 1)
	if err != nil {
		t.Fatal(err)
		return
	}

	// List branches.
	branches, err := ListBranches("parent-id", dir)
	if err != nil {
		t.Fatalf("list branches: %v", err)
		return
	}
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d: %v", len(branches), branches)
	}

	// Both children should be found.
	found := make(map[string]bool)
	for _, b := range branches {
		found[b] = true
	}
	if !found["child-1"] || !found["child-2"] {
		t.Fatalf("missing expected branches: %v", branches)
	}
}

func TestGetSessionLineage(t *testing.T) {
	dir := t.TempDir()

	// Create parent and branch.
	parentRec := transcript.NewRecorder("lineage-parent", dir)
	if err := parentRec.Record([]*schema.Message{
		{Role: schema.User, Content: "msg1"},
		{Role: schema.Assistant, Content: "msg2"},
		{Role: schema.User, Content: "msg3"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := parentRec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	_, err := parentRec.Branch("lineage-child", 2)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Check child lineage.
	childLineage, err := GetSessionLineage("lineage-child", dir)
	if err != nil {
		t.Fatalf("get child lineage: %v", err)
		return
	}
	if childLineage.ParentSessionID != "lineage-parent" {
		t.Fatalf("expected parent 'lineage-parent', got %q", childLineage.ParentSessionID)
	}
	if childLineage.BranchPoint != 2 {
		t.Fatalf("expected branch point 2, got %d", childLineage.BranchPoint)
	}
	if !childLineage.IsLeaf {
		t.Fatal("child should be a leaf")
	}

	// Check parent lineage.
	parentLineage, err := GetSessionLineage("lineage-parent", dir)
	if err != nil {
		t.Fatalf("get parent lineage: %v", err)
		return
	}
	if parentLineage.ParentSessionID != "" {
		t.Fatalf("parent should have no parent, got %q", parentLineage.ParentSessionID)
	}
	if parentLineage.IsLeaf {
		t.Fatal("parent should NOT be a leaf (it has a child)")
	}
	if len(parentLineage.Children) != 1 || parentLineage.Children[0] != "lineage-child" {
		t.Fatalf("expected [lineage-child], got %v", parentLineage.Children)
	}
}

// --- Metadata Fidelity Tests ---

func TestSessionMetadataFull_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	rec := transcript.NewRecorder("meta-session", dir)

	meta := &SessionMetadataFull{
		SessionID:       "meta-session",
		ParentSessionID: "parent-abc",
		BranchPoint:     5,
		Model:           "claude-sonnet-4-20250514",
		Provider:        "claude",
		CreatedAt:       time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 6, 11, 10, 30, 0, 0, time.UTC),
		MessageCount:    42,
		TokenUsage:      15000,
		IsLeaf:          true,
		GitBranch:       "feature/session-hardening",
		CWD:             "/workspace/project",
	}

	if err := WriteSessionMetadata(rec, meta); err != nil {
		t.Fatalf("write metadata: %v", err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Load and verify.
	result, err := rec.LoadFull()
	if err != nil {
		t.Fatalf("load: %v", err)
		return
	}

	loaded := ReadSessionMetadataFull(result)
	if loaded == nil {
		t.Fatal("expected metadata to be loaded")
		return
	}
	if loaded.SessionID != "meta-session" {
		t.Fatalf("session ID: %q", loaded.SessionID)
	}
	if loaded.ParentSessionID != "parent-abc" {
		t.Fatalf("parent: %q", loaded.ParentSessionID)
	}
	if loaded.BranchPoint != 5 {
		t.Fatalf("branch point: %d", loaded.BranchPoint)
	}
	if loaded.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("model: %q", loaded.Model)
	}
	if loaded.Provider != "claude" {
		t.Fatalf("provider: %q", loaded.Provider)
	}
	if loaded.MessageCount != 42 {
		t.Fatalf("message count: %d", loaded.MessageCount)
	}
	if loaded.TokenUsage != 15000 {
		t.Fatalf("token usage: %d", loaded.TokenUsage)
	}
	if !loaded.IsLeaf {
		t.Fatal("expected IsLeaf to be true")
	}
	if loaded.GitBranch != "feature/session-hardening" {
		t.Fatalf("git branch: %q", loaded.GitBranch)
	}
}

func TestSessionMetadataFull_LastWinsSemantics(t *testing.T) {
	dir := t.TempDir()
	rec := transcript.NewRecorder("last-wins", dir)

	// Write first version.
	meta1 := &SessionMetadataFull{
		SessionID:    "last-wins",
		Model:        "old-model",
		MessageCount: 10,
	}
	if err := WriteSessionMetadata(rec, meta1); err != nil {
		t.Fatal(err)
		return
	}

	// Write updated version.
	meta2 := &SessionMetadataFull{
		SessionID:    "last-wins",
		Model:        "new-model",
		MessageCount: 20,
	}
	if err := WriteSessionMetadata(rec, meta2); err != nil {
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

	loaded := ReadSessionMetadataFull(result)
	if loaded == nil {
		t.Fatal("expected metadata")
		return
	}
	// Should see the second write's values.
	if loaded.Model != "new-model" {
		t.Fatalf("expected 'new-model', got %q", loaded.Model)
	}
	if loaded.MessageCount != 20 {
		t.Fatalf("expected 20, got %d", loaded.MessageCount)
	}
}

func TestSessionMetadataFull_NilInputs(t *testing.T) {
	// WriteSessionMetadata with nil should not panic.
	if err := WriteSessionMetadata(nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	// ReadSessionMetadataFull with nil should return nil.
	if got := ReadSessionMetadataFull(nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
		return
	}

	// ReadSessionMetadataFull with no metadata entries should return nil.
	result := &transcript.LoadResult{}
	if got := ReadSessionMetadataFull(result); got != nil {
		t.Fatalf("expected nil, got %+v", got)
		return
	}
}

// --- Metadata from resume (rebuildMetadata) via ResumeSession ---

func TestResumeSession_PopulatesMetadataFromEntries(t *testing.T) {
	dir := t.TempDir()

	// Create a session with messages and metadata entries.
	rec := transcript.NewRecorder("resume-meta-test", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello", Extra: map[string]any{
			"timestamp": "2026-06-01T12:00:00Z",
			"model":     "claude-sonnet-4-20250514",
			"provider":  "claude",
		}},
		{Role: schema.Assistant, Content: "hi"},
	}
	if err := rec.Record(msgs, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("parent_session_id", "old-parent"); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("branch_point", "1"); err != nil {
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

	// Write full metadata for richer info.
	metaFull := &SessionMetadataFull{
		SessionID:       "resume-meta-test",
		ParentSessionID: "old-parent",
		ParentToolUseID: "old-parent-tool",
		BranchPoint:     1,
		Model:           "claude-sonnet-4-20250514",
		Provider:        "claude",
		IsLeaf:          false,
		TokenUsage:      5000,
		GitBranch:       "main",
		CWD:             "/workspace/project",
	}
	metaJSON, _ := json.Marshal(metaFull)
	if err := rec.RecordMetadata("session_metadata_full", string(metaJSON)); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Resume and verify metadata.
	resumed, err := ResumeSession(t.Context(), ResumeOptions{
		SessionID:  "resume-meta-test",
		SessionDir: dir,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
		return
	}

	meta := resumed.Metadata
	if meta.ParentSessionID != "old-parent" {
		t.Fatalf("parent: %q", meta.ParentSessionID)
	}
	if meta.ParentToolUseID != "old-parent-tool" {
		t.Fatalf("parent tool use: %q", meta.ParentToolUseID)
	}
	if meta.BranchPoint != 1 {
		t.Fatalf("branch point: %d", meta.BranchPoint)
	}
	if meta.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("model: %q", meta.Model)
	}
	if meta.Provider != "claude" {
		t.Fatalf("provider: %q", meta.Provider)
	}
	if meta.IsLeaf {
		t.Fatal("expected IsLeaf=false from full metadata")
	}
	if meta.TokenUsage != 5000 {
		t.Fatalf("token usage: %d", meta.TokenUsage)
	}
	if meta.MessageCount != 2 {
		t.Fatalf("message count: %d", meta.MessageCount)
	}
	if meta.GitBranch != "main" {
		t.Fatalf("git branch: %q", meta.GitBranch)
	}
	if meta.CWD != "/workspace/project" {
		t.Fatalf("cwd: %q", meta.CWD)
	}
}
