package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

// The manager retains transient search and selection state only. Payloads stay in the live
// thread draft and draft-media table, never in another restorable snapshot.
type composerContextManager struct {
	threadID   string
	selectedID string
	cursor     int
	query      textinput.Model
	searching  bool
	filter     int
	marked     map[string]bool
}

var composerContextTypes = [...]string{"All", "Files", "Output", "Skills", "MCP resources", "Pasted text", "Images"}

func (m *composerContextManager) filtered(items []threadComposerElement) []threadComposerElement {
	query := strings.ToLower(strings.TrimSpace(m.query.Value()))
	var result []threadComposerElement
	for _, item := range items {
		kind := 0
		switch item.Kind {
		case composerElementKindFile:
			kind = 1
		case composerElementKindTerminalOutput:
			kind = 2
		case composerElementKindSkill:
			kind = 3
		case composerElementKindMCPResource:
			kind = 4
		case composerElementKindPaste:
			kind = 5
		case composerElementKindImage:
			kind = 6
		}
		if m.filter != 0 && m.filter != kind {
			continue
		}
		metadata := []string{item.Label, item.Name, composerContextKindLabel(item)}
		// Paste.Value is the body, unlike paths and source IDs in other kinds.
		if item.Kind != composerElementKindPaste {
			metadata = append(metadata, item.Value)
		}
		matched := query == ""
		for _, value := range metadata {
			prefix, _ := composerContextPrefix(value, 4096)
			matched = matched || strings.Contains(strings.ToLower(prefix), query)
		}
		if matched {
			result = append(result, item)
		}
	}
	return result
}

func composerContextQuery(value string) string {
	// Bound work before decoding pasted input; a query is at most 128 runes.
	value, _ = composerContextPrefix(value, 1024)
	var result []rune
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			r = ' '
		case isUnsafeDisplayCellRune(r):
			continue
		}
		result = append(result, r)
		if len(result) == 128 {
			break
		}
	}
	return string(result)
}

func (a *App) updateComposerContextQuery(msg tea.Msg) tea.Cmd {
	state, active := a.activeDialogState()
	if !active || state != StateContextManager || !a.contextManager.searching {
		return nil
	}
	if paste, ok := msg.(tea.PasteMsg); ok {
		paste.Content = composerContextQuery(paste.Content)
		msg = paste
	}
	before := a.contextManager.query.Value()
	var cmd tea.Cmd
	a.contextManager.query, cmd = a.contextManager.query.Update(msg)
	value := a.contextManager.query.Value()
	if safe := composerContextQuery(value); safe != value {
		a.contextManager.query.SetValue(safe)
	}
	if before != a.contextManager.query.Value() {
		a.contextManager.marked = nil
		a.contextManager.cursor = 0
	}
	return cmd
}

func (a *App) focusComposerContextQuery(focus bool) tea.Cmd {
	a.keybindResolver.ResetPending()
	a.contextManager.searching = focus
	if focus {
		return a.contextManager.query.Focus()
	}
	a.contextManager.query.Blur()
	return nil
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
	a.contextManager.query = textinput.New()
	a.contextManager.query.Prompt = "Search: "
	a.contextManager.query.Placeholder = "name, path or source"
	a.contextManager.query.CharLimit = 128
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

func (a *App) handleComposerContextManagerKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" && !a.contextManager.searching {
		a.popDialog(StateContextManager)
		return nil
	}
	items, valid := a.composerContextItems()
	if !valid || !a.composerContextEditable() {
		a.removeDialog(StateContextManager)
		a.showNotification("Context draft changed; reopen the context manager", NotifyWarning)
		return nil
	}
	items = a.contextManager.filtered(items)
	switch msg.String() {
	case "tab", "shift+tab":
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		a.keybindResolver.ResetPending()
		a.contextManager.filter = (a.contextManager.filter + delta + len(composerContextTypes)) % len(composerContextTypes)
		a.contextManager.marked = nil
		a.contextManager.cursor = 0
		return nil
	case "ctrl+a":
		a.keybindResolver.ResetPending()
		all := len(items) > 0
		for _, item := range items {
			all = all && a.contextManager.marked[item.ID]
		}
		a.contextManager.marked = make(map[string]bool)
		if !all {
			for _, item := range items {
				a.contextManager.marked[item.ID] = true
			}
		}
		return nil
	}
	if a.contextManager.searching {
		switch msg.String() {
		case "esc", "enter":
			return a.focusComposerContextQuery(false)
		case "up", "down", "ctrl+p", "ctrl+n":
			a.focusComposerContextQuery(false)
		default:
			return a.updateComposerContextQuery(msg)
		}
	} else if msg.String() == "/" {
		return a.focusComposerContextQuery(true)
	}
	if a.keybindResolver != nil {
		resolution := a.keybindResolver.ResolveEvent(msg, keybindings.ContextChat)
		if resolution.Kind == keybindings.ResolutionMatch && resolution.Action == keybindings.ActionChatUndo {
			a.undoComposerEdit()
			a.contextManager.marked = nil
			return nil
		}
		if resolution.Kind == keybindings.ResolutionChordStarted || resolution.Kind == keybindings.ResolutionChordCancelled {
			return nil
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
	case "space":
		if len(items) > 0 {
			if a.contextManager.marked == nil {
				a.contextManager.marked = make(map[string]bool)
			}
			id := items[index].ID
			if a.contextManager.marked[id] {
				delete(a.contextManager.marked, id)
			} else {
				a.contextManager.marked[id] = true
			}
		}
	case "delete", "backspace":
		if len(a.contextManager.marked) > 0 {
			ids := make([]string, 0, len(a.contextManager.marked))
			for id := range a.contextManager.marked {
				ids = append(ids, id)
			}
			a.removeManagedComposerContexts(ids)
		} else if len(items) > 0 {
			a.removeManagedComposerContext(items[index].ID)
		}
		return nil
	case "enter":
		if len(items) > 0 {
			a.setTextareaRuneCursor(items[index].End)
		}
		a.popDialog(StateContextManager)
		a.focus = FocusEditor
		return nil
	}
	if index >= 0 && index < len(items) {
		a.contextManager.cursor = index
		a.contextManager.selectedID = items[index].ID
	}
	return nil
}

func (a *App) removeManagedComposerContext(id string) bool {
	return a.removeManagedComposerContexts([]string{id})
}

func (a *App) removeManagedComposerContexts(ids []string) bool {
	items, valid := a.composerContextItems()
	state, active := a.activeDialogState()
	if !valid || !active || state != StateContextManager || !a.composerContextEditable() || len(ids) == 0 {
		return false
	}
	visible := make(map[string]threadComposerElement)
	for _, item := range a.contextManager.filtered(items) {
		visible[item.ID] = item
	}
	targets := make(map[string]bool, len(ids))
	for _, id := range ids {
		item, exists := visible[id]
		if !exists || targets[id] {
			a.contextManager.marked = nil
			a.showNotification("Context selection changed; select attachments again", NotifyWarning)
			return false
		}
		if item.Kind != composerElementKindImage && item.Kind != composerElementKindPaste && item.Data == "" {
			a.showNotification("Wait for the selected content to finish loading", NotifyWarning)
			return false
		}
		targets[id] = true
	}
	before := a.captureComposerUndoEntry()
	runes := []rune(before.Text)
	type removal struct{ start, end int }
	var ranges []removal
	for i, item := range items {
		if !targets[item.ID] {
			continue
		}
		end := item.End
		if end < len(runes) && runes[end] == ' ' && (i+1 == len(items) || items[i+1].Start != end) {
			end++
		}
		ranges = append(ranges, removal{item.Start, end})
	}
	// Apply exact original rune ranges once. Sequential deletes or prefix/suffix
	// diffing lose identity for adjacent references with identical labels.
	var updated strings.Builder
	start := 0
	for _, span := range ranges {
		updated.WriteString(string(runes[start:span.start]))
		start = span.end
	}
	updated.WriteString(string(runes[start:]))
	rebase := func(offset int) int {
		removed := 0
		for _, span := range ranges {
			if offset < span.start {
				break
			}
			if offset < span.end {
				return span.start - removed
			}
			removed += span.end - span.start
		}
		return offset - removed
	}
	a.textarea.SetValue(updated.String())
	a.composerElements = nil
	for _, other := range items {
		if !targets[other.ID] {
			other.Start, other.End = rebase(other.Start), rebase(other.End)
			a.composerElements = append(a.composerElements, other)
		}
	}
	a.contextManager.marked = nil
	a.setTextareaRuneCursor(rebase(before.CursorOffset))
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.dismissMentionHints()
	a.syncInputModeFromText()
	a.updateLayout()
	return true
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
	query := "Search: " + composerContextLabel(a.contextManager.query.Value())
	if a.contextManager.searching {
		a.contextManager.query.SetWidth(max(1, innerWidth-8))
		query = a.contextManager.query.View()
	}
	parts = append(parts, query, fmt.Sprintf("Type: %s | %d marked", composerContextTypes[a.contextManager.filter], len(a.contextManager.marked)))
	total := len(items)
	items = a.contextManager.filtered(items)
	index := a.contextManager.selection(items)
	if !valid {
		parts = append(parts, "Draft changed; close and reopen")
	} else if len(items) == 0 {
		if total == 0 {
			parts = append(parts, "No attached context")
		} else {
			parts = append(parts, "No matching context")
		}
	} else {
		visible := max(1, min(6, height-14))
		start := max(0, index-visible+1)
		end := min(len(items), start+visible)
		for i := start; i < end; i++ {
			mark := " "
			if a.contextManager.marked[items[i].ID] {
				mark = "x"
			}
			row := modalEllipsize(profile, fmt.Sprintf("[%s] %d. %s  [%s]", mark, i+1, composerContextLabel(items[i].Label), composerContextKindLabel(items[i])), innerWidth, 0, "...")
			if i == index {
				row = a.styles.Selected.Render(row)
			}
			parts = append(parts, row)
		}
		parts = append(parts, fmt.Sprintf("%d-%d of %d matches", start+1, end, len(items)))
		if height >= 15 {
			parts = append(parts, a.composerContextDetail(items[index], innerWidth, min(3, max(0, height-14-visible)))...)
		}
	}
	if a.contextManager.searching {
		parts = append(parts, "Tab type | Ctrl+A matches", "Enter/Esc list | Up/Down select", "Ctrl+C close")
	} else {
		parts = append(parts, "/ search | Tab type", "Space mark | Ctrl+A matches", "Del remove | Enter locate", "Esc close | "+keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionChatUndo, "ctrl+z"), "undo"))
	}
	for i, line := range parts {
		parts[i] = modalProjectLine(profile, line, innerWidth, 0)
	}
	dialog := contentRenderStyleWidth(profile, a.styles.DialogBorder, innerWidth+2, strings.Join(parts, "\n"))
	view, _ := modalCenteredOverlay(profile, base, dialog, width, height)
	return view
}
