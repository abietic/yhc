package tui

import (
	"testing"
)

func TestSelectionItemBoundsNormalization(t *testing.T) {
	s := &Selection{
		anchor: &selItemPoint{itemIdx: 2, lineInItem: 3, col: 5},
		focus:  &selItemPoint{itemIdx: 1, lineInItem: 1, col: 10},
	}
	start, end := s.getItemBounds()
	if start.itemIdx != 1 || start.lineInItem != 1 || start.col != 10 {
		t.Errorf("start = %+v, want {1, 1, 10}", start)
	}
	if end.itemIdx != 2 || end.lineInItem != 3 || end.col != 5 {
		t.Errorf("end = %+v, want {2, 3, 5}", end)
	}
}

func TestSelectionHasSelection(t *testing.T) {
	s := &Selection{}
	if s.HasSelection() {
		t.Error("empty selection should return false")
	}

	s.anchor = &selItemPoint{itemIdx: 0, lineInItem: 0, col: 0}
	s.focus = &selItemPoint{itemIdx: 0, lineInItem: 0, col: 0}
	if s.HasSelection() {
		t.Error("same anchor and focus should return false")
	}

	s.focus = &selItemPoint{itemIdx: 0, lineInItem: 0, col: 5}
	if !s.HasSelection() {
		t.Error("different focus should return true")
	}
}

func TestSelectionClear(t *testing.T) {
	s := &Selection{
		anchor:     &selItemPoint{itemIdx: 1, lineInItem: 0, col: 3},
		focus:      &selItemPoint{itemIdx: 2, lineInItem: 1, col: 7},
		isDragging: true,
	}
	s.Clear()
	if s.anchor != nil || s.focus != nil || s.isDragging {
		t.Error("Clear should reset all fields")
	}
}

func TestFindWordBoundaries(t *testing.T) {
	tests := []struct {
		line  string
		col   int
		start int
		end   int
	}{
		{"hello world", 1, 0, 5},
		{"hello world", 6, 6, 11},
		{"hello world", 5, 5, 5}, // space
		{"foo_bar baz", 3, 0, 7},
		{"", 0, 0, 0},
		{"a", 0, 0, 1},
		{"  abc  ", 3, 2, 5},
	}
	for _, tt := range tests {
		s, e := findWordBoundaries(tt.line, tt.col)
		if s != tt.start || e != tt.end {
			t.Errorf("findWordBoundaries(%q, %d) = (%d, %d), want (%d, %d)",
				tt.line, tt.col, s, e, tt.start, tt.end)
		}
	}
}

func TestViewportPosToItemPoint(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 5)
	// Add enough items to fill the viewport (no padding)
	for i := 0; i < 10; i++ {
		chat.AppendUser("Hello")
	}
	chat.Render(80, 5)
	chat.ScrollToTop()
	chat.Render(80, 5)

	// First item should be at row 0 (viewport fully filled, no padding)
	descriptor := chat.currentViewportProjection().rows[0]
	metadata, _, ok := chat.selectionMetadata(
		descriptor.itemIdx,
		descriptor.lineInItem,
	)
	start, _, selectable := selectionRowCellBounds(metadata)
	if !ok || !selectable {
		t.Fatal("expected semantic metadata for row 0")
	}
	pt := chat.viewportPosToItemPoint(start, 0)
	if pt == nil {
		t.Fatal("expected non-nil point for row 0")
	}
	if pt.itemIdx != 0 {
		t.Errorf("itemIdx = %d, want 0", pt.itemIdx)
	}
}

func TestItemPointToViewportRow(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 5)
	// Add enough items to fill the viewport
	for i := 0; i < 10; i++ {
		chat.AppendUser("Message")
	}
	chat.Render(80, 5)
	chat.ScrollToTop()
	chat.Render(80, 5)

	// First item, first line should be at viewport row 0 (no padding)
	row := chat.ItemPointToViewportRow(0, 0)
	if row != 0 {
		t.Errorf("first item row = %d, want 0", row)
	}
}

func TestGetItemLine(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 20)
	chat.AppendUser("Test message")

	// Force render
	chat.Render(80, 20)

	line := chat.GetItemLine(0, 0)
	if line == "" {
		t.Error("expected non-empty line")
	}
}

func TestRenderItemRangeNoSelectPrefix(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 20)
	chat.AppendUser("Hello world")

	// Force render
	chat.Render(80, 20)

	text := chat.RenderItemRange(0, 0, 0, 0, 0, 5)
	if text == "" {
		t.Error("expected non-empty extracted text")
	}
}

func TestExpandSelection(t *testing.T) {
	s := &Selection{}
	s.expandAnchor = &expandSelPoint{col: 0, row: 0}
	s.expandFocus = &expandSelPoint{col: 5, row: 2}

	if !s.HasExpandSelection() {
		t.Error("should have expand selection")
	}

	sr, sc, er, ec := s.GetExpandBounds()
	if sr != 0 || sc != 0 || er != 2 || ec != 5 {
		t.Errorf("bounds = (%d,%d,%d,%d), want (0,0,2,5)", sr, sc, er, ec)
	}

	lines := []string{"hello world", "foo bar baz", "line three here"}
	text := s.ExtractExpandText(lines, DefaultDisplayCellProfile())
	if text == "" {
		t.Error("expected non-empty extracted text")
	}
}
