package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

const (
	clipboardMaxSourceBytes = 256 << 10
	clipboardNativeTimeout  = 2 * time.Second
)

const clipboardPowerShellScript = `[Console]::InputEncoding=[System.Text.UTF8Encoding]::new($false);$text=[Console]::In.ReadToEnd();Set-Clipboard -Value $text`

// ClipboardCaller identifies one of the four TUI clipboard entrypoints.
type ClipboardCaller uint8

const (
	ClipboardCallerChatSelection ClipboardCaller = iota
	ClipboardCallerExpandSelection
	ClipboardCallerKeyboardSelection
	ClipboardCallerActionCopy
)

type clipboardTerminalOutcome uint8

const (
	clipboardTerminalNotStarted clipboardTerminalOutcome = iota
	clipboardTerminalSequenceWritten
	clipboardTerminalFailed
)

type clipboardNativeOutcome uint8

const (
	clipboardNativeNotStarted clipboardNativeOutcome = iota
	clipboardNativeSucceeded
	clipboardNativeSkippedSSH
	clipboardNativeUnavailable
	clipboardNativeTimedOut
	clipboardNativeFailed
	clipboardNativeCancelled
)

type clipboardFailureCategory uint8

const (
	clipboardFailureNone clipboardFailureCategory = iota
	clipboardFailureEmpty
	clipboardFailureInvalidUTF8
	clipboardFailureOversized
	clipboardFailureBusy
	clipboardFailureCancelled
	clipboardFailureOutputUnavailable
	clipboardFailureOutputClosed
	clipboardFailureOutputPartial
	clipboardFailureOutputTimeout
	clipboardFailureOutputFailed
	clipboardFailureNativeUnavailable
	clipboardFailureNativeTimeout
	clipboardFailureNativeFailed
)

type clipboardResultMsg struct {
	requestID   uint64
	caller      ClipboardCaller
	sourceBytes int
	terminal    clipboardTerminalOutcome
	native      clipboardNativeOutcome
	failure     clipboardFailureCategory
}

type clipboardEnvironment struct {
	goos    string
	tmux    bool
	screen  bool
	ssh     bool
	wayland bool
}

func snapshotClipboardEnvironment() clipboardEnvironment {
	tmux := os.Getenv("TMUX") != "" || os.Getenv("TMUX_PANE") != ""
	return clipboardEnvironment{
		goos:    runtime.GOOS,
		tmux:    tmux,
		screen:  !tmux && os.Getenv("STY") != "",
		ssh:     os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "",
		wayland: os.Getenv("WAYLAND_DISPLAY") != "",
	}
}

type clipboardNativeCommand struct {
	path string
	args []string
}

type clipboardNativeRunner func(
	context.Context,
	clipboardNativeCommand,
	[]byte,
) error

type clipboardTerminalFailureSource interface {
	Failed() <-chan struct{}
	Err() error
}

type clipboardTerminalStartFence interface {
	startIfHealthy(func()) error
}

// ClipboardService owns one bounded, non-queued delivery path. App owns
// user-interaction cardinality; the service's semaphore is a defensive
// fail-fast guard for accidental direct concurrent use.
type ClipboardService struct {
	ctx           context.Context
	output        io.Writer
	outputFailure clipboardTerminalFailureSource
	outputStart   clipboardTerminalStartFence
	environment   clipboardEnvironment
	nativeCommand *clipboardNativeCommand
	nativeRunner  clipboardNativeRunner
	nativeTimeout time.Duration
	active        chan struct{}
}

// NewClipboardService snapshots native routing and reuses the exact writer
// already owned by Bubble Tea. Production passes its sole TerminalOutput.
func NewClipboardService(ctx context.Context, output io.Writer) *ClipboardService {
	return newClipboardService(
		ctx,
		output,
		snapshotClipboardEnvironment(),
		exec.LookPath,
		runClipboardNativeCommand,
		clipboardNativeTimeout,
	)
}

func newClipboardService(
	ctx context.Context,
	output io.Writer,
	environment clipboardEnvironment,
	lookPath func(string) (string, error),
	runner clipboardNativeRunner,
	nativeTimeout time.Duration,
) *ClipboardService {
	if ctx == nil {
		ctx = context.Background()
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if runner == nil {
		runner = runClipboardNativeCommand
	}
	if nativeTimeout <= 0 {
		nativeTimeout = clipboardNativeTimeout
	}
	service := &ClipboardService{
		ctx:           ctx,
		output:        output,
		environment:   environment,
		nativeCommand: resolveClipboardNativeCommand(environment, lookPath),
		nativeRunner:  runner,
		nativeTimeout: nativeTimeout,
		active:        make(chan struct{}, 1),
	}
	service.outputFailure, _ = output.(clipboardTerminalFailureSource)
	service.outputStart, _ = output.(clipboardTerminalStartFence)
	return service
}

func resolveClipboardNativeCommand(
	environment clipboardEnvironment,
	lookPath func(string) (string, error),
) *clipboardNativeCommand {
	if environment.ssh {
		return nil
	}
	resolve := func(name string, args ...string) *clipboardNativeCommand {
		path, err := lookPath(name)
		if err != nil {
			return nil
		}
		return &clipboardNativeCommand{path: path, args: append([]string(nil), args...)}
	}
	switch environment.goos {
	case "darwin":
		return resolve("pbcopy")
	case "linux":
		if environment.wayland {
			if command := resolve("wl-copy"); command != nil {
				return command
			}
		}
		if command := resolve("xclip", "-selection", "clipboard"); command != nil {
			return command
		}
		return resolve("xsel", "--clipboard", "--input")
	case "windows":
		return resolve(
			"powershell.exe",
			"-NoLogo",
			"-NoProfile",
			"-NonInteractive",
			"-Command",
			clipboardPowerShellScript,
		)
	default:
		return nil
	}
}

func runClipboardNativeCommand(
	ctx context.Context,
	command clipboardNativeCommand,
	source []byte,
) error {
	cmd := exec.CommandContext(ctx, command.path, command.args...)
	cmd.Stdin = bytes.NewReader(source)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

func validateClipboardSource(text string) clipboardFailureCategory {
	switch {
	case text == "":
		return clipboardFailureEmpty
	case len(text) > clipboardMaxSourceBytes:
		return clipboardFailureOversized
	case !utf8.ValidString(text):
		return clipboardFailureInvalidUTF8
	default:
		return clipboardFailureNone
	}
}

func clipboardOSC52Packet(source []byte, environment clipboardEnvironment) []byte {
	osc := "\x1b]52;c;" + base64.StdEncoding.EncodeToString(source) + "\x07"
	switch {
	case environment.tmux:
		return []byte(
			"\x1bPtmux;" +
				strings.ReplaceAll(osc, "\x1b", "\x1b\x1b") +
				"\x1b\\",
		)
	case environment.screen:
		return []byte("\x1bP" + osc + "\x1b\\")
	default:
		return []byte(osc)
	}
}

func (s *ClipboardService) command(
	requestID uint64,
	caller ClipboardCaller,
	text string,
) tea.Cmd {
	return func() tea.Msg {
		return s.deliver(requestID, caller, text)
	}
}

func (s *ClipboardService) deliver(
	requestID uint64,
	caller ClipboardCaller,
	text string,
) clipboardResultMsg {
	result := clipboardResultMsg{
		requestID:   requestID,
		caller:      caller,
		sourceBytes: len(text),
	}
	if failure := validateClipboardSource(text); failure != clipboardFailureNone {
		result.failure = failure
		return result
	}
	if s == nil {
		result.terminal = clipboardTerminalFailed
		result.failure = clipboardFailureOutputUnavailable
		return result
	}
	select {
	case s.active <- struct{}{}:
		defer func() { <-s.active }()
	default:
		result.failure = clipboardFailureBusy
		return result
	}
	if s.ctx.Err() != nil {
		result.failure = clipboardFailureCancelled
		result.native = clipboardNativeCancelled
		return result
	}
	if s.output == nil {
		result.terminal = clipboardTerminalFailed
		result.failure = clipboardFailureOutputUnavailable
		return result
	}

	packet := clipboardOSC52Packet([]byte(text), s.environment)
	written, err := s.output.Write(packet)
	if err != nil {
		result.terminal = clipboardTerminalFailed
		result.failure = classifyClipboardOutputFailure(err)
		return result
	}
	if written != len(packet) {
		result.terminal = clipboardTerminalFailed
		result.failure = clipboardFailureOutputPartial
		return result
	}
	result.terminal = clipboardTerminalSequenceWritten

	if s.ctx.Err() != nil {
		result.failure = clipboardFailureCancelled
		result.native = clipboardNativeCancelled
		return result
	}
	if failure, failed := s.currentTerminalFailure(); failed {
		result.terminal = clipboardTerminalFailed
		result.failure = failure
		return result
	}
	if s.environment.ssh {
		result.native = clipboardNativeSkippedSSH
		return result
	}
	if s.nativeCommand == nil {
		result.native = clipboardNativeUnavailable
		result.failure = clipboardFailureNativeUnavailable
		return result
	}

	nativeCtx, cancel := context.WithTimeout(s.ctx, s.nativeTimeout)
	defer cancel()
	nativeResult := make(chan error, 1)
	startNative := func() {
		go func() {
			nativeResult <- s.nativeRunner(
				nativeCtx,
				*s.nativeCommand,
				[]byte(text),
			)
		}()
	}
	if s.outputStart != nil {
		if err := s.outputStart.startIfHealthy(startNative); err != nil {
			result.terminal = clipboardTerminalFailed
			result.failure = classifyClipboardOutputFailure(err)
			return result
		}
	} else {
		if failure, failed := s.currentTerminalFailure(); failed {
			result.terminal = clipboardTerminalFailed
			result.failure = failure
			return result
		}
		startNative()
	}
	if s.outputFailure == nil {
		err = <-nativeResult
	} else {
		select {
		case err = <-nativeResult:
		case <-s.outputFailure.Failed():
			cancel()
			err = <-nativeResult
		case <-nativeCtx.Done():
			err = <-nativeResult
		}
	}
	if failure, failed := s.currentTerminalFailure(); failed {
		result.terminal = clipboardTerminalFailed
		result.native = clipboardNativeCancelled
		result.failure = failure
		return result
	}
	switch {
	case s.ctx.Err() != nil:
		result.native = clipboardNativeCancelled
		result.failure = clipboardFailureCancelled
	case errors.Is(nativeCtx.Err(), context.DeadlineExceeded),
		errors.Is(err, context.DeadlineExceeded):
		result.native = clipboardNativeTimedOut
		result.failure = clipboardFailureNativeTimeout
	case errors.Is(nativeCtx.Err(), context.Canceled),
		errors.Is(err, context.Canceled):
		result.native = clipboardNativeCancelled
		result.failure = clipboardFailureCancelled
	case err == nil:
		result.native = clipboardNativeSucceeded
	default:
		result.native = clipboardNativeFailed
		result.failure = clipboardFailureNativeFailed
	}
	return result
}

func (s *ClipboardService) currentTerminalFailure() (
	clipboardFailureCategory,
	bool,
) {
	if s.outputFailure == nil {
		return clipboardFailureNone, false
	}
	select {
	case <-s.outputFailure.Failed():
		return classifyClipboardOutputFailure(s.outputFailure.Err()), true
	default:
		return clipboardFailureNone, false
	}
}

func classifyClipboardOutputFailure(err error) clipboardFailureCategory {
	switch {
	case errors.Is(err, ErrTerminalOutputClosed), errors.Is(err, io.ErrClosedPipe):
		return clipboardFailureOutputClosed
	case errors.Is(err, ErrTerminalOutputWriteTimeout):
		return clipboardFailureOutputTimeout
	default:
		return clipboardFailureOutputFailed
	}
}

type clipboardPendingRequest struct {
	id     uint64
	caller ClipboardCaller
}

// SetClipboardService installs the composition-root-owned delivery service.
func (a *App) SetClipboardService(service *ClipboardService) {
	a.clipboard = service
}

func (a *App) requestClipboardCopy(caller ClipboardCaller, text string) tea.Cmd {
	if a.clipboardPending != nil {
		a.showNotification(clipboardFailureMessage(clipboardFailureBusy), NotifyWarning)
		return nil
	}
	if failure := validateClipboardSource(text); failure != clipboardFailureNone {
		a.showNotification(clipboardFailureMessage(failure), NotifyWarning)
		return nil
	}
	if a.clipboard == nil {
		a.showNotification(
			clipboardFailureMessage(clipboardFailureOutputUnavailable),
			NotifyError,
		)
		return nil
	}
	a.clipboardRequestID++
	if a.clipboardRequestID == 0 {
		a.clipboardRequestID++
	}
	pending := &clipboardPendingRequest{id: a.clipboardRequestID, caller: caller}
	a.clipboardPending = pending
	return a.clipboard.command(pending.id, pending.caller, text)
}

func (a *App) handleClipboardResult(result clipboardResultMsg) {
	pending := a.clipboardPending
	if pending == nil ||
		result.requestID != pending.id ||
		result.caller != pending.caller {
		return
	}
	a.clipboardPending = nil
	if result.failure == clipboardFailureCancelled ||
		(a.clipboard != nil && a.clipboard.ctx.Err() != nil) {
		return
	}
	switch {
	case result.failure == clipboardFailureBusy:
		a.showNotification(clipboardFailureMessage(result.failure), NotifyWarning)
	case result.failure == clipboardFailureEmpty ||
		result.failure == clipboardFailureInvalidUTF8 ||
		result.failure == clipboardFailureOversized:
		a.showNotification(clipboardFailureMessage(result.failure), NotifyWarning)
	case result.terminal == clipboardTerminalSequenceWritten &&
		result.native == clipboardNativeSucceeded:
		a.showToast("Copied to the system clipboard.")
	case result.terminal == clipboardTerminalSequenceWritten &&
		result.native == clipboardNativeSkippedSSH:
		a.showNotification(
			"Clipboard request sent to the terminal; acceptance is not confirmed.",
			NotifyWarning,
		)
	case result.terminal == clipboardTerminalSequenceWritten:
		a.showNotification(
			"Clipboard request sent to the terminal without confirmation: "+
				clipboardFailureLabel(result.failure)+".",
			NotifyWarning,
		)
	default:
		a.showNotification(clipboardFailureMessage(result.failure), NotifyError)
	}
}

func clipboardFailureMessage(failure clipboardFailureCategory) string {
	switch failure {
	case clipboardFailureEmpty:
		return "Clipboard payload is empty; no transport started."
	case clipboardFailureInvalidUTF8:
		return "Clipboard payload is not valid UTF-8; no transport started."
	case clipboardFailureOversized:
		return "Clipboard payload exceeds the 256 KiB (262,144-byte) limit; no transport started."
	case clipboardFailureBusy:
		return "A clipboard copy is already in progress."
	case clipboardFailureOutputUnavailable:
		return "Clipboard delivery failed: terminal output is unavailable."
	case clipboardFailureOutputClosed:
		return "Clipboard delivery failed: terminal output is closed."
	case clipboardFailureOutputPartial:
		return "Clipboard delivery failed: terminal output write was partial."
	case clipboardFailureOutputTimeout:
		return "Clipboard delivery failed: terminal output write timed out."
	case clipboardFailureOutputFailed:
		return "Clipboard delivery failed: terminal output write failed."
	default:
		return "Clipboard delivery failed."
	}
}

func clipboardFailureLabel(failure clipboardFailureCategory) string {
	switch failure {
	case clipboardFailureNativeUnavailable:
		return "native helper unavailable"
	case clipboardFailureNativeTimeout:
		return "native helper timed out"
	case clipboardFailureNativeFailed:
		return "native helper failed"
	default:
		return "native delivery unavailable"
	}
}
