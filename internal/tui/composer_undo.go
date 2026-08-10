package tui

const maxComposerUndoEntries = maxTextEditorUndoEntries

type composerUndoEntry struct {
	Text         string
	Elements     []threadComposerElement
	CursorOffset int
}

func (a *App) captureComposerUndoEntry() composerUndoEntry {
	if a == nil {
		return composerUndoEntry{}
	}
	snapshot := captureTextEditorSnapshot(a.textarea)
	return composerUndoEntry{
		Text:         snapshot.Text,
		Elements:     cloneThreadComposerElements(a.composerElements),
		CursorOffset: snapshot.CursorOffset,
	}
}

func (a *App) recordComposerUndo(before composerUndoEntry) {
	if a == nil || before.Text == a.textarea.Value() {
		return
	}
	if count := len(a.composerUndo); count > 0 {
		last := a.composerUndo[count-1]
		if last.Text == before.Text && last.CursorOffset == before.CursorOffset {
			return
		}
	}
	a.composerUndo = append(a.composerUndo, before)
	if len(a.composerUndo) > maxTextEditorUndoEntries {
		a.composerUndo = append(
			[]composerUndoEntry(nil),
			a.composerUndo[len(a.composerUndo)-maxTextEditorUndoEntries:]...,
		)
	}
	a.gcDraftMedia()
}

func (a *App) undoComposerEdit() {
	if a == nil || len(a.composerUndo) == 0 {
		a.showToast("Nothing to undo")
		return
	}
	index := len(a.composerUndo) - 1
	entry := a.composerUndo[index]
	a.composerUndo = a.composerUndo[:index]
	a.applyComposerUndoEntry(entry)
	a.dismissMentionHints()
	a.syncInputModeFromText()
	a.updateLayout()
}

func (a *App) applyComposerUndoEntry(entry composerUndoEntry) {
	a.textarea.SetValue(entry.Text)
	a.composerElements = cloneThreadComposerElements(entry.Elements)
	a.setTextareaRuneCursor(entry.CursorOffset)
	a.markComposerChanged()
	a.gcDraftMedia()
}

func cloneComposerUndoEntries(entries []composerUndoEntry) []composerUndoEntry {
	if len(entries) > maxTextEditorUndoEntries {
		entries = entries[len(entries)-maxTextEditorUndoEntries:]
	}
	cloned := append([]composerUndoEntry(nil), entries...)
	for i := range cloned {
		cloned[i].Elements = cloneThreadComposerElements(cloned[i].Elements)
	}
	return cloned
}
