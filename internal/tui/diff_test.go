package tui

import (
	"strings"
	"testing"
)

func TestComputeUnifiedDiff_BasicChange(t *testing.T) {
	old := "line1\nline2\nline3\nline4\nline5"
	new := "line1\nline2\nmodified\nline4\nline5"

	hunks := computeUnifiedDiff(old, new, 3)
	if len(hunks) == 0 {
		t.Fatal("expected at least one hunk")
	}

	hunk := hunks[0]
	if hunk.OldStart < 1 {
		t.Errorf("OldStart should be >= 1, got %d", hunk.OldStart)
	}
	if hunk.NewStart < 1 {
		t.Errorf("NewStart should be >= 1, got %d", hunk.NewStart)
	}

	// Should have at least one remove and one add
	hasRemove := false
	hasAdd := false
	for _, line := range hunk.Lines {
		if line.Type == diffLineRemove {
			hasRemove = true
		}
		if line.Type == diffLineAdd {
			hasAdd = true
		}
	}
	if !hasRemove {
		t.Error("expected at least one removal line")
	}
	if !hasAdd {
		t.Error("expected at least one addition line")
	}
}

func TestComputeUnifiedDiff_NoChange(t *testing.T) {
	text := "line1\nline2\nline3"
	hunks := computeUnifiedDiff(text, text, 3)
	if len(hunks) != 0 {
		t.Errorf("expected no hunks for identical text, got %d", len(hunks))
	}
}

func TestComputeUnifiedDiff_EmptyToContent(t *testing.T) {
	hunks := computeUnifiedDiff("", "new content\nline2", 3)
	if len(hunks) == 0 {
		t.Fatal("expected hunks for empty-to-content diff")
	}

	// All lines should be additions
	for _, line := range hunks[0].Lines {
		if line.Type != diffLineAdd {
			t.Errorf("expected all lines to be additions, got type %d", line.Type)
		}
	}
}

func TestComputeUnifiedDiff_ContentToEmpty(t *testing.T) {
	hunks := computeUnifiedDiff("old content\nline2", "", 3)
	if len(hunks) == 0 {
		t.Fatal("expected hunks for content-to-empty diff")
	}

	// All lines should be removals
	for _, line := range hunks[0].Lines {
		if line.Type != diffLineRemove {
			t.Errorf("expected all lines to be removals, got type %d", line.Type)
		}
	}
}

func TestComputeUnifiedDiff_LineNumbers(t *testing.T) {
	old := "a\nb\nc\nd\ne"
	new := "a\nb\nX\nd\ne"

	hunks := computeUnifiedDiff(old, new, 2)
	if len(hunks) == 0 {
		t.Fatal("expected at least one hunk")
	}

	// Check that line numbers are assigned correctly
	for _, line := range hunks[0].Lines {
		switch line.Type {
		case diffLineContext:
			if line.OldNum == 0 || line.NewNum == 0 {
				t.Errorf("context line should have both old and new numbers: old=%d new=%d", line.OldNum, line.NewNum)
			}
		case diffLineRemove:
			if line.OldNum == 0 {
				t.Errorf("remove line should have old number, got 0")
			}
		case diffLineAdd:
			if line.NewNum == 0 {
				t.Errorf("add line should have new number, got 0")
			}
		}
	}
}

func TestRenderStructuredDiff_HunkHeader(t *testing.T) {
	styles := defaultStyles()
	old := "line1\nline2\nline3"
	new := "line1\nmodified\nline3"

	output := renderStructuredDiff(styles, "test.go", old, new, 80)
	if output == "" {
		t.Fatal("expected non-empty output")
	}

	// Should contain hunk header
	if !strings.Contains(output, "@@") {
		t.Error("output should contain hunk header @@")
	}
}

func TestRenderStructuredDiff_FilePathHeader(t *testing.T) {
	styles := defaultStyles()
	old := "a\nb"
	new := "a\nc"

	output := renderStructuredDiff(styles, "/path/to/file.go", old, new, 80)
	if output == "" {
		t.Fatal("expected non-empty output")
	}

	// Should contain file path (shortened)
	if !strings.Contains(output, "file.go") {
		t.Error("output should contain file name")
	}
}

func TestRenderStructuredEditDiff_ParsesJSON(t *testing.T) {
	styles := defaultStyles()
	input := `{"file_path": "/tmp/test.go", "old_string": "hello\nworld", "new_string": "hello\nuniverse"}`

	output := renderStructuredEditDiff(styles, input, 80)
	if output == "" {
		t.Fatal("expected non-empty output for valid edit input")
	}

	// Should contain hunk header
	if !strings.Contains(output, "@@") {
		t.Error("output should contain @@ hunk header")
	}
}

func TestRenderStructuredEditDiff_EmptyInput(t *testing.T) {
	styles := defaultStyles()

	// Empty old and new strings
	output := renderStructuredEditDiff(styles, `{"file_path": "x.go", "old_string": "", "new_string": ""}`, 80)
	if output != "" {
		t.Error("expected empty output for empty old and new strings")
	}

	// Invalid JSON
	output = renderStructuredEditDiff(styles, "not json", 80)
	if output != "" {
		t.Error("expected empty output for invalid JSON")
	}
}

func TestFormatDiffGutter(t *testing.T) {
	line := diffHunkLine{
		Type:    diffLineRemove,
		Content: "removed line",
		OldNum:  5,
		NewNum:  0,
	}

	gutter := formatDiffGutter(line, 3)
	// Should contain the old line number "  5" and sigil "-"
	if !strings.Contains(gutter, "5") {
		t.Errorf("gutter should contain line number 5, got: %q", gutter)
	}
	if !strings.Contains(gutter, "-") {
		t.Errorf("gutter should contain sigil '-', got: %q", gutter)
	}
}
