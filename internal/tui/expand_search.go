package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ExpandSearchOverlay provides text search within the expanded view content.
// Scoped to line-based content (the pre-split expand lines), with match
// highlighting and navigation between matches.
type ExpandSearchOverlay struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	input       textinput.Model
	query       string
	matches     []ExpandSearchMatch // all matches found
	matchIdx    int                 // current match index, -1 if none
}

// ExpandSearchMatch represents a single match occurrence within the expanded content.
type ExpandSearchMatch struct {
	LineIndex int // line index within expandLines
	ColStart  int // byte offset of match start within the line
	ColEnd    int // byte offset of match end within the line
}

// NewExpandSearchOverlay creates a new expand search overlay.
func NewExpandSearchOverlay(styles Styles) *ExpandSearchOverlay {
	ti := textinput.New()
	ti.Placeholder = "Search in expanded view..."
	ti.CharLimit = 256
	ti.SetWidth(40)
	ti.Prompt = ""

	return &ExpandSearchOverlay{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
		input:       ti,
		matchIdx:    -1,
	}
}

func (s *ExpandSearchOverlay) SetStyles(styles Styles) {
	s.SetRenderEnvironment(s.environment.withStyles(styles))
}

func (s *ExpandSearchOverlay) SetRenderEnvironment(env RenderEnvironment) {
	s.environment = env.normalized()
	s.styles = s.environment.styles
}

// Show makes the search overlay visible and focuses the input.
func (s *ExpandSearchOverlay) Show() {
	s.visible = true
	s.input.SetValue("")
	s.input.Focus()
	s.matches = nil
	s.matchIdx = -1
	s.query = ""
}

// Close hides the search overlay and resets state.
func (s *ExpandSearchOverlay) Close() {
	s.visible = false
	s.input.Blur()
	s.matches = nil
	s.matchIdx = -1
	s.query = ""
}

// Visible returns whether the overlay is currently shown.
func (s *ExpandSearchOverlay) Visible() bool {
	return s.visible
}

// Query returns the current search query.
func (s *ExpandSearchOverlay) Query() string {
	return s.query
}

// MatchCount returns the total number of matches.
func (s *ExpandSearchOverlay) MatchCount() int {
	return len(s.matches)
}

// CurrentMatchIndex returns the 1-based current match index for display.
func (s *ExpandSearchOverlay) CurrentMatchIndex() int {
	if s.matchIdx < 0 {
		return 0
	}
	return s.matchIdx + 1
}

// CurrentMatch returns the current match, or nil if none.
func (s *ExpandSearchOverlay) CurrentMatch() *ExpandSearchMatch {
	if s.matchIdx < 0 || s.matchIdx >= len(s.matches) {
		return nil
	}
	return &s.matches[s.matchIdx]
}

// HandleKey processes key events for the expand search overlay.
// Returns: scrollToLine (line index to scroll to, -1 if none), dismissed bool, cmd.
func (s *ExpandSearchOverlay) HandleKey(msg tea.KeyPressMsg) (scrollToLine int, dismissed bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.Close()
		return -1, true, nil

	case "enter", "down":
		// Navigate to next match
		s.nextMatch()
		if m := s.CurrentMatch(); m != nil {
			return m.LineIndex, false, nil
		}
		return -1, false, nil

	case "up", "shift+enter":
		// Navigate to previous match
		s.prevMatch()
		if m := s.CurrentMatch(); m != nil {
			return m.LineIndex, false, nil
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

// UpdateMatches re-runs the search against the expanded content lines.
func (s *ExpandSearchOverlay) UpdateMatches(lines []string) {
	s.matches = nil
	s.matchIdx = -1

	query := strings.TrimSpace(s.query)
	if query == "" {
		return
	}

	queryLower := strings.ToLower(query)
	queryLen := len(queryLower)

	for lineIdx, line := range lines {
		lineLower := strings.ToLower(line)
		// Find all occurrences in this line
		searchFrom := 0
		for {
			idx := strings.Index(lineLower[searchFrom:], queryLower)
			if idx < 0 {
				break
			}
			absIdx := searchFrom + idx
			s.matches = append(s.matches, ExpandSearchMatch{
				LineIndex: lineIdx,
				ColStart:  absIdx,
				ColEnd:    absIdx + queryLen,
			})
			searchFrom = absIdx + queryLen
		}
	}

	if len(s.matches) > 0 {
		s.matchIdx = 0
	}
}

// nextMatch advances to the next match, wrapping around.
func (s *ExpandSearchOverlay) nextMatch() {
	if len(s.matches) == 0 {
		return
	}
	s.matchIdx++
	if s.matchIdx >= len(s.matches) {
		s.matchIdx = 0
	}
}

// prevMatch goes to the previous match, wrapping around.
func (s *ExpandSearchOverlay) prevMatch() {
	if len(s.matches) == 0 {
		return
	}
	s.matchIdx--
	if s.matchIdx < 0 {
		s.matchIdx = len(s.matches) - 1
	}
}

// Render renders the search bar as a single-line bar.
func (s *ExpandSearchOverlay) Render(width int) string {
	if !s.visible {
		return ""
	}

	// Adjust input width based on available space
	inputWidth := width - 40
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
			matchInfo = fmt.Sprintf(" %d of %d ", s.CurrentMatchIndex(), len(s.matches))
		} else {
			matchInfo = " 0 matches "
		}
	}
	matchDisplay := s.styles.Subtle.Render(matchInfo)

	// Navigation hints
	navHint := s.styles.Subtle.Render(" Up/Down navigate  Esc close search")

	// Compose the bar. Bubbles remains the editor/cursor owner; the selected
	// project grid owns only the final physical row.
	bar := "  " + searchIcon + inputView + matchDisplay + navHint

	// Style the entire bar with a background to separate from content
	barStyle := lipgloss.NewStyle().Background(s.styles.Element.GetBackground())

	profile := s.environment.normalized().profile
	bar = profile.padAligned(bar, max(width, 1), "left", 0)
	return contentProjectLine(profile, barStyle.Render(bar), width, 0)
}

// HighlightLine applies match highlighting to a single line for the given line index.
// It highlights all matches on that line, with the current match in a distinct style.
func (s *ExpandSearchOverlay) HighlightLine(line string, lineIdx int) string {
	if !s.visible || s.query == "" || len(s.matches) == 0 {
		return line
	}

	// Collect matches on this line
	type matchRange struct {
		start     int
		end       int
		isCurrent bool
	}
	var ranges []matchRange
	for i, m := range s.matches {
		if m.LineIndex == lineIdx {
			ranges = append(ranges, matchRange{
				start:     m.ColStart,
				end:       m.ColEnd,
				isCurrent: i == s.matchIdx,
			})
		}
	}

	if len(ranges) == 0 {
		return line
	}

	// Build highlighted line by splicing in styled segments
	var result strings.Builder
	pos := 0
	for _, r := range ranges {
		// Clamp to line bounds
		start := r.start
		end := r.end
		if start > len(line) {
			start = len(line)
		}
		if end > len(line) {
			end = len(line)
		}
		if start < pos {
			start = pos
		}
		if end <= start {
			continue
		}

		// Write text before match
		if pos < start {
			result.WriteString(line[pos:start])
		}

		// Write highlighted match
		matchText := line[start:end]
		if r.isCurrent {
			// Current match: reverse video (more prominent)
			result.WriteString(s.styles.Selected.Render(matchText))
		} else {
			// Other matches: bold highlight
			result.WriteString(s.styles.Highlight.Render(matchText))
		}
		pos = end
	}

	// Write remaining text
	if pos < len(line) {
		result.WriteString(line[pos:])
	}

	return result.String()
}
