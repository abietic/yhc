package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

type p139aAdmissionBlockingExecutor struct {
	*SubAgentExecutor
	entered chan struct{}
}

func (e *p139aAdmissionBlockingExecutor) ExecuteAgent(
	ctx context.Context,
	_ tools.AgentExecOptions,
) (*tools.AgentExecResult, error) {
	select {
	case e.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type p139aConcurrentAdmissionExecutor struct {
	*SubAgentExecutor
	ready       atomic.Int32
	releaseOnce sync.Once
	release     chan struct{}
	entered     chan string
}

func (e *p139aConcurrentAdmissionExecutor) RecordAgentLaunch(
	ctx context.Context,
	launch tools.AgentLaunchSnapshot,
) error {
	if err := e.SubAgentExecutor.RecordAgentLaunch(ctx, launch); err != nil {
		return err
	}
	if e.ready.Add(1) == 2 {
		e.releaseOnce.Do(func() { close(e.release) })
	}
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *p139aConcurrentAdmissionExecutor) ExecuteAgent(
	ctx context.Context,
	opts tools.AgentExecOptions,
) (*tools.AgentExecResult, error) {
	select {
	case e.entered <- opts.AgentID:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestP139aForegroundChildUsesProjectGraphWithExistingOwners(t *testing.T) {
	const (
		parentSession = "foreground-parent-session"
		parentThread  = "foreground-parent-thread"
		parentAgent   = "foreground-parent-agent"
		parentTool    = "foreground-parent-tool"
		childSession  = "foreground-child-session"
		childThread   = "foreground-child-thread"
		childAgent    = "foreground-child-agent"
		toolUseID     = "foreground-mutate-call"
	)
	cwd := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	registry := tools.NewRegistry()
	var toolCalls atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "ForegroundMutate"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			toolCalls.Add(1)
			return "mutated once", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   toolUseID,
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "ForegroundMutate",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	runtimeState := NewRuntimeStateStore()
	permissionRegistry := NewPermissionCoordinatorRegistry()
	var promptMu sync.Mutex
	var promptRequest PermissionPromptRequest
	var coordinatorOwned bool
	var promptCalls int
	executor := NewSubAgentExecutor(model, registry, cwd)
	executor.RuntimeState = runtimeState
	executor.PermissionRegistry = permissionRegistry
	executor.PermissionProjectRoot = cwd
	executor.RootSessionID = parentSession
	executor.ParentApprovals = permission.NewApprovalTracker()
	executor.MCPManager = tools.NewMCPToolManager()
	executor.SkillRegistry = skills.NewSkillRegistry()
	executor.ParentPermissionPrompt = func(
		ctx context.Context,
		request PermissionPromptRequest,
	) PermissionInteractionResult {
		promptMu.Lock()
		promptCalls++
		promptRequest = request
		coordinatorOwned = isCoordinatorOwnedPermissionPrompt(ctx)
		promptMu.Unlock()
		return PermissionInteractionResult{
			Decision: PermissionAllowOnce,
			Message:  "approved once",
		}
	}
	runner := tools.NewAgentRunner(2)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)
	executor.AgentRunner = runner

	result, err := tools.RunAgent(
		tools.WithAgentExecutor(
			tools.WithAgentRunner(context.Background(), runner),
			executor,
		),
		runner,
		tools.AgentExecOptions{
			Task:            "mutate once",
			Description:     "foreground Graph",
			SessionID:       childSession,
			ThreadID:        childThread,
			AgentID:         childAgent,
			ParentSessionID: parentSession,
			ParentThreadID:  parentThread,
			ParentAgentID:   parentAgent,
			ToolUseID:       parentTool,
		},
	)
	if err != nil {
		t.Fatalf("run foreground child: %v", err)
	}
	if result == nil || !strings.Contains(result.Result, "done") {
		t.Fatalf("foreground result = %#v", result)
	}
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want 2", model.callCount)
	}
	if toolCalls.Load() != 1 {
		promptMu.Lock()
		gotPromptCalls := promptCalls
		promptMu.Unlock()
		t.Fatalf(
			"tool calls = %d, want 1; prompt calls=%d; result=%#v",
			toolCalls.Load(),
			gotPromptCalls,
			result,
		)
	}

	promptMu.Lock()
	gotPromptCalls := promptCalls
	gotPrompt := promptRequest
	gotCoordinatorOwned := coordinatorOwned
	promptMu.Unlock()
	if gotPromptCalls != 1 || !gotCoordinatorOwned {
		t.Fatalf(
			"permission prompt calls=%d coordinator_owned=%v",
			gotPromptCalls,
			gotCoordinatorOwned,
		)
	}
	if gotPrompt.RootSessionID != parentSession ||
		gotPrompt.SessionID != childSession ||
		gotPrompt.ThreadID != childThread ||
		gotPrompt.AgentID != childAgent ||
		gotPrompt.ToolUseID != toolUseID {
		t.Fatalf("permission identity = %#v", gotPrompt)
	}

	thread, ok := runtimeState.ThreadSnapshot(childThread)
	if !ok {
		t.Fatal("child runtime thread was not projected")
	}
	terminalCount := 0
	permissionCount := 0
	for _, event := range thread.Events {
		if event.SessionID != childSession ||
			event.ThreadID != childThread ||
			event.AgentID != childAgent ||
			event.ParentSessionID != parentSession ||
			event.ParentThreadID != parentThread ||
			event.ParentAgentID != parentAgent ||
			event.ParentToolUseID != parentTool {
			t.Fatalf("runtime lineage drift = %#v", event.RuntimeEventEnvelope)
		}
		switch event.Type {
		case EventPermissionRequest:
			permissionCount++
			if event.ToolUseID != toolUseID {
				t.Fatalf("permission tool use id = %q", event.ToolUseID)
			}
		case EventTerminal:
			terminalCount++
		}
	}
	if permissionCount != 1 || terminalCount != 1 {
		t.Fatalf(
			"permission events=%d terminal events=%d",
			permissionCount,
			terminalCount,
		)
	}
	snapshot, ok := runner.GetAgentSnapshot(childAgent)
	if !ok || snapshot.Status != "completed" {
		t.Fatalf("Agent lifecycle = %#v, found=%v", snapshot, ok)
	}
	runtimeSnapshot := runtimeState.Snapshot(childThread)
	runtimeAgent, ok := runtimeSnapshot.Agents[childAgent]
	if !ok || runtimeAgent.Status != "completed" ||
		runtimeAgent.Generation != 1 {
		t.Fatalf("runtime Agent lifecycle = %#v, found=%v", runtimeAgent, ok)
	}

	loaded := loadProjectGraphSession(
		t,
		filepath.Join(outputDir, "transcripts"),
		childSession,
	)
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.QueryKernelVersion != queryKernelVersionProjectGraph ||
		metadata.QueryKernelStage !=
			string(queryKernelStageForegroundChild) ||
		metadata.AgentID != childAgent ||
		metadata.AgentGeneration != 1 ||
		metadata.ParentSessionID != parentSession ||
		metadata.ParentThreadID != parentThread ||
		metadata.ParentAgentID != parentAgent ||
		metadata.ParentToolUseID != parentTool {
		t.Fatalf("foreground child metadata = %#v", metadata)
	}

	restarted := NewQueryEngine(QueryEngineConfig{
		SessionID:        childSession,
		ThreadID:         childThread,
		AgentID:          childAgent,
		TranscriptDir:    filepath.Join(outputDir, "transcripts"),
		CWD:              cwd,
		ChatModel:        model,
		ToolRegistry:     registry,
		PermissionPrompt: executor.ParentPermissionPrompt,
	})
	defer restarted.Close()
	if restarted.queryKernelSelection.kernel == nil ||
		restarted.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		restarted.queryKernelSelection.stage !=
			queryKernelStageForegroundChild ||
		restarted.projectGraphHITLEnabled {
		t.Fatalf(
			"restart selection=%#v hitl=%v",
			restarted.queryKernelSelection,
			restarted.projectGraphHITLEnabled,
		)
	}
}

func TestP139aExistingForegroundPinRejectsParentLineageAndCWDDrift(t *testing.T) {
	const (
		childSession = "lineage-child-session"
		childThread  = "lineage-child-thread"
		childAgent   = "lineage-child-agent"
	)
	cwd := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	model := &canonicalScriptModel{}
	base := NewSubAgentExecutor(model, tools.NewRegistry(), cwd)
	base.MCPManager = tools.NewMCPToolManager()
	base.SkillRegistry = skills.NewSkillRegistry()
	executor := &p139aAdmissionBlockingExecutor{
		SubAgentExecutor: base,
		entered:          make(chan struct{}, 1),
	}
	firstRunner := tools.NewAgentRunner(1)
	firstRunner.SetOutputDir(outputDir)
	firstRunner.SetExecutor(executor)
	base.AgentRunner = firstRunner

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := tools.RunAgent(firstCtx, firstRunner, tools.AgentExecOptions{
			Task:            "pin exact lineage",
			SessionID:       childSession,
			ThreadID:        childThread,
			AgentID:         childAgent,
			ParentSessionID: "parent-session-a",
			ParentThreadID:  "parent-thread-a",
			ParentAgentID:   "parent-agent-a",
			ToolUseID:       "parent-tool-a",
		})
		firstResult <- err
	}()
	<-executor.entered
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel first foreground generation: %v", err)
	}

	secondRunner := tools.NewAgentRunner(1)
	secondRunner.SetOutputDir(outputDir)
	secondRunner.SetExecutor(base)
	base.AgentRunner = secondRunner
	_, err := tools.RunAgent(
		context.Background(),
		secondRunner,
		tools.AgentExecOptions{
			Task:            "must not claim another parent lineage",
			SessionID:       childSession,
			ThreadID:        childThread,
			AgentID:         childAgent,
			ParentSessionID: "parent-session-b",
			ParentThreadID:  "parent-thread-b",
			ParentAgentID:   "parent-agent-b",
			ToolUseID:       "parent-tool-b",
		},
	)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"incompatible execution admission",
		) {
		t.Fatalf("parent-lineage drift error = %v", err)
	}
	if model.callCount != 0 {
		t.Fatalf("parent-lineage drift reached model %d times", model.callCount)
	}

	thirdRunner := tools.NewAgentRunner(1)
	thirdRunner.SetOutputDir(outputDir)
	thirdRunner.SetExecutor(base)
	base.AgentRunner = thirdRunner
	_, err = tools.RunAgent(
		context.Background(),
		thirdRunner,
		tools.AgentExecOptions{
			Task:            "must not claim another cwd",
			SessionID:       childSession,
			ThreadID:        childThread,
			AgentID:         childAgent,
			ParentSessionID: "parent-session-a",
			ParentThreadID:  "parent-thread-a",
			ParentAgentID:   "parent-agent-a",
			ToolUseID:       "parent-tool-a",
			CWD:             t.TempDir(),
		},
	)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"incompatible execution admission",
		) {
		t.Fatalf("cwd drift error = %v", err)
	}

	loaded := loadProjectGraphSession(
		t,
		filepath.Join(outputDir, "transcripts"),
		childSession,
	)
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.ParentSessionID != "parent-session-a" ||
		metadata.ParentThreadID != "parent-thread-a" ||
		metadata.ParentAgentID != "parent-agent-a" ||
		metadata.ParentToolUseID != "parent-tool-a" {
		t.Fatalf("durable parent lineage drifted = %#v", metadata)
	}
}

func TestP139aConcurrentForegroundAdmissionDoesNotClobberWinner(t *testing.T) {
	const (
		childSession = "concurrent-child-session"
		childThread  = "concurrent-child-thread"
	)
	cwd := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	base := NewSubAgentExecutor(
		&canonicalScriptModel{},
		tools.NewRegistry(),
		cwd,
	)
	base.MCPManager = tools.NewMCPToolManager()
	base.SkillRegistry = skills.NewSkillRegistry()
	executor := &p139aConcurrentAdmissionExecutor{
		SubAgentExecutor: base,
		release:          make(chan struct{}),
		entered:          make(chan string, 2),
	}
	firstRunner := tools.NewAgentRunner(1)
	firstRunner.SetOutputDir(outputDir)
	firstRunner.SetExecutor(executor)
	secondRunner := tools.NewAgentRunner(1)
	secondRunner.SetOutputDir(outputDir)
	secondRunner.SetExecutor(executor)

	type runResult struct {
		agentID string
		err     error
	}
	results := make(chan runResult, 2)
	contexts := make([]context.Context, 2)
	cancels := make([]context.CancelFunc, 2)
	agentIDs := []string{"concurrent-agent-a", "concurrent-agent-b"}
	runners := []*tools.AgentRunner{firstRunner, secondRunner}
	for index := range runners {
		contexts[index], cancels[index] = context.WithCancel(
			context.Background(),
		)
		runIndex := index
		go func() {
			_, err := tools.RunAgent(
				contexts[runIndex],
				runners[runIndex],
				tools.AgentExecOptions{
					Task:            "task-" + agentIDs[runIndex],
					SessionID:       childSession,
					ThreadID:        childThread,
					AgentID:         agentIDs[runIndex],
					ParentSessionID: "concurrent-parent-session",
					ParentThreadID:  "concurrent-parent-thread",
					ToolUseID:       "concurrent-parent-tool",
				},
			)
			results <- runResult{
				agentID: agentIDs[runIndex],
				err:     err,
			}
		}()
	}

	var winner string
	select {
	case winner = <-executor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no concurrent admission winner reached executor")
	}
	var loser runResult
	select {
	case loser = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent admission loser did not fail closed")
	}
	if loser.agentID == winner || loser.err == nil {
		t.Fatalf("winner=%q loser=%#v", winner, loser)
	}
	if !strings.Contains(loser.err.Error(), "execution admission") {
		t.Fatalf("concurrent admission loser error = %v", loser.err)
	}

	loaded := loadProjectGraphSession(
		t,
		filepath.Join(outputDir, "transcripts"),
		childSession,
	)
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil || metadata.AgentID != winner {
		t.Fatalf(
			"concurrent admission metadata=%#v winner=%q",
			metadata,
			winner,
		)
	}
	if len(loaded.Messages) != 1 ||
		loaded.Messages[0].Content != "task-"+winner {
		t.Fatalf(
			"concurrent admission messages=%#v winner=%q",
			loaded.Messages,
			winner,
		)
	}

	for _, cancel := range cancels {
		cancel()
	}
	select {
	case finished := <-results:
		if finished.agentID != winner ||
			!errors.Is(finished.err, context.Canceled) {
			t.Fatalf("winner completion = %#v", finished)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent admission winner did not stop")
	}
}

func TestP139bBackgroundChildUsesProjectGraphAdmission(t *testing.T) {
	cwd := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	model := &canonicalScriptModel{}
	registry := tools.NewRegistry()
	executor := NewSubAgentExecutor(model, registry, cwd)
	executor.MCPManager = tools.NewMCPToolManager()
	executor.SkillRegistry = skills.NewSkillRegistry()
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)
	executor.AgentRunner = runner

	started, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		tools.AgentExecOptions{
			Task:      "background remains ordinary",
			SessionID: "background-session",
			ThreadID:  "background-thread",
			AgentID:   "background-agent",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentStatus(
		t,
		runner,
		started.ID,
		"completed",
		5*time.Second,
	)
	loaded := loadProjectGraphSession(
		t,
		filepath.Join(outputDir, "transcripts"),
		"background-session",
	)
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.QueryKernelVersion != queryKernelVersionProjectGraph ||
		metadata.QueryKernelStage !=
			string(queryKernelStageBackgroundChild) {
		t.Fatalf("background kernel metadata = %#v", metadata)
	}

	restarted := NewQueryEngine(QueryEngineConfig{
		SessionID:     "background-session",
		ThreadID:      "background-thread",
		AgentID:       "background-agent",
		TranscriptDir: filepath.Join(outputDir, "transcripts"),
		CWD:           cwd,
		ChatModel:     model,
		ToolRegistry:  registry,
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		},
	})
	defer restarted.Close()
	if restarted.queryKernelSelection.kernel == nil ||
		restarted.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		restarted.queryKernelSelection.stage !=
			queryKernelStageBackgroundChild ||
		restarted.projectGraphHITLEnabled {
		t.Fatalf(
			"background restart selection=%#v hitl=%v",
			restarted.queryKernelSelection,
			restarted.projectGraphHITLEnabled,
		)
	}
}

func TestP139bBackgroundProjectGraphKeepsCoordinatorPermissionAndOneTerminal(t *testing.T) {
	const (
		childSession = "background-permission-session"
		childThread  = "background-permission-thread"
		childAgent   = "background-permission-agent"
		toolUseID    = "background-mutate-call"
	)
	cwd := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	registry := tools.NewRegistry()
	var toolCalls atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "BackgroundMutate"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			toolCalls.Add(1)
			return "mutated once", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   toolUseID,
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "BackgroundMutate",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	runtimeState := NewRuntimeStateStore()
	permissionRegistry := NewPermissionCoordinatorRegistry()
	var promptMu sync.Mutex
	var promptCalls int
	var coordinatorOwned bool
	executor := NewSubAgentExecutor(model, registry, cwd)
	executor.RuntimeState = runtimeState
	executor.PermissionRegistry = permissionRegistry
	executor.PermissionProjectRoot = cwd
	executor.RootSessionID = "background-parent-session"
	executor.ParentApprovals = permission.NewApprovalTracker()
	executor.MCPManager = tools.NewMCPToolManager()
	executor.SkillRegistry = skills.NewSkillRegistry()
	executor.ParentPermissionPrompt = func(
		ctx context.Context,
		_ PermissionPromptRequest,
	) PermissionInteractionResult {
		promptMu.Lock()
		promptCalls++
		coordinatorOwned = isCoordinatorOwnedPermissionPrompt(ctx)
		promptMu.Unlock()
		return PermissionInteractionResult{
			Decision: PermissionAllowOnce,
		}
	}
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)
	executor.AgentRunner = runner

	started, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		tools.AgentExecOptions{
			Task:            "mutate once in background",
			SessionID:       childSession,
			ThreadID:        childThread,
			AgentID:         childAgent,
			ParentSessionID: "background-parent-session",
			ParentThreadID:  "background-parent-thread",
			ParentAgentID:   "background-parent-agent",
			ToolUseID:       "background-parent-tool",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentStatus(t, runner, started.ID, "completed", 5*time.Second)
	if model.callCount != 2 {
		t.Fatalf("background model calls = %d, want 2", model.callCount)
	}
	if toolCalls.Load() != 1 {
		t.Fatalf("background tool calls = %d, want 1", toolCalls.Load())
	}
	promptMu.Lock()
	gotPromptCalls := promptCalls
	gotCoordinatorOwned := coordinatorOwned
	promptMu.Unlock()
	if gotPromptCalls != 1 || !gotCoordinatorOwned {
		t.Fatalf(
			"permission prompt calls=%d coordinator_owned=%v",
			gotPromptCalls,
			gotCoordinatorOwned,
		)
	}

	thread, ok := runtimeState.ThreadSnapshot(childThread)
	if !ok {
		t.Fatal("background child runtime thread was not projected")
	}
	terminalCount := 0
	permissionCount := 0
	for _, event := range thread.Events {
		switch event.Type {
		case EventPermissionRequest:
			permissionCount++
		case EventTerminal:
			terminalCount++
		}
	}
	if permissionCount != 1 || terminalCount != 1 {
		t.Fatalf(
			"permission events=%d terminal events=%d",
			permissionCount,
			terminalCount,
		)
	}
	metadata := session.ReadSessionMetadataFull(loadProjectGraphSession(
		t,
		filepath.Join(outputDir, "transcripts"),
		childSession,
	))
	if metadata == nil ||
		metadata.QueryKernelStage !=
			string(queryKernelStageBackgroundChild) {
		t.Fatalf("background permission metadata = %#v", metadata)
	}
}

func TestP512ProjectGraphChildBashUsesDerivedGuestAuthority(t *testing.T) {
	const (
		parentSession = "p512-parent-session"
		parentThread  = "p512-parent-thread"
		parentAgent   = "p512-parent-agent"
	)

	newFixture := func(t *testing.T, command string, prompt func(PermissionPromptRequest) PermissionInteractionResult) (*tools.AgentRunner, *permission.ApprovalTracker, string, *containment.Bindings) {
		t.Helper()
		root := t.TempDir()
		if runtime.GOOS == "darwin" {
			cacheDir, err := os.UserCacheDir()
			if err != nil {
				t.Fatal(err)
			}
			root, err = os.MkdirTemp(cacheDir, "yhc-p512-child-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(root); err != nil {
					t.Errorf("remove P51.2 child workspace: %v", err)
				}
			})
		}
		childCWD := filepath.Join(root, "child")
		if err := os.Mkdir(childCWD, 0o700); err != nil {
			t.Fatal(err)
		}
		selection, err := NewSandboxSelection(
			containment.ProfileWorkspaceWrite,
			containment.SelectionDefault,
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		parentBindings, err := ResolveExecutionBindings(
			context.Background(), root, commands.EntrypointTUI, selection,
		)
		if err != nil {
			t.Fatal(err)
		}
		childBindings, err := DeriveChildExecutionBindings(
			context.Background(), parentBindings, childCWD, "p512-child-agent",
		)
		if err != nil {
			t.Fatal(err)
		}
		if childBindings.Guest().Digest() == parentBindings.Guest().Digest() {
			t.Fatal("child Guest binding retained parent identity")
		}

		registry := tools.NewRegistry()
		tools.RegisterDefaults(registry)
		model := &canonicalScriptModel{responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID: "p512-bash-call", Type: "function",
					Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":` + strconv.Quote(command) + `}`},
				}},
				ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
			}},
		}}}
		approvals := permission.NewApprovalTracker()
		executor := NewSubAgentExecutor(model, registry, root)
		executor.ExecutionBindings = parentBindings
		executor.PermissionMode = permission.ModeAuto
		executor.RootSessionID = parentSession
		executor.ParentApprovals = approvals
		executor.MCPManager = tools.NewMCPToolManager()
		executor.SkillRegistry = skills.NewSkillRegistry()
		if prompt != nil {
			executor.ParentPermissionPrompt = func(_ context.Context, request PermissionPromptRequest) PermissionInteractionResult {
				return prompt(request)
			}
		}
		runner := tools.NewAgentRunner(1)
		runner.SetOutputDir(filepath.Join(t.TempDir(), "agent-output"))
		runner.SetExecutor(executor)
		executor.AgentRunner = runner
		return runner, approvals, childCWD, childBindings
	}
	assertWorkspaceOutsideTempRoots := func(
		t *testing.T,
		bindings *containment.Bindings,
	) {
		t.Helper()
		spec := bindings.Guest().Policy().Spec()
		for _, tempRoot := range spec.TempRoots {
			if pathContains(tempRoot, spec.CWD) {
				t.Fatalf("P51.2 child oracle root is covered by approved temporary root")
			}
		}
	}

	t.Run("ordinary is prompt-free only with a complete derived Guest proof", func(t *testing.T) {
		var prompts atomic.Int32
		runner, approvals, childCWD, childBindings := newFixture(
			t,
			"printf derived-guest > ordinary.txt",
			func(PermissionPromptRequest) PermissionInteractionResult {
				prompts.Add(1)
				return PermissionInteractionResult{Decision: PermissionAllowOnce}
			},
		)
		if childBindings.Guest().Availability() != containment.BindingAvailable {
			t.Skip("complete Darwin Guest proof is unavailable")
		}
		assertWorkspaceOutsideTempRoots(t, childBindings)
		result, err := tools.RunAgent(context.Background(), runner, tools.AgentExecOptions{
			Task: "run ordinary Bash", SessionID: "p512-ordinary-session", ThreadID: "p512-ordinary-thread",
			AgentID: "p512-child-agent", CWD: childCWD, ParentSessionID: parentSession, ParentThreadID: parentThread, ParentAgentID: parentAgent,
		})
		if err != nil || result == nil || prompts.Load() != 0 {
			t.Fatalf("ordinary child result=%#v err=%v prompts=%d", result, err, prompts.Load())
		}
		contents, err := os.ReadFile(filepath.Join(childCWD, "ordinary.txt"))
		if err != nil || string(contents) != "derived-guest" {
			t.Fatalf("ordinary Bash output=%q err=%v", contents, err)
		}
		if approvals.Count() != 0 {
			t.Fatalf("ordinary contained auto Bash created approvals=%#v", approvals.List())
		}
	})

	t.Run("ordinary child cannot write through the wider parent Guest root", func(t *testing.T) {
		var prompts atomic.Int32
		runner, approvals, childCWD, childBindings := newFixture(
			t,
			"if printf escaped > ../parent-only.txt; then printf escaped-write-succeeded; else printf escaped-write-blocked; fi",
			func(PermissionPromptRequest) PermissionInteractionResult {
				prompts.Add(1)
				return PermissionInteractionResult{Decision: PermissionAllowOnce}
			},
		)
		if childBindings.Guest().Availability() != containment.BindingAvailable {
			t.Skip("complete Darwin Guest proof is unavailable")
		}
		assertWorkspaceOutsideTempRoots(t, childBindings)
		result, err := tools.RunAgent(context.Background(), runner, tools.AgentExecOptions{
			Task: "prove child Guest narrowing", SessionID: "p512-child-narrow-session", ThreadID: "p512-child-narrow-thread",
			AgentID: "p512-child-agent", CWD: childCWD, ParentSessionID: parentSession, ParentThreadID: parentThread, ParentAgentID: parentAgent,
		})
		if err != nil || result == nil || prompts.Load() != 0 {
			t.Fatalf("narrow child result=%#v err=%v prompts=%d", result, err, prompts.Load())
		}
		var toolOutput string
		for _, message := range result.Messages {
			if message != nil && message.Role == schema.Tool {
				toolOutput += message.Content
			}
		}
		parentOnly := filepath.Join(filepath.Dir(childCWD), "parent-only.txt")
		if _, statErr := os.Stat(parentOnly); statErr == nil {
			// The parent binding allows this path while the derived child binding
			// does not. Remove the sentinel before failing so a broken test does
			// not leave state in its temporary parent root.
			if removeErr := os.Remove(parentOnly); removeErr != nil {
				t.Fatalf("child escaped derived Guest root and sentinel cleanup failed: %v", removeErr)
			}
			t.Fatalf("child escaped derived Guest root; tool output=%q", toolOutput)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("inspect child escape sentinel: %v", statErr)
		}
		if !strings.HasSuffix(strings.TrimSpace(toolOutput), "escaped-write-blocked") {
			t.Fatalf("child Guest escape oracle output=%q", toolOutput)
		}
		if approvals.Count() != 0 {
			t.Fatalf("narrow child created approvals=%#v", approvals.List())
		}
	})

	t.Run("critical foreground accepts only live AllowOnce", func(t *testing.T) {
		var requests []PermissionPromptRequest
		runner, approvals, childCWD, _ := newFixture(t, "rm -f /", func(request PermissionPromptRequest) PermissionInteractionResult {
			requests = append(requests, request)
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		})
		result, err := tools.RunAgent(context.Background(), runner, tools.AgentExecOptions{
			Task: "confirm critical Bash", SessionID: "p512-critical-foreground-session", ThreadID: "p512-critical-foreground-thread",
			AgentID: "p512-child-agent", CWD: childCWD, ParentSessionID: parentSession, ParentThreadID: parentThread, ParentAgentID: parentAgent,
		})
		if err != nil || result == nil || len(requests) != 1 || requests[0].DecisionConstraint != PermissionAllowOnceOnly {
			t.Fatalf("foreground critical result=%#v err=%v requests=%#v", result, err, requests)
		}
		if approvals.Count() != 0 {
			t.Fatalf("foreground AllowOnce created persistent approvals=%#v", approvals.List())
		}
	})

	for _, decision := range []PermissionInteractionDecision{
		PermissionAllowSession,
		PermissionAllowAlways,
	} {
		t.Run("critical foreground rejects "+string(decision), func(t *testing.T) {
			var requests []PermissionPromptRequest
			runner, approvals, childCWD, _ := newFixture(t, "rm -f /", func(request PermissionPromptRequest) PermissionInteractionResult {
				requests = append(requests, request)
				return PermissionInteractionResult{Decision: decision}
			})
			result, err := tools.RunAgent(context.Background(), runner, tools.AgentExecOptions{
				Task: "reject persistent critical Bash", SessionID: "p512-critical-persistent-" + string(decision), ThreadID: "p512-critical-persistent-thread",
				AgentID: "p512-child-agent", CWD: childCWD, ParentSessionID: parentSession, ParentThreadID: parentThread, ParentAgentID: parentAgent,
			})
			if err != nil || result == nil || len(requests) != 1 || requests[0].DecisionConstraint != PermissionAllowOnceOnly {
				t.Fatalf("persistent critical result=%#v err=%v requests=%#v", result, err, requests)
			}
			if approvals.Count() != 0 {
				t.Fatalf("persistent critical decision created approvals=%#v", approvals.List())
			}
		})
	}

	t.Run("critical background uses a live parent route once", func(t *testing.T) {
		var requests []PermissionPromptRequest
		runner, approvals, childCWD, _ := newFixture(t, "rm -f /", func(request PermissionPromptRequest) PermissionInteractionResult {
			requests = append(requests, request)
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		})
		started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{
			Task: "reject critical Bash", SessionID: "p512-critical-background-session", ThreadID: "p512-critical-background-thread",
			AgentID: "p512-child-agent", CWD: childCWD, ParentSessionID: parentSession, ParentThreadID: parentThread, ParentAgentID: parentAgent,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitForAgentStatus(t, runner, started.ID, "completed", 5*time.Second)
		if len(requests) != 1 || requests[0].DecisionConstraint != PermissionAllowOnceOnly || approvals.Count() != 0 {
			t.Fatalf("background critical requests=%#v approvals=%#v", requests, approvals.List())
		}
	})

	t.Run("critical background without a live route fails closed", func(t *testing.T) {
		runner, approvals, childCWD, _ := newFixture(t, "rm -f /", nil)
		started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{
			Task: "reject critical Bash", SessionID: "p512-critical-background-headless-session", ThreadID: "p512-critical-background-headless-thread",
			AgentID: "p512-child-agent", CWD: childCWD, ParentSessionID: parentSession, ParentThreadID: parentThread, ParentAgentID: parentAgent,
		})
		if err != nil {
			t.Fatal(err)
		}
		waitForAgentStatus(t, runner, started.ID, "completed", 5*time.Second)
		if approvals.Count() != 0 {
			t.Fatalf("UI-less background critical approvals=%#v", approvals.List())
		}
	})
}

func TestP139bBackgroundContinuationPreservesExistingKernelPin(t *testing.T) {
	t.Run("foreground pin", func(t *testing.T) {
		cwd := t.TempDir()
		outputDir := filepath.Join(t.TempDir(), "agent-output")
		model := &canonicalScriptModel{}
		executor := NewSubAgentExecutor(model, tools.NewRegistry(), cwd)
		executor.MCPManager = tools.NewMCPToolManager()
		executor.SkillRegistry = skills.NewSkillRegistry()
		runner := tools.NewAgentRunner(1)
		runner.SetOutputDir(outputDir)
		runner.SetExecutor(executor)
		executor.AgentRunner = runner

		opts := tools.AgentExecOptions{
			Task:            "seed child session",
			SessionID:       "preserved-session",
			ThreadID:        "preserved-thread",
			AgentID:         "preserved-agent",
			ParentSessionID: "preserved-parent-session",
			ParentThreadID:  "preserved-parent-thread",
			ParentAgentID:   "preserved-parent-agent",
			ToolUseID:       "preserved-parent-tool",
		}
		if _, err := tools.RunAgent(context.Background(), runner, opts); err != nil {
			t.Fatal(err)
		}
		_, disposition, err := runner.SendOrResumeAgentMessage(
			opts.AgentID,
			tools.MessagePayload{Content: "continue asynchronously"},
		)
		if err != nil || disposition != "resumed" {
			t.Fatalf(
				"resume child: disposition=%q err=%v",
				disposition,
				err,
			)
		}
		waitForAgentStatus(
			t,
			runner,
			opts.AgentID,
			"completed",
			5*time.Second,
		)
		loaded := loadProjectGraphSession(
			t,
			filepath.Join(outputDir, "transcripts"),
			opts.SessionID,
		)
		metadata := session.ReadSessionMetadataFull(loaded)
		if metadata == nil ||
			metadata.QueryKernelVersion != queryKernelVersionProjectGraph ||
			metadata.QueryKernelStage !=
				string(queryKernelStageForegroundChild) {
			t.Fatalf("continued child kernel metadata = %#v", metadata)
		}
	})

	t.Run("historical legacy pin fails admission", func(t *testing.T) {
		cwd := t.TempDir()
		outputDir := filepath.Join(t.TempDir(), "agent-output")
		transcriptDir := filepath.Join(outputDir, "transcripts")
		const (
			sessionID = "legacy-preserved-session"
			threadID  = "legacy-preserved-thread"
			agentID   = "legacy-preserved-agent"
		)
		opts := tools.AgentExecOptions{
			Task:            "must not continue",
			SessionID:       sessionID,
			ThreadID:        threadID,
			AgentID:         agentID,
			ParentSessionID: "legacy-parent-session",
			ParentThreadID:  "legacy-parent-thread",
			ParentAgentID:   "legacy-parent-agent",
			ToolUseID:       "legacy-parent-tool",
		}
		recorder := transcript.NewRecorder(sessionID, transcriptDir)
		if err := recorder.Replace([]*schema.Message{{
			Role: schema.User, Content: "historical request",
		}}); err != nil {
			t.Fatal(err)
		}
		if err := session.WriteSessionMetadata(
			recorder,
			&session.SessionMetadataFull{
				SessionID:          sessionID,
				ThreadID:           threadID,
				AgentID:            agentID,
				ParentSessionID:    opts.ParentSessionID,
				ParentThreadID:     opts.ParentThreadID,
				ParentAgentID:      opts.ParentAgentID,
				ParentToolUseID:    opts.ToolUseID,
				CWD:                cwd,
				QueryKernelVersion: queryKernelVersionLegacy,
				QueryKernelStage:   string(queryKernelStageUnset),
				CreatedAt:          time.Now().UTC(),
			},
		); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(recorder.Path())
		if err != nil {
			t.Fatal(err)
		}

		model := &canonicalScriptModel{}
		executor := NewSubAgentExecutor(model, tools.NewRegistry(), cwd)
		executor.MCPManager = tools.NewMCPToolManager()
		executor.SkillRegistry = skills.NewSkillRegistry()
		runner := tools.NewAgentRunner(1)
		runner.SetOutputDir(outputDir)
		runner.SetExecutor(executor)
		executor.AgentRunner = runner
		if _, err := tools.RunAgentBackground(
			context.Background(),
			runner,
			opts,
		); err == nil || !strings.Contains(err.Error(), "incompatible execution admission") {
			t.Fatalf("legacy background admission error = %v", err)
		}
		if model.callCount != 0 {
			t.Fatalf("model calls = %d, want zero", model.callCount)
		}
		after, err := os.ReadFile(recorder.Path())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("rejected legacy background admission rewrote transcript")
		}
	})
}

func TestP139bBackgroundAdmissionRejectsUnattributedMessageOnlyTranscript(t *testing.T) {
	const sessionID = "message-only-background-session"
	cwd := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	transcriptDir := filepath.Join(outputDir, "transcripts")
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	if err := recorder.Replace([]*schema.Message{{
		Role: schema.User, Content: "unattributed history",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}

	model := &canonicalScriptModel{}
	executor := NewSubAgentExecutor(model, tools.NewRegistry(), cwd)
	executor.MCPManager = tools.NewMCPToolManager()
	executor.SkillRegistry = skills.NewSkillRegistry()
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)
	executor.AgentRunner = runner

	_, err = tools.RunAgentBackground(
		context.Background(),
		runner,
		tools.AgentExecOptions{
			Task:            "must not claim unattributed history",
			SessionID:       sessionID,
			ThreadID:        "message-only-thread",
			AgentID:         "message-only-agent",
			ParentSessionID: "message-only-parent-session",
			CWD:             t.TempDir(),
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "incompatible execution admission") {
		t.Fatalf("message-only background admission error = %v", err)
	}
	if model.callCount != 0 {
		t.Fatalf("message-only background admission reached model %d times", model.callCount)
	}
	after, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected message-only transcript was rewritten")
	}
}

func TestP139aForegroundChildPinCommitsBeforeExecutorEntry(t *testing.T) {
	const (
		childSession = "admission-child-session"
		childThread  = "admission-child-thread"
		childAgent   = "admission-child-agent"
	)
	cwd := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "agent-output")
	model := &canonicalScriptModel{}
	base := NewSubAgentExecutor(model, tools.NewRegistry(), cwd)
	base.MCPManager = tools.NewMCPToolManager()
	base.SkillRegistry = skills.NewSkillRegistry()
	executor := &p139aAdmissionBlockingExecutor{
		SubAgentExecutor: base,
		entered:          make(chan struct{}, 1),
	}
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)
	base.AgentRunner = runner

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := tools.RunAgent(ctx, runner, tools.AgentExecOptions{
			Task:      "must pin before executor",
			SessionID: childSession,
			ThreadID:  childThread,
			AgentID:   childAgent,
		})
		result <- err
	}()
	<-executor.entered
	if model.callCount != 0 {
		t.Fatalf("model calls before executor admission check = %d", model.callCount)
	}

	transcriptDir := filepath.Join(outputDir, "transcripts")
	loaded := loadProjectGraphSession(t, transcriptDir, childSession)
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.QueryKernelVersion != queryKernelVersionProjectGraph ||
		metadata.QueryKernelStage !=
			string(queryKernelStageForegroundChild) ||
		metadata.SessionID != childSession ||
		metadata.ThreadID != childThread ||
		metadata.AgentID != childAgent ||
		metadata.AgentGeneration != 1 {
		t.Fatalf("pre-executor foreground admission = %#v", metadata)
	}
	restarted := NewQueryEngine(QueryEngineConfig{
		SessionID:     childSession,
		ThreadID:      childThread,
		AgentID:       childAgent,
		TranscriptDir: transcriptDir,
		CWD:           cwd,
		ChatModel:     model,
		ToolRegistry:  tools.NewRegistry(),
	})
	if restarted.queryKernelSelection.kernel == nil ||
		restarted.queryKernelSelection.kernel.kind() != queryKernelProjectGraph ||
		restarted.queryKernelSelection.stage !=
			queryKernelStageForegroundChild {
		t.Fatalf(
			"crash-window restart selection = %#v",
			restarted.queryKernelSelection,
		)
	}
	restarted.Close()

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel admission-only execution: %v", err)
	}

	unpinnedDir := t.TempDir()
	unpinned := transcript.NewRecorder("unpinned-child", unpinnedDir)
	if err := unpinned.Replace([]*schema.Message{{
		Role: schema.User, Content: "seed only",
	}}); err != nil {
		t.Fatal(err)
	}
	unpinnedEngine := newForegroundChildQueryEngine(QueryEngineConfig{
		SessionID:     "unpinned-child",
		ThreadID:      "unpinned-thread",
		AgentID:       "unpinned-agent",
		TranscriptDir: unpinnedDir,
		CWD:           cwd,
		ChatModel:     model,
		ToolRegistry:  tools.NewRegistry(),
	})
	defer unpinnedEngine.Close()
	if _, err := unpinnedEngine.queryKernelForTurn(
		context.Background(),
	); err == nil ||
		!strings.Contains(
			err.Error(),
			"no durable query kernel selection",
		) {
		t.Fatalf("message-only foreground seed error = %v", err)
	}
	if model.callCount != 0 {
		t.Fatalf("crash-window model calls = %d, want 0", model.callCount)
	}
}

func TestP139aForegroundChildWithoutAgentIdentityFailsBeforeModel(t *testing.T) {
	model := &canonicalScriptModel{}
	eng := newForegroundChildQueryEngine(QueryEngineConfig{
		SessionID:     "missing-agent",
		ThreadID:      "missing-agent-thread",
		TranscriptDir: t.TempDir(),
		CWD:           t.TempDir(),
		ChatModel:     model,
		ToolRegistry:  tools.NewRegistry(),
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		},
	})
	defer eng.Close()
	if _, err := eng.queryKernelForTurn(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "requires an Agent identity") {
		t.Fatalf("selection error = %v", err)
	}
	if model.callCount != 0 {
		t.Fatalf("model calls = %d, want 0", model.callCount)
	}
}

func TestP139aForegroundChildParentCancellationSettlesOneGeneration(t *testing.T) {
	const (
		childAgent  = "cancel-child-agent"
		childThread = "cancel-child-thread"
	)
	model := &preResponseBlockingModel{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
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

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := tools.RunAgent(ctx, runner, tools.AgentExecOptions{
			Task:            "block until parent cancel",
			SessionID:       "cancel-child-session",
			ThreadID:        childThread,
			AgentID:         childAgent,
			ParentSessionID: "cancel-parent-session",
			ParentThreadID:  "cancel-parent-thread",
			ToolUseID:       "cancel-parent-tool",
		})
		result <- err
	}()
	<-model.entered
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	snapshot, ok := runner.GetAgentSnapshot(childAgent)
	if !ok || snapshot.Status != "aborted" {
		t.Fatalf("cancelled Agent lifecycle = %#v, found=%v", snapshot, ok)
	}
	thread, ok := runtimeState.ThreadSnapshot(childThread)
	if !ok {
		t.Fatal("cancelled child thread missing")
	}
	terminalCount := 0
	for _, event := range thread.Events {
		if event.Type == EventTerminal {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal events = %d, want 1", terminalCount)
	}
	runtimeAgent, ok := runtimeState.Snapshot(childThread).Agents[childAgent]
	if !ok || runtimeAgent.Status != "aborted" ||
		runtimeAgent.Generation != 1 {
		t.Fatalf(
			"cancelled runtime Agent lifecycle = %#v, found=%v, terminal=%#v",
			runtimeAgent,
			ok,
			thread.LastTerminal,
		)
	}
}
