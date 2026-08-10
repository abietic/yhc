package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

type durableCompletionExecutor struct {
	result string
}

func (e durableCompletionExecutor) ExecuteAgent(
	context.Context,
	tools.AgentExecOptions,
) (*tools.AgentExecResult, error) {
	return &tools.AgentExecResult{Result: e.result}, nil
}

func TestAgentCompletionRedeliversAfterRestartThenReceiptSuppressesReplay(
	t *testing.T,
) {
	const (
		parentSessionID = "completion-parent"
		parentThreadID  = "completion-parent-thread"
	)
	childOutputDir := t.TempDir()
	parentTranscriptDir := t.TempDir()
	cwd := t.TempDir()

	producer := tools.NewAgentRunner(1)
	producer.SetOutputDir(childOutputDir)
	producer.SetExecutor(durableCompletionExecutor{result: "terminal result"})
	started, err := tools.RunAgentBackground(
		context.Background(),
		producer,
		tools.AgentExecOptions{
			Task:            "finish durably",
			Description:     "Finish durably",
			ParentSessionID: parentSessionID,
			ParentThreadID:  parentThreadID,
			ToolUseID:       "tool-parent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForAgentStatus(
		t,
		producer,
		started.ID,
		"completed",
		2*time.Second,
	)
	if terminal.Completion == nil {
		t.Fatal("terminal child has no durable completion record")
	}

	restartedRunner := tools.NewAgentRunner(1)
	restartedRunner.SetOutputDir(childOutputDir)
	if _, err := restartedRunner.RegisterPersistedAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	parent := NewQueryEngine(QueryEngineConfig{
		SessionID:     parentSessionID,
		ThreadID:      parentThreadID,
		CWD:           cwd,
		TranscriptDir: parentTranscriptDir,
		AgentRunner:   restartedRunner,
	})
	t.Cleanup(parent.Close)

	var syncWG sync.WaitGroup
	syncErrors := make(chan error, 8)
	for index := 0; index < 8; index++ {
		syncWG.Add(1)
		go func() {
			defer syncWG.Done()
			syncErrors <- parent.SyncRuntimeItems(context.Background())
		}()
	}
	syncWG.Wait()
	close(syncErrors)
	for syncErr := range syncErrors {
		if syncErr != nil {
			t.Fatal(syncErr)
		}
	}
	items := parent.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != terminal.Completion.CompletionID ||
		items[0].AgentNotification == nil ||
		items[0].AgentNotification.TerminalSequence !=
			terminal.TerminalSequence {
		t.Fatalf("reconstructed completion item = %#v", items)
	}

	claimed, ok, err := parent.ClaimNextRuntimeItem()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("reconstructed completion was not claimable")
	}
	message := runtimeItemToAttachmentMessage(claimed)
	if err := parent.recordTranscriptMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	loaded, err := parent.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.AgentCompletionReceipts) != 1 {
		t.Fatalf(
			"parent completion receipts = %#v",
			loaded.AgentCompletionReceipts,
		)
	}
	receipt := loaded.AgentCompletionReceipts[0]
	if receipt.Version != transcript.AgentCompletionReceiptVersion ||
		receipt.CompletionID != terminal.Completion.CompletionID ||
		receipt.AgentID != started.ID ||
		receipt.Generation != terminal.ExecutionGeneration() ||
		receipt.TerminalSequence != terminal.TerminalSequence ||
		receipt.ParentSessionID != parentSessionID ||
		receipt.ParentThreadID != parentThreadID ||
		receipt.ParentToolUseID != "tool-parent" ||
		receipt.DeliveredAt.IsZero() {
		t.Fatalf("parent completion receipt = %#v", receipt)
	}

	secondRunner := tools.NewAgentRunner(1)
	secondRunner.SetOutputDir(childOutputDir)
	if _, err := secondRunner.RegisterPersistedAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	secondParent := NewQueryEngine(QueryEngineConfig{
		SessionID:     parentSessionID,
		ThreadID:      parentThreadID,
		CWD:           cwd,
		TranscriptDir: parentTranscriptDir,
		AgentRunner:   secondRunner,
	})
	t.Cleanup(secondParent.Close)
	if err := secondParent.SyncRuntimeItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	if replayed := secondParent.RuntimeItems(); len(replayed) != 0 {
		t.Fatalf("receipt did not suppress completion replay: %#v", replayed)
	}
}

func TestUnknownCompletionReceiptVersionFailsClosedForReinjection(
	t *testing.T,
) {
	const (
		parentSessionID = "unknown-receipt-parent"
		parentThreadID  = "unknown-receipt-thread"
	)
	childOutputDir := t.TempDir()
	parentTranscriptDir := t.TempDir()

	producer := tools.NewAgentRunner(1)
	producer.SetOutputDir(childOutputDir)
	producer.SetExecutor(durableCompletionExecutor{result: "diagnostic result"})
	started, err := tools.RunAgentBackground(
		context.Background(),
		producer,
		tools.AgentExecOptions{
			Task:            "unknown receipt",
			ParentSessionID: parentSessionID,
			ParentThreadID:  parentThreadID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForAgentStatus(
		t,
		producer,
		started.ID,
		"completed",
		2*time.Second,
	)
	if terminal.Completion == nil {
		t.Fatal("terminal child has no completion identity")
	}

	recorder := transcript.NewRecorder(parentSessionID, parentTranscriptDir)
	if err := recorder.RecordMessages([]*schema.Message{{
		Role:    schema.User,
		Content: "future receipt diagnostic",
		Extra: map[string]any{
			transcript.AgentCompletionReceiptExtraKey(): transcript.AgentCompletionReceipt{
				Version:          99,
				CompletionID:     terminal.Completion.CompletionID,
				AgentID:          started.ID,
				Generation:       terminal.ExecutionGeneration(),
				TerminalStatus:   "completed",
				TerminalSequence: terminal.TerminalSequence,
				ParentSessionID:  parentSessionID,
				ParentThreadID:   parentThreadID,
				DeliveredAt:      time.Now().UTC(),
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	restartedRunner := tools.NewAgentRunner(1)
	restartedRunner.SetOutputDir(childOutputDir)
	diagnostic, err := restartedRunner.RegisterPersistedAgent(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.Status != "completed" ||
		diagnostic.Completion == nil ||
		diagnostic.Completion.CompletionID !=
			terminal.Completion.CompletionID {
		t.Fatalf("child terminal diagnostic = %#v", diagnostic)
	}
	parent := NewQueryEngine(QueryEngineConfig{
		SessionID:     parentSessionID,
		ThreadID:      parentThreadID,
		CWD:           t.TempDir(),
		TranscriptDir: parentTranscriptDir,
		AgentRunner:   restartedRunner,
	})
	t.Cleanup(parent.Close)
	if err := parent.SyncRuntimeItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	if replayed := parent.RuntimeItems(); len(replayed) != 0 {
		t.Fatalf("unknown receipt version was reinjected: %#v", replayed)
	}
}

func TestAgentCompletionReceiptOutsideBoundedProjectionSettlesRecoveredLedger(
	t *testing.T,
) {
	const (
		parentSessionID = "completion-audit-parent"
		parentThreadID  = "completion-audit-thread"
	)
	childOutputDir := t.TempDir()
	parentTranscriptDir := t.TempDir()
	cwd := t.TempDir()

	producer := tools.NewAgentRunner(1)
	producer.SetOutputDir(childOutputDir)
	producer.SetExecutor(durableCompletionExecutor{result: "audit result"})
	started, err := tools.RunAgentBackground(
		context.Background(),
		producer,
		tools.AgentExecOptions{
			Task:            "survive receipt projection eviction",
			ParentSessionID: parentSessionID,
			ParentThreadID:  parentThreadID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForAgentStatus(
		t,
		producer,
		started.ID,
		"completed",
		2*time.Second,
	)
	if terminal.Completion == nil {
		t.Fatal("terminal child has no durable completion record")
	}

	firstRunner := tools.NewAgentRunner(1)
	firstRunner.SetOutputDir(childOutputDir)
	if _, err := firstRunner.RegisterPersistedAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	firstParent := NewQueryEngine(QueryEngineConfig{
		SessionID:     parentSessionID,
		ThreadID:      parentThreadID,
		CWD:           cwd,
		TranscriptDir: parentTranscriptDir,
		AgentRunner:   firstRunner,
	})
	if err := firstParent.SyncRuntimeItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := firstParent.ClaimNextRuntimeItem()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != terminal.Completion.CompletionID {
		t.Fatalf("claimed completion = %#v, ok=%v", claimed, ok)
	}

	// Simulate a crash after the parent transcript commit and before coordinator
	// settlement. The durable ledger still contains a processing item.
	if err := firstParent.GetTranscript().RecordMessages(
		[]*schema.Message{runtimeItemToAttachmentMessage(claimed)},
	); err != nil {
		t.Fatal(err)
	}
	newerReceipts := make([]*schema.Message, 0, 300)
	for index := 0; index < 300; index++ {
		newerReceipts = append(newerReceipts, &schema.Message{
			Role:    schema.User,
			Content: "newer completion receipt",
			Extra: map[string]any{
				transcript.AgentCompletionReceiptExtraKey(): transcript.AgentCompletionReceipt{
					Version:          transcript.AgentCompletionReceiptVersion,
					CompletionID:     fmt.Sprintf("newer-completion-%03d", index),
					AgentID:          fmt.Sprintf("newer-agent-%03d", index),
					Generation:       1,
					TerminalStatus:   "completed",
					TerminalSequence: 1,
					ParentSessionID:  parentSessionID,
					ParentThreadID:   parentThreadID,
					DeliveredAt:      time.Now().UTC(),
				},
			},
		})
	}
	if err := firstParent.GetTranscript().RecordMessages(newerReceipts); err != nil {
		t.Fatal(err)
	}
	if err := firstParent.GetTranscript().RecordLifecycleBoundary(
		transcript.LifecycleCompact,
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	firstParent.Close()

	loaded, err := transcript.NewRecorder(
		parentSessionID,
		parentTranscriptDir,
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range loaded.AgentCompletionReceipts {
		if receipt.CompletionID == terminal.Completion.CompletionID {
			t.Fatal("test setup did not evict the original bounded receipt projection")
		}
	}
	if len(loaded.Messages) != 0 {
		t.Fatalf("compact active messages = %#v, want empty", loaded.Messages)
	}

	secondRunner := tools.NewAgentRunner(1)
	secondRunner.SetOutputDir(childOutputDir)
	if _, err := secondRunner.RegisterPersistedAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	secondParent := NewQueryEngine(QueryEngineConfig{
		SessionID:     parentSessionID,
		ThreadID:      parentThreadID,
		CWD:           cwd,
		TranscriptDir: parentTranscriptDir,
		AgentRunner:   secondRunner,
	})
	t.Cleanup(secondParent.Close)
	if recovered := secondParent.RuntimeItems(); len(recovered) != 0 {
		t.Fatalf("receipt audit did not settle recovered ledger: %#v", recovered)
	}
	if err := secondParent.SyncRuntimeItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	if replayed := secondParent.RuntimeItems(); len(replayed) != 0 {
		t.Fatalf("evicted receipt was reinjected after restart: %#v", replayed)
	}
}

func TestRuntimeStateCollapsesDuplicateAgentCompletionProjection(t *testing.T) {
	store := NewRuntimeStateStore()
	completionID := "agent-completion:v1:duplicate"
	event := func(sequence uint64) QueryEvent {
		return QueryEvent{
			RuntimeEventEnvelope: RuntimeEventEnvelope{
				SessionID: "parent-session",
				ThreadID:  "parent-thread",
				TurnID:    "parent-turn",
				Sequence:  sequence,
				Timestamp: time.Unix(int64(sequence), 0).UTC(),
			},
			Type: EventAttachment,
			AttachmentMessage: &schema.Message{
				Role:    schema.User,
				Content: "terminal projection",
				Extra: map[string]any{
					"command_mode":                       "task-notification",
					"task_notification_agent_id":         "child-agent",
					"task_notification_status":           "completed",
					"agent_completion_id":                completionID,
					"agent_completion_terminal_sequence": uint64(3),
				},
			},
		}
	}
	if err := store.Apply(event(1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(event(2)); err != nil {
		t.Fatal(err)
	}

	snapshot := store.Snapshot("parent-thread")
	thread := snapshot.Threads["parent-thread"]
	if len(thread.Messages) != 1 || len(thread.Events) != 1 ||
		thread.LastSequence != 2 {
		t.Fatalf("duplicate completion projection = %#v", thread)
	}
	agent := snapshot.Agents["child-agent"]
	if agent.DeliveredCompletionID != completionID ||
		agent.DeliveredTerminalSequence != 3 {
		t.Fatalf("child completion projection = %#v", agent)
	}
}
