package filehistory

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func turnStart() time.Time { return time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC) }

func terminal(start time.Time) TurnRecord {
	return TurnRecord{SessionID: "session-1", ThreadID: "thread", TurnID: "turn", StartedAt: start, FinishedAt: start.Add(time.Second), TerminalReason: "completed", ModelRequested: true, KnownToolRecords: 3, KnownToolsSucceeded: 1, KnownToolsFailed: 1, KnownToolsRunning: 1, PendingInteractions: 2, ToolCountsBounded: true}
}

func TestTurnJournalRoundTripAndVersions(t *testing.T) {
	j, root, dir := openJournal(t)
	start := turnStart()
	if err := j.RecordTurnStart("turn", "thread", start); err != nil {
		t.Fatal(err)
	}
	if got := j.Snapshot().Revision; got != 1 {
		t.Fatalf("start revision=%d", got)
	}
	if err := j.RecordTurnStart("turn", "thread", start); err != nil {
		t.Fatal(err)
	}
	if got := j.Snapshot().Revision; got != 1 {
		t.Fatalf("idempotent start wrote frame=%d", got)
	}
	if err := j.RecordTurnTerminal(terminal(start)); err != nil {
		t.Fatal(err)
	}
	if got := j.Snapshot().Revision; got != 2 {
		t.Fatalf("terminal revision=%d", got)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, journalFile))
	if err != nil {
		t.Fatal(err)
	}
	j, err = OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, journalFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-only replay rewrote journal")
	}
	s := j.Snapshot()
	if len(s.Turns) != 1 || s.Turns[0] != terminal(start) || s.Revision != 2 {
		t.Fatalf("bad turn replay: %#v", s)
	}
	if err := j.RecordTurnTerminal(terminal(start)); err == nil {
		t.Fatal("accepted duplicate terminal")
	}
}

func TestTurnJournalPendingAndValidation(t *testing.T) {
	j, root, dir := openJournal(t)
	start := turnStart()
	if err := j.RecordTurnStart("turn", "thread", start); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := j.Snapshot().Turns; len(got) != 1 || !got[0].FinishedAt.IsZero() {
		t.Fatalf("pending turn lost: %#v", got)
	}
	bad := terminal(start)
	for _, mutate := range []func(*TurnRecord){
		func(r *TurnRecord) { r.SessionID = "other" }, func(r *TurnRecord) { r.ThreadID = "other" },
		func(r *TurnRecord) { r.StartedAt = r.StartedAt.Add(time.Second) }, func(r *TurnRecord) { r.FinishedAt = r.StartedAt.Add(-time.Second) },
		func(r *TurnRecord) { r.TerminalReason = "Not safe" }, func(r *TurnRecord) { r.KnownToolsFailed = 4 },
	} {
		candidate := bad
		mutate(&candidate)
		if err := j.RecordTurnTerminal(candidate); err == nil {
			t.Fatal("accepted invalid terminal")
		}
	}
	if err := j.RecordTurnTerminal(bad); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordTurnStart("turn", "thread", start); err == nil {
		t.Fatal("revived terminal turn")
	}
}

func TestTurnFramesRejectMixedPayload(t *testing.T) {
	j, _, _ := openJournal(t)
	start := turnStart()
	f := journalFrame{Version: 1, Type: "turn_start", Root: j.rootPath, SessionID: j.sessionID, FrameSequence: 1, Turn: &TurnRecord{SessionID: j.sessionID, ThreadID: "thread", TurnID: "turn", StartedAt: start}, Record: RevisionRecord{ID: "mixed"}}
	if err := j.applyFrame(f); err == nil {
		t.Fatal("accepted mixed turn payload")
	}
	f.Record = RevisionRecord{}
	f.Turn = nil
	if err := j.applyFrame(f); err == nil {
		t.Fatal("accepted missing turn payload")
	}
}
