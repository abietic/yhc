package tools

import "testing"

func TestMCPInventorySnapshotIncludesFailedServersDeterministically(t *testing.T) {
	manager := NewMCPToolManager()
	manager.recordFailure("zeta", "connect_failed")
	manager.recordFailure("alpha", "approval_denied")

	snapshot := manager.InventorySnapshot()
	if snapshot.Revision != 2 || len(snapshot.Servers) != 2 {
		t.Fatalf("inventory snapshot = %#v", snapshot)
	}
	if snapshot.Servers[0].Name != "alpha" ||
		snapshot.Servers[0].Status != "failed" ||
		snapshot.Servers[0].Health != "unavailable" ||
		snapshot.Servers[0].Diagnostic != "approval_denied" {
		t.Fatalf("first server = %#v", snapshot.Servers[0])
	}
	if snapshot.Servers[1].Name != "zeta" ||
		snapshot.Servers[1].Diagnostic != "connect_failed" {
		t.Fatalf("second server = %#v", snapshot.Servers[1])
	}

	snapshot.Servers[0].Name = "mutated"
	again := manager.InventorySnapshot()
	if again.Servers[0].Name != "alpha" {
		t.Fatalf("snapshot mutated live inventory: %#v", again)
	}
}

func TestMCPInventorySnapshotDeepClonesToolSchemas(t *testing.T) {
	manager := NewMCPToolManager()
	manager.clients["server"] = nil
	manager.tools["tool"] = &MCPToolInfo{
		ServerName: "server",
		ToolName:   "tool",
		InputSchema: map[string]any{
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []any{"path"},
		},
	}

	snapshot := manager.InventorySnapshot()
	if len(snapshot.Servers) != 1 || len(snapshot.Servers[0].Tools) != 1 {
		t.Fatalf("inventory snapshot = %#v", snapshot)
	}
	properties := snapshot.Servers[0].Tools[0].
		InputSchema["properties"].(map[string]any)
	properties["path"].(map[string]any)["type"] = "number"
	snapshot.Servers[0].Tools[0].
		InputSchema["required"].([]any)[0] = "mutated"

	again := manager.InventorySnapshot()
	againProperties := again.Servers[0].Tools[0].
		InputSchema["properties"].(map[string]any)
	if got := againProperties["path"].(map[string]any)["type"]; got != "string" {
		t.Fatalf("nested schema mutated live inventory: %v", got)
	}
	if got := again.Servers[0].Tools[0].
		InputSchema["required"].([]any)[0]; got != "path" {
		t.Fatalf("schema slice mutated live inventory: %v", got)
	}
}
