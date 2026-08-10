package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/engine"
)

type agentToolTraceActivity struct {
	ToolName    string
	Description string
}

type agentToolTrace struct {
	AgentID          string
	ParentToolUseID  string
	ExecutionKey     engine.RuntimeExecutionKey
	IdentityObserved bool
	IdentityResolved bool
	Status           string
	Summary          string
	LastToolName     string
	Error            string
	TranscriptPath   string
	TerminalReason   string
	ToolUses         int
	TotalTokens      int
	UnresolvedCount  int
	StartedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      time.Time
	RecentActivities []agentToolTraceActivity
}

func agentToolTraceFromSnapshot(snapshot engine.AgentParentTraceSnapshot) agentToolTrace {
	trace := agentToolTrace{
		AgentID:         snapshot.AgentID,
		ParentToolUseID: snapshot.ParentToolUseID,
		Status:          snapshot.Status,
		Summary:         snapshot.Summary,
		LastToolName:    snapshot.LastToolName,
		Error:           snapshot.Error,
		TranscriptPath:  snapshot.TranscriptPath,
		TerminalReason:  string(snapshot.TerminalReason),
		ToolUses:        snapshot.ToolUses,
		TotalTokens:     snapshot.TotalTokens,
		UnresolvedCount: snapshot.UnresolvedCount,
		StartedAt:       snapshot.StartedAt,
		UpdatedAt:       snapshot.UpdatedAt,
		CompletedAt:     snapshot.CompletedAt,
	}
	for _, activity := range snapshot.RecentActivities {
		trace.RecentActivities = append(trace.RecentActivities, agentToolTraceActivity{
			ToolName: activity.ToolName, Description: activity.Description,
		})
	}
	return trace
}

func (t agentToolTrace) active() bool {
	switch t.Status {
	case "running", "paused", "waiting_input":
		return true
	default:
		return false
	}
}

func equalAgentToolTrace(left, right *agentToolTrace) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.AgentID != right.AgentID || left.ParentToolUseID != right.ParentToolUseID ||
		left.ExecutionKey != right.ExecutionKey || left.IdentityResolved != right.IdentityResolved ||
		left.IdentityObserved != right.IdentityObserved ||
		left.Status != right.Status || left.Summary != right.Summary ||
		left.LastToolName != right.LastToolName || left.Error != right.Error || left.TranscriptPath != right.TranscriptPath ||
		left.TerminalReason != right.TerminalReason || left.ToolUses != right.ToolUses || left.TotalTokens != right.TotalTokens ||
		left.UnresolvedCount != right.UnresolvedCount || !left.StartedAt.Equal(right.StartedAt) ||
		!left.UpdatedAt.Equal(right.UpdatedAt) || !left.CompletedAt.Equal(right.CompletedAt) ||
		len(left.RecentActivities) != len(right.RecentActivities) {
		return false
	}
	for i := range left.RecentActivities {
		if left.RecentActivities[i] != right.RecentActivities[i] {
			return false
		}
	}
	return true
}

func resolveAgentToolTraceIdentity(
	trace agentToolTrace,
	explorer engine.TaskExplorerSnapshot,
) agentToolTrace {
	var matched engine.RuntimeExecutionKey
	found := false
	trace.IdentityObserved = true
	for _, execution := range explorer.Executions {
		if execution.Key.AgentID != trace.AgentID ||
			execution.ParentToolUseID != trace.ParentToolUseID {
			continue
		}
		if found {
			trace.ExecutionKey = engine.RuntimeExecutionKey{}
			trace.IdentityResolved = false
			return trace
		}
		matched = execution.Key
		found = true
	}
	trace.ExecutionKey = matched
	trace.IdentityResolved = found
	return trace
}

func cloneAgentToolTrace(trace agentToolTrace) agentToolTrace {
	trace.RecentActivities = append([]agentToolTraceActivity(nil), trace.RecentActivities...)
	return trace
}

func renderAgentToolTraceWithProfile(
	profile DisplayCellProfile,
	styles Styles,
	trace agentToolTrace,
	width int,
) string {
	width = max(12, width)
	status := firstNonEmptyTUIText(trace.Status, "running")
	statusParts := []string{status}
	if trace.TerminalReason != "" && trace.TerminalReason != status {
		statusParts = append(statusParts, trace.TerminalReason)
	}
	if trace.ToolUses > 0 {
		statusParts = append(statusParts, fmt.Sprintf("%d tools", trace.ToolUses))
	}
	if trace.TotalTokens > 0 {
		statusParts = append(statusParts, humanTokens(trace.TotalTokens))
	}
	end := trace.UpdatedAt
	if !trace.CompletedAt.IsZero() {
		end = trace.CompletedAt
	}
	if !trace.StartedAt.IsZero() && end.After(trace.StartedAt) {
		statusParts = append(statusParts, formatDurationShort(end.Sub(trace.StartedAt)))
	}
	statusText := strings.Join(statusParts, " \u00b7 ")
	switch status {
	case "completed":
		statusText = styles.ToolSuccess.Render(statusText)
	case "failed", "aborted", "killed":
		statusText = styles.ToolError.Render(statusText)
	default:
		statusText = styles.Subtle.Render(statusText)
	}

	lines := []string{boundedAgentTraceLineWithProfile(profile, "  \u23bf  ", statusText, width)}
	if strings.TrimSpace(trace.Summary) != "" {
		lines = append(lines, boundedAgentTraceLineWithProfile(profile, "     ", styles.Subtle.Render(trace.Summary), width))
	}
	for _, activity := range trace.RecentActivities {
		value := firstNonEmptyTUIText(activity.Description, activity.ToolName)
		if activity.ToolName != "" && activity.Description != "" {
			value = activity.ToolName + ": " + activity.Description
		}
		if value != "" {
			lines = append(lines, boundedAgentTraceLineWithProfile(profile, "     ", styles.Subtle.Render(value), width))
		}
	}
	if len(trace.RecentActivities) == 0 && strings.TrimSpace(trace.LastToolName) != "" {
		lines = append(lines, boundedAgentTraceLineWithProfile(profile, "     ", styles.Subtle.Render("Using "+trace.LastToolName), width))
	}
	if trace.UnresolvedCount > 0 {
		label := "request needs attention"
		if trace.UnresolvedCount != 1 {
			label = "requests need attention"
		}
		lines = append(lines, boundedAgentTraceLineWithProfile(profile, "     ", styles.ToolError.Render(fmt.Sprintf("! %d %s", trace.UnresolvedCount, label)), width))
	}
	if strings.TrimSpace(trace.Error) != "" {
		lines = append(lines, boundedAgentTraceLineWithProfile(profile, "     ", styles.ToolError.Render(trace.Error), width))
	}
	link := styles.ToolName.Underline(true).Render("\u2197 Open Agent details")
	lines = append(lines, boundedAgentTraceLineWithProfile(profile, "     ", link, width))
	return strings.Join(lines, "\n")
}

func boundedAgentTraceLineWithProfile(
	profile DisplayCellProfile,
	prefix, content string,
	width int,
) string {
	return contentProjectLine(profile, prefix+content, width, 0)
}
