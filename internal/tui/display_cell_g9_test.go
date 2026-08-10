package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// g9EvidenceWidthProfile is an independent test oracle for the accepted G9
// fixture set. Production must not import or derive behavior from it.
//
// The explicit overrides pin presentation-sensitive clusters. All other
// clusters use x/ansi's grapheme width only as the ordinary control group.
type g9EvidenceWidthProfile struct {
	id        string
	overrides map[string]int
}

func newG9EvidenceWidthProfile() g9EvidenceWidthProfile {
	return g9EvidenceWidthProfile{
		id: "g9-evidence-unicode-v1-ambiguous-narrow",
		overrides: map[string]int{
			"❤":       1,
			"❤︎":      1,
			"❤️":      2,
			"☀︎":      1,
			"☀️":      2,
			"🏷":       2,
			"🇺":       1,
			"🇺🇸":      2,
			"1️⃣":     2,
			"#️⃣":     2,
			"👩🏽‍💻":    2,
			"👨‍👩‍👧‍👦": 2,
			"가":      2,
			"क्ष":     2,
		},
	}
}

func (p g9EvidenceWidthProfile) cells(text string) int {
	plain := xansi.Strip(text)
	graphemes := uniseg.NewGraphemes(plain)
	width := 0
	for graphemes.Next() {
		cluster := graphemes.Str()
		if override, ok := p.overrides[cluster]; ok {
			width += override
			continue
		}
		width += xansi.StringWidth(cluster)
	}
	return width
}

func (p g9EvidenceWidthProfile) borderColumns(line string) []int {
	plain := xansi.Strip(line)
	graphemes := uniseg.NewGraphemes(plain)
	column := 0
	var borders []int
	for graphemes.Next() {
		cluster := graphemes.Str()
		if strings.Contains("┌┬┐├┼┤│└┴┘", cluster) {
			borders = append(borders, column)
		}
		column += p.cells(cluster)
	}
	return borders
}

func TestG9BIndependentWidthProfileAlignsTableGeometry(
	t *testing.T,
) {
	profile := newG9EvidenceWidthProfile()
	source := `| Case | Cluster | Note |
| --- | --- | --- |
| ascii | A | control |
| cjk | 中 | wide |
| nfc | é | composed |
| nfd | é | decomposed |
| hangul-nfc | 가 | syllable |
| hangul-jamo | 가 | cluster |
| indic | क्ष | conjunct |
| text-heart | ❤︎ | VS15 |
| emoji-heart | ❤️ | VS16 |
| modifier-zwj | 👩🏽‍💻 | cluster |
| family-zwj | 👨‍👩‍👧‍👦 | cluster |
| lone-ri | 🇺 | incomplete flag |
| flag | 🇺🇸 | pair |
| keycap | 1️⃣ | keycap |
| bare-tag | 🏷 | emoji presentation |`

	table := parseMarkdownTable(source)
	if table == nil {
		t.Fatal("current table parser did not recognize the evidence table")
	}
	rendered := renderTableWithThemeAndProfile(
		table,
		120,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	lines := strings.Split(rendered, "\n")
	if len(lines) < 3 {
		t.Fatalf("rendered evidence table has %d lines:\n%s", len(lines), rendered)
	}
	expected := profile.borderColumns(lines[0])
	if len(expected) != 4 {
		t.Fatalf(
			"profile %q top border columns = %v, want four borders",
			profile.id,
			expected,
		)
	}

	actualByCase := make(map[string][]int)
	clusterByCase := map[string]string{
		"ascii":        "A",
		"cjk":          "中",
		"nfc":          "é",
		"nfd":          "é",
		"hangul-nfc":   "가",
		"hangul-jamo":  "가",
		"indic":        "क्ष",
		"text-heart":   "❤︎",
		"emoji-heart":  "❤️",
		"modifier-zwj": "👩🏽‍💻",
		"family-zwj":   "👨‍👩‍👧‍👦",
		"lone-ri":      "🇺",
		"flag":         "🇺🇸",
		"keycap":       "1️⃣",
		"bare-tag":     "🏷",
	}
	for _, line := range lines {
		plain := xansi.Strip(line)
		for name := range clusterByCase {
			if strings.Contains(plain, " "+name+" ") {
				actualByCase[name] = profile.borderColumns(line)
			}
		}
	}

	if len(actualByCase) != len(clusterByCase) {
		t.Fatalf(
			"profile %q collected %d cases, want %d: %#v",
			profile.id,
			len(actualByCase),
			len(clusterByCase),
			actualByCase,
		)
	}
	for name, actual := range actualByCase {
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf(
				"profile %q case=%q cluster=%q columns=%v, want top border %v",
				profile.id,
				name,
				clusterByCase[name],
				actual,
				expected,
			)
		}
	}
}

func TestG9CSemanticTableParserOwnsEscapedAndCodeSpanPipes(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "escaped pipe",
			source: `| Expression | Result |
| --- | --- |
| a \| b | literal |`,
		},
		{
			name: "code span pipe",
			source: "| Expression | Result |\n" +
				"| --- | --- |\n" +
				"| `a | b` | code |\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			table := parseMarkdownTable(test.source)
			if table == nil || len(table.rows) != 1 {
				t.Fatalf("current parser result = %#v", table)
			}
			const parserOwnedColumns = 2
			if got := len(table.rows[0]); got != parserOwnedColumns {
				t.Fatalf(
					"semantic %s columns=%d, want Goldmark-owned %d",
					test.name,
					got,
					parserOwnedColumns,
				)
			}
			if !strings.Contains(table.rows[0][0].raw, "|") {
				t.Fatalf("semantic %s lost literal pipe: %#v", test.name, table.rows[0][0])
			}
		})
	}
}

func TestG9DTableLifecycleConvergesOnCompleteSemanticOwner(t *testing.T) {
	const table = `| Name | State |
| --- | --- |
| alpha | ready |`
	source := table + "\n\nAfter table."
	stream := &StreamingMarkdown{}

	initial := stream.Render(table, 52, ThemePolarNight)
	streamed := stream.Render(source, 52, ThemePolarNight)
	stream.Finalize(source)
	finalized := stream.Render(source, 52, ThemePolarNight)

	if strings.Contains(xansi.Strip(initial), "┌") ||
		!strings.Contains(xansi.Strip(finalized), "┌") {
		t.Fatalf(
			"incomplete/complete ownership mismatch: initial=%q finalized=%q",
			xansi.Strip(initial),
			xansi.Strip(finalized),
		)
	}
	if !strings.Contains(xansi.Strip(streamed), "┌") {
		t.Fatalf(
			"completed stable-prefix table did not use semantic owner:\n%s",
			xansi.Strip(streamed),
		)
	}
	if xansi.Strip(streamed) != xansi.Strip(finalized) {
		t.Fatalf(
			"stable-prefix and finalized visible table output diverged:\nstreamed=%q\nfinalized=%q",
			xansi.Strip(streamed),
			xansi.Strip(finalized),
		)
	}
}

func TestG9ACurrentTableBoundaryResizeAndFallbackEvidence(t *testing.T) {
	const source = `| Key | Value |
| --- | --- |
| emoji | 👩🏽‍💻 |
| long | a value that becomes a vertical record at narrow widths |`

	stream := &StreamingMarkdown{}
	runeBoundaries := []int{0}
	for index := range source {
		if index > 0 {
			runeBoundaries = append(runeBoundaries, index)
		}
	}
	runeBoundaries = append(runeBoundaries, len(source))
	for _, boundary := range runeBoundaries {
		rendered := stream.Render(source[:boundary], 80, ThemePolarNight)
		if !utf8.ValidString(rendered) {
			t.Fatalf("append boundary %d produced invalid UTF-8", boundary)
		}
	}

	stream.Finalize(source)
	wide := stream.Render(source, 80, ThemePolarNight)
	narrow := stream.Render(source, 28, ThemePolarNight)
	if wide == narrow {
		t.Fatal("resize did not invalidate table rendering")
	}
	plainNarrow := xansi.Strip(narrow)
	for _, field := range []string{"Key:", "Value:", "emoji", "👩🏽‍💻", "long"} {
		if !strings.Contains(plainNarrow, field) {
			t.Fatalf("narrow fallback lost %q:\n%s", field, plainNarrow)
		}
	}
}

func TestG9ACurrentEmptyUnevenAndStyledCellEvidence(t *testing.T) {
	profile := newG9EvidenceWidthProfile()
	table := &parsedTable{
		headers: []tableCell{
			{raw: "First"},
			{raw: "Second"},
			{raw: "Third"},
		},
		aligns: []string{"left", "center", "right"},
		rows: [][]tableCell{
			{
				{raw: ""},
				{raw: "\x1b[31mred\x1b[0m"},
			},
			{
				{raw: "link"},
				{
					raw: "\x1b]8;;https://example.com\x1b\\" +
						"site\x1b]8;;\x1b\\",
				},
				{raw: "tail"},
			},
		},
	}
	rendered := renderTableWithThemeAndProfile(
		table,
		80,
		markdownThemeForName(ThemePolarNight),
		DefaultDisplayCellProfile(),
	)
	lines := strings.Split(rendered, "\n")
	expected := profile.borderColumns(lines[0])
	for index, line := range lines {
		if !strings.ContainsAny(line, "│┼") {
			continue
		}
		if actual := profile.borderColumns(line); !reflect.DeepEqual(
			actual,
			expected,
		) {
			t.Fatalf(
				"profile %q styled/uneven row %d columns=%v, want %v",
				profile.id,
				index,
				actual,
				expected,
			)
		}
	}
	plain := xansi.Strip(rendered)
	for _, want := range []string{"red", "site", "tail"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("styled/uneven table lost %q:\n%s", want, plain)
		}
	}
}

func TestG9AIndependentProfileIgnoresANSIAndOSCGeometry(t *testing.T) {
	profile := newG9EvidenceWidthProfile()
	plain := "red link"
	styled := "\x1b[31mred\x1b[0m " +
		"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"
	if got := profile.cells(styled); got != profile.cells(plain) {
		t.Fatalf(
			"profile %q styled width=%d, plain width=%d",
			profile.id,
			got,
			profile.cells(plain),
		)
	}
}
