package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// contentRenderStyleWidth preserves the internal body-width compatibility
// contract while DisplayCellProfile owns the exact rendered rectangle.
func contentRenderStyleWidth(
	profile DisplayCellProfile,
	style lipgloss.Style,
	width int,
	content string,
) string {
	return contentProjectFixedBox(profile, style, width, 0, content).rendered
}

func contentRenderStyleBox(
	profile DisplayCellProfile,
	style lipgloss.Style,
	width, height int,
	content string,
) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	return contentProjectFixedBox(profile, style, width, height, content).rendered
}

// contentProjectLine is the G11.E3 projection boundary for one content row.
// It expands origin-sensitive tabs, preserves whole extended grapheme clusters,
// and balances supported SGR/OSC controls after truncation.
func contentProjectLine(
	profile DisplayCellProfile,
	line string,
	width, startColumn int,
) string {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	if width <= 0 {
		return ""
	}
	var projected string
	if selectionAnnotationsCollide(line) &&
		profile.measure(line, startColumn) > width {
		projected = selectionTruncateAnnotatedFailClosedAt(
			profile,
			line,
			width,
			startColumn,
		)
	} else {
		projected = profile.truncateAt(line, width, startColumn)
	}
	return profile.balanceControlLines([]string{projected})[0]
}

func contentEllipsize(
	profile DisplayCellProfile,
	line string,
	width, startColumn int,
	suffix string,
) string {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	if width <= 0 {
		return ""
	}
	if profile.measure(line, startColumn) <= width {
		return contentProjectLine(profile, line, width, startColumn)
	}
	suffix = contentProjectLine(profile, suffix, width, startColumn)
	suffixWidth := profile.measure(suffix, startColumn)
	if suffixWidth >= width {
		return suffix
	}
	var head string
	if selectionAnnotationsCollide(line) {
		head = selectionTruncateAnnotatedFailClosedAt(
			profile,
			line,
			width-suffixWidth,
			startColumn,
		)
	} else {
		head = profile.truncateAt(line, width-suffixWidth, startColumn)
	}
	return contentProjectLine(profile, head+suffix, width, startColumn)
}

func contentWrapLines(
	profile DisplayCellProfile,
	text string,
	width int,
	startColumn int,
) []string {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	if width <= 0 {
		return profile.balanceControlLines([]string{text})
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

	iter := profile.options.StringGraphemes(text)
	for iter.Next() {
		source := iter.Value()
		if source == "\n" {
			if word.Len() == 0 && lineWidth+spaceWidth <= width {
				flushSpace()
			}
			flushWord()
			finishLine()
			continue
		}

		column := startColumn + lineWidth + spaceWidth + wordWidth
		projected, cells, control := profile.projectCluster(source, iter.Width(), column)
		r, _ := utf8.DecodeRuneInString(source)
		isSpace := !control && r != utf8.RuneError && unicode.IsSpace(r) && r != '\u00a0'
		if isSpace {
			flushWord()
			space.WriteString(projected)
			spaceWidth += cells
			continue
		}
		if !control && source == "-" {
			flushSpace()
			if lineWidth+wordWidth+cells > width {
				word.WriteString(projected)
				wordWidth += cells
			} else {
				flushWord()
				line.WriteString(projected)
				lineWidth += cells
			}
			continue
		}

		if wordWidth+cells > width {
			flushWord()
		}
		word.WriteString(projected)
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

func contentProjectRows(
	profile DisplayCellProfile,
	lines []string,
	width, startColumn int,
) []string {
	projected := make([]string, len(lines))
	for index, line := range lines {
		projected[index] = contentProjectLine(profile, line, width, startColumn)
	}
	return projected
}
