package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeGitSource struct {
	resolved       map[string]string
	mergeBase      string
	nameStatus     []byte
	binaryDiff     []byte
	untrackedCount int
	resolveErr     error
	mergeBaseErr   error
	nameStatusErr  error
	binaryDiffErr  error
	untrackedErr   error
	mergeLeft      string
	mergeRight     string
	nameStatusBase string
	nameStatusHead string
	binaryDiffBase string
	binaryDiffHead string
	untrackedCalls int
	resolveCalls   []string
	trackedClean   bool
	trackedErr     error
	trackedCalls   int
}

func (source *fakeGitSource) Resolve(_ context.Context, rev string) (string, error) {
	source.resolveCalls = append(source.resolveCalls, rev)
	if source.resolveErr != nil {
		return "", source.resolveErr
	}
	return source.resolved[rev], nil
}

func (source *fakeGitSource) MergeBase(_ context.Context, left, right string) (string, error) {
	source.mergeLeft = left
	source.mergeRight = right
	return source.mergeBase, source.mergeBaseErr
}

func (source *fakeGitSource) NameStatus(_ context.Context, base, head string) ([]byte, error) {
	source.nameStatusBase = base
	source.nameStatusHead = head
	return append([]byte(nil), source.nameStatus...), source.nameStatusErr
}

func (source *fakeGitSource) BinaryDiff(_ context.Context, base, head string) ([]byte, error) {
	source.binaryDiffBase = base
	source.binaryDiffHead = head
	return append([]byte(nil), source.binaryDiff...), source.binaryDiffErr
}

func (source *fakeGitSource) UntrackedCount(_ context.Context) (int, error) {
	source.untrackedCalls++
	return source.untrackedCount, source.untrackedErr
}

func (source *fakeGitSource) TrackedWorktreeClean(_ context.Context) (bool, error) {
	source.trackedCalls++
	return source.trackedClean, source.trackedErr
}

func TestResolveSnapshot(t *testing.T) {
	source := &fakeGitSource{
		resolved:       map[string]string{"HEAD": "bbbb"},
		mergeBase:      "aaaa",
		nameStatus:     []byte("M\x00engine/query.go\x00"),
		binaryDiff:     []byte("patch"),
		untrackedCount: 3,
	}

	got, err := resolveSnapshot(
		context.Background(),
		"/repo",
		"origin/master",
		"",
		source,
	)
	if err != nil {
		t.Fatalf("resolveSnapshot() error = %v", err)
	}
	wantDigest := sha256.Sum256([]byte("patch"))
	want := GitSnapshot{
		BaseRef:          "origin/master",
		Base:             "aaaa",
		Head:             "bbbb",
		DiffDigest:       hex.EncodeToString(wantDigest[:]),
		Changed:          []GitChange{{Status: "M", Path: "engine/query.go"}},
		OutsideUntracked: 3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveSnapshot() = %#v, want %#v", got, want)
	}
}

func TestResolveSnapshotExplicitHeadUsesCommittedTree(t *testing.T) {
	source := &fakeGitSource{
		resolved:       map[string]string{"feature": "cccc"},
		mergeBase:      "aaaa",
		nameStatus:     []byte("A\x00engine/new.go\x00"),
		binaryDiff:     []byte("committed patch"),
		untrackedCount: 99,
		trackedClean:   true,
	}

	snapshot, err := resolveSnapshot(
		context.Background(),
		"/repo",
		"origin/master",
		"feature",
		source,
	)
	if err != nil {
		t.Fatalf("resolveSnapshot() error = %v", err)
	}
	if source.mergeLeft != "origin/master" || source.mergeRight != "cccc" {
		t.Fatalf("merge base inputs = %q, %q", source.mergeLeft, source.mergeRight)
	}
	if source.nameStatusBase != "aaaa" || source.nameStatusHead != "cccc" ||
		source.binaryDiffBase != "aaaa" || source.binaryDiffHead != "cccc" {
		t.Fatalf(
			"comparison inputs = name-status %q..%q, diff %q..%q",
			source.nameStatusBase,
			source.nameStatusHead,
			source.binaryDiffBase,
			source.binaryDiffHead,
		)
	}
	if source.trackedCalls != 1 || source.untrackedCalls != 0 || snapshot.OutsideUntracked != 0 {
		t.Fatalf("explicit head inspected untracked paths: calls=%d count=%d", source.untrackedCalls, snapshot.OutsideUntracked)
	}
}

func TestResolveSnapshotExplicitHeadRejectsDirtyTrackedTree(t *testing.T) {
	source := &fakeGitSource{
		resolved:     map[string]string{"feature": "cccc"},
		mergeBase:    "aaaa",
		trackedClean: false,
	}
	_, err := resolveSnapshot(context.Background(), "/repo", "origin/master", "feature", source)
	if err == nil || !strings.Contains(err.Error(), "tracked worktree is dirty") {
		t.Fatalf("resolveSnapshot() error = %v, want dirty tracked tree rejection", err)
	}
}

func TestParseNameStatus(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []GitChange
		wantErr bool
	}{
		{
			name:  "rename",
			input: "R100\x00engine/old.go\x00engine/new.go\x00",
			want: []GitChange{
				{Status: "R100-from", Path: "engine/old.go"},
				{Status: "R100-to", Path: "engine/new.go"},
			},
		},
		{
			name:  "delete",
			input: "D\x00engine/old.go\x00",
			want:  []GitChange{{Status: "D", Path: "engine/old.go"}},
		},
		{name: "empty", input: "", want: nil},
		{name: "truncated rename", input: "R100\x00engine/old.go\x00", wantErr: true},
		{name: "absolute", input: "M\x00/engine/query.go\x00", wantErr: true},
		{name: "traversal", input: "M\x00../engine/query.go\x00", wantErr: true},
		{name: "backslash", input: "M\x00engine\\query.go\x00", wantErr: true},
		{name: "duplicate", input: "M\x00engine/query.go\x00M\x00engine/query.go\x00", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseNameStatus([]byte(test.input))
			if (err != nil) != test.wantErr {
				t.Fatalf("parseNameStatus() error = %v, wantErr %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseNameStatus() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCommandGitSourceTracksOnlyTrackedDiff(t *testing.T) {
	repository := t.TempDir()
	runTestGit(t, repository, "init")
	runTestGit(t, repository, "config", "user.name", "Iteration Test")
	runTestGit(t, repository, "config", "user.email", "iteration@example.invalid")
	writeTestFile(t, repository, "engine/a.go", "package engine\n")
	runTestGit(t, repository, "add", "engine/a.go")
	runTestGit(t, repository, "commit", "-m", "base")
	base := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))

	writeTestFile(t, repository, "engine/a.go", "package engine\n\nconst changed = true\n")
	writeTestFile(t, repository, "scratch.txt", "first\n")
	source := commandGitSource{root: repository}
	first, err := resolveSnapshot(context.Background(), repository, base, "", source)
	if err != nil {
		t.Fatalf("resolve first snapshot: %v", err)
	}
	if first.Base != base || first.Head != base {
		t.Fatalf("snapshot commits = base %q head %q, want %q", first.Base, first.Head, base)
	}
	if !reflect.DeepEqual(first.Changed, []GitChange{{Status: "M", Path: "engine/a.go"}}) {
		t.Fatalf("snapshot changes = %#v", first.Changed)
	}
	if first.OutsideUntracked != 1 {
		t.Fatalf("outside untracked = %d, want 1", first.OutsideUntracked)
	}

	writeTestFile(t, repository, "scratch.txt", "second\n")
	second, err := resolveSnapshot(context.Background(), repository, base, "", source)
	if err != nil {
		t.Fatalf("resolve second snapshot: %v", err)
	}
	if second.DiffDigest != first.DiffDigest {
		t.Fatalf("untracked content changed digest: %q != %q", second.DiffDigest, first.DiffDigest)
	}

	writeTestFile(t, repository, "engine/a.go", "package engine\n\nconst changedAgain = true\n")
	third, err := resolveSnapshot(context.Background(), repository, base, "", source)
	if err != nil {
		t.Fatalf("resolve third snapshot: %v", err)
	}
	if third.DiffDigest == first.DiffDigest {
		t.Fatal("tracked content did not change diff digest")
	}
}

func TestCommandGitSourceRejectsInProgressMerge(t *testing.T) {
	repository := t.TempDir()
	runTestGit(t, repository, "init")
	runTestGit(t, repository, "config", "user.name", "Iteration Test")
	runTestGit(t, repository, "config", "user.email", "iteration@example.invalid")
	writeTestFile(t, repository, "README.md", "base\n")
	runTestGit(t, repository, "add", "README.md")
	runTestGit(t, repository, "commit", "-m", "base")
	gitDir := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "--git-dir"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repository, gitDir)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(strings.Repeat("a", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := (commandGitSource{root: repository}).Resolve(context.Background(), "HEAD")
	if err == nil || !strings.Contains(err.Error(), "merge or rebase") {
		t.Fatalf("Resolve() error = %v, want in-progress diagnostic", err)
	}
}

func TestCommandGitSourceRejectsDirtyExplicitComparison(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage bool
	}{
		{name: "unstaged"},
		{name: "staged", stage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			runTestGit(t, repository, "init")
			runTestGit(t, repository, "config", "user.name", "Iteration Test")
			runTestGit(t, repository, "config", "user.email", "iteration@example.invalid")
			writeTestFile(t, repository, "engine/a.go", "package engine\n")
			runTestGit(t, repository, "add", "engine/a.go")
			runTestGit(t, repository, "commit", "-m", "base")
			base := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))
			writeTestFile(t, repository, "engine/a.go", "package engine\n\nconst dirty = true\n")
			if test.stage {
				runTestGit(t, repository, "add", "engine/a.go")
			}

			_, err := resolveSnapshot(
				context.Background(),
				repository,
				base,
				base,
				commandGitSource{root: repository},
			)
			if err == nil || !strings.Contains(err.Error(), "tracked worktree is dirty") {
				t.Fatalf("resolveSnapshot() error = %v", err)
			}
		})
	}
}

func TestCommandGitSourceAllowsUntrackedExplicitComparison(t *testing.T) {
	repository := t.TempDir()
	runTestGit(t, repository, "init")
	runTestGit(t, repository, "config", "user.name", "Iteration Test")
	runTestGit(t, repository, "config", "user.email", "iteration@example.invalid")
	writeTestFile(t, repository, "engine/a.go", "package engine\n")
	runTestGit(t, repository, "add", "engine/a.go")
	runTestGit(t, repository, "commit", "-m", "base")
	base := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))
	writeTestFile(t, repository, "scratch.txt", "outside comparison\n")

	snapshot, err := resolveSnapshot(
		context.Background(),
		repository,
		base,
		base,
		commandGitSource{root: repository},
	)
	if err != nil {
		t.Fatalf("resolveSnapshot() error = %v", err)
	}
	if snapshot.OutsideUntracked != 0 || len(snapshot.Changed) != 0 {
		t.Fatalf("explicit committed comparison included untracked state: %#v", snapshot)
	}
}

func TestCommandGitSourceTreeSourceReadsCommittedAndCurrentTrackedTrees(t *testing.T) {
	repository := t.TempDir()
	runTestGit(t, repository, "init")
	runTestGit(t, repository, "config", "user.name", "Iteration Test")
	runTestGit(t, repository, "config", "user.email", "iteration@example.invalid")
	writeTestFile(t, repository, "engine/a.go", "package engine\nconst version = \"base\"\n")
	writeTestFile(t, repository, "engine/deleted.go", "package engine\n")
	runTestGit(t, repository, "add", "engine/a.go", "engine/deleted.go")
	runTestGit(t, repository, "commit", "-m", "base")
	base := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))

	writeTestFile(t, repository, "engine/a.go", "package engine\nconst version = \"worktree\"\n")
	if err := os.Remove(filepath.Join(repository, "engine", "deleted.go")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, repository, "engine/new.go", "package engine\n")
	runTestGit(t, repository, "add", "engine/new.go")

	source := commandGitSource{root: repository}
	committed, err := source.ListFiles(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(committed, []string{"engine/a.go", "engine/deleted.go"}) {
		t.Fatalf("committed files = %#v", committed)
	}
	baseData, err := source.ReadFile(context.Background(), base, "engine/a.go")
	if err != nil || !strings.Contains(string(baseData), "base") {
		t.Fatalf("committed data = %q, err = %v", baseData, err)
	}

	current, err := source.ListFiles(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, []string{"engine/a.go", "engine/new.go"}) {
		t.Fatalf("current files = %#v", current)
	}
	worktreeData, err := source.ReadFile(context.Background(), "", "engine/a.go")
	if err != nil || !strings.Contains(string(worktreeData), "worktree") {
		t.Fatalf("worktree data = %q, err = %v", worktreeData, err)
	}
}

func TestWorktreeHeadTreeSourceUsesWorktreeOnlyForChangedFiles(t *testing.T) {
	underlying := memoryTreeSource{
		"head": {
			"engine/changed.go":   {Data: []byte("committed changed")},
			"engine/unchanged.go": {Data: []byte("committed unchanged")},
		},
		"": {
			"engine/changed.go":   {Data: []byte("worktree changed")},
			"engine/unchanged.go": {Data: []byte("worktree should not be read")},
		},
	}
	source := worktreeHeadTreeSource{
		source: underlying,
		head:   "head",
		changed: map[string]struct{}{
			"engine/changed.go": {},
		},
	}
	files, err := source.ListFiles(context.Background(), "head")
	if err != nil || !reflect.DeepEqual(files, []string{"engine/changed.go", "engine/unchanged.go"}) {
		t.Fatalf("ListFiles() = %#v, %v", files, err)
	}
	changed, err := source.ReadFile(context.Background(), "head", "engine/changed.go")
	if err != nil || string(changed) != "worktree changed" {
		t.Fatalf("changed read = %q, %v", changed, err)
	}
	unchanged, err := source.ReadFile(context.Background(), "head", "engine/unchanged.go")
	if err != nil || string(unchanged) != "committed unchanged" {
		t.Fatalf("unchanged read = %q, %v", unchanged, err)
	}
}

func runTestGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func writeTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
