package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/tools"
)

// mcpSettingsSubView tracks whether the panel shows the server list or a tool list.
type mcpSettingsSubView int

const (
	mcpViewServerList mcpSettingsSubView = iota
	mcpViewToolList                      // expanded tool list for selected server
)

// mcpServerEntry represents a server in the panel list.
type mcpServerEntry struct {
	name      string
	status    mcp.ServerStatus
	toolCount int
	errorMsg  string
}

// mcpToolEntry represents a tool in the expanded tool list.
type mcpToolEntry struct {
	name        string
	description string
}

// MCPSettingsPanel is a modal overlay that shows configured MCP servers,
// their tools, and management controls (add/remove/restart).
// Triggered by /mcp (without subcommand arguments).
type MCPSettingsPanel struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	geometry    modalFrameGeometry

	subView mcpSettingsSubView

	// Server list state
	servers []mcpServerEntry
	cursor  int
	offset  int

	// Tool list state (expanded)
	selectedServer string
	toolItems      []mcpToolEntry
	toolCursor     int
	toolOffset     int

	// Action feedback message (briefly shown after add/remove/restart)
	feedback string
	manager  *tools.MCPToolManager
}

// NewMCPSettingsPanel creates a new MCP settings panel.
func NewMCPSettingsPanel(styles Styles) *MCPSettingsPanel {
	return &MCPSettingsPanel{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
	}
}

func (p *MCPSettingsPanel) SetStyles(styles Styles) {
	p.SetRenderEnvironment(p.environment.withStyles(styles))
}

func (p *MCPSettingsPanel) SetRenderEnvironment(env RenderEnvironment) {
	p.environment = env.normalized()
	p.styles = p.environment.styles
}

// SetManager binds the panel to the current engine's MCP scope.
func (p *MCPSettingsPanel) SetManager(manager *tools.MCPToolManager) {
	p.manager = manager
}

// Show makes the panel visible and refreshes server data.
func (p *MCPSettingsPanel) Show() {
	p.visible = true
	p.subView = mcpViewServerList
	p.cursor = 0
	p.offset = 0
	p.toolItems = nil
	p.toolCursor = 0
	p.toolOffset = 0
	p.selectedServer = ""
	p.feedback = ""
	p.Refresh()
}

// Close hides the panel.
func (p *MCPSettingsPanel) Close() {
	p.visible = false
}

// Visible returns whether the panel is currently shown.
func (p *MCPSettingsPanel) Visible() bool {
	return p.visible
}

// Refresh reloads server data from the MCP tool manager.
func (p *MCPSettingsPanel) Refresh() {
	mgr := p.manager
	if mgr == nil {
		p.servers = nil
		return
	}

	serverNames := mgr.ListConnectedServerNames()
	sort.Strings(serverNames)

	entries := make([]mcpServerEntry, 0, len(serverNames))
	for _, name := range serverNames {
		status, err := mgr.ServerStatus(name)
		var errMsg string
		if err != nil {
			status = mcp.StatusError
			errMsg = err.Error()
		}
		toolCount := mgr.ServerToolCount(name)

		entries = append(entries, mcpServerEntry{
			name:      name,
			status:    status,
			toolCount: toolCount,
			errorMsg:  errMsg,
		})
	}

	p.servers = entries

	// Clamp cursor
	if p.cursor >= len(p.servers) {
		p.cursor = len(p.servers) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// HandleKey processes key events for the MCP settings panel.
// Returns (dismissed, actionCmd) where dismissed means close panel and
// actionCmd is a tea.Cmd to execute (for async actions).
func (p *MCPSettingsPanel) HandleKey(msg tea.KeyPressMsg, viewHeight int) (dismissed bool, actionCmd tea.Cmd) {
	if p.subView == mcpViewToolList {
		return p.handleToolListKey(msg, viewHeight)
	}
	return p.handleServerListKey(msg, viewHeight)
}

func (p *MCPSettingsPanel) handleServerListKey(msg tea.KeyPressMsg, viewHeight int) (bool, tea.Cmd) {
	maxVisible := viewHeight - 10
	if maxVisible < 3 {
		maxVisible = 3
	}

	switch msg.String() {
	case "esc", "q":
		p.Close()
		return true, nil

	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
			p.adjustServerScroll(maxVisible)
		}
		return false, nil

	case "down", "j":
		if p.cursor < len(p.servers)-1 {
			p.cursor++
			p.adjustServerScroll(maxVisible)
		}
		return false, nil

	case "enter":
		if len(p.servers) > 0 && p.cursor < len(p.servers) {
			p.enterToolList()
		}
		return false, nil

	case "a":
		// Add server — delegate to /mcp add command (notify user)
		p.feedback = "Use /mcp add <name> <command> [args...] to add a server"
		return false, nil

	case "d":
		if len(p.servers) > 0 && p.cursor < len(p.servers) {
			return false, p.removeSelectedServer()
		}
		return false, nil

	case "r":
		if len(p.servers) > 0 && p.cursor < len(p.servers) {
			return false, p.restartSelectedServer()
		}
		return false, nil

	case "pgup":
		p.cursor -= maxVisible
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.adjustServerScroll(maxVisible)
		return false, nil

	case "pgdown":
		p.cursor += maxVisible
		if p.cursor >= len(p.servers) {
			p.cursor = len(p.servers) - 1
		}
		if p.cursor < 0 {
			p.cursor = 0
		}
		p.adjustServerScroll(maxVisible)
		return false, nil
	}

	return false, nil
}

func (p *MCPSettingsPanel) handleToolListKey(msg tea.KeyPressMsg, viewHeight int) (bool, tea.Cmd) {
	maxVisible := viewHeight - 8
	if maxVisible < 3 {
		maxVisible = 3
	}

	switch msg.String() {
	case "esc", "q":
		// Go back to server list
		p.subView = mcpViewServerList
		p.toolItems = nil
		p.toolCursor = 0
		p.toolOffset = 0
		return false, nil

	case "up", "k":
		if p.toolCursor > 0 {
			p.toolCursor--
			p.adjustToolScroll(maxVisible)
		}
		return false, nil

	case "down", "j":
		if p.toolCursor < len(p.toolItems)-1 {
			p.toolCursor++
			p.adjustToolScroll(maxVisible)
		}
		return false, nil

	case "pgup":
		p.toolCursor -= maxVisible
		if p.toolCursor < 0 {
			p.toolCursor = 0
		}
		p.adjustToolScroll(maxVisible)
		return false, nil

	case "pgdown":
		p.toolCursor += maxVisible
		if p.toolCursor >= len(p.toolItems) {
			p.toolCursor = len(p.toolItems) - 1
		}
		if p.toolCursor < 0 {
			p.toolCursor = 0
		}
		p.adjustToolScroll(maxVisible)
		return false, nil
	}

	return false, nil
}

func (p *MCPSettingsPanel) adjustServerScroll(maxVisible int) {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+maxVisible {
		p.offset = p.cursor - maxVisible + 1
	}
}

func (p *MCPSettingsPanel) adjustToolScroll(maxVisible int) {
	if p.toolCursor < p.toolOffset {
		p.toolOffset = p.toolCursor
	}
	if p.toolCursor >= p.toolOffset+maxVisible {
		p.toolOffset = p.toolCursor - maxVisible + 1
	}
}

func (p *MCPSettingsPanel) enterToolList() {
	server := p.servers[p.cursor]
	p.selectedServer = server.name
	p.subView = mcpViewToolList
	p.toolCursor = 0
	p.toolOffset = 0

	mgr := p.manager
	if mgr == nil {
		p.toolItems = nil
		return
	}

	serverTools := mgr.ServerTools(server.name)
	sort.Slice(serverTools, func(i, j int) bool {
		return serverTools[i].ToolName < serverTools[j].ToolName
	})

	items := make([]mcpToolEntry, 0, len(serverTools))
	for _, t := range serverTools {
		items = append(items, mcpToolEntry{
			name:        t.ToolName,
			description: t.Description,
		})
	}
	p.toolItems = items
}

func (p *MCPSettingsPanel) removeSelectedServer() tea.Cmd { //nolint:unparam
	if p.cursor >= len(p.servers) {
		return nil
	}
	server := p.servers[p.cursor]

	mgr := p.manager
	if mgr != nil {
		_ = mgr.DisconnectServer(server.name)
	}

	p.feedback = fmt.Sprintf("Removed server %q", server.name)
	p.Refresh()
	return nil
}

func (p *MCPSettingsPanel) restartSelectedServer() tea.Cmd { //nolint:unparam
	if p.cursor >= len(p.servers) {
		return nil
	}
	server := p.servers[p.cursor]

	mgr := p.manager
	if mgr == nil {
		p.feedback = "No MCP manager available"
		return nil
	}

	if err := mgr.ReconnectServer(context.Background(), server.name); err != nil {
		p.feedback = fmt.Sprintf("Restart failed: %v", err)
	} else {
		p.feedback = fmt.Sprintf("Restarted %q", server.name)
	}

	p.Refresh()
	return nil
}

// Overlay renders the MCP settings panel on top of the base view.
func (p *MCPSettingsPanel) Overlay(base string, width, height int) string {
	p.geometry = modalFrameGeometry{}
	if !p.visible {
		return base
	}
	profile := p.environment.normalized().profile

	// Refresh on every render for live status updates
	p.Refresh()

	dialogWidth := width - 6
	if dialogWidth > 72 {
		dialogWidth = 72
	}
	if dialogWidth < 44 {
		dialogWidth = 44
	}

	var dialog string
	if p.subView == mcpViewToolList {
		dialog = p.renderToolListView(dialogWidth, height)
	} else {
		dialog = p.renderServerListView(dialogWidth, height)
	}

	view, geometry := modalCenteredOverlay(profile, base, dialog, width, height)
	p.geometry = geometry
	return view
}

func (p *MCPSettingsPanel) renderServerListView(dialogWidth, height int) string {
	maxContentHeight := height - 8
	if maxContentHeight < 5 {
		maxContentHeight = 5
	}

	var parts []string

	// Title
	title := p.styles.DialogTitle.Render("  MCP Servers")
	if len(p.servers) > 0 {
		title += p.styles.Subtle.Render(fmt.Sprintf(" (%d)", len(p.servers)))
	}
	parts = append(parts, title)
	parts = append(parts, "")

	// Content
	if len(p.servers) == 0 {
		parts = append(parts, p.styles.Subtle.Render("  No MCP servers configured"))
		parts = append(parts, "")
		parts = append(parts, p.styles.Subtle.Render("  Use /mcp add <name> <command> [args...]"))
		parts = append(parts, p.styles.Subtle.Render("  or configure .mcp.json"))
		parts = append(parts, "")
	} else {
		// Build all server lines
		allLines := p.renderServerItems(dialogWidth)

		// Scroll window
		p.adjustServerScroll(maxContentHeight)

		end := p.offset + maxContentHeight
		if end > len(allLines) {
			end = len(allLines)
		}
		start := p.offset
		if start > len(allLines) {
			start = len(allLines)
		}

		visible := allLines[start:end]
		parts = append(parts, visible...)

		// Scroll indicator
		if len(allLines) > maxContentHeight {
			pct := 0
			denom := len(allLines) - maxContentHeight
			if denom > 0 {
				pct = (p.offset * 100) / denom
			}
			parts = append(parts, p.styles.Subtle.Render(fmt.Sprintf("  (%d%%)", pct)))
		}
	}

	// Feedback line
	if p.feedback != "" {
		parts = append(parts, "")
		parts = append(parts, "  "+p.styles.Warning.Render(p.feedback))
	}

	// Footer
	parts = append(parts, "")
	parts = append(parts, strings.Repeat("\u2500", dialogWidth-4))
	helpText := p.styles.DialogHelp.Render(
		"  \u2191\u2193 navigate \u00b7 Enter tools \u00b7 a add \u00b7 d remove \u00b7 r restart \u00b7 Esc close",
	)
	parts = append(parts, helpText)

	return contentRenderStyleWidth(
		p.environment.normalized().profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
}

func (p *MCPSettingsPanel) renderToolListView(dialogWidth, height int) string {
	maxContentHeight := height - 8
	if maxContentHeight < 5 {
		maxContentHeight = 5
	}

	var parts []string

	// Title
	title := p.styles.DialogTitle.Render("  " + p.selectedServer + " \u2014 Tools")
	if len(p.toolItems) > 0 {
		title += p.styles.Subtle.Render(fmt.Sprintf(" (%d)", len(p.toolItems)))
	}
	parts = append(parts, title)
	parts = append(parts, "")

	// Content
	if len(p.toolItems) == 0 {
		parts = append(parts, p.styles.Subtle.Render("  No tools available"))
		parts = append(parts, "")
	} else {
		// Build all tool lines
		allLines := p.renderToolItems(dialogWidth)

		// Scroll window
		p.adjustToolScroll(maxContentHeight)

		end := p.toolOffset + maxContentHeight
		if end > len(allLines) {
			end = len(allLines)
		}
		start := p.toolOffset
		if start > len(allLines) {
			start = len(allLines)
		}

		visible := allLines[start:end]
		parts = append(parts, visible...)

		// Scroll indicator
		if len(allLines) > maxContentHeight {
			pct := 0
			denom := len(allLines) - maxContentHeight
			if denom > 0 {
				pct = (p.toolOffset * 100) / denom
			}
			parts = append(parts, p.styles.Subtle.Render(fmt.Sprintf("  (%d%%)", pct)))
		}
	}

	// Footer
	parts = append(parts, "")
	parts = append(parts, strings.Repeat("\u2500", dialogWidth-4))
	helpText := p.styles.DialogHelp.Render(
		"  \u2191\u2193 navigate \u00b7 Esc back to server list",
	)
	parts = append(parts, helpText)

	return contentRenderStyleWidth(
		p.environment.normalized().profile,
		p.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
}

func (p *MCPSettingsPanel) renderServerItems(dialogWidth int) []string {
	var lines []string
	profile := p.environment.normalized().profile

	for i, server := range p.servers {
		isCursor := i == p.cursor

		// Status icon
		icon := p.serverStatusIcon(server.status)

		// Server name + status detail
		statusDetail := p.serverStatusDetail(server)

		// Compose line
		line := fmt.Sprintf("  %s %s", icon, server.name)
		if statusDetail != "" {
			line += " " + p.styles.Subtle.Render(statusDetail)
		}

		line = modalEllipsize(
			profile,
			line,
			max(1, dialogWidth-4),
			0,
			"...",
		)

		if isCursor {
			line = p.styles.Selected.Render(line)
		}
		lines = append(lines, line)

		// Blank separator between entries
		if i < len(p.servers)-1 {
			lines = append(lines, "")
		}
	}

	return lines
}

func (p *MCPSettingsPanel) renderToolItems(dialogWidth int) []string {
	var lines []string
	profile := p.environment.normalized().profile

	for i, tool := range p.toolItems {
		isCursor := i == p.toolCursor

		// Tool name
		name := tool.name

		// Description (truncated)
		desc := tool.description
		maxDesc := dialogWidth - profile.measure(name, 2) - 12
		if maxDesc < 10 {
			maxDesc = 10
		}
		desc = modalEllipsize(profile, desc, maxDesc, 5+profile.measure(name, 2), "...")

		line := fmt.Sprintf("  %s", name)
		if desc != "" {
			line += " " + p.styles.Subtle.Render("\u2014 "+desc)
		}

		if isCursor {
			line = p.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}

	return lines
}

func (p *MCPSettingsPanel) serverStatusIcon(status mcp.ServerStatus) string {
	switch status {
	case mcp.StatusConnected:
		return p.styles.ToolSuccess.Render("\u25cf") // filled circle (green)
	case mcp.StatusConnecting:
		return p.styles.ToolRunning.Render("\u25cb") // open circle
	case mcp.StatusDisconnected:
		return p.styles.Subtle.Render("\u25cb") // open circle (grey)
	case mcp.StatusError, mcp.StatusFailed:
		return p.styles.ToolError.Render("\u2717") // X mark
	case mcp.StatusDisabled:
		return p.styles.Subtle.Render("\u2212") // minus
	case mcp.StatusNeedsAuth:
		return p.styles.Warning.Render("\u26a0") // warning triangle
	default:
		return p.styles.Subtle.Render("\u25cb")
	}
}

func (p *MCPSettingsPanel) serverStatusDetail(server mcpServerEntry) string {
	switch server.status {
	case mcp.StatusConnected:
		if server.toolCount > 0 {
			return fmt.Sprintf("(connected \u00b7 %d tools)", server.toolCount)
		}
		return "(connected)"
	case mcp.StatusConnecting:
		return "(connecting...)"
	case mcp.StatusDisconnected:
		return "(disconnected)"
	case mcp.StatusError, mcp.StatusFailed:
		if server.errorMsg != "" {
			msg := modalEllipsize(
				p.environment.normalized().profile,
				server.errorMsg,
				30,
				8,
				"...",
			)
			return fmt.Sprintf("(error: %s)", msg)
		}
		return "(error)"
	case mcp.StatusDisabled:
		return "(disabled)"
	case mcp.StatusNeedsAuth:
		return "(needs auth)"
	default:
		return ""
	}
}
