package filehistory

import (
	"fmt"
	"time"
)

// TurnRecord is a fact-only lifecycle record. It intentionally omits model content.
type TurnRecord struct {
	SessionID, ThreadID, TurnID                                                  string
	StartedAt, FinishedAt                                                        time.Time
	TerminalReason                                                               string
	RestoreReason                                                                string
	ModelRequested                                                               bool
	RestoreBoundaryKnown, RestoreEligible                                        bool
	FileBoundarySequence                                                         uint64
	KnownToolRecords, KnownToolsSucceeded, KnownToolsFailed, KnownToolsCancelled int
	KnownToolsRunning, PendingInteractions                                       int
	ToolCountsBounded                                                            bool
}

func (j *RevisionJournal) RecordTurnStart(turnID, threadID string, at time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recordTurnStartLocked(TurnRecord{SessionID: j.sessionID, ThreadID: threadID, TurnID: turnID, StartedAt: at})
}

// RecordTurnStartWithRestoreBoundary records explicit restore eligibility for this turn.
func (j *RevisionJournal) RecordTurnStartWithRestoreBoundary(turnID, threadID string, at time.Time, eligible bool, reason string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recordTurnStartLocked(TurnRecord{SessionID: j.sessionID, ThreadID: threadID, TurnID: turnID, StartedAt: at, RestoreBoundaryKnown: true, RestoreEligible: eligible, RestoreReason: reason, FileBoundarySequence: uint64(len(j.order))})
}

func (j *RevisionJournal) recordTurnStartLocked(turn TurnRecord) error {
	if j.sealed {
		return ErrJournalClosed
	}
	if !validTurnStart(&turn, j.sessionID) {
		return fmt.Errorf("invalid revision journal turn start")
	}
	if prior, exists := j.turns[turn.TurnID]; exists {
		if prior.FinishedAt.IsZero() && sameTurnStart(prior, turn) {
			return nil
		}
		return fmt.Errorf("revision journal turn already exists")
	}
	if len(j.turnOrder) >= maxLogicalRecords {
		j.warning = ErrJournalCapacity.Error()
		return ErrJournalCapacity
	}
	if err := j.appendTurn("turn_start", turn); err != nil {
		return err
	}
	j.turns[turn.TurnID] = turn
	j.turnOrder = append(j.turnOrder, turn.TurnID)
	return nil
}

func (j *RevisionJournal) RecordTurnTerminal(turn TurnRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sealed {
		return ErrJournalClosed
	}
	if !validTurnTerminal(&turn, j.sessionID) {
		return fmt.Errorf("invalid revision journal turn terminal")
	}
	prior, exists := j.turns[turn.TurnID]
	if !exists || !prior.FinishedAt.IsZero() || !sameTurnStart(prior, turn) {
		return fmt.Errorf("invalid revision journal turn transition")
	}
	if err := j.appendTurn("turn_terminal", turn); err != nil {
		return err
	}
	j.turns[turn.TurnID] = turn
	return nil
}

func validTurnStart(turn *TurnRecord, sessionID string) bool {
	return turn != nil && turn.SessionID == sessionID && turn.ThreadID != "" && turn.TurnID != "" && utc(turn.StartedAt) && turn.FinishedAt.IsZero() && turn.TerminalReason == "" && validRestoreBoundary(turn) && !turn.ModelRequested && turn.KnownToolRecords == 0 && turn.KnownToolsSucceeded == 0 && turn.KnownToolsFailed == 0 && turn.KnownToolsCancelled == 0 && turn.KnownToolsRunning == 0 && turn.PendingInteractions == 0 && !turn.ToolCountsBounded
}

func validTurnTerminal(turn *TurnRecord, sessionID string) bool {
	if turn == nil || turn.SessionID != sessionID || turn.ThreadID == "" || turn.TurnID == "" || !utc(turn.StartedAt) || !utc(turn.FinishedAt) || turn.FinishedAt.Before(turn.StartedAt) || !safeTerminalReason(turn.TerminalReason) || !validRestoreBoundary(turn) {
		return false
	}
	if turn.KnownToolRecords < 0 || turn.KnownToolsSucceeded < 0 || turn.KnownToolsFailed < 0 || turn.KnownToolsCancelled < 0 || turn.KnownToolsRunning < 0 || turn.PendingInteractions < 0 {
		return false
	}
	remaining := turn.KnownToolRecords
	for _, count := range []int{turn.KnownToolsSucceeded, turn.KnownToolsFailed, turn.KnownToolsCancelled, turn.KnownToolsRunning} {
		if count > remaining {
			return false
		}
		remaining -= count
	}
	return true
}

func sameTurnStart(a, b TurnRecord) bool {
	if a.SessionID != b.SessionID || a.ThreadID != b.ThreadID || a.TurnID != b.TurnID || !a.StartedAt.Equal(b.StartedAt) || a.RestoreBoundaryKnown != b.RestoreBoundaryKnown || a.FileBoundarySequence != b.FileBoundarySequence {
		return false
	}
	if !a.RestoreBoundaryKnown {
		return !b.RestoreEligible && b.RestoreReason == ""
	}
	if !a.RestoreEligible {
		return !b.RestoreEligible && a.RestoreReason == b.RestoreReason
	}
	return (b.RestoreEligible && b.RestoreReason == "") || (!b.RestoreEligible && safeTerminalReason(b.RestoreReason))
}

func validRestoreBoundary(turn *TurnRecord) bool {
	if !turn.RestoreBoundaryKnown {
		return !turn.RestoreEligible && turn.RestoreReason == "" && turn.FileBoundarySequence == 0
	}
	if turn.FileBoundarySequence > maxLogicalRecords {
		return false
	}
	if turn.RestoreEligible {
		return turn.RestoreReason == ""
	}
	return safeTerminalReason(turn.RestoreReason)
}

func utc(t time.Time) bool { return !t.IsZero() && t.Location() == time.UTC }

func safeTerminalReason(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range []byte(s) {
		if c != '_' && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}
