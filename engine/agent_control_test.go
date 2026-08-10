package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type pauseBoundaryModel struct {
	mu            sync.Mutex
	calls         int
	secondEntered chan struct{}
}

func (m *pauseBoundaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *pauseBoundaryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "pause-boundary-tool",
				Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"pause.go"}`},
			}},
		}}), nil
	}
	select {
	case m.secondEntered <- struct{}{}:
	default:
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done after resume"}}), nil
}

func TestSendAgentMessageConvergesOptimisticInputAcrossQueueReplayAndResume(t *testing.T) {
	runner, runtimeState, entered, release := newBlockingSubagentRunner(t)
	started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{
		Task: "inspect controls", ParentSessionID: "leader-session", ParentThreadID: "leader-thread", ToolUseID: "spawn-controls",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not enter blocking tool")
	}
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: runtimeState, AgentRunner: runner})
	t.Cleanup(eng.Close)

	queued, err := eng.SendAgentMessage(started.ID, "please include queue behavior")
	if err != nil {
		t.Fatalf("send running Agent message: %v", err)
	}
	if queued.AgentID != started.ID || queued.Disposition != "queued" || queued.MessageID == "" {
		t.Fatalf("queued result = %#v", queued)
	}
	running, _ := eng.AgentDetailSnapshot(started.ID)
	message, count := findAgentDetailMessage(running.Messages, queued.MessageID)
	if count != 1 || message.Content != "please include queue behavior" || message.Completed || running.PendingMessageCount != 1 {
		t.Fatalf("optimistic queued message did not appear exactly once: message=%#v count=%d detail=%#v", message, count, running)
	}

	close(release)
	waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
	terminal := waitForAgentDetailGeneration(t, eng, started.ID, 1, "completed", 2*time.Second)
	message, count = findAgentDetailMessage(terminal.Messages, queued.MessageID)
	if count != 1 || message.Content != "please include queue behavior" || !message.Completed || terminal.PendingMessageCount != 0 {
		t.Fatalf("replayed message did not converge in place: message=%#v count=%d detail=%#v", message, count, terminal)
	}

	resumed, err := eng.SendAgentMessage(started.ID, "resume retained Agent")
	if err != nil || resumed.Disposition != "resumed" {
		t.Fatalf("resume retained Agent: result=%#v err=%v", resumed, err)
	}
	retainedTerminal := waitForAgentDetailGeneration(t, eng, started.ID, 2, "completed", 2*time.Second)
	if _, count = findAgentDetailMessage(retainedTerminal.Messages, resumed.MessageID); count != 1 {
		t.Fatalf("retained resume input count = %d, want 1: %#v", count, retainedTerminal.Messages)
	}
	waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)

	runner.Cleanup(-time.Second)
	evicted, err := eng.SendAgentMessage(started.ID, "resume evicted Agent")
	if err != nil || evicted.Disposition != "resumed" {
		t.Fatalf("resume evicted Agent: result=%#v err=%v", evicted, err)
	}
	evictedTerminal := waitForAgentDetailGeneration(t, eng, started.ID, 3, "completed", 2*time.Second)
	if _, count = findAgentDetailMessage(evictedTerminal.Messages, evicted.MessageID); count != 1 {
		t.Fatalf("evicted resume input count = %d, want 1: %#v", count, evictedTerminal.Messages)
	}
	waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
}

func TestAbortAgentPublishesCanonicalTerminalState(t *testing.T) {
	runner, runtimeState, entered, _ := newBlockingSubagentRunner(t)
	started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{Task: "abort controls"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not enter blocking tool")
	}
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: runtimeState, AgentRunner: runner})
	t.Cleanup(eng.Close)
	if err := eng.AbortAgent(started.ID); err != nil {
		t.Fatalf("abort Agent: %v", err)
	}

	detail := waitForAgentDetailGeneration(t, eng, started.ID, 1, "aborted", 2*time.Second)
	if detail.Thread.LastTerminal == nil {
		t.Fatalf("abort did not publish canonical terminal event: %#v", detail.Thread)
	}
	switch detail.Thread.LastTerminal.Reason {
	case TerminalAbortedStreaming, TerminalAbortedTools:
	default:
		t.Fatalf("abort terminal reason = %q", detail.Thread.LastTerminal.Reason)
	}
	deadline := time.Now().Add(2 * time.Second)
	var settled tools.RunningAgent
	for time.Now().Before(deadline) {
		settled, _ = runner.GetAgentSnapshot(started.ID)
		if settled.Status == "aborted" && settled.Error != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if settled.Error == nil {
		t.Fatalf("aborted runner did not settle execution error: %#v", settled)
	}
}

func TestPauseAgentWaitsAtSafeToolRoundBoundary(t *testing.T) {
	toolEntered := make(chan struct{}, 1)
	releaseTool := make(chan struct{})
	secondModelEntered := make(chan struct{}, 1)
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Read", Desc: "blocking pause-boundary tool"},
		ExecuteCtx: func(ctx context.Context, _ string) (string, error) {
			select {
			case toolEntered <- struct{}{}:
			default:
			}
			select {
			case <-releaseTool:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	runtimeState := NewRuntimeStateStore()
	executor := NewSubAgentExecutor(&pauseBoundaryModel{secondEntered: secondModelEntered}, registry, t.TempDir())
	executor.RuntimeState = runtimeState
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(executor)
	started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{Task: "pause at a safe boundary"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not enter the first tool round")
	}

	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: runtimeState, AgentRunner: runner})
	t.Cleanup(eng.Close)
	if err := eng.PauseAgent(started.ID); err != nil {
		t.Fatalf("request Agent pause: %v", err)
	}
	requested, ok := eng.AgentDetailSnapshot(started.ID)
	if !ok || requested.SteeringState != "paused" || requested.Agent.Status != "running" {
		t.Fatalf("pause request should be immediate but not interrupt the tool: %#v", requested)
	}
	close(releaseTool)
	paused := waitForAgentDetailGeneration(t, eng, started.ID, 1, "paused", 2*time.Second)
	if paused.SteeringState != "paused" || !paused.Agent.CompletedAt.IsZero() || paused.Thread.ActiveTurnID == "" {
		t.Fatalf("safe-boundary pause looked terminal or lost its turn: %#v", paused)
	}
	select {
	case <-secondModelEntered:
		t.Fatal("second model request started while Agent was paused")
	case <-time.After(50 * time.Millisecond):
	}

	if err := eng.ResumeAgent(started.ID); err != nil {
		t.Fatalf("resume paused Agent: %v", err)
	}
	select {
	case <-secondModelEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second model request did not start after resume")
	}
	waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
	completed := waitForAgentDetailGeneration(t, eng, started.ID, 1, "completed", 2*time.Second)

	pausedSequence := uint64(0)
	resumedSequence := uint64(0)
	for _, event := range completed.Thread.Events {
		if event.Type != EventAgentLifecycle {
			continue
		}
		switch event.Summary {
		case "paused":
			pausedSequence = event.Sequence
		case "resumed_control":
			resumedSequence = event.Sequence
		}
	}
	if pausedSequence == 0 || resumedSequence <= pausedSequence {
		t.Fatalf("pause/resume lifecycle order = paused:%d resumed:%d events:%#v", pausedSequence, resumedSequence, completed.Thread.Events)
	}
}

func TestAbortAgentReleasesPausedBoundary(t *testing.T) {
	runner, runtimeState, toolEntered, releaseTool := newBlockingSubagentRunner(t)
	started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{Task: "abort while paused"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-toolEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not enter the first tool round")
	}
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: runtimeState, AgentRunner: runner})
	t.Cleanup(eng.Close)
	if err := eng.PauseAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	close(releaseTool)
	waitForAgentDetailGeneration(t, eng, started.ID, 1, "paused", 2*time.Second)
	if err := eng.AbortAgent(started.ID); err != nil {
		t.Fatalf("abort paused Agent: %v", err)
	}
	aborted := waitForAgentDetailGeneration(t, eng, started.ID, 1, "aborted", 2*time.Second)
	if aborted.Thread.LastTerminal == nil {
		t.Fatalf("paused abort did not publish terminal state: %#v", aborted)
	}
	// Runtime terminal publication precedes the runner's durable-state flush.
	// AbortAgent sets status eagerly, so wait for finishAgentExecution to attach
	// the cancellation error before TempDir cleanup removes outputDir.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		settled, _ := runner.GetAgentSnapshot(started.ID)
		if settled.Status == "aborted" && settled.Error != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("paused abort did not finish durable runner cleanup")
}

func waitForAgentDetailGeneration(t *testing.T, eng *QueryEngine, agentID string, generation int64, status string, timeout time.Duration) AgentDetailSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		detail, ok := eng.AgentDetailSnapshot(agentID)
		if ok && detail.Agent.Generation == generation && detail.Agent.Status == status {
			return detail
		}
		time.Sleep(5 * time.Millisecond)
	}
	detail, _ := eng.AgentDetailSnapshot(agentID)
	t.Fatalf("Agent detail generation/status = %d/%q, want %d/%q: %#v", detail.Agent.Generation, detail.Agent.Status, generation, status, detail)
	return AgentDetailSnapshot{}
}

func findAgentDetailMessage(messages []AgentDetailMessage, messageID string) (AgentDetailMessage, int) {
	var found AgentDetailMessage
	count := 0
	for _, message := range messages {
		if message.ID == messageID {
			found = message
			count++
		}
	}
	return found, count
}
