package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestP272AnnotationMarkersAreZeroCell(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	if !selectionMarkersHaveZeroCells(profile) {
		t.Fatal("selection annotation markers must all measure zero cells")
	}
}

func TestP272AnnotationParserPublishesExactSemanticRows(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	annotated := selectionPresentation("> ") +
		selectionMarkSemanticStart +
		"\x1b[31mfirst  \x1b[0m\nsecond" +
		selectionMarkSemanticEnd +
		selectionHardBreak() +
		selectionSemantic("third")

	lines, rows, valid := parseSelectionAnnotations(profile, annotated)
	if !valid {
		t.Fatal("valid annotation stream was rejected")
	}
	if want := []string{
		"> \x1b[31mfirst  \x1b[0m",
		"second",
		"third",
	}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("visible lines = %#v, want %#v", lines, want)
	}
	if len(rows) != len(lines) {
		t.Fatalf("metadata rows = %d, visible rows = %d", len(rows), len(lines))
	}
	if rows[0].boundary != selectionBoundarySoft {
		t.Fatalf("first boundary = %v, want soft", rows[0].boundary)
	}
	if rows[1].boundary != selectionBoundaryHard {
		t.Fatalf("second boundary = %v, want hard", rows[1].boundary)
	}
	if got := selectionRowText(profile, rows[0], 0, 100); got != "first  " {
		t.Fatalf("first semantic row = %q, want trailing whitespace preserved", got)
	}
	if got := selectionRowText(profile, rows[1], 0, 100); got != "second" {
		t.Fatalf("second semantic row = %q", got)
	}
	if got := selectionRowText(profile, rows[2], 0, 100); got != "third" {
		t.Fatalf("third semantic row = %q", got)
	}
	if start, end, ok := selectionRowCellBounds(rows[0]); !ok || start != 2 || end != 9 {
		t.Fatalf("first semantic cell bounds = (%d, %d, %v), want (2, 9, true)", start, end, ok)
	}
}

func TestP272AnnotationParserKeepsDisjointSemanticSpans(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	annotated := selectionSemantic("left") +
		selectionPresentation(" | ") +
		selectionSemantic("right")

	lines, rows, valid := parseSelectionAnnotations(profile, annotated)
	if !valid || len(lines) != 1 || len(rows) != 1 {
		t.Fatalf("parse result valid=%v lines=%#v rows=%#v", valid, lines, rows)
	}
	if got := selectionRowText(profile, rows[0], 0, 100); got != "leftright" {
		t.Fatalf("semantic text = %q, want %q", got, "leftright")
	}
	if got := selectionRowRanges(rows[0], 0, 100); !reflect.DeepEqual(
		got,
		[][2]int{{0, 4}, {7, 12}},
	) {
		t.Fatalf("semantic ranges = %#v", got)
	}
}

func TestP272AnnotationParserClampsToExtendedGraphemeClusters(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	annotated := selectionPresentation(">") +
		selectionSemantic("中e\u0301👩🏽‍💻x")

	_, rows, valid := parseSelectionAnnotations(profile, annotated)
	if !valid || len(rows) != 1 {
		t.Fatalf("parse result valid=%v rows=%#v", valid, rows)
	}
	tests := []struct {
		name       string
		start, end int
		want       string
	}{
		{name: "wide cluster from middle", start: 2, end: 3, want: "中"},
		{name: "combining cluster", start: 3, end: 4, want: "e\u0301"},
		{name: "zwj cluster from middle", start: 5, end: 6, want: "👩🏽‍💻"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selectionRowText(
				profile,
				rows[0],
				test.start,
				test.end,
			); got != test.want {
				t.Fatalf("semantic slice = %q, want %q", got, test.want)
			}
		})
	}
}

func TestP272AnnotationParserRestoresTabSourceBytes(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	annotated := selectionPresentation(">") +
		selectionMarkSemanticStart +
		"a" + selectionSemanticTab() + "b" +
		selectionMarkSemanticEnd

	lines, rows, valid := parseSelectionAnnotations(profile, annotated)
	if !valid {
		t.Fatal("valid tab annotation stream was rejected")
	}
	if got, want := selectionPlainLine(lines[0]), ">a\tb"; got != want {
		t.Fatalf("visible line = %q, want %q", got, want)
	}
	if got := selectionRowText(profile, rows[0], 0, 100); got != "a\tb" {
		t.Fatalf("semantic tab text = %q", got)
	}
}

func TestP272AnnotationParserFailsClosedWithoutLeakingMarkers(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	annotated := selectionMarkSemanticStart + "unterminated"

	lines, rows, valid := parseSelectionAnnotations(profile, annotated)
	if valid {
		t.Fatal("malformed annotation stream must fail closed")
	}
	if len(rows) != 1 || rows[0].selectable || len(rows[0].spans) != 0 {
		t.Fatalf("malformed metadata must be nonselectable: %#v", rows)
	}
	for _, marker := range selectionAnnotationMarkers {
		if strings.Contains(strings.Join(lines, "\n"), marker) {
			t.Fatalf("renderer-private marker leaked into visible output: %q", marker)
		}
	}
}

func TestP272AnnotationParserInvalidatesEveryRowInUnclosedScope(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	tests := []struct {
		name      string
		annotated string
	}{
		{
			name: "semantic",
			annotated: selectionSemantic("sibling") + "\n" +
				selectionMarkSemanticStart + "first\nsecond",
		},
		{
			name: "hard rows",
			annotated: selectionSemantic("sibling") + "\n" +
				selectionMarkHardRowsStart +
				selectionSemantic("first\nsecond"),
		},
		{
			name: "tab",
			annotated: selectionSemantic("sibling") + "\n" +
				selectionMarkSemanticStart + "first" +
				selectionMarkTabStart + "\t\nsecond",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines, rows, valid := parseSelectionAnnotations(profile, test.annotated)
			if valid {
				t.Fatal("unclosed annotation scope was accepted")
			}
			if len(rows) != 3 {
				t.Fatalf("metadata rows = %d, want 3", len(rows))
			}
			if got := selectionRowText(profile, rows[0], 0, 100); got != "sibling" {
				t.Fatalf("completed sibling row = %q, want selectable sibling", got)
			}
			for _, marker := range selectionAnnotationMarkers {
				if strings.Contains(strings.Join(lines, "\n"), marker) {
					t.Fatalf("renderer-private marker leaked into visible output: %q", marker)
				}
			}
			for index := 1; index < len(rows); index++ {
				if rows[index].selectable || len(rows[index].spans) != 0 ||
					rows[index].boundary != selectionBoundaryNone {
					t.Fatalf("row %d in unclosed scope remained selectable: %#v", index, rows[index])
				}
			}
		})
	}
}

const p272FuzzCellSampleLimit = 64

func p272FuzzCellSamples(startCell, endCell int) []int {
	width := endCell - startCell
	if width <= 0 {
		return nil
	}
	sampleCount := min(width, p272FuzzCellSampleLimit)
	result := make([]int, 0, sampleCount)
	if sampleCount == 1 {
		return append(result, startCell)
	}
	for index := 0; index < sampleCount; index++ {
		result = append(
			result,
			startCell+index*(width-1)/(sampleCount-1),
		)
	}
	return result
}

func FuzzP272AnnotationRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"plain text",
		"中e\u0301👩🏽‍💻",
		"alpha  beta   ",
		"क्ष🏳️‍🌈tail",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		value = strings.ToValidUTF8(value, string(unicode.ReplacementChar))
		value = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' ||
				r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, value)
		if selectionAnnotationsCollide(value) {
			t.Skip()
		}

		profile := DefaultDisplayCellProfile()
		lines, rows, valid := parseSelectionAnnotations(
			profile,
			selectionPresentation("> ")+selectionSemantic(value),
		)
		if !valid || len(lines) != 1 || len(rows) != 1 {
			t.Fatalf(
				"round trip parse valid=%v lines=%#v rows=%#v",
				valid,
				lines,
				rows,
			)
		}
		if got := selectionPlainLine(lines[0]); got != "> "+value {
			t.Fatalf("visible round trip = %q, want %q", got, "> "+value)
		}
		start, end, selectable := selectionRowCellBounds(rows[0])
		semanticStart := profile.width("> ")
		semanticEnd := profile.width("> " + value)
		semanticCells := semanticEnd - semanticStart
		if value == "" || semanticCells <= 0 ||
			profile.measure(value, semanticStart) != semanticCells {
			if selectable {
				t.Fatal("semantic value without independent geometry became selectable")
			}
			return
		}
		if !selectable {
			t.Fatalf("semantic value with independent cells is not selectable: %q", value)
		}
		if got := selectionRowText(profile, rows[0], start, end); got != value {
			t.Fatalf("semantic round trip = %q, want %q", got, value)
		}
		// selectionRowText intentionally re-segments the semantic span for exact
		// byte boundaries. Keep exhaustive coverage for ordinary short values,
		// but bound long-input fuzz work so this oracle stays linear in input size.
		for _, cell := range p272FuzzCellSamples(start, end) {
			got := selectionRowText(profile, rows[0], cell, cell+1)
			if !utf8.ValidString(got) {
				t.Fatalf("cell %d split UTF-8: %q", cell, got)
			}
		}
	})
}

func TestP272FuzzCellSamplesBoundWorkAndKeepEdges(t *testing.T) {
	t.Run("short ranges keep every cell", func(t *testing.T) {
		got := p272FuzzCellSamples(3, 8)
		want := []int{3, 4, 5, 6, 7}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("samples = %#v, want %#v", got, want)
		}
	})

	t.Run("long ranges stay bounded and keep both edges", func(t *testing.T) {
		got := p272FuzzCellSamples(11, 1011)
		if len(got) != p272FuzzCellSampleLimit {
			t.Fatalf(
				"sample count = %d, want %d",
				len(got),
				p272FuzzCellSampleLimit,
			)
		}
		if got[0] != 11 || got[len(got)-1] != 1010 {
			t.Fatalf("sample edges = %d..%d, want 11..1010", got[0], got[len(got)-1])
		}
		for index := 1; index < len(got); index++ {
			if got[index] <= got[index-1] {
				t.Fatalf("samples are not strictly increasing: %#v", got)
			}
		}
	})

	if got := p272FuzzCellSamples(7, 7); got != nil {
		t.Fatalf("empty range samples = %#v, want nil", got)
	}
}

func TestSelectionAnnotationFragmentsFailClosed(t *testing.T) {
	for _, value := range []string{"\u200b", "\u200c", "\u2060", "tail\u200b"} {
		if !selectionAnnotationsCollide(value) {
			t.Fatalf("private marker fragment did not collide: %q", value)
		}
	}
}
