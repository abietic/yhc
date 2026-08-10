package tui

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/internal/tui/keybindings"
)

func (a *App) wideSidebarVisible() bool {
	if a == nil || a.engine == nil {
		return false
	}
	snapshot := a.taskExplorerSnapshot()
	return snapshot.Available &&
		(len(snapshot.WorkItems) > 0 ||
			len(snapshot.Executions) > 0 ||
			summarizeTaskExplorer(snapshot).hidden > 0)
}

func (a *App) renderWideSidebar() string {
	if a == nil || a.layout.sidebarRect.Width <= 0 || a.layout.sidebarRect.Height <= 0 {
		return ""
	}
	snapshot := a.taskExplorerSnapshot()
	summary := summarizeTaskExplorer(snapshot)
	contentWidth := max(1, a.layout.sidebarRect.Width-3)
	contentOrigin := a.layout.sidebarRect.X + 2
	lines := []string{
		fmt.Sprintf(
			"WORK  %d/%d done · %d live",
			summary.done,
			summary.total,
			summary.live,
		),
		fmt.Sprintf(
			"attention %d · hidden %d",
			summary.attention,
			summary.hidden,
		),
	}
	if summary.activity != "" {
		lines = append(lines, "now   "+summary.activity)
	}
	for _, row := range snapshot.WorkItems {
		lines = append(lines, fmt.Sprintf(
			"%-5s %s",
			responsiveTaskStatus(row.Status),
			firstNonEmptyString(
				row.Title,
				row.ActiveForm,
				row.WorkItemID,
			),
		))
	}
	for _, row := range snapshot.Executions {
		lines = append(lines, fmt.Sprintf(
			"%-5s %s@g%d %s",
			responsiveTaskStatus(string(row.Phase)),
			row.Key.AgentID,
			row.Key.Generation,
			firstNonEmptyString(
				row.Task,
				row.Description,
				row.Name,
				row.Activity,
			),
		))
	}
	footer := joinKeyHints(
		keyHint(a.shortcut(keybindings.ContextChat, keybindings.ActionTaskBackground, "ctrl+b"), "details"),
		keyHint("/agent", "switch"),
	)
	if footer == "" {
		footer = "Ctrl+T work · Ctrl+B executions"
	}
	available := max(0, a.layout.sidebarRect.Height-1)
	if len(lines) > available {
		lines = append(lines[:max(0, available-1)], "...")
	}
	for len(lines) < available {
		lines = append(lines, "")
	}
	lines = append(lines, footer)
	for index, line := range lines {
		line = a.renderEnvironment.profile.truncateAt(line, contentWidth, contentOrigin)
		if index == 0 {
			line = a.styles.Bold.Render(line)
		} else if index == len(lines)-1 {
			line = a.styles.Subtle.Render(line)
		}
		lines[index] = a.styles.Subtle.Render("│") + " " + line
	}
	return strings.Join(lines, "\n")
}

func responsiveTaskStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "running", "in_progress":
		return "RUN"
	case "paused":
		return "PAUSE"
	case "completed":
		return "DONE"
	case "failed", "aborted", "killed":
		return "FAIL"
	default:
		return "WAIT"
	}
}
