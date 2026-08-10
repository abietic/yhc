package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

type p139dProjectionExecutor struct {
	calls       atomic.Int64
	entered     chan struct{}
	enteredOnce sync.Once
	release     <-chan struct{}
	result      string
	err         error
}

func (e *p139dProjectionExecutor) RecordAgentExecutionAdmission(
	_ context.Context,
	opts tools.AgentExecOptions,
	messages []*schema.Message,
) error {
	recorder := transcript.NewRecorder(opts.SessionID, opts.TranscriptDir)
	defer recorder.Close() //nolint:errcheck
	if err := recorder.Replace(messages); err != nil {
		return err
	}
	stage := "foreground_child"
	if opts.IsBackgroundExecution() {
		stage = "background_child"
	}
	now := time.Now().UTC()
	return session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: opts.SessionID, ThreadID: opts.ThreadID, AgentID: opts.AgentID,
		AgentGeneration: 1, AgentName: opts.Name, AgentRole: opts.SubagentType,
		ParentSessionID: opts.ParentSessionID, ParentThreadID: opts.ParentThreadID,
		ParentAgentID: opts.ParentAgentID, ParentToolUseID: opts.ToolUseID,
		QueryKernelVersion: "project_graph/v1", QueryKernelStage: stage,
		Status: "running", PermissionMode: opts.Mode, CWD: opts.CWD,
		CreatedAt: now, UpdatedAt: now, MessageCount: len(messages),
	})
}

func (e *p139dProjectionExecutor) ExecuteAgent(
	ctx context.Context,
	opts tools.AgentExecOptions,
) (*tools.AgentExecResult, error) {
	e.calls.Add(1)
	if e.entered != nil {
		e.enteredOnce.Do(func() { close(e.entered) })
	}
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	result := e.result
	if result == "" {
		result = "ProjectGraph child completed"
	}
	return &tools.AgentExecResult{
		Result: result,
		Messages: []*schema.Message{
			{Role: schema.User, Content: opts.Task},
			{Role: schema.Assistant, Content: "inspected through the project graph"},
		},
	}, nil
}

func TestP139dProjectGraphRestartProjectsReplayAndEvictedViewsWithoutDispatch(t *testing.T) {
	cwd := t.TempDir()
	parentDir := filepath.Join(cwd, "parent-transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	producer := tools.NewAgentRunner(2)
	producer.SetOutputDir(outputDir)
	producerExecutor := &p139dProjectionExecutor{result: "bounded graph output"}
	producer.SetExecutor(producerExecutor)

	const (
		parentSession = "parent-session"
		parentThread  = "parent-thread"
		agentID       = "graph-foreground"
		childSession  = "graph-child-session"
		childThread   = "graph-child-thread"
	)
	if _, err := tools.RunAgent(context.Background(), producer, tools.AgentExecOptions{
		AgentID: agentID, SessionID: childSession, ThreadID: childThread,
		ParentSessionID: parentSession, ParentThreadID: parentThread,
		ToolUseID: "spawn-graph-child", Name: "graph scout",
		Task: "inspect durable graph projection", Description: "Inspect graph projection",
		SubagentType: "Explore", CWD: cwd,
	}); err != nil {
		t.Fatal(err)
	}
	parentPath := writeP139dParentSession(
		t, parentDir, parentSession, parentThread, cwd, []string{agentID},
	)

	replayExecutor := &p139dProjectionExecutor{err: errors.New("TUI replay must not dispatch")}
	replayRunner := tools.NewAgentRunner(2)
	replayRunner.SetOutputDir(outputDir)
	replayRunner.SetExecutor(replayExecutor)
	runtimeStore := engine.NewRuntimeStateStore(engine.RuntimeStoreLimits{Threads: 1})
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "fresh", ThreadID: "fresh-thread", CWD: cwd,
		TranscriptDir: parentDir, AgentRunner: replayRunner, RuntimeState: runtimeStore,
	})
	t.Cleanup(query.Close)
	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: parentSession, CWD: cwd, TranscriptDir: parentDir, TranscriptPath: parentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 ||
		resumed.RestoredAgents[0].Mode != string(engine.ThreadModeReplayOnly) {
		t.Fatalf("restored ProjectGraph Agents = %#v", resumed.RestoredAgents)
	}

	app := New(Config{Engine: query, Resumed: true, ReducedMotion: true})
	app.textarea.SetValue("leader draft survives graph inspection")
	before := query.RuntimeSnapshot()
	if err := app.activateThreadByID(childThread); err != nil {
		t.Fatal(err)
	}
	if app.activeThreadViewMode() != engine.ThreadModeReplayOnly {
		t.Fatalf("restart attach mode = %q", app.activeThreadViewMode())
	}
	detail, ok := query.AgentDetailSnapshot(agentID)
	if !ok || detail.Storage != "evicted" || detail.Agent.Generation != 1 ||
		detail.Agent.ParentSessionID != parentSession ||
		detail.Agent.ParentThreadID != parentThread ||
		detail.Agent.ParentToolUseID != "spawn-graph-child" ||
		detail.Output != "bounded graph output" {
		t.Fatalf("ProjectGraph replay detail = ok:%v %#v", ok, detail)
	}
	transcriptText := stripANSIForTest(app.chat.RenderAllExpanded(100))
	for _, want := range []string{"inspect durable graph projection", "inspected through the project graph"} {
		if !strings.Contains(transcriptText, want) {
			t.Fatalf("ProjectGraph transcript missing %q:\n%s", want, transcriptText)
		}
	}
	outputView := strings.Join(buildAgentDetailLines(detail, agentDetailOutput, 100, time.Now()), "\n")
	lineageView := strings.Join(buildAgentDetailLines(detail, agentDetailLineage, 100, time.Now()), "\n")
	for _, want := range []string{"bounded graph output", "Parent thread: " + parentThread, "Spawn tool: spawn-graph-child"} {
		if !strings.Contains(outputView+"\n"+lineageView, want) {
			t.Fatalf("ProjectGraph detail views missing %q:\n%s\n%s", want, outputView, lineageView)
		}
	}
	afterReplayNavigation := query.RuntimeSnapshot()
	assertP139dRuntimeUnchanged(t, before, afterReplayNavigation, agentID)

	if err := app.activateThreadByID(parentThread); err != nil {
		t.Fatal(err)
	}
	if app.textarea.Value() != "leader draft survives graph inspection" {
		t.Fatalf("leader presentation was not restored: %q", app.textarea.Value())
	}

	if err := runtimeStore.RestoreAgentSnapshot(engine.RuntimeAgentSnapshot{
		AgentID: "other-terminal", SessionID: "other-session", ThreadID: "other-thread",
		Status: "completed", Generation: 1, StartedAt: time.Now(), CompletedAt: time.Now(),
	}, []*schema.Message{{Role: schema.Assistant, Content: "other"}}, false); err != nil {
		t.Fatal(err)
	}
	entry, ok := p139dThreadEntry(query.ThreadCatalogSnapshot(), childThread)
	if !ok || entry.Mode != engine.ThreadModeEvictedTranscript {
		t.Fatalf("evicted ProjectGraph catalog entry = ok:%v %#v", ok, entry)
	}
	beforeEvictedNavigation := query.RuntimeSnapshot()
	if err := app.activateThreadByID(childThread); err != nil {
		t.Fatal(err)
	}
	if app.activeThreadViewMode() != engine.ThreadModeEvictedTranscript {
		t.Fatalf("evicted attach mode = %q", app.activeThreadViewMode())
	}
	evictedDetail, ok := query.AgentDetailSnapshot(agentID)
	if !ok || evictedDetail.Output != detail.Output || len(evictedDetail.Messages) != len(detail.Messages) {
		t.Fatalf("evicted detail drifted: ok:%v %#v", ok, evictedDetail)
	}
	assertP139dRuntimeUnchanged(t, beforeEvictedNavigation, query.RuntimeSnapshot(), agentID)
	if replayExecutor.calls.Load() != 0 {
		t.Fatalf("TUI replay dispatched %d Agent executions", replayExecutor.calls.Load())
	}
}

func TestP139dProjectGraphBackgroundLiveAttachIsProjectionOnly(t *testing.T) {
	cwd := t.TempDir()
	parentDir := filepath.Join(cwd, "parent-transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	executor := &p139dProjectionExecutor{entered: entered, release: release, result: "live done"}
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)

	const (
		parentSession = "background-parent"
		parentThread  = "background-parent-thread"
		agentID       = "graph-background"
		childThread   = "graph-background-thread"
	)
	started, err := tools.RunAgentBackground(t.Context(), runner, tools.AgentExecOptions{
		AgentID: agentID, SessionID: "graph-background-session", ThreadID: childThread,
		ParentSessionID: parentSession, ParentThreadID: parentThread,
		ToolUseID: "spawn-background", Name: "background graph",
		Task: "wait for graph projection", SubagentType: "Explore", CWD: cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = runner.Shutdown(shutdownCtx)
	})
	parentPath := writeP139dParentSession(
		t, parentDir, parentSession, parentThread, cwd, []string{started.ID},
	)
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "fresh", CWD: cwd, TranscriptDir: parentDir, AgentRunner: runner,
	})
	t.Cleanup(query.Close)
	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: parentSession, CWD: cwd, TranscriptDir: parentDir, TranscriptPath: parentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 ||
		resumed.RestoredAgents[0].Mode != string(engine.ThreadModeLiveAttach) {
		t.Fatalf("live restored ProjectGraph Agents = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}

	app := New(Config{Engine: query, Resumed: true, ReducedMotion: true})
	before := query.RuntimeSnapshot()
	if err := app.activateThreadByID(childThread); err != nil {
		t.Fatal(err)
	}
	detail, ok := query.AgentDetailSnapshot(agentID)
	if !ok || app.activeThreadViewMode() != engine.ThreadModeLiveAttach ||
		detail.Storage != "retained" || detail.Agent.Status != "running" ||
		detail.Agent.ParentToolUseID != "spawn-background" {
		t.Fatalf("live ProjectGraph detail = ok:%v mode:%q %#v", ok, app.activeThreadViewMode(), detail)
	}
	assertP139dRuntimeUnchanged(t, before, query.RuntimeSnapshot(), agentID)
	if executor.calls.Load() != 1 {
		t.Fatalf("live TUI attachment changed executor count to %d", executor.calls.Load())
	}

	releaseOnce.Do(func() { close(release) })
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestP139dProjectGraphOrphanViewAndControlsRemainInert(t *testing.T) {
	cwd := t.TempDir()
	parentDir := filepath.Join(cwd, "parent-transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	executor := &p139dProjectionExecutor{err: errors.New("orphan view must not dispatch")}
	runner.SetExecutor(executor)

	const (
		parentSession = "orphan-parent"
		parentThread  = "orphan-parent-thread"
		agentID       = "orphan-agent"
		childSession  = "orphan-session"
		childThread   = "orphan-thread"
	)
	child := transcript.NewRecorder(childSession, runner.DurableTranscriptDir())
	if err := child.Replace([]*schema.Message{{
		Role: schema.User, Content: "admitted before Agent metadata commit",
	}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := session.WriteSessionMetadata(child, &session.SessionMetadataFull{
		SessionID: childSession, ThreadID: childThread, AgentID: agentID,
		AgentGeneration: 1, ParentSessionID: parentSession, ParentThreadID: parentThread,
		ParentToolUseID: "spawn-orphan", QueryKernelVersion: "project_graph/v1",
		QueryKernelStage: "foreground_child", Status: "running", CWD: cwd,
		CreatedAt: now, UpdatedAt: now, MessageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	parentPath := writeP139dParentSession(t, parentDir, parentSession, parentThread, cwd, nil)
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "fresh", CWD: cwd, TranscriptDir: parentDir, AgentRunner: runner,
	})
	t.Cleanup(query.Close)
	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: parentSession, CWD: cwd, TranscriptDir: parentDir, TranscriptPath: parentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 ||
		resumed.RestoredAgents[0].Status != "aborted" ||
		resumed.RestoredAgents[0].Mode != string(engine.ThreadModeReplayOnly) {
		t.Fatalf("orphan restore = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}

	app := New(Config{Engine: query, Resumed: true, ReducedMotion: true})
	before := query.RuntimeSnapshot()
	if err := app.activateThreadByID(childThread); err != nil {
		t.Fatal(err)
	}
	detail, ok := query.AgentDetailSnapshot(agentID)
	if !ok || detail.Agent.Error == "" || detail.Thread.Status != engine.RuntimeThreadAborted ||
		len(detail.Messages) != 1 {
		t.Fatalf("orphan detail = ok:%v %#v", ok, detail)
	}
	app.textarea.SetValue("must remain inert")
	app.sendMessage()
	assertP139dRuntimeUnchanged(t, before, query.RuntimeSnapshot(), agentID)
	if executor.calls.Load() != 0 {
		t.Fatalf("orphan view dispatched %d Agent executions", executor.calls.Load())
	}
}

func writeP139dParentSession(
	t *testing.T,
	dir string,
	sessionID string,
	threadID string,
	cwd string,
	agentIDs []string,
) string {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, dir)
	if err := recorder.Replace([]*schema.Message{
		{Role: schema.User, Content: "parent prompt"},
		{Role: schema.Assistant, Content: "parent answer"},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeTUIProjectGraphRootMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: sessionID, ThreadID: threadID, CWD: cwd,
		AgentIDs:  append([]string(nil), agentIDs...),
		CreatedAt: now, UpdatedAt: now, MessageCount: 2,
	})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	return recorder.Path()
}

func p139dThreadEntry(
	catalog engine.RuntimeThreadCatalogSnapshot,
	threadID string,
) (engine.RuntimeThreadCatalogEntry, bool) {
	for _, entry := range catalog.Threads {
		if entry.ThreadID == threadID {
			return entry, true
		}
	}
	return engine.RuntimeThreadCatalogEntry{}, false
}

func assertP139dRuntimeUnchanged(
	t *testing.T,
	before engine.RuntimeSnapshot,
	after engine.RuntimeSnapshot,
	agentID string,
) {
	t.Helper()
	beforeAgent, beforeOK := before.Agents[agentID]
	afterAgent, afterOK := after.Agents[agentID]
	if before.Revision != after.Revision || beforeOK != afterOK ||
		beforeAgent.Status != afterAgent.Status ||
		beforeAgent.Generation != afterAgent.Generation ||
		beforeAgent.ThreadID != afterAgent.ThreadID ||
		beforeAgent.ParentThreadID != afterAgent.ParentThreadID ||
		beforeAgent.ParentToolUseID != afterAgent.ParentToolUseID {
		t.Fatalf("TUI projection mutated runtime truth:\nbefore=%#v\nafter=%#v", before, after)
	}
}
