package tui

import (
	"strings"

	"github.com/rivo/uniseg"
)

// layoutRect is one owned terminal region. Coordinates are zero-based and
// measured in terminal cells/rows.
type layoutRect struct {
	X, Y          int
	Width, Height int
}

func (r layoutRect) bottom() int { return r.Y + r.Height }

// layout calculates explicit, non-overlapping terminal regions. The legacy
// height aliases remain during the string-renderer migration so existing chat
// and input code can move one boundary at a time.
type layout struct {
	headerRect   layoutRect
	chatRect     layoutRect
	activityRect layoutRect
	hintRect     layoutRect
	editorRect   layoutRect
	sidebarRect  layoutRect
	statusRect   layoutRect
	overlayRect  layoutRect

	headerHeight  int // 1 for condensed logo line
	chatHeight    int
	editorHeight  int // includes border (top+bottom = +2)
	hintHeight    int // command hints below the editor
	spinnerHeight int // 1 when spinner is visible, 0 otherwise
	statusHeight  int
	width         int
	mode          responsiveLayoutMode
}

type responsiveLayoutMode string

const (
	layoutModeCompact  responsiveLayoutMode = "compact"
	layoutModeStandard responsiveLayoutMode = "standard"
	layoutModeWide     responsiveLayoutMode = "wide"
)

type layoutRequest struct {
	totalWidth        int
	totalHeight       int
	editorContentRows int
	hintHeight        int
	taskTreeHeight    int
	contextHeight     int
	spinnerVisible    bool
	editorVisible     bool
	sidebarVisible    bool
}

const (
	statusLines = 1 // compact single-line footer
	editorMin   = 3 // 1 content + 2 border

	// Minimum terminal dimensions below which the "window too small" screen is shown.
	minTermWidth  = 40
	minTermHeight = 12
	compactWidth  = 80
	compactHeight = 24
	wideWidth     = 150
	wideHeight    = 24
	sidebarMin    = 32
	sidebarMax    = 42
	mainWideMin   = 100

	// compactHintMaxRows bounds the autocomplete hint band in compact mode so
	// candidates stay visible without crowding out chat, editor, and status.
	compactHintMaxRows = 7
)

func responsiveLayoutDimensions(totalWidth, totalHeight int, sidebarVisible bool) (responsiveLayoutMode, int, int) {
	mode := layoutModeStandard
	if totalWidth < compactWidth || totalHeight < compactHeight {
		mode = layoutModeCompact
	}
	mainWidth := totalWidth
	sidebarWidth := 0
	if sidebarVisible && totalWidth >= wideWidth && totalHeight >= wideHeight {
		sidebarWidth = min(sidebarMax, max(sidebarMin, totalWidth/4))
		if totalWidth-sidebarWidth >= mainWideMin {
			mainWidth = totalWidth - sidebarWidth
			mode = layoutModeWide
		} else {
			sidebarWidth = 0
		}
	}
	return mode, mainWidth, sidebarWidth
}

type layoutBand struct {
	rect        layoutRect
	content     string
	alignBottom bool
}

// renderLayoutBands keeps the current string renderer while assigning every
// vertical surface an explicit rectangle. It is intentionally line-based, not
// a terminal-cell screen buffer.
func renderLayoutBands(profile DisplayCellProfile, width, height int, bands ...layoutBand) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	canvas := make([]string, height)
	for _, band := range bands {
		if band.rect.Height <= 0 || band.rect.Width <= 0 || band.rect.Y >= height {
			continue
		}
		lines := fitLayoutBandLines(band.content, band.rect.Height, band.alignBottom)
		for i, line := range lines {
			y := band.rect.Y + i
			if y < 0 || y >= len(canvas) {
				continue
			}
			line = profile.truncateAt(line, band.rect.Width, band.rect.X)
			if band.rect.X > 0 {
				line = strings.Repeat(" ", band.rect.X) + line
			}
			canvas[y] = line
		}
	}
	return strings.Join(canvas, "\n")
}

func fitLayoutBandLines(content string, height int, alignBottom bool) []string {
	if height <= 0 {
		return nil
	}
	var lines []string
	if content != "" {
		lines = strings.Split(content, "\n")
	}
	if len(lines) > height {
		if alignBottom {
			lines = lines[len(lines)-height:]
		} else {
			lines = lines[:height]
		}
	}
	out := make([]string, height)
	start := 0
	if alignBottom && len(lines) < height {
		start = height - len(lines)
	}
	copy(out[start:], lines)
	return out
}

func calculateLayout(req layoutRequest) layout {
	totalWidth := req.totalWidth
	totalHeight := req.totalHeight
	// Guard: clamp minimum terminal dimensions to prevent layout breakage.
	if totalWidth < 30 {
		totalWidth = 30
	}
	if totalHeight < 10 {
		totalHeight = 10
	}
	mode, mainWidth, sidebarWidth := responsiveLayoutDimensions(totalWidth, totalHeight, req.sidebarVisible)

	// Editor height = content + 2 (for top/bottom border).
	// Dynamic: grows with content, capped at a window-proportional maximum.
	//   - Small terminals (< 30 rows): max 1/3 of height
	//   - Medium terminals (30-50 rows): max 40% of height, up to 12 lines
	//   - Large terminals (> 50 rows): max 40% of height, up to 16 lines
	maxEditor := totalHeight * 2 / 5
	switch {
	case totalHeight < 30:
		if maxEditor > totalHeight/3 {
			maxEditor = totalHeight / 3
		}
	case totalHeight <= 50:
		if maxEditor > 12 {
			maxEditor = 12
		}
	default:
		if maxEditor > 16 {
			maxEditor = 16
		}
	}
	if maxEditor < editorMin {
		maxEditor = editorMin
	}

	var editorH int
	if req.editorVisible {
		editorH = req.editorContentRows + 2
		if editorH < editorMin {
			editorH = editorMin
		}
		if editorH > maxEditor {
			editorH = maxEditor
		}
	}

	headerH := max(0, req.contextHeight)
	if headerH > 1 {
		headerH = 1
	}
	statusH := statusLines
	desiredActivityH := max(0, req.taskTreeHeight)
	spinnerH := 0
	if req.spinnerVisible {
		spinnerH = 1
		desiredActivityH++
	}
	desiredHintH := max(0, req.hintHeight)
	if mode == layoutModeCompact {
		desiredHintH = min(desiredHintH, compactHintMaxRows)
		desiredActivityH = min(desiredActivityH, 2)
	}

	// Preserve a usable chat, status, and editor first. Activity is more urgent
	// than completion hints; both are clipped into their assigned rectangles.
	const chatMin = 3
	auxBudget := totalHeight - headerH - statusH - chatMin
	if auxBudget < 0 {
		auxBudget = 0
	}
	if editorH > auxBudget {
		editorH = auxBudget
	}
	if req.editorVisible && auxBudget >= editorMin && editorH < editorMin {
		editorH = editorMin
	}
	auxBudget -= editorH

	activityH := min(desiredActivityH, auxBudget)
	auxBudget -= activityH
	hintH := min(desiredHintH, auxBudget)
	if hintH > 0 && hintH < 3 {
		hintH = 0
	}

	chatH := totalHeight - headerH - activityH - hintH - editorH - statusH
	if chatH < chatMin {
		chatH = chatMin
	}

	headerRect := layoutRect{Width: mainWidth, Height: headerH}
	y := headerRect.bottom()
	chatRect := layoutRect{Y: y, Width: mainWidth, Height: chatH}
	y = chatRect.bottom()
	activityRect := layoutRect{Y: y, Width: mainWidth, Height: activityH}
	y = activityRect.bottom()
	hintRect := layoutRect{Y: y, Width: mainWidth, Height: hintH}
	y = hintRect.bottom()
	editorRect := layoutRect{Y: y, Width: mainWidth, Height: editorH}
	y = editorRect.bottom()
	statusRect := layoutRect{Y: y, Width: mainWidth, Height: statusH}

	return layout{
		headerRect:    headerRect,
		chatRect:      chatRect,
		activityRect:  activityRect,
		hintRect:      hintRect,
		editorRect:    editorRect,
		sidebarRect:   layoutRect{X: mainWidth, Width: sidebarWidth, Height: totalHeight},
		statusRect:    statusRect,
		overlayRect:   layoutRect{Width: totalWidth, Height: totalHeight},
		headerHeight:  headerH,
		chatHeight:    chatH,
		editorHeight:  editorH,
		hintHeight:    hintH,
		spinnerHeight: min(spinnerH, activityH),
		statusHeight:  statusH,
		width:         mainWidth,
		mode:          mode,
	}
}

func joinLayoutColumns(profile DisplayCellProfile, main, sidebar string, mainWidth, sidebarWidth, height int) string {
	if sidebarWidth <= 0 {
		return main
	}
	mainLines := fitLayoutBandLines(main, height, false)
	sidebarLines := fitLayoutBandLines(sidebar, height, false)
	joined := make([]string, height)
	for row := range height {
		joined[row] = fitLayoutColumnLine(profile, mainLines[row], mainWidth, 0) +
			fitLayoutColumnLine(profile, sidebarLines[row], sidebarWidth, mainWidth)
	}
	return strings.Join(joined, "\n")
}

func fitLayoutColumnLine(profile DisplayCellProfile, line string, width, startColumn int) string {
	if width <= 0 {
		return ""
	}
	line = profile.truncateAt(line, width, startColumn)
	if padding := width - profile.measure(line, startColumn); padding > 0 {
		line += strings.Repeat(" ", padding)
	}
	return line
}

// countVisualLines returns the number of visual lines the text will
// occupy when word-wrapped at the given width, matching the textarea's
// internal wrapping algorithm (which uses uniseg.StringWidth for grapheme
// cluster widths and wraps at word boundaries).
func countVisualLines(text string, width int) int {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return 1
	}
	total := 0
	for _, line := range strings.Split(text, "\n") {
		total += countWrappedLines(line, width)
	}
	if total == 0 {
		total = 1
	}
	return total
}

// countWrappedLines counts display lines for a single logical line when
// word-wrapped at the given width. Uses uniseg.StringWidth to match the
// textarea component's grapheme-aware width calculations.
func countWrappedLines(line string, width int) int {
	if line == "" {
		return 1
	}
	lineWidth := uniseg.StringWidth(line)
	if lineWidth <= width {
		return 1
	}
	// Word-wrap simulation: walk runes, accumulate width, break at boundaries
	lines := 1
	currentWidth := 0
	wordWidth := 0
	inWord := false

	for _, r := range line {
		rw := uniseg.StringWidth(string(r))
		isSpace := r == ' ' || r == '\t'

		if isSpace {
			if inWord {
				// End of word: commit word to current line
				if currentWidth+wordWidth > width && currentWidth > 0 {
					lines++
					currentWidth = wordWidth
				} else {
					currentWidth += wordWidth
				}
				wordWidth = 0
				inWord = false
			}
			currentWidth += rw
			if currentWidth > width {
				lines++
				currentWidth = rw
			}
		} else {
			inWord = true
			wordWidth += rw
			// If a single word exceeds width, force-break it
			if wordWidth > width {
				if currentWidth > 0 {
					lines++
				}
				// Break the overlong word at width boundary
				lines += (wordWidth - 1) / width
				currentWidth = wordWidth % width
				if currentWidth == 0 {
					currentWidth = width
				}
				wordWidth = 0
				inWord = false
			}
		}
	}
	// Flush remaining word
	if wordWidth > 0 {
		if currentWidth+wordWidth > width && currentWidth > 0 {
			lines++
		}
	}
	return lines
}
