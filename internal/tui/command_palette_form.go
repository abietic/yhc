package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine/commands"
)

type commandPaletteForm struct {
	operation commands.OperationDescription
	inputs    []textinput.Model
	cursor    int
}

func (p *CommandPalette) appendQuery(value string) {
	if strings.ContainsFunc(value, unicode.IsControl) {
		p.feedback = "Use single-line text"
		return
	}
	if len(p.query)+len(value) > 512 {
		p.feedback = "Search is too long"
		return
	}
	p.query += value
	p.applyFilter()
}

func (p *CommandPalette) HandlePaste(value string) {
	if p.details {
		return
	}
	if p.form == nil {
		p.appendQuery(value)
		return
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		p.feedback = "Use single-line text; control characters are not allowed"
		return
	}
	f := p.form
	if len(value)+len(f.inputs[f.cursor].Value()) > commands.MaxOperationFormBytes {
		p.feedback = "Value is too long"
		return
	}
	f.inputs[f.cursor], _ = f.inputs[f.cursor].Update(tea.PasteMsg{Content: value})
	p.refreshForm()
}

func (p *CommandPalette) openForm(operation commands.OperationDescription) {
	p.form = &commandPaletteForm{operation: operation}
	p.rebuildForm(operation, nil)
	p.refreshForm()
}

func (p *CommandPalette) rebuildForm(operation commands.OperationDescription, previous []string) {
	f := p.form
	f.operation = operation
	f.inputs = make([]textinput.Model, len(operation.Fields))
	for i, field := range operation.Fields {
		input := textinput.New()
		input.CharLimit = commands.MaxOperationFormBytes
		input.Prompt = ""
		input.SetVirtualCursor(true)
		value := field.Default
		if i < len(previous) {
			value = previous[i]
		}
		if strings.ContainsFunc(value, unicode.IsControl) {
			value = ""
			p.feedback = "Invalid control characters in field default"
		}
		input.SetValue(value)
		f.inputs[i] = input
	}
	f.cursor = min(f.cursor, len(f.inputs)-1)
	p.styleFormInputs()
	p.focusFormField()
}

func (p *CommandPalette) styleFormInputs() {
	if p.form == nil {
		return
	}
	styles := textinput.Styles{}
	styles.Focused.Text, styles.Blurred.Text = p.styles.DialogInputText, p.styles.DialogInputText
	styles.Focused.Placeholder, styles.Blurred.Placeholder = p.styles.DialogInputPlaceholder, p.styles.DialogInputPlaceholder
	styles.Cursor.Color = p.styles.DialogInputCursor.GetForeground()
	styles.Cursor.Blink = false
	for i := range p.form.inputs {
		p.form.inputs[i].SetStyles(styles)
	}
}

func (p *CommandPalette) focusFormField() {
	for i := range p.form.inputs {
		if i == p.form.cursor {
			p.form.inputs[i].Focus()
		} else {
			p.form.inputs[i].Blur()
		}
	}
}

func (p *CommandPalette) formValues() []string {
	values := make([]string, len(p.form.inputs))
	for i := range values {
		values[i] = p.form.inputs[i].Value()
	}
	return values
}

// A branch change keeps the common prefix and drops values whose field contract changed.
func (p *CommandPalette) refreshForm() {
	f := p.form
	values := p.formValues()
	operation, ok := p.registry.DescribeOperation(context.Background(), commands.EntrypointTUI, p.commandContext, f.operation.Command.Name, values)
	if !ok || operation.Token != f.operation.Token {
		p.feedback = "Command changed; Esc and reopen the form"
		return
	}
	if !sameOperationFields(operation.Fields, f.operation.Fields) {
		keep := 0
		for keep < min(len(operation.Fields), len(f.operation.Fields)) && sameOperationFields(operation.Fields[keep:keep+1], f.operation.Fields[keep:keep+1]) {
			keep++
		}
		p.rebuildForm(operation, values[:keep])
	} else {
		f.operation = operation
	}
}

func sameOperationFields(a, b []commands.OperationField) bool {
	return slices.EqualFunc(a, b, func(x, y commands.OperationField) bool {
		return x.Name == y.Name && x.Type == y.Type && x.Description == y.Description && x.Default == y.Default && x.Source == y.Source && x.Required == y.Required && x.Rest == y.Rest && slices.Equal(x.Choices, y.Choices)
	})
}

func (p *CommandPalette) handleFormKey(msg tea.KeyPressMsg) (string, bool) {
	p.feedback = ""
	f := p.form
	switch msg.String() {
	case "esc":
		p.form = nil
		return "", false
	case "ctrl+c", "ctrl+k":
		p.Close()
		return "", true
	case "f1":
		p.details = true
		p.detailOffset = 0
		return "", false
	case "enter":
		operation, ok := p.registry.DescribeOperation(context.Background(), commands.EntrypointTUI, p.commandContext, f.operation.Command.Name, p.formValues())
		if !ok || operation.Token != f.operation.Token {
			p.feedback = "Command changed; Esc and reopen the form"
			return "", false
		}
		input, err := p.registry.BuildOperationInput(context.Background(), commands.EntrypointTUI, p.commandContext, operation.Command.Name, p.formValues())
		if err != nil {
			p.feedback = err.Error()
			return "", false
		}
		p.selectionToken = operation.Token
		p.Close()
		return input, true
	case "tab", "shift+tab":
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		f.cursor = (f.cursor + delta + len(f.inputs)) % len(f.inputs)
		p.focusFormField()
	case "up", "down":
		field := f.operation.Fields[f.cursor]
		delta := 1
		if msg.String() == "up" {
			delta = -1
		}
		if len(field.Choices) > 0 {
			index := slices.Index(field.Choices, f.inputs[f.cursor].Value())
			if index < 0 {
				index = 0
			} else {
				index = (index + delta + len(field.Choices)) % len(field.Choices)
			}
			f.inputs[f.cursor].SetValue(field.Choices[index])
			p.refreshForm()
		} else {
			f.cursor = (f.cursor + delta + len(f.inputs)) % len(f.inputs)
			p.focusFormField()
		}
	default:
		if strings.ContainsFunc(msg.Text, unicode.IsControl) {
			p.feedback = "Use single-line text"
			return "", false
		}
		f.inputs[f.cursor], _ = f.inputs[f.cursor].Update(msg)
		p.refreshForm()
	}
	return "", false
}

func (p *CommandPalette) handleDetailsKey(msg tea.KeyPressMsg) (string, bool) {
	switch msg.String() {
	case "esc", "f1", "enter":
		p.details = false
	case "ctrl+c", "ctrl+k":
		p.Close()
		return "", true
	case "up", "pgup":
		p.detailOffset = max(0, p.detailOffset-1)
	case "down", "pgdown":
		p.detailOffset++
	}
	return "", false
}

func (p *CommandPalette) selectedOperation() (commands.OperationDescription, bool) {
	if p.form != nil {
		return p.form.operation, true
	}
	if p.cursor >= 0 && p.cursor < len(p.filtered) {
		operation, ok := p.operations[p.filtered[p.cursor].command.Name]
		return operation, ok
	}
	return commands.OperationDescription{}, false
}

func operationMetadata(cmd *commands.Command) []string {
	source := cmd.Source
	if cmd.SourceVersion != "" {
		source += "@" + cmd.SourceVersion
	}
	availability := string(cmd.Availability)
	if cmd.AvailabilityReason != "" {
		availability += " — " + cmd.AvailabilityReason
	}
	model := "unknown; inspect command"
	switch cmd.ModelInvocation {
	case commands.ModelInvocationNone:
		model = "no"
	case commands.ModelInvocationPossible:
		model = "possible (depends on action)"
	case commands.ModelInvocationPrompt:
		model = "yes, after prompt submit"
	}
	return []string{"Source: " + source + " (" + string(cmd.Trust) + ")", "Availability: " + availability, "Effect: " + string(cmd.SideEffect) + " (direct)", "Model: " + model}
}

func (p *CommandPalette) operationOverlay(base string, width, height int) string {
	if width < 8 || height < 5 {
		return base
	}
	profile := p.environment.profile
	inner := max(1, min(88, width-4))
	contentHeight := height - 2
	line := func(value string) string { return modalEllipsize(profile, value, inner, 2, "…") }
	operation, selected := p.selectedOperation()
	footer := "↑/↓ select · Enter edit/run · F1 details · Esc close"
	if p.form != nil {
		footer = "Tab fields · ↑/↓ choices · Enter run · Esc back"
		if strings.HasPrefix(operation.Command.Source, "skill:") {
			footer = "Tab fields · Enter stage Skill · Esc back"
		}
	}
	if inner < 55 {
		footer = "Enter edit/run · F1 details · Esc back"
		if p.form != nil {
			footer = "Tab fields · Enter run · F1 · Esc"
			if strings.HasPrefix(operation.Command.Source, "skill:") {
				footer = "Tab · Enter stage Skill · F1 · Esc"
			}
		}
	}
	var lines []string
	if p.details && selected {
		lines = append(lines, p.styles.DialogTitle.Render(line("Command details /"+operation.Command.Name)))
		details := []string{operation.Command.Description, "Usage: " + operation.Command.Usage}
		details = append(details, operationMetadata(operation.Command)...)
		if operation.Command.SourcePath != "" {
			details = append(details, "Path: "+operation.Command.SourcePath)
		}
		if operation.Command.Dependency != "" {
			details = append(details, "Requires: "+operation.Command.Dependency)
		}
		if operation.Command.ModelInvocation == commands.ModelInvocationPrompt {
			details = append(details, "Prompt execution may use tools under normal permissions.")
		}
		var wrapped []string
		for _, detail := range details {
			wrapped = append(wrapped, contentWrapLines(profile, composerContextLabel(detail), inner, 2)...)
		}
		visible := max(1, contentHeight-2)
		p.detailOffset = min(p.detailOffset, max(0, len(wrapped)-visible))
		lines = append(lines, wrapped[p.detailOffset:min(len(wrapped), p.detailOffset+visible)]...)
		footer = "↑/↓ scroll · F1/Esc back"
	} else {
		metadata := []string(nil)
		if selected {
			metadata = operationMetadata(operation.Command)
		}
		title := "Command Palette"
		if p.form != nil {
			title = "Parameters /" + operation.Command.Name
		}
		lines = append(lines, p.styles.DialogTitle.Render(line(title)))
		rows := max(1, contentHeight-len(metadata)-4)
		if p.form != nil {
			f := p.form
			start := max(0, f.cursor-rows+1)
			for i := start; i < min(len(f.inputs), start+rows); i++ {
				field := f.operation.Fields[i]
				marker := "  "
				if i == f.cursor {
					marker = "> "
				}
				label := field.Name
				if field.Required {
					label += "*"
				}
				prefix := fmt.Sprintf("%s%d/%d %s: ", marker, i+1, len(f.inputs), composerContextLabel(label))
				prefix = modalEllipsize(profile, prefix, max(4, inner/2), 2, "…")
				f.inputs[i].SetWidth(max(1, inner-profile.measure(prefix, 2)-1))
				lines = append(lines, modalProjectLine(profile, prefix+f.inputs[i].View(), inner, 2))
			}
			field := f.operation.Fields[f.cursor]
			hint := field.Type
			if field.Rest {
				hint = "arguments (slash quoting)"
			}
			if field.Description != "" {
				hint += " · " + field.Description
			}
			if len(field.Choices) > 0 {
				hint += " · ↑/↓ " + strings.Join(field.Choices, ", ")
			}
			if field.Default != "" {
				hint += " · default: " + field.Default
			}
			if field.Source != "" {
				hint += " · " + field.Source
			}
			lines = append(lines, p.styles.Subtle.Render(line(composerContextLabel(hint))))
		} else {
			lines = append(lines, line("❯ "+p.query+"_"))
			if len(p.filtered) == 0 {
				lines = append(lines, p.styles.Subtle.Render("No matching commands"))
			} else {
				p.offset = min(p.offset, max(0, len(p.filtered)-rows))
				if p.cursor < p.offset {
					p.offset = p.cursor
				}
				if p.cursor >= p.offset+rows {
					p.offset = p.cursor - rows + 1
				}
				for i := p.offset; i < min(len(p.filtered), p.offset+rows); i++ {
					itemLine := p.renderItem(profile, p.filtered[i], i == p.cursor, inner)
					if p.filtered[i].section != "" && (i == p.offset || p.filtered[i-1].section != p.filtered[i].section) {
						itemLine = line(p.filtered[i].section + ": " + itemLine)
					}
					lines = append(lines, itemLine)
				}
			}
		}
		for _, meta := range metadata {
			lines = append(lines, p.styles.Subtle.Render(line(composerContextLabel(meta))))
		}
		if p.feedback != "" {
			lines = append(lines, p.styles.Warning.Render(line(composerContextLabel(p.feedback))))
		}
	}
	// Reserve the footer even on short terminals.
	lines = lines[:min(len(lines), contentHeight-1)]
	lines = append(lines, p.styles.Subtle.Render(line(footer)))
	box := contentRenderStyleWidth(profile, p.styles.DialogBorder.Border(lipgloss.RoundedBorder()).Padding(0, 1), inner+2, strings.Join(lines, "\n"))
	overlay, _ := modalCenteredOverlay(profile, base, box, width, height)
	return overlay
}
