package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestColdReadGuardsRestoreExactContentAndRetainPartialDenials(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("verified cold file guards require Linux or Darwin")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full, partial := filepath.Join(root, "full.txt"), filepath.Join(root, "partial.txt")
	state := NewFileReadGuards()
	ctx := WithFileReadGuards(context.Background(), state)
	if _, err := WriteTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": full, "content": "before\n"})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": partial, "limit": 1})); err != nil {
		t.Fatal(err)
	}
	if HasFileBeenRead(full) {
		t.Fatal("scoped write contaminated process-global guard")
	}
	snapshot, err := state.Snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreFileReadGuards(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cold := WithFileReadGuards(context.Background(), restored)
	if _, err := EditTool().ExecuteCtx(cold, fileToolInput(t, map[string]any{"file_path": full, "old_string": "before", "new_string": "after"})); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteTool().ExecuteCtx(cold, fileToolInput(t, map[string]any{"file_path": partial, "content": "forbidden"})); err == nil {
		t.Fatal("partial-only guard became complete")
	}
	if _, err := RestoreFileReadGuards(snapshot); err == nil {
		t.Fatal("changed file accepted against old snapshot")
	}
	if err := os.WriteFile(full, []byte("external change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EditTool().ExecuteCtx(cold, fileToolInput(t, map[string]any{"file_path": full, "old_string": "external", "new_string": "forbidden"})); err == nil {
		t.Fatal("restored guard ignored post-restore change")
	}
}

func TestColdReadGuardsNeverManufactureCreditFromChangedPartialViewOrLegacyOwner(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("verified cold file guards require Linux or Darwin")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "data.txt")
	owner := NewFileReadGuards()
	ctx := WithFileReadGuards(context.Background(), owner)
	if _, err := WriteTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "content": "one\ntwo\n"})); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "limit": 1})); err != nil {
		t.Fatal(err)
	}
	snapshot, err := owner.Snapshot(root)
	if err != nil || snapshot.Entries[0].IsPartial {
		t.Fatalf("unchanged full credit lost: %+v %v", snapshot, err)
	}
	if err := os.WriteFile(path, []byte("changed\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "limit": 1})); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Snapshot(root); err == nil {
		t.Fatal("partial reread blessed externally changed full-read bytes")
	}
	RecordFileRead(path, false)
	isolated := WithFileReadGuards(context.Background(), NewFileReadGuards())
	if _, err := EditTool().ExecuteCtx(isolated, fileToolInput(t, map[string]any{"file_path": path, "old_string": "changed", "new_string": "forbidden"})); err == nil {
		t.Fatal("fresh scoped owner inherited global read credit")
	}
}

func TestColdReadGuardSnapshotsRejectMalformedEntriesAndLinks(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("verified cold file guards require Linux or Darwin")
	}
	for _, raw := range []string{`{"path":"x","sha256":"a","size":0}`, `{"path":"x","sha256":"a","is_partial":false}`, `{"path":"x","sha256":"a","size":0,"is_partial":false,"future":true}`} {
		var entry ReadGuardEntry
		if json.Unmarshal([]byte(raw), &entry) == nil {
			t.Fatalf("unverified entry accepted: %s", raw)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "owned.txt")
	owner := NewFileReadGuards()
	ctx := WithFileReadGuards(context.Background(), owner)
	if _, err := WriteTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "content": "bytes"})); err != nil {
		t.Fatal(err)
	}
	snapshot, err := owner.Snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreFileReadGuards(snapshot); err == nil {
		t.Fatal("hard-linked file accepted")
	}
	if err := os.Remove(filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+"-real", path); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreFileReadGuards(snapshot); err == nil {
		t.Fatal("symbolic link accepted")
	}
	if _, err := RestoreFileReadGuards(ReadGuardSnapshot{Version: 1, CWD: path + "-real", Entries: []ReadGuardEntry{}}); err == nil {
		t.Fatal("regular file accepted as empty snapshot root")
	}
}
