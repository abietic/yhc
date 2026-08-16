package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestP138ProjectGraphInterruptResumeExecutesToolExactlyOnce(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int32
	var executedInput atomic.Value
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWrite"},
		ExecuteCtx: func(_ context.Context, input string) (string, error) {
			executions.Add(1)
			executedInput.Store(input)
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "write-1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "GraphWrite",
						Arguments: `{"path":"original"}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWrite"}},
	)
	config.PermissionMode = permission.ModeDefault
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not block in the legacy permission callback")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	engine := NewQueryEngine(config)
	defer engine.Close()

	events, _ := engine.SubmitMessage(context.Background(), "write once")
	first := collectGraphHITLEvents(events)
	if first.terminal == nil ||
		first.terminal.Reason != TerminalWaitingInput {
		t.Fatalf("first terminal = %#v", first.terminal)
	}
	if first.request == nil ||
		first.request.Source != "project_graph" ||
		first.request.ToolUseID != "write-1" {
		t.Fatalf("interrupt request = %#v", first.request)
	}
	if executions.Load() != 0 {
		t.Fatalf("tool executed before resume: %d", executions.Load())
	}
	if model.callCount != 1 {
		t.Fatalf("model calls before resume = %d", model.callCount)
	}
	if _, err := os.Stat(projectGraphCheckpointPath(engine.transcript.Path())); err != nil {
		t.Fatalf("durable checkpoint missing: %v", err)
	}
	notificationID := "agent-completion:v1:deferred-during-graph-interrupt"
	if _, err := engine.inputCoordinator.Enqueue(RuntimeItem{
		ID:         notificationID,
		Kind:       RuntimeItemAgentNotification,
		Priority:   RuntimePriorityNext,
		Scope:      engine.runtimeInputScope(),
		IsMeta:     true,
		Origin:     "task-notification",
		Provenance: "agent_notification",
		AgentNotification: &RuntimeAgentNotification{
			CompletionID:     notificationID,
			AgentID:          "completed-child",
			Status:           "completed",
			Message:          "<task-notification><status>completed</status></task-notification>",
			Generation:       1,
			TerminalSequence: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if item, ok, err := engine.ClaimNextRuntimeItem(); err != nil || ok {
		t.Fatalf(
			"non-resume item crossed Graph interrupt: item=%#v ok=%v err=%v",
			item,
			ok,
			err,
		)
	}

	if !engine.ResolvePermissionInteraction(
		"write-1",
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	) {
		t.Fatal("permission decision was not durably accepted")
	}
	item, ok, err := engine.ClaimNextRuntimeItem()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || item.Kind != RuntimeItemPermissionDecision {
		t.Fatalf("claimed item = %#v ok=%v", item, ok)
	}
	resumedEvents, _ := engine.SubmitRuntimeItem(
		context.Background(),
		item,
	)
	second := collectGraphHITLEvents(resumedEvents)
	if second.terminal == nil ||
		second.terminal.Reason != TerminalCompleted {
		t.Fatalf(
			"resumed terminal = %#v types=%v runtime_err=%v",
			second.terminal,
			second.types,
			engine.runtimeStateErr,
		)
	}
	if executions.Load() != 1 {
		t.Fatalf(
			"tool executions = %d, want 1; model=%d resolved=%#v tool=%#v",
			executions.Load(),
			model.callCount,
			second.resolved,
			second.toolResult,
		)
	}
	if input, _ := executedInput.Load().(string); !strings.Contains(
		input,
		`"path":"original"`,
	) {
		t.Fatalf("tool input after resume = %q", input)
	}
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want 2", model.callCount)
	}
	if _, err := os.Stat(projectGraphCheckpointPath(engine.transcript.Path())); !os.IsNotExist(err) {
		t.Fatalf("completed checkpoint still exists: %v", err)
	}
	notificationDelivered := false
	for _, message := range engine.GetMessages() {
		if message != nil &&
			message.Extra != nil &&
			message.Extra["runtime_item_id"] == notificationID {
			notificationDelivered = true
		}
		if message != nil &&
			message.Extra != nil &&
			message.Extra["command_mode"] == "graph-resume" {
			t.Fatalf("resume intent leaked into model transcript: %#v", message)
		}
	}
	if !notificationDelivered {
		t.Fatal("deferred notification was not delivered during Graph resume")
	}
}

func TestProjectGraphHITLRequestPreservesTypedInteractionIdentity(t *testing.T) {
	original := projectGraphHITLRequest{
		Version: projectGraphHITLRequestVersion, RequestID: "request-1", InterruptID: "interrupt-1",
		InvocationDigest: strings.Repeat("a", 64), PolicyRevision: strings.Repeat("b", 64),
		ToolName: "AskUserQuestion", CanonicalToolName: "AskUserQuestion", Kind: PermissionInteractionKindQuestion, Attempt: 3,
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored projectGraphHITLRequest
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Kind != original.Kind || restored.Attempt != original.Attempt || restored.CanonicalToolName != original.CanonicalToolName {
		t.Fatalf("restored typed identity = %#v", restored)
	}
}

func TestP138ProjectGraphColdRestartResumesPersistedInterrupt(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWrite"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "cold-write",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "GraphWrite",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	transcriptDir := t.TempDir()
	config := projectGraphEngineConfig(
		t,
		transcriptDir,
		"graph-hitl-cold",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWrite"}},
	)
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking permission adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	first := NewQueryEngine(config)
	initialEvents, _ := first.SubmitMessage(
		context.Background(),
		"persist interrupt",
	)
	initial := collectGraphHITLEvents(initialEvents)
	if initial.terminal == nil ||
		initial.terminal.Reason != TerminalWaitingInput {
		t.Fatalf("initial terminal = %#v", initial.terminal)
	}
	initialInterrupt, _ := first.projectGraphCheckpoint.ActiveInterrupt()
	initialPolicyDetails := []any{
		first.permissionRulesSnapshot().Snapshot(),
		first.approvalTracker.List(),
		first.PermissionMode(),
		first.PlanState(),
		first.permissionRootSessionID,
		first.GetWorkingDirectories(),
		first.config.ToolSelection,
	}
	first.Close()

	resumeHostConfig := config
	resumeHostConfig.SessionID = "graph-hitl-resume-host"
	resumeHostConfig.ThreadID = ""
	resumeHostConfig.CWD = t.TempDir()
	resumeHostConfig.PermissionProjectRoot = ""
	// Cold restart is a new composition root; it must not reuse the first
	// root's immutable MCP process owner.
	resumeHostConfig.MCPManager = tools.NewMCPToolManager()
	hostHookDir := filepath.Join(resumeHostConfig.CWD, ".claude")
	if err := os.MkdirAll(hostHookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(hostHookDir, "hooks.json"),
		[]byte(
			`{"PreToolUse":[{"matcher":"GraphWrite","hooks":[`+
				`{"command":"printf 'stale host hook' >&2; exit 2"}`+
				`]}]}`,
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	restarted := NewQueryEngine(resumeHostConfig)
	defer restarted.Close()
	resumedSession, err := restarted.ResumeSession(
		context.Background(),
		"graph-hitl-cold",
	)
	if err != nil {
		t.Fatalf("resume session: %v", err)
	}
	if !slices.Contains(
		resumedSession.ActionableRequestIDs,
		"cold-write",
	) {
		t.Fatalf(
			"restored actionable requests = %v",
			resumedSession.ActionableRequestIDs,
		)
	}
	pending, ok := restarted.PendingProjectGraphPermissionRequest()
	if !ok || pending.ToolUseID != "cold-write" {
		t.Fatalf("restored request = %#v ok=%v", pending, ok)
	}
	if !restarted.ResolvePermissionInteraction(
		pending.ToolUseID,
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	) {
		t.Fatal("restored decision was not accepted")
	}
	item, ok, err := restarted.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim restored decision: item=%#v ok=%v err=%v", item, ok, err)
	}
	events, _ := restarted.SubmitRuntimeItem(context.Background(), item)
	resumed := collectGraphHITLEvents(events)
	if resumed.terminal == nil ||
		resumed.terminal.Reason != TerminalCompleted {
		active, _ := restarted.projectGraphCheckpoint.ActiveInterrupt()
		t.Fatalf(
			"resumed terminal = %#v initial=%#v active=%#v initial_policy=%#v current_policy=%#v",
			resumed.terminal,
			initialInterrupt,
			active,
			initialPolicyDetails,
			[]any{
				restarted.permissionRulesSnapshot().Snapshot(),
				restarted.approvalTracker.List(),
				restarted.PermissionMode(),
				restarted.PlanState(),
				restarted.permissionRootSessionID,
				restarted.GetWorkingDirectories(),
				restarted.config.ToolSelection,
			},
		)
	}
	if executions.Load() != 1 || model.callCount != 2 {
		t.Fatalf(
			"cold resume executions=%d model_calls=%d",
			executions.Load(),
			model.callCount,
		)
	}
}

func TestP138ProjectGraphCollectsAllDecisionsBeforeToolDispatch(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int32
	for _, name := range []string{"GraphWriteA", "GraphWriteB"} {
		toolName := name
		registry.Register(tools.ToolImpl{
			Info: &schema.ToolInfo{Name: toolName},
			ExecuteCtx: func(context.Context, string) (string, error) {
				executions.Add(1)
				return "ok", nil
			},
		})
	}
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{
						ID:   "multi-a",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "GraphWriteA",
							Arguments: `{}`,
						},
					},
					{
						ID:   "multi-b",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "GraphWriteB",
							Arguments: `{}`,
						},
					},
				},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl-multi",
		model,
		registry,
		&tools.ToolSelection{
			Names: []string{"GraphWriteA", "GraphWriteB"},
		},
	)
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking permission adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	query := NewQueryEngine(config)
	defer query.Close()

	firstEvents, _ := query.SubmitMessage(context.Background(), "run both")
	first := collectGraphHITLEvents(firstEvents)
	if first.request == nil || first.request.ToolUseID != "multi-a" {
		t.Fatalf("first request = %#v", first.request)
	}
	resolveAndClaimGraphDecision(t, query, "multi-a")
	secondEvents, _ := query.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, query),
	)
	second := collectGraphHITLEvents(secondEvents)
	if second.terminal == nil ||
		second.terminal.Reason != TerminalWaitingInput ||
		second.request == nil ||
		second.request.ToolUseID != "multi-b" {
		t.Fatalf(
			"second interrupt request=%#v terminal=%#v",
			second.request,
			second.terminal,
		)
	}
	if executions.Load() != 0 {
		t.Fatalf("partial approval dispatched tools: %d", executions.Load())
	}

	resolveAndClaimGraphDecision(t, query, "multi-b")
	finalEvents, _ := query.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, query),
	)
	final := collectGraphHITLEvents(finalEvents)
	if final.terminal == nil ||
		final.terminal.Reason != TerminalCompleted {
		t.Fatalf("final terminal = %#v", final.terminal)
	}
	if executions.Load() != 2 {
		t.Fatalf("tool executions = %d, want 2", executions.Load())
	}
}

func TestP501ProjectGraphCancelledResumeDoesNotDispatch(t *testing.T) {
	for _, afterFirstSettlement := range []bool{false, true} {
		name := map[bool]string{
			false: "before_first_settlement",
			true:  "after_first_settlement",
		}[afterFirstSettlement]
		t.Run(name, func(t *testing.T) {
			query, executions := newP501ProjectGraphCancellationEngine(t, name)
			initialEvents, _ := query.SubmitMessage(context.Background(), "run both")
			initial := collectGraphHITLEvents(initialEvents)
			if initial.terminal == nil ||
				initial.terminal.Reason != TerminalWaitingInput ||
				initial.request == nil ||
				initial.request.ToolUseID != "cancel-a" {
				t.Fatalf("initial request=%#v terminal=%#v", initial.request, initial.terminal)
			}

			resolveAndClaimGraphDecision(t, query, "cancel-a")
			item := mustClaimGraphDecision(t, query)
			if afterFirstSettlement {
				firstEvents, _ := query.SubmitRuntimeItem(context.Background(), item)
				first := collectGraphHITLEvents(firstEvents)
				if first.terminal == nil ||
					first.terminal.Reason != TerminalWaitingInput ||
					first.request == nil ||
					first.request.ToolUseID != "cancel-b" {
					t.Fatalf("second request=%#v terminal=%#v", first.request, first.terminal)
				}
				if executions.Load() != 0 {
					t.Fatalf("first settlement dispatched %d tools", executions.Load())
				}
				resolveAndClaimGraphDecision(t, query, "cancel-b")
				item = mustClaimGraphDecision(t, query)
			}

			cancelledCtx, cancel := context.WithCancel(context.Background())
			cancel()
			cancelledEvents, _ := query.SubmitRuntimeItem(cancelledCtx, item)
			cancelled := collectGraphHITLEvents(cancelledEvents)
			if cancelled.terminal == nil ||
				cancelled.terminal.Reason != TerminalAbortedStreaming {
				t.Fatalf("cancelled terminal = %#v", cancelled.terminal)
			}
			if executions.Load() != 0 {
				t.Fatalf("cancelled resume dispatched %d tools", executions.Load())
			}
			if _, err := os.Stat(projectGraphCheckpointPath(query.transcript.Path())); !os.IsNotExist(err) {
				t.Fatalf("cancelled checkpoint still exists: %v", err)
			}
			if _, err := os.Stat(filepath.Join(
				query.config.PermissionProjectRoot,
				".claude",
				"settings.local.json",
			)); !os.IsNotExist(err) {
				t.Fatalf("cancelled resume persisted a rule: %v", err)
			}

			lateEvents, _ := query.SubmitRuntimeItem(context.Background(), item)
			late := collectGraphHITLEvents(lateEvents)
			if late.terminal == nil ||
				late.terminal.Reason != TerminalPersistenceError {
				t.Fatalf("late terminal = %#v", late.terminal)
			}
			if executions.Load() != 0 {
				t.Fatalf("late resume dispatched %d tools", executions.Load())
			}
			if queued, ok, err := query.ClaimNextRuntimeItem(); err != nil || ok {
				t.Fatalf("post-cancel queue item=%#v ok=%v err=%v", queued, ok, err)
			}
		})
	}
}

func newP501ProjectGraphCancellationEngine(
	t *testing.T,
	suffix string,
) (*QueryEngine, *atomic.Int32) {
	t.Helper()
	registry := tools.NewRegistry()
	executions := &atomic.Int32{}
	for _, name := range []string{"GraphWriteA", "GraphWriteB"} {
		toolName := name
		registry.Register(tools.ToolImpl{
			Info: &schema.ToolInfo{Name: toolName},
			ExecuteCtx: func(context.Context, string) (string, error) {
				executions.Add(1)
				return "ok", nil
			},
		})
	}
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{
						ID:   "cancel-a",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "GraphWriteA",
							Arguments: `{}`,
						},
					},
					{
						ID:   "cancel-b",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "GraphWriteB",
							Arguments: `{}`,
						},
					},
				},
				ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl-cancel-"+suffix,
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWriteA", "GraphWriteB"}},
	)
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking permission adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	query := NewQueryEngine(config)
	t.Cleanup(query.Close)
	return query, executions
}

func TestProjectGraphHITLExecutionSettlesDistinctExactAlwaysDecisionsInRevisionChain(
	t *testing.T,
) {
	query, toolCtx, requests := newProjectGraphHITLExecutionPermissionTest(
		t,
		[]string{"first.txt", "second.txt"},
	)
	ctx := withProjectGraphHITLExecution(
		context.Background(),
		projectGraphHITLExecutionDecisions(t, query, toolCtx, requests),
	)
	for _, request := range requests {
		allowed, reason := query.resolveProjectGraphHITLPermission(
			ctx,
			request,
			nil,
		)
		if !allowed {
			t.Fatalf("resolve %s allowed=%v reason=%q", request.ToolUseID, allowed, reason)
		}
	}
	assertProjectGraphHITLExactRules(t, query, requests[0], requests[1])
}

func TestProjectGraphHITLExecutionExpiresAfterExternalPolicyDrift(
	t *testing.T,
) {
	query, toolCtx, requests := newProjectGraphHITLExecutionPermissionTest(
		t,
		[]string{"first.txt", "second.txt", "outside.txt"},
	)
	ctx := withProjectGraphHITLExecution(
		context.Background(),
		projectGraphHITLExecutionDecisions(t, query, toolCtx, requests[:2]),
	)
	if allowed, reason := query.resolveProjectGraphHITLPermission(ctx, requests[0], nil); !allowed {
		t.Fatalf("first resolve allowed=%v reason=%q", allowed, reason)
	}
	if err := query.PersistPermissionRule(
		"Read",
		requests[2].Input,
	); err != nil {
		t.Fatalf("persist external rule: %v", err)
	}
	if allowed, reason := query.resolveProjectGraphHITLPermission(ctx, requests[1], nil); allowed ||
		reason != "project graph permission intent expired" {
		t.Fatalf("second resolve allowed=%v reason=%q", allowed, reason)
	}
	assertProjectGraphHITLExactRules(t, query, requests[0], requests[2])
	assertProjectGraphHITLExactRulesAbsent(t, query, requests[1])
}

func TestP501ProjectGraphRejectsPolicyMutationBetweenCheckAndRebuild(t *testing.T) {
	query, toolCtx, requests := newProjectGraphHITLExecutionPermissionTest(
		t,
		[]string{"first.txt", "second.txt", "outside.txt"},
	)
	ctx := withProjectGraphHITLExecution(
		context.Background(),
		projectGraphHITLExecutionDecisions(t, query, toolCtx, requests[:2]),
	)
	execution := projectGraphHITLExecutionFromContext(ctx)
	if execution == nil {
		t.Fatal("project graph HITL execution is missing")
	}
	var persistExternal sync.Once
	execution.afterLivePolicyCheckForTest = func() {
		persistExternal.Do(func() {
			if err := query.PersistPermissionRule("Read", requests[2].Input); err != nil {
				t.Fatalf("persist external rule: %v", err)
			}
		})
	}

	allowed, reason := query.resolveProjectGraphHITLPermission(ctx, requests[0], nil)
	if allowed || reason != "project graph permission intent expired" {
		t.Fatalf("first resolve allowed=%v reason=%q", allowed, reason)
	}
	execution.mu.Lock()
	invalid := execution.invalid
	execution.mu.Unlock()
	if !invalid {
		t.Fatal("externally advanced batch remained valid")
	}
	for range 2 {
		allowed, reason = query.resolveProjectGraphHITLPermission(ctx, requests[1], nil)
		if allowed || reason != "project graph permission intent expired" {
			t.Fatalf("remaining resolve allowed=%v reason=%q", allowed, reason)
		}
	}
	assertProjectGraphHITLExactRules(t, query, requests[2])
	assertProjectGraphHITLExactRulesAbsent(t, query, requests[0])
	assertProjectGraphHITLExactRulesAbsent(t, query, requests[1])
}

func TestP501ProjectGraphConcurrentDecisionsShareOneRevisionChain(t *testing.T) {
	for _, externalDrift := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful_chain", true: "external_drift"}[externalDrift], func(t *testing.T) {
			query, toolCtx, requests := newProjectGraphHITLExecutionPermissionTest(
				t,
				[]string{"first.txt", "second.txt", "outside.txt"},
			)
			ctx := withProjectGraphHITLExecution(
				context.Background(),
				projectGraphHITLExecutionDecisions(t, query, toolCtx, requests[:2]),
			)
			execution := projectGraphHITLExecutionFromContext(ctx)
			firstEntered := make(chan struct{})
			secondEntered := make(chan struct{}, 1)
			releaseFirst := make(chan struct{})
			var calls atomic.Int32
			execution.afterLivePolicyCheckForTest = func() {
				if calls.Add(1) == 1 {
					close(firstEntered)
					<-releaseFirst
					return
				}
				secondEntered <- struct{}{}
			}
			type resolution struct {
				allowed bool
				reason  string
			}
			firstDone := make(chan resolution, 1)
			secondDone := make(chan resolution, 1)
			go func() {
				allowed, reason := query.resolveProjectGraphHITLPermission(ctx, requests[0], nil)
				firstDone <- resolution{allowed, reason}
			}()
			<-firstEntered
			if execution.mu.TryLock() {
				execution.mu.Unlock()
				t.Fatal("first hook did not hold the batch mutex")
			}
			secondStarted := make(chan struct{})
			go func() {
				close(secondStarted)
				allowed, reason := query.resolveProjectGraphHITLPermission(ctx, requests[1], nil)
				secondDone <- resolution{allowed, reason}
			}()
			<-secondStarted
			if externalDrift {
				if err := query.PersistPermissionRule("Read", requests[2].Input); err != nil {
					t.Fatalf("persist external rule: %v", err)
				}
			}
			close(releaseFirst)
			first := <-firstDone
			second := <-secondDone
			if externalDrift {
				if calls.Load() != 1 {
					t.Fatalf("hook calls after invalidation = %d", calls.Load())
				}
				for _, result := range []resolution{first, second} {
					if result.allowed || result.reason != "project graph permission intent expired" {
						t.Fatalf("drift resolution = %#v", result)
					}
				}
				assertProjectGraphHITLExactRules(t, query, requests[2])
				assertProjectGraphHITLExactRulesAbsent(t, query, requests[0])
				assertProjectGraphHITLExactRulesAbsent(t, query, requests[1])
				return
			}
			<-secondEntered
			for _, result := range []resolution{first, second} {
				if !result.allowed {
					t.Fatalf("successful resolution = %#v", result)
				}
			}
			assertProjectGraphHITLExactRules(t, query, requests[0], requests[1])
		})
	}
}

func TestP501ProjectGraphPersistenceFailureDoesNotAdvanceRevision(t *testing.T) {
	query, toolCtx, requests := newProjectGraphHITLExecutionPermissionTest(
		t,
		[]string{"first.txt"},
	)
	if err := os.WriteFile(
		filepath.Join(query.config.PermissionProjectRoot, ".claude"),
		[]byte("not a directory"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	ctx := withProjectGraphHITLExecution(
		context.Background(),
		projectGraphHITLExecutionDecisions(t, query, toolCtx, requests),
	)
	execution := projectGraphHITLExecutionFromContext(ctx)
	execution.mu.Lock()
	initialRevision := execution.currentPolicyRevision
	execution.mu.Unlock()
	for attempt := range 2 {
		allowed, reason := query.resolveProjectGraphHITLPermission(ctx, requests[0], nil)
		if allowed || !strings.HasPrefix(reason, "always-allow failed:") {
			t.Fatalf("attempt %d allowed=%v reason=%q", attempt, allowed, reason)
		}
	}
	execution.mu.Lock()
	currentRevision := execution.currentPolicyRevision
	execution.mu.Unlock()
	if currentRevision != initialRevision {
		t.Fatalf("revision advanced from %q to %q after failed persistence", initialRevision, currentRevision)
	}
	settings, err := os.ReadFile(filepath.Join(query.config.PermissionProjectRoot, ".claude"))
	if err != nil || string(settings) != "not a directory" {
		t.Fatalf("persistence changed .claude: content=%q err=%v", settings, err)
	}
}

func newProjectGraphHITLExecutionPermissionTest(
	t *testing.T,
	names []string,
) (*QueryEngine, *ToolUseContext, []PermissionPromptRequest) {
	t.Helper()
	root := t.TempDir()
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl-execution",
		&canonicalScriptModel{},
		registry,
		&tools.ToolSelection{Names: []string{"Read"}},
	)
	config.CWD = root
	config.PermissionProjectRoot = root
	config.PermissionMode = permission.ModeDefault
	query := NewQueryEngine(config)
	t.Cleanup(query.Close)
	read, ok := registry.Get("Read")
	if !ok || read.Info == nil {
		t.Fatal("Read tool is unavailable")
	}
	toolCtx := &ToolUseContext{
		SessionID: config.SessionID,
		Options: &ToolUseOptions{
			Tools:          []*schema.ToolInfo{read.Info},
			PermissionMode: permission.ModeDefault,
		},
	}
	requests := make([]PermissionPromptRequest, 0, len(names))
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, PermissionPromptRequest{
			ToolName:    "Read",
			ToolUseID:   "graph-hitl-execution-" + name,
			Input:       map[string]any{"file_path": path},
			SessionID:   config.SessionID,
			ToolContext: toolCtx,
		})
	}
	return query, toolCtx, requests
}

func projectGraphHITLExecutionDecisions(
	t *testing.T,
	query *QueryEngine,
	toolCtx *ToolUseContext,
	requests []PermissionPromptRequest,
) []RuntimePermissionDecision {
	t.Helper()
	decisions := make([]RuntimePermissionDecision, 0, len(requests))
	for _, request := range requests {
		schemaDigest, err := projectGraphToolSchemaDigest(
			query.toolRegistry,
			toolCtx.Options.Tools,
			request.ToolName,
		)
		if err != nil {
			t.Fatal(err)
		}
		scope := RuntimeInputScope{SessionID: request.SessionID}
		decisions = append(decisions, RuntimePermissionDecision{
			Version:          projectGraphHITLDecisionVersion,
			RequestID:        request.ToolUseID,
			InvocationDigest: projectGraphInvocationDigest(request, scope, schemaDigest),
			PolicyRevision:   query.projectGraphPolicyRevision(toolCtx),
			Result: PermissionInteractionResult{
				Decision: PermissionAllowAlways,
			},
		})
	}
	return decisions
}

func assertProjectGraphHITLExactRules(
	t *testing.T,
	query *QueryEngine,
	requests ...PermissionPromptRequest,
) {
	t.Helper()
	settings, err := os.ReadFile(
		filepath.Join(query.config.PermissionProjectRoot, ".claude", "settings.local.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		exact, buildErr := permission.BuildExactRuleFromInvocation(
			request.ToolName,
			request.Input,
			query.config.PermissionProjectRoot,
		)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		if strings.Count(string(settings), exact.Value) != 1 {
			t.Fatalf("settings = %s, want exact rule %q once", settings, exact.Value)
		}
	}
}

func assertProjectGraphHITLExactRulesAbsent(
	t *testing.T,
	query *QueryEngine,
	request PermissionPromptRequest,
) {
	t.Helper()
	settings, err := os.ReadFile(
		filepath.Join(query.config.PermissionProjectRoot, ".claude", "settings.local.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	exact, buildErr := permission.BuildExactRuleFromInvocation(
		request.ToolName,
		request.Input,
		query.config.PermissionProjectRoot,
	)
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if strings.Contains(string(settings), exact.Value) {
		t.Fatalf("settings unexpectedly contains expired exact rule %q: %s", exact.Value, settings)
	}
}

func TestP138ProjectGraphPolicyChangeExpiresPriorIntent(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWrite"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "policy-write",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "GraphWrite",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl-policy",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWrite"}},
	)
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking permission adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	query := NewQueryEngine(config)
	defer query.Close()

	initialEvents, _ := query.SubmitMessage(
		context.Background(),
		"change policy before resume",
	)
	initial := collectGraphHITLEvents(initialEvents)
	if initial.request == nil {
		t.Fatal("initial interrupt missing")
	}
	oldRevision := query.projectGraphCheckpoint.envelope.Interrupt.PolicyRevision
	resolveAndClaimGraphDecision(t, query, "policy-write")
	item := mustClaimGraphDecision(t, query)
	if err := query.SetPermissionMode(permission.ModeAcceptEdits); err != nil {
		t.Fatal(err)
	}
	resumeEvents, _ := query.SubmitRuntimeItem(context.Background(), item)
	resumed := collectGraphHITLEvents(resumeEvents)
	if resumed.terminal == nil ||
		resumed.terminal.Reason != TerminalWaitingInput ||
		resumed.request == nil ||
		resumed.request.ToolUseID != "policy-write" {
		t.Fatalf(
			"policy re-prompt request=%#v terminal=%#v",
			resumed.request,
			resumed.terminal,
		)
	}
	active, ok := query.projectGraphCheckpoint.ActiveInterrupt()
	if !ok || active.PolicyRevision == oldRevision {
		t.Fatalf(
			"policy revision did not rotate: old=%q active=%#v",
			oldRevision,
			active,
		)
	}
	if executions.Load() != 0 {
		t.Fatalf("expired intent dispatched the tool: %d", executions.Load())
	}
}

func TestP138ProjectGraphSchemaChangeExpiresPriorIntent(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int32
	toolInfo := &schema.ToolInfo{
		Name: "GraphWrite",
		Desc: "schema revision one",
	}
	registry.Register(tools.ToolImpl{
		Info: toolInfo,
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "schema-write",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "GraphWrite",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl-schema",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWrite"}},
	)
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking permission adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	query := NewQueryEngine(config)
	defer query.Close()

	initialEvents, _ := query.SubmitMessage(
		context.Background(),
		"change schema before resume",
	)
	initial := collectGraphHITLEvents(initialEvents)
	if initial.request == nil {
		t.Fatal("initial interrupt missing")
	}
	resolveAndClaimGraphDecision(t, query, "schema-write")
	item := mustClaimGraphDecision(t, query)
	toolInfo.Desc = "schema revision two"
	resumeEvents, _ := query.SubmitRuntimeItem(context.Background(), item)
	resumed := collectGraphHITLEvents(resumeEvents)
	if resumed.terminal == nil ||
		resumed.terminal.Reason != TerminalWaitingInput ||
		resumed.request == nil ||
		resumed.request.ToolUseID != "schema-write" {
		t.Fatalf(
			"schema re-prompt request=%#v terminal=%#v",
			resumed.request,
			resumed.terminal,
		)
	}
	if executions.Load() != 0 {
		t.Fatalf("expired schema intent dispatched the tool: %d", executions.Load())
	}
}

func TestP138ProjectGraphCheckpointEnvelopeFailsClosed(t *testing.T) {
	scope := RuntimeInputScope{
		SessionID: "graph-checkpoint-session",
		ThreadID:  "graph-checkpoint-thread",
	}
	request := projectGraphHITLRequest{
		Version:          projectGraphHITLRequestVersion,
		RequestID:        "checkpoint-write",
		InterruptID:      "interrupt-1",
		InvocationDigest: strings.Repeat("a", 64),
		PolicyRevision:   strings.Repeat("b", 64),
		ToolName:         "GraphWrite",
		Scope:            scope,
		Kind:             "permission",
	}

	t.Run("protected durable sidecar", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint.json")
		store, err := newProjectGraphCheckpointStore(path, scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Set(
			context.Background(),
			store.checkpointID,
			[]byte("opaque-eino-state"),
		); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkInterrupt(
			context.Background(),
			request,
		); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("checkpoint mode = %#o, want 0600", mode)
		}
		if _, err := newProjectGraphCheckpointStore(
			path,
			scope,
			nil,
		); err != nil {
			t.Fatalf("reload valid checkpoint: %v", err)
		}
	})

	t.Run("corrupted JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint.json")
		if err := os.WriteFile(
			path,
			[]byte(`{"version":`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := newProjectGraphCheckpointStore(
			path,
			scope,
			nil,
		); err == nil ||
			!strings.Contains(err.Error(), "decode project graph checkpoint") {
			t.Fatalf("corrupted checkpoint error = %v", err)
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint.json")
		envelope := projectGraphCheckpointEnvelope{
			Version:       projectGraphCheckpointEnvelopeVersion + 1,
			CheckpointID:  projectGraphCheckpointID(scope),
			KernelVersion: queryKernelVersionProjectGraph,
			Scope:         scope,
			Opaque:        []byte("opaque-eino-state"),
			Interrupt:     &request,
		}
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newProjectGraphCheckpointStore(
			path,
			scope,
			nil,
		); err == nil ||
			!strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsupported checkpoint error = %v", err)
		}
	})

	t.Run("scope identity mismatch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checkpoint.json")
		store, err := newProjectGraphCheckpointStore(path, scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Set(
			context.Background(),
			store.checkpointID,
			[]byte("opaque-eino-state"),
		); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkInterrupt(
			context.Background(),
			request,
		); err != nil {
			t.Fatal(err)
		}
		otherScope := scope
		otherScope.SessionID = "other-session"
		if _, err := newProjectGraphCheckpointStore(
			path,
			otherScope,
			nil,
		); err == nil ||
			!strings.Contains(err.Error(), "identity mismatch") {
			t.Fatalf("mismatched checkpoint error = %v", err)
		}
	})
}

func TestP138ProjectGraphUsesLiveExternalDecisionOwner(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int32
	var decisions atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWrite"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "external-decision-write",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "GraphWrite",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl-external-decision",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWrite"}},
	)
	config.CanUseTool = func(
		context.Context,
		string,
		map[string]any,
		*ToolUseContext,
	) (bool, string) {
		decisions.Add(1)
		return false, "live external policy denied"
	}
	query := NewQueryEngine(config)
	defer query.Close()

	events, _ := query.SubmitMessage(
		context.Background(),
		"respect the live external owner",
	)
	result := collectGraphHITLEvents(events)
	if result.request != nil {
		t.Fatalf(
			"external decision was converted to Graph HITL: %#v",
			result.request,
		)
	}
	if result.terminal == nil ||
		result.terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", result.terminal)
	}
	if decisions.Load() != 1 || executions.Load() != 0 {
		t.Fatalf(
			"live decisions=%d executions=%d",
			decisions.Load(),
			executions.Load(),
		)
	}
}

func TestP138ProjectGraphColdRestartReloadsProjectShellHooks(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWrite"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "hook-reload-write",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "GraphWrite",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	transcriptDir := t.TempDir()
	config := projectGraphEngineConfig(
		t,
		transcriptDir,
		"graph-hitl-hook-reload",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWrite"}},
	)
	hookDir := filepath.Join(config.CWD, ".claude")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookConfig := []byte(
		`{"PreToolUse":[{"matcher":"GraphWrite","hooks":[` +
			`{"command":"printf 'blocked by resumed project hook' >&2; exit 2"}` +
			`]}]}`,
	)
	if err := os.WriteFile(
		filepath.Join(hookDir, "hooks.json"),
		hookConfig,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking permission adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	first := NewQueryEngine(config)
	initialEvents, _ := first.SubmitMessage(
		context.Background(),
		"persist a hook-sensitive interrupt",
	)
	initial := collectGraphHITLEvents(initialEvents)
	if initial.request == nil ||
		initial.terminal == nil ||
		initial.terminal.Reason != TerminalWaitingInput {
		t.Fatalf(
			"initial request=%#v terminal=%#v",
			initial.request,
			initial.terminal,
		)
	}
	first.Close()

	hostConfig := config
	hostConfig.SessionID = "graph-hitl-hook-host"
	hostConfig.ThreadID = ""
	hostConfig.CWD = t.TempDir()
	hostConfig.PermissionProjectRoot = ""
	// This restart also owns a distinct process policy root.
	hostConfig.MCPManager = tools.NewMCPToolManager()
	host := NewQueryEngine(hostConfig)
	defer host.Close()
	if _, err := host.ResumeSession(
		context.Background(),
		"graph-hitl-hook-reload",
	); err != nil {
		t.Fatal(err)
	}
	if !host.ResolvePermissionInteraction(
		"hook-reload-write",
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	) {
		t.Fatal("restored decision was not accepted")
	}
	events, _ := host.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, host),
	)
	resumed := collectGraphHITLEvents(events)
	if resumed.terminal == nil ||
		resumed.terminal.Reason != TerminalCompleted {
		t.Fatalf("resumed terminal = %#v", resumed.terminal)
	}
	if executions.Load() != 0 {
		t.Fatalf("resumed project hook did not block execution: %d", executions.Load())
	}
	if resumed.toolResult == nil ||
		!strings.Contains(
			resumed.toolResult.Content,
			"blocked by resumed project hook",
		) {
		t.Fatalf("hook-denied result = %#v", resumed.toolResult)
	}
}

func TestP138ProjectGraphQuestionResumeAppliesUpdatedInput(t *testing.T) {
	registry := tools.NewRegistry()
	var executedInput string
	registry.Register(tools.ToolImpl{
		Info:       &schema.ToolInfo{Name: "AskUserQuestion"},
		IsReadOnly: true,
		ExecuteCtx: func(_ context.Context, input string) (string, error) {
			executedInput = input
			return "answered", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "question-1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "AskUserQuestion",
						Arguments: `{"question":"Choose","answer":""}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
				},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"graph-hitl-question",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"AskUserQuestion"}},
	)
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking question adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	query := NewQueryEngine(config)
	defer query.Close()
	initialEvents, _ := query.SubmitMessage(context.Background(), "ask me")
	initial := collectGraphHITLEvents(initialEvents)
	if initial.request == nil ||
		initial.request.ToolUseID != "question-1" {
		t.Fatalf("question interrupt = %#v", initial.request)
	}
	if !query.ResolvePermissionInteraction(
		"question-1",
		PermissionInteractionResult{
			Decision: PermissionAllowOnce,
			UpdatedInput: map[string]any{
				"question": "Choose",
				"answer":   "A",
			},
		},
	) {
		t.Fatal("question decision was not accepted")
	}
	events, _ := query.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, query),
	)
	resumed := collectGraphHITLEvents(events)
	if resumed.terminal == nil ||
		resumed.terminal.Reason != TerminalCompleted {
		t.Fatalf("question terminal = %#v", resumed.terminal)
	}
	if !strings.Contains(executedInput, `"answer":"A"`) {
		t.Fatalf("updated question input was not executed: %s", executedInput)
	}
}

func TestP138ProjectGraphPlanApprovalCommitsAfterTargetedResume(t *testing.T) {
	p200PreparePlan(t, "graph-hitl-plan", "", "# Plan")
	var executions atomic.Int32
	registry := p170PlanRegistry(&executions)
	registry.Register(planExitTestTool(&executions))
	config := QueryEngineConfig{
		SessionID:      "graph-hitl-plan",
		ThreadID:       "graph-hitl-plan",
		TranscriptDir:  t.TempDir(),
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
			p135c2ToolCall("exit-graph-plan", "ExitPlanMode", `{}`),
		}},
		ToolRegistry: registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"ExitPlanMode"},
		},
		MaxTurns: 2,
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			t.Fatal("ProjectGraph must not call the blocking Plan adapter")
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	}
	query := NewQueryEngine(config)
	defer query.Close()
	if query.queryKernelSelection.kernel.kind() != queryKernelProjectGraph {
		t.Fatalf("kernel selection = %#v", query.queryKernelSelection)
	}

	initialEvents, _ := query.SubmitMessage(
		context.Background(),
		"approve graph Plan",
	)
	initial := collectGraphHITLEvents(initialEvents)
	if initial.terminal == nil ||
		initial.terminal.Reason != TerminalWaitingInput ||
		initial.request == nil ||
		initial.request.PlanApproval == nil {
		t.Fatalf(
			"Plan interrupt request=%#v terminal=%#v",
			initial.request,
			initial.terminal,
		)
	}
	if query.PlanState().Phase != PlanPhaseAwaitingApproval ||
		executions.Load() != 0 {
		t.Fatalf(
			"pre-resume Plan state=%#v executions=%d",
			query.PlanState(),
			executions.Load(),
		)
	}
	if !query.ResolvePermissionInteraction(
		initial.request.ToolUseID,
		approvedPlanInteraction(
			initial.request.PlanApproval,
			permission.ModeBypassPermissions,
		),
	) {
		t.Fatal("Plan decision was not accepted")
	}
	events, _ := query.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, query),
	)
	resumed := collectGraphHITLEvents(events)
	if resumed.terminal == nil ||
		resumed.terminal.Reason != TerminalCompleted {
		t.Fatalf("Plan resume terminal = %#v", resumed.terminal)
	}
	if executions.Load() != 1 ||
		query.PlanState().Phase != PlanPhaseInactive ||
		query.PermissionMode() != permission.ModeBypassPermissions {
		t.Fatalf(
			"final Plan state=%#v mode=%q executions=%d",
			query.PlanState(),
			query.PermissionMode(),
			executions.Load(),
		)
	}
	if err := query.SetPermissionMode(permission.ModePlan); err != nil {
		t.Fatalf("re-enter Plan externally: %v", err)
	}
	if err := query.SetPermissionModeConfirmed(
		permission.ModeBypassPermissions,
		true,
	); err != nil {
		t.Fatalf("leave Plan externally: %v", err)
	}
	thread := query.RuntimeSnapshot().Threads["graph-hitl-plan"]
	if thread.Status != RuntimeThreadCompleted ||
		thread.ActiveTurnID != "" ||
		!thread.ActiveSince.IsZero() {
		t.Fatalf(
			"confirmed bypass seized idle runtime thread: %#v",
			thread,
		)
	}

	nextEvents, _ := query.SubmitMessage(
		context.Background(),
		"continue after graph Plan approval",
	)
	next := collectGraphHITLEvents(nextEvents)
	if next.terminal == nil ||
		next.terminal.Reason != TerminalCompleted {
		t.Fatalf(
			"post-Plan turn terminal=%#v types=%#v runtimeErr=%v state=%#v",
			next.terminal,
			next.types,
			query.RuntimeStateError(),
			query.RuntimeSnapshot().Threads["graph-hitl-plan"],
		)
	}
	if err := query.RuntimeStateError(); err != nil {
		t.Fatalf("post-Plan turn runtime reducer error: %v", err)
	}
}

func TestP138ProjectGraphPlanApprovalPolicyDriftExpiresBeforeSettlement(t *testing.T) {
	p200PreparePlan(t, "graph-hitl-plan-drift", "", "# Plan")
	var executions atomic.Int32
	registry := p170PlanRegistry(&executions)
	registry.Register(planExitTestTool(&executions))
	config := QueryEngineConfig{
		SessionID:      "graph-hitl-plan-drift",
		ThreadID:       "graph-hitl-plan-drift",
		TranscriptDir:  t.TempDir(),
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
			p135c2ToolCall("exit-graph-plan-drift", "ExitPlanMode", `{}`),
		}},
		ToolRegistry: registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"ExitPlanMode"},
		},
		MaxTurns: 2,
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			t.Fatal("ProjectGraph must not call the blocking Plan adapter")
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	}
	query := NewQueryEngine(config)
	defer query.Close()

	initialEvents, _ := query.SubmitMessage(context.Background(), "approve graph Plan")
	initial := collectGraphHITLEvents(initialEvents)
	if initial.request == nil || initial.request.PlanApproval == nil {
		t.Fatalf("Plan interrupt request = %#v", initial.request)
	}
	if !query.ResolvePermissionInteraction(
		initial.request.ToolUseID,
		approvedPlanInteraction(initial.request.PlanApproval, permission.ModeBypassPermissions),
	) {
		t.Fatal("Plan decision was not accepted")
	}
	item := mustClaimGraphDecision(t, query)
	query.mu.Lock()
	query.config.AdditionalDirs = append(
		query.config.AdditionalDirs,
		filepath.Join(config.CWD, "external-policy-drift"),
	)
	query.mu.Unlock()

	resumedEvents, _ := query.SubmitRuntimeItem(context.Background(), item)
	resumed := collectGraphHITLEvents(resumedEvents)
	if resumed.terminal == nil ||
		resumed.terminal.Reason != TerminalWaitingInput ||
		resumed.request == nil ||
		resumed.request.ToolUseID != initial.request.ToolUseID ||
		resumed.request.PlanApproval == nil {
		t.Fatalf(
			"expired Plan resume request=%#v terminal=%#v",
			resumed.request,
			resumed.terminal,
		)
	}
	if executions.Load() != 0 {
		t.Fatalf("expired Plan decision dispatched: %d", executions.Load())
	}
}

func resolveAndClaimGraphDecision(
	t *testing.T,
	query *QueryEngine,
	requestID string,
) {
	t.Helper()
	if !query.ResolvePermissionInteraction(
		requestID,
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	) {
		t.Fatalf("resolve %s failed", requestID)
	}
}

func mustClaimGraphDecision(
	t *testing.T,
	query *QueryEngine,
) RuntimeItem {
	t.Helper()
	item, ok, err := query.ClaimNextRuntimeItem()
	if err != nil || !ok ||
		item.Kind != RuntimeItemPermissionDecision {
		t.Fatalf("claim graph decision: item=%#v ok=%v err=%v", item, ok, err)
	}
	return item
}

type graphHITLCollectedEvents struct {
	request    *PermissionRequestEvent
	resolved   *PermissionResolvedEvent
	toolResult *schema.Message
	terminal   *Terminal
	types      []QueryEventType
}

func collectGraphHITLEvents(
	events <-chan QueryEvent,
) graphHITLCollectedEvents {
	var collected graphHITLCollectedEvents
	for event := range events {
		collected.types = append(collected.types, event.Type)
		switch event.Type {
		case EventPermissionRequest:
			collected.request = event.PermissionRequest
		case EventPermissionResolved:
			collected.resolved = event.PermissionResolved
		case EventToolResult:
			collected.toolResult = event.ToolResultMessage
		case EventTerminal:
			collected.terminal = event.TerminalInfo
		}
	}
	return collected
}

func TestP200ProjectGraphPlanIdentityRetainsInitialDigest(t *testing.T) {
	scope := RuntimeInputScope{
		SessionID: "plan-session",
		ThreadID:  "plan-thread",
	}
	initialDigest := PlanBytesDigest([]byte("# Initial Plan"))
	approval := &PlanApprovalRequest{
		RequestID:         "exit-plan",
		PlanRevision:      5,
		PlanFileIdentity:  "/tmp/plan.md",
		InitialPlanDigest: initialDigest,
		ReturnMode:        permission.ModeDontAsk,
	}
	prompt := PermissionPromptRequest{
		ToolName:     "ExitPlanMode",
		ToolUseID:    "exit-plan",
		PlanApproval: approval,
	}
	invocationDigest := projectGraphInvocationDigest(
		prompt,
		scope,
		"tool-schema",
	)
	request := projectGraphHITLRequest{
		Version:          projectGraphHITLRequestVersion,
		RequestID:        "exit-plan",
		InterruptID:      "interrupt-plan",
		InvocationDigest: invocationDigest,
		PolicyRevision:   "policy",
		ToolName:         "ExitPlanMode",
		Scope:            scope,
		Kind:             "plan_approval",
		PlanApproval:     approval,
	}
	if err := validateProjectGraphHITLRequest(request, true); err != nil {
		t.Fatalf("valid Plan HITL identity: %v", err)
	}
	cloned := cloneProjectGraphHITLRequest(request)
	if cloned.PlanApproval == approval ||
		cloned.PlanApproval.InitialPlanDigest != initialDigest ||
		cloned.InvocationDigest != invocationDigest {
		t.Fatalf("cloned Plan HITL identity = %#v", cloned)
	}

	changedPrompt := prompt
	changedApproval := *approval
	changedApproval.InitialPlanDigest = PlanBytesDigest([]byte("# Changed Plan"))
	changedPrompt.PlanApproval = &changedApproval
	if projectGraphInvocationDigest(changedPrompt, scope, "tool-schema") ==
		invocationDigest {
		t.Fatal("Plan initial digest did not participate in invocation identity")
	}

	request.PlanApproval = &changedApproval
	request.PlanApproval.InitialPlanDigest = ""
	if err := validateProjectGraphHITLRequest(request, true); err == nil ||
		!strings.Contains(err.Error(), "Plan approval identity") {
		t.Fatalf("missing Plan digest validation error = %v", err)
	}
}

func TestP512ProjectGraphConstraintParticipatesInIdentityAndValidation(t *testing.T) {
	scope := RuntimeInputScope{SessionID: "constraint-session"}
	request := PermissionPromptRequest{ToolName: "Bash", ToolUseID: "constraint-call", DecisionConstraint: PermissionAllowOnceOnly}
	if projectGraphInvocationDigest(request, scope, "schema") == projectGraphInvocationDigest(PermissionPromptRequest{ToolName: "Bash", ToolUseID: "constraint-call"}, scope, "schema") {
		t.Fatal("permission decision constraint did not participate in ProjectGraph identity")
	}
	durable := projectGraphHITLRequest{Version: projectGraphHITLRequestVersion, RequestID: "constraint-call", InterruptID: "interrupt", InvocationDigest: projectGraphInvocationDigest(request, scope, "schema"), PolicyRevision: "policy", ToolName: "Bash", Scope: scope, Kind: "permission", DecisionConstraint: PermissionDecisionConstraint("invalid")}
	if err := validateProjectGraphHITLRequest(durable, true); err == nil {
		t.Fatal("invalid decision constraint was accepted")
	}
	durable.DecisionConstraint = PermissionAllowOnceOnly
	if err := validateProjectGraphResumeDecision(durable, RuntimePermissionDecision{Version: projectGraphHITLDecisionVersion, RequestID: durable.RequestID, InterruptID: durable.InterruptID, InvocationDigest: durable.InvocationDigest, PolicyRevision: durable.PolicyRevision, DecisionConstraint: PermissionAllowOnceOnly, Result: PermissionInteractionResult{Decision: PermissionAllowAlways}}); err == nil {
		t.Fatal("forged persistent ProjectGraph decision was accepted")
	}
}

func TestP512ProjectGraphFirstInterruptProjectsConstraint(t *testing.T) {
	request := PermissionPromptRequest{
		ToolName: "Bash", ToolUseID: "critical-call",
		Input:     map[string]any{"command": "rm -rf /"},
		SessionID: "critical-session", ThreadID: "critical-thread",
		DecisionConstraint: PermissionAllowOnceOnly,
	}
	scope := RuntimeInputScope{SessionID: request.SessionID, ThreadID: request.ThreadID}
	durable := projectGraphHITLRequest{
		Version: projectGraphHITLRequestVersion, RequestID: request.ToolUseID,
		InterruptID:      "critical-interrupt",
		InvocationDigest: projectGraphInvocationDigest(request, scope, "schema"),
		PolicyRevision:   "policy", ToolName: request.ToolName,
		Input: request.Input, Scope: scope, Kind: "permission",
		DecisionConstraint: PermissionAllowOnceOnly,
	}
	info := &compose.InterruptInfo{InterruptContexts: []*compose.InterruptCtx{{
		ID: durable.InterruptID, IsRootCause: true,
		Info: projectGraphHITLInterruptInfo{Request: durable},
	}}}
	projected, err := projectGraphRootInterrupt(info)
	if err != nil {
		t.Fatal(err)
	}
	if projected.DecisionConstraint != PermissionAllowOnceOnly {
		t.Fatalf("first interrupt constraint = %q", projected.DecisionConstraint)
	}
}

func TestP512ProjectGraphResolveRewritesForgedPersistentDecision(t *testing.T) {
	scope := RuntimeInputScope{SessionID: "constraint-session"}
	checkpoint, err := newProjectGraphCheckpointStore("", scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.envelope.Opaque = []byte("opaque")
	request := projectGraphHITLRequest{
		Version: projectGraphHITLRequestVersion, RequestID: "constraint-call",
		InterruptID: "constraint-interrupt", InvocationDigest: "digest",
		PolicyRevision: "policy", ToolName: "Bash", Scope: scope,
		Kind: "permission", DecisionConstraint: PermissionAllowOnceOnly,
	}
	if err := checkpoint.MarkInterrupt(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{SessionID: scope.SessionID},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := &QueryEngine{projectGraphCheckpoint: checkpoint, inputCoordinator: coordinator}
	if !engine.ResolvePermissionInteraction(
		request.RequestID,
		PermissionInteractionResult{Decision: PermissionAllowAlways},
	) {
		t.Fatal("forged decision was not durably rewritten")
	}
	items := coordinator.Snapshot(scope)
	if len(items) != 1 || items[0].PermissionDecision == nil ||
		items[0].PermissionDecision.Result.Decision != PermissionDeny ||
		!strings.Contains(items[0].PermissionDecision.Result.Message, "constraint") {
		t.Fatalf("rewritten runtime items = %#v", items)
	}
}

func TestP512ProjectGraphColdRestartRetainsAllowOnceConstraint(t *testing.T) {
	scope := RuntimeInputScope{SessionID: "cold-constraint-session", ThreadID: "cold-constraint-thread"}
	checkpoint, err := newProjectGraphCheckpointStore("", scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.envelope.Opaque = []byte("opaque-checkpoint")
	request := projectGraphHITLRequest{
		Version: projectGraphHITLRequestVersion, RequestID: "cold-critical-call",
		InterruptID: "cold-critical-interrupt", InvocationDigest: "cold-invocation",
		PolicyRevision: "cold-policy", ToolName: "Bash", CanonicalToolName: "Bash",
		Scope: scope, Kind: PermissionInteractionKindPermission,
		DecisionConstraint: PermissionAllowOnceOnly,
		Presentation: &PermissionPresentation{
			Version: 1, ToolLabel: "Bash", Summary: "Allow this tool action?",
			Evidence:    []PermissionPresentationEvidence{{Label: "Access", Value: "May make destructive changes"}},
			GrantScopes: []PermissionInteractionDecision{PermissionAllowOnce},
		},
	}
	if err := checkpoint.MarkInterrupt(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(checkpoint.envelope)
	if err != nil {
		t.Fatal(err)
	}
	var restoredEnvelope projectGraphCheckpointEnvelope
	if err := json.Unmarshal(encoded, &restoredEnvelope); err != nil {
		t.Fatal(err)
	}
	restoredStore := &projectGraphCheckpointStore{envelope: restoredEnvelope}
	coordinator, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{SessionID: scope.SessionID, ThreadID: scope.ThreadID},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	restored := &QueryEngine{
		projectGraphCheckpoint: restoredStore,
		inputCoordinator:       coordinator,
	}
	pending, ok := restored.PendingProjectGraphPermissionRequest()
	if !ok || pending.DecisionConstraint != PermissionAllowOnceOnly ||
		pending.Presentation == nil || len(pending.Presentation.GrantScopes) != 1 ||
		pending.Presentation.GrantScopes[0] != PermissionAllowOnce {
		t.Fatalf("cold pending request = %#v, ok=%v", pending, ok)
	}
	if !restored.ResolvePermissionInteraction(
		request.RequestID,
		PermissionInteractionResult{Decision: PermissionAllowAlways},
	) {
		t.Fatal("cold forged persistent decision was not durably rewritten")
	}
	items := coordinator.Snapshot(scope)
	if len(items) != 1 || items[0].PermissionDecision == nil ||
		items[0].PermissionDecision.Result.Decision != PermissionDeny {
		t.Fatalf("cold rewritten runtime items = %#v", items)
	}
}
