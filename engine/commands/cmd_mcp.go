package commands

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/tools"
)

// executeMCP implements /mcp as manager-owned read-only inspection.
// Mirrors reference commands/mcp.tsx.
//
// Usage:
//
//	/mcp                              → list all servers and tools
//	/mcp <server>                     → show tools from a specific server
func executeMCP(ctx *CommandContext, args string) (*CommandResult, error) {
	args = strings.TrimSpace(args)
	parts := strings.Fields(args)
	if len(parts) > 0 {
		switch parts[0] {
		case "add", "remove", "restart":
			return &CommandResult{
				Output: "MCP mutation is unavailable: persisted configuration, manager inventory, runtime tool registry, model-visible generation, and rollback are not one transaction.",
			}, nil
		}
	}

	return executeMCPList(ctx, args)
}

// executeMCPList lists connected servers and tools.
func executeMCPList(ctx *CommandContext, filter string) (*CommandResult, error) {
	snapshot, ok := runtimeInspectionSnapshot(ctx)
	if !ok {
		return &CommandResult{
			Output: "MCP inspection is unavailable for this runtime.",
		}, nil
	}
	output, _ := RenderMCPInventory(snapshot.MCP, filter)
	return &CommandResult{Output: output}, nil
}

// RenderMCPInventory projects one detached manager snapshot. found is false
// only when a non-empty server filter does not match the snapshot.
func RenderMCPInventory(snapshot tools.MCPInventorySnapshot, filter string) (output string, found bool) {
	emptyInventoryLabel := "runtime manager"
	headingLabel := "runtime"
	if snapshot.Source == "configuration" {
		emptyInventoryLabel = "configured"
		headingLabel = "configured"
	}
	if len(snapshot.Servers) == 0 {
		return fmt.Sprintf(
			"No MCP servers in %s inventory (generation %d).",
			emptyInventoryLabel,
			snapshot.Revision,
		), strings.TrimSpace(filter) == ""
	}

	servers := append([]tools.MCPServerSnapshot(nil), snapshot.Servers...)
	if filter != "" {
		serverName := strings.TrimSpace(filter)
		var selected []tools.MCPServerSnapshot
		for _, server := range servers {
			if strings.EqualFold(server.Name, serverName) {
				selected = append(selected, server)
				break
			}
		}
		if len(selected) == 0 {
			names := make([]string, 0, len(servers))
			for _, server := range servers {
				names = append(names, server.Name)
			}
			return fmt.Sprintf(
				"No MCP server %q found.\n\nRuntime inventory: %s",
				serverName,
				strings.Join(names, ", "),
			), false
		}
		servers = selected
	}

	var sb strings.Builder
	totalTools := 0
	for _, server := range snapshot.Servers {
		totalTools += len(server.Tools)
	}
	fmt.Fprintf(
		&sb,
		"MCP %s inventory generation %d (%d servers, %d tools):\n\n",
		headingLabel,
		snapshot.Revision,
		len(snapshot.Servers),
		totalTools,
	)

	for _, server := range servers {
		fmt.Fprintf(
			&sb,
			"  %s [%s; health=%s; source=%s] (%d tools):\n",
			server.Name,
			server.Status,
			server.Health,
			server.Source,
			len(server.Tools),
		)
		if server.Diagnostic != "" {
			fmt.Fprintf(&sb, "    diagnostic: %s\n", server.Diagnostic)
		}
		for _, tool := range server.Tools {
			desc := tool.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			fmt.Fprintf(&sb, "    %-30s %s\n", tool.ToolName, desc)
		}
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n"), true
}
