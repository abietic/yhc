package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestStoreCompleteBelowInlineThreshold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private output-directory mode is not qualified on Windows")
	}
	store := NewResultStorage(t.TempDir())
	parts := []string{"prefix\n", strings.Repeat("🙂", 3000), "\nMIDDLE_DIAGNOSTIC\n", strings.Repeat("x", 12000)}
	want := strings.Join(parts, "")
	if store.ShouldStore(want) {
		t.Fatal("fixture exceeds the generic persistence threshold")
	}
	stored, err := store.StoreComplete("Bash", parts...)
	if err != nil || stored == nil {
		t.Fatalf("StoreComplete failed: %v", err)
	}
	got, err := os.ReadFile(stored.FilePath)
	info, statErr := os.Stat(stored.FilePath)
	if err != nil || statErr != nil || string(got) != want || info.Mode().Perm() != 0o600 {
		t.Fatal("complete output was not privately retained byte-for-byte")
	}
	if stored.FullSize != len(want) || !utf8.ValidString(stored.Preview) || store.Stats().Count != 1 {
		t.Fatal("complete output metadata is incorrect")
	}
	if got, err := store.Retrieve(stored.ID); err != nil || got != want {
		t.Fatal("complete output did not use existing result storage")
	}
}

func TestStoreCompleteRejectsOversizeBeforeCreatingDirectory(t *testing.T) {
	root := t.TempDir()
	store := NewResultStorage(root)
	part := strings.Repeat("x", 1<<20)
	parts := make([]string, 65)
	for i := range parts {
		parts[i] = part
	}
	if stored, err := store.StoreComplete("Bash", parts...); err == nil || stored != nil {
		t.Fatal("oversize output was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, toolResultsSubdir)); !os.IsNotExist(err) {
		t.Fatal("oversize output performed storage I/O")
	}
}

func TestStoreCompleteRejectsLinkedDirectory(t *testing.T) {
	root, foreign := t.TempDir(), t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(root, toolResultsSubdir)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if stored, err := NewResultStorage(root).StoreComplete("Bash", "private output"); err == nil || stored != nil {
		t.Fatal("linked result directory was accepted")
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatal("linked directory received output")
	}
}

func TestStoreCompleteConcurrentResultsDoNotOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private output-directory mode is not qualified on Windows")
	}
	store := NewResultStorage(t.TempDir())
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			<-start
			want := strings.Repeat("x", i+1)
			stored, err := store.StoreComplete("Bash", want)
			if err != nil {
				t.Errorf("StoreComplete: %v", err)
				return
			}
			got, err := os.ReadFile(stored.FilePath)
			if err != nil || string(got) != want {
				t.Error("concurrent output was overwritten")
			}
		})
	}
	close(start)
	workers.Wait()
	if store.Stats().Count != 8 {
		t.Fatal("concurrent result metadata was lost")
	}
}

func TestStoreCompleteRejectsDirectoryReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private output-directory mode is not qualified on Windows")
	}
	for _, stage := range []string{"before-open", "before-publication"} {
		t.Run(stage, func(t *testing.T) {
			root, foreign := t.TempDir(), t.TempDir()
			store := NewResultStorage(root)
			retired := filepath.Join(root, "retired-output")
			swap := func() {
				if err := os.Rename(filepath.Join(root, toolResultsSubdir), retired); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, filepath.Join(root, toolResultsSubdir)); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "before-open" {
				store.beforeCompleteRootOpenForTest = swap
			} else {
				store.beforeCompletePublishForTest = swap
			}
			if stored, err := store.StoreComplete("Bash", "private output"); err == nil || stored != nil {
				t.Fatal("replaced directory produced an output receipt")
			}
			for _, path := range []string{foreign, retired} {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatal("replacement left output or wrote into another directory")
				}
			}
			if store.Stats().Count != 0 {
				t.Fatal("failed publication entered the result index")
			}
		})
	}
}

func TestStoreCompleteRejectsFileReplacementWithoutRemovingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private output-directory mode is not qualified on Windows")
	}
	root := t.TempDir()
	store := NewResultStorage(root)
	var replacement string
	store.beforeCompletePublishForTest = func() {
		directory := filepath.Join(root, toolResultsSubdir)
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 {
			t.Fatal("missing unpublished output")
		}
		replacement = filepath.Join(directory, entries[0].Name())
		if err := os.Rename(replacement, replacement+".created"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(replacement, []byte("replacement-owned-by-other-writer"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if stored, err := store.StoreComplete("Bash", "private output"); err == nil || stored != nil {
		t.Fatal("replacement file produced a receipt")
	}
	data, err := os.ReadFile(replacement)
	if err != nil || string(data) != "replacement-owned-by-other-writer" || store.Stats().Count != 0 {
		t.Fatal("abort removed a replacement file or published stale metadata")
	}
}
