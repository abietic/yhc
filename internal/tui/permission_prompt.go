package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine"
)

// RiskLevel represents the assessed risk of a tool operation.
type RiskLevel int

const (
	RiskLow    RiskLevel = iota // Read-only operations, safe queries
	RiskMedium                  // File modifications, controlled operations
	RiskHigh                    // Shell commands, deletions, network access
)

// String returns a human-readable risk level string.
func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "Low"
	case RiskMedium:
		return "Medium"
	case RiskHigh:
		return "High"
	}
	return "Unknown"
}

// PermissionPromptOption represents a selectable action in the permission prompt.
type PermissionPromptOption struct {
	Label    string
	Shortcut string // keyboard shortcut hint (e.g., "a", "A", "d", "D")
	Response PermissionResponse
}

// PermissionPrompt is an enhanced permission prompt component with risk levels,
// timeout countdown, and structured display of tool information.
//
// This component is intended as the next-generation permission UI, building on
// the existing PermissionDialog with additional features:
//   - Risk level assessment and color coding
//   - Timeout countdown with auto-deny
//   - Structured tool argument display
//   - Context about what the tool wants to do
//   - Numbered options for quick keyboard selection
//
// Reference: claude-code-ripe BashPermissionRequest / FileEditPermissionRequest
type PermissionPrompt struct {
	visible            bool
	toolName           string
	toolInput          string
	inputParams        map[string]any
	sessionScope       string
	riskLevel          RiskLevel
	context            string // human-readable description of what the tool wants to do
	responseCh         chan<- PermissionResponse
	styles             Styles
	decisionConstraint engine.PermissionDecisionConstraint

	options     []PermissionPromptOption
	selectedIdx int

	// Timeout
	timeoutEnabled  bool
	timeoutDuration time.Duration
	timeoutStart    time.Time
	timeoutExpired  bool

	// UI state
	detailsExpanded bool // whether to show full argument details
}

// NewPermissionPrompt creates a new permission prompt component.
func NewPermissionPrompt(styles Styles) *PermissionPrompt {
	return &PermissionPrompt{
		styles: styles,
	}
}

func (p *PermissionPrompt) SetStyles(styles Styles) {
	p.styles = styles
}

// Show displays the permission prompt with the given tool information.
func (p *PermissionPrompt) Show(toolName, toolInput, sessionScope string, responseCh chan<- PermissionResponse) {
	p.visible = true
	p.toolName = toolName
	p.toolInput = toolInput
	p.sessionScope = sessionScope
	p.responseCh = responseCh
	p.selectedIdx = 0
	p.detailsExpanded = false
	p.timeoutExpired = false

	// Parse input params
	p.inputParams = make(map[string]any)
	if toolInput != "" && toolInput != "{}" {
		_ = json.Unmarshal([]byte(toolInput), &p.inputParams)
	}

	// Assess risk level
	p.riskLevel = assessRiskLevel(toolName, p.inputParams)

	// Generate context description
	p.context = generateToolContext(toolName, p.inputParams)

	// Build options
	p.options = buildPromptOptions(toolName, sessionScope, p.decisionConstraint)

	// Set timeout for high-risk operations
	if p.riskLevel == RiskHigh {
		p.timeoutEnabled = true
		p.timeoutDuration = 60 * time.Second
		p.timeoutStart = time.Now()
	} else {
		p.timeoutEnabled = false
	}
}

// ShowWithTimeout displays the permission prompt with an explicit timeout.
func (p *PermissionPrompt) ShowWithTimeout(toolName, toolInput, sessionScope string, timeout time.Duration, responseCh chan<- PermissionResponse) {
	p.Show(toolName, toolInput, sessionScope, responseCh)
	if timeout > 0 {
		p.timeoutEnabled = true
		p.timeoutDuration = timeout
		p.timeoutStart = time.Now()
	}
}

// HandleKey processes key events for the permission prompt.
// Returns (done, cmd) where done means the prompt should close.
func (p *PermissionPrompt) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if !p.visible {
		return false, nil
	}

	switch msg.String() {
	// Escape to deny
	case "esc":
		p.respond(PermissionDeny)
		return true, nil

	// Arrow navigation
	case "up", "k":
		if p.selectedIdx > 0 {
			p.selectedIdx--
		} else {
			p.selectedIdx = len(p.options) - 1
		}
		return false, nil
	case "down", "j":
		if p.selectedIdx < len(p.options)-1 {
			p.selectedIdx++
		} else {
			p.selectedIdx = 0
		}
		return false, nil

	// Enter to select current option
	case "enter":
		if p.selectedIdx >= 0 && p.selectedIdx < len(p.options) {
			p.respond(p.options[p.selectedIdx].Response)
			return true, nil
		}
		return false, nil

	// Number shortcuts (1-4)
	case "1":
		if len(p.options) >= 1 {
			p.respond(p.options[0].Response)
			return true, nil
		}
	case "2":
		if len(p.options) >= 2 {
			p.respond(p.options[1].Response)
			return true, nil
		}
	case "3":
		if len(p.options) >= 3 {
			p.respond(p.options[2].Response)
			return true, nil
		}
	case "4":
		if len(p.options) >= 4 {
			p.respond(p.options[3].Response)
			return true, nil
		}

	// Letter shortcuts
	case "a": // Allow once
		p.respond(PermissionAllow)
		return true, nil
	case "A": // Allow always (session)
		if p.decisionConstraint == engine.PermissionAllowOnceOnly {
			return false, nil
		}
		p.respond(PermissionAllowSession)
		return true, nil
	case "d": // Deny
		p.respond(PermissionDeny)
		return true, nil

	// Toggle details
	case "tab":
		p.detailsExpanded = !p.detailsExpanded
		return false, nil
	}

	return false, nil
}

// Tick updates timeout state. Returns true if timeout just expired.
func (p *PermissionPrompt) Tick() bool {
	if !p.visible || !p.timeoutEnabled || p.timeoutExpired {
		return false
	}
	if time.Since(p.timeoutStart) >= p.timeoutDuration {
		p.timeoutExpired = true
		p.respond(PermissionDeny)
		return true
	}
	return false
}

// ForceClose dismisses the prompt with a deny response.
func (p *PermissionPrompt) ForceClose() {
	if p.visible {
		p.respond(PermissionDeny)
	}
}

// IsVisible returns whether the prompt is currently displayed.
func (p *PermissionPrompt) IsVisible() bool {
	return p.visible
}

// RemainingTimeout returns the remaining timeout duration. Returns 0 if no timeout.
func (p *PermissionPrompt) RemainingTimeout() time.Duration {
	if !p.timeoutEnabled || p.timeoutExpired {
		return 0
	}
	remaining := p.timeoutDuration - time.Since(p.timeoutStart)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (p *PermissionPrompt) respond(resp PermissionResponse) {
	if p.responseCh != nil {
		p.responseCh <- resp
		p.responseCh = nil
	}
	p.visible = false
}

// --- Rendering ---

// Render returns the full rendered permission prompt for overlay display.
func (p *PermissionPrompt) Render(width, height int) string {
	if !p.visible {
		return ""
	}

	dialogWidth := width - 4
	if dialogWidth > 80 {
		dialogWidth = 80
	}
	if dialogWidth < 40 {
		dialogWidth = 40
	}

	contentWidth := dialogWidth - 4

	var lines []string

	// Top border
	borderLine := p.styles.DialogTitle.Render(strings.Repeat("\u2500", dialogWidth))
	lines = append(lines, borderLine)

	// Title with risk indicator
	title := p.renderTitle()
	lines = append(lines, "  "+title)
	lines = append(lines, "")

	// Tool context description
	if p.context != "" {
		contextLines := wrapPermText(p.context, contentWidth)
		for _, cl := range contextLines {
			lines = append(lines, "  "+p.styles.Subtle.Render(cl))
		}
		lines = append(lines, "")
	}

	// Tool details
	detailLines := p.renderToolDetails(contentWidth)
	lines = append(lines, detailLines...)
	lines = append(lines, "")

	// Risk assessment
	riskLine := p.renderRiskBadge()
	lines = append(lines, "  "+riskLine)
	lines = append(lines, "")

	// Options with selection indicator and shortcuts
	for i, opt := range p.options {
		optLine := p.renderOption(i, opt)
		lines = append(lines, optLine)
	}

	// Timeout countdown
	if p.timeoutEnabled && !p.timeoutExpired {
		lines = append(lines, "")
		remaining := p.RemainingTimeout()
		countdown := fmt.Sprintf("Auto-deny in %ds", int(remaining.Seconds()))
		timeoutStyle := p.styles.Warning
		if remaining < 10*time.Second {
			timeoutStyle = p.styles.Error
		}
		lines = append(lines, "  "+timeoutStyle.Render(countdown))
	}

	// Help line
	lines = append(lines, "")
	help := p.styles.DialogHelp.Render("  \u2191/\u2193 navigate \u00b7 enter select \u00b7 1-4 quick pick \u00b7 a/d allow/deny \u00b7 tab details \u00b7 esc cancel")
	lines = append(lines, help)

	return strings.Join(lines, "\n")
}

// Overlay renders the permission prompt on top of the base view.
func (p *PermissionPrompt) Overlay(base string, width, height int) string {
	if !p.visible {
		return base
	}

	dialog := p.Render(width, height)

	// Place dialog at the bottom of the view (inline style, like the existing PermissionDialog)
	baseLines := strings.Split(base, "\n")
	dialogLines := strings.Split(dialog, "\n")

	// Pad base to full height
	for len(baseLines) < height {
		baseLines = append(baseLines, "")
	}

	// Replace bottom lines with dialog
	startY := height - len(dialogLines)
	if startY < 0 {
		startY = 0
	}
	for i, line := range dialogLines {
		y := startY + i
		if y < len(baseLines) {
			baseLines[y] = line
		}
	}

	return strings.Join(baseLines[:height], "\n")
}

// renderTitle renders the dialog title with risk-colored icon.
func (p *PermissionPrompt) renderTitle() string {
	var icon string
	var titleStyle lipgloss.Style

	switch p.riskLevel {
	case RiskLow:
		icon = "\u25cf" // filled circle
		titleStyle = p.styles.ToolSuccess
	case RiskMedium:
		icon = "\u25b2" // triangle
		titleStyle = p.styles.Warning
	case RiskHigh:
		icon = "\u26a0" // warning sign
		titleStyle = p.styles.Error
	}

	toolTitle := permissionToolTitle(p.toolName)
	return titleStyle.Render(icon) + " " + p.styles.DialogTitle.Render(toolTitle)
}

// renderRiskBadge renders a colored risk level indicator.
func (p *PermissionPrompt) renderRiskBadge() string {
	var badgeStyle lipgloss.Style

	switch p.riskLevel {
	case RiskLow:
		badgeStyle = p.styles.ToolSuccess.Bold(true)
	case RiskMedium:
		badgeStyle = p.styles.Warning.Bold(true)
	case RiskHigh:
		badgeStyle = p.styles.Error.Bold(true)
	}

	return "Risk: " + badgeStyle.Render(p.riskLevel.String())
}

// renderOption renders a single selectable option line.
func (p *PermissionPrompt) renderOption(idx int, opt PermissionPromptOption) string {
	number := fmt.Sprintf("%d", idx+1)
	shortcut := ""
	if opt.Shortcut != "" {
		shortcut = " (" + opt.Shortcut + ")"
	}

	if idx == p.selectedIdx {
		pointer := p.styles.DialogTitle.Render("\u276f") // ❯
		label := p.styles.Highlight.Render(opt.Label)
		return fmt.Sprintf("  %s %s. %s%s", pointer, number, label, p.styles.Subtle.Render(shortcut))
	}
	return fmt.Sprintf("    %s. %s%s", p.styles.Subtle.Render(number), p.styles.Subtle.Render(opt.Label), p.styles.Subtle.Render(shortcut))
}

// renderToolDetails renders the tool-specific content.
func (p *PermissionPrompt) renderToolDetails(maxWidth int) []string {
	var lines []string

	switch p.toolName {
	case "Bash":
		cmd, _ := p.inputParams["command"].(string)
		if cmd == "" {
			lines = append(lines, "  "+p.styles.ToolName.Render("Bash"))
			break
		}
		lines = append(lines, "  "+p.styles.ToolName.Render("Command:"))
		cmdLines := strings.Split(cmd, "\n")
		maxShow := 10
		if !p.detailsExpanded && len(cmdLines) > 5 {
			maxShow = 5
		}
		for i, cl := range cmdLines {
			if i >= maxShow {
				remaining := len(cmdLines) - maxShow
				lines = append(lines, "    "+p.styles.Subtle.Render(fmt.Sprintf("... +%d more lines (tab to expand)", remaining)))
				break
			}
			if len(cl) > maxWidth-4 {
				cl = cl[:maxWidth-7] + "..."
			}
			lines = append(lines, "    "+p.styles.Bold.Render(cl))
		}
		if desc, ok := p.inputParams["description"].(string); ok && desc != "" {
			lines = append(lines, "    "+p.styles.Subtle.Render(desc))
		}

	case "Write":
		fp, _ := p.inputParams["file_path"].(string)
		lines = append(lines, "  "+p.styles.ToolName.Render("Write")+" "+shortenPath(fp))
		if content, ok := p.inputParams["content"].(string); ok && content != "" {
			contentLines := strings.Split(content, "\n")
			maxShow := 6
			if p.detailsExpanded {
				maxShow = 20
			}
			if len(contentLines) > maxShow {
				for _, cl := range contentLines[:maxShow] {
					if len(cl) > maxWidth-6 {
						cl = cl[:maxWidth-9] + "..."
					}
					lines = append(lines, "    "+p.styles.DiffAdded.Render("+ "+cl))
				}
				lines = append(lines, "    "+p.styles.Subtle.Render(fmt.Sprintf("... +%d more lines", len(contentLines)-maxShow)))
			} else {
				for _, cl := range contentLines {
					if len(cl) > maxWidth-6 {
						cl = cl[:maxWidth-9] + "..."
					}
					lines = append(lines, "    "+p.styles.DiffAdded.Render("+ "+cl))
				}
			}
		}

	case "Edit":
		fp, _ := p.inputParams["file_path"].(string)
		lines = append(lines, "  "+p.styles.ToolName.Render("Edit")+" "+shortenPath(fp))
		oldStr, _ := p.inputParams["old_string"].(string)
		newStr, _ := p.inputParams["new_string"].(string)
		if oldStr != "" || newStr != "" {
			maxShow := 4
			if p.detailsExpanded {
				maxShow = 15
			}
			oldLines := strings.Split(oldStr, "\n")
			for i, ol := range oldLines {
				if i >= maxShow {
					lines = append(lines, "    "+p.styles.Subtle.Render(fmt.Sprintf("... -%d more", len(oldLines)-maxShow)))
					break
				}
				if len(ol) > maxWidth-6 {
					ol = ol[:maxWidth-9] + "..."
				}
				lines = append(lines, "    "+p.styles.DiffRemoved.Render("- "+ol))
			}
			newLines := strings.Split(newStr, "\n")
			for i, nl := range newLines {
				if i >= maxShow {
					lines = append(lines, "    "+p.styles.Subtle.Render(fmt.Sprintf("... +%d more", len(newLines)-maxShow)))
					break
				}
				if len(nl) > maxWidth-6 {
					nl = nl[:maxWidth-9] + "..."
				}
				lines = append(lines, "    "+p.styles.DiffAdded.Render("+ "+nl))
			}
		}

	case "Read":
		fp, _ := p.inputParams["file_path"].(string)
		lines = append(lines, "  "+p.styles.ToolName.Render("Read")+" "+shortenPath(fp))

	default:
		args := formatToolArgs(p.toolName, p.toolInput)
		if args != "" {
			argStr := fmt.Sprintf("%s(%s)", p.styles.ToolName.Render(p.toolName), args)
			profile := DefaultDisplayCellProfile()
			if profile.width(argStr) > maxWidth {
				argStr = modalEllipsize(profile, argStr, maxWidth, 0, "...")
			}
			lines = append(lines, "  "+argStr)
		} else {
			lines = append(lines, "  "+p.styles.ToolName.Render(p.toolName))
		}

		// Show params as key-value pairs if details expanded
		if p.detailsExpanded && len(p.inputParams) > 0 {
			for k, v := range p.inputParams {
				valStr := fmt.Sprintf("%v", v)
				if len(valStr) > maxWidth-len(k)-8 {
					valStr = valStr[:maxWidth-len(k)-11] + "..."
				}
				lines = append(lines, "    "+p.styles.Subtle.Render(k+": ")+valStr)
			}
		}
	}

	return lines
}

// --- Risk assessment ---

// assessRiskLevel determines the risk level of a tool operation.
func assessRiskLevel(toolName string, params map[string]any) RiskLevel {
	switch toolName {
	case "Read", "Grep", "Glob":
		return RiskLow

	case "Write":
		return RiskMedium

	case "Edit":
		return RiskMedium

	case "Bash":
		return assessBashRisk(params)

	case "Agent":
		return RiskMedium

	default:
		// Unknown tools default to medium risk
		return RiskMedium
	}
}

// assessBashRisk determines the risk level of a bash command.
func assessBashRisk(params map[string]any) RiskLevel {
	cmd, _ := params["command"].(string)
	if cmd == "" {
		return RiskMedium
	}

	// High-risk patterns (checked first — these override everything)
	highRiskPatterns := []string{
		"rm -rf", "rm -r",
		"sudo ",
		"chmod ",
		"chown ",
		"dd ",
		"mkfs",
		"> /dev/",
		"curl | sh", "curl | bash",
		"wget -O - |",
		"kill -9",
		"pkill",
		"shutdown",
		"reboot",
		"git push --force",
		"git reset --hard",
		"drop table", "DROP TABLE",
		"truncate ", "TRUNCATE ",
	}

	cmdLower := strings.ToLower(cmd)
	for _, pattern := range highRiskPatterns {
		if strings.Contains(cmdLower, strings.ToLower(pattern)) {
			return RiskHigh
		}
	}

	// Low-risk: read-only commands (checked before medium to avoid false positives
	// from generic prefixes like "git " matching "git status")
	lowRiskPrefixes := []string{
		"ls", "cat", "head", "tail", "wc",
		"grep", "find", "which", "echo",
		"pwd", "whoami", "date", "uname",
		"git status", "git log", "git diff", "git branch",
	}
	for _, prefix := range lowRiskPrefixes {
		if strings.HasPrefix(cmd, prefix) {
			return RiskLow
		}
	}

	// Medium-risk: anything that modifies state
	mediumRiskPatterns := []string{
		"git ", "npm ", "yarn ", "pip ", "go ",
		"mkdir", "mv ", "cp ",
		"apt ", "brew ",
		"docker ", "kubectl ",
	}
	for _, pattern := range mediumRiskPatterns {
		if strings.Contains(cmd, pattern) {
			return RiskMedium
		}
	}

	return RiskMedium
}

// generateToolContext generates a human-readable description of what the tool wants to do.
func generateToolContext(toolName string, params map[string]any) string {
	switch toolName {
	case "Bash":
		cmd, _ := params["command"].(string)
		if desc, ok := params["description"].(string); ok && desc != "" {
			return desc
		}
		if cmd != "" {
			if len(cmd) > 100 {
				return "Execute a shell command"
			}
			return "Execute: " + cmd
		}
		return "Execute a shell command"

	case "Write":
		fp, _ := params["file_path"].(string)
		if fp != "" {
			return "Create or overwrite file: " + shortenPath(fp)
		}
		return "Write to a file"

	case "Edit":
		fp, _ := params["file_path"].(string)
		if fp != "" {
			return "Modify file: " + shortenPath(fp)
		}
		return "Edit a file"

	case "Read":
		fp, _ := params["file_path"].(string)
		if fp != "" {
			return "Read file: " + shortenPath(fp)
		}
		return "Read a file"

	case "Agent":
		desc, _ := params["description"].(string)
		if desc != "" {
			return "Spawn sub-agent: " + desc
		}
		return "Spawn a sub-agent"

	default:
		return fmt.Sprintf("Use tool: %s", toolName)
	}
}

// permissionToolTitle returns a title for the permission dialog based on tool type.
func permissionToolTitle(toolName string) string {
	switch toolName {
	case "Bash":
		return "Shell Command"
	case "Read", "Glob", "Grep":
		return "File Read"
	case "Write":
		return "File Write"
	case "Edit":
		return "File Edit"
	case "Agent":
		return "Agent Dispatch"
	default:
		return "Tool Use: " + toolName
	}
}

// buildPromptOptions constructs the list of selectable options.
func buildPromptOptions(_, sessionScope string, constraint engine.PermissionDecisionConstraint) []PermissionPromptOption {
	opts := []PermissionPromptOption{
		{Label: "Allow Once", Shortcut: "a", Response: PermissionAllow},
	}

	if constraint == engine.PermissionAllowOnceOnly {
		return append(opts, PermissionPromptOption{Label: "Deny", Shortcut: "d", Response: PermissionDeny})
	}

	// Session-scope option
	sessionLabel := "Allow for Session"
	if sessionScope != "" {
		sessionLabel = fmt.Sprintf("Allow for %s", sessionScope)
	}
	opts = append(opts, PermissionPromptOption{
		Label:    sessionLabel,
		Shortcut: "A",
		Response: PermissionAllowSession,
	})

	opts = append(opts, PermissionPromptOption{
		Label:    "Deny",
		Shortcut: "d",
		Response: PermissionDeny,
	})

	return opts
}

// wrapPermText wraps text to fit within the given width.
func wrapPermText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	currentLine := words[0]
	currentLen := len(words[0])

	for _, word := range words[1:] {
		wordLen := len(word)
		if currentLen+1+wordLen <= width {
			currentLine += " " + word
			currentLen += 1 + wordLen
		} else {
			lines = append(lines, currentLine)
			currentLine = word
			currentLen = wordLen
		}
	}
	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return lines
}

// --- Timeout tick message ---

// permissionTimeoutTickMsg is sent to update the timeout countdown.
type permissionTimeoutTickMsg struct{}

// permissionTimeoutTick returns a command that sends a tick every second for timeout.
func permissionTimeoutTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return permissionTimeoutTickMsg{}
	})
}

// --- Keybinding helpers for integration ---

// PermissionPromptKeyBindings returns the key bindings used by the permission prompt.
func PermissionPromptKeyBindings() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "option 1")),
		key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "option 2")),
		key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "option 3")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "allow once")),
		key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "allow session")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "deny")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel/deny")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "toggle details")),
	}
}
