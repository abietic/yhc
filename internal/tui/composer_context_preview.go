package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func (a *App) composerContextAtCursor() (threadComposerElement, int, bool) {
	value := a.textarea.Value()
	info := a.textarea.LineInfo()
	cursor := textareaCursorRuneOffset(value, a.textarea.Line(), info.StartColumn+info.ColumnOffset)
	runes := []rune(value)
	for _, element := range a.composerElements {
		if (element.Kind != composerElementKindGitDiff && element.Kind != composerElementKindTerminalOutput && (element.Kind != composerElementKindFile || element.StartLine == 0)) || !composerElementMatches(runes, element) {
			continue
		}
		if cursor >= element.Start && cursor <= element.End ||
			cursor == element.End+1 && element.End < len(runes) && runes[element.End] == ' ' {
			return element, cursor, true
		}
	}
	return threadComposerElement{}, cursor, false
}

func (a *App) removeComposerContext() bool {
	element, cursor, found := a.composerContextAtCursor()
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

func (a *App) renderComposerContextPreview() string {
	if a.focus != FocusEditor || a.inputMode != InputNormal || a.suppressingHistoryHints() || a.composerInputBlocked() {
		return ""
	}
	if _, active := a.activeDialogState(); active {
		return ""
	}
	element, _, found := a.composerContextAtCursor()
	if !found {
		return ""
	}
	profile := a.renderEnvironment.normalized().profile
	width := a.hintContentWidth()
	header := fmt.Sprintf("lines %d-%d of %d | %d B", element.StartLine, element.EndLine, element.TotalLines, len(element.Data))
	if element.Kind == composerElementKindTerminalOutput {
		header = fmt.Sprintf("Selected output | %d B", len(element.Data))
	}
	if element.Kind == composerElementKindGitDiff {
		header = fmt.Sprintf("Git diff | %s | %d B", composerContextLabel(element.Name), len(element.Data))
	}
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
			lineNumber := element.StartLine + index
			if element.Kind == composerElementKindTerminalOutput || element.Kind == composerElementKindGitDiff {
				lineNumber = index + 1
			}
			line = fmt.Sprintf("%d | %s", lineNumber, composerContextPreviewLiteral(strings.TrimSuffix(line, "\r")))
			clipped = clipped || profile.measure(line, 0) > width
			rows = append(rows, contentEllipsize(profile, line, width, 0, "..."))
		}
		status := "Full selected range attached"
		if clipped {
			status = "Preview clipped; full range attached"
		}
		if element.Kind == composerElementKindTerminalOutput || element.Kind == composerElementKindGitDiff {
			status = "Full selected text attached"
			if clipped {
				status = "Preview clipped; full text attached"
			}
		}
		rows = append(rows, profile.wrap(status, width, true)...)
	}
	rows = append(rows, profile.wrap("Backspace at reference end removes", width, true)...)
	return strings.Join(rows, "\n")
}

func composerContextPreviewLiteral(value string) string {
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
