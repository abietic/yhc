package tui

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errTerminalOutputTestSink = errors.New("terminal output test sink failure")

type terminalOutputRecordingSink struct {
	mu            sync.Mutex
	output        bytes.Buffer
	active        int
	maxConcurrent int
	closed        bool
	writeErr      error
}

func (s *terminalOutputRecordingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.active++
	if s.active > s.maxConcurrent {
		s.maxConcurrent = s.active
	}
	defer func() {
		s.active--
		s.mu.Unlock()
	}()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.output.Write(p)
}

func (s *terminalOutputRecordingSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *terminalOutputRecordingSink) snapshot() (string, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String(), s.maxConcurrent, s.closed
}

type terminalOutputBlockingSink struct {
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
}

func newTerminalOutputBlockingSink() *terminalOutputBlockingSink {
	return &terminalOutputBlockingSink{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *terminalOutputBlockingSink) Write([]byte) (int, error) {
	s.enterOnce.Do(func() { close(s.entered) })
	<-s.release
	return 0, errTerminalOutputTestSink
}

func (s *terminalOutputBlockingSink) Close() error {
	s.closeOnce.Do(func() { close(s.release) })
	return nil
}

type terminalOutputStubbornSink struct {
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newTerminalOutputStubbornSink() *terminalOutputStubbornSink {
	return &terminalOutputStubbornSink{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *terminalOutputStubbornSink) Write([]byte) (int, error) {
	s.enterOnce.Do(func() { close(s.entered) })
	<-s.release
	return 0, errTerminalOutputTestSink
}

func (s *terminalOutputStubbornSink) Close() error {
	return nil
}

func (s *terminalOutputStubbornSink) Release() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func terminalOutputTestConfig() TerminalOutputConfig {
	return TerminalOutputConfig{
		WriteTimeout:     20 * time.Millisecond,
		DrainTimeout:     20 * time.Millisecond,
		InterruptTimeout: time.Second,
	}
}

func waitTerminalOutputTest(
	t *testing.T,
	channel <-chan struct{},
	operation string,
) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s timed out", operation)
	}
}

func TestTerminalOutputWritesInOrderAndDrainsBeforeClose(t *testing.T) {
	sink := &terminalOutputRecordingSink{}
	output, err := NewTerminalOutputWriter(sink, terminalOutputTestConfig())
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}
	if _, err := output.Write([]byte("first")); err != nil {
		t.Fatalf("write first packet: %v", err)
	}
	if _, err := output.Write([]byte("-second")); err != nil {
		t.Fatalf("write second packet: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, maxConcurrent, closed := sink.snapshot()
	if got != "first-second" {
		t.Fatalf("sink output = %q, want %q", got, "first-second")
	}
	if maxConcurrent != 1 {
		t.Fatalf("maximum concurrent writes = %d, want 1", maxConcurrent)
	}
	if !closed || !output.Stopped() {
		t.Fatalf("closed=%v stopped=%v, want both true", closed, output.Stopped())
	}
}

func TestTerminalOutputBlockedWriteTimesOutAndCloseInterrupts(t *testing.T) {
	sink := newTerminalOutputBlockingSink()
	output, err := NewTerminalOutputWriter(sink, terminalOutputTestConfig())
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}

	writeDone := make(chan struct{})
	var writeErr error
	go func() {
		_, writeErr = output.Write([]byte("blocked"))
		close(writeDone)
	}()
	waitTerminalOutputTest(t, sink.entered, "sink write entry")
	waitTerminalOutputTest(t, writeDone, "bounded write failure")
	if !errors.Is(writeErr, ErrTerminalOutputWriteTimeout) {
		t.Fatalf("write error = %v, want write timeout", writeErr)
	}
	waitTerminalOutputTest(t, output.Failed(), "failure signal")

	closeErr := output.Close()
	if !errors.Is(closeErr, ErrTerminalOutputWriteTimeout) {
		t.Fatalf("Close error = %v, want original write timeout", closeErr)
	}
	if !errors.Is(closeErr, ErrTerminalOutputDrainTimeout) {
		t.Fatalf("Close error = %v, want drain timeout", closeErr)
	}
	if !output.Stopped() {
		t.Fatal("writer did not stop after sink interruption")
	}
	if !errors.Is(output.Err(), ErrTerminalOutputWriteTimeout) {
		t.Fatalf("stored error = %v, want write timeout", output.Err())
	}
}

func TestTerminalOutputCloseReportsInterruptTimeoutUntilWriterStops(t *testing.T) {
	sink := newTerminalOutputStubbornSink()
	output, err := NewTerminalOutputWriter(sink, TerminalOutputConfig{
		WriteTimeout:     10 * time.Millisecond,
		DrainTimeout:     10 * time.Millisecond,
		InterruptTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}

	writeDone := make(chan struct{})
	go func() {
		_, _ = output.Write([]byte("blocked"))
		close(writeDone)
	}()
	waitTerminalOutputTest(t, sink.entered, "stubborn sink write entry")
	waitTerminalOutputTest(t, writeDone, "stubborn sink write timeout")

	closeErr := output.Close()
	if !errors.Is(closeErr, ErrTerminalOutputWriteTimeout) {
		t.Fatalf("Close error = %v, want write timeout", closeErr)
	}
	if !errors.Is(closeErr, ErrTerminalOutputDrainTimeout) {
		t.Fatalf("Close error = %v, want drain timeout", closeErr)
	}
	if !errors.Is(closeErr, ErrTerminalOutputInterruptTimeout) {
		t.Fatalf("Close error = %v, want interrupt timeout", closeErr)
	}
	if output.Stopped() {
		t.Fatal("writer reported stopped before the sink released")
	}

	sink.Release()
	waitTerminalOutputTest(t, output.done, "stubborn sink release")
	if !output.Stopped() {
		t.Fatal("writer did not report stopped after the sink released")
	}
}

func TestTerminalOutputSinkFailureIsReportedOnce(t *testing.T) {
	sink := &terminalOutputRecordingSink{writeErr: errTerminalOutputTestSink}
	output, err := NewTerminalOutputWriter(sink, terminalOutputTestConfig())
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}

	if _, err := output.Write([]byte("fail")); !errors.Is(err, errTerminalOutputTestSink) {
		t.Fatalf("write error = %v, want sink failure", err)
	}
	waitTerminalOutputTest(t, output.Failed(), "failure signal")
	first := output.Err()
	if _, err := output.Write([]byte("again")); !errors.Is(err, errTerminalOutputTestSink) {
		t.Fatalf("second write error = %v, want first sink failure", err)
	}
	if output.Err() != first {
		t.Fatalf("stored failure changed: first=%v current=%v", first, output.Err())
	}
	if err := output.Close(); !errors.Is(err, errTerminalOutputTestSink) {
		t.Fatalf("Close error = %v, want sink failure", err)
	}
	_, _, closed := sink.snapshot()
	if !closed {
		t.Fatal("failed sink was not closed")
	}
}

func TestTerminalOutputRejectsWritesAfterClose(t *testing.T) {
	sink := &terminalOutputRecordingSink{}
	output, err := NewTerminalOutputWriter(sink, terminalOutputTestConfig())
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := output.Write([]byte("late")); !errors.Is(err, ErrTerminalOutputClosed) {
		t.Fatalf("late write error = %v, want closed", err)
	}
	got, _, _ := sink.snapshot()
	if got != "" {
		t.Fatalf("late write reached sink: %q", got)
	}
}

func TestTerminalOutputConcurrentCallersUseOneSinkWriter(t *testing.T) {
	sink := &terminalOutputRecordingSink{}
	output, err := NewTerminalOutputWriter(sink, TerminalOutputConfig{
		WriteTimeout:     time.Second,
		DrainTimeout:     time.Second,
		InterruptTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}

	const writers = 32
	start := make(chan struct{})
	var failures atomic.Int32
	var wait sync.WaitGroup
	wait.Add(writers)
	for index := 0; index < writers; index++ {
		go func() {
			defer wait.Done()
			<-start
			if _, err := output.Write([]byte("x")); err != nil {
				failures.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if failures.Load() != 0 {
		t.Fatalf("concurrent write failures = %d", failures.Load())
	}
	if err := output.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, maxConcurrent, _ := sink.snapshot()
	if len(got) != writers {
		t.Fatalf("sink bytes = %d, want %d", len(got), writers)
	}
	if maxConcurrent != 1 {
		t.Fatalf("maximum concurrent writes = %d, want 1", maxConcurrent)
	}
}

func BenchmarkTerminalOutputFastSink(b *testing.B) {
	sink := &terminalOutputRecordingSink{}
	output, err := NewTerminalOutputWriter(sink, TerminalOutputConfig{
		WriteTimeout:     time.Minute,
		DrainTimeout:     time.Minute,
		InterruptTimeout: time.Minute,
	})
	if err != nil {
		b.Fatalf("NewTerminalOutputWriter: %v", err)
	}
	defer output.Close() //nolint:errcheck

	packet := []byte("terminal frame")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := output.Write(packet); err != nil && !errors.Is(err, io.EOF) {
			b.Fatal(err)
		}
	}
}
