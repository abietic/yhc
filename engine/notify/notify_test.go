package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeHandler struct {
	supported bool
	err       error
	seen      []*Notification
}

type fakeExternalHandler struct {
	fakeHandler
}

func (h *fakeExternalHandler) IsExternalNotificationHandler() bool { return true }

func (h *fakeHandler) Notify(ctx context.Context, n *Notification) error {
	h.seen = append(h.seen, n)
	return h.err
}

func (h *fakeHandler) IsSupported() bool {
	return h.supported
}

func TestNotificationManagerSendDispatchesSupportedHandlers(t *testing.T) {
	mgr := NewNotificationManager()
	supported := &fakeHandler{supported: true}
	unsupported := &fakeHandler{supported: false}
	mgr.AddHandler(unsupported)
	mgr.AddHandler(supported)

	n := &Notification{Type: NotificationCompletion, Title: "Done", Body: "ok", SessionID: "s1"}
	if err := mgr.Send(context.Background(), n); err != nil {
		t.Fatalf("Send failed: %v", err)
		return
	}
	if len(unsupported.seen) != 0 {
		t.Fatalf("unsupported handler should be skipped, got %#v", unsupported.seen)
	}
	if len(supported.seen) != 1 || supported.seen[0].Title != "Done" {
		t.Fatalf("supported handler did not receive notification: %#v", supported.seen)
	}
	if n.Timestamp.IsZero() {
		t.Fatal("Send should set timestamp when missing")
	}
}

func TestNotificationManagerDisabledAndFirstError(t *testing.T) {
	mgr := NewNotificationManager()
	firstErr := errors.New("first")
	first := &fakeHandler{supported: true, err: firstErr}
	second := &fakeHandler{supported: true, err: errors.New("second")}
	mgr.AddHandler(first)
	mgr.AddHandler(second)

	mgr.Disable()
	if err := mgr.Send(context.Background(), &Notification{Title: "skip"}); err != nil {
		t.Fatalf("disabled Send should be nil, got %v", err)
		return
	}
	if len(first.seen) != 0 || len(second.seen) != 0 {
		t.Fatal("disabled manager should not call handlers")
	}

	mgr.Enable()
	err := mgr.Send(context.Background(), &Notification{Title: "fail"})
	if !errors.Is(err, firstErr) {
		t.Fatalf("expected first handler error, got %v", err)
	}
	if len(first.seen) != 1 || len(second.seen) != 1 {
		t.Fatalf("all supported handlers should be invoked even on error: first=%d second=%d", len(first.seen), len(second.seen))
	}
}

func TestNotificationManagerExternalPolicyDoesNotSuppressInAppHandlers(t *testing.T) {
	mgr := NewNotificationManager()
	external := &fakeExternalHandler{fakeHandler: fakeHandler{supported: true}}
	inApp := &fakeHandler{supported: true}
	mgr.AddHandler(external)
	mgr.AddHandler(inApp)

	allowExternal := false
	mgr.SetExternalPolicy(func() bool { return allowExternal })
	if err := mgr.Send(context.Background(), &Notification{Title: "focused"}); err != nil {
		t.Fatalf("Send while focused failed: %v", err)
	}
	if len(external.seen) != 0 || len(inApp.seen) != 1 {
		t.Fatalf("focused delivery mismatch: external=%d in-app=%d", len(external.seen), len(inApp.seen))
	}

	allowExternal = true
	if err := mgr.Send(context.Background(), &Notification{Title: "blurred"}); err != nil {
		t.Fatalf("Send while blurred failed: %v", err)
	}
	if len(external.seen) != 1 || len(inApp.seen) != 2 {
		t.Fatalf("blurred delivery mismatch: external=%d in-app=%d", len(external.seen), len(inApp.seen))
	}
}

func TestLogHandlerWritesFormattedNotification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.log")
	handler, err := NewLogHandler(path)
	if err != nil {
		t.Fatalf("NewLogHandler failed: %v", err)
		return
	}
	defer handler.Close() //nolint:errcheck

	n := &Notification{
		Type:      NotificationError,
		Title:     "Error",
		Body:      "boom",
		Urgent:    true,
		Timestamp: time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC),
		SessionID: "session-1",
	}
	if err := handler.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify failed: %v", err)
		return
	}
	if !handler.IsSupported() {
		t.Fatal("log handler should always be supported")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
		return
	}
	line := string(data)
	for _, want := range []string{"2026-06-13T12:00:00Z", "error", "Error", "boom", "session=session-1", "urgent=true"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %q: %s", want, line)
		}
	}
}

func TestConvenienceNotifications(t *testing.T) {
	mgr := NewNotificationManager()
	handler := &fakeHandler{supported: true}
	mgr.AddHandler(handler)

	NotifyCompletion(mgr, "s1", "done")
	NotifyError(mgr, "s2", errors.New("bad"))
	NotifyPermissionNeeded(mgr, "s3", "Bash")
	NotifyCompletion(nil, "ignored", "ignored")
	NotifyError(nil, "ignored", errors.New("ignored"))
	NotifyPermissionNeeded(nil, "ignored", "Ignored")

	if len(handler.seen) != 3 {
		t.Fatalf("expected 3 notifications, got %#v", handler.seen)
	}
	if handler.seen[0].Type != NotificationCompletion || handler.seen[0].Title != "Task Completed" || handler.seen[0].SessionID != "s1" {
		t.Fatalf("unexpected completion notification: %#v", handler.seen[0])
	}
	if handler.seen[1].Type != NotificationError || !handler.seen[1].Urgent || handler.seen[1].Body != "bad" {
		t.Fatalf("unexpected error notification: %#v", handler.seen[1])
	}
	if handler.seen[2].Type != NotificationPermissionNeeded || !strings.Contains(handler.seen[2].Body, "Bash") {
		t.Fatalf("unexpected permission notification: %#v", handler.seen[2])
	}
}
