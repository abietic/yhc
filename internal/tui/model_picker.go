package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine/provider"
)

// modelPickerItem represents a single selectable row in the model picker.
// Provider headers are non-selectable separators; models are selectable.
type modelPickerItem struct {
	isHeader bool   // true for provider group header (non-selectable)
	provider string // provider name (for headers)
	entry    provider.RuntimeInventoryEntry
}

// ModelPicker renders a modal overlay for browsing and selecting models.
// Models are grouped by provider. The current model is marked with a check.
// Navigation skips non-selectable provider headers.
type ModelPicker struct {
	visible      bool
	styles       Styles
	environment  RenderEnvironment
	items        []modelPickerItem
	cursor       int    // index into items (always points to a selectable item)
	currentModel string // currently active model ID for highlighting
	offset       int    // scroll offset for when list exceeds viewport
}

// NewModelPicker creates a new model picker overlay.
func NewModelPicker(styles Styles) *ModelPicker {
	return &ModelPicker{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
	}
}

func (p *ModelPicker) SetStyles(styles Styles) {
	p.SetRenderEnvironment(p.environment.withStyles(styles))
}

func (p *ModelPicker) SetRenderEnvironment(env RenderEnvironment) {
	p.environment = env.normalized()
	p.styles = p.environment.styles
}

// Show makes the picker visible with the configured inventory and current selector.
func (p *ModelPicker) Show(
	inventory provider.RuntimeInventorySnapshot,
	currentModel string,
) {
	p.visible = true
	p.currentModel = currentModel
	p.offset = 0
	p.items = p.buildItems(inventory)
	p.cursor = p.findFirstSelectable()
	// Try to position cursor on the current model
	if idx := p.findModel(currentModel); idx >= 0 {
		p.cursor = idx
	}
}

// Close hides the picker without making a selection.
func (p *ModelPicker) Close() {
	p.visible = false
}

// Visible returns whether the picker is currently shown.
func (p *ModelPicker) Visible() bool {
	return p.visible
}

// HandleKey processes key events for the model picker.
// Returns the selected model ID (non-empty on confirm) and whether the overlay was dismissed.
func (p *ModelPicker) HandleKey(msg tea.KeyPressMsg) (selectedModel string, dismissed bool) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		p.Close()
		return "", true

	case "enter":
		if p.cursor >= 0 && p.cursor < len(p.items) && !p.items[p.cursor].isHeader {
			selected := p.items[p.cursor].entry.Selector
			p.Close()
			return selected, true
		}

	case "up", "k":
		p.moveCursor(-1)

	case "down", "j":
		p.moveCursor(1)

	case "pgup":
		for i := 0; i < 5; i++ {
			p.moveCursor(-1)
		}

	case "pgdown":
		for i := 0; i < 5; i++ {
			p.moveCursor(1)
		}

	case "home", "g":
		p.cursor = p.findFirstSelectable()

	case "end", "G":
		p.cursor = p.findLastSelectable()
	}

	return "", false
}

// Overlay renders the model picker dialog on top of the base view.
func (p *ModelPicker) Overlay(base string, width, height int) string {
	if !p.visible {
		return base
	}

	dialogWidth := width - 6
	if dialogWidth > 70 {
		dialogWidth = 70
	}
	if dialogWidth < 44 {
		dialogWidth = 44
	}

	// Compute visible area height
	maxContentHeight := height - 8 // room for title, footer, borders
	if maxContentHeight < 5 {
		maxContentHeight = 5
	}

	profile := p.environment.normalized().profile

	// Render all item lines
	allLines := p.renderItems(profile, dialogWidth)

	// Adjust scroll offset to keep cursor visible
	cursorLine := p.cursorToLine()
	if cursorLine < p.offset {
		p.offset = cursorLine
	}
	if cursorLine >= p.offset+maxContentHeight {
		p.offset = cursorLine - maxContentHeight + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}

	// Slice visible lines
	end := p.offset + maxContentHeight
	if end > len(allLines) {
		end = len(allLines)
	}
	visibleLines := allLines[p.offset:end]

	// Build dialog content
	var parts []string

	// Title
	title := p.styles.DialogTitle.Render("  Select Model")
	parts = append(parts, title)
	parts = append(parts, "")

	// Content lines
	parts = append(parts, visibleLines...)

	// Scroll indicator
	scrollInfo := ""
	if len(allLines) > maxContentHeight {
		pct := 0
		denom := len(allLines) - maxContentHeight
		if denom > 0 {
			pct = (p.offset * 100) / denom
		}
		scrollInfo = fmt.Sprintf(" (%d%%)", pct)
	}

	// Footer
	parts = append(parts, "")
	parts = append(parts, strings.Repeat("\u2500", dialogWidth-4))
	helpText := p.styles.DialogHelp.Render(
		"  \u2191\u2193 navigate  Enter select  Esc cancel" + scrollInfo,
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

// buildItems constructs the flat list of display items from configured routes.
func (p *ModelPicker) buildItems(
	inventory provider.RuntimeInventorySnapshot,
) []modelPickerItem {
	grouped := make(map[string][]provider.RuntimeInventoryEntry)
	providers := make([]string, 0)
	for _, entry := range inventory.Entries {
		if _, exists := grouped[entry.Provider]; !exists {
			providers = append(providers, entry.Provider)
		}
		grouped[entry.Provider] = append(grouped[entry.Provider], entry)
	}
	sort.Strings(providers)
	var items []modelPickerItem

	for i, providerName := range providers {
		// Add blank separator line between groups (except the first)
		if i > 0 {
			items = append(items, modelPickerItem{isHeader: true, provider: ""})
		}
		// Provider header
		items = append(items, modelPickerItem{isHeader: true, provider: providerName})
		// Models in group
		entries := grouped[providerName]
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Selector) <
				strings.ToLower(entries[j].Selector)
		})
		for _, entry := range entries {
			items = append(items, modelPickerItem{isHeader: false, entry: entry})
		}
	}

	return items
}

// renderItems generates styled lines for all items.
func (p *ModelPicker) renderItems(
	profile DisplayCellProfile,
	dialogWidth int,
) []string {
	lines := make([]string, 0, len(p.items))

	// Styling
	providerStyle := p.styles.DialogTitle
	activeStyle := p.styles.ToolSuccess.Bold(true)
	capStyle := p.styles.Dim
	tierStyle := p.styles.Warning
	cursorStyle := lipgloss.NewStyle().Reverse(true)

	for i, item := range p.items {
		if item.isHeader {
			if item.provider == "" {
				// Blank separator
				lines = append(lines, "")
			} else {
				// Provider header
				lines = append(lines, "  "+providerStyle.Render(item.provider))
			}
			continue
		}

		// Model row
		entry := item.entry
		isCurrent := strings.EqualFold(entry.Selector, p.currentModel)
		isCursor := i == p.cursor

		// Marker: check for current, space otherwise
		marker := "  "
		if isCurrent {
			marker = activeStyle.Render("* ")
		}

		// Display name (left-aligned)
		name := entry.DisplayName
		if name == "" {
			name = entry.Selector
		}

		// Capability icons
		caps := p.renderCapabilities(entry)

		// Token limit
		tokens := formatTokens(entry.Metadata.ContextWindowTokens.Value)

		// Cost tier indicator
		tier := renderCostTier(entry.Metadata.CostTier.Value)

		// Compose the line
		// Layout: "  [marker] name   [caps] [tokens] [tier]"
		nameWidth := dialogWidth - 24 // leave room for caps, tokens, tier, padding
		if nameWidth < 16 {
			nameWidth = 16
		}
		name = modalEllipsize(profile, name, nameWidth, 6, "\u2026")

		capsStr := capStyle.Render(caps)
		tokensStr := capStyle.Render(tokens)
		tierStr := tierStyle.Render(tier)

		// Pad name to fixed width for alignment
		padded := profile.padAligned(name, nameWidth, "left", 6)

		line := fmt.Sprintf("    %s%s %s %s %s", marker, padded, capsStr, tokensStr, tierStr)

		if isCursor {
			// Highlight the entire line for the cursor
			line = cursorStyle.Render(line)
		}

		lines = append(lines, modalProjectLine(profile, line, max(dialogWidth, 1), 0))
	}

	return lines
}

// renderCapabilities renders capability icons for a model entry.
func (p *ModelPicker) renderCapabilities(
	entry provider.RuntimeInventoryEntry,
) string {
	var icons []string
	if entry.Metadata.Tools.Value {
		icons = append(icons, "T")
	}
	if entry.Metadata.Thinking.Value {
		icons = append(icons, "R")
	}
	if entry.Metadata.Images.Value || entry.Metadata.PDFs.Value {
		icons = append(icons, "V")
	}
	if len(icons) == 0 {
		return "   "
	}
	return strings.Join(icons, "")
}

// moveCursor moves the cursor by delta, skipping non-selectable headers.
func (p *ModelPicker) moveCursor(delta int) {
	if len(p.items) == 0 {
		return
	}

	step := 1
	if delta < 0 {
		step = -1
	}

	next := p.cursor
	for {
		next += step
		if next < 0 || next >= len(p.items) {
			// Wrap or clamp
			if next < 0 {
				next = len(p.items) - 1
			} else {
				next = 0
			}
		}
		if next == p.cursor {
			break // wrapped around completely
		}
		if !p.items[next].isHeader {
			p.cursor = next
			return
		}
	}
}

// findFirstSelectable returns the index of the first selectable item.
func (p *ModelPicker) findFirstSelectable() int {
	for i, item := range p.items {
		if !item.isHeader {
			return i
		}
	}
	return 0
}

// findLastSelectable returns the index of the last selectable item.
func (p *ModelPicker) findLastSelectable() int {
	for i := len(p.items) - 1; i >= 0; i-- {
		if !p.items[i].isHeader {
			return i
		}
	}
	return 0
}

// findModel returns the index of the item matching the given model ID, or -1.
func (p *ModelPicker) findModel(modelID string) int {
	normalized := strings.TrimSpace(modelID)
	for i, item := range p.items {
		if !item.isHeader && strings.EqualFold(item.entry.Selector, normalized) {
			return i
		}
	}
	return -1
}

// cursorToLine maps the current cursor position to a line index in rendered output.
// This accounts for blank separator lines (each item = 1 line).
func (p *ModelPicker) cursorToLine() int {
	// Since each item maps to exactly one rendered line, cursor == line index.
	if p.cursor < 0 {
		return 0
	}
	if p.cursor >= len(p.items) {
		return len(p.items) - 1
	}
	return p.cursor
}

// formatTokens formats a token count into a short human-readable string.
func formatTokens(tokens int) string {
	if tokens <= 0 {
		return "  -"
	}
	if tokens >= 1000000 {
		m := float64(tokens) / 1000000.0
		if m == float64(int(m)) {
			return fmt.Sprintf("%dM", int(m))
		}
		return fmt.Sprintf("%.1fM", m)
	}
	if tokens >= 1000 {
		k := tokens / 1000
		return fmt.Sprintf("%dk", k)
	}
	return fmt.Sprintf("%d", tokens)
}

// renderCostTier renders a cost tier as a short label.
func renderCostTier(tier string) string {
	switch tier {
	case "premium":
		return "$$$"
	case "standard":
		return "$$"
	case "budget":
		return "$"
	case "free":
		return "free"
	default:
		return ""
	}
}
