package engine

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/worktree"
)

func TestQueryEngineWorktreeLifecycleReducesCommittedRecords(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	runEngineWorktreeGit(t, root, "init", "-b", "master")
	runEngineWorktreeGit(t, root, "config", "user.email", "test@example.com")
	runEngineWorktreeGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runEngineWorktreeGit(t, root, "add", "tracked.txt")
	runEngineWorktreeGit(t, root, "commit", "-m", "base")

	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:         "parent-session",
		ThreadID:          "parent-thread",
		CWD:               root,
		MemoryProjectRoot: root,
		Clock: func() time.Time {
			return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
		},
	})
	t.Cleanup(engine.Close)
	service := engine.WorktreeLifecycleService()
	if service == nil {
		t.Fatal("worktree lifecycle service is nil")
	}
	owner := worktree.Owner{
		Kind:            worktree.OwnerAgent,
		ID:              "agent-worktree",
		SessionID:       "agent-session",
		ThreadID:        "agent-thread",
		ParentSessionID: "parent-session",
		ParentThreadID:  "parent-thread",
	}
	record, err := service.Create(t.Context(), worktree.CreateRequest{
		Owner:     owner,
		SourceDir: root,
	})
	if err != nil {
		t.Fatalf("create engine worktree: %v", err)
	}
	snapshot := engine.RuntimeSnapshot()
	projected, ok := snapshot.Worktrees[record.ID]
	if !ok ||
		projected.State != worktree.StateReady ||
		projected.RecordRevision != record.Revision ||
		projected.OwnerID != owner.ID ||
		projected.Path != record.Path {
		t.Fatalf("ready projection = %#v, ok=%v", projected, ok)
	}
	thread, ok := snapshot.Threads[owner.ThreadID]
	if !ok ||
		thread.AgentID != owner.ID ||
		thread.ParentThreadID != owner.ParentThreadID ||
		thread.LastSequence != 3 ||
		thread.ActiveTurnID != "" {
		t.Fatalf("worktree runtime thread = %#v, ok=%v", thread, ok)
	}
	removed, err := service.Remove(t.Context(), record.ID, owner)
	if err != nil {
		t.Fatalf("remove engine worktree: %v", err)
	}
	projected = engine.RuntimeSnapshot().Worktrees[record.ID]
	if projected.State != worktree.StateRemoved ||
		projected.RecordRevision != removed.Revision ||
		projected.Sequence != 5 {
		t.Fatalf("removed projection = %#v", projected)
	}
}

func TestQueryEngineStartupRestoresDurableWorktreeMetadata(t *testing.T) {
	root := t.TempDir()
	recordID := "restart-record"
	managedRoot := filepath.Join(
		root,
		".yhc",
		"worktrees",
		"v1",
		"trees",
	)
	recordPath := filepath.Join(managedRoot, recordID)
	if err := os.MkdirAll(recordPath, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 13, 30, 0, 0, time.UTC)
	record := worktree.Record{
		Version:            worktree.RecordVersion,
		ID:                 recordID,
		Owner:              testRuntimeWorktreeOwner(),
		RepositoryIdentity: filepath.Join(root, ".git"),
		RepoRoot:           root,
		Path:               recordPath,
		Branch:             "yhc/worktree/" + recordID,
		BaseCommit:         strings.Repeat("a", 40),
		State:              worktree.StateRetained,
		Revision:           4,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now,
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := filepath.Join(
		root,
		".yhc",
		"worktrees",
		"v1",
		"records",
	)
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(storeRoot, recordID+".json"),
		data,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:         "restart-parent",
		ThreadID:          "restart-parent",
		CWD:               root,
		MemoryProjectRoot: root,
	})
	t.Cleanup(engine.Close)
	projected, ok := engine.RuntimeSnapshot().Worktrees[recordID]
	if !ok ||
		projected.State != worktree.StateRetained ||
		projected.RecoveryDisposition != worktree.RecoveryInspectOnly ||
		projected.RecordRevision != record.Revision {
		t.Fatalf("startup recovery projection = %#v, ok=%v", projected, ok)
	}
	if len(engine.RuntimeSnapshot().Threads) != 0 {
		t.Fatalf(
			"startup discovery synthesized a runtime thread: %#v",
			engine.RuntimeSnapshot().Threads,
		)
	}
}

func TestRuntimeStateRejectsWorktreeRevisionGapWithoutMutation(t *testing.T) {
	store := NewRuntimeStateStore()
	creating := runtimeWorktreeEvent(
		1,
		1,
		"",
		worktree.StateCreating,
	)
	if err := store.Apply(creating); err != nil {
		t.Fatal(err)
	}
	invalid := runtimeWorktreeEvent(
		2,
		3,
		worktree.StateCreating,
		worktree.StateReady,
	)
	if err := store.Apply(invalid); err == nil ||
		!strings.Contains(err.Error(), "record revision 3, want 2") {
		t.Fatalf("revision gap error = %v", err)
	}
	snapshot := store.Snapshot("agent-thread")
	if snapshot.Worktrees["record-1"].State != worktree.StateCreating ||
		snapshot.Threads["agent-thread"].LastSequence != 1 ||
		snapshot.Revision != 1 {
		t.Fatalf("state mutated after rejection: %#v", snapshot)
	}
}

func TestRuntimeStateWorktreeReplayIsDeterministicAndSideEffectFree(t *testing.T) {
	events := []QueryEvent{
		runtimeWorktreeEvent(1, 1, "", worktree.StateCreating),
		runtimeWorktreeEvent(
			2,
			2,
			worktree.StateCreating,
			worktree.StateCreating,
		),
		runtimeWorktreeEvent(
			3,
			3,
			worktree.StateCreating,
			worktree.StateReady,
		),
		runtimeWorktreeEvent(
			4,
			4,
			worktree.StateReady,
			worktree.StateRetained,
		),
	}
	first := NewRuntimeStateStore()
	second := NewRuntimeStateStore()
	if err := first.Replay(events); err != nil {
		t.Fatal(err)
	}
	if err := second.Replay(events); err != nil {
		t.Fatal(err)
	}
	left := first.Snapshot("agent-thread")
	right := second.Snapshot("agent-thread")
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("replay mismatch:\nleft=%#v\nright=%#v", left, right)
	}
	if left.Worktrees["record-1"].State != worktree.StateRetained {
		t.Fatalf("replayed worktree = %#v", left.Worktrees["record-1"])
	}
	if _, err := os.Stat("/definitely-not-a-worktree-side-effect"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected replay side effect: %v", err)
	}
}

func TestRuntimeStateRestoresWorktreeMetadataIdempotently(t *testing.T) {
	store := NewRuntimeStateStore()
	updatedAt := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	recovery := worktree.RecoveryRecord{
		Disposition: worktree.RecoveryPending,
		Diagnostic:  "cleanup interrupted",
		Record: worktree.Record{
			Version:            worktree.RecordVersion,
			ID:                 "restored-record",
			Owner:              testRuntimeWorktreeOwner(),
			RepositoryIdentity: "/repo/.git",
			RepoRoot:           "/repo",
			Path:               "/repo/.yhc/worktrees/v1/trees/restored-record",
			Branch:             "yhc/worktree/restored-record",
			BaseCommit:         strings.Repeat("a", 40),
			State:              worktree.StateRemoving,
			Revision:           7,
			CreatedAt:          updatedAt.Add(-time.Hour),
			UpdatedAt:          updatedAt,
		},
	}
	if err := store.RestoreWorktreeSnapshots(
		[]worktree.RecoveryRecord{recovery},
	); err != nil {
		t.Fatal(err)
	}
	first := store.Snapshot("parent-thread")
	projected := first.Worktrees[recovery.Record.ID]
	if projected.State != worktree.StateRemoving ||
		projected.RecoveryDisposition != worktree.RecoveryPending ||
		projected.RecoveryDiagnostic != recovery.Diagnostic ||
		projected.Sequence != 0 {
		t.Fatalf("restored projection = %#v", projected)
	}
	if err := store.RestoreWorktreeSnapshots(
		[]worktree.RecoveryRecord{recovery},
	); err != nil {
		t.Fatal(err)
	}
	second := store.Snapshot("parent-thread")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf(
			"duplicate restore mutated runtime state:\nfirst=%#v\nsecond=%#v",
			first,
			second,
		)
	}
	if len(second.Threads) != 0 {
		t.Fatalf("metadata restore synthesized runtime threads: %#v", second.Threads)
	}
}

func testRuntimeWorktreeOwner() worktree.Owner {
	return worktree.Owner{
		Kind:            worktree.OwnerAgent,
		ID:              "agent-restored",
		SessionID:       "agent-session",
		ThreadID:        "agent-thread",
		ParentSessionID: "parent-session",
		ParentThreadID:  "parent-thread",
	}
}

func runtimeWorktreeEvent(
	sequence uint64,
	recordRevision uint64,
	from worktree.State,
	state worktree.State,
) QueryEvent {
	repositoryIdentity := ""
	baseCommit := ""
	repoRoot := "/repo"
	if recordRevision > 1 {
		repositoryIdentity = "/repo/.git"
		baseCommit = strings.Repeat("a", 40)
	}
	return QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID:       "agent-session",
			ThreadID:        "agent-thread",
			TurnID:          "worktree-record-1-" + string(rune('0'+recordRevision)),
			AgentID:         "agent-1",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
			Sequence:        sequence,
			Timestamp:       time.Date(2026, 7, 20, 12, 0, int(sequence), 0, time.UTC),
			CausationID:     "record-1",
		},
		Type: EventWorktreeLifecycle,
		WorktreeLifecycle: &WorktreeLifecycleEvent{
			RecordID:           "record-1",
			OwnerKind:          worktree.OwnerAgent,
			OwnerID:            "agent-1",
			FromState:          from,
			State:              state,
			RecordRevision:     recordRevision,
			RepositoryIdentity: repositoryIdentity,
			RepoRoot:           repoRoot,
			Path:               "/repo/.yhc/worktrees/v1/trees/record-1",
			Branch:             "yhc/worktree/record-1",
			BaseCommit:         baseCommit,
		},
	}
}

func runEngineWorktreeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), output, err)
	}
}
