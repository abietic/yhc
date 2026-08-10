package permission

// Mode represents the active permission checking mode.
// Mirrors src/types/permissions.ts:16-38.
type Mode string

const (
	// ModeDefault is the normal mode — asks user for permission on tool uses
	// not covered by allow rules.
	ModeDefault Mode = "default"

	// ModePlan is plan-only mode — the model can only think/plan;
	// tool use is denied or prompted.
	ModePlan Mode = "plan"

	// ModeAcceptEdits auto-allows contained Write/Edit operations
	// but still prompts for other tools.
	ModeAcceptEdits Mode = "acceptEdits"

	// ModeBypassPermissions skips permission checks entirely, except deny rules,
	// ask rules, safety checks, and tools requiring user interaction.
	ModeBypassPermissions Mode = "bypassPermissions"

	// ModeDontAsk converts all "ask" decisions to "deny" — never prompts user,
	// auto-denies anything not covered by allow rules.
	ModeDontAsk Mode = "dontAsk"

	// ModeAuto (internal) uses an AI classifier to decide whether tool use is safe.
	// Falls back to prompting on denial limits.
	ModeAuto Mode = "auto"

	// ModeBubble (internal) surfaces permission prompts to the parent terminal
	// rather than auto-denying. Used for subagents.
	ModeBubble Mode = "bubble"
)

// IsBypassMode returns true if the mode skips permission checks.
func (m Mode) IsBypassMode() bool {
	return m == ModeBypassPermissions
}

// IsDenialTrackingEnabled returns true if denial tracking should record
// denials and successes for this mode. Only the auto mode uses denial
// tracking to implement fallback-to-prompting thresholds.
// Mirrors permissions.ts:879-901.
func (m Mode) IsDenialTrackingEnabled() bool {
	return m == ModeAuto
}

// ShouldAutoAllow returns true if the mode should skip the user-provided
// CanUseTool check and auto-allow the tool use.
func (m Mode) ShouldAutoAllow() bool {
	return m == ModeBypassPermissions
}

// DefaultBehavior returns the default permission action for tool invocations
// that are not covered by any matching rule in this mode.
// This implements the mode semantics from the reference:
//   - default: ask for dangerous operations (ActionAsk)
//   - plan: deny tool execution (ActionDeny) — read-only mode
//   - acceptEdits: allow file edits, ask for other tools (context-dependent, returns ActionAsk as base)
//   - bypassPermissions: allow everything (ActionAllow)
//   - dontAsk: deny anything not explicitly allowed (ActionDeny)
//   - auto: defer to classifier (ActionAsk as placeholder for classifier input)
//   - bubble: ask (delegates to parent)
//
// Mirrors permissions.ts hasPermissionsToUseToolInner steps 2a and final transformation.
func (m Mode) DefaultBehavior() PermissionAction {
	switch m {
	case ModeBypassPermissions:
		return ActionAllow
	case ModePlan:
		return ActionDeny
	case ModeDontAsk:
		return ActionDeny
	case ModeAcceptEdits:
		// acceptEdits allows file operations but asks for others;
		// the caller must check tool type. Return ask as conservative default.
		return ActionAsk
	case ModeAuto:
		// Auto mode defers to classifier; ask is the placeholder that triggers it.
		return ActionAsk
	case ModeBubble:
		return ActionAsk
	default:
		// ModeDefault and any unknown mode: ask the user.
		return ActionAsk
	}
}

// AllowsToolByDefault returns true if a tool invocation should be auto-allowed
// based purely on mode semantics (before considering rules).
// This is a strict subset — it only returns true for bypassPermissions.
// acceptEdits requires tool-type awareness that the caller must provide.
func (m Mode) AllowsToolByDefault() bool {
	return m == ModeBypassPermissions
}

// DeniesToolByDefault returns true if the mode denies unmatched tool invocations
// without prompting the user. Used to implement "fail closed" semantics.
func (m Mode) DeniesToolByDefault() bool {
	return m == ModePlan || m == ModeDontAsk
}

// EvaluateWithMode applies mode semantics on top of a rule-based action.
// This implements the interaction between rules and modes:
//   - Rules always take precedence over mode defaults
//   - If no rule matched (ruleMatched=false), the mode's DefaultBehavior applies
//   - If a rule matched, its action is respected regardless of mode
//   - Exception: in dontAsk mode, "ask" is converted to "deny"
//
// Mirrors permissions.ts post-processing:
// - dontAsk converts ask→deny (line 508-515)
// - bypassPermissions returns allow for unmatched (line 1268-1281)
// - plan mode deny semantics
func EvaluateWithMode(mode Mode, ruleAction PermissionAction, ruleMatched bool) PermissionAction {
	if !ruleMatched {
		// No rule matched — use mode's default behavior
		return mode.DefaultBehavior()
	}

	// Rule matched — respect the rule action, with mode-specific transformations
	switch {
	case mode == ModeDontAsk && ruleAction == ActionAsk:
		// dontAsk converts all "ask" to "deny" — never prompts user
		return ActionDeny
	case mode == ModeBypassPermissions && ruleAction == ActionAsk:
		// bypassPermissions auto-allows "ask" decisions (but respects deny)
		return ActionAllow
	default:
		// All other cases: respect the rule action directly
		return ruleAction
	}
}

// ModeTitle returns a human-readable title for the mode.
// Mirrors PermissionMode.ts permissionModeTitle.
func (m Mode) ModeTitle() string {
	switch m {
	case ModeDefault:
		return "Default"
	case ModePlan:
		return "Plan Mode"
	case ModeAcceptEdits:
		return "Accept Edits"
	case ModeBypassPermissions:
		return "Bypass Permissions"
	case ModeDontAsk:
		return "Don't Ask"
	case ModeAuto:
		return "Auto Mode"
	case ModeBubble:
		return "Bubble"
	default:
		return "Default"
	}
}

// ParseMode converts a string to a Mode, returning ModeDefault for unknown values.
// Mirrors PermissionMode.ts permissionModeFromString.
func ParseMode(s string) Mode {
	switch Mode(s) {
	case ModeDefault, ModePlan, ModeAcceptEdits, ModeBypassPermissions, ModeDontAsk, ModeAuto, ModeBubble:
		return Mode(s)
	default:
		return ModeDefault
	}
}

// ValidModes returns all valid external permission modes (user-addressable).
// Internal modes like "auto" and "bubble" are excluded.
// Mirrors EXTERNAL_PERMISSION_MODES from the reference.
func ValidModes() []Mode {
	return []Mode{
		ModeDefault,
		ModePlan,
		ModeAcceptEdits,
		ModeBypassPermissions,
		ModeDontAsk,
	}
}
