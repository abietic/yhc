package tui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestG9CSemanticTableRunsAndSafeTerminalOutput(t *testing.T) {
	source := "| Cell |\n| --- |\n| ***both*** `code` ~~old~~ [site](https://example.com) <https://go.dev> <me@example.com> ![alt](https://img.example/x) <b>html</b> &amp; |"
	table := parseMarkdownTable(source)
	if table == nil || len(table.rows) != 1 || len(table.rows[0]) != 1 {
		t.Fatalf("semantic table = %#v", table)
	}
	cell := table.rows[0][0]
	if !strings.Contains(cell.raw, "both") || !strings.Contains(cell.raw, "code") ||
		!strings.Contains(cell.raw, "site") || !strings.Contains(cell.raw, "html") || !strings.Contains(cell.raw, "&") {
		t.Fatalf("plain semantic projection lost content: %q", cell.raw)
	}
	var bold, italic, code, strike, image, link bool
	for _, run := range cell.runs {
		bold = bold || run.bold
		italic = italic || run.italic
		code = code || run.code
		strike = strike || run.strike
		image = image || run.image
		link = link || run.linkURL != ""
	}
	if !bold || !italic || !code || !strike || !image || !link {
		t.Fatalf("semantic flags missing: %#v", cell.runs)
	}
	var imageDestination, emailDestination string
	for _, run := range cell.runs {
		if run.image {
			imageDestination = run.linkURL
		}
		if run.text == "me@example.com" {
			emailDestination = run.linkURL
		}
	}
	if imageDestination != "https://img.example/x" || emailDestination != "mailto:me@example.com" {
		t.Fatalf("semantic destinations image=%q email=%q runs=%#v", imageDestination, emailDestination, cell.runs)
	}
	rendered := renderTableWithThemeAndProfile(
		table,
		120,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	if !strings.Contains(rendered, "\x1b[") ||
		!strings.Contains(rendered, "\x1b]8;;https://example.com\x1b\\") ||
		!strings.Contains(rendered, "\x1b]8;;https://img.example/x\x1b\\") ||
		!strings.Contains(rendered, "\x1b]8;;mailto:me@example.com\x1b\\") {
		t.Fatalf("semantic output lacks SGR/OSC8: %q", rendered)
	}
	plain := xansi.Strip(rendered)
	for _, want := range []string{"both", "code", "old", "site", "alt", "html"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered semantic output lost %q: %q", want, plain)
		}
	}
	for _, line := range strings.Split(rendered, "\n") {
		assertWidthProfileControlStateClosed(t, DefaultDisplayCellProfile(), line)
	}
}

func TestG9CRejectsControlBearingLinkDestination(t *testing.T) {
	source := "| Link |\n| --- |\n| [visible](https://example.com%1b]8;;evil) |"
	table := parseMarkdownTable(source)
	if table == nil {
		t.Fatal("semantic table missing")
	}
	// Goldmark leaves percent encoding literal; exercise the terminal boundary
	// directly with the decoded control-bearing destination contract.
	table.rows[0][0].runs = []tableRun{{text: "visible", linkURL: "https://ok\x1b]8;;evil"}}
	rendered := renderTableWithThemeAndProfile(
		table,
		80,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	if strings.Contains(rendered, "\x1b]8;;https://ok") {
		t.Fatalf("unsafe OSC8 destination emitted: %q", rendered)
	}
	if !strings.Contains(xansi.Strip(rendered), "visible") {
		t.Fatalf("unsafe destination removed visible label: %q", rendered)
	}
}

func TestG9CRejectsInvalidUTF8LinkDestination(t *testing.T) {
	table := &parsedTable{
		headers: []tableCell{{raw: "Link"}},
		aligns:  []string{"left"},
		rows: [][]tableCell{{
			{
				raw:  "visible",
				runs: []tableRun{{text: "visible", linkURL: "https://ok/\xff"}},
			},
		}},
	}
	rendered := renderTableWithThemeAndProfile(
		table,
		80,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	if strings.Contains(rendered, "\x1b]8;;") {
		t.Fatalf("invalid UTF-8 OSC8 destination emitted: %q", rendered)
	}
	if !strings.Contains(xansi.Strip(rendered), "visible") {
		t.Fatalf("invalid destination removed visible label: %q", rendered)
	}
}

func TestG9CSanitizesDecodedAndLiteralTerminalControls(t *testing.T) {
	source := "| Cell |\n| --- |\n| &#27;[2J literal\x1b]8;;evil &NewLine; &#x9d;8 |"
	table := parseMarkdownTable(source)
	if table == nil || len(table.rows) != 1 || len(table.rows[0]) != 1 {
		t.Fatalf("semantic table = %#v", table)
	}
	cell := table.rows[0][0]
	for _, unsafe := range []rune{'\x1b', '\n', '\u009d'} {
		if strings.ContainsRune(cell.raw, unsafe) {
			t.Fatalf("semantic projection retained control %U: %q", unsafe, cell.raw)
		}
		for _, run := range cell.runs {
			if strings.ContainsRune(run.text, unsafe) {
				t.Fatalf("semantic run retained control %U: %#v", unsafe, cell.runs)
			}
		}
	}
	rendered := renderTableWithThemeAndProfile(
		table,
		100,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	if strings.Contains(rendered, "\x1b[2J") ||
		strings.Contains(rendered, "\x1b]8;;evil") ||
		strings.ContainsRune(rendered, '\u009d') {
		t.Fatalf("semantic text emitted terminal injection: %q", rendered)
	}
	for _, visible := range []string{"[2J", "literal", "]8;;evil", "8"} {
		if !strings.Contains(xansi.Strip(rendered), visible) {
			t.Fatalf("sanitization removed visible text %q: %q", visible, xansi.Strip(rendered))
		}
	}
}

func TestG9CTopLevelSourceRangePreservesProseAndHeaderOnlyTable(t *testing.T) {
	source := "前言\n\n| Name | State |\n| --- | ---: |\n\n后文\n"
	stripped, tables, ok := extractTableIslands(source, markdownStableComplete)
	if !ok || len(tables) != 1 || !strings.Contains(stripped, tables[0].token) {
		t.Fatalf("table extraction = %q %#v (ok=%v)", stripped, tables, ok)
	}
	if !strings.HasPrefix(stripped, "前言\n\n") || !strings.HasSuffix(stripped, "\n后文\n") {
		t.Fatalf("source range corrupted surrounding prose: %q", stripped)
	}
	table := tables[0].table
	if table == nil || len(table.headers) != 2 || len(table.rows) != 0 || table.aligns[1] != "right" {
		t.Fatalf("header-only semantic table = %#v", table)
	}
}

func TestG9CExtractsTableWithEmptyHeaderCells(t *testing.T) {
	source := "| | |\n| --- | --- |\n| a | b |\n"
	stripped, tables, ok := extractTableIslands(source, markdownStableComplete)
	if !ok || len(tables) != 1 || !strings.Contains(stripped, tables[0].token) {
		t.Fatalf("empty-header extraction = %q %#v (ok=%v)", stripped, tables, ok)
	}
	table := parseMarkdownTable(source)
	if table == nil || len(table.headers) != 2 || table.headers[0].raw != "" || table.headers[1].raw != "" {
		t.Fatalf("empty-header semantic table = %#v", table)
	}
}

func TestG9CNormalizesEmptyShortAndExtraRowsToGoldmarkColumns(t *testing.T) {
	source := "| A | B |\n| --- | --- |\n| only |\n| | value | extra |\n"
	table := parseMarkdownTable(source)
	if table == nil || len(table.rows) != 2 {
		t.Fatalf("semantic table = %#v", table)
	}
	for rowIndex, row := range table.rows {
		if got, want := len(row), len(table.headers); got != want {
			t.Fatalf("row %d columns=%d, want Goldmark normalized %d: %#v", rowIndex, got, want, row)
		}
	}
	if table.rows[0][1].raw != "" || table.rows[1][0].raw != "" || table.rows[1][1].raw != "value" {
		t.Fatalf("normalized cells = %#v", table.rows)
	}
}

func TestG9CCodeSpanParseViewOnlyMasksClosedUnescapedPipes(t *testing.T) {
	for _, source := range []string{
		"| A | B |\n| --- | --- |\n| `a | b` | x |\n",
		"| A | B |\n| --- | --- |\n| `a \\| b` | x |\n",
	} {
		table := parseMarkdownTable(source)
		if table == nil || len(table.rows) != 1 || len(table.rows[0]) != 2 ||
			!strings.Contains(table.rows[0][0].raw, "|") {
			t.Fatalf("closed code-span semantic table = %#v", table)
		}
	}
	unmatched := "| A | B |\n| --- | --- |\n| `a | b | x |\n"
	if got := string(tableParseView(unmatched)); got != unmatched {
		t.Fatalf("unmatched code span changed parse bytes: %q", got)
	}

	multibyte := "| 表达式 | 结果 |\n| --- | --- |\n| ``中 ` | 文`` | 好 |\n"
	view := tableParseView(multibyte)
	if len(view) != len(multibyte) || multibyte != string([]byte(multibyte)) {
		t.Fatalf("parse view changed canonical source or byte length: source=%d view=%d", len(multibyte), len(view))
	}
	table := parseMarkdownTable(multibyte)
	if table == nil || len(table.rows) != 1 || len(table.rows[0]) != 2 ||
		!strings.Contains(table.rows[0][0].raw, "中 ` | 文") {
		t.Fatalf("multi-backtick code-span semantic table = %#v view=%q", table, view)
	}

	escapedBackticks := "| A | B | C |\n| --- | --- | --- |\n| \\`left | right\\` | tail |\n"
	table = parseMarkdownTable(escapedBackticks)
	if table == nil || len(table.rows) != 1 || len(table.rows[0]) != 3 ||
		table.rows[0][0].raw != "`left" || table.rows[0][1].raw != "right`" ||
		table.rows[0][2].raw != "tail" {
		t.Fatalf("escaped backticks incorrectly owned code-span pipes: %#v", table)
	}

	backslashBeforeClose := "| A | B | C |\n| --- | --- | --- |\n| `a \\` | b` | tail |\n"
	table = parseMarkdownTable(backslashBeforeClose)
	if table == nil || len(table.rows) != 1 || len(table.rows[0]) != 3 {
		t.Fatalf("backslash before code-span close changed cell boundaries: %#v", table)
	}
}

func TestG9DNestedTableUsesSemanticIslandInsideContainer(t *testing.T) {
	source := "> | A | B |\n> | --- | --- |\n> | x | y |\n"
	stripped, tables, ok := extractTableIslands(source, markdownStableComplete)
	if !ok || len(tables) != 1 || !strings.Contains(stripped, "~~~text") {
		t.Fatalf("nested semantic extraction: stripped=%q tables=%#v ok=%v", stripped, tables, ok)
	}

	stream := &StreamingMarkdown{}
	stream.Finalize(source)
	rendered := xansi.Strip(stream.Render(source, 80, ThemePolarNight))
	for _, want := range []string{"▎ ┌", "A", "B", "x", "y"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("nested semantic table lost %q: %q", want, rendered)
		}
	}
}

func TestG9DPlaceholderSelectionSkipsSourceCollisionsAcrossTables(t *testing.T) {
	source := "literal ET0T\n\n" +
		"| A |\n| --- |\n| one |\n\n" +
		"| B |\n| --- |\n| two |\n"
	stripped, tables, ok := extractTableIslands(source, markdownStableComplete)
	if !ok || len(tables) != 2 {
		t.Fatalf("tables=%d stripped=%q ok=%v", len(tables), stripped, ok)
	}
	for index, placeholder := range []string{"ET1T", "ET2T"} {
		if !strings.Contains(stripped, placeholder) || tables[index].token != placeholder {
			t.Fatalf("missing collision-free %q: stripped=%q tables=%#v", placeholder, stripped, tables)
		}
	}
	if !strings.Contains(stripped, "literal ET0T") {
		t.Fatalf("source placeholder text was corrupted: %q", stripped)
	}
}

func TestG9CSemanticRunsSurviveNarrowVerticalFallback(t *testing.T) {
	source := "| Key | Value |\n| --- | --- |\n" +
		"| rich | [a deliberately long linked value](https://example.com/with%20space) and `code` 👩🏽‍💻 |\n"
	table := parseMarkdownTable(source)
	if table == nil {
		t.Fatal("semantic table missing")
	}
	rendered := renderTableWithThemeAndProfile(
		table,
		28,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	plain := xansi.Strip(rendered)
	if strings.Contains(plain, "┌") || !strings.Contains(plain, "Key:") ||
		!strings.Contains(plain, "deliberately") || !strings.Contains(plain, "code") ||
		!strings.Contains(plain, "👩🏽‍💻") {
		t.Fatalf("narrow semantic fallback lost structure or content:\n%s", plain)
	}
	if !strings.Contains(rendered, "\x1b]8;;https://example.com/with%20space\x1b\\") {
		t.Fatalf("narrow semantic fallback lost OSC8 destination: %q", rendered)
	}
	for _, line := range strings.Split(rendered, "\n") {
		assertWidthProfileControlStateClosed(t, DefaultDisplayCellProfile(), line)
	}
}
