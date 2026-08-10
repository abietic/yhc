package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type engineIsolationExecutor struct {
	entered chan<- string
}

type backgroundOwnershipProbeModel struct {
	entered  chan struct{}
	probe    chan struct{}
	survived chan bool
	release  chan struct{}
}

func (m *backgroundOwnershipProbeModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	if err := m.wait(ctx); err != nil {
		return nil, err
	}
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *backgroundOwnershipProbeModel) Stream(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.wait(ctx); err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "done",
	}}), nil
}

func (m *backgroundOwnershipProbeModel) wait(ctx context.Context) error {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	<-m.probe
	m.survived <- ctx.Err() == nil
	select {
	case <-m.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e engineIsolationExecutor) ExecuteAgent(ctx context.Context, opts tools.AgentExecOptions) (*tools.AgentExecResult, error) {
	e.entered <- opts.Task
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAgentRunnerIsolationBetweenEngines(t *testing.T) {
	runner1 := tools.NewAgentRunner(2)
	runner2 := tools.NewAgentRunner(4)

	ctx := context.Background()
	ctx1 := tools.WithAgentRunner(ctx, runner1)
	ctx2 := tools.WithAgentRunner(ctx, runner2)

	got1 := tools.AgentRunnerFromCtx(ctx1)
	got2 := tools.AgentRunnerFromCtx(ctx2)

	if got1 != runner1 {
		t.Error("ctx1 should return runner1")
	}
	if got2 != runner2 {
		t.Error("ctx2 should return runner2")
	}
	if got1 == got2 {
		t.Error("runners should be different instances")
	}

	gotDefault := tools.AgentRunnerFromCtx(ctx)
	if gotDefault != nil {
		t.Error("bare context must fail closed without an AgentRunner owner")
	}
}

func TestAgentDescriptionsStayScopedToEngineRegistry(t *testing.T) {
	originalDefaultRegistry := tools.DefaultRegistry
	t.Cleanup(func() {
		tools.DefaultRegistry = originalDefaultRegistry
	})

	compatibilityRegistry := tools.NewRegistry()
	compatibilityRegistry.Register(tools.AgentTool())
	engineRegistry := tools.NewRegistry()
	engineRegistry.Register(tools.AgentTool())
	tools.DefaultRegistry = compatibilityRegistry

	before := compatibilityRegistry.Resolve("Agent")
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:          &fixedResponseModel{response: "unused"},
		CWD:                t.TempDir(),
		TranscriptDir:      t.TempDir(),
		ToolRegistry:       engineRegistry,
		SkillRegistry:      skills.NewSkillRegistry(),
		PermissionRegistry: NewPermissionCoordinatorRegistry(),
	})
	t.Cleanup(engine.Close)

	after := compatibilityRegistry.Resolve("Agent")
	if after.Generation != before.Generation {
		t.Fatalf(
			"compatibility registry generation = %d, want unchanged %d",
			after.Generation,
			before.Generation,
		)
	}
	if after.Implementation.Info.Desc != before.Implementation.Info.Desc {
		t.Fatal("engine initialization mutated the compatibility Agent description")
	}
	scopedAgent, ok := engineRegistry.Get("Agent")
	if !ok || scopedAgent.Info == nil ||
		!strings.Contains(scopedAgent.Info.Desc, "Available agent types") {
		t.Fatalf(
			"engine-scoped Agent description was not updated: %#v",
			scopedAgent.Info,
		)
	}
}

func TestMCPManagerIsolationBetweenEngines(t *testing.T) {
	mgr1 := tools.NewMCPToolManager()
	mgr2 := tools.NewMCPToolManager()

	ctx := context.Background()
	ctx1 := tools.WithMCPManager(ctx, mgr1)
	ctx2 := tools.WithMCPManager(ctx, mgr2)

	got1 := tools.MCPManagerFromCtx(ctx1)
	got2 := tools.MCPManagerFromCtx(ctx2)

	if got1 != mgr1 {
		t.Error("ctx1 should return mgr1")
	}
	if got2 != mgr2 {
		t.Error("ctx2 should return mgr2")
	}
	if got1 == got2 {
		t.Error("managers should be different instances")
	}
}

func TestTaskManagerIsolationBetweenEngines(t *testing.T) {
	manager1 := tools.NewTaskManager()
	manager2 := tools.NewTaskManager()

	ctx := context.Background()
	ctx1 := tools.WithTaskManager(ctx, manager1)
	ctx2 := tools.WithTaskManager(ctx, manager2)

	if tools.TaskManagerFromCtx(ctx1) != manager1 {
		t.Error("ctx1 should return manager1")
	}
	if tools.TaskManagerFromCtx(ctx2) != manager2 {
		t.Error("ctx2 should return manager2")
	}
	if tools.TaskManagerFromCtx(ctx1) == tools.TaskManagerFromCtx(ctx2) {
		t.Error("task managers should be different instances")
	}
}

func TestConcurrentTaskInspectionRemainsEngineScoped(t *testing.T) {
	engine1 := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: p311bPrivateDir(t),
	})
	engine2 := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: p311bPrivateDir(t),
	})
	t.Cleanup(engine1.Close)
	t.Cleanup(engine2.Close)

	start := make(chan struct{})
	errs := make(chan error, 4)
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		<-start
		for i := range 64 {
			if _, err := engine1.GetTaskManager().CreateWithError(
				fmt.Sprintf("engine-one-%d", i),
				"private",
				"",
				nil,
			); err != nil {
				errs <- fmt.Errorf("engine one task create: %w", err)
				return
			}
		}
	}()
	go func() {
		defer writers.Done()
		<-start
		for i := range 64 {
			if _, err := engine2.GetTaskManager().CreateWithError(
				fmt.Sprintf("engine-two-%d", i),
				"private",
				"",
				nil,
			); err != nil {
				errs <- fmt.Errorf("engine two task create: %w", err)
				return
			}
		}
	}()

	var readers sync.WaitGroup
	readers.Add(2)
	inspect := func(engine *QueryEngine, prefix string) {
		defer readers.Done()
		<-start
		for range 64 {
			for _, task := range engine.RuntimeInspectionSnapshot().Tasks.LocalTasks {
				if !strings.HasPrefix(task.Subject, prefix) {
					errs <- fmt.Errorf(
						"%s inspection leaked task %q",
						prefix,
						task.Subject,
					)
					return
				}
			}
		}
	}
	go inspect(engine1, "engine-one-")
	go inspect(engine2, "engine-two-")

	close(start)
	writers.Wait()
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(engine1.RuntimeInspectionSnapshot().Tasks.LocalTasks) != 64 ||
		len(engine2.RuntimeInspectionSnapshot().Tasks.LocalTasks) != 64 {
		t.Fatal("concurrent task writes were lost")
	}
}

func TestSkillRegistryIsolationBetweenEngines(t *testing.T) {
	reg1 := tools.SkillRegistryFromCtx(context.Background())
	// Should fall back to DefaultSkillRegistry
	if reg1 != tools.DefaultSkillRegistry {
		t.Error("bare context should fall back to DefaultSkillRegistry")
	}
}

func TestWebFetchSideModelPerEngine(t *testing.T) {
	// Bare context falls back to global
	got := tools.WebFetchModelFromCtx(context.Background())
	if got != tools.WebFetchSideModel {
		t.Error("bare context should fall back to WebFetchSideModel")
	}
}

func TestConcurrentEnginesOwnAndShutdownIndependentRuntimes(t *testing.T) {
	engine1 := NewQueryEngine(QueryEngineConfig{
		CWD:           t.TempDir(),
		TranscriptDir: p311bPrivateDir(t),
	})
	engine2 := NewQueryEngine(QueryEngineConfig{
		CWD:           t.TempDir(),
		TranscriptDir: p311bPrivateDir(t),
	})
	t.Cleanup(engine1.Close)
	t.Cleanup(engine2.Close)
	if engine1.agentRunner == engine2.agentRunner ||
		engine1.GetTaskManager() == engine2.GetTaskManager() ||
		engine1.GetMCPManager() == engine2.GetMCPManager() {
		t.Fatal("top-level engines unexpectedly share runtime dependencies")
	}

	if _, err := engine1.GetTaskManager().CreateWithError(
		"engine-one",
		"private task",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if got := engine1.RuntimeInspectionSnapshot().Tasks.LocalTasks; len(got) != 1 {
		t.Fatalf("engine1 local tasks = %#v", got)
	}
	if got := engine2.RuntimeInspectionSnapshot().Tasks.LocalTasks; len(got) != 0 {
		t.Fatalf("engine2 leaked engine1 local tasks: %#v", got)
	}

	entered := make(chan string, 2)
	engine1.agentRunner.SetExecutor(engineIsolationExecutor{entered: entered})
	engine2.agentRunner.SetExecutor(engineIsolationExecutor{entered: entered})
	agent1, err := tools.RunAgentBackground(context.Background(), engine1.agentRunner, tools.AgentExecOptions{Task: "engine-1"})
	if err != nil {
		t.Fatal(err)
	}
	agent2, err := tools.RunAgentBackground(context.Background(), engine2.agentRunner, tools.AgentExecOptions{Task: "engine-2"})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, 2)
	for len(seen) < 2 {
		select {
		case task := <-entered:
			seen[task] = true
		case <-time.After(time.Second):
			t.Fatal("concurrent engine agents did not start")
		}
	}

	engine1.Close()
	first, _ := engine1.agentRunner.GetAgentSnapshot(agent1.ID)
	second, _ := engine2.agentRunner.GetAgentSnapshot(agent2.ID)
	if first.Status != "aborted" {
		t.Fatalf("closed engine Agent status = %q, want aborted", first.Status)
	}
	if second.Status != "running" {
		t.Fatalf("other engine Agent status = %q, want running", second.Status)
	}
}

func TestRootLineageSharesOneExplicitLogicalWorkRuntime(t *testing.T) {
	rootDir := p311bPrivateDir(t)
	root := NewQueryEngine(QueryEngineConfig{
		SessionID:     "lineage-session",
		ThreadID:      "lineage-root",
		CWD:           rootDir,
		TranscriptDir: rootDir,
	})
	t.Cleanup(root.Close)
	child := NewQueryEngine(QueryEngineConfig{
		SessionID:           "lineage-session",
		ThreadID:            "lineage-child",
		RootSessionID:       "lineage-session",
		AgentID:             "lineage-agent",
		CWD:                 t.TempDir(),
		TranscriptDir:       t.TempDir(),
		RuntimeState:        root.runtimeState,
		AgentRunner:         root.agentRunner,
		TaskManager:         root.taskManager,
		logicalWorkAdapter:  root.logicalWorkAdapter,
		sessionDeletionGate: root.sessionDeletionGate,
	})
	t.Cleanup(child.Close)

	if child.agentRunner != root.agentRunner ||
		child.taskManager != root.taskManager ||
		child.logicalWorkAdapter != root.logicalWorkAdapter ||
		child.runtimeState != root.runtimeState {
		t.Fatal("child QueryEngine allocated a second root-lineage owner")
	}
	if child.ownsAgentRunner {
		t.Fatal("child QueryEngine claimed ownership of the shared AgentRunner")
	}
}

func TestP139bEngineOwnedRunnerCloseCancelsAndJoinsBackgroundGraph(t *testing.T) {
	model := &preResponseBlockingModel{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	transcriptDir := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "owned-root-session",
		ThreadID:      "owned-root-thread",
		CWD:           t.TempDir(),
		TranscriptDir: transcriptDir,
		ChatModel:     model,
		ToolRegistry:  tools.NewRegistry(),
		SkillRegistry: skills.NewSkillRegistry(),
	})
	eng.agentRunner.SetOutputDir(t.TempDir())
	started, err := tools.RunAgentBackground(
		context.Background(),
		eng.agentRunner,
		tools.AgentExecOptions{
			Task:      "owned background graph",
			SessionID: "owned-background-session",
			ThreadID:  "owned-background-thread",
			AgentID:   "owned-background-agent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	<-model.entered

	eng.Close()
	snapshot, ok := eng.agentRunner.GetAgentSnapshot(started.ID)
	if !ok || snapshot.Status != "aborted" || snapshot.Error == nil {
		t.Fatalf(
			"owned runner Close did not join aborted Graph child: %#v, found=%v",
			snapshot,
			ok,
		)
	}
	thread, ok := eng.runtimeState.ThreadSnapshot("owned-background-thread")
	if !ok {
		t.Fatal("owned background runtime thread missing")
	}
	terminalCount := 0
	for _, event := range thread.Events {
		if event.Type == EventTerminal {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("owned background terminal count = %d, want 1", terminalCount)
	}
}

func TestP139bSharedRunnerSurvivesQueryEngineClose(t *testing.T) {
	model := &backgroundOwnershipProbeModel{
		entered:  make(chan struct{}, 1),
		probe:    make(chan struct{}),
		survived: make(chan bool, 1),
		release:  make(chan struct{}),
	}
	cwd := t.TempDir()
	runtimeState := NewRuntimeStateStore()
	executor := NewSubAgentExecutor(model, tools.NewRegistry(), cwd)
	executor.RuntimeState = runtimeState
	executor.MCPManager = tools.NewMCPToolManager()
	executor.SkillRegistry = skills.NewSkillRegistry()
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(executor)
	executor.AgentRunner = runner

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "shared-root-session",
		ThreadID:      "shared-root-thread",
		CWD:           cwd,
		TranscriptDir: t.TempDir(),
		RuntimeState:  runtimeState,
		AgentRunner:   runner,
	})
	started, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		tools.AgentExecOptions{
			Task:      "shared background graph",
			SessionID: "shared-background-session",
			ThreadID:  "shared-background-thread",
			AgentID:   "shared-background-agent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	<-model.entered

	eng.Close()
	close(model.probe)
	if survived := <-model.survived; !survived {
		t.Fatal("closing a QueryEngine cancelled its injected shared runner")
	}
	snapshot, ok := runner.GetAgentSnapshot(started.ID)
	if !ok || snapshot.Status != "running" {
		t.Fatalf(
			"shared background child after engine Close = %#v, found=%v",
			snapshot,
			ok,
		)
	}

	close(model.release)
	waitForAgentStatus(t, runner, started.ID, "completed", 5*time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("outer owner shutdown: %v", err)
	}
}

func TestCloneFileStateCacheIsMutationIsolated(t *testing.T) {
	parent := NewFileStateCache()
	parent.ReadFiles["parent.go"] = true
	child := cloneFileStateCache(parent)
	child.mu.Lock()
	child.ReadFiles["child.go"] = true
	child.mu.Unlock()

	parent.mu.RLock()
	_, leaked := parent.ReadFiles["child.go"]
	parent.mu.RUnlock()
	if leaked {
		t.Fatal("child file-state mutation leaked into parent")
	}
}

var _ tools.AgentExecutor = engineIsolationExecutor{}
