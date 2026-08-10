package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

const (
	maxTrackedSubagentToolCallsPerRequest = 256
	maxSubagentProgressRawInputBytes      = 64 * 1024
	maxSubagentProgressInputPreviewBytes  = 8 * 1024
)

type trackedSubagentActivity struct {
	key      string
	activity tools.ToolActivity
}

// subagentProgressTracker adapts Eino's chunked assistant events to the
// reference local-agent progress semantics. Per-request tool identities are
// reset at each model request so the tracker remains bounded across long runs.
type subagentProgressTracker struct {
	requestIndex      int
	requestStarted    bool
	currentUsageSeen  bool
	latestInputTokens int
	currentOutput     int
	cumulativeOutput  int
	toolUseCount      int
	seenCurrentCalls  map[string]struct{}
	recent            []trackedSubagentActivity
}

func newSubagentProgressTracker() *subagentProgressTracker {
	return &subagentProgressTracker{seenCurrentCalls: make(map[string]struct{})}
}

func (t *subagentProgressTracker) StartRequest() {
	if t.requestStarted {
		t.cumulativeOutput += t.currentOutput
	}
	t.requestStarted = true
	t.requestIndex++
	t.currentUsageSeen = false
	t.currentOutput = 0
	t.seenCurrentCalls = make(map[string]struct{})
}

func (t *subagentProgressTracker) Bootstrap(messages []*schema.Message) {
	for _, message := range messages {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		t.StartRequest()
		t.observeAssistantMessage(message)
	}
}

func (t *subagentProgressTracker) Observe(event QueryEvent) bool {
	switch event.Type {
	case EventStreamRequestStart:
		t.StartRequest()
		return false
	case EventAssistant:
		message := event.AssistantMessage
		if message == nil {
			message = event.Message
		}
		return t.observeAssistantMessage(message)
	default:
		return false
	}
}

func (t *subagentProgressTracker) observeAssistantMessage(message *schema.Message) bool {
	if message == nil || message.Role != schema.Assistant {
		return false
	}
	if !t.requestStarted {
		t.StartRequest()
	}

	changed := t.observeUsage(message.ResponseMeta)
	for index := range message.ToolCalls {
		if t.observeToolCall(message.ToolCalls[index], index) {
			changed = true
		}
	}
	return changed
}

func (t *subagentProgressTracker) observeUsage(meta *schema.ResponseMeta) bool {
	if meta == nil || meta.Usage == nil {
		return false
	}
	usage := meta.Usage
	input := max(usage.PromptTokens, 0)
	output := max(usage.CompletionTokens, 0)
	if usage.TotalTokens > input+output {
		switch {
		case input == 0:
			input = usage.TotalTokens - output
		case output == 0:
			output = usage.TotalTokens - input
		}
	}

	changed := false
	if !t.currentUsageSeen || input > t.latestInputTokens {
		t.latestInputTokens = input
		changed = true
	}
	if output > t.currentOutput {
		t.currentOutput = output
		changed = true
	}
	t.currentUsageSeen = true
	return changed
}

func (t *subagentProgressTracker) observeToolCall(call schema.ToolCall, index int) bool {
	name := strings.TrimSpace(call.Function.Name)
	if name == "" {
		return false
	}
	key := strings.TrimSpace(call.ID)
	if key == "" {
		key = fmt.Sprintf("request-%d:%d:%s", t.requestIndex, index, name)
	}

	_, seen := t.seenCurrentCalls[key]
	if !seen {
		if len(t.seenCurrentCalls) >= maxTrackedSubagentToolCallsPerRequest {
			return false
		}
		t.seenCurrentCalls[key] = struct{}{}
		t.toolUseCount++
	}

	if name == "SyntheticOutput" {
		return !seen
	}

	activity := buildSubagentToolActivity(name, call.Function.Arguments)
	for i := len(t.recent) - 1; i >= 0; i-- {
		if t.recent[i].key == key {
			if len(activity.Input) == 0 && len(t.recent[i].activity.Input) > 0 {
				activity.Input = t.recent[i].activity.Input
			}
			if reflect.DeepEqual(t.recent[i].activity, activity) {
				return false
			}
			t.recent[i].activity = activity
			return true
		}
	}

	t.recent = append(t.recent, trackedSubagentActivity{key: key, activity: activity})
	if len(t.recent) > tools.MaxAgentRecentActivities {
		t.recent = append([]trackedSubagentActivity(nil), t.recent[len(t.recent)-tools.MaxAgentRecentActivities:]...)
	}
	return true
}

func (t *subagentProgressTracker) Progress() tools.AgentProgress {
	progress := tools.AgentProgress{
		ToolUseCount: t.toolUseCount,
		TokenCount:   t.TokenCount(),
	}
	if len(t.recent) > 0 {
		progress.RecentActivities = make([]tools.ToolActivity, len(t.recent))
		for i, entry := range t.recent {
			progress.RecentActivities[i] = entry.activity
		}
		progress.ActivitySummary = t.recent[len(t.recent)-1].activity.ActivityDescription
	}
	return tools.NormalizeAgentProgress(progress)
}

func (t *subagentProgressTracker) TokenCount() int {
	return t.latestInputTokens + t.cumulativeOutput + t.currentOutput
}

func (t *subagentProgressTracker) HasProgress() bool {
	return t.toolUseCount > 0 || t.TokenCount() > 0 || len(t.recent) > 0
}

func buildSubagentToolActivity(name, rawInput string) tools.ToolActivity {
	input := make(map[string]any)
	if len(rawInput) > maxSubagentProgressRawInputBytes {
		input = map[string]any{
			"_input_truncated": true,
			"_preview":         truncateSubagentProgressBytes(rawInput, maxSubagentProgressInputPreviewBytes-256),
		}
	} else if strings.TrimSpace(rawInput) != "" {
		if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
			input = nil
		}
	}

	lowerName := strings.ToLower(name)
	activity := tools.ToolActivity{
		ToolName: name,
		Input:    input,
		IsSearch: lowerName == "grep" || lowerName == "glob" || lowerName == "toolsearch" || lowerName == "websearch",
		IsRead:   lowerName == "read" || lowerName == "ls",
	}
	activity.ActivityDescription = describeSubagentToolActivity(name, input)
	progress := tools.NormalizeAgentProgress(tools.AgentProgress{RecentActivities: []tools.ToolActivity{activity}})
	return progress.RecentActivities[0]
}

func truncateSubagentProgressBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && (value[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return value[:maxBytes]
}

func describeSubagentToolActivity(name string, input map[string]any) string {
	value := func(keys ...string) string {
		for _, key := range keys {
			if raw, ok := input[key]; ok {
				if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
		return ""
	}

	switch strings.ToLower(name) {
	case "read":
		return describeSubagentAction("Reading", value("file_path", "path"), name)
	case "ls":
		return describeSubagentAction("Listing", value("path"), name)
	case "grep", "websearch", "toolsearch":
		return describeSubagentAction("Searching for", value("pattern", "query"), name)
	case "glob":
		return describeSubagentAction("Finding", value("pattern", "path"), name)
	case "bash":
		return describeSubagentAction("Running", value("command"), name)
	case "edit", "write":
		return describeSubagentAction("Editing", value("file_path", "path"), name)
	case "agent":
		return describeSubagentAction("Delegating", value("description", "prompt"), name)
	default:
		return "Using " + name
	}
}

func describeSubagentAction(verb, subject, fallbackName string) string {
	if subject == "" {
		return "Using " + fallbackName
	}
	return verb + " " + subject
}

func (e *QueryEngine) configureSubagentProgress(
	messages []*schema.Message,
	description string,
	toolUseID string,
	observer func(tools.AgentProgress),
) *subagentProgressTracker {
	tracker := newSubagentProgressTracker()
	tracker.Bootstrap(messages)
	e.subagentProgress = tracker
	e.subagentProgressObserver = observer
	e.subagentProgressDescription = description
	e.subagentProgressToolUseID = toolUseID
	e.subagentProgressSeedPending = tracker.HasProgress()
	if tracker.HasProgress() && observer != nil {
		observer(tracker.Progress())
	}
	return tracker
}

func (e *QueryEngine) deriveSubagentProgressEvent(event QueryEvent) *QueryEvent {
	if e == nil || e.subagentProgress == nil || event.Type == EventTaskProgress {
		return nil
	}
	changed := e.subagentProgress.Observe(event)
	if event.Type == EventStreamRequestStart && e.subagentProgressSeedPending {
		changed = e.subagentProgress.HasProgress()
		e.subagentProgressSeedPending = false
	}
	if !changed {
		return nil
	}

	progress := e.subagentProgress.Progress()
	if e.subagentProgressObserver != nil {
		e.subagentProgressObserver(progress)
	}
	if strings.TrimSpace(e.config.AgentID) == "" {
		return nil
	}
	recent := make([]TaskProgressActivity, 0, len(progress.RecentActivities))
	for _, activity := range progress.RecentActivities {
		recent = append(recent, TaskProgressActivity{
			ToolName:    activity.ToolName,
			Description: activity.ActivityDescription,
			IsSearch:    activity.IsSearch,
			IsRead:      activity.IsRead,
		})
	}
	lastToolName := ""
	if progress.LastActivity != nil {
		lastToolName = progress.LastActivity.ToolName
	}
	return &QueryEvent{
		Type: EventTaskProgress,
		TaskProgress: &TaskProgressEvent{
			Type:             "system",
			Subtype:          "task_progress",
			TaskID:           e.config.AgentID,
			ToolUseID:        e.subagentProgressToolUseID,
			Description:      e.subagentProgressDescription,
			Usage:            TaskProgressUsage{TotalTokens: progress.TokenCount, ToolUses: progress.ToolUseCount},
			LastToolName:     lastToolName,
			Summary:          progress.DisplaySummary(),
			RecentActivities: recent,
		},
	}
}

func publishSubagentProgress(ctx context.Context, agentID string, progress tools.AgentProgress) {
	if agentID == "" {
		return
	}
	runner := tools.AgentRunnerFromCtx(ctx)
	if runner == nil {
		return
	}
	snapshot, ok := runner.GetAgentSnapshot(agentID)
	if !ok || snapshot.Status != "running" {
		return
	}
	_ = runner.UpdateAgentProgress(agentID, progress)
}
