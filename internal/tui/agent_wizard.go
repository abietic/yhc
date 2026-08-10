package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// AgentWizardStep represents the current step in the agent creation wizard.
type AgentWizardStep int

const (
	WizardStepName         AgentWizardStep = iota // Step 1: Name + Description
	WizardStepModel                               // Step 2: Model selection
	WizardStepInstructions                        // Step 3: System prompt
	WizardStepTools                               // Step 4: Tool selection
	WizardStepReview                              // Step 5: Review and confirm
)

// AgentWizardMode distinguishes create from edit.
type AgentWizardMode int

const (
	WizardModeCreate AgentWizardMode = iota
	WizardModeEdit
)

// AgentWizard is a multi-step modal form for creating or editing agent definitions.
type AgentWizard struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	geometry    modalFrameGeometry
	mode        AgentWizardMode
	step        AgentWizardStep

	// Form fields
	nameInput        textinput.Model
	descriptionInput textinput.Model
	modelInput       textinput.Model
	instructionsArea textarea.Model
	toolsInput       textinput.Model

	// Which field is focused within a step (for steps with multiple fields)
	fieldFocus int // 0 = first field, 1 = second field

	// Original name (for edit mode — used to detect rename)
	originalName string
}

// NewAgentWizard creates a new agent wizard overlay.
func NewAgentWizard(styles Styles) *AgentWizard {
	nameIn := textinput.New()
	nameIn.Placeholder = "my-agent"
	nameIn.CharLimit = 64
	nameIn.SetWidth(40)

	descIn := textinput.New()
	descIn.Placeholder = "A helpful description of what this agent does"
	descIn.CharLimit = 200
	descIn.SetWidth(40)

	modelIn := textinput.New()
	modelIn.Placeholder = "claude-sonnet-4-20250514 (or leave empty to inherit)"
	modelIn.CharLimit = 100
	modelIn.SetWidth(40)

	toolsIn := textinput.New()
	toolsIn.Placeholder = "Read, Grep, Glob (comma-separated, or empty for all)"
	toolsIn.CharLimit = 500
	toolsIn.SetWidth(40)

	instrArea := textarea.New()
	instrArea.Placeholder = "Enter the system prompt / instructions for this agent..."
	instrArea.ShowLineNumbers = false
	instrArea.CharLimit = 0
	instrArea.SetWidth(40)
	instrArea.SetHeight(6)

	wizard := &AgentWizard{
		styles:           styles,
		nameInput:        nameIn,
		descriptionInput: descIn,
		modelInput:       modelIn,
		instructionsArea: instrArea,
		toolsInput:       toolsIn,
	}
	wizard.SetRenderEnvironment(defaultRenderEnvironment(styles))
	return wizard
}

func (w *AgentWizard) SetStyles(styles Styles) {
	w.SetRenderEnvironment(w.environment.withStyles(styles))
}

func (w *AgentWizard) SetRenderEnvironment(env RenderEnvironment) {
	w.environment = env.normalized()
	w.styles = w.environment.styles
}

// Show opens the wizard in create mode with empty fields.
func (w *AgentWizard) Show() {
	w.visible = true
	w.mode = WizardModeCreate
	w.step = WizardStepName
	w.fieldFocus = 0
	w.originalName = ""

	w.nameInput.SetValue("")
	w.descriptionInput.SetValue("")
	w.modelInput.SetValue("")
	w.instructionsArea.SetValue("")
	w.toolsInput.SetValue("")

	w.nameInput.Focus()
	w.descriptionInput.Blur()
}

// ShowEdit opens the wizard in edit mode pre-filled with an existing definition.
func (w *AgentWizard) ShowEdit(name, description, model, instructions, tools string) {
	w.visible = true
	w.mode = WizardModeEdit
	w.step = WizardStepName
	w.fieldFocus = 0
	w.originalName = name

	w.nameInput.SetValue(name)
	w.descriptionInput.SetValue(description)
	w.modelInput.SetValue(model)
	w.instructionsArea.SetValue(instructions)
	w.toolsInput.SetValue(tools)

	w.nameInput.Focus()
	w.descriptionInput.Blur()
}

// Close hides the wizard.
func (w *AgentWizard) Close() {
	w.visible = false
	w.nameInput.Blur()
	w.descriptionInput.Blur()
	w.modelInput.Blur()
	w.instructionsArea.Blur()
	w.toolsInput.Blur()
}

// Visible returns whether the wizard is currently shown.
func (w *AgentWizard) Visible() bool {
	return w.visible
}

// AgentWizardResult holds the data from a completed wizard.
type AgentWizardResult struct {
	Name         string
	Description  string
	Model        string
	Instructions string
	Tools        []string
}

// HandleKey processes key events for the wizard.
// Returns (result, dismissed) where result is non-nil on confirm.
func (w *AgentWizard) HandleKey(msg tea.KeyPressMsg) (*AgentWizardResult, bool) {
	key := msg.String()

	switch key {
	case "esc":
		w.Close()
		return nil, true

	case "tab":
		// Move to next field or next step
		if w.step == WizardStepName {
			if w.fieldFocus == 0 {
				// Move from name to description
				w.fieldFocus = 1
				w.nameInput.Blur()
				w.descriptionInput.Focus()
				return nil, false
			}
			// Move to next step
			w.advanceStep()
			return nil, false
		}
		if w.step == WizardStepReview {
			// Confirm on tab at review step
			return w.buildResult(), true
		}
		w.advanceStep()
		return nil, false

	case "shift+tab":
		// Move to previous field or previous step
		if w.step == WizardStepName {
			if w.fieldFocus == 1 {
				// Move from description back to name
				w.fieldFocus = 0
				w.descriptionInput.Blur()
				w.nameInput.Focus()
				return nil, false
			}
			// At the very first field — do nothing
			return nil, false
		}
		w.retreatStep()
		return nil, false

	case "enter":
		if w.step == WizardStepReview {
			return w.buildResult(), true
		}
		// In instructions step, enter adds a newline (handled by textarea)
		if w.step == WizardStepInstructions {
			w.instructionsArea, _ = w.instructionsArea.Update(msg)
			return nil, false
		}
		// Otherwise, advance to next step
		w.advanceStep()
		return nil, false

	case "ctrl+n":
		// Force advance
		w.advanceStep()
		return nil, false

	case "ctrl+p":
		// Force retreat
		w.retreatStep()
		return nil, false
	}

	// Forward key to the active input
	w.updateActiveInput(msg)
	return nil, false
}

// advanceStep moves to the next step, setting up focus.
func (w *AgentWizard) advanceStep() {
	w.blurAll()
	switch w.step {
	case WizardStepName:
		w.step = WizardStepModel
		w.modelInput.Focus()
	case WizardStepModel:
		w.step = WizardStepInstructions
		w.instructionsArea.Focus()
	case WizardStepInstructions:
		w.step = WizardStepTools
		w.toolsInput.Focus()
	case WizardStepTools:
		w.step = WizardStepReview
	case WizardStepReview:
		// Already at end
	}
	w.fieldFocus = 0
}

// retreatStep moves to the previous step.
func (w *AgentWizard) retreatStep() {
	w.blurAll()
	switch w.step {
	case WizardStepName:
		// Already at start
	case WizardStepModel:
		w.step = WizardStepName
		w.fieldFocus = 1
		w.descriptionInput.Focus()
	case WizardStepInstructions:
		w.step = WizardStepModel
		w.modelInput.Focus()
	case WizardStepTools:
		w.step = WizardStepInstructions
		w.instructionsArea.Focus()
	case WizardStepReview:
		w.step = WizardStepTools
		w.toolsInput.Focus()
	}
}

// blurAll blurs all inputs.
func (w *AgentWizard) blurAll() {
	w.nameInput.Blur()
	w.descriptionInput.Blur()
	w.modelInput.Blur()
	w.instructionsArea.Blur()
	w.toolsInput.Blur()
}

// updateActiveInput forwards the key to the currently active input field.
func (w *AgentWizard) updateActiveInput(msg tea.KeyPressMsg) {
	switch w.step {
	case WizardStepName:
		if w.fieldFocus == 0 {
			w.nameInput, _ = w.nameInput.Update(msg)
		} else {
			w.descriptionInput, _ = w.descriptionInput.Update(msg)
		}
	case WizardStepModel:
		w.modelInput, _ = w.modelInput.Update(msg)
	case WizardStepInstructions:
		w.instructionsArea, _ = w.instructionsArea.Update(msg)
	case WizardStepTools:
		w.toolsInput, _ = w.toolsInput.Update(msg)
	case WizardStepReview:
		// No input at review step
	}
}

// buildResult constructs the result from current field values.
func (w *AgentWizard) buildResult() *AgentWizardResult {
	name := strings.TrimSpace(w.nameInput.Value())
	if name == "" {
		return nil
	}
	desc := strings.TrimSpace(w.descriptionInput.Value())
	if desc == "" {
		return nil
	}
	instr := strings.TrimSpace(w.instructionsArea.Value())
	if instr == "" {
		return nil
	}

	var tools []string
	toolsStr := strings.TrimSpace(w.toolsInput.Value())
	if toolsStr != "" {
		for _, t := range strings.Split(toolsStr, ",") {
			if v := strings.TrimSpace(t); v != "" {
				tools = append(tools, v)
			}
		}
	}

	return &AgentWizardResult{
		Name:         name,
		Description:  desc,
		Model:        strings.TrimSpace(w.modelInput.Value()),
		Instructions: instr,
		Tools:        tools,
	}
}

// Overlay renders the wizard dialog on top of the base view.
func (w *AgentWizard) Overlay(base string, width, height int) string {
	w.geometry = modalFrameGeometry{}
	if !w.visible {
		return base
	}

	dialogWidth := width - 6
	if dialogWidth > 64 {
		dialogWidth = 64
	}
	if dialogWidth < 40 {
		dialogWidth = 40
	}

	// Set input widths to match dialog
	inputWidth := dialogWidth - 8
	if inputWidth < 20 {
		inputWidth = 20
	}
	w.nameInput.SetWidth(inputWidth)
	w.descriptionInput.SetWidth(inputWidth)
	w.modelInput.SetWidth(inputWidth)
	w.toolsInput.SetWidth(inputWidth)
	w.instructionsArea.SetWidth(inputWidth)

	var parts []string

	// Title
	modeLabel := "Create Agent"
	if w.mode == WizardModeEdit {
		modeLabel = "Edit Agent"
	}
	stepLabel := w.stepLabel()
	title := w.styles.DialogTitle.Render(fmt.Sprintf("  %s --- %s", modeLabel, stepLabel))
	parts = append(parts, title)
	parts = append(parts, "")

	// Step content
	switch w.step {
	case WizardStepName:
		parts = append(parts, w.renderStepName()...)
	case WizardStepModel:
		parts = append(parts, w.renderStepModel()...)
	case WizardStepInstructions:
		parts = append(parts, w.renderStepInstructions()...)
	case WizardStepTools:
		parts = append(parts, w.renderStepTools()...)
	case WizardStepReview:
		parts = append(parts, w.renderStepReview(inputWidth)...)
	}

	// Footer
	parts = append(parts, "")
	parts = append(parts, strings.Repeat("\u2500", dialogWidth-4))
	helpText := w.helpText()
	parts = append(parts, w.styles.DialogHelp.Render("  "+helpText))

	dialog := contentRenderStyleWidth(
		w.environment.normalized().profile,
		w.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
	rendered, geometry := modalCenteredOverlay(
		w.environment.profile,
		base,
		dialog,
		width,
		height,
	)
	w.geometry = geometry
	return rendered
}

// stepLabel returns the step indicator text.
func (w *AgentWizard) stepLabel() string {
	switch w.step {
	case WizardStepName:
		return "Step 1/5: Name"
	case WizardStepModel:
		return "Step 2/5: Model"
	case WizardStepInstructions:
		return "Step 3/5: Instructions"
	case WizardStepTools:
		return "Step 4/5: Tools"
	case WizardStepReview:
		return "Step 5/5: Review"
	}
	return ""
}

// helpText returns the help line for the current step.
func (w *AgentWizard) helpText() string {
	if w.step == WizardStepReview {
		return "Enter confirm | Shift+Tab back | Esc cancel"
	}
	if w.step == WizardStepName && w.fieldFocus == 0 {
		return "Tab next field | Esc cancel"
	}
	return "Tab next | Shift+Tab back | Esc cancel"
}

// renderStepName renders step 1: name and description.
func (w *AgentWizard) renderStepName() []string {
	labelStyle := lipgloss.NewStyle().Bold(true)
	var lines []string
	lines = append(lines, "  "+labelStyle.Render("Name:"))
	lines = append(lines, "  "+w.nameInput.View())
	lines = append(lines, "")
	lines = append(lines, "  "+labelStyle.Render("Description:"))
	lines = append(lines, "  "+w.descriptionInput.View())
	return lines
}

// renderStepModel renders step 2: model selection.
func (w *AgentWizard) renderStepModel() []string {
	labelStyle := lipgloss.NewStyle().Bold(true)
	var lines []string
	lines = append(lines, "  "+labelStyle.Render("Model:"))
	lines = append(lines, "  "+w.modelInput.View())
	lines = append(lines, "")
	lines = append(lines, "  "+w.styles.Subtle.Render("  Leave empty to inherit parent model."))
	lines = append(lines, "  "+w.styles.Subtle.Render("  Examples: claude-sonnet-4-20250514, gpt-4o"))
	return lines
}

// renderStepInstructions renders step 3: system prompt / instructions.
func (w *AgentWizard) renderStepInstructions() []string {
	labelStyle := lipgloss.NewStyle().Bold(true)
	var lines []string
	lines = append(lines, "  "+labelStyle.Render("Instructions (system prompt):"))
	// Render textarea with indentation
	taView := w.instructionsArea.View()
	for _, line := range strings.Split(taView, "\n") {
		lines = append(lines, "  "+line)
	}
	return lines
}

// renderStepTools renders step 4: tool selection.
func (w *AgentWizard) renderStepTools() []string {
	labelStyle := lipgloss.NewStyle().Bold(true)
	var lines []string
	lines = append(lines, "  "+labelStyle.Render("Allowed Tools:"))
	lines = append(lines, "  "+w.toolsInput.View())
	lines = append(lines, "")
	lines = append(lines, "  "+w.styles.Subtle.Render("  Comma-separated tool names."))
	lines = append(lines, "  "+w.styles.Subtle.Render("  Leave empty to allow all tools."))
	lines = append(lines, "  "+w.styles.Subtle.Render("  Common: Read, Write, Edit, Bash, Grep, Glob"))
	return lines
}

// renderStepReview renders step 5: review all fields before confirm.
func (w *AgentWizard) renderStepReview(maxWidth int) []string {
	labelStyle := lipgloss.NewStyle().Bold(true)
	valueStyle := w.styles.ToolSuccess

	name := strings.TrimSpace(w.nameInput.Value())
	desc := strings.TrimSpace(w.descriptionInput.Value())
	model := strings.TrimSpace(w.modelInput.Value())
	instr := strings.TrimSpace(w.instructionsArea.Value())
	tools := strings.TrimSpace(w.toolsInput.Value())

	if model == "" {
		model = "(inherit)"
	}
	if tools == "" {
		tools = "(all)"
	}

	// Keep the review projection on the App-selected terminal grid without
	// splitting extended grapheme clusters or control sequences.
	instrDisplay := modalEllipsize(
		w.environment.profile,
		instr,
		max(1, maxWidth-4),
		0,
		"...",
	)

	var lines []string
	lines = append(lines, "  "+labelStyle.Render("Name: ")+valueStyle.Render(name))
	lines = append(lines, "  "+labelStyle.Render("Description: ")+valueStyle.Render(desc))
	lines = append(lines, "  "+labelStyle.Render("Model: ")+valueStyle.Render(model))
	lines = append(lines, "  "+labelStyle.Render("Instructions: ")+valueStyle.Render(instrDisplay))
	lines = append(lines, "  "+labelStyle.Render("Tools: ")+valueStyle.Render(tools))

	// Validation warnings
	var warnings []string
	if name == "" {
		warnings = append(warnings, "Name is required")
	}
	if desc == "" {
		warnings = append(warnings, "Description is required")
	}
	if instr == "" {
		warnings = append(warnings, "Instructions are required")
	}
	if len(warnings) > 0 {
		lines = append(lines, "")
		for _, warn := range warnings {
			lines = append(lines, "  "+w.styles.Warning.Render("! "+warn))
		}
	}

	return lines
}

// SaveAgentDefinition writes the agent definition as a markdown file with YAML frontmatter
// to the user's agents directory (~/.claude/agents/).
func SaveAgentDefinition(result *AgentWizardResult) (string, error) {
	if result == nil {
		return "", fmt.Errorf("no agent data to save")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	agentsDir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create agents directory: %w", err)
	}

	// Build the markdown content with YAML frontmatter
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", result.Name)
	fmt.Fprintf(&sb, "description: %s\n", result.Description)
	if result.Model != "" {
		fmt.Fprintf(&sb, "model: %s\n", result.Model)
	}
	if len(result.Tools) > 0 {
		fmt.Fprintf(&sb, "tools: %s\n", strings.Join(result.Tools, ", "))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(result.Instructions)
	sb.WriteString("\n")

	// Filename: sanitize name
	filename := sanitizeAgentFilename(result.Name) + ".md"
	filePath := filepath.Join(agentsDir, filename)

	if err := os.WriteFile(filePath, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("cannot write agent file: %w", err)
	}

	return filePath, nil
}

// sanitizeAgentFilename converts a name into a safe filename.
func sanitizeAgentFilename(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == ' ' || r == '_' || r == '-':
			sb.WriteRune('-')
		}
	}
	result := sb.String()
	if result == "" {
		result = "agent"
	}
	return result
}
