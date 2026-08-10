package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"
)

// RegistryHook is called during tool lifecycle events (register/unregister).
type RegistryHook func(name string, impl ToolImpl)

// ToolMetadata tracks runtime statistics for a registered tool.
type ToolMetadata struct {
	Name            string
	RegisteredAt    time.Time
	CallCount       int64
	TotalDurationNs int64 // total execution time in nanoseconds
	LastError       string
	LastErrorAt     time.Time
	Enabled         bool
}

// ToolOrigin identifies who supplied a registered tool implementation.
// Registration metadata is host-owned capability evidence; it is never user
// authority by itself.
type ToolOrigin string

const (
	ToolOriginUnknown ToolOrigin = ""
	ToolOriginBuiltin ToolOrigin = "builtin"
	ToolOriginMCP     ToolOrigin = "mcp"
	ToolOriginApp     ToolOrigin = "app"
	ToolOriginDynamic ToolOrigin = "dynamic"
)

// ToolActionKind is the host-owned coarse action family used to build one
// canonical permission descriptor. It deliberately does not infer effects
// from model-visible names or descriptions.
type ToolActionKind string

const (
	ToolActionUnknown         ToolActionKind = ""
	ToolActionRead            ToolActionKind = "read"
	ToolActionWrite           ToolActionKind = "write"
	ToolActionRuntimeState    ToolActionKind = "runtime_state"
	ToolActionProcessLocal    ToolActionKind = "process_local"
	ToolActionShell           ToolActionKind = "shell"
	ToolActionNetwork         ToolActionKind = "network"
	ToolActionChild           ToolActionKind = "child"
	ToolActionDynamic         ToolActionKind = "dynamic"
	ToolActionUserInteraction ToolActionKind = "user_interaction"
	ToolActionPlanTransition  ToolActionKind = "plan_transition"
)

// ToolCapabilities contains the effect facts declared by the host for a tool.
// Declared=false means the capability set is incomplete and permission policy
// must fail closed unless an exact user authority covers the invocation.
type ToolCapabilities struct {
	Declared                bool
	Origin                  ToolOrigin
	ActionKind              ToolActionKind
	Network                 bool
	Child                   bool
	Dynamic                 bool
	RequiresUserInteraction bool
	ShellComplete           bool
}

// ToolResolution is one atomic registry snapshot for a requested name.
// CanonicalName resolves aliases while Registered and Enabled remain distinct
// so a disabled tool cannot be mistaken for an unknown tool.
type ToolResolution struct {
	Implementation ToolImpl
	RequestedName  string
	CanonicalName  string
	Registered     bool
	Enabled        bool
	Generation     uint64
}

// ToolExecutionLease binds one already-authorized action to a registry
// generation. Registry mutations block until Execute or Cancel consumes the
// lease, closing the gap between the final generation check and dispatch.
type ToolExecutionLease struct {
	implementation ToolImpl
	requestedName  string
	release        func()
	once           sync.Once
}

// Execute consumes the lease at the dispatch linearization point, then invokes
// the captured implementation. Mutations after this point affect later
// actions, not the action already dispatched through this lease.
func (l *ToolExecutionLease) Execute(
	ctx context.Context,
	input string,
) (string, error) {
	if l == nil {
		return "", fmt.Errorf("tool execution lease is nil")
	}
	l.Cancel()
	if l.implementation.ExecuteCtx != nil {
		return l.implementation.ExecuteCtx(ctx, input)
	}
	if l.implementation.Execute == nil {
		return "tool has no execute function: " + l.requestedName, nil
	}
	return l.implementation.Execute(input)
}

// Cancel releases an unconsumed lease without dispatching the tool.
func (l *ToolExecutionLease) Cancel() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
}

// AverageDuration returns the average execution duration for this tool.
func (m *ToolMetadata) AverageDuration() time.Duration {
	count := atomic.LoadInt64(&m.CallCount)
	if count == 0 {
		return 0
	}
	total := atomic.LoadInt64(&m.TotalDurationNs)
	return time.Duration(total / count)
}

// Registry maps tool names to their implementations and preserves registration order.
type Registry struct {
	mu                sync.RWMutex
	tools             map[string]ToolImpl
	order             []string
	disabled          map[string]bool
	metadata          map[string]*ToolMetadata
	onRegisterHooks   []RegistryHook
	onUnregisterHooks []RegistryHook
	generation        uint64
}

// ToolImpl pairs a tool's schema with its execution function and runtime metadata.
type ToolImpl struct {
	Info              *schema.ToolInfo
	Execute           func(input string) (string, error)
	ExecuteCtx        func(ctx context.Context, input string) (string, error) // optional, takes precedence when set
	IsConcurrencySafe func(input map[string]any) bool
	SkipResultBudget  bool     // if true, results from this tool are never truncated
	Aliases           []string // alternative names this tool can be looked up by
	Capabilities      ToolCapabilities
	// RegistrationOwner is an opaque process-local identity used to replace
	// one dynamic tool generation without deleting a newer or unrelated row.
	// It is never projected to the model or persisted.
	RegistrationOwner string

	// Contract fields mirroring Tool.ts behavioral metadata.
	IsReadOnly               bool                             // safe in plan mode; does not modify state
	IsPlanModeTransition     bool                             // changes plan mode and may pass the plan-mode write guard
	NeedsPermissions         bool                             // requires permission check before execution
	DefaultPermissionAllowed bool                             // skips interactive permission by default after explicit rules
	IsHidden                 bool                             // not exposed to the model in tool list
	RequiresQueryEngine      bool                             // declaration only; execution must be intercepted by QueryEngine
	IsDestructive            bool                             // destructive action (rm, drop, force-push)
	ValidateInput            func(input map[string]any) error // pre-execution input validation
	// InterruptBehavior is "cancel" or "block" (default "block").
	// "cancel" tools are aborted on user interrupt; "block" tools complete naturally.
	// Mirrors Tool.ts interruptBehavior().
	InterruptBehavior string
	// Prompt returns tool-specific system prompt content injected into model context.
	// Reference: each tool directory has prompt.ts that contributes to the system prompt.
	Prompt func() string
}

var (
	// ErrRegistryGenerationChanged reports that a compare-and-replace batch
	// lost its capability-generation race.
	ErrRegistryGenerationChanged = errors.New("tool registry generation changed")
	// ErrRegistryNameCollision reports that an atomic batch would overwrite an
	// existing canonical name or alias.
	ErrRegistryNameCollision = errors.New("tool registry name collision")
)

const unavailableWorktreeToolReason = "top-level session worktree switching is unavailable; use Agent with isolation=\"worktree\""

// UnavailableBuiltinToolReason reports whether name is permanently reserved by
// a built-in compatibility boundary and cannot be registered or executed.
func UnavailableBuiltinToolReason(name string) (string, bool) {
	switch name {
	case "EnterWorktree", "ExitWorktree":
		return unavailableWorktreeToolReason, true
	default:
		return "", false
	}
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:    make(map[string]ToolImpl),
		disabled: make(map[string]bool),
		metadata: make(map[string]*ToolMetadata),
	}
}

// OnRegister adds a hook that fires after a tool is registered.
func (r *Registry) OnRegister(hook RegistryHook) {
	if r == nil || hook == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onRegisterHooks = append(r.onRegisterHooks, hook)
}

// OnUnregister adds a hook that fires after a tool is unregistered.
func (r *Registry) OnUnregister(hook RegistryHook) {
	if r == nil || hook == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onUnregisterHooks = append(r.onUnregisterHooks, hook)
}

// Register adds a tool implementation to the registry.
func (r *Registry) Register(impl ToolImpl) {
	if r == nil || impl.Info == nil || impl.Info.Name == "" {
		return
	}
	if _, unavailable := UnavailableBuiltinToolReason(impl.Info.Name); unavailable {
		return
	}
	for _, alias := range impl.Aliases {
		if _, unavailable := UnavailableBuiltinToolReason(alias); unavailable {
			return
		}
	}
	r.mu.Lock()
	name := impl.Info.Name
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = impl
	// Register aliases pointing to the same tool.
	for _, alias := range impl.Aliases {
		if alias != "" && alias != name {
			r.tools[alias] = impl
		}
	}
	// Initialize metadata for this tool.
	if _, exists := r.metadata[name]; !exists {
		r.metadata[name] = &ToolMetadata{
			Name:         name,
			RegisteredAt: time.Now(),
			Enabled:      true,
		}
	}
	r.generation++
	// Copy hooks before releasing the lock.
	hooks := make([]RegistryHook, len(r.onRegisterHooks))
	copy(hooks, r.onRegisterHooks)
	r.mu.Unlock()

	// Fire register hooks outside the lock to avoid deadlocks.
	for _, hook := range hooks {
		hook(name, impl)
	}
}

// Unregister removes a tool from the registry by name.
func (r *Registry) Unregister(name string) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	impl, ok := r.tools[name]
	if !ok {
		r.mu.Unlock()
		return
	}
	canonicalName := name
	if impl.Info != nil && impl.Info.Name != "" {
		canonicalName = impl.Info.Name
	}
	delete(r.tools, canonicalName)
	delete(r.disabled, canonicalName)
	delete(r.metadata, canonicalName)
	// Remove aliases.
	for _, alias := range impl.Aliases {
		if alias != "" && alias != canonicalName {
			delete(r.tools, alias)
		}
	}
	// Remove from order slice.
	for i, n := range r.order {
		if n == canonicalName {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	r.generation++
	// Copy hooks before releasing the lock.
	hooks := make([]RegistryHook, len(r.onUnregisterHooks))
	copy(hooks, r.onUnregisterHooks)
	r.mu.Unlock()

	// Fire unregister hooks outside the lock.
	for _, hook := range hooks {
		hook(canonicalName, impl)
	}
}

// Enable re-enables a previously disabled tool.
func (r *Registry) Enable(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	impl, ok := r.tools[name]
	if !ok {
		return
	}
	canonicalName := name
	if impl.Info != nil && impl.Info.Name != "" {
		canonicalName = impl.Info.Name
	}
	if !r.disabled[canonicalName] {
		return
	}
	delete(r.disabled, canonicalName)
	if meta, ok := r.metadata[canonicalName]; ok {
		meta.Enabled = true
	}
	r.generation++
}

// Disable temporarily disables a tool without removing it from the registry.
// Disabled tools are not returned by Get or List.
func (r *Registry) Disable(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	impl, exists := r.tools[name]
	if !exists {
		return
	}
	canonicalName := name
	if impl.Info != nil && impl.Info.Name != "" {
		canonicalName = impl.Info.Name
	}
	if r.disabled[canonicalName] {
		return
	}
	r.disabled[canonicalName] = true
	if meta, ok := r.metadata[canonicalName]; ok {
		meta.Enabled = false
	}
	r.generation++
}

// IsEnabled returns whether a tool is currently enabled.
func (r *Registry) IsEnabled(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	impl, exists := r.tools[name]
	if !exists {
		return false
	}
	canonicalName := name
	if impl.Info != nil && impl.Info.Name != "" {
		canonicalName = impl.Info.Name
	}
	return !r.disabled[canonicalName]
}

// GetMetadata returns runtime metadata for a tool, or nil if not found.
func (r *Registry) GetMetadata(name string) *ToolMetadata {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if impl, ok := r.tools[name]; ok && impl.Info != nil && impl.Info.Name != "" {
		name = impl.Info.Name
	}
	return r.metadata[name]
}

// RecordCall records a tool execution for metadata tracking.
func (r *Registry) RecordCall(name string, duration time.Duration, err error) {
	if r == nil {
		return
	}
	r.mu.RLock()
	meta, ok := r.metadata[name]
	r.mu.RUnlock()
	if !ok {
		return
	}
	atomic.AddInt64(&meta.CallCount, 1)
	atomic.AddInt64(&meta.TotalDurationNs, int64(duration))
	if err != nil {
		r.mu.Lock()
		meta.LastError = err.Error()
		meta.LastErrorAt = time.Now()
		r.mu.Unlock()
	}
}

// Get returns a tool implementation by name. Returns false for disabled tools.
func (r *Registry) Get(name string) (ToolImpl, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return ToolImpl{}, false
	}
	canonicalName := name
	if t.Info != nil && t.Info.Name != "" {
		canonicalName = t.Info.Name
	}
	if r.disabled[canonicalName] {
		return ToolImpl{}, false
	}
	return t, true
}

// GetIncludeDisabled returns a tool implementation by name regardless of its enabled state.
func (r *Registry) GetIncludeDisabled(name string) (ToolImpl, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Resolve returns an atomic availability, canonical-identity, implementation,
// and capability-generation snapshot for name.
func (r *Registry) Resolve(name string) ToolResolution {
	resolution := ToolResolution{RequestedName: name}
	if r == nil {
		return resolution
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	resolution.Generation = r.generation
	impl, ok := r.tools[name]
	if !ok {
		return resolution
	}
	resolution.Implementation = impl
	resolution.Registered = true
	resolution.CanonicalName = name
	if impl.Info != nil && impl.Info.Name != "" {
		resolution.CanonicalName = impl.Info.Name
	}
	resolution.Enabled = !r.disabled[resolution.CanonicalName]
	return resolution
}

// AcquireExecution verifies the exact canonical identity and capability
// generation while retaining the registry read lock until the returned lease
// is executed or cancelled.
func (r *Registry) AcquireExecution(
	requestedName string,
	canonicalName string,
	generation uint64,
) (*ToolExecutionLease, error) {
	if r == nil {
		return nil, fmt.Errorf("tool registry is nil")
	}
	r.mu.RLock()
	if r.generation != generation {
		r.mu.RUnlock()
		return nil, fmt.Errorf(
			"tool registry generation changed: got %d, want %d",
			r.generation,
			generation,
		)
	}
	implementation, registered := r.tools[requestedName]
	if !registered || implementation.Info == nil {
		r.mu.RUnlock()
		return nil, fmt.Errorf("tool %q is not registered", requestedName)
	}
	actualCanonicalName := implementation.Info.Name
	if actualCanonicalName == "" ||
		actualCanonicalName != canonicalName {
		r.mu.RUnlock()
		return nil, fmt.Errorf(
			"tool %q canonical identity changed",
			requestedName,
		)
	}
	if r.disabled[actualCanonicalName] {
		r.mu.RUnlock()
		return nil, fmt.Errorf("tool %q is disabled", requestedName)
	}
	return &ToolExecutionLease{
		implementation: implementation,
		requestedName:  requestedName,
		release:        r.mu.RUnlock,
	}, nil
}

// Generation reports the current registry capability generation.
func (r *Registry) Generation() uint64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.generation
}

type registryHookEvent struct {
	name string
	impl ToolImpl
}

// ReplaceOwnedTools atomically removes every row owned by removeOwners and
// installs additions if the registry still has expectedGeneration. Canonical
// names and aliases are collision-checked against the post-removal view before
// any mutation. Dynamic owners use this as the publication boundary for one
// complete capability generation.
func (r *Registry) ReplaceOwnedTools(
	expectedGeneration uint64,
	removeOwners []string,
	additions []ToolImpl,
) (uint64, error) {
	generation, dispatchHooks, err := r.replaceOwnedToolsDeferred(
		expectedGeneration,
		removeOwners,
		additions,
	)
	if dispatchHooks != nil {
		dispatchHooks()
	}
	return generation, err
}

// replaceOwnedToolsDeferred performs the same atomic commit as
// ReplaceOwnedTools but returns synchronous hook delivery to its caller.
// Lifecycle owners use it to release their own locks before a hook can
// re-enter them. Callers must invoke a non-nil dispatch exactly once.
func (r *Registry) replaceOwnedToolsDeferred(
	expectedGeneration uint64,
	removeOwners []string,
	additions []ToolImpl,
) (uint64, func(), error) {
	if r == nil {
		return 0, nil, errors.New("tool registry is nil")
	}
	removedOwners := make(map[string]struct{}, len(removeOwners))
	for _, owner := range removeOwners {
		if owner != "" {
			removedOwners[owner] = struct{}{}
		}
	}

	r.mu.Lock()
	if r.generation != expectedGeneration {
		actual := r.generation
		r.mu.Unlock()
		return actual, nil, fmt.Errorf(
			"%w: got %d, want %d",
			ErrRegistryGenerationChanged,
			actual,
			expectedGeneration,
		)
	}

	candidateNames := make(map[string]struct{})
	for _, impl := range additions {
		if impl.Info == nil ||
			impl.Info.Name == "" ||
			impl.RegistrationOwner == "" {
			r.mu.Unlock()
			return expectedGeneration, nil, errors.New(
				"owned tool registration requires name and owner",
			)
		}
		names := append([]string{impl.Info.Name}, impl.Aliases...)
		for _, name := range names {
			if name == "" {
				continue
			}
			if _, unavailable := UnavailableBuiltinToolReason(name); unavailable {
				r.mu.Unlock()
				return expectedGeneration, nil, fmt.Errorf(
					"%w: reserved name %q",
					ErrRegistryNameCollision,
					name,
				)
			}
			if _, duplicate := candidateNames[name]; duplicate {
				r.mu.Unlock()
				return expectedGeneration, nil, fmt.Errorf(
					"%w: duplicate candidate %q",
					ErrRegistryNameCollision,
					name,
				)
			}
			candidateNames[name] = struct{}{}
			if current, exists := r.tools[name]; exists {
				if _, replaced := removedOwners[current.RegistrationOwner]; !replaced {
					r.mu.Unlock()
					return expectedGeneration, nil, fmt.Errorf(
						"%w: %q",
						ErrRegistryNameCollision,
						name,
					)
				}
			}
		}
	}

	var unregistered []registryHookEvent
	if len(removedOwners) > 0 {
		seen := make(map[string]struct{})
		for _, name := range r.order {
			impl, exists := r.tools[name]
			if !exists {
				continue
			}
			if _, remove := removedOwners[impl.RegistrationOwner]; !remove {
				continue
			}
			canonical := name
			if impl.Info != nil && impl.Info.Name != "" {
				canonical = impl.Info.Name
			}
			if _, duplicate := seen[canonical]; duplicate {
				continue
			}
			seen[canonical] = struct{}{}
			unregistered = append(unregistered, registryHookEvent{
				name: canonical,
				impl: impl,
			})
		}
		for name, impl := range r.tools {
			if _, remove := removedOwners[impl.RegistrationOwner]; !remove {
				continue
			}
			canonical := name
			if impl.Info != nil && impl.Info.Name != "" {
				canonical = impl.Info.Name
			}
			if _, duplicate := seen[canonical]; duplicate {
				continue
			}
			seen[canonical] = struct{}{}
			unregistered = append(unregistered, registryHookEvent{
				name: canonical,
				impl: impl,
			})
		}
		sort.Slice(unregistered, func(i, j int) bool {
			return unregistered[i].name < unregistered[j].name
		})
	}

	if len(unregistered) == 0 && len(additions) == 0 {
		generation := r.generation
		r.mu.Unlock()
		return generation, nil, nil
	}

	for name, impl := range r.tools {
		if _, remove := removedOwners[impl.RegistrationOwner]; remove {
			delete(r.tools, name)
		}
	}
	keptOrder := r.order[:0]
	for _, name := range r.order {
		impl, exists := r.tools[name]
		if !exists {
			delete(r.disabled, name)
			delete(r.metadata, name)
			continue
		}
		keptOrder = append(keptOrder, name)
		if impl.Info != nil && impl.Info.Name != name {
			delete(r.disabled, name)
			delete(r.metadata, name)
		}
	}
	r.order = keptOrder

	registered := make([]registryHookEvent, 0, len(additions))
	for _, impl := range additions {
		name := impl.Info.Name
		r.order = append(r.order, name)
		r.tools[name] = impl
		for _, alias := range impl.Aliases {
			if alias != "" && alias != name {
				r.tools[alias] = impl
			}
		}
		delete(r.disabled, name)
		r.metadata[name] = &ToolMetadata{
			Name:         name,
			RegisteredAt: time.Now(),
			Enabled:      true,
		}
		registered = append(registered, registryHookEvent{
			name: name,
			impl: impl,
		})
	}
	r.generation++
	generation := r.generation
	unregisterHooks := append([]RegistryHook(nil), r.onUnregisterHooks...)
	registerHooks := append([]RegistryHook(nil), r.onRegisterHooks...)
	r.mu.Unlock()

	dispatchHooks := func() {
		for _, event := range unregistered {
			for _, hook := range unregisterHooks {
				hook(event.name, event.impl)
			}
		}
		for _, event := range registered {
			for _, hook := range registerHooks {
				hook(event.name, event.impl)
			}
		}
	}
	return generation, dispatchHooks, nil
}

// RemoveOwnedTools removes exactly the currently registered rows belonging to
// owners. It intentionally does not require an expected generation: connection
// loss must remove stale dynamic capabilities even when an unrelated registry
// mutation won a race.
func (r *Registry) RemoveOwnedTools(owners ...string) uint64 {
	generation, dispatchHooks := r.removeOwnedToolsDeferred(owners...)
	if dispatchHooks != nil {
		dispatchHooks()
	}
	return generation
}

// removeOwnedToolsDeferred performs exact-owner removal and returns
// synchronous unregister-hook delivery to its caller. Callers must invoke a
// non-nil dispatch exactly once after releasing their own lifecycle locks.
func (r *Registry) removeOwnedToolsDeferred(owners ...string) (uint64, func()) {
	if r == nil {
		return 0, nil
	}
	ownerSet := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		if owner != "" {
			ownerSet[owner] = struct{}{}
		}
	}
	if len(ownerSet) == 0 {
		return r.Generation(), nil
	}

	r.mu.Lock()
	var removed []registryHookEvent
	seen := make(map[string]struct{})
	for name, impl := range r.tools {
		if _, owned := ownerSet[impl.RegistrationOwner]; !owned {
			continue
		}
		canonical := name
		if impl.Info != nil && impl.Info.Name != "" {
			canonical = impl.Info.Name
		}
		if _, duplicate := seen[canonical]; !duplicate {
			seen[canonical] = struct{}{}
			removed = append(removed, registryHookEvent{
				name: canonical,
				impl: impl,
			})
		}
		delete(r.tools, name)
	}
	if len(removed) == 0 {
		generation := r.generation
		r.mu.Unlock()
		return generation, nil
	}
	keptOrder := r.order[:0]
	for _, name := range r.order {
		if _, removedName := seen[name]; removedName {
			delete(r.disabled, name)
			delete(r.metadata, name)
			continue
		}
		keptOrder = append(keptOrder, name)
	}
	r.order = keptOrder
	r.generation++
	generation := r.generation
	hooks := append([]RegistryHook(nil), r.onUnregisterHooks...)
	r.mu.Unlock()

	sort.Slice(removed, func(i, j int) bool {
		return removed[i].name < removed[j].name
	})
	dispatchHooks := func() {
		for _, event := range removed {
			for _, hook := range hooks {
				hook(event.name, event.impl)
			}
		}
	}
	return generation, dispatchHooks
}

// Update replaces the implementation of an already-registered tool.
// Does nothing if the tool is not registered.
func (r *Registry) Update(name string, impl ToolImpl) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.tools[name]
	if !exists || current.Info == nil || impl.Info == nil {
		return
	}
	canonicalName := current.Info.Name
	if canonicalName == "" || impl.Info.Name != canonicalName {
		return
	}
	r.tools[canonicalName] = impl
	for _, alias := range current.Aliases {
		if alias != "" && alias != canonicalName {
			delete(r.tools, alias)
		}
	}
	for _, alias := range impl.Aliases {
		if alias != "" && alias != canonicalName {
			r.tools[alias] = impl
		}
	}
	r.generation++
}

// RegisterDefaults registers all built-in tools on the registry.
func RegisterDefaults(r *Registry) {
	for _, impl := range []ToolImpl{
		AgentTool(),
		TaskOutputTool(),
		withPrompt(BashTool(), bashToolPrompt),
		withPrompt(GrepTool(), grepToolPrompt),
		GlobTool(),
		withPrompt(ReadTool(), readToolPrompt),
		withPrompt(EditTool(), editToolPrompt),
		withPrompt(WriteTool(), writeToolPromptSection),
		WebFetchTool(),
		WebSearchTool(),
		AskUserQuestionTool(),
		TaskStopTool(),
		TaskCreateTool(),
		TaskGetTool(),
		TaskUpdateTool(),
		TaskListTool(),
		TaskTool(),
		EnterPlanModeTool(),
		ExitPlanModeTool(),
		MonitorTool(),
		SleepTool(),
		ToolSearchTool(),
		SkillTool(),
		SendMessageTool(),
		TodoWriteTool(),
		NotebookEditTool(),
		ScheduleCronTool(),
		ScheduleWakeupTool(),
		BashOutputTool(),
		KillShellTool(),
		TeamCreateTool(),
		TeamDeleteTool(),
		LSPTool(),
		MCPTool(),
		ListMcpResourcesTool(),
		ReadMcpResourceTool(),
		BriefTool(),
		ConfigTool(),
		McpAuthTool(),
		GetGoalTool(),
		UpdateGoalTool(),
	} {
		r.Register(impl)
	}
	applyToolContracts(r)
	DefaultRegistry = r
}

// GetToolPrompts returns all non-empty tool prompt sections for system context.
func (r *Registry) GetToolPrompts() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]string)
	for name, impl := range r.tools {
		if impl.Prompt != nil {
			if p := impl.Prompt(); p != "" {
				result[name] = p
			}
		}
	}
	return result
}

// readOnlyTools lists tools that only read state and are safe in plan mode.
var readOnlyTools = map[string]bool{
	"Grep":                 true,
	"Glob":                 true,
	"Read":                 true,
	"WebFetch":             true,
	"WebSearch":            true,
	"AskUserQuestion":      true,
	"TaskGet":              true,
	"TaskList":             true,
	"TaskOutput":           true,
	"ToolSearch":           true,
	"Monitor":              true,
	"Sleep":                true,
	"Skill":                true,
	"Brief":                true,
	GetGoalToolName:        true,
	"LSP":                  true,
	"EnterPlanMode":        true,
	"ScheduleWakeup":       true,
	"ListMcpResourcesTool": true,
	"ReadMcpResourceTool":  true,
}

var planModeTransitionTools = map[string]bool{
	"EnterPlanMode": true,
	"ExitPlanMode":  true,
}

// defaultPermissionAllowedTools mutate trusted host-owned internal state and
// do not require an interactive prompt. Explicit deny and ask rules still win.
var defaultPermissionAllowedTools = map[string]bool{
	"TodoWrite":        true,
	UpdateGoalToolName: true,
}

// permissionRequiredTools lists tools that require permission checks.
var permissionRequiredTools = map[string]bool{
	"Bash":                 true,
	"BashOutput":           true,
	"KillShell":            true,
	"Edit":                 true,
	"Write":                true,
	"NotebookEdit":         true,
	"mcp_tool":             true,
	"McpAuth":              true,
	"ListMcpResourcesTool": true,
	"ReadMcpResourceTool":  true,
	"ScheduleCron":         true,
	"Agent":                true,
	"Task":                 true,
	"TaskCreate":           true,
	"TaskUpdate":           true,
	"TaskStop":             true,
	"TeamCreate":           true,
	"TeamDelete":           true,
	"SendMessage":          true,
	"Config":               true,
	"ExitPlanMode":         true,
}

// destructiveTools lists tools that perform irreversible actions.
var destructiveTools = map[string]bool{
	"Bash":      true,
	"KillShell": true,
	"Write":     true,
}

var builtInToolCapabilities = map[string]ToolCapabilities{
	"Agent":                {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionChild, Child: true},
	"Task":                 {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionChild, Child: true},
	"TaskOutput":           {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"Bash":                 {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionShell, ShellComplete: false},
	"BashOutput":           {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionShell, ShellComplete: false},
	"KillShell":            {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionShell, ShellComplete: false},
	"Grep":                 {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"Glob":                 {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"Read":                 {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"Edit":                 {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionWrite},
	"Write":                {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionWrite},
	"NotebookEdit":         {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionWrite},
	"WebFetch":             {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionNetwork, Network: true},
	"WebSearch":            {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionNetwork, Network: true},
	"AskUserQuestion":      {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionUserInteraction, RequiresUserInteraction: true},
	"TaskStop":             {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRuntimeState},
	"TaskCreate":           {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRuntimeState},
	"TaskGet":              {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"TaskUpdate":           {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRuntimeState},
	"TaskList":             {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"EnterPlanMode":        {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionPlanTransition},
	"ExitPlanMode":         {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionPlanTransition, RequiresUserInteraction: true},
	"Monitor":              {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"Sleep":                {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"ToolSearch":           {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"Skill":                {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	"SendMessage":          {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionChild, Child: true},
	"TodoWrite":            {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRuntimeState},
	"ScheduleCron":         {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRuntimeState},
	"ScheduleWakeup":       {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRuntimeState},
	"TeamCreate":           {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionChild, Child: true},
	"TeamDelete":           {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionChild, Child: true},
	"LSP":                  {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionDynamic, Dynamic: true},
	"mcp_tool":             {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionDynamic, Network: true, Dynamic: true},
	"ListMcpResourcesTool": {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionDynamic, Network: true, Dynamic: true},
	"ReadMcpResourceTool":  {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionDynamic, Network: true, Dynamic: true},
	"Brief":                {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	GetGoalToolName:        {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRead},
	UpdateGoalToolName:     {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionRuntimeState},
	"Config":               {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionWrite},
	"McpAuth":              {Declared: true, Origin: ToolOriginBuiltin, ActionKind: ToolActionDynamic, Network: true, Dynamic: true, RequiresUserInteraction: true},
}

// applyToolContracts sets behavioral metadata on all registered tools based on
// the classification maps above.
func applyToolContracts(r *Registry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.order {
		impl, ok := r.tools[name]
		if !ok {
			continue
		}
		if readOnlyTools[name] {
			impl.IsReadOnly = true
		}
		if planModeTransitionTools[name] {
			impl.IsPlanModeTransition = true
		}
		if defaultPermissionAllowedTools[name] {
			impl.DefaultPermissionAllowed = true
		}
		if permissionRequiredTools[name] {
			impl.NeedsPermissions = true
		}
		if destructiveTools[name] {
			impl.IsDestructive = true
		}
		if capabilities, declared := builtInToolCapabilities[name]; declared {
			impl.Capabilities = capabilities
		}
		r.tools[name] = impl
	}
	r.generation++
}

// ToolProgressEvent carries a complete replacement-safe progress snapshot from
// a running tool. Callers must not concatenate successive Content values.
type ToolProgressEvent struct {
	ToolName  string
	ToolUseID string
	Content   string
	IsFinal   bool
}

// progressKeyType is the context key for progress callbacks.
type progressKeyType struct{}

type attachmentKeyType struct{}

type mediaSupportKeyType struct{}

// WithProgressFn returns a context carrying a tool progress callback.
func WithProgressFn(ctx context.Context, fn func(ToolProgressEvent)) context.Context {
	return context.WithValue(ctx, progressKeyType{}, fn)
}

// EmitProgress sends a progress event if a callback is registered on ctx.
func EmitProgress(ctx context.Context, evt ToolProgressEvent) {
	if fn, ok := ctx.Value(progressKeyType{}).(func(ToolProgressEvent)); ok && fn != nil {
		fn(evt)
	}
}

// WithAttachmentFn lets a tool add model-visible messages after its textual
// tool result without widening the ToolExecutor signature.
func WithAttachmentFn(ctx context.Context, fn func(*schema.Message)) context.Context {
	return context.WithValue(ctx, attachmentKeyType{}, fn)
}

// EmitAttachment queues one supplemental tool message when the engine owns an
// attachment collector. It reports whether the message can be delivered.
func EmitAttachment(ctx context.Context, message *schema.Message) bool {
	if message == nil {
		return false
	}
	if fn, ok := ctx.Value(attachmentKeyType{}).(func(*schema.Message)); ok && fn != nil {
		fn(message)
		return true
	}
	return false
}

// WithMediaSupport records whether the active model accepts image inputs.
func WithMediaSupport(ctx context.Context, supported bool) context.Context {
	return context.WithValue(ctx, mediaSupportKeyType{}, supported)
}

// MediaSupported returns the active model's image-input capability.
func MediaSupported(ctx context.Context) bool {
	supported, _ := ctx.Value(mediaSupportKeyType{}).(bool)
	return supported
}

// List returns all registered and enabled tool infos in registration order.
func (r *Registry) List() []*schema.ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]*schema.ToolInfo, 0, len(r.order))
	for _, name := range r.order {
		if r.disabled[name] {
			continue
		}
		if tool, ok := r.tools[name]; ok && tool.Info != nil {
			infos = append(infos, tool.Info)
		}
	}
	if len(infos) == 0 && len(r.tools) > 0 {
		keys := make([]string, 0, len(r.tools))
		for name := range r.tools {
			if !r.disabled[name] {
				keys = append(keys, name)
			}
		}
		sort.Strings(keys)
		for _, name := range keys {
			infos = append(infos, r.tools[name].Info)
		}
	}
	return infos
}

// Names returns the names of all registered (including disabled) tools in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}
