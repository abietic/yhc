package tui

import (
	"strings"
	"testing"
)

func TestChatViewScrollUpDown(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 10)

	// Add many items to exceed viewport
	for i := 0; i < 50; i++ {
		chat.AppendUser("Message line content here")
	}

	// Should start at follow mode (bottom)
	chat.Render(80, 10)
	if !chat.Following() {
		t.Error("should start in follow mode")
	}

	// ScrollUp should disable follow
	chat.ScrollUp(5)
	if chat.Following() {
		t.Error("ScrollUp should disable follow mode")
	}

	// ScrollToBottom should re-enable follow
	chat.ScrollToBottom()
	if !chat.Following() {
		t.Error("ScrollToBottom should enable follow mode")
	}
}

func TestChatViewScrollToTop(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 10)

	for i := 0; i < 20; i++ {
		chat.AppendUser("Line content")
	}
	chat.Render(80, 10)

	chat.ScrollToTop()
	if chat.offsetIdx != 0 || chat.offsetLine != 0 {
		t.Errorf("ScrollToTop: offsetIdx=%d, offsetLine=%d, want 0,0",
			chat.offsetIdx, chat.offsetLine)
	}
}

func TestChatViewTotalContentHeight(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 10)

	chat.AppendUser("Hello")
	chat.AppendUser("World")

	// Render to populate cache
	chat.Render(80, 10)

	h := chat.TotalContentHeight()
	if h <= 0 {
		t.Errorf("TotalContentHeight = %d, want > 0", h)
	}
}

func TestChatViewScrollAnimated(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 10)

	for i := 0; i < 50; i++ {
		chat.AppendUser("Message")
	}
	chat.Render(80, 10)
	chat.ScrollToTop()

	// Small scroll should be immediate (no animation)
	chat.ScrollAnimated(3)
	if chat.scrollRemaining != 0 {
		t.Errorf("small scroll should be immediate, scrollRemaining=%d", chat.scrollRemaining)
	}

	// Large scroll should start animation
	chat.ScrollAnimated(20)
	if chat.scrollRemaining != 20 {
		t.Errorf("large scroll should set scrollRemaining=20, got %d", chat.scrollRemaining)
	}

	// AnimateStep should reduce remaining
	chat.AnimateStep()
	if chat.scrollRemaining >= 20 {
		t.Errorf("AnimateStep should reduce scrollRemaining, got %d", chat.scrollRemaining)
	}
}

func TestChatViewRenderCache(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 10)
	chat.AppendUser("Cached content")

	r1 := chat.Render(80, 10)
	r2 := chat.Render(80, 10)

	if r1 != r2 {
		t.Error("consecutive renders with no changes should return same content")
	}
}

func TestScrollbarRender(t *testing.T) {
	styles := defaultStyles()

	// No scrollbar when content fits
	sb := renderScrollbar(10, 5, 10, 0, styles)
	if sb != "" {
		t.Error("scrollbar should be empty when content fits viewport")
	}

	// Scrollbar present when content exceeds viewport
	sb = renderScrollbar(10, 50, 10, 0, styles)
	if sb == "" {
		t.Error("scrollbar should be non-empty when content exceeds viewport")
	}
}

func TestSelectionSurvivesScroll(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 10)

	for i := 0; i < 30; i++ {
		chat.AppendUser("Content line")
	}
	chat.Render(80, 10)
	chat.ScrollToTop()
	chat.Render(80, 10)

	// Start a selection at item 2 (known to be visible after scroll to top)
	s := &Selection{}
	pt := chat.viewportPosToItemPoint(3, 2)
	if pt == nil {
		t.Fatal("expected valid point")
	}
	s.anchor = pt
	s.focus = &selItemPoint{itemIdx: pt.itemIdx, lineInItem: pt.lineInItem, col: pt.col + 5}

	// Verify selection exists
	if !s.HasSelection() {
		t.Fatal("expected selection")
	}

	// Scroll down — selection should still be valid
	chat.ScrollDown(10)
	chat.Render(80, 10)

	// Selection item coordinates are unchanged (scroll-independent)
	if !s.HasSelection() {
		t.Error("selection should survive scrolling")
	}
	start, _ := s.getItemBounds()
	if start.itemIdx != pt.itemIdx {
		t.Errorf("anchor itemIdx changed after scroll: got %d, want %d", start.itemIdx, pt.itemIdx)
	}

	// Text extraction should still work
	text := s.ExtractTextFromChat(chat)
	if text == "" {
		t.Error("ExtractTextFromChat should return text after scroll")
	}
}

// Regression: scrolling down must clamp at the follow offset — a frame may
// never degenerate into top-padded blank before snapping to the bottom.
func TestChatViewScrollDownClampsAtFollowOffset(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 10)

	for i := 0; i < 30; i++ {
		chat.AppendUser("message with some content to exceed one rendered line")
	}
	chat.Render(80, 10) // populate caches at follow bottom

	chat.ScrollToTop()
	chat.Render(80, 10)

	maxIdx, maxLine := chat.followOffset(chat.renderWidth(), chat.height)

	chat.offsetIdx = maxIdx
	chat.offsetLine = maxLine
	chat.ScrollDown(0)
	if chat.Following() {
		t.Fatal("zero-distance scroll changed follow mode")
	}
	chat.ScrollToTop()

	frames := 0
	for !chat.Following() && frames < 60 {
		chat.ScrollDown(3)
		frames++
		if !chat.Following() {
			// Never past the follow offset.
			if chat.offsetIdx > maxIdx || (chat.offsetIdx == maxIdx && chat.offsetLine > maxLine) {
				t.Fatalf("frame %d: offset (%d,%d) past follow offset (%d,%d)",
					frames, chat.offsetIdx, chat.offsetLine, maxIdx, maxLine)
			}
			// Never a top-padded (leading blank lines) degenerate frame.
			out := chat.Render(80, 10)
			lines := strings.Split(out, "\n")
			if len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
				t.Fatalf("frame %d: degenerate top-padded view at (%d,%d)",
					frames, chat.offsetIdx, chat.offsetLine)
			}
		}
	}
	if !chat.Following() {
		t.Fatal("never reached follow after clamp")
	}
	// Final bottom view is a full screenful.
	out := chat.Render(80, 10)
	lines := strings.Split(out, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		t.Fatal("bottom view is top-padded after clamp")
	}
}
