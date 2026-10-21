package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTextSupportsExactEdit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
	}{
		{name: "go-import-tabs", source: "package p\n\nimport (\n\t\"target\"\n)\n"},
		{name: "mixed-indent-and-string-tab", source: " \tvalue := \"target\tdata\"\n\t  next()\n"},
		{name: "make-recipe", source: "all:\n\t@echo target\n"},
		{name: "spaces", source: "  target := \"two  spaces\"\n"},
		{name: "crlf", source: "first\r\n\tvalue := \"target\there\"\r\n"},
		{name: "no-final-newline", source: "\ttarget\tvalue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte(tc.source), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := ReadTool().ExecuteCtx(t.Context(), fileToolInput(t, map[string]any{"file_path": path}))
			if err != nil {
				t.Fatal(err)
			}
			// Copy only the text after each line-number prefix, exactly as the
			// model-visible Edit instructions specify. Do not normalize it.
			rows := strings.Split(strings.TrimSuffix(result, "\n"), "\n")
			lines := make([]string, len(rows))
			for i, row := range rows {
				_, text, ok := strings.Cut(row, "→")
				if !ok {
					t.Fatalf("Read row %d has no line-number prefix", i)
				}
				lines[i] = text
			}
			copied := strings.Join(lines, "\n")
			want := strings.Replace(tc.source, "target", "changed", 1)
			if strings.Contains(tc.source, "\t") {
				_, err := EditTool().ExecuteCtx(t.Context(), fileToolInput(t, map[string]any{
					"file_path": path, "old_string": strings.ReplaceAll(tc.source, "\t", "    "), "new_string": want,
				}))
				if err == nil {
					t.Fatal("Edit accepted spaces in place of source tabs")
				}
				unchanged, err := os.ReadFile(path)
				if err != nil || string(unchanged) != tc.source {
					t.Fatal("rejected whitespace-mismatched edit changed the file")
				}
			}
			_, err = EditTool().ExecuteCtx(t.Context(), fileToolInput(t, map[string]any{
				"file_path": path, "old_string": copied, "new_string": want,
			}))
			if err != nil {
				t.Fatalf("exact text copied from Read could not be edited: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != want {
				t.Fatalf("edited bytes = %q, want %q", data, want)
			}
		})
	}
}

func TestReadTabTextStillHonorsLineBudget(t *testing.T) {
	line := strings.Repeat("\t", maxCharsPerLine+1)
	got := formatReadResult("source.txt", 7, []string{line})
	want := "     8→" + strings.Repeat("\t", maxCharsPerLine) + "...\n"
	if got != want {
		t.Fatal("Read must preserve tabs within the existing per-line byte limit")
	}
}
