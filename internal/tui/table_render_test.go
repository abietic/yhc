package tui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestParseMarkdownTable(t *testing.T) {
	input := `| Name | Age | City |
| --- | --- | --- |
| Alice | 30 | NYC |
| Bob | 25 | LA |`

	tbl := parseMarkdownTable(input)
	if tbl == nil {
		t.Fatal("expected non-nil table")
	}
	if len(tbl.headers) != 3 {
		t.Fatalf("expected 3 headers, got %d", len(tbl.headers))
	}
	if tbl.headers[0].raw != "Name" {
		t.Errorf("header[0] = %q, want %q", tbl.headers[0].raw, "Name")
	}
	if len(tbl.rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(tbl.rows))
	}
	if tbl.rows[0][1].raw != "30" {
		t.Errorf("row[0][1] = %q, want %q", tbl.rows[0][1].raw, "30")
	}
}

func TestParseTableAlignment(t *testing.T) {
	input := `| Left | Center | Right |
| :--- | :---: | ---: |
| a | b | c |`

	tbl := parseMarkdownTable(input)
	if tbl == nil {
		t.Fatal("expected non-nil table")
	}
	if tbl.aligns[0] != "left" {
		t.Errorf("aligns[0] = %q, want left", tbl.aligns[0])
	}
	if tbl.aligns[1] != "center" {
		t.Errorf("aligns[1] = %q, want center", tbl.aligns[1])
	}
	if tbl.aligns[2] != "right" {
		t.Errorf("aligns[2] = %q, want right", tbl.aligns[2])
	}
}

func TestRenderTableProportionalWidths(t *testing.T) {
	tbl := &parsedTable{
		headers: []tableCell{
			{raw: "Name"},
			{raw: "Description"},
		},
		aligns: []string{"left", "left"},
		rows: [][]tableCell{
			{{raw: "foo"}, {raw: "A short item"}},
			{{raw: "bar"}, {raw: "Another longer description here"}},
		},
	}

	result := renderTableWithThemeAndProfile(
		tbl,
		80,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	if result == "" {
		t.Fatal("renderTable returned empty string")
	}

	lines := strings.Split(result, "\n")
	// Should have top border, header, middle border, 2 data rows (possibly multi-line), bottom border
	if len(lines) < 5 {
		t.Fatalf("expected at least 5 lines, got %d:\n%s", len(lines), result)
	}

	// First line should be top border with box-drawing chars
	if !strings.HasPrefix(lines[0], "┌") {
		t.Errorf("expected top border to start with ┌, got %q", lines[0])
	}

	// Last line should be bottom border
	lastLine := lines[len(lines)-1]
	if !strings.HasPrefix(lastLine, "└") {
		t.Errorf("expected bottom border to start with └, got %q", lastLine)
	}

	// Column widths should NOT be uniform — Description column should be wider
	// than Name column (proportional to content)
	if strings.Contains(lines[0], "┬") {
		parts := strings.Split(lines[0], "┬")
		if len(parts) == 2 {
			// Name column portion is shorter than Description column
			nameWidth := len(parts[0])
			descWidth := len(parts[1])
			if nameWidth >= descWidth {
				t.Errorf("expected Description column wider than Name, got name=%d desc=%d", nameWidth, descWidth)
			}
		}
	}
}

func TestRenderTableVerticalFallback(t *testing.T) {
	// Create a table that would require very tall rows at narrow width
	tbl := &parsedTable{
		headers: []tableCell{
			{raw: "Key"},
			{raw: "Value"},
		},
		aligns: []string{"left", "left"},
		rows: [][]tableCell{
			{{raw: "name"}, {raw: "This is an extremely long value that will definitely need to wrap across multiple lines when rendered in a very narrow terminal window"}},
		},
	}

	// At width=30 this should trigger vertical format
	result := renderTableWithThemeAndProfile(
		tbl,
		30,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	if result == "" {
		t.Fatal("renderTable returned empty string")
	}
	// Vertical format uses bold ANSI for labels
	if !strings.Contains(result, "\x1b[1m") {
		t.Errorf("expected vertical format with bold labels, got:\n%s", result)
	}
	// Should NOT have box-drawing borders
	if strings.Contains(result, "┌") {
		t.Errorf("expected vertical format (no box borders), got:\n%s", result)
	}
}

func TestRenderVerticalFieldBoundsNarrowWidthsAndControlState(t *testing.T) {
	tests := []struct {
		name  string
		label string
		value string
		width int
		want  string
	}{
		{name: "one-cell-empty", label: "A", width: 1, want: "A:"},
		{name: "two-cell-value", label: "AB", value: "x", width: 2, want: "AB:x"},
		{
			name:  "styled-label",
			label: "\x1b[31mLongLabel\x1b[0m",
			value: "styled",
			width: 9,
			want:  "LongLabel:styled",
		},
		{
			name:  "linked-value",
			label: "Key",
			value: "\x1b]8;;https://example.com\x1b\\" +
				"abcdefghij\x1b]8;;\x1b\\",
			width: 10,
			want:  "Key:abcdefghij",
		},
	}
	profile := DefaultDisplayCellProfile()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines := renderVerticalField(test.label, test.value, test.width, profile)
			var plain strings.Builder
			for index, line := range lines {
				if cells := profile.width(line); cells > test.width {
					t.Fatalf(
						"line %d width=%d > %d: %q",
						index,
						cells,
						test.width,
						xansi.Strip(line),
					)
				}
				assertWidthProfileControlStateClosed(t, profile, line)
				plain.WriteString(strings.ReplaceAll(xansi.Strip(line), " ", ""))
			}
			if plain.String() != test.want {
				t.Fatalf("visible field=%q want=%q", plain.String(), test.want)
			}
		})
	}
}

func TestStripAndSpliceTables(t *testing.T) {
	content := `Here is some text.

| Col1 | Col2 |
| --- | --- |
| a | b |

And more text.`

	stripped, tables, ok := extractTableIslands(content, markdownStableComplete)
	if !ok || len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d (ok=%v)", len(tables), ok)
	}

	// Stripped content should have the placeholder
	if !strings.Contains(stripped, tables[0].token) {
		t.Errorf("stripped content missing placeholder:\n%s", stripped)
	}

	// Splice should replace placeholder with rendered table
	theme := markdownThemeForName(ThemePolarNight)
	profile := DefaultDisplayCellProfile()
	entry := getRendererWithProfile(80, theme, profile)
	if entry == nil {
		t.Fatal("renderer unavailable")
	}
	entry.mu.Lock()
	rendered, err := entry.renderer.Render(stripped)
	entry.mu.Unlock()
	if err != nil {
		t.Fatalf("render stripped source: %v", err)
	}
	spliced, ok := spliceTableIslands(rendered, tables, 80, theme, profile)
	if !ok {
		t.Fatal("splice rejected renderer-owned sentinel")
	}
	if strings.Contains(spliced, tables[0].token) {
		t.Errorf("spliced output still has placeholder:\n%s", spliced)
	}
	// Should contain box-drawing characters from rendered table
	if !strings.Contains(spliced, "┌") {
		t.Errorf("spliced output missing rendered table:\n%s", spliced)
	}
	// Should preserve surrounding text
	if !strings.Contains(xansi.Strip(spliced), "Here is some text.") {
		t.Errorf("spliced output missing surrounding text:\n%s", spliced)
	}
}

func TestCellMinWidth(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"hello", 5},
		{"hello world", 5}, // longest word is "hello" (5) or "world" (5)
		{"a", 3},           // minimum is tableMinColWidth=3
		{"", 3},
		{"superlongword", 13},
	}
	for _, tt := range tests {
		got := cellMinWidthWithProfile(tt.text, DefaultDisplayCellProfile())
		if got != tt.want {
			t.Errorf("cellMinWidth(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}
