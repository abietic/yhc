package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/notify"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/tui"
	"github.com/abietic/yhc/internal/tui/terminalcap"
	"github.com/abietic/yhc/tools"
)

type runtimeFlags struct {
	provider               string
	model                  string
	modelProfile           string
	apiKey                 string
	baseURL                string
	fallbackModel          string
	preflight              bool
	yolo                   bool
	permissionMode         string
	approvalReviewShadow   bool
	approvalReviewProvider string
	approvalReviewModel    string
	approvalReviewAPIKey   string
	approvalReviewBaseURL  string
	approvalReviewTimeout  time.Duration
	approvalReviewAudit    bool
	approvalReviewAuditDir string
	maxTurns               int
	maxTurnsSet            bool
	tools                  []string
	toolsSet               bool
	sandbox                string
	sandboxSet             bool
}

type rootOptions struct {
	runtime      runtimeFlags
	print        bool
	plain        bool
	mouse        bool
	resume       string
	outputFormat string
}

func newRootCommand() *cobra.Command {
	options := &rootOptions{mouse: true, outputFormat: string(outputFormatText)}
	root := &cobra.Command{
		Use:           identity.CommandName + " [prompt]",
		Short:         identity.ProductLongName + ", an AI coding assistant",
		Long:          "An AI coding assistant with access to tools. Starts an interactive TUI by default; use exec or -p for non-interactive execution.",
		Args:          maximumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoot(cmd, args, options)
		},
	}
	bindRuntimeFlags(root.Flags(), &options.runtime)
	root.Flags().BoolVarP(&options.print, "print", "p", false, "Compatibility headless mode; prefer the exec subcommand")
	root.Flags().BoolVar(&options.plain, "plain", false, "Plain text REPL (no TUI)")
	root.Flags().BoolVar(&options.mouse, "mouse", true, "Enable mouse tracking for scroll (use Shift+drag for text selection; YHC_DISABLE_MOUSE=1 disables it, with EINO_AGENT_DISABLE_MOUSE kept as an alias)")
	root.Flags().StringVar(&options.resume, "resume", "", "Resume a previous session by ID")
	root.Flags().StringVar(&options.outputFormat, "output-format", string(outputFormatText), "Headless output format (text or json)")

	sessionsCommand := newSessionsCommand()
	configCommand := newConfigCommand()
	doctorCommand := newDoctorCommand()
	mcpCommand := newMCPInspectionCommand()
	pluginsCommand := newPluginsCommand()
	permissionReviewAuditCommand := newPermissionReviewAuditCommand()
	root.AddCommand(
		newExecCommand(),
		newGoalCommand(),
		newMigrateStateCommand(),
		newServeCommand(),
		newResumeCommand(),
		sessionsCommand,
		configCommand,
		doctorCommand,
		mcpCommand,
		pluginsCommand,
		permissionReviewAuditCommand,
		newVersionCommand(),
		newCompletionCommand(root),
	)
	installFlagErrorHandlers(root)
	installSessionFlagErrorHandlers(sessionsCommand)
	installInspectionFlagErrorHandlers(configCommand, "config")
	installInspectionFlagErrorHandlers(doctorCommand, "doctor")
	installInspectionFlagErrorHandlers(mcpCommand, "mcp")
	installInspectionFlagErrorHandlers(pluginsCommand, "plugins")
	return root
}

func newResumeCommand() *cobra.Command {
	flags := &runtimeFlags{}
	mouse := true
	confirmLegacyStopped := false
	command := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a previous session in the TUI",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.captureExplicit(cmd)
			return runTUI(
				cmd.Context(),
				*flags,
				mouse,
				args[0],
				confirmLegacyStopped,
			)
		},
	}
	bindRuntimeFlags(command.Flags(), flags)
	command.Flags().BoolVar(&mouse, "mouse", true, "Enable mouse tracking for scroll")
	command.Flags().BoolVar(
		&confirmLegacyStopped,
		"confirm-legacy-stopped",
		false,
		"Attest that the archived legacy producer has stopped before importing its session bundle",
	)
	return command
}

func bindRuntimeFlags(flags *pflag.FlagSet, values *runtimeFlags) {
	flags.StringVar(&values.provider, "provider", "", "Model provider (anthropic/claude, openai, google/gemini, deepseek, qwen, ark)")
	flags.StringVar(&values.model, "model", "", "Model name")
	flags.StringVar(&values.modelProfile, "model-profile", "", "Configured user model profile")
	flags.StringVar(&values.apiKey, "api-key", "", "API key")
	flags.StringVar(&values.baseURL, "base-url", "", "Model provider base URL")
	flags.StringVar(&values.fallbackModel, "fallback-model", "", "Fallback model used after bounded overload retries")
	flags.BoolVar(&values.preflight, "provider-preflight", false, "Check provider connectivity and credentials during startup")
	flags.BoolVarP(&values.yolo, "yolo", "y", false, "Bypass all permission checks")
	flags.StringVar(&values.permissionMode, "permission-mode", "", "Permission mode (default, plan, acceptEdits, bypassPermissions)")
	flags.BoolVar(&values.approvalReviewShadow, "permission-review-shadow", false, "Run the separate permission reviewer in non-authoritative shadow mode")
	flags.StringVar(&values.approvalReviewProvider, "permission-review-provider", "", "Explicit provider for the separate permission reviewer")
	flags.StringVar(&values.approvalReviewModel, "permission-review-model", "", "Explicit model for the separate permission reviewer")
	flags.StringVar(&values.approvalReviewAPIKey, "permission-review-api-key", "", "API key for the separate permission reviewer")
	flags.StringVar(&values.approvalReviewBaseURL, "permission-review-base-url", "", "Base URL for the separate permission reviewer")
	flags.DurationVar(&values.approvalReviewTimeout, "permission-review-timeout", 8*time.Second, "Absolute timeout for one permission shadow review")
	flags.BoolVar(&values.approvalReviewAudit, "permission-review-audit", false, "Retain bounded local redacted permission reviewer measurements")
	flags.StringVar(&values.approvalReviewAuditDir, "permission-review-audit-dir", "", "Override the local permission reviewer audit directory")
	flags.IntVar(&values.maxTurns, "max-turns", 0, "Max conversation turns (0 = unlimited)")
	flags.StringSliceVar(&values.tools, "tools", nil, "Available built-in tools: empty disables all, default enables all, or provide a comma-separated list")
	flags.StringVar(&values.sandbox, "sandbox", "", "Sandbox profile (workspace-write or danger-full-access)")
}

func (flags *runtimeFlags) captureExplicit(cmd *cobra.Command) {
	flags.maxTurnsSet = cmd.Flags().Changed("max-turns")
	flags.toolsSet = cmd.Flags().Changed("tools")
	flags.sandboxSet = cmd.Flags().Changed("sandbox")
}

func resolveSandboxSelection(flags runtimeFlags, appConfig *config.Config) (config.SandboxSelection, error) {
	var sandboxConfig *config.SandboxConfig
	if appConfig != nil {
		sandboxConfig = appConfig.Sandbox
	}
	return config.ResolveSandbox(config.SandboxSelectionInput{
		Config:        sandboxConfig,
		CLIProfile:    flags.sandbox,
		CLIProfileSet: flags.sandboxSet,
	})
}

func resolveEngineSandboxSelection(flags runtimeFlags, appConfig *config.Config) (*engine.SandboxSelection, error) {
	selection, err := resolveSandboxSelection(flags, appConfig)
	if err != nil {
		return nil, err
	}
	return engine.NewSandboxSelection(
		containment.Profile(selection.GuestProfile),
		containment.SelectionSource(selection.Source),
		selection.ExtraReadRoots,
	)
}

// Execute runs the root command.
func Execute() error {
	return ExecuteContext(context.Background())
}

// ExecuteContext constructs a fresh command tree and runs it with ctx.
func ExecuteContext(ctx context.Context) error {
	return newRootCommand().ExecuteContext(ctx)
}

func runRoot(cmd *cobra.Command, args []string, options *rootOptions) error {
	ctx := cmd.Context()
	options.runtime.captureExplicit(cmd)

	if options.print && options.plain {
		return usageErrorf("--print and --plain cannot be used together")
	}
	if len(args) > 0 && !options.print {
		return usageErrorf("a positional prompt requires --print or the exec subcommand")
	}
	if options.print {
		prompt := ""
		if len(args) > 0 {
			prompt = args[0]
		}
		return runHeadless(ctx, prompt, headlessOptions{
			Runtime:      options.runtime,
			Resume:       options.resume,
			OutputFormat: options.outputFormat,
			Stdin:        cmd.InOrStdin(),
			Stdout:       cmd.OutOrStdout(),
			Stderr:       cmd.ErrOrStderr(),
		})
	}
	if options.plain {
		if options.outputFormat != string(outputFormatText) {
			return usageErrorf("--output-format is available only with --print or exec")
		}
		return runPlainREPL(ctx, options.runtime, options.resume)
	}
	if options.outputFormat != string(outputFormatText) {
		return usageErrorf("--output-format is available only with --print or exec")
	}
	return runTUI(ctx, options.runtime, options.mouse, options.resume, false)
}

func runTUI(
	ctx context.Context,
	flags runtimeFlags,
	mouse bool,
	resumeID string,
	confirmLegacyStopped bool,
) error {
	resumeSource, err := prepareConfiguredSessionResume(
		ctx,
		resumeID,
		confirmLegacyStopped,
	)
	if err != nil {
		return err
	}
	// Save terminal state for panic recovery
	termState := tui.SaveTerminalState()
	defer tui.PanicRecovery(termState)

	engineCfg, provCfg, appCfg, err := buildEngineConfig(ctx, flags, os.Stderr)
	if err != nil {
		return err
	}
	engineCfg.EnableLongSessionServices = true
	engineCfg.CommandEntrypoint = commands.EntrypointTUI
	terminalCaps := terminalcap.Current(mouse)
	focusState := terminalcap.NewFocusState(terminalCaps.FocusReporting)
	if engineCfg.NotifyManager != nil {
		engineCfg.NotifyManager.SetExternalPolicy(focusState.ExternalNotificationsAllowed)
	}

	app := tui.New(tui.Config{
		Model:         string(provCfg.Provider) + ":" + provCfg.Model,
		Theme:         appCfg.Theme,
		Fullscreen:    true,
		MouseEnabled:  terminalCaps.Mouse,
		ReducedMotion: appCfg.ReducedMotion || envFlagEnabled(identity.RuntimeEnvReducedMotion) || envFlagEnabled(identity.RuntimeEnvAccessibility),
		TerminalCaps:  &terminalCaps,
		FocusState:    focusState,
	})

	engineCfg.PermissionPrompt = app.MakePermissionPromptFn()
	engineCfg.RepeatedToolCallPrompt = app.MakeRepeatedToolCallPromptFn()
	eng := engine.NewQueryEngine(engineCfg)
	emitExecutionContainmentStartupDiagnostic(os.Stderr, eng)
	defer eng.Close()
	defer printResumeHint(os.Stderr, eng)

	if err := resumeConfiguredSession(ctx, eng, resumeSource, os.Stderr); err != nil {
		return err
	}

	app.SetEngine(eng)

	terminalOutput, err := tui.NewTerminalOutput(os.Stdout)
	if err != nil {
		return err
	}
	defer terminalOutput.Close() //nolint:errcheck
	app.SetClipboardService(tui.NewClipboardService(ctx, terminalOutput))

	options := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithOutput(tuiProgramOutput{Writer: terminalOutput, terminal: os.Stdout}),
	}
	p := tea.NewProgram(app, options...)
	app.SetProgram(p)

	var notificationAdapter *tuiNotifyAdapter
	stopNotifications := func() {}
	if engineCfg.NotifyManager != nil {
		notificationAdapter = newTUINotifyAdapter(p.Send)
		engineCfg.NotifyManager.AddHandler(notificationAdapter)
		notificationAdapter.start()
		stopNotifications = notificationAdapter.close
	}
	err = runTUIProgram(p, terminalOutput, termState.Restore, stopNotifications)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func runTUIProgram(
	program *tea.Program,
	output *tui.TerminalOutput,
	restore func(),
	programStopped func(),
) error {
	monitorDone := make(chan struct{})
	monitorExited := make(chan struct{})
	go func() {
		defer close(monitorExited)
		select {
		case <-output.Failed():
			program.Kill()
		case <-monitorDone:
		}
	}()

	_, runErr := program.Run()
	if programStopped != nil {
		programStopped()
	}
	close(monitorDone)
	<-monitorExited

	outputErr := output.Close()
	if outputErr != nil {
		if output.Stopped() && restore != nil {
			restore()
		}
		terminalErr := fmt.Errorf("terminal output failed: %w", outputErr)
		if runErr != nil && !errors.Is(runErr, tea.ErrProgramKilled) {
			return errors.Join(fmt.Errorf("TUI error: %w", runErr), terminalErr)
		}
		return terminalErr
	}
	if runErr != nil {
		return fmt.Errorf("TUI error: %w", runErr)
	}
	return nil
}

// tuiProgramOutput keeps renderer writes behind TerminalOutput while exposing
// the original terminal descriptor Bubble Tea needs for initial size detection
// and SIGWINCH-driven resize events.
type tuiProgramOutput struct {
	io.Writer
	terminal *os.File
}

func (o tuiProgramOutput) Read([]byte) (int, error) {
	return 0, os.ErrInvalid
}

func (o tuiProgramOutput) Close() error {
	return nil
}

func (o tuiProgramOutput) Fd() uintptr {
	return o.terminal.Fd()
}

func envFlagEnabled(name identity.RuntimeEnvName) bool {
	return identity.EnvTruthy(name.Pair())
}

func runPlainREPL(ctx context.Context, flags runtimeFlags, resumeID string) error {
	resumeSource, err := prepareConfiguredSessionResume(ctx, resumeID, false)
	if err != nil {
		return err
	}
	engineCfg, provCfg, _, err := buildEngineConfig(ctx, flags, os.Stderr)
	if err != nil {
		return err
	}
	engineCfg.EnableLongSessionServices = true
	engineCfg.CommandEntrypoint = commands.EntrypointPlain

	input := newPlainInputBroker(bufio.NewReader(os.Stdin))
	plainPrompt := makePlainPermissionPrompt(input, os.Stdout)
	engineCfg.PermissionPrompt = plainPrompt

	eng := engine.NewQueryEngine(engineCfg)
	emitExecutionContainmentStartupDiagnostic(os.Stderr, eng)
	defer eng.Close()
	defer printResumeHint(os.Stderr, eng)
	if err := resumeConfiguredSession(ctx, eng, resumeSource, os.Stderr); err != nil {
		return err
	}
	if err := drivePlainPendingProjectGraphPermission(
		ctx,
		eng,
		plainPrompt,
		os.Stdout,
		os.Stderr,
	); err != nil {
		return err
	}

	cmdRegistry := eng.GetCommandRegistry()

	fmt.Fprintf(os.Stdout, "Model: %s:%s\n", provCfg.Provider, provCfg.Model)
	fmt.Fprintln(os.Stdout, "Type a prompt or /exit")

	return drivePlainREPL(
		ctx,
		eng,
		cmdRegistry,
		input,
		plainPrompt,
		os.Stdout,
		os.Stderr,
	)
}

func emitExecutionContainmentStartupDiagnostic(stderr io.Writer, eng *engine.QueryEngine) {
	if stderr == nil || eng == nil {
		return
	}
	code, message := eng.ExecutionContainmentStartupDiagnostic()
	if code == "" || message == "" {
		return
	}
	fmt.Fprintf(stderr, "Warning [%s]: %s\n", code, message)
}

func drivePlainPendingProjectGraphPermission(
	ctx context.Context,
	eng *engine.QueryEngine,
	prompt engine.PermissionPromptFn,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if eng == nil {
		return errors.New("plain ProjectGraph engine is unavailable")
	}
	request, ok := eng.PendingProjectGraphPermissionRequest()
	if !ok {
		return nil
	}
	if err := resolvePlainProjectGraphPermission(
		ctx,
		eng,
		prompt,
		request,
	); err != nil {
		return err
	}
	events, err := claimPlainProjectGraphResume(ctx, eng)
	if err != nil {
		return err
	}
	return drivePlainQueryEvents(
		ctx,
		eng,
		prompt,
		stdout,
		stderr,
		events,
	)
}

func drivePlainQueryEvents(
	ctx context.Context,
	eng *engine.QueryEngine,
	prompt engine.PermissionPromptFn,
	stdout io.Writer,
	stderr io.Writer,
	events <-chan engine.QueryEvent,
) error {
	if eng == nil {
		return errors.New("plain ProjectGraph engine is unavailable")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	for {
		waitingForResume := false
		var terminalErr error
		for evt := range events {
			switch evt.Type {
			case engine.EventAssistant:
				if evt.AssistantMessage != nil {
					fmt.Fprint(stdout, evt.AssistantMessage.Content)
				} else if evt.Message != nil {
					fmt.Fprint(stdout, evt.Message.Content)
				}
			case engine.EventToolResult:
				if evt.ToolResultMessage != nil {
					name := evt.ToolResultMessage.ToolName
					if name == "" {
						name = "tool"
					}
					content := evt.ToolResultMessage.Content
					if len(content) > 200 {
						content = content[:200] + "..."
					}
					fmt.Fprintf(stdout, "\n[%s] %s\n", name, content)
				} else if evt.Message != nil {
					content := evt.Message.Content
					if len(content) > 200 {
						content = content[:200] + "..."
					}
					fmt.Fprintf(stdout, "\n[tool] %s\n", content)
				}
			case engine.EventCompactBoundary:
				fmt.Fprint(stdout, "\n[compacting context...]\n")
			case engine.EventMaxTurnsReached:
				fmt.Fprint(stderr, "\n[max turns reached]\n")
			case engine.EventPermissionReview:
				if diagnostic := permissionReviewDiagnostic(evt.PermissionReview); diagnostic != "" {
					fmt.Fprintf(stderr, "\n[%s]\n", diagnostic)
				}
			case engine.EventModelAttempt:
				if notice := modelFallbackNotice(evt.ModelAttempt); notice != "" {
					fmt.Fprintf(stderr, "\n[%s]\n", notice)
				}
			case engine.EventGoalLifecycle:
				if evt.GoalLifecycle != nil {
					goal := evt.GoalLifecycle.Goal
					fmt.Fprintf(
						stdout,
						"\n[Goal %s] status=%s revision=%d tokens=%d continuation=%d\n",
						evt.GoalLifecycle.Phase,
						goal.Status,
						goal.Revision,
						goal.TokensUsed,
						goal.ContinuationOrdinal,
					)
				}
			case engine.EventPermissionRequest:
				if evt.PermissionRequest != nil &&
					evt.PermissionRequest.Source == "project_graph" {
					if err := resolvePlainProjectGraphPermission(
						ctx,
						eng,
						prompt,
						*evt.PermissionRequest,
					); err != nil {
						return err
					}
				}
			case engine.EventTerminal:
				if evt.TerminalInfo == nil {
					continue
				}
				waitingForResume = evt.TerminalInfo.Reason == engine.TerminalWaitingInput
				if evt.TerminalInfo.Err != nil {
					terminalErr = evt.TerminalInfo.Err
				}
				if !waitingForResume && evt.TerminalInfo.Reason != "" {
					fmt.Fprintf(
						stderr,
						"\n[session ended: %s]\n",
						evt.TerminalInfo.Reason,
					)
				}
			}
		}
		if terminalErr != nil {
			return terminalErr
		}
		if !waitingForResume {
			return nil
		}
		var err error
		events, err = claimPlainProjectGraphResume(ctx, eng)
		if err != nil {
			return err
		}
	}
}

func permissionReviewDiagnostic(review *engine.PermissionReviewEvent) string {
	if review == nil {
		return ""
	}
	tool := permissionReviewDiagnosticToken(review.CanonicalTool, "tool")
	switch review.Phase {
	case engine.PermissionReviewChecking:
		return fmt.Sprintf("permission review shadow checking: %s", tool)
	case engine.PermissionReviewCompleted:
		decision := permissionReviewDiagnosticToken(review.Decision, "unknown")
		reason := permissionReviewDiagnosticToken(review.ReasonCode, "unknown")
		return fmt.Sprintf(
			"permission review shadow completed: %s %s/%s",
			tool,
			decision,
			reason,
		)
	case engine.PermissionReviewUnavailable:
		reason := permissionReviewDiagnosticToken(review.ReasonCode, "unavailable")
		return fmt.Sprintf(
			"permission review shadow unavailable: %s %s",
			tool,
			reason,
		)
	default:
		return ""
	}
}

func permissionReviewDiagnosticToken(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return fallback
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == '.' {
			continue
		}
		return fallback
	}
	return value
}

func modelFallbackNotice(attempt *engine.ModelAttemptEvent) string {
	if attempt == nil ||
		attempt.Phase != engine.ModelAttemptStarted ||
		attempt.AttemptIndex <= 0 ||
		attempt.SwitchCount <= 0 ||
		!isSafeModelProfileID(attempt.Profile) {
		return ""
	}
	return fmt.Sprintf(
		"Model fallback: profile %s after overload (switch %d)",
		attempt.Profile,
		attempt.SwitchCount,
	)
}

func isSafeModelProfileID(profile string) bool {
	if len(profile) == 0 || len(profile) > 64 ||
		profile[0] < 'a' || profile[0] > 'z' {
		return false
	}
	for index := 1; index < len(profile); index++ {
		character := profile[index]
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func resolvePlainProjectGraphPermission(
	ctx context.Context,
	eng *engine.QueryEngine,
	prompt engine.PermissionPromptFn,
	request engine.PermissionRequestEvent,
) error {
	if prompt == nil {
		return errors.New("plain ProjectGraph interaction is unavailable")
	}
	result := prompt(ctx, engine.PermissionPromptRequest{
		ToolName:     request.ToolName,
		ToolUseID:    request.ToolUseID,
		Input:        request.Input,
		Message:      request.Message,
		SessionScope: request.Message,
		SessionID:    eng.SessionID(),
		ThreadID:     eng.ThreadID(),
		AgentID:      eng.AgentID(),
		PlanApproval: request.PlanApproval,
	})
	if !eng.ResolvePermissionInteraction(request.ToolUseID, result) {
		return fmt.Errorf(
			"plain ProjectGraph interaction %q is no longer active",
			request.ToolUseID,
		)
	}
	return nil
}

func claimPlainProjectGraphResume(
	ctx context.Context,
	eng *engine.QueryEngine,
) (<-chan engine.QueryEvent, error) {
	item, ok, err := eng.ClaimNextRuntimeItem()
	if err != nil {
		return nil, err
	}
	if !ok || item.Kind != engine.RuntimeItemPermissionDecision {
		return nil, errors.New(
			"plain ProjectGraph permission decision was not claimable",
		)
	}
	events, _ := eng.SubmitRuntimeItem(ctx, item)
	return events, nil
}

func plainCommandRunsThroughEngine(registry *commands.Registry, input string) bool {
	if registry == nil {
		return false
	}
	name, _ := commands.ParseCommandInput(input)
	cmd := registry.Get(name)
	if cmd == nil {
		return false
	}
	return (cmd.Availability == commands.AvailabilitySupported ||
		cmd.Availability == commands.AvailabilityHidden) &&
		cmd.Entrypoints.Supports(commands.EntrypointPlain) &&
		cmd.ExecutionOwner == commands.ExecutionOwnerEngine
}

func runPlainEngineCommand(
	ctx context.Context,
	eng *engine.QueryEngine,
	input string,
) (*engine.CommandResultEvent, error) {
	events, _ := eng.SubmitMessage(ctx, input)
	var outcome *engine.CommandResultEvent
	for event := range events {
		if event.Type == engine.EventCommandResult {
			outcome = event.CommandResult
		}
		if event.Type == engine.EventTerminal &&
			event.TerminalInfo != nil &&
			event.TerminalInfo.Err != nil {
			return outcome, event.TerminalInfo.Err
		}
	}
	if outcome == nil {
		return nil, fmt.Errorf("engine command returned no typed result")
	}
	return outcome, nil
}

func makePlainPermissionPrompt(input *plainInputBroker, writer io.Writer) engine.PermissionPromptFn {
	promptGate := make(chan struct{}, 1)
	promptGate <- struct{}{}
	return func(ctx context.Context, request engine.PermissionPromptRequest) engine.PermissionInteractionResult {
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-ctx.Done():
			return plainPermissionContextResult(ctx)
		case <-promptGate:
		}
		defer func() { promptGate <- struct{}{} }()
		if ctx.Err() != nil {
			return plainPermissionContextResult(ctx)
		}
		if request.PlanApproval != nil {
			return readPlainPlanApproval(ctx, input, writer, request.PlanApproval)
		}

		desc := request.ToolName
		if command, ok := request.Input["command"].(string); ok && command != "" {
			desc += ": " + command
		}
		fmt.Fprintf(writer, "Allow %s? [y] once / [s] session / [a] always / [n] deny: ", desc)
		result := input.next(ctx)
		line, err := result.line, result.err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return plainPermissionContextResult(ctx)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return engine.PermissionInteractionResult{Decision: engine.PermissionDeny, Message: err.Error()}
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes", "once":
			return engine.PermissionInteractionResult{Decision: engine.PermissionAllowOnce}
		case "s", "session":
			return engine.PermissionInteractionResult{Decision: engine.PermissionAllowSession}
		case "a", "always":
			return engine.PermissionInteractionResult{Decision: engine.PermissionAllowAlways}
		default:
			return engine.PermissionInteractionResult{Decision: engine.PermissionDeny, Message: "user denied permission"}
		}
	}
}

func readPlainPlanApproval(
	ctx context.Context,
	input *plainInputBroker,
	writer io.Writer,
	request *engine.PlanApprovalRequest,
) engine.PermissionInteractionResult {
	planBytes, reviewedDigest, readErr := engine.ReadPlanReviewSnapshot(request.PlanFileIdentity)
	if readErr != nil {
		return engine.PermissionInteractionResult{
			Decision: engine.PermissionDeny,
			Message:  readErr.Error(),
			PlanApproval: &engine.PlanApprovalDecision{
				RequestID:    request.RequestID,
				PlanRevision: request.PlanRevision,
				Outcome:      engine.PlanApprovalCancel,
				TargetMode:   permission.ModePlan,
			},
		}
	}
	returnMode := request.ReturnMode
	if returnMode == "" {
		returnMode = permission.ModeDefault
	}
	targets := engine.PlanApprovalTargetModes(returnMode)
	actionPrompt := plainPlanActionPrompt(returnMode, targets)
	decision := &engine.PlanApprovalDecision{
		RequestID:    request.RequestID,
		PlanRevision: request.PlanRevision,
		Outcome:      engine.PlanApprovalCancel,
		TargetMode:   permission.ModePlan,
	}
	result := engine.PermissionInteractionResult{
		Decision:     engine.PermissionDeny,
		Message:      "User rejected the plan.",
		PlanApproval: decision,
	}
	terminal := func(err error) engine.PermissionInteractionResult {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result = plainPermissionContextResult(ctx)
			result.PlanApproval = decision
			return result
		}
		if err != nil {
			result.Message = err.Error()
		}
		return result
	}
	for {
		fmt.Fprintf(writer, "Plan revision %d (%s):\n%s\n--- end plan ---\nApprove? %s / [r <feedback>] revise / [n] cancel: ", request.PlanRevision, request.PlanFileIdentity, string(planBytes), actionPrompt)
		readResult := input.next(ctx)
		line, err := readResult.line, readResult.err
		if err != nil {
			return terminal(err)
		}
		normalized := strings.TrimSpace(line)
		lower := strings.ToLower(normalized)
		var target permission.Mode
		switch lower {
		case "p", "previous", "m", "manual":
			target = returnMode
		case "e", "edits", "acceptedits":
			target = permission.ModeAcceptEdits
		case "b", "bypass":
			target = permission.ModeBypassPermissions
		case "n", "no", "cancel":
			return result
		default:
			if feedback, ok := strings.CutPrefix(normalized, "r "); ok && strings.TrimSpace(feedback) != "" {
				decision.Outcome = engine.PlanApprovalRevise
				decision.Feedback = strings.TrimSpace(feedback)
				result.Message = "User requested further planning: " + decision.Feedback
				return result
			}
			continue
		}
		if !containsPlanTarget(targets, target) {
			continue
		}
		if target != permission.ModeBypassPermissions {
			decision.Outcome = engine.PlanApprovalApprove
			decision.TargetMode = target
			decision.ReviewedPlanDigest = reviewedDigest
			result.Decision = engine.PermissionAllowOnce
			result.Message = ""
			return result
		}
		confirmed, back, err := readPlainBypassConfirmation(ctx, input, writer)
		if err != nil {
			return terminal(err)
		}
		if back {
			continue
		}
		if confirmed {
			decision.Outcome = engine.PlanApprovalApprove
			decision.TargetMode = target
			decision.Confirmed = true
			decision.ReviewedPlanDigest = reviewedDigest
			result.Decision = engine.PermissionAllowOnce
			result.Message = ""
			return result
		}
	}
}

func plainPlanActionPrompt(returnMode permission.Mode, targets []permission.Mode) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		switch target {
		case returnMode:
			parts = append(parts, fmt.Sprintf("[p] previous permissions (%s)", target))
		case permission.ModeAcceptEdits:
			parts = append(parts, "[e] auto-accept edits")
		case permission.ModeBypassPermissions:
			parts = append(parts, "[b] bypass permissions")
		}
	}
	return strings.Join(parts, " / ")
}

func containsPlanTarget(targets []permission.Mode, target permission.Mode) bool {
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

// readPlainBypassConfirmation is a separate round. Only the displayed token
// grants bypass; negative input returns to targets and other input repeats it.
func readPlainBypassConfirmation(ctx context.Context, input *plainInputBroker, writer io.Writer) (bool, bool, error) {
	const token = "BYPASS"
	for {
		fmt.Fprintf(writer, "Bypass permissions is risky. Type %s to confirm, or n to go back: ", token)
		readResult := input.next(ctx)
		line, err := readResult.line, readResult.err
		if err != nil {
			return false, false, err
		}
		if strings.TrimSpace(line) == token {
			return true, false, nil
		}
		if lower := strings.ToLower(strings.TrimSpace(line)); lower == "n" || lower == "no" || lower == "back" {
			return false, true, nil
		}
	}
}

func plainPermissionContextResult(ctx context.Context) engine.PermissionInteractionResult {
	if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		return engine.PermissionInteractionResult{
			Decision: engine.PermissionTimedOut,
			Message:  "permission request timed out",
		}
	}
	return engine.PermissionInteractionResult{
		Decision: engine.PermissionCancelled,
		Message:  "permission request cancelled",
	}
}

// plainREPLHandleAction processes a command action result in the plain REPL.
// Returns "quit" to exit, "prompt" to feed result.Output as a prompt, or "" to continue.
func plainREPLHandleAction(result *commands.CommandResult, writer io.Writer) string {
	switch result.Action {
	case commands.ActionQuit:
		return "quit"
	case commands.ActionPrompt:
		return "prompt"
	case commands.ActionToggleVim, commands.ActionChangeTheme,
		commands.ActionAgentCreate, commands.ActionAgentEdit:
		fmt.Fprintln(writer, "This command requires the TUI. Run without --plain.")
	}
	return ""
}

// --- Helpers ---

func resolveProviderInput(appConfig *config.Config, flags runtimeFlags) provider.ResolveInput {
	input := provider.ResolveInput{
		Explicit: provider.Config{
			Provider: provider.Provider(flags.provider),
			Model:    flags.model,
			APIKey:   flags.apiKey,
			BaseURL:  flags.baseURL,
		},
	}
	if appConfig != nil {
		input.Configured = provider.Config{
			Provider:     provider.Provider(appConfig.Provider),
			Model:        appConfig.Model,
			BaseURL:      appConfig.APIBaseURL,
			ModelAliases: appConfig.ModelAliases,
		}
	}
	return input
}

func resolveFallbackModel(appConfig *config.Config, flags runtimeFlags) string {
	if value := strings.TrimSpace(flags.fallbackModel); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("PROV_FALLBACK_MODEL")); value != "" {
		return value
	}
	if appConfig != nil {
		return strings.TrimSpace(appConfig.FallbackModel)
	}
	return ""
}

func explicitLegacyRuntimeFields(flags runtimeFlags) []string {
	fields := make([]string, 0, 5)
	if strings.TrimSpace(flags.provider) != "" {
		fields = append(fields, "--provider")
	}
	if strings.TrimSpace(flags.model) != "" {
		fields = append(fields, "--model")
	}
	if strings.TrimSpace(flags.apiKey) != "" {
		fields = append(fields, "--api-key")
	}
	if strings.TrimSpace(flags.baseURL) != "" {
		fields = append(fields, "--base-url")
	}
	if strings.TrimSpace(flags.fallbackModel) != "" {
		fields = append(fields, "--fallback-model")
	}
	return fields
}

func resolvePermissionMode(appConfig *config.Config, flags runtimeFlags) permission.Mode {
	if flags.yolo {
		return permission.ModeBypassPermissions
	}
	if flags.permissionMode != "" {
		return permission.Mode(flags.permissionMode)
	}
	if appConfig != nil && appConfig.PermissionMode != "" {
		return permission.Mode(appConfig.PermissionMode)
	}
	return permission.ModeDefault
}

func resolveMaxTurns(appConfig *config.Config, flags runtimeFlags) int {
	if flags.maxTurns != 0 || flags.maxTurnsSet {
		return flags.maxTurns
	}
	if value := os.Getenv("CLAUDE_MAX_TURNS"); value != "" {
		maxTurns, err := strconv.Atoi(value)
		if err != nil {
			return -1
		}
		return maxTurns
	}
	if appConfig != nil {
		return appConfig.MaxTurns
	}
	return 0
}

func resolveApprovalReviewer(
	ctx context.Context,
	flags runtimeFlags,
	stderr io.Writer,
) (*provider.ApprovalReviewerRuntime, error) {
	if !flags.approvalReviewShadow {
		return nil, nil
	}
	if strings.TrimSpace(flags.approvalReviewProvider) == "" ||
		strings.TrimSpace(flags.approvalReviewModel) == "" {
		return nil, fmt.Errorf(
			"permission review shadow requires explicit --permission-review-provider and --permission-review-model",
		)
	}
	if flags.approvalReviewTimeout <= 0 {
		return nil, fmt.Errorf("permission review timeout must be positive")
	}
	runtime, err := provider.NewApprovalReviewer(ctx, provider.ApprovalReviewerOptions{
		Provider: provider.Provider(flags.approvalReviewProvider),
		Model:    flags.approvalReviewModel,
		APIKey:   flags.approvalReviewAPIKey,
		BaseURL:  flags.approvalReviewBaseURL,
		Timeout:  flags.approvalReviewTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"initialize separate permission reviewer: %s",
			redactSensitiveText(err.Error(), flags.approvalReviewAPIKey),
		)
	}
	if stderr != nil {
		fmt.Fprintf(
			stderr,
			"Permission reviewer shadow enabled (non-authoritative): provider=%s model=%s data_boundary=%s timeout=%s\n",
			runtime.Route.Provider,
			runtime.Route.Model,
			runtime.Route.DataBoundary,
			flags.approvalReviewTimeout,
		)
	}
	return runtime, nil
}

func resolveApprovalReviewAudit(
	flags runtimeFlags,
	stderr io.Writer,
) (*permission.ReviewAuditStore, error) {
	if !flags.approvalReviewAudit {
		if strings.TrimSpace(flags.approvalReviewAuditDir) != "" {
			return nil, fmt.Errorf(
				"--permission-review-audit-dir requires --permission-review-audit",
			)
		}
		return nil, nil
	}
	if !flags.approvalReviewShadow {
		return nil, fmt.Errorf(
			"--permission-review-audit requires --permission-review-shadow",
		)
	}
	store, err := permission.NewReviewAuditStore(permission.ReviewAuditStoreOptions{
		Dir: flags.approvalReviewAuditDir,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"initialize permission review audit store: local store unavailable",
		)
	}
	if stderr != nil {
		fmt.Fprintln(
			stderr,
			"Permission reviewer audit enabled (local redacted size-window; non-authoritative)",
		)
	}
	return store, nil
}

func buildEngineConfig(ctx context.Context, flags runtimeFlags, stderr io.Writer) (engine.QueryEngineConfig, provider.ResolvedConfig, *config.Config, error) {
	return buildEngineConfigForCWD(ctx, flags, mustCwd(), stderr)
}

func buildEngineConfigForCWD(
	ctx context.Context,
	flags runtimeFlags,
	cwd string,
	stderr io.Writer,
) (engine.QueryEngineConfig, provider.ResolvedConfig, *config.Config, error) {
	if strings.TrimSpace(cwd) == "" {
		return engine.QueryEngineConfig{}, provider.ResolvedConfig{}, nil, errors.New("engine CWD is required")
	}
	if stderr == nil {
		stderr = io.Discard
	}

	configSources, err := config.LoadConfigSources(cwd)
	if err != nil {
		return engine.QueryEngineConfig{}, provider.ResolvedConfig{}, nil, fmt.Errorf("load config: %w", err)
	}
	appConfig := configSources.Effective
	sandboxSelection, err := resolveEngineSandboxSelection(flags, appConfig)
	if err != nil {
		return engine.QueryEngineConfig{}, provider.ResolvedConfig{}, appConfig, err
	}
	for _, diagnostic := range configSources.SandboxDiagnostics {
		fmt.Fprintf(stderr, "Warning [%s]: %s", diagnostic.Code, diagnostic.Message)
		if len(diagnostic.Keys) > 0 {
			fmt.Fprintf(stderr, " (keys: %s)", strings.Join(diagnostic.Keys, ", "))
		}
		fmt.Fprintln(stderr)
	}
	maxTurns := resolveMaxTurns(appConfig, flags)
	if maxTurns < 0 {
		return engine.QueryEngineConfig{}, provider.ResolvedConfig{}, appConfig, fmt.Errorf("max turns must be zero (unlimited) or positive")
	}

	fallbackModel := resolveFallbackModel(appConfig, flags)
	runtime, err := provider.NewConfiguredRuntime(ctx, provider.ConfiguredRuntimeOptions{
		Sources:              configSources,
		ExplicitModelProfile: flags.modelProfile,
		ExplicitLegacyFields: explicitLegacyRuntimeFields(flags),
		LegacyFallbackModel:  fallbackModel,
		Resolution:           resolveProviderInput(appConfig, flags),
		Preflight:            flags.preflight || envFlagEnabled(identity.RuntimeEnvProviderPreflight),
	})
	if err != nil {
		safeErr := redactSensitiveText(err.Error(), flags.apiKey)
		fmt.Fprintf(stderr, "Model init error: %s\n", safeErr)
		fmt.Fprintln(stderr, "\nSupported providers: anthropic/claude, openai, google/gemini, deepseek, qwen, ark")
		fmt.Fprintln(stderr, "Set provider-specific variables or PROV, PROV_API_KEY, PROV_MODEL; flags have highest priority")
		return engine.QueryEngineConfig{}, provider.ResolvedConfig{}, appConfig, errors.New(safeErr)
	}
	provCfg := runtime.Main
	chatModel := runtime.ChatModel
	for _, diagnostic := range runtime.PortfolioDiagnostics() {
		fmt.Fprintf(stderr, "Warning [%s]: %s", diagnostic.Code, diagnostic.Message)
		if len(diagnostic.Keys) > 0 {
			fmt.Fprintf(stderr, " (keys: %s)", strings.Join(diagnostic.Keys, ", "))
		}
		if diagnostic.Path != "" {
			fmt.Fprintf(stderr, " (path: %q)", diagnostic.Path)
		}
		fmt.Fprintln(stderr)
	}
	if runtime.UsesNamedPortfolio() {
		inventory := runtime.InventorySnapshot()
		for _, entry := range inventory.Entries {
			if !strings.EqualFold(entry.Selector, inventory.Default) {
				continue
			}
			fmt.Fprintf(
				stderr,
				"Selected model profile %s (%s:%s)\n",
				entry.Selector,
				entry.Provider,
				entry.APIModel,
			)
			break
		}
	}
	if fallbackModel != "" && !runtime.UsesNamedPortfolio() {
		fallbackConfig, prepareErr := runtime.PrepareModel(ctx, fallbackModel)
		if prepareErr != nil {
			return engine.QueryEngineConfig{}, provCfg, appConfig, fmt.Errorf("fallback model %q: %w", fallbackModel, prepareErr)
		}
		if fallbackConfig.Provider == provCfg.Provider && fallbackConfig.Model == provCfg.Model {
			return engine.QueryEngineConfig{}, provCfg, appConfig, fmt.Errorf("fallback model cannot resolve to the same provider and model as the main model")
		}
	}
	approvalReviewer, err := resolveApprovalReviewer(ctx, flags, stderr)
	if err != nil {
		return engine.QueryEngineConfig{}, provCfg, appConfig, err
	}
	approvalReviewAudit, err := resolveApprovalReviewAudit(flags, stderr)
	if err != nil {
		return engine.QueryEngineConfig{}, provCfg, appConfig, err
	}

	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)
	var toolSelection *tools.ToolSelection
	if flags.toolsSet {
		parsed := tools.ParseToolSelection(flags.tools)
		toolSelection = &parsed
	}

	permMode := resolvePermissionMode(appConfig, flags)

	systemPrompt := "You are a helpful AI assistant with access to tools. Use the available tools to accomplish tasks. When a tool would help answer a question or complete a task, call it directly rather than describing what you would do."
	if appConfig.CustomSystemPrompt != "" {
		systemPrompt = appConfig.CustomSystemPrompt
	}

	hookExec := hooks.NewExecutor()

	// Create notification manager with default handlers.
	notifyMgr := notify.NewNotificationManager()
	notifyMgr.AddHandler(&notify.TerminalBellHandler{})
	notifyMgr.AddHandler(&notify.OSNotifyHandler{})
	catalogPath, legacyCatalogPath := session.DefaultCatalogPaths()

	engineCfg := engine.QueryEngineConfig{
		CWD:                      cwd,
		CustomSystemPrompt:       systemPrompt,
		MaxTurns:                 maxTurns,
		ChatModel:                chatModel,
		ToolRegistry:             reg,
		ToolSelection:            toolSelection,
		SimpleTools:              envFlagEnabled(identity.RuntimeEnvSimple),
		MemoryProjectRoot:        cwd,
		EnablePersistentMemory:   !envFlagEnabled(identity.RuntimeEnvSimple),
		Model:                    provCfg.Model,
		FallbackModel:            fallbackModel,
		ModelResolver:            runtime,
		PromptCapabilityResolver: engine.DefaultPromptCapabilityResolver(),
		PermissionMode:           permMode,
		SandboxSelection:         sandboxSelection,
		HookExecutor:             hookExec,
		NotifyManager:            notifyMgr,
		WebFetchModel:            chatModel,
		SessionCatalogPath:       catalogPath,
		LegacySessionCatalogPath: legacyCatalogPath,
		GoalCapability:           goalCapabilityConfig(appConfig),
	}
	if approvalReviewer != nil {
		engineCfg.ApprovalReviewShadow = true
		engineCfg.ApprovalReviewer = approvalReviewer.Reviewer
		engineCfg.ApprovalReviewerRoute = approvalReviewer.Route
		engineCfg.ApprovalReviewTimeout = flags.approvalReviewTimeout
	}
	if approvalReviewAudit != nil {
		engineCfg.ApprovalReviewAudit = approvalReviewAudit
	}

	return engineCfg, provCfg, appConfig, nil
}

func goalCapabilityConfig(appConfig *config.Config) *engine.GoalCapabilityConfig {
	capability := &engine.GoalCapabilityConfig{Enabled: true}
	if appConfig == nil || appConfig.Goal == nil {
		return capability
	}
	if appConfig.Goal.Enabled != nil {
		capability.Enabled = *appConfig.Goal.Enabled
	}
	if appConfig.Goal.DefaultTokenBudget != nil {
		budget := *appConfig.Goal.DefaultTokenBudget
		capability.DefaultTokenBudget = &budget
	}
	return capability
}

func prepareConfiguredSessionResume(
	ctx context.Context,
	resumeID string,
	confirmLegacyStopped bool,
) (*session.SessionInfo, error) {
	resumeID = strings.TrimSpace(resumeID)
	if resumeID == "" {
		return nil, nil
	}
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return nil, fmt.Errorf("resume session %s: resolve current directory: %w", resumeID, cwdErr)
	}
	info, err := session.AdmitDefaultSessionResume(ctx, cwd, resumeID)
	if err == nil {
		return &info, nil
	}
	if !errors.Is(err, session.ErrLegacySessionImportRequired) ||
		!confirmLegacyStopped {
		return nil, fmt.Errorf("resume session %s: %w", resumeID, err)
	}
	target, ok := session.LegacySessionImportTarget(err)
	if !ok {
		return nil, fmt.Errorf("resume session %s: %w", resumeID, err)
	}
	userRoots, rootsErr := session.DefaultSessionImportUserRoots()
	if rootsErr != nil {
		return nil, fmt.Errorf("resume session %s: %w", resumeID, rootsErr)
	}
	_, importErr := session.ImportSessionForResume(ctx, session.ImportRequest{
		Target:               target,
		UserRoots:            userRoots,
		ConfirmLegacyStopped: true,
	})
	if importErr != nil && !errors.Is(
		importErr,
		session.ErrSessionImportAlreadyCommitted,
	) {
		return nil, fmt.Errorf("resume session %s: %w", resumeID, importErr)
	}
	info, err = session.AdmitDefaultSessionResume(ctx, cwd, resumeID)
	if err != nil {
		return nil, fmt.Errorf("resume session %s after import: %w", resumeID, err)
	}
	return &info, nil
}

func resumeConfiguredSession(
	ctx context.Context,
	eng *engine.QueryEngine,
	resumeSource *session.SessionInfo,
	stderr io.Writer,
) error {
	if resumeSource == nil {
		return nil
	}
	resumed, err := eng.SessionService().ResumeInfo(ctx, *resumeSource)
	if err != nil {
		return fmt.Errorf("resume session %s: %w", resumeSource.SessionID, err)
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "Resumed session %s (%d messages)\n", resumed.SessionID, len(resumed.Messages))
	}
	return nil
}

func printResumeHint(w io.Writer, eng *engine.QueryEngine) {
	if w == nil || eng == nil || eng.SessionID() == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "\nResume this session with:\n  %s resume %s\n", identity.CommandName, eng.SessionID())
}

func mustCwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "/tmp"
	}
	return d
}

const tuiNotifyPendingCapacity = 3

// tuiNotifyAdapter bridges engine notifications to one bounded Bubble Tea
// transport pump. It never reads or mutates App presentation state.
type tuiNotifyAdapter struct {
	mu      sync.Mutex
	ready   *sync.Cond
	pending []tui.NotificationDeliveryMsg
	send    func(tea.Msg)
	started bool
	closed  bool
	wg      sync.WaitGroup
}

func newTUINotifyAdapter(send func(tea.Msg)) *tuiNotifyAdapter {
	adapter := &tuiNotifyAdapter{send: send}
	adapter.ready = sync.NewCond(&adapter.mu)
	return adapter
}

func (a *tuiNotifyAdapter) start() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.started || a.closed || a.send == nil {
		a.mu.Unlock()
		return
	}
	a.started = true
	a.wg.Add(1)
	a.mu.Unlock()
	go a.pump()
}

func (a *tuiNotifyAdapter) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		clear(a.pending)
		a.pending = nil
		a.ready.Broadcast()
	}
	a.mu.Unlock()
	a.wg.Wait()
}

func (a *tuiNotifyAdapter) offer(message tui.NotificationDeliveryMsg) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	if len(a.pending) == tuiNotifyPendingCapacity {
		copy(a.pending, a.pending[1:])
		a.pending[len(a.pending)-1] = message
	} else {
		a.pending = append(a.pending, message)
	}
	a.ready.Signal()
	a.mu.Unlock()
}

func (a *tuiNotifyAdapter) pump() {
	defer a.wg.Done()
	for {
		a.mu.Lock()
		for len(a.pending) == 0 && !a.closed {
			a.ready.Wait()
		}
		if a.closed {
			a.mu.Unlock()
			return
		}
		message := a.pending[0]
		copy(a.pending, a.pending[1:])
		a.pending[len(a.pending)-1] = tui.NotificationDeliveryMsg{}
		a.pending = a.pending[:len(a.pending)-1]
		a.mu.Unlock()

		a.send(message)
	}
}

func (a *tuiNotifyAdapter) IsSupported() bool { return true }

func (a *tuiNotifyAdapter) Notify(_ context.Context, n *notify.Notification) error {
	if a == nil || n == nil {
		return nil
	}
	var sev tui.NotificationSeverity
	switch {
	case n.Type == notify.NotificationError:
		sev = tui.NotifyError
	case n.Type == notify.NotificationCompletion:
		sev = tui.NotifySuccess
	case n.Urgent:
		sev = tui.NotifyWarning
	default:
		sev = tui.NotifyInfo
	}
	msg := n.Title
	if n.Body != "" {
		msg += ": " + n.Body
	}
	a.offer(tui.NotificationDeliveryMsg{Message: msg, Severity: sev})
	return nil
}
