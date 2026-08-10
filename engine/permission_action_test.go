package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestP221bDescriptorUsesCanonicalRegisteredValidatedAction(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	customValidations := 0
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "PolicyAction",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"value": {
						Type:     schema.String,
						Required: true,
					},
					"nested": {
						Type: schema.Object,
					},
				},
			),
		},
		Aliases: []string{"policy_alias"},
		Capabilities: tools.ToolCapabilities{
			Declared:   true,
			Origin:     tools.ToolOriginBuiltin,
			ActionKind: tools.ToolActionRuntimeState,
		},
		ValidateInput: func(input map[string]any) error {
			customValidations++
			if input["value"] == "invalid" {
				return errors.New("invalid value")
			}
			return nil
		},
		Execute: func(string) (string, error) { return "ok", nil },
	})
	selection := &tools.ToolSelection{Names: []string{"PolicyAction"}}
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:            root,
		ToolRegistry:   registry,
		ToolSelection:  selection,
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			return true, ""
		},
	})
	t.Cleanup(engine.Close)

	input := map[string]any{
		"value": "valid",
		"nested": map[string]any{
			"key": "original",
		},
	}
	action, err := engine.buildPermissionActionDescriptor(
		"policy_alias",
		input,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if action.RequestedToolName != "policy_alias" ||
		action.CanonicalToolName != "PolicyAction" ||
		!action.Registered ||
		!action.Enabled ||
		!action.Selected {
		t.Fatalf("registry identity = %#v", action)
	}
	if action.Origin != tools.ToolOriginBuiltin ||
		action.ActionKind != tools.ToolActionRuntimeState ||
		!action.CapabilitiesDeclared ||
		!action.SchemaValidated ||
		!action.CustomValidationComplete {
		t.Fatalf("capability/validation facts = %#v", action)
	}
	if action.CapabilityGeneration != registry.Generation() ||
		action.CWD != root ||
		len(action.WorkingRoots) != 1 ||
		action.WorkingRoots[0] != root ||
		action.PolicySnapshotID == "" {
		t.Fatalf("runtime binding = %#v", action)
	}
	if customValidations != 1 {
		t.Fatalf("custom validations = %d, want 1", customValidations)
	}
	input["value"] = "mutated"
	input["nested"].(map[string]any)["key"] = "mutated"
	if action.Input["value"] != "valid" ||
		action.Input["nested"].(map[string]any)["key"] != "original" {
		t.Fatalf("descriptor retained caller aliases: %#v", action.Input)
	}
	var canonical map[string]any
	if err := json.Unmarshal([]byte(action.CanonicalInput), &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical["value"] != "valid" {
		t.Fatalf("canonical input = %#v", canonical)
	}
}

func TestTodoWriteRuntimeStateDescriptor(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:            t.TempDir(),
		ToolRegistry:   registry,
		PermissionMode: permission.ModeAuto,
	})
	t.Cleanup(engine.Close)

	action, err := engine.buildPermissionActionDescriptor(
		"TodoWrite",
		map[string]any{"todos": []any{}},
		&ToolUseContext{Options: &ToolUseOptions{
			PermissionMode: permission.ModeAuto,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if action.ActionKind != tools.ToolActionRuntimeState ||
		!action.InternalStateDefaultSafe {
		t.Fatalf("TodoWrite descriptor = %#v", action)
	}
}

func TestDefaultSafeInternalStateRequiresTrustedContract(t *testing.T) {
	trusted := tools.ToolImpl{DefaultPermissionAllowed: true}
	for _, test := range []struct {
		name         string
		impl         tools.ToolImpl
		capabilities tools.ToolCapabilities
		want         bool
	}{
		{
			name: "built-in runtime state",
			impl: trusted,
			capabilities: tools.ToolCapabilities{
				Declared:   true,
				Origin:     tools.ToolOriginBuiltin,
				ActionKind: tools.ToolActionRuntimeState,
			},
			want: true,
		},
		{
			name: "built-in process local",
			impl: trusted,
			capabilities: tools.ToolCapabilities{
				Declared:   true,
				Origin:     tools.ToolOriginBuiltin,
				ActionKind: tools.ToolActionProcessLocal,
			},
			want: true,
		},
		{
			name: "missing registry opt-in",
			capabilities: tools.ToolCapabilities{
				Declared:   true,
				Origin:     tools.ToolOriginBuiltin,
				ActionKind: tools.ToolActionRuntimeState,
			},
		},
		{
			name: "undeclared capability",
			impl: trusted,
			capabilities: tools.ToolCapabilities{
				Origin:     tools.ToolOriginBuiltin,
				ActionKind: tools.ToolActionRuntimeState,
			},
		},
		{
			name: "external runtime state",
			impl: trusted,
			capabilities: tools.ToolCapabilities{
				Declared:   true,
				Origin:     tools.ToolOriginMCP,
				ActionKind: tools.ToolActionRuntimeState,
			},
		},
		{
			name: "built-in read",
			impl: trusted,
			capabilities: tools.ToolCapabilities{
				Declared:   true,
				Origin:     tools.ToolOriginBuiltin,
				ActionKind: tools.ToolActionRead,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := defaultSafeInternalState(test.impl, test.capabilities); got != test.want {
				t.Fatalf("default safe = %v, want %v", got, test.want)
			}
		})
	}
}

func TestP221bInvalidUnavailableAndUnselectedActionsDenyBeforeInteraction(
	t *testing.T,
) {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "PolicyAction",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"value": {
						Type:     schema.String,
						Required: true,
					},
				},
			),
		},
		Capabilities: tools.ToolCapabilities{
			Declared:   true,
			Origin:     tools.ToolOriginBuiltin,
			ActionKind: tools.ToolActionRuntimeState,
		},
		ValidateInput: func(input map[string]any) error {
			if input["value"] == "invalid" {
				return errors.New("custom validation rejected value")
			}
			return nil
		},
	})
	var prompts atomic.Int32
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:            t.TempDir(),
		ToolRegistry:   registry,
		ToolSelection:  &tools.ToolSelection{Names: []string{"PolicyAction"}},
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			prompts.Add(1)
			return true, ""
		},
	})
	t.Cleanup(engine.Close)

	cases := []struct {
		name     string
		toolName string
		input    map[string]any
	}{
		{
			name:     "unknown",
			toolName: "UnknownAction",
			input:    map[string]any{"value": "valid"},
		},
		{
			name:     "unavailable",
			toolName: "EnterWorktree",
			input:    map[string]any{},
		},
		{
			name:     "schema invalid",
			toolName: "PolicyAction",
			input:    map[string]any{},
		},
		{
			name:     "custom invalid",
			toolName: "PolicyAction",
			input:    map[string]any{"value": "invalid"},
		},
		{
			name:     "non durable",
			toolName: "PolicyAction",
			input:    map[string]any{"value": make(chan int)},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			outcome := engine.evaluateInvocationPolicy(
				context.Background(),
				engine.config.CanUseTool,
				test.toolName,
				test.input,
				nil,
			)
			if outcome.Decision != invocationPolicyDeny || outcome.Allowed {
				t.Fatalf("outcome = %#v", outcome)
			}
		})
	}

	registry.Disable("PolicyAction")
	if outcome := engine.evaluateInvocationPolicy(
		context.Background(),
		engine.config.CanUseTool,
		"PolicyAction",
		map[string]any{"value": "valid"},
		nil,
	); outcome.Decision != invocationPolicyDeny {
		t.Fatalf("disabled outcome = %#v", outcome)
	}
	registry.Enable("PolicyAction")

	selectedOut := NewQueryEngine(QueryEngineConfig{
		CWD:            t.TempDir(),
		ToolRegistry:   registry,
		ToolSelection:  &tools.ToolSelection{Names: []string{}},
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			prompts.Add(1)
			return true, ""
		},
	})
	t.Cleanup(selectedOut.Close)
	if outcome := selectedOut.evaluateInvocationPolicy(
		context.Background(),
		selectedOut.config.CanUseTool,
		"PolicyAction",
		map[string]any{"value": "valid"},
		nil,
	); outcome.Decision != invocationPolicyDeny ||
		!strings.Contains(outcome.Reason, "--tools") {
		t.Fatalf("selected-out outcome = %#v", outcome)
	}
	if prompts.Load() != 0 {
		t.Fatalf("invalid actions reached interaction %d time(s)", prompts.Load())
	}
}

func TestP221bSupportedAutoRootsInstallPolicyWithoutInteraction(t *testing.T) {
	entrypoints := []commands.Entrypoint{
		commands.EntrypointTUI,
		commands.EntrypointPlain,
		commands.EntrypointHeadless,
		commands.EntrypointACP,
	}
	for _, entrypoint := range entrypoints {
		t.Run(string(entrypoint), func(t *testing.T) {
			engine := NewQueryEngine(QueryEngineConfig{
				CWD:               t.TempDir(),
				PermissionMode:    permission.ModeAuto,
				CommandEntrypoint: entrypoint,
			})
			t.Cleanup(engine.Close)
			assertP221bNonInteractiveAutoPolicy(t, engine)
		})
	}
	t.Run("child", func(t *testing.T) {
		engine := NewQueryEngine(QueryEngineConfig{
			CWD:            t.TempDir(),
			AgentID:        "child-agent",
			PermissionMode: permission.ModeAuto,
		})
		t.Cleanup(engine.Close)
		assertP221bNonInteractiveAutoPolicy(t, engine)
	})

	t.Run("caller authoritative direct construction", func(t *testing.T) {
		engine := NewQueryEngine(QueryEngineConfig{
			CWD:            t.TempDir(),
			PermissionMode: permission.ModeAuto,
		})
		t.Cleanup(engine.Close)
		if engine.toolRegistry != nil || engine.wrappedCanUseTool != nil {
			t.Fatalf(
				"direct nil/nil boundary installed registry/policy: registry=%p callback=%v",
				engine.toolRegistry,
				engine.wrappedCanUseTool != nil,
			)
		}
	})
}

func assertP221bNonInteractiveAutoPolicy(
	t *testing.T,
	engine *QueryEngine,
) {
	t.Helper()
	if engine.toolRegistry == nil || engine.wrappedCanUseTool == nil {
		t.Fatalf(
			"supported Auto root has registry=%p callback=%v",
			engine.toolRegistry,
			engine.wrappedCanUseTool != nil,
		)
	}
	if allowed, reason := engine.wrappedCanUseTool(
		context.Background(),
		"TodoWrite",
		map[string]any{"todos": []any{}},
		nil,
	); !allowed || reason != "" {
		t.Fatalf("deterministic safe action = (%v, %q)", allowed, reason)
	}
	firstAllowed, firstReason := engine.wrappedCanUseTool(
		context.Background(),
		"Bash",
		map[string]any{"command": "true"},
		nil,
	)
	secondAllowed, secondReason := engine.wrappedCanUseTool(
		context.Background(),
		"Bash",
		map[string]any{"command": "true"},
		nil,
	)
	if firstAllowed ||
		secondAllowed ||
		firstReason == "" ||
		firstReason != secondReason {
		t.Fatalf(
			"human-required action was not one stable deny: first=(%v,%q) second=(%v,%q)",
			firstAllowed,
			firstReason,
			secondAllowed,
			secondReason,
		)
	}
}

func TestP221bAutoCapabilityMatrixRequiresHumanBeforeClassifier(t *testing.T) {
	var classifierCalls atomic.Int32
	var promptCalls atomic.Int32
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:            t.TempDir(),
		PermissionMode: permission.ModeAuto,
		ChatModel: &funcModel{fn: func(
			context.Context,
			[]*schema.Message,
			...model.Option,
		) (*schema.Message, error) {
			classifierCalls.Add(1)
			return &schema.Message{
				Role:    schema.Assistant,
				Content: "<allow/>",
			}, nil
		}},
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			promptCalls.Add(1)
			return PermissionInteractionResult{
				Decision: PermissionAllowOnce,
			}
		},
	})
	t.Cleanup(engine.Close)

	engine.toolRegistry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "mcp__demo__status",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"value": {
						Type:     schema.String,
						Required: true,
					},
				},
			),
		},
		Capabilities: tools.ToolCapabilities{
			Declared:   true,
			Origin:     tools.ToolOriginMCP,
			ActionKind: tools.ToolActionDynamic,
			Network:    true,
			Dynamic:    true,
		},
	})
	cases := []struct {
		toolName string
		input    map[string]any
	}{
		{
			toolName: "Bash",
			input:    map[string]any{"command": "true"},
		},
		{
			toolName: "Agent",
			input: map[string]any{
				"description": "bounded work",
				"prompt":      "do work",
			},
		},
		{
			toolName: "WebFetch",
			input: map[string]any{
				"url":    "https://example.test",
				"prompt": "summarize",
			},
		},
		{
			toolName: "WebSearch",
			input:    map[string]any{"query": "current status"},
		},
		{
			toolName: "mcp_tool",
			input: map[string]any{
				"tool":      "status",
				"arguments": map[string]any{},
			},
		},
		{
			toolName: "mcp__demo__status",
			input:    map[string]any{"value": "current"},
		},
	}
	for _, test := range cases {
		t.Run(test.toolName, func(t *testing.T) {
			before := promptCalls.Load()
			outcome := engine.evaluateInvocationPolicy(
				withToolUseID(
					context.Background(),
					"capability-"+test.toolName,
				),
				nil,
				test.toolName,
				test.input,
				nil,
			)
			if outcome.Decision != invocationPolicyRequireHuman ||
				!outcome.Allowed {
				t.Fatalf("outcome = %#v", outcome)
			}
			if promptCalls.Load() != before+1 {
				t.Fatalf(
					"prompt calls = %d, want %d",
					promptCalls.Load(),
					before+1,
				)
			}
		})
	}
	if classifierCalls.Load() != 0 {
		t.Fatalf(
			"human-required capabilities reached classifier %d time(s)",
			classifierCalls.Load(),
		)
	}
}

func TestP221bAutoRuleAuthorityAndPrecedence(t *testing.T) {
	const command = "go test ./engine"
	var prompts atomic.Int32
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:            t.TempDir(),
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			prompts.Add(1)
			return true, ""
		},
	})
	t.Cleanup(engine.Close)

	tests := []struct {
		name        string
		rules       []permission.PermissionRule
		wantAllowed bool
		wantPrompts int32
	}{
		{
			name: "exact local allow",
			rules: []permission.PermissionRule{{
				ToolName:     "Bash",
				InputPattern: command,
				Action:       permission.ActionAllow,
				Source:       permission.SourceLocal,
			}},
			wantAllowed: true,
		},
		{
			name: "exact user allow",
			rules: []permission.PermissionRule{{
				ToolName:     "Bash",
				InputPattern: command,
				Action:       permission.ActionAllow,
				Source:       permission.SourceUser,
			}},
			wantAllowed: true,
		},
		{
			name: "exact project allow requires person",
			rules: []permission.PermissionRule{{
				ToolName:     "Bash",
				InputPattern: command,
				Action:       permission.ActionAllow,
				Source:       permission.SourceProject,
			}},
			wantAllowed: true,
			wantPrompts: 1,
		},
		{
			name: "broad user allow requires person",
			rules: []permission.PermissionRule{{
				ToolName:     "Bash",
				InputPattern: "go *",
				Action:       permission.ActionAllow,
				Source:       permission.SourceUser,
			}},
			wantAllowed: true,
			wantPrompts: 1,
		},
		{
			name: "ask beats exact allow",
			rules: []permission.PermissionRule{
				{
					ToolName:     "Bash",
					InputPattern: command,
					Action:       permission.ActionAllow,
					Source:       permission.SourceLocal,
				},
				{
					ToolName:     "Bash",
					InputPattern: command,
					Action:       permission.ActionAsk,
					Source:       permission.SourceProject,
				},
			},
			wantAllowed: true,
			wantPrompts: 1,
		},
		{
			name: "deny beats ask and allow",
			rules: []permission.PermissionRule{
				{
					ToolName:     "Bash",
					InputPattern: command,
					Action:       permission.ActionAllow,
					Source:       permission.SourceLocal,
				},
				{
					ToolName:     "Bash",
					InputPattern: command,
					Action:       permission.ActionAsk,
					Source:       permission.SourceUser,
				},
				{
					ToolName:     "Bash",
					InputPattern: command,
					Action:       permission.ActionDeny,
					Source:       permission.SourceProject,
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompts.Store(0)
			engine.mu.Lock()
			engine.permissionRules = permission.NewRulesEngine(test.rules)
			engine.mu.Unlock()
			allowed, _ := engine.wrappedCanUseTool(
				context.Background(),
				"Bash",
				map[string]any{"command": command},
				nil,
			)
			if allowed != test.wantAllowed ||
				prompts.Load() != test.wantPrompts {
				t.Fatalf(
					"permission = allowed:%v prompts:%d, want allowed:%v prompts:%d",
					allowed,
					prompts.Load(),
					test.wantAllowed,
					test.wantPrompts,
				)
			}
		})
	}
}

func TestP221bTypedSessionGrantDoesNotWidenAuto(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	var prompts atomic.Int32
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:            root,
		PermissionMode: permission.ModeAuto,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			prompts.Add(1)
			return false, "human denied"
		},
	})
	t.Cleanup(engine.Close)

	command := map[string]any{"command": "go test ./engine"}
	if err := engine.ApproveForSession("Bash", command); err != nil {
		t.Fatal(err)
	}
	if allowed, reason := engine.wrappedCanUseTool(
		context.Background(),
		"Bash",
		command,
		nil,
	); !allowed || reason != "" {
		t.Fatalf("exact command grant = (%v, %q)", allowed, reason)
	}
	if allowed, _ := engine.wrappedCanUseTool(
		context.Background(),
		"Bash",
		map[string]any{"command": "go test ./tools"},
		nil,
	); allowed {
		t.Fatal("exact command grant widened to another command")
	}

	contained := filepath.Join(root, "pkg", "file.go")
	if err := engine.ApproveForSession(
		"Read",
		map[string]any{"file_path": contained},
	); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := engine.wrappedCanUseTool(
		context.Background(),
		"Read",
		map[string]any{
			"file_path": filepath.Join(root, "pkg", "other.go"),
		},
		nil,
	); !allowed {
		t.Fatal("contained read scope did not authorize the same root")
	}
	if allowed, _ := engine.wrappedCanUseTool(
		context.Background(),
		"Read",
		map[string]any{
			"file_path": filepath.Join(external, "outside.go"),
		},
		nil,
	); allowed {
		t.Fatal("contained read scope crossed the working-root boundary")
	}
	if prompts.Load() != 2 {
		t.Fatalf("near-miss grants prompted %d times, want 2", prompts.Load())
	}
}

func TestP221bPreToolRewriteRevalidatesOnceBeforePermission(t *testing.T) {
	registry, _ := p221bPolicyActionRegistry(nil)
	var hookCalls atomic.Int32
	var permissionCalls atomic.Int32
	var executions atomic.Int32
	hookExecutor := hooks.NewExecutor()
	hookExecutor.RegisterPreTool(func(
		context.Context,
		string,
		string,
		map[string]any,
	) *hooks.PreToolHookResult {
		hookCalls.Add(1)
		return &hooks.PreToolHookResult{
			UpdatedInput: map[string]any{},
		}
	})
	outcome := executeToolCall(
		context.Background(),
		QueryParams{
			ToolRegistry: registry,
			CanUseTool: func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				permissionCalls.Add(1)
				return true, ""
			},
			ToolExecutor: func(
				context.Context,
				string,
				string,
			) (string, error) {
				executions.Add(1)
				return "executed", nil
			},
		},
		hookExecutor,
		nil,
		&schema.ToolCall{
			ID: "hook-rewrite",
			Function: schema.FunctionCall{
				Name:      "PolicyAction",
				Arguments: `{"value":"initial"}`,
			},
		},
		nil,
	)
	if outcome == nil ||
		outcome.Result == nil ||
		!p221bMessageIsError(outcome.Result) {
		t.Fatalf("outcome = %#v", outcome)
	}
	if hookCalls.Load() != 1 ||
		permissionCalls.Load() != 0 ||
		executions.Load() != 0 {
		t.Fatalf(
			"hook/permission/execution = %d/%d/%d",
			hookCalls.Load(),
			permissionCalls.Load(),
			executions.Load(),
		)
	}
}

func TestP221bPermissionRewriteSettlesAndDispatchesOnlyFinalInput(
	t *testing.T,
) {
	var executed atomic.Int32
	var executedInput atomic.Value
	registry, _ := p221bPolicyActionRegistry(func(input string) {
		executed.Add(1)
		executedInput.Store(input)
	})
	var prompts atomic.Int32
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		ToolRegistry:      registry,
		PermissionMode:    permission.ModeAuto,
		CommandEntrypoint: commands.EntrypointHeadless,
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			if prompts.Add(1) == 1 {
				return PermissionInteractionResult{
					Decision: PermissionAllowSession,
					UpdatedInput: map[string]any{
						"value": "final",
					},
				}
			}
			return PermissionInteractionResult{
				Decision: PermissionDeny,
				Message:  "unexpected old input",
			}
		},
	})
	t.Cleanup(engine.Close)

	outcome := executeToolCall(
		context.Background(),
		QueryParams{
			ToolRegistry: registry,
			CanUseTool:   engine.wrappedCanUseTool,
			ToolExecutor: engine.toolExecutor,
		},
		nil,
		&ToolUseContext{Options: &ToolUseOptions{
			PermissionMode: permission.ModeAuto,
		}},
		&schema.ToolCall{
			ID: "rewrite-final",
			Function: schema.FunctionCall{
				Name:      "PolicyAction",
				Arguments: `{"value":"initial"}`,
			},
		},
		nil,
	)
	if outcome == nil ||
		outcome.Result == nil ||
		p221bMessageIsError(outcome.Result) {
		t.Fatalf("outcome = %#v", outcome)
	}
	if executed.Load() != 1 ||
		executedInput.Load() != `{"value":"final"}` {
		t.Fatalf(
			"execution = count:%d input:%v",
			executed.Load(),
			executedInput.Load(),
		)
	}
	if allowed, _ := engine.wrappedCanUseTool(
		withToolUseID(context.Background(), "rewrite-old"),
		"PolicyAction",
		map[string]any{"value": "initial"},
		nil,
	); allowed {
		t.Fatal("old input was authorized by final-input session grant")
	}
	if allowed, reason := engine.wrappedCanUseTool(
		withToolUseID(context.Background(), "rewrite-final-grant"),
		"PolicyAction",
		map[string]any{"value": "final"},
		nil,
	); !allowed || reason != "" {
		t.Fatalf("final input grant = (%v, %q)", allowed, reason)
	}
	if prompts.Load() != 2 {
		t.Fatalf("prompt calls = %d, want 2", prompts.Load())
	}
}

func TestP221bPermissionRewriteCannotBypassDenyValidationOrAccounting(
	t *testing.T,
) {
	tests := []struct {
		name          string
		finalInput    map[string]any
		rules         []permission.PermissionRule
		customInvalid string
	}{
		{
			name:       "deny rule",
			finalInput: map[string]any{"value": "blocked"},
			rules: []permission.PermissionRule{{
				ToolName:     "PolicyAction",
				InputPattern: `{"value":"blocked"}`,
				Action:       permission.ActionDeny,
				Source:       permission.SourceProject,
			}},
		},
		{
			name:       "schema invalid",
			finalInput: map[string]any{},
		},
		{
			name:          "custom invalid",
			finalInput:    map[string]any{"value": "blocked"},
			customInvalid: "blocked",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var executions atomic.Int32
			registry, _ := p221bPolicyActionRegistryWithValidation(
				func(input string) {
					executions.Add(1)
				},
				test.customInvalid,
			)
			var prompts atomic.Int32
			engine := NewQueryEngine(QueryEngineConfig{
				CWD:               t.TempDir(),
				ToolRegistry:      registry,
				PermissionMode:    permission.ModeAuto,
				CommandEntrypoint: commands.EntrypointHeadless,
				PermissionPrompt: func(
					context.Context,
					PermissionPromptRequest,
				) PermissionInteractionResult {
					prompts.Add(1)
					return PermissionInteractionResult{
						Decision:     PermissionAllowSession,
						UpdatedInput: test.finalInput,
					}
				},
			})
			t.Cleanup(engine.Close)
			if len(test.rules) > 0 {
				engine.mu.Lock()
				engine.permissionRules = permission.NewRulesEngine(test.rules)
				engine.mu.Unlock()
			}
			var events []QueryEvent
			outcome := executeToolCall(
				context.Background(),
				QueryParams{
					ToolRegistry: registry,
					CanUseTool:   engine.wrappedCanUseTool,
					ToolExecutor: engine.toolExecutor,
				},
				nil,
				&ToolUseContext{Options: &ToolUseOptions{
					PermissionMode: permission.ModeAuto,
				}},
				&schema.ToolCall{
					ID: "rewrite-denied",
					Function: schema.FunctionCall{
						Name:      "PolicyAction",
						Arguments: `{"value":"initial"}`,
					},
				},
				func(event QueryEvent) {
					if event.Type == EventPermissionRequest ||
						event.Type == EventPermissionResolved {
						events = append(events, event)
					}
				},
			)
			if outcome == nil ||
				outcome.Result == nil ||
				!p221bMessageIsError(outcome.Result) {
				t.Fatalf("outcome = %#v", outcome)
			}
			if prompts.Load() != 1 ||
				executions.Load() != 0 ||
				len(events) != 2 {
				t.Fatalf(
					"prompt/execution/events = %d/%d/%d",
					prompts.Load(),
					executions.Load(),
					len(events),
				)
			}
			if approvals := engine.approvalTracker.List(); len(approvals) != 0 {
				t.Fatalf("denied rewrite committed grant: %#v", approvals)
			}
			denials := engine.GetPermissionDenials()
			if len(denials) != 1 {
				t.Fatalf("denials = %#v", denials)
			}
			if got, ok := denials[0].Input["value"]; ok &&
				test.finalInput["value"] != got {
				t.Fatalf(
					"accounting input = %#v, want final %#v",
					denials[0].Input,
					test.finalInput,
				)
			}
		})
	}
}

func TestP221bPolicyAndRegistryDriftDenyBeforeDispatch(t *testing.T) {
	var executions atomic.Int32
	registry, implementation := p221bPolicyActionRegistry(func(string) {
		executions.Add(1)
	})
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:               t.TempDir(),
		ToolRegistry:      registry,
		PermissionMode:    permission.ModeAuto,
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(engine.Close)

	action, err := engine.buildPermissionActionDescriptor(
		"PolicyAction",
		map[string]any{"value": "approved"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.permissionRules = permission.NewRulesEngine(
		[]permission.PermissionRule{{
			ToolName:     "PolicyAction",
			InputPattern: `{"value":"approved"}`,
			Action:       permission.ActionAsk,
			Source:       permission.SourceProject,
		}},
	)
	engine.mu.Unlock()
	if _, err := engine.toolExecutor(
		withPermissionDispatchAction(
			context.Background(),
			action,
			nil,
		),
		"PolicyAction",
		action.CanonicalInput,
	); err == nil ||
		!strings.Contains(err.Error(), "policy changed") {
		t.Fatalf("policy drift error = %v", err)
	}

	engine.mu.Lock()
	engine.permissionRules = permission.NewRulesEngine(nil)
	engine.mu.Unlock()
	action, err = engine.buildPermissionActionDescriptor(
		"PolicyAction",
		map[string]any{"value": "approved"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	updated := implementation
	updated.Execute = func(string) (string, error) {
		executions.Add(100)
		return "updated", nil
	}
	registry.Update("PolicyAction", updated)
	if _, err := engine.toolExecutor(
		withPermissionDispatchAction(
			context.Background(),
			action,
			nil,
		),
		"PolicyAction",
		action.CanonicalInput,
	); err == nil ||
		!strings.Contains(err.Error(), "changed before dispatch") {
		t.Fatalf("registry drift error = %v", err)
	}
	if executions.Load() != 0 {
		t.Fatalf("drift executed tool %d time(s)", executions.Load())
	}
}

func TestP221bResolvedPathDriftDeniesBeforeDispatch(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	linkPath := filepath.Join(workspace, "target")
	if err := os.Mkdir(linkPath, 0o755); err != nil {
		t.Fatal(err)
	}

	var executions atomic.Int32
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Write",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"file_path": {
						Type:     schema.String,
						Required: true,
					},
					"content": {
						Type:     schema.String,
						Required: true,
					},
				},
			),
		},
		Capabilities: tools.ToolCapabilities{
			Declared:   true,
			Origin:     tools.ToolOriginBuiltin,
			ActionKind: tools.ToolActionWrite,
		},
		Execute: func(string) (string, error) {
			executions.Add(1)
			return "unexpected", nil
		},
	})
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:          workspace,
		ToolRegistry: registry,
	})
	t.Cleanup(engine.Close)

	action, err := engine.buildPermissionActionDescriptor(
		"Write",
		map[string]any{
			"file_path": filepath.Join(linkPath, "out.txt"),
			"content":   "approved",
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !action.PathWithinRoots {
		t.Fatalf("initial path is not contained: %#v", action.Path)
	}
	if err := os.Rename(linkPath, filepath.Join(workspace, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.toolExecutor(
		withPermissionDispatchAction(
			context.Background(),
			action,
			nil,
		),
		"Write",
		action.CanonicalInput,
	); err == nil ||
		!strings.Contains(err.Error(), "changed before dispatch") {
		t.Fatalf("resolved path drift error = %v", err)
	}
	if executions.Load() != 0 {
		t.Fatalf("resolved path drift executed tool %d time(s)", executions.Load())
	}
}

func TestP221bCommitTransitionRejectsUnrelatedPolicyDrift(t *testing.T) {
	registry, _ := p221bPolicyActionRegistry(nil)
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:          t.TempDir(),
		ToolRegistry: registry,
	})
	t.Cleanup(engine.Close)
	action, err := engine.buildPermissionActionDescriptor(
		"PolicyAction",
		map[string]any{"value": "approved"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	before := engine.effectivePolicySnapshot(nil)
	if err := engine.ApproveForSession(
		action.CanonicalToolName,
		action.Input,
	); err != nil {
		t.Fatal(err)
	}
	afterExpected := engine.effectivePolicySnapshot(nil)
	if !engine.permissionCommitTransitionExpected(
		before,
		afterExpected,
		action,
		PermissionAllowSession,
	) {
		t.Fatal("exact session grant was not recognized as the owned transition")
	}
	engine.mu.Lock()
	engine.permissionRules = permission.NewRulesEngine(
		[]permission.PermissionRule{{
			ToolName:     "PolicyAction",
			InputPattern: `{"value":"approved"}`,
			Action:       permission.ActionAsk,
			Source:       permission.SourceProject,
		}},
	)
	engine.mu.Unlock()
	afterDrift := engine.effectivePolicySnapshot(nil)
	if engine.permissionCommitTransitionExpected(
		before,
		afterDrift,
		action,
		PermissionAllowSession,
	) {
		t.Fatal("unrelated ask-rule drift was masked as the session grant")
	}
}

func p221bPolicyActionRegistry(
	onExecute func(string),
) (*tools.Registry, tools.ToolImpl) {
	return p221bPolicyActionRegistryWithValidation(
		onExecute,
		"",
	)
}

func p221bMessageIsError(message *schema.Message) bool {
	if message == nil {
		return false
	}
	isError, _ := message.Extra["is_error"].(bool)
	return isError
}

func p221bPolicyActionRegistryWithValidation(
	onExecute func(string),
	invalidValue string,
) (*tools.Registry, tools.ToolImpl) {
	registry := tools.NewRegistry()
	implementation := tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "PolicyAction",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"value": {
						Type:     schema.String,
						Required: true,
					},
				},
			),
		},
		Capabilities: tools.ToolCapabilities{
			Declared:   true,
			Origin:     tools.ToolOriginBuiltin,
			ActionKind: tools.ToolActionRuntimeState,
		},
		ValidateInput: func(input map[string]any) error {
			if invalidValue != "" &&
				input["value"] == invalidValue {
				return errors.New("custom validation rejected value")
			}
			return nil
		},
		Execute: func(input string) (string, error) {
			if onExecute != nil {
				onExecute(input)
			}
			return "executed " + input, nil
		},
	}
	registry.Register(implementation)
	return registry, implementation
}
