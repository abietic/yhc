package tui

import (
	"html"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	gast "github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	gtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const (
	tableMinColWidth = 3
	tableMaxRowLines = 4
	tableSafeMargin  = 4
)

type tableCell struct {
	raw  string // plain semantic compatibility projection
	runs []tableRun
}

// tableRun is a terminal-independent semantic inline fragment.
type tableRun struct {
	text                       string
	bold, italic, code, strike bool
	image                      bool
	linkURL                    string
}

type parsedTable struct {
	headers []tableCell
	aligns  []string // "left", "center", "right" per column
	rows    [][]tableCell
}

// renderTableWithThemeAndProfile renders a parsed Markdown table with the
// caller-selected App geometry profile.
func renderTableWithThemeAndProfile(tbl *parsedTable, termWidth int, theme markdownTheme, profile DisplayCellProfile) string {
	return renderTableWithThemeAndProfileMode(
		tbl,
		termWidth,
		theme,
		profile,
		false,
	)
}

func renderTableWithThemeAndProfileMode(
	tbl *parsedTable,
	termWidth int,
	theme markdownTheme,
	profile DisplayCellProfile,
	selection bool,
) string {
	numCols := len(tbl.headers)
	if numCols == 0 {
		return ""
	}

	// Step 1: Calculate min (longest word) and ideal (full content) widths per column.
	minWidths := make([]int, numCols)
	idealWidths := make([]int, numCols)
	for col := 0; col < numCols; col++ {
		header := tbl.headers[col].raw
		minWidths[col] = cellMinWidthWithProfile(header, profile)
		idealWidths[col] = maxInt(profile.width(header), tableMinColWidth)
		for _, row := range tbl.rows {
			if col < len(row) {
				text := row[col].raw
				minWidths[col] = maxInt(minWidths[col], cellMinWidthWithProfile(text, profile))
				idealWidths[col] = maxInt(idealWidths[col], maxInt(profile.width(text), tableMinColWidth))
			}
		}
	}

	// Step 2: Available width after borders and margin.
	// Border: │ content │ content │ = 1 + (2 padding + 1 border) per column
	borderOverhead := 1 + numCols*3
	availableWidth := maxInt(termWidth-borderOverhead-tableSafeMargin, numCols*tableMinColWidth)

	// Step 3: Distribute widths.
	totalMin := sumInts(minWidths)
	totalIdeal := sumInts(idealWidths)
	needsHardWrap := false
	var colWidths []int

	switch {
	case totalIdeal <= availableWidth:
		colWidths = idealWidths
	case totalMin <= availableWidth:
		extra := availableWidth - totalMin
		overflows := make([]int, numCols)
		totalOverflow := 0
		for i := range overflows {
			overflows[i] = idealWidths[i] - minWidths[i]
			totalOverflow += overflows[i]
		}
		colWidths = make([]int, numCols)
		for i := range colWidths {
			bonus := 0
			if totalOverflow > 0 {
				bonus = overflows[i] * extra / totalOverflow
			}
			colWidths[i] = minWidths[i] + bonus
		}
	default:
		needsHardWrap = true
		scaleFactor := float64(availableWidth) / float64(totalMin)
		colWidths = make([]int, numCols)
		for i := range colWidths {
			colWidths[i] = maxInt(int(float64(minWidths[i])*scaleFactor), tableMinColWidth)
		}
	}

	// Step 4: Check if vertical format is needed.
	maxLines := calcMaxRowLines(tbl, colWidths, needsHardWrap, theme, profile)
	if maxLines > tableMaxRowLines {
		return renderVerticalTableMode(
			tbl,
			termWidth,
			theme,
			profile,
			selection,
		)
	}

	// Step 5: Render horizontal table.
	var lines []string
	lines = append(lines, renderBorder(colWidths, "top"))
	lines = append(lines, renderRowMode(
		tbl.headers,
		colWidths,
		tbl.aligns,
		needsHardWrap,
		true,
		theme,
		profile,
		selection,
	)...)
	lines = append(lines, renderBorder(colWidths, "middle"))
	for i, row := range tbl.rows {
		lines = append(lines, renderRowMode(
			row,
			colWidths,
			tbl.aligns,
			needsHardWrap,
			false,
			theme,
			profile,
			selection,
		)...)
		if i < len(tbl.rows)-1 {
			lines = append(lines, renderBorder(colWidths, "middle"))
		}
	}
	lines = append(lines, renderBorder(colWidths, "bottom"))

	// Safety: if any line exceeds terminal width, fall back to vertical.
	for _, line := range lines {
		if profile.width(line) > termWidth-tableSafeMargin {
			return renderVerticalTableMode(
				tbl,
				termWidth,
				theme,
				profile,
				selection,
			)
		}
	}

	rendered := strings.Join(lines, "\n")
	if selection {
		return selectionHardRows(rendered)
	}
	return rendered
}

func renderRowMode(
	cells []tableCell,
	colWidths []int,
	aligns []string,
	hardWrap, isHeader bool,
	theme markdownTheme,
	profile DisplayCellProfile,
	selection bool,
) []string {
	numCols := len(colWidths)
	cellLines := make([][]string, numCols)
	maxHeight := 1

	for col := 0; col < numCols; col++ {
		content := ""
		if col < len(cells) {
			content = renderTableCell(cells[col], theme)
		}
		wrapped := wrapCell(content, colWidths[col], hardWrap, profile)
		if selection {
			for index, line := range wrapped {
				if line != "" {
					wrapped[index] = selectionSemantic(
						selectionAnnotateTabs(line),
					)
				}
			}
		}
		cellLines[col] = wrapped
		if len(wrapped) > maxHeight {
			maxHeight = len(wrapped)
		}
	}

	var result []string
	for lineIdx := 0; lineIdx < maxHeight; lineIdx++ {
		var sb strings.Builder
		sb.WriteByte('\xe2') // │ = U+2502 (3 bytes)
		sb.WriteByte('\x94')
		sb.WriteByte('\x82')
		for col := 0; col < numCols; col++ {
			text := ""
			if lineIdx < len(cellLines[col]) {
				text = cellLines[col][lineIdx]
			}
			w := colWidths[col]
			align := "left"
			if col < len(aligns) {
				align = aligns[col]
			}
			if isHeader {
				align = "center"
			}
			sb.WriteByte(' ')
			sb.WriteString(padAlignedCell(text, w, align, profile))
			sb.WriteString(" │")
		}
		result = append(result, sb.String())
	}
	return result
}

// renderBorder renders a horizontal table border.
func renderBorder(colWidths []int, position string) string {
	var left, mid, cross, right string
	switch position {
	case "top":
		left, mid, cross, right = "┌", "─", "┬", "┐"
	case "middle":
		left, mid, cross, right = "├", "─", "┼", "┤"
	case "bottom":
		left, mid, cross, right = "└", "─", "┴", "┘"
	}
	var sb strings.Builder
	sb.WriteString(left)
	for i, w := range colWidths {
		sb.WriteString(strings.Repeat(mid, w+2))
		if i < len(colWidths)-1 {
			sb.WriteString(cross)
		} else {
			sb.WriteString(right)
		}
	}
	return sb.String()
}

func renderVerticalTableMode(
	tbl *parsedTable,
	termWidth int,
	theme markdownTheme,
	profile DisplayCellProfile,
	selection bool,
) string {
	var lines []string
	headers := make([]string, len(tbl.headers))
	for i, h := range tbl.headers {
		headers[i] = renderTableCell(h, theme)
	}

	termWidth = maxInt(termWidth, 1)
	sepWidth := minInt(maxInt(termWidth-1, 1), 40)
	separator := strings.Repeat("─", sepWidth)

	for ri, row := range tbl.rows {
		if ri > 0 {
			lines = append(lines, separator)
		}
		for ci, cell := range row {
			label := ""
			if ci < len(headers) {
				label = headers[ci]
			}
			if label == "" {
				label = "Column"
			}
			value := strings.TrimSpace(renderTableCell(cell, theme))
			value = strings.ReplaceAll(value, "\n", " ")
			lines = append(
				lines,
				renderVerticalFieldMode(
					label,
					value,
					termWidth,
					profile,
					selection,
				)...,
			)
		}
	}
	rendered := strings.Join(lines, "\n")
	if selection {
		return selectionHardRows(rendered)
	}
	return rendered
}

func renderVerticalField(
	label string,
	value string,
	width int,
	profile DisplayCellProfile,
) []string {
	return renderVerticalFieldMode(label, value, width, profile, false)
}

func renderVerticalFieldMode(
	label string,
	value string,
	width int,
	profile DisplayCellProfile,
	selection bool,
) []string {
	width = maxInt(width, 1)
	labelContent := label
	if selection {
		labelContent = selectionSemantic(selectionAnnotateTabs(label))
	}
	labelSuffix := ":"
	if selection {
		labelSuffix = selectionPresentation(labelSuffix)
	}
	styledLabel := "\x1b[1m" + labelContent + labelSuffix + "\x1b[0m"
	labelWidth := profile.width(label) + 1
	if labelWidth+1 < width {
		valueWidth := maxInt(width-labelWidth-1, 1)
		wrapped := wrapCell(value, valueWidth, true, profile)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		if selection {
			for index, line := range wrapped {
				if line != "" {
					wrapped[index] = selectionSemantic(
						selectionAnnotateTabs(line),
					)
				}
			}
		}
		gap := " "
		if selection {
			gap = selectionPresentation(gap)
		}
		lines := []string{styledLabel + gap + wrapped[0]}
		indent := minInt(2, maxInt(width-1, 0))
		for index := 1; index < len(wrapped); index++ {
			prefix := strings.Repeat(" ", indent)
			if selection {
				prefix = selectionPresentation(prefix)
			}
			lines = append(lines, prefix+wrapped[index])
		}
		return lines
	}

	lines := profile.wrap(styledLabel, width, true)
	if value == "" {
		return lines
	}
	indent := minInt(2, maxInt(width-1, 0))
	valueWidth := maxInt(width-indent, 1)
	for _, line := range wrapCell(value, valueWidth, true, profile) {
		prefix := strings.Repeat(" ", indent)
		if selection {
			prefix = selectionPresentation(prefix)
			line = selectionSemantic(selectionAnnotateTabs(line))
		}
		lines = append(lines, prefix+line)
	}
	return lines
}

// wrapCell wraps text to fit within width, returning lines.
func wrapCell(text string, width int, hardWrap bool, profile DisplayCellProfile) []string {
	if width <= 0 {
		return []string{text}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}

	return profile.wrap(text, width, hardWrap)
}

// padAlignedCell pads text to the given width with the specified alignment.
func padAlignedCell(text string, width int, align string, profile DisplayCellProfile) string {
	textW := profile.width(text)
	if textW >= width {
		return text
	}
	padding := width - textW
	switch align {
	case "center":
		left := padding / 2
		right := padding - left
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
	case "right":
		return strings.Repeat(" ", padding) + text
	default: // left
		return text + strings.Repeat(" ", padding)
	}
}

func cellMinWidthWithProfile(text string, profile DisplayCellProfile) int {
	words := strings.Fields(text)
	if len(words) == 0 {
		return tableMinColWidth
	}
	max := tableMinColWidth
	for _, w := range words {
		ww := profile.width(w)
		if ww > max {
			max = ww
		}
	}
	return max
}

// calcMaxRowLines calculates the maximum number of lines any row would need.
func calcMaxRowLines(tbl *parsedTable, colWidths []int, hardWrap bool, theme markdownTheme, profile DisplayCellProfile) int {
	maxLines := 1
	for col, h := range tbl.headers {
		if col < len(colWidths) {
			lines := wrapCell(renderTableCell(h, theme), colWidths[col], hardWrap, profile)
			if len(lines) > maxLines {
				maxLines = len(lines)
			}
		}
	}
	for _, row := range tbl.rows {
		for col, cell := range row {
			if col < len(colWidths) {
				lines := wrapCell(renderTableCell(cell, theme), colWidths[col], hardWrap, profile)
				if len(lines) > maxLines {
					maxLines = len(lines)
				}
			}
		}
	}
	return maxLines
}

// parseMarkdownTable retains the direct-test helper while Goldmark owns the
// grammar. Production parses the complete prepared source exactly once.
func parseMarkdownTable(block string) *parsedTable {
	_, tables, ok := extractTableIslands(block, markdownStableComplete)
	if !ok || len(tables) == 0 {
		return nil
	}
	return tables[0].table
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sumInts(s []int) int {
	total := 0
	for _, v := range s {
		total += v
	}
	return total
}

const tablePlaceholderPrefix = "ET"

type tableSourceRange struct {
	start, end    int
	table         *parsedTable
	prefix        string
	deferred      bool
	literalSource string
}

type tableIsland struct {
	token         string
	table         *parsedTable
	deferred      bool
	literalSource string
}

// extractTableIslands uses Goldmark's GFM AST as the sole table grammar
// owner. Complete tables become collision-free fenced-code sentinels so
// Glamour can project their blockquote/list continuation prefix without
// owning table layout. Tables under the active final top-level block use the
// same sentinel but splice back sanitized literal source until promotion or
// Finalize proves that block complete.
//
// The parse view only masks code-span pipes to work around Goldmark v1.7.13's
// table extension bug; it preserves every byte offset used for extraction.
func extractTableIslands(
	content string,
	completeness markdownCompleteness,
) (string, []tableIsland, bool) {
	parseView := tableParseView(content)
	streamingMarkdownParserMu.Lock()
	doc := streamingMarkdownParser.Parser().Parse(gtext.NewReader(parseView))
	streamingMarkdownParserMu.Unlock()

	ranges := make([]tableSourceRange, 0)
	extractionOK := true
	_ = gast.Walk(doc, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		table, ok := node.(*extast.Table)
		if !ok {
			return gast.WalkContinue, nil
		}
		parsed, start, end, ok := semanticTableFromAST(table, []byte(content))
		if !ok {
			extractionOK = false
			return gast.WalkStop, nil
		}
		prefix := tableSourceContainerPrefix(content, start)
		ranges = append(ranges, tableSourceRange{
			start:  start,
			end:    end,
			table:  parsed,
			prefix: prefix,
			deferred: completeness == markdownStreamingIncomplete &&
				topLevelMarkdownBlock(node, doc) == doc.LastChild(),
			literalSource: tableLiteralSource(content[start:end], prefix),
		})
		return gast.WalkSkipChildren, nil
	})
	if !extractionOK {
		return content, nil, false
	}
	if len(ranges) == 0 {
		return content, nil, true
	}
	if !validTableRanges(ranges, len(content)) {
		return content, nil, false
	}

	tables := make([]tableIsland, 0, len(ranges))
	var out strings.Builder
	last := 0
	placeholderIndex := 0
	for _, r := range ranges {
		out.WriteString(content[last:r.start])
		var token string
		for {
			token = tablePlaceholderToken(placeholderIndex)
			placeholderIndex++
			if !strings.Contains(content, token) {
				break
			}
		}
		out.WriteString(tableIslandSkeleton(r.prefix, token, content[r.start:r.end]))
		tables = append(tables, tableIsland{
			token:         token,
			table:         r.table,
			deferred:      r.deferred,
			literalSource: r.literalSource,
		})
		last = r.end
	}
	out.WriteString(content[last:])
	return out.String(), tables, true
}

// spliceTableIslands replaces each rendered sentinel line while retaining
// Glamour's surrounding block margins and projected container continuation
// prefix. Missing or duplicated sentinels fail closed; callers then render
// sanitized literal source rather than exposing an anchor or selecting
// Glamour as a second table owner.
func spliceTableIslands(
	rendered string,
	tables []tableIsland,
	width int,
	theme markdownTheme,
	profile DisplayCellProfile,
) (string, bool) {
	return spliceTableIslandsMode(
		rendered,
		tables,
		width,
		theme,
		profile,
		false,
	)
}

func spliceTableIslandsMode(
	rendered string,
	tables []tableIsland,
	width int,
	theme markdownTheme,
	profile DisplayCellProfile,
	selection bool,
) (string, bool) {
	if len(tables) == 0 {
		return rendered, true
	}
	for _, island := range tables {
		if strings.Count(rendered, island.token) != 1 ||
			strings.Count(xansi.Strip(rendered), island.token) != 1 {
			return "", false
		}
		lines := strings.Split(rendered, "\n")
		lineIndex := -1
		for index, line := range lines {
			if strings.Contains(line, island.token) {
				if lineIndex >= 0 {
					return "", false
				}
				lineIndex = index
			}
		}
		if lineIndex < 0 {
			return "", false
		}

		line := lines[lineIndex]
		tokenIndex := strings.Index(line, island.token)
		visiblePrefix := xansi.Strip(line[:tokenIndex])
		visibleSuffix := xansi.Strip(line[tokenIndex+len(island.token):])
		if selection {
			visiblePrefix = selectionStripMarkers(visiblePrefix)
			visibleSuffix = selectionStripMarkers(visibleSuffix)
		}
		if !strings.HasSuffix(visiblePrefix, "  ") ||
			strings.TrimSpace(visibleSuffix) != "" {
			return "", false
		}
		visiblePrefix = strings.TrimSuffix(visiblePrefix, "  ")
		if !validTableContainerProjection(visiblePrefix) {
			return "", false
		}

		contentWidth := maxInt(width-profile.width(visiblePrefix), 1)
		var replacement string
		if island.deferred {
			replacement = renderDeferredTableLiteral(
				island.literalSource,
				contentWidth,
				profile,
			)
			if selection {
				annotated, ok := selectionAnnotateVisibleRows(
					profile,
					replacement,
					0,
				)
				if !ok {
					return "", false
				}
				replacement = annotated
			}
		} else {
			replacement = renderTableWithThemeAndProfileMode(
				island.table,
				contentWidth,
				theme,
				profile,
				selection,
			)
		}

		styledPrefix := styleTableContainerProjection(visiblePrefix, theme)
		if selection {
			styledPrefix = selectionPresentation(styledPrefix)
		}
		replacementLines := strings.Split(replacement, "\n")
		for index := range replacementLines {
			replacementLines[index] = styledPrefix + replacementLines[index]
		}
		replacementLines = profile.balanceControlLines(replacementLines)
		lines[lineIndex] = strings.Join(replacementLines, "\n")
		rendered = strings.Join(lines, "\n")
	}
	return rendered, true
}

func tablePlaceholderToken(index int) string {
	return tablePlaceholderPrefix +
		strings.ToUpper(strconv.FormatInt(int64(index), 36)) +
		"T"
}

func topLevelMarkdownBlock(node, document gast.Node) gast.Node {
	current := node
	for current != nil && current.Parent() != nil && current.Parent() != document {
		current = current.Parent()
	}
	return current
}

func tableSourceContainerPrefix(content string, start int) string {
	end := start
	for end < len(content) && content[end] != '\n' {
		end++
	}
	line := content[start:end]
	index := 0
	for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
		index++
	}
	for index < len(line) && line[index] == '>' {
		index++
		if index < len(line) && line[index] == ' ' {
			index++
		}
		for index < len(line) && (line[index] == ' ' || line[index] == '\t') {
			index++
		}
	}
	return line[:index]
}

func tableLiteralSource(source, prefix string) string {
	source = strings.TrimSuffix(source, "\n")
	lines := strings.Split(source, "\n")
	for index := range lines {
		lines[index] = strings.TrimPrefix(lines[index], prefix)
	}
	return strings.Join(lines, "\n")
}

func tableIslandSkeleton(prefix, token, original string) string {
	var out strings.Builder
	out.WriteString(prefix)
	out.WriteString("~~~text\n")
	out.WriteString(prefix)
	out.WriteString(token)
	out.WriteByte('\n')
	out.WriteString(prefix)
	out.WriteString("~~~")
	if strings.HasSuffix(original, "\n") {
		out.WriteByte('\n')
	}
	return out.String()
}

func validTableContainerProjection(prefix string) bool {
	for _, r := range prefix {
		if r != ' ' && r != '▎' {
			return false
		}
	}
	return true
}

func styleTableContainerProjection(prefix string, theme markdownTheme) string {
	if prefix == "" {
		return ""
	}
	bar := termenv.String("▎").
		Foreground(theme.colorProfile().Color(theme.brand)).
		String()
	if !strings.HasSuffix(bar, "\x1b[0m") {
		bar += "\x1b[0m"
	}
	return strings.ReplaceAll(prefix, "▎", bar)
}

func renderDeferredTableLiteral(
	source string,
	width int,
	profile DisplayCellProfile,
) string {
	width = maxInt(width, 1)
	lines := strings.Split(source, "\n")
	var rendered []string
	for _, line := range lines {
		line = sanitizeLiteralTableLine(line)
		rendered = append(rendered, profile.wrap(line, width, true)...)
	}
	return strings.Join(rendered, "\n")
}

func sanitizeLiteralTableLine(line string) string {
	line = strings.ReplaceAll(line, "\t", "    ")
	line = strings.ToValidUTF8(line, string(unicode.ReplacementChar))
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return unicode.ReplacementChar
		}
		return r
	}, line)
}

func renderSafeMarkdownLiteral(
	source string,
	width int,
	profile DisplayCellProfile,
) string {
	width = maxInt(width, 1)
	lines := strings.Split(source, "\n")
	var rendered []string
	for _, line := range lines {
		rendered = append(
			rendered,
			profile.wrap(sanitizeLiteralTableLine(line), width, true)...,
		)
	}
	return strings.Join(rendered, "\n")
}

func semanticTableFromAST(table *extast.Table, source []byte) (*parsedTable, int, int, bool) {
	parsed := &parsedTable{}
	min, max := len(source), -1
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		cells, ok := semanticTableRow(row, source, &min, &max)
		if !ok {
			return nil, 0, 0, false
		}
		switch row.(type) {
		case *extast.TableHeader:
			parsed.headers = cells
		case *extast.TableRow:
			parsed.rows = append(parsed.rows, cells)
		}
	}
	if len(parsed.headers) == 0 || min > max {
		return nil, 0, 0, false
	}
	for _, alignment := range table.Alignments {
		parsed.aligns = append(parsed.aligns, alignment.String())
	}
	start := lineStart(source, min)
	end := lineEnd(source, max)
	if len(parsed.rows) == 0 { // Cells omit the delimiter; include it explicitly.
		end = lineEnd(source, end)
	}
	return parsed, start, end, start < end && end <= len(source)
}

func semanticTableRow(row gast.Node, source []byte, min, max *int) ([]tableCell, bool) {
	cells := make([]tableCell, 0, row.ChildCount())
	for node := row.FirstChild(); node != nil; node = node.NextSibling() {
		cellNode, ok := node.(*extast.TableCell)
		if !ok {
			return nil, false
		}
		for i := 0; i < cellNode.Lines().Len(); i++ {
			segment := cellNode.Lines().At(i)
			if segment.Start < *min {
				*min = segment.Start
			}
			if segment.Stop > *max {
				*max = segment.Stop
			}
		}
		cells = append(cells, semanticTableCell(cellNode, source))
	}
	return cells, true
}

func semanticTableCell(node gast.Node, source []byte) tableCell {
	runs := semanticRuns(node, source, tableRun{})
	var plain strings.Builder
	for _, run := range runs {
		plain.WriteString(run.text)
	}
	return tableCell{raw: strings.TrimSpace(plain.String()), runs: runs}
}

func semanticRuns(node gast.Node, source []byte, state tableRun) []tableRun {
	var out []tableRun
	var visit func(gast.Node, tableRun)
	visit = func(current gast.Node, style tableRun) {
		switch n := current.(type) {
		case *gast.Text:
			text := string(n.Value(source))
			if !n.IsRaw() {
				text = string(util.UnescapePunctuations([]byte(text)))
			}
			appendTableRun(&out, style, html.UnescapeString(text))
			return
		case *gast.String:
			text := string(n.Value)
			if !n.IsRaw() {
				text = string(util.UnescapePunctuations([]byte(text)))
			}
			appendTableRun(&out, style, html.UnescapeString(text))
			return
		case *gast.Emphasis:
			if n.Level == 1 {
				style.italic = true
			} else {
				style.bold = true
			}
		case *gast.CodeSpan:
			style.code = true
		case *gast.Link:
			style.linkURL = safeTableLink(string(n.Destination))
		case *gast.AutoLink:
			destination := string(n.URL(source))
			if n.AutoLinkType == gast.AutoLinkEmail {
				destination = "mailto:" + destination
			}
			style.linkURL = safeTableLink(destination)
			appendTableRun(&out, style, html.UnescapeString(string(n.Label(source))))
			return
		case *gast.Image:
			style.image = true
			style.linkURL = safeTableLink(string(n.Destination))
		case *extast.Strikethrough:
			style.strike = true
		case *gast.RawHTML:
			appendTableRun(&out, style, html.UnescapeString(string(n.Segments.Value(source))))
			return
		}
		for child := current.FirstChild(); child != nil; child = child.NextSibling() {
			visit(child, style)
		}
	}
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		visit(child, state)
	}
	return out
}

func appendTableRun(runs *[]tableRun, style tableRun, text string) {
	text = sanitizeTableText(text)
	if text == "" {
		return
	}
	style.text = text
	if len(*runs) > 0 {
		last := &(*runs)[len(*runs)-1]
		if last.bold == style.bold && last.italic == style.italic && last.code == style.code &&
			last.strike == style.strike && last.image == style.image && last.linkURL == style.linkURL {
			last.text += text
			return
		}
	}
	*runs = append(*runs, style)
}

// sanitizeTableText prevents Markdown entity decoding or raw source bytes from
// becoming terminal control sequences. Semantic text remains visible, while
// SGR and OSC 8 are introduced only by renderTableCell from trusted metadata.
func sanitizeTableText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return unicode.ReplacementChar
		}
		return r
	}, text)
}

func safeTableLink(destination string) string {
	if !utf8.ValidString(destination) {
		return ""
	}
	for _, r := range destination {
		if unicode.IsControl(r) || r == '\\' { // OSC terminator/control injection.
			return ""
		}
	}
	return destination
}

func renderTableCell(cell tableCell, theme markdownTheme) string {
	if len(cell.runs) == 0 {
		return cell.raw
	}
	var out strings.Builder
	for _, run := range cell.runs {
		text := run.text
		if run.code {
			text = termenv.String(text).Foreground(theme.colorProfile().Color(theme.sky)).Background(theme.colorProfile().Color(theme.element)).String()
		}
		if run.image {
			text = termenv.String(text).Foreground(theme.colorProfile().Color("212")).Underline().String()
		} else if run.linkURL != "" {
			text = termenv.String(text).Foreground(theme.colorProfile().Color("35")).Bold().Underline().String()
		}
		if destination := safeTableLink(run.linkURL); destination != "" {
			text = "\x1b]8;;" + destination + "\x1b\\" + text + "\x1b]8;;\x1b\\"
		}
		if run.bold {
			text = "\x1b[1m" + text + "\x1b[22m"
		}
		if run.italic {
			text = "\x1b[3m" + text + "\x1b[23m"
		}
		if run.strike {
			text = "\x1b[9m" + text + "\x1b[29m"
		}
		out.WriteString(text)
	}
	return out.String()
}

func lineStart(source []byte, offset int) int {
	for offset > 0 && source[offset-1] != '\n' {
		offset--
	}
	return offset
}

func lineEnd(source []byte, offset int) int {
	for offset < len(source) && source[offset] != '\n' {
		offset++
	}
	if offset < len(source) {
		offset++
	}
	return offset
}

func validTableRanges(ranges []tableSourceRange, sourceLen int) bool {
	previous := 0
	for _, r := range ranges {
		if r.start < previous || r.start < 0 || r.end > sourceLen || r.start >= r.end {
			return false
		}
		previous = r.end
	}
	return true
}

func tableParseView(content string) []byte {
	view := []byte(content)
	for lineStart := 0; lineStart < len(view); {
		lineEnd := lineStart
		for lineEnd < len(view) && view[lineEnd] != '\n' {
			lineEnd++
		}
		maskCodeSpanPipes(view, lineStart, lineEnd)
		lineStart = lineEnd + 1
	}
	return view
}

func maskCodeSpanPipes(view []byte, start, end int) {
	for i := start; i < end; {
		if view[i] != '`' || punctuationEscapedAt(view, start, i) {
			i++
			continue
		}
		open := i
		for i < end && view[i] == '`' {
			i++
		}
		run := i - open
		close := -1
		masked := make([]int, 0)
		for j := i; j < end; j++ {
			if view[j] == '|' && !punctuationEscapedAt(view, start, j) {
				view[j] = '!'
				masked = append(masked, j)
			}
			// Backslashes are literal after a code-span delimiter opens, so an
			// equal-length run closes the span even when preceded by '\'.
			if view[j] != '`' {
				continue
			}
			k := j
			for k < end && view[k] == '`' {
				k++
			}
			if k-j == run {
				close = k
				break
			}
			j = k - 1
		}
		if close < 0 { // unmatched: restore this candidate line and leave Goldmark unchanged.
			for _, pos := range masked {
				view[pos] = '|'
			}
			continue
		}
		i = close
	}
}

func punctuationEscapedAt(view []byte, start, index int) bool {
	backslashes := 0
	for index > start && view[index-1] == '\\' {
		backslashes++
		index--
	}
	return backslashes%2 == 1
}
