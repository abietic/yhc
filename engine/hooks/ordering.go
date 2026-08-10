package hooks

import (
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Lifecycle Ordering Enforcement
//
// This module enforces correct hook execution ordering:
//   - Pre-hooks fire before the operation, post-hooks fire after.
//   - If multiple hooks match, they fire in registration order.
//   - Nested scopes: outer pre -> inner pre -> operation -> inner post -> outer post.
//   - Violations are detected and reported (not silently ignored).
//
// The ordering enforcer tracks the sequence of hook events and detects
// out-of-order execution, which would indicate a bug in the hook dispatch
// logic rather than user error.
// ---------------------------------------------------------------------------

// HookPhase represents the phase of a hook relative to its operation.
type HookPhase string

const (
	// HookPhasePre indicates a hook that fires before an operation.
	HookPhasePre HookPhase = "pre"
	// HookPhasePost indicates a hook that fires after an operation.
	HookPhasePost HookPhase = "post"
)

// OrderedHookEvent records a hook execution event for ordering validation.
type OrderedHookEvent struct {
	// Phase is pre or post.
	Phase HookPhase
	// Scope identifies the operation (e.g., "tool:Bash", "query", "agent:review").
	Scope string
	// HookIndex is the registration order index of this hook.
	HookIndex int
	// Timestamp is when the hook fired.
	Timestamp time.Time
}

// OrderingViolation describes a detected hook ordering violation.
type OrderingViolation struct {
	// Description explains what went wrong.
	Description string
	// Expected is what should have happened.
	Expected string
	// Actual is what happened instead.
	Actual string
	// Scope is the operation scope where the violation occurred.
	Scope string
	// Timestamp is when the violation was detected.
	Timestamp time.Time
}

func (v *OrderingViolation) String() string {
	return fmt.Sprintf("[hook ordering violation] scope=%s: %s (expected: %s, actual: %s)",
		v.Scope, v.Description, v.Expected, v.Actual)
}

// LifecycleOrderEnforcer tracks hook execution order and detects violations.
// It is safe for concurrent use.
type LifecycleOrderEnforcer struct {
	mu sync.Mutex

	// events is the ordered history of hook events (bounded).
	events []OrderedHookEvent
	// maxEvents limits the event history size (prevents unbounded growth).
	maxEvents int

	// activeScopes tracks scopes that have had pre-hooks fired but not yet
	// their corresponding post-hooks. Key is scope name.
	activeScopes map[string]*scopeState

	// violations collects detected ordering violations.
	violations []OrderingViolation

	// onViolation is called when a violation is detected (optional callback).
	onViolation func(v *OrderingViolation)
}

// scopeState tracks the state of a single scope for ordering validation.
type scopeState struct {
	// scope is the scope identifier.
	scope string
	// preCount is the number of pre-hooks that have fired.
	preCount int
	// postCount is the number of post-hooks that have fired.
	postCount int
	// lastPreIndex is the registration index of the last pre-hook that fired.
	lastPreIndex int
	// lastPostIndex is the registration index of the last post-hook that fired.
	lastPostIndex int
	// startTime is when the first pre-hook fired.
	startTime time.Time
	// parentScope is the enclosing scope (for nesting validation).
	parentScope string
}

// NewLifecycleOrderEnforcer creates a new ordering enforcer.
func NewLifecycleOrderEnforcer() *LifecycleOrderEnforcer {
	return &LifecycleOrderEnforcer{
		maxEvents:    1000,
		activeScopes: make(map[string]*scopeState),
	}
}

// SetViolationHandler sets a callback that fires when ordering violations
// are detected. Useful for logging or alerting.
func (e *LifecycleOrderEnforcer) SetViolationHandler(handler func(v *OrderingViolation)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onViolation = handler
}

// RecordPre records a pre-hook execution event. Returns a violation if
// the pre-hook fires out of order.
func (e *LifecycleOrderEnforcer) RecordPre(scope string, hookIndex int) *OrderingViolation {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	event := OrderedHookEvent{
		Phase:     HookPhasePre,
		Scope:     scope,
		HookIndex: hookIndex,
		Timestamp: now,
	}
	e.appendEvent(event)

	state, exists := e.activeScopes[scope]
	if !exists {
		// First pre-hook for this scope: create state.
		e.activeScopes[scope] = &scopeState{
			scope:        scope,
			preCount:     1,
			lastPreIndex: hookIndex,
			startTime:    now,
		}
		return nil
	}

	// Scope already active: validate ordering.
	if state.postCount > 0 {
		// Post-hooks have already fired: pre-hook after post is a violation.
		v := &OrderingViolation{
			Description: "pre-hook fired after post-hooks have already started",
			Expected:    "all pre-hooks before any post-hooks",
			Actual:      fmt.Sprintf("pre-hook index %d fired after %d post-hooks", hookIndex, state.postCount),
			Scope:       scope,
			Timestamp:   now,
		}
		e.recordViolation(v)
		return v
	}

	// Validate registration order.
	if hookIndex < state.lastPreIndex {
		v := &OrderingViolation{
			Description: "pre-hook fired out of registration order",
			Expected:    fmt.Sprintf("hook index >= %d", state.lastPreIndex),
			Actual:      fmt.Sprintf("hook index %d", hookIndex),
			Scope:       scope,
			Timestamp:   now,
		}
		e.recordViolation(v)
		return v
	}

	state.preCount++
	state.lastPreIndex = hookIndex
	return nil
}

// RecordPost records a post-hook execution event. Returns a violation if
// the post-hook fires out of order.
func (e *LifecycleOrderEnforcer) RecordPost(scope string, hookIndex int) *OrderingViolation {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	event := OrderedHookEvent{
		Phase:     HookPhasePost,
		Scope:     scope,
		HookIndex: hookIndex,
		Timestamp: now,
	}
	e.appendEvent(event)

	state, exists := e.activeScopes[scope]
	if !exists {
		// Post-hook without any pre-hook: violation.
		v := &OrderingViolation{
			Description: "post-hook fired without preceding pre-hook",
			Expected:    "at least one pre-hook before post-hooks",
			Actual:      fmt.Sprintf("post-hook index %d with no active scope", hookIndex),
			Scope:       scope,
			Timestamp:   now,
		}
		e.recordViolation(v)
		return v
	}

	// Validate registration order for post-hooks.
	if state.postCount > 0 && hookIndex < state.lastPostIndex {
		v := &OrderingViolation{
			Description: "post-hook fired out of registration order",
			Expected:    fmt.Sprintf("hook index >= %d", state.lastPostIndex),
			Actual:      fmt.Sprintf("hook index %d", hookIndex),
			Scope:       scope,
			Timestamp:   now,
		}
		e.recordViolation(v)
		return v
	}

	state.postCount++
	state.lastPostIndex = hookIndex
	return nil
}

// BeginScope marks the start of a nested scope. The nested scope must
// complete (all post-hooks fire) before the parent scope's post-hooks.
func (e *LifecycleOrderEnforcer) BeginScope(scope, parentScope string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	state, exists := e.activeScopes[scope]
	if !exists {
		e.activeScopes[scope] = &scopeState{
			scope:       scope,
			startTime:   time.Now(),
			parentScope: parentScope,
		}
	} else {
		state.parentScope = parentScope
	}
}

// EndScope marks the end of a scope. Validates that the scope completed
// properly (had at least one pre-hook and post-hooks were all fired).
func (e *LifecycleOrderEnforcer) EndScope(scope string) *OrderingViolation {
	e.mu.Lock()
	defer e.mu.Unlock()

	state, exists := e.activeScopes[scope]
	if !exists {
		// Ending a scope that was never started is a warning, not an error.
		return nil
	}

	// Check if post-hooks were fired.
	if state.preCount > 0 && state.postCount == 0 {
		v := &OrderingViolation{
			Description: "scope ended without any post-hooks firing",
			Expected:    "post-hooks after pre-hooks",
			Actual:      fmt.Sprintf("%d pre-hooks fired, 0 post-hooks", state.preCount),
			Scope:       scope,
			Timestamp:   time.Now(),
		}
		e.recordViolation(v)
		delete(e.activeScopes, scope)
		return v
	}

	delete(e.activeScopes, scope)
	return nil
}

// ValidateNesting checks that an inner scope completed before the outer
// scope's post-hooks fire. Call this before firing outer post-hooks.
func (e *LifecycleOrderEnforcer) ValidateNesting(outerScope, innerScope string) *OrderingViolation {
	e.mu.Lock()
	defer e.mu.Unlock()

	innerState, innerExists := e.activeScopes[innerScope]
	if !innerExists {
		// Inner scope already ended: nesting is valid.
		return nil
	}

	// Inner scope still active when outer is about to post: violation.
	if innerState.preCount > 0 && innerState.postCount < innerState.preCount {
		v := &OrderingViolation{
			Description: "inner scope not completed before outer scope post-hooks",
			Expected:    fmt.Sprintf("inner scope %q completed before outer scope %q posts", innerScope, outerScope),
			Actual:      fmt.Sprintf("inner scope has %d pre, %d post", innerState.preCount, innerState.postCount),
			Scope:       outerScope,
			Timestamp:   time.Now(),
		}
		e.recordViolation(v)
		return v
	}

	return nil
}

// GetViolations returns all detected violations.
func (e *LifecycleOrderEnforcer) GetViolations() []OrderingViolation {
	e.mu.Lock()
	defer e.mu.Unlock()

	result := make([]OrderingViolation, len(e.violations))
	copy(result, e.violations)
	return result
}

// HasViolations returns true if any ordering violations have been detected.
func (e *LifecycleOrderEnforcer) HasViolations() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.violations) > 0
}

// ViolationCount returns the number of detected violations.
func (e *LifecycleOrderEnforcer) ViolationCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.violations)
}

// ActiveScopes returns the names of currently active (unclosed) scopes.
func (e *LifecycleOrderEnforcer) ActiveScopes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	var scopes []string
	for name := range e.activeScopes {
		scopes = append(scopes, name)
	}
	return scopes
}

// Reset clears all state (events, scopes, violations).
func (e *LifecycleOrderEnforcer) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.events = nil
	e.activeScopes = make(map[string]*scopeState)
	e.violations = nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (e *LifecycleOrderEnforcer) appendEvent(event OrderedHookEvent) {
	e.events = append(e.events, event)
	// Trim to max size.
	if len(e.events) > e.maxEvents {
		e.events = e.events[len(e.events)-e.maxEvents:]
	}
}

func (e *LifecycleOrderEnforcer) recordViolation(v *OrderingViolation) {
	e.violations = append(e.violations, *v)
	if e.onViolation != nil {
		e.onViolation(v)
	}
}

// ---------------------------------------------------------------------------
// Integration with Executor
// ---------------------------------------------------------------------------

// WithOrderEnforcer attaches a lifecycle order enforcer to the executor.
// When set, the executor validates hook ordering during execution.
func (e *Executor) WithOrderEnforcer(enforcer *LifecycleOrderEnforcer) {
	e.orderEnforcer = enforcer
}

// GetOrderEnforcer returns the attached ordering enforcer, or nil if none.
func (e *Executor) GetOrderEnforcer() *LifecycleOrderEnforcer {
	return e.orderEnforcer
}
