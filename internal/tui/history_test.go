package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

func TestHistoryWritesCanonicalAndLeavesClaudeCompatibilityInPlace(t *testing.T) {
	project := t.TempDir()
	legacy := filepath.Join(project, ".eino-agent", "history")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyContents := []byte("legacy entry\n")
	if err := os.WriteFile(legacy, legacyContents, 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(project)
	locations, err := defaultHistoryLocations()
	if err != nil {
		t.Fatal(err)
	}

	entry := "first line\nsecond\\line"
	if err := saveHistoryEntryAt(locations, entry); err != nil {
		t.Fatalf("saveHistoryEntryAt() = %v", err)
	}

	canonical := filepath.Join(project, ".yhc", "history")
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("canonical history = %v", err)
	}
	if want := "first line\\nsecond\\\\line\n"; string(got) != want {
		t.Fatalf("canonical history = %q, want %q", got, want)
	}
	if got, err := os.ReadFile(legacy); err != nil || !bytes.Equal(got, legacyContents) {
		t.Fatalf("legacy history changed: %q, %v", got, err)
	}
	for path, wantMode := range map[string]os.FileMode{
		filepath.Dir(canonical): 0o700,
		canonical:               0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Fatalf("mode for %s = %#o, want %#o", path, got, wantMode)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "history.jsonl")); err != nil {
		t.Fatalf("Claude compatibility history missing: %v", err)
	}
}

func TestHistoryWrappersUseResolvedLocations(t *testing.T) {
	project := t.TempDir()
	compatibilityConfigDir := t.TempDir()
	originalResolver := resolveHistoryLocations
	resolveHistoryLocations = func() (historyLocations, error) {
		return historyLocations{
			projectRoot:            project,
			compatibilityConfigDir: compatibilityConfigDir,
		}, nil
	}
	t.Cleanup(func() { resolveHistoryLocations = originalResolver })

	const entry = "wrapper history entry"
	if err := saveHistoryEntry(entry); err != nil {
		t.Fatal(err)
	}
	if got := loadHistory(); len(got) != 1 || got[0] != entry {
		t.Fatalf("loadHistory() = %v, want [%q]", got, entry)
	}
	for _, name := range []string{
		filepath.Join(project, ".yhc", historyFileName),
		filepath.Join(compatibilityConfigDir, "history.jsonl"),
	} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("resolved history path %q: %v", name, err)
		}
	}
}

func TestHistoryMigrationPreservesOrderAndBounds(t *testing.T) {
	roots := newHistoryMigrationRoots(t)
	var source strings.Builder
	for index := range maxHistoryLines + 2 {
		source.WriteString("entry-")
		source.WriteString(string(rune('a' + index%26)))
		source.WriteString("-")
		source.WriteString(strings.Repeat("x", index/26+1))
		source.WriteString("\\nline\\\\tail\n")
	}
	legacy := filepath.Join(roots.Legacy, historyFileName)
	if err := os.WriteFile(legacy, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyBefore, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}

	result, err := (statemigration.Importer{}).Import(
		t.Context(), roots, HistoryMigrationSpec(),
	)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	canonical := filepath.Join(roots.Canonical, historyFileName)
	data, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseHistoryEntries(data)
	if err != nil {
		t.Fatalf("parse canonical history: %v", err)
	}
	if len(entries) != maxHistoryLines {
		t.Fatalf("entries = %d, want %d", len(entries), maxHistoryLines)
	}
	if entries[0] != "entry-c-x\nline\\tail" || entries[len(entries)-1] != "entry-h-xxxxxxxxxxxxxxxxxxxx\nline\\tail" {
		t.Fatalf("order/bounds = first %q, last %q", entries[0], entries[len(entries)-1])
	}
	if got, err := os.ReadFile(legacy); err != nil || !bytes.Equal(got, legacyBefore) {
		t.Fatalf("legacy source changed: %q, %v", got, err)
	}
	for path, wantMode := range map[string]os.FileMode{
		roots.Canonical: 0o700,
		canonical:       0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != wantMode {
			t.Fatalf("mode for %s = %#o, want %#o", path, got, wantMode)
		}
	}

	for _, test := range []struct {
		name   string
		source []byte
	}{
		{name: "malformed escape", source: []byte("bad\\q\n")},
		{name: "NUL", source: []byte("bad\x00\n")},
		{name: "invalid UTF-8", source: []byte{'b', 0xff, '\n'}},
		{name: "oversized", source: []byte(strings.Repeat("x", maxHistoryLineBytes+1) + "\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalidRoots := newHistoryMigrationRoots(t)
			if err := os.WriteFile(filepath.Join(invalidRoots.Legacy, historyFileName), test.source, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := (statemigration.Importer{}).Import(
				t.Context(), invalidRoots, HistoryMigrationSpec(),
			)
			if err == nil || result.Status != statemigration.StatusUnsafe {
				t.Fatalf("invalid Import() = %#v, %v", result, err)
			}
			if _, statErr := os.Lstat(filepath.Join(invalidRoots.Canonical, historyFileName)); !os.IsNotExist(statErr) {
				t.Fatalf("invalid import created a destination: %v", statErr)
			}
		})
	}
}

func TestHistoryRejectsSymlinkCanonicalRoot(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Symlink(outside, filepath.Join(project, ".yhc")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Chdir(project)
	locations, err := defaultHistoryLocations()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveHistoryEntryAt(locations, "must stay inside canonical state"); err == nil {
		t.Fatal("saveHistoryEntryAt accepted a symlink canonical root")
	}
	if _, err := os.Lstat(filepath.Join(outside, historyFileName)); !os.IsNotExist(err) {
		t.Fatalf("history escaped canonical root: %v", err)
	}
}

func newHistoryMigrationRoots(t *testing.T) statepath.Roots {
	t.Helper()
	root := t.TempDir()
	roots := statepath.Roots{
		Canonical: filepath.Join(root, ".yhc"),
		Legacy:    filepath.Join(root, ".eino-agent"),
	}
	if err := os.Mkdir(roots.Legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	return roots
}
