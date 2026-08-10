package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/internal/buildinfo"
)

func newVersionCommand() *cobra.Command {
	format := string(outputFormatText)
	command := &cobra.Command{
		Use:   "version",
		Short: "Print build and version information",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := parseOutputFormat(format)
			if err != nil {
				return err
			}
			info := buildinfo.Current()
			if output == outputFormatJSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetEscapeHTML(false)
				if err := encoder.Encode(info); err != nil {
					return fmt.Errorf("write version output: %w", err)
				}
				return nil
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.ShortText(info))
			return err
		},
	}
	command.Flags().StringVar(&format, "output-format", string(outputFormatText), "Output format (text or json)")
	return command
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate a shell completion script",
		Args:      maximumNArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := "bash"
			if len(args) == 1 {
				shell = strings.ToLower(strings.TrimSpace(args[0]))
			}
			switch shell {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return usageErrorf("unsupported completion shell %q", shell)
			}
		},
	}
}
