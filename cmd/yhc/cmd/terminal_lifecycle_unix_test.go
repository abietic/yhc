//go:build unix

package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	charmterm "github.com/charmbracelet/x/term"
	"github.com/creack/pty"

	"github.com/abietic/yhc/internal/tui"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

const terminalLifecycleHelperEnv = "YHC_TERMINAL_LIFECYCLE_HELPER"

type panicLifecycleModel struct{}

func (panicLifecycleModel) Init() tea.Cmd {
	return nil
}

func (panicLifecycleModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		panic("intentional TUI lifecycle panic")
	}
	return panicLifecycleModel{}, nil
}

func (panicLifecycleModel) View() tea.View {
	view := tea.NewView("panic lifecycle test")
	view.WindowTitle = "eino-agent-panic-test"
	view.AltScreen = true
	view.ReportFocus = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func TestTUITerminalRestorationPTY(t *testing.T) {
	if mode := os.Getenv(terminalLifecycleHelperEnv); mode != "" {
		runTerminalLifecycleHelper(mode)
		return
	}

	tests := []struct {
		name        string
		mode        string
		input       byte
		startup     string
		wantSuccess bool
	}{
		{name: "ctrl-d EOF", mode: "eof", input: 0x04, startup: "default", wantSuccess: true},
		{name: "panic", mode: "panic", input: 'x', startup: "\x1b[?1004h", wantSuccess: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := runTerminalLifecycleChild(t, tt.mode, tt.input, tt.startup)
			if tt.wantSuccess && err != nil {
				t.Fatalf("lifecycle helper failed: %v\n%s", err, output)
			}
			if !tt.wantSuccess && err == nil {
				t.Fatalf("panic lifecycle helper unexpectedly succeeded\n%s", output)
			}
			for _, sequence := range []string{
				"\x1b[?1049h", "\x1b[?1049l",
				"\x1b[?2004h", "\x1b[?2004l",
				"\x1b[?1004h", "\x1b[?1004l",
				"\x1b[?1002h", "\x1b[?1002l",
				"\x1b[?1006h", "\x1b[?1006l",
				"\x1b[?25h",
			} {
				if !strings.Contains(output, sequence) {
					t.Fatalf("terminal output missing %q:\n%q", sequence, output)
				}
			}
		})
	}
}

func runTerminalLifecycleHelper(mode string) {
	state := tui.SaveTerminalState()
	defer tui.PanicRecovery(state)

	caps := terminalcap.Capabilities{
		Platform:       "linux",
		Terminal:       "wezterm",
		Interactive:    true,
		FocusReporting: true,
		Mouse:          true,
		BracketedPaste: true,
		SuspendResume:  true,
	}
	var model tea.Model
	switch mode {
	case "panic":
		model = panicLifecycleModel{}
	default:
		model = tui.New(tui.Config{
			Resumed:      true,
			Fullscreen:   true,
			MouseEnabled: true,
			TerminalCaps: &caps,
		})
	}
	output, err := tui.NewTerminalOutput(os.Stdout)
	if err != nil {
		panic(err)
	}
	defer output.Close() //nolint:errcheck
	programOutput := tuiProgramOutput{Writer: output, terminal: os.Stdout}
	if !charmterm.IsTerminal(programOutput.Fd()) {
		panic("lifecycle helper stdout is not a terminal")
	}
	if _, ok := any(programOutput).(charmterm.File); !ok {
		panic("TUI program output does not preserve the terminal file contract")
	}
	program := tea.NewProgram(model, tea.WithOutput(programOutput))
	if err := runTUIProgram(program, output, state.Restore, nil); err != nil {
		panic(err)
	}
}

func runTerminalLifecycleChild(t *testing.T, mode string, input byte, startupMarker string) (string, error) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestTUITerminalRestorationPTY$")
	command.Env = append(os.Environ(), terminalLifecycleHelperEnv+"="+mode)
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start lifecycle PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck

	var output bytes.Buffer
	var outputMu sync.Mutex
	// The ordinary App waits for a rendered post-resize frame so this PTY path
	// proves Bubble Tea received its automatic initial WindowSizeMsg. The panic
	// model still waits for its last asynchronous Init command before input.
	startup := []byte(startupMarker)
	started := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		var once sync.Once
		buf := make([]byte, 4096)
		for {
			n, readErr := terminal.Read(buf)
			if n > 0 {
				outputMu.Lock()
				_, _ = output.Write(buf[:n])
				if bytes.Contains(output.Bytes(), startup) {
					once.Do(func() { close(started) })
				}
				outputMu.Unlock()
			}
			if readErr != nil {
				break
			}
		}
		close(readDone)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		_ = terminal.Close()
		<-readDone
		outputMu.Lock()
		defer outputMu.Unlock()
		t.Fatalf("wait for TUI startup timed out\n%q", output.String())
	}
	if _, err := terminal.Write([]byte{input}); err != nil {
		t.Fatalf("write lifecycle input: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()

	select {
	case err := <-waitDone:
		_ = terminal.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Fatal("PTY reader did not finish after child exit")
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		return output.String(), err
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-waitDone
		_ = terminal.Close()
		<-readDone
		outputMu.Lock()
		defer outputMu.Unlock()
		t.Fatalf("terminal lifecycle helper timed out\n%q", output.String())
		return "", nil
	}
}
