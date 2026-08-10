package bridge

import (
	"sync"
)

// BridgeManager provides lifecycle management for the complete bridge system:
// StateStore + BridgeAdapter + Client registry. It is the single entry point
// for wiring the bridge to an engine instance.
//
// Usage:
//
//	mgr := NewBridgeManager(BridgeManagerConfig{
//	    Model:          "claude-sonnet-4-20250514",
//	    SessionID:      "sess-123",
//	    CWD:            "/path/to/project",
//	    PermissionMode: "default",
//	})
//	mgr.Start()
//	defer mgr.Stop()
//
//	// Feed engine events:
//	mgr.HandleEvent(bridge.EventFromStreamStart())
//
//	// Subscribe external clients:
//	client := mgr.RegisterClient(RegisterClientOpts{...})
type BridgeManager struct {
	store   *StateStore
	adapter *BridgeAdapter
	config  BridgeManagerConfig

	mu      sync.Mutex
	started bool
	stopped bool
}

// BridgeManagerConfig holds configuration for the BridgeManager.
type BridgeManagerConfig struct {
	// Model is the initial model name.
	Model string

	// FallbackModel is the initial fallback model name.
	FallbackModel string

	// SessionID is the current session identifier.
	SessionID string

	// CWD is the working directory.
	CWD string

	// PermissionMode is the initial permission mode.
	PermissionMode string

	// StoreOptions are optional configuration for the underlying StateStore.
	StoreOptions []StoreOption
}

// NewBridgeManager creates a fully wired BridgeManager with a StateStore and
// BridgeAdapter ready to accept events. Call Start() to begin accepting events.
func NewBridgeManager(config BridgeManagerConfig) *BridgeManager {
	store := NewStateStore(config.StoreOptions...)
	adapter := NewBridgeAdapter(store)

	return &BridgeManager{
		store:   store,
		adapter: adapter,
		config:  config,
	}
}

// Start initializes state and begins accepting events.
// It is safe to call multiple times (no-op after first call).
func (m *BridgeManager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.stopped {
		return
	}

	// Initialize state with known values.
	m.adapter.InitializeState(InitialState{
		Model:          m.config.Model,
		FallbackModel:  m.config.FallbackModel,
		SessionID:      m.config.SessionID,
		CWD:            m.config.CWD,
		PermissionMode: m.config.PermissionMode,
	})

	m.adapter.Start()
	m.started = true
}

// Stop shuts down the adapter and cleans up resources.
// After Stop(), no more events will be processed.
// It is safe to call multiple times (no-op after first call).
func (m *BridgeManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}

	m.adapter.Stop()
	m.stopped = true
}

// IsRunning returns whether the manager is currently active.
func (m *BridgeManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started && !m.stopped
}

// HandleEvent forwards an engine event to the adapter for state translation.
// This is the primary method called by the engine's event loop.
func (m *BridgeManager) HandleEvent(event EngineEvent) {
	m.adapter.HandleEvent(event)
}

// Store returns the underlying StateStore for direct access (e.g., subscribing
// observers, taking snapshots, registering clients).
func (m *BridgeManager) Store() *StateStore {
	return m.store
}

// Adapter returns the underlying BridgeAdapter.
func (m *BridgeManager) Adapter() *BridgeAdapter {
	return m.adapter
}

// RegisterClient registers a client with the StateStore's client registry.
// This is a convenience method that delegates to the store.
func (m *BridgeManager) RegisterClient(opts RegisterClientOpts) *Client {
	return m.store.RegisterClient(opts)
}

// UnregisterClient removes a client from the registry.
func (m *BridgeManager) UnregisterClient(id string) {
	m.store.UnregisterClient(id)
}

// Snapshot returns a point-in-time copy of the current state.
func (m *BridgeManager) Snapshot() *StateSnapshot {
	return m.store.Snapshot()
}

// Subscribe creates a new observer for the given topics.
func (m *BridgeManager) Subscribe(topics ...Topic) *Observer {
	return m.store.Subscribe(topics...)
}
