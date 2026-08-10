package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestUserMessagePanelUsesBrandBarForEveryLine(t *testing.T) {
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		t.Run(string(theme), func(t *testing.T) {
			styles := StylesForTheme(theme)
			rendered := (&UserMessage{content: "first line\nsecond line"}).Render(32, styles)
			barStyle, _ := userMessagePanelRunStyles(styles)
			wantBarSGR := lastSGRBefore(
				barStyle.Render(userMessageBarGlyph),
				userMessageBarGlyph,
				false,
			)

			lines := strings.Split(rendered, "\n")
			if len(lines) != 2 {
				t.Fatalf("panel produced %d lines, want 2:\n%q", len(lines), rendered)
			}
			for i, line := range lines {
				visible := strings.TrimSpace(stripANSIForTest(line))
				if !strings.HasPrefix(visible, userMessageBarGlyph+" ") {
					t.Errorf("line %d = %q, want brand bar prefix", i, visible)
				}
				if strings.Contains(visible, "●") {
					t.Errorf("line %d retains old user prefix: %q", i, visible)
				}
				if got := lastSGRBefore(line, userMessageBarGlyph, false); got != wantBarSGR {
					t.Errorf("line %d bar SGR = %q, want %q", i, got, wantBarSGR)
				}
			}
			if _, noBackground := styles.UserMessageBlock.GetBackground().(lipgloss.NoColor); !noBackground {
				wantBackground := lastSGRBefore(
					styles.UserMessageBlock.Padding(0).Render("first line"),
					"first line",
					false,
				)
				if gotBackground := lastSGRBefore(lines[0], "first line", false); gotBackground != wantBackground {
					t.Errorf("content background SGR = %q, want %q", gotBackground, wantBackground)
				}
			}
		})
	}
}

func TestUserMessagePanelPreservesWrappingAndRawContent(t *testing.T) {
	const width = 32
	content := strings.Repeat("wrapped user content ", 8)
	message := &UserMessage{content: content}
	rendered := message.Render(width, StylesForTheme(ThemePolarNight))
	lines := strings.Split(rendered, "\n")
	if len(lines) < 3 {
		t.Fatalf("long panel produced %d lines, want at least 3", len(lines))
	}
	for i, line := range lines {
		visible := strings.TrimSpace(stripANSIForTest(line))
		if !strings.HasPrefix(visible, userMessageBarGlyph+" ") {
			t.Errorf("wrapped line %d = %q, want bar prefix", i, visible)
		}
		if got := lipgloss.Width(line); got != width {
			t.Errorf("wrapped line %d width = %d, want %d", i, got, width)
		}
	}
	if got := message.RenderRaw(HistoryRenderContext{}); got != content {
		t.Fatalf("raw user content changed:\n got %q\nwant %q", got, content)
	}
}

func TestUserMessagePanelStickyProjectionUsesSameBar(t *testing.T) {
	styles := StylesForTheme(ThemePolarNight)
	chat := NewChatView(styles)
	chat.SetSize(48, 5)
	chat.AppendUser("first prompt")
	chat.AppendSystem("first result")
	chat.AppendSystem("second result")
	chat.restoreAway()
	chat.offsetIdx = 2
	chat.offsetLine = 0
	chat.viewDirty = true

	firstLine := strings.Split(chat.Render(48, 5), "\n")[0]
	visible := strings.TrimSpace(stripANSIForTest(firstLine))
	if !strings.HasPrefix(visible, userMessageBarGlyph+" first prompt") {
		t.Fatalf("sticky user projection = %q, want bar plus prompt", visible)
	}
	if strings.Contains(visible, "❯") || strings.Contains(visible, "●") {
		t.Fatalf("sticky user projection retains old prefix: %q", visible)
	}
	barStyle, _ := userMessagePanelRunStyles(styles)
	wantBarSGR := lastSGRBefore(
		barStyle.Render(userMessageBarGlyph),
		userMessageBarGlyph,
		false,
	)
	if got := lastSGRBefore(firstLine, userMessageBarGlyph, false); got != wantBarSGR {
		t.Fatalf("sticky bar SGR = %q, want %q", got, wantBarSGR)
	}
}

func TestUserMessagePanelGolden(t *testing.T) {
	var got strings.Builder
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		styles := StylesForTheme(theme)
		rendered := (&UserMessage{content: "first line\nsecond line"}).Render(32, styles)
		fmt.Fprintf(&got, "[%s]\nrender=%q\n\n", theme, rendered)
	}

	path := "testdata/user_message_panel.golden"
	actual := strings.TrimSpace(got.String()) + "\n"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatalf("update user-message panel golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user-message panel golden: %v\n--- got ---\n%s", err, actual)
	}
	if actual != string(want) {
		t.Fatalf("user-message panel golden mismatch:\n--- got ---\n%s--- want ---\n%s", actual, want)
	}
}

func BenchmarkUserMessagePanel(b *testing.B) {
	message := &UserMessage{content: strings.Repeat("wrapped user content ", 8)}
	styles := StylesForTheme(ThemePolarNight)
	b.ReportAllocs()
	for range b.N {
		message.Render(80, styles)
	}
}
