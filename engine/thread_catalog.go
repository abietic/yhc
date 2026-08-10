package engine

import (
	"sort"
	"strings"
	"time"
)

// ThreadAttachmentMode describes how a TUI may inspect a canonical thread.
type ThreadAttachmentMode string

const (
	ThreadModeLiveAttach        ThreadAttachmentMode = "live_attach"
	ThreadModeReplayOnly        ThreadAttachmentMode = "replay_only"
	ThreadModeEvictedTranscript ThreadAttachmentMode = "evicted_transcript"
)

// RuntimeThreadCatalogEntry is a payload-free index row. Event/message rings
// remain in RuntimeStateStore and full evicted content remains on disk.
type RuntimeThreadCatalogEntry struct {
	ThreadID        string
	SessionID       string
	AgentID         string
	ParentThreadID  string
	ParentAgentID   string
	ParentToolUseID string
	Name            string
	Description     string
	AgentType       string
	TranscriptPath  string
	Status          RuntimeThreadStatus
	Mode            ThreadAttachmentMode
	ActiveTurnID    string
	IsActive        bool
	HasLiveMessage  bool
	EventCount      int
	MessageCount    int
	PermissionCount int
	QuestionCount   int
	DroppedEvents   uint64
	DroppedMessages uint64
	LastSequence    uint64
	StartedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     time.Time
}

// RuntimeThreadCatalogSnapshot is the bounded thread index consumed by future
// navigation. It carries no ordinary history payloads.
type RuntimeThreadCatalogSnapshot struct {
	Revision       uint64
	ActiveThreadID string
	Threads        []RuntimeThreadCatalogEntry
}

// ThreadCatalogSnapshot returns the current engine-local thread index.
func (e *QueryEngine) ThreadCatalogSnapshot() RuntimeThreadCatalogSnapshot {
	if e == nil || e.runtimeState == nil {
		return RuntimeThreadCatalogSnapshot{}
	}
	e.mu.Lock()
	activeThreadID := e.config.ThreadID
	e.mu.Unlock()
	return e.runtimeState.ThreadCatalogSnapshot(activeThreadID)
}

// ThreadCatalogSnapshot joins retained runtime threads with Agent metadata
// whose bounded thread ring was evicted but whose transcript remains durable.
func (s *RuntimeStateStore) ThreadCatalogSnapshot(activeThreadID string) RuntimeThreadCatalogSnapshot {
	if s == nil {
		return RuntimeThreadCatalogSnapshot{ActiveThreadID: activeThreadID}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := RuntimeThreadCatalogSnapshot{
		Revision:       s.revision,
		ActiveThreadID: activeThreadID,
		Threads:        make([]RuntimeThreadCatalogEntry, 0, len(s.threads)+len(s.agents)),
	}
	seen := make(map[string]struct{}, len(s.threads))
	for threadID, thread := range s.threads {
		entry := RuntimeThreadCatalogEntry{
			ThreadID:        threadID,
			SessionID:       thread.SessionID,
			AgentID:         thread.AgentID,
			ParentThreadID:  thread.ParentThreadID,
			ParentAgentID:   thread.ParentAgentID,
			ParentToolUseID: thread.ParentToolUseID,
			Status:          thread.Status,
			Mode:            ThreadModeLiveAttach,
			ActiveTurnID:    thread.ActiveTurnID,
			IsActive:        threadID == activeThreadID,
			HasLiveMessage:  thread.LiveMessage != nil,
			EventCount:      len(thread.Events),
			MessageCount:    len(thread.Messages),
			DroppedEvents:   thread.DroppedEvents,
			DroppedMessages: thread.DroppedMessages,
			LastSequence:    thread.LastSequence,
			StartedAt:       thread.StartedAt,
			UpdatedAt:       thread.UpdatedAt,
			CompletedAt:     thread.CompletedAt,
		}
		if isRuntimeTerminalStatus(thread.Status) && len(thread.PendingInteractions) == 0 {
			entry.Mode = ThreadModeReplayOnly
		}
		for _, request := range thread.PendingInteractions {
			switch request.Kind {
			case "question":
				entry.QuestionCount++
			default:
				entry.PermissionCount++
			}
		}
		if agent, ok := s.agents[thread.AgentID]; ok {
			entry.TranscriptPath = agent.TranscriptPath
			entry.Name = agent.Name
			entry.Description = firstNonEmptyRuntimeCatalogText(agent.Description, agent.Task)
			entry.AgentType = agent.AgentType
		}
		snapshot.Threads = append(snapshot.Threads, entry)
		seen[threadID] = struct{}{}
	}
	for _, agent := range s.agents {
		if _, ok := seen[agent.ThreadID]; ok || strings.TrimSpace(agent.ThreadID) == "" || strings.TrimSpace(agent.TranscriptPath) == "" {
			continue
		}
		snapshot.Threads = append(snapshot.Threads, RuntimeThreadCatalogEntry{
			ThreadID:        agent.ThreadID,
			SessionID:       agent.SessionID,
			AgentID:         agent.AgentID,
			ParentThreadID:  agent.ParentThreadID,
			ParentAgentID:   agent.ParentAgentID,
			ParentToolUseID: agent.ParentToolUseID,
			Name:            agent.Name,
			Description:     firstNonEmptyRuntimeCatalogText(agent.Description, agent.Task),
			AgentType:       agent.AgentType,
			TranscriptPath:  agent.TranscriptPath,
			Status:          RuntimeThreadStatus(agent.Status),
			Mode:            ThreadModeEvictedTranscript,
			IsActive:        agent.ThreadID == activeThreadID,
			StartedAt:       agent.StartedAt,
			UpdatedAt:       agent.UpdatedAt,
			CompletedAt:     agent.CompletedAt,
		})
	}
	sort.Slice(snapshot.Threads, func(i, j int) bool {
		left, right := snapshot.Threads[i], snapshot.Threads[j]
		if left.IsActive != right.IsActive {
			return left.IsActive
		}
		if left.StartedAt.Equal(right.StartedAt) {
			return left.ThreadID < right.ThreadID
		}
		return left.StartedAt.Before(right.StartedAt)
	})
	return snapshot
}

func firstNonEmptyRuntimeCatalogText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
