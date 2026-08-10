package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/internal/identity"
	acpserver "github.com/abietic/yhc/server/acp"
)

func newServeACPCommand() *cobra.Command {
	flags := &runtimeFlags{}
	command := &cobra.Command{
		Use:   "acp",
		Short: "Start ACP server (stdio)",
		Long:  "Start " + identity.ProductName + " as an ACP (Agent Client Protocol) server over stdio for IDE integration.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.captureExplicit(cmd)
			return runServeACP(cmd, *flags)
		},
	}
	bindRuntimeFlags(command.Flags(), flags)
	return command
}

func runServeACP(cmd *cobra.Command, flags runtimeFlags) error {
	cwd := mustCwd()
	appConfig, err := config.LoadEffectiveConfig(cwd)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	maxTurns := resolveMaxTurns(appConfig, flags)
	if maxTurns < 0 {
		return fmt.Errorf("max turns must be zero (unlimited) or positive")
	}

	environment := resolveACPEnvironmentOptions()
	agent, err := acpserver.NewAgent(acpserver.Config{
		ProviderFlag:                  flags.provider,
		ModelFlag:                     flags.model,
		ModelProfileFlag:              flags.modelProfile,
		APIKeyFlag:                    flags.apiKey,
		BaseURLFlag:                   flags.baseURL,
		FallbackModelFlag:             flags.fallbackModel,
		ProviderPreflight:             flags.preflight || environment.ProviderPreflight,
		PermissionModeFlag:            flags.permissionMode,
		SandboxProfileFlag:            flags.sandbox,
		SandboxProfileFlagSet:         flags.sandboxSet,
		ApprovalReviewShadow:          flags.approvalReviewShadow,
		ApprovalReviewProvider:        flags.approvalReviewProvider,
		ApprovalReviewModel:           flags.approvalReviewModel,
		ApprovalReviewAPIKey:          flags.approvalReviewAPIKey,
		ApprovalReviewBaseURL:         flags.approvalReviewBaseURL,
		ApprovalReviewTimeout:         flags.approvalReviewTimeout,
		ApprovalReviewAudit:           flags.approvalReviewAudit,
		ApprovalReviewAuditDir:        flags.approvalReviewAuditDir,
		YoloMode:                      flags.yolo,
		MaxTurns:                      maxTurns,
		CWD:                           cwd,
		ToolsFlag:                     append([]string(nil), flags.tools...),
		ToolsFlagSet:                  flags.toolsSet,
		SimpleTools:                   environment.SimpleTools,
		DisableACPAssistantMessageIDs: environment.DisableAssistantMessageIDs,
		DisableACPCommandUpdates:      environment.DisableCommandUpdates,
	})
	if err != nil {
		return fmt.Errorf("failed to create ACP agent: %w", err)
	}
	defer agent.Close()
	if flags.approvalReviewAudit {
		fmt.Fprintln(
			os.Stderr,
			"Permission reviewer audit enabled (local redacted size-window; non-authoritative)",
		)
	}

	conn := acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)
	agent.SetConnection(conn)

	fmt.Fprintf(os.Stderr, "%s ACP server started (stdio)\n", identity.CommandName)
	return waitForServeDone(cmd.Context(), conn.Done())
}

type acpEnvironmentOptions struct {
	ProviderPreflight          bool
	SimpleTools                bool
	DisableAssistantMessageIDs bool
	DisableCommandUpdates      bool
}

func resolveACPEnvironmentOptions() acpEnvironmentOptions {
	return acpEnvironmentOptions{
		ProviderPreflight:          envFlagEnabled(identity.RuntimeEnvProviderPreflight),
		SimpleTools:                envFlagEnabled(identity.RuntimeEnvSimple),
		DisableAssistantMessageIDs: envFlagEnabled(identity.RuntimeEnvDisableACPAssistantMessageIDs),
		DisableCommandUpdates:      envFlagEnabled(identity.RuntimeEnvDisableACPCommandUpdates),
	}
}

func waitForServeDone(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
