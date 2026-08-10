package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
)

type agentDetailTab int

const (
	agentDetailOverview agentDetailTab = iota
	agentDetailActivity
	agentDetailTranscript
	agentDetailOutput
	agentDetailLineage
	agentDetailTabCount
)

type agentDetailControl struct {
	input       textinput.Model
	active      bool
	notice      string
	noticeError bool
	action      func(engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult
}

func newAgentDetailControl() agentDetailControl {
	input := textinput.New()
	input.Prompt = "> "
	input.CharLimit = 4096
	return agentDetailControl{input: input}
}

func (c *agentDetailControl) setActionProvider(
	action func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult,
) {
	c.action = action
}

func (c *agentDetailControl) reset() {
	c.active = false
	c.notice = ""
	c.noticeError = false
	c.input.Blur()
	c.input.SetValue("")
}

func (c *agentDetailControl) handleKey(
	execution engine.TaskExplorerExecution,
	snapshot engine.TaskExplorerSnapshot,
	writable bool,
	msg tea.KeyPressMsg,
) (handled, changed bool, cmd tea.Cmd) {
	if !writable {
		c.reset()
		switch msg.String() {
		case "i", "x", "p":
			c.notice = "Replay and evicted Agent views are read-only"
			c.noticeError = true
			return true, false, nil
		default:
			return false, false, nil
		}
	}
	if c.active {
		switch msg.String() {
		case "esc":
			c.active = false
			c.input.Blur()
			c.input.SetValue("")
			return true, false, nil
		case "enter":
			content := c.input.Value()
			if strings.TrimSpace(content) == "" {
				c.notice = "Message cannot be empty"
				c.noticeError = true
				return true, false, nil
			}
			action := engine.TaskExplorerActionSend
			if !taskExplorerExecutionAllows(execution, action) {
				action = engine.TaskExplorerActionContinue
			}
			if c.action == nil ||
				!taskExplorerExecutionAllows(execution, action) {
				c.notice = "Agent messaging is unavailable"
				c.noticeError = true
				return true, false, nil
			}
			result, ok := c.submitAction(
				execution,
				snapshot,
				action,
				content,
			)
			if !ok {
				c.notice = firstNonEmptyTUIText(
					result.Message,
					result.Conflict,
					"Agent messaging conflicted with refreshed state",
				)
				c.noticeError = true
				return true, false, nil
			}
			c.active = false
			c.input.Blur()
			c.input.SetValue("")
			c.noticeError = false
			switch action {
			case engine.TaskExplorerActionContinue:
				c.notice = "Agent resumed"
			default:
				c.notice = "Message queued"
			}
			return true, true, nil
		default:
			var updated textinput.Model
			updated, cmd = c.input.Update(msg)
			c.input = updated
			return true, false, cmd
		}
	}

	switch msg.String() {
	case "i":
		if strings.TrimSpace(execution.Key.AgentID) == "" {
			return false, false, nil
		}
		c.notice = ""
		c.noticeError = false
		c.active = true
		return true, false, c.input.Focus()
	case "x":
		if !taskExplorerExecutionAllows(
			execution,
			engine.TaskExplorerActionCancel,
		) {
			return false, false, nil
		}
		if c.action == nil {
			c.notice = "Agent abort is unavailable"
			c.noticeError = true
			return true, false, nil
		}
		result, ok := c.submitAction(
			execution,
			snapshot,
			engine.TaskExplorerActionCancel,
			"",
		)
		if !ok {
			c.notice = firstNonEmptyTUIText(
				result.Message,
				result.Conflict,
				"Agent abort conflicted with refreshed state",
			)
			c.noticeError = true
			return true, false, nil
		}
		c.notice = "Abort requested"
		c.noticeError = false
		return true, true, nil
	case "p":
		action := engine.TaskExplorerActionPause
		if taskExplorerExecutionAllows(
			execution,
			engine.TaskExplorerActionResume,
		) {
			action = engine.TaskExplorerActionResume
		}
		if !taskExplorerExecutionAllows(execution, action) {
			return false, false, nil
		}
		if c.action == nil {
			c.notice = "Agent steering is unavailable"
			c.noticeError = true
			return true, false, nil
		}
		result, ok := c.submitAction(execution, snapshot, action, "")
		if !ok {
			c.notice = firstNonEmptyTUIText(
				result.Message,
				result.Conflict,
				"Agent steering conflicted with refreshed state",
			)
			c.noticeError = true
			return true, false, nil
		}
		if action == engine.TaskExplorerActionResume {
			c.notice = "Agent resumed"
		} else {
			c.notice = "Pause requested"
		}
		c.noticeError = false
		return true, true, nil
	default:
		return false, false, nil
	}
}

func (c *agentDetailControl) submitAction(
	execution engine.TaskExplorerExecution,
	snapshot engine.TaskExplorerSnapshot,
	action engine.TaskExplorerAction,
	payload string,
) (engine.TaskExplorerActionResult, bool) {
	if c.action == nil {
		return engine.TaskExplorerActionResult{}, false
	}
	requestID := uuid.NewString()
	result := c.action(engine.TaskExplorerActionRequest{
		RequestID:       requestID,
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         execution.Key.AgentID,
		Generation:      execution.Key.Generation,
		Action:          action,
		Payload:         payload,
	})
	return result, result.RequestID == requestID &&
		result.BoardID == snapshot.BoardID &&
		result.BoardRevision == snapshot.Revision.Board &&
		result.AgentID == execution.Key.AgentID &&
		result.Generation == execution.Key.Generation &&
		result.Action == action &&
		result.Conflict == ""
}

func (c *agentDetailControl) viewWithProfile(
	profile DisplayCellProfile,
	styles Styles,
	width int,
) string {
	width = max(8, width)
	if c.active {
		c.input.SetWidth(max(4, width-2))
		return modalProjectLine(profile, c.input.View(), width, 0)
	}
	if c.notice == "" {
		return ""
	}
	notice := modalEllipsize(profile, c.notice, width, 0, "...")
	if c.noticeError {
		return styles.Error.Render(notice)
	}
	return styles.ToolSuccess.Render(notice)
}

func (c *agentDetailControl) visible() bool {
	return c.active || c.notice != ""
}

func (t agentDetailTab) next() agentDetailTab {
	return (t + 1) % agentDetailTabCount
}

func (t agentDetailTab) previous() agentDetailTab {
	return (t + agentDetailTabCount - 1) % agentDetailTabCount
}

func renderAgentDetailTabs(styles Styles, selected agentDetailTab, width int) string {
	return renderAgentDetailTabsWithProfile(
		DefaultDisplayCellProfile(),
		styles,
		selected,
		width,
	)
}

func renderAgentDetailTabsWithProfile(
	profile DisplayCellProfile,
	styles Styles,
	selected agentDetailTab,
	width int,
) string {
	labels := []string{"Overview", "Activity", "Transcript", "Output", "Lineage"}
	if width < 52 {
		labels = []string{"Info", "Activity", "Chat", "Output", "Lineage"}
	}
	if width < 38 {
		labels = []string{"Info", "Act", "Chat", "Out", "Link"}
	}
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		if agentDetailTab(i) == selected {
			parts = append(parts, styles.Selected.Render(" "+label+" "))
		} else {
			parts = append(parts, styles.Subtle.Render(" "+label+" "))
		}
	}
	line := strings.Join(parts, " ")
	return modalProjectLine(profile, line, width, 0)
}

func buildAgentDetailLines(detail engine.AgentDetailSnapshot, tab agentDetailTab, width int, now time.Time) []string {
	return buildAgentDetailLinesWithProfile(
		DefaultDisplayCellProfile(),
		detail,
		tab,
		width,
		now,
	)
}

func buildAgentDetailLinesWithProfile(
	profile DisplayCellProfile,
	detail engine.AgentDetailSnapshot,
	tab agentDetailTab,
	width int,
	now time.Time,
) []string {
	width = max(12, width)
	switch tab {
	case agentDetailActivity:
		return buildAgentActivityLines(profile, detail, width)
	case agentDetailTranscript:
		return buildAgentTranscriptLines(profile, detail, width)
	case agentDetailOutput:
		return buildAgentOutputLines(profile, detail, width)
	case agentDetailLineage:
		return buildAgentLineageLines(profile, detail, width)
	default:
		return buildAgentOverviewLines(profile, detail, width, now)
	}
}

func buildAgentOverviewLines(
	profile DisplayCellProfile,
	detail engine.AgentDetailSnapshot,
	width int,
	now time.Time,
) []string {
	agent := detail.Agent
	lines := make([]string, 0, 20)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Status", firstNonEmptyTUIText(agent.Status, string(detail.Thread.Status)))
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Steering", detail.SteeringState)
	if detail.TotalPausedMS > 0 {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Paused total", formatDurationShort(time.Duration(detail.TotalPausedMS)*time.Millisecond))
	}
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Name", agent.Name)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Agent", agent.AgentID)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Type", agent.AgentType)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Model", agent.Model)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Permission", agent.PermissionMode)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Isolation", agent.Isolation)
	if agent.Generation > 0 {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Generation", fmt.Sprintf("%d", agent.Generation))
	}
	if !agent.StartedAt.IsZero() {
		end := now
		if !agent.CompletedAt.IsZero() {
			end = agent.CompletedAt
		}
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Elapsed", formatDurationShort(maxDuration(0, end.Sub(agent.StartedAt))))
	}
	if detail.PendingMessageCount > 0 {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Pending input", fmt.Sprintf("%d", detail.PendingMessageCount))
	}
	if detail.UnresolvedCount > 0 {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Attention", fmt.Sprintf("%d unresolved request(s)", detail.UnresolvedCount))
	}
	if detail.Thread.LastTerminal != nil {
		terminal := string(detail.Thread.LastTerminal.Reason)
		if detail.Thread.LastTerminal.Error != "" {
			terminal += ": " + detail.Thread.LastTerminal.Error
		}
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Terminal", terminal)
	} else if agent.Error != "" {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Error", agent.Error)
	}
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Storage", detail.Storage)
	if detail.LoadError != "" {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Read warning", detail.LoadError)
	}
	if len(lines) == 0 {
		return []string{"(no overview available)"}
	}
	return lines
}

func agentDetailControlHelpWithProfile(
	profile DisplayCellProfile,
	detail engine.AgentDetailSnapshot,
	mode engine.ThreadAttachmentMode,
	width int,
) string {
	if mode == engine.ThreadModeReplayOnly || mode == engine.ThreadModeEvictedTranscript {
		return modalProjectLine(
			profile,
			"  read-only replay · ←/→ view · ↑/↓ scroll · Esc back",
			max(8, width),
			0,
		)
	}
	status := detail.Agent.Status
	active := status == "running" || status == "waiting_input" || status == "paused"
	parts := []string{"i message"}
	if active {
		if detail.SteeringState == "paused" || status == "paused" {
			parts = append(parts, "p resume")
		} else {
			parts = append(parts, "p pause")
		}
		parts = append(parts, "x abort")
	}
	parts = append(parts, "\u2190/\u2192 view", "\u2191/\u2193 scroll", "Esc back")
	if width < 48 {
		parts = []string{"i msg"}
		if active {
			if detail.SteeringState == "paused" || status == "paused" {
				parts = append(parts, "p go")
			} else {
				parts = append(parts, "p pause")
			}
			parts = append(parts, "x stop")
		}
		parts = append(parts, "\u2190/\u2192", "Esc")
	}
	return modalProjectLine(
		profile,
		"  "+strings.Join(parts, " \u00b7 "),
		max(8, width),
		0,
	)
}

func buildAgentActivityLines(
	profile DisplayCellProfile,
	detail engine.AgentDetailSnapshot,
	width int,
) []string {
	agent := detail.Agent
	lines := make([]string, 0, 20)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Task", agent.Task)
	if agent.Description != "" && agent.Description != agent.Task {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Description", agent.Description)
	}
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Summary", agent.Progress.Summary)
	if agent.Progress.ToolUses > 0 {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Tool uses", fmt.Sprintf("%d", agent.Progress.ToolUses))
	}
	if agent.Progress.TotalTokens > 0 {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Tokens", fmt.Sprintf("%d", agent.Progress.TotalTokens))
	}
	if agent.Progress.DurationMS > 0 {
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Runtime", formatDurationShort(time.Duration(agent.Progress.DurationMS)*time.Millisecond))
	}
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Last tool", agent.Progress.LastToolName)
	if len(agent.Progress.RecentActivities) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Recent activity")
		for _, activity := range agent.Progress.RecentActivities {
			value := firstNonEmptyTUIText(activity.Description, activity.ToolName)
			if activity.ToolName != "" && activity.Description != "" {
				value = activity.ToolName + ": " + activity.Description
			}
			lines = appendAgentDetailWrappedWithProfile(profile, lines, width, "  "+value)
		}
	}
	if len(lines) == 0 {
		return []string{"(no activity yet)"}
	}
	return lines
}

func buildAgentTranscriptLines(
	profile DisplayCellProfile,
	detail engine.AgentDetailSnapshot,
	width int,
) []string {
	lines := make([]string, 0, len(detail.Messages)*3)
	if detail.Thread.DroppedMessages > 0 {
		lines = append(lines, fmt.Sprintf("[%d older live message(s) omitted]", detail.Thread.DroppedMessages), "")
	}
	for i, message := range detail.Messages {
		label := strings.ToUpper(firstNonEmptyTUIText(message.Role, "message"))
		if message.ToolName != "" {
			label += " " + message.ToolName
		}
		if !message.Completed {
			label += " (live)"
		}
		lines = append(lines, label)
		if message.ReasoningContent != "" {
			lines = appendAgentDetailWrappedWithProfile(profile, lines, width, "Thinking: "+message.ReasoningContent)
		}
		if message.Content != "" {
			lines = appendAgentDetailWrappedWithProfile(profile, lines, width, message.Content)
		}
		for _, call := range message.ToolCalls {
			callText := "Tool: " + firstNonEmptyTUIText(call.Name, call.ID)
			if call.InputPreview != "" {
				callText += " " + call.InputPreview
			}
			lines = appendAgentDetailWrappedWithProfile(profile, lines, width, callText)
		}
		if i < len(detail.Messages)-1 {
			lines = append(lines, "")
		}
	}
	if len(lines) == 0 {
		return []string{"(no messages yet)"}
	}
	return lines
}

func buildAgentOutputLines(
	profile DisplayCellProfile,
	detail engine.AgentDetailSnapshot,
	width int,
) []string {
	lines := make([]string, 0)
	if detail.OutputTruncated {
		lines = appendAgentDetailWrappedWithProfile(profile, lines, width, "[showing the bounded output tail]")
		lines = append(lines, "")
	}
	if detail.Output == "" {
		lines = append(lines, "(no output yet)")
	} else {
		lines = appendAgentDetailWrappedWithProfile(profile, lines, width, detail.Output)
	}
	if detail.Agent.OutputFile != "" {
		lines = append(lines, "")
		lines = appendAgentDetailFieldWithProfile(profile, lines, width, "File", detail.Agent.OutputFile)
	}
	return lines
}

func buildAgentLineageLines(
	profile DisplayCellProfile,
	detail engine.AgentDetailSnapshot,
	width int,
) []string {
	agent := detail.Agent
	lines := make([]string, 0, 16)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Session", agent.SessionID)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Thread", agent.ThreadID)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Parent session", agent.ParentSessionID)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Parent thread", agent.ParentThreadID)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Parent Agent", agent.ParentAgentID)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Spawn tool", agent.ParentToolUseID)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "CWD", agent.CWD)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Worktree", agent.WorktreePath)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Branch", agent.WorktreeBranch)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Transcript", agent.TranscriptPath)
	lines = appendAgentDetailFieldWithProfile(profile, lines, width, "Output", agent.OutputFile)
	if len(lines) == 0 {
		return []string{"(no lineage available)"}
	}
	return lines
}

func appendAgentDetailFieldWithProfile(
	profile DisplayCellProfile,
	lines []string,
	width int,
	label, value string,
) []string {
	if strings.TrimSpace(value) == "" {
		return lines
	}
	return appendAgentDetailWrappedWithProfile(profile, lines, width, label+": "+value)
}

func appendAgentDetailWrappedWithProfile(
	profile DisplayCellProfile,
	lines []string,
	width int,
	text string,
) []string {
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, profile.wrap(paragraph, max(1, width), false)...)
	}
	return lines
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func agentPanelDialogWidth(terminalWidth int) int {
	return max(18, min(80, terminalWidth-6))
}
