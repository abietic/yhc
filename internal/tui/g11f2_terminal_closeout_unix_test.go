//go:build unix

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

const (
	g11F2PTYHelperEnv       = "YHC_G11F2_PTY_HELPER"
	g11F2PTYWidthEnv        = "YHC_G11F2_PTY_WIDTH"
	g11F2PhysicalGridEnv    = "YHC_G11F2_PHYSICAL_GRID"
	g11F2TerminalNameEnv    = "YHC_G11F2_TERMINAL"
	g11F2TerminalVersionEnv = "YHC_G11F2_TERMINAL_VERSION"
	g11F2FontEnv            = "YHC_G11F2_FONT"
	g11F2FontFallbackEnv    = "YHC_G11F2_FONT_FALLBACK"
)

var g11F2ClickPattern = regexp.MustCompile(`C([0-9]+),([0-9]+)`)

type g11F2PTYModel struct {
	app           *App
	initialWidth  int
	initialHeight int
	phase         string
	streaming     bool
}

func (m *g11F2PTYModel) Init() tea.Cmd {
	return m.app.Init()
}

func (m *g11F2PTYModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		switch message.String() {
		case "s":
			m.app.chat.AppendOrUpdateAssistant(
				"G11F2_STREAM\n\n" +
					"| Live | Value |\n| --- | --- |\n| stream | 🏷 क्ष |",
			)
			m.streaming = true
			m.phase = "S"
			return m, nil
		case "t":
			if err := m.app.applyTheme(string(ThemeDaybreak)); err != nil {
				m.phase = "X"
			} else {
				m.phase = "T"
			}
			return m, nil
		case "n":
			m.app.terminalCaps.Color = terminalcap.ColorNone
			m.phase = "N"
			if strings.Contains(m.app.renderView(), "\x1b") {
				m.phase = "X"
			}
			return m, nil
		case "a":
			m.app.chat.ScrollUp(1)
			m.phase = "A"
			return m, nil
		case "q":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		_, command := m.app.Update(message)
		if m.streaming &&
			(message.Width != m.initialWidth || message.Height != m.initialHeight) {
			m.phase = "R"
		}
		return m, command
	case tea.MouseMsg:
		_, command := m.app.Update(message)
		if m.app.chat.Following() {
			m.phase = "F"
		}
		return m, command
	}

	_, command := m.app.Update(message)
	return m, command
}

func (m *g11F2PTYModel) View() tea.View {
	return m.app.View()
}

func TestG11F2TerminalLifecyclePTY(t *testing.T) {
	if os.Getenv(g11F2PTYHelperEnv) == "1" {
		runG11F2PTYHelper(t)
		return
	}

	runG11F2PTYMatrix(t, []uint16{40, 48, 72, 80, 120, 150, 180})
}

func runG11F2PTYMatrix(t *testing.T, widths []uint16) {
	t.Helper()
	if len(widths) == 0 {
		t.Fatal("G11.F2 PTY matrix requires at least one width")
	}
	initialWidth := widths[0]
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestG11F2TerminalLifecyclePTY$",
	)
	command.Env = append(
		os.Environ(),
		g11F2PTYHelperEnv+"=1",
		fmt.Sprintf("%s=%d", g11F2PTYWidthEnv, initialWidth),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	terminal, err := pty.StartWithSize(
		command,
		&pty.Winsize{Cols: initialWidth, Rows: 24},
	)
	if err != nil {
		t.Fatalf("start G11.F2 PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck

	output := newLockedPTYOutput(int(initialWidth), 24)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 8192)
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

	g11F2WaitPTYAway(t, command, output, 0, initialWidth, 24)
	clickX, clickY := g11F2ClickCoordinates(t, output.plain())
	mark := output.size()
	g11F2WritePTYClick(t, terminal, clickX, clickY)
	waitPTYContainsAfter(
		t,
		command,
		output,
		mark,
		fmt.Sprintf("F2STATUS:F %dx24", initialWidth),
	)

	mark = output.size()
	writePTY(t, terminal, "s")
	waitPTYContainsAfter(
		t,
		command,
		output,
		mark,
		fmt.Sprintf("F2STATUS:S %dx24", initialWidth),
	)
	waitPTYContainsAfter(t, command, output, mark, "G11F2_STREAM")

	lastRepaintMarker := ""
	lastRepaintObservedAt := -1
	currentWidth := initialWidth
	currentHeight := uint16(24)
	for _, width := range widths[1:] {
		currentWidth = width
		currentHeight = 26
		mark = output.size()
		if err := pty.Setsize(
			terminal,
			&pty.Winsize{Cols: width, Rows: currentHeight},
		); err != nil {
			t.Fatalf("resize G11.F2 PTY to %d columns: %v", width, err)
		}
		output.setSize(int(width), int(currentHeight))
		lastRepaintMarker = fmt.Sprintf(
			"F2STATUS:R %dx%d",
			width,
			currentHeight,
		)
		waitPTYContainsAfter(
			t,
			command,
			output,
			mark,
			lastRepaintMarker,
		)
		// The emulator proves the marker was visibly rendered. Record the raw
		// output boundary here instead of requiring the text to be contiguous
		// among Bubble Tea's ANSI cursor movements.
		lastRepaintObservedAt = output.size()
		waitPTYContainsAfter(t, command, output, mark, "G11F2_STREAM")

		mark = output.size()
		writePTY(t, terminal, "a")
		g11F2WaitPTYAway(
			t,
			command,
			output,
			mark,
			width,
			currentHeight,
		)
		clickX, clickY = g11F2ClickCoordinates(t, output.plainAfter(mark))
		mark = output.size()
		g11F2WritePTYClick(t, terminal, clickX, clickY)
		waitPTYContainsAfter(
			t,
			command,
			output,
			mark,
			fmt.Sprintf("F2STATUS:F %dx%d", width, currentHeight),
		)
	}

	mark = output.size()
	writePTY(t, terminal, "t")
	waitPTYContainsAfter(
		t,
		command,
		output,
		mark,
		fmt.Sprintf("F2STATUS:T %dx%d", currentWidth, currentHeight),
	)
	waitPTYContainsAfter(t, command, output, mark, "G11F2_STREAM")

	mark = output.size()
	writePTY(t, terminal, "n")
	waitPTYContainsAfter(
		t,
		command,
		output,
		mark,
		fmt.Sprintf("F2STATUS:N %dx%d", currentWidth, currentHeight),
	)
	waitPTYContainsAfter(t, command, output, mark, "G11F2_STREAM")

	writePTY(t, terminal, "q")
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf("G11.F2 PTY helper failed: %v\n%s", waitErr, output.raw())
		}
	case <-time.After(12 * time.Second):
		_ = command.Process.Kill()
		<-waitDone
		t.Fatalf("G11.F2 PTY helper timed out\n%s", output.raw())
	}
	_ = terminal.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("G11.F2 PTY reader did not finish")
	}

	raw := output.raw()
	for _, sequence := range []string{
		"\x1b[?1049h", "\x1b[?1049l",
		"\x1b[?1002h", "\x1b[?1002l",
		"\x1b[?1006h", "\x1b[?1006l",
		"\x1b[?25h",
	} {
		if !strings.Contains(raw, sequence) {
			t.Fatalf("G11.F2 terminal output missing lifecycle sequence %q", sequence)
		}
	}
	altEnterIndex := strings.LastIndex(raw, "\x1b[?1049h")
	altExitIndex := strings.LastIndex(raw, "\x1b[?1049l")
	restoredIndex := strings.LastIndex(raw, "G11F2_TERMINAL_RESTORED")
	if altEnterIndex < 0 || lastRepaintObservedAt <= altEnterIndex ||
		altExitIndex < lastRepaintObservedAt || restoredIndex <= altExitIndex {
		t.Fatalf(
			"G11.F2 alternate-screen repaint/restoration order invalid: enter=%d repaint-observed=%d exit=%d restored=%d",
			altEnterIndex,
			lastRepaintObservedAt,
			altExitIndex,
			restoredIndex,
		)
	}
}

func g11F2WaitPTYAway(
	t *testing.T,
	command *exec.Cmd,
	output *lockedPTYOutput,
	offset int,
	width, height uint16,
) {
	t.Helper()
	for _, marker := range []string{
		fmt.Sprintf("F2STATUS:A %dx%d", width, height),
		"G11F2_STICKY",
		"Jump to bottom",
		"G11F2_TABLE",
	} {
		waitPTYContainsAfter(t, command, output, offset, marker)
	}
	if width >= 150 {
		waitPTYContainsAfter(t, command, output, offset, "G11F2_AGENT")
	}
}

func g11F2WritePTYClick(
	t *testing.T,
	terminal *os.File,
	x, y int,
) {
	t.Helper()
	writePTY(
		t,
		terminal,
		fmt.Sprintf(
			"\x1b[<0;%d;%dM\x1b[<0;%d;%dm",
			x+1,
			y+1,
			x+1,
			y+1,
		),
	)
}

func runG11F2PTYHelper(t *testing.T) {
	t.Helper()
	width, err := strconv.Atoi(os.Getenv(g11F2PTYWidthEnv))
	if err != nil || width < 40 {
		t.Fatalf("invalid G11.F2 PTY width %q", os.Getenv(g11F2PTYWidthEnv))
	}

	query := responsiveTestEngine(t)
	capabilities := terminalcap.Capabilities{
		Platform:       "linux",
		Terminal:       "xterm",
		Interactive:    true,
		Mouse:          true,
		BracketedPaste: true,
		Color:          terminalcap.ColorTrueColor,
	}
	app := New(Config{
		Engine:       query,
		Resumed:      true,
		Fullscreen:   true,
		MouseEnabled: true,
		TerminalCaps: &capabilities,
	})
	explorer := p313ExplorerSnapshot()
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution("g11f2-agent", 1, engine.TaskExplorerExecutionRunning),
	}
	explorer.Executions[0].Task = "G11F2_AGENT"
	installP313ExplorerSnapshot(app, &explorer)
	updateAppSilent(app, tea.WindowSizeMsg{Width: width, Height: 24})
	app.reducedMotion = true
	// Keep one fixed-size Unicode user-message payload in the real PTY
	// resize/click matrix. P41.1 promotion uses this as lifecycle evidence only;
	// tab origins and exact cell geometry remain owned by its deterministic
	// differential fixtures.
	app.chat.AppendUser("G11F2_STICKY 👩‍💻e\u0301")
	for index := range 36 {
		app.chat.AppendSystem(fmt.Sprintf("frozen row %02d", index))
	}
	app.chat.AppendOrUpdateAssistant(
		"| Item | Value |\n" +
			"| --- | --- |\n" +
			"| G11F2_TABLE | 🏷 क्ष |\n\n" +
			"G11F2_TABLE",
	)
	app.chat.FinishAssistant()
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatRect.Height)
	app.chat.ScrollUp(1)
	if app.chat.Following() {
		t.Fatal("G11.F2 helper failed to establish away state")
	}

	model := &g11F2PTYModel{
		app:           app,
		initialWidth:  width,
		initialHeight: 24,
		phase:         "A",
	}
	app.statusLineHook = func(_, _ string) (string, string) {
		geometry := app.chat.pillGeometry(
			app.layout.chatRect.Width,
			app.layout.chatRect.Height,
		)
		clickX := app.layout.chatRect.X +
			(geometry.start+geometry.end-1)/2
		clickY := app.layout.chatRect.Y + geometry.row
		return fmt.Sprintf(
			"F2STATUS:%s %dx%d C%d,%d",
			model.phase,
			app.width,
			app.height,
			clickX,
			clickY,
		), ""
	}

	program := tea.NewProgram(
		model,
	)
	app.SetProgram(program)
	if _, err := program.Run(); err != nil {
		t.Fatalf("run G11.F2 TUI: %v", err)
	}
	fmt.Fprint(os.Stdout, "G11F2_TERMINAL_RESTORED")
}

func g11F2ClickCoordinates(t *testing.T, output string) (int, int) {
	t.Helper()
	matches := g11F2ClickPattern.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		t.Fatalf("G11.F2 status did not publish click coordinates:\n%s", output)
	}
	last := matches[len(matches)-1]
	x, err := strconv.Atoi(last[1])
	if err != nil {
		t.Fatalf("parse G11.F2 click X: %v", err)
	}
	y, err := strconv.Atoi(last[2])
	if err != nil {
		t.Fatalf("parse G11.F2 click Y: %v", err)
	}
	return x, y
}

func TestG11F2PhysicalGridDiagnostic(t *testing.T) {
	if os.Getenv(g11F2PhysicalGridEnv) != "1" {
		t.Skip("opt-in physical terminal/font diagnostic; PTY evidence does not make a physical-grid claim")
	}
	metadata := map[string]string{
		"terminal":         os.Getenv(g11F2TerminalNameEnv),
		"terminal_version": os.Getenv(g11F2TerminalVersionEnv),
		"font":             os.Getenv(g11F2FontEnv),
		"font_fallback":    os.Getenv(g11F2FontFallbackEnv),
	}
	for field, value := range metadata {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("physical-grid diagnostic requires %s metadata", field)
		}
	}

	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open controlling terminal: %v", err)
	}
	defer terminal.Close() //nolint:errcheck
	if !term.IsTerminal(terminal.Fd()) {
		t.Fatal("physical-grid diagnostic requires an interactive controlling terminal")
	}
	state, err := term.MakeRaw(terminal.Fd())
	if err != nil {
		t.Fatalf("enter raw terminal mode: %v", err)
	}
	defer func() {
		_ = term.Restore(terminal.Fd(), state)
		_, _ = terminal.Write([]byte("\x1b[0m\r\n"))
	}()

	profile := DefaultDisplayCellProfile()
	fixtures := []string{"ASCII", "中", "e\u0301", "क्ष", "❤️", "👩🏽‍💻", "🇺🇸", "1️⃣"}
	for _, fixture := range fixtures {
		_, _ = terminal.Write([]byte("\x1b[2J\x1b[H"))
		beforeRow, beforeColumn := g11F2ReadCursorPosition(t, terminal)
		if _, err := terminal.Write([]byte(fixture)); err != nil {
			t.Fatalf("write physical-grid fixture %q: %v", fixture, err)
		}
		afterRow, afterColumn := g11F2ReadCursorPosition(t, terminal)
		if beforeRow != afterRow {
			t.Fatalf(
				"physical-grid fixture %q wrapped rows: before=%d after=%d",
				fixture,
				beforeRow,
				afterRow,
			)
		}
		actual := afterColumn - beforeColumn
		expected := profile.width(fixture)
		t.Logf(
			"terminal=%q version=%q font=%q fallback=%q fixture=%q expected_cells=%d cursor_cells=%d",
			metadata["terminal"],
			metadata["terminal_version"],
			metadata["font"],
			metadata["font_fallback"],
			fixture,
			expected,
			actual,
		)
		if actual != expected {
			t.Fatalf(
				"physical terminal/font grid differs for %q: profile=%d cursor=%d",
				fixture,
				expected,
				actual,
			)
		}
	}
}

func g11F2ReadCursorPosition(t *testing.T, terminal *os.File) (int, int) {
	t.Helper()
	if _, err := terminal.Write([]byte("\x1b[6n")); err != nil {
		t.Fatalf("request cursor position: %v", err)
	}
	response := make([]byte, 0, 24)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		descriptors := []unix.PollFd{{
			Fd:     int32(terminal.Fd()),
			Events: unix.POLLIN,
		}}
		ready, err := unix.Poll(descriptors, 100)
		if err != nil {
			t.Fatalf("poll cursor response: %v", err)
		}
		if ready == 0 {
			continue
		}
		var value [1]byte
		if _, err := terminal.Read(value[:]); err != nil {
			t.Fatalf("read cursor response: %v", err)
		}
		response = append(response, value[0])
		if value[0] == 'R' {
			break
		}
	}
	match := regexp.MustCompile(`^\x1b\[([0-9]+);([0-9]+)R$`).
		FindStringSubmatch(string(response))
	if len(match) != 3 {
		t.Fatalf("invalid cursor-position response %q", response)
	}
	row, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("parse cursor row: %v", err)
	}
	column, err := strconv.Atoi(match[2])
	if err != nil {
		t.Fatalf("parse cursor column: %v", err)
	}
	return row, column
}
