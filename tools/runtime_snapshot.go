package tools

import (
	"sort"
	"time"
)

// RuntimeTaskSnapshot is the bounded read-only task state visible to the
// current runtime. It intentionally contains listing metadata only: callers can
// render /tasks without gaining mutation or foreground/background controls.
type RuntimeTaskSnapshot struct {
	Agents     []RuntimeAgentSnapshot
	LocalTasks []LocalTaskSnapshot
}

// RuntimeAgentSnapshot carries the bounded state needed to list local/background
// Agent runtime entries.
type RuntimeAgentSnapshot struct {
	ID                  string
	SessionID           string
	ThreadID            string
	ParentSessionID     string
	ParentThreadID      string
	ParentAgentID       string
	Type                string
	AgentType           string
	Model               string
	PermissionMode      string
	Isolation           string
	CWD                 string
	WorktreePath        string
	WorktreeBranch      string
	Task                string
	Description         string
	Name                string
	ToolUseID           string
	Status              string
	DisplayMode         AgentDisplayMode
	Error               string
	Generation          int64
	StartedAt           time.Time
	CompletedAt         time.Time
	OutputFile          string
	TranscriptPath      string
	PendingMessageCount int
	ToolUseCount        int
	TokenCount          int
	LastToolName        string
	Summary             string
	RecentActivities    []ToolActivity
}

// LocalTaskSnapshot carries the bounded state needed to list local
// TaskCreate/TaskUpdate/TaskStop records.
type LocalTaskSnapshot struct {
	ID          string
	Subject     string
	Description string
	ActiveForm  string
	Status      TaskStatus
	Owner       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RuntimeTaskSnapshotFrom returns a stable, read-only snapshot of the runtime
// visible background agents and local task-list records. It does not drain
// lifecycle events or terminal notifications.
func RuntimeTaskSnapshotFrom(runner *AgentRunner, manager *TaskManager) RuntimeTaskSnapshot {
	return RuntimeTaskSnapshot{
		Agents:     runtimeAgentSnapshots(runner),
		LocalTasks: localTaskSnapshots(manager),
	}
}

func runtimeAgentSnapshots(runner *AgentRunner) []RuntimeAgentSnapshot {
	if runner == nil {
		return nil
	}
	runner.mu.RLock()
	agents := make([]*RunningAgent, 0, len(runner.activeAgents))
	for _, agent := range runner.activeAgents {
		agents = append(agents, agent)
	}
	runner.mu.RUnlock()

	snapshots := make([]RuntimeAgentSnapshot, 0, len(agents))
	for _, agent := range agents {
		agent.mu.Lock()
		lastToolName := ""
		if agent.Progress.LastActivity != nil {
			lastToolName = agent.Progress.LastActivity.ToolName
		}
		snapshot := RuntimeAgentSnapshot{
			ID:                  agent.ID,
			SessionID:           agent.SessionID,
			ThreadID:            agent.ThreadID,
			ParentSessionID:     agent.ParentSessionID,
			ParentThreadID:      agent.ParentThreadID,
			ParentAgentID:       agent.ParentAgentID,
			Type:                agent.Type,
			AgentType:           agent.Options.SubagentType,
			Model:               agent.Options.Model,
			PermissionMode:      firstNonEmpty(agent.Options.Mode, agent.Options.InheritedPermissionMode),
			Isolation:           agent.Options.Isolation,
			CWD:                 agent.Options.CWD,
			WorktreePath:        agent.WorktreePath,
			WorktreeBranch:      agent.WorktreeBranch,
			Task:                agent.Task,
			Description:         agent.Description,
			Name:                agent.Name,
			ToolUseID:           agent.ToolUseID,
			Status:              agent.Status,
			DisplayMode:         runtimeAgentDisplayMode(agent),
			Error:               agentErrorText(agent.Error),
			Generation:          agent.runGeneration,
			StartedAt:           agent.StartedAt,
			CompletedAt:         agent.CompletedAt,
			OutputFile:          agent.OutputFile,
			TranscriptPath:      agent.TranscriptPath,
			PendingMessageCount: agent.PendingMessageCount,
			ToolUseCount:        agent.Progress.ToolUseCount,
			TokenCount:          agent.Progress.TokenCount,
			LastToolName:        lastToolName,
			Summary:             agent.Progress.DisplaySummary(),
			RecentActivities:    cloneAgentProgress(agent.Progress).RecentActivities,
		}
		agent.mu.Unlock()
		snapshots = append(snapshots, snapshot)
	}

	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].StartedAt.Equal(snapshots[j].StartedAt) {
			return snapshots[i].ID < snapshots[j].ID
		}
		return snapshots[i].StartedAt.Before(snapshots[j].StartedAt)
	})
	return snapshots
}

func runtimeAgentDisplayMode(agent *RunningAgent) AgentDisplayMode {
	if agent == nil {
		return ""
	}
	if agent.Options.IsBackgroundExecution() {
		return DisplayModeBackground
	}
	if !agent.Options.IsForegroundExecution() {
		return ""
	}
	if agent.foregroundWait != nil && agent.foregroundWait.state == agentForegroundWaitBackgrounded {
		return DisplayModeBackgrounded
	}
	return DisplayModeForeground
}

func localTaskSnapshots(manager *TaskManager) []LocalTaskSnapshot {
	if manager == nil {
		return nil
	}
	records := manager.List()
	snapshots := make([]LocalTaskSnapshot, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		snapshots = append(snapshots, LocalTaskSnapshot{
			ID:          record.ID,
			Subject:     record.Subject,
			Description: record.Description,
			ActiveForm:  record.ActiveForm,
			Status:      record.Status,
			Owner:       record.Owner,
			CreatedAt:   record.CreatedAt,
			UpdatedAt:   record.UpdatedAt,
		})
	}
	return snapshots
}
