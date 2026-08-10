package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// bypassConfirmOptions defines the options shown in the bypass confirmation dialog.
// Mirrors the reference BypassPermissionsModeDialog.tsx options.
var bypassConfirmOptions = []struct {
	label string
	value string
}{
	{label: "No, go back", value: "decline"},
	{label: "Yes, I accept the risks", value: "accept"},
}

// showBypassConfirm opens the bypass permissions confirmation dialog.
// Called when the user attempts to enter bypass mode via shift+tab or /bypass.
func (a *App) showBypassConfirm() {
	a.bypassConfirmIdx = 0 // default to "No, go back" (safe default)
	a.pushDialog(StateBypassConfirm)
}

// handleBypassConfirmKey processes key events while the bypass confirmation
// dialog is displayed. Mirrors the reference BypassPermissionsModeDialog behavior:
// - Up/Down/j/k: navigate options
// - Enter: confirm selection
// - Esc/n: cancel (go back without changes)
// - y: quick-accept shortcut
func (a *App) handleBypassConfirmKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		if a.bypassConfirmIdx > 0 {
			a.bypassConfirmIdx--
		} else {
			a.bypassConfirmIdx = len(bypassConfirmOptions) - 1
		}
		return nil

	case "down", "j":
		if a.bypassConfirmIdx < len(bypassConfirmOptions)-1 {
			a.bypassConfirmIdx++
		} else {
			a.bypassConfirmIdx = 0
		}
		return nil

	case "enter":
		return a.resolveBypassConfirm()

	case "esc", "n":
		// Cancel — return to previous mode without changes
		a.popDialog(StateBypassConfirm)
		a.chat.AppendSystem("bypass mode canceled")
		return nil

	case "y":
		// Quick-accept shortcut
		a.bypassConfirmIdx = 1 // "Yes, I accept"
		return a.resolveBypassConfirm()
	}

	return nil
}

// resolveBypassConfirm applies the user's selection from the bypass confirm dialog.
func (a *App) resolveBypassConfirm() tea.Cmd {
	if a.bypassConfirmIdx < 0 || a.bypassConfirmIdx >= len(bypassConfirmOptions) {
		a.popDialog(StateBypassConfirm)
		return nil
	}

	selected := bypassConfirmOptions[a.bypassConfirmIdx]
	a.popDialog(StateBypassConfirm)

	switch selected.value {
	case "accept":
		if err := a.setConfirmedBypassMode(); err != nil {
			a.chat.AppendSystem("mode change failed: " + err.Error())
			return nil
		}
		a.chat.AppendSystem("accept all tools on — bypassing permissions")
	case "decline":
		a.chat.AppendSystem("bypass mode canceled")
	}

	return nil
}

// bypassConfirmOverlay renders the bypass permissions confirmation dialog
// as a centered overlay. Styled with warning colors following the reference
// BypassPermissionsModeDialog.tsx design.
func (a *App) bypassConfirmOverlay(base string) string {
	profile := a.renderEnvironment.normalized().profile
	dialogWidth := a.width - 10
	if dialogWidth > 70 {
		dialogWidth = 70
	}
	if dialogWidth < 40 {
		dialogWidth = 40
	}

	// Warning title with error/warning styling (reference uses color="error")
	title := a.styles.Warning.Bold(true).Render("WARNING: Bypass Permissions Mode")

	// Build the warning text content
	// Reference: "In Bypass Permissions mode, Claude Code will not ask for your
	// approval before running potentially dangerous commands."
	warningText := "In Bypass Permissions mode, tools will run without asking\n" +
		"for your approval — including potentially dangerous commands."

	sandboxNote := "This mode should only be used in a sandboxed container/VM\n" +
		"that has restricted internet access and can easily be\n" +
		"restored if damaged."

	acceptText := "By proceeding, you accept all responsibility for actions\n" +
		"taken while running in Bypass Permissions mode."

	// Render selectable options with pointer indicator
	optLines := make([]string, len(bypassConfirmOptions))
	for i, opt := range bypassConfirmOptions {
		if i == a.bypassConfirmIdx {
			pointer := a.styles.Warning.Render(">")
			optLines[i] = fmt.Sprintf("  %s %s", pointer, a.styles.Warning.Bold(true).Render(opt.label))
		} else {
			optLines[i] = fmt.Sprintf("    %s", a.styles.Subtle.Render(opt.label))
		}
	}

	help := a.styles.DialogHelp.Render("up/down navigate - enter select - y accept - esc/n cancel")

	// Compose dialog content
	var lines []string
	lines = append(lines, title)
	lines = append(lines, "")
	lines = append(lines, a.styles.Warning.Render(warningText))
	lines = append(lines, "")
	lines = append(lines, a.styles.Subtle.Render(sandboxNote))
	lines = append(lines, "")
	lines = append(lines, acceptText)
	lines = append(lines, "")
	lines = append(lines, strings.Repeat("─", dialogWidth-4))
	lines = append(lines, "")
	lines = append(lines, optLines...)
	lines = append(lines, "")
	lines = append(lines, help)

	dialog := contentRenderStyleWidth(
		profile,
		a.styles.DialogBorder,
		dialogWidth,
		strings.Join(lines, "\n"),
	)
	view, _ := modalCenteredOverlay(profile, base, dialog, a.width, a.height)
	return view
}
