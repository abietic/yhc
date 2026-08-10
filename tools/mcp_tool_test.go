package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/mcp"
)

func TestP420MCPManagerRejectsSharedRootReplacementBeforeServerOwnership(t *testing.T) {
	cwd := t.TempDir()
	first := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointACP)
	replacement := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointPlain)
	manager := NewMCPToolManager()
	if err := manager.BindExecutionPolicy(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.BindExecutionPolicy(replacement); err == nil {
		t.Fatal("unstarted MCP manager accepted a different root policy")
	}
	if manager.ExecutionPolicyDigest() != first.Digest() {
		t.Fatalf("manager policy changed to %q", manager.ExecutionPolicyDigest())
	}
}

func TestMCPInspectionManagerReportsConfiguredServersWithoutSecretsOrConnections(t *testing.T) {
	manager := NewMCPInspectionManager(&mcp.MCPConfig{Servers: map[string]*mcp.MCPServerConfig{
		"remote": {
			Name:    "remote",
			Enabled: true,
			URL:     "https://user:secret@example.test/path?token=secret",
			Headers: map[string]string{"Authorization": "Bearer secret"},
		},
		"local": {
			Name:    "local",
			Enabled: false,
			Command: "secret-command",
			Args:    []string{"--token", "secret"},
			Env:     map[string]string{"API_KEY": "secret"},
		},
	}})

	snapshot := manager.InventorySnapshot()
	if snapshot.Revision != 1 || snapshot.Source != "configuration" || len(snapshot.Servers) != 2 {
		t.Fatalf("inspection snapshot = %#v", snapshot)
	}
	if snapshot.Servers[0].Name != "local" || snapshot.Servers[0].Status != "disabled" ||
		snapshot.Servers[1].Name != "remote" || snapshot.Servers[1].Status != "configured" {
		t.Fatalf("inspection servers = %#v", snapshot.Servers)
	}
	for _, server := range snapshot.Servers {
		if server.Source != "configuration" || server.Health != "unprobed" ||
			server.Diagnostic != "inspection_only_no_connection" || len(server.Tools) != 0 {
			t.Fatalf("inspection server = %#v", server)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"secret-command",
		"Bearer secret",
		"user:secret",
		"API_KEY",
		"--token",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("inspection snapshot exposed %q: %s", secret, encoded)
		}
	}
	if len(manager.clients) != 0 || len(manager.tools) != 0 || len(manager.failures) != 0 {
		t.Fatalf("inspection manager created runtime state: %#v", manager)
	}
}
