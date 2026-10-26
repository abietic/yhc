package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine"
)

func renderExecutionRecap(summary engine.ExecutionSummary) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Execution facts · %s\nSession: %s\nThread: %s\n", summary.AsOf.UTC().Format("2006-01-02T15:04:05Z"), changeReviewDisplay(summary.SessionID), changeReviewDisplay(summary.ThreadID))
	if turn := summary.LatestTurn; turn != nil {
		status := "incomplete (no durable terminal)"
		if summary.ActiveTurnID == turn.TurnID {
			status = "running"
		}
		if !turn.FinishedAt.IsZero() {
			status = "execution " + turn.TerminalReason
		}
		fmt.Fprintf(&text, "\nLatest observed turn: %s\nState: %s\nFirst observed: %s\n", changeReviewDisplay(turn.TurnID), changeReviewDisplay(status), turn.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
		if !turn.FinishedAt.IsZero() {
			fmt.Fprintf(&text, "Terminal observed: %s\nRetained tool records: %d · succeeded %d · failed %d · cancelled %d · running %d\n", turn.FinishedAt.UTC().Format("2006-01-02T15:04:05Z"), turn.KnownToolRecords, turn.KnownToolsSucceeded, turn.KnownToolsFailed, turn.KnownToolsCancelled, turn.KnownToolsRunning)
		}
		if turn.ToolCountsBounded {
			text.WriteString("Tool records are bounded; older details may be omitted.\n")
		}
		if !turn.FinishedAt.IsZero() {
			fmt.Fprintf(&text, "Retained unresolved interactions at terminal: %d\n", turn.PendingInteractions)
		}
	} else {
		text.WriteString("\nNo durable turn facts are available for this session.\n")
	}
	text.WriteString("\nCurrent retained tasks: " + recapCounts(summary.CurrentTaskStatuses, summary.TasksAvailable) + "\n")
	if summary.TasksPartial {
		text.WriteString("Task projection is partial.\n")
	}
	text.WriteString("Current accepted input: " + recapCounts(summary.AcceptedInputStatuses, summary.AcceptedInputsKnown) + "\n")
	text.WriteString("\nCaptured file history (Write/Edit only):\n")
	for _, record := range summary.History.Records {
		state := "uncaptured"
		if record.BeforeCaptured && record.AfterCaptured {
			state = "unchanged"
			if record.Changed {
				state = "changed"
			}
		}
		fmt.Fprintf(&text, "- %s · %s · %s · turn %s\n", changeReviewDisplay(record.Path), state, changeReviewDisplay(record.Outcome), changeReviewDisplay(record.TurnID))
	}
	if len(summary.History.Records) == 0 {
		text.WriteString("No file records captured.\n")
	}
	if summary.History.Warning != "" {
		text.WriteString(changeReviewDisplay(summary.History.Warning) + "\n")
	}
	text.WriteString("\nVerification receipts: none independently recorded.\nExecution termination, task status and tool exit outcomes do not establish project verification.\nUse /changes to review captured file content; uncaptured and external effects remain outside this history.")
	return text.String()
}

func recapCounts(counts map[string]int, known bool) string {
	if !known {
		return "unavailable"
	}
	if len(counts) == 0 {
		return "none recorded"
	}
	keys := make([]string, 0, len(counts))
	for status := range counts {
		keys = append(keys, status)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, status := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", changeReviewDisplay(status), counts[status]))
	}
	return strings.Join(parts, " · ")
}

func (a *App) appendExecutionCompletionRecap(event engine.QueryEvent) {
	if a.engine == nil || event.TerminalInfo == nil {
		return
	}
	summary := a.engine.ExecutionSummary()
	turn := summary.LatestTurn
	if turn == nil || turn.TurnID != event.TurnID || !turn.ModelRequested {
		return
	}
	files := make(map[string]bool)
	unknown := 0
	for _, record := range summary.History.Records {
		if record.TurnID != turn.TurnID {
			continue
		}
		if record.BeforeCaptured && record.AfterCaptured {
			if record.Changed {
				files[record.Path] = true
			}
		} else {
			unknown++
		}
	}
	a.chat.AppendSystem(fmt.Sprintf("Execution %s · %d captured file changes · %d uncaptured operations · verification not recorded · /recap", changeReviewDisplay(turn.TerminalReason), len(files), unknown))
}
