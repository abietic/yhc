package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

const maxAgentThreadPickerRows = 128

// AgentThreadPicker selects conversation threads, not Agent definitions. The
// existing `/agent create|list|edit` command surface remains unchanged.
type AgentThreadPicker struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	input       textinput.Model

	items      []engine.RuntimeThreadCatalogEntry
	filtered   []int
	cursor     int
	offset     int
	activeID   string
	leaderID   string
	lastWidth  int
	lastHeight int
}

func NewAgentThreadPicker(styles Styles) *AgentThreadPicker {
	input := textinput.New()
	input.Placeholder = "Search Agent threads"
	input.Prompt = "/ "
	input.CharLimit = 256
	return &AgentThreadPicker{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
		input:       input,
	}
}

func (p *AgentThreadPicker) SetStyles(styles Styles) {
	p.SetRenderEnvironment(p.environment.withStyles(styles))
}

func (p *AgentThreadPicker) SetRenderEnvironment(env RenderEnvironment) {
	p.environment = env.normalized()
	p.styles = p.environment.styles
}

func (p *AgentThreadPicker) Show(items []engine.RuntimeThreadCatalogEntry, activeID, leaderID string) {
	p.visible = true
	p.items = stableThreadCatalogOrder(items, leaderID)
	if len(p.items) > maxAgentThreadPickerRows {
		p.items = append([]engine.RuntimeThreadCatalogEntry(nil), p.items[:maxAgentThreadPickerRows]...)
	}
	p.activeID = activeID
	p.leaderID = leaderID
	p.cursor = 0
	p.offset = 0
	p.input.SetValue("")
	p.input.Focus()
	p.refilter(activeID)
}

func (p *AgentThreadPicker) Close() {
	p.visible = false
	p.input.Blur()
	p.items = nil
	p.filtered = nil
	p.cursor = 0
	p.offset = 0
}

func (p *AgentThreadPicker) Visible() bool {
	return p != nil && p.visible
}

func (p *AgentThreadPicker) HandleKey(msg tea.KeyPressMsg) (threadID string, selected, dismissed bool, cmd tea.Cmd) {
	if p == nil || !p.visible {
		return "", false, false, nil
	}
	switch msg.String() {
	case "esc":
		p.Close()
		return "", false, true, nil
	case "up", "ctrl+p":
		p.move(-1)
		return "", false, false, nil
	case "down", "ctrl+n":
		p.move(1)
		return "", false, false, nil
	case "enter":
		item, ok := p.selectedItem()
		if !ok {
			return "", false, false, nil
		}
		threadID = item.ThreadID
		p.Close()
		return threadID, true, true, nil
	default:
		before := p.input.Value()
		p.input, cmd = p.input.Update(msg)
		if p.input.Value() != before {
			p.refilter("")
		}
		return "", false, false, cmd
	}
}

func (p *AgentThreadPicker) selectedItem() (engine.RuntimeThreadCatalogEntry, bool) {
	if p == nil || p.cursor < 0 || p.cursor >= len(p.filtered) {
		return engine.RuntimeThreadCatalogEntry{}, false
	}
	index := p.filtered[p.cursor]
	if index < 0 || index >= len(p.items) {
		return engine.RuntimeThreadCatalogEntry{}, false
	}
	return p.items[index], true
}

func (p *AgentThreadPicker) move(delta int) {
	if len(p.filtered) == 0 {
		p.cursor = 0
		return
	}
	p.cursor = (p.cursor + delta + len(p.filtered)) % len(p.filtered)
	p.ensureVisible(p.maxVisibleRows())
}

func (p *AgentThreadPicker) refilter(preferredThreadID string) {
	query := strings.ToLower(strings.TrimSpace(p.input.Value()))
	p.filtered = p.filtered[:0]
	preferred := -1
	for index, item := range p.items {
		haystack := strings.ToLower(strings.Join([]string{
			agentThreadLabel(item, p.leaderID), item.ThreadID, item.AgentID, item.Name, item.Description, item.AgentType,
			string(item.Status), string(item.Mode),
		}, "\n"))
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		p.filtered = append(p.filtered, index)
		if item.ThreadID == preferredThreadID {
			preferred = len(p.filtered) - 1
		}
	}
	if preferred >= 0 {
		p.cursor = preferred
	} else {
		p.cursor = 0
	}
	p.offset = 0
	p.ensureVisible(p.maxVisibleRows())
}

func (p *AgentThreadPicker) maxVisibleRows() int {
	rows := p.lastHeight - 10
	if rows < 3 {
		rows = 3
	}
	if rows > 12 {
		rows = 12
	}
	return rows
}

func (p *AgentThreadPicker) ensureVisible(maxVisible int) {
	if maxVisible <= 0 {
		return
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+maxVisible {
		p.offset = p.cursor - maxVisible + 1
	}
	maxOffset := len(p.filtered) - maxVisible
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.offset > maxOffset {
		p.offset = maxOffset
	}
}

func (p *AgentThreadPicker) Overlay(base string, width, height int) string {
	if p == nil || !p.visible {
		return base
	}
	p.lastWidth = width
	p.lastHeight = height
	profile := p.environment.normalized().profile
	dialogWidth := width - 8
	if dialogWidth > 76 {
		dialogWidth = 76
	}
	if dialogWidth < 24 {
		dialogWidth = 24
	}
	if width > 0 && dialogWidth > width {
		dialogWidth = width
	}
	innerWidth := dialogWidth - 4
	p.input.SetWidth(max(12, innerWidth-2))
	maxVisible := p.maxVisibleRows()
	p.ensureVisible(maxVisible)

	parts := []string{
		p.styles.DialogTitle.Render("Agent threads"),
		p.input.View(),
		"",
	}
	if len(p.filtered) == 0 {
		parts = append(parts, p.styles.Subtle.Render("No matching threads"))
	} else {
		end := min(len(p.filtered), p.offset+maxVisible)
		for visibleIndex := p.offset; visibleIndex < end; visibleIndex++ {
			item := p.items[p.filtered[visibleIndex]]
			line := p.renderItemWithProfile(profile, item, innerWidth)
			if visibleIndex == p.cursor {
				line = p.styles.Selected.Render(line)
			}
			parts = append(parts, line)
		}
		if len(p.filtered) > maxVisible {
			parts = append(parts, p.styles.Subtle.Render(fmt.Sprintf("%d-%d of %d", p.offset+1, end, len(p.filtered))))
		}
	}
	parts = append(parts, "", p.styles.DialogHelp.Render("Up/Down navigate  Enter open  Esc close"))
	dialog := contentRenderStyleWidth(
		profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
	view, _ := modalCenteredOverlay(profile, base, dialog, width, height)
	return view
}

func (p *AgentThreadPicker) renderItem(
	item engine.RuntimeThreadCatalogEntry,
	width int,
) string {
	return p.renderItemWithProfile(
		p.environment.normalized().profile,
		item,
		width,
	)
}

func (p *AgentThreadPicker) renderItemWithProfile(
	profile DisplayCellProfile,
	item engine.RuntimeThreadCatalogEntry,
	width int,
) string {
	active := " "
	if item.ThreadID == p.activeID {
		active = "●"
	}
	status := agentThreadStatusIcon(item.Status)
	label := agentThreadLabel(item, p.leaderID)
	meta := string(item.Status)
	switch item.Mode {
	case engine.ThreadModeReplayOnly:
		meta += " · replay"
	case engine.ThreadModeEvictedTranscript:
		meta += " · disk"
	}
	attention := item.PermissionCount + item.QuestionCount
	if attention > 0 {
		meta += fmt.Sprintf(" · !%d", attention)
	}
	line := fmt.Sprintf("%s %s %-20s %s", active, status, label, meta)
	return modalEllipsize(profile, line, width, 0, "...")
}

func agentThreadStatusIcon(status engine.RuntimeThreadStatus) string {
	switch status {
	case engine.RuntimeThreadRunning:
		return "●"
	case engine.RuntimeThreadPaused:
		return "Ⅱ"
	case engine.RuntimeThreadWaitingInput:
		return "!"
	case engine.RuntimeThreadCompleted:
		return "✓"
	case engine.RuntimeThreadFailed, engine.RuntimeThreadAborted:
		return "×"
	default:
		return "○"
	}
}

func agentThreadLabel(item engine.RuntimeThreadCatalogEntry, leaderID string) string {
	if item.ThreadID == leaderID || item.AgentID == "" {
		return "main"
	}
	for _, label := range []string{item.Name, item.Description, item.AgentType, item.AgentID, item.ThreadID} {
		if strings.TrimSpace(label) != "" {
			return label
		}
	}
	return "agent"
}

func stableThreadCatalogOrder(items []engine.RuntimeThreadCatalogEntry, leaderID string) []engine.RuntimeThreadCatalogEntry {
	ordered := append([]engine.RuntimeThreadCatalogEntry(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		leftLeader := left.ThreadID == leaderID
		rightLeader := right.ThreadID == leaderID
		if leftLeader != rightLeader {
			return leftLeader
		}
		if left.StartedAt.Equal(right.StartedAt) {
			return left.ThreadID < right.ThreadID
		}
		return left.StartedAt.Before(right.StartedAt)
	})
	return ordered
}
