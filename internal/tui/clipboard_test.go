package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type p273RecordingWriter struct {
	mu      sync.Mutex
	packets [][]byte
	n       int
	err     error
}

func (w *p273RecordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.packets = append(w.packets, append([]byte(nil), p...))
	if w.n != 0 || w.err != nil {
		return w.n, w.err
	}
	return len(p), nil
}

func (w *p273RecordingWriter) snapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	packets := make([][]byte, len(w.packets))
	for index := range w.packets {
		packets[index] = append([]byte(nil), w.packets[index]...)
	}
	return packets
}

type p273TerminalFailureWriter struct {
	p273RecordingWriter
	failed      chan struct{}
	failure     error
	failOnWrite bool
	failOnce    sync.Once
	gateMu      sync.Mutex
	failedState bool
}

func (w *p273TerminalFailureWriter) Write(p []byte) (int, error) {
	written, err := w.p273RecordingWriter.Write(p)
	if w.failOnWrite {
		w.signalFailure()
	}
	return written, err
}

func (w *p273TerminalFailureWriter) Failed() <-chan struct{} {
	return w.failed
}

func (w *p273TerminalFailureWriter) Err() error {
	return w.failure
}

func (w *p273TerminalFailureWriter) signalFailure() {
	w.gateMu.Lock()
	defer w.gateMu.Unlock()
	w.failOnce.Do(func() {
		w.failedState = true
		close(w.failed)
	})
}

func (w *p273TerminalFailureWriter) startIfHealthy(start func()) error {
	w.gateMu.Lock()
	defer w.gateMu.Unlock()
	if w.failedState {
		return w.failure
	}
	start()
	return nil
}

func p273MissingLookPath(string) (string, error) {
	return "", errP273HelperNotFound
}

var errP273HelperNotFound = errors.New("helper not found")

func p273Service(
	ctx context.Context,
	output io.Writer,
	environment clipboardEnvironment,
	lookPath func(string) (string, error),
	runner clipboardNativeRunner,
	timeout time.Duration,
) *ClipboardService {
	return newClipboardService(
		ctx,
		output,
		environment,
		lookPath,
		runner,
		timeout,
	)
}

func TestP273ClipboardOSC52PacketsAreExact(t *testing.T) {
	source := []byte("hello")
	encoded := base64.StdEncoding.EncodeToString(source)
	tests := []struct {
		name        string
		environment clipboardEnvironment
		want        string
	}{
		{
			name: "direct",
			want: "\x1b]52;c;" + encoded + "\x07",
		},
		{
			name:        "tmux",
			environment: clipboardEnvironment{tmux: true},
			want:        "\x1bPtmux;\x1b\x1b]52;c;" + encoded + "\x07\x1b\\",
		},
		{
			name:        "screen",
			environment: clipboardEnvironment{screen: true},
			want:        "\x1bP\x1b]52;c;" + encoded + "\x07\x1b\\",
		},
		{
			name:        "tmux takes precedence",
			environment: clipboardEnvironment{tmux: true, screen: true},
			want:        "\x1bPtmux;\x1b\x1b]52;c;" + encoded + "\x07\x1b\\",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := clipboardOSC52Packet(source, test.environment)
			if string(got) != test.want {
				t.Fatalf("packet = %q, want %q", got, test.want)
			}
		})
	}
}

func TestP273ClipboardHasNoLegacyOrRawStdoutPath(t *testing.T) {
	for _, path := range []string{
		"app.go",
		"key_actions.go",
		"selection.go",
		"clipboard.go",
	} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if bytes.Contains(source, []byte("CopyToClipboard")) {
			t.Fatalf("%s retains legacy result-free clipboard path", path)
		}
		if path == "clipboard.go" && bytes.Contains(source, []byte("os.Stdout")) {
			t.Fatal("clipboard service bypasses TerminalOutput through raw os.Stdout")
		}
	}
}

func TestP273ClipboardPayloadBoundaryIsInclusiveAndNeverTruncates(t *testing.T) {
	writer := &p273RecordingWriter{}
	service := p273Service(
		context.Background(),
		writer,
		clipboardEnvironment{goos: "other"},
		p273MissingLookPath,
		nil,
		time.Second,
	)
	source := strings.Repeat("a", clipboardMaxSourceBytes)
	result := service.deliver(1, ClipboardCallerActionCopy, source)
	if result.sourceBytes != clipboardMaxSourceBytes ||
		result.terminal != clipboardTerminalSequenceWritten ||
		result.native != clipboardNativeUnavailable ||
		result.failure != clipboardFailureNativeUnavailable {
		t.Fatalf("inclusive-boundary result = %#v", result)
	}
	packets := writer.snapshot()
	if len(packets) != 1 {
		t.Fatalf("boundary writes = %d, want 1", len(packets))
	}
	wantPacket := clipboardOSC52Packet([]byte(source), clipboardEnvironment{})
	if !bytes.Equal(packets[0], wantPacket) {
		t.Fatal("boundary packet was truncated or changed")
	}
	wantBase64Length := base64.StdEncoding.EncodedLen(clipboardMaxSourceBytes)
	if got := len(packets[0]) - len("\x1b]52;c;") - len("\x07"); got != wantBase64Length {
		t.Fatalf("base64 bytes = %d, want %d", got, wantBase64Length)
	}

	oversized := strings.Repeat("b", clipboardMaxSourceBytes+1)
	result = service.deliver(2, ClipboardCallerActionCopy, oversized)
	if result.failure != clipboardFailureOversized ||
		result.terminal != clipboardTerminalNotStarted {
		t.Fatalf("oversized result = %#v", result)
	}
	if got := len(writer.snapshot()); got != 1 {
		t.Fatalf("oversized payload started transport; writes = %d", got)
	}
}

func TestP273ClipboardRejectsEmptyAndInvalidUTF8BeforeTransport(t *testing.T) {
	writer := &p273RecordingWriter{}
	service := p273Service(
		context.Background(),
		writer,
		clipboardEnvironment{},
		p273MissingLookPath,
		nil,
		time.Second,
	)
	tests := []struct {
		name string
		text string
		want clipboardFailureCategory
	}{
		{name: "empty", text: "", want: clipboardFailureEmpty},
		{name: "invalid UTF-8", text: string([]byte{0xff}), want: clipboardFailureInvalidUTF8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := service.deliver(1, ClipboardCallerChatSelection, test.text)
			if result.failure != test.want ||
				result.terminal != clipboardTerminalNotStarted {
				t.Fatalf("result = %#v, want failure %v", result, test.want)
			}
		})
	}
	if packets := writer.snapshot(); len(packets) != 0 {
		t.Fatalf("invalid payload reached transport: %q", packets)
	}
}

func TestP273ClipboardNativeRoutingIsFixedAndShellFree(t *testing.T) {
	paths := map[string]string{
		"pbcopy":         "/fixed/pbcopy",
		"wl-copy":        "/fixed/wl-copy",
		"xclip":          "/fixed/xclip",
		"xsel":           "/fixed/xsel",
		"powershell.exe": `C:\fixed\powershell.exe`,
	}
	lookPath := func(name string) (string, error) {
		path, ok := paths[name]
		if !ok {
			return "", errP273HelperNotFound
		}
		return path, nil
	}
	tests := []struct {
		name        string
		environment clipboardEnvironment
		missing     map[string]bool
		want        *clipboardNativeCommand
	}{
		{
			name:        "SSH suppresses native",
			environment: clipboardEnvironment{goos: "darwin", ssh: true},
		},
		{
			name:        "macOS pbcopy",
			environment: clipboardEnvironment{goos: "darwin"},
			want:        &clipboardNativeCommand{path: "/fixed/pbcopy"},
		},
		{
			name:        "Wayland wl-copy",
			environment: clipboardEnvironment{goos: "linux", wayland: true},
			want:        &clipboardNativeCommand{path: "/fixed/wl-copy"},
		},
		{
			name:        "Wayland falls back to xclip",
			environment: clipboardEnvironment{goos: "linux", wayland: true},
			missing:     map[string]bool{"wl-copy": true},
			want: &clipboardNativeCommand{
				path: "/fixed/xclip",
				args: []string{"-selection", "clipboard"},
			},
		},
		{
			name:        "X11 falls back to xsel",
			environment: clipboardEnvironment{goos: "linux"},
			missing:     map[string]bool{"xclip": true},
			want: &clipboardNativeCommand{
				path: "/fixed/xsel",
				args: []string{"--clipboard", "--input"},
			},
		},
		{
			name:        "Windows fixed PowerShell",
			environment: clipboardEnvironment{goos: "windows"},
			want: &clipboardNativeCommand{
				path: `C:\fixed\powershell.exe`,
				args: []string{
					"-NoLogo",
					"-NoProfile",
					"-NonInteractive",
					"-Command",
					clipboardPowerShellScript,
				},
			},
		},
		{
			name:        "other platform unavailable",
			environment: clipboardEnvironment{goos: "freebsd"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveClipboardNativeCommand(
				test.environment,
				func(name string) (string, error) {
					if test.missing[name] {
						return "", errP273HelperNotFound
					}
					return lookPath(name)
				},
			)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("route = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestP273ClipboardNativeSuccessUsesFixedArgvStdinAndDeadline(t *testing.T) {
	writer := &p273RecordingWriter{}
	var gotCommand clipboardNativeCommand
	var gotSource []byte
	var deadlineRemaining time.Duration
	runner := func(
		ctx context.Context,
		command clipboardNativeCommand,
		source []byte,
	) error {
		gotCommand = command
		gotSource = append([]byte(nil), source...)
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("native helper context has no deadline")
		}
		deadlineRemaining = time.Until(deadline)
		return nil
	}
	service := p273Service(
		context.Background(),
		writer,
		clipboardEnvironment{goos: "linux", wayland: true},
		func(name string) (string, error) { return "/fixed/" + name, nil },
		runner,
		clipboardNativeTimeout,
	)
	result := service.deliver(7, ClipboardCallerExpandSelection, "中 clipboard")
	if result.terminal != clipboardTerminalSequenceWritten ||
		result.native != clipboardNativeSucceeded ||
		result.failure != clipboardFailureNone {
		t.Fatalf("success result = %#v", result)
	}
	if !reflect.DeepEqual(
		gotCommand,
		clipboardNativeCommand{path: "/fixed/wl-copy"},
	) {
		t.Fatalf("native command = %#v", gotCommand)
	}
	if string(gotSource) != "中 clipboard" {
		t.Fatalf("native stdin = %q", gotSource)
	}
	if deadlineRemaining <= 0 || deadlineRemaining > clipboardNativeTimeout {
		t.Fatalf("native deadline remaining = %s", deadlineRemaining)
	}
}

func TestP273ClipboardSSHTimeoutFailureAndCancellationAreTyped(t *testing.T) {
	tests := []struct {
		name        string
		ctx         func() context.Context
		environment clipboardEnvironment
		timeout     time.Duration
		runner      clipboardNativeRunner
		wantNative  clipboardNativeOutcome
		wantFailure clipboardFailureCategory
		wantWrites  int
	}{
		{
			name:        "SSH skip",
			ctx:         context.Background,
			environment: clipboardEnvironment{goos: "linux", ssh: true},
			wantNative:  clipboardNativeSkippedSSH,
			wantWrites:  1,
		},
		{
			name:        "timeout",
			ctx:         context.Background,
			environment: clipboardEnvironment{goos: "darwin"},
			timeout:     10 * time.Millisecond,
			runner: func(ctx context.Context, _ clipboardNativeCommand, _ []byte) error {
				<-ctx.Done()
				return ctx.Err()
			},
			wantNative:  clipboardNativeTimedOut,
			wantFailure: clipboardFailureNativeTimeout,
			wantWrites:  1,
		},
		{
			name:        "deadline wins over late nil runner result",
			ctx:         context.Background,
			environment: clipboardEnvironment{goos: "darwin"},
			timeout:     10 * time.Millisecond,
			runner: func(ctx context.Context, _ clipboardNativeCommand, _ []byte) error {
				<-ctx.Done()
				return nil
			},
			wantNative:  clipboardNativeTimedOut,
			wantFailure: clipboardFailureNativeTimeout,
			wantWrites:  1,
		},
		{
			name:        "helper failure",
			ctx:         context.Background,
			environment: clipboardEnvironment{goos: "darwin"},
			runner: func(context.Context, clipboardNativeCommand, []byte) error {
				return errors.New("secret stderr /host/path")
			},
			wantNative:  clipboardNativeFailed,
			wantFailure: clipboardFailureNativeFailed,
			wantWrites:  1,
		},
		{
			name: "program cancelled before transport",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			environment: clipboardEnvironment{goos: "darwin"},
			wantNative:  clipboardNativeCancelled,
			wantFailure: clipboardFailureCancelled,
			wantWrites:  0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &p273RecordingWriter{}
			service := p273Service(
				test.ctx(),
				writer,
				test.environment,
				func(name string) (string, error) { return "/fixed/" + name, nil },
				test.runner,
				test.timeout,
			)
			result := service.deliver(1, ClipboardCallerChatSelection, "safe text")
			if result.native != test.wantNative || result.failure != test.wantFailure {
				t.Fatalf("result = %#v", result)
			}
			if got := len(writer.snapshot()); got != test.wantWrites {
				t.Fatalf("writes = %d, want %d", got, test.wantWrites)
			}
		})
	}
}

func TestP273ClipboardOutputFailureStopsBeforeNativeAndRedacts(t *testing.T) {
	secretErr := errors.New("secret stderr /host/path")
	tests := []struct {
		name        string
		writer      io.Writer
		wantFailure clipboardFailureCategory
	}{
		{
			name:        "unavailable",
			writer:      nil,
			wantFailure: clipboardFailureOutputUnavailable,
		},
		{
			name: "closed",
			writer: &p273RecordingWriter{
				err: ErrTerminalOutputClosed,
			},
			wantFailure: clipboardFailureOutputClosed,
		},
		{
			name: "timeout",
			writer: &p273RecordingWriter{
				err: ErrTerminalOutputWriteTimeout,
			},
			wantFailure: clipboardFailureOutputTimeout,
		},
		{
			name: "partial",
			writer: &p273RecordingWriter{
				n: 1,
			},
			wantFailure: clipboardFailureOutputPartial,
		},
		{
			name: "generic redacted failure",
			writer: &p273RecordingWriter{
				err: secretErr,
			},
			wantFailure: clipboardFailureOutputFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nativeCalls := 0
			service := p273Service(
				context.Background(),
				test.writer,
				clipboardEnvironment{goos: "darwin"},
				func(name string) (string, error) { return "/fixed/" + name, nil },
				func(context.Context, clipboardNativeCommand, []byte) error {
					nativeCalls++
					return nil
				},
				time.Second,
			)
			result := service.deliver(1, ClipboardCallerActionCopy, "safe text")
			if result.terminal != clipboardTerminalFailed ||
				result.failure != test.wantFailure {
				t.Fatalf("result = %#v", result)
			}
			if nativeCalls != 0 {
				t.Fatalf("native helper calls = %d, want 0", nativeCalls)
			}
			rendered := clipboardFailureMessage(result.failure)
			if strings.Contains(rendered, "secret") || strings.Contains(rendered, "/host/") {
				t.Fatalf("failure leaked raw error: %q", rendered)
			}
		})
	}
}

func TestP273ClipboardTerminalOutputFailureSignalsExistingLifecycle(t *testing.T) {
	sink := &terminalOutputRecordingSink{writeErr: errTerminalOutputTestSink}
	output, err := NewTerminalOutputWriter(sink, terminalOutputTestConfig())
	if err != nil {
		t.Fatalf("NewTerminalOutputWriter: %v", err)
	}
	defer output.Close() //nolint:errcheck

	service := p273Service(
		context.Background(),
		output,
		clipboardEnvironment{goos: "darwin"},
		func(name string) (string, error) { return "/fixed/" + name, nil },
		func(context.Context, clipboardNativeCommand, []byte) error {
			t.Fatal("native helper started after terminal failure")
			return nil
		},
		time.Second,
	)
	result := service.deliver(1, ClipboardCallerActionCopy, "terminal failure")
	if result.terminal != clipboardTerminalFailed ||
		result.failure != clipboardFailureOutputFailed {
		t.Fatalf("clipboard result = %#v", result)
	}
	waitTerminalOutputTest(t, output.Failed(), "clipboard terminal failure signal")
	if !errors.Is(output.Err(), errTerminalOutputTestSink) {
		t.Fatalf("TerminalOutput failure = %v", output.Err())
	}
}

func TestP273ClipboardTerminalFailurePreventsOrCancelsNativeHelper(t *testing.T) {
	t.Run("failure after OSC write prevents native start", func(t *testing.T) {
		writer := &p273TerminalFailureWriter{
			failed:      make(chan struct{}),
			failure:     ErrTerminalOutputWriteTimeout,
			failOnWrite: true,
		}
		nativeCalls := 0
		service := p273Service(
			context.Background(),
			writer,
			clipboardEnvironment{goos: "darwin"},
			func(name string) (string, error) { return "/fixed/" + name, nil },
			func(context.Context, clipboardNativeCommand, []byte) error {
				nativeCalls++
				return nil
			},
			time.Second,
		)

		result := service.deliver(
			1,
			ClipboardCallerActionCopy,
			"terminal failed after write",
		)
		if result.terminal != clipboardTerminalFailed ||
			result.native != clipboardNativeNotStarted ||
			result.failure != clipboardFailureOutputTimeout {
			t.Fatalf("terminal failure result = %#v", result)
		}
		if nativeCalls != 0 {
			t.Fatalf("native helper calls = %d, want 0", nativeCalls)
		}
	})

	t.Run("failure during native execution cancels helper", func(t *testing.T) {
		writer := &p273TerminalFailureWriter{
			failed:  make(chan struct{}),
			failure: ErrTerminalOutputClosed,
		}
		nativeEntered := make(chan struct{})
		service := p273Service(
			context.Background(),
			writer,
			clipboardEnvironment{goos: "darwin"},
			func(name string) (string, error) { return "/fixed/" + name, nil },
			func(ctx context.Context, _ clipboardNativeCommand, _ []byte) error {
				close(nativeEntered)
				<-ctx.Done()
				return ctx.Err()
			},
			time.Second,
		)
		resultCh := make(chan clipboardResultMsg, 1)
		go func() {
			resultCh <- service.deliver(
				2,
				ClipboardCallerActionCopy,
				"terminal failed during native",
			)
		}()

		select {
		case <-nativeEntered:
		case <-time.After(time.Second):
			t.Fatal("native helper did not start")
		}
		writer.signalFailure()

		select {
		case result := <-resultCh:
			if result.terminal != clipboardTerminalFailed ||
				result.native != clipboardNativeCancelled ||
				result.failure != clipboardFailureOutputClosed {
				t.Fatalf("terminal failure result = %#v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("terminal failure did not cancel native helper")
		}
	})
}

type p273RejectingStartFenceWriter struct {
	p273RecordingWriter
	failed chan struct{}
}

func (w *p273RejectingStartFenceWriter) Failed() <-chan struct{} {
	return w.failed
}

func (w *p273RejectingStartFenceWriter) Err() error {
	return nil
}

func (w *p273RejectingStartFenceWriter) startIfHealthy(func()) error {
	return ErrTerminalOutputClosed
}

func TestP273ClipboardAtomicStartFenceClosesPostCheckRace(t *testing.T) {
	writer := &p273RejectingStartFenceWriter{failed: make(chan struct{})}
	nativeCalls := 0
	service := p273Service(
		context.Background(),
		writer,
		clipboardEnvironment{goos: "darwin"},
		func(name string) (string, error) { return "/fixed/" + name, nil },
		func(context.Context, clipboardNativeCommand, []byte) error {
			nativeCalls++
			return nil
		},
		time.Second,
	)

	result := service.deliver(
		3,
		ClipboardCallerActionCopy,
		"failure linearized after precheck",
	)
	if result.terminal != clipboardTerminalFailed ||
		result.native != clipboardNativeNotStarted ||
		result.failure != clipboardFailureOutputClosed {
		t.Fatalf("atomic start-fence result = %#v", result)
	}
	if nativeCalls != 0 {
		t.Fatalf("native helper calls = %d, want 0", nativeCalls)
	}
}

type p273BlockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *p273BlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestP273ClipboardServiceConcurrentUseFailsFastWithoutQueue(t *testing.T) {
	writer := &p273BlockingWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := p273Service(
		context.Background(),
		writer,
		clipboardEnvironment{goos: "other"},
		p273MissingLookPath,
		nil,
		time.Second,
	)
	firstDone := make(chan clipboardResultMsg, 1)
	go func() {
		firstDone <- service.deliver(1, ClipboardCallerChatSelection, "first")
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not enter writer")
	}

	started := time.Now()
	second := service.deliver(2, ClipboardCallerActionCopy, "second")
	if second.failure != clipboardFailureBusy ||
		second.terminal != clipboardTerminalNotStarted {
		t.Fatalf("concurrent result = %#v", second)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("concurrent delivery queued for %s", elapsed)
	}

	close(writer.release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not finish")
	}
}
