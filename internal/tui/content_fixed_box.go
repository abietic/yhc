package tui

import (
	"image/color"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

type contentFixedBoxProjection struct {
	rendered string
	rows     []string
	outer    layoutRect
	inner    layoutRect
}

type contentFixedBoxBorder struct {
	style                    lipgloss.Style
	border                   lipgloss.Border
	top, right, bottom, left bool
	topHeight, rightWidth    int
	bottomHeight, leftWidth  int
}

func contentProjectFixedBox(
	profile DisplayCellProfile,
	style lipgloss.Style,
	bodyWidth, requestedHeight int,
	content string,
) contentFixedBoxProjection {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	if bodyWidth <= 0 || requestedHeight < 0 {
		return contentFixedBoxProjection{}
	}

	border := contentFixedBoxBorderForStyle(style)
	if requestedHeight > 0 {
		border.topHeight = min(border.topHeight, requestedHeight)
		border.bottomHeight = min(
			border.bottomHeight,
			requestedHeight-border.topHeight,
		)
	}
	paddingTop, paddingRight, paddingBottom, paddingLeft := style.GetPadding()
	paddingTop = max(paddingTop, 0)
	paddingRight = max(paddingRight, 0)
	paddingBottom = max(paddingBottom, 0)
	paddingLeft = max(paddingLeft, 0)
	innerWidth := max(bodyWidth-paddingLeft-paddingRight, 0)
	innerOrigin := border.leftWidth + paddingLeft

	contentRows := []string{""}
	if innerWidth > 0 {
		contentRows = contentWrapSemanticLines(
			profile,
			content,
			innerWidth,
			innerOrigin,
		)
		align := contentFixedHorizontalAlign(style.GetAlignHorizontal())
		for index, row := range contentRows {
			row = profile.padAligned(row, innerWidth, align, innerOrigin)
			contentRows[index] = fitLayoutColumnLine(
				profile,
				row,
				innerWidth,
				innerOrigin,
			)
		}
	}

	paddingRune := string(style.GetPaddingChar())
	leftPadding := contentFillPattern(
		profile,
		paddingRune,
		paddingLeft,
		border.leftWidth,
	)
	rightPaddingOrigin := border.leftWidth + paddingLeft + innerWidth
	rightPadding := contentFillPattern(
		profile,
		paddingRune,
		paddingRight,
		rightPaddingOrigin,
	)
	bodyHeight := paddingTop + len(contentRows) + paddingBottom
	emittedPaddingTop := paddingTop
	emittedPaddingBottom := paddingBottom
	innerHeight := len(contentRows)
	if requestedHeight > 0 {
		bodyHeight = requestedHeight - border.topHeight - border.bottomHeight
		emittedPaddingTop = min(paddingTop, bodyHeight)
		emittedPaddingBottom = min(
			paddingBottom,
			bodyHeight-emittedPaddingTop,
		)
		innerHeight = bodyHeight - emittedPaddingTop - emittedPaddingBottom
		contentRows = contentFitFixedBoxHeight(
			contentRows,
			innerHeight,
			style.GetAlignVertical(),
			contentFillPattern(profile, " ", innerWidth, innerOrigin),
		)
	}

	bodyRows := make([]string, 0, bodyHeight)
	blankBody := contentFillPattern(profile, paddingRune, bodyWidth, border.leftWidth)
	for range emittedPaddingTop {
		bodyRows = append(bodyRows, blankBody)
	}
	for _, row := range contentRows {
		row = leftPadding + row + rightPadding
		bodyRows = append(bodyRows, fitLayoutColumnLine(
			profile,
			row,
			bodyWidth,
			border.leftWidth,
		))
	}
	for range emittedPaddingBottom {
		bodyRows = append(bodyRows, blankBody)
	}

	paint := contentFixedBoxPaintStyle(style)
	paintedRows := make([]string, len(bodyRows))
	for index, row := range bodyRows {
		paintedRows[index] = paint.Render(row)
	}
	rows := contentApplyFixedBoxBorder(
		profile,
		border,
		bodyWidth,
		paintedRows,
	)
	rows = profile.balanceControlLines(rows)
	projection := contentFixedBoxProjection{
		rows: append([]string(nil), rows...),
		outer: layoutRect{
			Width:  bodyWidth + border.leftWidth + border.rightWidth,
			Height: len(rows),
		},
		inner: layoutRect{
			X:      innerOrigin,
			Y:      border.topHeight + emittedPaddingTop,
			Width:  innerWidth,
			Height: innerHeight,
		},
	}
	projection.rendered = strings.Join(projection.rows, "\n")
	return projection
}

func contentFixedBoxBorderForStyle(style lipgloss.Style) contentFixedBoxBorder {
	border, top, right, bottom, left := style.GetBorder()
	if border != (lipgloss.Border{}) && !top && !right && !bottom && !left {
		top, right, bottom, left = true, true, true, true
	}
	result := contentFixedBoxBorder{
		style: style, border: border,
		top: top, right: right, bottom: bottom, left: left,
	}
	if top {
		result.topHeight = max(style.GetBorderTopSize(), 1)
	}
	if right {
		result.rightWidth = max(style.GetBorderRightSize(), 1)
	}
	if bottom {
		result.bottomHeight = max(style.GetBorderBottomSize(), 1)
	}
	if left {
		result.leftWidth = max(style.GetBorderLeftSize(), 1)
	}
	return result
}

func contentFixedBoxPaintStyle(style lipgloss.Style) lipgloss.Style {
	return style.
		UnsetWidth().
		UnsetHeight().
		UnsetMaxWidth().
		UnsetMaxHeight().
		UnsetMargins().
		UnsetPadding().
		UnsetAlign().
		UnsetAlignHorizontal().
		UnsetAlignVertical().
		UnsetBorderStyle().
		UnsetBorderTop().
		UnsetBorderRight().
		UnsetBorderBottom().
		UnsetBorderLeft().
		UnsetBorderForeground().
		UnsetBorderBackground().
		UnsetBorderForegroundBlend().
		UnsetBorderForegroundBlendOffset().
		UnsetInline().
		UnsetTabWidth().
		UnsetTransform()
}

func contentFixedHorizontalAlign(position lipgloss.Position) string {
	switch position {
	case lipgloss.Right:
		return "right"
	case lipgloss.Center:
		return "center"
	default:
		return "left"
	}
}

func contentFitFixedBoxHeight(
	rows []string,
	height int,
	align lipgloss.Position,
	blank string,
) []string {
	if height <= 0 {
		return nil
	}
	if len(rows) > height {
		start := 0
		switch align {
		case lipgloss.Bottom:
			start = len(rows) - height
		case lipgloss.Center:
			start = (len(rows) - height) / 2
		}
		return append([]string(nil), rows[start:start+height]...)
	}
	missing := height - len(rows)
	top := 0
	switch align {
	case lipgloss.Bottom:
		top = missing
	case lipgloss.Center:
		top = missing / 2
	}
	result := make([]string, 0, height)
	for range top {
		result = append(result, blank)
	}
	result = append(result, rows...)
	for len(result) < height {
		result = append(result, blank)
	}
	return result
}

func contentApplyFixedBoxBorder(
	profile DisplayCellProfile,
	border contentFixedBoxBorder,
	bodyWidth int,
	bodyRows []string,
) []string {
	rows := make([]string, 0, border.topHeight+len(bodyRows)+border.bottomHeight)
	for range border.topHeight {
		rows = append(rows, contentFixedHorizontalBorderRow(
			profile,
			border,
			bodyWidth,
			true,
		))
	}
	for index, row := range bodyRows {
		left := ""
		if border.left {
			left = contentFillPattern(
				profile,
				contentVerticalBorderPiece(profile, border.border.Left, index),
				border.leftWidth,
				0,
			)
			left = contentPaintBorder(
				left,
				border.style.GetBorderLeftForeground(),
				border.style.GetBorderLeftBackground(),
			)
		}
		right := ""
		if border.right {
			right = contentFillPattern(
				profile,
				contentVerticalBorderPiece(profile, border.border.Right, index),
				border.rightWidth,
				border.leftWidth+bodyWidth,
			)
			right = contentPaintBorder(
				right,
				border.style.GetBorderRightForeground(),
				border.style.GetBorderRightBackground(),
			)
		}
		rows = append(rows, left+row+right)
	}
	for range border.bottomHeight {
		rows = append(rows, contentFixedHorizontalBorderRow(
			profile,
			border,
			bodyWidth,
			false,
		))
	}
	return rows
}

func contentFixedHorizontalBorderRow(
	profile DisplayCellProfile,
	border contentFixedBoxBorder,
	bodyWidth int,
	top bool,
) string {
	middle := border.border.Bottom
	leftCorner := border.border.BottomLeft
	rightCorner := border.border.BottomRight
	foreground := border.style.GetBorderBottomForeground()
	background := border.style.GetBorderBottomBackground()
	if top {
		middle = border.border.Top
		leftCorner = border.border.TopLeft
		rightCorner = border.border.TopRight
		foreground = border.style.GetBorderTopForeground()
		background = border.style.GetBorderTopBackground()
	}
	left := ""
	if border.left {
		left = contentFillPattern(
			profile,
			contentFirstBorderPiece(profile, leftCorner),
			border.leftWidth,
			0,
		)
	}
	center := contentFillPattern(profile, middle, bodyWidth, border.leftWidth)
	right := ""
	if border.right {
		right = contentFillPattern(
			profile,
			contentFirstBorderPiece(profile, rightCorner),
			border.rightWidth,
			border.leftWidth+bodyWidth,
		)
	}
	return contentPaintBorder(left+center+right, foreground, background)
}

func contentFirstBorderPiece(profile DisplayCellProfile, value string) string {
	clusters := profile.clusters(value, 0)
	if len(clusters) == 0 {
		return " "
	}
	return clusters[0].text
}

func contentVerticalBorderPiece(
	profile DisplayCellProfile,
	value string,
	row int,
) string {
	clusters := profile.clusters(value, 0)
	if len(clusters) == 0 {
		return " "
	}
	return clusters[row%len(clusters)].text
}

func contentPaintBorder(value string, foreground, background color.Color) string {
	if value == "" {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(foreground).
		Background(background).
		Render(value)
}

func contentFillPattern(
	profile DisplayCellProfile,
	pattern string,
	width, startColumn int,
) string {
	if width <= 0 {
		return ""
	}
	if pattern == "" {
		pattern = " "
	}
	var result strings.Builder
	for profile.measure(result.String(), startColumn) < width {
		before := profile.measure(result.String(), startColumn)
		result.WriteString(pattern)
		if profile.measure(result.String(), startColumn) <= before {
			break
		}
	}
	return fitLayoutColumnLine(profile, result.String(), width, startColumn)
}

// contentWrapSemanticLines wraps with the selected profile while retaining tab
// source bytes until horizontal alignment selects their physical origin.
func contentWrapSemanticLines(
	profile DisplayCellProfile,
	text string,
	width int,
	startColumn int,
) []string {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	if width <= 0 {
		return []string{""}
	}

	var (
		lines      []string
		line       strings.Builder
		lineWidth  int
		word       strings.Builder
		wordWidth  int
		space      strings.Builder
		spaceWidth int
	)
	flushSpace := func() {
		if space.Len() == 0 {
			return
		}
		line.WriteString(space.String())
		lineWidth += spaceWidth
		space.Reset()
		spaceWidth = 0
	}
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		flushSpace()
		line.WriteString(word.String())
		lineWidth += wordWidth
		word.Reset()
		wordWidth = 0
	}
	finishLine := func() {
		lines = append(lines, line.String())
		line.Reset()
		lineWidth = 0
		space.Reset()
		spaceWidth = 0
	}

	for _, cluster := range profile.clusters(text, startColumn) {
		source := cluster.source
		if source == "\n" {
			if word.Len() == 0 && lineWidth+spaceWidth <= width {
				flushSpace()
			}
			flushWord()
			finishLine()
			continue
		}

		column := startColumn + lineWidth + spaceWidth + wordWidth
		retained := cluster.text
		cells := cluster.cells
		control := cluster.control
		if source == "\t" {
			retained = source
			cells = profile.tabCells(column)
		}
		// A fixed box must never widen for a single cluster that cannot fit.
		// Keep the omission at a grapheme boundary; zero-width SGR/OSC8
		// controls are retained and balanced below.
		if !control && cells > width {
			continue
		}
		r, _ := utf8.DecodeRuneInString(source)
		isSpace := !control && r != utf8.RuneError && unicode.IsSpace(r) && r != '\u00a0'
		if isSpace {
			flushWord()
			space.WriteString(retained)
			spaceWidth += cells
			continue
		}
		if !control && source == "-" {
			flushSpace()
			if lineWidth+wordWidth+cells > width {
				word.WriteString(retained)
				wordWidth += cells
			} else {
				flushWord()
				line.WriteString(retained)
				lineWidth += cells
			}
			continue
		}
		if wordWidth+cells > width {
			flushWord()
		}
		word.WriteString(retained)
		wordWidth += cells
		if lineWidth+spaceWidth+wordWidth > width {
			finishLine()
		}
		if wordWidth == width {
			flushWord()
		}
	}
	flushWord()
	if line.Len() > 0 || space.Len() > 0 || len(lines) == 0 {
		flushSpace()
		lines = append(lines, line.String())
	}
	return profile.balanceControlLines(lines)
}
