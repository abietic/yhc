package tui

import (
	"strings"
)

// Selection annotations are renderer-private, zero-cell sentinels. They are
// parsed and removed before a render-cache entry is published.
const (
	selectionMarkSemanticStart = "\u2060\u200b\u2060\u200c\u2060\u200b"
	selectionMarkSemanticEnd   = "\u2060\u200c\u2060\u200b\u2060\u200c"
	selectionMarkSoftBoundary  = "\u200c\u200b\u2060\u200c\u200b\u2060"
	selectionMarkHardBoundary  = "\u200b\u2060\u200c\u2060\u200b\u2060"
	selectionMarkHardRowsStart = "\u200c\u2060\u200b\u2060\u200c\u2060"
	selectionMarkHardRowsEnd   = "\u200b\u200c\u2060\u200b\u200c\u2060"
	selectionMarkPresentStart  = "\u2060\u200b\u200c\u2060\u200b\u200c"
	selectionMarkPresentEnd    = "\u2060\u200c\u200b\u2060\u200c\u200b"
	selectionMarkTabStart      = "\u200b\u2060\u200b\u200c\u2060\u200c"
	selectionMarkTabEnd        = "\u200c\u2060\u200c\u200b\u2060\u200b"
)

var selectionAnnotationMarkers = [...]string{
	selectionMarkSemanticStart,
	selectionMarkSemanticEnd,
	selectionMarkSoftBoundary,
	selectionMarkHardBoundary,
	selectionMarkHardRowsStart,
	selectionMarkHardRowsEnd,
	selectionMarkPresentStart,
	selectionMarkPresentEnd,
	selectionMarkTabStart,
	selectionMarkTabEnd,
}

type selectionBoundaryKind uint8

const (
	selectionBoundaryNone selectionBoundaryKind = iota
	selectionBoundarySoft
	selectionBoundaryHard
)

type selectionSemanticSpan struct {
	startCell int
	endCell   int
	text      string
}

type selectionRowMetadata struct {
	spans      []selectionSemanticSpan
	boundary   selectionBoundaryKind
	selectable bool
}

type selectionAnnotatedRender struct {
	rendered  string
	annotated bool
}

// historySelectionRenderItem is implemented by built-in renderers that can
// publish exact same-pass selection facts.
type historySelectionRenderItem interface {
	renderSelection(HistoryRenderContext) selectionAnnotatedRender
}

func selectionAnnotationsCollide(values ...string) bool {
	for _, value := range values {
		for _, marker := range selectionAnnotationMarkers {
			for _, markerRune := range marker {
				// A partial marker at either side of a wrapper can combine with
				// the wrapper itself into another valid marker. Treat every
				// private marker rune as a collision and fall back to visible,
				// non-selectable content instead of parsing ambiguous metadata.
				if strings.ContainsRune(value, markerRune) {
					return true
				}
			}
		}
	}
	return false
}

func selectionStripMarkers(value string) string {
	for _, marker := range selectionAnnotationMarkers {
		value = strings.ReplaceAll(value, marker, "")
	}
	return value
}

func selectionTruncateAnnotatedLine(
	profile DisplayCellProfile,
	line string,
	width int,
) string {
	if profile.measure(
		selectionPlainLine(selectionStripMarkers(line)),
		0,
	) <= width {
		return line
	}
	return selectionTruncateAnnotatedFailClosedAt(
		profile,
		line,
		width,
		0,
	)
}

func selectionTrimAnnotatedPadding(
	profile DisplayCellProfile,
	line string,
	width int,
) string {
	truncated := profile.truncate(line, width)
	originalMarkers := selectionMarkerSequence(line)
	keptMarkers := selectionMarkerSequence(truncated)
	if len(keptMarkers) > len(originalMarkers) {
		return truncated
	}
	for index := range keptMarkers {
		if keptMarkers[index] != originalMarkers[index] {
			return truncated
		}
	}
	for _, marker := range originalMarkers[len(keptMarkers):] {
		truncated += marker
	}
	return truncated
}

func selectionTruncateAnnotatedFailClosedAt(
	profile DisplayCellProfile,
	line string,
	width int,
	startColumn int,
) string {
	truncated := profile.truncateAt(line, width, startColumn)
	originalMarkers := selectionMarkerSequence(line)
	keptMarkers := selectionMarkerSequence(truncated)
	if len(keptMarkers) <= len(originalMarkers) {
		matches := true
		for index := range keptMarkers {
			if keptMarkers[index] != originalMarkers[index] {
				matches = false
				break
			}
		}
		if matches {
			for _, marker := range originalMarkers[len(keptMarkers):] {
				truncated += marker
			}
		}
	}
	// An unmatched tab end invalidates only this row without changing semantic,
	// presentation, or hard-row nesting carried into a continuation row.
	return truncated + selectionMarkTabEnd
}

func selectionMarkerSequence(value string) []string {
	var markers []string
	for index := 0; index < len(value); {
		found := false
		for _, marker := range selectionAnnotationMarkers {
			if strings.HasPrefix(value[index:], marker) {
				markers = append(markers, marker)
				index += len(marker)
				found = true
				break
			}
		}
		if !found {
			index++
		}
	}
	return markers
}

func selectionSemantic(value string) string {
	return selectionMarkSemanticStart + value + selectionMarkSemanticEnd
}

func selectionPresentation(value string) string {
	return selectionMarkPresentStart + value + selectionMarkPresentEnd
}

func selectionHardBreak() string {
	return selectionMarkHardBoundary + "\n"
}

func selectionSoftBreak() string {
	return selectionMarkSoftBoundary + "\n"
}

func selectionHardRows(value string) string {
	return selectionMarkHardRowsStart + value + selectionMarkHardRowsEnd
}

func selectionSemanticTab() string {
	return selectionMarkTabStart + "\t" + selectionMarkTabEnd
}

func selectionAnnotateTabs(value string) string {
	if !strings.ContainsRune(value, '\t') {
		return value
	}
	return strings.ReplaceAll(value, "\t", selectionSemanticTab())
}

func selectionAnnotatedWrappedText(
	profile DisplayCellProfile,
	value string,
	width int,
) (string, bool) {
	if selectionAnnotationsCollide(value) {
		return "", false
	}
	if width <= 0 {
		return selectionSemantic(selectionAnnotateTabs(value)), true
	}

	sourceLines := strings.Split(value, "\n")
	var result strings.Builder
	for lineIndex, sourceLine := range sourceLines {
		if lineIndex > 0 {
			result.WriteString(selectionHardBreak())
		}
		if profile.width(sourceLine) <= width {
			result.WriteString(selectionSemantic(selectionAnnotateTabs(sourceLine)))
			continue
		}

		words := strings.Fields(sourceLine)
		if len(words) == 0 {
			continue
		}
		var wrappedLines []string
		var current strings.Builder
		currentCells := 0
		for wordIndex, word := range words {
			wordCells := profile.width(word)
			switch {
			case wordIndex == 0:
				current.WriteString(selectionAnnotateTabs(word))
				currentCells = wordCells
			case currentCells+1+wordCells <= width:
				current.WriteByte(' ')
				current.WriteString(selectionAnnotateTabs(word))
				currentCells += 1 + wordCells
			default:
				wrappedLines = append(wrappedLines, current.String())
				current.Reset()
				current.WriteString(selectionAnnotateTabs(word))
				currentCells = wordCells
			}
		}
		wrappedLines = append(wrappedLines, current.String())
		for wrappedIndex, wrappedLine := range wrappedLines {
			if wrappedIndex > 0 {
				result.WriteString(selectionSoftBreak())
			}
			result.WriteString(selectionSemantic(wrappedLine))
		}
	}
	return result.String(), true
}

func selectionAnnotateVisibleRows(
	profile DisplayCellProfile,
	rendered string,
	prefixCells int,
) (string, bool) {
	if selectionAnnotationsCollide(rendered) {
		return "", false
	}
	lines := strings.Split(rendered, "\n")
	for index, line := range lines {
		lines[index] = selectionAnnotateVisibleLine(profile, line, prefixCells)
	}
	return strings.Join(lines, selectionHardBreak()), true
}

func selectionAnnotateRenderedRows(
	profile DisplayCellProfile,
	rendered string,
	prefixCells int,
) (string, bool) {
	if !selectionMarkersHaveZeroCells(profile) {
		return "", false
	}
	lines := strings.Split(rendered, "\n")
	for index, line := range lines {
		hasAnnotation := selectionAnnotationsCollide(line)
		if !hasAnnotation {
			lines[index] = selectionAnnotateVisibleLine(
				profile,
				line,
				prefixCells,
			)
		}
		if index < len(lines)-1 &&
			!selectionLineHasBoundary(lines[index]) {
			lines[index] += selectionMarkHardBoundary
		}
	}
	return strings.Join(lines, "\n"), true
}

func selectionAnnotateKnownRows(
	profile DisplayCellProfile,
	rendered string,
	prefixCells []int,
) (string, bool) {
	if selectionAnnotationsCollide(rendered) {
		return "", false
	}
	lines := strings.Split(rendered, "\n")
	if len(lines) != len(prefixCells) {
		return "", false
	}
	for index, line := range lines {
		if prefixCells[index] < 0 {
			lines[index] = selectionPresentation(line)
		} else {
			lines[index] = selectionAnnotateVisibleLine(
				profile,
				line,
				prefixCells[index],
			)
		}
	}
	return strings.Join(lines, selectionHardBreak()), true
}

func selectionLineHasBoundary(line string) bool {
	return strings.Contains(line, selectionMarkSoftBoundary) ||
		strings.Contains(line, selectionMarkHardBoundary) ||
		strings.Contains(line, selectionMarkHardRowsStart) ||
		strings.Contains(line, selectionMarkHardRowsEnd)
}

func selectionAnnotatedContentWrap(
	profile DisplayCellProfile,
	content string,
	width int,
	startColumn int,
) (string, bool) {
	if selectionAnnotationsCollide(content) {
		return "", false
	}
	sourceLines := strings.Split(content, "\n")
	var result strings.Builder
	for lineIndex, sourceLine := range sourceLines {
		if lineIndex > 0 {
			result.WriteString(selectionHardBreak())
		}
		wrapped := contentWrapLines(
			profile,
			sourceLine,
			width,
			startColumn,
		)
		for wrappedIndex, line := range wrapped {
			if wrappedIndex > 0 {
				result.WriteString(selectionSoftBreak())
			}
			if line != "" {
				result.WriteString(selectionSemantic(
					selectionAnnotateTabs(line),
				))
			}
		}
	}
	return result.String(), true
}

func selectionAnnotatedProfileWrap(
	profile DisplayCellProfile,
	text string,
	width int,
) (string, bool) {
	if selectionAnnotationsCollide(text) {
		return "", false
	}
	paragraphs := strings.Split(
		strings.ReplaceAll(text, "\r\n", "\n"),
		"\n",
	)
	var result strings.Builder
	for paragraphIndex, paragraph := range paragraphs {
		if paragraphIndex > 0 {
			result.WriteString(selectionHardBreak())
		}
		if paragraph == "" {
			continue
		}
		wrapped := profile.wrap(paragraph, max(1, width), false)
		for lineIndex, line := range wrapped {
			if lineIndex > 0 {
				result.WriteString(selectionSoftBreak())
			}
			result.WriteString(selectionSemantic(
				selectionAnnotateTabs(line),
			))
		}
	}
	return result.String(), true
}

func selectionAnnotatePlainMarkdownRendered(
	profile DisplayCellProfile,
	rendered string,
) string {
	lines := strings.Split(rendered, "\n")
	annotated := make([]string, len(lines))
	for index, line := range lines {
		if line != "" {
			annotated[index] = selectionAnnotateVisibleLine(
				profile,
				line,
				0,
			)
		}
	}
	var result strings.Builder
	for index, line := range annotated {
		if index > 0 {
			previousEmpty := lines[index-1] == ""
			currentEmpty := lines[index] == ""
			if previousEmpty || currentEmpty {
				result.WriteString(selectionHardBreak())
			} else {
				result.WriteString(selectionSoftBreak())
			}
		}
		result.WriteString(line)
	}
	return result.String()
}

func selectionAnnotateVisibleLine(
	profile DisplayCellProfile,
	line string,
	prefixCells int,
) string {
	var result strings.Builder
	semanticOpen := false
	for _, cluster := range profile.clusters(line, 0) {
		if cluster.control {
			result.WriteString(cluster.source)
			continue
		}
		semantic := cluster.endColumn > prefixCells
		if cluster.cells == 0 {
			semantic = cluster.startColumn >= prefixCells
		}
		if semantic && !semanticOpen {
			result.WriteString(selectionMarkSemanticStart)
			semanticOpen = true
		}
		if !semantic && semanticOpen {
			result.WriteString(selectionMarkSemanticEnd)
			semanticOpen = false
		}
		result.WriteString(cluster.source)
	}
	if semanticOpen {
		result.WriteString(selectionMarkSemanticEnd)
	}
	return result.String()
}

func selectionMarkersHaveZeroCells(profile DisplayCellProfile) bool {
	for _, marker := range selectionAnnotationMarkers {
		if profile.measure(marker, 0) != 0 {
			return false
		}
	}
	return true
}

// parseSelectionAnnotations strips renderer-private annotations while building
// immutable cell-to-semantic spans for the exact visible rows.
func parseSelectionAnnotations(
	profile DisplayCellProfile,
	annotated string,
) ([]string, []selectionRowMetadata, bool) {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	if !selectionMarkersHaveZeroCells(profile) {
		return strings.Split(annotated, "\n"), nil, false
	}

	var (
		lines             []string
		rows              []selectionRowMetadata
		display           strings.Builder
		semantic          strings.Builder
		row               selectionRowMetadata
		semanticDepth     int
		presentDepth      int
		hardRows          int
		pendingSoft       bool
		pendingHard       bool
		tabOpen           bool
		tabStartCell      int
		spanOpen          bool
		hardSpanCell      = -1
		pendingCell       = -1
		rowValid          = true
		valid             = true
		semanticStartRows []int
		presentStartRows  []int
		hardRowsStartRows []int
		tabStartRow       = -1
	)

	displayCells := func() int {
		return profile.measure(selectionPlainLine(display.String()), 0)
	}
	spanStartCell := 0
	startSpan := func() {
		spanStartCell = displayCells()
		semantic.Reset()
		spanOpen = true
		pendingCell = -1
	}
	flushSpan := func() {
		if !spanOpen {
			return
		}
		if semantic.Len() == 0 {
			spanOpen = false
			return
		}
		text := selectionPlainLine(semantic.String())
		endCell := displayCells()
		// A semantic boundary may fall inside an extended grapheme cluster,
		// for example when an emoji modifier attaches to presentation text on
		// its left. The byte-boundary mapper can safely re-segment only spans
		// whose standalone geometry matches the exact visible row. Fail closed
		// instead of publishing a span that can return different bytes.
		if text != "" && endCell > spanStartCell &&
			profile.measure(text, spanStartCell) == endCell-spanStartCell {
			row.spans = append(row.spans, selectionSemanticSpan{
				startCell: spanStartCell,
				endCell:   endCell,
				text:      text,
			})
		}
		semantic.Reset()
		spanOpen = false
	}
	maybeStartPendingSpan := func() {
		if pendingCell >= 0 && semanticDepth > 0 &&
			presentDepth == 0 && !tabOpen &&
			displayCells() >= pendingCell {
			startSpan()
		}
	}
	finishLine := func(boundary selectionBoundaryKind) {
		flushSpan()
		row.boundary = boundary
		row.selectable = rowValid && len(row.spans) > 0
		if !rowValid {
			row.spans = nil
			row.boundary = selectionBoundaryNone
		}
		lines = append(lines, display.String())
		rows = append(rows, row)
		display.Reset()
		row = selectionRowMetadata{}
		rowValid = true
		if semanticDepth > 0 && presentDepth == 0 && !tabOpen {
			if hardRows > 0 && hardSpanCell >= 0 {
				pendingCell = hardSpanCell
				maybeStartPendingSpan()
			} else {
				startSpan()
			}
		}
	}

	for index := 0; index < len(annotated); {
		switch {
		case strings.HasPrefix(annotated[index:], selectionMarkSemanticStart):
			semanticStartRows = append(semanticStartRows, len(rows))
			if semanticDepth == 0 && presentDepth == 0 {
				startSpan()
				if hardRows > 0 && hardSpanCell < 0 {
					hardSpanCell = spanStartCell
				}
			}
			semanticDepth++
			index += len(selectionMarkSemanticStart)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkSemanticEnd):
			if semanticDepth == 0 || tabOpen {
				valid = false
				rowValid = false
			} else {
				if semanticDepth == 1 && presentDepth == 0 {
					flushSpan()
				}
				semanticDepth--
				semanticStartRows = semanticStartRows[:len(semanticStartRows)-1]
			}
			index += len(selectionMarkSemanticEnd)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkHardBoundary):
			pendingHard = true
			pendingSoft = false
			index += len(selectionMarkHardBoundary)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkSoftBoundary):
			if !pendingHard {
				pendingSoft = true
			}
			index += len(selectionMarkSoftBoundary)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkHardRowsStart):
			hardRowsStartRows = append(hardRowsStartRows, len(rows))
			if hardRows == 0 {
				hardSpanCell = -1
			}
			hardRows++
			index += len(selectionMarkHardRowsStart)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkHardRowsEnd):
			if hardRows == 0 {
				valid = false
				rowValid = false
			} else {
				hardRows--
				hardRowsStartRows = hardRowsStartRows[:len(hardRowsStartRows)-1]
				if hardRows == 0 {
					hardSpanCell = -1
					pendingCell = -1
				}
			}
			index += len(selectionMarkHardRowsEnd)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkPresentStart):
			presentStartRows = append(presentStartRows, len(rows))
			if presentDepth == 0 && semanticDepth > 0 && !tabOpen {
				flushSpan()
			}
			presentDepth++
			index += len(selectionMarkPresentStart)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkPresentEnd):
			if presentDepth == 0 {
				valid = false
				rowValid = false
			} else {
				presentDepth--
				presentStartRows = presentStartRows[:len(presentStartRows)-1]
				if presentDepth == 0 && semanticDepth > 0 && !tabOpen {
					if pendingCell >= 0 {
						maybeStartPendingSpan()
					} else {
						startSpan()
					}
				}
			}
			index += len(selectionMarkPresentEnd)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkTabStart):
			if semanticDepth == 0 || presentDepth > 0 || tabOpen {
				valid = false
				rowValid = false
			} else {
				flushSpan()
				tabOpen = true
				tabStartCell = displayCells()
				tabStartRow = len(rows)
			}
			index += len(selectionMarkTabStart)
			continue
		case strings.HasPrefix(annotated[index:], selectionMarkTabEnd):
			if !tabOpen {
				valid = false
				rowValid = false
			} else {
				tabOpen = false
				tabStartRow = -1
				endCell := displayCells()
				row.spans = append(row.spans, selectionSemanticSpan{
					startCell: tabStartCell,
					endCell:   endCell,
					text:      "\t",
				})
				if semanticDepth > 0 && presentDepth == 0 {
					startSpan()
				}
			}
			index += len(selectionMarkTabEnd)
			continue
		}

		if annotated[index] == '\n' {
			if tabOpen {
				valid = false
				rowValid = false
			}
			boundary := selectionBoundaryNone
			switch {
			case pendingHard:
				boundary = selectionBoundaryHard
			case pendingSoft:
				boundary = selectionBoundarySoft
			case hardRows > 0:
				boundary = selectionBoundaryHard
			case semanticDepth > 0 && presentDepth == 0:
				boundary = selectionBoundarySoft
			}
			finishLine(boundary)
			pendingSoft = false
			pendingHard = false
			index++
			continue
		}

		display.WriteByte(annotated[index])
		if spanOpen && semanticDepth > 0 && presentDepth == 0 && !tabOpen {
			semantic.WriteByte(annotated[index])
		}
		if !spanOpen {
			maybeStartPendingSpan()
		}
		index++
	}

	invalidFrom := len(rows) + 1
	for _, starts := range [][]int{
		semanticStartRows,
		presentStartRows,
		hardRowsStartRows,
	} {
		if len(starts) > 0 {
			invalidFrom = min(invalidFrom, starts[0])
		}
	}
	if tabStartRow >= 0 {
		invalidFrom = min(invalidFrom, tabStartRow)
	}
	if pendingSoft {
		invalidFrom = min(invalidFrom, len(rows))
	}
	if invalidFrom <= len(rows) {
		valid = false
		rowValid = false
	}
	finishLine(func() selectionBoundaryKind {
		if pendingHard {
			return selectionBoundaryHard
		}
		return selectionBoundaryNone
	}())
	for index := invalidFrom; index < len(rows); index++ {
		rows[index].selectable = false
		rows[index].spans = nil
		rows[index].boundary = selectionBoundaryNone
	}

	return lines, rows, valid
}

func selectionRowText(
	profile DisplayCellProfile,
	row selectionRowMetadata,
	startCell, endCell int,
) string {
	if !row.selectable || startCell >= endCell {
		return ""
	}
	var result strings.Builder
	for _, span := range row.spans {
		from := max(startCell, span.startCell)
		to := min(endCell, span.endCell)
		if from >= to {
			continue
		}
		result.WriteString(selectionSemanticSpanText(profile, span, from, to))
	}
	return result.String()
}

func selectionSemanticSpanText(
	profile DisplayCellProfile,
	span selectionSemanticSpan,
	startCell, endCell int,
) string {
	if span.text == "" || startCell >= endCell {
		return ""
	}
	startByte := selectionSemanticSpanByteBoundary(
		profile,
		span,
		startCell,
		false,
	)
	endByte := selectionSemanticSpanByteBoundary(
		profile,
		span,
		endCell,
		true,
	)
	if startByte > endByte {
		startByte = endByte
	}
	return span.text[startByte:endByte]
}

func selectionSemanticSpanByteBoundary(
	profile DisplayCellProfile,
	span selectionSemanticSpan,
	cell int,
	endBoundary bool,
) int {
	byteOffset := 0
	for _, cluster := range profile.clusters(span.text, span.startCell) {
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
	return len(span.text)
}

func selectionRowRanges(
	row selectionRowMetadata,
	startCell, endCell int,
) [][2]int {
	if !row.selectable || startCell >= endCell {
		return nil
	}
	ranges := make([][2]int, 0, len(row.spans))
	for _, span := range row.spans {
		from := max(startCell, span.startCell)
		to := min(endCell, span.endCell)
		if from < to {
			ranges = append(ranges, [2]int{from, to})
		}
	}
	return ranges
}

func selectionRowCellBounds(row selectionRowMetadata) (int, int, bool) {
	if !row.selectable || len(row.spans) == 0 {
		return 0, 0, false
	}
	return row.spans[0].startCell, row.spans[len(row.spans)-1].endCell, true
}
