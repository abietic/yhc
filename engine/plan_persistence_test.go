package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

type p172NoDispatchModel struct {
	mu    sync.Mutex
	calls int
}

func (m *p172NoDispatchModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return &schema.Message{Role: schema.Assistant, Content: "unexpected"}, nil
}

func (m *p172NoDispatchModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "unexpected",
	}}), nil
}

func (m *p172NoDispatchModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestP172CheckpointPersistsVersionedPlanStateWithoutApprovalAuthority(
	t *testing.T,
) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "session", ThreadID: "thread", CWD: cwd,
		TranscriptDir: dir, PermissionMode: permission.ModePlan,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "plan"},
		{Role: schema.Assistant, Content: "draft"},
	})

	eng.planMu.Lock()
	eng.planState = PlanState{
		Phase:                 PlanPhaseAwaitingApproval,
		PlanFileIdentity:      filepath.Join(cwd, ".claude", "plans", "session.md"),
		ReturnMode:            permission.ModeAcceptEdits,
		ApprovalRequestID:     "exit-1",
		ApprovalInitialDigest: PlanBytesDigest([]byte("# Plan")),
		Revision:              7,
	}
	eng.planMu.Unlock()
	if err := eng.persistSessionCheckpoint("waiting_input"); err != nil {
		t.Fatal(err)
	}

	loaded, err := transcript.NewRecorder("session", dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	meta := session.ReadSessionMetadataFull(loaded)
	if meta == nil || meta.PlanState == nil {
		t.Fatal("versioned Plan checkpoint missing")
	}
	if meta.PlanState.Version != session.PersistedPlanStateVersion ||
		meta.PlanState.Phase != string(PlanPhaseAwaitingApproval) ||
		meta.PlanState.ApprovalRequestID != "exit-1" ||
		meta.PlanState.ApprovalInitialDigest !=
			PlanBytesDigest([]byte("# Plan")) ||
		meta.PlanState.Revision != 7 ||
		meta.PlanState.ReturnMode != string(permission.ModeAcceptEdits) {
		t.Fatalf("persisted Plan state = %#v", meta.PlanState)
	}
	data, err := os.ReadFile(transcript.NewRecorder("session", dir).Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"callback", "response_channel", "approval_grant"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("checkpoint persisted non-durable approval authority %q", forbidden)
		}
	}
}

func TestP172ColdResumeRestoresActivePlanAndExactIdentityWithoutDispatch(
	t *testing.T,
) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	oldHome := p17H0RealTempDir(t)
	planPath := filepath.Join(oldHome, ".claude", "plans", "selected.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("1. keep exact identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := writeP172Session(t, dir, cwd, "selected", &session.PersistedPlanState{
		Version:          session.PersistedPlanStateVersion,
		Phase:            string(PlanPhaseActive),
		PlanFileIdentity: planPath,
		ReturnMode:       string(permission.ModeAcceptEdits),
		Revision:         7,
	}, string(permission.ModeDefault))
	t.Setenv("HOME", t.TempDir())
	noDispatch := &p172NoDispatchModel{}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: cwd, TranscriptDir: dir,
		ChatModel: noDispatch,
	})
	t.Cleanup(eng.Close)

	resumed, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	state := eng.PlanState()
	if state.Phase != PlanPhaseActive ||
		state.PlanFileIdentity != planPath ||
		state.ReturnMode != permission.ModeAcceptEdits ||
		state.Revision != 7 ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("restored Plan state = %#v mode=%q", state, eng.PermissionMode())
	}
	if noDispatch.CallCount() != 0 {
		t.Fatalf("resume dispatched model %d time(s)", noDispatch.CallCount())
	}
	if !containsSessionWarning(
		resumed.Warnings,
		"restored Plan permission mode",
	) {
		t.Fatalf("mode repair warning missing: %#v", resumed.Warnings)
	}
	snapshot := eng.RuntimeSnapshot()
	thread := snapshot.Threads["selected"]
	if thread.Plan == nil ||
		thread.Plan.Phase != PlanPhaseActive ||
		thread.Plan.PlanFileIdentity != planPath ||
		thread.Plan.Revision != 7 {
		t.Fatalf("runtime Plan projection = %#v", thread.Plan)
	}

	allowed := evaluateToolContextPlanPolicy(&ToolUseContext{
		SessionID: "selected",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModePlan,
			PlanFilePath:   planPath,
		},
	}, nil, "Write", map[string]any{"file_path": planPath})
	if !allowed.Allowed {
		t.Fatalf("restored exact Plan path denied: %#v", allowed)
	}
}

func TestP172ColdAwaitingApprovalNormalizesAndInvalidatesOldRequest(
	t *testing.T,
) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	planPath := filepath.Join(
		p17H0RealTempDir(t),
		".claude",
		"plans",
		"selected.md",
	)
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	recorder := writeP172Session(t, dir, cwd, "selected", &session.PersistedPlanState{
		Version:           session.PersistedPlanStateVersion,
		Phase:             string(PlanPhaseAwaitingApproval),
		PlanFileIdentity:  planPath,
		ReturnMode:        string(permission.ModeBypassPermissions),
		ApprovalRequestID: "exit-old",
		Revision:          9,
	}, string(permission.ModePlan))
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: cwd, TranscriptDir: dir,
	})
	t.Cleanup(eng.Close)

	resumed, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	state := eng.PlanState()
	if state.Phase != PlanPhaseActive ||
		state.ApprovalRequestID != "" ||
		state.Revision != 10 ||
		state.ReturnMode != permission.ModeBypassPermissions {
		t.Fatalf("normalized Plan state = %#v", state)
	}
	if len(resumed.ActionableRequestIDs) != 0 ||
		!containsSessionWarning(
			resumed.Warnings,
			"normalized persisted AwaitingApproval to Active",
		) {
		t.Fatalf("cold normalization warnings=%#v actionable=%#v", resumed.Warnings, resumed.ActionableRequestIDs)
	}
	result := eng.settlePlanApproval(
		&PlanApprovalRequest{
			RequestID: "exit-old", PlanRevision: 9,
			PlanFileIdentity: planPath,
			ReturnMode:       permission.ModeBypassPermissions,
		},
		PermissionInteractionResult{
			Decision: PermissionAllowOnce,
			PlanApproval: &PlanApprovalDecision{
				RequestID: "exit-old", PlanRevision: 9,
				Outcome: PlanApprovalApprove, Confirmed: true,
				TargetMode: permission.ModeBypassPermissions,
			},
		},
		nil,
	)
	if result.Decision != PermissionDeny ||
		!strings.Contains(result.Message, "stale") ||
		eng.PlanState() != state {
		t.Fatalf("old approval reused: result=%#v state=%#v", result, eng.PlanState())
	}

	reloaded, err := transcript.NewRecorder("selected", dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	meta := session.ReadSessionMetadataFull(reloaded)
	if meta == nil || meta.PlanState == nil ||
		meta.PlanState.Phase != string(PlanPhaseActive) ||
		meta.PlanState.ApprovalRequestID != "" ||
		meta.PlanState.Revision != 10 {
		t.Fatalf("normalized checkpoint = %#v", meta)
	}
}

func TestP172SameProcessExactLiveApprovalIsReprojectedWithoutDuplication(
	t *testing.T,
) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	planPath := filepath.Join(
		p17H0RealTempDir(t),
		".claude",
		"plans",
		"selected.md",
	)
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	planBytes := []byte("# Plan\n")
	if err := os.WriteFile(planPath, planBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	planDigest := PlanBytesDigest(planBytes)
	record := &session.PersistedPlanState{
		Version:               session.PersistedPlanStateVersion,
		Phase:                 string(PlanPhaseAwaitingApproval),
		PlanFileIdentity:      planPath,
		ReturnMode:            string(permission.ModeAcceptEdits),
		ApprovalRequestID:     "exit-live",
		ApprovalInitialDigest: planDigest,
		Revision:              4,
	}
	recorder := transcript.NewRecorder("selected", dir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: "prompt"},
		{Role: schema.Assistant, Content: "response"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected", CWD: cwd,
		PermissionMode: string(permission.ModePlan), PlanState: record,
		PendingRequestIDs: []string{"exit-live"},
		CreatedAt:         now, UpdatedAt: now, MessageCount: 2,
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStore()
	if err := store.Apply(QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: "selected", ThreadID: "selected",
			TurnID: "turn-plan", Sequence: 1, Timestamp: now,
		},
		Type: EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{
			ToolName: "ExitPlanMode", ToolUseID: "exit-live",
			Kind: "plan_approval",
			PlanApproval: &PlanApprovalRequest{
				RequestID:         "exit-live",
				PlanRevision:      4,
				PlanFileIdentity:  planPath,
				InitialPlanDigest: planDigest,
				ReturnMode:        permission.ModeAcceptEdits,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: cwd, TranscriptDir: dir,
		RuntimeState: store,
	})
	t.Cleanup(eng.Close)
	liveKey := permissionRequestKey{
		engineID: eng.permissionEngineID, toolUseID: "exit-live",
	}
	eng.permissionCoordinator.mu.Lock()
	eng.permissionCoordinator.pending[liveKey] = &permissionPendingRequest{
		request: PermissionPromptRequest{
			ToolName: "ExitPlanMode", ToolUseID: "exit-live",
			SessionID: "selected", ThreadID: "selected",
			PlanApproval: &PlanApprovalRequest{
				RequestID:         "exit-live",
				PlanRevision:      4,
				PlanFileIdentity:  planPath,
				InitialPlanDigest: planDigest,
				ReturnMode:        permission.ModeAcceptEdits,
			},
		},
	}
	eng.permissionCoordinator.mu.Unlock()
	t.Cleanup(func() {
		eng.permissionCoordinator.mu.Lock()
		delete(eng.permissionCoordinator.pending, liveKey)
		eng.permissionCoordinator.mu.Unlock()
	})

	resumed, err := eng.ResumeSession(t.Context(), "selected")
	if err != nil {
		t.Fatal(err)
	}
	state := eng.PlanState()
	if state.Phase != PlanPhaseAwaitingApproval ||
		state.ApprovalRequestID != "exit-live" ||
		state.Revision != 4 ||
		len(resumed.ActionableRequestIDs) != 1 ||
		resumed.ActionableRequestIDs[0] != "exit-live" ||
		containsSessionWarning(resumed.Warnings, "normalized persisted") ||
		eng.permissionCoordinator.PendingCount() != 1 {
		t.Fatalf(
			"live approval reprojection = state:%#v actionable:%#v warnings:%#v pending:%d",
			state,
			resumed.ActionableRequestIDs,
			resumed.Warnings,
			eng.permissionCoordinator.PendingCount(),
		)
	}
}

func TestP172CrossProjectResumeCancelsLiveApprovalAndColdNormalizes(
	t *testing.T,
) {
	currentCWD := t.TempDir()
	restoredCWD := t.TempDir()
	dir := filepath.Join(restoredCWD, "transcripts")
	planPath := filepath.Join(
		p17H0RealTempDir(t),
		".claude",
		"plans",
		"selected.md",
	)
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	record := &session.PersistedPlanState{
		Version:           session.PersistedPlanStateVersion,
		Phase:             string(PlanPhaseAwaitingApproval),
		PlanFileIdentity:  planPath,
		ReturnMode:        string(permission.ModeAcceptEdits),
		ApprovalRequestID: "exit-cross-project",
		Revision:          4,
	}
	recorder := transcript.NewRecorder("selected", dir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: "prompt"},
		{Role: schema.Assistant, Content: "response"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected", CWD: restoredCWD,
		PermissionMode: string(permission.ModePlan), PlanState: record,
		PendingRequestIDs: []string{"exit-cross-project"},
		CreatedAt:         now, UpdatedAt: now, MessageCount: 2,
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	store := NewRuntimeStateStore()
	if err := store.Apply(QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: "selected", ThreadID: "selected",
			TurnID: "turn-plan", Sequence: 1, Timestamp: now,
		},
		Type: EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{
			ToolName: "ExitPlanMode", ToolUseID: "exit-cross-project",
			Kind: "plan_approval",
			PlanApproval: &PlanApprovalRequest{
				RequestID: "exit-cross-project", PlanRevision: 4,
				PlanFileIdentity: planPath,
				ReturnMode:       permission.ModeAcceptEdits,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	registry := NewPermissionCoordinatorRegistry()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: currentCWD, TranscriptDir: dir,
		RuntimeState: store, PermissionRegistry: registry,
	})
	t.Cleanup(eng.Close)
	oldCoordinator := eng.permissionCoordinator
	liveKey := permissionRequestKey{
		engineID:  eng.permissionEngineID,
		toolUseID: "exit-cross-project",
	}
	_, cancel := context.WithCancel(context.Background())
	oldCoordinator.mu.Lock()
	oldCoordinator.pending[liveKey] = &permissionPendingRequest{
		request: PermissionPromptRequest{
			ToolName: "ExitPlanMode", ToolUseID: "exit-cross-project",
			SessionID: "selected", ThreadID: "selected",
			PlanApproval: &PlanApprovalRequest{
				RequestID: "exit-cross-project", PlanRevision: 4,
				PlanFileIdentity: planPath,
				ReturnMode:       permission.ModeAcceptEdits,
			},
		},
		result: make(chan PermissionInteractionResult, 1),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	oldCoordinator.mu.Unlock()

	resumed, err := eng.ResumeSession(t.Context(), "selected")
	if err != nil {
		t.Fatal(err)
	}
	state := eng.PlanState()
	if state.Phase != PlanPhaseActive ||
		state.ApprovalRequestID != "" ||
		state.Revision != 5 ||
		len(resumed.ActionableRequestIDs) != 0 ||
		!containsSessionWarning(resumed.Warnings, "normalized persisted") ||
		!containsSessionWarning(resumed.Warnings, "cancelled 1 live Plan") {
		t.Fatalf(
			"cross-project restore = state:%#v actionable:%#v warnings:%#v",
			state,
			resumed.ActionableRequestIDs,
			resumed.Warnings,
		)
	}
	if eng.permissionCoordinator == oldCoordinator ||
		eng.permissionCoordinator.ProjectIdentity().key() !=
			ResolvePermissionProjectIdentity(restoredCWD).key() {
		t.Fatal("resume retained the previous project permission coordinator")
	}
	if oldCoordinator.PendingCount() != 0 ||
		eng.ResolvePermissionInteraction(
			"exit-cross-project",
			PermissionInteractionResult{Decision: PermissionAllowOnce},
		) {
		t.Fatal("cross-project restore left the old approval actionable")
	}
	thread, ok := store.ThreadSnapshot("selected")
	if !ok || thread.Plan == nil ||
		thread.Plan.Phase != PlanPhaseActive ||
		thread.Plan.ApprovalRequestID != "" ||
		len(thread.PendingInteractions) != 0 {
		t.Fatalf("cross-project runtime projection = %#v", thread)
	}
}

func TestP172FixtureGraphConsumesRestoredExactPlanCapability(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	oldHome := p17H0RealTempDir(t)
	planPath := filepath.Join(oldHome, ".claude", "plans", "selected.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	recorder := writeP172Session(t, dir, cwd, "selected", &session.PersistedPlanState{
		Version:          session.PersistedPlanStateVersion,
		Phase:            string(PlanPhaseActive),
		PlanFileIdentity: planPath,
		ReturnMode:       string(permission.ModeDefault),
		Revision:         6,
	}, string(permission.ModePlan))
	t.Setenv("HOME", t.TempDir())
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{Name: "Write"}})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: cwd, TranscriptDir: dir,
		ToolRegistry: registry,
	})
	t.Cleanup(eng.Close)
	if _, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir,
		TranscriptPath: recorder.Path(),
	}); err != nil {
		t.Fatal(err)
	}
	turnID := "graph-restored-plan"
	planTurn, err := eng.beginPlanTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.endPlanTurn(turnID)
	toolContext := &ToolUseContext{
		SessionID: "selected",
		ThreadID:  "selected",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: planTurn.Mode,
			PlanFilePath:   planTurn.State.PlanFileIdentity,
		},
	}
	var executions int
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphModelRound, error) {
			if round.Number == 1 {
				return projectGraphModelRound{
					Decision: projectGraphModelToolCalls,
					ToolCalls: []schema.ToolCall{p135c2ToolCall(
						"write-plan",
						"Write",
						`{"file_path":`+
							p17H0JSONString(t, planPath)+`}`,
					)},
				}, nil
			}
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    "done",
			}, nil
		},
		tool: bindProjectGraphCanonicalToolRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalToolRoundInput, error) {
			return canonicalToolRoundInput{
				params: QueryParams{
					ToolRegistry: registry,
					CanUseTool: func(
						context.Context,
						string,
						map[string]any,
						*ToolUseContext,
					) (bool, string) {
						return true, ""
					},
					ToolExecutor: func(
						context.Context,
						string,
						string,
					) (string, error) {
						executions++
						return "written", nil
					},
					repeatedToolGuard: newRepeatedToolCallGuard(),
				},
				toolUseContext:    toolContext,
				cancellationChain: NewCancellationChain(ctx),
				hookExecutor:      hooks.NewExecutor(),
			}, nil
		}),
	})
	result, err := runnable.Invoke(
		t.Context(),
		projectGraphKernelInput{RunID: "restored-plan"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != projectGraphResultTerminal ||
		executions != 1 ||
		len(result.ToolMessages) != 1 ||
		result.ToolMessages[0].Content != "written" ||
		toolContext.Options.PlanFilePath != planPath {
		t.Fatalf(
			"restored Graph Plan trace = result:%#v executions:%d context:%#v",
			result,
			executions,
			toolContext,
		)
	}
}

func TestP172CanonicalToolExecutionCarriesExactPlanIdentity(t *testing.T) {
	exact := filepath.Join(
		p17H0RealTempDir(t),
		".claude",
		"plans",
		"selected.md",
	)
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Read"},
		ExecuteCtx: func(ctx context.Context, _ string) (string, error) {
			return tools.PlanFileIdentityFromCtx(ctx), nil
		},
	})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "selected", ThreadID: "selected",
		CWD: t.TempDir(), ToolRegistry: registry,
	})
	t.Cleanup(eng.Close)
	call := p135c2ToolCall("read-plan", "Read", `{}`)
	outcome := executeToolCall(
		t.Context(),
		QueryParams{
			ToolRegistry: registry,
			ToolExecutor: eng.toolExecutor,
			CanUseTool: func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				return true, ""
			},
		},
		hooks.NewExecutor(),
		&ToolUseContext{
			SessionID: "selected", ThreadID: "selected", PlanMode: true,
			Options: &ToolUseOptions{
				PermissionMode: permission.ModePlan,
				PlanFilePath:   exact,
			},
		},
		&call,
		nil,
	)
	if outcome == nil || outcome.Result == nil ||
		outcome.Result.Content != exact {
		t.Fatalf("canonical Plan identity propagation = %#v", outcome)
	}
}

func TestP172UnsupportedRecordPreservesPlanContainment(t *testing.T) {
	state, mode, warnings := restorePersistedPlanState(
		&session.PersistedPlanState{
			Version:           99,
			Phase:             string(PlanPhaseAwaitingApproval),
			PlanFileIdentity:  "/tmp/unsafe.md",
			ReturnMode:        string(permission.ModeBypassPermissions),
			ApprovalRequestID: "stale",
			Revision:          99,
		},
		string(permission.ModePlan),
		"selected",
		"",
		permission.ModeDefault,
		false,
	)
	if state.Phase != PlanPhaseActive ||
		state.ApprovalRequestID != "" ||
		state.Revision != 1 ||
		mode != permission.ModePlan ||
		len(warnings) == 0 {
		t.Fatalf("unsupported fallback = state:%#v mode:%q warnings:%#v", state, mode, warnings)
	}
}

type p172CompactionModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

func (m *p172CompactionModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{
		Role: schema.Assistant, Content: "[Summary of conversation so far]",
	}, nil
}

func (m *p172CompactionModel) Stream(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "done",
	}}), nil
}

func TestP172CompactionAfterResumeReinjectsPersistedExactPlanPath(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	oldHome := p17H0RealTempDir(t)
	planPath := filepath.Join(oldHome, ".claude", "plans", "selected.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("1. preserve this plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := transcript.NewRecorder("selected", dir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: strings.Repeat("older question ", 220)},
		{Role: schema.Assistant, Content: strings.Repeat("older answer ", 200)},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected", CWD: cwd,
		PermissionMode: string(permission.ModePlan),
		PlanState: &session.PersistedPlanState{
			Version: session.PersistedPlanStateVersion,
			Phase:   string(PlanPhaseActive), PlanFileIdentity: planPath,
			ReturnMode: string(permission.ModeDefault), Revision: 4,
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		MessageCount: 4,
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	model := &p172CompactionModel{}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: cwd, TranscriptDir: dir,
		ChatModel: model, CustomSystemPrompt: "You are helpful.", MaxTurns: 2,
	})
	t.Cleanup(eng.Close)
	if _, err := eng.ResumeSession(t.Context(), "selected"); err != nil {
		t.Fatal(err)
	}
	events, _ := eng.SubmitMessage(t.Context(), "continue")
	for range events {
	}

	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.inputs) == 0 {
		t.Fatal("expected model input after compaction")
	}
	found := false
	for _, message := range model.inputs[len(model.inputs)-1] {
		if message != nil && message.Extra != nil &&
			message.Extra["type"] == "plan_file_reference" &&
			message.Extra["planFilePath"] == planPath &&
			strings.Contains(message.Content, "preserve this plan") {
			found = true
		}
	}
	if !found {
		t.Fatalf("exact Plan attachment missing from model input: %#v", model.inputs[len(model.inputs)-1])
	}
}

func writeP172Session(
	t *testing.T,
	dir string,
	cwd string,
	sessionID string,
	planState *session.PersistedPlanState,
	mode string,
) *transcript.Recorder {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, dir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: "prompt"},
		{Role: schema.Assistant, Content: "response"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: sessionID, ThreadID: sessionID, CWD: cwd,
		PermissionMode: mode, PlanState: planState,
		CreatedAt: now, UpdatedAt: now, MessageCount: 2,
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	return recorder
}
