package bridge

import (
	"sync"
	"testing"
	"time"
)

// --- BridgeAdapter tests ---

func TestBridgeAdapter_NewAdapter(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)

	if adapter == nil {
		t.Fatal("NewBridgeAdapter returned nil")
		return
	}
	if adapter.IsRunning() {
		t.Error("adapter should not be running before Start()")
	}
}

func TestBridgeAdapter_Lifecycle(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)

	// Not running before Start.
	if adapter.IsRunning() {
		t.Error("should not be running before Start()")
	}

	adapter.Start()
	if !adapter.IsRunning() {
		t.Error("should be running after Start()")
	}

	adapter.Stop()
	if adapter.IsRunning() {
		t.Error("should not be running after Stop()")
	}

	// Start after Stop should have no effect.
	adapter.Start()
	if adapter.IsRunning() {
		t.Error("should not be running after Stop() even if Start() called again")
	}
}

func TestBridgeAdapter_IgnoresEventsBeforeStart(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)

	// Send event before Start — should be ignored.
	adapter.HandleEvent(EventFromStreamStart())

	if store.Get(FieldConversationStatus) != nil {
		t.Error("events before Start() should be ignored")
	}
}

func TestBridgeAdapter_IgnoresEventsAfterStop(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	// Set initial state.
	adapter.HandleEvent(EventFromStreamStart())
	if store.Get(FieldConversationStatus) != StatusThinking {
		t.Fatal("should have updated to thinking")
	}

	adapter.Stop()

	// Reset manually to detect change.
	store.Set(FieldConversationStatus, StatusIdle)

	// Events after Stop should be ignored.
	adapter.HandleEvent(EventFromStreamStart())
	if store.Get(FieldConversationStatus) != StatusIdle {
		t.Error("events after Stop() should be ignored")
	}
}

func TestBridgeAdapter_StreamStart_SetsThinking(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	adapter.HandleEvent(EventFromStreamStart())

	if got := store.Get(FieldConversationStatus); got != StatusThinking {
		t.Errorf("after StreamStart: status = %v, want %v", got, StatusThinking)
	}
}

func TestBridgeAdapter_Terminal_SetsIdle(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	// Start thinking first.
	adapter.HandleEvent(EventFromStreamStart())

	// Then terminal.
	adapter.HandleEvent(EventFromTerminal())

	if got := store.Get(FieldConversationStatus); got != StatusIdle {
		t.Errorf("after Terminal: status = %v, want %v", got, StatusIdle)
	}
	if got := store.GetBool(FieldIsCompacting); got {
		t.Error("after Terminal: IsCompacting should be false")
	}
}

func TestBridgeAdapter_ToolStart_SetsToolRunning(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	adapter.HandleEvent(EventFromToolProgress("Bash", "tool-1", false))

	if got := store.Get(FieldConversationStatus); got != StatusToolRunning {
		t.Errorf("after ToolStart: status = %v, want %v", got, StatusToolRunning)
	}

	activeTools, ok := store.Get(FieldActiveTools).([]string)
	if !ok {
		t.Fatal("FieldActiveTools should be []string")
	}
	if len(activeTools) != 1 || activeTools[0] != "Bash" {
		t.Errorf("active tools = %v, want [Bash]", activeTools)
	}
}

func TestBridgeAdapter_ToolComplete_RemovesTool(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	// Start a tool.
	adapter.HandleEvent(EventFromToolProgress("Bash", "tool-1", false))

	// Complete it.
	adapter.HandleEvent(EventFromToolProgress("Bash", "tool-1", true))

	activeTools, ok := store.Get(FieldActiveTools).([]string)
	if !ok {
		t.Fatal("FieldActiveTools should be []string")
	}
	if len(activeTools) != 0 {
		t.Errorf("active tools after complete = %v, want empty", activeTools)
	}

	// Status should revert to thinking.
	if got := store.Get(FieldConversationStatus); got != StatusThinking {
		t.Errorf("after all tools complete: status = %v, want %v", got, StatusThinking)
	}
}

func TestBridgeAdapter_MultipleTools(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	// Start two tools.
	adapter.HandleEvent(EventFromToolProgress("Bash", "tool-1", false))
	adapter.HandleEvent(EventFromToolProgress("Read", "tool-2", false))

	activeTools, _ := store.Get(FieldActiveTools).([]string)
	if len(activeTools) != 2 {
		t.Errorf("active tools = %v, want 2 tools", activeTools)
	}

	// Complete one — status should stay tool_running.
	adapter.HandleEvent(EventFromToolProgress("Bash", "tool-1", true))

	if got := store.Get(FieldConversationStatus); got != StatusToolRunning {
		t.Errorf("with one tool remaining: status = %v, want %v", got, StatusToolRunning)
	}

	activeTools, _ = store.Get(FieldActiveTools).([]string)
	if len(activeTools) != 1 {
		t.Errorf("active tools after one complete = %v, want 1 tool", activeTools)
	}

	// Complete the other — status should revert to thinking.
	adapter.HandleEvent(EventFromToolProgress("Read", "tool-2", true))

	if got := store.Get(FieldConversationStatus); got != StatusThinking {
		t.Errorf("after all tools complete: status = %v, want %v", got, StatusThinking)
	}
}

func TestBridgeAdapter_Terminal_ClearsActiveTools(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	// Start a tool.
	adapter.HandleEvent(EventFromToolProgress("Bash", "tool-1", false))

	// Terminal should clear everything.
	adapter.HandleEvent(EventFromTerminal())

	activeTools, _ := store.Get(FieldActiveTools).([]string)
	if len(activeTools) != 0 {
		t.Errorf("active tools after terminal = %v, want empty", activeTools)
	}
}

func TestBridgeAdapter_TokenUsage_Accumulates(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.InitializeState(InitialState{})
	adapter.Start()

	adapter.HandleEvent(EventFromTokenUsage(100, 50))
	adapter.HandleEvent(EventFromTokenUsage(200, 75))

	if got := store.GetInt(FieldInputTokens); got != 300 {
		t.Errorf("input tokens = %d, want 300", got)
	}
	if got := store.GetInt(FieldOutputTokens); got != 125 {
		t.Errorf("output tokens = %d, want 125", got)
	}
}

func TestBridgeAdapter_ModelChange(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	adapter.HandleEvent(EventFromModelChange("claude-sonnet-4-20250514"))

	if got := store.GetString(FieldCurrentModel); got != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", got, "claude-sonnet-4-20250514")
	}
}

func TestBridgeAdapter_SessionUpdate(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	adapter.HandleEvent(EventFromSessionUpdate("sess-abc", "/home/user/project"))

	if got := store.GetString(FieldSessionID); got != "sess-abc" {
		t.Errorf("session ID = %q, want %q", got, "sess-abc")
	}
	if got := store.GetString(FieldSessionCWD); got != "/home/user/project" {
		t.Errorf("session CWD = %q, want %q", got, "/home/user/project")
	}
}

func TestBridgeAdapter_PermissionMode(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	adapter.HandleEvent(EventFromPermissionMode("plan"))

	if got := store.GetString(FieldPermissionMode); got != "plan" {
		t.Errorf("permission mode = %q, want %q", got, "plan")
	}
}

func TestBridgeAdapter_TurnCount(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	adapter.HandleEvent(EventFromTurnCount(5))

	if got := store.GetInt(FieldTurnCount); got != 5 {
		t.Errorf("turn count = %d, want 5", got)
	}
}

func TestBridgeAdapter_Compaction(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	adapter.HandleEvent(EventFromCompaction(true))

	if got := store.GetBool(FieldIsCompacting); !got {
		t.Error("is compacting should be true")
	}

	adapter.HandleEvent(EventFromCompaction(false))

	if got := store.GetBool(FieldIsCompacting); got {
		t.Error("is compacting should be false after clearing")
	}
}

func TestBridgeAdapter_InitializeState(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)

	adapter.InitializeState(InitialState{
		Model:          "claude-sonnet-4-20250514",
		FallbackModel:  "claude-haiku",
		SessionID:      "sess-init",
		CWD:            "/workspace",
		PermissionMode: "default",
	})

	if got := store.GetString(FieldCurrentModel); got != "claude-sonnet-4-20250514" {
		t.Errorf("initial model = %q, want %q", got, "claude-sonnet-4-20250514")
	}
	if got := store.GetString(FieldFallbackModel); got != "claude-haiku" {
		t.Errorf("initial fallback model = %q, want %q", got, "claude-haiku")
	}
	if got := store.GetString(FieldSessionID); got != "sess-init" {
		t.Errorf("initial session ID = %q, want %q", got, "sess-init")
	}
	if got := store.GetString(FieldSessionCWD); got != "/workspace" {
		t.Errorf("initial CWD = %q, want %q", got, "/workspace")
	}
	if got := store.GetString(FieldPermissionMode); got != "default" {
		t.Errorf("initial permission mode = %q, want %q", got, "default")
	}
	if got := store.Get(FieldConversationStatus); got != StatusIdle {
		t.Errorf("initial conversation status = %v, want %v", got, StatusIdle)
	}
	if got := store.GetInt(FieldInputTokens); got != 0 {
		t.Errorf("initial input tokens = %d, want 0", got)
	}
	if got := store.GetInt(FieldOutputTokens); got != 0 {
		t.Errorf("initial output tokens = %d, want 0", got)
	}
}

func TestBridgeAdapter_ConversationFlow(t *testing.T) {
	// Simulate a complete conversation flow:
	// idle -> thinking -> tool_running -> thinking -> idle
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.InitializeState(InitialState{
		Model:     "claude-sonnet-4-20250514",
		SessionID: "sess-flow",
	})
	adapter.Start()

	obs := store.Subscribe(TopicConversation)
	defer obs.Close()

	// Phase 1: Stream request starts (thinking)
	adapter.HandleEvent(EventFromStreamStart())
	expectStatus(t, store, StatusThinking)

	// Phase 2: Tool starts (tool_running)
	adapter.HandleEvent(EventFromToolProgress("Read", "tool-1", false))
	expectStatus(t, store, StatusToolRunning)

	// Phase 3: Tool completes (back to thinking)
	adapter.HandleEvent(EventFromToolProgress("Read", "tool-1", true))
	expectStatus(t, store, StatusThinking)

	// Phase 4: Terminal (idle)
	adapter.HandleEvent(EventFromTerminal())
	expectStatus(t, store, StatusIdle)

	// Observer should have received changes.
	received := drainObserver(obs, 100*time.Millisecond)
	if len(received) < 4 {
		t.Errorf("observer received %d changes, expected at least 4", len(received))
	}
}

func TestBridgeAdapter_ObserversReceiveUpdates(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.Start()

	// Subscribe to model changes.
	obs := store.Subscribe(TopicModel)
	defer obs.Close()

	adapter.HandleEvent(EventFromModelChange("new-model"))

	select {
	case change := <-obs.Changes():
		if change.Field != FieldCurrentModel {
			t.Errorf("expected FieldCurrentModel change, got %q", change.Field)
		}
		if change.NewValue != "new-model" {
			t.Errorf("expected new-model, got %v", change.NewValue)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("observer did not receive model change")
	}
}

func TestBridgeAdapter_ConcurrentEvents(t *testing.T) {
	store := NewStateStore()
	adapter := NewBridgeAdapter(store)
	adapter.InitializeState(InitialState{})
	adapter.Start()

	const goroutines = 20
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				adapter.HandleEvent(EventFromTokenUsage(1, 1))
				adapter.HandleEvent(EventFromTurnCount(id*iterations + i))
			}
		}(g)
	}

	wg.Wait()

	// Tokens should accumulate (goroutines * iterations * 1 each).
	expectedTokens := goroutines * iterations
	gotInput := store.GetInt(FieldInputTokens)
	gotOutput := store.GetInt(FieldOutputTokens)
	if gotInput != expectedTokens {
		t.Errorf("concurrent input tokens = %d, want %d", gotInput, expectedTokens)
	}
	if gotOutput != expectedTokens {
		t.Errorf("concurrent output tokens = %d, want %d", gotOutput, expectedTokens)
	}
}

// --- BridgeManager tests ---

func TestBridgeManager_NewManager(t *testing.T) {
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:          "claude-sonnet-4-20250514",
		SessionID:      "sess-test",
		CWD:            "/workspace",
		PermissionMode: "default",
	})

	if mgr == nil {
		t.Fatal("NewBridgeManager returned nil")
		return
	}
	if mgr.Store() == nil {
		t.Error("Store() returned nil")
	}
	if mgr.Adapter() == nil {
		t.Error("Adapter() returned nil")
	}
	if mgr.IsRunning() {
		t.Error("should not be running before Start()")
	}
}

func TestBridgeManager_StartStop(t *testing.T) {
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:     "claude-sonnet-4-20250514",
		SessionID: "sess-lifecycle",
	})

	mgr.Start()
	if !mgr.IsRunning() {
		t.Error("should be running after Start()")
	}

	// Double Start should be no-op.
	mgr.Start()
	if !mgr.IsRunning() {
		t.Error("should still be running after double Start()")
	}

	mgr.Stop()
	if mgr.IsRunning() {
		t.Error("should not be running after Stop()")
	}

	// Double Stop should be no-op.
	mgr.Stop()
}

func TestBridgeManager_InitializesState(t *testing.T) {
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:          "claude-sonnet-4-20250514",
		FallbackModel:  "claude-haiku",
		SessionID:      "sess-init",
		CWD:            "/workspace",
		PermissionMode: "plan",
	})
	mgr.Start()
	defer mgr.Stop()

	snap := mgr.Snapshot()
	if snap.GetString(FieldCurrentModel) != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", snap.GetString(FieldCurrentModel), "claude-sonnet-4-20250514")
	}
	if snap.GetString(FieldFallbackModel) != "claude-haiku" {
		t.Errorf("fallback = %q, want %q", snap.GetString(FieldFallbackModel), "claude-haiku")
	}
	if snap.GetString(FieldSessionID) != "sess-init" {
		t.Errorf("session = %q, want %q", snap.GetString(FieldSessionID), "sess-init")
	}
	if snap.GetString(FieldSessionCWD) != "/workspace" {
		t.Errorf("cwd = %q, want %q", snap.GetString(FieldSessionCWD), "/workspace")
	}
	if snap.GetString(FieldPermissionMode) != "plan" {
		t.Errorf("permission mode = %q, want %q", snap.GetString(FieldPermissionMode), "plan")
	}
	if snap.Get(FieldConversationStatus) != StatusIdle {
		t.Errorf("conversation status = %v, want %v", snap.Get(FieldConversationStatus), StatusIdle)
	}
}

func TestBridgeManager_HandleEvent(t *testing.T) {
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:     "initial-model",
		SessionID: "sess-events",
	})
	mgr.Start()
	defer mgr.Stop()

	mgr.HandleEvent(EventFromStreamStart())

	snap := mgr.Snapshot()
	if snap.Get(FieldConversationStatus) != StatusThinking {
		t.Errorf("status = %v, want %v", snap.Get(FieldConversationStatus), StatusThinking)
	}
}

func TestBridgeManager_ClientIntegration(t *testing.T) {
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:     "claude-sonnet-4-20250514",
		SessionID: "sess-client",
	})
	mgr.Start()
	defer mgr.Stop()

	client := mgr.RegisterClient(RegisterClientOpts{
		ID:     "tui-1",
		Type:   ClientTypeTUI,
		Topics: []Topic{TopicConversation, TopicModel},
	})

	if client == nil {
		t.Fatal("RegisterClient returned nil")
		return
	}

	// Emit a model change — client should observe it.
	mgr.HandleEvent(EventFromModelChange("new-model"))

	obs := client.Observer()
	select {
	case change := <-obs.Changes():
		if change.Field != FieldCurrentModel {
			t.Errorf("client received %q, want FieldCurrentModel", change.Field)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("client did not receive model change")
	}

	mgr.UnregisterClient("tui-1")
	if mgr.Store().ClientCount() != 0 {
		t.Error("client should be unregistered")
	}
}

func TestBridgeManager_Subscribe(t *testing.T) {
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:     "claude-sonnet-4-20250514",
		SessionID: "sess-sub",
	})
	mgr.Start()
	defer mgr.Stop()

	obs := mgr.Subscribe(TopicTokens)
	defer obs.Close()

	mgr.HandleEvent(EventFromTokenUsage(500, 200))

	select {
	case change := <-obs.Changes():
		if change.Topic != TopicTokens {
			t.Errorf("expected TopicTokens, got %q", change.Topic)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subscriber did not receive token change")
	}
}

func TestBridgeManager_FullConversationScenario(t *testing.T) {
	mgr := NewBridgeManager(BridgeManagerConfig{
		Model:          "claude-sonnet-4-20250514",
		SessionID:      "sess-scenario",
		CWD:            "/project",
		PermissionMode: "default",
	})
	mgr.Start()
	defer mgr.Stop()

	// Subscribe to all topics.
	obs := mgr.Subscribe()
	defer obs.Close()

	// Simulate a full conversation:
	// 1. Stream starts
	mgr.HandleEvent(EventFromStreamStart())
	// 2. Tool runs
	mgr.HandleEvent(EventFromToolProgress("Bash", "t1", false))
	// 3. Token usage from tool
	mgr.HandleEvent(EventFromTokenUsage(500, 200))
	// 4. Tool completes
	mgr.HandleEvent(EventFromToolProgress("Bash", "t1", true))
	// 5. Another stream request
	mgr.HandleEvent(EventFromStreamStart())
	// 6. Turn update
	mgr.HandleEvent(EventFromTurnCount(2))
	// 7. Terminal
	mgr.HandleEvent(EventFromTerminal())

	// Verify final state.
	snap := mgr.Snapshot()

	if snap.Get(FieldConversationStatus) != StatusIdle {
		t.Errorf("final status = %v, want idle", snap.Get(FieldConversationStatus))
	}
	if snap.GetInt(FieldInputTokens) != 500 {
		t.Errorf("final input tokens = %d, want 500", snap.GetInt(FieldInputTokens))
	}
	if snap.GetInt(FieldOutputTokens) != 200 {
		t.Errorf("final output tokens = %d, want 200", snap.GetInt(FieldOutputTokens))
	}
	if snap.GetInt(FieldTurnCount) != 2 {
		t.Errorf("final turn count = %d, want 2", snap.GetInt(FieldTurnCount))
	}

	// Observer should have received multiple changes.
	received := drainObserver(obs, 100*time.Millisecond)
	if len(received) < 5 {
		t.Errorf("observer received %d changes, expected at least 5", len(received))
	}
}

// --- Helpers ---

func expectStatus(t *testing.T, store *StateStore, expected ConversationStatus) {
	t.Helper()
	got := store.Get(FieldConversationStatus)
	if got != expected {
		t.Errorf("conversation status = %v, want %v", got, expected)
	}
}

func drainObserver(obs *Observer, timeout time.Duration) []StateChange {
	var received []StateChange
	timer := time.After(timeout)
	for {
		select {
		case c, ok := <-obs.Changes():
			if !ok {
				return received
			}
			received = append(received, c)
		case <-timer:
			return received
		}
	}
}
