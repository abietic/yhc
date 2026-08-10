package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifestFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func manifestWithEntry(entry FileEntry) *Manifest {
	return &Manifest{
		Version: 4,
		Summary: Summary{ReferenceFiles: 1, LedgerFiles: 1},
		Files:   []FileEntry{entry},
	}
}

func TestLedgerReferenceFilesUsesManifestInventory(t *testing.T) {
	manifest := &Manifest{Files: []FileEntry{
		{Path: "src/A.ts"},
		{Path: "src/B.tsx"},
	}}
	got := ledgerReferenceFiles(manifest)
	if strings.Join(got, ",") != "src/A.ts,src/B.tsx" {
		t.Fatalf("ledger reference files = %#v", got)
	}
	got[0] = "mutated"
	if manifest.Files[0].Path != "src/A.ts" {
		t.Fatal("ledger reference files alias manifest storage")
	}
	if got := ledgerReferenceFiles(nil); got != nil {
		t.Fatalf("nil manifest files = %#v, want nil", got)
	}
}

func TestResolveReferenceRepoPathUsesExternalReferenceRoot(t *testing.T) {
	repoRoot := t.TempDir()
	referenceRoot := t.TempDir()

	got, err := resolveReferenceRepoPath(repoRoot, ".reference/claude-code-ripe", referenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(referenceRoot, "claude-code-ripe")
	if got != want {
		t.Fatalf("reference repo path = %q, want %q", got, want)
	}
}

func TestResolveReferenceRepoPathDefaultsToRepositoryRoot(t *testing.T) {
	repoRoot := t.TempDir()

	got, err := resolveReferenceRepoPath(repoRoot, ".reference/claude-code-ripe", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repoRoot, ".reference", "claude-code-ripe")
	if got != want {
		t.Fatalf("reference repo path = %q, want %q", got, want)
	}
}

func TestResolveReferenceRepoPathResolvesRelativeExternalRootFromRepositoryRoot(t *testing.T) {
	repoRoot := t.TempDir()

	got, err := resolveReferenceRepoPath(repoRoot, ".reference/claude-code-ripe", "external-references")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repoRoot, "external-references", "claude-code-ripe")
	if got != want {
		t.Fatalf("reference repo path = %q, want %q", got, want)
	}
}

func TestResolveReferenceRepoPathRejectsPathsOutsideReferenceNamespace(t *testing.T) {
	for _, repo := range []string{
		"../claude-code-ripe",
		".reference/../claude-code-ripe",
		".reference/nested/../../claude-code-ripe",
		".reference/",
	} {
		t.Run(repo, func(t *testing.T) {
			_, err := resolveReferenceRepoPath(t.TempDir(), repo, t.TempDir())
			if err == nil {
				t.Fatalf("accepted reference override %q outside .reference namespace", repo)
			}
			if !strings.Contains(err.Error(), ".reference") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRejectsRetiredTUIPathMarkerInTargets(t *testing.T) {
	root := t.TempDir()
	entry := FileEntry{
		Path:    "src/Foo.ts",
		Scope:   "required",
		Status:  "done",
		Targets: []string{"internal/tui/components/foo.go"},
		Tests:   []string{"engine/foo_test.go"},
	}
	writeManifestFixture(t, root, entry.Targets[0], "")
	writeManifestFixture(t, root, entry.Tests[0], "")

	err := validate(manifestWithEntry(entry), root, []string{"src/Foo.ts"})
	if err == nil {
		t.Fatal("expected error for retired TUI path marker in targets")
	}
	if !strings.Contains(err.Error(), "src/Foo.ts: targets contains retired TUI path marker \"internal/tui/components\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsRetiredTUIPathMarkerInTests(t *testing.T) {
	root := t.TempDir()
	entry := FileEntry{
		Path:    "src/Bar.ts",
		Scope:   "required",
		Status:  "done",
		Targets: []string{"engine/bar.go"},
		Tests:   []string{"internal/tui/rendering/bar_test.go"},
	}
	writeManifestFixture(t, root, entry.Targets[0], "")
	writeManifestFixture(t, root, entry.Tests[0], "")

	err := validate(manifestWithEntry(entry), root, []string{"src/Bar.ts"})
	if err == nil {
		t.Fatal("expected error for retired TUI path marker in tests")
	}
	if !strings.Contains(err.Error(), "src/Bar.ts: tests contains retired TUI path marker \"internal/tui/rendering\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsRetiredTUIPathMarkerInNotes(t *testing.T) {
	root := t.TempDir()
	entry := FileEntry{
		Path:   "src/Baz.ts",
		Scope:  "required",
		Status: "partial",
		Notes:  "Ported to internal/tui/input/baz.go",
	}

	err := validate(manifestWithEntry(entry), root, []string{"src/Baz.ts"})
	if err == nil {
		t.Fatal("expected error for retired TUI path marker in notes")
	}
	if !strings.Contains(err.Error(), "src/Baz.ts: notes contains retired TUI path marker \"internal/tui/input\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAcceptsCleanEntry(t *testing.T) {
	root := t.TempDir()
	entry := FileEntry{
		Path:    "src/Clean.ts",
		Scope:   "required",
		Status:  "done",
		Targets: []string{"engine/clean.go"},
		Tests:   []string{"engine/clean_test.go"},
		Notes:   "No retired TUI paths here.",
	}
	writeManifestFixture(t, root, entry.Targets[0], "")
	writeManifestFixture(t, root, entry.Tests[0], "")

	err := validate(manifestWithEntry(entry), root, []string{"src/Clean.ts"})
	if err != nil {
		t.Fatalf("expected clean entry to pass, got: %v", err)
	}
}
