package engine

import (
	"time"

	"github.com/abietic/yhc/engine/filehistory"
)

// ExecutionSummary is a read-only join of separately identified fact sources.
// An execution outcome or successful tool result is not a verification receipt.
type ExecutionSummary struct {
	SessionID, ThreadID          string
	AsOf                         time.Time
	History                      WorkspaceChangeSnapshot
	LatestTurn                   *filehistory.TurnRecord
	ActiveTurnID                 string
	CurrentTaskStatuses          map[string]int
	TasksAvailable, TasksPartial bool
	AcceptedInputStatuses        map[string]int
	AcceptedInputsKnown          bool
	Verification                 string
}

// recordExecutionTurnEvent runs only at the production emitter's accepted
// boundary. Runtime/session replay never calls it and never starts work.
func (e *QueryEngine) recordExecutionTurnEvent(event QueryEvent) {
	if event.SessionID == "" || event.ThreadID == "" || event.TurnID == "" {
		return
	}
	var observed filehistory.TurnRecord
	terminal := event.Type == EventTerminal && event.TerminalInfo != nil
	if terminal && e.runtimeState != nil {
		if thread, ok := e.runtimeState.ThreadSnapshot(event.ThreadID); ok {
			observed.PendingInteractions = len(thread.PendingInteractions)
			for _, tool := range thread.Tools {
				if tool.TurnID != event.TurnID {
					continue
				}
				observed.KnownToolRecords++
				switch tool.Status {
				case "completed":
					observed.KnownToolsSucceeded++
				case "failed":
					observed.KnownToolsFailed++
				case "cancelled":
					observed.KnownToolsCancelled++
				case "running":
					observed.KnownToolsRunning++
				}
			}
		}
		// Tool/state rings are bounded. These are retained records, not an
		// inferred complete count of all actions in a long execution.
		observed.ToolCountsBounded = true
	}
	identity, c := e.lockWorkspaceRevisions()
	defer c.mu.Unlock()
	if identity.sessionID != event.SessionID || identity.threadID != event.ThreadID {
		return
	}
	journal := c.journalLocked(identity)
	if journal == nil {
		return
	}
	turn, found := c.turns[event.TurnID]
	if !found {
		if err := journal.RecordTurnStart(event.TurnID, event.ThreadID, event.Timestamp.UTC()); err != nil {
			c.warning = "Execution history is incomplete; the turn start was not recorded."
			return
		}
		turn = filehistory.TurnRecord{SessionID: event.SessionID, ThreadID: event.ThreadID, TurnID: event.TurnID, StartedAt: event.Timestamp.UTC()}
		c.turns[event.TurnID] = turn
	}
	if !turn.FinishedAt.IsZero() {
		return
	}
	if event.Type == EventStreamRequestStart {
		turn.ModelRequested = true
		c.turns[event.TurnID] = turn
	}
	if !terminal {
		return
	}
	observed.SessionID, observed.ThreadID, observed.TurnID = turn.SessionID, turn.ThreadID, turn.TurnID
	observed.StartedAt, observed.FinishedAt = turn.StartedAt, event.Timestamp.UTC()
	observed.TerminalReason = string(event.TerminalInfo.Reason)
	observed.ModelRequested = turn.ModelRequested
	if err := journal.RecordTurnTerminal(observed); err != nil {
		c.warning = "Execution history is incomplete; the terminal fact was not recorded."
		return
	}
	c.turns[event.TurnID] = observed
}

func (e *QueryEngine) ExecutionSummary() ExecutionSummary {
	summary := ExecutionSummary{AsOf: time.Now().UTC(), Verification: "not_recorded"}
	if e == nil {
		summary.History.Warning = "Execution summary requires an engine."
		return summary
	}
	history := e.WorkspaceChanges()
	summary.History, summary.SessionID, summary.ThreadID = history, history.SessionID, history.ThreadID
	if len(history.Turns) > 0 {
		last := history.Turns[len(history.Turns)-1]
		summary.LatestTurn = &last
	}
	if e.runtimeState != nil {
		if thread, ok := e.runtimeState.ThreadSnapshot(history.ThreadID); ok && thread.SessionID == history.SessionID {
			summary.ActiveTurnID = thread.ActiveTurnID
		}
	}
	tasks := e.TaskExplorerSnapshot()
	if tasks.Available && tasks.SessionID == history.SessionID {
		summary.TasksAvailable = true
		summary.CurrentTaskStatuses = make(map[string]int)
		for _, item := range tasks.WorkItems {
			summary.CurrentTaskStatuses[item.Status]++
		}
		summary.TasksPartial = tasks.Hidden.WorkBoardOutsidePrimary > 0
		for _, count := range tasks.Hidden.WorkItems {
			summary.TasksPartial = summary.TasksPartial || count > 0
		}
	}
	if queued, err := e.QueuedPromptInputs(); err == nil {
		summary.AcceptedInputsKnown = true
		summary.AcceptedInputStatuses = make(map[string]int)
		for _, item := range queued {
			summary.AcceptedInputStatuses[string(item.State)]++
		}
	}
	currentSession, currentThread, currentGeneration := e.WorkspaceHistoryIdentity()
	if currentSession != history.SessionID || currentThread != history.ThreadID || currentGeneration != history.OwnerGeneration {
		return ExecutionSummary{SessionID: currentSession, ThreadID: currentThread, AsOf: time.Now().UTC(), Verification: "not_recorded", History: WorkspaceChangeSnapshot{SessionID: currentSession, ThreadID: currentThread, OwnerGeneration: currentGeneration, Warning: "Session changed while reading execution facts; refresh."}}
	}
	return summary
}
