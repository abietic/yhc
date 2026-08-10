package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/internal/identity"
	mcpserver "github.com/abietic/yhc/server/mcp"
)

func newServeMCPCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start MCP server (stdio)",
		Long:  "Start " + identity.ProductName + " as an MCP server over stdio, exposing built-in tools to MCP clients.",
		Args:  noArgs,
		RunE:  runServeMCP,
	}
}

func runServeMCP(cmd *cobra.Command, args []string) error {
	cwd := mustCwd()

	fmt.Fprintf(cmd.ErrOrStderr(), "%s MCP server starting (stdio)\n", identity.CommandName)
	return mcpserver.Serve(cmd.Context(), mcpserver.Config{
		CWD: cwd,
	})
}
