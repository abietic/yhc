// Package notify provides a notification system for alerting users about
// important engine events such as task completion, errors, and permission requests.
package notify

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Notification types
// ---------------------------------------------------------------------------

// NotificationType classifies the kind of notification being sent.
type NotificationType string

const (
	// NotificationCompletion indicates a task or session has completed.
	NotificationCompletion NotificationType = "completion"
	// NotificationError indicates an error occurred.
	NotificationError NotificationType = "error"
	// NotificationPermissionNeeded indicates a tool requires user permission.
	NotificationPermissionNeeded NotificationType = "permission_needed"
	// NotificationIdle indicates the agent is idle and waiting for input.
	NotificationIdle NotificationType = "idle"
	// NotificationCompact indicates a compaction event occurred.
	NotificationCompact NotificationType = "compact"
)

// ---------------------------------------------------------------------------
// Notification struct
// ---------------------------------------------------------------------------

// Notification represents a single notification event.
type Notification struct {
	Type      NotificationType
	Title     string
	Body      string
	Urgent    bool
	Timestamp time.Time
	SessionID string
}

// ---------------------------------------------------------------------------
// Handler interface
// ---------------------------------------------------------------------------

// NotificationHandler is the interface that notification backends must implement.
type NotificationHandler interface {
	// Notify delivers a notification to the user.
	Notify(ctx context.Context, n *Notification) error
	// IsSupported returns true if this handler can operate in the current environment.
	IsSupported() bool
}

// ExternalNotificationHandler marks handlers that interrupt the user's
// desktop or terminal outside the in-app notification surface.
type ExternalNotificationHandler interface {
	NotificationHandler
	IsExternalNotificationHandler() bool
}

// ---------------------------------------------------------------------------
// TerminalBellHandler
// ---------------------------------------------------------------------------

// TerminalBellHandler sends a terminal bell character (\a) to stdout.
type TerminalBellHandler struct{}

// Notify writes the BEL character to stdout.
func (h *TerminalBellHandler) Notify(ctx context.Context, n *Notification) error {
	_, err := fmt.Fprint(os.Stdout, "\a")
	return err
}

// IsSupported returns true when stdout is connected to a terminal.
func (h *TerminalBellHandler) IsSupported() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// IsExternalNotificationHandler marks BEL as an external interruption.
func (h *TerminalBellHandler) IsExternalNotificationHandler() bool { return true }

// ---------------------------------------------------------------------------
// OSNotifyHandler
// ---------------------------------------------------------------------------

// OSNotifyHandler sends desktop notifications using OS-native tools.
// On Linux it uses notify-send; on macOS it uses osascript.
type OSNotifyHandler struct{}

// Notify dispatches a desktop notification via the OS notification system.
func (h *OSNotifyHandler) Notify(ctx context.Context, n *Notification) error {
	switch runtime.GOOS {
	case "linux":
		args := []string{n.Title, n.Body}
		if n.Urgent {
			args = append([]string{"-u", "critical"}, args...)
		}
		cmd := exec.CommandContext(ctx, "notify-send", args...)
		return cmd.Run()
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, n.Body, n.Title)
		cmd := exec.CommandContext(ctx, "osascript", "-e", script)
		return cmd.Run()
	default:
		return fmt.Errorf("os notifications not supported on %s", runtime.GOOS)
	}
}

// IsSupported returns true if the OS notification tool is available.
func (h *OSNotifyHandler) IsSupported() bool {
	switch runtime.GOOS {
	case "linux":
		_, err := exec.LookPath("notify-send")
		return err == nil
	case "darwin":
		_, err := exec.LookPath("osascript")
		return err == nil
	default:
		return false
	}
}

// IsExternalNotificationHandler marks OS notifications as external.
func (h *OSNotifyHandler) IsExternalNotificationHandler() bool { return true }

// ---------------------------------------------------------------------------
// LogHandler
// ---------------------------------------------------------------------------

// LogHandler writes notifications to a log file.
type LogHandler struct {
	Path   string
	mu     sync.Mutex
	logger *log.Logger
	file   *os.File
}

// NewLogHandler creates a LogHandler that appends to the given file path.
func NewLogHandler(path string) (*LogHandler, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("notify: open log file: %w", err)
	}
	return &LogHandler{
		Path:   path,
		file:   f,
		logger: log.New(f, "", 0),
	}, nil
}

// Notify appends a formatted notification entry to the log file.
func (h *LogHandler) Notify(ctx context.Context, n *Notification) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logger.Printf("[%s] %s | %s: %s (session=%s urgent=%v)",
		n.Timestamp.Format(time.RFC3339),
		n.Type,
		n.Title,
		n.Body,
		n.SessionID,
		n.Urgent,
	)
	return nil
}

// IsSupported always returns true since file logging is universally available.
func (h *LogHandler) IsSupported() bool {
	return true
}

// Close closes the underlying log file.
func (h *LogHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		return h.file.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// NotificationManager
// ---------------------------------------------------------------------------

// NotificationManager dispatches notifications to registered handlers.
type NotificationManager struct {
	handlers       []NotificationHandler
	mu             sync.RWMutex
	enabled        bool
	minLevel       NotificationType
	externalPolicy func() bool
}

// NewNotificationManager creates a new enabled NotificationManager with no handlers.
func NewNotificationManager() *NotificationManager {
	return &NotificationManager{
		enabled: true,
	}
}

// AddHandler registers a notification handler. Handlers that report
// IsSupported() == false are silently skipped during Send.
func (m *NotificationManager) AddHandler(h NotificationHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers = append(m.handlers, h)
}

// Send delivers a notification to all supported handlers.
// If the manager is disabled, Send returns nil immediately.
func (m *NotificationManager) Send(ctx context.Context, n *Notification) error {
	m.mu.RLock()
	if !m.enabled {
		m.mu.RUnlock()
		return nil
	}
	handlers := append([]NotificationHandler(nil), m.handlers...)
	externalPolicy := m.externalPolicy
	m.mu.RUnlock()

	if n.Timestamp.IsZero() {
		n.Timestamp = time.Now()
	}

	var firstErr error
	for _, h := range handlers {
		if external, ok := h.(ExternalNotificationHandler); ok &&
			external.IsExternalNotificationHandler() && externalPolicy != nil && !externalPolicy() {
			continue
		}
		if !h.IsSupported() {
			continue
		}
		if err := h.Notify(ctx, n); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SetExternalPolicy controls delivery to external handlers such as desktop
// notifications and BEL. In-app and log handlers remain unaffected.
func (m *NotificationManager) SetExternalPolicy(policy func() bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.externalPolicy = policy
}

// Enable enables notification delivery.
func (m *NotificationManager) Enable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = true
}

// Disable disables notification delivery. Send becomes a no-op.
func (m *NotificationManager) Disable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = false
}

// SetMinLevel sets the minimum notification type level for filtering.
func (m *NotificationManager) SetMinLevel(level NotificationType) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.minLevel = level
}

// ---------------------------------------------------------------------------
// Convenience functions
// ---------------------------------------------------------------------------

// NotifyCompletion sends a completion notification for the given session.
func NotifyCompletion(mgr *NotificationManager, sessionID, summary string) {
	if mgr == nil {
		return
	}
	_ = mgr.Send(context.Background(), &Notification{
		Type:      NotificationCompletion,
		Title:     "Task Completed",
		Body:      summary,
		Urgent:    false,
		Timestamp: time.Now(),
		SessionID: sessionID,
	})
}

// NotifyError sends an error notification for the given session.
func NotifyError(mgr *NotificationManager, sessionID string, err error) {
	if mgr == nil {
		return
	}
	_ = mgr.Send(context.Background(), &Notification{
		Type:      NotificationError,
		Title:     "Error",
		Body:      err.Error(),
		Urgent:    true,
		Timestamp: time.Now(),
		SessionID: sessionID,
	})
}

// NotifyPermissionNeeded sends a permission-needed notification for the given session.
func NotifyPermissionNeeded(mgr *NotificationManager, sessionID, toolName string) {
	if mgr == nil {
		return
	}
	_ = mgr.Send(context.Background(), &Notification{
		Type:      NotificationPermissionNeeded,
		Title:     "Permission Required",
		Body:      fmt.Sprintf("Tool %q requires user approval", toolName),
		Urgent:    true,
		Timestamp: time.Now(),
		SessionID: sessionID,
	})
}
