package tui

import (
	"strings"
	"testing"
)

// Revontuli identity glyph contract (P19.2.0):
//   - system identity prefix: U+2727 outline star (✧)
//   - assistant prefix (final + streaming): U+2726 filled star (✦)
//   - model-status identity icon: U+2726 filled star (✦)
//
// Assistant and model-status identities use Styles.AssistantPrefix. User,
// tool-status, and spinner glyphs are outside this slice and remain free to
// evolve in their accepted follow-up slices.
const (
	expectedSystemStar    = "✧"
	expectedAssistantStar = "✦"
)

func TestGlyphIdentityConstantsUseRevontuliMapping(t *testing.T) {
	if systemIdentityGlyph != expectedSystemStar {
		t.Fatalf("system identity glyph = %q, want %q", systemIdentityGlyph, expectedSystemStar)
	}
	if assistantIdentityGlyph != expectedAssistantStar {
		t.Fatalf("assistant identity glyph = %q, want %q", assistantIdentityGlyph, expectedAssistantStar)
	}
}

func TestGlyphSystemMessageUsesOutlineStar(t *testing.T) {
	styles := defaultStyles()
	rendered := (&SystemMessage{content: "ready"}).Render(80, styles)
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, expectedSystemStar+" ready") {
		t.Fatalf("system message = %q, want outline star %q prefix", plain, expectedSystemStar)
	}
	if strings.Contains(plain, "✻") {
		t.Fatalf("system message still uses borrowed glyph U+273B: %q", plain)
	}
}

func TestGlyphHelpEmptyUsesOutlineStar(t *testing.T) {
	styles := defaultStyles()
	rendered := (&HelpMessage{}).Render(80, styles)
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, expectedSystemStar+" No commands available") {
		t.Fatalf("empty help = %q, want outline star %q prefix", plain, expectedSystemStar)
	}
}

func TestGlyphAssistantMessageUsesFilledStarBrandPrefix(t *testing.T) {
	styles := StylesForTheme(ThemePolarNight)
	msg := &AssistantMessage{content: "first line\n\nsecond line", finished: true, version: 1}
	lines := msg.RenderLines(80, styles)
	if len(lines) < 2 {
		t.Fatalf("assistant render produced %d lines, want >= 2", len(lines))
	}
	wantPrefix := styles.AssistantPrefix.Render(expectedAssistantStar) + " "
	if !strings.HasPrefix(lines[0], wantPrefix) {
		t.Fatalf("assistant first line = %q, want AssistantPrefix-styled %q prefix", lines[0], expectedAssistantStar)
	}
	if !strings.Contains(wantPrefix, "\x1b[") {
		t.Fatalf("AssistantPrefix render of %q has no ANSI styling: %q", expectedAssistantStar, wantPrefix)
	}
	if !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("assistant continuation line = %q, want two-space indent", lines[1])
	}
	if strings.Contains(lines[0], "●") {
		t.Fatalf("assistant prefix still uses tool-status glyph: %q", lines[0])
	}
}

func TestGlyphStreamingMessageUsesFilledStarBrandPrefix(t *testing.T) {
	styles := defaultStyles()
	msg := NewStreamingMessage(styles)
	msg.AppendContent("streaming line")
	lines := msg.RenderLines(80, styles)
	if len(lines) == 0 {
		t.Fatal("streaming render produced no lines")
	}
	wantPrefix := styles.AssistantPrefix.Render(expectedAssistantStar) + " "
	if !strings.HasPrefix(lines[0], wantPrefix) {
		t.Fatalf("streaming first line = %q, want AssistantPrefix-styled %q prefix", lines[0], expectedAssistantStar)
	}
}

func TestGlyphModelStatusUsesFilledStarBrandIcon(t *testing.T) {
	app := New(Config{Resumed: true, Model: "test-model"})
	app.width = 150
	app.height = 24
	app.state = StateChat
	app.updateLayout()

	rendered := app.renderStatus()
	wantIcon := app.styles.AssistantPrefix.Render(expectedAssistantStar)
	if !strings.Contains(rendered, wantIcon+" test-model") {
		t.Fatalf("status line = %q, want brand-styled %q icon before model", stripANSIForTest(rendered), expectedAssistantStar)
	}
	if !strings.Contains(wantIcon, "\x1b[") {
		t.Fatalf("AssistantPrefix render of %q has no ANSI styling: %q", expectedAssistantStar, wantIcon)
	}
	plain := stripANSIForTest(rendered)
	if strings.Contains(plain, "⬡") {
		t.Fatalf("model status still uses hexagon U+2B21: %q", plain)
	}
	for i, line := range strings.Split(rendered, "\n") {
		if got := app.renderEnvironment.profile.width(line); got > app.layout.width {
			t.Fatalf("status line %d width = %d, exceeds layout width %d", i, got, app.layout.width)
		}
	}
}

func TestAlignStatusLineProfileFallbackDoesNotOverflow(t *testing.T) {
	left := "⏸ plan · main"
	profile := DefaultDisplayCellProfile()
	width := profile.width(left)

	for _, tc := range []struct {
		name  string
		right string
	}{
		{name: "left only"},
		{name: "crowded sides", right: expectedAssistantStar + " model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := alignStatusLine(profile, left, tc.right, width)
			if got := profile.width(rendered); got > width {
				t.Fatalf("status fallback width = %d, want <= %d: %q", got, width, rendered)
			}
		})
	}
}
