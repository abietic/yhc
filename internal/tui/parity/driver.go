//go:build parity

package parity

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Driver manages spawning a TUI binary in a PTY and interacting with it.
type Driver struct {
	config BinaryConfig
	width  int
	height int

	cmd  *exec.Cmd
	ptmx *os.File
	emu  *ScreenEmulator

	mu   sync.Mutex
	done chan struct{}
}

// NewDriver creates a Driver for the given binary config and terminal dimensions.
func NewDriver(cfg BinaryConfig, width, height int) *Driver {
	if width == 0 {
		width = 100
	}
	if height == 0 {
		height = 30
	}
	return &Driver{
		config: cfg,
		width:  width,
		height: height,
		emu:    NewScreenEmulator(width, height),
		done:   make(chan struct{}),
	}
}

// Start spawns the binary in a PTY and begins reading output into the emulator.
func (d *Driver) Start(ctx context.Context) error {
	d.cmd = exec.CommandContext(ctx, d.config.Command, d.config.Args...)
	if d.config.WorkDir != "" {
		d.cmd.Dir = d.config.WorkDir
	}

	d.cmd.Env = parityEnvironment(os.Environ(), d.config.Env, d.config.UnsetEnv)
	// Disable mouse to simplify output parsing
	d.cmd.Env = append(d.cmd.Env, "YHC_DISABLE_MOUSE=1")
	// Force a known terminal size via COLUMNS/LINES
	d.cmd.Env = append(d.cmd.Env,
		fmt.Sprintf("COLUMNS=%d", d.width),
		fmt.Sprintf("LINES=%d", d.height),
	)

	size := &pty.Winsize{
		Rows: uint16(d.height),
		Cols: uint16(d.width),
	}

	ptmx, err := pty.StartWithSize(d.cmd, size)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	d.ptmx = ptmx

	// Pipe PTY output into the VT emulator
	go func() {
		defer close(d.done)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				d.emu.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// Drain emulator input responses (terminal query replies like OSC 10/11)
	// and forward them back to the PTY so the application doesn't block.
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := d.emu.emu.Read(buf)
			if n > 0 {
				d.ptmx.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	return nil
}

func parityEnvironment(inherited, overrides, unset []string) []string {
	blocked := make(map[string]struct{}, len(unset))
	for _, key := range unset {
		blocked[key] = struct{}{}
	}
	env := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if _, remove := blocked[key]; !remove {
			env = append(env, entry)
		}
	}
	return append(env, overrides...)
}

// SendText types literal text into the PTY stdin.
func (d *Driver) SendText(s string) error {
	_, err := d.ptmx.WriteString(s)
	return err
}

// SendKey sends a special key sequence to the PTY.
func (d *Driver) SendKey(key string) error {
	seq, ok := keyMap[key]
	if !ok {
		return fmt.Errorf("unknown key: %q", key)
	}
	_, err := d.ptmx.Write([]byte(seq))
	return err
}

// WaitForStable waits until screen content stops changing for stableDuration.
func (d *Driver) WaitForStable(timeout, stableDuration time.Duration) error {
	deadline := time.Now().Add(timeout)
	pollInterval := 50 * time.Millisecond
	lastContent := ""
	lastChangeTime := time.Now()

	for time.Now().Before(deadline) {
		current := d.emu.PlainText()
		if current != lastContent {
			lastContent = current
			lastChangeTime = time.Now()
		} else if time.Since(lastChangeTime) >= stableDuration {
			return nil
		}

		select {
		case <-d.done:
			return io.EOF
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("screen did not stabilize within %v", timeout)
}

// WaitForPattern waits until screen content matches the regex pattern.
func (d *Driver) WaitForPattern(pattern string, timeout time.Duration) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}

	deadline := time.Now().Add(timeout)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		content := d.emu.PlainText()
		if re.MatchString(content) {
			return nil
		}

		select {
		case <-d.done:
			return fmt.Errorf("process exited before pattern matched; screen:\n%s", d.emu.PlainText())
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("pattern %q not found within %v; screen:\n%s", pattern, timeout, d.emu.PlainText())
}

// Screen returns the current plain text screen content.
func (d *Driver) Screen() string {
	return d.emu.PlainText()
}

// RawScreen returns the current screen content with ANSI sequences.
func (d *Driver) RawScreen() string {
	return d.emu.RawRender()
}

// CaptureScreen takes a snapshot of the current screen state.
func (d *Driver) CaptureScreen(scenarioName, captureID string, project Project) *Capture {
	return &Capture{
		Project:   project,
		Scenario:  scenarioName,
		CaptureID: captureID,
		Raw:       d.emu.RawRender(),
		Plain:     d.emu.PlainText(),
		Timestamp: time.Now(),
		AltScreen: d.emu.IsAltScreen(),
	}
}

// Stop terminates the process and closes the PTY.
func (d *Driver) Stop() error {
	if d.cmd != nil && d.cmd.Process != nil {
		d.cmd.Process.Signal(os.Interrupt)
		timer := time.AfterFunc(2*time.Second, func() {
			d.cmd.Process.Kill()
		})
		d.cmd.Wait()
		timer.Stop()
	}
	if d.ptmx != nil {
		d.ptmx.Close()
	}
	<-d.done
	return nil
}

// keyMap maps human-readable key names to their escape sequences.
var keyMap = map[string]string{
	"enter":     "\r",
	"tab":       "\t",
	"esc":       "\x1b",
	"escape":    "\x1b",
	"backspace": "\x7f",
	"delete":    "\x1b[3~",
	"up":        "\x1b[A",
	"down":      "\x1b[B",
	"right":     "\x1b[C",
	"left":      "\x1b[D",
	"home":      "\x1b[H",
	"end":       "\x1b[F",
	"ctrl+c":    "\x03",
	"ctrl+d":    "\x04",
	"ctrl+l":    "\x0c",
	"ctrl+z":    "\x1a",
	"ctrl+t":    "\x14",
	"space":     " ",
}
