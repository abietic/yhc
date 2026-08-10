package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
)

// --- Test helpers ---

func dispatchCommand(
	registry *Registry,
	cmdCtx *CommandContext,
	input string,
) (*CommandResult, error) {
	return registry.Dispatch(context.Background(), EntrypointTUI, cmdCtx, input)
}

func testPluginCommand(name string, aliases ...string) *Command {
	return &Command{
		Name:    name,
		Aliases: aliases,
		Kind:    CommandKindPromptWorkflow,
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			return &CommandResult{Output: name, Action: ActionPrompt}, nil
		},
	}
}

func newTestContext() *CommandContext {
	return &CommandContext{
		CWD:       os.TempDir(),
		Model:     "test-model",
		SessionID: "test-session-123",
		Messages:  []*schema.Message{},
		Extra:     map[string]any{},
	}
}

// --- /help tests ---

func TestHelpListsCommands(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{}, "/help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !strings.Contains(result.Output, "Available commands:") {
		t.Errorf("expected 'Available commands:' in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "/help") {
		t.Errorf("expected '/help' in command list")
	}
	if !strings.Contains(result.Output, "/clear") {
		t.Errorf("expected '/clear' in command list")
	}
	if !strings.Contains(result.Output, "Use /help <command>") {
		t.Errorf("expected help hint at end of output")
	}
}

func TestHelpShowsDetailedHelpForCommand(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{}, "/help diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !strings.Contains(result.Output, "/diff") {
		t.Errorf("expected '/diff' in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "Usage:") {
		t.Errorf("expected 'Usage:' in detailed help")
	}
	// Should contain detailed help text
	if !strings.Contains(result.Output, "staged") {
		t.Errorf("expected detailed help to mention 'staged'")
	}
}

func TestHelpUnknownCommand(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{}, "/help nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !strings.Contains(result.Output, "Unknown command: /nonexistent") {
		t.Errorf("expected unknown command message, got %q", result.Output)
	}
}

func TestHelpShowsAliases(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{}, "/help clear")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !strings.Contains(result.Output, "Aliases:") {
		t.Errorf("expected 'Aliases:' for clear command (has reset)")
	}
	if !strings.Contains(result.Output, "/reset") {
		t.Errorf("expected '/reset' alias in help")
	}
	if strings.Contains(result.Output, "/new") {
		t.Errorf("removed /new alias still appears in clear help")
	}
}

func TestThemeRemainsAvailableWhileColorAndTagAreUnavailable(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	themes, err := dispatchCommand(r, &CommandContext{}, "/theme")
	if err != nil {
		t.Fatalf("dispatch /theme catalog: %v", err)
	}
	for _, want := range []string{
		"polar-night (default)", "daybreak", "dark-ansi", "light-ansi",
		"snowy", "aubergine", "dark, light",
	} {
		if !strings.Contains(themes.Output, want) {
			t.Errorf("/theme catalog = %q, want %q", themes.Output, want)
		}
	}

	theme, err := dispatchCommand(r, &CommandContext{}, "/theme light")
	if err != nil {
		t.Fatalf("dispatch /theme: %v", err)
		return
	}
	if theme.Action != ActionChangeTheme || theme.Data["theme"] != "light" {
		t.Fatalf("unexpected theme result: %#v", theme)
	}

	color, err := dispatchCommand(r, &CommandContext{}, "/color #D77757")
	if err != nil {
		t.Fatalf("dispatch /color: %v", err)
		return
	}
	if color.Action != ActionNone || color.Removed == nil || !strings.Contains(color.Output, "/theme") {
		t.Fatalf("unexpected color result: %#v", color)
	}

	tag, err := dispatchCommand(r, &CommandContext{}, "/tag bugfix")
	if err != nil {
		t.Fatalf("dispatch /tag: %v", err)
		return
	}
	if tag.Action != ActionNone || tag.Removed == nil || !strings.Contains(tag.Output, "/sessions rename") {
		t.Fatalf("unexpected tag result: %#v", tag)
	}
}

func TestKeybindingsCommandShowsShortcuts(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{}, "/keybindings")
	if err != nil {
		t.Fatalf("dispatch /keybindings: %v", err)
		return
	}
	for _, want := range []string{"Key Bindings", "Ctrl+C", "Ctrl+D", "Shift+Tab"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("expected keybindings output to contain %q, got:\n%s", want, result.Output)
		}
	}
}

func TestFeedbackAliasReturnsCompatibilityError(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{Model: "test-model", CWD: "/tmp/project"}, "/feedback user feedback text")
	if err != nil {
		t.Fatalf("dispatch /feedback: %v", err)
		return
	}
	if result.Action != ActionNone ||
		result.Removed == nil ||
		!strings.Contains(result.Output, "/feedback was removed in P21.0") ||
		!strings.Contains(result.Output, "external delivery channel") {
		t.Fatalf("unexpected feedback alias output:\n%s", result.Output)
	}
}

// --- /version tests ---

func TestVersionShowsInfo(t *testing.T) {
	ctx := newTestContext()
	result, err := executeVersion(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	for _, want := range []string{
		"YHC version information",
		"Version:",
		"Commit:",
		"Go:",
		"Platform:",
	} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, result.Output)
		}
	}
}

func TestVersionShowsGoVersion(t *testing.T) {
	ctx := newTestContext()
	result, err := executeVersion(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	// Should contain the go version prefix
	if !strings.Contains(result.Output, "go1.") {
		t.Errorf("expected Go version (go1.x) in output")
	}
}

// --- /diff tests ---

type fixedWorkspaceDiffProvider struct {
	snapshot WorkspaceDiffSnapshot
	err      error
}

func (p fixedWorkspaceDiffProvider) WorkspaceDiff(
	context.Context,
	WorkspaceDiffMode,
) (WorkspaceDiffSnapshot, error) {
	return p.snapshot, p.err
}

func TestDiffNotGitRepo(t *testing.T) {
	ctx := newTestContext()
	ctx.Engine = fixedWorkspaceDiffProvider{snapshot: WorkspaceDiffSnapshot{State: WorkspaceDiffNotGit}}

	result, err := executeDiff(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !strings.Contains(result.Output, "not a Git repository") {
		t.Errorf("expected explicit non-Git message, got %q", result.Output)
	}
}

func TestDiffInGitRepo(t *testing.T) {
	ctx := newTestContext()
	ctx.Engine = fixedWorkspaceDiffProvider{snapshot: WorkspaceDiffSnapshot{State: WorkspaceDiffReady, Mode: WorkspaceDiffStat}}

	result, err := executeDiff(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	// Should either show changes or "No uncommitted changes"
	if result.Output == "" {
		t.Errorf("expected non-empty output")
	}
}

func TestDiffStagedMode(t *testing.T) {
	ctx := newTestContext()
	ctx.Engine = fixedWorkspaceDiffProvider{snapshot: WorkspaceDiffSnapshot{State: WorkspaceDiffReady, Mode: WorkspaceDiffStaged}}

	result, err := executeDiff(ctx, "staged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	// Should show staged info or "No staged changes"
	if !strings.Contains(result.Output, "staged") && !strings.Contains(result.Output, "Staged") {
		t.Errorf("expected 'staged' in output for staged mode, got %q", result.Output)
	}
}

// --- /init tests ---

func TestInitReturnsProjectNativePromptWithoutWriting(t *testing.T) {
	dir, err := os.MkdirTemp("", "eino-test-init-*")
	if err != nil {
		t.Fatal(err)
		return
	}
	defer os.RemoveAll(dir) //nolint:errcheck

	ctx := newTestContext()
	ctx.CWD = dir

	result, err := executeInit(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	// Should trigger ActionPrompt
	if result.Action != ActionPrompt {
		t.Errorf("expected ActionPrompt, got %q", result.Action)
	}

	if !strings.Contains(result.Output, "AGENTS.md") ||
		!strings.Contains(result.Output, "ordinary Read") ||
		!strings.Contains(result.Output, "Do not create CLAUDE.md") {
		t.Fatalf("project-native init prompt = %q", result.Output)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("/init handler wrote files before permission flow: %#v", entries)
	}
}

func TestInitDefersExistingFileInspectionToOrdinaryTools(t *testing.T) {
	dir, err := os.MkdirTemp("", "eino-test-init-existing-*")
	if err != nil {
		t.Fatal(err)
		return
	}
	defer os.RemoveAll(dir) //nolint:errcheck

	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Existing"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	ctx := newTestContext()
	ctx.CWD = dir

	result, err := executeInit(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	if !strings.Contains(result.Output, "create or update") || !strings.Contains(result.Output, "Read existing AGENTS.md") {
		t.Errorf("expected tool-mediated AGENTS.md inspection, got %q", result.Output)
	}
}

func TestInitForceOverwrite(t *testing.T) {
	dir, err := os.MkdirTemp("", "eino-test-init-force-*")
	if err != nil {
		t.Fatal(err)
		return
	}
	defer os.RemoveAll(dir) //nolint:errcheck

	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Existing"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	ctx := newTestContext()
	ctx.CWD = dir

	result, err := executeInit(ctx, "--force")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	if !strings.Contains(result.Output, "rebuild") || !strings.Contains(result.Output, "AGENTS.md") {
		t.Errorf("expected AGENTS.md rebuild prompt with --force, got %q", result.Output)
	}
}

// --- Argument validation tests ---

func TestValidateArgsNoSchema(t *testing.T) {
	cmd := &Command{Name: "test"}
	if err := cmd.ValidateArgs([]string{"anything", "goes"}); err != nil {
		t.Errorf("expected no error with no schema, got %v", err)
	}
}

func TestValidateArgsMissingRequired(t *testing.T) {
	cmd := &Command{
		Name:  "test",
		Usage: "/test <name>",
		Args: []ArgDef{
			{Name: "name", Type: "string", Required: true, Description: "The name"},
		},
	}

	err := cmd.ValidateArgs([]string{})
	if err == nil {
		t.Fatal("expected error for missing required arg")
		return
	}
	if !strings.Contains(err.Error(), "missing required") {
		t.Errorf("expected 'missing required' in error, got %q", err.Error())
	}
}

func TestValidateArgsIntType(t *testing.T) {
	cmd := &Command{
		Name:  "test",
		Usage: "/test <count>",
		Args: []ArgDef{
			{Name: "count", Type: "int", Required: true, Description: "Number of items"},
		},
	}

	// Valid int
	if err := cmd.ValidateArgs([]string{"42"}); err != nil {
		t.Errorf("expected no error for valid int, got %v", err)
	}

	// Invalid int
	err := cmd.ValidateArgs([]string{"abc"})
	if err == nil {
		t.Fatal("expected error for non-integer")
		return
	}
	if !strings.Contains(err.Error(), "expected integer") {
		t.Errorf("expected 'expected integer' in error, got %q", err.Error())
	}
}

func TestValidateArgsBoolType(t *testing.T) {
	cmd := &Command{
		Name:  "test",
		Usage: "/test <flag>",
		Args: []ArgDef{
			{Name: "flag", Type: "bool", Required: true, Description: "A flag"},
		},
	}

	// Valid bools
	for _, v := range []string{"true", "false", "yes", "no", "1", "0", "on", "off"} {
		if err := cmd.ValidateArgs([]string{v}); err != nil {
			t.Errorf("expected no error for %q, got %v", v, err)
		}
	}

	// Invalid bool
	err := cmd.ValidateArgs([]string{"maybe"})
	if err == nil {
		t.Fatal("expected error for invalid bool")
		return
	}
	if !strings.Contains(err.Error(), "expected boolean") {
		t.Errorf("expected 'expected boolean' in error, got %q", err.Error())
	}
}

func TestValidateArgsOptionalOK(t *testing.T) {
	cmd := &Command{
		Name:  "test",
		Usage: "/test [name]",
		Args: []ArgDef{
			{Name: "name", Type: "string", Required: false, Description: "Optional name"},
		},
	}

	// No args should be fine (optional)
	if err := cmd.ValidateArgs([]string{}); err != nil {
		t.Errorf("expected no error for omitted optional arg, got %v", err)
	}

	// With args should also be fine
	if err := cmd.ValidateArgs([]string{"hello"}); err != nil {
		t.Errorf("expected no error for provided optional arg, got %v", err)
	}
}

// --- FormatHelp tests ---

func TestFormatHelpBasic(t *testing.T) {
	cmd := &Command{
		Name:        "test",
		Description: "A test command",
		Usage:       "/test [args]",
	}

	help := cmd.FormatHelp()
	if !strings.Contains(help, "/test") {
		t.Errorf("expected command name in help")
	}
	if !strings.Contains(help, "A test command") {
		t.Errorf("expected description in help")
	}
	if !strings.Contains(help, "Usage: /test [args]") {
		t.Errorf("expected usage in help")
	}
}

func TestFormatHelpWithAliases(t *testing.T) {
	cmd := &Command{
		Name:        "clear",
		Aliases:     []string{"reset", "new"},
		Description: "Clear history",
		Usage:       "/clear",
	}

	help := cmd.FormatHelp()
	if !strings.Contains(help, "Aliases:") {
		t.Errorf("expected 'Aliases:' in help")
	}
	if !strings.Contains(help, "/reset") {
		t.Errorf("expected '/reset' alias in help")
	}
	if !strings.Contains(help, "/new") {
		t.Errorf("expected '/new' alias in help")
	}
}

func TestFormatHelpWithArgs(t *testing.T) {
	cmd := &Command{
		Name:        "model",
		Description: "Change model",
		Usage:       "/model <name>",
		Args: []ArgDef{
			{Name: "name", Type: "string", Required: true, Description: "Model name to switch to"},
		},
	}

	help := cmd.FormatHelp()
	if !strings.Contains(help, "Arguments:") {
		t.Errorf("expected 'Arguments:' section")
	}
	if !strings.Contains(help, "name") {
		t.Errorf("expected 'name' argument in help")
	}
	if !strings.Contains(help, "(required)") {
		t.Errorf("expected '(required)' marker")
	}
}

func TestFormatHelpWithDetailedHelp(t *testing.T) {
	cmd := &Command{
		Name:         "diff",
		Description:  "Show changes",
		Usage:        "/diff [mode]",
		DetailedHelp: "Shows git diff output for the current repository.",
	}

	help := cmd.FormatHelp()
	if !strings.Contains(help, "Shows git diff output") {
		t.Errorf("expected detailed help text in output")
	}
}

// --- Command dispatch routing tests ---

func TestDispatchRouting(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	tests := []struct {
		input   string
		wantErr bool
		wantCmd string
	}{
		{"/help", false, ""},
		{"/clear", false, ""},
		{"/version", false, ""},
		{"/bug", false, ""},
		{"/nonexistent", true, ""},
		{"not a command", true, ""},
		{"/", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := dispatchCommand(r, &CommandContext{CWD: os.TempDir(), Model: "test"}, tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for input %q", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for input %q: %v", tt.input, err)
			}
		})
	}
}

func TestDispatchAliases(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	// /reset is an alias for /clear
	result, err := dispatchCommand(r, &CommandContext{CWD: os.TempDir()}, "/reset")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if result.Action != ActionClear {
		t.Errorf("expected ActionClear for /reset alias, got %q", result.Action)
	}

	// /exit is an alias for /quit
	result, err = dispatchCommand(r, &CommandContext{CWD: os.TempDir()}, "/exit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if result.Action != ActionQuit {
		t.Errorf("expected ActionQuit for /exit alias, got %q", result.Action)
	}

	result, err = dispatchCommand(r, &CommandContext{}, "/new")
	if err != nil {
		t.Fatalf("dispatch /new: %v", err)
	}
	if result.Action != ActionNew {
		t.Fatalf("expected ActionNew for /new, got %q", result.Action)
	}
}

func TestDefaultCommandContractAndAliasSnapshot(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	visible := make([]string, 0, len(r.List()))
	for _, cmd := range r.List() {
		visible = append(visible, cmd.Name)
	}
	wantVisible := []string{
		"help", "new", "clear", "compact", "status", "model", "sessions", "resume",
		"terminal", "suspend", "quit", "version", "usage",
		"context", "config", "diff", "copy", "init", "permissions", "hooks",
		"doctor", "plan", "goal", "memory", "add-dir", "tasks",
		"effort", "skills", "agents", "agent", "mcp", "team", "queue", "files",
		"vim", "theme", "fork", "keybindings", "search", "reload-plugins",
	}
	if got, want := strings.Join(visible, ","), strings.Join(wantVisible, ","); got != want {
		t.Fatalf("visible commands:\n got %s\nwant %s", got, want)
	}

	var aliases []string
	for _, name := range r.order {
		cmd := r.commands[name]
		if cmd.Availability != AvailabilitySupported {
			t.Fatalf("active command %s has unavailable state %q", name, cmd.Availability)
		}
		if len(cmd.Aliases) > 0 {
			aliases = append(aliases, name+"="+strings.Join(cmd.Aliases, "|"))
		}
	}
	wantAliases := []string{
		"clear=reset",
		"quit=exit",
		"context=ctx",
		"team=teams",
	}
	if got, want := strings.Join(aliases, ","), strings.Join(wantAliases, ","); got != want {
		t.Fatalf("aliases:\n got %s\nwant %s", got, want)
	}

	entrypoints := []Entrypoint{
		EntrypointTUI,
		EntrypointPlain,
		EntrypointHeadless,
		EntrypointACP,
		EntrypointAdministration,
	}
	for _, name := range r.order {
		cmd := r.commands[name]
		if cmd.Kind == "" || cmd.Availability == "" || cmd.SideEffect == "" ||
			cmd.ResultKind == "" || cmd.ExecutionOwner == "" ||
			cmd.Source == "" || cmd.Trust == "" || cmd.Execute == nil ||
			cmd.Category == "" || cmd.DiscoveryTier == "" ||
			cmd.DisplayOrder <= 0 || cmd.PhaseScope == "" ||
			cmd.legacyExecute != nil {
			t.Fatalf("incomplete canonical contract for %s: %#v", name, cmd)
		}
		for _, key := range commandKeys(cmd) {
			for _, entrypoint := range entrypoints {
				wantDiscoverable := cmd.Availability == AvailabilitySupported &&
					cmd.Entrypoints.Supports(entrypoint)
				got := r.GetFor(entrypoint, key)
				if (got != nil) != wantDiscoverable {
					t.Fatalf(
						"GetFor(%s, %s) present=%v want=%v contract=%#v",
						entrypoint,
						key,
						got != nil,
						wantDiscoverable,
						cmd,
					)
				}
			}
		}
	}
	wantEntrypointCounts := map[Entrypoint]int{
		EntrypointTUI:            40,
		EntrypointPlain:          30,
		EntrypointHeadless:       18,
		EntrypointACP:            14,
		EntrypointAdministration: 0,
	}
	for entrypoint, want := range wantEntrypointCounts {
		if got := len(r.ListFor(entrypoint)); got != want {
			t.Fatalf("%s command count = %d, want %d", entrypoint, got, want)
		}
	}
}

func TestUnavailableCommandsReturnCompatibilityErrorsWithoutActions(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	invocations := []string{
		"/plugin install example",
		"/bug broken",
		"/feedback broken",
		"/undo 2",
		"/rewrite",
		"/retry",
		"/branch checkpoint",
		"/rewind file.go",
		"/color blue",
		"/fast on",
		"/tag release",
		"/share",
		"/release-notes",
		"/mode plan",
		"/bypass",
		"/yolo",
	}
	for _, input := range invocations {
		t.Run(input, func(t *testing.T) {
			result, err := dispatchCommand(r, &CommandContext{}, input)
			if err != nil {
				t.Fatalf("dispatch %s: %v", input, err)
			}
			if result.Action != ActionNone || result.Data != nil {
				t.Fatalf("%s returned side effect: %#v", input, result)
			}
			invoked, _ := ParseCommandInput(input)
			if result.Removed == nil || !strings.Contains(result.Output, "/"+invoked+" was removed in P21.0:") {
				t.Fatalf("%s compatibility output = %q", input, result.Output)
			}
		})
	}

	logout, err := dispatchCommand(r, &CommandContext{}, "/logout openai")
	if err != nil {
		t.Fatalf("dispatch /logout: %v", err)
	}
	if logout.Action != ActionNone || logout.Removed == nil {
		t.Fatalf("logout compatibility output = %#v", logout)
	}
}

func TestTUILocalCommandsUseTheCanonicalTUISnapshot(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	for _, input := range []string{"/team", "/teams", "/queue remove all", "/search term"} {
		result, err := dispatchCommand(r, &CommandContext{}, input)
		if err != nil {
			t.Fatalf("dispatch %s: %v", input, err)
		}
		if result.Action != ActionNone || result.Output == "" {
			t.Fatalf("TUI-local result for %s = %#v", input, result)
		}
	}
	for _, name := range []string{"team", "teams", "queue", "search"} {
		if r.GetFor(EntrypointTUI, name) == nil {
			t.Fatalf("%s absent from TUI snapshot", name)
		}
		if r.GetFor(EntrypointPlain, name) != nil {
			t.Fatalf("%s leaked into plain snapshot", name)
		}
	}
}

func TestRegistryDispatchPreservesQuotedArgsAndRawInput(t *testing.T) {
	r := NewRegistry()
	var gotRaw string
	var gotArgs []string
	_ = r.Register(&Command{
		Name:  "capture",
		Usage: "/capture <value> [flag]",
		Args: []ArgDef{
			{Name: "value", Type: "string", Required: true},
			{Name: "flag", Type: "string"},
		},
		Execute: func(_ context.Context, cmdCtx *CommandContext) (*CommandResult, error) {
			gotRaw = cmdCtx.RawInput
			gotArgs = append([]string(nil), cmdCtx.Args...)
			return &CommandResult{Output: strings.Join(cmdCtx.Args, " ")}, nil
		},
	})

	result, err := dispatchCommand(r, &CommandContext{}, `/capture "Bash(rm -rf *)" --project`)
	if err != nil {
		t.Fatalf("dispatch quoted command: %v", err)
	}
	if gotRaw != `/capture "Bash(rm -rf *)" --project` {
		t.Fatalf("RawInput = %q", gotRaw)
	}
	if got, want := strings.Join(gotArgs, "|"), "Bash(rm -rf *)|--project"; got != want {
		t.Fatalf("Args = %q, want %q", got, want)
	}
	if result.Output != "Bash(rm -rf *) --project" {
		t.Fatalf("Execute args = %q", result.Output)
	}
}

func TestRegistryDispatchValidatesBeforeExecution(t *testing.T) {
	r := NewRegistry()
	called := false
	_ = r.Register(&Command{
		Name:  "count",
		Usage: "/count <n>",
		Args: []ArgDef{
			{Name: "n", Type: "int", Required: true},
		},
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			called = true
			return &CommandResult{}, nil
		},
	})

	_, err := dispatchCommand(r, &CommandContext{}, "/count nope")
	if err == nil || !strings.Contains(err.Error(), `argument "n"`) {
		t.Fatalf("validation error = %v", err)
	}
	if called {
		t.Fatal("command executed before argument validation")
	}
}

func TestRegistryRejectsNormalizedNameAndAliasCollisionsAtomically(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(testPluginCommand("Alpha", "Beta")); err != nil {
		t.Fatalf("register initial command: %v", err)
	}
	beforeOrder := append([]string(nil), r.order...)

	for _, candidate := range []*Command{
		testPluginCommand(" /ALPHA "),
		testPluginCommand("gamma", " /BETA "),
		testPluginCommand("delta", "delta"),
	} {
		if err := r.Register(candidate); err == nil {
			t.Fatalf("collision candidate %#v succeeded", candidate)
		}
		if got := strings.Join(r.order, ","); got != strings.Join(beforeOrder, ",") {
			t.Fatalf("failed registration changed order: %q", got)
		}
		if r.Get("gamma") != nil || r.Get("delta") != nil {
			t.Fatal("failed registration changed live lookup")
		}
	}
}

func TestRegistrySnapshotsCannotMutateLiveContract(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	snapshot := r.Get("clear")
	snapshot.Name = "changed"
	snapshot.Aliases[0] = "changed"
	snapshot.Entrypoints = EntrypointsNone
	snapshot.Category = CommandCategoryWorkflow
	snapshot.DiscoveryTier = DiscoveryTierPrimary
	snapshot.DisplayOrder = 1
	snapshot.PhaseScope = PhaseScopeAny

	live := r.Get("clear")
	if live.Name != "clear" ||
		len(live.Aliases) != 1 ||
		live.Aliases[0] != "reset" ||
		!live.Entrypoints.Supports(EntrypointTUI) ||
		live.Category != CommandCategorySession ||
		live.DiscoveryTier != DiscoveryTierSecondary ||
		live.DisplayOrder != 1010 ||
		live.PhaseScope != PhaseScopeIdleOnly {
		t.Fatalf("snapshot mutation changed live contract: %#v", live)
	}
}

func TestRegistryDispatchRejectsMalformedQuotedInputBeforeExecution(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(&Command{
		Name: "probe",
		Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
			called = true
			return &CommandResult{}, nil
		},
	}); err != nil {
		t.Fatalf("register probe: %v", err)
	}

	for _, input := range []string{`/probe "open`, `/probe 'open`, `/probe "escape\`} {
		if _, err := dispatchCommand(r, &CommandContext{}, input); err == nil ||
			!strings.Contains(err.Error(), "invalid command input") {
			t.Fatalf("dispatch malformed %q error = %v", input, err)
		}
	}
	if called {
		t.Fatal("handler ran for malformed quoted input")
	}
}

func FuzzParseCommandInput(f *testing.F) {
	// Quoting and invalid UTF-8 must never panic: successful parses normalize the
	// command name, while malformed input is rejected by the strict parser.
	for _, input := range []string{
		`/help`,
		`/permissions add "Bash(rm -rf *)"`,
		`/probe 'single quoted'`,
		`/probe "unterminated`,
		"",
	} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		name, _, err := parseCommandInputStrict(input)
		if err != nil {
			return
		}
		if name != strings.ToLower(name) {
			t.Fatalf("parser returned non-normalized name %q", name)
		}
	})
}

func TestPermissionsDispatchPreservesQuotedRule(t *testing.T) {
	projectDir := t.TempDir()

	r := NewRegistry()
	RegisterDefaults(r)
	result, err := dispatchCommand(r,
		&CommandContext{CWD: projectDir},
		`/permissions add allow "Bash(rm -rf *)" --project`,
	)
	if err != nil {
		t.Fatalf("dispatch permissions: %v", err)
	}
	if !strings.Contains(result.Output, "Bash(rm -rf *)") {
		t.Fatalf("permission output lost quoted rule: %q", result.Output)
	}
	if result.Action != ActionPermissions ||
		result.Data["operation"] != "add" ||
		result.Data["rule_action"] != string(permission.ActionAllow) ||
		result.Data["rule"] != "Bash(rm -rf *)" ||
		result.Data["destination"] != string(permission.DestProjectSettings) {
		t.Fatalf("permission intent = %#v", result)
	}
	rules, err := permission.LoadPermissionRules(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Fatalf("registry dispatch applied permission side effect: %#v", rules)
	}
}

// --- IsCommand tests ---

func TestIsCommand(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"/help", true},
		{"/clear", true},
		{"/ space", false},
		{"//double", false},
		{"/", false},
		{"", false},
		{"hello", false},
		{"/A", true},
		{"/z", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsCommand(tt.input)
			if got != tt.want {
				t.Errorf("IsCommand(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// --- ParseCommandInput tests ---

func TestParseCommandInput(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs []string
	}{
		{"/help", "help", nil},
		{"/help model", "help", []string{"model"}},
		{"/model gpt-4o", "model", []string{"gpt-4o"}},
		{`/rename "my session"`, "rename", []string{"my session"}},
		{"/undo 3", "undo", []string{"3"}},
		{"/diff full", "diff", []string{"full"}},
		{"/bug model hangs", "bug", []string{"model", "hangs"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			name, args := ParseCommandInput(tt.input)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if len(args) != len(tt.wantArgs) {
				t.Errorf("args = %v, want %v", args, tt.wantArgs)
				return
			}
			for i := range args {
				if args[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %q, want %q", i, args[i], tt.wantArgs[i])
				}
			}
		})
	}
}

// --- /version integration test ---

func TestVersionViaRegistry(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{CWD: os.TempDir()}, "/version")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !strings.Contains(result.Output, "YHC version") {
		t.Errorf("expected version info, got %q", result.Output)
	}
}

// --- /bug integration test ---

func TestBugViaRegistryReturnsCompatibilityError(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)

	result, err := dispatchCommand(r, &CommandContext{CWD: os.TempDir(), Model: "test"}, "/bug something broke")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if result.Action != ActionNone ||
		result.Removed == nil ||
		!strings.Contains(result.Output, "/bug was removed in P21.0") {
		t.Fatalf("unexpected /bug result: %#v", result)
	}
}

// --- Registry.ReplacePluginCommands contract tests ---

// builtInOrder returns the canonical names of non-plugin commands in registration
// order. It reads unexported fields because the test package is white-box.
func builtInOrder(r *Registry) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []string
	for _, name := range r.order {
		if _, dynamic := r.promptCommandCanonicals[name]; dynamic {
			continue
		}
		out = append(out, name)
	}
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRegistryReplacePluginCommands(t *testing.T) {
	tests := []struct {
		name    string
		replace func() []*Command
		wantErr bool
		check   func(t *testing.T, r *Registry, pluginA *Command, beforeBuiltIns []string, helpCmd *Command)
	}{
		{
			name: "SuccessfulSwap",
			replace: func() []*Command {
				return []*Command{
					testPluginCommand("plugin:b"),
				}
			},
			wantErr: false,
			check: func(t *testing.T, r *Registry, pluginA *Command, beforeBuiltIns []string, helpCmd *Command) {
				if r.Get("plugin:a") != nil {
					t.Errorf("plugin:a should have been removed")
				}
				pluginB := r.Get("plugin:b")
				if pluginB == nil {
					t.Fatalf("plugin:b should be present")
				}
				if pluginB.Name != "plugin:b" {
					t.Errorf("plugin:b name = %q, want %q", pluginB.Name, "plugin:b")
				}
			},
		},
		{
			name: "ConflictWithBuiltIn",
			replace: func() []*Command {
				return []*Command{
					testPluginCommand(" /HELP "),
				}
			},
			wantErr: true,
			check: func(t *testing.T, r *Registry, pluginA *Command, beforeBuiltIns []string, helpCmd *Command) {
				if got := r.Get("plugin:a"); got == nil || got.Name != pluginA.Name {
					t.Errorf("plugin:a should still be the previous snapshot, got %v", got)
				}
				if got := r.Get("help"); got == nil || got.Name != helpCmd.Name {
					t.Errorf("built-in help command changed")
				}
			},
		},
		{
			name: "DuplicateWithinSnapshot",
			replace: func() []*Command {
				return []*Command{
					testPluginCommand("plugin:x", "Shared"),
					testPluginCommand("plugin:y", " /SHARED "),
				}
			},
			wantErr: true,
			check: func(t *testing.T, r *Registry, pluginA *Command, beforeBuiltIns []string, helpCmd *Command) {
				if got := r.Get("plugin:a"); got == nil || got.Name != pluginA.Name {
					t.Errorf("plugin:a should still be the previous snapshot, got %v", got)
				}
				if r.Get("plugin:x") != nil {
					t.Errorf("plugin:x should not have been installed")
				}
				if r.Get("plugin:y") != nil {
					t.Errorf("plugin:y should not have been installed")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			RegisterDefaults(r)

			helpCmd := r.Get("help")
			if helpCmd == nil {
				t.Fatal("built-in help command not registered")
			}
			beforeBuiltIns := builtInOrder(r)

			pluginA := testPluginCommand("plugin:a")
			if err := r.ReplacePluginCommands([]*Command{pluginA}); err != nil {
				t.Fatalf("initial plugin:a install failed: %v", err)
			}

			err := r.ReplacePluginCommands(tt.replace())
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReplacePluginCommands error = %v, wantErr = %v", err, tt.wantErr)
			}

			if got := r.Get("help"); got == nil || got.Name != helpCmd.Name {
				t.Errorf("built-in help command changed after replacement")
			}
			if afterBuiltIns := builtInOrder(r); !stringSlicesEqual(beforeBuiltIns, afterBuiltIns) {
				t.Errorf("built-in order changed:\nbefore: %v\nafter:  %v", beforeBuiltIns, afterBuiltIns)
			}

			tt.check(t, r, pluginA, beforeBuiltIns, helpCmd)
		})
	}
}

func TestRegistryReplacePluginCommandsConcurrentDispatch(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	command := func(version int) *Command {
		return &Command{
			Name: "plugin:stable",
			Execute: func(context.Context, *CommandContext) (*CommandResult, error) {
				return &CommandResult{Output: fmt.Sprintf("version-%d", version)}, nil
			},
		}
	}
	if err := r.ReplacePluginCommands([]*Command{command(0)}); err != nil {
		t.Fatalf("install initial plugin command: %v", err)
	}

	start := make(chan struct{})
	errCh := make(chan error, 8)
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for range 250 {
				result, err := dispatchCommand(r, &CommandContext{}, "/plugin:stable")
				if err != nil {
					errCh <- err
					return
				}
				if !strings.HasPrefix(result.Output, "version-") {
					errCh <- fmt.Errorf("partial command result %q", result.Output)
					return
				}
			}
		}()
	}
	close(start)
	for version := 1; version <= 100; version++ {
		if err := r.ReplacePluginCommands([]*Command{command(version)}); err != nil {
			t.Fatalf("replace plugin command version %d: %v", version, err)
		}
	}
	readers.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestCommandResultValidatedAccessors(t *testing.T) {
	result := &CommandResult{Data: map[string]any{
		"model":   "test-model",
		"enabled": true,
		"bad":     42,
	}}
	if got, err := result.RequiredString("model"); err != nil || got != "test-model" {
		t.Fatalf("RequiredString(model) = %q, %v", got, err)
	}
	if got, err := result.RequiredBool("enabled"); err != nil || !got {
		t.Fatalf("RequiredBool(enabled) = %v, %v", got, err)
	}
	if got, err := result.OptionalString("missing"); err != nil || got != "" {
		t.Fatalf("OptionalString(missing) = %q, %v", got, err)
	}
	if _, err := result.RequiredString("bad"); err == nil {
		t.Fatal("RequiredString accepted a non-string payload")
	}
	if _, err := result.RequiredBool("missing"); err == nil {
		t.Fatal("RequiredBool accepted a missing payload")
	}
}

func TestDefaultCommandExecutionOwners(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)

	engineOwned := []string{
		"clear",
		"compact",
		"sessions",
		"resume",
		"model",
		"permissions",
		"plan",
		"effort",
		"add-dir",
		"fork",
		"reload-plugins",
	}
	for _, name := range engineOwned {
		cmd := registry.Get(name)
		if cmd == nil || cmd.ExecutionOwner != ExecutionOwnerEngine {
			t.Fatalf("/%s owner = %#v, want engine", name, cmd)
		}
	}

	entrypointOwned := []string{
		"help",
		"quit",
		"suspend",
		"theme",
	}
	for _, name := range entrypointOwned {
		cmd := registry.Get(name)
		if cmd == nil || cmd.ExecutionOwner != ExecutionOwnerEntrypoint {
			t.Fatalf("/%s owner = %#v, want entrypoint", name, cmd)
		}
	}
}

func TestEngineOwnedHandlersReturnPureMutationIntents(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	tests := []struct {
		input  string
		action CommandAction
	}{
		{input: "/fork child-name", action: ActionFork},
		{input: "/sessions rename current session-name", action: ActionRename},
		{input: `/permissions add allow "Read(/tmp/*)" --local`, action: ActionPermissions},
		{input: "/reload-plugins", action: ActionReload},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result, err := registry.Dispatch(
				context.Background(),
				EntrypointPlain,
				&CommandContext{CWD: t.TempDir()},
				test.input,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || result.Action != test.action {
				t.Fatalf("mutation intent = %#v, want action %q", result, test.action)
			}
		})
	}
}
