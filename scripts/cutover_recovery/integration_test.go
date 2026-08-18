package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type realGitReader struct{}

func (realGitReader) Run(ctx context.Context, root string, argv ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, argv...)...)
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return output, realGitExitError{err: err, code: exit.ExitCode()}
	}
	return output, err
}

type realGitExitError struct {
	err  error
	code int
}

func (e realGitExitError) Error() string { return e.err.Error() }
func (e realGitExitError) Unwrap() error { return e.err }
func (e realGitExitError) ExitCode() int { return e.code }

func TestIntegrationMoveRepairRollback(t *testing.T) {
	base := t.TempDir()
	public := filepath.Join(base, "public")
	private := filepath.Join(base, "private")
	linked := filepath.Join(base, "linked")
	prunable := filepath.Join(base, "prunable")
	archive := filepath.Join(base, "archive")
	archiveLinked := filepath.Join(base, "archive-linked")
	for _, root := range []string{public, private} {
		runGit(t, base, "init", root)
		runGit(t, root, "config", "user.email", "cutover@example.test")
		runGit(t, root, "config", "user.name", "Cutover Test")
		if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, root, "add", "tracked.txt")
		runGit(t, root, "commit", "-m", "initial")
	}
	runGit(t, public, "remote", "add", "origin", "git@github.com:abietic/yhc.git")
	runGit(t, private, "remote", "add", "origin", "git@github.com:abietic/yhc-private-history.git")
	runGit(t, private, "worktree", "add", "-b", "linked", linked)
	runGit(t, private, "worktree", "add", "-b", "prunable", prunable)
	if err := os.RemoveAll(prunable); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "stashed.txt"), []byte("stash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, private, "add", "stashed.txt")
	runGit(t, private, "stash", "push", "-m", "cutover fixture")
	if err := os.WriteFile(filepath.Join(linked, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	private = mustResolve(t, private)
	public = mustResolve(t, public)
	linked = mustResolve(t, linked)
	input := cutoverInput{
		SchemaVersion:             schemaVersion,
		ExpectedPublicRepository:  "abietic/yhc",
		ExpectedPrivateRepository: "abietic/yhc-private-history",
		Mappings: []archiveMappingInput{
			{Kind: "main_checkout", Source: private, Destination: archive},
			{Kind: "linked_worktree", Source: linked, Destination: archiveLinked},
		},
		Defaults: []classificationDefault{
			{Kind: "ref", Classification: "private_recovery", Owner: "test", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"},
			{Kind: "worktree", Classification: "private_recovery", Owner: "test", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"},
			{Kind: "dirty_path", Classification: "private_recovery", Owner: "test", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"},
			{Kind: "stash", Classification: "private_recovery", Owner: "test", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"},
		},
		Rules: []classificationRule{{
			Kind:               "dirty_path",
			Source:             linked,
			Identity:           strings.Join([]string{"??", base64.StdEncoding.EncodeToString([]byte("dirty.txt")), ""}, "\x1f"),
			Classification:     "private_recovery",
			Owner:              "test",
			RestoreDisposition: "preserve",
			ChecksumPolicy:     "sha256",
		}},
	}
	reader := realGitReader{}
	privateInventory, err := collectPrivateInventory(context.Background(), reader, private, input)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPrunable(privateInventory.Worktrees) {
		t.Fatalf("expected prunable registration, got %+v", privateInventory.Worktrees)
	}
	prunableIDs := prunableRecordIDs(privateInventory.Worktrees)
	publicRecord, err := collectRepositoryRecord(context.Background(), reader, public, "public")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := sealManifest(manifest{
		SchemaVersion:   schemaVersion,
		CapturedAt:      time.Now().UTC().Format(time.RFC3339),
		Public:          publicRecord,
		Private:         privateInventory.Repository,
		ArchiveMapping:  privateInventory.ArchiveMapping,
		Refs:            privateInventory.Refs,
		Worktrees:       privateInventory.Worktrees,
		DirtyPaths:      privateInventory.DirtyPaths,
		Stashes:         privateInventory.Stashes,
		Classifications: privateInventory.Classifications,
		Aggregates:      aggregateRecord{ArchiveMappings: len(privateInventory.ArchiveMapping), Refs: len(privateInventory.Refs), Worktrees: len(privateInventory.Worktrees), DirtyPaths: len(privateInventory.DirtyPaths), Stashes: len(privateInventory.Stashes), Classifications: len(privateInventory.Classifications)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		zero := zeroProcessReader{}
		if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: zero, Now: time.Now}, frozen, phasePreMove); err != nil {
			t.Fatalf("pre-move verification: %v", err)
		}
		if err := os.Mkdir(archive, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: zero, Now: time.Now}, frozen, phasePreMove); err == nil || !strings.Contains(err.Error(), "collision") {
			t.Fatalf("pre-move destination collision accepted: %v", err)
		}
		if err := os.Remove(archive); err != nil {
			t.Fatal(err)
		}
		if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: occupiedProcessReader{}, Now: time.Now}, frozen, phasePreMove); err == nil || !strings.Contains(err.Error(), "process_occupancy") {
			t.Fatalf("pre-move occupancy accepted: %v", err)
		}
	}

	if err := os.Rename(linked, archiveLinked); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(private, archive); err != nil {
		t.Fatal(err)
	}
	runGit(t, archive, "worktree", "repair", archiveLinked)
	if runtime.GOOS == "darwin" {
		zero := zeroProcessReader{}
		if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: zero, Now: time.Now}, frozen, phasePreRollback); err != nil {
			t.Fatalf("pre-rollback verification: %v", err)
		}
		if err := os.Mkdir(private, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: zero, Now: time.Now}, frozen, phasePreRollback); err == nil || !strings.Contains(err.Error(), "collision") {
			t.Fatalf("pre-rollback source collision accepted: %v", err)
		}
		if err := os.Remove(private); err != nil {
			t.Fatal(err)
		}
		if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: occupiedProcessReader{}, Now: time.Now}, frozen, phasePreRollback); err == nil || !strings.Contains(err.Error(), "process_occupancy") {
			t.Fatalf("pre-rollback occupancy accepted: %v", err)
		}
	}
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phasePostMove); err != nil {
		t.Fatalf("post-move verification: %v", err)
	}
	allChanged := cloneManifest(frozen)
	presentIndex := firstPresentWorktree(t, allChanged.Worktrees)
	allChanged.Public.OriginRepository = "abietic/other-public"
	allChanged.Private.OriginRepository = "abietic/other-private"
	allChanged.Refs[0].ObjectID = strings.Repeat("f", 40)
	allChanged.Worktrees[presentIndex].Head = strings.Repeat("e", 40)
	allChanged.DirtyPaths[0].OmissionReason = "changed"
	allChanged.Stashes[0].ObjectID = strings.Repeat("d", 40)
	allChanged, err = sealManifest(allChanged)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, allChanged, phasePostMove); err == nil || !strings.Contains(err.Error(), "dirty_paths,private_repository,public_repository,refs,stashes,worktrees") {
		t.Fatalf("aggregate mismatch codes = %v", err)
	}
	porcelainChanged := cloneManifest(frozen)
	porcelainChanged.Worktrees[firstPresentWorktree(t, porcelainChanged.Worktrees)].PorcelainBase64 = base64.StdEncoding.EncodeToString([]byte("worktree /different\x00HEAD " + strings.Repeat("a", 40)))
	porcelainChanged, err = sealManifest(porcelainChanged)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, porcelainChanged, phasePostMove); err == nil || !strings.Contains(err.Error(), "worktrees") {
		t.Fatalf("porcelain mismatch accepted: %v", err)
	}
	commonChanged := cloneManifest(frozen)
	commonChanged.Worktrees[firstPresentWorktree(t, commonChanged.Worktrees)].CommonDir = filepath.Join(base, "different-common-dir")
	commonChanged, err = sealManifest(commonChanged)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, commonChanged, phasePostMove); err == nil || !strings.Contains(err.Error(), "worktrees") {
		t.Fatalf("common-dir mismatch accepted: %v", err)
	}
	runGit(t, archive, "remote", "set-url", "origin", "git@github.com:abietic/other-private.git")
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phasePostMove); err == nil || !strings.Contains(err.Error(), "private_repository") {
		t.Fatalf("private remote mismatch accepted: %v", err)
	}
	runGit(t, archive, "remote", "set-url", "origin", "git@github.com:abietic/yhc-private-history.git")
	runGit(t, public, "remote", "set-url", "origin", "git@github.com:abietic/other-public.git")
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phasePostMove); err == nil || !strings.Contains(err.Error(), "public_repository") {
		t.Fatalf("public remote mismatch accepted: %v", err)
	}
	runGit(t, public, "remote", "set-url", "origin", "git@github.com:abietic/yhc.git")
	if err := os.WriteFile(filepath.Join(archiveLinked, "dirty.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phasePostMove); err == nil || !strings.Contains(err.Error(), "dirty_paths") {
		t.Fatalf("changed sha256 dirty file accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveLinked, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(archiveLinked, linked); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(archive, private); err != nil {
		t.Fatal(err)
	}
	runGit(t, private, "worktree", "repair", linked)
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phaseRollback); err != nil {
		t.Fatalf("rollback verification: %v", err)
	}
	rollbackCommon := cloneManifest(frozen)
	rollbackCommon.Worktrees[firstPresentWorktree(t, rollbackCommon.Worktrees)].CommonDir = filepath.Join(base, "different-rollback-common-dir")
	rollbackCommon, err = sealManifest(rollbackCommon)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, rollbackCommon, phaseRollback); err == nil || !strings.Contains(err.Error(), "worktrees") {
		t.Fatalf("rollback common-dir mismatch accepted: %v", err)
	}
	runGit(t, private, "stash", "drop")
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phaseRollback); err == nil || !strings.Contains(err.Error(), "stashes") {
		t.Fatalf("stash mismatch accepted: %v", err)
	}
	rolledBack, err := collectPrivateInventory(context.Background(), reader, private, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := prunableRecordIDs(rolledBack.Worktrees); strings.Join(got, ",") != strings.Join(prunableIDs, ",") {
		t.Fatalf("rollback prunable IDs = %v, want %v", got, prunableIDs)
	}
	if err := os.WriteFile(filepath.Join(private, "head-change.txt"), []byte("head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, private, "add", "head-change.txt")
	runGit(t, private, "commit", "-m", "head change")
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phaseRollback); err == nil || !strings.Contains(err.Error(), "private_repository") {
		t.Fatalf("HEAD mismatch accepted: %v", err)
	}
	runGit(t, private, "checkout", "-b", "moved-branch")
	if err := verifyLiveState(context.Background(), dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}, frozen, phaseRollback); err == nil || !strings.Contains(err.Error(), "worktrees") {
		t.Fatalf("branch mismatch accepted: %v", err)
	}
}

type unavailableProcessReader struct{}

func (unavailableProcessReader) Run(context.Context, ...string) (commandResult, error) {
	return commandResult{}, fmt.Errorf("process reader should not be called")
}

type zeroProcessReader struct{}

func (zeroProcessReader) Run(context.Context, ...string) (commandResult, error) {
	return commandResult{ExitCode: 1}, nil
}

type occupiedProcessReader struct{}

func (occupiedProcessReader) Run(_ context.Context, argv ...string) (commandResult, error) {
	root := argv[len(argv)-1]
	return commandResult{Stdout: []byte("p99\nfcwd\nn" + root + "\n")}, nil
}

func containsPrunable(worktrees []worktreeRecord) bool {
	for _, worktree := range worktrees {
		if worktree.Prunable && !worktree.Present {
			return true
		}
	}
	return false
}

func prunableRecordIDs(worktrees []worktreeRecord) []string {
	var ids []string
	for _, worktree := range worktrees {
		if worktree.Prunable && !worktree.Present {
			ids = append(ids, worktree.RecordID)
		}
	}
	return ids
}

func cloneManifest(m manifest) manifest {
	m.Refs = append([]refRecord(nil), m.Refs...)
	m.Worktrees = append([]worktreeRecord(nil), m.Worktrees...)
	m.DirtyPaths = append([]dirtyPathRecord(nil), m.DirtyPaths...)
	m.Stashes = append([]stashRecord(nil), m.Stashes...)
	m.Classifications = append([]classificationRecord(nil), m.Classifications...)
	return m
}

func firstPresentWorktree(t *testing.T, worktrees []worktreeRecord) int {
	t.Helper()
	for index, worktree := range worktrees {
		if worktree.Present {
			return index
		}
	}
	t.Fatal("fixture has no present worktree")
	return 0
}

func runGit(t *testing.T, root string, argv ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, argv...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(argv, " "), err, output)
	}
}
