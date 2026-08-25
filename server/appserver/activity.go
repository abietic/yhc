package appserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
)

const (
	maxActivityEntries       = 100
	maxActivityIdentityBytes = 512
)

var (
	activityKinds = map[string]struct{}{
		"turn": {}, "tool": {}, "task": {}, "agent": {}, "interaction": {},
	}
	activityStates = map[string]struct{}{
		"started": {}, "running": {}, "waiting": {}, "paused": {},
		"completed": {}, "stopped": {}, "failed": {}, "resolved": {},
	}
	activityCategories = map[string]struct{}{
		"file_read": {}, "file_search": {}, "file_change": {}, "command": {},
		"network": {}, "task": {}, "agent": {}, "tool": {},
		"permission": {}, "question": {}, "plan_approval": {}, "repeated_tool": {},
	}
)

type activityLog struct {
	mu                        sync.Mutex
	entries                   []ActivityEntry
	pendingInteractionOrigins map[string]string
}

func newActivityLog() *activityLog {
	return &activityLog{
		entries:                   make([]ActivityEntry, 0, maxActivityEntries),
		pendingInteractionOrigins: make(map[string]string),
	}
}

func (l *activityLog) upsert(entry ActivityEntry) bool {
	_, updated := l.upsertEntry(entry)
	return updated
}

func (l *activityLog) upsertEntry(entry ActivityEntry) (ActivityEntry, bool) {
	if l == nil || !validActivityEntry(entry) {
		return ActivityEntry{}, false
	}
	entry.Timestamp = entry.Timestamp.UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry.Kind == "interaction" {
		if originTurnID := l.pendingInteractionOrigins[entry.ID]; originTurnID != "" {
			entry.TurnID = originTurnID
		}
	}
	for index, existing := range l.entries {
		if existing.ID != entry.ID {
			continue
		}
		if existing.Kind != entry.Kind {
			return ActivityEntry{}, false
		}
		if existing.TurnID != entry.TurnID {
			if existing.Kind != "interaction" {
				return ActivityEntry{}, false
			}
			entry.TurnID = existing.TurnID
		}
		if entry.Category == "" {
			entry.Category = existing.Category
		}
		if activityStateIsTerminal(existing.Kind, existing.State) && existing.State != entry.State {
			return ActivityEntry{}, false
		}
		if existing == entry {
			return ActivityEntry{}, false
		}
		copy(l.entries[index:], l.entries[index+1:])
		l.entries = l.entries[:len(l.entries)-1]
		break
	}
	if entry.Kind == "interaction" {
		switch entry.State {
		case "waiting":
			if l.pendingInteractionOrigins == nil {
				l.pendingInteractionOrigins = make(map[string]string)
			}
			if _, exists := l.pendingInteractionOrigins[entry.ID]; !exists {
				l.pendingInteractionOrigins[entry.ID] = entry.TurnID
			}
		case "resolved":
			delete(l.pendingInteractionOrigins, entry.ID)
		}
	}
	l.entries = append(l.entries, entry)
	if overflow := len(l.entries) - maxActivityEntries; overflow > 0 {
		copy(l.entries, l.entries[overflow:])
		l.entries = l.entries[:maxActivityEntries]
	}
	return entry, true
}

func activityStateIsTerminal(kind, state string) bool {
	if kind == "interaction" {
		return state == "resolved"
	}
	if kind == "turn" && state == "waiting" {
		return true
	}
	return state == "completed" || state == "stopped" || state == "failed"
}

func (l *activityLog) snapshot() []ActivityEntry {
	if l == nil {
		return []ActivityEntry{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ActivityEntry(nil), l.entries...)
}

func validActivityEntry(entry ActivityEntry) bool {
	if !safeActivityIdentity(entry.ID) || !safeActivityIdentity(entry.TurnID) {
		return false
	}
	if _, ok := activityKinds[entry.Kind]; !ok {
		return false
	}
	if _, ok := activityStates[entry.State]; !ok {
		return false
	}
	if entry.Category != "" {
		if _, ok := activityCategories[entry.Category]; !ok {
			return false
		}
	}
	return true
}

func safeActivityIdentity(value string) bool {
	return value != "" && len(value) <= maxActivityIdentityBytes && utf8.ValidString(value)
}

func activityID(kind, turnID, resourceID string) (string, bool) {
	if _, ok := activityKinds[kind]; !ok || !safeActivityIdentity(turnID) ||
		!safeActivityIdentity(resourceID) {
		return "", false
	}
	digest := sha256.Sum256([]byte(kind + "\x00" + turnID + "\x00" + resourceID))
	return "activity-" + hex.EncodeToString(digest[:]), true
}

func activityTimestamp(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func projectTurnActivity(
	turnID string,
	reason engine.TerminalReason,
	timestamp time.Time,
) (ActivityEntry, bool) {
	id, ok := activityID("turn", turnID, turnID)
	if !ok {
		return ActivityEntry{}, false
	}
	state := "started"
	switch reason {
	case "":
	case engine.TerminalCompleted:
		state = "completed"
	case engine.TerminalWaitingInput:
		state = "waiting"
	case engine.TerminalAbortedStreaming, engine.TerminalAbortedTools, engine.TerminalHookStopped:
		state = "stopped"
	default:
		state = "failed"
	}
	return ActivityEntry{
		ID: id, TurnID: turnID, Kind: "turn", State: state,
		Timestamp: activityTimestamp(timestamp),
	}, true
}

func projectSyntheticActivity(
	eventType, turnID string,
	data any,
	timestamp time.Time,
) (ActivityEntry, bool) {
	switch eventType {
	case "turn.accepted":
		return projectTurnActivity(turnID, "", timestamp)
	case "turn.finished":
		reason := engine.TerminalReason("")
		if fields, ok := data.(map[string]any); ok {
			switch value := fields["reason"].(type) {
			case engine.TerminalReason:
				reason = value
			case string:
				reason = engine.TerminalReason(value)
			}
		}
		if reason == "" {
			return ActivityEntry{}, false
		}
		return projectTurnActivity(turnID, reason, timestamp)
	default:
		return ActivityEntry{}, false
	}
}

func projectEngineActivity(event engine.QueryEvent, fallbackTurnID string) (ActivityEntry, bool) {
	turnID := strings.TrimSpace(event.TurnID)
	if turnID == "" {
		turnID = strings.TrimSpace(fallbackTurnID)
	}
	if !safeActivityIdentity(turnID) {
		return ActivityEntry{}, false
	}
	timestamp := activityTimestamp(event.Timestamp)
	switch event.Type {
	case engine.EventToolProgress:
		if event.ToolProgress == nil {
			return ActivityEntry{}, false
		}
		state := "running"
		if event.ToolProgress.IsFinal {
			state = "completed"
		}
		return activityForResource(
			"tool", state, toolActivityCategory(event.ToolProgress.ToolName),
			turnID, event.ToolProgress.ToolUseID, timestamp,
		)
	case engine.EventToolResult:
		message := event.ToolResultMessage
		if message == nil {
			message = event.Message
		}
		if message == nil {
			return ActivityEntry{}, false
		}
		return activityForResource(
			"tool", "completed", toolActivityCategory(message.ToolName),
			turnID, message.ToolCallID, timestamp,
		)
	case engine.EventCommandLifecycle:
		if event.CommandLifecycle == nil {
			return ActivityEntry{}, false
		}
		state := "running"
		if event.CommandLifecycle.Phase == engine.CommandLifecycleCompleted {
			state = "completed"
		} else if event.CommandLifecycle.Phase != engine.CommandLifecycleStarted {
			return ActivityEntry{}, false
		}
		return activityForResource(
			"tool", state, "command", turnID,
			event.CommandLifecycle.CommandUUID, timestamp,
		)
	case engine.EventTaskProgress:
		if event.TaskProgress == nil {
			return ActivityEntry{}, false
		}
		return activityForResource(
			"task", "running", "task", turnID, event.TaskProgress.TaskID, timestamp,
		)
	case engine.EventTaskLifecycle:
		if event.TaskLifecycle == nil {
			return ActivityEntry{}, false
		}
		state, ok := activityLifecycleState(event.TaskLifecycle.Status, event.TaskLifecycle.Phase)
		if !ok {
			return ActivityEntry{}, false
		}
		return activityForResource(
			"task", state, "task", turnID, event.TaskLifecycle.TaskID, timestamp,
		)
	case engine.EventAgentLifecycle:
		if event.AgentLifecycle == nil || !safeActivityIdentity(event.AgentID) {
			return ActivityEntry{}, false
		}
		state, ok := activityLifecycleState(event.AgentLifecycle.Status, event.AgentLifecycle.Phase)
		if !ok {
			return ActivityEntry{}, false
		}
		resourceID := fmt.Sprintf("%s:%d", event.AgentID, event.AgentGeneration)
		return activityForResource("agent", state, "agent", turnID, resourceID, timestamp)
	case engine.EventPermissionRequest:
		if event.PermissionRequest == nil {
			return ActivityEntry{}, false
		}
		category, ok := interactionActivityCategory(event.PermissionRequest.Kind)
		if !ok {
			return ActivityEntry{}, false
		}
		return activityForResource(
			"interaction", "waiting", category, turnID,
			event.PermissionRequest.ToolUseID, timestamp,
		)
	case engine.EventPermissionResolved:
		if event.PermissionResolved == nil {
			return ActivityEntry{}, false
		}
		category, _ := interactionActivityCategory(event.PermissionResolved.Kind)
		return activityForResource(
			"interaction", "resolved", category, turnID,
			event.PermissionResolved.ToolUseID, timestamp,
		)
	default:
		return ActivityEntry{}, false
	}
}

func activityForResource(
	kind, state, category, turnID, resourceID string,
	timestamp time.Time,
) (ActivityEntry, bool) {
	resourceID = strings.TrimSpace(resourceID)
	identityTurnID := turnID
	if kind == "interaction" {
		// A permission response resumes the graph under a fresh runtime turn, but
		// ToolUseID remains the session-local identity of the interaction.
		identityTurnID = resourceID
	}
	id, ok := activityID(kind, identityTurnID, resourceID)
	if !ok {
		return ActivityEntry{}, false
	}
	entry := ActivityEntry{
		ID: id, TurnID: turnID, Kind: kind, State: state,
		Category: category, Timestamp: timestamp,
	}
	return entry, validActivityEntry(entry)
}

func activityLifecycleState(values ...string) (string, bool) {
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "pending", "queued", "created", "starting", "started", "running", "in_progress", "launched":
			return "running", true
		case "paused", "waiting":
			return "paused", true
		case "completed", "complete", "succeeded", "success", "done":
			return "completed", true
		case "stopped", "killed", "cancelled", "canceled", "aborted":
			return "stopped", true
		case "failed", "error", "launch_failed":
			return "failed", true
		}
	}
	return "", false
}

func interactionActivityCategory(kind string) (string, bool) {
	switch kind {
	case engine.PermissionInteractionKindPermission:
		return "permission", true
	case engine.PermissionInteractionKindQuestion:
		return "question", true
	case engine.PermissionInteractionKindPlanApproval:
		return "plan_approval", true
	case engine.PermissionInteractionKindRepeatedTool:
		return "repeated_tool", true
	default:
		return "", false
	}
}

func toolActivityCategory(toolName string) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch {
	case name == "read" || name == "readfile" || name == "notebookread":
		return "file_read"
	case name == "grep" || name == "glob" || name == "ls" ||
		strings.Contains(name, "search"):
		return "file_search"
	case name == "write" || name == "edit" || name == "multiedit" ||
		name == "notebookedit" || strings.Contains(name, "patch"):
		return "file_change"
	case name == "bash" || name == "shell" || name == "terminal" ||
		strings.Contains(name, "command"):
		return "command"
	case strings.HasPrefix(name, "web") || strings.HasPrefix(name, "http") ||
		strings.Contains(name, "fetch"):
		return "network"
	case strings.HasPrefix(name, "task") || strings.Contains(name, "todo"):
		return "task"
	case name == "agent" || strings.Contains(name, "subagent"):
		return "agent"
	default:
		return "tool"
	}
}
