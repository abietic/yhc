package appserver

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/abietic/yhc/tools"
)

type mcpInventoryEngine struct {
	*taskExplorerEngine
	inventory tools.MCPInventorySnapshot
	reads     atomic.Int32
}

func (e *mcpInventoryEngine) MCPInventorySnapshot() tools.MCPInventorySnapshot {
	e.reads.Add(1)
	return e.inventory
}

func TestMCPInventoryOwnedReadOnlyRoute(t *testing.T) {
	e := &mcpInventoryEngine{taskExplorerEngine: &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{}, false)}}
	e.inventory = tools.MCPInventorySnapshot{Revision: 1<<54 + 1, Source: "runtime", Servers: []tools.MCPServerSnapshot{{Name: "docs", Source: "runtime-manager", Status: "connected", Health: "healthy", Tools: []*tools.MCPToolInfo{{ToolName: "search", Description: "Search docs", InputSchema: map[string]any{"secret": "fixture-private-schema"}}}}}}
	server, hs := newTaskExplorerServer(t, e)
	endpoint := hs.URL + "/v1/sessions/owned-session/mcp-inventory"
	r := taskExplorerRequest(t, endpoint, http.MethodGet)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", r.StatusCode, readBody(t, r))
	}
	var got mcpInventoryResponse
	decodeResponse(t, r, &got)
	if got.SessionID != "owned-session" || got.Revision != strconv.FormatUint(e.inventory.Revision, 10) || len(got.Servers) != 1 || got.Servers[0].Tools[0].Name != "search" {
		t.Fatalf("projection=%+v", got)
	}
	if strings.Contains(string(mustJSON(t, got)), "fixture-private-schema") || e.reads.Load() != 1 || e.calls.Load() != 0 || e.mutations.Load() != 0 {
		t.Fatal("read exposed schema or entered another runtime owner")
	}
	for _, path := range []string{"unknown/mcp-inventory", "owned-session/mcp-inventory?path=private"} {
		r := taskExplorerRequest(t, hs.URL+"/v1/sessions/"+path, http.MethodGet)
		if r.StatusCode != 404 && r.StatusCode != 400 {
			t.Fatalf("path=%s status=%d", path, r.StatusCode)
		}
		r.Body.Close()
	}
	r, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", r.StatusCode)
	}
	r.Body.Close()
	r = taskExplorerRequest(t, endpoint, http.MethodPost)
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("mutation status=%d", r.StatusCode)
	}
	r.Body.Close()
	server.sessions["owned-session"].mu.Lock()
	server.sessions["owned-session"].closed = true
	server.sessions["owned-session"].mu.Unlock()
	r = taskExplorerRequest(t, endpoint, http.MethodGet)
	if r.StatusCode != 404 || e.reads.Load() != 1 {
		t.Fatal("closed session entered MCP reader")
	}
	r.Body.Close()
}

func TestMCPInventoryUnavailableAdapter(t *testing.T) {
	e := &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{}, false)}
	_, hs := newTaskExplorerServer(t, e)
	r := taskExplorerRequest(t, hs.URL+"/v1/sessions/owned-session/mcp-inventory", http.MethodGet)
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotImplemented || e.mutations.Load() != 0 {
		t.Fatalf("status=%d", r.StatusCode)
	}
}

func TestMCPInventoryProjectionBoundsAndFixedDiagnostics(t *testing.T) {
	snapshot := tools.MCPInventorySnapshot{Revision: ^uint64(0), Source: "/private/fixture"}
	for i := 0; i < 70; i++ {
		server := tools.MCPServerSnapshot{Name: strings.Repeat("界", 600), Source: "/private/fixture", Status: "custom-secret", Health: "custom-secret", Diagnostic: "connect /private/fixture with credential"}
		for j := 0; j < 70; j++ {
			server.Tools = append(server.Tools, &tools.MCPToolInfo{ToolName: "tool", Description: strings.Repeat("界", 600), InputSchema: map[string]any{"secret": "private-schema"}})
		}
		snapshot.Servers = append(snapshot.Servers, server)
	}
	got := projectMCPInventory("owned-session", snapshot)
	if got.Revision != "18446744073709551615" || len(got.Servers) != 64 || got.HiddenServers != 6 || got.HiddenTools != 70*70-512 {
		t.Fatalf("bounds=%+v", got)
	}
	count := 0
	for _, server := range got.Servers {
		if !utf8.ValidString(server.Name) || utf8.RuneCountInString(server.Name) != 512 || !server.Truncated || server.Status != "unknown" || server.Health != "unknown" || server.Diagnostic != "unavailable" {
			t.Fatal("unbounded or unsafe server metadata")
		}
		if len(server.Tools) > 64 || server.HiddenTools != 70-len(server.Tools) {
			t.Fatal("per-server tool bound")
		}
		for _, tool := range server.Tools {
			if !tool.Truncated || utf8.RuneCountInString(tool.Description) != 512 {
				t.Fatal("tool text bound")
			}
		}
		count += len(server.Tools)
	}
	if count != 512 || strings.Contains(string(mustJSON(t, got)), "private") {
		t.Fatal("unbounded or private projection")
	}
}
