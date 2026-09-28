package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileToolInput(t *testing.T, input map[string]any) string {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPartialReadPreservesKnownCompleteFile(t *testing.T) {
	for _, source := range []string{"write", "full-read"} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "long.txt")
			original := "unique target\n" + strings.Repeat("other line\n", 400)
			ctx := context.Background()
			if source == "write" {
				if _, err := WriteTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "content": original})); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "limit": 1000})); err != nil {
					t.Fatal(err)
				}
			}
			for _, input := range []map[string]any{{"file_path": path, "offset": 1, "limit": 20}, {"file_path": path}} {
				if _, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, input)); err != nil {
					t.Fatal(err)
				}
			}
			result, err := EditTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "old_string": "unique target", "new_string": "updated target"}))
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != strings.Replace(original, "unique target", "updated target", 1) {
				t.Fatalf("edit after partial reread did not update the file: %s", result)
			}
		})
	}
}

func TestFileReadGuardFailureIsAnErrorAndDoesNotMutate(t *testing.T) {
	for _, state := range []string{"unread", "partial"} {
		for _, name := range []string{"Edit", "Write"} {
			t.Run(state+"/"+name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "existing.txt")
				original := "first\nsecond\nthird\n"
				if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				if state == "partial" {
					if _, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path, "limit": 1})); err != nil {
						t.Fatal(err)
					}
				}
				tool := EditTool()
				input := map[string]any{"file_path": path, "old_string": "first", "new_string": "changed"}
				if name == "Write" {
					tool = WriteTool()
					input = map[string]any{"file_path": path, "content": "replacement"}
				}
				_, err := tool.ExecuteCtx(ctx, fileToolInput(t, input))
				if err == nil {
					t.Error("guard rejection must report a tool error")
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != original {
					t.Fatal("guard rejection changed the file")
				}
				if HasFileBeenRead(path) {
					t.Fatal("unread or partial-only file became fully read")
				}
			})
		}
	}
}
