package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

// The manager retains navigation identity only. Payloads stay in the live
// thread draft and draft-media table, never in another restorable snapshot.
type composerContextManager struct {
	threadID   string
	selectedID string
	cursor     int
}

func (a *App) composerContextEditable() bool {
	return !a.composerInputBlocked() && a.composerImageLoadPending == nil &&
		!a.externalEditorActive && !a.historySearch.Active && a.inputMode == InputNormal &&
		a.activeThreadViewMode() == engine.ThreadModeLiveAttach
}

func (a *App) openComposerContextManager() {
	if _, active := a.activeDialogState(); active {
		return
	}
	if (a.state != StateChat && a.state != StateWelcome) || !a.composerContextEditable() {
		a.showNotification("Context management requires an editable ordinary draft", NotifyWarning)
		return
	}
	a.contextManager = composerContextManager{threadID: a.activeThreadViewID()}
	items, valid := a.composerContextItems()
	if !valid {
		a.showNotification("Attached context has inconsistent references", NotifyWarning)
		return
	}
	cursor := a.captureComposerUndoEntry().CursorOffset
	for _, item := range items {
		if cursor >= item.Start && cursor <= item.End {
			a.contextManager.selectedID = item.ID
		}
	}
	a.dismissMentionHints()
	a.cancelArgumentCompletion()
	a.pushDialog(StateContextManager)
}

func (a *App) composerContextItems() ([]threadComposerElement, bool) {
	if a.contextManager.threadID != a.activeThreadViewID() || len(a.composerElements) > maxThreadComposerElements {
		return nil, false
	}
	items := append([]threadComposerElement(nil), a.composerElements...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Start < items[j].Start })
	runes := []rune(a.textarea.Value())
	seen := make(map[string]bool, len(items))
	end := 0
	for _, item := range items {
		if item.ID == "" || seen[item.ID] || item.Start < end || !composerElementMatches(runes, item) {
			return nil, false
		}
		seen[item.ID] = true
		end = item.End
	}
	return items, true
}

func (m *composerContextManager) selection(items []threadComposerElement) int {
	for index, item := range items {
		if item.ID == m.selectedID {
			m.cursor = index
			return index
		}
	}
	m.cursor = max(0, min(m.cursor, len(items)-1))
	if len(items) > 0 {
		m.selectedID = items[m.cursor].ID
	}
	return m.cursor
}

func (a *App) handleComposerContextManagerKey(msg tea.KeyPressMsg) {
	if msg.String() == "esc" {
		a.popDialog(StateContextManager)
		return
	}
	items, valid := a.composerContextItems()
	if !valid || !a.composerContextEditable() {
		a.removeDialog(StateContextManager)
		a.showNotification("Context draft changed; reopen the context manager", NotifyWarning)
		return
	}
	if a.keybindResolver != nil {
		resolution := a.keybindResolver.ResolveEvent(msg, keybindings.ContextChat)
		if resolution.Kind == keybindings.ResolutionMatch && resolution.Action == keybindings.ActionChatUndo {
			a.undoComposerEdit()
			return
		}
		if resolution.Kind == keybindings.ResolutionChordStarted || resolution.Kind == keybindings.ResolutionChordCancelled {
			return
		}
	}
	index := a.contextManager.selection(items)
	switch msg.String() {
	case "up", "k", "ctrl+p":
		index = max(0, index-1)
	case "down", "j", "ctrl+n":
		index = min(len(items)-1, index+1)
	case "home":
		index = 0
	case "end":
		index = len(items) - 1
	case "delete", "backspace":
		if len(items) > 0 {
			a.removeManagedComposerContext(items[index].ID)
		}
		return
	case "enter":
		if len(items) > 0 {
			a.setTextareaRuneCursor(items[index].End)
		}
		a.popDialog(StateContextManager)
		a.focus = FocusEditor
		return
	}
	if index >= 0 && index < len(items) {
		a.contextManager.cursor = index
		a.contextManager.selectedID = items[index].ID
	}
}

func (a *App) removeManagedComposerContext(id string) bool {
	items, valid := a.composerContextItems()
	state, active := a.activeDialogState()
	if !valid || !active || state != StateContextManager || !a.composerContextEditable() {
		return false
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if item.Kind != composerElementKindImage && item.Kind != composerElementKindPaste && item.Data == "" {
			a.showNotification("Wait for the selected content to finish loading", NotifyWarning)
			return false
		}
		before := a.captureComposerUndoEntry()
		runes := []rune(before.Text)
		end := item.End
		if end < len(runes) && runes[end] == ' ' {
			occupied := false
			for _, other := range items {
				occupied = occupied || other.ID != id && other.Start == end
			}
			if !occupied {
				end++
			}
		}
		updated := string(runes[:item.Start]) + string(runes[end:])
		a.textarea.SetValue(updated)
		// Apply the known edit range directly. Prefix/suffix diffing cannot
		// distinguish adjacent references with identical visible labels.
		a.composerElements = nil
		for _, other := range items {
			if other.ID == id {
				continue
			}
			if other.Start >= end {
				other.Start -= end - item.Start
				other.End -= end - item.Start
			}
			a.composerElements = append(a.composerElements, other)
		}
		cursor := before.CursorOffset
		if cursor >= end {
			cursor -= end - item.Start
		} else if cursor > item.Start {
			cursor = item.Start
		}
		a.setTextareaRuneCursor(cursor)
		a.markComposerChanged()
		a.recordComposerUndo(before)
		a.dismissMentionHints()
		a.syncInputModeFromText()
		a.updateLayout()
		return true
	}
	return false
}

func composerContextPrefix(value string, limit int) (string, bool) {
	end := min(len(value), limit)
	for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], end < len(value)
}

func composerContextLabel(value string) string {
	value, clipped := composerContextPrefix(value, 256)
	value = composerContextPreviewLiteral(value)
	if clipped {
		value += "..."
	}
	return value
}

func composerContextKindLabel(item threadComposerElement) string {
	switch item.Kind {
	case composerElementKindFile:
		if item.StartLine > 0 {
			return "File lines"
		}
		return "File"
	case composerElementKindTerminalOutput:
		return "Output"
	case composerElementKindSkill:
		return "Skill"
	case composerElementKindMCPResource:
		return "MCP resource"
	case composerElementKindPaste:
		return "Pasted text"
	case composerElementKindImage:
		return "Image"
	default:
		return "Attachment"
	}
}

func (a *App) composerContextDetail(item threadComposerElement, width, maxLines int) []string {
	profile := a.renderEnvironment.normalized().profile
	body := item.Data
	if item.Kind == composerElementKindPaste {
		body = item.Value
	}
	size := len(body)
	if item.Kind == composerElementKindImage {
		image := a.draftMedia[item.ID]
		if image == nil {
			return []string{"Image content unavailable"}
		}
		return []string{modalEllipsize(profile, fmt.Sprintf("%s | %s | %d B", composerContextLabel(item.Name), composerContextLabel(image.MIMEType), len(image.Data)), width, 0, "..."), "Image preview: metadata only"}
	}
	if body == "" {
		return []string{"Loading attached content..."}
	}
	meta := fmt.Sprintf("%d B | %s", size, composerContextLabel(item.Value))
	if item.Kind == composerElementKindPaste {
		meta = fmt.Sprintf("%d B | pasted text", size)
	}
	rows := []string{modalEllipsize(profile, meta, width, 0, "...")}
	prefix, clipped := composerContextPrefix(body, 1024)
	lines := strings.Split(strings.TrimSuffix(prefix, "\n"), "\n")
	clipped = clipped || len(lines) > maxLines
	for i, line := range lines[:min(len(lines), maxLines)] {
		lineNumber := i + 1
		if item.StartLine > 0 {
			lineNumber += item.StartLine - 1
		}
		line = fmt.Sprintf("%d | %s", lineNumber, composerContextPreviewLiteral(strings.TrimSuffix(line, "\r")))
		clipped = clipped || profile.measure(line, 0) > width
		rows = append(rows, modalEllipsize(profile, line, width, 0, "..."))
	}
	status := "Full text attached"
	if clipped {
		status = "Preview clipped; full text kept"
	}
	return append(rows, modalEllipsize(profile, status, width, 0, "..."))
}

func (a *App) composerContextManagerOverlay(base string, width, height int) string {
	profile := a.renderEnvironment.normalized().profile
	items, valid := a.composerContextItems()
	innerWidth := max(1, min(76, width-6))
	parts := []string{a.styles.DialogTitle.Render(fmt.Sprintf("Attached context (%d)", len(items)))}
	index := a.contextManager.selection(items)
	if !valid {
		parts = append(parts, "Draft changed; close and reopen")
	} else if len(items) == 0 {
		parts = append(parts, "No attached context")
	} else {
		visible := max(1, min(6, height-11))
		start := max(0, index-visible+1)
		end := min(len(items), start+visible)
		for i := start; i < end; i++ {
			row := modalEllipsize(profile, fmt.Sprintf("%d. %s  [%s]", i+1, composerContextLabel(items[i].Label), composerContextKindLabel(items[i])), innerWidth, 0, "...")
			if i == index {
				row = a.styles.Selected.Render(row)
			}
			parts = append(parts, row)
		}
		parts = append(parts, fmt.Sprintf("%d-%d of %d", start+1, end, len(items)))
		if height >= 12 {
			parts = append(parts, a.composerContextDetail(items[index], innerWidth, min(3, max(0, height-11-visible)))...)
		}
	}
	parts = append(parts, "Up/Down select | Del remove", "Enter locate | Esc close", keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatUndo, "ctrl+z"), "undo"))
	for i, line := range parts {
		parts[i] = modalProjectLine(profile, line, innerWidth, 0)
	}
	dialog := contentRenderStyleWidth(profile, a.styles.DialogBorder, innerWidth+2, strings.Join(parts, "\n"))
	view, _ := modalCenteredOverlay(profile, base, dialog, width, height)
	return view
}
