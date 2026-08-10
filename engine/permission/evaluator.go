package permission

import (
	"context"
	"fmt"
	"sync"
)

// EvaluationSource identifies which subsystem produced the decision.
type EvaluationSource string

const (
	// SourceSessionStore means a persisted session decision produced the result.
	SourceEvalSessionStore EvaluationSource = "session_store"
	// SourceEvalRule means a rule from the rules engine produced the result.
	SourceEvalRule EvaluationSource = "rule"
	// SourceEvalMode means the mode's default behavior produced the result.
	SourceEvalMode EvaluationSource = "mode"
	// SourceEvalRateLimit means the denial was rate-limited (auto-denied without prompt).
	SourceEvalRateLimit EvaluationSource = "rate_limit"
	// SourceEvalClassification means the tool risk classifier produced the result.
	SourceEvalClassification EvaluationSource = "classification"
	// SourceEvalAcceptEdits means acceptEdits mode auto-allowed the operation.
	SourceEvalAcceptEdits EvaluationSource = "accept_edits"
)

// EvaluationResult is the outcome of the unified permission evaluation pipeline.
// It composes all subsystems (session store, rules, precedence, mode, classifier)
// into a single decision.
type EvaluationResult struct {
	// Decision is the final permission action: allow, deny, or ask.
	Decision PermissionAction
	// Reason explains why this decision was made.
	Reason string
	// Source identifies which subsystem produced the decision.
	Source EvaluationSource
	// MatchedRule is the specific rule that matched, if any.
	MatchedRule *PermissionRule
	// RiskLevel is the tool's risk classification (only set when classifier contributed).
	RiskLevel ToolRiskLevel
}

// Evaluator is the unified permission evaluation engine. It composes:
//   - SessionStore: persisted allow-always/deny-always decisions
//   - RulesEngine: rule matching with precedence
//   - Mode: permission mode semantics (default, plan, bypass, etc.)
//   - DenialTrackingState: rate limiting for repeated denials
//   - ToolRiskClassifier: tool operation risk classification
//
// The evaluation pipeline is:
//  1. Check denial rate limiting → auto-deny if rate-limited
//  2. Check session store → use persisted decision if found
//  3. Evaluate rules with precedence → use winning rule if matched
//  4. Apply mode semantics → determine default behavior for unmatched
//  5. Apply acceptEdits mode special logic if applicable
//  6. Apply risk classification as a fallback signal when no rule matched
//
// This is the "one function to call" that combines all pieces.
// Mirrors the combined logic of hasPermissionsToUseToolInner in permissions.ts.
type Evaluator struct {
	mu sync.RWMutex

	// SessionStore holds persisted allow-always/deny-always decisions.
	SessionStore *SessionStore
	// Rules is the rule evaluation engine with all loaded rules.
	Rules *RulesEngine
	// Mode is the current permission mode.
	Mode Mode
	// DenialTracking manages rate limiting for repeated denials.
	DenialTracking *DenialTrackingState
	// Classifier categorizes tools by risk level.
	Classifier *ToolRiskClassifier
	// WorkingDir is the current working directory (needed for acceptEdits checks).
	WorkingDir string
}

// EvaluatorConfig holds configuration for creating an Evaluator.
type EvaluatorConfig struct {
	// SessionStore is optional; if nil, session decisions are skipped.
	SessionStore *SessionStore
	// Rules are the permission rules to load into the engine.
	Rules []PermissionRule
	// Mode is the initial permission mode.
	Mode Mode
	// WorkingDir is the current working directory.
	WorkingDir string
}

// NewEvaluator creates a unified permission evaluator from the given config.
func NewEvaluator(cfg EvaluatorConfig) *Evaluator {
	e := &Evaluator{
		SessionStore:   cfg.SessionStore,
		Rules:          NewRulesEngine(cfg.Rules),
		Mode:           cfg.Mode,
		DenialTracking: NewDenialTrackingState(),
		Classifier:     NewToolRiskClassifier(),
		WorkingDir:     cfg.WorkingDir,
	}
	if e.SessionStore == nil {
		e.SessionStore = NewSessionStore()
	}
	if e.Mode == "" {
		e.Mode = ModeDefault
	}
	return e
}

// Evaluate runs the full permission evaluation pipeline for a tool invocation.
// This is the single entry point that composes all subsystems.
//
// Pipeline order:
//  1. Rate limit check (suppress repeated prompts)
//  2. Session store lookup (persisted decisions)
//  3. Rule evaluation with precedence
//  4. Mode application (transforms unmatched or ask decisions)
//  5. AcceptEdits special handling
//  6. Risk classification fallback
func (e *Evaluator) Evaluate(_ context.Context, toolName string, toolInput map[string]any) EvaluationResult {
	e.mu.RLock()
	mode := e.Mode
	cwd := e.WorkingDir
	e.mu.RUnlock()

	// Step 1: Rate limit check.
	// If the same tool+input was denied recently, auto-deny without re-prompting.
	if e.DenialTracking != nil && e.DenialTracking.IsRateLimited(toolName, toolInput) {
		return EvaluationResult{
			Decision: ActionDeny,
			Reason:   "rate-limited: same operation was recently denied",
			Source:   SourceEvalRateLimit,
		}
	}

	// Step 2: Session store lookup.
	// Persisted decisions (allow-always, deny-always) take priority over rules.
	if e.SessionStore != nil {
		if decision, found := e.SessionStore.Lookup(toolName, toolInput); found {
			return EvaluationResult{
				Decision: decision.Action,
				Reason:   fmt.Sprintf("persisted %s decision from session store", decision.Action),
				Source:   SourceEvalSessionStore,
			}
		}
	}

	// Step 3: Rule evaluation with specificity-aware precedence.
	// The resolution order is:
	//   - Any deny match → deny (deny is absolute)
	//   - Among ask and allow matches: the most specific rule wins
	//   - If equally specific: ask > allow (conservative tiebreaker)
	var ruleAction PermissionAction
	var matchedRule *PermissionRule
	var ruleMatched bool

	if e.Rules != nil {
		ruleAction, matchedRule, ruleMatched = e.resolveWithSpecificity(toolName, toolInput)
	}

	if ruleMatched {
		// Step 4a: Apply mode transformation on matched rule action.
		finalAction := EvaluateWithMode(mode, ruleAction, true)
		reason := fmt.Sprintf("rule matched: %s", formatRuleDescription(matchedRule))
		if finalAction != ruleAction {
			reason += fmt.Sprintf(" (transformed %s→%s by mode %s)", ruleAction, finalAction, mode)
		}
		return EvaluationResult{
			Decision:    finalAction,
			Reason:      reason,
			Source:      SourceEvalRule,
			MatchedRule: matchedRule,
		}
	}

	// Step 4b: No rule matched — apply mode default behavior.
	// Before applying mode default, check acceptEdits special logic.
	if mode == ModeAcceptEdits {
		if AcceptEditsCheck(toolName, toolInput, cwd) {
			return EvaluationResult{
				Decision: ActionAllow,
				Reason:   "auto-allowed by acceptEdits mode (file operation within working directory)",
				Source:   SourceEvalAcceptEdits,
			}
		}
	}

	// Step 5: Apply mode default for unmatched tools.
	modeDefault := mode.DefaultBehavior()

	// Step 6: If mode says "ask", use risk classification to refine the decision.
	// Read-only tools can be auto-allowed even when the mode defaults to "ask".
	if modeDefault == ActionAsk && e.Classifier != nil {
		classification := e.Classifier.Classify(toolName, toolInput)
		if classification.Level == RiskReadOnly {
			return EvaluationResult{
				Decision:  ActionAllow,
				Reason:    fmt.Sprintf("auto-allowed: %s classified as read-only (%s)", toolName, classification.Reason),
				Source:    SourceEvalClassification,
				RiskLevel: RiskReadOnly,
			}
		}
		// For destructive operations, always ask regardless of mode
		if classification.Level == RiskDestructive {
			return EvaluationResult{
				Decision:  ActionAsk,
				Reason:    fmt.Sprintf("requires confirmation: %s classified as destructive (%s)", toolName, classification.Reason),
				Source:    SourceEvalClassification,
				RiskLevel: RiskDestructive,
			}
		}
		// Write-level risk: use mode default (which is "ask" here)
		return EvaluationResult{
			Decision:  modeDefault,
			Reason:    fmt.Sprintf("mode %s default (%s classified as write-level risk: %s)", mode, toolName, classification.Reason),
			Source:    SourceEvalMode,
			RiskLevel: RiskWrite,
		}
	}

	return EvaluationResult{
		Decision: modeDefault,
		Reason:   fmt.Sprintf("no rule matched; mode %s default = %s", mode, modeDefault),
		Source:   SourceEvalMode,
	}
}

// RecordDenial records a denial event for rate limiting purposes.
func (e *Evaluator) RecordDenial(toolName string, input map[string]any, reason string) {
	if e.DenialTracking != nil {
		e.DenialTracking.RecordDenialWithDetails(toolName, input, ReasonRule, reason)
	}
}

// RecordSuccess records a successful permission check (resets consecutive denials).
func (e *Evaluator) RecordSuccess() {
	if e.DenialTracking != nil {
		e.DenialTracking.RecordSuccess()
	}
}

// PersistDecision saves an allow-always or deny-always decision to the session store.
func (e *Evaluator) PersistDecision(toolName, inputPattern string, action PermissionAction, scope DecisionScope, reason string) {
	if e.SessionStore == nil {
		return
	}
	e.SessionStore.Add(SessionDecision{
		ToolName:     toolName,
		InputPattern: inputPattern,
		Action:       action,
		Scope:        scope,
		Reason:       reason,
	})
}

// GetMode returns the current permission mode.
func (e *Evaluator) GetMode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Mode
}

// SetMode changes the permission mode. Use TransitionMode for validated transitions
// with proper state cleanup.
func (e *Evaluator) SetMode(mode Mode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Mode = mode
}

// formatRuleDescription formats a matched rule for display in reason strings.
func formatRuleDescription(rule *PermissionRule) string {
	if rule == nil {
		return "<nil>"
	}
	desc := rule.ToolName
	if rule.InputPattern != "" {
		desc += "(" + rule.InputPattern + ")"
	}
	desc += ":" + string(rule.Action)
	if rule.Source != "" {
		desc += " [" + rule.Source + "]"
	}
	return desc
}

// resolveWithSpecificity performs rule matching with specificity-aware resolution.
// The semantics implement true specificity-first resolution:
//   - The MOST SPECIFIC matching rule wins, regardless of its action type
//   - When two rules are equally specific, deny > ask > allow (conservative tiebreaker)
//   - This means a specific "allow" rule beats a broad "deny" rule
//
// This is the correct behavior for the unified evaluator where a project-specific
// allow rule like "Bash(rm /tmp/test*):allow" should override a broader
// "Bash(rm*):deny" rule, because the former is more specific.
//
// Mirrors the reference behavior where content-level rules (with input patterns)
// override tool-level rules (without patterns), and more specific patterns
// override less specific ones.
func (e *Evaluator) resolveWithSpecificity(toolName string, toolInput map[string]any) (PermissionAction, *PermissionRule, bool) {
	if e.Rules == nil {
		return ActionAsk, nil, false
	}

	matches := e.Rules.collectMatches(toolName, toolInput)
	if len(matches) == 0 {
		return ActionAsk, nil, false
	}

	// Find the most specific match. When equally specific, use action priority
	// (deny > ask > allow) as tiebreaker.
	best := matches[0]
	for _, m := range matches[1:] {
		if isMoreSpecific(m, best) {
			best = m
		} else if !isMoreSpecific(best, m) {
			// Equal specificity — use action priority as tiebreaker
			if actionPriority(m.Rule.Action) > actionPriority(best.Rule.Action) {
				best = m
			}
		}
	}

	return best.Rule.Action, &best.Rule, true
}

// actionPriority returns the priority weight for tiebreaking equally-specific rules.
// Higher value = wins the tiebreak. deny > ask > allow.
func actionPriority(a PermissionAction) int {
	switch a {
	case ActionDeny:
		return 3
	case ActionAsk:
		return 2
	case ActionAllow:
		return 1
	default:
		return 0
	}
}
