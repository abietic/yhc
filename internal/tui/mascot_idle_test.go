package tui

import (
	"math"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type mascotIdleScheduleProbe struct {
	delays      []time.Duration
	generations []uint64
}

func (p *mascotIdleScheduleProbe) after(delay time.Duration, generation uint64) tea.Cmd {
	p.delays = append(p.delays, delay)
	p.generations = append(p.generations, generation)
	return func() tea.Msg {
		return mascotIdleTickMsg{generation: generation}
	}
}

func (p *mascotIdleScheduleProbe) message(index int) mascotIdleTickMsg {
	return mascotIdleTickMsg{generation: p.generations[index]}
}

func fixedMascotIdleRandom(values ...float64) func() float64 {
	index := 0
	return func() float64 {
		if len(values) == 0 {
			return 0
		}
		value := values[min(index, len(values)-1)]
		index++
		return value
	}
}

func newMascotIdleTestApp(t *testing.T, cfg Config, values ...float64) (*App, *mascotIdleScheduleProbe) {
	t.Helper()
	app := sizedWelcomeApp(80, 24, cfg)
	probe := &mascotIdleScheduleProbe{}
	app.mascotIdleRand = fixedMascotIdleRandom(values...)
	app.mascotIdleAfter = probe.after
	return app, probe
}

func TestMascotIdleSchedulesOneInjectedDelayChain(t *testing.T) {
	app, probe := newMascotIdleTestApp(t, Config{Chooser: func(int) int { return 0 }}, 0.5)

	cmd := app.ensureMascotIdleTick()
	if cmd == nil {
		t.Fatal("visible welcome mascot did not schedule an idle delay")
	}
	if !app.mascotIdleScheduled || len(probe.delays) != 1 {
		t.Fatalf("schedule state = %v, delays = %v, want one pending delay", app.mascotIdleScheduled, probe.delays)
	}
	if got := probe.delays[0]; got != 4*time.Second {
		t.Fatalf("injected delay = %v, want 4s", got)
	}
	if got := cmd(); got != probe.message(0) {
		t.Fatalf("injected clock message = %#v, want %#v", got, probe.message(0))
	}
	if duplicate := app.ensureMascotIdleTick(); duplicate != nil {
		t.Fatal("second ensure created a duplicate idle delay")
	}
	if len(probe.delays) != 1 {
		t.Fatalf("delay chain count = %d, want 1", len(probe.delays))
	}
}

func TestMascotIdleDelayClampsToExactThreeFiveSecondBounds(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  time.Duration
	}{
		{name: "negative", value: -1, want: 3 * time.Second},
		{name: "nan", value: math.NaN(), want: 3 * time.Second},
		{name: "lower", value: 0, want: 3 * time.Second},
		{name: "middle", value: 0.5, want: 4 * time.Second},
		{name: "upper", value: 1, want: 5 * time.Second},
		{name: "overflow", value: 2, want: 5 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, _ := newMascotIdleTestApp(t, Config{}, test.value)
			if got := app.mascotIdleDelay(); got != test.want {
				t.Fatalf("delay for %v = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestMascotIdleSequenceSelectionIsDeterministic(t *testing.T) {
	tests := []struct {
		name   string
		random []float64
		pose   MascotPose
		frames int
	}{
		{name: "blink", random: []float64{0.25}, pose: PoseBlink, frames: 3},
		{name: "right seven", random: []float64{0.1, 0.4, 0}, pose: PoseLookRight, frames: 7},
		{name: "left eight", random: []float64{0.1, 0.6, 0.5}, pose: PoseLookLeft, frames: 8},
		{name: "left nine upper bound", random: []float64{0.1, 0.6, 1}, pose: PoseLookLeft, frames: 9},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, _ := newMascotIdleTestApp(t, Config{}, test.random...)
			sequence := app.mascotIdleSequence()
			if len(sequence) != test.frames {
				t.Fatalf("frame count = %d, want %d", len(sequence), test.frames)
			}
			for index, frame := range sequence {
				if frame != (Frame{Pose: test.pose}) {
					t.Fatalf("frame %d = %#v, want pose %v at offset 0", index, frame, test.pose)
				}
			}
		})
	}
}

func TestMascotIdleAcceptedTickCompletesAndRearmsOnce(t *testing.T) {
	app, probe := newMascotIdleTestApp(t, Config{Chooser: func(int) int { return 0 }}, 0, 0.5)
	if cmd := app.ensureMascotIdleTick(); cmd == nil {
		t.Fatal("initial idle delay was not scheduled")
	}

	if _, cmd := app.Update(probe.message(0)); cmd == nil {
		t.Fatal("accepted idle beat did not schedule frame ticks")
	}
	if !app.mascotAnim.IdleActive() || app.mascotAnim.CurrentPose() != PoseBlink {
		t.Fatalf("accepted beat state = active:%v pose:%v, want active blink", app.mascotAnim.IdleActive(), app.mascotAnim.CurrentPose())
	}
	if app.mascotIdleScheduled {
		t.Fatal("idle delay remained pending while its animation was active")
	}

	for range 3 {
		app.Update(mascotTickMsg{})
	}
	if app.mascotAnim.Active() {
		t.Fatal("three-frame blink did not finish after three frame ticks")
	}
	if !app.mascotIdleScheduled || len(probe.delays) != 2 {
		t.Fatalf("completion schedule state = %v, delays = %d, want exactly one rearm", app.mascotIdleScheduled, len(probe.delays))
	}
	if cmd := app.ensureMascotIdleTick(); cmd != nil || len(probe.delays) != 2 {
		t.Fatal("completion rearm allowed a second concurrent delay")
	}
}

func TestMascotIdleGenerationRejectsStaleResizeTick(t *testing.T) {
	app, probe := newMascotIdleTestApp(t, Config{}, 0, 1)
	app.ensureMascotIdleTick()
	first := probe.message(0)

	if _, cmd := app.Update(tea.WindowSizeMsg{Width: 90, Height: 24}); cmd == nil {
		t.Fatal("visible resize did not replace the invalidated delay")
	}
	if len(probe.generations) != 2 {
		t.Fatalf("resize schedules = %d, want 2", len(probe.generations))
	}
	second := probe.message(1)
	if second.generation == first.generation {
		t.Fatal("resize reused the stale delay generation")
	}

	app.Update(first)
	if app.mascotAnim.Active() {
		t.Fatal("stale pre-resize tick started an animation")
	}
	if !app.mascotIdleScheduled || app.mascotIdleGeneration != second.generation {
		t.Fatal("stale tick cleared or replaced the current delay")
	}

	app.Update(second)
	if !app.mascotAnim.IdleActive() {
		t.Fatal("current post-resize tick was not accepted")
	}
}

func TestMascotIdleActiveSequenceStopsOnVisibleResizeAndRearmsOnce(t *testing.T) {
	app, probe := newMascotIdleTestApp(t, Config{}, 0, 0.5, 1)
	app.ensureMascotIdleTick()
	app.Update(probe.message(0))
	if !app.mascotAnim.IdleActive() {
		t.Fatal("idle sequence did not start")
	}

	if _, cmd := app.Update(tea.WindowSizeMsg{Width: 90, Height: 24}); cmd == nil {
		t.Fatal("visible resize did not replace the active idle sequence with a delay")
	}
	if app.mascotAnim.Active() {
		t.Fatal("pre-resize idle sequence remained active")
	}
	if !app.mascotIdleScheduled || len(probe.delays) != 2 {
		t.Fatalf("resize schedule state = %v, delays = %d, want one replacement delay", app.mascotIdleScheduled, len(probe.delays))
	}

	app.Update(mascotTickMsg{})
	if app.mascotAnim.Active() {
		t.Fatal("pre-resize frame tick advanced the stopped idle sequence")
	}
	if !app.mascotIdleScheduled || len(probe.delays) != 2 {
		t.Fatal("stale frame tick disturbed the replacement delay")
	}
}

func TestMascotIdleActiveSequenceStopsOnStateTransition(t *testing.T) {
	app, probe := newMascotIdleTestApp(t, Config{}, 0, 0.5)
	app.ensureMascotIdleTick()
	app.Update(probe.message(0))
	if !app.mascotAnim.IdleActive() {
		t.Fatal("idle sequence did not start")
	}

	app.state = StateChat
	app.reconcileMascotIdle()
	if app.mascotAnim.Active() || app.mascotIdleScheduled {
		t.Fatal("state transition retained active idle work")
	}
	if _, cmd := app.Update(mascotTickMsg{}); cmd != nil || app.mascotAnim.Active() {
		t.Fatal("pre-transition frame tick advanced outside welcome")
	}
}

func TestMascotClickSequenceContinuesAcrossVisibleResize(t *testing.T) {
	app, _ := newMascotIdleTestApp(t, Config{Chooser: func(int) int { return 0 }})
	if cmd := app.mascotAnim.TriggerAnimation(); cmd == nil {
		t.Fatal("click sequence did not start")
	}
	app.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if !app.mascotAnim.Active() || app.mascotAnim.kind != mascotSequenceClick || app.mascotAnim.index != 0 {
		t.Fatal("visible resize cancelled or advanced the click sequence")
	}
	app.Update(mascotTickMsg{})
	if app.mascotAnim.index != 1 {
		t.Fatal("pre-resize click frame tick did not continue the click sequence")
	}
}

func TestMascotIdleStopsOnHiddenResizeAndStateTransition(t *testing.T) {
	tests := []struct {
		name   string
		change func(*App)
	}{
		{
			name: "hidden resize",
			change: func(app *App) {
				app.Update(tea.WindowSizeMsg{Width: 56, Height: 24})
			},
		},
		{
			name: "chat transition",
			change: func(app *App) {
				app.state = StateChat
				app.reconcileMascotIdle()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, probe := newMascotIdleTestApp(t, Config{}, 0)
			app.ensureMascotIdleTick()
			stale := probe.message(0)
			test.change(app)

			if app.mascotIdleScheduled || app.mascotAnim.Active() {
				t.Fatal("ineligible context retained mascot work")
			}
			if _, cmd := app.Update(stale); cmd != nil {
				t.Fatal("stale tick scheduled work after context shutdown")
			}
			if app.mascotAnim.Active() {
				t.Fatal("stale tick animated after context shutdown")
			}
		})
	}
}

func TestMascotIdleStopsWhenFirstMessageLeavesWelcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app, probe := newMascotIdleTestApp(t, Config{}, 0)
	app.SetEngine(newTextSubmissionEngine(t))
	app.ensureMascotIdleTick()
	stale := probe.message(0)
	app.textarea.SetValue("hello")

	cmd := app.sendMessage()
	settleComposerCommand(t, app, cmd)
	if app.state != StateChat {
		t.Fatalf("submission state = %v, want chat", app.state)
	}
	if app.mascotIdleScheduled || app.mascotAnim.Active() {
		t.Fatal("first submission retained mascot work")
	}
	if _, cmd := app.Update(stale); cmd != nil || app.mascotAnim.Active() {
		t.Fatal("pre-submission idle tick advanced after entering chat")
	}
}

func TestMascotClickInvalidatesDelayAndPreemptsIdleWithoutSecondFrameChain(t *testing.T) {
	cfg := Config{Fullscreen: true, MouseEnabled: true, Chooser: func(int) int { return 0 }}
	app, probe := newMascotIdleTestApp(t, cfg, 0)
	app.ensureMascotIdleTick()
	stale := probe.message(0)
	bounds, ok := app.welcomeMascotBounds()
	if !ok {
		t.Fatal("visible mascot has no click bounds")
	}
	click := tuiMouseMsg{
		X: bounds.x, Y: bounds.y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	}

	if _, cmd := app.Update(click); cmd == nil {
		t.Fatal("click with a pending idle delay did not start its frame chain")
	}
	if app.mascotIdleScheduled || app.mascotAnim.kind != mascotSequenceClick {
		t.Fatal("click did not invalidate idle ownership")
	}
	app.Update(stale)
	if app.mascotAnim.kind != mascotSequenceClick || app.mascotAnim.index != 0 {
		t.Fatal("stale idle tick interfered with the click sequence")
	}

	idleApp, idleProbe := newMascotIdleTestApp(t, cfg, 0, 0.5)
	idleApp.ensureMascotIdleTick()
	idleApp.Update(idleProbe.message(0))
	if !idleApp.mascotAnim.IdleActive() {
		t.Fatal("idle blink did not start")
	}
	idleBounds, ok := idleApp.welcomeMascotBounds()
	if !ok {
		t.Fatal("idle mascot has no click bounds")
	}
	click.X, click.Y = idleBounds.x, idleBounds.y
	if _, cmd := idleApp.Update(click); cmd != nil {
		t.Fatal("idle preemption created a second frame tick chain")
	}
	if idleApp.mascotAnim.kind != mascotSequenceClick || idleApp.mascotAnim.index != 0 {
		t.Fatal("click did not preempt the idle sequence")
	}
	idleApp.Update(mascotTickMsg{})
	if idleApp.mascotAnim.index != 1 {
		t.Fatal("the existing idle frame chain did not advance the preempting click")
	}
}

func TestMascotIdleSuppressedWhenHiddenOrReducedMotion(t *testing.T) {
	tests := []struct {
		name string
		app  func(t *testing.T) *App
	}{
		{
			name: "hidden",
			app: func(t *testing.T) *App {
				t.Helper()
				return sizedWelcomeApp(56, 24, Config{})
			},
		},
		{
			name: "reduced motion",
			app: func(t *testing.T) *App {
				t.Helper()
				return sizedWelcomeApp(80, 24, Config{ReducedMotion: true})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := test.app(t)
			probe := &mascotIdleScheduleProbe{}
			app.mascotIdleAfter = probe.after
			if cmd := app.ensureMascotIdleTick(); cmd != nil {
				t.Fatal("ineligible context scheduled an idle delay")
			}
			if _, cmd := app.Update(mascotIdleTickMsg{generation: app.mascotIdleGeneration}); cmd != nil {
				t.Fatal("ineligible context accepted an idle tick")
			}
			if app.mascotAnim.Active() || len(probe.delays) != 0 {
				t.Fatal("ineligible context performed mascot work")
			}
		})
	}
}
