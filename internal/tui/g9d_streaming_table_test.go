package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestG9DOnlyActiveLastTopLevelTableIsDeferred(t *testing.T) {
	first := "| First |\n| --- |\n| stable |\n"
	last := "| Last |\n| --- |\n| active |\n"
	source := first + "\n" + last

	stream := &StreamingMarkdown{}
	rendered := stream.Render(source, 48, ThemePolarNight)
	plain := xansi.Strip(rendered)
	if got := strings.Count(plain, "┌"); got != 1 {
		t.Fatalf("semantic complete-table count=%d, want 1:\n%s", got, plain)
	}
	for _, literal := range []string{"| Last |", "| --- |", "| active |"} {
		if !strings.Contains(plain, literal) {
			t.Fatalf("active last table lost literal %q:\n%s", literal, plain)
		}
	}
}

func TestG9DNestedCompleteAndDeferredContainerProjection(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		itemLine    string
		tablePrefix string
	}{
		{
			name:        "blockquote",
			source:      "> | A | B |\n> | --- | --- |\n> | x | y |\n",
			tablePrefix: "▎ ",
		},
		{
			name:        "unordered-list",
			source:      "- item\n\n    | A | B |\n    | --- | --- |\n    | x | y |\n",
			itemLine:    "• item",
			tablePrefix: "",
		},
		{
			name:        "ordered-list",
			source:      "1. item\n\n    | A | B |\n    | --- | --- |\n    | x | y |\n",
			itemLine:    "1. item",
			tablePrefix: "",
		},
		{
			name:        "blockquote-list",
			source:      "> - item\n>\n>     | A | B |\n>     | --- | --- |\n>     | x | y |\n",
			itemLine:    "▎ • item",
			tablePrefix: "▎ ",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &StreamingMarkdown{}
			live := stream.Render(test.source, 40, ThemePolarNight)
			livePlain := xansi.Strip(live)
			if strings.Contains(livePlain, "┌") {
				t.Fatalf("active nested table selected semantic owner:\n%s", livePlain)
			}
			for _, line := range []string{"| A | B |", "| --- | --- |", "| x | y |"} {
				if !strings.Contains(livePlain, test.tablePrefix+line) {
					t.Fatalf("active literal lost container projection %q:\n%s", test.tablePrefix+line, livePlain)
				}
			}
			if test.itemLine != "" && strings.Count(livePlain, test.itemLine) != 1 {
				t.Fatalf("active list marker count for %q != 1:\n%s", test.itemLine, livePlain)
			}

			stream.Finalize(test.source)
			complete := stream.Render(test.source, 40, ThemePolarNight)
			completePlain := xansi.Strip(complete)
			if !strings.Contains(completePlain, test.tablePrefix+"┌") {
				t.Fatalf("complete nested table lost semantic box prefix %q:\n%s", test.tablePrefix, completePlain)
			}
			if test.itemLine != "" && strings.Count(completePlain, test.itemLine) != 1 {
				t.Fatalf("complete list marker count for %q != 1:\n%s", test.itemLine, completePlain)
			}
			for _, line := range strings.Split(complete, "\n") {
				assertWidthProfileControlStateClosed(t, DefaultDisplayCellProfile(), line)
			}
		})
	}
}

func TestG9DRendererAndProjectionCacheIdentityIncludesProfileAndCompleteness(t *testing.T) {
	source := "| A | B |\n| --- | --- |\n| x | y |\n\nTail"
	theme := markdownThemeForName(ThemePolarNight)
	firstProfile := DefaultDisplayCellProfile()
	secondProfile := firstProfile
	secondProfile.id += "-test-generation"

	env := defaultRenderEnvironment(StylesForTheme(theme.name))
	env.profile = firstProfile
	firstEntry := getRendererWithEnvironment(48, theme, env)
	env.profile = secondProfile
	if firstEntry == getRendererWithEnvironment(48, theme, env) {
		t.Fatal("renderer cache reused a different width-profile generation")
	}

	stream := &StreamingMarkdown{}
	stream.renderWithProfile(source, 48, ThemePolarNight, firstProfile)
	if stream.fullCacheIdentity.displayCellProfileID != firstProfile.id ||
		stream.fullCacheIdentity.completeness != markdownStreamingIncomplete ||
		stream.stableCacheIdentity.displayCellProfileID != firstProfile.id ||
		stream.stableCacheIdentity.completeness != markdownStableComplete {
		t.Fatalf(
			"first cache identity full=%#v stable=%#v",
			stream.fullCacheIdentity,
			stream.stableCacheIdentity,
		)
	}

	stream.renderWithProfile(source, 48, ThemePolarNight, secondProfile)
	if stream.fullCacheIdentity.displayCellProfileID != secondProfile.id ||
		stream.stableCacheIdentity.displayCellProfileID != secondProfile.id {
		t.Fatalf(
			"profile change retained old projection: full=%#v stable=%#v",
			stream.fullCacheIdentity,
			stream.stableCacheIdentity,
		)
	}

	stream.Finalize(source)
	stream.renderWithProfile(source, 48, ThemePolarNight, secondProfile)
	if stream.fullCacheIdentity.completeness != markdownFinalizedComplete ||
		stream.fullCacheIdentity.source != source ||
		stream.fullCacheIdentity.width != 48 ||
		stream.fullCacheIdentity.theme != theme ||
		stream.fullCacheIdentity.colorProfile != theme.colorProfile() {
		t.Fatalf("finalized cache identity incomplete: %#v", stream.fullCacheIdentity)
	}
}

func TestG9DSentinelCollisionAndFailClosedCardinality(t *testing.T) {
	source := "literal ET0T\n\n" +
		"| A |\n| --- |\n| one |\n\n" +
		"| B |\n| --- |\n| two |\n"
	stripped, islands, ok := extractTableIslands(source, markdownStableComplete)
	if !ok || len(islands) != 2 {
		t.Fatalf("island extraction ok=%v count=%d", ok, len(islands))
	}
	if islands[0].token != "ET1T" || islands[1].token != "ET2T" {
		t.Fatalf("collision-free tokens=%q,%q", islands[0].token, islands[1].token)
	}
	if strings.Contains(stripped, "\nET0T\n") {
		t.Fatalf("source collision was selected as sentinel:\n%s", stripped)
	}

	theme := markdownThemeForName(ThemePolarNight)
	profile := DefaultDisplayCellProfile()
	if _, ok := spliceTableIslands(
		"  "+islands[0].token+"\n  "+islands[0].token,
		islands[:1],
		40,
		theme,
		profile,
	); ok {
		t.Fatal("duplicate sentinel did not fail closed")
	}
	if _, ok := spliceTableIslands(
		"missing",
		islands[:1],
		40,
		theme,
		profile,
	); ok {
		t.Fatal("missing sentinel did not fail closed")
	}
}

func TestG9DDeferredLiteralSanitizesAndBoundsPhysicalLines(t *testing.T) {
	source := "| Key | Value |\n" +
		"| --- | --- |\n" +
		"| long | abc\x1b[2Jdef\u009bghi\tjklmnopqrstuvwxyz |\n"
	stream := &StreamingMarkdown{}
	rendered := stream.Render(source, 24, ThemePolarNight)
	if strings.Contains(rendered, "\x1b[2J") || strings.ContainsRune(rendered, '\u009b') {
		t.Fatalf("deferred literal emitted source terminal controls: %q", rendered)
	}
	if !utf8.ValidString(rendered) {
		t.Fatalf("deferred literal emitted invalid UTF-8: %q", rendered)
	}
	profile := DefaultDisplayCellProfile()
	for index, line := range strings.Split(rendered, "\n") {
		if cells := profile.width(line); cells > 24 {
			t.Fatalf("line %d width=%d > 24: %q", index, cells, xansi.Strip(line))
		}
		assertWidthProfileControlStateClosed(t, profile, line)
	}

	invalid := "| A |\n| --- |\n| bad \xff byte |\n"
	if got := stream.Render(invalid, 24, ThemePolarNight); !utf8.ValidString(got) {
		t.Fatalf("invalid source byte escaped deferred literal: %q", got)
	}
}

func TestG9DAppendBoundariesRemainLiteralUntilFinalize(t *testing.T) {
	source := "| Key | Value |\n| --- | --- |\n| emoji | 👩🏽‍💻 |\n"
	stream := &StreamingMarkdown{}
	for end := range source {
		rendered := stream.Render(source[:end], 40, ThemePolarNight)
		if !utf8.ValidString(rendered) {
			t.Fatalf("append boundary %d emitted invalid UTF-8", end)
		}
	}
	live := stream.Render(source, 40, ThemePolarNight)
	if strings.Contains(xansi.Strip(live), "┌") {
		t.Fatalf("active terminal table selected semantic owner:\n%s", xansi.Strip(live))
	}
	stream.Finalize(source)
	complete := stream.Render(source, 40, ThemePolarNight)
	if !strings.Contains(xansi.Strip(complete), "┌") {
		t.Fatalf("Finalize did not select semantic owner:\n%s", xansi.Strip(complete))
	}
}

func TestG9DSemanticIslandSentinelSurvivesSupportedThemesAndWidths(t *testing.T) {
	sources := []string{
		"| A | B |\n| --- | --- |\n| x | y |\n",
		"> | A | B |\n> | --- | --- |\n> | x | y |\n",
		"- item\n\n    | A | B |\n    | --- | --- |\n    | x | y |\n",
		"> - item\n>\n>     | A | B |\n>     | --- | --- |\n>     | x | y |\n",
	}
	for _, theme := range supportedThemeNames {
		for _, width := range []int{10, 18, 48} {
			for sourceIndex, source := range sources {
				stream := &StreamingMarkdown{}
				stream.Finalize(source)
				rendered := stream.Render(source, width, theme)
				plain := xansi.Strip(rendered)
				if !strings.Contains(plain, "A") || !strings.Contains(plain, "x") {
					t.Fatalf(
						"theme=%s width=%d source=%d lost table content:\n%s",
						theme,
						width,
						sourceIndex,
						plain,
					)
				}
				if !strings.Contains(plain, "┌") && !strings.Contains(plain, "A:") {
					t.Fatalf(
						"theme=%s width=%d source=%d did not use semantic owner:\n%s",
						theme,
						width,
						sourceIndex,
						plain,
					)
				}
				_, islands, ok := extractTableIslands(
					prepareForGlamour(source),
					markdownFinalizedComplete,
				)
				if !ok || len(islands) != 1 {
					t.Fatalf("theme=%s width=%d source=%d extraction failed", theme, width, sourceIndex)
				}
				if strings.Contains(rendered, islands[0].token) {
					t.Fatalf(
						"theme=%s width=%d source=%d leaked sentinel %q",
						theme,
						width,
						sourceIndex,
						islands[0].token,
					)
				}
				for _, line := range strings.Split(rendered, "\n") {
					assertWidthProfileControlStateClosed(t, DefaultDisplayCellProfile(), line)
					if cells := DefaultDisplayCellProfile().width(line); cells > width {
						t.Fatalf(
							"theme=%s width=%d source=%d physical line width=%d: %q",
							theme,
							width,
							sourceIndex,
							cells,
							xansi.Strip(line),
						)
					}
				}
			}
		}
	}
}

func TestG9DNarrowSemanticFallbackBoundsLongLabelsAndValues(t *testing.T) {
	for _, source := range []string{
		"| HeaderLong | Value |\n| --- | --- |\n| abcdefghijklmnop | qrstuvwxyz |\n",
		"> | HeaderLong | Value |\n> | --- | --- |\n> | abcdefghijklmnop | qrstuvwxyz |\n",
	} {
		stream := &StreamingMarkdown{}
		stream.Finalize(source)
		rendered := stream.Render(source, 10, ThemePolarNight)
		plain := xansi.Strip(rendered)
		joined := strings.NewReplacer("\n", "", " ", "", "▎", "").Replace(plain)
		for _, want := range []string{"HeaderLong:", "abcdefghijklmnop", "qrstuvwxyz"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("narrow fallback lost %q:\n%s", want, plain)
			}
		}
		for index, line := range strings.Split(rendered, "\n") {
			if cells := DefaultDisplayCellProfile().width(line); cells > 10 {
				t.Fatalf("line %d width=%d > 10: %q", index, cells, xansi.Strip(line))
			}
			assertWidthProfileControlStateClosed(t, DefaultDisplayCellProfile(), line)
		}
	}
}
