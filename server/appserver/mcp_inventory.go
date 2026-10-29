package appserver

import (
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/abietic/yhc/tools"
)

const (
	mcpInventoryServers     = 64
	mcpInventoryServerTools = 64
	mcpInventoryTools       = 512
)

type sessionMCPInventoryReader interface {
	MCPInventorySnapshot() tools.MCPInventorySnapshot
}

type mcpInventoryResponse struct {
	SessionID     string               `json:"session_id"`
	Revision      string               `json:"revision"`
	Source        string               `json:"source"`
	Servers       []mcpInventoryServer `json:"servers"`
	HiddenServers int                  `json:"hidden_servers"`
	HiddenTools   int                  `json:"hidden_tools"`
}

type mcpInventoryServer struct {
	Name        string             `json:"name"`
	Source      string             `json:"source"`
	Status      string             `json:"status"`
	Health      string             `json:"health"`
	Diagnostic  string             `json:"diagnostic"`
	Tools       []mcpInventoryTool `json:"tools"`
	HiddenTools int                `json:"hidden_tools"`
	Truncated   bool               `json:"truncated"`
}

type mcpInventoryTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Truncated   bool   `json:"truncated"`
}

// Inspection uses the existing session manager. It never loads configuration,
// connects a server, probes a tool, or mutates a registry/model generation.
func (s *Server) handleMCPInventory(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "MCP inventory does not accept query parameters")
		return
	}
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, 404, "session_not_found", "session not found")
		return
	}
	owned.turnAdmissionMu.Lock()
	defer owned.turnAdmissionMu.Unlock()
	owned.mu.Lock()
	closed := owned.closed
	owned.mu.Unlock()
	if closed {
		writeError(w, 404, "session_not_found", "session not found")
		return
	}
	reader, ok := owned.engine.(sessionMCPInventoryReader)
	if !ok {
		writeError(w, 501, "mcp_inventory_unavailable", "MCP inventory is unavailable for this runtime")
		return
	}
	writeJSON(w, http.StatusOK, projectMCPInventory(owned.id, reader.MCPInventorySnapshot()))
}

func mcpInventoryChoice(value string, choices ...string) string {
	for _, choice := range choices {
		if value == choice {
			return value
		}
	}
	return "unknown"
}

func mcpInventoryDiagnostic(value string) string {
	switch value {
	case "", "connect_failed", "approval_denied", "connection_closed", "inspection_only_no_connection", "tool_discovery_failed", "tool_identity_invalid", "reconnect_failed":
		return value
	default:
		return "unavailable"
	}
}

func projectMCPInventory(sessionID string, snapshot tools.MCPInventorySnapshot) mcpInventoryResponse {
	out := mcpInventoryResponse{SessionID: sessionID, Revision: strconv.FormatUint(snapshot.Revision, 10), Source: mcpInventoryChoice(snapshot.Source, "runtime", "configuration"), Servers: []mcpInventoryServer{}}
	displayedTools := 0
	for _, server := range snapshot.Servers {
		validTools := 0
		for _, tool := range server.Tools {
			if tool != nil {
				validTools++
			}
		}
		out.HiddenTools += validTools
		if len(out.Servers) == mcpInventoryServers {
			out.HiddenServers++
			continue
		}
		row := mcpInventoryServer{Name: taskExplorerHTTPText(server.Name), Source: mcpInventoryChoice(server.Source, "runtime-manager", "configuration"), Status: mcpInventoryChoice(server.Status, "connected", "connecting", "disconnected", "error", "failed", "needs-auth", "disabled", "configured"), Health: mcpInventoryChoice(server.Health, "healthy", "degraded", "unavailable", "unprobed"), Diagnostic: mcpInventoryDiagnostic(server.Diagnostic), Tools: []mcpInventoryTool{}, HiddenTools: validTools, Truncated: !utf8.ValidString(server.Name) || utf8.RuneCountInString(server.Name) > taskExplorerHTTPRunes}
		for _, tool := range server.Tools {
			if tool == nil {
				continue
			}
			if len(row.Tools) == mcpInventoryServerTools || displayedTools == mcpInventoryTools {
				break
			}
			row.Tools = append(row.Tools, mcpInventoryTool{Name: taskExplorerHTTPText(tool.ToolName), Description: taskExplorerHTTPText(tool.Description), Truncated: !utf8.ValidString(tool.ToolName) || !utf8.ValidString(tool.Description) || utf8.RuneCountInString(tool.ToolName) > taskExplorerHTTPRunes || utf8.RuneCountInString(tool.Description) > taskExplorerHTTPRunes})
			row.HiddenTools--
			out.HiddenTools--
			displayedTools++
		}
		out.Servers = append(out.Servers, row)
	}
	return out
}
