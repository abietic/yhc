package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/muesli/termenv"
)

const markdownThemeFixture = `# ALPHA-H1

## BETA-H2

### GAMMA-H3

Body with ` + "`DELTA-CODE`" + `.

> EPSILON-QUOTE

---

` + "```go" + `
fmt.Println("ANSI-SAFE")
` + "```" + `
`

func TestMarkdownThemeRenderGolden(t *testing.T) {
	var got strings.Builder
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		rendered := (&StreamingMarkdown{}).Render(
			markdownThemeFixture,
			48,
			theme,
		)
		snapshot := markdownThemeSnapshot(theme, rendered)
		got.WriteString(snapshot)
		got.WriteByte('\n')

		if theme == ThemeDarkAnsi {
			for _, forbidden := range []string{"38;2;", "48;2;", "38;5;", "48;5;"} {
				if strings.Contains(rendered, forbidden) {
					t.Errorf("%s render contains non-ANSI-16 escape %q", theme, forbidden)
				}
			}
		}
	}

	want, err := os.ReadFile("testdata/markdown_theme.golden")
	if err != nil {
		t.Fatalf("read Markdown theme golden: %v", err)
	}
	if strings.TrimSpace(got.String()) != strings.TrimSpace(string(want)) {
		t.Fatalf(
			"Markdown theme golden mismatch:\n--- got ---\n%s--- want ---\n%s",
			got.String(),
			want,
		)
	}
}

func TestMarkdownThemeUsesSemanticPaletteTokens(t *testing.T) {
	for _, name := range []ThemeName{
		ThemePolarNight,
		ThemeDaybreak,
		ThemeDarkAnsi,
	} {
		t.Run(string(name), func(t *testing.T) {
			theme := markdownThemeForName(name)
			rendered := (&StreamingMarkdown{}).Render(
				markdownThemeFixture,
				48,
				name,
			)
			fields := markdownThemeSGRFields(rendered)
			want := map[string]string{
				"h1":         expectedColorSGR(theme.colorProfile(), theme.brand, ""),
				"h2":         expectedColorSGR(theme.colorProfile(), theme.sky, ""),
				"h3":         expectedColorSGR(theme.colorProfile(), theme.violet, ""),
				"inlineCode": expectedColorSGR(theme.colorProfile(), theme.sky, theme.element),
				"quoteBar":   expectedColorSGR(theme.colorProfile(), theme.brand, ""),
				"quoteText":  expectedColorSGR(theme.colorProfile(), theme.inactive, ""),
				"rule":       expectedColorSGR(theme.colorProfile(), theme.borderSubtle, ""),
			}
			for field, wantSGR := range want {
				if !strings.Contains(fields[field], wantSGR) {
					t.Errorf("%s SGR = %q, want color fragment %q", field, fields[field], wantSGR)
				}
			}
		})
	}
}

func TestStreamingMarkdownThemeSwitchInvalidatesNestedCaches(t *testing.T) {
	source := "# STABLE-H1\n\nA complete paragraph.\n\nMutable tail"
	polar := StylesForTheme(ThemePolarNight)
	daybreak := StylesForTheme(ThemeDaybreak)
	stream := &StreamingMarkdown{}

	before := stream.Render(source, 52, polar.theme)
	if stream.stablePrefix == "" {
		t.Fatal("test requires a populated stable-prefix cache")
	}
	after := stream.Render(source, 52, daybreak.theme)
	fresh := (&StreamingMarkdown{}).Render(source, 52, daybreak.theme)
	if after != fresh {
		t.Fatal("theme switch reused a stale stable or full Markdown cache")
	}
	if before == after {
		t.Fatal("theme switch did not change themed Markdown output")
	}

	stream.Finalize(source)
	finalized := stream.Render(source, 52, daybreak.theme)
	if !stream.finalized {
		t.Fatal("canonical finalized render lost its lifecycle state")
	}
	switchedFinal := stream.Render(source, 52, polar.theme)
	freshFinal := (&StreamingMarkdown{}).Render(source, 52, polar.theme)
	if switchedFinal != freshFinal {
		t.Fatal("finalized theme switch did not rerender from canonical source")
	}
	if finalized == switchedFinal {
		t.Fatal("finalized theme switch retained the previous palette")
	}
	if !stream.finalized {
		t.Fatal("theme invalidation cleared finalized lifecycle state")
	}
}

func TestPlanDialogMarkdownUsesLiveStyles(t *testing.T) {
	dialog := NewPlanDialog(StylesForTheme(ThemePolarNight))
	dialog.plan = markdownThemeFixture
	before := strings.Join(dialog.renderPlanMarkdown(48, 100), "\n")

	dialog.SetStyles(StylesForTheme(ThemeDaybreak))
	after := strings.Join(dialog.renderPlanMarkdown(48, 100), "\n")
	fresh := NewPlanDialog(StylesForTheme(ThemeDaybreak))
	fresh.plan = markdownThemeFixture
	if want := strings.Join(fresh.renderPlanMarkdown(48, 100), "\n"); after != want {
		t.Fatal("Plan dialog reused Markdown rendered under its previous styles")
	}
	if before == after {
		t.Fatal("Plan dialog Markdown did not change after SetStyles")
	}
}

func markdownThemeSnapshot(theme ThemeName, rendered string) string {
	fields := markdownThemeSGRFields(rendered)
	return fmt.Sprintf(
		"[%s]\nvisible=%q\nh1=%s\nh2=%s\nh3=%s\ninline-code=%s\nquote-bar=%s\nquote-text=%s\nrule=%s\n",
		theme,
		normalizeThemeVisible(rendered),
		fields["h1"],
		fields["h2"],
		fields["h3"],
		fields["inlineCode"],
		fields["quoteBar"],
		fields["quoteText"],
		fields["rule"],
	)
}

func markdownThemeSGRFields(rendered string) map[string]string {
	return map[string]string{
		"h1":         lastSGRBefore(rendered, "ALPHA-H1", false),
		"h2":         lastSGRBefore(rendered, "BETA-H2", false),
		"h3":         lastSGRBefore(rendered, "GAMMA-H3", false),
		"inlineCode": lastSGRBefore(rendered, "DELTA-CODE", false),
		"quoteBar":   lastSGRBefore(rendered, "▎", false),
		"quoteText":  lastSGRBefore(rendered, "EPSILON-QUOTE", false),
		"rule":       lastSGRBefore(rendered, "━━━━━━━━", true),
	}
}

func lastSGRBefore(rendered, token string, last bool) string {
	index := strings.Index(rendered, token)
	if last {
		index = strings.LastIndex(rendered, token)
	}
	if index < 0 {
		return "<missing>"
	}
	prefix := rendered[:index]
	start := strings.LastIndex(prefix, "\x1b[")
	if start < 0 {
		return "<none>"
	}
	end := strings.IndexByte(prefix[start:], 'm')
	if end < 0 {
		return "<unterminated>"
	}
	return strings.ReplaceAll(prefix[start:start+end+1], "\x1b", "<ESC>")
}

func normalizeThemeVisible(rendered string) string {
	lines := strings.Split(stripANSIForTest(rendered), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func expectedColorSGR(_ termenv.Profile, foreground, background string) string {
	style := lipgloss.NewStyle()
	if foreground != "" {
		style = style.Foreground(lipgloss.Color(foreground))
	}
	if background != "" {
		style = style.Background(lipgloss.Color(background))
	}
	sgr := lastSGRBefore(style.Render("marker"), "marker", false)
	sgr = strings.TrimPrefix(sgr, "<ESC>[")
	return strings.TrimSuffix(sgr, "m")
}
