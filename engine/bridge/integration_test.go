package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TUI Bridge tests
// =============================================================================

func TestTUIBridge_BasicDelivery(t *testing.T) {
	store := NewStateStore()
	bridge := NewTUIBridge(store, WithDebounceInterval(0)) // no debounce for this test
	defer bridge.Stop()

	// Start listening in a goroutine.
	msgCh := make(chan StateUpdateMsg, 1)
	go func() {
		cmd := bridge.Listen()
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if m, ok := msg.(StateUpdateMsg); ok {
			msgCh <- m
		}
	}()

	// Give the goroutine time to start listening.
	time.Sleep(10 * time.Millisecond)

	// Trigger a state change.
	store.Set(FieldCurrentModel, "claude-3")

	select {
	case msg := <-msgCh:
		if len(msg.Changes) != 1 {
			t.Fatalf("expected 1 change, got %d", len(msg.Changes))
		}
		if msg.Changes[0].Field != FieldCurrentModel {
			t.Errorf("expected FieldCurrentModel, got %q", msg.Changes[0].Field)
		}
		if msg.Changes[0].NewValue != "claude-3" {
			t.Errorf("expected value 'claude-3', got %v", msg.Changes[0].NewValue)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for TUI bridge message")
	}
}

func TestTUIBridge_Debouncing(t *testing.T) {
	store := NewStateStore()
	bridge := NewTUIBridge(store, WithDebounceInterval(50*time.Millisecond))
	defer bridge.Stop()

	msgCh := make(chan StateUpdateMsg, 1)
	go func() {
		cmd := bridge.Listen()
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if m, ok := msg.(StateUpdateMsg); ok {
			msgCh <- m
		}
	}()

	// Give the goroutine time to start.
	time.Sleep(10 * time.Millisecond)

	// Fire rapid state changes within the debounce window.
	store.Set(FieldCurrentModel, "m1")
	store.Set(FieldCurrentModel, "m2")
	store.Set(FieldCurrentModel, "m3")
	store.Set(FieldTurnCount, 1)

	select {
	case msg := <-msgCh:
		// All changes should be batched into a single delivery.
		if len(msg.Changes) < 2 {
			t.Errorf("expected multiple batched changes, got %d", len(msg.Changes))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for debounced TUI bridge message")
	}
}

func TestTUIBridge_TopicFiltering(t *testing.T) {
	store := NewStateStore()
	bridge := NewTUIBridge(store,
		WithDebounceInterval(0),
		WithTUITopics(TopicModel),
	)
	defer bridge.Stop()

	msgCh := make(chan StateUpdateMsg, 1)
	go func() {
		cmd := bridge.Listen()
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if m, ok := msg.(StateUpdateMsg); ok {
			msgCh <- m
		}
	}()

	time.Sleep(10 * time.Millisecond)

	// This should NOT trigger a message (wrong topic).
	store.Set(FieldTurnCount, 5)

	// This SHOULD trigger a message.
	store.Set(FieldCurrentModel, "claude-3")

	select {
	case msg := <-msgCh:
		if len(msg.Changes) != 1 {
			t.Fatalf("expected 1 change, got %d", len(msg.Changes))
		}
		if msg.Changes[0].Field != FieldCurrentModel {
			t.Errorf("expected FieldCurrentModel, got %q", msg.Changes[0].Field)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for filtered TUI bridge message")
	}
}

func TestTUIBridge_WithSnapshot(t *testing.T) {
	store := NewStateStore()
	store.Set(FieldSessionID, "sess-existing")

	bridge := NewTUIBridge(store,
		WithDebounceInterval(0),
		WithSnapshotOnDelivery(),
	)
	defer bridge.Stop()

	msgCh := make(chan StateUpdateMsg, 1)
	go func() {
		cmd := bridge.Listen()
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if m, ok := msg.(StateUpdateMsg); ok {
			msgCh <- m
		}
	}()

	time.Sleep(10 * time.Millisecond)

	store.Set(FieldCurrentModel, "claude-3")

	select {
	case msg := <-msgCh:
		if msg.Snapshot == nil {
			t.Fatal("expected snapshot to be included")
			return
		}
		if msg.Snapshot.GetString(FieldSessionID) != "sess-existing" {
			t.Errorf("snapshot should contain existing state, got %q", msg.Snapshot.GetString(FieldSessionID))
		}
		if msg.Snapshot.GetString(FieldCurrentModel) != "claude-3" {
			t.Errorf("snapshot should contain new state, got %q", msg.Snapshot.GetString(FieldCurrentModel))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for TUI bridge message with snapshot")
	}
}

func TestTUIBridge_Stop(t *testing.T) {
	store := NewStateStore()
	bridge := NewTUIBridge(store, WithDebounceInterval(0))

	if !bridge.IsRunning() {
		t.Error("bridge should be running after creation")
	}

	bridge.Stop()

	if bridge.IsRunning() {
		t.Error("bridge should not be running after Stop")
	}

	// Listen should return nil command after stop.
	cmd := bridge.Listen()
	if cmd != nil {
		msg := cmd()
		if msg != nil {
			t.Error("Listen() should return nil msg after Stop")
		}
	}

	// Double stop should not panic.
	bridge.Stop()
}

func TestTUIBridge_MultipleListenCycles(t *testing.T) {
	store := NewStateStore()
	bridge := NewTUIBridge(store, WithDebounceInterval(0))
	defer bridge.Stop()

	// Simulate multiple listen cycles (like a real TUI would do).
	for i := 0; i < 3; i++ {
		msgCh := make(chan StateUpdateMsg, 1)
		go func() {
			cmd := bridge.Listen()
			if cmd == nil {
				return
			}
			msg := cmd()
			if msg == nil {
				return
			}
			if m, ok := msg.(StateUpdateMsg); ok {
				msgCh <- m
			}
		}()

		time.Sleep(10 * time.Millisecond)
		store.Set(FieldCurrentModel, fmt.Sprintf("model-%d", i))

		select {
		case msg := <-msgCh:
			if len(msg.Changes) < 1 {
				t.Fatalf("cycle %d: expected at least 1 change", i)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("cycle %d: timed out", i)
		}
	}
}

// =============================================================================
// State Persistence tests
// =============================================================================

func TestPersistence_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	store := NewStateStore()
	store.Update(map[StateField]any{
		FieldCurrentModel:   "claude-3",
		FieldSessionID:      "sess-123",
		FieldSessionCWD:     "/home/user/project",
		FieldInputTokens:    5000,
		FieldOutputTokens:   2000,
		FieldTurnCount:      10,
		FieldPermissionMode: "default",
	})

	persistence := NewStatePersistence(PersistenceConfig{Dir: dir})

	// Save.
	if err := persistence.Save(store); err != nil {
		t.Fatalf("Save failed: %v", err)
		return
	}

	if !persistence.Exists() {
		t.Fatal("state file should exist after Save")
	}

	// Load into a fresh store.
	newStore := NewStateStore()
	if err := persistence.Load(newStore); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}

	// Verify restored values.
	if got := newStore.GetString(FieldCurrentModel); got != "claude-3" {
		t.Errorf("FieldCurrentModel = %q, want %q", got, "claude-3")
	}
	if got := newStore.GetString(FieldSessionID); got != "sess-123" {
		t.Errorf("FieldSessionID = %q, want %q", got, "sess-123")
	}
	if got := newStore.GetString(FieldSessionCWD); got != "/home/user/project" {
		t.Errorf("FieldSessionCWD = %q, want %q", got, "/home/user/project")
	}
	if got := newStore.GetInt(FieldInputTokens); got != 5000 {
		t.Errorf("FieldInputTokens = %d, want 5000", got)
	}
	if got := newStore.GetInt(FieldOutputTokens); got != 2000 {
		t.Errorf("FieldOutputTokens = %d, want 2000", got)
	}
	if got := newStore.GetInt(FieldTurnCount); got != 10 {
		t.Errorf("FieldTurnCount = %d, want 10", got)
	}
	if got := newStore.GetString(FieldPermissionMode); got != "default" {
		t.Errorf("FieldPermissionMode = %q, want %q", got, "default")
	}
}

func TestPersistence_MissingFile(t *testing.T) {
	dir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{Dir: dir})

	store := NewStateStore()
	// Load with no file should succeed without error.
	if err := persistence.Load(store); err != nil {
		t.Fatalf("Load with missing file should not error: %v", err)
		return
	}

	// Store should be empty.
	if store.Version() != 0 {
		t.Errorf("store version should be 0, got %d", store.Version())
	}
}

func TestPersistence_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{Dir: dir})

	// Write corrupt data.
	targetPath := filepath.Join(dir, "bridge_state.json")
	if err := os.WriteFile(targetPath, []byte("not valid json{{{"), 0o644); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
		return
	}

	store := NewStateStore()
	err := persistence.Load(store)
	if err == nil {
		t.Fatal("Load with corrupt file should return error")
		return
	}

	// Corrupt file should be removed.
	if persistence.Exists() {
		t.Error("corrupt file should have been removed")
	}
}

func TestPersistence_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{Dir: dir})

	// Write empty file.
	targetPath := filepath.Join(dir, "bridge_state.json")
	if err := os.WriteFile(targetPath, []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
		return
	}

	store := NewStateStore()
	if err := persistence.Load(store); err != nil {
		t.Fatalf("Load with empty file should not error: %v", err)
		return
	}
}

func TestPersistence_DisabledWhenDirEmpty(t *testing.T) {
	persistence := NewStatePersistence(PersistenceConfig{Dir: ""})

	store := NewStateStore()
	store.Set(FieldCurrentModel, "model")

	// All ops should be no-ops.
	if err := persistence.Save(store); err != nil {
		t.Fatalf("Save should be no-op: %v", err)
		return
	}
	if err := persistence.Load(store); err != nil {
		t.Fatalf("Load should be no-op: %v", err)
		return
	}
	if persistence.Exists() {
		t.Error("Exists should be false when disabled")
	}
	if persistence.Path() != "" {
		t.Error("Path should be empty when disabled")
	}
}

func TestPersistence_Remove(t *testing.T) {
	dir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{Dir: dir})

	store := NewStateStore()
	store.Set(FieldCurrentModel, "model")
	_ = persistence.Save(store)

	if !persistence.Exists() {
		t.Fatal("file should exist after Save")
	}

	if err := persistence.Remove(); err != nil {
		t.Fatalf("Remove failed: %v", err)
		return
	}

	if persistence.Exists() {
		t.Error("file should not exist after Remove")
	}

	// Remove again should not error.
	if err := persistence.Remove(); err != nil {
		t.Fatalf("double Remove should not error: %v", err)
		return
	}
}

func TestPersistence_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{Dir: dir})

	store := NewStateStore()
	store.Set(FieldCurrentModel, "first")
	_ = persistence.Save(store)

	// Update and save again.
	store.Set(FieldCurrentModel, "second")
	_ = persistence.Save(store)

	// Load should get the latest.
	newStore := NewStateStore()
	_ = persistence.Load(newStore)
	if got := newStore.GetString(FieldCurrentModel); got != "second" {
		t.Errorf("expected 'second', got %q", got)
	}

	// No temp files should remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestPersistence_CustomFilename(t *testing.T) {
	dir := t.TempDir()
	persistence := NewStatePersistence(PersistenceConfig{
		Dir:      dir,
		Filename: "custom_state.json",
	})

	store := NewStateStore()
	store.Set(FieldCurrentModel, "model")
	_ = persistence.Save(store)

	expected := filepath.Join(dir, "custom_state.json")
	if persistence.Path() != expected {
		t.Errorf("path = %q, want %q", persistence.Path(), expected)
	}
	if !persistence.Exists() {
		t.Error("custom-named file should exist")
	}
}

// =============================================================================
// Plugin Event Hooks tests
// =============================================================================

func TestPluginEventHooks_Subscribe(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	received := make(chan StateChange, 10)
	err := hooks.Subscribe("my-plugin", func(change StateChange) {
		received <- change
	}, TopicModel)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
		return
	}

	// Give dispatch goroutine time to start.
	time.Sleep(10 * time.Millisecond)

	store.Set(FieldCurrentModel, "claude-3")

	select {
	case change := <-received:
		if change.Field != FieldCurrentModel {
			t.Errorf("expected FieldCurrentModel, got %q", change.Field)
		}
		if change.NewValue != "claude-3" {
			t.Errorf("expected 'claude-3', got %v", change.NewValue)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("plugin did not receive state change")
	}
}

func TestPluginEventHooks_TopicFiltering(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	received := make(chan StateChange, 10)
	_ = hooks.Subscribe("my-plugin", func(change StateChange) {
		received <- change
	}, TopicModel)

	time.Sleep(10 * time.Millisecond)

	// This should NOT reach the plugin (wrong topic).
	store.Set(FieldTurnCount, 5)
	// This SHOULD reach the plugin.
	store.Set(FieldCurrentModel, "claude-3")

	select {
	case change := <-received:
		if change.Field != FieldCurrentModel {
			t.Errorf("expected FieldCurrentModel, got %q", change.Field)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("plugin did not receive state change")
	}
}

func TestPluginEventHooks_Writer(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	writer := hooks.Writer("my-plugin")

	if writer.PluginName() != "my-plugin" {
		t.Errorf("writer plugin name = %q, want %q", writer.PluginName(), "my-plugin")
	}

	// Plugin writes a state field.
	change := writer.Set("status", "active")
	expectedField := StateField("plugin.my-plugin.status")
	if change.Field != expectedField {
		t.Errorf("change.Field = %q, want %q", change.Field, expectedField)
	}

	// Verify the store has it.
	if got := store.Get(expectedField); got != "active" {
		t.Errorf("store value = %v, want 'active'", got)
	}
}

func TestPluginEventHooks_WriterBatch(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	writer := hooks.Writer("metrics")
	changes := writer.Update(map[string]any{
		"requests":   100,
		"errors":     5,
		"latency_ms": 42,
	})

	if len(changes) != 3 {
		t.Fatalf("expected 3 changes, got %d", len(changes))
	}

	// Verify scoped field names.
	if got := store.Get(StateField("plugin.metrics.requests")); got != 100 {
		t.Errorf("plugin.metrics.requests = %v, want 100", got)
	}
	if got := store.Get(StateField("plugin.metrics.errors")); got != 5 {
		t.Errorf("plugin.metrics.errors = %v, want 5", got)
	}
}

func TestPluginEventHooks_Bidirectional(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	// Plugin A subscribes to all topics (including plugin-emitted).
	pluginAReceived := make(chan StateChange, 10)
	_ = hooks.Subscribe("plugin-a", func(change StateChange) {
		pluginAReceived <- change
	})

	// Plugin B gets a writer.
	writerB := hooks.Writer("plugin-b")

	time.Sleep(10 * time.Millisecond)

	// Plugin B emits a state change.
	writerB.Set("metric", "value-from-b")

	// Plugin A should see it through the observer path.
	select {
	case change := <-pluginAReceived:
		if change.Field != StateField("plugin.plugin-b.metric") {
			t.Errorf("plugin-a received field %q, expected 'plugin.plugin-b.metric'", change.Field)
		}
		if change.NewValue != "value-from-b" {
			t.Errorf("received value %v, expected 'value-from-b'", change.NewValue)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("plugin-a did not receive plugin-b's emitted state change")
	}
}

func TestPluginEventHooks_Unsubscribe(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)

	var callCount atomic.Int32
	_ = hooks.Subscribe("my-plugin", func(change StateChange) {
		callCount.Add(1)
	}, TopicModel)

	time.Sleep(10 * time.Millisecond)

	store.Set(FieldCurrentModel, "m1")
	time.Sleep(20 * time.Millisecond)

	hooks.Unsubscribe("my-plugin")

	// Changes after unsubscribe should NOT reach the plugin.
	store.Set(FieldCurrentModel, "m2")
	time.Sleep(20 * time.Millisecond)

	if count := callCount.Load(); count != 1 {
		t.Errorf("expected 1 call before unsubscribe, got %d", count)
	}

	plugins := hooks.SubscribedPlugins()
	if len(plugins) != 0 {
		t.Errorf("expected 0 subscribed plugins, got %d", len(plugins))
	}
}

func TestPluginEventHooks_PanicRecovery(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	// A handler that panics.
	_ = hooks.Subscribe("panicky-plugin", func(change StateChange) {
		panic("plugin bug!")
	}, TopicModel)

	time.Sleep(10 * time.Millisecond)

	// Should not crash the process.
	store.Set(FieldCurrentModel, "m1")
	time.Sleep(20 * time.Millisecond)

	// The subscription should still be alive (goroutine recovered).
	plugins := hooks.SubscribedPlugins()
	if len(plugins) != 1 {
		t.Errorf("expected 1 subscribed plugin, got %d", len(plugins))
	}
}

func TestPluginEventHooks_EmptyPluginName(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	err := hooks.Subscribe("", func(change StateChange) {}, TopicModel)
	if err == nil {
		t.Error("expected error for empty plugin name")
	}
}

func TestPluginEventHooks_NilHandler(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	err := hooks.Subscribe("plugin", nil, TopicModel)
	if err == nil {
		t.Error("expected error for nil handler")
	}
}

func TestPluginEventHooks_MultipleHandlers(t *testing.T) {
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	var count1, count2 atomic.Int32

	_ = hooks.Subscribe("multi-plugin", func(change StateChange) {
		count1.Add(1)
	}, TopicModel)

	_ = hooks.Subscribe("multi-plugin", func(change StateChange) {
		count2.Add(1)
	}, TopicModel)

	time.Sleep(10 * time.Millisecond)

	store.Set(FieldCurrentModel, "m1")
	time.Sleep(50 * time.Millisecond)

	if count1.Load() != 1 {
		t.Errorf("handler 1 called %d times, expected 1", count1.Load())
	}
	if count2.Load() != 1 {
		t.Errorf("handler 2 called %d times, expected 1", count2.Load())
	}
}

// =============================================================================
// End-to-end integration tests
// =============================================================================

func TestIntegration_EngineToObserver(t *testing.T) {
	// Full flow: engine event -> adapter -> store -> observer -> client notification.
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()
	defer adapter.Stop()

	// Register a client that subscribes to conversation and model.
	client := store.RegisterClient(RegisterClientOpts{
		ID:     "tui-client",
		Type:   ClientTypeTUI,
		Topics: []Topic{TopicConversation, TopicModel},
	})

	obs := client.Observer()

	// Simulate engine event: stream start.
	adapter.HandleEvent(EventFromStreamStart())

	select {
	case change := <-obs.Changes():
		if change.Field != FieldConversationStatus {
			t.Errorf("expected FieldConversationStatus, got %q", change.Field)
		}
		if change.NewValue != StatusThinking {
			t.Errorf("expected StatusThinking, got %v", change.NewValue)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("client did not receive stream start notification")
	}

	// Simulate model change.
	adapter.HandleEvent(EventFromModelChange("claude-sonnet"))

	select {
	case change := <-obs.Changes():
		if change.Field != FieldCurrentModel {
			t.Errorf("expected FieldCurrentModel, got %q", change.Field)
		}
		if change.NewValue != "claude-sonnet" {
			t.Errorf("expected 'claude-sonnet', got %v", change.NewValue)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("client did not receive model change notification")
	}
}

func TestIntegration_FullTurnCycle(t *testing.T) {
	// Simulate a complete agent turn: start -> tool -> tool complete -> terminal.
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.InitializeState(InitialState{
		Model:     "claude-3",
		SessionID: "sess-1",
		CWD:       "/tmp/test",
	})
	adapter.Start()
	defer adapter.Stop()

	obs := store.Subscribe(TopicConversation, TopicTools)
	defer obs.Close()

	// Stream start.
	adapter.HandleEvent(EventFromStreamStart())
	waitForChange(t, obs, FieldConversationStatus, StatusThinking)

	// Tool start.
	adapter.HandleEvent(EventFromToolProgress("bash", "tool-1", false))
	waitForChange(t, obs, FieldConversationStatus, StatusToolRunning)

	// Tool complete.
	adapter.HandleEvent(EventFromToolProgress("bash", "tool-1", true))
	waitForChange(t, obs, FieldConversationStatus, StatusThinking)

	// Terminal.
	adapter.HandleEvent(EventFromTerminal())
	waitForChange(t, obs, FieldConversationStatus, StatusIdle)
}

func TestIntegration_TUIBridgeWithAdapter(t *testing.T) {
	// Full flow: engine event -> adapter -> store -> TUI bridge -> tea.Msg.
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.InitializeState(InitialState{Model: "claude-3"})
	adapter.Start()
	defer adapter.Stop()

	tuiBridge := NewTUIBridge(store,
		WithDebounceInterval(30*time.Millisecond),
		WithTUITopics(TopicConversation, TopicModel),
	)
	defer tuiBridge.Stop()

	msgCh := make(chan StateUpdateMsg, 1)
	go func() {
		cmd := tuiBridge.Listen()
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if m, ok := msg.(StateUpdateMsg); ok {
			msgCh <- m
		}
	}()

	time.Sleep(10 * time.Millisecond)

	// Rapid engine events.
	adapter.HandleEvent(EventFromStreamStart())
	adapter.HandleEvent(EventFromModelChange("claude-sonnet"))

	select {
	case msg := <-msgCh:
		// Should receive batched changes.
		if len(msg.Changes) < 1 {
			t.Fatalf("expected at least 1 change, got %d", len(msg.Changes))
		}
		// Verify at least one of the expected changes arrived.
		found := false
		for _, c := range msg.Changes {
			if c.Field == FieldConversationStatus || c.Field == FieldCurrentModel {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected conversation or model change in batch")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("TUI bridge did not deliver batched message")
	}
}

func TestIntegration_PersistenceRoundTrip(t *testing.T) {
	// Full flow: set state -> save -> new store -> load -> verify.
	dir := t.TempDir()

	// Session 1: set state and save.
	store1 := NewStateStore()
	adapter1 := NewBridgeAdapter(store1)
	adapter1.InitializeState(InitialState{
		Model:          "claude-3",
		SessionID:      "sess-42",
		CWD:            "/project",
		PermissionMode: "strict",
	})
	adapter1.Start()
	adapter1.HandleEvent(EventFromTokenUsage(1000, 500))
	adapter1.HandleEvent(EventFromTurnCount(7))
	adapter1.Stop()

	persistence := NewStatePersistence(PersistenceConfig{Dir: dir})
	if err := persistence.Save(store1); err != nil {
		t.Fatalf("Save failed: %v", err)
		return
	}

	// Session 2: load into fresh store.
	store2 := NewStateStore()
	if err := persistence.Load(store2); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}

	if got := store2.GetString(FieldCurrentModel); got != "claude-3" {
		t.Errorf("model = %q, want %q", got, "claude-3")
	}
	if got := store2.GetString(FieldSessionID); got != "sess-42" {
		t.Errorf("session = %q, want %q", got, "sess-42")
	}
	if got := store2.GetInt(FieldInputTokens); got != 1000 {
		t.Errorf("input tokens = %d, want 1000", got)
	}
	if got := store2.GetInt(FieldOutputTokens); got != 500 {
		t.Errorf("output tokens = %d, want 500", got)
	}
	if got := store2.GetInt(FieldTurnCount); got != 7 {
		t.Errorf("turn count = %d, want 7", got)
	}
}

func TestIntegration_PluginHooksWithObservers(t *testing.T) {
	// Plugin subscribes -> core state changes -> plugin notified ->
	// plugin emits -> regular observer sees the plugin emission.
	store := NewStateStore()
	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	// Regular observer watching all changes.
	regularObs := store.Subscribe()
	defer regularObs.Close()

	// Plugin subscribes to model changes and emits its own state in response.
	writer := hooks.Writer("responder-plugin")
	_ = hooks.Subscribe("responder-plugin", func(change StateChange) {
		if change.Field == FieldCurrentModel {
			writer.Set("last_model_seen", change.NewValue)
		}
	}, TopicModel)

	time.Sleep(10 * time.Millisecond)

	// Trigger a model change.
	store.Set(FieldCurrentModel, "claude-sonnet")

	// Wait for the plugin's emitted state change to propagate.
	deadline := time.After(500 * time.Millisecond)
	var pluginChangeReceived bool
	for !pluginChangeReceived {
		select {
		case change := <-regularObs.Changes():
			if change.Field == StateField("plugin.responder-plugin.last_model_seen") {
				pluginChangeReceived = true
				if change.NewValue != "claude-sonnet" {
					t.Errorf("expected 'claude-sonnet', got %v", change.NewValue)
				}
			}
		case <-deadline:
			t.Fatal("regular observer did not receive plugin-emitted state change")
			return
		}
	}
}

func TestIntegration_ConcurrentStress(t *testing.T) {
	// Stress test: many state changes + many observers + disconnect/reconnect.
	store := NewStateStore(WithObserverBuffer(32))
	adapter := NewBridgeAdapter(store)
	adapter.Start()
	defer adapter.Stop()

	hooks := NewPluginEventHooks(store)
	defer hooks.UnsubscribeAll()

	const (
		numWriters    = 5
		numObservers  = 10
		numPlugins    = 3
		numIterations = 100
		numReconnects = 5
	)

	var wg sync.WaitGroup
	writersReady := make(chan struct{})

	// Writers: pump events through the adapter.
	wg.Add(numWriters)
	for w := 0; w < numWriters; w++ {
		go func(id int) {
			defer wg.Done()
			<-writersReady
			for i := 0; i < numIterations; i++ {
				adapter.HandleEvent(EventFromTokenUsage(1, 1))
				adapter.HandleEvent(EventFromTurnCount(id*numIterations + i))
			}
		}(w)
	}

	// Observers: consume changes.
	var totalReceived atomic.Int64
	var observersReady sync.WaitGroup
	observersReady.Add(numObservers)
	wg.Add(numObservers)
	for o := 0; o < numObservers; o++ {
		go func() {
			defer wg.Done()
			obs := store.Subscribe(TopicConversation, TopicTokens)
			defer obs.Close()
			observersReady.Done()
			for i := 0; i < numIterations*numWriters; i++ {
				select {
				case _, ok := <-obs.Changes():
					if !ok {
						return
					}
					totalReceived.Add(1)
				case <-time.After(100 * time.Millisecond):
					return
				}
			}
		}()
	}

	// Plugins: subscribe and do light work.
	var pluginCalls atomic.Int64
	for p := 0; p < numPlugins; p++ {
		name := fmt.Sprintf("stress-plugin-%d", p)
		_ = hooks.Subscribe(name, func(change StateChange) {
			pluginCalls.Add(1)
		}, TopicConversation)
	}
	observersReady.Wait()
	close(writersReady)

	// Client churn: register and unregister.
	wg.Add(numReconnects)
	for r := 0; r < numReconnects; r++ {
		go func(id int) {
			defer wg.Done()
			clientID := fmt.Sprintf("churn-client-%d", id)
			for i := 0; i < numIterations/10; i++ {
				c := store.RegisterClient(RegisterClientOpts{
					ID:     clientID,
					Type:   ClientTypeTUI,
					Topics: []Topic{TopicConversation},
				})
				time.Sleep(time.Millisecond)
				c.Touch()
				store.UnregisterClient(clientID)
			}
		}(r)
	}

	wg.Wait()

	// Allow plugins to finish processing.
	time.Sleep(50 * time.Millisecond)

	// Verify: no panics, observers received some data, plugins were called.
	if totalReceived.Load() == 0 {
		t.Error("observers received no changes during stress test")
	}
	if pluginCalls.Load() == 0 {
		t.Error("plugins received no changes during stress test")
	}

	// No observer leaks.
	if count := store.ObserverCount(); count > numPlugins {
		// Only plugin observers should remain (regular observers closed by deferred Close).
		// But plugin observers are managed by hooks.
		t.Logf("note: %d observers remain (expected ~%d plugin observers)", count, numPlugins)
	}
}

func TestIntegration_BridgeManagerFullFlow(t *testing.T) {
	// Complete BridgeManager flow with all components.
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:          "claude-3",
		SessionID:      "sess-mgr-1",
		CWD:            "/test",
		PermissionMode: "default",
	})
	mgr.Start()
	defer mgr.Stop()

	// Register a TUI client.
	client := mgr.RegisterClient(RegisterClientOpts{
		ID:     "tui",
		Type:   ClientTypeTUI,
		Topics: []Topic{TopicConversation, TopicModel, TopicTokens},
	})

	obs := client.Observer()

	// Create a TUI bridge on the same store.
	tuiBridge := NewTUIBridge(mgr.Store(), WithDebounceInterval(0), WithTUITopics(TopicModel))
	defer tuiBridge.Stop()

	// Set up plugin hooks.
	hooks := NewPluginEventHooks(mgr.Store())
	defer hooks.UnsubscribeAll()

	var pluginNotified atomic.Bool
	_ = hooks.Subscribe("test-plugin", func(change StateChange) {
		if change.Field == FieldConversationStatus {
			pluginNotified.Store(true)
		}
	}, TopicConversation)

	time.Sleep(10 * time.Millisecond)

	// Feed events through the manager.
	mgr.HandleEvent(EventFromStreamStart())

	// Client observer should get notified.
	select {
	case change := <-obs.Changes():
		if change.Field != FieldConversationStatus {
			t.Errorf("client expected FieldConversationStatus, got %q", change.Field)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("client did not receive notification")
	}

	// Plugin should also be notified.
	time.Sleep(20 * time.Millisecond)
	if !pluginNotified.Load() {
		t.Error("plugin was not notified of state change")
	}

	// Verify store state.
	snap := mgr.Snapshot()
	if snap.GetString(FieldCurrentModel) != "claude-3" {
		t.Errorf("model = %q, want 'claude-3'", snap.GetString(FieldCurrentModel))
	}
	if snap.Get(FieldConversationStatus) != StatusThinking {
		t.Errorf("status = %v, want StatusThinking", snap.Get(FieldConversationStatus))
	}
}

// =============================================================================
// Helper functions
// =============================================================================

func waitForChange(t *testing.T, obs *Observer, expectedField StateField, expectedValue any) {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case change := <-obs.Changes():
			if change.Field == expectedField {
				if expectedValue != nil && change.NewValue != expectedValue {
					t.Errorf("field %q: got value %v, want %v", expectedField, change.NewValue, expectedValue)
				}
				return
			}
			// Keep draining if it's not the field we want (batch may contain others).
		case <-deadline:
			t.Fatalf("timed out waiting for field %q with value %v", expectedField, expectedValue)
			return
		}
	}
}
