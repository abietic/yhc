package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestConfigureHeadlessPermissionsNeverInstallsInteractivePrompt(t *testing.T) {
	for _, mode := range []permission.Mode{permission.ModeDefault, permission.ModeBypassPermissions} {
		var stderr bytes.Buffer
		cfg := engine.QueryEngineConfig{
			PermissionMode: mode,
			PermissionPrompt: func(context.Context, engine.PermissionPromptRequest) engine.PermissionInteractionResult {
				return engine.PermissionInteractionResult{Decision: engine.PermissionAllowAlways}
			},
		}
		configureHeadlessPermissions(&cfg, &stderr)
		if cfg.PermissionPrompt != nil {
			t.Fatalf("mode %q retained an interactive permission prompt", mode)
		}

		allowed, reason := cfg.CanUseTool(context.Background(), "Bash", nil, nil)
		if mode == permission.ModeBypassPermissions {
			if !allowed || reason != "" || stderr.Len() != 0 {
				t.Fatalf("bypass result = (%v, %q), stderr %q", allowed, reason, stderr.String())
			}
			continue
		}
		if allowed || !strings.Contains(reason, "no interactive permission prompt") {
			t.Fatalf("default result = (%v, %q)", allowed, reason)
		}
		if !strings.Contains(stderr.String(), "permission denied: Bash") {
			t.Fatalf("missing diagnostic denial: %q", stderr.String())
		}
	}
}

func TestHeadlessBypassCannotFabricatePlanApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sessionID = "headless-plan-entrypoint"
	if err := tools.SavePlan(sessionID, "", "# Headless Plan\n"); err != nil {
		t.Fatal(err)
	}

	var entered atomic.Int32
	var exited atomic.Int32
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "EnterPlanMode"},
		IsPlanModeTransition: true,
		Execute: func(string) (string, error) {
			entered.Add(1)
			return "entered", nil
		},
	})
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
		IsPlanModeTransition: true,
		Execute: func(string) (string, error) {
			exited.Add(1)
			return "exited", nil
		},
	})

	workspace := t.TempDir()
	cfg := engine.QueryEngineConfig{
		SessionID:      sessionID,
		ThreadID:       "headless-plan-thread",
		TranscriptDir:  filepath.Join(workspace, "transcripts"),
		CWD:            workspace,
		PermissionMode: permission.ModeBypassPermissions,
		ChatModel: &headlessRecoveryModel{responses: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "enter-headless",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "EnterPlanMode",
						Arguments: `{}`,
					},
				}},
			},
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "exit-headless",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "ExitPlanMode",
						Arguments: `{}`,
					},
				}},
			},
			{Role: schema.Assistant, Content: "done"},
		}},
		ToolRegistry: registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"EnterPlanMode", "ExitPlanMode"},
		},
		MaxTurns: 4,
	}
	var stderr bytes.Buffer
	configureHeadlessPermissions(&cfg, &stderr)
	if cfg.PermissionPrompt != nil {
		t.Fatal("headless bypass installed an interactive Plan approval prompt")
	}

	eng := engine.NewQueryEngine(cfg)
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(context.Background(), "enter then exit Plan mode")
	var exitResult *schema.Message
	for event := range events {
		if event.Type == engine.EventToolResult &&
			event.ToolResultMessage != nil &&
			event.ToolResultMessage.ToolCallID == "exit-headless" {
			exitResult = event.ToolResultMessage
		}
	}
	if entered.Load() != 1 || exited.Load() != 0 {
		t.Fatalf(
			"transition executions enter=%d exit=%d",
			entered.Load(),
			exited.Load(),
		)
	}
	if exitResult == nil ||
		exitResult.Extra == nil ||
		exitResult.Extra["is_error"] != true ||
		!strings.Contains(
			exitResult.Content,
			"structured Plan approval prompting not available",
		) {
		t.Fatalf("headless ExitPlanMode result = %#v", exitResult)
	}
	if eng.PermissionMode() != permission.ModePlan ||
		eng.PlanState().Phase != engine.PlanPhaseActive {
		t.Fatalf(
			"headless Plan state mode=%q state=%#v",
			eng.PermissionMode(),
			eng.PlanState(),
		)
	}
}
