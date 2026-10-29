package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCoverageFeedbackMatchesEditGuard(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lines      int
		params     map[string]any
		priorFull  bool
		wantNotice string
		canEdit    bool
	}{
		{name: "default-full", lines: 3, canEdit: true},
		{name: "explicit-full", lines: 3, params: map[string]any{"offset": 0, "limit": 10}, canEdit: true},
		{name: "short-range", lines: 3, params: map[string]any{"limit": 1}, wantNotice: "[Partial Read: lines 1-1 of 3.]"},
		{name: "offset-is-zero-based", lines: 3, params: map[string]any{"offset": 2}, wantNotice: "[Partial Read: lines 3-3 of 3.]"},
		{name: "default-window", lines: 301, wantNotice: "[Partial Read: lines 1-250 of 301.]"},
		{name: "centered-window", lines: 301, params: map[string]any{"line": 280, "limit": 10}, wantNotice: "[Partial Read: lines 275-284 of 301.]"},
		{name: "hard-limit", lines: 2001, params: map[string]any{"limit": 5000}, wantNotice: "[Partial Read: lines 1-2000 of 2001.]"},
		{name: "partial-after-full", lines: 3, priorFull: true, params: map[string]any{"limit": 1}, wantNotice: "[Partial Read: lines 1-1 of 3.]", canEdit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.txt")
			lines := make([]string, tc.lines)
			for i := range lines {
				lines[i] = "ordinary fixture line"
			}
			lines[0] = "unique edit target"
			original := strings.Join(lines, "\n")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.priorFull {
				if _, err := ReadTool().ExecuteCtx(t.Context(), fileToolInput(t, map[string]any{"file_path": path})); err != nil {
					t.Fatal(err)
				}
			}
			input := map[string]any{"file_path": path}
			for key, value := range tc.params {
				input[key] = value
			}
			result, err := ReadTool().ExecuteCtx(t.Context(), fileToolInput(t, input))
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantNotice != "" {
				if !strings.HasSuffix(result, "\n"+tc.wantNotice+"\n") {
					t.Errorf("Read omitted the actual partial-window notice: want %s", tc.wantNotice)
				}
			} else if strings.Contains(result, "[Partial Read:") {
				t.Error("complete line coverage was reported as partial")
			}
			if HasFileBeenRead(path) != tc.canEdit {
				t.Errorf("complete read state=%t want=%t", HasFileBeenRead(path), tc.canEdit)
			}
			_, err = EditTool().ExecuteCtx(t.Context(), fileToolInput(t, map[string]any{
				"file_path": path, "old_string": "unique edit target", "new_string": "updated edit target",
			}))
			if (err == nil) != tc.canEdit {
				t.Errorf("edit allowed=%t want=%t", err == nil, tc.canEdit)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := original
			if tc.canEdit {
				want = strings.Replace(original, "unique edit target", "updated edit target", 1)
			}
			if string(data) != want {
				t.Fatal("file content does not match the edit admission result")
			}
		})
	}
}
