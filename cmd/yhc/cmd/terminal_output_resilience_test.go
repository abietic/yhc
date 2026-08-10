package cmd

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/internal/tui"
)

const (
	p150InitialFrame = "P150_INITIAL_FRAME"
	p150HeldFrame    = "P150_HELD_FRAME"
	p150ResumedFrame = "P150_RESUMED_FRAME"
)

type (
	p150SetFrameMsg string
	p150PanicMsg    struct{}
)

type p150ProbeModel struct {
	mu           sync.Mutex
	view         string
	started      chan struct{}
	startOnce    sync.Once
	updated      chan string
	resized      chan tea.WindowSizeMsg
	panicEntered chan struct{}
	panicOnce    sync.Once
}

func newP150ProbeModel() *p150ProbeModel {
	return &p150ProbeModel{
		view:         p150InitialFrame,
		started:      make(chan struct{}),
		updated:      make(chan string, 16),
		resized:      make(chan tea.WindowSizeMsg, 1),
		panicEntered: make(chan struct{}),
	}
}

func (m *p150ProbeModel) Init() tea.Cmd {
	m.startOnce.Do(func() { close(m.started) })
	return nil
}

func (m *p150ProbeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case p150SetFrameMsg:
		frame := string(msg)
		m.mu.Lock()
		m.view = frame
		m.mu.Unlock()
		m.updated <- frame
	case tea.WindowSizeMsg:
		m.resized <- msg
	case p150PanicMsg:
		m.panicOnce.Do(func() { close(m.panicEntered) })
		panic("p150 deliberate panic")
	}
	return m, nil
}

func (m *p150ProbeModel) View() tea.View {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := tea.NewView(m.view)
	view.AltScreen = true
	return view
}

type p150BarrierWriter struct {
	mu          sync.Mutex
	output      bytes.Buffer
	blockMarker []byte
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
	writeNotice chan struct{}
}

func newP150BarrierWriter(marker string) *p150BarrierWriter {
	return &p150BarrierWriter{
		blockMarker: []byte(marker),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
		writeNotice: make(chan struct{}, 64),
	}
}

func (w *p150BarrierWriter) Write(p []byte) (int, error) {
	copied := append([]byte(nil), p...)
	w.mu.Lock()
	_, _ = w.output.Write(copied)
	w.mu.Unlock()

	select {
	case w.writeNotice <- struct{}{}:
	default:
	}
	if bytes.Contains(copied, w.blockMarker) {
		w.enterOnce.Do(func() {
			close(w.entered)
			<-w.release
		})
	}
	return len(p), nil
}

func (w *p150BarrierWriter) Release() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func (w *p150BarrierWriter) Close() error {
	w.Release()
	return nil
}

func (w *p150BarrierWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.output.Bytes()...)
}

func (w *p150BarrierWriter) WaitContains(t *testing.T, marker string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if bytes.Contains(w.Bytes(), []byte(marker)) {
			return
		}
		select {
		case <-w.writeNotice:
		case <-deadline.C:
			t.Fatalf("timed out waiting for output marker %q", marker)
		}
	}
}

type p150ProgramRun struct {
	program *tea.Program
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func startP150Program(t *testing.T, model tea.Model, output io.Writer) *p150ProgramRun {
	t.Helper()
	program := tea.NewProgram(
		model,
		tea.WithInput(bytes.NewReader(nil)),
		tea.WithOutput(output),
		tea.WithFPS(120),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignals(),
	)
	run := &p150ProgramRun{program: program, done: make(chan struct{})}
	go func() {
		_, err := program.Run()
		run.mu.Lock()
		run.err = err
		run.mu.Unlock()
		close(run.done)
	}()
	t.Cleanup(func() {
		if releaser, ok := output.(interface{ Release() }); ok {
			releaser.Release()
		}
		program.Kill()
		waitP150Closed(t, run.done, "program cleanup")
	})
	return run
}

func (r *p150ProgramRun) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func waitP150Closed(t *testing.T, ch <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s timed out", operation)
	}
}

func waitP150Value(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("updated frame = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for frame update %q", want)
	}
}

func assertP150Open(t *testing.T, ch <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s completed while the terminal write was still blocked", operation)
	default:
	}
}

func assertP150RestoreOrder(t *testing.T, output []byte, marker string) {
	t.Helper()
	restore := []byte("\x1b[?1049l")
	markerAt := bytes.Index(output, []byte(marker))
	restoreAt := bytes.LastIndex(output, restore)
	if markerAt < 0 {
		t.Fatalf("terminal output is missing application marker %q", marker)
	}
	if restoreAt < 0 {
		t.Fatalf("terminal output is missing alternate-screen restore: %q", output)
	}
	if markerAt > restoreAt {
		t.Fatalf("application frame %q was written after restore", marker)
	}
	if bytes.Contains(output[restoreAt+len(restore):], []byte("P150_")) {
		t.Fatalf("application output arrived after restore: %q", output[restoreAt:])
	}
}

func TestP150GracefulQuitHoldsFrameUntilWriteReleased(t *testing.T) {
	model := newP150ProbeModel()
	writer := newP150BarrierWriter(p150HeldFrame)
	run := startP150Program(t, model, writer)
	waitP150Closed(t, model.started, "program start")

	run.program.Send(p150SetFrameMsg(p150HeldFrame))
	waitP150Value(t, model.updated, p150HeldFrame)
	waitP150Closed(t, writer.entered, "blocked frame entry")

	quitSent := make(chan struct{})
	go func() {
		run.program.Quit()
		close(quitSent)
	}()
	waitP150Closed(t, quitSent, "quit request")
	assertP150Open(t, run.done, "graceful shutdown")

	writer.Release()
	waitP150Closed(t, run.done, "graceful shutdown")
	if err := run.Err(); err != nil {
		t.Fatalf("graceful program exit: %v", err)
	}
	assertP150RestoreOrder(t, writer.Bytes(), p150HeldFrame)
}

func TestP150ModelPanicHoldsFrameUntilWriteReleased(t *testing.T) {
	model := newP150ProbeModel()
	writer := newP150BarrierWriter(p150HeldFrame)
	run := startP150Program(t, model, writer)
	waitP150Closed(t, model.started, "program start")

	run.program.Send(p150SetFrameMsg(p150HeldFrame))
	waitP150Value(t, model.updated, p150HeldFrame)
	waitP150Closed(t, writer.entered, "blocked frame entry")

	run.program.Send(p150PanicMsg{})
	waitP150Closed(t, model.panicEntered, "model panic")
	assertP150Open(t, run.done, "panic shutdown")

	writer.Release()
	waitP150Closed(t, run.done, "panic shutdown")
	if err := run.Err(); !errors.Is(err, tea.ErrProgramKilled) || !errors.Is(err, tea.ErrProgramPanic) {
		t.Fatalf("panic error = %v, want ErrProgramKilled wrapping ErrProgramPanic", err)
	}
	assertP150RestoreOrder(t, writer.Bytes(), p150HeldFrame)
}

func TestP150ReleaseTerminalWaitsForFrameThenRestore(t *testing.T) {
	model := newP150ProbeModel()
	writer := newP150BarrierWriter(p150HeldFrame)
	run := startP150Program(t, model, writer)
	waitP150Closed(t, model.started, "program start")

	run.program.Send(p150SetFrameMsg(p150HeldFrame))
	waitP150Value(t, model.updated, p150HeldFrame)
	waitP150Closed(t, writer.entered, "blocked frame entry")

	releaseStarted := make(chan struct{})
	releaseDone := make(chan error, 1)
	go func() {
		close(releaseStarted)
		releaseDone <- run.program.ReleaseTerminal()
	}()
	waitP150Closed(t, releaseStarted, "release request")
	select {
	case err := <-releaseDone:
		t.Fatalf("ReleaseTerminal returned before the pending frame was released: %v", err)
	default:
	}
	if bytes.Contains(writer.Bytes(), []byte("\x1b[?1049l")) {
		t.Fatal("alternate screen restored while an application frame was pending")
	}

	writer.Release()
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatalf("ReleaseTerminal: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReleaseTerminal timed out after writer release")
	}
	if err := run.program.RestoreTerminal(); err != nil {
		t.Fatalf("RestoreTerminal: %v", err)
	}
	run.program.Send(p150SetFrameMsg(p150ResumedFrame))
	waitP150Value(t, model.updated, p150ResumedFrame)
	writer.WaitContains(t, p150ResumedFrame)

	run.program.Quit()
	waitP150Closed(t, run.done, "post-resume shutdown")
	if err := run.Err(); err != nil {
		t.Fatalf("post-resume program exit: %v", err)
	}

	output := writer.Bytes()
	firstExit := bytes.Index(output, []byte("\x1b[?1049l"))
	secondEnter := -1
	if firstExit >= 0 {
		if relative := bytes.Index(output[firstExit+len("\x1b[?1049l"):], []byte("\x1b[?1049h")); relative >= 0 {
			secondEnter = firstExit + len("\x1b[?1049l") + relative
		}
	}
	resumed := bytes.Index(output, []byte(p150ResumedFrame))
	lastExit := bytes.LastIndex(output, []byte("\x1b[?1049l"))
	if firstExit < 0 || secondEnter <= firstExit || resumed <= secondEnter || lastExit <= resumed {
		t.Fatalf("invalid release/resume/restore order: firstExit=%d secondEnter=%d resumed=%d lastExit=%d", firstExit, secondEnter, resumed, lastExit)
	}
	assertP150RestoreOrder(t, output, p150ResumedFrame)
}

type p150FailingWriter struct {
	mu     sync.Mutex
	writes int
	err    error
}

func (w *p150FailingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return 0, w.err
}

type p150QuitModel struct{}

func (p150QuitModel) Init() tea.Cmd                       { return tea.Quit }
func (p150QuitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return p150QuitModel{}, nil }
func (p150QuitModel) View() tea.View {
	view := tea.NewView("P150_WRITER_ERROR_FRAME")
	view.AltScreen = true
	return view
}

func TestP150WriterErrorIsSilentlyIgnored(t *testing.T) {
	sinkErr := errors.New("p150 writer failure")
	writer := &p150FailingWriter{err: sinkErr}
	program := tea.NewProgram(
		p150QuitModel{},
		tea.WithInput(nil),
		tea.WithOutput(writer),
		tea.WithoutSignals(),
	)
	if _, err := program.Run(); err != nil {
		t.Fatalf("Bubble Tea surfaced writer error unexpectedly: %v", err)
	}
	writer.mu.Lock()
	writes := writer.writes
	writer.mu.Unlock()
	if writes == 0 {
		t.Fatal("failing writer was never called")
	}
	t.Logf("classified silent_writer_error after %d ignored writes: %v", writes, sinkErr)
}

func TestP150ResizeAndRapidInvalidationBackpressureIsBounded(t *testing.T) {
	model := newP150ProbeModel()
	writer := newP150BarrierWriter(p150HeldFrame)
	run := startP150Program(t, model, writer)
	waitP150Closed(t, model.started, "program start")
	select {
	case initial := <-model.resized:
		if initial != (tea.WindowSizeMsg{Width: 80, Height: 24}) {
			t.Fatalf("initial resize = %+v, want 80x24", initial)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial resize")
	}

	run.program.Send(p150SetFrameMsg(p150HeldFrame))
	waitP150Value(t, model.updated, p150HeldFrame)
	waitP150Closed(t, writer.entered, "blocked frame entry")

	resize := tea.WindowSizeMsg{Width: 120, Height: 40}
	resizeSent := make(chan struct{})
	go func() {
		run.program.Send(resize)
		close(resizeSent)
	}()
	waitP150Closed(t, resizeSent, "resize send")

	const blocked = "P150_BACKPRESSURED_INVALIDATION"
	invalidationSent := make(chan struct{})
	go func() {
		run.program.Send(p150SetFrameMsg(blocked))
		close(invalidationSent)
	}()
	assertP150Open(t, invalidationSent, "post-resize invalidation send")

	writer.Release()
	select {
	case got := <-model.resized:
		if got != resize {
			t.Fatalf("resize = %+v, want %+v", got, resize)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for resize update after writer release")
	}
	waitP150Closed(t, invalidationSent, "backpressured invalidation send")
	waitP150Value(t, model.updated, blocked)
	run.program.Quit()
	waitP150Closed(t, run.done, "rapid invalidation shutdown")
	if err := run.Err(); err != nil {
		t.Fatalf("rapid invalidation program exit: %v", err)
	}
	t.Log("classified bounded_backpressure: resize occupied the pending update and blocked the following invalidation")
}

func TestP150LateProgressAfterRestoreIsRejected(t *testing.T) {
	model := newP150ProbeModel()
	writer := newP150BarrierWriter("P150_NEVER_BLOCK")
	run := startP150Program(t, model, writer)
	waitP150Closed(t, model.started, "program start")

	run.program.Quit()
	waitP150Closed(t, run.done, "normal shutdown")
	if err := run.Err(); err != nil {
		t.Fatalf("normal program exit: %v", err)
	}
	before := writer.Bytes()

	run.program.Send(p150SetFrameMsg("P150_LATE_TOOL_CHILD_PROGRESS"))
	select {
	case got := <-model.updated:
		t.Fatalf("late progress reached the model after shutdown: %q", got)
	default:
	}
	if after := writer.Bytes(); !bytes.Equal(after, before) {
		t.Fatalf("terminal output changed after restore:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestP151BlockedWriterFailsClosedAndRestoresAfterWriterStops(t *testing.T) {
	model := newP150ProbeModel()
	writer := newP150BarrierWriter(p150HeldFrame)
	output, err := tui.NewTerminalOutputWriter(writer, tui.TerminalOutputConfig{
		WriteTimeout:     20 * time.Millisecond,
		DrainTimeout:     20 * time.Millisecond,
		InterruptTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}
	program := tea.NewProgram(
		model,
		tea.WithInput(bytes.NewReader(nil)),
		tea.WithOutput(output),
		tea.WithFPS(120),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignals(),
	)
	t.Cleanup(func() {
		writer.Release()
		program.Kill()
		_ = output.Close()
	})

	restoreSnapshot := make(chan []byte, 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- runTUIProgram(program, output, func() {
			restoreSnapshot <- writer.Bytes()
		}, nil)
	}()

	waitP150Closed(t, model.started, "program start")
	program.Send(p150SetFrameMsg(p150HeldFrame))
	waitP150Value(t, model.updated, p150HeldFrame)
	waitP150Closed(t, writer.entered, "blocked frame entry")

	select {
	case err := <-runDone:
		if !errors.Is(err, tui.ErrTerminalOutputWriteTimeout) {
			t.Fatalf("runTUIProgram error = %v, want terminal output timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bounded terminal output shutdown timed out")
	}
	var atRestore []byte
	select {
	case atRestore = <-restoreSnapshot:
	default:
		t.Fatal("fallback restore did not run after output failure")
	}
	if !output.Stopped() {
		t.Fatal("fallback restore ran before the writer stopped")
	}
	if afterRestore := writer.Bytes(); !bytes.Equal(afterRestore, atRestore) {
		t.Fatalf(
			"writer emitted bytes after fallback restore:\nat restore=%q\nafter=%q",
			atRestore,
			afterRestore,
		)
	}
}
