package tui

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestWidthProfile(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	if profile.id == "" {
		t.Fatal("default profile identity is empty")
	}
	if profile.options.EastAsianWidth || !profile.options.ControlSequences {
		t.Fatalf("unexpected profile options: %+v", profile.options)
	}
	if got := profile.width("\x1b[31mA\x1b[0m"); got != 1 {
		t.Fatalf("ANSI width = %d, want 1", got)
	}
}

func TestWidthProfileMatchesIndependentG9Oracle(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	oracle := newG9EvidenceWidthProfile()
	for name, cluster := range map[string]string{
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
	} {
		if got, want := profile.width(cluster), oracle.cells(cluster); got != want {
			t.Errorf(
				"profile %q case=%q cluster=%q width=%d, independent oracle=%d",
				profile.id,
				name,
				cluster,
				got,
				want,
			)
		}
	}
}

func TestWidthProfileWrapProperties(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	cases := []string{
		"plain ASCII text",
		"e\u0301cole",
		"👨‍👩‍👧‍👦 family",
		"क्\u200dष",
		"\x1b[31mred\x1b[0m and \x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
	}
	for _, input := range cases {
		for width := 1; width <= 12; width++ {
			lines := profile.wrap(input, width, true)
			if got := xansi.Strip(strings.Join(lines, "")); got != xansi.Strip(input) {
				t.Fatalf("wrap(%q, %d) changed visible bytes: got %q", input, width, got)
			}
			for _, line := range lines {
				if got := profile.width(line); got > width &&
					visibleClusterCount(profile, line) != 1 {
					t.Fatalf(
						"wrap(%q, %d) line width=%d with multiple visible clusters",
						input,
						width,
						got,
					)
				}
			}
		}
	}
}

func TestWidthProfileWordWrapUsesProfileBoundaries(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	input := "\x1b[31malpha beta 👩🏽‍💻\x1b[0m"
	lines := profile.wrap(input, 7, false)
	for _, line := range lines {
		if got := profile.width(line); got > 7 &&
			visibleClusterCount(profile, line) != 1 {
			t.Fatalf("word-wrapped line width=%d, want <=7: %q", got, line)
		}
	}
	gotBytes := strings.ReplaceAll(xansi.Strip(strings.Join(lines, "")), " ", "")
	wantBytes := strings.ReplaceAll(xansi.Strip(input), " ", "")
	if gotBytes != wantBytes {
		t.Fatalf("word wrap changed non-break bytes: got %q, want %q", gotBytes, wantBytes)
	}
}

func TestWidthProfileWrapBalancesSGRAndOSC8PerLine(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	for name, input := range map[string]string{
		"sgr":  "\x1b[31mabcdef\x1b[0m",
		"osc8": "\x1b]8;;https://example.com\x1b\\abcdef\x1b]8;;\x1b\\",
		"both": "\x1b]8;;https://example.com\x1b\\\x1b[31mabcdef\x1b[0m\x1b]8;;\x1b\\",
	} {
		t.Run(name, func(t *testing.T) {
			lines := profile.wrap(input, 3, true)
			if len(lines) != 2 {
				t.Fatalf("wrapped lines=%d, want 2: %#v", len(lines), lines)
			}
			if got := xansi.Strip(strings.Join(lines, "")); got != "abcdef" {
				t.Fatalf("visible wrapped content=%q, want abcdef", got)
			}
			for index, line := range lines {
				assertWidthProfileControlStateClosed(t, profile, line)
				if got := profile.width(line); got != 3 {
					t.Fatalf("line %d width=%d, want 3: %q", index, got, line)
				}
			}
		})
	}
}

func TestRenderedTableClosesCellControlsBeforeBorders(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	for name, value := range map[string]string{
		"sgr":  "\x1b[31mabcdef\x1b[0m",
		"osc8": "\x1b]8;;https://example.com\x1b\\abcdef\x1b]8;;\x1b\\",
	} {
		t.Run(name, func(t *testing.T) {
			table := &parsedTable{
				headers: []tableCell{{raw: "Key"}},
				aligns:  []string{"left"},
				rows:    [][]tableCell{{{raw: value}}},
			}
			rendered := renderTableWithThemeAndProfile(
				table,
				12,
				markdownThemeForName(ThemePolarNight),
				profile,
			)
			if !strings.Contains(xansi.Strip(rendered), "abcd") {
				t.Fatalf("rendered table lost wrapped content: %q", rendered)
			}
			for index, line := range strings.Split(rendered, "\n") {
				assertWidthProfileControlStateClosed(t, profile, line)
				if got := profile.width(line); got > 8 {
					t.Fatalf("line %d width=%d, want <=8: %q", index, got, line)
				}
			}
		})
	}
}

func TestWidthProfileTruncatePreservesClustersAndControls(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	input := "\x1b[31m👩🏽‍💻red\x1b[0m " +
		"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"
	truncated := profile.truncate(input, 2)
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncate returned invalid UTF-8: %q", truncated)
	}
	if got := profile.width(truncated); got != 2 {
		t.Fatalf("truncate width=%d, want 2: %q", got, truncated)
	}
	for _, control := range []string{
		"\x1b[31m",
		"\x1b[0m",
		"\x1b]8;;https://example.com\x1b\\",
		"\x1b]8;;\x1b\\",
	} {
		if !strings.Contains(truncated, control) {
			t.Fatalf("truncate lost ANSI/OSC control %q: %q", control, truncated)
		}
	}
	if !strings.Contains(truncated, "👩🏽‍💻") {
		t.Fatalf("truncate split the leading grapheme cluster: %q", truncated)
	}
}

func FuzzWidthProfileClusterAndControlPreservation(f *testing.F) {
	for _, seed := range []string{
		"e\u0301", "👩🏽\u200d💻", "🇺🇳", "क्\u200dष", "\x1b[31mred\x1b[0m", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
	} {
		f.Add(seed, uint8(4))
	}
	f.Fuzz(func(t *testing.T, input string, width uint8) {
		if !utf8.ValidString(input) {
			t.Skip()
		}
		profile := DefaultDisplayCellProfile()
		want, ok := expectedWidthProfileFuzzProjection(profile, input)
		if !ok {
			t.Skip()
		}
		// x/ansi is the independent decoder for this property. Some valid OSC-8
		// payloads containing combining Unicode are outside its parse domain; do
		// not attribute that pre-existing decoder disagreement to wrapping.
		if sourceProjection := strings.ReplaceAll(xansi.Strip(input), "\n", ""); sourceProjection != want {
			t.Skip()
		}
		limit := int(width%32) + 1
		lines := profile.wrap(input, limit, true)
		got := strings.ReplaceAll(xansi.Strip(strings.Join(lines, "")), "\n", "")
		if got != want {
			t.Fatalf("wrap changed bytes: got %q, want %q", got, want)
		}
		for _, line := range lines {
			assertWidthProfileControlStateClosed(t, profile, line)
			lineWidth := profile.width(line)
			if lineWidth > limit && visibleClusterCount(profile, line) != 1 {
				t.Fatalf(
					"line width %d exceeds limit %d with multiple visible clusters for %q",
					lineWidth,
					limit,
					line,
				)
			}
		}
		truncated := profile.truncate(input, limit)
		if !utf8.ValidString(truncated) {
			t.Fatalf("truncate returned invalid UTF-8: %q", truncated)
		}
		if got := profile.width(truncated); got > limit {
			t.Fatalf("truncate width %d exceeds limit %d for %q", got, limit, truncated)
		}
	})
}

func expectedWidthProfileFuzzProjection(
	profile DisplayCellProfile,
	input string,
) (string, bool) {
	iter := profile.options.StringGraphemes(input)
	var visible strings.Builder
	for iter.Next() {
		cluster := iter.Value()
		// Tabs expand from the owning rectangle origin, so their exact byte
		// projection depends on wrapping state rather than only on the input.
		if cluster == "\t" {
			return "", false
		}
		if cluster == "\n" ||
			isSupportedDisplayCellControl(cluster, iter.Width()) {
			continue
		}
		visible.WriteString(strings.Map(expectedWidthProfileRune, cluster))
	}
	return visible.String(), true
}

func expectedWidthProfileRune(r rune) rune {
	switch {
	case unicode.IsControl(r):
		return unicode.ReplacementChar
	case r == '\u061c' || r == '\u200e' || r == '\u200f':
		return unicode.ReplacementChar
	case r >= '\u202a' && r <= '\u202e':
		return unicode.ReplacementChar
	case r >= '\u2066' && r <= '\u2069':
		return unicode.ReplacementChar
	default:
		return r
	}
}

func TestWidthProfileControlValidatorsRejectEmbeddedUnsafeRunes(t *testing.T) {
	for _, test := range []struct {
		name     string
		sequence string
		sgr      bool
		osc8     bool
	}{
		{name: "sgr", sequence: "\x1b[1;31m", sgr: true},
		{name: "sgr-reset", sequence: "\x1b[m", sgr: true},
		{name: "sgr-colon", sequence: "\x1b[38:2::255:0:0m", sgr: true},
		{name: "sgr-control", sequence: "\x1b[31\x10m"},
		{name: "sgr-private-greater", sequence: "\x1b[>4;2m"},
		{name: "sgr-private-question", sequence: "\x1b[?4m"},
		{name: "sgr-intermediate", sequence: "\x1b[1 m"},
		{name: "osc8", sequence: "\x1b]8;;https://example.com\x1b\\", osc8: true},
		{name: "osc8-control", sequence: "\x1b]8;;\x1b\x10\x1b\\"},
		{name: "osc8-bidi", sequence: "\x1b]8;;https://example.com/\u202e\x1b\\"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isSGRSequence(test.sequence); got != test.sgr {
				t.Fatalf("isSGRSequence(%q)=%t, want %t", test.sequence, got, test.sgr)
			}
			if got := isOSC8Sequence(test.sequence); got != test.osc8 {
				t.Fatalf("isOSC8Sequence(%q)=%t, want %t", test.sequence, got, test.osc8)
			}
		})
	}

	privateKeyboardMode := "\x1b[>4;2m"
	balanced := DefaultDisplayCellProfile().balanceControlLines([]string{
		privateKeyboardMode + "first",
		"second",
	})
	if strings.Contains(balanced[1], privateKeyboardMode) {
		t.Fatalf("private keyboard mode was replayed across lines: %q", balanced[1])
	}
}

func visibleClusterCount(profile DisplayCellProfile, input string) int {
	iter := profile.options.StringGraphemes(input)
	count := 0
	for iter.Next() {
		if profile.clusterWidth(iter.Value(), iter.Width()) > 0 {
			count++
		}
	}
	return count
}

func assertWidthProfileControlStateClosed(
	t *testing.T,
	profile DisplayCellProfile,
	line string,
) {
	t.Helper()
	state := displayCellControlState{}
	state.observe(profile, line)
	if state.sgrReplay != "" || state.osc8Open != "" {
		t.Fatalf(
			"line leaked control state: sgr=%q osc8=%q line=%q",
			state.sgrReplay,
			state.osc8Open,
			line,
		)
	}
}
