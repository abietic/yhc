package hooks

import (
	"sync"
	"time"
)

// SessionHookFn is an in-memory function hook (vs shell/HTTP hooks from config).
// Used by plugins and session-scoped hooks that don't need disk persistence.
//
// Reference: src/utils/hooks/sessionHooks.ts (447 lines)
type SessionHookFn func(event SessionHookEvent) error

// SessionHookEvent represents a hook event with its payload.
type SessionHookEvent struct {
	Type    string
	Payload map[string]interface{}
}

// SessionHookRegistry manages per-session in-memory function hooks.
// Separate from the file-based hook config manager.
type SessionHookRegistry struct {
	mu    sync.RWMutex
	hooks map[string][]SessionHookEntry
}

// SessionHookEntry is a registered session hook with metadata.
type SessionHookEntry struct {
	ID       string
	Source   string
	Fn       SessionHookFn
	Once     bool
	executed bool
}

// NewSessionHookRegistry creates a new empty registry.
func NewSessionHookRegistry() *SessionHookRegistry {
	return &SessionHookRegistry{
		hooks: make(map[string][]SessionHookEntry),
	}
}

// Register adds a session-scoped hook for the given event type.
func (r *SessionHookRegistry) Register(eventType string, entry SessionHookEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks[eventType] = append(r.hooks[eventType], entry)
}

// Execute runs all registered hooks for the given event type.
// Once-hooks are removed after execution.
func (r *SessionHookRegistry) Execute(event SessionHookEvent) []error {
	r.mu.Lock()
	entries := make([]SessionHookEntry, len(r.hooks[event.Type]))
	copy(entries, r.hooks[event.Type])
	r.mu.Unlock()

	var errs []error
	var executedOnce []string

	for _, entry := range entries {
		if entry.Once && entry.executed {
			continue
		}
		if err := entry.Fn(event); err != nil {
			errs = append(errs, err)
		}
		if entry.Once {
			executedOnce = append(executedOnce, entry.ID)
		}
	}

	if len(executedOnce) > 0 {
		r.mu.Lock()
		for _, id := range executedOnce {
			hooks := r.hooks[event.Type]
			for i := range hooks {
				if hooks[i].ID == id {
					hooks[i].executed = true
				}
			}
		}
		r.mu.Unlock()
	}

	return errs
}

// Clear removes all registered hooks.
func (r *SessionHookRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = make(map[string][]SessionHookEntry)
}

// HookEventBroadcaster emits started/progress/response events for hook
// execution monitoring in the UI.
//
// Reference: src/utils/hooks/hookEvents.ts (192 lines)
type HookEventBroadcaster struct {
	mu        sync.RWMutex
	listeners []func(HookExecutionEvent)
}

// HookExecutionEvent represents a hook lifecycle event.
type HookExecutionEvent struct {
	Phase     string // "started", "progress", "response", "error"
	HookID    string
	EventType string
	Timestamp time.Time
	Data      map[string]interface{}
}

// NewHookEventBroadcaster creates a broadcaster.
func NewHookEventBroadcaster() *HookEventBroadcaster {
	return &HookEventBroadcaster{}
}

// Subscribe registers a listener for hook execution events.
func (b *HookEventBroadcaster) Subscribe(cb func(HookExecutionEvent)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners = append(b.listeners, cb)
}

// Emit broadcasts a hook execution event to all listeners.
func (b *HookEventBroadcaster) Emit(event HookExecutionEvent) {
	b.mu.RLock()
	listeners := make([]func(HookExecutionEvent), len(b.listeners))
	copy(listeners, b.listeners)
	b.mu.RUnlock()

	for _, cb := range listeners {
		cb(event)
	}
}

// HooksConfigSnapshot creates a stable snapshot of hook configuration
// during execution to prevent config changes from affecting in-flight hooks.
//
// Reference: src/utils/hooks/hooksConfigSnapshot.ts (133 lines)
type HooksConfigSnapshot struct {
	mu       sync.Mutex
	snapshot map[string]interface{}
	frozen   bool
}

// NewHooksConfigSnapshot creates a new snapshot from current config.
func NewHooksConfigSnapshot(config map[string]interface{}) *HooksConfigSnapshot {
	snap := make(map[string]interface{}, len(config))
	for k, v := range config {
		snap[k] = v
	}
	return &HooksConfigSnapshot{
		snapshot: snap,
		frozen:   true,
	}
}

// Get returns a config value from the frozen snapshot.
func (s *HooksConfigSnapshot) Get(key string) (interface{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.snapshot[key]
	return v, ok
}
