package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeGitReader struct {
	responses map[string][]byte
	errors    map[string]error
}

type fakeGitCommandError struct{ code int }

func (e fakeGitCommandError) Error() string { return "git command failed" }
func (e fakeGitCommandError) ExitCode() int { return e.code }

func (f fakeGitReader) Run(_ context.Context, root string, argv ...string) ([]byte, error) {
	key := root + "\x00" + strings.Join(argv, "\x00")
	if err := f.errors[key]; err != nil {
		return nil, err
	}
	b, ok := f.responses[key]
	if !ok {
		return nil, errors.New("unexpected git command: " + key)
	}
	return b, nil
}

func gitKey(root string, argv ...string) string { return root + "\x00" + strings.Join(argv, "\x00") }

func TestCollectGitInventory(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked worktree")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	linked = mustResolve(t, linked)
	for _, root := range []string{private, linked} {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "archive")
	input := testCutoverInput(private, linked, archive)
	reader := inventoryReader(private, linked)

	inventory, err := collectPrivateInventory(context.Background(), reader, private, input)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Repository.OriginRepository != "abietic/yhc-private-history" {
		t.Fatalf("private origin = %q", inventory.Repository.OriginRepository)
	}
	if len(inventory.Refs) != 2 || len(inventory.Worktrees) != 2 || len(inventory.ArchiveMapping) != 2 {
		t.Fatalf("inventory counts = refs:%d worktrees:%d mappings:%d", len(inventory.Refs), len(inventory.Worktrees), len(inventory.ArchiveMapping))
	}
	if inventory.Worktrees[1].Branch != "" || !inventory.Worktrees[1].Detached {
		t.Fatalf("detached worktree = %+v", inventory.Worktrees[1])
	}
	if !inventory.Worktrees[1].Locked {
		t.Fatal("locked worktree was lost")
	}
	if len(inventory.Classifications) != len(inventory.Refs)+len(inventory.Worktrees)+len(inventory.DirtyPaths)+len(inventory.Stashes) {
		t.Fatalf("classification count = %d", len(inventory.Classifications))
	}
}

func TestCollectDirtyRename(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	linked = mustResolve(t, linked)
	for _, root := range []string{private, linked} {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "archive")
	input := testCutoverInput(private, linked, archive)
	if err := os.WriteFile(filepath.Join(private, "space file"), []byte("fixture only"), 0o600); err != nil {
		t.Fatal(err)
	}
	input.Rules = append(input.Rules, classificationRule{Kind: "dirty_path", Source: private, Identity: strings.Join([]string{" M", base64.StdEncoding.EncodeToString([]byte("space file")), ""}, "\x1f"), Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "sha256"})
	reader := inventoryReader(private, linked)
	inventory, err := collectPrivateInventory(context.Background(), reader, private, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.DirtyPaths) != 2 {
		t.Fatalf("dirty paths = %d", len(inventory.DirtyPaths))
	}
	rename := inventory.DirtyPaths[0]
	if rename.StatusCode != "R " {
		t.Fatalf("rename status = %q", rename.StatusCode)
	}
	if got := decodePath(t, rename.RelativePathBase64); got != "new name\nfile" {
		t.Fatalf("new path = %q", got)
	}
	if got := decodePath(t, rename.OriginalPathBase64); got != "old name" {
		t.Fatalf("old path = %q", got)
	}
	if got := decodePath(t, inventory.DirtyPaths[1].RelativePathBase64); got != "space file" {
		t.Fatalf("space path = %q", got)
	}
	if inventory.DirtyPaths[1].Size != int64(len("fixture only")) || !strings.HasPrefix(inventory.DirtyPaths[1].SHA256, "sha256:") || inventory.DirtyPaths[1].OmissionReason != "" {
		t.Fatalf("sha256 dirty record = %+v", inventory.DirtyPaths[1])
	}
}

func TestCollectStashes(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	linked = mustResolve(t, linked)
	for _, root := range []string{private, linked} {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	input := testCutoverInput(private, linked, filepath.Join(t.TempDir(), "archive"))
	inventory, err := collectPrivateInventory(context.Background(), inventoryReader(private, linked), private, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Stashes) != 1 || inventory.Stashes[0].RefName != "stash@{0}" || inventory.Stashes[0].CapturedUnix != 1700000000 {
		t.Fatalf("stashes = %+v", inventory.Stashes)
	}
}

func TestCollectClassificationCoverage(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	linked = mustResolve(t, linked)
	for _, root := range []string{private, linked} {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	input := testCutoverInput(private, linked, filepath.Join(t.TempDir(), "archive"))
	inventory, err := collectPrivateInventory(context.Background(), inventoryReader(private, linked), private, input)
	if err != nil {
		t.Fatal(err)
	}
	want := len(inventory.Refs) + len(inventory.Worktrees) + len(inventory.DirtyPaths) + len(inventory.Stashes)
	if len(inventory.Classifications) != want {
		t.Fatalf("classification coverage = %d, want %d", len(inventory.Classifications), want)
	}
	input.Rules = append(input.Rules, classificationRule{Kind: "stash", Source: private, Identity: "stash@{missing}", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "retain_archive", ChecksumPolicy: "omit_sensitive"})
	if _, err := collectPrivateInventory(context.Background(), inventoryReader(private, linked), private, input); err == nil || !strings.Contains(err.Error(), "matched no record") {
		t.Fatalf("unmatched override error = %v", err)
	}
}

func TestCollectPrunableAbsentAndReadOnlyAllowlist(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	linked = mustResolve(t, linked)
	for _, root := range []string{private, linked} {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent-prunable")
	input := testCutoverInput(private, linked, filepath.Join(t.TempDir(), "archive"))
	reader := inventoryReader(private, linked)
	reader.responses[gitKey(private, "worktree", "list", "--porcelain", "-z")] = []byte("worktree " + private + "\x00HEAD " + strings.Repeat("a", 40) + "\x00branch refs/heads/main\x00\x00worktree " + linked + "\x00HEAD " + strings.Repeat("b", 40) + "\x00detached\x00\x00worktree " + missing + "\x00HEAD " + strings.Repeat("c", 40) + "\x00prunable stale\x00\x00")
	inventory, err := collectPrivateInventory(context.Background(), reader, private, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Worktrees) != 3 || inventory.Worktrees[2].Present || !inventory.Worktrees[2].Prunable {
		t.Fatalf("prunable record = %+v", inventory.Worktrees)
	}
	if len(inventory.ArchiveMapping) != 2 {
		t.Fatalf("prunable registration received mapping: %+v", inventory.ArchiveMapping)
	}
	if _, err := (safeGitReader{reader: reader, roots: map[string]struct{}{private: {}}}).run(context.Background(), private, "reset", "--hard"); err == nil {
		t.Fatal("mutating command was admitted")
	}
}

func TestCollectPublicRepositoryRecord(t *testing.T) {
	root := mustResolve(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	reader := fakeGitReader{responses: map[string][]byte{
		gitKey(root, "rev-parse", "--show-toplevel"):               []byte(root + "\n"),
		gitKey(root, "rev-parse", "--verify", "HEAD"):              []byte(strings.Repeat("a", 40) + "\n"),
		gitKey(root, "rev-parse", "--git-common-dir"):              []byte(".git\n"),
		gitKey(root, "remote", "get-url", "--all", "origin"):       []byte("https://github.com/abietic/yhc.git\n"),
		gitKey(root, "symbolic-ref", "--quiet", "--short", "HEAD"): []byte("master\n"),
	}, errors: map[string]error{}}
	record, err := collectRepositoryRecord(context.Background(), reader, root, "public")
	if err != nil {
		t.Fatal(err)
	}
	if record.Role != "public" || record.OriginRepository != "abietic/yhc" || record.Branch != "master" || record.Detached {
		t.Fatalf("public record = %+v", record)
	}
}

func TestCollectRejectsFrozenInventoryMismatches(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	linked = mustResolve(t, linked)
	for _, root := range []string{private, linked} {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	input := testCutoverInput(private, linked, filepath.Join(t.TempDir(), "archive"))
	t.Run("invalid ref object id", func(t *testing.T) {
		reader := inventoryReader(private, linked)
		reader.responses[gitKey(private, "for-each-ref", "--format=%(refname)%00%(objectname)%00")] = []byte("refs/heads/main\x00INVALID\x00\n")
		if _, err := collectPrivateInventory(context.Background(), reader, private, input); err == nil || !strings.Contains(err.Error(), "object ID") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("non-detached symbolic ref failure", func(t *testing.T) {
		reader := inventoryReader(private, linked)
		reader.errors[gitKey(linked, "symbolic-ref", "--quiet", "--short", "HEAD")] = fakeGitCommandError{code: 2}
		if _, err := collectPrivateInventory(context.Background(), reader, private, input); err == nil || !strings.Contains(err.Error(), "symbolic branch") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("live head differs from porcelain", func(t *testing.T) {
		reader := inventoryReader(private, linked)
		reader.responses[gitKey(linked, "rev-parse", "--verify", "HEAD")] = []byte(strings.Repeat("a", 40) + "\n")
		if _, err := collectPrivateInventory(context.Background(), reader, private, input); err == nil || !strings.Contains(err.Error(), "differs from worktree porcelain") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("multiple exact overrides", func(t *testing.T) {
		duplicated := input
		rule := classificationRule{Kind: "stash", Source: private, Identity: "stash@{0}", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"}
		duplicated.Rules = []classificationRule{rule, rule}
		if _, err := collectPrivateInventory(context.Background(), inventoryReader(private, linked), private, duplicated); err == nil || !strings.Contains(err.Error(), "multiple classification overrides") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestCollectPrunableDirectoryShellIsAbsent(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := mustResolve(t, t.TempDir())
	staleShell := mustResolve(t, t.TempDir())
	for _, root := range []string{private, linked} {
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	input := testCutoverInput(private, linked, filepath.Join(t.TempDir(), "archive"))
	reader := inventoryReader(private, linked)
	reader.responses[gitKey(private, "worktree", "list", "--porcelain", "-z")] = []byte("worktree " + private + "\x00HEAD " + strings.Repeat("a", 40) + "\x00branch refs/heads/main\x00\x00worktree " + linked + "\x00HEAD " + strings.Repeat("b", 40) + "\x00detached\x00\x00worktree " + staleShell + "\x00HEAD " + strings.Repeat("c", 40) + "\x00branch refs/heads/stale\x00prunable gitdir file points to non-existent location\x00\x00")

	inventory, err := collectPrivateInventory(context.Background(), reader, private, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Worktrees) != 3 || inventory.Worktrees[2].Present || !inventory.Worktrees[2].Prunable {
		t.Fatalf("prunable directory shell = %+v", inventory.Worktrees)
	}
	if len(inventory.ArchiveMapping) != 2 {
		t.Fatalf("prunable directory shell received mapping: %+v", inventory.ArchiveMapping)
	}
}

func TestCollectRejectsAliasedDestinationsAndUnknownDefaults(t *testing.T) {
	private := mustResolve(t, t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	linked = mustResolve(t, linked)
	destination := filepath.Join(t.TempDir(), "archive")
	worktrees := []worktreeRecord{{RecordID: makeRecordID("worktree", private, private), Source: private, Present: true}, {RecordID: makeRecordID("worktree", private, linked), Source: linked, Present: true}}
	if _, err := collectMappings(private, worktrees, []archiveMappingInput{{Kind: "main_checkout", Source: private, Destination: destination}, {Kind: "linked_worktree", Source: linked, Destination: filepath.Join(destination, "..", "archive")}}); err == nil || !strings.Contains(err.Error(), "aliased") {
		t.Fatalf("alias destination error = %v", err)
	}
	input := testCutoverInput(private, linked, destination)
	input.Defaults[3].Kind = "unknown"
	if _, err := classifyInventory(input, private, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "classification default") {
		t.Fatalf("unknown default error = %v", err)
	}
}

func TestNormalizeOriginRejectsNonGitHubSchemes(t *testing.T) {
	for _, value := range []string{"http://github.com/abietic/yhc.git", "git@evil.example:abietic/yhc.git", "ssh://git@github.com:2222/abietic/yhc.git"} {
		if _, err := normalizeOrigin(value); err == nil {
			t.Fatalf("origin %q was accepted", value)
		}
	}
	for _, value := range []string{"https://github.com/abietic/yhc.git", "git@github.com:abietic/yhc.git", "ssh://git@github.com/abietic/yhc.git"} {
		if got, err := normalizeOrigin(value); err != nil || got != "abietic/yhc" {
			t.Fatalf("origin %q = %q, %v", value, got, err)
		}
	}
}

func TestValidGitObjectID(t *testing.T) {
	for _, value := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		if !validGitObjectID(value) {
			t.Fatalf("valid object ID %q rejected", value)
		}
	}
	for _, value := range []string{strings.Repeat("A", 40), strings.Repeat("a", 39), strings.Repeat("g", 40)} {
		if validGitObjectID(value) {
			t.Fatalf("invalid object ID %q accepted", value)
		}
	}
}

func TestParseNULLineRecordsRequiresActualGitRecordBoundaries(t *testing.T) {
	output := []byte("refs/heads/main\x00" + strings.Repeat("a", 40) + "\x00\nrefs/tags/v1\x00" + strings.Repeat("b", 40) + "\x00\n")
	records, err := parseNULLineRecords(output, 2, "ref")
	if err != nil || len(records) != 2 || string(records[1][0]) != "refs/tags/v1" {
		t.Fatalf("records = %#v, err = %v", records, err)
	}
	for _, malformed := range [][]byte{[]byte("ref\x00oid\x00"), []byte("ref\x00oid\n"), []byte("ref\x00oid\x00extra\x00\n")} {
		if _, err := parseNULLineRecords(malformed, 2, "ref"); err == nil {
			t.Fatalf("malformed wire format accepted: %q", malformed)
		}
	}
}

func TestSymbolicBranchRejectsEmptySuccessAndNonCanonicalPath(t *testing.T) {
	root := mustResolve(t, t.TempDir())
	reader := fakeGitReader{responses: map[string][]byte{gitKey(root, "symbolic-ref", "--quiet", "--short", "HEAD"): []byte("\n")}, errors: map[string]error{}}
	if _, _, err := symbolicBranch(context.Background(), safeGitReader{reader: reader, roots: map[string]struct{}{root: {}}}, root); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty symbolic-ref error = %v", err)
	}
	if safeRelativePath([]byte("a/../b")) {
		t.Fatal("non-canonical relative path was accepted")
	}
}

func testCutoverInput(private, linked, archive string) cutoverInput {
	return cutoverInput{SchemaVersion: schemaVersion, ExpectedPrivateRepository: "abietic/yhc-private-history", ExpectedPublicRepository: "abietic/yhc", Mappings: []archiveMappingInput{{Kind: "main_checkout", Source: private, Destination: archive}, {Kind: "linked_worktree", Source: linked, Destination: filepath.Join(filepath.Dir(archive), "linked-archive")}}, Defaults: []classificationDefault{{Kind: "ref", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"}, {Kind: "worktree", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"}, {Kind: "dirty_path", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"}, {Kind: "stash", Classification: "private_recovery", Owner: "operator", RestoreDisposition: "preserve", ChecksumPolicy: "omit_sensitive"}}}
}

func inventoryReader(private, linked string) fakeGitReader {
	responses := map[string][]byte{}
	add := func(root, output string, args ...string) {
		responses[gitKey(root, args...)] = []byte(output)
	}
	for _, root := range []string{private, linked} {
		add(root, root+"\n", "rev-parse", "--show-toplevel")
		add(root, strings.Repeat("a", 40)+"\n", "rev-parse", "--verify", "HEAD")
		add(root, ".git\n", "rev-parse", "--git-common-dir")
	}
	add(linked, strings.Repeat("b", 40)+"\n", "rev-parse", "--verify", "HEAD")
	add(private, "git@github.com:abietic/yhc-private-history.git\n", "remote", "get-url", "--all", "origin")
	add(private, "refs/heads/main\x00"+strings.Repeat("a", 40)+"\x00\nrefs/tags/v1\x00"+strings.Repeat("b", 40)+"\x00\n", "for-each-ref", "--format=%(refname)%00%(objectname)%00")
	add(private, "worktree "+private+"\x00HEAD "+strings.Repeat("a", 40)+"\x00branch refs/heads/main\x00\x00worktree "+linked+"\x00HEAD "+strings.Repeat("b", 40)+"\x00detached\x00locked\x00\x00", "worktree", "list", "--porcelain", "-z")
	add(private, "R  new name\nfile\x00old name\x00 M space file\x00", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	add(linked, "", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	add(private, "main\n", "symbolic-ref", "--quiet", "--short", "HEAD")
	add(private, "stash@{0}\x00"+strings.Repeat("c", 40)+"\x001700000000\x00\n", "stash", "list", "--format=%gd%x00%H%x00%ct%x00")
	return fakeGitReader{responses: responses, errors: map[string]error{gitKey(linked, "symbolic-ref", "--quiet", "--short", "HEAD"): fakeGitCommandError{code: 1}}}
}

func decodePath(t *testing.T, encoded string) string {
	t.Helper()
	got, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func mustResolve(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
