package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/buildinfo"
	"github.com/abietic/yhc/internal/identity"
)

func TestRootCommandPublishesYHCIdentityAndNoLegacyCommandAlias(t *testing.T) {
	root := newRootCommand()
	if got, want := root.Use, identity.CommandName+" [prompt]"; got != want {
		t.Fatalf("root use = %q, want %q", got, want)
	}
	if root.Name() != identity.CommandName || !strings.Contains(root.Short, identity.ProductLongName) {
		t.Fatalf("root identity name=%q short=%q", root.Name(), root.Short)
	}
	for _, alias := range root.Aliases {
		if alias == identity.LegacyCommandName {
			t.Fatalf("root retains legacy command alias %q", alias)
		}
	}

	var completion bytes.Buffer
	root.SetOut(&completion)
	root.SetErr(&completion)
	root.SetArgs([]string{"completion", "zsh"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(completion.String(), "#compdef "+identity.CommandName) ||
		!strings.Contains(completion.String(), "compdef _"+identity.CommandName+" "+identity.CommandName) ||
		strings.Contains(completion.String(), identity.LegacyCommandName) {
		t.Fatalf("completion projects a noncanonical command: %q", completion.String())
	}
}

func TestCLICommandTreeAndFlagScope(t *testing.T) {
	root := newRootCommand()
	names := make([]string, 0, len(root.Commands()))
	for _, command := range root.Commands() {
		names = append(names, command.Name())
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "completion,config,doctor,exec,goal,mcp,migrate-state,permission-review-audit,plugins,resume,serve,sessions,version"; got != want {
		t.Fatalf("root commands = %q, want %q", got, want)
	}
	if root.PersistentFlags().HasFlags() {
		t.Fatalf("root has persistent flags: %s", root.PersistentFlags().FlagUsages())
	}

	execCommand := findCommand(t, root, "exec")
	goalCommand := findCommand(t, root, "goal")
	goalRunCommand := findCommand(t, root, "goal", "run")
	resumeCommand := findCommand(t, root, "resume")
	acpCommand := findCommand(t, root, "serve", "acp")
	appCommand := findCommand(t, root, "serve", "app")
	mcpCommand := findCommand(t, root, "serve", "mcp")
	versionCommand := findCommand(t, root, "version")
	completionCommand := findCommand(t, root, "completion")
	sessionsCommand := findCommand(t, root, "sessions")
	sessionsListCommand := findCommand(t, root, "sessions", "list")
	configCommand := findCommand(t, root, "config")
	doctorCommand := findCommand(t, root, "doctor")
	inspectionMCPCommand := findCommand(t, root, "mcp")
	pluginsCommand := findCommand(t, root, "plugins")
	permissionReviewAuditCommand := findCommand(
		t,
		root,
		"permission-review-audit",
	)
	permissionReviewAuditReportCommand := findCommand(
		t,
		root,
		"permission-review-audit",
		"report",
	)
	permissionReviewAuditDeleteCommand := findCommand(
		t,
		root,
		"permission-review-audit",
		"delete",
	)

	for _, command := range []*cobra.Command{
		root,
		execCommand,
		goalRunCommand,
		resumeCommand,
		acpCommand,
		appCommand,
	} {
		for _, flag := range []string{
			"provider", "model", "api-key", "base-url", "fallback-model",
			"sandbox",
			"provider-preflight", "permission-mode", "yolo", "max-turns",
			"tools", "permission-review-shadow", "permission-review-provider",
			"permission-review-model", "permission-review-api-key",
			"permission-review-base-url", "permission-review-timeout",
			"permission-review-audit", "permission-review-audit-dir",
		} {
			if command.Flags().Lookup(flag) == nil {
				t.Fatalf("%s missing runtime flag --%s", command.CommandPath(), flag)
			}
		}
	}
	for _, command := range []*cobra.Command{
		mcpCommand,
		goalCommand,
		versionCommand,
		completionCommand,
		sessionsCommand,
		sessionsListCommand,
		configCommand,
		doctorCommand,
		inspectionMCPCommand,
		pluginsCommand,
		permissionReviewAuditCommand,
		permissionReviewAuditReportCommand,
		permissionReviewAuditDeleteCommand,
	} {
		for _, flag := range []string{
			"provider", "model", "api-key", "permission-mode", "max-turns",
			"tools", "mouse", "permission-review-shadow",
			"permission-review-audit", "permission-review-audit-dir",
		} {
			if command.Flags().Lookup(flag) != nil || command.InheritedFlags().Lookup(flag) != nil {
				t.Fatalf("%s unexpectedly exposes --%s", command.CommandPath(), flag)
			}
		}
	}
	if sessionsCommand.PersistentFlags().Lookup("output-format") == nil {
		t.Fatal("sessions command is missing its scoped --output-format flag")
	}
	for _, command := range []*cobra.Command{configCommand, inspectionMCPCommand, pluginsCommand} {
		if command.PersistentFlags().Lookup("output-format") == nil {
			t.Fatalf("%s is missing its scoped --output-format flag", command.CommandPath())
		}
	}
	if doctorCommand.Flags().Lookup("output-format") == nil {
		t.Fatal("doctor is missing its scoped --output-format flag")
	}
	if permissionReviewAuditCommand.PersistentFlags().Lookup("output-format") == nil ||
		permissionReviewAuditCommand.PersistentFlags().Lookup("dir") == nil ||
		permissionReviewAuditDeleteCommand.Flags().Lookup("confirm") == nil {
		t.Fatal("permission-review-audit scoped flags are incomplete")
	}
	for _, name := range []string{"archive"} {
		if command, _, err := sessionsCommand.Find([]string{name}); err == nil && command != sessionsCommand {
			t.Fatalf("sessions unexpectedly exposes %s", name)
		}
	}
	deleteCommand, _, err := sessionsCommand.Find([]string{"delete"})
	if err != nil || deleteCommand == sessionsCommand {
		t.Fatal("sessions is missing delete")
	}
	recoverCommand, _, err := sessionsCommand.Find([]string{"recover-workboard"})
	if err != nil || recoverCommand == sessionsCommand ||
		recoverCommand.Flags().Lookup("board-id") == nil ||
		recoverCommand.Flags().Lookup("revision") == nil ||
		recoverCommand.Flags().Lookup("acknowledge-data-loss") == nil {
		t.Fatal("sessions recover-workboard contract is incomplete")
	}
	if execCommand.Flags().Lookup("mouse") != nil || execCommand.Flags().Lookup("plain") != nil || execCommand.Flags().Lookup("print") != nil {
		t.Fatalf("exec exposes interactive root flags: %s", execCommand.Flags().FlagUsages())
	}
	if goalRunCommand.Flags().Lookup("resume") == nil ||
		goalRunCommand.Flags().Lookup("max-continuations") == nil ||
		goalRunCommand.Flags().Lookup("output-format") == nil {
		t.Fatalf("goal run scoped flags are incomplete: %s", goalRunCommand.Flags().FlagUsages())
	}
}

func TestRootPositionalPromptFailsInsteadOfLaunchingInteractiveMode(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"this used to be ignored"})
	err := root.Execute()
	if ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "exec") {
		t.Fatalf("root positional error = %v, exit = %d", err, ExitCode(err))
	}
}

func TestRootRuntimeFlagCannotLeakIntoSubcommand(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"--provider", "openai", "version"})
	err := root.Execute()
	if ExitCode(err) != ExitUsage || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("prefixed root flag error = %v, exit = %d", err, ExitCode(err))
	}
}

func TestCLIUsageFailuresUseExit2(t *testing.T) {
	for _, args := range [][]string{
		{"serve", "bogus"},
		{"completion", "invalid-shell"},
		{"version", "extra"},
		{"serve", "mcp", "--provider", "openai"},
	} {
		root := newRootCommand()
		root.SetArgs(args)
		err := root.Execute()
		if ExitCode(err) != ExitUsage {
			t.Fatalf("args %v error = %v, exit = %d", args, err, ExitCode(err))
		}
	}
}

func TestVersionAndCompletionDoNotInitializeRuntime(t *testing.T) {
	t.Run("version json", func(t *testing.T) {
		root := newRootCommand()
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetArgs([]string{"version", "--output-format", "json"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		var info buildinfo.Info
		if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
			t.Fatalf("version output is not JSON: %v; %q", err, stdout.String())
		}
		if info.SchemaVersion != buildinfo.SchemaVersion || info.Version == "" || info.GoVersion == "" || info.OS == "" || info.Arch == "" {
			t.Fatalf("incomplete version envelope: %#v", info)
		}
	})

	t.Run("completion", func(t *testing.T) {
		root := newRootCommand()
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetArgs([]string{"completion", "bash"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), "__start_yhc") || strings.Contains(stdout.String(), "eino-agent") {
			t.Fatalf("unexpected completion output: %q", stdout.String())
		}
	})
}

func TestResolveHeadlessPromptContracts(t *testing.T) {
	tests := []struct {
		name           string
		prompt         string
		stdin          string
		stdinTerminal  bool
		want           string
		wantUsageError bool
	}{
		{name: "argument only", prompt: "summarize", stdinTerminal: true, want: "summarize"},
		{name: "stdin only", stdin: "inspect tests\n", want: "inspect tests\n"},
		{name: "dash stdin", prompt: "-", stdin: "inspect tests\n", want: "inspect tests\n"},
		{name: "argument plus stdin", prompt: "summarize", stdin: "test output\n", want: "summarize\n\n<stdin>\ntest output\n</stdin>"},
		{name: "empty pipe ignored with argument", prompt: "summarize", stdin: "\n", want: "summarize"},
		{name: "missing terminal input", stdinTerminal: true, wantUsageError: true},
		{name: "empty forced stdin", prompt: "-", stdin: " \n", wantUsageError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveHeadlessPrompt(test.prompt, strings.NewReader(test.stdin), test.stdinTerminal)
			if test.wantUsageError {
				if ExitCode(err) != ExitUsage {
					t.Fatalf("error = %v, exit = %d", err, ExitCode(err))
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("prompt = %q, err = %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestHeadlessJSONEnvelopeAndDiagnosticsAreRedacted(t *testing.T) {
	const secret = "sk-secret-value"
	events := make(chan engine.QueryEvent, 2)
	events <- engine.QueryEvent{
		Type: engine.EventToolResult,
		ToolResultMessage: &schema.Message{
			Role:     schema.Tool,
			ToolName: "Bash",
			Content:  "authorization=Bearer " + secret + "\nprivate result body",
		},
	}
	events <- engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalModelError,
			Err:    errors.New("api_key=" + secret + " request https://user:pass@example.com/private?token=" + secret),
		},
	}
	close(events)

	var stdout, stderr bytes.Buffer
	result := collectHeadlessEvents(context.Background(), &stderr, events)
	result.Err = sanitizeHeadlessError(result.Err, secret)
	if err := renderHeadlessResult(outputFormatJSON, &stdout, &stderr, result); err != nil {
		t.Fatal(err)
	}
	var envelope headlessEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON envelope: %v; %q", err, stdout.String())
	}
	if envelope.SchemaVersion != headlessEnvelopeSchemaVersion || envelope.Status != "failed" || envelope.ExitCode != ExitFailure || envelope.Error == nil || envelope.Error.Code != "runtime_error" {
		t.Fatalf("envelope = %#v", envelope)
	}
	combined := stdout.String() + stderr.String()
	for _, forbidden := range []string{secret, "private result body", "user:pass", "/private", "?token="} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("output leaked %q: %q", forbidden, combined)
		}
	}
	if !strings.Contains(stderr.String(), "[Bash] completed (") {
		t.Fatalf("missing safe tool diagnostic: %q", stderr.String())
	}
}

func TestPermissionReviewDiagnosticsExposeOnlyBoundedShadowStatus(t *testing.T) {
	const secret = "secret-control"
	events := make(chan engine.QueryEvent, 3)
	events <- engine.QueryEvent{
		Type: engine.EventPermissionReview,
		PermissionReview: &engine.PermissionReviewEvent{
			Phase:         engine.PermissionReviewChecking,
			CanonicalTool: "Read",
		},
	}
	events <- engine.QueryEvent{
		Type: engine.EventPermissionReview,
		PermissionReview: &engine.PermissionReviewEvent{
			Phase:         engine.PermissionReviewCompleted,
			CanonicalTool: "Read\n" + secret,
			Decision:      "approve",
			ReasonCode:    "expected_safe",
			RequestID:     secret,
		},
	}
	close(events)

	var stderr bytes.Buffer
	result := collectHeadlessEvents(context.Background(), &stderr, events)
	if result.Status != "completed" {
		t.Fatalf("headless result = %#v", result)
	}
	output := stderr.String()
	if !strings.Contains(output, "permission review shadow checking: Read") ||
		!strings.Contains(output, "permission review shadow completed: tool approve/expected_safe") {
		t.Fatalf("permission review diagnostics = %q", output)
	}
	if strings.Contains(output, secret) {
		t.Fatalf("permission review diagnostics leaked opaque or unsafe fields: %q", output)
	}
}

func TestP462HeadlessFallbackNoticeDoesNotChangeStructuredOutput(t *testing.T) {
	events := make(chan engine.QueryEvent, 3)
	events <- engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID: "alternate", AttemptIndex: 1,
			Profile: "fallback.profile", Phase: engine.ModelAttemptStarted,
			SwitchCount: 1, APIModel: "secret-api-model",
		},
	}
	events <- engine.QueryEvent{
		Type:             engine.EventAssistant,
		AssistantMessage: &schema.Message{Content: "assistant output"},
	}
	events <- engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalCompleted,
		},
	}
	close(events)

	var diagnostics bytes.Buffer
	result := collectHeadlessEvents(
		context.Background(),
		&diagnostics,
		events,
	)
	if result.Output != "assistant output" || result.Status != "completed" {
		t.Fatalf("headless result = %#v", result)
	}
	const notice = "Model fallback: profile fallback.profile after overload (switch 1)"
	if strings.Count(diagnostics.String(), notice) != 1 {
		t.Fatalf("headless diagnostics = %q", diagnostics.String())
	}
	var stdout, stderr bytes.Buffer
	if err := renderHeadlessResult(
		outputFormatJSON,
		&stdout,
		&stderr,
		result,
	); err != nil {
		t.Fatal(err)
	}
	var envelope headlessEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Output != "assistant output" ||
		strings.Contains(stdout.String(), notice) ||
		strings.Contains(stdout.String()+diagnostics.String(), "secret-api-model") {
		t.Fatalf(
			"headless fallback crossed result boundary: envelope=%#v diagnostics=%q",
			envelope,
			diagnostics.String(),
		)
	}
}

func TestP462FallbackNoticeRejectsIncompleteOrUnsafeAttempt(t *testing.T) {
	const secret = "secret-control"
	for _, attempt := range []*engine.ModelAttemptEvent{
		nil,
		{AttemptID: "primary", Profile: "primary", Phase: engine.ModelAttemptStarted},
		{AttemptID: "discarded", AttemptIndex: 1, Profile: "safe", Phase: engine.ModelAttemptDiscarded, SwitchCount: 1},
		{AttemptID: "missing-switch", AttemptIndex: 1, Profile: "safe", Phase: engine.ModelAttemptStarted},
		{AttemptID: "unsafe", AttemptIndex: 1, Profile: "safe\n" + secret, Phase: engine.ModelAttemptStarted, SwitchCount: 1},
	} {
		events := make(chan engine.QueryEvent, 1)
		events <- engine.QueryEvent{
			Type: engine.EventModelAttempt, ModelAttempt: attempt,
		}
		close(events)
		var stderr bytes.Buffer
		_ = collectHeadlessEvents(context.Background(), &stderr, events)
		if stderr.Len() != 0 || strings.Contains(stderr.String(), secret) {
			t.Fatalf("attempt %#v produced diagnostics %q", attempt, stderr.String())
		}
	}
}

func TestHeadlessCancellationUsesExit130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := make(chan engine.QueryEvent, 1)
	events <- engine.QueryEvent{Type: engine.EventTerminal, TerminalInfo: &engine.Terminal{Reason: engine.TerminalAbortedStreaming}}
	close(events)
	result := collectHeadlessEvents(ctx, &bytes.Buffer{}, events)
	if result.Status != "cancelled" || result.ErrorCode != "cancelled" || result.ExitCode != ExitCancelled || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("cancelled result = %#v", result)
	}
}

func TestRedactSensitiveTextCoversStructuredCredentials(t *testing.T) {
	input := `{"api_key":"sk-json-secret","authorization":"Bearer bearer-secret","cookie":"session=cookie-secret"} request https://user:pass@example.com/private?token=query-secret`
	got := redactSensitiveText(input)
	for _, forbidden := range []string{
		"sk-json-secret",
		"bearer-secret",
		"cookie-secret",
		"user:pass",
		"/private",
		"query-secret",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redaction leaked %q: %q", forbidden, got)
		}
	}
}

func TestHeadlessRenderRedactsBearerWithoutExactSecret(t *testing.T) {
	const secret = "bearer-secret-without-flag"
	for _, format := range []outputFormat{outputFormatText, outputFormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			events := make(chan engine.QueryEvent, 1)
			events <- engine.QueryEvent{
				Type: engine.EventTerminal,
				TerminalInfo: &engine.Terminal{
					Reason: engine.TerminalModelError,
					Err:    errors.New("authorization=Bearer " + secret),
				},
			}
			close(events)

			var stdout, stderr bytes.Buffer
			result := collectHeadlessEvents(context.Background(), &stderr, events)
			result.Err = sanitizeHeadlessError(result.Err)
			if err := renderHeadlessResult(format, &stdout, &stderr, result); err != nil {
				t.Fatal(err)
			}
			if combined := stdout.String() + stderr.String(); strings.Contains(combined, secret) {
				t.Fatalf("%s output leaked bearer token: %q", format, combined)
			}
		})
	}
}

func TestBlockingCLIEntrypointsObserveCancellation(t *testing.T) {
	t.Run("plain input", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader, writer := io.Pipe()
		t.Cleanup(func() {
			_ = reader.Close()
			_ = writer.Close()
		})
		result := make(chan error, 1)
		input := newPlainInputBroker(bufio.NewReader(reader))
		go func() {
			result <- input.next(ctx).err
		}()
		cancel()
		select {
		case err := <-result:
			if ExitCode(err) != ExitCancelled {
				t.Fatalf("plain cancellation error = %v, exit = %d", err, ExitCode(err))
			}
		case <-time.After(time.Second):
			t.Fatal("plain input did not observe cancellation")
		}
	})

	t.Run("protocol wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitForServeDone(ctx, make(chan struct{})); ExitCode(err) != ExitCancelled {
			t.Fatalf("serve cancellation error = %v, exit = %d", err, ExitCode(err))
		}
	})
}

func findCommand(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	command, remaining, err := root.Find(path)
	if err != nil || len(remaining) != 0 || command == root {
		t.Fatalf("find command %v: command=%v remaining=%v err=%v", path, command, remaining, err)
	}
	return command
}
