package tui

import (
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const maxTextEditorUndoEntries = 100

type textEditorSnapshot struct {
	Text         string
	CursorOffset int
}

func newBoundedTextarea(
	placeholder string,
	width, height int,
	promptWidth int,
	prompt func(line int) string,
	reducedMotion bool,
) textarea.Model {
	model := textarea.New()
	model.Placeholder = placeholder
	model.ShowLineNumbers = false
	model.CharLimit = 0
	model.SetWidth(max(1, width))
	model.SetHeight(max(1, height))
	if prompt != nil {
		model.SetPromptFunc(promptWidth, func(info textarea.PromptInfo) string {
			return prompt(info.LineNumber)
		})
	}
	styles := model.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	styles.Cursor.Blink = !reducedMotion
	model.SetStyles(styles)
	model.Focus()
	return model
}

func captureTextEditorSnapshot(model textarea.Model) textEditorSnapshot {
	line := model.LineInfo()
	return textEditorSnapshot{
		Text: model.Value(),
		CursorOffset: textareaCursorRuneOffset(
			model.Value(),
			model.Line(),
			line.StartColumn+line.ColumnOffset,
		),
	}
}

func restoreTextEditorSnapshot(
	model *textarea.Model,
	snapshot textEditorSnapshot,
) {
	if model == nil {
		return
	}
	model.SetValue(snapshot.Text)
	setTextareaRuneCursor(model, snapshot.CursorOffset)
}

func recordTextEditorUndo(
	entries []textEditorSnapshot,
	before textEditorSnapshot,
	currentText string,
) []textEditorSnapshot {
	if before.Text == currentText {
		return entries
	}
	if count := len(entries); count > 0 {
		last := entries[count-1]
		if last.Text == before.Text &&
			last.CursorOffset == before.CursorOffset {
			return entries
		}
	}
	entries = append(entries, before)
	if len(entries) > maxTextEditorUndoEntries {
		entries = append(
			[]textEditorSnapshot(nil),
			entries[len(entries)-maxTextEditorUndoEntries:]...,
		)
	}
	return entries
}

func updateTextEditor(
	model textarea.Model,
	undo []textEditorSnapshot,
	msg tea.Msg,
) (textarea.Model, []textEditorSnapshot, tea.Cmd) {
	before := captureTextEditorSnapshot(model)
	updated, cmd := model.Update(msg)
	undo = recordTextEditorUndo(undo, before, updated.Value())
	return updated, undo, cmd
}

func setTextareaRuneCursor(model *textarea.Model, position int) {
	if model == nil {
		return
	}
	runes := []rune(model.Value())
	position = max(0, min(position, len(runes)))
	line, column := 0, 0
	for _, r := range runes[:position] {
		if r == '\n' {
			line++
			column = 0
		} else {
			column++
		}
	}
	for model.Line() > line {
		model.CursorUp()
	}
	for model.Line() < line {
		model.CursorDown()
	}
	model.SetCursorColumn(column)
}
