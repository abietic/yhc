package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"
)

func TestExecutionRecapDoesNotClaimVerificationFromTerminalTasksOrBash(t *testing.T) {
	turn := filehistory.TurnRecord{TurnID: "turn", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), TerminalReason: "completed", ModelRequested: true, KnownToolRecords: 1, KnownToolsSucceeded: 1}
	summary := engine.ExecutionSummary{AsOf: time.Now().UTC(), SessionID: "s", ThreadID: "t", LatestTurn: &turn, TasksAvailable: true, CurrentTaskStatuses: map[string]int{"completed": 1, "cancelled": 2}, AcceptedInputsKnown: true, History: engine.WorkspaceChangeSnapshot{Records: []filehistory.RevisionHeader{{ToolName: "Bash", Outcome: "succeeded", TurnID: "turn"}}}}
	got := renderExecutionRecap(summary)
	for _, want := range []string{"Retained tool records", "Verification receipts: none independently recorded", "do not establish project verification", "completed=1", "cancelled=2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("recap missing %q: %s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "verification passed") {
		t.Fatalf("recap inferred verification: %s", got)
	}
}

func TestExecutionRecapCancelledAndNoModelTurnRemainFacts(t *testing.T) {
	turn := filehistory.TurnRecord{TurnID: "cancelled", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), TerminalReason: "cancelled", ModelRequested: false}
	got := renderExecutionRecap(engine.ExecutionSummary{AsOf: time.Now().UTC(), LatestTurn: &turn})
	if !strings.Contains(got, "execution cancelled") || !strings.Contains(got, "Verification receipts: none independently recorded") {
		t.Fatalf("cancelled recap wording: %s", got)
	}
}
