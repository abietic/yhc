package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	engineerrors "github.com/abietic/yhc/engine/errors"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	enginetransport "github.com/abietic/yhc/engine/transport"
)

const headlessEnvelopeSchemaVersion = 1

var errMaxTurnsReached = errors.New("max turns reached")

type outputFormat string

const (
	outputFormatText  outputFormat = "text"
	outputFormatJSON  outputFormat = "json"
	outputFormatJSONL outputFormat = "jsonl"
)

type headlessOptions struct {
	Runtime      runtimeFlags
	Resume       string
	OutputFormat string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
}

type headlessResult struct {
	Status         string
	Output         string
	SessionID      string
	TerminalReason string
	TerminalEvent  engine.RuntimeEventEnvelope
	ErrorCode      string
	Err            error
	ExitCode       int
}

type headlessEnvelope struct {
	SchemaVersion  int                    `json:"schema_version"`
	Status         string                 `json:"status"`
	Output         string                 `json:"output,omitempty"`
	SessionID      string                 `json:"session_id,omitempty"`
	TerminalReason string                 `json:"terminal_reason,omitempty"`
	ExitCode       int                    `json:"exit_code"`
	Error          *headlessEnvelopeError `json:"error,omitempty"`
}

type headlessEnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newExecCommand() *cobra.Command {
	options := &headlessOptions{OutputFormat: string(outputFormatText)}
	command := &cobra.Command{
		Use:   "exec [prompt]",
		Short: "Run one non-interactive prompt",
		Long:  "Run one prompt through the canonical QueryEngine and exit. Without a prompt, or with '-', input is read from stdin.",
		Args:  maximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.Runtime.captureExplicit(cmd)
			options.Stdin = cmd.InOrStdin()
			options.Stdout = cmd.OutOrStdout()
			options.Stderr = cmd.ErrOrStderr()
			prompt := ""
			if len(args) == 1 {
				prompt = args[0]
			}
			return runHeadless(cmd.Context(), prompt, *options)
		},
	}
	bindRuntimeFlags(command.Flags(), &options.Runtime)
	command.Flags().StringVar(&options.Resume, "resume", "", "Resume a previous session by ID")
	command.Flags().StringVar(&options.OutputFormat, "output-format", string(outputFormatText), "Output format (text, json, or jsonl)")
	return command
}

func parseOutputFormat(value string) (outputFormat, error) {
	switch outputFormat(strings.ToLower(strings.TrimSpace(value))) {
	case outputFormatText:
		return outputFormatText, nil
	case outputFormatJSON:
		return outputFormatJSON, nil
	case outputFormatJSONL:
		return outputFormatJSONL, nil
	default:
		return "", usageErrorf("unsupported output format %q (expected text, json, or jsonl)", value)
	}
}

func runHeadless(ctx context.Context, promptArgument string, options headlessOptions) error {
	format, err := parseOutputFormat(options.OutputFormat)
	if err != nil {
		return renderHeadlessFailure(formatForError(options.OutputFormat), options, err, "usage_error", ExitUsage)
	}
	options = normalizeHeadlessWriters(options)

	prompt, err := resolveHeadlessPrompt(promptArgument, options.Stdin, readerIsTerminal(options.Stdin))
	if err != nil {
		return renderHeadlessFailure(format, options, err, "usage_error", ExitUsage)
	}
	resumeSource, err := prepareConfiguredSessionResume(ctx, options.Resume, false)
	if err != nil {
		return renderHeadlessFailure(
			format,
			options,
			err,
			errorCodeForFailure(err, "session_error"),
			ExitCode(err),
		)
	}

	engineCfg, _, _, err := buildEngineConfig(ctx, options.Runtime, options.Stderr)
	if err != nil {
		return renderHeadlessFailure(format, options, err, errorCodeForFailure(err, "runtime_error"), ExitCode(err))
	}

	configureHeadlessPermissions(&engineCfg, options.Stderr)
	engineCfg.CommandEntrypoint = commands.EntrypointHeadless

	eng := engine.NewQueryEngine(engineCfg)
	emitExecutionContainmentStartupDiagnostic(options.Stderr, eng)
	defer eng.Close()
	defer printResumeHint(options.Stderr, eng)
	if err := resumeConfiguredSession(ctx, eng, resumeSource, options.Stderr); err != nil {
		return renderHeadlessFailure(format, options, err, errorCodeForFailure(err, "session_error"), ExitCode(err))
	}

	queryCtx := ctx
	cancelQuery := func() {}
	if format == outputFormatJSONL {
		queryCtx, cancelQuery = context.WithCancel(ctx)
		defer cancelQuery()
	}
	events, _ := eng.SubmitMessage(queryCtx, prompt)
	var result headlessResult
	if format == outputFormatJSONL {
		writer := enginetransport.NewLifecycleWriter(options.Stdout)
		var streamErr error
		result, streamErr = collectHeadlessEventsWithObserver(
			queryCtx,
			options.Stderr,
			events,
			func(event engine.QueryEvent) error {
				_, err := writer.WriteEvent(event)
				if err != nil {
					cancelQuery()
				}
				return err
			},
		)
		if streamErr != nil {
			return fmt.Errorf("write headless lifecycle event: %w", streamErr)
		}
	} else {
		result = collectHeadlessEvents(queryCtx, options.Stderr, events)
	}
	result.SessionID = eng.SessionID()
	result.Err = sanitizeHeadlessError(result.Err, options.Runtime.apiKey)
	if err := renderHeadlessResult(format, options.Stdout, options.Stderr, result); err != nil {
		return err
	}
	if result.ExitCode != ExitSuccess {
		return renderedExitError(result.ExitCode, result.Err)
	}
	return nil
}

func normalizeHeadlessWriters(options headlessOptions) headlessOptions {
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	return options
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func resolveHeadlessPrompt(promptArgument string, stdin io.Reader, stdinTerminal bool) (string, error) {
	promptProvided := promptArgument != "" && promptArgument != "-"
	if promptProvided && stdinTerminal {
		if strings.TrimSpace(promptArgument) == "" {
			return "", usageErrorf("no prompt provided")
		}
		return promptArgument, nil
	}
	if !promptProvided && promptArgument != "-" && stdinTerminal {
		return "", usageErrorf("no prompt provided (pass an argument, use exec -, or pipe stdin)")
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	stdinText := string(data)
	if promptProvided {
		if strings.TrimSpace(stdinText) == "" {
			return promptArgument, nil
		}
		return promptWithStdinContext(promptArgument, stdinText), nil
	}
	if strings.TrimSpace(stdinText) == "" {
		return "", usageErrorf("no prompt provided via stdin")
	}
	return stdinText, nil
}

func promptWithStdinContext(prompt, stdinText string) string {
	combined := prompt + "\n\n<stdin>\n" + stdinText
	if !strings.HasSuffix(stdinText, "\n") {
		combined += "\n"
	}
	return combined + "</stdin>"
}

func configureHeadlessPermissions(engineCfg *engine.QueryEngineConfig, stderr io.Writer) {
	engineCfg.PermissionPrompt = nil
	if engineCfg.PermissionMode == permission.ModeBypassPermissions {
		engineCfg.CanUseTool = func(_ context.Context, _ string, _ map[string]any, _ *engine.ToolUseContext) (bool, string) {
			return true, ""
		}
		return
	}
	engineCfg.CanUseTool = func(_ context.Context, toolName string, _ map[string]any, _ *engine.ToolUseContext) (bool, string) {
		fmt.Fprintf(stderr, "[permission denied: %s] headless mode (use -y to auto-allow)\n", toolName)
		return false, "headless mode: no interactive permission prompt available (use -y to auto-allow)"
	}
}

type headlessEventObserver func(engine.QueryEvent) error

func collectHeadlessEvents(ctx context.Context, stderr io.Writer, events <-chan engine.QueryEvent) headlessResult {
	result, _ := collectHeadlessEventsWithObserver(ctx, stderr, events, nil)
	return result
}

func collectHeadlessEventsWithObserver(
	ctx context.Context,
	stderr io.Writer,
	events <-chan engine.QueryEvent,
	observer headlessEventObserver,
) (headlessResult, error) {
	result := headlessResult{Status: "completed", ExitCode: ExitSuccess}
	var output strings.Builder
	var observerErr error
	for event := range events {
		if observer != nil && observerErr == nil {
			observerErr = observer(event)
		}
		switch event.Type {
		case engine.EventAssistant:
			if event.AssistantMessage != nil {
				output.WriteString(event.AssistantMessage.Content)
			} else if event.Message != nil {
				output.WriteString(event.Message.Content)
			}
		case engine.EventToolResult:
			name, size := headlessToolResultMetadata(event)
			fmt.Fprintf(stderr, "[%s] completed (%d bytes)\n", name, size)
		case engine.EventCommandResult:
			if event.CommandResult != nil {
				output.WriteString(event.CommandResult.Output)
				if event.CommandResult.Status == engine.CommandResultFailed ||
					event.CommandResult.Status == engine.CommandResultUnsupported {
					result.ErrorCode = "command_failed"
					result.Err = fmt.Errorf("command %s: %s", event.CommandResult.Command, event.CommandResult.Status)
				}
			}
		case engine.EventCompactBoundary:
			fmt.Fprintln(stderr, "--- context compacted ---")
		case engine.EventMaxTurnsReached:
			fmt.Fprintln(stderr, "[max turns reached]")
			result.ErrorCode = "max_turns"
			result.Err = errMaxTurnsReached
		case engine.EventPermissionReview:
			if diagnostic := permissionReviewDiagnostic(event.PermissionReview); diagnostic != "" {
				fmt.Fprintf(stderr, "[%s]\n", diagnostic)
			}
		case engine.EventModelAttempt:
			if notice := modelFallbackNotice(event.ModelAttempt); notice != "" {
				fmt.Fprintf(stderr, "[%s]\n", notice)
			}
		case engine.EventTerminal:
			result.TerminalEvent = event.RuntimeEventEnvelope
			if event.TerminalInfo != nil {
				result.TerminalReason = string(event.TerminalInfo.Reason)
				if event.TerminalInfo.Err != nil {
					result.Err = event.TerminalInfo.Err
					result.ErrorCode = "runtime_error"
				}
			}
		}
	}
	result.Output = output.String()
	classifyHeadlessResult(ctx, &result)
	return result, observerErr
}

func headlessToolResultMetadata(event engine.QueryEvent) (string, int) {
	if event.ToolResultMessage != nil {
		name := event.ToolResultMessage.ToolName
		if name == "" {
			name = "tool"
		}
		return name, len(event.ToolResultMessage.Content)
	}
	if event.Message != nil {
		return "tool", len(event.Message.Content)
	}
	return "tool", 0
}

func classifyHeadlessResult(ctx context.Context, result *headlessResult) {
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		result.Status = "cancelled"
		result.ErrorCode = "cancelled"
		result.Err = context.Canceled
		result.ExitCode = ExitCancelled
		return
	}
	if errors.Is(result.Err, context.Canceled) || engineerrors.IsAbort(result.Err) {
		result.Status = "cancelled"
		result.ErrorCode = "cancelled"
		result.ExitCode = ExitCancelled
		return
	}
	switch engine.TerminalReason(result.TerminalReason) {
	case engine.TerminalAbortedStreaming, engine.TerminalAbortedTools:
		result.Status = "cancelled"
		result.ErrorCode = "cancelled"
		if result.Err == nil {
			result.Err = errors.New("execution cancelled")
		}
		result.ExitCode = ExitCancelled
		return
	case engine.TerminalMaxTurns:
		result.ErrorCode = "max_turns"
		if result.Err == nil {
			result.Err = errMaxTurnsReached
		}
	case engine.TerminalWaitingInput:
		result.ErrorCode = "waiting_input"
		if result.Err == nil {
			result.Err = errors.New("execution requires interactive input")
		}
	}
	if errors.Is(result.Err, errMaxTurnsReached) || result.ErrorCode == "max_turns" {
		result.Status = "max_turns"
		result.ExitCode = ExitFailure
		return
	}
	if result.Err != nil {
		result.Status = "failed"
		if result.ErrorCode == "" {
			result.ErrorCode = "runtime_error"
		}
		result.ExitCode = ExitFailure
	}
}

func sanitizeHeadlessError(err error, exactSecrets ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactSensitiveText(err.Error(), exactSecrets...))
}

func renderHeadlessResult(format outputFormat, stdout, stderr io.Writer, result headlessResult) error {
	if format == outputFormatJSON {
		envelope := headlessEnvelope{
			SchemaVersion:  headlessEnvelopeSchemaVersion,
			Status:         result.Status,
			Output:         result.Output,
			SessionID:      result.SessionID,
			TerminalReason: result.TerminalReason,
			ExitCode:       result.ExitCode,
		}
		if result.Err != nil {
			envelope.Error = &headlessEnvelopeError{Code: result.ErrorCode, Message: result.Err.Error()}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(envelope)
	}
	if format == outputFormatJSONL {
		identity := enginetransport.LifecycleIdentityFromEnvelope(result.TerminalEvent)
		if identity.SessionID == "" {
			identity.SessionID = result.SessionID
		}
		lifecycleResult := enginetransport.LifecycleResult{
			LifecycleIdentity: identity,
			Status:            result.Status,
			Output:            result.Output,
			TerminalReason:    result.TerminalReason,
			ExitCode:          result.ExitCode,
		}
		if result.Err != nil {
			lifecycleResult.Error = &enginetransport.LifecycleError{
				Code:    result.ErrorCode,
				Message: result.Err.Error(),
			}
		}
		return enginetransport.NewLifecycleWriter(stdout).WriteResult(lifecycleResult)
	}
	if result.Output != "" {
		if _, err := io.WriteString(stdout, result.Output); err != nil {
			return fmt.Errorf("write headless output: %w", err)
		}
		if !strings.HasSuffix(result.Output, "\n") {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				return fmt.Errorf("write headless output terminator: %w", err)
			}
		}
	}
	if result.Err != nil {
		_, err := fmt.Fprintf(stderr, "Error: %s\n", result.Err)
		return err
	}
	return nil
}

func renderHeadlessFailure(format outputFormat, options headlessOptions, err error, code string, exitCode int) error {
	options = normalizeHeadlessWriters(options)
	safeErr := sanitizeHeadlessError(err, options.Runtime.apiKey)
	result := headlessResult{
		Status:    "failed",
		ErrorCode: code,
		Err:       safeErr,
		ExitCode:  exitCode,
	}
	if exitCode == ExitCancelled {
		result.Status = "cancelled"
	}
	if renderErr := renderHeadlessResult(format, options.Stdout, options.Stderr, result); renderErr != nil {
		return renderErr
	}
	return renderedExitError(exitCode, safeErr)
}

func errorCodeForFailure(err error, fallback string) string {
	if ExitCode(err) == ExitCancelled {
		return "cancelled"
	}
	if errors.Is(err, session.ErrLegacySessionImportRequired) {
		return "legacy_session_import_required"
	}
	return fallback
}

func formatForError(value string) outputFormat {
	if strings.EqualFold(strings.TrimSpace(value), string(outputFormatJSON)) {
		return outputFormatJSON
	}
	if strings.EqualFold(strings.TrimSpace(value), string(outputFormatJSONL)) {
		return outputFormatJSONL
	}
	return outputFormatText
}

// consumeHeadlessEvents retains the package-level renderer seam used by
// focused engine projection tests.
func consumeHeadlessEvents(stdout, stderr io.Writer, events <-chan engine.QueryEvent) error {
	result := collectHeadlessEvents(context.Background(), stderr, events)
	result.Err = sanitizeHeadlessError(result.Err)
	if err := renderHeadlessResult(outputFormatText, stdout, stderr, result); err != nil {
		return err
	}
	return result.Err
}
