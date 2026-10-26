package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestWorkspaceHistoryReadIsSideEffectFree(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{SessionID: "history-read", CWD: root, TranscriptDir: dir})
	t.Cleanup(eng.Close)
	snapshot := eng.WorkspaceChanges()
	if snapshot.Warning != "" || len(snapshot.Records) != 0 {
		t.Fatalf("empty snapshot = %+v", snapshot)
	}
	if _, err := os.Stat(filepath.Join(dir, "history-read.workspace-history")); !os.IsNotExist(err) {
		t.Fatal("read-only history created storage")
	}
}

func TestWorkspaceHistoryCapturesActualProductionWritesAndReplays(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "created.txt")
	args, err := json.Marshal(map[string]string{"file_path": path, "content": "captured content\n"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	m := &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		if calls.Add(1) == 1 {
			return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "write-history", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: string(args)}}}}, nil
		}
		return &schema.Message{Role: schema.Assistant, Content: "Written"}, nil
	}}
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool())
	eng := NewQueryEngine(QueryEngineConfig{ToolRegistry: registry, SessionID: "history-production", CWD: root, TranscriptDir: dir, ChatModel: m, Model: "fixture", PermissionMode: permission.ModeAcceptEdits})
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(t.Context(), "Create the file")
	var turnID string
	for event := range events {
		if event.Type == EventTerminal {
			turnID = event.TurnID
			if event.TerminalInfo.Reason != TerminalCompleted {
				t.Fatalf("turn failed: %+v", event.TerminalInfo)
			}
		}
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "captured content\n" {
		t.Fatalf("actual write: %q %v", content, err)
	}
	snapshot := eng.WorkspaceChanges()
	if snapshot.Warning != "" || len(snapshot.Records) != 1 || snapshot.Records[0].TurnID != turnID || !snapshot.Records[0].Changed {
		t.Fatalf("production history: %+v", snapshot)
	}
	record, ok := eng.WorkspaceChange(snapshot.Records[0].ID)
	if !ok || !record.Before.Captured || record.Before.Exists || !record.After.Captured || string(record.After.Data) != "captured content\n" || record.Outcome != "succeeded" {
		t.Fatalf("file facts: %+v", record)
	}
	summary := eng.ExecutionSummary()
	if summary.LatestTurn == nil || summary.LatestTurn.TurnID != turnID || !summary.LatestTurn.ModelRequested || summary.LatestTurn.TerminalReason != string(TerminalCompleted) || summary.LatestTurn.KnownToolRecords != 1 || summary.Verification != "not_recorded" {
		t.Fatalf("production execution facts: %+v", summary)
	}
	eng.Close()
	restarted := NewQueryEngine(QueryEngineConfig{SessionID: "history-host", CWD: root, TranscriptDir: dir})
	t.Cleanup(restarted.Close)
	if _, err := restarted.ResumeSession(t.Context(), "history-production"); err != nil {
		t.Fatal(err)
	}
	replay := restarted.WorkspaceChanges()
	if replay.Warning != "" || len(replay.Records) != 1 || replay.Records[0].ID != record.ID || calls.Load() != 2 {
		t.Fatalf("replay mutated or lost facts: %+v calls=%d", replay, calls.Load())
	}
}

func TestWorkspaceHistoryDeniedToolsDoNotCreateSnapshots(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	var calls, denied atomic.Int32
	args, err := json.Marshal(map[string]string{"file_path": filepath.Join(root, "denied.txt"), "content": "no"})
	if err != nil {
		t.Fatal(err)
	}
	m := &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		if calls.Add(1) == 1 {
			return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "denied-history", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: string(args)}}}}, nil
		}
		return &schema.Message{Role: schema.Assistant, Content: "Permission denied"}, nil
	}}
	registry := tools.NewRegistry()
	registry.Register(tools.WriteTool())
	eng := NewQueryEngine(QueryEngineConfig{ToolRegistry: registry, SessionID: "history-denied", CWD: root, TranscriptDir: dir, ChatModel: m, Model: "fixture", CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
		denied.Add(1)
		return false, "denied by fixture"
	}})
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(t.Context(), "Write denied file")
	for range events {
	}
	if denied.Load() != 1 {
		t.Fatalf("permission callback calls=%d; fixture did not exercise denial", denied.Load())
	}
	if snapshot := eng.WorkspaceChanges(); len(snapshot.Records) != 0 {
		t.Fatalf("denied tool acquired a snapshot: %+v", snapshot)
	}
	if _, err := os.Stat(filepath.Join(root, "denied.txt")); !os.IsNotExist(err) {
		t.Fatal("denied tool wrote a file")
	}
}

func TestWorkspaceHistoryStaleCallbacksCannotReopenOwner(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{SessionID: "history-stale", CWD: t.TempDir(), TranscriptDir: t.TempDir()})
	t.Cleanup(eng.Close)
	old := eng.workspaceRecorderForTurn("old-turn")
	eng.mu.Lock()
	eng.resetWorkspaceRevisions()
	eng.mu.Unlock()
	if finish := old("Write", "old-tool", map[string]any{"file_path": "file.txt"}); finish != nil {
		t.Fatal("stale observer reopened an invalidated owner")
	}
	if snapshot := eng.WorkspaceChanges(); len(snapshot.Records) != 0 || snapshot.Warning != "" {
		t.Fatalf("stale observer changed current owner: %+v", snapshot)
	}
}

func TestWorkspaceHistoryRecordsCommittedGoalTerminal(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{Clock: func() time.Time { return now }})
	budget := uint64(1000)
	if _, err := eng.goalService.create(goalCreateRequest{Objective: "fixture goal", TokenBudget: &budget}); err != nil {
		t.Fatal(err)
	}
	events := make(chan QueryEvent, 32)
	emitter := beginP242aGoalTurn(t, eng, events, "history-goal-turn", false, now)
	now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{Type: EventTerminal, TerminalInfo: &Terminal{Reason: TerminalCompleted}}) {
		t.Fatal("goal terminal not delivered")
	}
	emitter.Close()
	eng.endPlanTurn("history-goal-turn")
	history := eng.WorkspaceChanges()
	if history.Warning != "" || len(history.Turns) != 1 || history.Turns[0].FinishedAt.IsZero() || history.Turns[0].TerminalReason != string(TerminalCompleted) {
		t.Fatalf("accepted goal terminal missing: %+v", history)
	}
	identity := eng.workspaceRevisionIdentity()
	eng.Close()
	replayed, err := filehistory.OpenRevisionJournal(identity.cwd, filepath.Join(identity.dir, identity.sessionID+".workspace-history"), identity.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	if turns := replayed.Snapshot().Turns; len(turns) != 1 || turns[0].FinishedAt.IsZero() {
		t.Fatal("Goal terminal was not durable on replay")
	}
}
