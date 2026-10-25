package cmd

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine/execution"
)

// Usage inspection is provider-free and does not construct QueryEngine.
func newUsageCommand() *cobra.Command {
	command := &cobra.Command{Use: "usage", Short: "Inspect retained invocation accounting"}
	var directory, segmentID string
	inspect := &cobra.Command{
		Use: "inspect", Short: "Read a usage checkpoint without resuming or granting spending", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(directory) == "" || strings.TrimSpace(segmentID) == "" {
				return usageErrorf("checkpoint-dir and segment-id are required")
			}
			result, err := execution.ReadRunUsageCheckpointDir(directory, segmentID)
			if err != nil {
				return usageErrorf("usage checkpoint cannot be validated")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	inspect.Flags().StringVar(&directory, "checkpoint-dir", "", "Retained private invocation checkpoint directory")
	inspect.Flags().StringVar(&segmentID, "segment-id", "", "Exact expected invocation segment ID from the caller")
	command.AddCommand(inspect)
	return command
}
