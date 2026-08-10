package mcp

import (
	"context"
	"sync"
)

// NotificationType identifies categories of MCP notifications.
type NotificationType string

const (
	// NotificationProgress is sent by the server to report progress on long-running operations.
	NotificationProgress NotificationType = "notifications/progress"
	// NotificationResourceUpdated is sent by the server when a subscribed resource changes.
	NotificationResourceUpdated NotificationType = "notifications/resources/updated"
	// NotificationLogging is sent by the server with log messages.
	NotificationLogging NotificationType = "notifications/message"
	// NotificationCancelled is sent by the client to cancel an in-flight request.
	NotificationCancelled NotificationType = "notifications/cancelled"
)

// ProgressNotification contains progress information from the server.
type ProgressNotification struct {
	// ProgressToken identifies which request this progress belongs to.
	ProgressToken any `json:"progressToken"`
	// Progress is the current progress value.
	Progress float64 `json:"progress"`
	// Total is the total expected progress (0 means unknown).
	Total float64 `json:"total,omitempty"`
	// Message is an optional human-readable description of progress.
	Message string `json:"message,omitempty"`
}

// ResourceUpdatedNotification indicates a resource has changed.
type ResourceUpdatedNotification struct {
	// URI is the resource that was updated.
	URI string `json:"uri"`
}

// LoggingNotification contains a log message from the server.
type LoggingNotification struct {
	// Level is the severity level (e.g., "info", "warning", "error").
	Level string `json:"level"`
	// Logger is the optional logger name.
	Logger string `json:"logger,omitempty"`
	// Data is the log payload.
	Data any `json:"data"`
}

// NotificationHandler is a callback for handling a specific notification type.
// Handlers are invoked in separate goroutines and should be non-blocking.
type NotificationHandler func(ctx context.Context, notification any)

// NotificationDispatcher manages registration and dispatch of notification handlers.
// It is goroutine-safe and dispatches notifications non-blocking in separate goroutines.
type NotificationDispatcher struct {
	mu       sync.RWMutex
	handlers map[NotificationType][]NotificationHandler
}

// NewNotificationDispatcher creates a new NotificationDispatcher.
func NewNotificationDispatcher() *NotificationDispatcher {
	return &NotificationDispatcher{
		handlers: make(map[NotificationType][]NotificationHandler),
	}
}

// Register adds a handler for a specific notification type.
// Multiple handlers can be registered for the same type.
// Returns a function to unregister the handler.
func (d *NotificationDispatcher) Register(notificationType NotificationType, handler NotificationHandler) func() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.handlers[notificationType] = append(d.handlers[notificationType], handler)

	// Return an unregister function.
	idx := len(d.handlers[notificationType]) - 1
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		handlers := d.handlers[notificationType]
		if idx < len(handlers) {
			// Remove handler by swapping with last and truncating.
			handlers[idx] = handlers[len(handlers)-1]
			d.handlers[notificationType] = handlers[:len(handlers)-1]
		}
	}
}

// Dispatch sends a notification to all registered handlers for the given type.
// Each handler is invoked in a separate goroutine (non-blocking).
func (d *NotificationDispatcher) Dispatch(ctx context.Context, notificationType NotificationType, notification any) {
	d.mu.RLock()
	handlers := make([]NotificationHandler, len(d.handlers[notificationType]))
	copy(handlers, d.handlers[notificationType])
	d.mu.RUnlock()

	for _, handler := range handlers {
		h := handler
		go h(ctx, notification)
	}
}

// HasHandlers returns true if there are registered handlers for the given type.
func (d *NotificationDispatcher) HasHandlers(notificationType NotificationType) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.handlers[notificationType]) > 0
}

// HandlerCount returns the number of registered handlers for a given type.
func (d *NotificationDispatcher) HandlerCount(notificationType NotificationType) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.handlers[notificationType])
}

// Clear removes all handlers for all notification types.
func (d *NotificationDispatcher) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers = make(map[NotificationType][]NotificationHandler)
}
