package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// MCPApprovalResponse represents the user's decision on an MCP server approval request.
type MCPApprovalResponse int

const (
	// MCPApprovalAllow approves the MCP server for the current session only.
	MCPApprovalAllow MCPApprovalResponse = iota
	// MCPApprovalAllowAlways persists approval for this server across sessions.
	MCPApprovalAllowAlways
	// MCPApprovalDeny rejects the MCP server connection/import.
	MCPApprovalDeny
)

// MCPApprovalRequest describes an MCP server seeking user approval.
// It is sent from the MCP subsystem to the TUI via channel or tea.Msg.
type MCPApprovalRequest struct {
	// ServerName is the unique identifier/display name of the MCP server.
	ServerName string
	// Source is the command or URL used to connect to the server.
	Source string
	// Tools lists tool names the server offers (may be empty if not yet discovered).
	Tools []string
}

// mcpApprovalOption is a selectable item in the MCP approval dialog.
type mcpApprovalOption struct {
	label    string
	response MCPApprovalResponse
}

// MCPApprovalDialog renders a modal overlay for MCP server approval.
// It follows the same pattern as PermissionDialog: a request comes in,
// the dialog renders, and the user selects an option. The result is sent
// back via a response channel.
type MCPApprovalDialog struct {
	visible     bool
	request     MCPApprovalRequest
	responseCh  chan<- MCPApprovalResponse
	styles      Styles
	environment RenderEnvironment
	geometry    modalFrameGeometry

	options     []mcpApprovalOption
	selectedIdx int
}

// NewMCPApprovalDialog creates a new MCP approval dialog.
func NewMCPApprovalDialog(styles Styles) *MCPApprovalDialog {
	return &MCPApprovalDialog{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
	}
}

func (d *MCPApprovalDialog) SetStyles(styles Styles) {
	d.SetRenderEnvironment(d.environment.withStyles(styles))
}

func (d *MCPApprovalDialog) SetRenderEnvironment(env RenderEnvironment) {
	d.environment = env.normalized()
	d.styles = d.environment.styles
}

// Visible returns whether the dialog is currently shown.
func (d *MCPApprovalDialog) Visible() bool {
	return d.visible
}

// Show displays the MCP approval dialog with the given request.
func (d *MCPApprovalDialog) Show(req MCPApprovalRequest, responseCh chan<- MCPApprovalResponse) {
	d.visible = true
	d.request = req
	d.responseCh = responseCh
	d.selectedIdx = 0
	d.options = []mcpApprovalOption{
		{label: "Allow (this session)", response: MCPApprovalAllow},
		{label: "Always allow", response: MCPApprovalAllowAlways},
		{label: "Deny", response: MCPApprovalDeny},
	}
}

// HandleKey processes key events for the MCP approval dialog.
// Returns (done, cmd) where done means the dialog should close.
func (d *MCPApprovalDialog) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Esc defaults to Deny (safe default)
		d.respond(MCPApprovalDeny)
		return true, nil

	case "up", "k":
		if d.selectedIdx > 0 {
			d.selectedIdx--
		} else {
			d.selectedIdx = len(d.options) - 1 // wrap
		}
		return false, nil

	case "down", "j":
		if d.selectedIdx < len(d.options)-1 {
			d.selectedIdx++
		} else {
			d.selectedIdx = 0 // wrap
		}
		return false, nil

	case "enter":
		if d.selectedIdx >= 0 && d.selectedIdx < len(d.options) {
			d.respond(d.options[d.selectedIdx].response)
			return true, nil
		}
		return false, nil
	}
	return false, nil
}

// ForceClose dismisses the dialog with a Deny response.
// Used when ctrl+c interrupts the dialog.
func (d *MCPApprovalDialog) ForceClose() {
	d.respond(MCPApprovalDeny)
}

func (d *MCPApprovalDialog) respond(resp MCPApprovalResponse) {
	if d.responseCh != nil {
		d.responseCh <- resp
		d.responseCh = nil
	}
	d.visible = false
}

// Overlay renders the MCP approval dialog on top of the existing view.
// The dialog is centered in the terminal as a bordered modal.
func (d *MCPApprovalDialog) Overlay(base string, width, height int) string {
	d.geometry = modalFrameGeometry{}
	if !d.visible {
		return base
	}
	profile := d.environment.normalized().profile

	// Determine dialog inner width (responsive to terminal width)
	dialogWidth := width - 8
	if dialogWidth > 60 {
		dialogWidth = 60
	}
	if dialogWidth < 30 {
		dialogWidth = 30
	}

	// Build dialog content lines
	var lines []string

	// Title
	title := d.styles.Warning.Bold(true).Render("MCP Server Approval")
	lines = append(lines, title)
	lines = append(lines, "")

	// Server info
	serverLine := fmt.Sprintf("  Server: %s", d.styles.Bold.Render(d.request.ServerName))
	lines = append(lines, serverLine)

	if d.request.Source != "" {
		// Truncate source if too long
		maxSourceLen := dialogWidth - 12
		if maxSourceLen < 10 {
			maxSourceLen = 10
		}
		source := modalEllipsize(
			profile,
			d.request.Source,
			maxSourceLen,
			10,
			"...",
		)
		sourceLine := fmt.Sprintf("  Source: %s", d.styles.Subtle.Render(source))
		lines = append(lines, sourceLine)
	}

	// Tools list (if any)
	if len(d.request.Tools) > 0 {
		lines = append(lines, "")
		lines = append(lines, "  "+d.styles.Subtle.Render("Tools offered:"))
		maxTools := 8
		for i, tool := range d.request.Tools {
			if i >= maxTools {
				remaining := len(d.request.Tools) - maxTools
				lines = append(lines, fmt.Sprintf("    %s", d.styles.Subtle.Render(fmt.Sprintf("... and %d more", remaining))))
				break
			}
			lines = append(lines, fmt.Sprintf("    %s %s", d.styles.Subtle.Render("\u2022"), tool))
		}
	}

	// Warning notice
	lines = append(lines, "")
	warningIcon := d.styles.Warning.Render("\u26a0")
	warningText := d.styles.Warning.Render("MCP servers may execute code or access")
	lines = append(lines, fmt.Sprintf("  %s %s", warningIcon, warningText))
	lines = append(lines, "    "+d.styles.Warning.Render("system resources. All tool calls require"))
	lines = append(lines, "    "+d.styles.Warning.Render("approval."))

	// Options
	lines = append(lines, "")
	for i, opt := range d.options {
		if i == d.selectedIdx {
			pointer := d.styles.DialogTitle.Render("\u276f")
			lines = append(lines, fmt.Sprintf("  %s %s", pointer, d.styles.Highlight.Render(opt.label)))
		} else {
			lines = append(lines, fmt.Sprintf("    %s", d.styles.Subtle.Render(opt.label)))
		}
	}

	// Help
	lines = append(lines, "")
	help := d.styles.DialogHelp.Render("  \u2191/\u2193 navigate \u00b7 enter select \u00b7 esc deny")
	lines = append(lines, help)

	// Build bordered box
	content := strings.Join(lines, "\n")

	// Use lipgloss border for the modal
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(d.styles.Warning.GetForeground()).
		Padding(1, 2)

	box := contentRenderStyleWidth(profile, boxStyle, dialogWidth, content)

	// The legacy approval surface intentionally obscures the base frame. The
	// shared helper preserves that behavior while owning final box measurement
	// and origin-aware centering.
	view, geometry := modalCenteredOverlay(profile, "", box, width, height)
	d.geometry = geometry
	return view
}

// mcpApprovalRequestMsg is the Bubble Tea message that triggers the MCP approval dialog.
// It is sent from the MCP connection flow to the TUI program.
type mcpApprovalRequestMsg struct {
	request    MCPApprovalRequest
	responseCh chan<- MCPApprovalResponse
}

// RequestMCPApproval is a helper that sends an MCP approval request to the TUI
// and blocks until the user responds. It is intended to be called from the MCP
// subsystem (on a separate goroutine) to get user consent before connecting
// to a server or importing tools.
//
// The caller must have access to the tea.Program instance to send the message.
// Returns the user's decision.
func RequestMCPApproval(program *tea.Program, req MCPApprovalRequest) MCPApprovalResponse {
	if program == nil {
		// No TUI available — default to deny for safety.
		return MCPApprovalDeny
	}

	responseCh := make(chan MCPApprovalResponse, 1)
	program.Send(mcpApprovalRequestMsg{
		request:    req,
		responseCh: responseCh,
	})

	resp := <-responseCh
	return resp
}
