package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	engineconfig "github.com/abietic/yhc/engine/config"
	enginemcp "github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/tools"
)

type inspectionCommandOptions struct {
	outputFormat string
}

type inspectionAdministrationHost struct {
	engine        *engine.QueryEngine
	configLoadErr error
}

type inspectionActionError struct {
	code   string
	result any
	err    error
}

func (e *inspectionActionError) Error() string { return e.err.Error() }
func (e *inspectionActionError) Unwrap() error { return e.err }

type mcpInspectionOutput struct {
	Revision uint64                      `json:"revision"`
	Source   string                      `json:"source"`
	Servers  []mcpInspectionServerOutput `json:"servers"`
}

type mcpInspectionServerOutput struct {
	Name       string                    `json:"name"`
	Source     string                    `json:"source"`
	Status     string                    `json:"status"`
	Health     string                    `json:"health"`
	Diagnostic string                    `json:"diagnostic,omitempty"`
	Tools      []mcpInspectionToolOutput `json:"tools"`
}

type mcpInspectionToolOutput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type pluginInspectionOutput struct {
	Health         string                   `json:"health"`
	ProcessScope   string                   `json:"process_scope"`
	Candidate      pluginCandidateOutput    `json:"candidate"`
	LiveGeneration pluginGenerationOutput   `json:"live_generation"`
	Diagnostics    []pluginDiagnosticOutput `json:"diagnostics"`
}

type pluginCandidateOutput struct {
	Digest         string               `json:"digest,omitempty"`
	Commands       int                  `json:"commands"`
	BundledPacks   int                  `json:"bundled_packs"`
	EnabledPlugins int                  `json:"enabled_plugins"`
	Sources        []pluginSourceOutput `json:"sources"`
}

type pluginGenerationOutput struct {
	Revision uint64               `json:"revision"`
	Digest   string               `json:"digest,omitempty"`
	Commands int                  `json:"commands"`
	Sources  []pluginSourceOutput `json:"sources"`
}

type pluginSourceOutput struct {
	Kind       string `json:"kind"`
	Trust      string `json:"trust"`
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Directory  string `json:"directory,omitempty"`
	Commands   int    `json:"commands"`
	Skills     int    `json:"skills"`
	Hooks      int    `json:"hooks"`
	MCPServers int    `json:"mcp_servers"`
	Health     string `json:"health"`
}

type pluginDiagnosticOutput struct {
	Source   string `json:"source,omitempty"`
	Plugin   string `json:"plugin,omitempty"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func newConfigCommand() *cobra.Command {
	options := &inspectionCommandOptions{outputFormat: string(outputFormatText)}
	command := &cobra.Command{
		Use:   "config",
		Short: "Inspect redacted effective configuration",
		Args:  inspectionAdministrationArgs("config", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return renderInspectionUsage(
				cmd,
				"config",
				usageErrorf("config requires the show subcommand"),
			)
		},
	}
	command.PersistentFlags().StringVar(
		&options.outputFormat,
		"output-format",
		string(outputFormatText),
		"Output format (text or json)",
	)
	command.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show effective values, source, and freshness without secrets",
		Args:  inspectionAdministrationArgs("config.show", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInspectionAdministration(
				cmd,
				options,
				"config.show",
				true,
				false,
				func(ctx context.Context, host inspectionAdministrationHost) (administrationOutput, error) {
					if host.configLoadErr != nil {
						return administrationOutput{}, &inspectionActionError{
							code: "config_error",
							err:  fmt.Errorf("load effective configuration: %w", host.configLoadErr),
						}
					}
					snapshot, err := host.engine.DiagnosticsSnapshot(ctx)
					if err != nil {
						return administrationOutput{}, err
					}
					return administrationOutput{
						text:   commands.RenderDiagnosticConfig(snapshot),
						result: snapshot.Config,
					}, nil
				},
			)
		},
	})
	return command
}

func newDoctorCommand() *cobra.Command {
	options := &inspectionCommandOptions{outputFormat: string(outputFormatText)}
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Run read-only runtime and configuration diagnostics",
		Args:  inspectionAdministrationArgs("doctor", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInspectionAdministration(
				cmd,
				options,
				"doctor",
				true,
				false,
				func(ctx context.Context, host inspectionAdministrationHost) (administrationOutput, error) {
					snapshot, err := host.engine.DiagnosticsSnapshot(ctx)
					if err != nil {
						return administrationOutput{}, err
					}
					return administrationOutput{
						text:   commands.RenderDiagnosticDoctor(snapshot),
						result: snapshot.Doctor,
					}, nil
				},
			)
		},
	}
	command.Flags().StringVar(
		&options.outputFormat,
		"output-format",
		string(outputFormatText),
		"Output format (text or json)",
	)
	return command
}

func newMCPInspectionCommand() *cobra.Command {
	options := &inspectionCommandOptions{outputFormat: string(outputFormatText)}
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Inspect configured MCP servers without connecting them",
		Args:  inspectionAdministrationArgs("mcp", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return renderInspectionUsage(
				cmd,
				"mcp",
				usageErrorf("mcp requires one of: list, get"),
			)
		},
	}
	command.PersistentFlags().StringVar(
		&options.outputFormat,
		"output-format",
		string(outputFormatText),
		"Output format (text or json)",
	)
	command.AddCommand(
		newMCPListCommand(options),
		newMCPGetCommand(options),
	)
	return command
}

func newMCPListCommand(options *inspectionCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers with unprobed health",
		Args:  inspectionAdministrationArgs("mcp.list", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInspectionAdministration(
				cmd,
				options,
				"mcp.list",
				false,
				true,
				func(_ context.Context, host inspectionAdministrationHost) (administrationOutput, error) {
					snapshot := host.engine.MCPInventorySnapshot()
					text, _ := commands.RenderMCPInventory(snapshot, "")
					return administrationOutput{text: text, result: projectMCPInventory(snapshot)}, nil
				},
			)
		},
	}
}

func newMCPGetCommand(options *inspectionCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "get <server>",
		Short: "Inspect one configured MCP server",
		Args:  inspectionAdministrationArgs("mcp.get", exactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspectionAdministration(
				cmd,
				options,
				"mcp.get",
				false,
				true,
				func(_ context.Context, host inspectionAdministrationHost) (administrationOutput, error) {
					snapshot := host.engine.MCPInventorySnapshot()
					text, found := commands.RenderMCPInventory(snapshot, args[0])
					if !found {
						return administrationOutput{}, &inspectionActionError{
							code: "not_found",
							err:  fmt.Errorf("MCP server %q was not found", args[0]),
						}
					}
					projected := projectMCPInventory(snapshot)
					for _, server := range projected.Servers {
						if strings.EqualFold(server.Name, args[0]) {
							return administrationOutput{text: text, result: server}, nil
						}
					}
					return administrationOutput{}, fmt.Errorf("MCP server projection is inconsistent")
				},
			)
		},
	}
}

func newPluginsCommand() *cobra.Command {
	options := &inspectionCommandOptions{outputFormat: string(outputFormatText)}
	command := &cobra.Command{
		Use:   "plugins",
		Short: "Inspect and validate prompt-command plugin generations",
		Args:  inspectionAdministrationArgs("plugins", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return renderInspectionUsage(
				cmd,
				"plugins",
				usageErrorf("plugins requires one of: list, validate, reload"),
			)
		},
	}
	command.PersistentFlags().StringVar(
		&options.outputFormat,
		"output-format",
		string(outputFormatText),
		"Output format (text or json)",
	)
	command.AddCommand(
		newPluginsListCommand(options),
		newPluginsValidateCommand(options),
		newPluginsReloadCommand(options),
	)
	return command
}

func newPluginsListCommand(options *inspectionCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the live local generation and configured candidate sources",
		Args:  inspectionAdministrationArgs("plugins.list", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInspectionAdministration(
				cmd,
				options,
				"plugins.list",
				false,
				false,
				func(_ context.Context, host inspectionAdministrationHost) (administrationOutput, error) {
					validated, validationErr := host.engine.ValidatePromptCommands()
					result := projectPluginValidation(validated, validationErr)
					return administrationOutput{
						text:   formatPluginInspection("Plugin generation inspection", result),
						result: result,
					}, nil
				},
			)
		},
	}
}

func newPluginsValidateCommand(options *inspectionCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the complete configured candidate without replacing it",
		Args:  inspectionAdministrationArgs("plugins.validate", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInspectionAdministration(
				cmd,
				options,
				"plugins.validate",
				false,
				false,
				func(_ context.Context, host inspectionAdministrationHost) (administrationOutput, error) {
					validated, err := host.engine.ValidatePromptCommands()
					result := projectPluginValidation(validated, err)
					if err != nil {
						return administrationOutput{}, &inspectionActionError{
							code:   "plugin_validation_error",
							result: result,
							err:    fmt.Errorf("validate prompt-command candidate: %w", err),
						}
					}
					return administrationOutput{
						text:   formatPluginInspection("Plugin generation validation", result),
						result: result,
					}, nil
				},
			)
		},
	}
}

func newPluginsReloadCommand(options *inspectionCommandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Atomically replace the inspection process prompt-command generation",
		Args:  inspectionAdministrationArgs("plugins.reload", noArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInspectionAdministration(
				cmd,
				options,
				"plugins.reload",
				false,
				false,
				func(_ context.Context, host inspectionAdministrationHost) (administrationOutput, error) {
					reloaded, err := host.engine.ReloadPromptCommands()
					result := projectPluginReload(reloaded, err)
					if err != nil {
						return administrationOutput{}, &inspectionActionError{
							code:   "plugin_reload_error",
							result: result,
							err:    fmt.Errorf("reload prompt-command generation: %w", err),
						}
					}
					return administrationOutput{
						text:   formatPluginInspection("Plugin generation reload", result),
						result: result,
					}, nil
				},
			)
		},
	}
}

func runInspectionAdministration(
	cmd *cobra.Command,
	options *inspectionCommandOptions,
	operation string,
	includeDiagnostics bool,
	includeMCP bool,
	action func(context.Context, inspectionAdministrationHost) (administrationOutput, error),
) error {
	format, err := parseOutputFormat(options.outputFormat)
	if err != nil {
		return renderInspectionFailure(
			formatForError(options.outputFormat),
			cmd,
			operation,
			err,
			"usage_error",
			ExitUsage,
			nil,
		)
	}
	if err := cmd.Context().Err(); err != nil {
		return renderInspectionFailure(
			format,
			cmd,
			operation,
			err,
			"cancelled",
			ExitCancelled,
			nil,
		)
	}
	host, err := newInspectionAdministrationHost(includeDiagnostics, includeMCP)
	if err != nil {
		return renderInspectionFailure(
			format,
			cmd,
			operation,
			err,
			"inspection_error",
			ExitFailure,
			nil,
		)
	}
	defer host.engine.Close()

	output, err := action(cmd.Context(), host)
	if err != nil {
		exitCode := ExitCode(err)
		if exitCode == ExitSuccess {
			exitCode = ExitFailure
		}
		code := "inspection_error"
		var result any
		var actionErr *inspectionActionError
		if errors.As(err, &actionErr) {
			code = actionErr.code
			result = actionErr.result
		}
		if errors.Is(err, context.Canceled) {
			code = "cancelled"
			exitCode = ExitCancelled
		}
		return renderInspectionFailure(
			format,
			cmd,
			operation,
			err,
			code,
			exitCode,
			result,
		)
	}
	return renderAdministrationSuccess(
		format,
		cmd.OutOrStdout(),
		cmd.ErrOrStderr(),
		operation,
		output,
		"inspection",
	)
}

func newInspectionAdministrationHost(
	includeDiagnostics bool,
	includeMCP bool,
) (inspectionAdministrationHost, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return inspectionAdministrationHost{}, fmt.Errorf("resolve current working directory: %w", err)
	}
	appConfig := engineconfig.DefaultConfig()
	var configLoadErr error
	var modelName string
	var resolver engine.ModelResolver
	if includeDiagnostics {
		appConfig, configLoadErr = engineconfig.LoadEffectiveConfig(cwd)
		if appConfig == nil {
			appConfig = engineconfig.DefaultConfig()
		}
		resolutionInput := resolveProviderInput(appConfig, runtimeFlags{})
		resolved, resolveErr := provider.ResolveConfig(resolutionInput)
		modelName = strings.TrimSpace(appConfig.Model)
		if resolveErr == nil {
			modelName = resolved.Model
		}
		resolver = engine.ModelResolverFunc(func(string) (provider.ResolvedConfig, error) {
			return resolved, resolveErr
		})
	}

	mcpManager := tools.NewMCPToolManager()
	if includeMCP {
		mcpConfig, err := enginemcp.LoadMCPConfig(cwd)
		if err != nil {
			return inspectionAdministrationHost{}, fmt.Errorf("load MCP configuration: %w", err)
		}
		mcpManager = tools.NewMCPInspectionManager(mcpConfig)
	}
	toolRegistry := tools.NewRegistry()
	tools.RegisterDefaults(toolRegistry)
	eng := engine.NewInspectionAdministrationEngine(engine.InspectionAdministrationConfig{
		CWD:            cwd,
		Model:          modelName,
		FallbackModel:  resolveFallbackModel(appConfig, runtimeFlags{}),
		ModelResolver:  resolver,
		PermissionMode: resolvePermissionMode(appConfig, runtimeFlags{}),
		ToolRegistry:   toolRegistry,
		MCPManager:     mcpManager,
	})
	return inspectionAdministrationHost{engine: eng, configLoadErr: configLoadErr}, nil
}

func inspectionAdministrationArgs(
	operation string,
	validate cobra.PositionalArgs,
) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return renderInspectionUsage(cmd, operation, err)
		}
		return nil
	}
}

func installInspectionFlagErrorHandlers(command *cobra.Command, prefix string) {
	command.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return renderInspectionUsage(
			cmd,
			inspectionOperation(cmd, prefix),
			usageErrorf("%v", err),
		)
	})
	for _, child := range command.Commands() {
		installInspectionFlagErrorHandlers(child, prefix)
	}
}

func inspectionOperation(cmd *cobra.Command, prefix string) string {
	if cmd == nil || cmd.Name() == prefix {
		return prefix
	}
	return prefix + "." + cmd.Name()
}

func inspectionOutputFormat(cmd *cobra.Command) string {
	if cmd != nil {
		if flag := cmd.Flag("output-format"); flag != nil {
			return flag.Value.String()
		}
	}
	return string(outputFormatText)
}

func renderInspectionUsage(cmd *cobra.Command, operation string, err error) error {
	return renderInspectionFailure(
		formatForError(inspectionOutputFormat(cmd)),
		cmd,
		operation,
		err,
		"usage_error",
		ExitUsage,
		nil,
	)
}

func renderInspectionFailure(
	format outputFormat,
	cmd *cobra.Command,
	operation string,
	err error,
	code string,
	exitCode int,
	result any,
) error {
	return renderAdministrationFailureWithResult(
		format,
		cmd.OutOrStdout(),
		cmd.ErrOrStderr(),
		operation,
		err,
		code,
		exitCode,
		"inspection",
		result,
	)
}

func projectMCPInventory(snapshot tools.MCPInventorySnapshot) mcpInspectionOutput {
	output := mcpInspectionOutput{
		Revision: snapshot.Revision,
		Source:   snapshot.Source,
		Servers:  make([]mcpInspectionServerOutput, 0, len(snapshot.Servers)),
	}
	for _, server := range snapshot.Servers {
		projected := mcpInspectionServerOutput{
			Name:       server.Name,
			Source:     server.Source,
			Status:     server.Status,
			Health:     server.Health,
			Diagnostic: server.Diagnostic,
			Tools:      make([]mcpInspectionToolOutput, 0, len(server.Tools)),
		}
		for _, tool := range server.Tools {
			if tool == nil {
				continue
			}
			projected.Tools = append(projected.Tools, mcpInspectionToolOutput{
				Name:        tool.ToolName,
				Description: tool.Description,
			})
		}
		output.Servers = append(output.Servers, projected)
	}
	return output
}

func projectPluginValidation(
	validated commands.PromptCommandValidationResult,
	err error,
) pluginInspectionOutput {
	health := "valid"
	if err != nil {
		health = "invalid"
	}
	return pluginInspectionOutput{
		Health:       health,
		ProcessScope: "inspection-host",
		Candidate: pluginCandidateOutput{
			Digest:         validated.Digest,
			Commands:       validated.Commands,
			BundledPacks:   validated.BundledPacks,
			EnabledPlugins: validated.EnabledPlugins,
			Sources:        projectPluginSources(validated.Sources),
		},
		LiveGeneration: projectPluginGeneration(validated.LiveGeneration),
		Diagnostics:    projectPluginDiagnostics(validated.Diagnostics),
	}
}

func projectPluginReload(
	reloaded commands.PromptCommandReloadResult,
	err error,
) pluginInspectionOutput {
	health := "valid"
	if err != nil {
		health = "invalid"
	}
	return pluginInspectionOutput{
		Health:       health,
		ProcessScope: "inspection-host",
		Candidate: pluginCandidateOutput{
			Digest:         reloaded.Generation.Digest,
			Commands:       reloaded.Commands,
			BundledPacks:   reloaded.BundledPacks,
			EnabledPlugins: reloaded.EnabledPlugins,
			Sources:        projectPluginSources(reloaded.Generation.Sources),
		},
		LiveGeneration: projectPluginGeneration(reloaded.Generation),
		Diagnostics:    projectPluginDiagnostics(reloaded.Diagnostics),
	}
}

func projectPluginGeneration(
	generation commands.PromptCommandGenerationSnapshot,
) pluginGenerationOutput {
	return pluginGenerationOutput{
		Revision: generation.Revision,
		Digest:   generation.Digest,
		Commands: generation.Commands,
		Sources:  projectPluginSources(generation.Sources),
	}
}

func projectPluginSources(sources []commands.PromptCommandSourceSnapshot) []pluginSourceOutput {
	output := make([]pluginSourceOutput, 0, len(sources))
	for _, source := range sources {
		output = append(output, pluginSourceOutput{
			Kind:       string(source.Kind),
			Trust:      string(source.Trust),
			Name:       source.Name,
			Version:    source.Version,
			Directory:  source.Directory,
			Commands:   source.Commands,
			Skills:     source.Skills,
			Hooks:      source.Hooks,
			MCPServers: source.MCPServers,
			Health:     source.Health,
		})
	}
	return output
}

func projectPluginDiagnostics(
	diagnostics []commands.PromptCommandDiagnostic,
) []pluginDiagnosticOutput {
	output := make([]pluginDiagnosticOutput, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		output = append(output, pluginDiagnosticOutput{
			Source:   diagnostic.Source,
			Plugin:   diagnostic.Plugin,
			Severity: diagnostic.Severity,
			Code:     diagnostic.Code,
			Message:  redactSensitiveText(diagnostic.Message),
		})
	}
	return output
}

func formatPluginInspection(title string, result pluginInspectionOutput) string {
	var output strings.Builder
	fmt.Fprintf(
		&output,
		"%s\nhealth=%s scope=%s candidate_commands=%d live_revision=%d",
		title,
		result.Health,
		result.ProcessScope,
		result.Candidate.Commands,
		result.LiveGeneration.Revision,
	)
	if result.Candidate.Digest != "" {
		digest := result.Candidate.Digest
		if len(digest) > 12 {
			digest = digest[:12]
		}
		fmt.Fprintf(&output, " digest=%s", digest)
	}
	for _, source := range result.Candidate.Sources {
		fmt.Fprintf(
			&output,
			"\n  %s@%s [%s; kind=%s; trust=%s]: commands=%d skills=%d hooks=%d mcp=%d source=%s",
			source.Name,
			defaultInspectionValue(source.Version, "unknown"),
			defaultInspectionValue(source.Health, "healthy"),
			source.Kind,
			source.Trust,
			source.Commands,
			source.Skills,
			source.Hooks,
			source.MCPServers,
			source.Directory,
		)
	}
	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(
			&output,
			"\n  diagnostic [%s/%s] %s: %s",
			diagnostic.Severity,
			diagnostic.Code,
			defaultInspectionValue(diagnostic.Plugin, diagnostic.Source),
			diagnostic.Message,
		)
	}
	return output.String()
}

func defaultInspectionValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
