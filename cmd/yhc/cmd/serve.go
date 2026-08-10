package cmd

import (
	"github.com/spf13/cobra"

	"github.com/abietic/yhc/internal/identity"
)

func newServeCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "serve",
		Short: "Start a protocol server",
		Long:  "Start " + identity.ProductName + " as a protocol server. Available protocols: acp, mcp.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return usageErrorf("serve requires a protocol: acp or mcp")
		},
	}
	command.AddCommand(newServeACPCommand(), newServeMCPCommand())
	return command
}
