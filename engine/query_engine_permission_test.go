package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestP512ContainedAutoBashOrder(t *testing.T) {
	selection, err := NewSandboxSelection(
		containment.ProfileWorkspaceWrite,
		containment.SelectionDefault,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var prompts atomic.Int32
	var gotConstraint PermissionDecisionConstraint
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		CommandEntrypoint: commands.EntrypointTUI,
		PermissionMode:    permission.ModeAuto,
		SandboxSelection:  selection,
		PermissionPrompt: func(_ context.Context, request PermissionPromptRequest) PermissionInteractionResult {
			prompts.Add(1)
			gotConstraint = request.DecisionConstraint
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		},
	})
	t.Cleanup(engine.Close)

	ordinary := engine.evaluateInvocationPolicy(
		withToolUseID(context.Background(), "ordinary"),
		nil,
		"Bash",
		map[string]any{"command": "go test ./engine"},
		nil,
	)
	available := engine.ExecutionBindingMatrix().Guest().Availability() ==
		containment.BindingAvailable
	if available {
		if !ordinary.Allowed || ordinary.Decision != invocationPolicyAllow ||
			prompts.Load() != 0 {
			t.Fatalf("contained ordinary outcome=%#v prompts=%d", ordinary, prompts.Load())
		}
	} else if !ordinary.Allowed || prompts.Load() != 1 {
		t.Fatalf("unavailable fallback outcome=%#v prompts=%d", ordinary, prompts.Load())
	}

	critical := engine.evaluateInvocationPolicy(
		withToolUseID(context.Background(), "critical"),
		nil,
		"Bash",
		map[string]any{"command": "rm -rf /"},
		nil,
	)
	wantCriticalPrompts := int32(1)
	if !available {
		wantCriticalPrompts = 2
	}
	if !critical.Allowed || critical.Decision != invocationPolicyRequireHuman ||
		prompts.Load() != wantCriticalPrompts ||
		gotConstraint != PermissionAllowOnceOnly {
		t.Fatalf("critical outcome=%#v prompts=%d constraint=%q", critical, prompts.Load(), gotConstraint)
	}

	engine.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{
		ToolName: "Bash", InputPattern: "rm -rf /", Action: permission.ActionDeny,
		Source: permission.SourceLocal,
	}})
	denied := engine.evaluateInvocationPolicy(
		withToolUseID(context.Background(), "denied"),
		nil,
		"Bash",
		map[string]any{"command": "rm -rf /"},
		nil,
	)
	if denied.Allowed || denied.Decision != invocationPolicyDeny ||
		prompts.Load() != wantCriticalPrompts {
		t.Fatalf("explicit deny outcome=%#v prompts=%d", denied, prompts.Load())
	}
}

func TestP512ExactUserAuthorityRemainsSeparateFromProofShortcut(t *testing.T) {
	selection, err := NewSandboxSelection(
		containment.ProfileDangerFullAccess,
		containment.SelectionCLI,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var prompts atomic.Int32
	var constraint PermissionDecisionConstraint
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		CommandEntrypoint: commands.EntrypointTUI,
		PermissionMode:    permission.ModeAuto,
		SandboxSelection:  selection,
		PermissionPrompt: func(_ context.Context, request PermissionPromptRequest) PermissionInteractionResult {
			prompts.Add(1)
			constraint = request.DecisionConstraint
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		},
	})
	t.Cleanup(eng.Close)
	eng.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{
		{
			ToolName: "Bash", InputPattern: "printf explicit", Action: permission.ActionAllow,
			Source: permission.SourceLocal,
		},
		{
			ToolName: "Bash", InputPattern: "rm -rf /", Action: permission.ActionAllow,
			Source: permission.SourceLocal,
		},
	})

	var settled *PermissionActionDescriptor
	ordinary := eng.evaluateInvocationPolicy(
		withSettledPermissionActionPtr(
			withToolUseID(context.Background(), "explicit-ordinary"),
			&settled,
		),
		nil,
		"Bash",
		map[string]any{"command": "printf explicit"},
		nil,
	)
	if !ordinary.Allowed || prompts.Load() != 0 || settled == nil ||
		settled.admission != permissionAdmissionNone {
		t.Fatalf(
			"explicit authority outcome=%#v prompts=%d settled=%#v",
			ordinary,
			prompts.Load(),
			settled,
		)
	}
	if allowed, _ := completeProofBoundBashAdmission(*settled); allowed {
		t.Fatal("ambient explicit authority was mislabeled as proof-bound admission")
	}

	critical := eng.evaluateInvocationPolicy(
		withToolUseID(context.Background(), "explicit-critical"),
		nil,
		"Bash",
		map[string]any{"command": "rm -rf /"},
		nil,
	)
	if !critical.Allowed || critical.Decision != invocationPolicyRequireHuman ||
		prompts.Load() != 1 || constraint != PermissionAllowOnceOnly {
		t.Fatalf(
			"critical explicit authority outcome=%#v prompts=%d constraint=%q",
			critical,
			prompts.Load(),
			constraint,
		)
	}
}

func TestP512ContainedAutoBashEntrypointMatrix(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        permission.Mode
		entrypoint  commands.Entrypoint
		interactive bool
	}{
		{name: "default/tui", mode: permission.ModeDefault, entrypoint: commands.EntrypointTUI, interactive: true},
		{name: "default/plain", mode: permission.ModeDefault, entrypoint: commands.EntrypointPlain, interactive: true},
		{name: "default/headless", mode: permission.ModeDefault, entrypoint: commands.EntrypointHeadless},
		{name: "default/headless goal", mode: permission.ModeDefault, entrypoint: commands.EntrypointHeadlessGoal},
		{name: "default/acp", mode: permission.ModeDefault, entrypoint: commands.EntrypointACP, interactive: true},
		{name: "auto/tui", mode: permission.ModeAuto, entrypoint: commands.EntrypointTUI, interactive: true},
		{name: "auto/plain", mode: permission.ModeAuto, entrypoint: commands.EntrypointPlain, interactive: true},
		{name: "auto/headless", mode: permission.ModeAuto, entrypoint: commands.EntrypointHeadless},
		{name: "auto/headless goal", mode: permission.ModeAuto, entrypoint: commands.EntrypointHeadlessGoal},
		{name: "auto/acp", mode: permission.ModeAuto, entrypoint: commands.EntrypointACP, interactive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			selection, err := NewSandboxSelection(
				containment.ProfileWorkspaceWrite,
				containment.SelectionDefault,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			var prompts atomic.Int32
			var gotConstraint PermissionDecisionConstraint
			config := QueryEngineConfig{
				CWD:               t.TempDir(),
				CommandEntrypoint: test.entrypoint,
				PermissionMode:    test.mode,
				SandboxSelection:  selection,
			}
			if test.interactive {
				config.PermissionPrompt = func(
					_ context.Context,
					request PermissionPromptRequest,
				) PermissionInteractionResult {
					prompts.Add(1)
					gotConstraint = request.DecisionConstraint
					return PermissionInteractionResult{Decision: PermissionAllowOnce}
				}
			} else {
				config.CanUseTool = func(
					context.Context,
					string,
					map[string]any,
					*ToolUseContext,
				) (bool, string) {
					return false, "interactive permission prompting not available"
				}
			}
			engine := NewQueryEngine(config)
			t.Cleanup(engine.Close)
			if engine.wrappedCanUseTool == nil {
				t.Fatal("supported proof-bound entrypoint did not install invocation policy")
			}

			var ordinaryAction *PermissionActionDescriptor
			ordinary := engine.evaluateInvocationPolicy(
				withSettledPermissionActionPtr(
					withToolUseID(context.Background(), "ordinary-"+test.name),
					&ordinaryAction,
				),
				engine.config.CanUseTool,
				"Bash",
				map[string]any{"command": "git status --short"},
				nil,
			)
			available := engine.ExecutionBindingMatrix().Guest().Availability() ==
				containment.BindingAvailable
			if available {
				if !ordinary.Allowed || ordinaryAction == nil ||
					ordinaryAction.admission != permissionAdmissionProofBoundBash ||
					prompts.Load() != 0 {
					t.Fatalf(
						"contained ordinary outcome=%#v action=%#v prompts=%d",
						ordinary,
						ordinaryAction,
						prompts.Load(),
					)
				}
			} else if ordinaryAction != nil &&
				ordinaryAction.admission == permissionAdmissionProofBoundBash {
				t.Fatalf("unavailable Guest admitted contained action %#v", ordinaryAction)
			}

			critical := engine.evaluateInvocationPolicy(
				withToolUseID(context.Background(), "critical-"+test.name),
				engine.config.CanUseTool,
				"Bash",
				map[string]any{"command": "rm -rf /"},
				nil,
			)
			if test.interactive {
				wantPrompts := int32(1)
				if !available {
					wantPrompts++
				}
				if !critical.Allowed ||
					critical.Decision != invocationPolicyRequireHuman ||
					gotConstraint != PermissionAllowOnceOnly ||
					prompts.Load() != wantPrompts {
					t.Fatalf(
						"interactive critical outcome=%#v constraint=%q prompts=%d",
						critical,
						gotConstraint,
						prompts.Load(),
					)
				}
			} else if critical.Allowed ||
				critical.Decision != invocationPolicyRequireHuman ||
				strings.TrimSpace(critical.Reason) == "" {
				t.Fatalf("non-interactive critical outcome=%#v", critical)
			}
			if len(engine.approvalTracker.List()) != 0 {
				t.Fatalf("entrypoint created persistent grant: %#v", engine.approvalTracker.List())
			}
		})
	}
}

func TestP512CriticalPathPersistentDecisionFailsClosed(t *testing.T) {
	for _, decision := range []PermissionInteractionDecision{
		PermissionAllowSession,
		PermissionAllowAlways,
	} {
		t.Run(string(decision), func(t *testing.T) {
			engine := NewQueryEngine(QueryEngineConfig{
				CWD:               t.TempDir(),
				CommandEntrypoint: commands.EntrypointTUI,
				PermissionMode:    permission.ModeBypassPermissions,
				PermissionPrompt: func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
					return PermissionInteractionResult{Decision: decision}
				},
			})
			t.Cleanup(engine.Close)
			outcome := engine.evaluateInvocationPolicy(
				withToolUseID(context.Background(), "forged-"+string(decision)),
				nil,
				"Bash",
				map[string]any{"command": "rm -rf /"},
				nil,
			)
			if outcome.Allowed || outcome.Decision != invocationPolicyRequireHuman ||
				!strings.Contains(outcome.Reason, "constraint") {
				t.Fatalf("forged %q outcome = %#v", decision, outcome)
			}
			if len(engine.approvalTracker.List()) != 0 {
				t.Fatalf("forged %q created grant", decision)
			}
		})
	}
}

func TestP512CriticalPathDontAskNeverInvokesInteraction(t *testing.T) {
	for _, structured := range []bool{false, true} {
		name := "legacy"
		if structured {
			name = "structured"
		}
		t.Run(name, func(t *testing.T) {
			var callbacks atomic.Int32
			config := QueryEngineConfig{
				CWD:               t.TempDir(),
				CommandEntrypoint: commands.EntrypointTUI,
				PermissionMode:    permission.ModeDontAsk,
				CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
					callbacks.Add(1)
					return true, ""
				},
			}
			if structured {
				config.PermissionPrompt = func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
					callbacks.Add(1)
					return PermissionInteractionResult{Decision: PermissionAllowOnce}
				}
			}
			engine := NewQueryEngine(config)
			t.Cleanup(engine.Close)
			outcome := engine.evaluateInvocationPolicy(
				withToolUseID(context.Background(), "dontask-"+name),
				engine.config.CanUseTool,
				"Bash",
				map[string]any{"command": "rm -rf /"},
				nil,
			)
			if outcome.Allowed || outcome.Decision != invocationPolicyRequireHuman ||
				outcome.Reason != "sandbox_critical_path_confirmation_required" {
				t.Fatalf("dontAsk critical outcome = %#v", outcome)
			}
			if callbacks.Load() != 0 || len(engine.approvalTracker.List()) != 0 {
				t.Fatalf("dontAsk interaction/grants = %d/%d", callbacks.Load(), len(engine.approvalTracker.List()))
			}
		})
	}
}

func TestP512PreToolRewriteRestartsPermissionAuthority(t *testing.T) {
	newEngine := func(
		t *testing.T,
		prompt PermissionPromptFn,
	) *QueryEngine {
		t.Helper()
		selection, err := NewSandboxSelection(
			containment.ProfileWorkspaceWrite,
			containment.SelectionDefault,
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		eng := NewQueryEngine(QueryEngineConfig{
			CWD:               t.TempDir(),
			CommandEntrypoint: commands.EntrypointTUI,
			PermissionMode:    permission.ModeAuto,
			SandboxSelection:  selection,
			PermissionPrompt:  prompt,
		})
		t.Cleanup(eng.Close)
		return eng
	}
	executeRewrite := func(
		t *testing.T,
		eng *QueryEngine,
		updated map[string]any,
		executions *atomic.Int32,
	) *toolExecutionOutcome {
		t.Helper()
		hookExecutor := hooks.NewExecutor()
		hookExecutor.RegisterPreTool(func(
			context.Context,
			string,
			string,
			map[string]any,
		) *hooks.PreToolHookResult {
			return &hooks.PreToolHookResult{UpdatedInput: updated}
		})
		return executeToolCall(
			context.Background(),
			QueryParams{
				ToolRegistry: eng.toolRegistry,
				CanUseTool:   eng.wrappedCanUseTool,
				ToolExecutor: func(_ context.Context, _, input string) (string, error) {
					executions.Add(1)
					return input, nil
				},
			},
			hookExecutor,
			&ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}},
			&schema.ToolCall{
				ID: "rewrite-call",
				Function: schema.FunctionCall{
					Name: "Bash", Arguments: `{"command":"git status --short"}`,
				},
			},
			nil,
		)
	}

	t.Run("routine to critical requires constrained live decision", func(t *testing.T) {
		var prompts atomic.Int32
		var executions atomic.Int32
		eng := newEngine(t, func(_ context.Context, request PermissionPromptRequest) PermissionInteractionResult {
			prompts.Add(1)
			if request.DecisionConstraint != PermissionAllowOnceOnly {
				t.Fatalf("rewrite constraint = %q", request.DecisionConstraint)
			}
			return PermissionInteractionResult{Decision: PermissionDeny}
		})
		outcome := executeRewrite(t, eng, map[string]any{"command": "rm -f /"}, &executions)
		if outcome == nil || outcome.Result == nil || !p221bMessageIsError(outcome.Result) ||
			prompts.Load() != 1 || executions.Load() != 0 {
			t.Fatalf("critical rewrite outcome=%#v prompts=%d executions=%d", outcome, prompts.Load(), executions.Load())
		}
	})

	t.Run("routine to invalid stops before permission", func(t *testing.T) {
		var prompts atomic.Int32
		var executions atomic.Int32
		eng := newEngine(t, func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
			prompts.Add(1)
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		})
		outcome := executeRewrite(t, eng, map[string]any{}, &executions)
		if outcome == nil || outcome.Result == nil || !p221bMessageIsError(outcome.Result) ||
			prompts.Load() != 0 || executions.Load() != 0 {
			t.Fatalf("invalid rewrite outcome=%#v prompts=%d executions=%d", outcome, prompts.Load(), executions.Load())
		}
	})

	t.Run("routine to routine receives a fresh proof-bound admission", func(t *testing.T) {
		var prompts atomic.Int32
		var executions atomic.Int32
		eng := newEngine(t, func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
			prompts.Add(1)
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		})
		if eng.ExecutionBindingMatrix().Guest().Availability() != containment.BindingAvailable {
			t.Skip("complete Darwin Guest proof is unavailable")
		}
		outcome := executeRewrite(t, eng, map[string]any{"command": "go test ./engine"}, &executions)
		if outcome == nil || outcome.Result == nil || p221bMessageIsError(outcome.Result) ||
			prompts.Load() != 0 || executions.Load() != 1 {
			t.Fatalf("routine rewrite outcome=%#v prompts=%d executions=%d", outcome, prompts.Load(), executions.Load())
		}
	})
}

func TestP512ContainedAutoBashDispatch(t *testing.T) {
	selection, err := NewSandboxSelection(
		containment.ProfileWorkspaceWrite,
		containment.SelectionDefault,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:               root,
		CommandEntrypoint: commands.EntrypointHeadless,
		PermissionMode:    permission.ModeAuto,
		SandboxSelection:  selection,
	})
	t.Cleanup(engine.Close)
	var settled *PermissionActionDescriptor
	ctx := withSettledPermissionActionPtr(
		withToolUseID(context.Background(), "dispatch"),
		&settled,
	)
	outcome := engine.evaluateInvocationPolicy(
		ctx,
		nil,
		"Bash",
		map[string]any{"command": "printf p512"},
		nil,
	)
	available := engine.ExecutionBindingMatrix().Guest().Availability() ==
		containment.BindingAvailable
	if !available {
		if outcome.Allowed || settled == nil || settled.admission != permissionAdmissionNone {
			t.Fatalf("unsupported host outcome=%#v settled=%#v", outcome, settled)
		}
		return
	}
	if !outcome.Allowed || settled == nil ||
		settled.admission != permissionAdmissionProofBoundBash {
		t.Fatalf("admission outcome=%#v settled=%#v", outcome, settled)
	}
	dispatchCtx := withPermissionDispatchAction(context.Background(), *settled, nil)
	result, err := engine.toolExecutor(
		dispatchCtx,
		"Bash",
		`{"command":"printf p512"}`,
	)
	if err != nil || !strings.Contains(result, "p512") {
		t.Fatalf("dispatch result=%q err=%v", result, err)
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("non-Darwin host unexpectedly satisfied Seatbelt proof")
	}
}

func TestP512ContainedAutoBashDispatchRejectsBindingDrift(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		CommandEntrypoint: commands.EntrypointHeadless,
		PermissionMode:    permission.ModeAuto,
	})
	t.Cleanup(engine.Close)
	action, err := engine.buildPermissionActionDescriptor(
		"Bash",
		map[string]any{"command": "printf blocked"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	action = permissionActionWithExecutionIdentity(action, containment.ExecutionIdentity{
		ProcessClass: containment.ProcessClassGuest,
		PolicyDigest: "forged-policy", BindingDigest: "forged-binding",
		Profile: containment.ProfileWorkspaceWrite, State: containment.StateDegraded,
		Adapter: containment.AdapterDarwinSeatbelt, Network: containment.NetworkDenied,
		CredentialMode:       containment.CredentialAmbientEnvironment,
		CapabilityGeneration: "forged-generation", Availability: containment.BindingAvailable,
		AdapterAxes: containedAutoBashAdapterAxes, RuntimeAxes: containedAutoBashRuntimeAxes,
		Enforced: containedAutoBashAxes,
		Root:     containment.RootIdentity{Path: engine.config.CWD, Device: 1, Inode: 1},
	})
	action.admission = permissionAdmissionProofBoundBash
	_, err = engine.toolExecutor(
		withPermissionDispatchAction(context.Background(), action, nil),
		"Bash",
		`{"command":"printf blocked"}`,
	)
	if err == nil || !strings.Contains(err.Error(), "sandbox_binding_expired") {
		t.Fatalf("binding drift error = %v", err)
	}
}

func TestP512ContainedAutoBashDispatchRejectsReplacedRootBeforeExecution(
	t *testing.T,
) {
	if runtime.GOOS != "darwin" {
		t.Skip("real Seatbelt root revalidation requires Darwin")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
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
	var executions atomic.Int32
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Bash",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"command": {Type: schema.String, Required: true},
				},
			),
		},
		Capabilities: tools.ToolCapabilities{
			Declared:   true,
			Origin:     tools.ToolOriginBuiltin,
			ActionKind: tools.ToolActionShell,
		},
		Execute: func(string) (string, error) {
			executions.Add(1)
			return "unexpected", nil
		},
	})
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:               root,
		ToolRegistry:      registry,
		CommandEntrypoint: commands.EntrypointHeadless,
		PermissionMode:    permission.ModeAuto,
		SandboxSelection:  selection,
	})
	t.Cleanup(engine.Close)
	if engine.ExecutionBindingMatrix().Guest().Availability() != containment.BindingAvailable {
		t.Skip("Darwin Seatbelt Guest binding is unavailable")
	}

	var settled *PermissionActionDescriptor
	policyCtx := withSettledPermissionActionPtr(
		withToolUseID(context.Background(), "replaced-root"),
		&settled,
	)
	outcome := engine.evaluateInvocationPolicy(
		policyCtx,
		nil,
		"Bash",
		map[string]any{"command": "printf should-not-run"},
		nil,
	)
	if !outcome.Allowed || settled == nil ||
		settled.admission != permissionAdmissionProofBoundBash {
		t.Fatalf("contained admission outcome=%#v settled=%#v", outcome, settled)
	}

	movedRoot := filepath.Join(parent, "workspace-original")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(movedRoot) })

	_, err = engine.toolExecutor(
		withPermissionDispatchAction(context.Background(), *settled, nil),
		"Bash",
		settled.CanonicalInput,
	)
	if err == nil || !strings.Contains(err.Error(), "sandbox_binding_expired") {
		t.Fatalf("replaced root dispatch error = %v", err)
	}
	if executions.Load() != 0 {
		t.Fatalf("replaced root executed tool %d time(s)", executions.Load())
	}
}

type queryEnginePermissionModel struct {
	callCount int
	toolName  string
	args      string
}

func (m *queryEnginePermissionModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *queryEnginePermissionModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_denied_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      m.toolName,
					Arguments: m.args,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func TestExecuteToolCallCarriesEffectivePermissionMode(t *testing.T) {
	var inherited string
	outcome := executeToolCall(context.Background(), QueryParams{
		ToolExecutor: func(ctx context.Context, toolName, input string) (string, error) {
			inherited = tools.InheritedPermissionModeFromContext(ctx)
			return "ok", nil
		},
	}, nil, &ToolUseContext{Options: &ToolUseOptions{
		PermissionMode: permission.ModeBypassPermissions,
	}}, &schema.ToolCall{
		ID: "tool-use-id",
		Function: schema.FunctionCall{
			Name:      "Agent",
			Arguments: `{}`,
		},
	}, nil)

	if outcome == nil || outcome.Result == nil {
		t.Fatal("executeToolCall returned no result")
		return
	}
	if inherited != string(permission.ModeBypassPermissions) {
		t.Fatalf("inherited permission mode = %q, want %q", inherited, permission.ModeBypassPermissions)
	}
}

func TestQueryEngineTracksPermissionDenialsPerTurn(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-perm-a",
		TranscriptDir:      filepath.Join(t.TempDir(), "transcripts"),
		CWD:                t.TempDir(),
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           5,
		ChatModel: &queryEnginePermissionModel{
			toolName: "Bash",
			args:     `{"command":"pwd"}`,
		},
		ToolRegistry: reg,
		Tools:        reg.List(),
		Model:        "test-model",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "policy blocked"
		},
		Clock: func() time.Time {
			return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
		},
	})

	events, _ := eng.SubmitMessage(context.Background(), "run denied tool")
	for range events {
	}

	denials := eng.GetPermissionDenials()
	if len(denials) != 1 {
		t.Fatalf("expected 1 permission denial, got %d (%#v)", len(denials), denials)
	}
	if denials[0].ToolName != "Bash" {
		t.Fatalf("expected tool name Bash, got %q", denials[0].ToolName)
	}
	if denials[0].ToolUseID != "call_denied_1" {
		t.Fatalf("expected tool use id call_denied_1, got %q", denials[0].ToolUseID)
	}
	if denials[0].Input["command"] != "pwd" {
		t.Fatalf("expected denial input to preserve command, got %#v", denials[0].Input)
	}
}

func TestQueryEngineResetsPermissionDenialsBetweenTurns(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	allow := false
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "session-perm-b",
		TranscriptDir:      filepath.Join(t.TempDir(), "transcripts"),
		CWD:                t.TempDir(),
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           5,
		ChatModel: &queryEnginePermissionModel{
			toolName: "Bash",
			args:     `{"command":"pwd"}`,
		},
		ToolRegistry: reg,
		Tools:        reg.List(),
		Model:        "test-model",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			if allow {
				return true, ""
			}
			return false, "first turn blocked"
		},
	})

	events, _ := eng.SubmitMessage(context.Background(), "first turn")
	for range events {
	}
	if len(eng.GetPermissionDenials()) != 1 {
		t.Fatalf("expected 1 denial after first turn, got %#v", eng.GetPermissionDenials())
	}

	allow = true
	eng.config.ChatModel = &queryEnginePermissionModel{toolName: "Bash", args: `{"command":"pwd"}`}
	events, _ = eng.SubmitMessage(context.Background(), "second turn")
	for range events {
	}
	if len(eng.GetPermissionDenials()) != 0 {
		t.Fatalf("expected denials to reset on second turn, got %#v", eng.GetPermissionDenials())
	}
}

func TestQueryEngineSDKCompatPermissionToolNameForAgent(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "blocked"
		},
	})

	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Agent", map[string]any{
		"description": "bounded work",
		"prompt":      "do work",
	}, nil)
	if allowed || reason != "blocked" {
		t.Fatalf("expected wrapped canUseTool to deny, got allowed=%v reason=%q", allowed, reason)
	}
	denials := eng.GetPermissionDenials()
	if len(denials) != 1 {
		t.Fatalf("expected 1 denial, got %#v", denials)
	}
	if denials[0].ToolName != "Task" {
		t.Fatalf("expected SDK compat Agent->Task mapping, got %q", denials[0].ToolName)
	}
}

func TestQueryEngineDenialTrackingCountersAccumulate(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "denied"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}

	// Record multiple denials
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)

	consecutive, total := eng.GetDenialTrackingState()
	if consecutive != 2 {
		t.Fatalf("expected consecutive=2, got %d", consecutive)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
}

func TestQueryEngineDenialTrackingSuccessResetsConsecutive(t *testing.T) {
	allowNext := false
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			if allowNext {
				return true, ""
			}
			return false, "denied"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}

	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)

	allowNext = true
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)

	consecutive, total := eng.GetDenialTrackingState()
	if consecutive != 0 {
		t.Fatalf("expected consecutive=0 after success, got %d", consecutive)
	}
	if total != 2 {
		t.Fatalf("expected total=2 (only denials count), got %d", total)
	}
}

func TestQueryEngineShouldFallbackToPromptingAfterConsecutiveDenials(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "denied"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}

	// 2 denials - below threshold
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)
	if eng.ShouldFallbackToPrompting() {
		t.Fatal("should not fallback with only 2 consecutive denials")
	}

	// 3rd denial - at threshold
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)
	if !eng.ShouldFallbackToPrompting() {
		t.Fatal("should fallback after 3 consecutive denials")
	}
}

func TestQueryEngineBypassModeSkipsPermissionCheck(t *testing.T) {
	called := false
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeBypassPermissions,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			called = true
			return false, "should not reach here"
		},
	})

	bypassCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeBypassPermissions}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "echo safe"}, bypassCtx)

	if !allowed {
		t.Fatalf("expected bypass mode to auto-allow, got denied reason=%q", reason)
	}
	if called {
		t.Fatal("expected bypass mode to skip the inner CanUseTool check")
	}
	if len(eng.GetPermissionDenials()) != 0 {
		t.Fatal("expected no denials in bypass mode")
	}
}

func TestQueryEngineDefaultModeDoesNotTrackDenials(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeDefault,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "denied"
		},
	})

	defaultCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, defaultCtx)
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, defaultCtx)

	// Denials are still recorded in the denial list (for SDK consumers)
	if len(eng.GetPermissionDenials()) != 2 {
		t.Fatalf("expected 2 denials recorded, got %d", len(eng.GetPermissionDenials()))
	}

	// But denial tracking counters should NOT increment (only auto mode)
	consecutive, total := eng.GetDenialTrackingState()
	if consecutive != 0 || total != 0 {
		t.Fatalf("expected denial tracking counters to be 0 in default mode, got consecutive=%d total=%d", consecutive, total)
	}
}

func TestP202PlanReviseBypassesGenericDenialAccounting(t *testing.T) {
	const sessionID = "p20-2-revise-accounting"
	planPath := p200PreparePlan(t, sessionID, "", "# Plan\n")
	registry := tools.NewRegistry()
	registry.Register(planExitTestTool(nil))

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      sessionID,
		ThreadID:       "p20-2-thread",
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ToolRegistry:   registry,
		PermissionPrompt: func(
			_ context.Context,
			request PermissionPromptRequest,
		) PermissionInteractionResult {
			return PermissionInteractionResult{
				Decision: PermissionDeny,
				Message:  "revise Plan",
				PlanApproval: &PlanApprovalDecision{
					RequestID:    request.PlanApproval.RequestID,
					PlanRevision: request.PlanApproval.PlanRevision,
					Outcome:      PlanApprovalRevise,
					Feedback:     "cover rollback",
					TargetMode:   permission.ModePlan,
				},
			}
		},
	})
	eng.planMu.Lock()
	eng.planState = PlanState{
		Phase:            PlanPhaseActive,
		Revision:         1,
		PlanFileIdentity: planPath,
		ReturnMode:       permission.ModeAuto,
	}
	eng.planMu.Unlock()
	if _, err := eng.beginPlanTurn("p20-2-turn"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		eng.endPlanTurn("p20-2-turn")
		eng.Close()
	})

	allowed, reason := eng.wrappedCanUseTool(
		withToolUseID(context.Background(), "p20-2-exit"),
		"ExitPlanMode",
		map[string]any{},
		&ToolUseContext{
			PlanMode: true,
			Options: &ToolUseOptions{
				PermissionMode: permission.ModePlan,
			},
		},
	)
	if allowed ||
		!strings.Contains(reason, "cover rollback") {
		t.Fatalf("Revise result = allowed:%v reason:%q", allowed, reason)
	}
	if denials := eng.GetPermissionDenials(); len(denials) != 0 {
		t.Fatalf("Revise entered generic denials: %#v", denials)
	}
	if consecutive, total := eng.GetDenialTrackingState(); consecutive != 0 ||
		total != 0 {
		t.Fatalf(
			"Revise entered auto-denial counters: consecutive=%d total=%d",
			consecutive,
			total,
		)
	}
	if state := eng.PlanState(); state.Phase != PlanPhaseActive ||
		state.ReturnMode != permission.ModeAuto {
		t.Fatalf("Revise state = %#v", state)
	}
}

func TestQueryEngineToolContextModeOverridesEngineConfig(t *testing.T) {
	called := false
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeDefault, // engine says default
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			called = true
			return false, "denied"
		},
	})

	// But toolCtx says bypass — toolCtx should win
	bypassCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeBypassPermissions}}
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, bypassCtx)

	if !allowed {
		t.Fatal("expected toolCtx bypass mode to override engine default mode")
	}
	if called {
		t.Fatal("expected bypass to skip the inner CanUseTool check")
	}
}

func TestQueryEngineDontAskModeDeniesButDoesNotTrack(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeDontAsk,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "denied in dontAsk"
		},
	})

	dontAskCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDontAsk}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, dontAskCtx)

	if allowed {
		t.Fatal("expected dontAsk mode to respect denial from inner check")
	}
	if reason != "permission denied (dont-ask mode: no interactive prompting)" {
		t.Fatalf("expected dont-ask denial reason, got %q", reason)
	}

	// Denial tracking should NOT fire for dontAsk mode
	consecutive, total := eng.GetDenialTrackingState()
	if consecutive != 0 || total != 0 {
		t.Fatalf("expected no denial tracking in dontAsk mode, got consecutive=%d total=%d", consecutive, total)
	}
}

func TestTodoWriteDefaultPermissionPreservesExplicitRules(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)

	tests := []struct {
		name           string
		mode           permission.Mode
		rules          []permission.PermissionRule
		wantAllowed    bool
		wantInnerCalls int
		wantReason     string
	}{
		{
			name:        "default skips prompt",
			mode:        permission.ModeDefault,
			wantAllowed: true,
		},
		{
			name:        "dontAsk still allows internal state update",
			mode:        permission.ModeDontAsk,
			wantAllowed: true,
		},
		{
			name:        "plan allows task tracking",
			mode:        permission.ModePlan,
			wantAllowed: true,
		},
		{
			name:        "auto allows trusted runtime state",
			mode:        permission.ModeAuto,
			wantAllowed: true,
		},
		{
			name: "explicit deny wins",
			mode: permission.ModeDefault,
			rules: []permission.PermissionRule{{
				ToolName: "TodoWrite",
				Action:   permission.ActionDeny,
			}},
			wantReason: "permission rule denied tool use",
		},
		{
			name: "explicit ask wins",
			mode: permission.ModeDefault,
			rules: []permission.PermissionRule{{
				ToolName: "TodoWrite",
				Action:   permission.ActionAsk,
			}},
			wantAllowed:    true,
			wantInnerCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			innerCalls := 0
			eng := &QueryEngine{
				config: QueryEngineConfig{
					PermissionMode: test.mode,
					ToolRegistry:   registry,
				},
				toolRegistry:    registry,
				permissionRules: permission.NewRulesEngine(test.rules),
			}
			canUse := eng.wrapCanUseTool(func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				innerCalls++
				return true, ""
			})
			probe := &projectGraphHITLProbe{}
			allowed, reason := canUse(
				withProjectGraphHITLProbe(context.Background(), probe),
				"TodoWrite",
				map[string]any{"todos": []any{}},
				nil,
			)
			if allowed != test.wantAllowed || reason != test.wantReason {
				t.Fatalf(
					"permission result = (%v, %q), want (%v, %q)",
					allowed,
					reason,
					test.wantAllowed,
					test.wantReason,
				)
			}
			if innerCalls != test.wantInnerCalls {
				t.Fatalf("inner calls = %d, want %d", innerCalls, test.wantInnerCalls)
			}
			if test.wantInnerCalls == 0 && probe.captured != nil {
				t.Fatalf("TodoWrite created a Graph interrupt: %#v", probe.captured)
			}
		})
	}
}

func TestQueryEngineAcceptEditsModeAllowsWriteInCWD(t *testing.T) {
	innerCalled := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			innerCalled = true
			return false, "should not reach"
		},
	})

	acceptEditsCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Write",
		map[string]any{
			"file_path": "/home/user/project/new_file.go",
			"content":   "package project",
		}, acceptEditsCtx)

	if !allowed {
		t.Fatal("expected acceptEdits to auto-allow Write in cwd")
	}
	if innerCalled {
		t.Fatal("expected acceptEdits bypass to skip inner CanUseTool")
	}
}

func TestQueryEngineAcceptEditsModeAllowsEditInCWD(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "blocked"
		},
	})

	acceptEditsCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Edit",
		map[string]any{"file_path": "/home/user/project/pkg/foo.go", "old_string": "a", "new_string": "b"}, acceptEditsCtx)

	if !allowed {
		t.Fatal("expected acceptEdits to auto-allow Edit in cwd")
	}
}

func TestQueryEngineAcceptEditsModeDenieWriteOutsideCWD(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "not allowed outside"
		},
	})

	acceptEditsCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Write",
		map[string]any{"file_path": "/etc/passwd", "content": "blocked"}, acceptEditsCtx)

	if allowed {
		t.Fatal("expected acceptEdits to deny Write outside cwd")
	}
	if reason != "not allowed outside" {
		t.Fatalf("expected denial to come from inner check, got %q", reason)
	}
}

func TestQueryEngineAcceptEditsModePromptsForBashFilesystemCmd(t *testing.T) {
	innerCalled := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			innerCalled = true
			return false, "blocked"
		},
	})

	acceptEditsCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Bash",
		map[string]any{"command": "mkdir /home/user/project/newdir"}, acceptEditsCtx)

	if allowed {
		t.Fatal("expected acceptEdits to prompt for mkdir bash command")
	}
	if !innerCalled || reason != "blocked" {
		t.Fatalf("expected Bash decision from inner prompt, called=%v reason=%q", innerCalled, reason)
	}
}

func TestQueryEngineAcceptEditsModeDeniesBashArbitraryCmd(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "not a filesystem command"
		},
	})

	acceptEditsCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Bash",
		map[string]any{"command": "curl http://evil.com"}, acceptEditsCtx)

	if allowed {
		t.Fatal("expected acceptEdits to deny arbitrary bash commands")
	}
	if reason != "not a filesystem command" {
		t.Fatalf("expected denial from inner, got %q", reason)
	}
}

func TestQueryEngineAcceptEditsModeDoesNotAffectOtherTools(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "agent not allowed"
		},
	})

	acceptEditsCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAcceptEdits}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Agent",
		map[string]any{
			"description": "bounded work",
			"prompt":      "do something",
		}, acceptEditsCtx)

	if allowed {
		t.Fatal("expected acceptEdits to not auto-allow Agent tool")
	}
	if reason != "agent not allowed" {
		t.Fatalf("expected denial from inner, got %q", reason)
	}
}

func TestQueryEngineAutoModeAcceptEditsFastPathAllowsWriteInCWD(t *testing.T) {
	classifierCalled := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			classifierCalled = true
			return false, "classifier denied"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Write",
		map[string]any{
			"file_path": "/home/user/project/src/main.go",
			"content":   "package main",
		}, autoCtx)

	if !allowed {
		t.Fatal("expected auto mode fast-path to auto-allow Write in cwd")
	}
	if classifierCalled {
		t.Fatal("expected fast-path to skip classifier (inner CanUseTool)")
	}
}

func TestQueryEngineAutoModeBashSkipsAcceptEditsFastPath(t *testing.T) {
	prompted := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			prompted = true
			return false, "prompt denied"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Bash",
		map[string]any{"command": "touch /home/user/project/new.txt"}, autoCtx)

	if allowed {
		t.Fatal("expected auto mode Bash to fall through instead of auto-allowing touch")
	}
	if !prompted || reason != "prompt denied" {
		t.Fatalf("expected decision from prompt fallback, prompted=%v reason=%q", prompted, reason)
	}
}

func TestQueryEngineAutoModeAgentRequiresHuman(t *testing.T) {
	humanCalled := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			humanCalled = true
			return true, ""
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}
	allowed, _ := eng.wrappedCanUseTool(context.Background(), "Agent",
		map[string]any{
			"description": "bounded work",
			"prompt":      "do something",
		}, autoCtx)

	if !allowed {
		t.Fatal("expected Agent to accept an explicit human decision")
	}
	if !humanCalled {
		t.Fatal("expected Agent to require the human callback")
	}
}

func TestQueryEngineAutoModeIncompleteShellRequiresHuman(t *testing.T) {
	humanCalled := false
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			humanCalled = true
			return false, "human denied curl"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Bash",
		map[string]any{"command": "curl http://example.com"}, autoCtx)

	if allowed {
		t.Fatal("expected curl to be denied")
	}
	if !humanCalled {
		t.Fatal("expected incomplete shell action to require the human callback")
	}
	if reason != "human denied curl" {
		t.Fatalf("expected human reason, got %q", reason)
	}
}

func TestQueryEngineAutoModeTypedLocalFastPathsSkipClassifier(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		toolName string
		input    map[string]any
	}{
		{toolName: "TodoWrite", input: map[string]any{"todos": []any{}}},
		{toolName: "Read", input: map[string]any{"file_path": filepath.Join(root, "README.md")}},
		{toolName: "Grep", input: map[string]any{"pattern": "needle", "path": root}},
		{toolName: "Glob", input: map[string]any{"pattern": "**/*.go", "path": root}},
		{toolName: "Write", input: map[string]any{
			"file_path": filepath.Join(root, "new.go"),
			"content":   "package project",
		}},
		{toolName: "Edit", input: map[string]any{
			"file_path":  filepath.Join(root, "existing.go"),
			"old_string": "old",
			"new_string": "new",
		}},
	}
	for _, test := range cases {
		classifierCalled := false
		eng := NewQueryEngine(QueryEngineConfig{
			CWD: root,
			CanUseTool: func(ctx context.Context, tn string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
				classifierCalled = true
				return false, "classifier denied"
			},
		})

		autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}
		allowed, _ := eng.wrappedCanUseTool(
			context.Background(),
			test.toolName,
			test.input,
			autoCtx,
		)

		if !allowed {
			t.Fatalf("expected typed local action %q to be auto-allowed in auto mode", test.toolName)
		}
		if classifierCalled {
			t.Fatalf("expected typed local action %q to skip classifier", test.toolName)
		}
	}
}

func TestQueryEngineAutoModeSafeAllowlistDoesNotApplyInDefaultMode(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "denied in default mode"
		},
	})

	defaultCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	allowed, reason := eng.wrappedCanUseTool(context.Background(), "Read",
		map[string]any{"file_path": "/etc/passwd"}, defaultCtx)

	if allowed {
		t.Fatal("expected safe allowlist to not apply in default mode")
	}
	if reason != "denied in default mode" {
		t.Fatalf("expected denial from inner, got %q", reason)
	}
}

func TestQueryEngineEmitsClassifierStatusOnlyWhenAutoModeClassifierReports(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			ReportClassifierStatusChecking(ctx, toolName)
			result := permission.PermissionResult{
				Decision: permission.DecisionAllow,
				Reason:   permission.ReasonClassifier,
				Message:  "model classified command as safe",
				ToolName: toolName,
			}
			ReportClassifierStatusCompleted(ctx, result)
			ReportClassifierStatusCleared(ctx, toolName)
			return true, result.Message
		},
	})

	var statuses []ClassifierStatusEvent
	outcome := executeToolCall(context.Background(), QueryParams{
		CanUseTool:   eng.wrappedCanUseTool,
		ToolRegistry: eng.toolRegistry,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) { return "ok", nil },
	}, nil, &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}, &schema.ToolCall{
		ID:   "call_classifier_1",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "TaskCreate",
			Arguments: `{"subject":"check","description":"check state"}`,
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventClassifierStatus && evt.ClassifierStatus != nil {
			if evt.ClassifierStatus.ToolUseID != "call_classifier_1" || evt.ClassifierStatus.ToolName != "TaskCreate" {
				t.Fatalf("unexpected classifier status identity: %#v", evt.ClassifierStatus)
				return
			}
			statuses = append(statuses, *evt.ClassifierStatus)
		}
	})

	if outcome == nil || outcome.Result == nil {
		t.Fatalf("expected tool outcome, got %#v", outcome)
		return
	}
	want := []ClassifierStatusPhase{ClassifierStatusChecking, ClassifierStatusCompleted, ClassifierStatusCleared}
	if len(statuses) != len(want) {
		t.Fatalf("expected classifier phases %#v, got %#v", want, statuses)
	}
	for i := range want {
		if statuses[i].Phase != want[i] {
			t.Fatalf("expected classifier phase %d to be %q, got %q", i, want[i], statuses[i].Phase)
		}
	}
	completed := statuses[1]
	if completed.Decision != string(permission.DecisionAllow) {
		t.Fatalf("expected completed decision %q, got %q", permission.DecisionAllow, completed.Decision)
	}
	if completed.Reason != string(permission.ReasonClassifier) {
		t.Fatalf("expected completed reason %q, got %q", permission.ReasonClassifier, completed.Reason)
	}
	if completed.Message != "model classified command as safe" {
		t.Fatalf("expected real classifier message, got %q", completed.Message)
	}
}

func TestQueryEngineDoesNotEmitClassifierStatusForGenericAutoModeCallback(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return true, "generic policy approved"
		},
	})

	var classifierEvents int
	outcome := executeToolCall(context.Background(), QueryParams{
		CanUseTool:   eng.wrappedCanUseTool,
		ToolRegistry: eng.toolRegistry,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) { return "ok", nil },
	}, nil, &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}, &schema.ToolCall{
		ID:   "call_generic_permission_1",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "Bash",
			Arguments: `{"command":"curl https://example.com"}`,
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventClassifierStatus {
			classifierEvents++
		}
	})

	if outcome == nil || outcome.Result == nil {
		t.Fatalf("expected tool outcome, got %#v", outcome)
		return
	}
	if classifierEvents != 0 {
		t.Fatalf("expected generic permission callback to emit no classifier status, got %d", classifierEvents)
	}
}

func TestQueryEngineDoesNotEmitClassifierStatusForAutoModeFastPaths(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			t.Fatalf("fast path should skip classifier inner check for %s", toolName)
			return false, "unexpected"
		},
	})

	var classifierEvents int
	outcome := executeToolCall(context.Background(), QueryParams{
		CanUseTool:   eng.wrappedCanUseTool,
		ToolRegistry: eng.toolRegistry,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) { return "ok", nil },
	}, nil, &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}, &schema.ToolCall{
		ID:   "call_fast_path_1",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "Read",
			Arguments: `{"file_path":"/home/user/project/README.md"}`,
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventClassifierStatus {
			classifierEvents++
		}
	})

	if outcome == nil || outcome.Result == nil {
		t.Fatalf("expected tool outcome, got %#v", outcome)
		return
	}
	if classifierEvents != 0 {
		t.Fatalf("expected no classifier status events on safe fast path, got %d", classifierEvents)
	}
}

func TestQueryEngineAutoModeHumanRequiredActionDoesNotMasqueradeAsClassifierFallback(t *testing.T) {
	var sawFallbackSignal bool
	var calls int
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeAuto,
		CWD:            "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			if permission.IsFallbackPrompting(ctx) {
				sawFallbackSignal = true
			}
			calls++
			return false, "human denied"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}

	for i := 0; i < 4; i++ {
		eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "dangerous"}, autoCtx)
	}
	if calls != 4 {
		t.Fatalf("human callback calls = %d, want 4", calls)
	}
	if sawFallbackSignal {
		t.Fatal("human-required shell action was mislabeled as classifier fallback")
	}
	consecutive, total := eng.GetDenialTrackingState()
	if consecutive != 4 || total != 4 {
		t.Fatalf("denial accounting = %d/%d, want 4/4", consecutive, total)
	}
}

func TestQueryEngineAutoModeFallbackSignalNotSetBelowThreshold(t *testing.T) {
	var sawFallback bool
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeAuto,
		CWD:            "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			if permission.IsFallbackPrompting(ctx) {
				sawFallback = true
			}
			return false, "denied"
		},
	})

	autoCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeAuto}}

	// Only 2 denials — below threshold
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "x"}, autoCtx)
	eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "x"}, autoCtx)

	if sawFallback {
		t.Fatal("should not set fallback signal below threshold")
	}
}

func TestQueryEngineAutoModeFallbackSignalNotSetInDefaultMode(t *testing.T) {
	var sawFallback bool
	eng := NewQueryEngine(QueryEngineConfig{
		PermissionMode: permission.ModeDefault,
		CWD:            "/home/user/project",
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			if permission.IsFallbackPrompting(ctx) {
				sawFallback = true
			}
			return false, "denied"
		},
	})

	defaultCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}

	// Even with many denials, default mode should never set fallback signal
	for i := 0; i < 5; i++ {
		eng.wrappedCanUseTool(context.Background(), "Bash", map[string]any{"command": "x"}, defaultCtx)
	}

	if sawFallback {
		t.Fatal("default mode should never set fallback-to-prompting signal")
	}
}
