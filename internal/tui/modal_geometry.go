package tui

import (
	"path/filepath"
	"strings"
)

// modalFrameGeometry is the profile-owned rectangle published by one modal
// render. Rectangles use terminal cells with an inclusive origin and exclusive
// end, matching layoutRect and pointer routing.
type modalFrameGeometry struct {
	outer layoutRect
}

func modalProjectLine(
	profile DisplayCellProfile,
	line string,
	width, startColumn int,
) string {
	if width <= 0 {
		return ""
	}
	projected := profile.truncateAt(line, width, startColumn)
	return profile.balanceControlLines([]string{projected})[0]
}

func modalEllipsize(
	profile DisplayCellProfile,
	line string,
	width, startColumn int,
	suffix string,
) string {
	if width <= 0 {
		return ""
	}
	projected := profile.truncateAt(line, width, startColumn)
	if profile.measure(line, startColumn) <= width {
		return profile.balanceControlLines([]string{projected})[0]
	}
	suffix = profile.truncateAt(suffix, width, startColumn)
	suffixWidth := profile.measure(suffix, startColumn)
	if suffixWidth >= width {
		return suffix
	}
	head := profile.truncateAt(line, width-suffixWidth, startColumn)
	projected = profile.truncateAt(head+suffix, width, startColumn)
	return profile.balanceControlLines([]string{projected})[0]
}

func modalTailEllipsize(
	profile DisplayCellProfile,
	line string,
	width, startColumn int,
	prefix string,
) string {
	if width <= 0 {
		return ""
	}
	if profile.measure(line, startColumn) <= width {
		return modalProjectLine(profile, line, width, startColumn)
	}
	prefix = modalProjectLine(profile, prefix, width, startColumn)
	if profile.measure(prefix, startColumn) >= width {
		return prefix
	}
	clusters := profile.clusters(line, startColumn)
	tail := ""
	for index := len(clusters) - 1; index >= 0; index-- {
		candidate := clusters[index].source + tail
		if profile.measure(prefix+candidate, startColumn) > width {
			break
		}
		tail = candidate
	}
	return modalProjectLine(profile, prefix+tail, width, startColumn)
}

func modalTruncatePath(
	profile DisplayCellProfile,
	path string,
	width, startColumn int,
) string {
	if width <= 0 {
		return ""
	}
	if profile.measure(path, startColumn) <= width {
		return profile.truncateAt(path, width, startColumn)
	}
	separator := string(filepath.Separator)
	last := filepath.Base(path)
	prefix := "…" + separator
	if strings.HasPrefix(path, "~"+separator) {
		prefix = "~" + separator + "…" + separator
	}
	prefix = profile.truncateAt(prefix, width, startColumn)
	remaining := width - profile.measure(prefix, startColumn)
	if remaining <= 0 {
		return prefix
	}
	return profile.truncateAt(
		prefix+profile.truncateAt(last, remaining, startColumn+profile.measure(prefix, startColumn)),
		width,
		startColumn,
	)
}

func modalTopFrame(
	profile DisplayCellProfile,
	lines []string,
	width, height int,
) (string, modalFrameGeometry) {
	if width <= 0 || height <= 0 {
		return "", modalFrameGeometry{}
	}
	projected := make([]string, len(lines))
	for index := range lines {
		projected[index] = modalProjectLine(profile, lines[index], width, 0)
	}
	return modalTopProjectedFrame(profile, projected, height)
}

func modalTopProjectedFrame(
	profile DisplayCellProfile,
	lines []string,
	height int,
) (string, modalFrameGeometry) {
	if height <= 0 {
		return "", modalFrameGeometry{}
	}
	visible := min(len(lines), height)
	result := append([]string(nil), lines[:visible]...)
	maxWidth := 0
	for _, line := range result {
		maxWidth = max(maxWidth, profile.measure(line, 0))
	}
	for len(result) < height {
		result = append(result, "")
	}
	return strings.Join(result, "\n"), modalFrameGeometry{
		outer: layoutRect{X: 0, Y: 0, Width: maxWidth, Height: visible},
	}
}

func modalBottomOverlay(
	profile DisplayCellProfile,
	base string,
	lines []string,
	width, height int,
) (string, modalFrameGeometry) {
	if width <= 0 || height <= 0 {
		return "", modalFrameGeometry{}
	}
	baseLines := modalBaseLines(profile, base, width, height)
	visible := min(len(lines), height)
	startY := 0
	if len(lines) <= height {
		startY = height - visible
	}
	maxWidth := 0
	for index := 0; index < visible; index++ {
		line := modalProjectLine(profile, lines[index], width, 0)
		maxWidth = max(maxWidth, profile.measure(line, 0))
		baseLines[startY+index] = line
	}
	return strings.Join(baseLines, "\n"), modalFrameGeometry{
		outer: layoutRect{X: 0, Y: startY, Width: maxWidth, Height: visible},
	}
}

func modalCenteredOverlay(
	profile DisplayCellProfile,
	base, overlay string,
	width, height int,
) (string, modalFrameGeometry) {
	if width <= 0 || height <= 0 {
		return "", modalFrameGeometry{}
	}
	lines := strings.Split(overlay, "\n")
	startX := modalCenteredStartColumn(profile, lines, width)
	visible := min(len(lines), height)
	startY := 0
	if len(lines) <= height {
		startY = (height - visible) / 2
	}
	baseLines := modalBaseLines(profile, base, width, height)
	maxWidth := 0
	for index := 0; index < visible; index++ {
		line := modalProjectLine(profile, lines[index], width-startX, startX)
		maxWidth = max(maxWidth, profile.measure(line, startX))
		baseLines[startY+index] = fitLayoutColumnLine(
			profile,
			strings.Repeat(" ", startX)+line,
			width,
			0,
		)
	}
	return strings.Join(baseLines, "\n"), modalFrameGeometry{
		outer: layoutRect{
			X:      startX,
			Y:      startY,
			Width:  maxWidth,
			Height: visible,
		},
	}
}

func modalCenteredStartColumn(
	profile DisplayCellProfile,
	lines []string,
	width int,
) int {
	bestColumn := 0
	bestImbalance := width + 1
	found := false
	for candidate := 0; candidate <= width; candidate++ {
		boxWidth := 0
		for _, line := range lines {
			boxWidth = max(boxWidth, profile.measure(line, candidate))
		}
		right := width - candidate - boxWidth
		if right < 0 {
			continue
		}
		imbalance := candidate - right
		if imbalance < 0 {
			imbalance = -imbalance
		}
		if !found || imbalance < bestImbalance {
			bestColumn = candidate
			bestImbalance = imbalance
			found = true
		}
	}
	return bestColumn
}

func modalBaseLines(
	profile DisplayCellProfile,
	base string,
	width, height int,
) []string {
	lines := strings.Split(base, "\n")
	if base == "" {
		lines = nil
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for index := range lines {
		lines[index] = modalProjectLine(profile, lines[index], width, 0)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func modalClipRect(rect layoutRect, height int) layoutRect {
	if rect.Width <= 0 || rect.Height <= 0 || rect.Y < 0 || rect.Y >= height {
		return layoutRect{}
	}
	rect.Height = min(rect.Height, height-rect.Y)
	return rect
}
