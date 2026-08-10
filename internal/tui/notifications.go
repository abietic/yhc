package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// NotificationSeverity represents the severity level of a notification.
type NotificationSeverity int

const (
	NotifyInfo    NotificationSeverity = iota // informational (default)
	NotifySuccess                             // positive outcome
	NotifyWarning                             // attention required
	NotifyError                               // error occurred
)

// Notification represents a single toast notification.
type Notification struct {
	Message   string
	Severity  NotificationSeverity
	CreatedAt time.Time
	Duration  time.Duration // auto-dismiss duration; 0 = use default
}

// NotificationDeliveryMsg transfers one immutable notification value into the
// Bubble Tea update loop. CreatedAt is intentionally assigned by App.Update.
type NotificationDeliveryMsg struct {
	Message  string
	Severity NotificationSeverity
}

// NotificationStack manages a stack of toast notifications.
// Newest notifications are appended to the end; oldest are dismissed first.
type NotificationStack struct {
	items      []Notification
	maxVisible int           // max notifications shown at once
	defaultTTL time.Duration // default auto-dismiss duration
}

const (
	defaultNotificationTTL  = 5 * time.Second
	defaultMaxVisibleNotify = 3
)

// NewNotificationStack creates a notification stack with default settings.
func NewNotificationStack() *NotificationStack {
	return &NotificationStack{
		maxVisible: defaultMaxVisibleNotify,
		defaultTTL: defaultNotificationTTL,
	}
}

// PushAt adds a new notification to the stack at the App-owned time.
// If the stack exceeds maxVisible, the oldest is evicted immediately.
func (ns *NotificationStack) PushAt(now time.Time, msg string, severity NotificationSeverity) {
	ns.PushWithDurationAt(now, msg, severity, ns.defaultTTL)
}

// PushWithDurationAt adds a notification with a custom auto-dismiss duration
// at the App-owned time.
func (ns *NotificationStack) PushWithDurationAt(
	now time.Time,
	msg string,
	severity NotificationSeverity,
	dur time.Duration,
) {
	ns.items = append(ns.items, Notification{
		Message:   msg,
		Severity:  severity,
		CreatedAt: now,
		Duration:  dur,
	})
	for len(ns.items) > ns.maxVisible {
		ns.items = ns.items[1:]
	}
}

// PruneAt removes every notification whose deadline is at or before now.
func (ns *NotificationStack) PruneAt(now time.Time) {
	pruned := ns.items[:0]
	for _, n := range ns.items {
		if now.Before(ns.deadline(n)) {
			pruned = append(pruned, n)
		}
	}
	clear(ns.items[len(pruned):])
	ns.items = pruned
}

// EarliestDeadline returns the earliest active notification deadline.
func (ns *NotificationStack) EarliestDeadline() (time.Time, bool) {
	if len(ns.items) == 0 {
		return time.Time{}, false
	}
	earliest := ns.deadline(ns.items[0])
	for _, n := range ns.items[1:] {
		if deadline := ns.deadline(n); deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest, true
}

func (ns *NotificationStack) deadline(n Notification) time.Time {
	ttl := n.Duration
	if ttl == 0 {
		ttl = ns.defaultTTL
	}
	return n.CreatedAt.Add(ttl)
}

// Active returns a defensive copy of the currently visible notifications.
func (ns *NotificationStack) Active() []Notification {
	return append([]Notification(nil), ns.items...)
}

// HasActive returns true if there are any visible notifications.
func (ns *NotificationStack) HasActive() bool {
	return len(ns.items) > 0
}

// Clear removes all notifications.
func (ns *NotificationStack) Clear() {
	ns.items = nil
}

// Count returns the number of active notifications.
func (ns *NotificationStack) Count() int {
	return len(ns.items)
}

// Render renders the notification stack as a multi-line string.
// Each notification is rendered with severity-appropriate styling.
func (ns *NotificationStack) Render(styles Styles, width int) string {
	return ns.RenderWithEnvironment(defaultRenderEnvironment(styles), width)
}

func (ns *NotificationStack) RenderWithEnvironment(
	env RenderEnvironment,
	width int,
) string {
	env = env.normalized()
	active := ns.Active()
	if len(active) == 0 {
		return ""
	}

	var lines []string
	for _, n := range active {
		lines = append(lines, renderNotificationLine(env, n, width, ""))
	}

	return strings.Join(lines, "\n")
}

// RenderSingleLine renders the most recent notification as a single line
// (for embedding in the status bar when space is limited).
func (ns *NotificationStack) RenderSingleLine(styles Styles, width int) string {
	return ns.RenderSingleLineWithEnvironment(defaultRenderEnvironment(styles), width)
}

func (ns *NotificationStack) RenderSingleLineWithEnvironment(
	env RenderEnvironment,
	width int,
) string {
	env = env.normalized()
	active := ns.Active()
	if len(active) == 0 {
		return ""
	}
	// Show most recent notification
	n := active[len(active)-1]
	suffix := ""
	if len(active) > 1 {
		suffix = fmt.Sprintf(" (+%d)", len(active)-1)
	}
	return renderNotificationLine(env, n, width, suffix)
}

func renderNotificationLine(
	env RenderEnvironment,
	notification Notification,
	width int,
	suffix string,
) string {
	profile := env.profile
	icon, style := notificationStyle(env.styles, notification.Severity)
	prefix := icon + " "
	prefixWidth := profile.measure(prefix, 0)
	suffixWidth := profile.measure(suffix, prefixWidth)
	messageWidth := max(0, width-prefixWidth-suffixWidth)
	message := contentEllipsize(
		profile,
		notification.Message,
		messageWidth,
		prefixWidth,
		"\u2026",
	)
	return contentProjectLine(
		profile,
		prefix+style.Render(message)+suffix,
		width,
		0,
	)
}

// notificationStyle returns the icon and text style for a severity level.
func notificationStyle(styles Styles, severity NotificationSeverity) (string, lipgloss.Style) {
	switch severity {
	case NotifySuccess:
		return styles.ToolSuccess.Render("\u2713"), styles.ToolSuccess
	case NotifyWarning:
		return styles.Warning.Render("\u26a0"), styles.Warning
	case NotifyError:
		return styles.Error.Render("\u2717"), styles.Error
	default: // NotifyInfo
		return styles.Subtle.Render("\u2022"), styles.Subtle
	}
}
