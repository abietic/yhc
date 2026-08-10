package engine

import (
	"sort"
	"strings"
	"time"
)

const maxAgentParentTraceActivities = 3

// AgentParentTraceSnapshot is the bounded child summary rendered under the
// spawning Agent tool call. Full messages and output stay in Agent detail.
type AgentParentTraceSnapshot struct {
	Revision         uint64
	AgentID          string
	ParentToolUseID  string
	Name             string
	Task             string
	Status           string
	Summary          string
	LastToolName     string
	Error            string
	TranscriptPath   string
	TerminalReason   TerminalReason
	ToolUses         int
	TotalTokens      int
	UnresolvedCount  int
	StartedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      time.Time
	RecentActivities []RuntimeAgentActivitySnapshot
}

// AgentParentTraceSnapshots returns deterministic, payload-free summaries for
// Agent tool calls. It does not clone message, tool-output, or event rings.
func (e *QueryEngine) AgentParentTraceSnapshots() []AgentParentTraceSnapshot {
	if e == nil || e.runtimeState == nil {
		return nil
	}
	return e.runtimeState.AgentParentTraceSnapshots()
}

// AgentParentTraceSnapshots snapshots all child rows under one reducer read
// lock so a TUI refresh cannot combine Agent and thread state from revisions.
func (s *RuntimeStateStore) AgentParentTraceSnapshots() []AgentParentTraceSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]AgentParentTraceSnapshot, 0, len(s.agents))
	for _, agent := range s.agents {
		if strings.TrimSpace(agent.ParentToolUseID) == "" {
			continue
		}
		trace := AgentParentTraceSnapshot{
			Revision:        s.revision,
			AgentID:         agent.AgentID,
			ParentToolUseID: agent.ParentToolUseID,
			Name:            agent.Name,
			Task:            agent.Task,
			Status:          agent.Status,
			Summary:         agent.Progress.Summary,
			LastToolName:    agent.Progress.LastToolName,
			Error:           agent.Error,
			TranscriptPath:  agent.TranscriptPath,
			ToolUses:        agent.Progress.ToolUses,
			TotalTokens:     agent.Progress.TotalTokens,
			StartedAt:       agent.StartedAt,
			UpdatedAt:       agent.UpdatedAt,
			CompletedAt:     agent.CompletedAt,
		}
		activities := agent.Progress.RecentActivities
		if len(activities) > maxAgentParentTraceActivities {
			activities = activities[len(activities)-maxAgentParentTraceActivities:]
		}
		trace.RecentActivities = append([]RuntimeAgentActivitySnapshot(nil), activities...)
		if thread := s.threads[agent.ThreadID]; thread != nil {
			if strings.TrimSpace(trace.Status) == "" {
				trace.Status = string(thread.Status)
			}
			trace.UnresolvedCount = len(thread.PendingInteractions)
			if thread.LastTerminal != nil {
				trace.TerminalReason = thread.LastTerminal.Reason
				if trace.Error == "" {
					trace.Error = thread.LastTerminal.Error
				}
			}
		}
		out = append(out, trace)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out
}
