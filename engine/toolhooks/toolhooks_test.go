package toolhooks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/permission"
)

func TestRunPreHooksPermissionRulesDenyAndAskBlockBeforeHooks(t *testing.T) {
	tests := []struct {
		name       string
		action     permission.PermissionAction
		wantReason string
	}{
		{
			name:       "deny",
			action:     permission.ActionDeny,
			wantReason: "permission rule denied tool Bash",
		},
		{
			name:       "ask",
			action:     permission.ActionAsk,
			wantReason: "permission rule requires confirmation for tool Bash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := permission.NewRulesEngine([]permission.PermissionRule{{
				ToolName:     "Bash",
				InputPattern: "rm*",
				Action:       tt.action,
			}})
			runner := NewToolHookRunner(rules, nil)
			called := false
			runner.RegisterPreHook(func(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error) {
				called = true
				return &PreToolResult{Allowed: true}, nil
			})

			result, err := runner.RunPreHooks(context.Background(), &ToolHookContext{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": "rm -rf /tmp/project"},
			})
			if err != nil {
				t.Fatalf("RunPreHooks returned error: %v", err)
				return
			}
			if result == nil || result.Allowed {
				t.Fatalf("expected blocked result, got %#v", result)
				return
			}
			if result.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", result.Reason, tt.wantReason)
			}
			if called {
				t.Fatal("programmatic pre-hook should not run after permission block")
			}
		})
	}
}

func TestRunPreHooksChainsModifiedInputAndSkipExecution(t *testing.T) {
	runner := NewToolHookRunner(permission.NewRulesEngine([]permission.PermissionRule{{
		ToolName: "*",
		Action:   permission.ActionAllow,
	}}), nil)

	var secondSaw string
	runner.RegisterPreHook(func(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error) {
		return &PreToolResult{
			Allowed:       true,
			ModifiedInput: map[string]any{"command": "echo from first hook"},
		}, nil
	})
	runner.RegisterPreHook(func(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error) {
		secondSaw, _ = hookCtx.ToolInput["command"].(string)
		return &PreToolResult{
			Allowed:       true,
			ModifiedInput: map[string]any{"command": secondSaw + " and second hook"},
			SkipExecution: true,
		}, nil
	})

	result, err := runner.RunPreHooks(context.Background(), &ToolHookContext{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "original"},
	})
	if err != nil {
		t.Fatalf("RunPreHooks returned error: %v", err)
		return
	}
	if result == nil || !result.Allowed || !result.SkipExecution {
		t.Fatalf("expected allowed skip result, got %#v", result)
		return
	}
	if secondSaw != "echo from first hook" {
		t.Fatalf("second hook saw %q", secondSaw)
	}
	if got := result.ModifiedInput["command"]; got != "echo from first hook and second hook" {
		t.Fatalf("modified command = %#v", got)
	}
}

func TestRunPreHooksDenyWinsAndPreservesPreviousModification(t *testing.T) {
	runner := NewToolHookRunner(nil, nil)
	runner.RegisterPreHook(func(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error) {
		return &PreToolResult{
			Allowed:       true,
			ModifiedInput: map[string]any{"file_path": "/tmp/safe"},
		}, nil
	})
	runner.RegisterPreHook(func(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error) {
		return &PreToolResult{Allowed: false, Reason: "blocked by policy"}, nil
	})

	result, err := runner.RunPreHooks(context.Background(), &ToolHookContext{
		ToolName:  "Write",
		ToolInput: map[string]any{"file_path": "/tmp/original"},
	})
	if err != nil {
		t.Fatalf("RunPreHooks returned error: %v", err)
		return
	}
	if result == nil || result.Allowed || result.Reason != "blocked by policy" {
		t.Fatalf("unexpected deny result: %#v", result)
		return
	}
	if got := result.ModifiedInput["file_path"]; got != "/tmp/safe" {
		t.Fatalf("expected prior modified input to be preserved, got %#v", result.ModifiedInput)
	}
}

func TestRunPreHooksPropagatesHookError(t *testing.T) {
	runner := NewToolHookRunner(nil, nil)
	runner.RegisterPreHook(func(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error) {
		return nil, errors.New("hook exploded")
	})

	_, err := runner.RunPreHooks(context.Background(), &ToolHookContext{ToolName: "Read"})
	if err == nil || !strings.Contains(err.Error(), "pre-hook error: hook exploded") {
		t.Fatalf("expected wrapped pre-hook error, got %v", err)
		return
	}
}

func TestRunPostHooksChainsOutputAndAbort(t *testing.T) {
	runner := NewToolHookRunner(nil, nil)
	runner.RegisterPostHook(func(ctx context.Context, hookCtx *ToolHookContext, result string) (*PostToolResult, error) {
		if result != "raw output" {
			t.Fatalf("first hook saw %q", result)
		}
		return &PostToolResult{ModifiedOutput: "redacted output"}, nil
	})
	runner.RegisterPostHook(func(ctx context.Context, hookCtx *ToolHookContext, result string) (*PostToolResult, error) {
		if result != "redacted output" {
			t.Fatalf("second hook saw %q", result)
		}
		return &PostToolResult{
			ModifiedOutput: "final output",
			ShouldAbort:    true,
			AbortReason:    "post hook requested stop",
		}, nil
	})

	result, err := runner.RunPostHooks(context.Background(), &ToolHookContext{ToolName: "Read"}, "raw output")
	if err != nil {
		t.Fatalf("RunPostHooks returned error: %v", err)
		return
	}
	if result == nil || result.ModifiedOutput != "final output" || !result.ShouldAbort || result.AbortReason != "post hook requested stop" {
		t.Fatalf("unexpected post-hook result: %#v", result)
		return
	}
}

func TestRunPostHooksPropagatesHookError(t *testing.T) {
	runner := NewToolHookRunner(nil, nil)
	runner.RegisterPostHook(func(ctx context.Context, hookCtx *ToolHookContext, result string) (*PostToolResult, error) {
		return nil, errors.New("post failed")
	})

	_, err := runner.RunPostHooks(context.Background(), &ToolHookContext{ToolName: "Bash"}, "output")
	if err == nil || !strings.Contains(err.Error(), "post-hook error: post failed") {
		t.Fatalf("expected wrapped post-hook error, got %v", err)
		return
	}
}

func TestTryParseModifiedInput(t *testing.T) {
	parsed := tryParseModifiedInput(`{"command":"echo hi","timeout":3}`)
	if parsed == nil || parsed["command"] != "echo hi" || parsed["timeout"].(float64) != 3 {
		t.Fatalf("unexpected parsed input: %#v", parsed)
		return
	}

	if got := tryParseModifiedInput("not json"); got != nil {
		t.Fatalf("expected non-JSON output to be ignored, got %#v", got)
		return
	}
	if got := tryParseModifiedInput(`["not","object"]`); got != nil {
		t.Fatalf("expected non-object JSON output to be ignored, got %#v", got)
		return
	}
}
