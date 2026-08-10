//go:build unix

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestTUITerminalShutdownRestoresTermiosAndKillsOwnedShellTreePTY(t *testing.T) {
	timeout := 15 * time.Second
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	readyFile := filepath.Join(root, "ready")
	commandText := fmt.Sprintf("sleep 600 & child=$!; echo $child > %q; : > %q; wait $child", pidFile, readyFile)
	provider := newTerminalShutdownScriptedProvider(t, commandText)
	defer provider.Close()

	binary := filepath.Join(root, "yhc")
	build := exec.Command("go", "build", "-o", binary, "./cmd/yhc")
	build.Dir = projectRootForTerminalShutdownPTY(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real binary: %v (%s)", err, boundedTerminalShutdownDiagnostic(output))
	}

	arguments := []string{
		"--provider", "openai", "--model", "gpt-4o", "--base-url", provider.URL,
		"--api-key", "test-key", "--tools", "Bash", "--yolo", "--sandbox", "danger-full-access",
	}
	command := exec.Command("/bin/sh", append([]string{"-c", `IFS= read -r _; exec "$@"`, "terminal-shutdown-gate", binary}, arguments...)...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"TERM=xterm-256color",
	)
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start real binary PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck
	entry := terminalShutdownTermios(t, terminal)
	if _, err := terminal.Write([]byte("\n")); err != nil {
		t.Fatalf("release real binary startup gate: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	exited := false

	output := newTerminalShutdownPTYOutput(80, 24)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 4096)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				output.append(buffer[:count])
			}
			if readErr != nil {
				return
			}
		}
	}()
	replyDone := make(chan struct{})
	go func() {
		defer close(replyDone)
		buffer := make([]byte, 256)
		for {
			count, readErr := output.emulator.Read(buffer)
			if count > 0 {
				_, _ = terminal.Write(buffer[:count])
			}
			if readErr != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		if !exited && command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_ = command.Process.Kill()
			select {
			case <-waitDone:
			case <-time.After(time.Second):
				t.Error("CLI did not exit during fallback cleanup")
			}
		}
		_ = output.emulator.Close()
		_ = terminal.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("PTY reader did not stop during cleanup")
		}
		select {
		case <-replyDone:
		case <-time.After(time.Second):
			t.Error("PTY reply pump did not stop during cleanup")
		}
	})

	waitTerminalShutdownPTY(t, timeout, func() bool {
		return output.emulator.IsAltScreen()
	}, "TUI startup")
	waitTerminalShutdownPTY(t, timeout, func() bool {
		active := terminalShutdownTermios(t, terminal)
		return !reflect.DeepEqual(active, entry)
	}, "active raw terminal mode")
	waitTerminalShutdownPTY(t, timeout, func() bool {
		return strings.Contains(output.screen(), "Ask anything...")
	}, "TUI composer ready")
	const prompt = "pty-owned-shell"
	if _, err := terminal.Write([]byte(prompt)); err != nil {
		t.Fatalf("type TUI prompt: %v", err)
	}
	waitTerminalShutdownPTY(t, timeout, func() bool {
		return strings.Contains(output.screen(), prompt)
	}, "TUI prompt echo")
	if _, err := terminal.Write([]byte("\r")); err != nil {
		t.Fatalf("submit TUI prompt: %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(timeout):
		t.Fatalf("provider did not receive first request category=calls_%d", provider.calls())
	}
	waitTerminalShutdownPTY(t, timeout, func() bool {
		_, err := os.Stat(readyFile)
		return err == nil
	}, fmt.Sprintf("Bash child readiness provider_calls=%d", provider.calls()))

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read Bash child PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid Bash child PID category=%q", strings.TrimSpace(string(pidBytes)))
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("Bash child not alive pid=%d state=%v", pid, err)
	}

	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatalf("request supported TUI cancellation: %v", err)
	}
	waitTerminalShutdownPTY(t, timeout, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}, "owned Bash child cleanup")
	waitTerminalShutdownPTY(t, timeout, func() bool {
		return strings.Contains(output.screen(), "Request interrupted.")
	}, "TUI interruption settlement")
	waitTerminalShutdownPTY(t, timeout, func() bool {
		screen := output.screen()
		return !strings.Contains(screen, "Cancelling...") &&
			!strings.Contains(screen, "Cancellation in progress")
	}, "TUI cancellation terminal settlement")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatalf("request supported TUI shutdown confirmation: %v", err)
	}
	waitTerminalShutdownPTY(t, timeout, func() bool {
		return strings.Contains(output.screen(), "Press Ctrl+C again to exit")
	}, "TUI shutdown confirmation")
	if _, err := terminal.Write([]byte("\x03")); err != nil {
		t.Fatalf("confirm supported TUI shutdown: %v", err)
	}
	select {
	case err := <-waitDone:
		exited = true
		if err == nil {
			t.Log("CLI exited after shutdown")
		}
	case <-time.After(timeout):
		t.Fatalf("CLI shutdown timed out pid=%d output=%s", command.Process.Pid, boundedTerminalShutdownDiagnostic(output.snapshot()))
	}

	final := terminalShutdownTermios(t, terminal)
	if !reflect.DeepEqual(final, entry) {
		t.Fatalf("terminal state was not restored category=termios")
	}
	for _, sequence := range []string{"\x1b[?1049h", "\x1b[?1049l", "\x1b[?2004h", "\x1b[?2004l", "\x1b[?25h"} {
		if !bytes.Contains(output.snapshot(), []byte(sequence)) {
			t.Fatalf("terminal protocol missing %q output=%s", sequence, boundedTerminalShutdownDiagnostic(output.snapshot()))
		}
	}
}

type terminalShutdownPTYOutput struct {
	mu       sync.Mutex
	b        []byte
	emulator *vt.SafeEmulator
}

func newTerminalShutdownPTYOutput(width, height int) *terminalShutdownPTYOutput {
	return &terminalShutdownPTYOutput{emulator: vt.NewSafeEmulator(width, height)}
}

func (o *terminalShutdownPTYOutput) append(value []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.b) < 16<<10 {
		o.b = append(o.b, value...)
		if len(o.b) > 16<<10 {
			o.b = o.b[:16<<10]
		}
	}
	_, _ = o.emulator.Write(value)
}

func (o *terminalShutdownPTYOutput) screen() string { return o.emulator.Render() }

func (o *terminalShutdownPTYOutput) snapshot() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.b...)
}

func terminalShutdownTermios(t *testing.T, terminal *os.File) *term.State {
	t.Helper()
	state, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatalf("read PTY termios: %v", err)
	}
	return state
}

func waitTerminalShutdownPTY(t *testing.T, timeout time.Duration, ready func() bool, category string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", category)
}

type terminalShutdownScriptedProvider struct {
	URL string
	*httptest.Server
	command string
	mu      sync.Mutex
	count   int
	started chan struct{}
	once    sync.Once
}

func newTerminalShutdownScriptedProvider(t *testing.T, command string) *terminalShutdownScriptedProvider {
	t.Helper()
	p := &terminalShutdownScriptedProvider{command: command, started: make(chan struct{})}
	p.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		p.once.Do(func() { close(p.started) })
		p.mu.Lock()
		p.count++
		p.mu.Unlock()
		defer request.Body.Close() //nolint:errcheck
		if request.Method != http.MethodPost || request.URL.Path != "/responses" {
			http.Error(writer, "contract", http.StatusBadRequest)
			return
		}
		if _, err := io.Copy(io.Discard, io.LimitReader(request.Body, 64<<10)); err != nil {
			http.Error(writer, "body", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"type": "function_call", "id": "item-bash", "call_id": "bash", "name": "Bash", "arguments": fmt.Sprintf(`{"command":%q}`, p.command), "status": "completed"}
		for _, event := range []any{
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": "response", "object": "response", "status": "completed", "model": "gpt-4o", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		} {
			encoded, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.(map[string]any)["type"], encoded)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	p.URL = p.Server.URL
	return p
}

func (p *terminalShutdownScriptedProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

func projectRootForTerminalShutdownPTY(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}
	return root
}

func boundedTerminalShutdownDiagnostic(value []byte) string {
	value = bytes.ReplaceAll(value, []byte("\x1b"), []byte("<ESC>"))
	if len(value) > 512 {
		value = value[:512]
	}
	return strconv.Quote(string(value))
}
