package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/internal/tui/attachments"
)

// Exact indexed filenames win over range syntax, preserving literal '#' names.
func (a *App) fileMentionQuery(query string) (string, attachments.LineRange, error) {
	for _, path := range a.mentionIndex.files {
		if strings.EqualFold(query, path) {
			return query, attachments.LineRange{}, nil
		}
	}
	index := strings.LastIndexByte(query, '#')
	if index < 0 {
		return query, attachments.LineRange{}, nil
	}
	lines, err := attachments.ParseLineRange(query[index:])
	if err != nil {
		// A partially typed literal filename containing '#' still completes.
		for _, path := range a.mentionIndex.files {
			if mentionMatches(query, path) {
				return query, attachments.LineRange{}, nil
			}
		}
		return "", attachments.LineRange{}, err
	}
	if index == 0 {
		return "", attachments.LineRange{}, fmt.Errorf("file range needs a path")
	}
	return query[:index], lines, nil
}

func (a *App) renderFileMentionRangeError() string {
	if a.focus != FocusEditor || a.inputMode != InputNormal {
		return ""
	}
	_, _, query, active := a.activeMentionToken()
	if !active || strings.HasPrefix(query, "skill:") || strings.HasPrefix(query, "mcp:") {
		return ""
	}
	query = strings.TrimPrefix(strings.ToLower(query), "file:")
	if _, _, err := a.fileMentionQuery(query); err == nil {
		return ""
	}
	profile := a.renderEnvironment.normalized().profile
	return strings.Join(profile.wrap("Invalid line range: use #N or #N-M (positive, inclusive)", a.hintContentWidth(), true), "\n")
}

func loadFileRangeMention(elementID, path string, lines attachments.LineRange) tea.Cmd {
	return func() tea.Msg {
		excerpt, err := attachments.ReadFileRange(path, lines)
		return composerMentionPayloadMsg{
			elementID: elementID, data: excerpt.Content, mimeType: "text/plain",
			totalLines: excerpt.TotalLines, err: err,
		}
	}
}

func (a *App) composerFileRangeAtCursor() (threadComposerElement, int, bool) {
	value := a.textarea.Value()
	info := a.textarea.LineInfo()
	cursor := textareaCursorRuneOffset(value, a.textarea.Line(), info.StartColumn+info.ColumnOffset)
	runes := []rune(value)
	for _, element := range a.composerElements {
		if element.Kind != composerElementKindFile || element.StartLine == 0 || !composerElementMatches(runes, element) {
			continue
		}
		if cursor >= element.Start && cursor <= element.End ||
			cursor == element.End+1 && element.End < len(runes) && runes[element.End] == ' ' {
			return element, cursor, true
		}
	}
	return threadComposerElement{}, cursor, false
}

func (a *App) removeComposerFileRange() bool {
	element, cursor, found := a.composerFileRangeAtCursor()
	if !found || cursor < element.End {
		return false
	}
	before := a.captureComposerUndoEntry()
	runes := []rune(before.Text)
	updated := string(runes[:element.Start]) + string(runes[cursor:])
	a.textarea.SetValue(updated)
	a.reconcileComposerElements(before.Text, updated)
	a.setTextareaRuneCursor(element.Start)
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.dismissMentionHints()
	a.syncInputModeFromText()
	return true
}

func (a *App) renderComposerFileRangePreview() string {
	if a.focus != FocusEditor || a.inputMode != InputNormal || a.suppressingHistoryHints() || a.composerInputBlocked() {
		return ""
	}
	if _, active := a.activeDialogState(); active {
		return ""
	}
	element, _, found := a.composerFileRangeAtCursor()
	if !found {
		return ""
	}
	profile := a.renderEnvironment.normalized().profile
	width := a.hintContentWidth()
	header := fmt.Sprintf("lines %d-%d of %d | %d B", element.StartLine, element.EndLine, element.TotalLines, len(element.Data))
	if element.Data == "" {
		header = fmt.Sprintf("Loading lines %d-%d...", element.StartLine, element.EndLine)
	}
	rows := profile.wrap(header, width, true)
	if element.Data != "" {
		end := min(len(element.Data), 1024)
		for end < len(element.Data) && end > 0 && !utf8.RuneStart(element.Data[end]) {
			end--
		}
		preview := strings.Split(strings.TrimSuffix(element.Data[:end], "\n"), "\n")
		clipped := end < len(element.Data) || len(preview) > 3
		for index, line := range preview[:min(len(preview), 3)] {
			line = fmt.Sprintf("%d | %s", element.StartLine+index, fileRangePreviewLiteral(strings.TrimSuffix(line, "\r")))
			clipped = clipped || profile.measure(line, 0) > width
			rows = append(rows, contentEllipsize(profile, line, width, 0, "..."))
		}
		status := "Full selected range attached"
		if clipped {
			status = "Preview clipped; full range attached"
		}
		rows = append(rows, profile.wrap(status, width, true)...)
	}
	rows = append(rows, profile.wrap("Backspace at reference end removes", width, true)...)
	return strings.Join(rows, "\n")
}

func fileRangePreviewLiteral(value string) string {
	var result strings.Builder
	for _, character := range value {
		if character == '\t' || isUnsafeDisplayCellRune(character) {
			fmt.Fprintf(&result, "\\u%04x", character)
		} else {
			result.WriteRune(character)
		}
	}
	return result.String()
}
