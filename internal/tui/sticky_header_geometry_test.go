package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestStickyHeaderExcludesVisiblePrompt(t *testing.T) {
	chat := NewChatView(StylesForTheme(ThemePolarNight))
	chat.SetSize(80, 6)
	chat.AppendUser("first prompt")
	chat.appendChatItem(&AssistantMessage{content: "answer one", finished: true})
	chat.AppendUser("second prompt")
	chat.appendChatItem(&AssistantMessage{content: "answer two", finished: true})
	chat.appendChatItem(&AssistantMessage{content: "answer three", finished: true})
	chat.Render(80, 6)

	for i, item := range chat.items {
		if message, ok := item.(*UserMessage); ok && message.content == "second prompt" {
			chat.offsetIdx = i
			break
		}
	}
	chat.restoreAway()
	chat.viewDirty = true

	lines := strings.Split(stripANSIForTest(chat.Render(80, 6)), "\n")
	if strings.Contains(lines[0], "second prompt") {
		t.Fatalf("visible user message was duplicated in sticky header:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "first prompt") {
		t.Fatalf("sticky header did not show the nearest prompt above the viewport:\n%s", strings.Join(lines, "\n"))
	}
}

func TestStickyHeaderTruncatesUnicodeByDisplayWidth(t *testing.T) {
	chat := NewChatView(StylesForTheme(ThemePolarNight))
	chat.SetSize(24, 6)
	chat.AppendUser("这是一个很长的中文提示，需要按终端显示宽度安全截断")
	chat.appendChatItem(&AssistantMessage{content: "answer", finished: true})
	chat.appendChatItem(&AssistantMessage{content: "another answer", finished: true})
	chat.offsetIdx = 1
	chat.restoreAway()
	chat.viewDirty = true

	first := strings.Split(stripANSIForTest(chat.Render(24, 6)), "\n")[0]
	if !utf8.ValidString(first) {
		t.Fatalf("sticky header contains invalid UTF-8: %q", first)
	}
	if width := xansi.StringWidth(first); width > 24 {
		t.Fatalf("sticky header width = %d, want <= 24: %q", width, first)
	}
	if !strings.Contains(first, "…") {
		t.Fatalf("long Unicode prompt was not truncated: %q", first)
	}
}

func TestG11D3StickyHeaderKeepsPillOnPublishedFinalRow(t *testing.T) {
	chat := NewChatView(StylesForTheme(ThemePolarNight))
	chat.SetSize(80, 6)
	chat.AppendUser("sticky prompt")
	for range 12 {
		chat.AppendUser("history")
	}
	chat.Render(80, 6)
	chat.ScrollToTop()
	geometry := chat.pillGeometry(80, 6)
	lines := strings.Split(chat.Render(80, 6), "\n")
	if geometry.row != len(lines)-1 || lines[geometry.row] != geometry.renderedLine {
		t.Fatalf("sticky pill row=%d lines=%d geometry=%#v", geometry.row, len(lines), geometry)
	}
}

func TestJumpToBottomPillHitbox(t *testing.T) {
	app := newTestApp(80, 24)
	app.chat.SetSize(app.layout.chatRect.Width, app.layout.chatHeight)
	for range 20 {
		app.chat.AppendUser("message")
	}
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	app.chat.ScrollUp(1)
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)

	row := app.layout.chatHeight - 1
	width := app.layout.chatRect.Width
	if !app.pillClickHits(width/2, row) {
		t.Fatal("center of the pill row should hit")
	}
	if app.pillClickHits(0, row) {
		t.Fatal("far left of the pill row should miss")
	}
	if app.pillClickHits(width/2, row-1) {
		t.Fatal("row above the pill should miss")
	}
	app.chat.ResetFollow()
	if app.pillClickHits(width/2, row) {
		t.Fatal("no pill while following")
	}
}

func TestJumpToBottomPillClickRestoresFollow(t *testing.T) {
	app := newTestApp(80, 24)
	app.chat.SetSize(app.layout.chatRect.Width, app.layout.chatHeight)
	for range 20 {
		app.chat.AppendUser("message")
	}
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	app.chat.ScrollUp(1)
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	if app.chat.Following() {
		t.Fatal("precondition: chat should be scrolled away")
	}

	_, _ = app.Update(tuiMouseMsg{
		X:      app.layout.chatRect.Width / 2,
		Y:      app.layout.chatRect.Y + app.layout.chatHeight - 1,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if !app.chat.Following() {
		t.Fatal("clicking the visible jump-to-bottom pill did not restore follow")
	}
}
