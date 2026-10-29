package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRestoredForkInvalidationRequiresFreshReadForWriteAndEdit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "target.txt")
	logical := filepath.Join(root, "nested", "..", "target.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	owner := NewFileReadGuards()
	ownerContext := WithFileReadGuards(context.Background(), owner)
	if _, err := ReadTool().ExecuteCtx(ownerContext, fileToolInput(t, map[string]any{"file_path": path})); err != nil {
		t.Fatalf("initial actual read: %v", err)
	}
	snapshot, err := owner.Snapshot(root)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored, err := RestoreFileReadGuards(snapshot)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The default must not replace an explicitly restored owner.
	restoredContext := WithFileReadGuards(context.Background(), restored)
	if got := WithDefaultFileReadGuards(restoredContext, NewFileReadGuards()); BoundFileReadGuards(got) != restored {
		t.Fatal("default guards replaced explicit restored owner")
	}
	fork := ForkFileReadGuards(restoredContext, []string{root})
	if fork.m[path] == restored.m[path] {
		t.Fatal("fork retained an entry pointer from its source")
	}
	fork.Invalidate(logical, path)
	forkContext := WithFileReadGuards(context.Background(), fork)
	if _, err := WriteTool().ExecuteCtx(forkContext, fileToolInput(t, map[string]any{"file_path": path, "content": "write denied\n"})); err == nil {
		t.Fatal("invalidated restored fork allowed Write without a fresh Read")
	}
	if _, err := ReadTool().ExecuteCtx(forkContext, fileToolInput(t, map[string]any{"file_path": path})); err != nil {
		t.Fatalf("fresh read for write: %v", err)
	}
	if _, err := WriteTool().ExecuteCtx(forkContext, fileToolInput(t, map[string]any{"file_path": path, "content": "written\n"})); err != nil {
		t.Fatalf("write after fresh read: %v", err)
	}
	assertReadGuardBytes(t, path, "written\n")

	fork.Invalidate(logical, path)
	if _, err := EditTool().ExecuteCtx(forkContext, fileToolInput(t, map[string]any{"file_path": path, "old_string": "written", "new_string": "edit denied"})); err == nil {
		t.Fatal("invalidated restored fork allowed Edit without a fresh Read")
	}
	if _, err := ReadTool().ExecuteCtx(forkContext, fileToolInput(t, map[string]any{"file_path": path})); err != nil {
		t.Fatalf("fresh read for edit: %v", err)
	}
	if _, err := EditTool().ExecuteCtx(forkContext, fileToolInput(t, map[string]any{"file_path": path, "old_string": "written", "new_string": "edited"})); err != nil {
		t.Fatalf("edit after fresh read: %v", err)
	}
	assertReadGuardBytes(t, path, "edited\n")
}

func TestForkReadGuardsLeavesLegacyCreditAndBoundsCopy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "invalidated.txt")
	unaffected := filepath.Join(root, "unaffected.txt")
	for _, target := range []string{path, unaffected} {
		if err := os.WriteFile(target, []byte("legacy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		RecordFileRead(target, false)
	}
	t.Cleanup(func() {
		readFileState.Lock()
		delete(readFileState.m, path)
		delete(readFileState.m, unaffected)
		readFileState.Unlock()
	})

	fork := ForkFileReadGuards(context.Background(), []string{root})
	fork.Invalidate(path)
	if _, err := WriteTool().ExecuteCtx(WithFileReadGuards(context.Background(), fork), fileToolInput(t, map[string]any{"file_path": path, "content": "fork denied\n"})); err == nil {
		t.Fatal("invalidated fork retained legacy credit")
	}
	if _, err := WriteTool().ExecuteCtx(WithFileReadGuards(context.Background(), fork), fileToolInput(t, map[string]any{"file_path": unaffected, "content": "fork preserved\n"})); err != nil {
		t.Fatalf("fork lost unaffected legacy credit: %v", err)
	}
	if _, err := WriteTool().ExecuteCtx(context.Background(), fileToolInput(t, map[string]any{"file_path": path, "content": "legacy preserved\n"})); err != nil {
		t.Fatalf("fork invalidation changed process-global legacy credit: %v", err)
	}
	assertReadGuardBytes(t, path, "legacy preserved\n")
	assertReadGuardBytes(t, unaffected, "fork preserved\n")

	large := NewFileReadGuards()
	large.Lock()
	for i := 0; i <= maxReadGuardEntries; i++ {
		large.m[filepath.Join(root, fmt.Sprintf("entry-%d", i))] = &ReadFileEntry{}
	}
	large.Unlock()
	if got := ForkFileReadGuards(WithFileReadGuards(context.Background(), large), []string{root}); len(got.m) != 0 || got.root != "" || got.restored {
		t.Fatalf("oversize source produced non-fresh fork: %#v", got)
	}
}

func TestForkReadGuardsConcurrentUseIsRaceSafe(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "race.txt")
	owner := NewFileReadGuards()
	owner.m[path] = &ReadFileEntry{}
	ctx := WithFileReadGuards(context.Background(), owner)

	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				fork := ForkFileReadGuards(ctx, []string{root})
				fork.Invalidate(path)
				_ = hasFileBeenReadContext(WithFileReadGuards(context.Background(), fork), path)
			}
		}()
	}
	workers.Wait()
}

func assertReadGuardBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("file bytes = %q, want %q", got, want)
	}
}
