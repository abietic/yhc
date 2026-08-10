package workboard

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
)

func TestProjectionBootstrapIdempotentAndNewer(t *testing.T) {
	current := projectionTestRecord(t, "board", 2)
	reducer := NewProjectionReducer()
	if err := reducer.Bootstrap(current); err != nil {
		t.Fatalf("bootstrap current: %v", err)
	}
	if err := reducer.Bootstrap(current); err != nil {
		t.Fatalf("bootstrap identical: %v", err)
	}
	newer := cloneAuthorityRecord(current)
	newer.Board.Revision++
	if err := reducer.Bootstrap(newer); err != nil {
		t.Fatalf("bootstrap newer: %v", err)
	}
	if got := reducer.Snapshot().Record.Board.Revision; got != 3 {
		t.Fatalf("projection revision = %d, want 3", got)
	}
}

func TestProjectionBootstrapRejectsConflictsAndDefensivelyCopies(t *testing.T) {
	current := projectionTestRecord(t, "board", 2)
	reducer := NewProjectionReducer()
	if err := reducer.Bootstrap(current); err != nil {
		t.Fatalf("bootstrap current: %v", err)
	}
	otherBoard := cloneAuthorityRecord(current)
	otherBoard.BoardID = "other"
	if err := reducer.Bootstrap(otherBoard); err == nil {
		t.Fatal("BoardID mismatch unexpectedly accepted")
	}
	regressed := cloneAuthorityRecord(current)
	regressed.Board.Revision = 1
	if err := reducer.Bootstrap(regressed); err == nil {
		t.Fatal("revision regression unexpectedly accepted")
	}
	conflict := cloneAuthorityRecord(current)
	conflict.Compatibility.NextTaskID++
	if err := reducer.Bootstrap(conflict); err == nil {
		t.Fatal("same-revision content conflict unexpectedly accepted")
	}
	snapshot := reducer.Snapshot()
	if len(snapshot.Diagnostics) != 3 {
		t.Fatalf("diagnostics = %+v", snapshot.Diagnostics)
	}
	snapshot.Record.Board.Items[0].Title = "mutated"
	snapshot.Diagnostics[0].Message = "mutated"
	again := reducer.Snapshot()
	if again.Record.Board.Items[0].Title == "mutated" ||
		again.Diagnostics[0].Message == "mutated" {
		t.Fatalf("snapshot was not defensive: %+v", again)
	}
}

func TestProjectionReservationRejectsMismatchWithoutSwap(t *testing.T) {
	current := projectionTestRecord(t, "board", 1)
	reducer := NewProjectionReducer()
	if err := reducer.Bootstrap(current); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	next := cloneAuthorityRecord(current)
	next.Board.Revision++
	if _, err := reducer.reserve("other", 1, next); err == nil {
		t.Fatal("mismatched reservation unexpectedly accepted")
	}
	if got := reducer.Snapshot().Record.Board.Revision; got != 1 {
		t.Fatalf("failed reservation swapped revision to %d", got)
	}
	if _, err := reducer.reserve("board", 1, current); err == nil {
		t.Fatal("invalid next revision unexpectedly accepted")
	}
}

func TestProjectionDiagnosticBounds(t *testing.T) {
	reducer := NewProjectionReducer()
	for range 129 {
		reducer.diagnose(strings.Repeat("x", maxProjectionDiagnosticRunes+1))
	}
	snapshot := reducer.Snapshot()
	if len(snapshot.Diagnostics) != maxProjectionDiagnostics {
		t.Fatalf("diagnostic count = %d", len(snapshot.Diagnostics))
	}
	if got := len([]rune(snapshot.Diagnostics[0].Message)); got != maxProjectionDiagnosticRunes {
		t.Fatalf("diagnostic rune length = %d", got)
	}
}

func TestProjectionRejects1025thAuthoritativeItemWithoutReplacement(t *testing.T) {
	tasks := make([]*tools.TaskRecord, 0, MaxItems)
	for index := range MaxItems {
		id := fmt.Sprintf("%d", index+1)
		tasks = append(tasks, &tools.TaskRecord{
			ID:        id,
			Subject:   "task-" + id,
			Status:    tools.TaskStatusPending,
			CreatedAt: time.Unix(1, 0).UTC(),
			UpdatedAt: time.Unix(1, 0).UTC(),
		})
	}
	record, err := seedAuthorityRecord(
		"session",
		"board",
		tools.TaskManagerSnapshot{NextID: MaxItems + 1, Tasks: tasks},
		tools.TodoScope{SessionID: "session"},
		nil,
	)
	if err != nil {
		t.Fatalf("seed maximum record: %v", err)
	}
	reducer := NewProjectionReducer()
	if err := reducer.Bootstrap(record); err != nil {
		t.Fatalf("bootstrap maximum record: %v", err)
	}

	oversized := cloneAuthorityRecord(record)
	oversized.Board.Revision++
	extraItem := oversized.Board.Items[len(oversized.Board.Items)-1]
	extraItem.ID = "1025"
	extraItem.Source.LegacyID = "1025"
	extraItem.Order = MaxItems
	oversized.Board.Items = append(oversized.Board.Items, extraItem)
	extraTask := oversized.Compatibility.Tasks[len(oversized.Compatibility.Tasks)-1]
	extraTask.ID = "1025"
	extraTask.Subject = "task-1025"
	oversized.Compatibility.Tasks = append(
		oversized.Compatibility.Tasks,
		extraTask,
	)
	oversized.Compatibility.NextTaskID = MaxItems + 2

	if err := reducer.Bootstrap(oversized); err == nil ||
		!strings.Contains(err.Error(), "exceed limit 1024") {
		t.Fatalf("1,025-item bootstrap error = %v", err)
	}
	snapshot := reducer.Snapshot()
	if len(snapshot.Record.Board.Items) != MaxItems ||
		snapshot.Record.Board.Revision != record.Board.Revision {
		t.Fatalf("rejected oversized bootstrap changed projection: %#v", snapshot)
	}
}

func projectionTestRecord(t *testing.T, boardID string, revision uint64) AuthorityRecord {
	t.Helper()
	record, err := seedAuthorityRecord(
		"session",
		boardID,
		tools.TaskManagerSnapshot{NextID: 2, Tasks: []*tools.TaskRecord{{
			ID: "1", Subject: "task", Status: tools.TaskStatusPending,
			CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
		}}},
		tools.TodoScope{SessionID: "session"},
		nil,
	)
	if err != nil {
		t.Fatalf("seed record: %v", err)
	}
	record.Board.Revision = revision
	return record
}
