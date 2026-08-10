package mcp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// NotificationDispatcher Tests
// =============================================================================

func TestNewNotificationDispatcher(t *testing.T) {
	d := NewNotificationDispatcher()
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
		return
	}
	if d.HasHandlers(NotificationProgress) {
		t.Fatal("expected no handlers initially")
	}
}

func TestNotificationDispatcher_Register(t *testing.T) {
	d := NewNotificationDispatcher()

	called := make(chan struct{}, 1)
	d.Register(NotificationProgress, func(ctx context.Context, notification any) {
		called <- struct{}{}
	})

	if !d.HasHandlers(NotificationProgress) {
		t.Fatal("expected handler to be registered")
	}
	if d.HandlerCount(NotificationProgress) != 1 {
		t.Fatalf("expected 1 handler, got %d", d.HandlerCount(NotificationProgress))
	}
}

func TestNotificationDispatcher_RegisterMultiple(t *testing.T) {
	d := NewNotificationDispatcher()

	d.Register(NotificationProgress, func(ctx context.Context, notification any) {})
	d.Register(NotificationProgress, func(ctx context.Context, notification any) {})
	d.Register(NotificationResourceUpdated, func(ctx context.Context, notification any) {})

	if d.HandlerCount(NotificationProgress) != 2 {
		t.Fatalf("expected 2 progress handlers, got %d", d.HandlerCount(NotificationProgress))
	}
	if d.HandlerCount(NotificationResourceUpdated) != 1 {
		t.Fatalf("expected 1 resource handler, got %d", d.HandlerCount(NotificationResourceUpdated))
	}
}

func TestNotificationDispatcher_Unregister(t *testing.T) {
	d := NewNotificationDispatcher()

	unregister := d.Register(NotificationProgress, func(ctx context.Context, notification any) {})

	if d.HandlerCount(NotificationProgress) != 1 {
		t.Fatalf("expected 1 handler, got %d", d.HandlerCount(NotificationProgress))
	}

	unregister()

	if d.HandlerCount(NotificationProgress) != 0 {
		t.Fatalf("expected 0 handlers after unregister, got %d", d.HandlerCount(NotificationProgress))
	}
}

func TestNotificationDispatcher_Dispatch(t *testing.T) {
	d := NewNotificationDispatcher()

	var received atomic.Value
	done := make(chan struct{})

	d.Register(NotificationProgress, func(ctx context.Context, notification any) {
		received.Store(notification)
		close(done)
	})

	notification := &ProgressNotification{
		Progress: 50,
		Total:    100,
		Message:  "halfway",
	}

	d.Dispatch(context.Background(), NotificationProgress, notification)

	select {
	case <-done:
		got := received.Load().(*ProgressNotification)
		if got.Progress != 50 || got.Total != 100 || got.Message != "halfway" {
			t.Fatalf("unexpected notification: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification dispatch")
	}
}

func TestNotificationDispatcher_DispatchMultipleHandlers(t *testing.T) {
	d := NewNotificationDispatcher()

	var count atomic.Int32
	done := make(chan struct{})

	for i := 0; i < 3; i++ {
		d.Register(NotificationProgress, func(ctx context.Context, notification any) {
			if count.Add(1) == 3 {
				close(done)
			}
		})
	}

	d.Dispatch(context.Background(), NotificationProgress, &ProgressNotification{})

	select {
	case <-done:
		if count.Load() != 3 {
			t.Fatalf("expected 3 handler calls, got %d", count.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for all handlers")
	}
}

func TestNotificationDispatcher_DispatchNonBlocking(t *testing.T) {
	d := NewNotificationDispatcher()

	// Register a handler that blocks.
	d.Register(NotificationProgress, func(ctx context.Context, notification any) {
		time.Sleep(10 * time.Second)
	})

	// Dispatch should return immediately (non-blocking).
	start := time.Now()
	d.Dispatch(context.Background(), NotificationProgress, &ProgressNotification{})
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("dispatch took %v, expected non-blocking", elapsed)
	}
}

func TestNotificationDispatcher_DispatchNoHandlers(t *testing.T) {
	d := NewNotificationDispatcher()

	// Should not panic when there are no handlers.
	d.Dispatch(context.Background(), NotificationProgress, &ProgressNotification{})
}

func TestNotificationDispatcher_DispatchWrongType(t *testing.T) {
	d := NewNotificationDispatcher()

	called := make(chan struct{}, 1)
	d.Register(NotificationProgress, func(ctx context.Context, notification any) {
		called <- struct{}{}
	})

	// Dispatch a different type - progress handler should not be called.
	d.Dispatch(context.Background(), NotificationResourceUpdated, &ResourceUpdatedNotification{})

	select {
	case <-called:
		t.Fatal("handler should not have been called for different notification type")
	case <-time.After(100 * time.Millisecond):
		// Expected - no call.
	}
}

func TestNotificationDispatcher_Clear(t *testing.T) {
	d := NewNotificationDispatcher()

	d.Register(NotificationProgress, func(ctx context.Context, notification any) {})
	d.Register(NotificationResourceUpdated, func(ctx context.Context, notification any) {})
	d.Register(NotificationLogging, func(ctx context.Context, notification any) {})

	d.Clear()

	if d.HasHandlers(NotificationProgress) {
		t.Fatal("expected no progress handlers after clear")
	}
	if d.HasHandlers(NotificationResourceUpdated) {
		t.Fatal("expected no resource handlers after clear")
	}
	if d.HasHandlers(NotificationLogging) {
		t.Fatal("expected no logging handlers after clear")
	}
}

func TestNotificationDispatcher_ConcurrentAccess(t *testing.T) {
	d := NewNotificationDispatcher()

	var wg sync.WaitGroup

	// Concurrent registrations.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Register(NotificationProgress, func(ctx context.Context, notification any) {})
		}()
	}

	// Concurrent dispatches.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Dispatch(context.Background(), NotificationProgress, &ProgressNotification{})
		}()
	}

	// Concurrent reads.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = d.HasHandlers(NotificationProgress)
			_ = d.HandlerCount(NotificationProgress)
		}()
	}

	wg.Wait()
}

func TestNotificationDispatcher_ResourceUpdated(t *testing.T) {
	d := NewNotificationDispatcher()

	var received atomic.Value
	done := make(chan struct{})

	d.Register(NotificationResourceUpdated, func(ctx context.Context, notification any) {
		received.Store(notification)
		close(done)
	})

	notification := &ResourceUpdatedNotification{URI: "file:///test.txt"}
	d.Dispatch(context.Background(), NotificationResourceUpdated, notification)

	select {
	case <-done:
		got := received.Load().(*ResourceUpdatedNotification)
		if got.URI != "file:///test.txt" {
			t.Fatalf("expected URI %q, got %q", "file:///test.txt", got.URI)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resource notification")
	}
}

func TestNotificationDispatcher_Logging(t *testing.T) {
	d := NewNotificationDispatcher()

	var received atomic.Value
	done := make(chan struct{})

	d.Register(NotificationLogging, func(ctx context.Context, notification any) {
		received.Store(notification)
		close(done)
	})

	notification := &LoggingNotification{
		Level:  "warning",
		Logger: "test-logger",
		Data:   "something happened",
	}
	d.Dispatch(context.Background(), NotificationLogging, notification)

	select {
	case <-done:
		got := received.Load().(*LoggingNotification)
		if got.Level != "warning" {
			t.Fatalf("expected level %q, got %q", "warning", got.Level)
		}
		if got.Logger != "test-logger" {
			t.Fatalf("expected logger %q, got %q", "test-logger", got.Logger)
		}
		if got.Data != "something happened" {
			t.Fatalf("expected data %q, got %v", "something happened", got.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for logging notification")
	}
}

func TestNotificationTypes_Constants(t *testing.T) {
	// Verify notification type constants match MCP protocol.
	if NotificationProgress != "notifications/progress" {
		t.Fatalf("unexpected progress type: %s", NotificationProgress)
	}
	if NotificationResourceUpdated != "notifications/resources/updated" {
		t.Fatalf("unexpected resource updated type: %s", NotificationResourceUpdated)
	}
	if NotificationLogging != "notifications/message" {
		t.Fatalf("unexpected logging type: %s", NotificationLogging)
	}
	if NotificationCancelled != "notifications/cancelled" {
		t.Fatalf("unexpected cancelled type: %s", NotificationCancelled)
	}
}

// =============================================================================
// MCPClient Notification Integration Tests
// =============================================================================

func TestMCPClient_Notifications_ReturnsDispatcher(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	dispatcher := client.Notifications()
	if dispatcher == nil {
		t.Fatal("expected non-nil notification dispatcher")
		return
	}

	// Should be the same instance on repeated calls.
	if client.Notifications() != dispatcher {
		t.Fatal("expected same dispatcher instance")
	}
}

func TestMCPClient_Notifications_RegisterBeforeConnect(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})

	// Should be able to register handlers before connecting.
	unregister := client.Notifications().Register(NotificationProgress, func(ctx context.Context, notification any) {})
	if !client.Notifications().HasHandlers(NotificationProgress) {
		t.Fatal("expected handler to be registered")
	}
	unregister()
}
