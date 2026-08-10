package mcp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// ReconnectConfig Tests
// =============================================================================

func TestDefaultReconnectConfig(t *testing.T) {
	cfg := DefaultReconnectConfig()

	if cfg.InitialBackoff != 1*time.Second {
		t.Fatalf("expected initial backoff 1s, got %v", cfg.InitialBackoff)
	}
	if cfg.MaxBackoff != 30*time.Second {
		t.Fatalf("expected max backoff 30s, got %v", cfg.MaxBackoff)
	}
	if cfg.BackoffFactor != 2.0 {
		t.Fatalf("expected backoff factor 2.0, got %v", cfg.BackoffFactor)
	}
	if cfg.MaxAttempts != 10 {
		t.Fatalf("expected max attempts 10, got %d", cfg.MaxAttempts)
	}
	if cfg.HealthCheckInterval != 30*time.Second {
		t.Fatalf("expected health interval 30s, got %v", cfg.HealthCheckInterval)
	}
}

// =============================================================================
// calculateBackoff Tests
// =============================================================================

func TestCalculateBackoff_FirstAttempt(t *testing.T) {
	backoff := calculateBackoff(1, 1*time.Second, 30*time.Second, 2.0)
	if backoff != 1*time.Second {
		t.Fatalf("expected 1s for first attempt, got %v", backoff)
	}
}

func TestCalculateBackoff_ExponentialGrowth(t *testing.T) {
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
	}

	for _, tt := range tests {
		backoff := calculateBackoff(tt.attempt, 1*time.Second, 30*time.Second, 2.0)
		if backoff != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, backoff)
		}
	}
}

func TestCalculateBackoff_CapsAtMax(t *testing.T) {
	backoff := calculateBackoff(10, 1*time.Second, 30*time.Second, 2.0)
	if backoff != 30*time.Second {
		t.Fatalf("expected capped at 30s, got %v", backoff)
	}
}

func TestCalculateBackoff_ZeroAttempt(t *testing.T) {
	backoff := calculateBackoff(0, 1*time.Second, 30*time.Second, 2.0)
	if backoff != 1*time.Second {
		t.Fatalf("expected initial backoff for attempt 0, got %v", backoff)
	}
}

// =============================================================================
// reconnectionManager Tests
// =============================================================================

func TestReconnectionManager_StartStop(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})
	config := ReconnectConfig{
		InitialBackoff:      100 * time.Millisecond,
		MaxBackoff:          1 * time.Second,
		BackoffFactor:       2.0,
		MaxAttempts:         3,
		HealthCheckInterval: 0, // Disable health check for this test.
	}

	rm := newReconnectionManager(client, config)

	if rm.IsActive() {
		t.Fatal("expected manager to be inactive before Start")
	}

	rm.Start(context.Background())
	if !rm.IsActive() {
		t.Fatal("expected manager to be active after Start")
	}

	rm.Stop()
	if rm.IsActive() {
		t.Fatal("expected manager to be inactive after Stop")
	}
}

func TestReconnectionManager_AddRemoveSubscription(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})
	config := DefaultReconnectConfig()

	rm := newReconnectionManager(client, config)

	rm.AddSubscription("file:///test.txt")
	rm.AddSubscription("file:///other.txt")

	// Duplicate should not add.
	rm.AddSubscription("file:///test.txt")

	rm.mu.Lock()
	count := len(rm.subscriptions)
	rm.mu.Unlock()

	if count != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", count)
	}

	rm.RemoveSubscription("file:///test.txt")

	rm.mu.Lock()
	count = len(rm.subscriptions)
	rm.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 subscription after removal, got %d", count)
	}
}

func TestReconnectionManager_RemoveNonexistent(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})
	config := DefaultReconnectConfig()
	rm := newReconnectionManager(client, config)

	rm.AddSubscription("file:///test.txt")
	rm.RemoveSubscription("file:///nonexistent.txt")

	rm.mu.Lock()
	count := len(rm.subscriptions)
	rm.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 subscription unchanged, got %d", count)
	}
}

func TestReconnectionManager_State(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})
	config := DefaultReconnectConfig()
	rm := newReconnectionManager(client, config)

	state := rm.State()
	if state.Attempts != 0 {
		t.Fatalf("expected 0 attempts initially, got %d", state.Attempts)
	}
	if state.Connected {
		t.Fatal("expected not connected initially")
	}
	if state.LastError != nil {
		t.Fatal("expected nil error initially")
		return
	}
}

func TestReconnectionManager_TriggerReconnect_NotActive(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})
	config := DefaultReconnectConfig()
	rm := newReconnectionManager(client, config)

	// Should not start reconnecting if not active.
	rm.TriggerReconnect(context.Background(), fmt.Errorf("test error"))

	state := rm.State()
	if state.Attempts != 0 {
		t.Fatal("expected no attempts when not active")
	}
}

func TestReconnectionManager_ReconnectLoop_Callbacks(t *testing.T) {
	// Create a mock client that fails to reconnect.
	client := NewMCPClient(ServerConfig{
		Name:    "test",
		Type:    "stdio",
		Command: "/nonexistent/binary",
	})

	var reconnectingCalls atomic.Int32
	var failedCalled atomic.Bool

	config := ReconnectConfig{
		InitialBackoff:      10 * time.Millisecond,
		MaxBackoff:          50 * time.Millisecond,
		BackoffFactor:       2.0,
		MaxAttempts:         3,
		HealthCheckInterval: 0,
		OnReconnecting: func(attempt int, delay time.Duration) {
			reconnectingCalls.Add(1)
		},
		OnReconnectFailed: func(lastErr error) {
			failedCalled.Store(true)
		},
	}

	rm := newReconnectionManager(client, config)
	rm.active.Store(true)

	rm.TriggerReconnect(context.Background(), fmt.Errorf("connection lost"))

	// Wait for reconnection loop to complete.
	time.Sleep(500 * time.Millisecond)

	if reconnectingCalls.Load() != 3 {
		t.Fatalf("expected 3 reconnecting callbacks, got %d", reconnectingCalls.Load())
	}
	if !failedCalled.Load() {
		t.Fatal("expected OnReconnectFailed to be called")
	}

	state := rm.State()
	if state.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", state.Attempts)
	}
}

func TestReconnectionManager_ReconnectLoop_ContextCancel(t *testing.T) {
	client := NewMCPClient(ServerConfig{
		Name:    "test",
		Type:    "stdio",
		Command: "/nonexistent/binary",
	})

	var attempts atomic.Int32

	config := ReconnectConfig{
		InitialBackoff:      100 * time.Millisecond,
		MaxBackoff:          1 * time.Second,
		BackoffFactor:       2.0,
		MaxAttempts:         10,
		HealthCheckInterval: 0,
		OnReconnecting: func(attempt int, delay time.Duration) {
			attempts.Add(1)
		},
	}

	rm := newReconnectionManager(client, config)
	rm.active.Store(true)

	ctx, cancel := context.WithCancel(context.Background())

	rm.TriggerReconnect(ctx, fmt.Errorf("connection lost"))

	// Cancel after a short delay.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for the goroutine to notice.
	time.Sleep(200 * time.Millisecond)

	// Should have been cancelled before completing all attempts.
	if attempts.Load() >= 10 {
		t.Fatal("expected reconnection to be cancelled before completing all attempts")
	}
}

func TestReconnectionManager_DeduplicateTriggers(t *testing.T) {
	client := NewMCPClient(ServerConfig{
		Name:    "test",
		Type:    "stdio",
		Command: "/nonexistent/binary",
	})

	var reconnectingCalls atomic.Int32

	config := ReconnectConfig{
		InitialBackoff:      50 * time.Millisecond,
		MaxBackoff:          100 * time.Millisecond,
		BackoffFactor:       2.0,
		MaxAttempts:         2,
		HealthCheckInterval: 0,
		OnReconnecting: func(attempt int, delay time.Duration) {
			reconnectingCalls.Add(1)
		},
	}

	rm := newReconnectionManager(client, config)
	rm.active.Store(true)

	// Trigger multiple times concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rm.TriggerReconnect(context.Background(), fmt.Errorf("test"))
		}()
	}
	wg.Wait()

	// Wait for the reconnection loop.
	time.Sleep(500 * time.Millisecond)

	// Only one loop should run (max 2 attempts from that loop).
	if reconnectingCalls.Load() > 2 {
		t.Fatalf("expected at most 2 reconnecting calls (deduplication), got %d", reconnectingCalls.Load())
	}
}

// =============================================================================
// MCPClient Reconnection Integration Tests
// =============================================================================

func TestMCPClient_EnableDisableReconnect(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	config := ReconnectConfig{
		InitialBackoff:      1 * time.Second,
		MaxBackoff:          10 * time.Second,
		BackoffFactor:       2.0,
		MaxAttempts:         5,
		HealthCheckInterval: 0,
	}

	client.EnableReconnect(context.Background(), config)

	client.mu.Lock()
	hasRM := client.reconnManager != nil
	client.mu.Unlock()

	if !hasRM {
		t.Fatal("expected reconnManager to be set")
	}

	state := client.ReconnectState()
	if state == nil {
		t.Fatal("expected non-nil reconnect state")
		return
	}

	client.DisableReconnect()

	client.mu.Lock()
	rm := client.reconnManager
	client.mu.Unlock()

	if rm == nil {
		t.Fatal("reconnManager should still exist, just stopped")
		return
	}
	if rm.IsActive() {
		t.Fatal("expected reconnManager to be inactive after disable")
	}
}

func TestMCPClient_ReconnectState_NilWhenDisabled(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	state := client.ReconnectState()
	if state != nil {
		t.Fatal("expected nil state when reconnection not enabled")
		return
	}
}

func TestMCPClient_Ping_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	err := client.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error when pinging disconnected client")
		return
	}
	if err.Error() != "mcp: client is not connected" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMCPClient_Subscribe_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	err := client.Subscribe(context.Background(), "file:///test.txt")
	if err == nil {
		t.Fatal("expected error when subscribing on disconnected client")
		return
	}
	if err.Error() != "mcp: client is not connected" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMCPClient_Unsubscribe_NotConnected(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	err := client.Unsubscribe(context.Background(), "file:///test.txt")
	if err == nil {
		t.Fatal("expected error when unsubscribing on disconnected client")
		return
	}
	if err.Error() != "mcp: client is not connected" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// =============================================================================
// Backoff Sequence Tests
// =============================================================================

func TestBackoffSequence(t *testing.T) {
	initial := 1 * time.Second
	max := 30 * time.Second
	factor := 2.0

	expected := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // Capped.
		30 * time.Second, // Stays at max.
	}

	for i, exp := range expected {
		got := calculateBackoff(i+1, initial, max, factor)
		if got != exp {
			t.Errorf("attempt %d: expected %v, got %v", i+1, exp, got)
		}
	}
}

func TestBackoffSequence_CustomFactor(t *testing.T) {
	initial := 500 * time.Millisecond
	max := 10 * time.Second
	factor := 1.5

	b1 := calculateBackoff(1, initial, max, factor)
	b2 := calculateBackoff(2, initial, max, factor)
	b3 := calculateBackoff(3, initial, max, factor)

	if b1 != 500*time.Millisecond {
		t.Fatalf("expected 500ms for attempt 1, got %v", b1)
	}
	if b2 != 750*time.Millisecond {
		t.Fatalf("expected 750ms for attempt 2, got %v", b2)
	}
	// 500ms * 1.5^2 = 1125ms
	if b3 != 1125*time.Millisecond {
		t.Fatalf("expected 1125ms for attempt 3, got %v", b3)
	}
}
