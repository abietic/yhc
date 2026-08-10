package tui

import (
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

// selectionPlainLine keeps clipboard bytes control-free while the selected
// display-cell profile remains the sole cell/source conversion owner.
func selectionPlainLine(line string) string {
	return xansi.Strip(line)
}

func selectionLineCells(profile DisplayCellProfile, line string) int {
	return profile.measure(selectionPlainLine(line), 0)
}

func selectionCellByteBoundary(
	profile DisplayCellProfile,
	plain string,
	cell int,
	endBoundary bool,
) int {
	if cell <= 0 && !endBoundary {
		return 0
	}
	byteOffset := 0
	for _, cluster := range profile.clusters(plain, 0) {
		nextOffset := byteOffset + len(cluster.source)
		if cluster.cells == 0 && cell == cluster.startColumn {
			if endBoundary {
				byteOffset = nextOffset
				continue
			}
			return byteOffset
		}
		if cell <= cluster.startColumn {
			return byteOffset
		}
		if cell < cluster.endColumn {
			if endBoundary {
				return nextOffset
			}
			return byteOffset
		}
		byteOffset = nextOffset
	}
	return len(plain)
}

func selectionSliceCells(
	profile DisplayCellProfile,
	line string,
	startCell, endCell int,
) string {
	plain := selectionPlainLine(line)
	if startCell < 0 {
		startCell = 0
	}
	lineCells := profile.measure(plain, 0)
	if endCell > lineCells {
		endCell = lineCells
	}
	if startCell >= endCell {
		return ""
	}
	startByte := selectionCellByteBoundary(profile, plain, startCell, false)
	endByte := selectionCellByteBoundary(profile, plain, endCell, true)
	if startByte > endByte {
		startByte = endByte
	}
	return strings.TrimRight(plain[startByte:endByte], " \t")
}

func selectionHighlightCells(
	profile DisplayCellProfile,
	line string,
	startCell, endCell int,
) string {
	if startCell < 0 {
		startCell = 0
	}
	if endCell <= startCell {
		return line
	}
	var result strings.Builder
	for _, cluster := range profile.clusters(line, 0) {
		if cluster.control {
			result.WriteString(cluster.source)
			continue
		}
		selected := cluster.endColumn > startCell &&
			cluster.startColumn < endCell
		if cluster.cells == 0 {
			selected = cluster.startColumn >= startCell &&
				cluster.startColumn < endCell
		}
		if selected {
			result.WriteString("\x1b[7m")
			result.WriteString(cluster.text)
			result.WriteString("\x1b[27m")
			continue
		}
		result.WriteString(cluster.text)
	}
	return result.String()
}
