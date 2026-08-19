// Package commands implements a command registry and dispatcher for slash commands.
// This mirrors the command system in the reference's commands.ts where users can type
// /command during a conversation to trigger built-in operations.
package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/internal/identity"
)

// Entrypoint identifies a product surface that can discover or dispatch a
// command. Standalone MCP is intentionally not a conversation command
// entrypoint.
type Entrypoint string

// CommandCategory groups discovery surfaces without changing execution ownership.
type CommandCategory string

const (
	CommandCategorySession    CommandCategory = "Session"
	CommandCategoryRuntime    CommandCategory = "Runtime"
	CommandCategorySafety     CommandCategory = "Safety"
	CommandCategoryWorkspace  CommandCategory = "Workspace"
	CommandCategoryAgents     CommandCategory = "Agents"
	CommandCategoryExtensions CommandCategory = "Extensions"
	CommandCategoryUI         CommandCategory = "UI"
	CommandCategoryWorkflow   CommandCategory = "Workflow"
)

// CommandCategoriesInDisplayOrder returns the stable help category order.
func CommandCategoriesInDisplayOrder() []CommandCategory {
	return []CommandCategory{
		CommandCategorySession,
		CommandCategoryRuntime,
		CommandCategorySafety,
		CommandCategoryWorkspace,
		CommandCategoryAgents,
		CommandCategoryExtensions,
		CommandCategoryUI,
		CommandCategoryWorkflow,
	}
}

// DiscoveryTier controls whether an empty discovery view suggests a command.
type DiscoveryTier string

const (
	DiscoveryTierPrimary   DiscoveryTier = "primary"
	DiscoveryTierSecondary DiscoveryTier = "secondary"
)

type PhaseScope string

const (
	PhaseScopeIdleOnly PhaseScope = "idle-only"
	PhaseScopeAny      PhaseScope = "any"
)

type CommandPhase string

const (
	CommandPhaseIdle       CommandPhase = "idle"
	CommandPhaseActiveTurn CommandPhase = "active-turn"
)

type CommandEnvironment struct {
	Entrypoint Entrypoint
	Phase      CommandPhase
}

const (
	EntrypointTUI            Entrypoint = "tui"
	EntrypointPlain          Entrypoint = "plain"
	EntrypointHeadless       Entrypoint = "headless"
	EntrypointHeadlessGoal   Entrypoint = "headless-goal"
	EntrypointACP            Entrypoint = "acp"
	EntrypointAdministration Entrypoint = "cli-administration"
)

// EntrypointSet is a compact capability set for command discovery.
type EntrypointSet uint8

const EntrypointsNone EntrypointSet = 0

const (
	EntrypointsTUI EntrypointSet = 1 << iota
	EntrypointsPlain
	EntrypointsHeadless
	EntrypointsACP
	EntrypointsAdministration
	EntrypointsHeadlessGoal
)

func (s EntrypointSet) Supports(entrypoint Entrypoint) bool {
	var required EntrypointSet
	switch entrypoint {
	case EntrypointTUI:
		required = EntrypointsTUI
	case EntrypointPlain:
		required = EntrypointsPlain
	case EntrypointHeadless:
		required = EntrypointsHeadless
	case EntrypointHeadlessGoal:
		required = EntrypointsHeadlessGoal
	case EntrypointACP:
		required = EntrypointsACP
	case EntrypointAdministration:
		required = EntrypointsAdministration
	default:
		return false
	}
	return s&required != 0
}

// CommandKind declares the command's product role without changing who applies
// its current action. Action ownership remains a P16.3 concern.
type CommandKind string

const (
	CommandKindQuery           CommandKind = "query"
	CommandKindRuntimeMutation CommandKind = "runtime-mutation"
	CommandKindSessionMutation CommandKind = "durable-session-mutation"
	CommandKindUIAction        CommandKind = "ui-action"
	CommandKindPromptWorkflow  CommandKind = "prompt-workflow"
	CommandKindAdministration  CommandKind = "administration"
)

// AvailabilityState determines discovery and direct-dispatch behavior.
type AvailabilityState string

const (
	AvailabilitySupported   AvailabilityState = "supported"
	AvailabilityHidden      AvailabilityState = "hidden"
	AvailabilityDisabled    AvailabilityState = "disabled"
	AvailabilityUnavailable AvailabilityState = "unavailable"
)

// SideEffectKind declares the strongest side-effect boundary a command may
// reach after its current result is applied.
type SideEffectKind string

const (
	SideEffectNone           SideEffectKind = "none"
	SideEffectProcessLocal   SideEffectKind = "process-local"
	SideEffectWorkspace      SideEffectKind = "workspace"
	SideEffectDurableSession SideEffectKind = "durable-session"
	SideEffectAuthentication SideEffectKind = "authentication-state"
	SideEffectExternal       SideEffectKind = "external-service"
)

// ResultKind describes the typed shape consumers should expect while the
// legacy Action/Data bridge remains in place until P16.3.
type ResultKind string

const (
	ResultKindText   ResultKind = "text"
	ResultKindAction ResultKind = "action"
	ResultKindPrompt ResultKind = "prompt"
	ResultKindUI     ResultKind = "ui-action"
)

// ExecutionOwner declares which boundary is allowed to apply a command's
// runtime or session action. Entrypoint-owned commands are presentation or
// workflow operations and must not be submitted to the engine action executor.
type ExecutionOwner string

const (
	ExecutionOwnerEngine     ExecutionOwner = "engine"
	ExecutionOwnerEntrypoint ExecutionOwner = "entrypoint"
)

// CommandSourceKind identifies which command layer supplied a record.
type CommandSourceKind string

const (
	CommandSourceCore    CommandSourceKind = "core"
	CommandSourceBundled CommandSourceKind = "bundled-workflow-pack"
	CommandSourcePlugin  CommandSourceKind = "configured-plugin"
)

// CommandTrustClass describes the authority granted to command content. It is
// descriptive metadata only: prompt workflows always remain subject to the
// ordinary model tool and permission boundaries.
type CommandTrustClass string

const (
	CommandTrustCore       CommandTrustClass = "core"
	CommandTrustBundled    CommandTrustClass = "bundled"
	CommandTrustConfigured CommandTrustClass = "configured"
)

// CommandCompatibility records compatibility-only names or a removal gate.
type CommandCompatibility struct {
	DeprecatedAliases []string
	RemovalBoundary   string
}

// CommandCompatibilityWarning describes an explicitly deprecated alias used
// for a successful command dispatch.
type CommandCompatibilityWarning struct {
	Alias           string
	Replacement     string
	RemovalBoundary string
}

// RemovedCommand preserves deterministic guidance for a retired command
// without making it an executable or discoverable capability.
type RemovedCommand struct {
	Name        string
	Aliases     []string
	Reason      string
	Replacement string
	RemovedIn   string
}

// CommandHandler is the single canonical command execution shape.
type CommandHandler func(context.Context, *CommandContext) (*CommandResult, error)

// AvailabilityResolver evaluates a runtime capability from the same command
// context used by discovery and dispatch. Static Availability still owns
// product scope; the resolver may only narrow a supported command when the
// active provider, model, or entrypoint capability is unavailable.
type AvailabilityResolver func(context.Context, *CommandContext) (AvailabilityState, string)

type legacyCommandExecutor func(*CommandContext, string) (*CommandResult, error)

// CommandAction represents an action the engine should perform after a command executes.
type CommandAction string

const (
	ActionNone        CommandAction = ""
	ActionNew         CommandAction = "new_session"
	ActionClear       CommandAction = "clear"
	ActionCompact     CommandAction = "compact"
	ActionSessions    CommandAction = "sessions"
	ActionResume      CommandAction = "resume"
	ActionChangeModel CommandAction = "change_model"
	ActionChangeMode  CommandAction = "change_mode"
	ActionExport      CommandAction = "export"
	ActionQuit        CommandAction = "quit"
	ActionSuspend     CommandAction = "suspend"
	ActionPrompt      CommandAction = "prompt"       // inject output as a user prompt for the AI
	ActionCopy        CommandAction = "copy"         // copy a committed assistant result in the TUI
	ActionToggleVim   CommandAction = "toggle_vim"   // toggle vim editing mode
	ActionChangeTheme CommandAction = "change_theme" // switch theme
	ActionRename      CommandAction = "rename_session"
	ActionPermissions CommandAction = "permission_rules"
	ActionReload      CommandAction = "reload_plugins"
	ActionGoal        CommandAction = "goal"
)

// Command represents a registered slash command.
type Command struct {
	// Name is the command name without the leading slash.
	Name string
	// Aliases are alternative names for this command.
	Aliases []string
	// Description is a short description of what the command does.
	Description string
	// DetailedHelp provides extended help text shown by /help <command>.
	DetailedHelp string
	// Usage shows the command syntax.
	Usage string
	// Source, SourceVersion, and Trust identify where command content came from.
	Source        string
	SourceVersion string
	Trust         CommandTrustClass
	// Category, DiscoveryTier and DisplayOrder own layered command discovery.
	Category      CommandCategory
	DiscoveryTier DiscoveryTier
	DisplayOrder  int
	PhaseScope    PhaseScope
	// Args defines the argument schema for validation.
	Args []ArgDef
	// Kind, Entrypoints, Availability, Dependency, SideEffect, ResultKind and
	// Compatibility form the canonical discovery and dispatch contract.
	Kind               CommandKind
	Entrypoints        EntrypointSet
	Availability       AvailabilityState
	AvailabilityReason string
	Dependency         string
	SideEffect         SideEffectKind
	ResultKind         ResultKind
	ExecutionOwner     ExecutionOwner
	Compatibility      CommandCompatibility
	// ResolveAvailability narrows supported discovery and dispatch using the
	// active runtime capability snapshot.
	ResolveAvailability AvailabilityResolver
	// Execute is the only handler shape stored by the registry.
	Execute CommandHandler
	// legacyExecute is adapted and cleared before a built-in enters the
	// registry. It is not observable through registry snapshots.
	legacyExecute legacyCommandExecutor
}

// ArgDef defines a single command argument for validation.
type ArgDef struct {
	// Name is the argument name for display purposes.
	Name string
	// Type is the argument type: "string", "int", "bool".
	Type string
	// Required indicates whether this argument must be provided.
	Required bool
	// Description describes what this argument does.
	Description string
	// Default is the default value if not provided (for optional args).
	Default string
}

// ValidateArgs validates the given arguments against the command's ArgDef schema.
// Returns nil if valid, or an error describing the problem.
func (c *Command) ValidateArgs(args []string) error {
	if len(c.Args) == 0 {
		return nil // No schema defined, accept anything
	}

	// Check required arguments
	requiredCount := 0
	for _, def := range c.Args {
		if def.Required {
			requiredCount++
		}
	}

	if len(args) < requiredCount {
		var names []string
		for _, def := range c.Args {
			if def.Required {
				names = append(names, "<"+def.Name+">")
			}
		}
		return fmt.Errorf("missing required argument(s): %s\nUsage: %s", strings.Join(names, ", "), c.Usage)
	}

	// Validate types for provided arguments
	for i, arg := range args {
		if i >= len(c.Args) {
			break // Extra args are allowed
		}
		def := c.Args[i]
		if err := validateArgType(arg, def); err != nil {
			return fmt.Errorf("argument %q: %w\nUsage: %s", def.Name, err, c.Usage)
		}
	}

	return nil
}

// validateArgType checks a single argument value against its type definition.
func validateArgType(value string, def ArgDef) error {
	switch def.Type {
	case "int":
		if value == "" {
			return nil
		}
		for _, c := range value {
			if c == '-' {
				continue
			}
			if c < '0' || c > '9' {
				return fmt.Errorf("expected integer, got %q", value)
			}
		}
	case "bool":
		switch strings.ToLower(value) {
		case "true", "false", "1", "0", "yes", "no", "on", "off":
			return nil
		default:
			return fmt.Errorf("expected boolean (true/false), got %q", value)
		}
	case "string", "":
		// Any string is valid
	}
	return nil
}

// FormatHelp returns the full help text for this command, including usage,
// description, arguments, aliases, and detailed help if available.
func (c *Command) FormatHelp() string {
	return c.FormatHelpFor("")
}

// FormatHelpFor renders detailed help from the same entrypoint-aware record
// used by discovery and dispatch.
func (c *Command) FormatHelpFor(entrypoint Entrypoint) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "/%s — %s\n", c.Name, c.Description)
	fmt.Fprintf(&sb, "\nUsage: %s\n", c.Usage)
	if c.Source != "" {
		source := c.Source
		if c.SourceVersion != "" {
			source += "@" + c.SourceVersion
		}
		fmt.Fprintf(&sb, "Source: %s\n", source)
	}
	if c.Trust != "" {
		fmt.Fprintf(&sb, "Trust: %s\n", c.Trust)
	}
	if entrypoint != "" {
		state := c.Availability
		if !c.Entrypoints.Supports(entrypoint) {
			state = AvailabilityUnavailable
		}
		fmt.Fprintf(&sb, "Availability: %s on %s\n", state, entrypoint)
		if c.AvailabilityReason != "" {
			fmt.Fprintf(&sb, "Reason: %s\n", c.AvailabilityReason)
		}
	}

	if len(c.Aliases) > 0 {
		aliases := make([]string, len(c.Aliases))
		for i, a := range c.Aliases {
			aliases[i] = "/" + a
		}
		fmt.Fprintf(&sb, "Aliases: %s\n", strings.Join(aliases, ", "))
	}

	if len(c.Args) > 0 {
		sb.WriteString("\nArguments:\n")
		for _, def := range c.Args {
			var required string
			switch {
			case def.Required:
				required = " (required)"
			case def.Default != "":
				required = fmt.Sprintf(" (default: %s)", def.Default)
			default:
				required = " (optional)"
			}
			fmt.Fprintf(&sb, "  %-12s %-8s %s%s\n", def.Name, def.Type, def.Description, required)
		}
	}

	if c.DetailedHelp != "" {
		sb.WriteString("\n")
		sb.WriteString(c.DetailedHelp)
		sb.WriteString("\n")
	}

	return sb.String()
}

// CommandContext provides context to a command's Execute function.
type CommandContext struct {
	// Context carries the same cancellation/deadline observed by Execute. It is
	// retained for legacy inner implementations adapted at registration.
	Context context.Context
	// Entrypoint is the product surface performing discovery or dispatch.
	Entrypoint Entrypoint
	// Environment is the detached discovery/dispatch snapshot. A missing phase
	// is idle for compatibility with existing callers.
	Environment CommandEnvironment
	// SessionID is the current session.
	SessionID string
	// CWD is the current working directory (also serves as ProjectDir).
	CWD string
	// Model is the current model name.
	Model string
	// Messages is the current conversation history.
	Messages []*schema.Message
	// WorkingDirectories is the detached active workspace-root snapshot.
	WorkingDirectories []string
	// Args holds parsed arguments after the command name.
	Args []string
	// RawInput is the full user input string.
	RawInput string
	// Engine is a reference to the engine for operations.
	Engine interface{}
	// Extra allows passing additional context.
	Extra map[string]any
}

// CommandResult is the result returned from executing a command.
type CommandResult struct {
	// Output is the text to display to the user.
	Output string
	// Action is an optional action for the engine to perform.
	Action CommandAction
	// Data is optional structured data for the action.
	Data map[string]any
	// Availability records a runtime capability rejection. It lets the engine
	// project unsupported rather than successful without re-evaluating the
	// capability after dispatch.
	Availability AvailabilityState
	// Removed contains typed migration guidance for a retired command.
	Removed *RemovedCommand
	// CompatibilityWarning is set only when an explicitly deprecated alias was used.
	CompatibilityWarning *CommandCompatibilityWarning
}

// OptionalString returns a validated optional string action payload.
func (r *CommandResult) OptionalString(key string) (string, error) {
	if r == nil || r.Data == nil {
		return "", nil
	}
	value, exists := r.Data[key]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("command result data %q must be a string, got %T", key, value)
	}
	return text, nil
}

// RequiredString returns a non-empty validated string action payload.
func (r *CommandResult) RequiredString(key string) (string, error) {
	value, err := r.OptionalString(key)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("command result data %q must be a non-empty string", key)
	}
	return value, nil
}

// RequiredBool returns a validated boolean action payload.
func (r *CommandResult) RequiredBool(key string) (bool, error) {
	if r == nil || r.Data == nil {
		return false, fmt.Errorf("command result data %q is required", key)
	}
	value, exists := r.Data[key]
	if !exists || value == nil {
		return false, fmt.Errorf("command result data %q is required", key)
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("command result data %q must be a bool, got %T", key, value)
	}
	return flag, nil
}

// OptionalBool returns a validated optional boolean action payload.
func (r *CommandResult) OptionalBool(key string) (bool, bool, error) {
	if r == nil || r.Data == nil {
		return false, false, nil
	}
	value, exists := r.Data[key]
	if !exists || value == nil {
		return false, false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, true, fmt.Errorf("command result data %q must be a bool, got %T", key, value)
	}
	return flag, true, nil
}

// PromptCommandSourceSnapshot describes one source participating in an
// accepted prompt-command generation.
type PromptCommandSourceSnapshot struct {
	Kind       CommandSourceKind
	Trust      CommandTrustClass
	Name       string
	Version    string
	Directory  string
	Commands   int
	Skills     int
	Hooks      int
	MCPServers int
	Health     string
}

// PromptCommandDiagnostic describes one bounded candidate-generation validation
// result without making that candidate live.
type PromptCommandDiagnostic struct {
	Source   string
	Plugin   string
	Severity string
	Code     string
	Message  string
}

// PromptCommandGenerationCandidate is a complete, validated dynamic command
// generation. Registry replacement performs the final built-in and alias
// collision checks before committing it.
type PromptCommandGenerationCandidate struct {
	Digest      string
	Commands    []*Command
	Sources     []PromptCommandSourceSnapshot
	Diagnostics []PromptCommandDiagnostic
}

// PromptCommandGenerationSnapshot is committed under the same registry lock as the
// dynamic command map and order.
type PromptCommandGenerationSnapshot struct {
	Revision    uint64
	Digest      string
	Commands    int
	Sources     []PromptCommandSourceSnapshot
	Diagnostics []PromptCommandDiagnostic
}

// PromptCommandReloadResult summarizes an atomic bundled/plugin command refresh.
type PromptCommandReloadResult struct {
	EnabledPlugins int
	BundledPacks   int
	Commands       int
	Generation     PromptCommandGenerationSnapshot
	Diagnostics    []PromptCommandDiagnostic
}

// PromptCommandValidationResult summarizes a complete candidate without
// publishing it. LiveGeneration proves which generation remained committed
// across the validation attempt.
type PromptCommandValidationResult struct {
	EnabledPlugins int
	BundledPacks   int
	Commands       int
	Digest         string
	Sources        []PromptCommandSourceSnapshot
	Diagnostics    []PromptCommandDiagnostic
	LiveGeneration PromptCommandGenerationSnapshot
}

// Compatibility aliases keep embedding callers source-compatible while the
// production owner uses the prompt-command names above.
type (
	PluginSourceSnapshot      = PromptCommandSourceSnapshot
	PluginDiagnostic          = PromptCommandDiagnostic
	PluginGenerationCandidate = PromptCommandGenerationCandidate
	PluginGenerationSnapshot  = PromptCommandGenerationSnapshot
	PluginReloadResult        = PromptCommandReloadResult
)

// Registry holds registered commands and supports lookup and dispatch.
type Registry struct {
	mu                      sync.RWMutex
	commands                map[string]*Command
	order                   []string
	removed                 map[string]*RemovedCommand
	removedOrder            []string
	promptCommandKeys       map[string]struct{}
	promptCommandCanonicals map[string]struct{}
	promptGeneration        PromptCommandGenerationSnapshot
}

// NewRegistry creates a new empty command registry.
func NewRegistry() *Registry {
	return &Registry{
		commands:                make(map[string]*Command),
		removed:                 make(map[string]*RemovedCommand),
		promptCommandKeys:       make(map[string]struct{}),
		promptCommandCanonicals: make(map[string]struct{}),
	}
}

// Register validates and atomically adds one command. Canonical names and
// aliases share one normalized namespace and can never overwrite an existing
// record.
func (r *Registry) Register(cmd *Command) error {
	if r == nil {
		return fmt.Errorf("command registry is nil")
	}
	prepared, err := prepareCommand(cmd)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range commandKeys(prepared) {
		if _, exists := r.commands[key]; exists {
			return fmt.Errorf("command name or alias %q conflicts with an existing command", key)
		}
		if _, exists := r.removed[key]; exists {
			return fmt.Errorf("command name or alias %q conflicts with a removed command", key)
		}
	}
	r.registerLocked(prepared)
	return nil
}

// RegisterRemoved validates and atomically reserves a retired command name.
func (r *Registry) RegisterRemoved(removed *RemovedCommand) error {
	if r == nil {
		return fmt.Errorf("command registry is nil")
	}
	prepared, err := prepareRemovedCommand(removed)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range removedCommandKeys(prepared) {
		if _, exists := r.commands[key]; exists {
			return fmt.Errorf("removed command name or alias %q conflicts with an existing command", key)
		}
		if _, exists := r.removed[key]; exists {
			return fmt.Errorf("removed command name or alias %q conflicts with an existing removed command", key)
		}
	}
	r.removed[prepared.Name] = prepared
	r.removedOrder = append(r.removedOrder, prepared.Name)
	for _, alias := range prepared.Aliases {
		r.removed[alias] = prepared
	}
	return nil
}

func (r *Registry) mustRegisterRemoved(removed *RemovedCommand) {
	if err := r.RegisterRemoved(removed); err != nil {
		panic("commands: invalid removed built-in registry: " + err.Error())
	}
}

func (r *Registry) mustRegister(cmd *Command) {
	if err := r.Register(cmd); err != nil {
		panic("commands: invalid built-in registry: " + err.Error())
	}
}

func (r *Registry) registerLocked(cmd *Command) {
	r.commands[cmd.Name] = cmd
	r.order = append(r.order, cmd.Name)
	for _, alias := range cmd.Aliases {
		r.commands[alias] = cmd
	}
}

func prepareCommand(cmd *Command) (*Command, error) {
	if cmd == nil {
		return nil, fmt.Errorf("command is nil")
	}
	prepared := *cmd
	prepared.Name = normalizeCommandKey(cmd.Name)
	if prepared.Name == "" {
		return nil, fmt.Errorf("command name is required")
	}
	prepared.Aliases = make([]string, 0, len(cmd.Aliases))
	seen := map[string]struct{}{prepared.Name: {}}
	for _, rawAlias := range cmd.Aliases {
		alias := normalizeCommandKey(rawAlias)
		if alias == "" {
			return nil, fmt.Errorf("command %q has an empty alias", prepared.Name)
		}
		if _, duplicate := seen[alias]; duplicate {
			return nil, fmt.Errorf("command %q repeats name or alias %q", prepared.Name, alias)
		}
		seen[alias] = struct{}{}
		prepared.Aliases = append(prepared.Aliases, alias)
	}
	prepared.Args = append([]ArgDef(nil), cmd.Args...)
	prepared.Compatibility.DeprecatedAliases = append(
		[]string(nil),
		cmd.Compatibility.DeprecatedAliases...,
	)

	if err := applyCommandContractDefaults(&prepared); err != nil {
		return nil, err
	}
	if prepared.Execute == nil && prepared.legacyExecute != nil {
		legacy := prepared.legacyExecute
		prepared.Execute = func(ctx context.Context, cmdCtx *CommandContext) (*CommandResult, error) {
			return legacy(cmdCtx, strings.Join(cmdCtx.Args, " "))
		}
	}
	prepared.legacyExecute = nil
	if prepared.Execute == nil {
		return nil, fmt.Errorf("command %q has no handler", prepared.Name)
	}
	return &prepared, nil
}

func normalizeCommandKey(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	return strings.ToLower(strings.TrimSpace(name))
}

func commandKeys(cmd *Command) []string {
	keys := make([]string, 0, 1+len(cmd.Aliases))
	keys = append(keys, cmd.Name)
	keys = append(keys, cmd.Aliases...)
	return keys
}

func removedCommandKeys(removed *RemovedCommand) []string {
	return append([]string{removed.Name}, removed.Aliases...)
}

func prepareRemovedCommand(removed *RemovedCommand) (*RemovedCommand, error) {
	if removed == nil {
		return nil, fmt.Errorf("removed command is nil")
	}
	prepared := *removed
	prepared.Name = normalizeCommandKey(removed.Name)
	if prepared.Name == "" || strings.TrimSpace(prepared.Reason) == "" || strings.TrimSpace(prepared.RemovedIn) == "" {
		return nil, fmt.Errorf("removed command name, reason, and removed-in are required")
	}
	seen := map[string]struct{}{prepared.Name: {}}
	prepared.Aliases = make([]string, 0, len(removed.Aliases))
	for _, rawAlias := range removed.Aliases {
		alias := normalizeCommandKey(rawAlias)
		if alias == "" {
			return nil, fmt.Errorf("removed command %q has an empty alias", prepared.Name)
		}
		if _, duplicate := seen[alias]; duplicate {
			return nil, fmt.Errorf("removed command %q repeats name or alias %q", prepared.Name, alias)
		}
		seen[alias] = struct{}{}
		prepared.Aliases = append(prepared.Aliases, alias)
	}
	return &prepared, nil
}

func cloneRemovedCommand(removed *RemovedCommand) *RemovedCommand {
	if removed == nil {
		return nil
	}
	cloned := *removed
	cloned.Aliases = append([]string(nil), removed.Aliases...)
	return &cloned
}

func cloneCommand(cmd *Command) *Command {
	if cmd == nil {
		return nil
	}
	cloned := *cmd
	cloned.Aliases = append([]string(nil), cmd.Aliases...)
	cloned.Args = append([]ArgDef(nil), cmd.Args...)
	cloned.Compatibility.DeprecatedAliases = append(
		[]string(nil),
		cmd.Compatibility.DeprecatedAliases...,
	)
	return &cloned
}

func applyCommandContractDefaults(cmd *Command) error {
	if cmd.Source == "" {
		cmd.Source = string(CommandSourceCore)
	}
	if cmd.Trust == "" {
		cmd.Trust = CommandTrustCore
	}
	if cmd.Kind == "" {
		cmd.Kind = CommandKindQuery
	}
	if cmd.Entrypoints == EntrypointsNone {
		cmd.Entrypoints = EntrypointsTUI | EntrypointsPlain
	}
	if cmd.Availability == "" {
		cmd.Availability = AvailabilitySupported
	}
	if cmd.SideEffect == "" {
		cmd.SideEffect = SideEffectNone
	}
	if cmd.ResultKind == "" {
		cmd.ResultKind = ResultKindText
	}
	if err := applyDiscoveryDefaults(cmd); err != nil {
		return err
	}
	deprecated := make(map[string]struct{}, len(cmd.Compatibility.DeprecatedAliases))
	for i, rawAlias := range cmd.Compatibility.DeprecatedAliases {
		alias := normalizeCommandKey(rawAlias)
		if alias == "" {
			return fmt.Errorf("command %q has an empty deprecated alias", cmd.Name)
		}
		if _, duplicate := deprecated[alias]; duplicate {
			return fmt.Errorf("command %q repeats deprecated alias %q", cmd.Name, alias)
		}
		if !containsCommandAlias(cmd.Aliases, alias) {
			return fmt.Errorf("command %q deprecated alias %q is not an alias", cmd.Name, alias)
		}
		deprecated[alias] = struct{}{}
		cmd.Compatibility.DeprecatedAliases[i] = alias
	}
	if len(deprecated) > 0 && strings.TrimSpace(cmd.Compatibility.RemovalBoundary) == "" {
		return fmt.Errorf("command %q deprecated aliases require a removal boundary", cmd.Name)
	}
	if len(deprecated) == 0 && strings.TrimSpace(cmd.Compatibility.RemovalBoundary) != "" {
		return fmt.Errorf("command %q removal boundary requires deprecated aliases", cmd.Name)
	}
	if _, local := tuiLocalCommands[cmd.Name]; local {
		cmd.Kind = CommandKindUIAction
		cmd.Entrypoints = EntrypointsTUI
		cmd.Availability = AvailabilitySupported
		cmd.AvailabilityReason = ""
		cmd.SideEffect = SideEffectProcessLocal
		cmd.ResultKind = ResultKindUI
	}
	if _, workflow := promptWorkflowCommands[cmd.Name]; workflow {
		cmd.Kind = CommandKindPromptWorkflow
		cmd.ResultKind = ResultKindPrompt
	}
	if _, engineOwned := engineOwnedCommands[cmd.Name]; engineOwned {
		cmd.Entrypoints = EntrypointsTUI |
			EntrypointsPlain |
			EntrypointsHeadless |
			EntrypointsACP
		cmd.ExecutionOwner = ExecutionOwnerEngine
	}
	if entrypoints, overridden := engineOwnedEntrypointOverrides[cmd.Name]; overridden {
		cmd.Entrypoints = entrypoints
	}
	if sideEffect, mutating := mutatingCommandSideEffects[cmd.Name]; mutating {
		cmd.Kind = CommandKindRuntimeMutation
		if sideEffect == SideEffectDurableSession {
			cmd.Kind = CommandKindSessionMutation
		}
		cmd.SideEffect = sideEffect
		cmd.ResultKind = ResultKindAction
	}
	if cmd.ExecutionOwner == "" {
		cmd.ExecutionOwner = ExecutionOwnerEntrypoint
		if cmd.Kind == CommandKindRuntimeMutation ||
			cmd.Kind == CommandKindSessionMutation {
			cmd.ExecutionOwner = ExecutionOwnerEngine
		}
	}
	return nil
}

type commandDiscoveryMetadata struct {
	category CommandCategory
	tier     DiscoveryTier
	order    int
	phase    PhaseScope
}

var coreDiscoveryMetadata = map[string]commandDiscoveryMetadata{
	"new":         {category: CommandCategorySession, tier: DiscoveryTierPrimary, order: 10, phase: PhaseScopeIdleOnly},
	"compact":     {category: CommandCategorySession, tier: DiscoveryTierPrimary, order: 20, phase: PhaseScopeIdleOnly},
	"sessions":    {category: CommandCategorySession, tier: DiscoveryTierPrimary, order: 30, phase: PhaseScopeIdleOnly},
	"model":       {category: CommandCategoryRuntime, tier: DiscoveryTierPrimary, order: 40, phase: PhaseScopeIdleOnly},
	"plan":        {category: CommandCategorySafety, tier: DiscoveryTierPrimary, order: 50, phase: PhaseScopeIdleOnly},
	"permissions": {category: CommandCategorySafety, tier: DiscoveryTierPrimary, order: 60, phase: PhaseScopeIdleOnly},
	"status":      {category: CommandCategoryRuntime, tier: DiscoveryTierPrimary, order: 70, phase: PhaseScopeIdleOnly},
	"diff":        {category: CommandCategoryWorkspace, tier: DiscoveryTierPrimary, order: 80, phase: PhaseScopeIdleOnly},
	"files":       {category: CommandCategoryWorkspace, tier: DiscoveryTierPrimary, order: 90, phase: PhaseScopeIdleOnly},
	"agents":      {category: CommandCategoryAgents, tier: DiscoveryTierPrimary, order: 100, phase: PhaseScopeIdleOnly},

	"clear":    {category: CommandCategorySession, tier: DiscoveryTierSecondary, order: 1010, phase: PhaseScopeIdleOnly},
	"resume":   {category: CommandCategorySession, tier: DiscoveryTierSecondary, order: 1020, phase: PhaseScopeIdleOnly},
	"fork":     {category: CommandCategorySession, tier: DiscoveryTierSecondary, order: 1030, phase: PhaseScopeIdleOnly},
	"terminal": {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2010, phase: PhaseScopeIdleOnly},
	"suspend":  {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2020, phase: PhaseScopeIdleOnly},
	"quit":     {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2030, phase: PhaseScopeIdleOnly},
	"version":  {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2040, phase: PhaseScopeIdleOnly},
	"usage":    {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2050, phase: PhaseScopeIdleOnly},
	"context":  {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2060, phase: PhaseScopeIdleOnly},
	"config":   {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2070, phase: PhaseScopeIdleOnly},
	"effort":   {category: CommandCategoryRuntime, tier: DiscoveryTierSecondary, order: 2080, phase: PhaseScopeIdleOnly},
	"hooks":    {category: CommandCategorySafety, tier: DiscoveryTierSecondary, order: 3010, phase: PhaseScopeIdleOnly},
	"doctor":   {category: CommandCategorySafety, tier: DiscoveryTierSecondary, order: 3020, phase: PhaseScopeIdleOnly},
	"init":     {category: CommandCategoryWorkspace, tier: DiscoveryTierSecondary, order: 4010, phase: PhaseScopeIdleOnly},
	"memory":   {category: CommandCategoryWorkspace, tier: DiscoveryTierSecondary, order: 4020, phase: PhaseScopeIdleOnly},
	"add-dir":  {category: CommandCategoryWorkspace, tier: DiscoveryTierSecondary, order: 4030, phase: PhaseScopeIdleOnly},
	"tasks":    {category: CommandCategoryAgents, tier: DiscoveryTierSecondary, order: 5010, phase: PhaseScopeIdleOnly},
	"agent":    {category: CommandCategoryAgents, tier: DiscoveryTierSecondary, order: 5020, phase: PhaseScopeAny},
	"team":     {category: CommandCategoryAgents, tier: DiscoveryTierSecondary, order: 5030, phase: PhaseScopeAny},
	"queue":    {category: CommandCategoryAgents, tier: DiscoveryTierSecondary, order: 5040, phase: PhaseScopeAny},
	"skills":   {category: CommandCategoryExtensions, tier: DiscoveryTierSecondary, order: 6010, phase: PhaseScopeIdleOnly},
	"mcp":      {category: CommandCategoryExtensions, tier: DiscoveryTierSecondary, order: 6020, phase: PhaseScopeIdleOnly},
	"reload-plugins": {
		category: CommandCategoryExtensions,
		tier:     DiscoveryTierSecondary,
		order:    6030,
		phase:    PhaseScopeIdleOnly,
	},
	"help":        {category: CommandCategoryUI, tier: DiscoveryTierSecondary, order: 7010, phase: PhaseScopeIdleOnly},
	"copy":        {category: CommandCategoryUI, tier: DiscoveryTierSecondary, order: 7020, phase: PhaseScopeIdleOnly},
	"vim":         {category: CommandCategoryUI, tier: DiscoveryTierSecondary, order: 7030, phase: PhaseScopeIdleOnly},
	"theme":       {category: CommandCategoryUI, tier: DiscoveryTierSecondary, order: 7040, phase: PhaseScopeIdleOnly},
	"keybindings": {category: CommandCategoryUI, tier: DiscoveryTierSecondary, order: 7050, phase: PhaseScopeAny},
	"search":      {category: CommandCategoryUI, tier: DiscoveryTierSecondary, order: 7060, phase: PhaseScopeIdleOnly},
}

func applyDiscoveryDefaults(cmd *Command) error {
	metadata, known := coreDiscoveryMetadata[cmd.Name]
	if !known {
		metadata = commandDiscoveryMetadata{
			category: CommandCategoryRuntime,
			tier:     DiscoveryTierSecondary,
			order:    9000,
			phase:    PhaseScopeIdleOnly,
		}
		if cmd.Kind == CommandKindPromptWorkflow {
			metadata.category = CommandCategoryWorkflow
			metadata.order = 8000
		}
		if cmd.Trust == CommandTrustBundled {
			switch cmd.Name {
			case "review":
				metadata.tier = DiscoveryTierPrimary
				metadata.order = 110
			case "commit":
				metadata.tier = DiscoveryTierPrimary
				metadata.order = 120
			}
		}
	}
	if cmd.Category == "" {
		cmd.Category = metadata.category
	}
	if cmd.DiscoveryTier == "" {
		cmd.DiscoveryTier = metadata.tier
	}
	if cmd.DisplayOrder == 0 {
		cmd.DisplayOrder = metadata.order
	}
	if cmd.PhaseScope == "" {
		cmd.PhaseScope = metadata.phase
	}
	if !validCommandCategory(cmd.Category) {
		return fmt.Errorf("command %q has invalid discovery category %q", cmd.Name, cmd.Category)
	}
	if cmd.DiscoveryTier != DiscoveryTierPrimary && cmd.DiscoveryTier != DiscoveryTierSecondary {
		return fmt.Errorf("command %q has invalid discovery tier %q", cmd.Name, cmd.DiscoveryTier)
	}
	if cmd.DisplayOrder <= 0 {
		return fmt.Errorf("command %q has invalid display order %d", cmd.Name, cmd.DisplayOrder)
	}
	if cmd.PhaseScope != PhaseScopeIdleOnly && cmd.PhaseScope != PhaseScopeAny {
		return fmt.Errorf("command %q has invalid phase scope %q", cmd.Name, cmd.PhaseScope)
	}
	return nil
}

func validCommandCategory(category CommandCategory) bool {
	for _, candidate := range CommandCategoriesInDisplayOrder() {
		if category == candidate {
			return true
		}
	}
	return false
}

func containsCommandAlias(aliases []string, want string) bool {
	for _, alias := range aliases {
		if alias == want {
			return true
		}
	}
	return false
}

var tuiLocalCommands = map[string]struct{}{
	"agent":       {},
	"search":      {},
	"team":        {},
	"queue":       {},
	"terminal":    {},
	"suspend":     {},
	"vim":         {},
	"theme":       {},
	"keybindings": {},
	"copy":        {},
}

var promptWorkflowCommands = map[string]struct{}{
	"init":   {},
	"memory": {},
}

var engineOwnedCommands = map[string]struct{}{
	"new":         {},
	"clear":       {},
	"compact":     {},
	"sessions":    {},
	"resume":      {},
	"fork":        {},
	"model":       {},
	"permissions": {},
	"plan":        {},
	"effort":      {},
	"status":      {},
	"context":     {},
	"usage":       {},
	"config":      {},
	"doctor":      {},
	"diff":        {},
	"files":       {},
	"add-dir":     {},
	"goal":        {},
}

var engineOwnedEntrypointOverrides = map[string]EntrypointSet{
	// ACP owns external session handles keyed by the original identity. Until
	// that protocol has an atomic remap contract, identity-changing slash
	// commands fail during registry admission before engine mutation.
	"new":      EntrypointsTUI | EntrypointsPlain | EntrypointsHeadless,
	"sessions": EntrypointsTUI | EntrypointsPlain | EntrypointsHeadless,
	"resume":   EntrypointsTUI | EntrypointsPlain | EntrypointsHeadless,
	"fork":     EntrypointsTUI | EntrypointsPlain | EntrypointsHeadless,
	"goal":     EntrypointsTUI | EntrypointsPlain,
}

var mutatingCommandSideEffects = map[string]SideEffectKind{
	"new":            SideEffectDurableSession,
	"clear":          SideEffectDurableSession,
	"compact":        SideEffectDurableSession,
	"resume":         SideEffectDurableSession,
	"fork":           SideEffectDurableSession,
	"model":          SideEffectProcessLocal,
	"permissions":    SideEffectProcessLocal,
	"plan":           SideEffectDurableSession,
	"effort":         SideEffectProcessLocal,
	"add-dir":        SideEffectDurableSession,
	"goal":           SideEffectDurableSession,
	"reload-plugins": SideEffectProcessLocal,
}

// ReplacePluginCommands is a compatibility wrapper for callers that contribute
// only configured-plugin prompt commands.
func (r *Registry) ReplacePluginCommands(pluginCommands []*Command) error {
	_, err := r.ReplacePromptCommandGeneration(PromptCommandGenerationCandidate{
		Commands: configuredPluginCommands(pluginCommands),
	})
	return err
}

func configuredPluginCommands(pluginCommands []*Command) []*Command {
	configured := make([]*Command, 0, len(pluginCommands))
	for _, raw := range pluginCommands {
		cmd := cloneCommand(raw)
		if cmd != nil {
			if cmd.Source == "" {
				cmd.Source = string(CommandSourcePlugin)
			}
			if cmd.Trust == "" {
				cmd.Trust = CommandTrustConfigured
			}
		}
		configured = append(configured, cmd)
	}
	return configured
}

type promptCommandReplacement struct {
	commands   map[string]*Command
	order      []string
	keys       map[string]struct{}
	canonicals map[string]struct{}
}

func preparePromptCommandGeneration(
	candidate PromptCommandGenerationCandidate,
) ([]*Command, error) {
	preparedCommands := make([]*Command, 0, len(candidate.Commands))
	candidateKeys := make(map[string]struct{})
	for _, rawCommand := range candidate.Commands {
		if rawCommand == nil || strings.TrimSpace(rawCommand.Source) == "" ||
			rawCommand.Trust == "" {
			return nil, fmt.Errorf(
				"prompt command source and trust class are required",
			)
		}
		cmd, err := prepareCommand(rawCommand)
		if err != nil {
			return nil, fmt.Errorf("prompt command: %w", err)
		}
		for _, key := range commandKeys(cmd) {
			if _, duplicate := candidateKeys[key]; duplicate {
				return nil, fmt.Errorf(
					"duplicate prompt command name or alias %q",
					key,
				)
			}
			candidateKeys[key] = struct{}{}
		}
		preparedCommands = append(preparedCommands, cmd)
	}
	return preparedCommands, nil
}

// buildPromptCommandReplacementLocked constructs the complete next registry
// state without mutating the live generation. The caller must hold r.mu for
// reading or writing.
func (r *Registry) buildPromptCommandReplacementLocked(
	preparedCommands []*Command,
) (promptCommandReplacement, error) {
	nextCommands := make(map[string]*Command, len(r.commands)+len(preparedCommands))
	for key, cmd := range r.commands {
		if _, dynamic := r.promptCommandKeys[key]; !dynamic {
			nextCommands[key] = cmd
		}
	}
	nextOrder := make([]string, 0, len(r.order)+len(preparedCommands))
	for _, name := range r.order {
		if _, dynamic := r.promptCommandCanonicals[name]; !dynamic {
			nextOrder = append(nextOrder, name)
		}
	}

	nextKeys := make(map[string]struct{})
	nextCanonicals := make(map[string]struct{})
	for _, cmd := range preparedCommands {
		keys := commandKeys(cmd)
		for _, key := range keys {
			if _, removed := r.removed[key]; removed && !strings.Contains(key, ":") {
				return promptCommandReplacement{}, fmt.Errorf(
					"prompt command %q conflicts with a removed command", key,
				)
			}
			if _, exists := nextCommands[key]; exists {
				return promptCommandReplacement{}, fmt.Errorf(
					"prompt command %q conflicts with an existing command",
					key,
				)
			}
			nextKeys[key] = struct{}{}
		}
		nextCommands[cmd.Name] = cmd
		for _, alias := range cmd.Aliases {
			nextCommands[alias] = cmd
		}
		nextOrder = append(nextOrder, cmd.Name)
		nextCanonicals[cmd.Name] = struct{}{}
	}

	return promptCommandReplacement{
		commands:   nextCommands,
		order:      nextOrder,
		keys:       nextKeys,
		canonicals: nextCanonicals,
	}, nil
}

// ValidatePromptCommandGeneration applies the same source, alias, and live
// registry collision checks as replacement without changing the committed
// command map, order, or generation metadata.
func (r *Registry) ValidatePromptCommandGeneration(
	candidate PromptCommandGenerationCandidate,
) (PromptCommandGenerationSnapshot, error) {
	if r == nil {
		return PromptCommandGenerationSnapshot{}, fmt.Errorf("command registry is nil")
	}
	preparedCommands, err := preparePromptCommandGeneration(candidate)
	if err != nil {
		return r.PromptCommandGeneration(), err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, err = r.buildPromptCommandReplacementLocked(preparedCommands)
	return clonePromptCommandGeneration(r.promptGeneration), err
}

// ReplacePromptCommandGeneration validates and atomically swaps one complete
// bundled/configured prompt-command generation. Help, completion, palette and
// dispatch readers observe either the previous generation or the complete new
// generation.
func (r *Registry) ReplacePromptCommandGeneration(
	candidate PromptCommandGenerationCandidate,
) (PromptCommandGenerationSnapshot, error) {
	if r == nil {
		return PromptCommandGenerationSnapshot{}, fmt.Errorf("command registry is nil")
	}
	preparedCommands, err := preparePromptCommandGeneration(candidate)
	if err != nil {
		return PromptCommandGenerationSnapshot{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	replacement, err := r.buildPromptCommandReplacementLocked(preparedCommands)
	if err != nil {
		return PromptCommandGenerationSnapshot{}, err
	}

	nextGeneration := PromptCommandGenerationSnapshot{
		Revision:    r.promptGeneration.Revision + 1,
		Digest:      candidate.Digest,
		Commands:    len(preparedCommands),
		Sources:     clonePromptCommandSources(candidate.Sources),
		Diagnostics: clonePromptCommandDiagnostics(candidate.Diagnostics),
	}
	r.commands = replacement.commands
	r.order = replacement.order
	r.promptCommandKeys = replacement.keys
	r.promptCommandCanonicals = replacement.canonicals
	r.promptGeneration = nextGeneration
	return clonePromptCommandGeneration(nextGeneration), nil
}

// PromptCommandGeneration returns the immutable metadata committed with the
// live bundled/configured prompt-command generation.
func (r *Registry) PromptCommandGeneration() PromptCommandGenerationSnapshot {
	if r == nil {
		return PromptCommandGenerationSnapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return clonePromptCommandGeneration(r.promptGeneration)
}

// ReplacePluginGeneration preserves the previous API while delegating to the
// single prompt-command generation owner.
func (r *Registry) ReplacePluginGeneration(
	candidate PluginGenerationCandidate,
) (PluginGenerationSnapshot, error) {
	candidate.Commands = configuredPluginCommands(candidate.Commands)
	return r.ReplacePromptCommandGeneration(candidate)
}

// PluginGeneration preserves the previous inspection API.
func (r *Registry) PluginGeneration() PluginGenerationSnapshot {
	return r.PromptCommandGeneration()
}

func clonePromptCommandGeneration(
	generation PromptCommandGenerationSnapshot,
) PromptCommandGenerationSnapshot {
	generation.Sources = clonePromptCommandSources(generation.Sources)
	generation.Diagnostics = clonePromptCommandDiagnostics(generation.Diagnostics)
	return generation
}

func clonePromptCommandSources(
	sources []PromptCommandSourceSnapshot,
) []PromptCommandSourceSnapshot {
	return append([]PromptCommandSourceSnapshot(nil), sources...)
}

func clonePromptCommandDiagnostics(
	diagnostics []PromptCommandDiagnostic,
) []PromptCommandDiagnostic {
	return append([]PromptCommandDiagnostic(nil), diagnostics...)
}

// Get looks up a command by name or alias. Returns nil if not found.
func (r *Registry) Get(name string) *Command {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneCommand(r.commands[normalizeCommandKey(name)])
}

// GetRemoved looks up a retired command by canonical name or alias.
func (r *Registry) GetRemoved(name string) *RemovedCommand {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneRemovedCommand(r.removed[normalizeCommandKey(name)])
}

// ListRemoved returns canonical retired commands in registration order.
func (r *Registry) ListRemoved() []*RemovedCommand {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*RemovedCommand, 0, len(r.removedOrder))
	for _, name := range r.removedOrder {
		result = append(result, cloneRemovedCommand(r.removed[name]))
	}
	return result
}

// GetFor returns a command only when it is discoverable on the entrypoint.
func (r *Registry) GetFor(entrypoint Entrypoint, name string) *Command {
	cmd := r.Get(name)
	if cmd == nil ||
		cmd.Availability != AvailabilitySupported ||
		!cmd.Entrypoints.Supports(entrypoint) {
		return nil
	}
	return cmd
}

// ResolveForContext returns the entrypoint-scoped command with its effective
// runtime availability applied. Unlike GetForContext it also returns a command
// narrowed to unavailable so help can explain the same capability result that
// direct dispatch enforces.
func (r *Registry) ResolveForContext(
	ctx context.Context,
	entrypoint Entrypoint,
	cmdCtx *CommandContext,
	name string,
) *Command {
	cmd := r.Get(name)
	if cmd == nil || !cmd.Entrypoints.Supports(entrypoint) {
		return nil
	}
	state, reason := resolveCommandAvailability(ctx, cmd, commandContextForEnvironment(cmdCtx, entrypoint))
	cmd.Availability = state
	cmd.AvailabilityReason = reason
	return cmd
}

// GetForContext returns a command only when the active runtime capability is
// discoverable on the entrypoint.
func (r *Registry) GetForContext(
	ctx context.Context,
	entrypoint Entrypoint,
	cmdCtx *CommandContext,
	name string,
) *Command {
	cmd := r.ResolveForContext(ctx, entrypoint, cmdCtx, name)
	if cmd == nil || cmd.Availability != AvailabilitySupported {
		return nil
	}
	return cmd
}

// List returns every command that is discoverable on at least one entrypoint.
// Entrypoint-specific consumers must use ListFor.
func (r *Registry) List() []*Command {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var result []*Command
	for _, name := range r.order {
		if seen[name] {
			continue
		}
		seen[name] = true
		cmd := r.commands[name]
		if cmd != nil && cmd.Availability == AvailabilitySupported &&
			cmd.Entrypoints != EntrypointsNone {
			result = append(result, cloneCommand(cmd))
		}
	}
	return result
}

// ListFor returns one immutable discovery snapshot for an entrypoint.
func (r *Registry) ListFor(entrypoint Entrypoint) []*Command {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Command
	for _, name := range r.order {
		cmd := r.commands[name]
		if cmd != nil &&
			cmd.Availability == AvailabilitySupported &&
			cmd.Entrypoints.Supports(entrypoint) {
			result = append(result, cloneCommand(cmd))
		}
	}
	return result
}

// ListForContext returns the discovery snapshot after applying the active
// runtime capability resolver for every statically supported command.
func (r *Registry) ListForContext(
	ctx context.Context,
	entrypoint Entrypoint,
	cmdCtx *CommandContext,
) []*Command {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	candidates := make([]*Command, 0, len(r.order))
	for _, name := range r.order {
		cmd := r.commands[name]
		if cmd == nil || !cmd.Entrypoints.Supports(entrypoint) {
			continue
		}
		candidates = append(candidates, cloneCommand(cmd))
	}
	r.mu.RUnlock()
	var result []*Command
	for _, cmd := range candidates {
		state, _ := resolveCommandAvailability(ctx, cmd, commandContextForEnvironment(cmdCtx, entrypoint))
		if state == AvailabilitySupported {
			result = append(result, cmd)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DisplayOrder != result[j].DisplayOrder {
			return result[i].DisplayOrder < result[j].DisplayOrder
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// CommandDiscoveryInput carries the optional unstructured argument hint
// projected for one discovery row.
type CommandDiscoveryInput struct {
	Hint string `json:"hint"`
}

// CommandDiscoveryRow is one immutable SDK-neutral discovery row. The JSON
// field names and order are the canonical wire shape used by the digest.
type CommandDiscoveryRow struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *CommandDiscoveryInput `json:"input,omitempty"`
}

// CommandDiscoverySnapshot is the immutable SDK-neutral discovery projection
// owned by the registry. Digest is the lowercase hex SHA-256 of the canonical
// JSON encoding of Rows and is the only replacement identity for discovery
// consumers. PromptCommandGeneration metadata is merely a recomputation
// trigger and is never part of this identity.
type CommandDiscoverySnapshot struct {
	Rows   []CommandDiscoveryRow
	Digest string
}

// DiscoverySnapshotForContext consumes ListForContext exactly once and
// projects the immutable SDK-neutral discovery snapshot for an entrypoint,
// preserving its visibility filtering and order. A nil or empty registry
// yields empty rows and the deterministic SHA-256 of the canonical empty
// JSON array.
func (r *Registry) DiscoverySnapshotForContext(
	ctx context.Context,
	entrypoint Entrypoint,
	cmdCtx *CommandContext,
) CommandDiscoverySnapshot {
	commands := r.ListForContext(ctx, entrypoint, cmdCtx)
	rows := make([]CommandDiscoveryRow, 0, len(commands))
	for _, cmd := range commands {
		rows = append(rows, CommandDiscoveryRow{
			Name:        cmd.Name,
			Description: cmd.Description,
			Input:       commandDiscoveryInput(cmd),
		})
	}
	return CommandDiscoverySnapshot{
		Rows:   rows,
		Digest: commandDiscoveryDigest(rows),
	}
}

// commandDiscoveryInput derives the optional input hint from the trimmed
// usage suffix after the exact canonical "/name" prefix separated by
// whitespace. Without a canonical non-empty suffix, the ordered ArgDef
// placeholders supply the hint: <name> for required arguments and [name] or
// [name=default] for optional arguments. Commands with no hint omit the
// input object.
func commandDiscoveryInput(cmd *Command) *CommandDiscoveryInput {
	if cmd == nil {
		return nil
	}
	hint := ""
	if rest, ok := strings.CutPrefix(cmd.Usage, "/"+cmd.Name); ok && rest != "" {
		if r, _ := utf8.DecodeRuneInString(rest); unicode.IsSpace(r) {
			hint = strings.TrimSpace(rest)
		}
	}
	if hint == "" {
		hint = commandArgDefsHint(cmd.Args)
	}
	if hint == "" {
		return nil
	}
	return &CommandDiscoveryInput{Hint: hint}
}

func commandArgDefsHint(args []ArgDef) string {
	parts := make([]string, 0, len(args))
	for _, def := range args {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		switch {
		case def.Required:
			parts = append(parts, "<"+name+">")
		case def.Default != "":
			parts = append(parts, "["+name+"="+def.Default+"]")
		default:
			parts = append(parts, "["+name+"]")
		}
	}
	return strings.Join(parts, " ")
}

// commandDiscoveryDigest returns the lowercase hex SHA-256 of the canonical
// JSON encoding of the rows: ordered objects carrying exactly the fields
// name, description, and the optional input object, with an empty row set
// serialized as [].
func commandDiscoveryDigest(rows []CommandDiscoveryRow) string {
	if rows == nil {
		rows = []CommandDiscoveryRow{}
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(rows); err != nil {
		// Rows carry only strings and cannot fail to encode.
		encoded.Reset()
		encoded.WriteString("[]\n")
	}
	canonical := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func commandContextForEnvironment(cmdCtx *CommandContext, entrypoint Entrypoint) *CommandContext {
	if cmdCtx == nil {
		cmdCtx = &CommandContext{}
	}
	cloned := *cmdCtx
	cloned.Entrypoint = entrypoint
	cloned.Environment.Entrypoint = entrypoint
	if cloned.Environment.Phase == "" {
		cloned.Environment.Phase = CommandPhaseIdle
	}
	return &cloned
}

func resolveCommandAvailability(
	ctx context.Context,
	cmd *Command,
	cmdCtx *CommandContext,
) (AvailabilityState, string) {
	if cmd == nil {
		return AvailabilityUnavailable, "command is not registered"
	}
	phase := CommandPhaseIdle
	if cmdCtx != nil && cmdCtx.Environment.Phase != "" {
		phase = cmdCtx.Environment.Phase
	}
	switch phase {
	case CommandPhaseIdle:
	case CommandPhaseActiveTurn:
		if cmd.PhaseScope == PhaseScopeIdleOnly {
			return AvailabilityUnavailable, "command is available only while no request is running"
		}
		if cmdCtx != nil && len(cmdCtx.Args) > 0 &&
			(cmd.Name == "agent" || cmd.Name == "team" || cmd.Name == "keybindings") {
			return AvailabilityUnavailable,
				fmt.Sprintf("only /%s without arguments is available while a request is running", cmd.Name)
		}
	default:
		return AvailabilityUnavailable, "command phase is invalid"
	}
	state := cmd.Availability
	reason := strings.TrimSpace(cmd.AvailabilityReason)
	if state != AvailabilitySupported || cmd.ResolveAvailability == nil {
		return state, reason
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, resolvedReason := cmd.ResolveAvailability(ctx, cmdCtx)
	if resolved == "" {
		resolved = AvailabilitySupported
	}
	if resolved != AvailabilitySupported && strings.TrimSpace(resolvedReason) == "" {
		resolvedReason = "the active runtime capability is unavailable"
	}
	return resolved, strings.TrimSpace(resolvedReason)
}

// Dispatch is the only strict parse, validation, availability and execution
// boundary after an entrypoint has classified command ownership.
func (r *Registry) Dispatch(
	ctx context.Context,
	entrypoint Entrypoint,
	cmdCtx *CommandContext,
	input string,
) (*CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("command canceled before dispatch: %w", err)
	}
	input = strings.TrimSpace(input)
	if !IsCommand(input) {
		return nil, fmt.Errorf("not a command: %q", input)
	}

	name, args, parseErr := parseCommandInputStrict(input)
	if parseErr != nil {
		return nil, fmt.Errorf("invalid command input: %w", parseErr)
	}

	r.mu.RLock()
	cmd := r.commands[name]
	removed := r.removed[name]
	r.mu.RUnlock()
	if removed != nil {
		removed = cloneRemovedCommand(removed)
		return &CommandResult{
			Output:       removedCommandGuidance(name, removed),
			Action:       ActionNone,
			Availability: AvailabilityUnavailable,
			Removed:      removed,
		}, nil
	}
	if cmd == nil {
		return nil, fmt.Errorf("unknown command: /%s", name)
	}
	if !cmd.Entrypoints.Supports(entrypoint) {
		return nil, fmt.Errorf(
			"/%s is unavailable on %s: supported entrypoint scope does not include %s",
			name,
			entrypoint,
			entrypoint,
		)
	}
	cmdCtx = commandContextForEnvironment(cmdCtx, entrypoint)
	cmdCtx.RawInput = input
	cmdCtx.Args = append([]string(nil), args...)
	cmdCtx.Context = ctx
	cmdCtx.Entrypoint = entrypoint
	cmdCtx.Environment.Entrypoint = entrypoint
	if cmdCtx.Environment.Phase == "" {
		cmdCtx.Environment.Phase = CommandPhaseIdle
	}
	effectiveAvailability, effectiveReason := resolveCommandAvailability(ctx, cmd, cmdCtx)
	if effectiveAvailability == AvailabilityDisabled ||
		effectiveAvailability == AvailabilityUnavailable {
		reason := strings.TrimSpace(effectiveReason)
		if reason == "" {
			reason = "the required capability is unavailable"
		}
		return &CommandResult{
			Output:       fmt.Sprintf("/%s is unavailable: %s.", name, reason),
			Action:       ActionNone,
			Availability: effectiveAvailability,
		}, nil
	}
	if err := cmd.ValidateArgs(args); err != nil {
		return nil, fmt.Errorf("/%s: %w", name, err)
	}
	// Phase can change after admission while a palette selection is pending.
	effectiveAvailability, effectiveReason = resolveCommandAvailability(ctx, cmd, cmdCtx)
	if effectiveAvailability == AvailabilityDisabled || effectiveAvailability == AvailabilityUnavailable {
		reason := strings.TrimSpace(effectiveReason)
		if reason == "" {
			reason = "the required capability is unavailable"
		}
		return &CommandResult{Output: fmt.Sprintf("/%s is unavailable: %s.", name, reason), Action: ActionNone, Availability: effectiveAvailability}, nil
	}

	result, err := cmd.Execute(ctx, cmdCtx)
	if err != nil || result == nil || !containsCommandAlias(cmd.Compatibility.DeprecatedAliases, name) {
		return result, err
	}
	warning := &CommandCompatibilityWarning{
		Alias:           name,
		Replacement:     cmd.Name,
		RemovalBoundary: cmd.Compatibility.RemovalBoundary,
	}
	result.CompatibilityWarning = warning
	warningText := fmt.Sprintf(
		"Warning: /%s is deprecated; use /%s before %s.",
		warning.Alias,
		warning.Replacement,
		warning.RemovalBoundary,
	)
	if result.Output == "" {
		result.Output = warningText
	} else {
		result.Output += "\n\n" + warningText
	}
	return result, nil
}

func removedCommandGuidance(invoked string, removed *RemovedCommand) string {
	if strings.TrimSpace(removed.Replacement) == "" {
		return fmt.Sprintf("/%s was removed in %s: %s", invoked, removed.RemovedIn, removed.Reason)
	}
	return fmt.Sprintf("/%s was removed in %s: %s\n\nUse %s.", invoked, removed.RemovedIn, removed.Reason, removed.Replacement)
}

// IsCommand returns true if the input string looks like a slash command.
func IsCommand(input string) bool {
	input = strings.TrimSpace(input)
	if len(input) < 2 {
		return false
	}
	if input[0] != '/' {
		return false
	}
	// The character after the slash must be a letter (not another slash or space)
	c := input[1]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// RegisterDefaults registers all built-in commands into the given registry.
func RegisterDefaults(r *Registry) {
	r.mustRegister(&Command{
		Name:        "help",
		Description: "List available commands or show help for a specific command",
		Usage:       "/help [command]",
		DetailedHelp: `Show the list of available slash commands, or detailed help for a specific command.

Examples:
  /help          — list all commands
  /help model    — show detailed help for /model
  /help diff     — show detailed help for /diff`,
		Args: []ArgDef{
			{Name: "command", Type: "string", Required: false, Description: "Command name to get help for"},
		},
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			args = strings.TrimSpace(args)
			resolveCtx := context.Background()
			if ctx != nil && ctx.Context != nil {
				resolveCtx = ctx.Context
			}
			if args != "" {
				// Show detailed help for a specific command
				cmdName := strings.TrimPrefix(strings.ToLower(args), "/")
				cmd := r.ResolveForContext(resolveCtx, ctx.Entrypoint, ctx, cmdName)
				if cmd == nil ||
					(cmd.Availability != AvailabilitySupported && cmd.ResolveAvailability == nil) {
					return &CommandResult{
						Output: fmt.Sprintf("Unknown command: /%s\n\nUse /help to see all available commands.", cmdName),
					}, nil
				}
				return &CommandResult{Output: cmd.FormatHelpFor(ctx.Entrypoint)}, nil
			}

			// List all commands
			var sb strings.Builder
			sb.WriteString("Available commands:\n\n")
			all := r.ListForContext(resolveCtx, ctx.Entrypoint, ctx)
			for _, category := range CommandCategoriesInDisplayOrder() {
				printed := false
				for _, cmd := range all {
					if cmd.Category != category {
						continue
					}
					if !printed {
						fmt.Fprintf(&sb, "%s:\n", category)
						printed = true
					}
					fmt.Fprintf(&sb, "  %-20s %s\n", "/"+cmd.Name, cmd.Description)
				}
				if printed {
					sb.WriteString("\n")
				}
			}
			sb.WriteString("\nUse /help <command> for detailed help on a specific command.")
			return &CommandResult{Output: sb.String()}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "new",
		Description: "Start a fresh session without deleting the current transcript",
		Usage:       "/new",
		legacyExecute: func(_ *CommandContext, _ string) (*CommandResult, error) {
			return &CommandResult{
				Output: "Starting a new session...",
				Action: ActionNew,
			}, nil
		},
	})

	r.mustRegister(&Command{
		Name:          "clear",
		Aliases:       []string{"reset"},
		Description:   "Clear conversation history and free up context",
		Usage:         "/clear",
		legacyExecute: executeClear,
	})

	r.mustRegister(&Command{
		Name:          "compact",
		Description:   "Compact conversation history, keeping a summary in context",
		Usage:         "/compact [custom instructions]",
		legacyExecute: executeCompact,
	})

	r.mustRegister(&Command{
		Name:           "status",
		Description:    "Show a source-derived runtime and session summary",
		Usage:          "/status",
		ExecutionOwner: ExecutionOwnerEngine,
		legacyExecute:  executeStatus,
	})

	r.mustRegister(&Command{
		Name:          "model",
		Description:   "Show or change the current model",
		Usage:         "/model [name]",
		legacyExecute: executeModel,
	})

	r.mustRegister(&Command{
		Name:        "sessions",
		Description: "List, search, resume, rename, or export saved sessions",
		Usage:       "/sessions [list [limit]|search <query> [limit]|resume <session-id>|rename <session-id> <name>|export <session-id> [filename]]",
		DetailedHelp: `Use one session service for discovery and lifecycle actions.

Examples:
  /sessions
  /sessions list 20
  /sessions search "plan mode"
  /sessions resume SESSION_ID
  /sessions rename SESSION_ID "release investigation"
  /sessions export SESSION_ID report.md

Use "current" in place of SESSION_ID for rename or export. In the TUI,
/resume without an ID opens the same service-backed picker.`,
		Kind:           CommandKindSessionMutation,
		SideEffect:     SideEffectDurableSession,
		ResultKind:     ResultKindAction,
		ExecutionOwner: ExecutionOwnerEngine,
		Execute:        executeSessions,
	})

	r.mustRegister(&Command{
		Name:        "resume",
		Description: "Select or resume a previous session",
		Usage:       "/resume [session-id]",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			sessionID := strings.TrimSpace(args)
			output := "Select a session to resume."
			if sessionID != "" {
				output = fmt.Sprintf("Resuming session %s...", sessionID)
			}
			return &CommandResult{
				Output: output,
				Action: ActionResume,
				Data:   map[string]any{"session_id": sessionID},
			}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "terminal",
		Description: "Show terminal capabilities and active degradation paths",
		Usage:       "/terminal",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			if ctx.Extra != nil {
				if summary, ok := ctx.Extra["terminal_capabilities"].(string); ok && summary != "" {
					return &CommandResult{Output: summary}, nil
				}
			}
			return &CommandResult{Output: "Terminal capability diagnostics are available in the interactive TUI."}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "suspend",
		Description: "Suspend the TUI and return with the shell fg command",
		Usage:       "/suspend",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			interactive, _ := ctx.Extra["interactive_tui"].(bool)
			if !interactive {
				return &CommandResult{Output: "Suspend is available only in the interactive TUI."}, nil
			}
			return &CommandResult{
				Output: "Suspending TUI. Run fg to resume.",
				Action: ActionSuspend,
			}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "quit",
		Aliases:     []string{"exit"},
		Description: "Exit the session",
		Usage:       "/quit",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			return &CommandResult{
				Output: "Goodbye!",
				Action: ActionQuit,
			}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "version",
		Description: "Show version information and build details",
		Usage:       "/version",
		DetailedHelp: `Display the current version of ` + identity.ProductName + `, including build metadata
such as Go version, commit hash, build time, and platform.

Examples:
  /version   — show full version info`,
		legacyExecute: executeVersion,
	})

	r.mustRegister(&Command{
		Name:           "usage",
		Description:    "Show persisted provider-reported token usage and coverage",
		Usage:          "/usage",
		ExecutionOwner: ExecutionOwnerEngine,
		legacyExecute:  executeUsage,
	})

	r.mustRegister(&Command{
		Name:           "context",
		Aliases:        []string{"ctx"},
		Description:    "Show attributable context contributors and known token fields",
		Usage:          "/context",
		ExecutionOwner: ExecutionOwnerEngine,
		legacyExecute:  executeContextCommand,
	})

	r.mustRegister(&Command{
		Name:           "config",
		Description:    "Show redacted effective configuration and winning sources",
		Usage:          "/config",
		ExecutionOwner: ExecutionOwnerEngine,
		legacyExecute:  executeConfig,
	})

	r.mustRegister(&Command{
		Name:        "diff",
		Description: "Show git diff summary for uncommitted changes",
		Usage:       "/diff [full|staged|stat]",
		DetailedHelp: `Show a summary of uncommitted changes in the current git repository.
By default shows a numstat summary (files changed with +/- counts).

Subcommands:
  full    — show the full patch diff (truncated at 200 lines)
  staged  — show only staged changes (git diff --cached)
  stat    — show diffstat summary (default)

Examples:
  /diff         — show numstat summary
  /diff full    — show full patch
  /diff staged  — show staged changes only`,
		Args: []ArgDef{
			{Name: "mode", Type: "string", Required: false, Description: "full, staged, or stat"},
		},
		ExecutionOwner: ExecutionOwnerEngine,
		legacyExecute:  executeDiff,
	})

	r.mustRegister(&Command{
		Name:                "copy",
		Description:         "Copy a committed assistant response to the clipboard",
		Usage:               "/copy [N]",
		ResolveAvailability: resolveCopyAvailability,
		legacyExecute:       executeCopy,
	})

	r.mustRegister(&Command{
		Name:        "init",
		Description: "Create or refresh project-native AGENTS.md instructions",
		Usage:       "/init [--force]",
		DetailedHelp: `Analyze the current project and create or update AGENTS.md through
the ordinary Agent tool and permission workflow. The command itself never writes
configuration files.

If --force is passed, rebuild the project instructions from verified sources.

Examples:
	/init          — create or refresh AGENTS.md
	/init --force  — rebuild AGENTS.md from current project sources`,
		SideEffect: SideEffectWorkspace,
		Args: []ArgDef{
			{Name: "flags", Type: "string", Required: false, Description: "--force to recreate"},
		},
		legacyExecute: executeInit,
	})

	r.mustRegister(&Command{
		Name:        "permissions",
		Description: "Show or change typed permission modes and rules",
		Usage:       "/permissions [mode <mode>|bypass confirm|rules [list|add|remove] ...]",
		DetailedHelp: `Permission modes and rules share one engine-owned safety control.

Bypass is fail-closed unless the exact command carries the confirmation token:
  /permissions bypass confirm

Use quoted rule arguments so parsing is deterministic:
  /permissions rules add allow "Bash(go test *)" --local`,
		legacyExecute: executePermissions,
	})

	r.mustRegister(&Command{
		Name:          "hooks",
		Description:   "View hook configurations",
		Usage:         "/hooks",
		legacyExecute: executeHooks,
	})

	r.mustRegister(&Command{
		Name:           "doctor",
		Description:    "Run stable read-only runtime, provider, config, and transcript checks",
		Usage:          "/doctor",
		ExecutionOwner: ExecutionOwnerEngine,
		legacyExecute:  executeDoctor,
	})

	// --- Tier 2 commands ---

	r.mustRegister(&Command{
		Name:          "plan",
		Description:   "Toggle plan mode or view current plan",
		Usage:         "/plan [off|<description>]",
		legacyExecute: executePlan,
	})

	r.mustRegister(goalCommand())

	r.mustRegister(&Command{
		Name:          "memory",
		Description:   "Inspect instruction memory or request a scoped project mutation",
		Usage:         "/memory [status|browse|edit project|delete project|migrate project]",
		SideEffect:    SideEffectWorkspace,
		legacyExecute: executeMemory,
	})

	r.mustRegister(&Command{
		Name:          "add-dir",
		Description:   "Add a working directory to the session",
		Usage:         "/add-dir <path>",
		legacyExecute: executeAddDir,
	})

	r.mustRegister(&Command{
		Name:          "tasks",
		Description:   "Inspect runtime-visible tasks",
		Usage:         "/tasks",
		legacyExecute: executeTasks,
	})

	// --- Tier 3 commands ---

	r.mustRegister(&Command{
		Name:                "effort",
		Description:         "Show or change reasoning effort level",
		Usage:               "/effort [level]",
		ResolveAvailability: resolveEffortAvailability,
		legacyExecute:       executeEffort,
	})

	r.mustRegister(&Command{
		Name:          "skills",
		Description:   "List available skills",
		Usage:         "/skills [query]",
		legacyExecute: executeSkills,
	})

	r.mustRegister(&Command{
		Name:          "agents",
		Description:   "List built-in sub-agent types",
		Usage:         "/agents [name]",
		legacyExecute: executeAgents,
	})

	r.mustRegister(&Command{
		Name:          "agent",
		Description:   "Switch runtime Agent threads or manage custom definitions",
		Usage:         "/agent [create|list|edit <name>]",
		legacyExecute: executeAgent,
	})
	r.mustRegister(&Command{
		Name:          "mcp",
		Description:   "Inspect runtime MCP servers and tools",
		Usage:         "/mcp [server]",
		legacyExecute: executeMCP,
	})

	r.mustRegister(&Command{
		Name:        "team",
		Aliases:     []string{"teams"},
		Description: "View active team members (sub-agents) and their status",
		Usage:       "/team",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			// Handled locally in the TUI layer (opens teams panel).
			// Registry entry exists for autocomplete/help.
			return &CommandResult{Output: "Opening team members panel..."}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "queue",
		Description: "List, edit, or remove queued input for the active thread",
		Usage:       "/queue [list|edit <id|last>|remove <id|last|all>]",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			return &CommandResult{Output: "Queue controls are available in the interactive TUI."}, nil
		},
	})

	// --- Tier 4 commands (expanded registry for parity) ---

	r.mustRegister(&Command{
		Name:          "files",
		Description:   "List all files currently in context",
		Usage:         "/files",
		legacyExecute: executeFiles,
	})

	r.mustRegister(&Command{
		Name:        "vim",
		Description: "Toggle vim editing mode",
		Usage:       "/vim",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			return &CommandResult{
				Output: "Toggling vim mode.",
				Action: ActionToggleVim,
			}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "theme",
		Description: "Change the color theme",
		Usage:       "/theme [name]",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			if args == "" {
				return &CommandResult{
					Output: "Available themes: polar-night (default), daybreak, dark-ansi, light-ansi, snowy, aubergine\nLegacy aliases for one release: dark, light\nUsage: /theme <name>",
				}, nil
			}
			return &CommandResult{
				Output: fmt.Sprintf("Switching theme to: %s", args),
				Action: ActionChangeTheme,
				Data:   map[string]any{"theme": args},
			}, nil
		},
	})

	r.mustRegister(&Command{
		Name:          "fork",
		Description:   "Fork conversation: create a branch and continue on it",
		Usage:         "/fork [name]",
		legacyExecute: executeFork,
	})

	r.mustRegister(&Command{
		Name:        "keybindings",
		Description: "Show key bindings",
		Usage:       "/keybindings",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			return &CommandResult{
				Output: "Key Bindings:\n  Enter        → submit\n  Ctrl+J       → newline\n  Ctrl+C       → interrupt running / clear input\n  Ctrl+D       → exit\n  Ctrl+O       → expand editor\n  Shift+Tab    → cycle permission mode\n  Up/Down      → history navigation\n  Escape       → clear input / exit command mode\n  /            → enter command mode\n  !            → shell mode",
			}, nil
		},
	})

	r.mustRegister(&Command{
		Name:        "search",
		Description: "Search the current conversation",
		Usage:       "/search [query]",
		legacyExecute: func(ctx *CommandContext, args string) (*CommandResult, error) {
			return &CommandResult{
				Output: "Search controls are available in the interactive TUI.",
			}, nil
		},
	})

	registerAdditionalCommands(r)
	registerRemovedDefaults(r)
}

func registerRemovedDefaults(r *Registry) {
	removedDefaults := []*RemovedCommand{
		{Name: "plugin", Reason: "plugin installation and removal have no trusted owner", Replacement: "/reload-plugins for configured prompt commands or provider-free plugins CLI for inspection", RemovedIn: "P21.0"},
		{Name: "bug", Aliases: []string{"feedback"}, Reason: "bug delivery has no owned cross-entrypoint channel", Replacement: "describe the issue normally and use an owned external delivery channel", RemovedIn: "P21.0"},
		{Name: "undo", Reason: "durable reversible turn history is not implemented", Replacement: "use /fork before future work for durable lineage", RemovedIn: "P21.0"},
		{Name: "rewrite", Aliases: []string{"retry"}, Reason: "durable reversible turn history is not implemented", Replacement: "send a corrected request or /fork for durable lineage", RemovedIn: "P21.0"},
		{Name: "branch", Reason: "checkpoint-only branching is not an owned contract", Replacement: "/fork [name]", RemovedIn: "P21.0"},
		{Name: "rewind", Reason: "file rollback is owned by Git and the workspace", Replacement: "Git/workspace-owned recovery; no automatic file rollback", RemovedIn: "P21.0"},
		{Name: "color", Reason: "prompt color has no effective runtime owner", Replacement: "/theme", RemovedIn: "P21.0"},
		{Name: "fast", Reason: "fast-model routing is not provider-aware", Replacement: "/model with an explicit supported model", RemovedIn: "P21.0"},
		{Name: "tag", Reason: "durable indexed session tags are not implemented", Replacement: "/sessions rename for durable searchable metadata", RemovedIn: "P21.0"},
		{Name: "share", Reason: "sharing has no owned backend", Replacement: "/sessions export then an owned external channel", RemovedIn: "P21.0"},
		{Name: "release-notes", Reason: "release notes are not connected to authoritative metadata", Replacement: "/version plus authoritative release artifacts", RemovedIn: "P21.0"},
		{Name: "mode", Reason: "generic mode strings are not a supported safety contract", Replacement: "/permissions mode <mode>", RemovedIn: "P21.0"},
		{Name: "bypass", Aliases: []string{"yolo"}, Reason: "top-level bypass is not canonical", Replacement: "/permissions bypass confirm", RemovedIn: "P21.0"},
		{Name: "logout", Reason: "credential removal is owned by the provider or credential source", Replacement: "remove credentials via environment/config/token store/provider owner", RemovedIn: "P21.0"},
		{Name: "login", Reason: "provider authentication is not project-owned", Replacement: "configure provider via its env/config; /config shows redacted state", RemovedIn: "P21.0"},
		{Name: "env", Reason: "environment diagnostics are provided by supported commands", Replacement: "/doctor or /config", RemovedIn: "P21.0"},
		{Name: "output-style", Reason: "output style belongs to project configuration", Replacement: "project config; /config reports effective state", RemovedIn: "P21.0"},
		{Name: "session", Aliases: []string{"remote"}, Reason: "legacy remote session shortcuts are retired", Replacement: "/sessions", RemovedIn: "P21.0"},
		{Name: "stats", Aliases: []string{"cost"}, Reason: "usage aliases expired", Replacement: "/usage", RemovedIn: "P21.0"},
		{Name: "settings", Reason: "configuration alias expired", Replacement: "/config", RemovedIn: "P21.0"},
		{Name: "allowed-tools", Reason: "permissions alias expired", Replacement: "/permissions", RemovedIn: "P21.0"},
		{Name: "bashes", Reason: "tasks alias expired", Replacement: "/tasks", RemovedIn: "P21.0"},
		{Name: "summary", Reason: "conversation summaries are ordinary requests, not a default workflow", Replacement: "ask for a summary normally; /compact only when context compaction is desired", RemovedIn: "P21.2"},
		{Name: "onboarding", Reason: "project initialization is owned by the project-native command", Replacement: "use project-native /init", RemovedIn: "P21.2"},
		{Name: "pr-comments", Reason: "PR-comment integration is not a default workflow", Replacement: "request normally or define/use a qualified configured-plugin command", RemovedIn: "P21.2"},
		{Name: "issue", Reason: "issue integration is not a default workflow", Replacement: "request normally or define/use a qualified configured-plugin command", RemovedIn: "P21.2"},
		{Name: "commit-push-pr", Aliases: []string{"cpr"}, Reason: "push and PR creation remain ordinary tool actions", Replacement: "use /commit, then explicitly request push/PR creation under ordinary tools/permissions, or define a qualified configured workflow", RemovedIn: "P21.2"},
	}
	for _, removed := range removedDefaults {
		r.mustRegisterRemoved(removed)
	}
}
