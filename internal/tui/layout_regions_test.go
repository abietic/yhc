package tui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestLayoutRectanglesPartitionViewport(t *testing.T) {
	t.Parallel()

	layout := calculateLayout(layoutRequest{
		totalWidth: 80, totalHeight: 24,
		editorContentRows: 2, hintHeight: 7, taskTreeHeight: 4,
		contextHeight: 1, spinnerVisible: true, editorVisible: true,
	})
	ordered := []layoutRect{
		layout.headerRect,
		layout.chatRect,
		layout.activityRect,
		layout.hintRect,
		layout.editorRect,
		layout.statusRect,
	}
	wantY := 0
	for i, rect := range ordered {
		if rect.Y != wantY {
			t.Fatalf("region %d starts at y=%d, want %d: %#v", i, rect.Y, wantY, rect)
		}
		if rect.Width != 80 || rect.Height < 0 {
			t.Fatalf("invalid region %d: %#v", i, rect)
		}
		wantY = rect.bottom()
	}
	if wantY != 24 {
		t.Fatalf("regions end at row %d, want 24", wantY)
	}
	if layout.chatRect.Height < 3 || layout.editorRect.Height < editorMin {
		t.Fatalf("required regions collapsed: chat=%#v editor=%#v", layout.chatRect, layout.editorRect)
	}
	if layout.sidebarRect.Width != 0 {
		t.Fatalf("inactive sidebar owns width: %#v", layout.sidebarRect)
	}
	if layout.overlayRect != (layoutRect{Width: 80, Height: 24}) {
		t.Fatalf("overlay rect = %#v", layout.overlayRect)
	}
}

func TestLayoutRectanglesStayBoundedAtMinimumViewport(t *testing.T) {
	t.Parallel()

	layout := calculateLayout(layoutRequest{
		totalWidth: 40, totalHeight: 12,
		editorContentRows: 40, hintHeight: 14, taskTreeHeight: 6,
		contextHeight: 1, spinnerVisible: true, editorVisible: true,
	})
	if layout.statusRect.bottom() != 12 {
		t.Fatalf("minimum layout ends at %d, want 12: %#v", layout.statusRect.bottom(), layout)
	}
	if layout.chatRect.Height < 3 || layout.editorRect.Height < editorMin {
		t.Fatalf("minimum layout lost chat/editor: %#v", layout)
	}
}

func TestAppViewHonorsTerminalBounds(t *testing.T) {
	t.Parallel()

	app := newTestApp(52, 16)
	app.chat.AppendUser(strings.Repeat("wide ", 20))
	app.textarea.SetValue(strings.Repeat("input ", 40))
	app.activeTools = map[string]*inlineToolEntry{
		"one": {name: "Read", description: strings.Repeat("long-path/", 20)},
		"two": {name: "Grep", description: strings.Repeat("pattern ", 20)},
	}
	app.activeToolsOrder = []string{"one", "two"}
	app.updateLayout()

	assertViewBounds(t, app.renderView(), 52, 16)

	app.dialog.Show("Read", `{"file_path":"/tmp/example"}`, "", make(chan PermissionResponse, 1))
	app.state = StatePermission
	assertViewBounds(t, app.renderView(), 52, 16)
}

func assertViewBounds(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("view height = %d, want %d\n%s", len(lines), height, view)
	}
	for i, line := range lines {
		if got := xansi.StringWidth(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, width, xansi.Strip(line))
		}
	}
}
