package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestApp(width, height int) *App {
	app := New(Config{})
	// Simulate WindowSizeMsg to initialize layout
	updateAppSilent(app, tea.WindowSizeMsg{Width: width, Height: height})
	// Move past welcome screen
	app.state = StateChat
	return app
}

func updateAppSilent(app *App, msg tea.Msg) {
	app.Update(msg)
}

func TestInputAreaInitialHeight(t *testing.T) {
	app := newTestApp(80, 40)

	// With empty textarea, editor should show exactly 1 visible line
	h := app.layout.editorHeight
	if h != 3 { // 1 content + 2 border
		t.Errorf("initial editorHeight = %d, want 3 (1 line + 2 border)", h)
	}
}

func TestInputAreaExpandsOnLongLine(t *testing.T) {
	app := newTestApp(80, 40)

	// Type a line that's longer than the editor width (80 - 4 border = 76 chars content area)
	longLine := strings.Repeat("a", 200)
	app.textarea.SetValue(longLine)
	app.updateLayout()

	// Editor should expand to show all wrapped lines
	if app.layout.editorHeight <= 3 {
		t.Errorf("editorHeight = %d after long line, want > 3", app.layout.editorHeight)
	}

	// All wrapped lines should be visible in the textarea view
	view := app.textarea.View()
	visibleLines := strings.Count(view, "\n") + 1
	// The textarea should show multiple lines (the text wraps)
	if visibleLines <= 1 {
		t.Errorf("textarea shows %d visible lines for 200-char text, want > 1", visibleLines)
	}

	// The FIRST characters of the long line must be visible in the output
	stripped := strings.ReplaceAll(view, "\n", "")
	if !strings.Contains(stripped, "aaa") {
		t.Errorf("textarea view doesn't contain the text content")
	}
}

func TestInputAreaShrinksOnDelete(t *testing.T) {
	app := newTestApp(80, 40)

	// Type long text, then clear it
	app.textarea.SetValue(strings.Repeat("x", 200))
	app.updateLayout()
	expandedHeight := app.layout.editorHeight

	app.textarea.SetValue("")
	app.updateLayout()
	shrunkHeight := app.layout.editorHeight

	if shrunkHeight >= expandedHeight {
		t.Errorf("after clear: height %d should be < expanded height %d", shrunkHeight, expandedHeight)
	}
	if shrunkHeight != 3 {
		t.Errorf("after clear: height = %d, want 3", shrunkHeight)
	}
}

func TestInputAreaWrappedLineFirstPartVisible(t *testing.T) {
	app := newTestApp(80, 40)

	// Create text that wraps: "AAAA...BBB...CCC..."
	// Each part fills one wrapped line
	editorContentWidth := 80 - 4 - 2 // border (4) + prompt "❯ " (2) = 74 usable
	part1 := strings.Repeat("A", editorContentWidth)
	part2 := strings.Repeat("B", editorContentWidth)
	part3 := strings.Repeat("C", editorContentWidth)
	longLine := part1 + part2 + part3

	app.textarea.SetValue(longLine)
	app.updateLayout()

	// Move cursor to end (simulates typing)
	app.textarea.CursorEnd()
	app.updateLayout()

	view := app.textarea.View()

	// ALL three parts must be visible — the first wrapped line (A's) must NOT be hidden
	if !strings.Contains(view, "AAAA") {
		t.Errorf("first wrapped part (A's) not visible in textarea view:\n%s", view)
	}
	if !strings.Contains(view, "BBBB") {
		t.Errorf("second wrapped part (B's) not visible in textarea view:\n%s", view)
	}
	if !strings.Contains(view, "CCCC") {
		t.Errorf("third wrapped part (C's) not visible in textarea view:\n%s", view)
	}
}

func TestInputAreaUpDownInWrappedLine(t *testing.T) {
	app := newTestApp(80, 40)

	// Create a single logical line that wraps to 3 display lines
	editorContentWidth := 80 - 4 - 2 // 74 usable
	longLine := strings.Repeat("x", editorContentWidth*3)

	app.textarea.SetValue(longLine)
	app.textarea.CursorEnd()
	app.updateLayout()

	// Cursor should be on the last wrapped line (RowOffset should be > 0)
	li := app.textarea.LineInfo()
	if li.RowOffset == 0 && li.Height > 1 {
		t.Errorf("cursor at end should have RowOffset > 0, got RowOffset=%d Height=%d", li.RowOffset, li.Height)
	}

	// Press Up — should move within the wrapped line, NOT navigate history
	initialHistIdx := app.historyIdx
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyUp})

	// History index should NOT have changed (up arrow should move within wrap)
	if app.historyIdx != initialHistIdx {
		t.Errorf("Up arrow in wrapped line navigated history: historyIdx changed from %d to %d", initialHistIdx, app.historyIdx)
	}

	// LineInfo should show cursor moved up
	liAfter := app.textarea.LineInfo()
	if liAfter.RowOffset >= li.RowOffset && li.RowOffset > 0 {
		t.Errorf("Up arrow didn't move cursor up within wrap: RowOffset before=%d after=%d", li.RowOffset, liAfter.RowOffset)
	}
}

func TestInputAreaResizeShowsMoreLines(t *testing.T) {
	// Start with a small window
	app := newTestApp(80, 20)

	// Type long content (wraps to many lines, capped by small window)
	app.textarea.SetValue(strings.Repeat("z", 500))
	app.updateLayout()

	smallHeight := app.layout.editorHeight
	smallView := app.textarea.View()
	smallVisibleLines := strings.Count(smallView, "\n") + 1

	// Resize to larger window
	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 50})

	largeHeight := app.layout.editorHeight
	largeView := app.textarea.View()
	largeVisibleLines := strings.Count(largeView, "\n") + 1

	if largeHeight <= smallHeight {
		t.Errorf("after resize larger: editorHeight %d should be > %d", largeHeight, smallHeight)
	}
	if largeVisibleLines <= smallVisibleLines {
		t.Errorf("after resize larger: visible lines %d should be > %d", largeVisibleLines, smallVisibleLines)
	}
}

func TestInputAreaResizeRevealsHiddenPrefix(t *testing.T) {
	// Start with small window where content overflows editor max
	app := newTestApp(80, 20) // small: editorMax will be ~6-7

	// Type content that wraps to more lines than the small editor can show
	// 80 - 4(border) - 2(prompt) = 74 content width per line
	// 5 wrapped lines of content
	longLine := strings.Repeat("A", 74) + strings.Repeat("B", 74) + strings.Repeat("C", 74) + strings.Repeat("D", 74) + strings.Repeat("E", 74)
	app.textarea.SetValue(longLine)
	app.textarea.CursorEnd()
	app.updateLayout()

	// In small window, not all lines may be visible
	smallView := app.textarea.View()

	// Now resize to large window where all content can fit
	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 50})

	largeView := app.textarea.View()

	// After resize, the FIRST part (A's) must be visible
	if !strings.Contains(largeView, "AAAA") {
		t.Errorf("after resize larger, first wrapped part (A's) not visible.\nSmall view has A's: %v\nLarge view:\n%s",
			strings.Contains(smallView, "AAAA"), largeView)
	}
	// And last part too
	if !strings.Contains(largeView, "EEEE") {
		t.Errorf("after resize larger, last wrapped part (E's) not visible")
	}
}
