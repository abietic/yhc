package engine

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func sharedConfigEngineFixture(t *testing.T, entrypoint commands.Entrypoint, agentID string) *QueryEngine {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), SessionID: "config-fixture", AgentID: agentID,
		ToolRegistry: registry, Model: "running-model", PermissionMode: permission.ModeDefault,
		CommandEntrypoint: entrypoint,
		PermissionPrompt: func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
			return PermissionInteractionResult{Decision: PermissionAllowOnce}
		},
	})
	t.Cleanup(eng.Close)
	return eng
}

func TestSharedConfigToolUsesEngineWorkspaceWithoutLiveRebinding(t *testing.T) {
	for _, item := range []struct {
		name       string
		entrypoint commands.Entrypoint
		agentID    string
	}{
		{"tui", commands.EntrypointTUI, ""},
		{"plain", commands.EntrypointPlain, ""},
		{"headless", commands.EntrypointHeadless, ""},
		{"acp", commands.EntrypointACP, ""},
		{"child", commands.EntrypointHeadless, "child-fixture"},
	} {
		t.Run(item.name, func(t *testing.T) {
			eng := sharedConfigEngineFixture(t, item.entrypoint, item.agentID)
			other := t.TempDir()
			ctx := tools.WithExecutionCWD(t.Context(), other)
			for _, input := range []map[string]any{
				{"setting": "model", "value": "next-model"},
				{"setting": "permission_mode", "value": "acceptEdits"},
			} {
				var settled *PermissionActionDescriptor
				outcome := eng.evaluateInvocationPolicy(withSettledPermissionActionPtr(withToolUseID(ctx, "config-"+input["setting"].(string)), &settled), nil, "Config", input, nil)
				if !outcome.Allowed || settled == nil {
					t.Fatalf("Config admission = %#v", outcome)
				}
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := eng.toolExecutor(withPermissionDispatchAction(ctx, *settled, nil), "Config", string(encoded)); err != nil {
					t.Fatal(err)
				}
			}
			if eng.config.Model != "running-model" || eng.PermissionMode() != permission.ModeDefault {
				t.Fatal("persisted defaults rebound the running session")
			}
			snapshot, err := engineconfig.SettingsSnapshot(eng.config.CWD, "project")
			if err != nil || string(snapshot.Fields["model"].StoredValue) != `"next-model"` || string(snapshot.Fields["permission_mode"].StoredValue) != `"acceptEdits"` {
				t.Fatal("Config did not use engine-owned project settings")
			}
			if _, err := os.Stat(engineconfig.ProjectConfigPath(other)); !os.IsNotExist(err) {
				t.Fatal("Config used caller cwd instead of engine cwd")
			}
		})
	}
}

func TestSharedConfigToolPermissionAndProjectionRemainAuthoritative(t *testing.T) {
	eng := sharedConfigEngineFixture(t, commands.EntrypointTUI, "")
	var visible bool
	for _, info := range eng.modelVisibleTools() {
		visible = visible || info.Name == "Config"
	}
	impl, ok := eng.toolRegistry.Get("Config")
	if !visible || !ok || !impl.NeedsPermissions || impl.IsReadOnly {
		t.Fatal("Config lost write-tool permission metadata")
	}
	input := map[string]any{"setting": "model", "value": "blocked-model"}
	action, err := eng.buildPermissionActionDescriptor("Config", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Config", Action: permission.ActionDeny, Source: permission.SourceProject}})
	eng.mu.Unlock()
	outcome := eng.evaluateInvocationPolicy(withToolUseID(t.Context(), "denied-config"), nil, "Config", input, nil)
	if outcome.Allowed {
		t.Fatal("Config bypassed explicit deny")
	}
	if _, err := eng.toolExecutor(withPermissionDispatchAction(t.Context(), action, nil), "Config", action.CanonicalInput); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal("Config dispatched after permission binding drift")
	}
	for _, info := range eng.modelVisibleTools() {
		if info.Name == "Config" {
			t.Fatal("denied Config remained model visible")
		}
	}
	if _, err := os.Stat(engineconfig.ProjectConfigPath(eng.config.CWD)); !os.IsNotExist(err) {
		t.Fatal("denied Config wrote settings")
	}
	eng.mu.Lock()
	eng.permissionRules = permission.NewRulesEngine(nil)
	eng.mu.Unlock()
	eng.SetPermissionMode(permission.ModePlan)
	for _, info := range eng.modelVisibleTools() {
		if info.Name == "Config" {
			t.Fatal("Plan mode projected a configuration writer")
		}
	}
	if _, ok := eng.toolRegistry.Get("Config"); !ok {
		t.Fatal("projection removed Config from the runtime registry")
	}
}

func TestSharedConfigToolCanonicalAdmissionUsesTypedValuesAndRedactedErrors(t *testing.T) {
	eng := sharedConfigEngineFixture(t, commands.EntrypointTUI, "")
	params := QueryParams{ToolRegistry: eng.toolRegistry, CanUseTool: eng.wrappedCanUseTool, ToolExecutor: eng.toolExecutor}
	toolCtx := &ToolUseContext{SessionID: eng.config.SessionID, Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	for _, input := range []string{
		`{"setting":"reduced_motion","value":false}`,
		`{"setting":"max_turns","value":0}`,
		`{"setting":"max_turns","value":null}`,
		`{"action":"list","setting":"_"}`,
	} {
		outcome := executeToolCall(t.Context(), params, nil, toolCtx, toolCallWithArgs("config-canonical", "Config", input), nil)
		if outcome == nil || outcome.Result == nil || outcome.Result.Extra["is_error"] == true {
			if outcome != nil && outcome.Result != nil {
				t.Fatal(outcome.Result.Content)
			}
			t.Fatal("canonical Config call did not complete")
		}
	}
	for _, input := range []string{
		`{"setting":"private-fixture-sentinel","value":"private-fixture-sentinel"}`,
		`{"setting":"model","scope":"private-fixture-sentinel","value":"private-fixture-sentinel"}`,
		`{"setting":"model","action":"private-fixture-sentinel","value":"private-fixture-sentinel"}`,
	} {
		outcome := executeToolCall(t.Context(), params, nil, toolCtx, toolCallWithArgs("invalid-config-canonical", "Config", input), nil)
		if outcome == nil || outcome.Result == nil || outcome.Result.Extra["is_error"] != true || strings.Contains(outcome.Result.Content, "private-fixture-sentinel") {
			t.Fatal("canonical Config invalid input was admitted or disclosed")
		}
	}
}

func TestSharedConfigToolCanonicalPreservesIntegerPrecision(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("large integer string exceeds this platform's shared int setting")
	}
	eng := sharedConfigEngineFixture(t, commands.EntrypointTUI, "")
	params := QueryParams{ToolRegistry: eng.toolRegistry, CanUseTool: eng.wrappedCanUseTool, ToolExecutor: eng.toolExecutor}
	toolCtx := &ToolUseContext{SessionID: eng.config.SessionID, Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	for _, value := range []string{"9007199254740992", "9007199254740993"} {
		outcome := executeToolCall(t.Context(), params, nil, toolCtx, toolCallWithArgs("config-unsafe-integer", "Config", `{"setting":"max_turns","value":`+value+`}`), nil)
		if outcome == nil || outcome.Result == nil || outcome.Result.Extra["is_error"] != true {
			t.Fatal("unsafe native integer was persisted by canonical admission")
		}
	}
	if _, err := os.Stat(engineconfig.ProjectConfigPath(eng.config.CWD)); !os.IsNotExist(err) {
		t.Fatal("canonical unsafe integer wrote settings before rejection")
	}
	for _, value := range []string{"9007199254740991", `"9007199254740993"`} {
		outcome := executeToolCall(t.Context(), params, nil, toolCtx, toolCallWithArgs("config-exact-integer", "Config", `{"setting":"max_turns","value":`+value+`}`), nil)
		if outcome == nil || outcome.Result == nil || outcome.Result.Extra["is_error"] == true {
			t.Fatal("supported exact integer was rejected")
		}
		snapshot, err := engineconfig.SettingsSnapshot(eng.config.CWD, "project")
		if err != nil || string(snapshot.Fields["max_turns"].StoredValue) != strings.Trim(value, `"`) {
			t.Fatal("canonical integer changed precision")
		}
	}
}
