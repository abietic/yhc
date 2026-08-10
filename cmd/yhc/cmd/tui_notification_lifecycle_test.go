package cmd

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/notify"
	"github.com/abietic/yhc/internal/tui"
)

const p351LivenessTimeout = 10 * time.Second

func p351Notification(title string) *notify.Notification {
	return &notify.Notification{
		Type:  notify.NotificationCompletion,
		Title: title,
	}
}

func p351Wait[T any](t *testing.T, ch <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(p351LivenessTimeout):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func TestP351AdapterMapsTypedMessageWithoutWaitingForPresentation(t *testing.T) {
	delivered := make(chan tui.NotificationDeliveryMsg, 1)
	adapter := newTUINotifyAdapter(func(message tea.Msg) {
		delivered <- message.(tui.NotificationDeliveryMsg)
	})
	adapter.start()
	t.Cleanup(adapter.close)

	if err := adapter.Notify(context.Background(), &notify.Notification{
		Type:   notify.NotificationError,
		Title:  "failure",
		Body:   "details",
		Urgent: true,
	}); err != nil {
		t.Fatal(err)
	}
	message := p351Wait(t, delivered, "typed notification")
	if message.Message != "failure: details" ||
		message.Severity != tui.NotifyError {
		t.Fatalf("delivery message = %#v", message)
	}
}

func TestP351AdapterBlockedSendRetainsLatestThreePending(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	delivered := make(chan string, 4)
	var first sync.Once
	adapter := newTUINotifyAdapter(func(message tea.Msg) {
		notification := message.(tui.NotificationDeliveryMsg)
		first.Do(func() {
			close(firstEntered)
			<-releaseFirst
		})
		delivered <- notification.Message
	})
	adapter.start()
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
		adapter.close()
	})

	_ = adapter.Notify(context.Background(), p351Notification("one"))
	p351Wait(t, firstEntered, "blocked first send")
	for _, message := range []string{"two", "three", "four", "five"} {
		_ = adapter.Notify(context.Background(), p351Notification(message))
	}
	close(releaseFirst)

	for index, want := range []string{"one", "three", "four", "five"} {
		if got := p351Wait(t, delivered, "retained notification"); got != want {
			t.Fatalf("delivery[%d] = %q, want %q", index, got, want)
		}
	}
	adapter.close()
	_ = adapter.Notify(context.Background(), p351Notification("late"))
	select {
	case got := <-delivered:
		t.Fatalf("post-close notification delivered: %q", got)
	default:
	}
}

func TestP351AdapterConcurrentProducersReturnWhileSendBlocked(t *testing.T) {
	sendEntered := make(chan struct{})
	releaseSend := make(chan struct{})
	var once sync.Once
	adapter := newTUINotifyAdapter(func(tea.Msg) {
		once.Do(func() { close(sendEntered) })
		<-releaseSend
	})
	adapter.start()
	t.Cleanup(func() {
		select {
		case <-releaseSend:
		default:
			close(releaseSend)
		}
		adapter.close()
	})

	_ = adapter.Notify(context.Background(), p351Notification("in-flight"))
	p351Wait(t, sendEntered, "in-flight send")

	const producers = 128
	var wg sync.WaitGroup
	wg.Add(producers)
	for index := range producers {
		go func() {
			defer wg.Done()
			_ = adapter.Notify(
				context.Background(),
				p351Notification("producer-"+strconv.Itoa(index)),
			)
		}()
	}
	returned := make(chan struct{})
	go func() {
		wg.Wait()
		close(returned)
	}()
	p351Wait(t, returned, "non-blocking producers")
	close(releaseSend)
}

type p351ProgramDeadline struct {
	delay   time.Duration
	fire    chan struct{}
	message func(time.Time) tea.Msg
}

type p351ProgramClock struct {
	mu        sync.Mutex
	now       time.Time
	scheduled chan p351ProgramDeadline
}

func (c *p351ProgramClock) time() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *p351ProgramClock) set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func (c *p351ProgramClock) after(
	delay time.Duration,
	message func(time.Time) tea.Msg,
) tea.Cmd {
	deadline := p351ProgramDeadline{
		delay:   delay,
		fire:    make(chan struct{}),
		message: message,
	}
	c.scheduled <- deadline
	return func() tea.Msg {
		<-deadline.fire
		return deadline.message(c.time())
	}
}

type p351ProgramProbe struct {
	app             *tui.App
	firstEntered    chan struct{}
	releaseFirst    chan struct{}
	first           sync.Once
	frames          chan string
	deliveries      chan string
	deliveryUpdates atomic.Int64
}

func (p *p351ProgramProbe) Init() tea.Cmd {
	return p.app.Init()
}

func (p *p351ProgramProbe) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	notification, delivered := message.(tui.NotificationDeliveryMsg)
	if delivered {
		p.first.Do(func() {
			close(p.firstEntered)
			<-p.releaseFirst
		})
		p.deliveryUpdates.Add(1)
	}
	before := p.app.View().Content
	_, cmd := p.app.Update(message)
	after := p.app.View().Content
	if delivered {
		p.deliveries <- notification.Message
	}
	if before != after {
		select {
		case p.frames <- after:
		default:
		}
	}
	return p, cmd
}

func (p *p351ProgramProbe) View() tea.View {
	return p.app.View()
}

func p351WaitFrame(
	t *testing.T,
	frames <-chan string,
	description string,
	match func(string) bool,
) string {
	t.Helper()
	timer := time.NewTimer(p351LivenessTimeout)
	defer timer.Stop()
	for {
		select {
		case frame := <-frames:
			if match(frame) {
				return frame
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", description)
			return ""
		}
	}
}

func TestP351RealProgramPreRunBackpressureExpiryAndTermination(t *testing.T) {
	base := time.Unix(70_000, 0)
	clock := &p351ProgramClock{
		now:       base,
		scheduled: make(chan p351ProgramDeadline, 8),
	}
	app := tui.New(tui.Config{
		Resumed:           true,
		ReducedMotion:     true,
		Chooser:           func(int) int { return 0 },
		NotificationNow:   clock.time,
		NotificationAfter: clock.after,
	})
	probe := &p351ProgramProbe{
		app:          app,
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		frames:       make(chan string, 64),
		deliveries:   make(chan string, 8),
	}
	program := tea.NewProgram(
		probe,
		tea.WithInput(nil),
		tea.WithoutRenderer(),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignals(),
	)
	adapter := newTUINotifyAdapter(program.Send)
	adapter.start()

	_ = adapter.Notify(context.Background(), p351Notification("pre-run"))
	runDone := make(chan error, 1)
	go func() {
		_, err := program.Run()
		runDone <- err
	}()
	p351Wait(t, probe.firstEntered, "pre-run offer reaching Update")

	_ = adapter.Notify(context.Background(), p351Notification("blocked-send"))
	timer := time.NewTimer(p351LivenessTimeout)
	defer timer.Stop()
	for {
		adapter.mu.Lock()
		pending := len(adapter.pending)
		adapter.mu.Unlock()
		if pending == 0 {
			break
		}
		select {
		case <-timer.C:
			t.Fatal("pump did not enter blocked Program.Send")
		default:
			runtime.Gosched()
		}
	}

	const burst = 64
	producersDone := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(burst)
		for index := range burst {
			go func() {
				defer wg.Done()
				_ = adapter.Notify(
					context.Background(),
					p351Notification("burst-"+strconv.Itoa(index)),
				)
			}()
		}
		wg.Wait()
		close(producersDone)
	}()
	p351Wait(t, producersDone, "producers during blocked Update and Send")
	close(probe.releaseFirst)

	firstDelivery := p351Wait(t, probe.deliveries, "pre-run delivery settlement")
	if firstDelivery != "pre-run" {
		t.Fatalf("first delivery = %q, want pre-run", firstDelivery)
	}
	deadline := p351Wait(t, clock.scheduled, "notification deadline")
	if deadline.delay != 5*time.Second {
		t.Fatalf("deadline delay = %s, want 5s", deadline.delay)
	}
	retainedBurst := 0
	for index := range 4 {
		delivery := p351Wait(t, probe.deliveries, "retained delivery settlement")
		if index == 0 && delivery != "blocked-send" {
			t.Fatalf("delivery[0] = %q, want blocked-send", delivery)
		}
		if strings.HasPrefix(delivery, "burst-") {
			retainedBurst++
		}
	}
	if retainedBurst != 3 {
		t.Fatalf("retained burst deliveries = %d, want 3", retainedBurst)
	}
	p351WaitFrame(t, probe.frames, "retained burst delivery frame", func(frame string) bool {
		return strings.Contains(frame, "burst-")
	})

	clock.set(base.Add(5 * time.Second))
	close(deadline.fire)
	p351WaitFrame(t, probe.frames, "idle expiry frame", func(frame string) bool {
		return !strings.Contains(frame, "pre-run") &&
			!strings.Contains(frame, "blocked-send") &&
			!strings.Contains(frame, "burst-")
	})

	program.Quit()
	if err := p351Wait(t, runDone, "program termination"); err != nil {
		t.Fatalf("program.Run: %v", err)
	}
	adapter.close()
	beforeLate := probe.deliveryUpdates.Load()
	_ = adapter.Notify(context.Background(), p351Notification("post-termination"))
	if afterLate := probe.deliveryUpdates.Load(); afterLate != beforeLate {
		t.Fatalf("post-termination offer updated model: before=%d after=%d", beforeLate, afterLate)
	}
}

func TestP351ProductionAdapterLifecycleOrdering(t *testing.T) {
	sourceBytes, err := os.ReadFile("root.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	programIndex := strings.Index(source, "p := tea.NewProgram(app, options...)")
	adapterIndex := strings.Index(source, "notificationAdapter = newTUINotifyAdapter(p.Send)")
	registerIndex := strings.Index(source, "engineCfg.NotifyManager.AddHandler(notificationAdapter)")
	startIndex := strings.Index(source, "notificationAdapter.start()")
	stopBindingIndex := strings.Index(source, "stopNotifications = notificationAdapter.close")
	runIndex := strings.Index(source, "err = runTUIProgram(p, terminalOutput")
	if programIndex < 0 || adapterIndex < 0 || registerIndex < 0 ||
		startIndex < 0 || stopBindingIndex < 0 || runIndex < 0 {
		t.Fatalf(
			"notification lifecycle wiring incomplete: program=%d adapter=%d register=%d start=%d stop-binding=%d run=%d",
			programIndex,
			adapterIndex,
			registerIndex,
			startIndex,
			stopBindingIndex,
			runIndex,
		)
	}
	if programIndex >= adapterIndex ||
		adapterIndex >= registerIndex ||
		registerIndex >= startIndex ||
		startIndex >= stopBindingIndex ||
		stopBindingIndex >= runIndex {
		t.Fatalf(
			"notification lifecycle order unsafe: program=%d adapter=%d register=%d start=%d stop-binding=%d run=%d",
			programIndex,
			adapterIndex,
			registerIndex,
			startIndex,
			stopBindingIndex,
			runIndex,
		)
	}
	programRunIndex := strings.Index(source, "_, runErr := program.Run()")
	stopIndex := strings.Index(source, "programStopped()")
	outputCloseIndex := strings.Index(source, "outputErr := output.Close()")
	if programRunIndex < 0 ||
		programRunIndex >= stopIndex ||
		stopIndex >= outputCloseIndex {
		t.Fatalf(
			"adapter stop is not between Program.Run and output close: run=%d stop=%d output-close=%d",
			programRunIndex,
			stopIndex,
			outputCloseIndex,
		)
	}
	for _, forbidden := range []string{
		"ShowNotification(",
		"app *tui.App",
		"go func() {\n\t\t_ = a.Notify",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("root.go retained forbidden notification owner %q", forbidden)
		}
	}
}
