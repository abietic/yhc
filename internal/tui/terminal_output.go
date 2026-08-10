package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTerminalOutputWriteTimeout     = 750 * time.Millisecond
	defaultTerminalOutputDrainTimeout     = time.Second
	defaultTerminalOutputInterruptTimeout = 250 * time.Millisecond
)

var (
	// ErrTerminalOutputClosed is returned when a frame arrives after Close.
	ErrTerminalOutputClosed = errors.New("terminal output is closed")
	// ErrTerminalOutputWriteTimeout identifies a sink write that exceeded the
	// bounded synchronous acknowledgement window.
	ErrTerminalOutputWriteTimeout = errors.New("terminal output write timed out")
	// ErrTerminalOutputDrainTimeout identifies a writer that did not stop after
	// the input side was closed.
	ErrTerminalOutputDrainTimeout = errors.New("terminal output drain timed out")
	// ErrTerminalOutputInterruptTimeout identifies a writer that remained
	// blocked after its platform interrupt path ran.
	ErrTerminalOutputInterruptTimeout = errors.New("terminal output interrupt timed out")
)

// TerminalOutputConfig bounds every stage of terminal output shutdown.
// Zero values select the production defaults.
type TerminalOutputConfig struct {
	WriteTimeout     time.Duration
	DrainTimeout     time.Duration
	InterruptTimeout time.Duration
}

func (c TerminalOutputConfig) normalized() TerminalOutputConfig {
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = defaultTerminalOutputWriteTimeout
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = defaultTerminalOutputDrainTimeout
	}
	if c.InterruptTimeout <= 0 {
		c.InterruptTimeout = defaultTerminalOutputInterruptTimeout
	}
	return c
}

type terminalOutputSink interface {
	io.WriteCloser
	prepare() error
	finish()
	interrupt() error
}

type terminalOutputDeadlineWriter interface {
	SetWriteDeadline(time.Time) error
}

type basicTerminalOutputSink struct {
	writer   io.WriteCloser
	closeErr error
	closeMu  sync.Mutex
	closed   bool
}

func (s *basicTerminalOutputSink) Write(p []byte) (int, error) {
	return s.writer.Write(p)
}

func (s *basicTerminalOutputSink) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if !s.closed {
		s.closed = true
		s.closeErr = s.writer.Close()
	}
	return s.closeErr
}

func (s *basicTerminalOutputSink) prepare() error { return nil }
func (s *basicTerminalOutputSink) finish()        {}
func (s *basicTerminalOutputSink) interrupt() error {
	return s.Close()
}

func (s *basicTerminalOutputSink) SetWriteDeadline(deadline time.Time) error {
	writer, ok := s.writer.(terminalOutputDeadlineWriter)
	if !ok {
		return os.ErrNoDeadline
	}
	return writer.SetWriteDeadline(deadline)
}

type terminalOutputRequest struct {
	data   []byte
	result chan terminalOutputResult
}

type terminalOutputResult struct {
	written int
	err     error
}

// TerminalOutput serializes Bubble Tea output through one synchronously
// acknowledged writer. The request channel is unbuffered and Write waits for
// its result, so at most one packet can be in flight and memory cannot grow
// with render invalidations.
type TerminalOutput struct {
	config TerminalOutputConfig
	sink   terminalOutputSink

	requests chan terminalOutputRequest
	done     chan struct{}
	failed   chan struct{}

	writeMu sync.Mutex
	closed  atomic.Bool
	stopped atomic.Bool

	failureOnce sync.Once
	failureMu   sync.Mutex
	failure     error

	closeOnce sync.Once
	closeErr  error
}

// NewTerminalOutput creates the production terminal writer around a
// platform-interruptible duplicate of output. The original file remains
// available for ordered fallback cleanup after this writer has stopped.
func NewTerminalOutput(output *os.File) (*TerminalOutput, error) {
	return NewTerminalOutputWithConfig(output, TerminalOutputConfig{})
}

// NewTerminalOutputWithConfig is the configurable production constructor used
// by deterministic platform tests. Normal callers should use
// NewTerminalOutput.
func NewTerminalOutputWithConfig(
	output *os.File,
	config TerminalOutputConfig,
) (*TerminalOutput, error) {
	if output == nil {
		return nil, errors.New("terminal output file is nil")
	}
	sink, err := newTerminalOutputSink(output)
	if err != nil {
		return nil, fmt.Errorf("prepare terminal output: %w", err)
	}
	return newTerminalOutput(sink, config)
}

// NewTerminalOutputWriter constructs the same bounded writer around an
// injected sink. Ownership of sink transfers to TerminalOutput.
func NewTerminalOutputWriter(
	sink io.WriteCloser,
	config TerminalOutputConfig,
) (*TerminalOutput, error) {
	if sink == nil {
		return nil, errors.New("terminal output sink is nil")
	}
	return newTerminalOutput(&basicTerminalOutputSink{writer: sink}, config)
}

func newTerminalOutput(
	sink terminalOutputSink,
	config TerminalOutputConfig,
) (*TerminalOutput, error) {
	output := &TerminalOutput{
		config:   config.normalized(),
		sink:     sink,
		requests: make(chan terminalOutputRequest),
		done:     make(chan struct{}),
		failed:   make(chan struct{}),
	}
	prepared := make(chan error, 1)
	go output.run(prepared)
	if err := <-prepared; err != nil {
		<-output.done
		return nil, fmt.Errorf("prepare terminal writer: %w", err)
	}
	return output, nil
}

func (o *TerminalOutput) run(prepared chan<- error) {
	if err := o.sink.prepare(); err != nil {
		prepared <- err
		_ = o.sink.Close()
		o.stopped.Store(true)
		close(o.done)
		return
	}
	prepared <- nil

	defer close(o.done)
	defer o.stopped.Store(true)
	defer o.sink.finish()
	defer o.sink.Close() //nolint:errcheck

	for request := range o.requests {
		result := o.writePacket(request.data)
		if result.err != nil {
			o.recordFailure(fmt.Errorf("terminal output write: %w", result.err))
		}
		request.result <- result
		if result.err != nil {
			return
		}
	}
}

func (o *TerminalOutput) writePacket(data []byte) terminalOutputResult {
	deadlineWriter, hasDeadline := o.sink.(terminalOutputDeadlineWriter)
	deadlineSet := false
	if hasDeadline {
		if err := deadlineWriter.SetWriteDeadline(time.Now().Add(o.config.WriteTimeout)); err == nil {
			deadlineSet = true
		}
	}
	if deadlineSet {
		defer deadlineWriter.SetWriteDeadline(time.Time{}) //nolint:errcheck
	}

	written := 0
	for written < len(data) {
		n, err := o.sink.Write(data[written:])
		written += n
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				err = errors.Join(ErrTerminalOutputWriteTimeout, err)
			}
			return terminalOutputResult{written: written, err: err}
		}
		if n == 0 {
			return terminalOutputResult{written: written, err: io.ErrNoProgress}
		}
	}
	return terminalOutputResult{written: written}
}

// Write transfers one copied packet to the sole sink writer and waits for the
// physical write result. The one total deadline covers both handoff and sink
// acknowledgement.
func (o *TerminalOutput) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	o.writeMu.Lock()
	defer o.writeMu.Unlock()

	if o.closed.Load() {
		return 0, ErrTerminalOutputClosed
	}
	if err := o.Err(); err != nil {
		return 0, err
	}

	request := terminalOutputRequest{
		data:   append([]byte(nil), p...),
		result: make(chan terminalOutputResult, 1),
	}
	timer := time.NewTimer(o.config.WriteTimeout)
	defer stopTerminalOutputTimer(timer)

	select {
	case o.requests <- request:
	case <-o.done:
		if err := o.Err(); err != nil {
			return 0, err
		}
		return 0, io.ErrClosedPipe
	case <-timer.C:
		err := fmt.Errorf(
			"%w after %s",
			ErrTerminalOutputWriteTimeout,
			o.config.WriteTimeout,
		)
		o.recordFailure(err)
		return 0, err
	}

	select {
	case result := <-request.result:
		if result.err != nil {
			if err := o.Err(); err != nil {
				return result.written, err
			}
			return result.written, result.err
		}
		return result.written, nil
	case <-o.done:
		if err := o.Err(); err != nil {
			return 0, err
		}
		return 0, io.ErrClosedPipe
	case <-timer.C:
		err := fmt.Errorf(
			"%w after %s",
			ErrTerminalOutputWriteTimeout,
			o.config.WriteTimeout,
		)
		o.recordFailure(err)
		return 0, err
	}
}

func stopTerminalOutputTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (o *TerminalOutput) recordFailure(err error) {
	if err == nil {
		return
	}
	o.failureOnce.Do(func() {
		o.failureMu.Lock()
		o.failure = err
		o.failureMu.Unlock()
		close(o.failed)
	})
}

// Failed closes exactly once when the first terminal output failure occurs.
func (o *TerminalOutput) Failed() <-chan struct{} {
	return o.failed
}

// Err returns the first terminal output failure.
func (o *TerminalOutput) Err() error {
	o.failureMu.Lock()
	defer o.failureMu.Unlock()
	return o.failure
}

// startIfHealthy linearizes a short-lived dependent-operation admission
// against terminal failure publication and Close. start must return
// immediately after starting the cancellable operation; it must not wait for
// that operation to finish.
func (o *TerminalOutput) startIfHealthy(start func()) error {
	if start == nil {
		return errors.New("terminal output dependent operation is nil")
	}
	o.writeMu.Lock()
	defer o.writeMu.Unlock()
	o.failureMu.Lock()
	defer o.failureMu.Unlock()
	switch {
	case o.closed.Load():
		return ErrTerminalOutputClosed
	case o.failure != nil:
		return o.failure
	case o.stopped.Load():
		return io.ErrClosedPipe
	default:
		start()
		return nil
	}
}

// Stopped reports whether the sole sink writer can no longer emit bytes.
func (o *TerminalOutput) Stopped() bool {
	return o.stopped.Load()
}

// Close rejects later packets, drains the writer, and interrupts a blocked
// platform sink after a bounded deadline.
func (o *TerminalOutput) Close() error {
	o.closeOnce.Do(func() {
		o.closeErr = o.close()
	})
	return o.closeErr
}

func (o *TerminalOutput) close() error {
	o.writeMu.Lock()
	o.closed.Store(true)
	close(o.requests)
	o.writeMu.Unlock()

	drainTimer := time.NewTimer(o.config.DrainTimeout)
	select {
	case <-o.done:
		stopTerminalOutputTimer(drainTimer)
		return o.Err()
	case <-drainTimer.C:
	}

	drainErr := fmt.Errorf(
		"%w after %s",
		ErrTerminalOutputDrainTimeout,
		o.config.DrainTimeout,
	)
	o.recordFailure(drainErr)
	interruptErr := o.sink.interrupt()

	interruptTimer := time.NewTimer(o.config.InterruptTimeout)
	defer stopTerminalOutputTimer(interruptTimer)
	select {
	case <-o.done:
		return errors.Join(o.Err(), drainErr, interruptErr)
	case <-interruptTimer.C:
		return errors.Join(
			o.Err(),
			drainErr,
			interruptErr,
			fmt.Errorf(
				"%w after %s",
				ErrTerminalOutputInterruptTimeout,
				o.config.InterruptTimeout,
			),
		)
	}
}
