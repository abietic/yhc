package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// PermissionResponse represents the user's decision on a permission request.
type PermissionResponse int

const (
	PermissionAllow PermissionResponse = iota
	PermissionDeny
	PermissionAllowSession
	PermissionAllowAlways
)

// permOption is a selectable item in the permission dialog.
type permOption struct {
	label    string
	response PermissionResponse
}

type permissionKeyMap struct {
	allow        key.Binding
	deny         key.Binding
	allowSession key.Binding
	allowAlways  key.Binding
}

func defaultPermissionKeyMap() permissionKeyMap {
	return permissionKeyMap{
		allow:        key.NewBinding(key.WithKeys("a", "y")),
		deny:         key.NewBinding(key.WithKeys("d", "n", "esc")),
		allowSession: key.NewBinding(key.WithKeys("s")),
		allowAlways:  key.NewBinding(key.WithKeys("A")),
	}
}

// PermissionDialog renders a permission prompt overlay with arrow-key selection.
type PermissionDialog struct {
	visible      bool
	toolName     string
	toolInput    string
	sessionScope string
	repeatedTool bool
	message      string
	responseCh   chan<- PermissionResponse
	styles       Styles
	environment  RenderEnvironment
	geometry     modalFrameGeometry
	keyMap       permissionKeyMap

	options     []permOption
	selectedIdx int

	// Feedback text: Tab toggles editing, typed chars go to feedback.
	feedback     string
	feedbackMode bool
}

func NewPermissionDialog(styles Styles) *PermissionDialog {
	return &PermissionDialog{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
		keyMap:      defaultPermissionKeyMap(),
	}
}

func (d *PermissionDialog) SetStyles(styles Styles) {
	d.SetRenderEnvironment(d.environment.withStyles(styles))
}

func (d *PermissionDialog) SetRenderEnvironment(env RenderEnvironment) {
	d.environment = env.normalized()
	d.styles = d.environment.styles
}

// buildOptions returns tool-specific option labels.
func buildOptions(toolName, sessionScope string) []permOption {
	opts := []permOption{
		{label: "Yes", response: PermissionAllow},
	}

	// Session-scope option with tool-aware label
	if sessionScope != "" {
		opts = append(opts, permOption{
			label:    fmt.Sprintf("Yes, and don't ask again for %s", sessionScope),
			response: PermissionAllowSession,
		})
	} else {
		switch toolName {
		case "Bash":
			opts = append(opts, permOption{
				label:    "Yes, and don't ask again for this command",
				response: PermissionAllowSession,
			})
		default:
			opts = append(opts, permOption{
				label:    fmt.Sprintf("Yes, and don't ask again for %s", toolName),
				response: PermissionAllowSession,
			})
		}
	}

	opts = append(opts, permOption{label: "No", response: PermissionDeny})
	return opts
}

// Show displays the permission dialog.
func (d *PermissionDialog) Show(toolName, toolInput, sessionScope string, responseCh chan<- PermissionResponse) {
	d.visible = true
	d.toolName = toolName
	d.toolInput = toolInput
	d.sessionScope = sessionScope
	d.repeatedTool = false
	d.message = ""
	d.responseCh = responseCh
	d.options = buildOptions(toolName, sessionScope)
	d.selectedIdx = 0
	d.feedback = ""
	d.feedbackMode = false
}

// ShowRepeatedTool displays a one-call override prompt for a repeated identical
// tool call. Its choices intentionally cannot persist a permission rule.
func (d *PermissionDialog) ShowRepeatedTool(toolName, message string, attempt int, responseCh chan<- PermissionResponse) {
	d.visible = true
	d.toolName = toolName
	d.toolInput = ""
	d.sessionScope = ""
	d.repeatedTool = true
	d.message = strings.TrimSpace(message)
	if attempt > 0 {
		d.message = fmt.Sprintf("Attempt %d: %s", attempt, d.message)
	}
	d.responseCh = responseCh
	d.options = []permOption{
		{label: "Run this call once", response: PermissionAllow},
		{label: "Stop and change strategy", response: PermissionDeny},
	}
	d.selectedIdx = 0
	d.feedback = ""
	d.feedbackMode = false
}

// HandleKey processes key events for the dialog.
// Returns (done, cmd) where done means the dialog should close.
func (d *PermissionDialog) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	// Tab toggles feedback mode
	if msg.String() == "tab" {
		d.feedbackMode = !d.feedbackMode
		return false, nil
	}

	// Esc exits feedback mode, or denies permission
	if msg.String() == "esc" {
		if d.feedbackMode {
			d.feedbackMode = false
			return false, nil
		}
		d.respond(PermissionDeny)
		return true, nil
	}

	// In feedback mode, handle text input
	if d.feedbackMode {
		switch msg.String() {
		case "backspace":
			if len(d.feedback) > 0 {
				d.feedback = d.feedback[:len(d.feedback)-1]
			}
		case "enter":
			// Exit feedback mode (feedback text preserved)
			d.feedbackMode = false
		default:
			// Append printable characters
			if msg.Text != "" {
				d.feedback += msg.Text
			}
		}
		return false, nil
	}

	switch {
	// Arrow-key navigation
	case msg.String() == "up" || msg.String() == "k":
		if d.selectedIdx > 0 {
			d.selectedIdx--
		} else {
			d.selectedIdx = len(d.options) - 1 // wrap
		}
		return false, nil
	case msg.String() == "down" || msg.String() == "j":
		if d.selectedIdx < len(d.options)-1 {
			d.selectedIdx++
		} else {
			d.selectedIdx = 0 // wrap
		}
		return false, nil
	case msg.String() == "enter":
		if d.selectedIdx >= 0 && d.selectedIdx < len(d.options) {
			d.respond(d.options[d.selectedIdx].response)
			return true, nil
		}
		return false, nil

	// Single-key accelerators (kept for power users)
	case key.Matches(msg, d.keyMap.allow):
		d.respond(PermissionAllow)
		return true, nil
	case key.Matches(msg, d.keyMap.deny):
		d.respond(PermissionDeny)
		return true, nil
	case !d.repeatedTool && key.Matches(msg, d.keyMap.allowSession):
		d.respond(PermissionAllowSession)
		return true, nil
	case !d.repeatedTool && key.Matches(msg, d.keyMap.allowAlways):
		d.respond(PermissionAllowAlways)
		return true, nil
	}
	return false, nil
}

// Feedback returns the user-provided feedback text (if any).
func (d *PermissionDialog) Feedback() string {
	return d.feedback
}

// ForceClose dismisses the dialog with a deny response.
// Used when ctrl+c interrupts a permission prompt.
func (d *PermissionDialog) ForceClose() {
	d.respond(PermissionDeny)
}

// dismissWithoutResponse hides presentation while its owner retains the
// runtime request. The coordinator owns the detached response channel.
func (d *PermissionDialog) dismissWithoutResponse() {
	d.responseCh = nil
	d.visible = false
}

func (d *PermissionDialog) respond(resp PermissionResponse) {
	if d.responseCh != nil {
		d.responseCh <- resp
		d.responseCh = nil
	}
	d.visible = false
}

// dialogTitle returns a tool-specific title for the permission dialog.
func dialogTitle(toolName string) string {
	switch toolName {
	case "Bash":
		return "Bash command"
	case "Read", "Write", "Edit", "Glob", "Grep":
		return "File access"
	case "Agent":
		return "Agent dispatch"
	default:
		return "Tool use"
	}
}

// Overlay renders the dialog on top of the existing view.
func (d *PermissionDialog) Overlay(base string, width, height int) string {
	d.geometry = modalFrameGeometry{}
	if !d.visible {
		return base
	}
	profile := d.environment.normalized().profile

	// Build inline-style permission prompt (reference: top border only, permission color)
	topBorder := d.styles.DialogTitle.Render(strings.Repeat("─", max(0, width)))

	titleText := dialogTitle(d.toolName)
	if d.repeatedTool {
		titleText = "Repeated tool call"
	}
	title := d.styles.DialogTitle.Render("  " + titleText)

	// Tool-specific content display
	// Reference: BashPermissionRequest shows full command,
	// FileEditPermissionRequest shows file path + diff, etc.
	toolLines := d.renderToolContent(width)

	// Cap content height to ensure options are always visible.
	// Reserve space for: top border + title + blank + options + feedback + help + margins
	maxContentLines := height - len(d.options) - 8
	if maxContentLines < 3 {
		maxContentLines = 3
	}
	if len(toolLines) > maxContentLines {
		toolLines = toolLines[:maxContentLines-1]
		toolLines = append(toolLines, d.styles.Subtle.Render("  ... (content truncated, approve to see full output)"))
	}

	// Render selectable options with ❯ indicator
	optLines := make([]string, len(d.options))
	for i, opt := range d.options {
		if i == d.selectedIdx {
			pointer := d.styles.DialogTitle.Render("❯")
			optLines[i] = fmt.Sprintf("  %s %s", pointer, d.styles.Highlight.Render(opt.label))
		} else {
			optLines[i] = fmt.Sprintf("    %s", d.styles.Subtle.Render(opt.label))
		}
	}

	help := d.styles.DialogHelp.Render("  ↑/↓ navigate · enter select · tab feedback · esc cancel")

	var parts []string
	parts = append(parts, topBorder)
	parts = append(parts, title)
	parts = append(parts, toolLines...)
	parts = append(parts, "")
	parts = append(parts, optLines...)
	// Show feedback input when active or has content
	if d.feedbackMode || d.feedback != "" {
		fbLabel := "  Feedback: "
		cursor := ""
		if d.feedbackMode {
			cursor = "█"
		}
		fbLine := d.styles.Subtle.Render(fbLabel) + d.feedback + cursor
		parts = append(parts, fbLine)
	}
	parts = append(parts, "")
	parts = append(parts, help)

	view, geometry := modalBottomOverlay(profile, base, parts, width, height)
	d.geometry = geometry
	return view
}

// renderToolContent returns tool-specific content lines for the permission dialog.
// Reference: BashPermissionRequest shows the full command prominently,
// FileEditPermissionRequest shows file path, etc.
func (d *PermissionDialog) renderToolContent(width int) []string {
	profile := d.environment.normalized().profile
	if d.repeatedTool {
		lines := []string{"  " + d.styles.ToolName.Render(d.toolName)}
		if d.message != "" {
			lines = append(lines, "    "+d.styles.Subtle.Render(d.message))
		}
		return lines
	}

	var params map[string]any
	if d.toolInput != "" && d.toolInput != "{}" {
		_ = json.Unmarshal([]byte(d.toolInput), &params)
	}

	maxWidth := width - 6
	if maxWidth < 20 {
		maxWidth = 20
	}

	switch d.toolName {
	case "Bash":
		cmd, _ := params["command"].(string)
		if cmd == "" {
			return []string{"  " + d.styles.ToolName.Render("Bash")}
		}
		// Show full command in a visible block (reference: BashPermissionRequest)
		lines := []string{"  " + d.styles.ToolName.Render("Bash")}
		cmdLines := strings.Split(cmd, "\n")
		for _, cl := range cmdLines {
			cl = modalEllipsize(
				profile,
				cl,
				maxWidth,
				4,
				"...",
			)
			lines = append(lines, "    "+d.styles.Bold.Render(cl))
		}
		if desc, ok := params["description"].(string); ok && desc != "" {
			lines = append(lines, "    "+d.styles.Subtle.Render(desc))
		}
		return lines

	case "Read":
		fp, _ := params["file_path"].(string)
		return []string{"  " + d.styles.ToolName.Render("Read") + " " + shortenPath(fp)}

	case "Write":
		fp, _ := params["file_path"].(string)
		lines := []string{"  " + d.styles.ToolName.Render("Write") + " " + shortenPath(fp)}
		// Show first few lines of content being written
		if content, ok := params["content"].(string); ok && content != "" {
			contentLines := strings.Split(content, "\n")
			max := 8
			if len(contentLines) < max {
				max = len(contentLines)
			}
			for _, cl := range contentLines[:max] {
				cl = modalEllipsize(
					profile,
					cl,
					maxWidth,
					6,
					"...",
				)
				lines = append(lines, "    "+d.styles.DiffAdded.Render("+ "+cl))
			}
			if len(contentLines) > 8 {
				lines = append(lines, "    "+d.styles.Subtle.Render(fmt.Sprintf("… +%d more lines", len(contentLines)-8)))
			}
		}
		return lines

	case "Edit":
		fp, _ := params["file_path"].(string)
		lines := []string{"  " + d.styles.ToolName.Render("Edit") + " " + shortenPath(fp)}
		// Show old_string → new_string diff inline
		oldStr, _ := params["old_string"].(string)
		newStr, _ := params["new_string"].(string)
		if oldStr != "" || newStr != "" {
			oldLines := strings.Split(oldStr, "\n")
			newLines := strings.Split(newStr, "\n")
			maxShow := 6
			for i, ol := range oldLines {
				if i >= maxShow {
					lines = append(lines, "    "+d.styles.Subtle.Render(fmt.Sprintf("… -%d more lines", len(oldLines)-maxShow)))
					break
				}
				ol = modalEllipsize(
					profile,
					ol,
					max(1, maxWidth-4),
					6,
					"...",
				)
				lines = append(lines, "    "+d.styles.DiffRemoved.Render("- "+ol))
			}
			for i, nl := range newLines {
				if i >= maxShow {
					lines = append(lines, "    "+d.styles.Subtle.Render(fmt.Sprintf("… +%d more lines", len(newLines)-maxShow)))
					break
				}
				nl = modalEllipsize(
					profile,
					nl,
					max(1, maxWidth-4),
					6,
					"...",
				)
				lines = append(lines, "    "+d.styles.DiffAdded.Render("+ "+nl))
			}
		}
		return lines

	default:
		args := formatToolArgs(d.toolName, d.toolInput)
		if args != "" {
			return []string{fmt.Sprintf("  %s(%s)", d.styles.ToolName.Render(d.toolName), args)}
		}
		return []string{"  " + d.styles.ToolName.Render(d.toolName)}
	}
}
