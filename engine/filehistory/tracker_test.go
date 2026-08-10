package filehistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotBeforePreservesOriginalAndRevertRestoresFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	originalModTime := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.Chtimes(path, originalModTime, originalModTime); err != nil {
		t.Fatal(err)
		return
	}

	tracker := NewFileTracker(dir)
	if err := tracker.SnapshotBefore("nested/file.txt"); err != nil {
		t.Fatalf("SnapshotBefore failed: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte("first edit\n"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := tracker.SnapshotBefore("nested/file.txt"); err != nil {
		t.Fatalf("second SnapshotBefore failed: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte("second edit\n"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	tracker.RecordChange("nested/file.txt", "edit", "Write", 7)
	changes := tracker.GetChanges()
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %#v", changes)
	}
	if changes[0].OriginalHash != fileHash([]byte("original\n")) {
		t.Fatalf("original hash not preserved: %#v", changes[0])
	}
	if changes[0].NewHash != fileHash([]byte("second edit\n")) {
		t.Fatalf("new hash not captured: %#v", changes[0])
	}

	if !tracker.CanRevert("nested/file.txt") {
		t.Fatal("expected snapshot to be revertible")
	}
	if err := tracker.Revert("nested/file.txt"); err != nil {
		t.Fatalf("Revert failed: %v", err)
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
		return
	}
	if string(content) != "original\n" {
		t.Fatalf("unexpected reverted content: %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
		return
	}
	if !info.ModTime().Equal(originalModTime) {
		t.Fatalf("mod time was not restored: %s", info.ModTime())
	}
}

func TestSnapshotForNewFileRevertDeletesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	tracker := NewFileTracker(dir)
	if err := tracker.SnapshotBefore("new.txt"); err != nil {
		t.Fatalf("SnapshotBefore failed: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	tracker.RecordChange("new.txt", "create", "Write", 3)
	if len(tracker.GetChangedFiles()) != 1 || tracker.GetChangedFiles()[0] != path {
		t.Fatalf("unexpected changed files: %#v", tracker.GetChangedFiles())
	}

	if err := tracker.Revert("new.txt"); err != nil {
		t.Fatalf("Revert failed: %v", err)
		return
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected created file to be deleted, err=%v", err)
	}
}

func TestRevertAllUsesStableOrderAndCollectsErrors(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.txt")
	remove := filepath.Join(dir, "remove.txt")
	if err := os.WriteFile(keep, []byte("keep-original"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	tracker := NewFileTracker(dir)
	if err := tracker.SnapshotBefore("keep.txt"); err != nil {
		t.Fatal(err)
		return
	}
	if err := tracker.SnapshotBefore("remove.txt"); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(keep, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(remove, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	errs := tracker.RevertAll()
	if len(errs) != 0 {
		t.Fatalf("unexpected revert errors: %#v", errs)
	}
	content, err := os.ReadFile(keep)
	if err != nil {
		t.Fatal(err)
		return
	}
	if string(content) != "keep-original" {
		t.Fatalf("keep not restored: %q", content)
	}
	if _, err := os.Stat(remove); !os.IsNotExist(err) {
		t.Fatalf("remove should be absent after revert, err=%v", err)
	}
}

func TestSummaryExportAndResumeCopyAreStableCopies(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(b, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	tracker := NewFileTracker(dir)
	for _, name := range []string{"b.txt", "a.txt"} {
		if err := tracker.SnapshotBefore(name); err != nil {
			t.Fatal(err)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+" changed"), 0o644); err != nil {
			t.Fatal(err)
			return
		}
		tracker.RecordChange(name, "edit", "Edit", 1)
	}

	summary := tracker.Summary()
	firstA := strings.Index(summary, a)
	firstB := strings.Index(summary, b)
	if firstA == -1 || firstB == -1 || firstA > firstB {
		t.Fatalf("summary should list files in sorted order, got:\n%s", summary)
	}

	resume := tracker.CopyForResume()
	resume[0].Operation = "mutated"
	if tracker.GetChanges()[0].Operation == "mutated" {
		t.Fatal("CopyForResume returned mutable internal storage")
	}

	exportPath := filepath.Join(dir, "out", "changes.json")
	if err := tracker.Export(exportPath); err != nil {
		t.Fatalf("Export failed: %v", err)
		return
	}
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
		return
	}
	var exported []FileChange
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatalf("export was not valid JSON: %v", err)
		return
	}
	if len(exported) != 2 || exported[0].Path != b || exported[1].Path != a {
		t.Fatalf("unexpected exported changes: %#v", exported)
	}
}

func TestAbsolutePathsAreCleanedAndUnknownRevertFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "..", "file.txt")
	clean := filepath.Clean(path)
	if err := os.WriteFile(clean, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	tracker := NewFileTracker(dir)
	if err := tracker.SnapshotBefore(path); err != nil {
		t.Fatal(err)
		return
	}
	snapshot := tracker.GetSnapshot(clean)
	if snapshot == nil || snapshot.Path != clean {
		t.Fatalf("expected clean absolute snapshot, got %#v", snapshot)
		return
	}

	if err := tracker.Revert("missing.txt"); err == nil {
		t.Fatal("expected missing snapshot revert to fail")
		return
	}
}
