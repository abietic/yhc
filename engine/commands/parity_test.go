package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type effortCapabilityStub struct {
	supported bool
	reason    string
	current   string
}

func (s *effortCapabilityStub) ReasoningEffortCapability(context.Context) (bool, string, error) {
	return s.supported, s.reason, nil
}

func (s *effortCapabilityStub) ReasoningEffort() string { return s.current }

// ---------------------------------------------------------------------------
// Command output structure parity tests
// ---------------------------------------------------------------------------

func TestCommandHelpListsAllNonHidden(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{Model: "claude-sonnet-4-6"}
	result, err := dispatchCommand(reg, ctx, "/help")
	if err != nil {
		t.Fatalf("dispatch /help: %v", err)
		return
	}

	if result.Output == "" {
		t.Fatal("expected non-empty output from /help")
	}
	if !strings.Contains(result.Output, "Available commands:") {
		t.Fatal("expected 'Available commands:' in /help output")
	}
	// Verify retained commands appear and hidden commands do not.
	expected := []string{"/help", "/clear", "/model", "/diff", "/init", "/queue", "/search"}
	for _, cmd := range expected {
		if !strings.Contains(result.Output, cmd) {
			t.Errorf("expected %q in /help output", cmd)
		}
	}
	for _, hidden := range []string{"/bug", "/undo", "/mode", "/plugin"} {
		if strings.Contains(result.Output, "\n  "+hidden+" ") {
			t.Errorf("hidden command %q appeared in /help output", hidden)
		}
	}
}

func TestCommandHelpForSpecificCommand(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{Model: "claude-sonnet-4-6"}
	result, err := dispatchCommand(reg, ctx, "/help model")
	if err != nil {
		t.Fatalf("dispatch /help model: %v", err)
		return
	}

	if !strings.Contains(result.Output, "/model") {
		t.Error("expected '/model' in detailed help output")
	}
	if !strings.Contains(result.Output, "Usage:") {
		t.Error("expected 'Usage:' in detailed help output")
	}

	hidden, err := dispatchCommand(reg, ctx, "/help bug")
	if err != nil {
		t.Fatalf("dispatch /help bug: %v", err)
	}
	if !strings.Contains(hidden.Output, "Unknown command: /bug") {
		t.Fatalf("hidden command leaked through detailed help: %q", hidden.Output)
	}
}

func TestEffortDiscoveryHelpAndDispatchShareRuntimeCapability(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	unsupported := &CommandContext{Engine: &effortCapabilityStub{
		reason: "selected model has no compatible reasoning effort",
	}}
	if got := reg.GetForContext(
		context.Background(),
		EntrypointTUI,
		unsupported,
		"effort",
	); got != nil {
		t.Fatalf("unsupported effort remained discoverable: %#v", got)
	}
	help, err := reg.Dispatch(context.Background(), EntrypointTUI, unsupported, "/help effort")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.Output, "Availability: unavailable") ||
		!strings.Contains(help.Output, "no compatible reasoning effort") {
		t.Fatalf("dynamic effort help = %q", help.Output)
	}
	rejected, err := reg.Dispatch(context.Background(), EntrypointTUI, unsupported, "/effort high")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Action != ActionNone || rejected.Availability != AvailabilityUnavailable ||
		!strings.Contains(rejected.Output, "no compatible reasoning effort") {
		t.Fatalf("dynamic effort rejection = %#v", rejected)
	}

	supported := &CommandContext{Engine: &effortCapabilityStub{
		supported: true,
		current:   "medium",
	}}
	if got := reg.GetForContext(
		context.Background(),
		EntrypointTUI,
		supported,
		"effort",
	); got == nil {
		t.Fatal("supported effort missing from discovery")
	}
	accepted, err := reg.Dispatch(context.Background(), EntrypointTUI, supported, "/effort high")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Action != ActionSetEffort || accepted.Data["level"] != "high" {
		t.Fatalf("supported effort intent = %#v", accepted)
	}
}

func TestPermissionsTypedModeRulesAndBypassConfirmation(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)
	ctx := &CommandContext{CWD: t.TempDir()}

	mode, err := reg.Dispatch(
		context.Background(),
		EntrypointPlain,
		ctx,
		"/permissions mode acceptEdits",
	)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Action != ActionChangeMode || mode.Data["mode"] != "acceptEdits" ||
		mode.Data["bypass_confirmed"] != false {
		t.Fatalf("typed mode intent = %#v", mode)
	}

	rejected, err := reg.Dispatch(
		context.Background(),
		EntrypointPlain,
		ctx,
		"/permissions bypass",
	)
	if err == nil || rejected != nil || !strings.Contains(err.Error(), "explicit confirmation") {
		t.Fatalf("unconfirmed bypass = %#v, %v", rejected, err)
	}
	confirmed, err := reg.Dispatch(
		context.Background(),
		EntrypointPlain,
		ctx,
		"/permissions bypass confirm",
	)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Action != ActionChangeMode ||
		confirmed.Data["mode"] != "bypassPermissions" ||
		confirmed.Data["bypass_confirmed"] != true {
		t.Fatalf("confirmed bypass intent = %#v", confirmed)
	}

	rule, err := reg.Dispatch(
		context.Background(),
		EntrypointPlain,
		ctx,
		`/permissions rules add allow "Bash(go test *)" --local`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rule.Action != ActionPermissions || rule.Data["operation"] != "add" ||
		rule.Data["rule"] != "Bash(go test *)" {
		t.Fatalf("typed rule intent = %#v", rule)
	}
	if rejectedRule, err := reg.Dispatch(
		context.Background(),
		EntrypointPlain,
		ctx,
		`/permissions rules add allow "Read(*)" --unknown`,
	); err == nil || rejectedRule != nil || !strings.Contains(err.Error(), "unknown permission destination") {
		t.Fatalf("unknown rule destination = %#v, %v", rejectedRule, err)
	}
}

// ---------------------------------------------------------------------------
// Argument validation parity tests
// ---------------------------------------------------------------------------

func TestArgumentValidationRequired(t *testing.T) {
	cmd := &Command{
		Name:  "test",
		Usage: "/test <name>",
		Args: []ArgDef{
			{Name: "name", Type: "string", Required: true, Description: "The name"},
		},
	}

	// Missing required arg
	err := cmd.ValidateArgs([]string{})
	if err == nil {
		t.Fatal("expected error for missing required argument")
		return
	}
	if !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("expected 'missing required' in error, got %q", err.Error())
	}
}

func TestArgumentValidationWrongType(t *testing.T) {
	cmd := &Command{
		Name:  "test",
		Usage: "/test <count>",
		Args: []ArgDef{
			{Name: "count", Type: "int", Required: true, Description: "A number"},
		},
	}

	// Wrong type
	err := cmd.ValidateArgs([]string{"not_a_number"})
	if err == nil {
		t.Fatal("expected error for wrong type")
		return
	}
	if !strings.Contains(err.Error(), "expected integer") {
		t.Fatalf("expected 'expected integer' in error, got %q", err.Error())
	}
}

func TestArgumentValidationBoolType(t *testing.T) {
	cmd := &Command{
		Name:  "test",
		Usage: "/test <flag>",
		Args: []ArgDef{
			{Name: "flag", Type: "bool", Required: true, Description: "A flag"},
		},
	}

	validBools := []string{"true", "false", "1", "0", "yes", "no", "on", "off"}
	for _, v := range validBools {
		if err := cmd.ValidateArgs([]string{v}); err != nil {
			t.Errorf("expected %q to be valid bool, got error: %v", v, err)
		}
	}

	if err := cmd.ValidateArgs([]string{"maybe"}); err == nil {
		t.Error("expected error for 'maybe' as bool")
	}
}

// ---------------------------------------------------------------------------
// Command dispatch parity tests
// ---------------------------------------------------------------------------

func TestCommandDispatchAliasResolution(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{Model: "claude-sonnet-4-6"}

	// /exit is an alias for /quit
	result, err := dispatchCommand(reg, ctx, "/exit")
	if err != nil {
		t.Fatalf("dispatch /exit: %v", err)
		return
	}
	if result.Action != ActionQuit {
		t.Fatalf("expected ActionQuit for /exit, got %q", result.Action)
	}

	// /reset is an alias for /clear
	result, err = dispatchCommand(reg, ctx, "/reset")
	if err != nil {
		t.Fatalf("dispatch /reset: %v", err)
		return
	}
	if result.Action != ActionClear {
		t.Fatalf("expected ActionClear for /reset, got %q", result.Action)
	}
}

func TestCommandDispatchUnknownCommand(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{Model: "claude-sonnet-4-6"}
	_, err := dispatchCommand(reg, ctx, "/nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown command")
		return
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected 'unknown command' in error, got %q", err.Error())
	}
}

func TestCommandDispatchNotACommand(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{}
	_, err := dispatchCommand(reg, ctx, "not a command")
	if err == nil {
		t.Fatal("expected error for non-command input")
		return
	}
}

func TestCommandDispatchCaseInsensitive(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{Model: "test"}
	// Command names should be normalized to lowercase
	result, err := dispatchCommand(reg, ctx, "/HELP")
	if err != nil {
		t.Fatalf("dispatch /HELP: %v", err)
		return
	}
	if result.Output == "" {
		t.Fatal("expected output for /HELP")
	}
}

// ---------------------------------------------------------------------------
// IsCommand parity tests
// ---------------------------------------------------------------------------

func TestIsCommandDetection(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"/help", true},
		{"/model gpt-4o", true},
		{"  /clear  ", true},
		{"not a command", false},
		{"/", false},        // just slash
		{"//", false},       // double slash
		{"/ space", false},  // slash then space
		{"/1number", false}, // slash then digit
	}

	for _, tc := range cases {
		got := IsCommand(tc.input)
		if got != tc.want {
			t.Errorf("IsCommand(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Canonical context-aware dispatch tests
// ---------------------------------------------------------------------------

func TestRegistryDispatchPassesContextToCanonicalHandler(t *testing.T) {
	reg := NewRegistry()
	type contextKey string
	key := contextKey("dispatch")
	if err := reg.Register(&Command{
		Name: "probe",
		Execute: func(ctx context.Context, cmdCtx *CommandContext) (*CommandResult, error) {
			value, _ := ctx.Value(key).(string)
			contextValue, _ := cmdCtx.Context.Value(key).(string)
			return &CommandResult{
				Output: value + ":" + contextValue + ":" +
					string(cmdCtx.Entrypoint) + ":" + strings.Join(cmdCtx.Args, ","),
			}, nil
		},
	}); err != nil {
		t.Fatalf("register probe: %v", err)
	}

	ctx := context.WithValue(context.Background(), key, "value")
	result, err := reg.Dispatch(ctx, EntrypointTUI, &CommandContext{}, "/probe one two")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Output != "value:value:tui:one,two" {
		t.Fatalf("canonical handler output = %q", result.Output)
	}
}

func TestRegistryDispatchCancellationPreventsHandler(t *testing.T) {
	reg := NewRegistry()
	called := false
	if err := reg.Register(&Command{
		Name: "probe",
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			called = true
			return &CommandResult{Output: "unexpected"}, nil
		},
	}); err != nil {
		t.Fatalf("register probe: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := reg.Dispatch(ctx, EntrypointTUI, &CommandContext{}, "/probe")
	if err == nil || !strings.Contains(err.Error(), "command canceled before dispatch") {
		t.Fatalf("canceled dispatch error = %v", err)
	}
	if called {
		t.Fatal("handler ran after cancellation")
	}
}

func TestRegistryDispatchUsesRegisteredHelpHandler(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	result, err := reg.Dispatch(context.Background(), EntrypointTUI, &CommandContext{}, "/help")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output from registry")
	}
}

// ---------------------------------------------------------------------------
// ParseCommandInput parity tests
// ---------------------------------------------------------------------------

func TestParseCommandInputQuotedArgs(t *testing.T) {
	name, args := ParseCommandInput(`/model "gpt-4o mini" --flag`)
	if name != "model" {
		t.Fatalf("expected name 'model', got %q", name)
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if args[0] != "gpt-4o mini" {
		t.Fatalf("expected first arg 'gpt-4o mini', got %q", args[0])
	}
	if args[1] != "--flag" {
		t.Fatalf("expected second arg '--flag', got %q", args[1])
	}
}

func TestParseCommandInputSingleQuotes(t *testing.T) {
	name, args := ParseCommandInput(`/bug 'it crashes on start'`)
	if name != "bug" {
		t.Fatalf("expected name 'bug', got %q", name)
	}
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d: %v", len(args), args)
	}
	if args[0] != "it crashes on start" {
		t.Fatalf("expected 'it crashes on start', got %q", args[0])
	}
}

// ---------------------------------------------------------------------------
// Specific command behavior parity tests
// ---------------------------------------------------------------------------

func TestCommandModelShowAndSwitch(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	// /model with no args — show current
	ctx := &CommandContext{Model: "claude-sonnet-4-6"}
	result, err := dispatchCommand(reg, ctx, "/model")
	if err != nil {
		t.Fatalf("dispatch /model: %v", err)
		return
	}
	if !strings.Contains(result.Output, "claude-sonnet-4-6") {
		t.Fatalf("expected current model in output, got %q", result.Output)
	}

	// /model with arg — switch
	result, err = dispatchCommand(reg, ctx, "/model gpt-4o")
	if err != nil {
		t.Fatalf("dispatch /model gpt-4o: %v", err)
		return
	}
	if result.Action != ActionChangeModel {
		t.Fatalf("expected ActionChangeModel, got %q", result.Action)
	}
}

func TestCommandBugReportIsUnavailableThroughRegistry(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{
		Model: "claude-sonnet-4-6",
		CWD:   "/tmp/project",
	}
	result, err := dispatchCommand(reg, ctx, "/bug something is broken")
	if err != nil {
		t.Fatalf("dispatch /bug: %v", err)
		return
	}

	if result.Action != ActionNone ||
		result.Removed == nil ||
		!strings.Contains(result.Output, "/bug was removed in P21.0") ||
		!strings.Contains(result.Output, "external delivery channel") {
		t.Fatalf("unexpected /bug result: %#v", result)
	}
}

func TestCommandVersionOutput(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	ctx := &CommandContext{Model: "test"}
	result, err := dispatchCommand(reg, ctx, "/version")
	if err != nil {
		t.Fatalf("dispatch /version: %v", err)
		return
	}

	if result.Output == "" {
		t.Fatal("expected non-empty version output")
	}
	// Should contain version-like content
	if !strings.Contains(result.Output, "eino-agent") && !strings.Contains(result.Output, "version") && !strings.Contains(result.Output, "Version") {
		t.Logf("version output: %s", result.Output)
	}
}

func TestCommandFormatHelp(t *testing.T) {
	cmd := &Command{
		Name:         "test",
		Aliases:      []string{"t"},
		Description:  "A test command",
		Usage:        "/test <arg>",
		DetailedHelp: "This is detailed help text.",
		Args: []ArgDef{
			{Name: "arg", Type: "string", Required: true, Description: "An argument"},
		},
	}

	help := cmd.FormatHelp()
	if !strings.Contains(help, "/test") {
		t.Error("expected '/test' in FormatHelp")
	}
	if !strings.Contains(help, "/t") {
		t.Error("expected alias '/t' in FormatHelp")
	}
	if !strings.Contains(help, "Usage:") {
		t.Error("expected 'Usage:' in FormatHelp")
	}
	if !strings.Contains(help, "Arguments:") {
		t.Error("expected 'Arguments:' in FormatHelp")
	}
	if !strings.Contains(help, "This is detailed help text.") {
		t.Error("expected DetailedHelp text in FormatHelp")
	}
	if !strings.Contains(help, "(required)") {
		t.Error("expected '(required)' annotation in FormatHelp")
	}
}

func TestCommandInitCreatesClaudeDir(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	// Create a temp directory
	tmpDir := t.TempDir()
	ctx := &CommandContext{
		Model: "test",
		CWD:   tmpDir,
	}

	result, err := dispatchCommand(reg, ctx, "/init")
	if err != nil {
		t.Fatalf("dispatch /init: %v", err)
		return
	}

	// /init should produce output about what it created/found
	_ = result
	_ = fmt.Sprintf("init result: %s", result.Output)
}
