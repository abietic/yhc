package tui

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type notificationDeadlineCall struct {
	delay   time.Duration
	message func(time.Time) tea.Msg
}

type notificationTestClock struct {
	now       time.Time
	scheduled []notificationDeadlineCall
}

func (c *notificationTestClock) time() time.Time {
	return c.now
}

func (c *notificationTestClock) after(
	delay time.Duration,
	message func(time.Time) tea.Msg,
) tea.Cmd {
	c.scheduled = append(c.scheduled, notificationDeadlineCall{
		delay:   delay,
		message: message,
	})
	return func() tea.Msg {
		return message(c.now)
	}
}

func newNotificationTestApp(clock *notificationTestClock, reducedMotion bool) *App {
	app := New(Config{
		Chooser:           func(int) int { return 0 },
		ReducedMotion:     reducedMotion,
		NotificationNow:   clock.time,
		NotificationAfter: clock.after,
	})
	app.width = 80
	app.height = 24
	app.updateLayout()
	return app
}

func requireNotificationMessages(t *testing.T, app *App, want ...string) {
	t.Helper()
	active := app.notifications.Active()
	if len(active) != len(want) {
		t.Fatalf("notifications = %#v, want messages %q", active, want)
	}
	for index, message := range want {
		if active[index].Message != message {
			t.Fatalf("notification[%d] = %#v, want %q", index, active[index], message)
		}
	}
}

func TestP351DeliveryOwnsCreationRedrawAndIdleExpiry(t *testing.T) {
	base := time.Unix(10_000, 0)
	clock := &notificationTestClock{now: base}
	app := newNotificationTestApp(clock, false)

	_, expiry := app.Update(NotificationDeliveryMsg{
		Message:  "build finished",
		Severity: NotifySuccess,
	})
	requireNotificationMessages(t, app, "build finished")
	active := app.notifications.Active()
	if active[0].CreatedAt != base || active[0].Duration != defaultNotificationTTL {
		t.Fatalf("accepted notification = %#v", active[0])
	}
	if expiry == nil || len(clock.scheduled) != 1 ||
		clock.scheduled[0].delay != defaultNotificationTTL {
		t.Fatalf("expiry schedule = %#v, cmd nil=%v", clock.scheduled, expiry == nil)
	}
	if got := stripANSIForTest(app.activeToast()); !strings.Contains(got, "build finished") {
		t.Fatalf("status-line notification = %q", got)
	}

	clock.now = base.Add(defaultNotificationTTL)
	_, next := app.Update(expiry())
	if next != nil || app.notifications.Count() != 0 ||
		app.notificationExpiryScheduled {
		t.Fatalf(
			"expired state: count=%d scheduled=%v next=%v",
			app.notifications.Count(),
			app.notificationExpiryScheduled,
			next != nil,
		)
	}
}

func TestP351EarliestDeadlineAndStaleGeneration(t *testing.T) {
	base := time.Unix(20_000, 0)
	clock := &notificationTestClock{now: base}
	app := newNotificationTestApp(clock, true)
	app.notifications.PushWithDurationAt(base, "long", NotifyInfo, 5*time.Second)
	longTick := app.reconcileNotificationExpiry()
	if longTick == nil || clock.scheduled[0].delay != 5*time.Second {
		t.Fatalf("long schedule = %#v", clock.scheduled)
	}
	longGeneration := app.notificationExpiryGeneration

	clock.now = base.Add(time.Second)
	app.notifications.PushWithDurationAt(clock.now, "short", NotifyWarning, time.Second)
	_, shortTick := app.Update(tea.BlurMsg{})
	if shortTick == nil || len(clock.scheduled) != 2 ||
		clock.scheduled[1].delay != time.Second ||
		app.notificationExpiryGeneration == longGeneration {
		t.Fatalf(
			"short schedule = %#v generation=%d old=%d",
			clock.scheduled,
			app.notificationExpiryGeneration,
			longGeneration,
		)
	}
	currentGeneration := app.notificationExpiryGeneration

	_, staleNext := app.Update(longTick())
	requireNotificationMessages(t, app, "long", "short")
	if staleNext != nil ||
		app.notificationExpiryGeneration != currentGeneration ||
		!app.notificationExpiryScheduled {
		t.Fatalf(
			"stale tick mutated timer: next=%v generation=%d scheduled=%v",
			staleNext != nil,
			app.notificationExpiryGeneration,
			app.notificationExpiryScheduled,
		)
	}

	clock.now = base.Add(2 * time.Second)
	_, next := app.Update(shortTick())
	requireNotificationMessages(t, app, "long")
	if next == nil || len(clock.scheduled) != 3 ||
		clock.scheduled[2].delay != 3*time.Second {
		t.Fatalf("next earliest schedule = %#v", clock.scheduled)
	}
}

func TestP351EvictedEarliestWakeRemainsAuthoritative(t *testing.T) {
	base := time.Unix(30_000, 0)
	clock := &notificationTestClock{now: base}
	app := newNotificationTestApp(clock, true)
	app.notifications.PushWithDurationAt(base, "evicted", NotifyInfo, time.Second)
	earlyTick := app.reconcileNotificationExpiry()
	earlyGeneration := app.notificationExpiryGeneration

	clock.now = base.Add(100 * time.Millisecond)
	for _, message := range []string{"two", "three", "four"} {
		_, cmd := app.Update(NotificationDeliveryMsg{Message: message})
		if cmd != nil {
			t.Fatalf("default-TTL overflow rescheduled %q", message)
		}
	}
	requireNotificationMessages(t, app, "two", "three", "four")
	if app.notificationExpiryGeneration != earlyGeneration ||
		len(clock.scheduled) != 1 {
		t.Fatalf(
			"eviction replaced authoritative wake: generation=%d schedules=%d",
			app.notificationExpiryGeneration,
			len(clock.scheduled),
		)
	}

	clock.now = base.Add(time.Second)
	_, next := app.Update(earlyTick())
	requireNotificationMessages(t, app, "two", "three", "four")
	if next == nil || len(clock.scheduled) != 2 ||
		clock.scheduled[1].delay != 4*time.Second+100*time.Millisecond {
		t.Fatalf("post-eviction reconciliation = %#v", clock.scheduled)
	}
}

func TestP351SameDeadlineAndEmptyInvalidation(t *testing.T) {
	base := time.Unix(40_000, 0)
	clock := &notificationTestClock{now: base}
	app := newNotificationTestApp(clock, true)
	app.notifications.PushWithDurationAt(base, "one", NotifyInfo, 2*time.Second)
	app.notifications.PushWithDurationAt(base, "two", NotifyError, 2*time.Second)
	tick := app.reconcileNotificationExpiry()

	clock.now = base.Add(2 * time.Second)
	_, next := app.Update(tick())
	if next != nil || app.notifications.Count() != 0 {
		t.Fatalf("same-deadline expiry count=%d next=%v", app.notifications.Count(), next != nil)
	}

	_, pending := app.Update(NotificationDeliveryMsg{Message: "clear me"})
	generation := app.notificationExpiryGeneration
	app.notifications.Clear()
	_, invalidation := app.Update(tea.BlurMsg{})
	if invalidation != nil || app.notificationExpiryScheduled ||
		app.notificationExpiryGeneration == generation {
		t.Fatalf(
			"empty invalidation: cmd=%v scheduled=%v generation=%d old=%d",
			invalidation != nil,
			app.notificationExpiryScheduled,
			app.notificationExpiryGeneration,
			generation,
		)
	}
	_, stale := app.Update(pending())
	if stale != nil || app.notifications.Count() != 0 {
		t.Fatalf("invalidated tick rescheduled empty stack: cmd=%v", stale != nil)
	}
}

func TestP351NotificationReadsAndRenderArePure(t *testing.T) {
	base := time.Unix(50_000, 0)
	clock := &notificationTestClock{now: base}
	app := newNotificationTestApp(clock, false)
	_, _ = app.Update(NotificationDeliveryMsg{
		Message:  "pure",
		Severity: NotifyWarning,
	})

	beforeItems := app.notifications.Active()
	beforeScheduled := app.notificationExpiryScheduled
	beforeGeneration := app.notificationExpiryGeneration
	beforeDeadline := app.notificationScheduledDeadline
	for range 3 {
		active := app.notifications.Active()
		active[0].Message = "caller mutation"
		_ = app.notifications.Count()
		_ = app.notifications.HasActive()
		_ = app.notifications.Render(app.styles, 80)
		_ = app.notifications.RenderSingleLine(app.styles, 80)
		_ = app.activeToast()
		_ = app.renderView()
	}
	if !reflect.DeepEqual(app.notifications.Active(), beforeItems) ||
		app.notificationExpiryScheduled != beforeScheduled ||
		app.notificationExpiryGeneration != beforeGeneration ||
		app.notificationScheduledDeadline != beforeDeadline {
		t.Fatalf(
			"notification read mutated state: items=%#v scheduled=%v generation=%d deadline=%v",
			app.notifications.Active(),
			app.notificationExpiryScheduled,
			app.notificationExpiryGeneration,
			app.notificationScheduledDeadline,
		)
	}
}

func TestP351ReducedMotionDoesNotDisableNotificationExpiry(t *testing.T) {
	base := time.Unix(60_000, 0)
	clock := &notificationTestClock{now: base}
	app := newNotificationTestApp(clock, true)
	_, expiry := app.Update(NotificationDeliveryMsg{Message: "accessible"})
	if expiry == nil {
		t.Fatal("reduced motion suppressed notification deadline")
	}
	clock.now = base.Add(defaultNotificationTTL)
	_, _ = app.Update(expiry())
	if app.notifications.Count() != 0 {
		t.Fatalf("reduced-motion notification survived expiry: %#v", app.notifications.Active())
	}
}

func TestP351ProductionNotificationOwnerBoundary(t *testing.T) {
	appSource, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	notificationSource, err := os.ReadFile("notifications.go")
	if err != nil {
		t.Fatal(err)
	}
	appText := string(appSource)
	notificationText := string(notificationSource)
	for _, forbidden := range []string{
		"toastMsg",
		"toastTime",
		"toastDuration",
		"ShowNotification(",
	} {
		if strings.Contains(appText, forbidden) {
			t.Fatalf("app.go retained legacy notification owner %q", forbidden)
		}
	}
	if got := strings.Count(appText, "a.notifications.PushAt("); got != 1 {
		t.Fatalf("App notification mutation sites = %d, want sole showNotification owner", got)
	}
	for _, forbidden := range []string{"time.Now(", "time.Since(", ".PruneAt("} {
		if strings.Contains(notificationText, forbidden) {
			t.Fatalf("notifications.go retained read/render-time owner %q", forbidden)
		}
	}
}
