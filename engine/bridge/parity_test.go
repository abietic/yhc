package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// StateStore parity tests
// ---------------------------------------------------------------------------

func TestStateStoreAtomicBatchUpdates(t *testing.T) {
	store := NewStateStore()

	// Batch update — all fields should update atomically
	changes := store.Update(map[StateField]any{
		FieldCurrentModel:   "claude-sonnet-4-6",
		FieldSessionID:      "sess-123",
		FieldInputTokens:    1000,
		FieldOutputTokens:   500,
		FieldPermissionMode: "default",
	})

	if len(changes) != 5 {
		t.Fatalf("expected 5 changes, got %d", len(changes))
	}

	// Verify all values set
	if store.GetString(FieldCurrentModel) != "claude-sonnet-4-6" {
		t.Fatalf("expected 'claude-sonnet-4-6', got %q", store.GetString(FieldCurrentModel))
	}
	if store.GetInt(FieldInputTokens) != 1000 {
		t.Fatalf("expected 1000, got %d", store.GetInt(FieldInputTokens))
	}
}

func TestStateStoreSnapshotImmutability(t *testing.T) {
	store := NewStateStore()
	store.Set(FieldCurrentModel, "model-A")

	snap := store.Snapshot()
	if snap.GetString(FieldCurrentModel) != "model-A" {
		t.Fatal("snapshot should reflect current state")
	}

	// Modify the store after taking the snapshot
	store.Set(FieldCurrentModel, "model-B")

	// Snapshot should still show old value
	if snap.GetString(FieldCurrentModel) != "model-A" {
		t.Fatal("snapshot should be immutable after store changes")
	}

	// Modifying the snapshot map should not affect the store
	snap.Fields[FieldCurrentModel] = "model-C"
	if store.GetString(FieldCurrentModel) != "model-B" {
		t.Fatal("modifying snapshot should not affect the store")
	}
}

func TestStateStoreHistoryBuffer(t *testing.T) {
	store := NewStateStore(WithHistorySize(5))

	// Write more changes than history buffer can hold
	for i := 0; i < 10; i++ {
		store.Set(FieldTurnCount, i)
	}

	// History should only contain the last 5
	history := store.History(0)
	if len(history) != 5 {
		t.Fatalf("expected 5 history entries, got %d", len(history))
	}

	// Oldest entry should be turn 5 (0-indexed: changes 5,6,7,8,9)
	oldest := history[0]
	if oldest.NewValue != 5 {
		t.Fatalf("expected oldest history value 5, got %v", oldest.NewValue)
	}
}

// ---------------------------------------------------------------------------
// Observer parity tests
// ---------------------------------------------------------------------------

func TestObserverTopicFiltering(t *testing.T) {
	store := NewStateStore()

	// Subscribe only to model topic
	modelObs := store.Subscribe(TopicModel)
	defer modelObs.Close()

	// Set a model field (TopicModel) — observer should receive
	store.Set(FieldCurrentModel, "gpt-4o")

	// Set a token field (TopicTokens) — observer should NOT receive
	store.Set(FieldInputTokens, 100)

	// Give a moment for delivery
	time.Sleep(5 * time.Millisecond)

	// Drain the channel
	var received []StateChange
	for {
		select {
		case change := <-modelObs.Changes():
			received = append(received, change)
		default:
			goto done
		}
	}
done:

	if len(received) != 1 {
		t.Fatalf("expected 1 change on model topic, got %d", len(received))
	}
	if received[0].Field != FieldCurrentModel {
		t.Fatalf("expected FieldCurrentModel change, got %q", received[0].Field)
	}
}

func TestObserverNonBlockingDelivery(t *testing.T) {
	// Create store with very small observer buffer
	store := NewStateStore(WithObserverBuffer(2))

	obs := store.Subscribe(TopicAll)
	defer obs.Close()

	// Fire many changes — should not block the store
	for i := 0; i < 100; i++ {
		store.Set(FieldTurnCount, i)
	}

	// Observer should have dropped some
	dropped := obs.Dropped()
	if dropped == 0 {
		t.Fatal("expected some dropped changes with small buffer and many updates")
	}
}

func TestObserverCloseCleanup(t *testing.T) {
	store := NewStateStore()

	obs := store.Subscribe(TopicAll)
	initialCount := store.ObserverCount()
	if initialCount != 1 {
		t.Fatalf("expected 1 observer, got %d", initialCount)
	}

	obs.Close()

	// After close, observer count should be 0
	if store.ObserverCount() != 0 {
		t.Fatalf("expected 0 observers after close, got %d", store.ObserverCount())
	}

	// Close again — should be idempotent (no panic)
	obs.Close()
}

// ---------------------------------------------------------------------------
// Client Registry parity tests
// ---------------------------------------------------------------------------

func TestClientRegistryLifecycle(t *testing.T) {
	store := NewStateStore()

	// Register a client
	client := store.RegisterClient(RegisterClientOpts{
		ID:   "client-1",
		Type: ClientTypeTUI,
	})
	if client == nil {
		t.Fatal("expected non-nil client")
		return
	}
	if client.ID != "client-1" {
		t.Fatalf("expected client ID 'client-1', got %q", client.ID)
	}

	// Verify registered
	clients := store.ListClients()
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}

	// Disconnect
	store.UnregisterClient("client-1")
	clients = store.ListClients()
	if len(clients) != 0 {
		t.Fatalf("expected 0 clients after disconnect, got %d", len(clients))
	}
}

// ---------------------------------------------------------------------------
// Plugin lifecycle parity tests
// ---------------------------------------------------------------------------

func TestPluginManagerLifecycle(t *testing.T) {
	store := NewStateStore()
	pm := NewPluginManager(PluginManagerConfig{
		StartTimeout: 1 * time.Second,
		StopTimeout:  1 * time.Second,
		Store:        store,
	})

	p := &parityTestPlugin{name: "test-plugin", version: "1.0.0"}

	// Register
	if err := pm.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
		return
	}

	// Verify registered state
	info := pm.Registry().Get("test-plugin")
	if info == nil {
		t.Fatal("expected non-nil service info")
		return
	}
	if info.State != ServiceStateRegistered {
		t.Fatalf("expected 'registered' state, got %q", info.State)
	}

	// Start
	if err := pm.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll: %v", err)
		return
	}
	if !p.started {
		t.Fatal("expected plugin to be started")
	}
	info = pm.Registry().Get("test-plugin")
	if info.State != ServiceStateRunning {
		t.Fatalf("expected 'running' state, got %q", info.State)
	}

	// Stop
	if err := pm.StopAll(context.Background()); err != nil {
		t.Fatalf("StopAll: %v", err)
		return
	}
	if !p.stopped {
		t.Fatal("expected plugin to be stopped")
	}
	info = pm.Registry().Get("test-plugin")
	if info.State != ServiceStateStopped {
		t.Fatalf("expected 'stopped' state, got %q", info.State)
	}
}

func TestPluginManagerDuplicateRegister(t *testing.T) {
	pm := NewPluginManager(PluginManagerConfig{})
	p1 := &parityTestPlugin{name: "dup", version: "1.0"}
	p2 := &parityTestPlugin{name: "dup", version: "2.0"}

	if err := pm.Register(p1); err != nil {
		t.Fatalf("first register: %v", err)
		return
	}
	if err := pm.Register(p2); err == nil {
		t.Fatal("expected error for duplicate register")
		return
	}
}

// ---------------------------------------------------------------------------
// Service Registry parity tests
// ---------------------------------------------------------------------------

func TestServiceRegistryRegisterQueryUnregister(t *testing.T) {
	reg := NewServiceRegistry()

	err := reg.Register("svc-1", "1.0", []string{"chat", "tools"})
	if err != nil {
		t.Fatalf("Register: %v", err)
		return
	}

	// Query by name
	info := reg.Get("svc-1")
	if info == nil {
		t.Fatal("expected non-nil service info")
		return
	}
	if info.Version != "1.0" {
		t.Fatalf("expected version '1.0', got %q", info.Version)
	}

	// Query by capability
	results := reg.QueryByCapability("chat")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'chat' capability, got %d", len(results))
	}

	// Non-existent capability
	results = reg.QueryByCapability("nonexistent")
	if len(results) != 0 {
		t.Fatalf("expected 0 results for unknown capability, got %d", len(results))
	}

	// Set state to stopped before unregister
	_ = reg.SetState("svc-1", ServiceStateStopped)

	// Unregister
	if err := reg.Unregister("svc-1"); err != nil {
		t.Fatalf("Unregister: %v", err)
		return
	}

	// Verify gone
	if reg.Has("svc-1") {
		t.Fatal("service should not exist after unregister")
	}
}

func TestServiceRegistryCannotUnregisterRunning(t *testing.T) {
	reg := NewServiceRegistry()
	_ = reg.Register("svc-1", "1.0", nil)
	_ = reg.SetState("svc-1", ServiceStateRunning)

	err := reg.Unregister("svc-1")
	if err == nil {
		t.Fatal("expected error when unregistering a running service")
		return
	}
}

// ---------------------------------------------------------------------------
// Persistence parity tests
// ---------------------------------------------------------------------------

func TestPersistenceSaveLoadRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	store := NewStateStore()
	store.Update(map[StateField]any{
		FieldCurrentModel:   "claude-sonnet-4-6",
		FieldSessionID:      "sess-abc",
		FieldInputTokens:    5000,
		FieldOutputTokens:   2000,
		FieldTurnCount:      10,
		FieldPermissionMode: "bypass",
	})

	persistence := NewStatePersistence(PersistenceConfig{Dir: tmpDir})

	// Save
	if err := persistence.Save(store); err != nil {
		t.Fatalf("Save: %v", err)
		return
	}

	// Verify file exists
	if !persistence.Exists() {
		t.Fatal("expected state file to exist after save")
	}

	// Load into fresh store
	store2 := NewStateStore()
	if err := persistence.Load(store2); err != nil {
		t.Fatalf("Load: %v", err)
		return
	}

	if store2.GetString(FieldCurrentModel) != "claude-sonnet-4-6" {
		t.Fatalf("expected 'claude-sonnet-4-6', got %q", store2.GetString(FieldCurrentModel))
	}
	if store2.GetInt(FieldInputTokens) != 5000 {
		t.Fatalf("expected 5000, got %d", store2.GetInt(FieldInputTokens))
	}
	if store2.GetString(FieldPermissionMode) != "bypass" {
		t.Fatalf("expected 'bypass', got %q", store2.GetString(FieldPermissionMode))
	}
}

func TestPersistenceCorruptFileHandling(t *testing.T) {
	tmpDir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{Dir: tmpDir})

	// Write corrupt data
	stateFile := filepath.Join(tmpDir, "bridge_state.json")
	_ = os.WriteFile(stateFile, []byte("not valid json {{{"), 0o644)

	store := NewStateStore()
	err := persistence.Load(store)
	if err == nil {
		t.Fatal("expected error for corrupt state file")
		return
	}

	// Corrupt file should be removed
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatal("expected corrupt state file to be removed")
	}
}

func TestPersistenceEmptyFileIsIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{Dir: tmpDir})

	// Write empty file
	stateFile := filepath.Join(tmpDir, "bridge_state.json")
	_ = os.WriteFile(stateFile, []byte(""), 0o644)

	store := NewStateStore()
	err := persistence.Load(store)
	if err != nil {
		t.Fatalf("expected nil error for empty file, got: %v", err)
		return
	}
}

// ---------------------------------------------------------------------------
// TUI Bridge debounce parity test
// ---------------------------------------------------------------------------

func TestStateStoreVersionMonotonic(t *testing.T) {
	store := NewStateStore()

	v1 := store.Version()
	store.Set(FieldCurrentModel, "a")
	v2 := store.Version()
	store.Set(FieldCurrentModel, "b")
	v3 := store.Version()

	if v2 <= v1 || v3 <= v2 {
		t.Fatalf("versions should be monotonically increasing: v1=%d v2=%d v3=%d", v1, v2, v3)
	}
}

func TestStateStoreSelectiveTopicSnapshot(t *testing.T) {
	store := NewStateStore()
	store.Set(FieldCurrentModel, "model-x")
	store.Set(FieldInputTokens, 100)
	store.Set(FieldSessionID, "sess-1")

	// Snapshot only model topic
	snap := store.SnapshotSlice(TopicModel)
	if snap.GetString(FieldCurrentModel) != "model-x" {
		t.Fatal("expected model field in model-topic snapshot")
	}
	// Token and session fields should NOT be in a model-only snapshot
	if snap.Get(FieldInputTokens) != nil {
		t.Fatal("expected no token field in model-topic snapshot")
		return
	}
	if snap.Get(FieldSessionID) != nil {
		t.Fatal("expected no session field in model-topic snapshot")
		return
	}
}

// ---------------------------------------------------------------------------
// Concurrent observer safety test
// ---------------------------------------------------------------------------

func TestObserverConcurrentSafety(t *testing.T) {
	store := NewStateStore()

	var wg sync.WaitGroup

	// Spawn multiple observers concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			obs := store.Subscribe(TopicAll)
			time.Sleep(5 * time.Millisecond)
			obs.Close()
		}()
	}

	// Concurrently write state changes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			store.Set(FieldTurnCount, val)
		}(i)
	}

	wg.Wait()
	// If no panic/deadlock, the test passes
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type parityTestPlugin struct {
	name    string
	version string
	started bool
	stopped bool
}

func (p *parityTestPlugin) Name() string           { return p.name }
func (p *parityTestPlugin) Version() string        { return p.version }
func (p *parityTestPlugin) Capabilities() []string { return []string{"test"} }
func (p *parityTestPlugin) Dependencies() []string { return nil }
func (p *parityTestPlugin) Start(ctx context.Context) error {
	p.started = true
	return nil
}

func (p *parityTestPlugin) Stop(ctx context.Context) error {
	p.stopped = true
	return nil
}
func (p *parityTestPlugin) Health() error { return nil }

// Verify persistedState JSON serialization for round-trip correctness.
func TestPersistedStateJSON(t *testing.T) {
	state := persistedState{
		Version:        42,
		Timestamp:      time.Now(),
		Model:          "test-model",
		SessionID:      "sess-1",
		InputTokens:    100,
		OutputTokens:   50,
		TurnCount:      5,
		PermissionMode: "default",
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
		return
	}

	var loaded persistedState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
		return
	}

	if loaded.Model != "test-model" {
		t.Fatalf("expected 'test-model', got %q", loaded.Model)
	}
	if loaded.InputTokens != 100 {
		t.Fatalf("expected 100, got %d", loaded.InputTokens)
	}
}
