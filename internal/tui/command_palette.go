package tui

import (
	"context"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine/commands"
)

// commandPaletteItem represents a single entry in the command palette.
type commandPaletteItem struct {
	command *commands.Command
	score   int // fuzzy match score (higher = better match)
	section string
}

const (
	commandPaletteSectionRecent    = "Recent"
	commandPaletteSectionSuggested = "Suggested"
)

// CommandPalette is a searchable modal overlay listing all available slash commands.
// Opened via Ctrl+K. Users can type to fuzzy-filter, navigate with arrows,
// and select a command with Enter. Esc dismisses the palette.
type CommandPalette struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	query       string
	cursor      int
	all         []*commands.Command // all non-hidden commands
	recent      []string            // canonical names, newest first; intentionally instance-local
	filtered    []commandPaletteItem
	offset      int // scroll offset for long lists
}

// NewCommandPalette creates a new command palette.
func NewCommandPalette(styles Styles) *CommandPalette {
	return &CommandPalette{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
	}
}

func (p *CommandPalette) SetStyles(styles Styles) {
	p.SetRenderEnvironment(p.environment.withStyles(styles))
}

func (p *CommandPalette) SetRenderEnvironment(env RenderEnvironment) {
	p.environment = env.normalized()
	p.styles = p.environment.styles
}

// Show makes the palette visible and populates it from the registry.
func (p *CommandPalette) Show(registry *commands.Registry) {
	p.ShowFor(registry, nil)
}

// ShowFor populates the palette from the active runtime capability snapshot.
func (p *CommandPalette) ShowFor(registry *commands.Registry, cmdCtx *commands.CommandContext) {
	p.visible = true
	p.query = ""
	p.cursor = 0
	p.offset = 0
	p.all = nil
	p.filtered = nil

	if registry != nil {
		p.all = registry.ListForContext(
			context.Background(),
			commands.EntrypointTUI,
			cmdCtx,
		)
	}
	p.applyFilter()
}

// Close hides the palette without selecting anything.
func (p *CommandPalette) Close() {
	p.visible = false
}

// Visible returns whether the palette is currently shown.
func (p *CommandPalette) Visible() bool {
	return p.visible
}

// HandleKey processes key events for the command palette.
// Returns the selected command name (non-empty on confirm) and whether the overlay was dismissed.
func (p *CommandPalette) HandleKey(msg tea.KeyPressMsg) (selectedCommand string, dismissed bool) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+k":
		p.Close()
		return "", true

	case "enter":
		if p.cursor >= 0 && p.cursor < len(p.filtered) {
			selected := p.filtered[p.cursor].command.Name
			p.Close()
			return selected, true
		}
		// No items — just dismiss
		p.Close()
		return "", true

	case "up", "ctrl+p":
		if len(p.filtered) > 0 {
			p.cursor--
			if p.cursor < 0 {
				p.cursor = len(p.filtered) - 1
			}
			p.ensureCursorVisible()
		}

	case "down", "ctrl+n":
		if len(p.filtered) > 0 {
			p.cursor = (p.cursor + 1) % len(p.filtered)
			p.ensureCursorVisible()
		}

	case "pgup":
		p.cursor -= 5
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.ensureCursorVisible()

	case "pgdown":
		p.cursor += 5
		if p.cursor >= len(p.filtered) {
			p.cursor = len(p.filtered) - 1
		}
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.ensureCursorVisible()

	case "backspace":
		if p.query != "" {
			runes := []rune(p.query)
			p.query = string(runes[:len(runes)-1])
			p.applyFilter()
		}

	default:
		if msg.Text != "" {
			p.query += msg.Text
			p.applyFilter()
		}
	}

	return "", false
}

// Overlay renders the command palette dialog on top of the base view.
func (p *CommandPalette) Overlay(base string, width, height int) string {
	if !p.visible {
		return base
	}

	dialogWidth := width - 8
	if dialogWidth > 70 {
		dialogWidth = 70
	}
	if dialogWidth < 44 {
		dialogWidth = 44
	}

	// Compute visible area height
	maxContentHeight := height - 10 // room for title, search, footer, borders
	if maxContentHeight < 5 {
		maxContentHeight = 5
	}

	// Render visible items
	visibleItems := maxContentHeight
	if p.query == "" {
		visibleItems -= p.emptyQuerySectionCount()
		if visibleItems < 1 {
			visibleItems = 1
		}
	}
	if visibleItems > len(p.filtered) {
		visibleItems = len(p.filtered)
	}

	// Ensure offset is sane
	if p.offset > len(p.filtered)-visibleItems {
		p.offset = len(p.filtered) - visibleItems
	}
	if p.offset < 0 {
		p.offset = 0
	}

	profile := p.environment.normalized().profile
	var parts []string

	// Title
	title := p.styles.DialogTitle.Render("  Command Palette")
	parts = append(parts, title)
	parts = append(parts, "")

	// Search input
	searchPrompt := p.styles.Subtle.Render("  > ")
	cursor := p.styles.Bold.Render("|")
	if p.query == "" {
		placeholder := p.styles.Subtle.Render("Type to search commands...")
		parts = append(parts, searchPrompt+placeholder+cursor)
	} else {
		parts = append(parts, searchPrompt+p.query+cursor)
	}
	parts = append(parts, "  "+strings.Repeat("\u2500", dialogWidth-6))

	// Command list
	if len(p.filtered) == 0 {
		if p.query != "" {
			parts = append(parts, "  "+p.styles.Subtle.Render("No matching commands"))
		} else {
			parts = append(parts, "  "+p.styles.Subtle.Render("No commands registered"))
		}
	} else {
		end := p.offset + visibleItems
		if end > len(p.filtered) {
			end = len(p.filtered)
		}

		// Scroll up indicator
		if p.offset > 0 {
			parts = append(parts, "  "+p.styles.Subtle.Render("\u2191 more"))
		}

		for i := p.offset; i < end; i++ {
			item := p.filtered[i]
			if item.section != "" &&
				(i == p.offset || item.section != p.filtered[i-1].section) {
				parts = append(parts, "  "+p.styles.Bold.Render(item.section))
			}
			line := p.renderItem(profile, item, i == p.cursor, dialogWidth)
			parts = append(parts, line)
		}

		// Scroll down indicator
		if end < len(p.filtered) {
			parts = append(parts, "  "+p.styles.Subtle.Render("\u2193 more"))
		}
	}

	// Footer
	parts = append(parts, "")
	parts = append(parts, "  "+strings.Repeat("\u2500", dialogWidth-6))
	helpText := p.styles.DialogHelp.Render(
		"  \u2191\u2193 navigate  Enter select  Esc cancel",
	)
	parts = append(parts, helpText)

	dialog := contentRenderStyleWidth(
		profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
	view, _ := modalCenteredOverlay(profile, base, dialog, width, height)
	return view
}

// renderItem renders a single command item in the palette list.
func (p *CommandPalette) renderItem(
	profile DisplayCellProfile,
	item commandPaletteItem,
	isCursor bool,
	dialogWidth int,
) string {
	cmd := item.command

	// Command name with leading slash
	name := "/" + cmd.Name

	// Aliases
	aliasStr := ""
	if len(cmd.Aliases) > 0 {
		aliases := make([]string, len(cmd.Aliases))
		for i, a := range cmd.Aliases {
			aliases[i] = "/" + a
		}
		aliasStr = " " + p.styles.Subtle.Render("("+strings.Join(aliases, ", ")+")")
	}

	originStr := ""
	if cmd.Source != string(commands.CommandSourceCore) ||
		cmd.Trust != commands.CommandTrustCore {
		originStr = " " + p.styles.Subtle.Render(
			"["+cmd.Source+" / "+string(cmd.Trust)+"]",
		)
	}
	// Description — truncate to fit
	desc := cmd.Description
	nameWidth := profile.measure(name+aliasStr+originStr, 4)
	descAvail := dialogWidth - nameWidth - 8 // padding + margins
	if descAvail < 10 {
		descAvail = 10
	}
	desc = modalEllipsize(profile, desc, descAvail, 6+nameWidth, "\u2026")

	// Build the line
	var line string
	if isCursor {
		marker := p.styles.EditorPrompt.Render("> ")
		nameStyled := p.styles.DialogTitle.Render(name)
		line = "  " + marker + nameStyled + aliasStr + originStr + "  " + desc
		// Highlight the entire line
		line = lipgloss.NewStyle().Reverse(true).Render(line)
	} else {
		nameStyled := p.styles.DialogTitle.Render(name)
		line = "    " + nameStyled + aliasStr + originStr + "  " + p.styles.Subtle.Render(desc)
	}

	return modalProjectLine(profile, line, max(dialogWidth, 1), 0)
}

// applyFilter filters commands based on the current query using fuzzy matching.
func (p *CommandPalette) applyFilter() {
	p.filtered = p.filtered[:0]
	query := strings.ToLower(strings.TrimSpace(p.query))

	if query == "" {
		seen := make(map[string]struct{})
		for _, name := range p.recent {
			for _, cmd := range p.all {
				if cmd.Name == name {
					p.filtered = append(p.filtered, commandPaletteItem{
						command: cmd,
						section: commandPaletteSectionRecent,
					})
					seen[name] = struct{}{}
					break
				}
			}
		}
		for _, cmd := range p.all {
			if cmd.DiscoveryTier == commands.DiscoveryTierPrimary {
				if _, exists := seen[cmd.Name]; !exists {
					p.filtered = append(p.filtered, commandPaletteItem{
						command: cmd,
						section: commandPaletteSectionSuggested,
					})
					seen[cmd.Name] = struct{}{}
				}
			}
		}
		p.cursor, p.offset = 0, 0
		return
	}
	for _, cmd := range p.all {

		// Check name match
		nameScore := fuzzyScore(cmd.Name, query)
		// Check alias match
		aliasScore := 0
		for _, alias := range cmd.Aliases {
			if s := fuzzyScore(alias, query); s > aliasScore {
				aliasScore = s
			}
		}
		// Check description match (lower priority)
		descScore := fuzzyScore(strings.ToLower(cmd.Description), query) / 2

		bestScore := nameScore
		if aliasScore > bestScore {
			bestScore = aliasScore
		}
		if descScore > bestScore {
			bestScore = descScore
		}

		if bestScore > 0 {
			p.filtered = append(p.filtered, commandPaletteItem{command: cmd, score: bestScore})
		}
	}

	// Sort by score descending (better matches first)
	sortPaletteItems(p.filtered)

	// Reset cursor
	p.cursor = 0
	p.offset = 0
}

// RecordRecent persists a result-bound successful selection only on this
// palette instance. App owns admission and exact local/query result matching.
func (p *CommandPalette) RecordRecent(name string) {
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	if name == "" {
		return
	}
	next := []string{name}
	for _, existing := range p.recent {
		if existing != name {
			next = append(next, existing)
		}
		if len(next) == 3 {
			break
		}
	}
	p.recent = next
}

func (p *CommandPalette) emptyQuerySectionCount() int {
	count := 0
	previous := ""
	for _, item := range p.filtered {
		if item.section != "" && item.section != previous {
			count++
			previous = item.section
		}
	}
	return count
}

// sortPaletteItems sorts palette items by score descending, then by name ascending.
func sortPaletteItems(items []commandPaletteItem) {
	// Simple insertion sort — typically < 50 items
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			if items[j].score > items[j-1].score {
				items[j], items[j-1] = items[j-1], items[j]
			} else if items[j].score == items[j-1].score &&
				(items[j].command.DisplayOrder < items[j-1].command.DisplayOrder ||
					(items[j].command.DisplayOrder == items[j-1].command.DisplayOrder &&
						items[j].command.Name < items[j-1].command.Name)) {
				items[j], items[j-1] = items[j-1], items[j]
			} else {
				break
			}
		}
	}
}

// ensureCursorVisible adjusts the scroll offset to keep the cursor in view.
func (p *CommandPalette) ensureCursorVisible() {
	// We use a default visible window of 12 items
	visibleWindow := 12
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+visibleWindow {
		p.offset = p.cursor - visibleWindow + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// fuzzyScore computes a simple fuzzy match score for a target string against a query.
// Returns 0 if no match. Higher scores indicate better matches.
// Uses substring match, prefix match, and character-sequence matching.
func fuzzyScore(target, query string) int {
	if query == "" {
		return 1
	}
	target = strings.ToLower(target)

	// Exact match
	if target == query {
		return 100
	}

	// Prefix match (highest priority after exact)
	if strings.HasPrefix(target, query) {
		return 80
	}

	// Contains match
	if strings.Contains(target, query) {
		return 60
	}

	// Fuzzy character sequence match: all query chars appear in order
	qi := 0
	for ti := 0; ti < len(target) && qi < len(query); ti++ {
		if target[ti] == query[qi] {
			qi++
		}
	}
	if qi == len(query) {
		// All characters matched in order
		// Score bonus for matches at word boundaries
		score := 30
		qi = 0
		for ti := 0; ti < len(target) && qi < len(query); ti++ {
			if target[ti] == query[qi] {
				qi++
				if ti == 0 || !unicode.IsLetter(rune(target[ti-1])) {
					score += 5 // word boundary bonus
				}
			}
		}
		return score
	}

	return 0
}
