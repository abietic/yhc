package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadPagesEditGuard(t *testing.T) {
	requirePagedReadVerification(t)
	type page struct{ offset, limit int }
	for _, tc := range []struct {
		name         string
		lines        int
		pages        []page
		changeBefore int
		changeAfter  bool
		clippedLine  int
		allow        bool
	}{
		{name: "overlapping-111-lines", lines: 111, pages: []page{{0, 80}, {75, 36}}, allow: true},
		{name: "above-single-read-limit", lines: 2001, pages: []page{{0, 2000}, {2000, 1}}, allow: true},
		{name: "out-of-order", lines: 8, pages: []page{{5, 3}, {0, 2}, {2, 3}}, allow: true},
		{name: "gap", lines: 8, pages: []page{{0, 3}, {4, 4}}},
		{name: "changed-between-pages", lines: 8, pages: []page{{0, 4}, {4, 4}}, changeBefore: 1},
		{name: "changed-after-pages", lines: 8, pages: []page{{0, 4}, {4, 4}}, changeAfter: true},
		{name: "truncated-line", lines: 8, pages: []page{{0, 4}, {4, 4}}, clippedLine: 2},
	} {
		for _, scoped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/scoped=%t", tc.name, scoped), func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "source.txt")
				lines := make([]string, tc.lines)
				for i := range lines {
					lines[i] = fmt.Sprintf("line %d", i+1)
				}
				lines[0] = "unique edit target"
				if tc.clippedLine > 0 {
					lines[tc.clippedLine] = strings.Repeat("x", 2100)
				}
				content := strings.Join(lines, "\n")
				write := func() {
					t.Helper()
					if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				write()
				change := func() {
					content = strings.Replace(content, "line 2", "modified externally", 1)
					write()
				}
				ctx := WithSessionID(t.Context(), t.Name())
				if scoped {
					ctx = WithFileReadGuards(ctx, NewFileReadGuards())
				}
				for i, p := range tc.pages {
					if tc.changeBefore > 0 && i == tc.changeBefore {
						change()
					}
					result := readPage(t, ctx, path, p.offset, p.limit, tc.lines)
					complete := strings.Contains(result, "[Read coverage complete across calls for this file.]")
					if complete != (i == len(tc.pages)-1 && (tc.allow || tc.changeAfter)) {
						t.Error("coverage notice did not match the pages shown")
					}
				}
				if tc.changeAfter {
					change()
				}
				_, err = EditTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
					"file_path": path, "old_string": "unique edit target", "new_string": "updated",
				}))
				if tc.allow {
					content = strings.Replace(content, "unique edit target", "updated", 1)
				}
				actual, readErr := os.ReadFile(path)
				if readErr != nil || string(actual) != content {
					t.Errorf("actual file differs from the expected mutation outcome: read error %v", readErr)
				}
				if (err == nil) != tc.allow {
					t.Errorf("edit allowed=%t want=%t: %v", err == nil, tc.allow, err)
				}
			})
		}
	}
}

func TestPagedReadWriteGuardsRemainSessionScoped(t *testing.T) {
	requirePagedReadVerification(t)
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := WithSessionID(t.Context(), "reader-session")
	other := WithSessionID(t.Context(), "other-session")
	writeInput := fileToolInput(t, map[string]any{"file_path": path, "content": "replacement"})
	readPage(t, reader, path, 0, 2, 3)
	readPage(t, other, path, 2, 1, 3)
	if _, err := WriteTool().ExecuteCtx(other, writeInput); err == nil {
		t.Fatal("write admitted with pages from different sessions")
	}
	readPage(t, reader, path, 0, 2, 3)
	readPage(t, reader, path, 2, 1, 3)
	if _, err := WriteTool().ExecuteCtx(other, writeInput); err == nil {
		t.Fatal("write reused another session's verified page coverage")
	}
	if _, err := WriteTool().ExecuteCtx(reader, writeInput); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatal("same-session complete evidence did not permit overwrite")
	}
}

func TestPagedReadDoesNotMixAgentIdentitiesInSharedOwner(t *testing.T) {
	requirePagedReadVerification(t)
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := WithFileReadGuards(WithSessionID(t.Context(), "shared-session"), NewFileReadGuards())
	first := WithAgentID(WithThreadID(owner, "one-thread"), "one-agent")
	other := WithAgentID(WithThreadID(owner, "other-thread"), "other-agent")
	readPage(t, first, path, 0, 2, 3)
	readPage(t, other, path, 2, 1, 3)
	if hasFileBeenReadContext(other, path) {
		t.Fatal("another Agent's partial read supplied page coverage")
	}
	readPage(t, first, path, 0, 2, 3)
	readPage(t, first, path, 2, 1, 3)
	if !hasFileBeenReadContext(first, path) || hasFileBeenReadContext(other, path) {
		t.Fatal("completed coverage escaped the original Thread and Agent")
	}
}

func TestIncompleteReadPagesDoNotSurviveColdRestore(t *testing.T) {
	requirePagedReadVerification(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := NewFileReadGuards()
	ctx := WithFileReadGuards(t.Context(), owner)
	readPage(t, ctx, path, 0, 2, 3)
	snapshot, err := owner.Snapshot(root)
	if err != nil || len(snapshot.Entries) != 1 || !snapshot.Entries[0].IsPartial {
		t.Fatalf("incomplete page coverage was not captured as partial: %v", err)
	}
	fresh, err := RestoreFileReadGuards(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cold := WithFileReadGuards(t.Context(), fresh)
	readPage(t, cold, path, 2, 1, 3)
	if hasFileBeenReadContext(cold, path) {
		t.Fatal("recovered partial state contributed a previously read range")
	}
	readPage(t, cold, path, 0, 2, 3)
	if !hasFileBeenReadContext(cold, path) {
		t.Fatal("all pages were observed again in the restored owner")
	}
}

func readPage(t *testing.T, ctx context.Context, path string, offset, limit, total int) string {
	t.Helper()
	text, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
		"file_path": path, "offset": offset, "limit": limit,
	}))
	want := fmt.Sprintf("[Partial Read: lines %d-%d of %d.]", offset+1, offset+limit, total)
	if err != nil || !strings.Contains(text, want) {
		t.Fatalf("Read did not report the expected range %s: %v", want, err)
	}
	return text
}

func requirePagedReadVerification(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("paged reads require verified file evidence")
	}
}
