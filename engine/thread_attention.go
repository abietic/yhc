package engine

import "sort"

// RuntimeThreadAttentionSnapshot is the unresolved-only canonical projection
// used to reconcile TUI response handles after switching or event replay.
type RuntimeThreadAttentionSnapshot struct {
	SessionID string
	ThreadID  string
	AgentID   string
	Status    RuntimeThreadStatus
	Requests  []RuntimeInteractionSnapshot
}

// ThreadAttentionSnapshots returns unresolved interactions for all retained
// threads. Terminal-with-pending threads remain retained by store policy.
func (e *QueryEngine) ThreadAttentionSnapshots() []RuntimeThreadAttentionSnapshot {
	if e == nil || e.runtimeState == nil {
		return nil
	}
	return e.runtimeState.ThreadAttentionSnapshots()
}

func (s *RuntimeStateStore) ThreadAttentionSnapshots() []RuntimeThreadAttentionSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	attention := make([]RuntimeThreadAttentionSnapshot, 0)
	for _, thread := range s.threads {
		if len(thread.PendingInteractions) == 0 {
			continue
		}
		row := RuntimeThreadAttentionSnapshot{
			SessionID: thread.SessionID,
			ThreadID:  thread.ThreadID,
			AgentID:   thread.AgentID,
			Status:    thread.Status,
			Requests:  make([]RuntimeInteractionSnapshot, 0, len(thread.PendingInteractions)),
		}
		for _, request := range thread.PendingInteractions {
			request.Input = cloneRuntimeMap(request.Input)
			row.Requests = append(row.Requests, request)
		}
		sort.Slice(row.Requests, func(i, j int) bool {
			if row.Requests[i].Sequence == row.Requests[j].Sequence {
				return row.Requests[i].ID < row.Requests[j].ID
			}
			return row.Requests[i].Sequence < row.Requests[j].Sequence
		})
		attention = append(attention, row)
	}
	sort.Slice(attention, func(i, j int) bool {
		left, right := s.threads[attention[i].ThreadID], s.threads[attention[j].ThreadID]
		if left.StartedAt.Equal(right.StartedAt) {
			return attention[i].ThreadID < attention[j].ThreadID
		}
		return left.StartedAt.Before(right.StartedAt)
	})
	return attention
}
