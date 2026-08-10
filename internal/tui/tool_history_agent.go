package tui

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type agentToolHistoryRenderer struct{}

type agentHistoryInput struct {
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	SubagentType    string `json:"subagent_type"`
	AgentType       string `json:"agent_type"`
	Model           string `json:"model"`
	RunInBackground bool   `json:"run_in_background"`
	Isolation       string `json:"isolation"`
	parseError      bool
}

func (agentToolHistoryRenderer) Render(state toolHistoryRenderState) string {
	state = state.normalized()
	input := parseAgentHistoryInput(state.Input)
	if state.Context.Mode == HistoryRenderRaw || state.Context.Mode == HistoryRenderTranscript {
		return renderAgentHistoryTranscript(state, input)
	}
	header := renderAgentHistoryHeader(state, input)
	if state.compact() {
		return header
	}
	if state.AgentTrace != nil {
		return header + "\n" + renderAgentToolTraceWithProfile(
			state.profile(),
			state.Context.Styles,
			*state.AgentTrace,
			state.Context.Width,
		)
	}
	if state.Output == "" {
		return header
	}
	body := formatAgentOutput(state.Output, state.fullOutput())
	body = renderIndentedResultWithProfile(
		state.profile(),
		state.Context.Styles,
		body,
		max(10, state.Context.Width-5),
		state.Status,
	)
	return header + "\n" + body
}

func parseAgentHistoryInput(input string) agentHistoryInput {
	var parsed agentHistoryInput
	if input == "" || input == "{}" {
		return parsed
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		parsed.parseError = true
	}
	if parsed.SubagentType == "" {
		parsed.SubagentType = parsed.AgentType
	}
	return parsed
}

func renderAgentHistoryHeader(state toolHistoryRenderState, input agentHistoryInput) string {
	styles := state.Context.Styles
	status, visualStatus := agentHistoryStatus(state)
	header := toolIcon(styles, visualStatus, state.SpinnerCount) + " " + toolNameStyled(styles, "Agent")
	statusStyle := styles.Subtle
	switch visualStatus {
	case ToolRunning:
		statusStyle = styles.ToolRunning
	case ToolSuccess:
		statusStyle = styles.ToolSuccess
	case ToolError:
		statusStyle = styles.ToolError
	}
	header += " " + statusStyle.Render(status)

	var detail []string
	if state.AgentTrace != nil && state.AgentTrace.AgentID != "" {
		detail = append(
			detail,
			"@"+truncateSingleLineWithProfile(state.profile(), state.AgentTrace.AgentID, 16),
		)
	}
	if input.SubagentType != "" {
		detail = append(detail, input.SubagentType)
	}
	if input.Description != "" {
		detail = append(detail, input.Description)
	} else if input.parseError {
		detail = append(detail, truncateSingleLineWithProfile(state.profile(), state.Input, 80))
	}
	if input.RunInBackground {
		detail = append(detail, "background")
	}
	if len(detail) > 0 {
		header += " " + styles.Subtle.Render("("+strings.Join(detail, " · ")+")")
	}
	return contentEllipsize(state.profile(), header, state.Context.Width, 0, "…")
}

func agentHistoryStatus(state toolHistoryRenderState) (string, ToolStatus) {
	if state.AgentTrace != nil {
		switch state.AgentTrace.Status {
		case "running":
			return "running", ToolRunning
		case "waiting_input":
			return "needs input", ToolRunning
		case "paused":
			return "paused", ToolPending
		case "completed":
			return "completed", ToolSuccess
		case "failed":
			return "failed", ToolError
		case "aborted":
			return "aborted", ToolError
		case "killed":
			return "killed", ToolError
		case "":
		default:
			return strings.ReplaceAll(state.AgentTrace.Status, "_", " "), state.DisplayStatus
		}
	}
	switch state.DisplayStatus {
	case ToolRunning:
		return "launching", ToolRunning
	case ToolPending:
		return "pending", ToolPending
	case ToolSuccess:
		return "completed", ToolSuccess
	case ToolError:
		return "failed", ToolError
	default:
		return "pending", ToolPending
	}
}

func renderAgentHistoryTranscript(state toolHistoryRenderState, input agentHistoryInput) string {
	var lines []string
	status, _ := agentHistoryStatus(state)
	if state.AgentTrace != nil && state.AgentTrace.AgentID != "" {
		lines = append(lines, "Agent "+state.AgentTrace.AgentID)
	} else {
		lines = append(lines, "Agent")
	}
	appendAgentTranscriptField := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, label+": "+value)
		}
	}
	appendAgentTranscriptField("Status", status)
	appendAgentTranscriptField("Description", input.Description)
	appendAgentTranscriptField("Type", input.SubagentType)
	appendAgentTranscriptField("Model", input.Model)
	appendAgentTranscriptField("Isolation", input.Isolation)
	appendAgentTranscriptField("Prompt", input.Prompt)
	if state.AgentTrace != nil {
		trace := state.AgentTrace
		appendAgentTranscriptField("Summary", trace.Summary)
		for _, activity := range trace.RecentActivities {
			appendAgentTranscriptField("Activity", agentActivityText(activity))
		}
		if trace.UnresolvedCount > 0 {
			lines = append(lines, fmt.Sprintf("Attention: %d unresolved", trace.UnresolvedCount))
		}
		appendAgentTranscriptField("Terminal reason", trace.TerminalReason)
		appendAgentTranscriptField("Error", trace.Error)
		appendAgentTranscriptField("Transcript", trace.TranscriptPath)
	} else {
		appendAgentTranscriptField("Output", state.Output)
	}
	return ansi.Strip(strings.Join(lines, "\n"))
}

type agentTraceHistoryItem struct {
	id      string
	version uint64
	trace   agentToolTrace
}

func (i *agentTraceHistoryItem) ID() string      { return i.id }
func (i *agentTraceHistoryItem) Version() uint64 { return i.version }
func (i *agentTraceHistoryItem) Finished() bool  { return !i.trace.active() }

func (i *agentTraceHistoryItem) Render(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	return renderAgentToolTraceWithProfile(ctx.displayCellProfile(), ctx.Styles, i.trace, ctx.Width)
}

func (i *agentTraceHistoryItem) Raw(HistoryRenderContext) string {
	state := toolHistoryRenderState{AgentTrace: &i.trace, DisplayStatus: ToolSuccess}
	return renderAgentHistoryTranscript(state, agentHistoryInput{})
}

func (i *agentTraceHistoryItem) Height(ctx HistoryRenderContext) int {
	return historyRenderedHeight(i.Render(ctx))
}

func (i *agentTraceHistoryItem) Selectable() bool    { return true }
func (i *agentTraceHistoryItem) NoSelectPrefix() int { return 5 }

func (i *agentTraceHistoryItem) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	rendered := i.Render(ctx)
	annotated, ok := selectionAnnotateVisibleRows(
		ctx.displayCellProfile(),
		rendered,
		i.NoSelectPrefix(),
	)
	if !ok {
		return selectionAnnotatedRender{rendered: rendered}
	}
	return selectionAnnotatedRender{rendered: annotated, annotated: true}
}

func (i *agentTraceHistoryItem) NestedHistoryItems() []HistoryItem {
	items := make([]HistoryItem, 0, len(i.trace.RecentActivities))
	seen := make(map[uint64]int)
	for _, activity := range i.trace.RecentActivities {
		hash := agentActivityHash(activity)
		occurrence := seen[hash]
		seen[hash]++
		items = append(items, &agentActivityHistoryItem{
			id:       fmt.Sprintf("%s:activity:%x:%d", i.id, hash, occurrence),
			activity: activity,
		})
	}
	return items
}

type agentActivityHistoryItem struct {
	id       string
	activity agentToolTraceActivity
}

func (i *agentActivityHistoryItem) ID() string      { return i.id }
func (i *agentActivityHistoryItem) Version() uint64 { return 1 }
func (i *agentActivityHistoryItem) Finished() bool  { return true }

func (i *agentActivityHistoryItem) Render(ctx HistoryRenderContext) string {
	return ctx.Styles.Subtle.Render(agentActivityText(i.activity))
}

func (i *agentActivityHistoryItem) Raw(HistoryRenderContext) string {
	return agentActivityText(i.activity)
}

func (i *agentActivityHistoryItem) Height(ctx HistoryRenderContext) int {
	return historyRenderedHeight(i.Render(ctx))
}

func (i *agentActivityHistoryItem) Selectable() bool    { return true }
func (i *agentActivityHistoryItem) NoSelectPrefix() int { return 0 }

func (i *agentActivityHistoryItem) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	rendered := i.Render(ctx)
	annotated, ok := selectionAnnotateVisibleRows(
		ctx.displayCellProfile(),
		rendered,
		0,
	)
	if !ok {
		return selectionAnnotatedRender{rendered: rendered}
	}
	return selectionAnnotatedRender{rendered: annotated, annotated: true}
}

func agentActivityHash(activity agentToolTraceActivity) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(activity.ToolName))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(activity.Description))
	return hash.Sum64()
}

func agentActivityText(activity agentToolTraceActivity) string {
	if activity.ToolName != "" && activity.Description != "" {
		return activity.ToolName + ": " + activity.Description
	}
	return firstNonEmpty(activity.Description, activity.ToolName)
}

func agentNestedHistoryItems(tool *ToolMessage) []HistoryItem {
	if tool == nil || tool.name != "Agent" || tool.agentTrace == nil {
		return nil
	}
	id := "agent:" + firstNonEmpty(tool.agentTrace.AgentID, tool.toolCallID)
	return []HistoryItem{&agentTraceHistoryItem{
		id:      id,
		version: tool.version,
		trace:   cloneAgentToolTrace(*tool.agentTrace),
	}}
}
