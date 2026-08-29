package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// SearchMatch represents a single search hit within the chat items.
type SearchMatch struct {
	ItemIndex int    // index into ChatView.items
	Text      string // the matched line or snippet (for display)
	Role      string // "user", "assistant", "tool", "system", "thinking"
}

// SearchOverlay provides full-text search across conversation history.
// Triggered by Ctrl+F, rendered as a compact bar at the top of the screen.
type SearchOverlay struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	input       textinput.Model
	matches     []SearchMatch
	matchIdx    int // current match index (0-based), -1 if no matches
	query       string
}

// NewSearchOverlay creates a new search overlay.
func NewSearchOverlay(styles Styles) *SearchOverlay {
	ti := textinput.New()
	ti.Placeholder = "Search..."
	ti.CharLimit = 256
	ti.SetWidth(40)
	ti.Prompt = ""

	return &SearchOverlay{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
		input:       ti,
		matchIdx:    -1,
	}
}

func (s *SearchOverlay) SetStyles(styles Styles) {
	s.SetRenderEnvironment(s.environment.withStyles(styles))
}

func (s *SearchOverlay) SetRenderEnvironment(env RenderEnvironment) {
	s.environment = env.normalized()
	s.styles = s.environment.styles
}

// Show makes the search overlay visible and focuses the input.
func (s *SearchOverlay) Show() {
	s.visible = true
	s.input.SetValue("")
	s.input.Focus()
	s.matches = nil
	s.matchIdx = -1
	s.query = ""
}

// Close hides the search overlay and resets state.
func (s *SearchOverlay) Close() {
	s.visible = false
	s.input.Blur()
	s.matches = nil
	s.matchIdx = -1
	s.query = ""
}

// Visible returns whether the overlay is currently shown.
func (s *SearchOverlay) Visible() bool {
	return s.visible
}

// Query returns the current search query.
func (s *SearchOverlay) Query() string {
	return s.query
}

// CurrentMatch returns the current match, or nil if none.
func (s *SearchOverlay) CurrentMatch() *SearchMatch {
	if s.matchIdx < 0 || s.matchIdx >= len(s.matches) {
		return nil
	}
	return &s.matches[s.matchIdx]
}

// MatchCount returns the total number of matches.
func (s *SearchOverlay) MatchCount() int {
	return len(s.matches)
}

// CurrentMatchIndex returns the 1-based current match index for display.
func (s *SearchOverlay) CurrentMatchIndex() int {
	if s.matchIdx < 0 {
		return 0
	}
	return s.matchIdx + 1
}

// HandleKey processes key events for the search overlay.
// Returns: scrollToItem (item index to scroll to, -1 if none), dismissed bool, cmd.
func (s *SearchOverlay) HandleKey(msg tea.KeyPressMsg) (scrollToItem int, dismissed bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.Close()
		return -1, true, nil

	case "enter", "down":
		// Navigate to next match
		s.nextMatch()
		if m := s.CurrentMatch(); m != nil {
			return m.ItemIndex, false, nil
		}
		return -1, false, nil

	case "up", "shift+enter":
		// Navigate to previous match
		s.prevMatch()
		if m := s.CurrentMatch(); m != nil {
			return m.ItemIndex, false, nil
		}
		return -1, false, nil

	default:
		// Pass to text input
		var tiCmd tea.Cmd
		s.input, tiCmd = s.input.Update(msg)
		// Check if query changed
		newQuery := s.input.Value()
		if newQuery != s.query {
			s.query = newQuery
		}
		return -1, false, tiCmd
	}
}

// UpdateMatches re-runs the search against the chat items.
// Should be called after the query changes.
func (s *SearchOverlay) UpdateMatches(items []ChatItem) {
	s.matches = nil
	s.matchIdx = -1

	query := strings.TrimSpace(s.query)
	if query == "" {
		return
	}

	queryLower := strings.ToLower(query)

	for idx, item := range items {
		text, role := extractSearchableText(item)
		if text == "" {
			continue
		}
		if strings.Contains(strings.ToLower(text), queryLower) {
			// Extract matching line for display
			snippet := extractMatchSnippet(text, queryLower)
			s.matches = append(s.matches, SearchMatch{
				ItemIndex: idx,
				Text:      snippet,
				Role:      role,
			})
		}
	}

	if len(s.matches) > 0 {
		s.matchIdx = 0
	}
}

// nextMatch advances to the next match, wrapping around.
func (s *SearchOverlay) nextMatch() {
	if len(s.matches) == 0 {
		return
	}
	s.matchIdx++
	if s.matchIdx >= len(s.matches) {
		s.matchIdx = 0
	}
}

// prevMatch goes to the previous match, wrapping around.
func (s *SearchOverlay) prevMatch() {
	if len(s.matches) == 0 {
		return
	}
	s.matchIdx--
	if s.matchIdx < 0 {
		s.matchIdx = len(s.matches) - 1
	}
}

// Render renders the search bar overlay as a single-line bar at the top of the screen.
func (s *SearchOverlay) Render(width int) string {
	if !s.visible {
		return ""
	}

	// Adjust input width based on available space
	inputWidth := width - 40 // leave room for status and controls
	if inputWidth < 20 {
		inputWidth = 20
	}
	if inputWidth > 60 {
		inputWidth = 60
	}
	s.input.SetWidth(inputWidth)

	// Build the search bar content
	searchIcon := s.styles.Subtle.Render("/ ")
	inputView := s.input.View()

	// Match count display
	var matchInfo string
	if s.query != "" {
		if len(s.matches) > 0 {
			matchInfo = fmt.Sprintf(" %d/%d ", s.CurrentMatchIndex(), len(s.matches))
		} else {
			matchInfo = " 0 matches "
		}
	}
	matchDisplay := s.styles.Subtle.Render(matchInfo)

	// Navigation hints
	navHint := s.styles.Subtle.Render(" Up/Down navigate  Esc close")

	// Compose the bar. Bubbles remains the editor/cursor owner; the selected
	// project grid owns only the final physical row.
	bar := "  " + searchIcon + inputView + matchDisplay + navHint

	// Style the entire bar with a bottom border to separate from content
	barStyle := lipgloss.NewStyle().Background(s.styles.Element.GetBackground())

	profile := s.environment.normalized().profile
	bar = profile.padAligned(bar, max(width, 1), "left", 0)
	return contentProjectLine(profile, barStyle.Render(bar), width, 0)
}

// extractSearchableText returns the searchable text content and role label
// for a given ChatItem.
func extractSearchableText(item ChatItem) (text, role string) {
	switch m := item.(type) {
	case *UserMessage:
		return m.content, "user"
	case *AssistantMessage:
		return m.content, "assistant"
	case *ThinkingMessage:
		return m.content, "thinking"
	case *SystemMessage:
		return m.content, "system"
	case *ToolMessage:
		// Search in tool name, input, and output
		parts := []string{m.name}
		if m.input != "" {
			parts = append(parts, m.input)
		}
		if m.output != "" {
			parts = append(parts, m.output)
		}
		return strings.Join(parts, "\n"), "tool"
	case *ToolGroupMessage:
		var parts []string
		for _, t := range m.tools {
			parts = append(parts, t.name)
			if t.output != "" {
				parts = append(parts, t.output)
			}
		}
		return strings.Join(parts, "\n"), "tool"
	default:
		return "", ""
	}
}

// extractMatchSnippet extracts the first matching line from the text.
func extractMatchSnippet(text, queryLower string) string {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), queryLower) {
			// Truncate long lines
			if len(trimmed) > 80 {
				trimmed = trimmed[:77] + "..."
			}
			return trimmed
		}
	}
	// Fallback: return first non-empty line
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len(trimmed) > 80 {
				trimmed = trimmed[:77] + "..."
			}
			return trimmed
		}
	}
	return ""
}
