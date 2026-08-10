//go:build unix

package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
)

const (
	p150SlowPTYHelperEnv = "YHC_P150_SLOW_PTY_HELPER"
	p150PTYReady         = "P150_PTY_READY"
	p150PTYHeldFrame     = "P150_PTY_HELD_FRAME"
	p150PTYHeldDiff      = "HELD_FRAME"
	p150ShellUsable      = "P150_SHELL_USABLE"
)

type p150PTYProgressMsg struct{}

type p150PTYModel struct {
	mu          sync.Mutex
	view        string
	initialSeen <-chan struct{}
	status      io.Writer
}

func (m *p150PTYModel) Init() tea.Cmd {
	return func() tea.Msg {
		<-m.initialSeen
		return p150PTYProgressMsg{}
	}
}

func (m *p150PTYModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case p150PTYProgressMsg:
		var frame strings.Builder
		frame.WriteString(p150PTYHeldFrame)
		frame.WriteByte('\n')
		for index := 0; index < 256; index++ {
			_, _ = fmt.Fprintf(
				&frame,
				"streaming-%03d tool-progress=%03d Agent-progress=%03d %s\n",
				index,
				index,
				index,
				strings.Repeat("x", 96),
			)
		}
		m.mu.Lock()
		m.view = frame.String()
		m.mu.Unlock()
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+d" {
			_, _ = io.WriteString(m.status, "Q")
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *p150PTYModel) View() tea.View {
	m.mu.Lock()
	defer m.mu.Unlock()
	view := tea.NewView(m.view)
	view.AltScreen = true
	view.ReportFocus = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

type p150PTYGateWriter struct {
	output      io.Writer
	status      io.Writer
	control     io.Reader
	initialSeen chan struct{}
	initialOnce sync.Once
	blockOnce   sync.Once
}

func (w *p150PTYGateWriter) Write(p []byte) (int, error) {
	// Bubble Tea v2 emits cell diffs, so the changed suffix is the stable
	// observable write rather than the complete logical frame.
	if bytes.Contains(p, []byte(p150PTYHeldDiff)) {
		var control [1]byte
		w.blockOnce.Do(func() {
			_, _ = io.WriteString(w.status, "F")
			_, _ = io.ReadFull(w.control, control[:])
		})
	}
	n, err := w.output.Write(p)
	if bytes.Contains(p, []byte(p150PTYReady)) {
		w.initialOnce.Do(func() { close(w.initialSeen) })
	}
	return n, err
}

func TestP150SlowPTYRestoresParentShellAfterSustainedProgress(t *testing.T) {
	if os.Getenv(p150SlowPTYHelperEnv) != "" {
		runP150SlowPTYHelper()
		return
	}

	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create status pipe: %v", err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		_ = statusRead.Close()
		_ = statusWrite.Close()
		t.Fatalf("create control pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = statusRead.Close()
		_ = statusWrite.Close()
		_ = controlRead.Close()
		_ = controlWrite.Close()
	})

	script := `"$1" -test.run=^TestP150SlowPTYRestoresParentShellAfterSustainedProgress$; code=$?; printf '\nP150_SHELL_USABLE\n'; exit "$code"`
	command := exec.Command("/bin/sh", "-c", script, "p150-shell", os.Args[0])
	command.Env = append(os.Environ(), p150SlowPTYHelperEnv+"=1")
	command.ExtraFiles = []*os.File{statusWrite, controlRead}
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start slow-reader PTY helper: %v", err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = terminal.Close()
	})
	_ = statusWrite.Close()
	_ = controlRead.Close()

	var output bytes.Buffer
	var outputMu sync.Mutex
	readerPaused := make(chan struct{})
	resumeReader := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		paused := false
		for {
			n, readErr := terminal.Read(buf)
			if n > 0 {
				outputMu.Lock()
				_, _ = output.Write(buf[:n])
				ready := bytes.Contains(output.Bytes(), []byte(p150PTYReady))
				outputMu.Unlock()
				if ready && !paused {
					paused = true
					close(readerPaused)
					<-resumeReader
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	select {
	case <-readerPaused:
	case <-time.After(3 * time.Second):
		outputMu.Lock()
		got := output.String()
		outputMu.Unlock()
		t.Fatalf("PTY startup and reader pause timed out\noutput=%q", got)
	}
	assertP150StatusByte(t, statusRead, 'F', "blocked PTY frame")
	if _, err := terminal.Write([]byte{0x04}); err != nil {
		t.Fatalf("write Ctrl+D to PTY: %v", err)
	}
	assertP150StatusByte(t, statusRead, 'Q', "quit processing")

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case err := <-waitDone:
		t.Fatalf("slow PTY helper exited before its blocked frame was released: %v", err)
	default:
	}

	close(resumeReader)
	if _, err := controlWrite.Write([]byte{'R'}); err != nil {
		t.Fatalf("release blocked PTY writer: %v", err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("slow PTY helper failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("slow PTY helper timed out")
	}
	_ = terminal.Close()
	waitP150Closed(t, readDone, "PTY reader shutdown")

	outputMu.Lock()
	got := append([]byte(nil), output.Bytes()...)
	outputMu.Unlock()
	for _, marker := range []string{
		p150PTYHeldDiff,
		// Bubble Tea v2's cell renderer clips an oversized view to the
		// terminal grid. Row 22 is the last logical progress row visible
		// below the held-frame header in a 24-row PTY.
		"streaming-022",
		"tool-progress=022",
		"Agent-progress=022",
		"\x1b[?1049l",
		"\x1b[?2004l",
		"\x1b[?1004l",
		"\x1b[?25h",
		p150ShellUsable,
	} {
		if !bytes.Contains(got, []byte(marker)) {
			t.Fatalf("slow PTY output missing %q", marker)
		}
	}
	restoreAt := bytes.LastIndex(got, []byte("\x1b[?1049l"))
	shellAt := bytes.LastIndex(got, []byte(p150ShellUsable))
	if restoreAt < 0 || shellAt <= restoreAt {
		t.Fatalf("parent shell marker did not follow terminal restore: restore=%d shell=%d", restoreAt, shellAt)
	}
}

func runP150SlowPTYHelper() {
	status := os.NewFile(3, "p150-status")
	control := os.NewFile(4, "p150-control")
	if status == nil || control == nil {
		panic("p150 helper pipes unavailable")
	}
	defer status.Close()  //nolint:errcheck
	defer control.Close() //nolint:errcheck

	writer := &p150PTYGateWriter{
		output:      os.Stdout,
		status:      status,
		control:     control,
		initialSeen: make(chan struct{}),
	}
	model := &p150PTYModel{
		view:        p150PTYReady,
		initialSeen: writer.initialSeen,
		status:      status,
	}
	program := tea.NewProgram(
		model,
		tea.WithOutput(writer),
		tea.WithFPS(120),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignals(),
	)
	if _, err := program.Run(); err != nil {
		panic(err)
	}
}

func assertP150StatusByte(t *testing.T, reader io.Reader, want byte, operation string) {
	t.Helper()
	result := make(chan struct {
		value byte
		err   error
	}, 1)
	go func() {
		var value [1]byte
		_, err := io.ReadFull(reader, value[:])
		result <- struct {
			value byte
			err   error
		}{value: value[0], err: err}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("%s status read: %v", operation, got.err)
		}
		if got.value != want {
			t.Fatalf("%s status = %q, want %q", operation, got.value, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s status timed out", operation)
	}
}
