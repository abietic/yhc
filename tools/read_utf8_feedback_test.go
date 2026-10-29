package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadLongUnicodeLineRemainsExactEditEvidence(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{name: "two-byte", source: "x" + strings.Repeat("é", 1000)},
		{name: "three-byte", source: strings.Repeat("界", 668)},
		{name: "four-byte", source: "x" + strings.Repeat("🙂", 500)},
		{name: "boundary", source: strings.Repeat("x", 1999) + "🙂"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte(tc.source), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := WithFileReadGuards(t.Context(), NewFileReadGuards())
			result, err := ReadTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"file_path": path}))
			if err != nil {
				t.Fatal(err)
			}
			if !utf8.ValidString(result) {
				t.Error("Read split a UTF-8 rune at its per-line byte limit")
			}
			_, text, ok := strings.Cut(strings.TrimSuffix(result, "\n"), "→")
			if !ok || !strings.HasSuffix(text, "...") {
				t.Fatal("missing line prefix or established truncation marker")
			}
			text = strings.TrimSuffix(text, "...")
			if len(text) > maxCharsPerLine {
				t.Fatal("line preview exceeded the existing byte limit")
			}
			// A provider JSON round-trip must not introduce replacement runes
			// that prevent exact Edit matching against the original source.
			wire, err := json.Marshal(text)
			if err != nil {
				t.Fatal(err)
			}
			var shown string
			if err := json.Unmarshal(wire, &shown); err != nil {
				t.Fatal(err)
			}
			if shown != text || !strings.HasPrefix(tc.source, shown) {
				t.Fatal("model-visible preview is not an exact source prefix")
			}
			_, err = EditTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
				"file_path": path, "old_string": shown, "new_string": "updated",
			}))
			if err != nil {
				t.Fatalf("exact displayed prefix could not be edited: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "updated"+tc.source[len(shown):] {
				t.Fatal("Edit did not preserve the undisplayed suffix")
			}
		})
	}
}

func FuzzFormatReadResultPreservesUTF8(f *testing.F) {
	f.Add("ordinary line")
	f.Add("x" + strings.Repeat("🙂", 500))
	f.Add(strings.Repeat("界", 668))
	f.Fuzz(func(t *testing.T, line string) {
		if !utf8.ValidString(line) || len(line) > 256<<10 {
			t.Skip()
		}
		got := formatReadResult("source", 0, []string{line})
		if !utf8.ValidString(got) {
			t.Fatal("valid source produced invalid UTF-8 feedback")
		}
		shown := strings.TrimSuffix(strings.TrimPrefix(got, "     1→"), "\n")
		if len(line) <= maxCharsPerLine {
			if shown != line {
				t.Fatal("untruncated source changed")
			}
			return
		}
		if !strings.HasSuffix(shown, "...") {
			t.Fatal("long line lost its truncation marker")
		}
		prefix := strings.TrimSuffix(shown, "...")
		if len(prefix) > maxCharsPerLine || !strings.HasPrefix(line, prefix) {
			t.Fatal("preview changed source bytes or exceeded its budget")
		}
	})
}
