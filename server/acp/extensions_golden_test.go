package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

func TestHookAndPermissionExtensionsGolden(t *testing.T) {
	conn, client, agent := setupTestACPWithAgent(t, &mockChatModel{responses: []*schema.Message{{Role: schema.Assistant, Content: "done"}}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber, ClientCapabilities: acpsdk.ClientCapabilities{},
	}); err != nil {
		t.Fatal(err)
	}

	sessionID := acpsdk.SessionId("golden-session")
	events := []engine.QueryEvent{
		{Type: engine.EventPermissionRequest, PermissionRequest: &engine.PermissionRequestEvent{ToolName: "Bash", ToolUseID: "permission-1"}},
		{Type: engine.EventPermissionResolved, PermissionResolved: &engine.PermissionResolvedEvent{ToolUseID: "permission-1", Decision: "allow", Message: "Approved for session"}},
		{Type: engine.EventHookStatus, HookStatus: &engine.HookStatusEvent{HookName: "lint", StatusMessage: "Running lint check...", Phase: "running"}},
		{Type: engine.EventHookResponse, HookResponse: &engine.HookResponseEvent{HookID: "hook-1", HookName: "lint", StatusMessage: "Lint check failed", Phase: "completed", Outcome: "failed", ExitCode: 1}},
	}
	for _, event := range events {
		if err := agent.streamEvent(ctx, sessionID, event); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for len(client.getExtensions()) < len(events) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	extensions := client.getExtensions()
	if len(extensions) != len(events) {
		t.Fatalf("extension count = %d, want %d: %#v", len(extensions), len(events), extensions)
	}

	normalized := make([]map[string]any, 0, len(extensions))
	for _, extension := range extensions {
		var params map[string]any
		if err := json.Unmarshal(extension.Params, &params); err != nil {
			t.Fatal(err)
		}
		delete(params, "timestamp")
		normalized = append(normalized, map[string]any{"method": extension.Method, "params": params})
	}
	actual, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	expected, err := os.ReadFile(filepath.Join("testdata", "hook_permission_extensions.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("ACP extension golden mismatch\nactual:\n%s\nexpected:\n%s", actual, expected)
	}
}
