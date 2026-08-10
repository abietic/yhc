package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestPermissionDeniedHookRetryEmitsMetaMessage(t *testing.T) {
	ctx := context.Background()
	mdl := &singleToolCallModel{toolName: "Bash", args: `{"command":"rm -rf /"}`}
	maxTurns := 4

	hookExec := hooks.NewExecutor()
	hookExec.RegisterPermissionDenied(func(
		ctx context.Context,
		toolName string,
		toolUseID string,
		input map[string]any,
		reason string,
	) *hooks.PermissionDeniedHookResult {
		return &hooks.PermissionDeniedHookResult{Retry: true}
	})

	events, _ := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "do it"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
		ToolRegistry: permissionDeniedHookTestRegistry(),
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "dangerous command"
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			t.Fatal("tool should not execute on denial")
			return "", nil
		},
	})

	// Should see a tool_result error message followed by a retry attachment
	var foundDenial bool
	var foundRetry bool
	for _, evt := range events {
		if evt.Type == EventToolResult && evt.ToolResultMessage != nil {
			if strings.Contains(evt.ToolResultMessage.Content, "permission denied") {
				foundDenial = true
			}
		}
		if evt.Type == EventAttachment && evt.AttachmentMessage != nil {
			if kind, _ := evt.AttachmentMessage.Extra["attachment_kind"].(string); kind == "permission_denied_retry" {
				foundRetry = true
				if !strings.Contains(evt.AttachmentMessage.Content, "PermissionDenied hook") {
					t.Fatalf("unexpected retry message content: %q", evt.AttachmentMessage.Content)
				}
			}
		}
	}
	if !foundDenial {
		t.Fatal("expected permission denied tool_result event")
	}
	if !foundRetry {
		t.Fatal("expected permission_denied_retry attachment event from hook")
	}
}

func TestPermissionDeniedHookNoRetrySkipsMetaMessage(t *testing.T) {
	ctx := context.Background()
	mdl := &singleToolCallModel{toolName: "Bash", args: `{"command":"ls"}`}
	maxTurns := 4

	hookExec := hooks.NewExecutor()
	hookExec.RegisterPermissionDenied(func(
		ctx context.Context,
		toolName string,
		toolUseID string,
		input map[string]any,
		reason string,
	) *hooks.PermissionDeniedHookResult {
		return &hooks.PermissionDeniedHookResult{Retry: false}
	})

	events, _ := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "list"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
		ToolRegistry: permissionDeniedHookTestRegistry(),
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "blocked"
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "", nil
		},
	})

	for _, evt := range events {
		if evt.Type == EventAttachment && evt.AttachmentMessage != nil {
			if kind, _ := evt.AttachmentMessage.Extra["attachment_kind"].(string); kind == "permission_denied_retry" {
				t.Fatal("should not emit retry message when hook says Retry=false")
				return
			}
		}
	}
}

func TestPermissionDeniedHookReceivesCorrectParams(t *testing.T) {
	ctx := context.Background()
	mdl := &singleToolCallModel{toolName: "Bash", args: `{"command":"pwd"}`}
	maxTurns := 4

	var capturedToolName, capturedToolUseID, capturedReason string
	var capturedInput map[string]any

	hookExec := hooks.NewExecutor()
	hookExec.RegisterPermissionDenied(func(
		ctx context.Context,
		toolName string,
		toolUseID string,
		input map[string]any,
		reason string,
	) *hooks.PermissionDeniedHookResult {
		capturedToolName = toolName
		capturedToolUseID = toolUseID
		capturedInput = input
		capturedReason = reason
		return nil
	})

	collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "go"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
		ToolRegistry: permissionDeniedHookTestRegistry(),
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "no access"
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "", nil
		},
	})

	if capturedToolName != "Bash" {
		t.Fatalf("expected toolName=Bash, got %q", capturedToolName)
	}
	if capturedToolUseID != "call_1" {
		t.Fatalf("expected toolUseID=call_1, got %q", capturedToolUseID)
	}
	if capturedReason != "no access" {
		t.Fatalf("expected reason='no access', got %q", capturedReason)
	}
	if capturedInput == nil || capturedInput["command"] != "pwd" {
		t.Fatalf("expected input with command=pwd, got %v", capturedInput)
		return
	}
}

func TestPermissionDeniedHookAttachmentsEmitted(t *testing.T) {
	ctx := context.Background()
	mdl := &singleToolCallModel{toolName: "Bash", args: `{"command":"test"}`}
	maxTurns := 4

	hookExec := hooks.NewExecutor()
	hookExec.RegisterPermissionDenied(func(
		ctx context.Context,
		toolName string,
		toolUseID string,
		input map[string]any,
		reason string,
	) *hooks.PermissionDeniedHookResult {
		return &hooks.PermissionDeniedHookResult{
			Retry: false,
			Attachments: []*schema.Message{
				{Role: schema.User, Content: "hook-attachment-content", Extra: map[string]any{"is_meta": true, "attachment_kind": "custom_hook_output"}},
			},
		}
	})

	events, _ := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "go"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
		ToolRegistry: permissionDeniedHookTestRegistry(),
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
			return false, "blocked"
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "", nil
		},
	})

	var foundCustomAttachment bool
	for _, evt := range events {
		if evt.Type == EventAttachment && evt.AttachmentMessage != nil {
			if kind, _ := evt.AttachmentMessage.Extra["attachment_kind"].(string); kind == "custom_hook_output" {
				foundCustomAttachment = true
				if evt.AttachmentMessage.Content != "hook-attachment-content" {
					t.Fatalf("unexpected attachment content: %q", evt.AttachmentMessage.Content)
				}
			}
		}
	}
	if !foundCustomAttachment {
		t.Fatal("expected custom_hook_output attachment from permission denied hook")
	}
}

func permissionDeniedHookTestRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	return registry
}
