package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// MessageSelector provides a mode for selecting a prior user message to edit/rewrite.
// When active, user messages in the chat are highlighted and navigable with
// Up/Down keys. Pressing Enter selects the message for rewriting.
type MessageSelector struct {
	active      bool
	styles      Styles
	environment RenderEnvironment

	// userIndices holds the ChatView item indices of UserMessages
	userIndices []int
	// selectedPos is the position within userIndices (0-based from the end)
	selectedPos int
}

// NewMessageSelector creates a new message selector.
func NewMessageSelector(styles Styles) *MessageSelector {
	return &MessageSelector{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
	}
}

func (ms *MessageSelector) SetStyles(styles Styles) {
	ms.SetRenderEnvironment(ms.environment.withStyles(styles))
}

func (ms *MessageSelector) SetRenderEnvironment(env RenderEnvironment) {
	ms.environment = env.normalized()
	ms.styles = ms.environment.styles
}

// Active returns whether the selector mode is currently active.
func (ms *MessageSelector) Active() bool {
	return ms.active
}

// Show activates the message selector mode. It scans the chat items and
// identifies all user message indices. The most recent user message is
// selected by default.
func (ms *MessageSelector) Show(items []ChatItem) {
	ms.active = true
	ms.userIndices = nil
	ms.selectedPos = 0

	// Collect indices of UserMessage items
	for i, item := range items {
		if _, ok := item.(*UserMessage); ok {
			ms.userIndices = append(ms.userIndices, i)
		}
	}

	// Select the last (most recent) user message by default
	if len(ms.userIndices) > 0 {
		ms.selectedPos = len(ms.userIndices) - 1
	}
}

// Close deactivates the message selector mode.
func (ms *MessageSelector) Close() {
	ms.active = false
	ms.userIndices = nil
	ms.selectedPos = 0
}

// SelectedItemIndex returns the ChatView item index of the currently selected
// user message. Returns -1 if no selection.
func (ms *MessageSelector) SelectedItemIndex() int {
	if len(ms.userIndices) == 0 || ms.selectedPos < 0 || ms.selectedPos >= len(ms.userIndices) {
		return -1
	}
	return ms.userIndices[ms.selectedPos]
}

// SelectedContent returns the text content of the currently selected user message.
// Requires access to the items slice to extract the content.
func (ms *MessageSelector) SelectedContent(items []ChatItem) string {
	idx := ms.SelectedItemIndex()
	if idx < 0 || idx >= len(items) {
		return ""
	}
	if um, ok := items[idx].(*UserMessage); ok {
		return um.content
	}
	return ""
}

func (ms *MessageSelector) selectedComposer(items []ChatItem) (string, []threadComposerElement) {
	idx := ms.SelectedItemIndex()
	if idx < 0 || idx >= len(items) {
		return "", nil
	}
	if message, ok := items[idx].(*UserMessage); ok {
		return message.content, cloneThreadComposerElements(message.composerElements)
	}
	return "", nil
}

// UserMessageCount returns the number of selectable user messages.
func (ms *MessageSelector) UserMessageCount() int {
	return len(ms.userIndices)
}

// HandleKey processes key events when in message selection mode.
// Returns:
//   - selected: true if user pressed Enter (message was selected for rewrite)
//   - dismissed: true if user pressed Esc (mode was cancelled)
//   - cmd: any tea.Cmd to execute
func (ms *MessageSelector) HandleKey(msg tea.KeyPressMsg) (selected, dismissed bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		ms.Close()
		return false, true, nil

	case "up", "k":
		// Move to previous (older) user message
		if ms.selectedPos > 0 {
			ms.selectedPos--
		}
		return false, false, nil

	case "down", "j":
		// Move to next (newer) user message
		if ms.selectedPos < len(ms.userIndices)-1 {
			ms.selectedPos++
		}
		return false, false, nil

	case "enter":
		// Confirm selection
		if len(ms.userIndices) > 0 {
			return true, false, nil
		}
		return false, true, nil

	default:
		return false, false, nil
	}
}

// IsItemSelected returns true if the given item index is the currently selected
// user message. Used by the chat renderer to apply highlight styling.
func (ms *MessageSelector) IsItemSelected(itemIndex int) bool {
	if !ms.active || len(ms.userIndices) == 0 {
		return false
	}
	return ms.SelectedItemIndex() == itemIndex
}

// IsUserMessageItem returns true if the given item index is a user message
// (and thus selectable in message selection mode).
func (ms *MessageSelector) IsUserMessageItem(itemIndex int) bool {
	if !ms.active {
		return false
	}
	for _, idx := range ms.userIndices {
		if idx == itemIndex {
			return true
		}
	}
	return false
}

// RenderHintBar renders the mode indicator/hint bar shown during message selection.
func (ms *MessageSelector) RenderHintBar(width int) string {
	if !ms.active {
		return ""
	}
	profile := ms.environment.normalized().profile

	hint := "  Rewrite mode: "
	controls := ms.styles.Subtle.Render("Up/Down") + " select message" +
		" " + ms.styles.Subtle.Render("Enter") + " edit" +
		" " + ms.styles.Subtle.Render("Esc") + " cancel"

	count := ""
	if len(ms.userIndices) > 0 {
		pos := ms.selectedPos + 1
		total := len(ms.userIndices)
		count = ms.styles.Dim.Render(" [") +
			ms.styles.AssistantPrefix.Render(intToStr(pos)) +
			ms.styles.Dim.Render("/"+intToStr(total)+"]")
	}

	bar := fitLayoutColumnLine(profile, hint+controls+count, width, 0)

	barStyle := lipgloss.NewStyle()
	if bg := ms.styles.Element.GetBackground(); bg != nil {
		barStyle = barStyle.Background(bg)
	}

	return barStyle.Render(bar)
}

// RenderSelectedHighlight renders a user message with selection highlighting.
// This wraps the normal render output with a distinctive visual treatment.
func (ms *MessageSelector) RenderSelectedHighlight(rendered string, width int) string {
	profile := ms.environment.normalized().profile
	// Apply a selection indicator and distinct background
	highlightStyle := lipgloss.NewStyle()
	if bg := ms.styles.Selection.GetBackground(); bg != nil {
		highlightStyle = highlightStyle.Background(bg)
	}

	// Add a left-side selection indicator
	lines := strings.Split(rendered, "\n")
	result := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			// First line gets the selection arrow
			line = ms.styles.AssistantPrefix.Render("▶ ") + line
		} else {
			line = "  " + line
		}
		line = fitLayoutColumnLine(profile, line, width, 0)
		result = append(result, highlightStyle.Render(line))
	}

	return strings.Join(result, "\n")
}

// intToStr converts an int to string without importing strconv.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + intToStr(-n)
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
