package permission

import (
	"fmt"
	"sync"
)

// ModeTransitionError is returned when a mode transition is invalid.
type ModeTransitionError struct {
	From   Mode
	To     Mode
	Reason string
}

func (e *ModeTransitionError) Error() string {
	return fmt.Sprintf("cannot transition from %s to %s: %s", e.From, e.To, e.Reason)
}

// ModeTransitionResult describes what happened during a mode transition.
type ModeTransitionResult struct {
	// PreviousMode is the mode before the transition.
	PreviousMode Mode
	// NewMode is the mode after the transition.
	NewMode Mode
	// SessionDecisionsCleared reports how many session decisions were cleared.
	SessionDecisionsCleared int
	// RateLimitsCleared indicates whether rate limits were reset.
	RateLimitsCleared bool
	// DenialTrackingReset indicates whether denial tracking was reset.
	DenialTrackingReset bool
}

// TransitionValidation holds validation rules for mode transitions.
// Mirrors reference behavior where bypass mode requires explicit confirmation
// and certain transitions clear state.
type TransitionValidation struct {
	// RequiresConfirmation is true if this transition needs explicit user consent.
	RequiresConfirmation bool
	// ClearsWriteDecisions is true if write-permission decisions should be cleared.
	ClearsWriteDecisions bool
	// ClearsAllDecisions is true if all session decisions should be cleared.
	ClearsAllDecisions bool
	// ResetsRateLimits is true if denial rate limits should be cleared.
	ResetsRateLimits bool
	// ResetsDenialTracking is true if denial counters should be zeroed.
	ResetsDenialTracking bool
}

// ValidateTransition checks whether a mode transition is valid and returns
// the required cleanup actions. Returns an error if the transition is not allowed.
//
// Transition rules (mirrors reference behavior):
//   - Any mode → bypass: requires explicit user confirmation
//   - Any mode → plan: clears all write-allow session decisions
//   - Bypass → any other: clears all session decisions (reset to safe state)
//   - Any mode → dontAsk: clears rate limits (allows fresh prompting cycle)
//   - Same mode → same mode: no-op (always valid)
func ValidateTransition(from, to Mode) (TransitionValidation, error) {
	// Same mode is always valid (no-op)
	if from == to {
		return TransitionValidation{}, nil
	}

	// Validate target mode is a user-addressable mode
	if !isValidTargetMode(to) {
		return TransitionValidation{}, &ModeTransitionError{
			From:   from,
			To:     to,
			Reason: fmt.Sprintf("%q is not a valid user-addressable mode", to),
		}
	}

	validation := TransitionValidation{
		// Mode changes always reset rate limits to allow fresh evaluation
		ResetsRateLimits:     true,
		ResetsDenialTracking: true,
	}

	switch {
	case to == ModeBypassPermissions:
		// Transitioning to bypass requires explicit confirmation
		validation.RequiresConfirmation = true

	case to == ModePlan:
		// Transitioning to plan clears write-allow decisions
		// (plan mode doesn't allow writes, so prior write-allows are irrelevant)
		validation.ClearsWriteDecisions = true

	case from == ModeBypassPermissions:
		// Leaving bypass clears all decisions (prevents bypass-era decisions from
		// persisting into a more restrictive mode)
		validation.ClearsAllDecisions = true

	case to == ModeDontAsk:
		// DontAsk mode denies all "ask" decisions — clear rate limits so the
		// denial tracking starts fresh
		validation.ResetsRateLimits = true
	}

	return validation, nil
}

// isValidTargetMode checks if a mode is a valid target for user-initiated transitions.
// Internal modes (auto, bubble) cannot be targets of manual transitions.
func isValidTargetMode(m Mode) bool {
	switch m {
	case ModeDefault, ModePlan, ModeAcceptEdits, ModeBypassPermissions, ModeDontAsk:
		return true
	default:
		return false
	}
}

// ModeManager manages mode transitions with proper state cleanup.
// It wraps the Evaluator's mode and coordinates cleanup of related subsystems.
// Thread-safe for concurrent access.
type ModeManager struct {
	mu sync.Mutex

	// evaluator is the unified evaluator whose mode we manage.
	evaluator *Evaluator
	// confirmBypass is a callback that asks the user to confirm bypass mode.
	// Returns true if confirmed, false if rejected.
	// If nil, bypass transitions are always rejected.
	confirmBypass func() bool
}

// NewModeManager creates a mode manager for the given evaluator.
// confirmBypass is called when transitioning to bypass mode and must return true
// for the transition to proceed. Pass nil to reject all bypass transitions.
func NewModeManager(evaluator *Evaluator, confirmBypass func() bool) *ModeManager {
	return &ModeManager{
		evaluator:     evaluator,
		confirmBypass: confirmBypass,
	}
}

// TransitionTo validates and executes a mode transition.
// Returns the transition result on success, or an error if the transition is invalid
// or was rejected by the user.
//
// State cleanup performed on transition:
//   - Rate limits are always cleared (fresh evaluation cycle)
//   - Denial tracking is always reset (prevents cross-mode state leakage)
//   - Plan mode: write-allow session decisions are cleared
//   - Leaving bypass: all session decisions are cleared
//   - Bypass mode: requires confirmBypass() to return true
func (m *ModeManager) TransitionTo(to Mode) (ModeTransitionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	from := m.evaluator.GetMode()
	result := ModeTransitionResult{
		PreviousMode: from,
		NewMode:      to,
	}

	// Same mode is a no-op
	if from == to {
		return result, nil
	}

	// Validate the transition
	validation, err := ValidateTransition(from, to)
	if err != nil {
		return result, err
	}

	// Handle bypass confirmation
	if validation.RequiresConfirmation {
		if m.confirmBypass == nil || !m.confirmBypass() {
			return result, &ModeTransitionError{
				From:   from,
				To:     to,
				Reason: "user did not confirm bypass mode transition",
			}
		}
	}

	// Execute state cleanup
	if validation.ClearsAllDecisions {
		if m.evaluator.SessionStore != nil {
			decisions := m.evaluator.SessionStore.List()
			result.SessionDecisionsCleared = len(decisions)
			m.evaluator.SessionStore.Clear("")
		}
	} else if validation.ClearsWriteDecisions {
		result.SessionDecisionsCleared = m.clearWriteDecisions()
	}

	if validation.ResetsRateLimits {
		if m.evaluator.DenialTracking != nil {
			m.evaluator.DenialTracking.ClearAllRateLimits()
			result.RateLimitsCleared = true
		}
	}

	if validation.ResetsDenialTracking {
		if m.evaluator.DenialTracking != nil {
			m.evaluator.DenialTracking.Reset()
			result.DenialTrackingReset = true
		}
	}

	// Apply the mode change
	m.evaluator.SetMode(to)
	result.NewMode = to

	return result, nil
}

// CurrentMode returns the current mode.
func (m *ModeManager) CurrentMode() Mode {
	return m.evaluator.GetMode()
}

// clearWriteDecisions removes session decisions that allow write operations.
// Returns the number of decisions cleared.
func (m *ModeManager) clearWriteDecisions() int {
	if m.evaluator.SessionStore == nil {
		return 0
	}

	decisions := m.evaluator.SessionStore.List()
	cleared := 0
	for _, d := range decisions {
		if d.Action == ActionAllow && isWriteTool(d.ToolName) {
			m.evaluator.SessionStore.Remove(d.ToolName, d.InputPattern, d.Scope)
			cleared++
		}
	}
	return cleared
}

// isWriteTool returns true if the tool modifies files/state.
func isWriteTool(toolName string) bool {
	switch toolName {
	case "Write", "Edit", "Bash":
		return true
	default:
		return false
	}
}
