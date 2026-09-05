//go:build unix

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestCommandArgumentGhostPTY(t *testing.T) {
	const helperEnv = "YHC_COMMAND_HINT_PTY_HELPER"
	if os.Getenv(helperEnv) == "1" {
		t.Setenv("HOME", t.TempDir())
		app := New(Config{Resumed: true})
		source := skills.NewSkillRegistry()
		source.Register(&skills.Skill{Name: "inspect", Description: "Inspect a package", ArgumentHint: "<package>", Content: "Inspect $ARGUMENTS"})
		app.commandRegistry.SetSkillRegistry(source)
		app.fullscreen = true
		app.reducedMotion = true
		app.terminalCaps = terminalcap.Capabilities{Platform: "linux", Terminal: "xterm", Interactive: true, BracketedPaste: true}
		app.statusLineHook = func(_, _ string) (string, string) {
			marker := "OTHER"
			switch app.textarea.Value() {
			case "":
				marker = "EMPTY"
			case "/skill:inspect ":
				marker = "READY"
			case "/skill:inspect engine":
				marker = "TYPED"
			}
			return fmt.Sprintf("HINT_%s %dx%d", marker, app.width, app.height), ""
		}
		program := tea.NewProgram(app)
		app.SetProgram(program)
		if _, err := program.Run(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(os.Stdout, "HINT_PTY_RESTORED")
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCommandArgumentGhostPTY$")
	command.Env = append(os.Environ(), helperEnv+"=1", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	output := newLockedPTYOutput(80, 24)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 8192)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				output.append(buffer[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		_ = terminal.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("PTY reader did not exit")
		}
	})
	waitPTYContains(t, command, output, "HINT_EMPTY")
	writePTY(t, terminal, "/inspect\t")
	waitPTYContains(t, command, output, "HINT_READY")
	if !strings.Contains(output.screenPlain(), "<package>") {
		t.Fatalf("ghost missing from PTY frame: %s", output.screenPlain())
	}
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	output.setSize(40, 24)
	waitPTYContains(t, command, output, "40x24")
	if !strings.Contains(output.screenPlain(), "<package>") {
		t.Fatalf("ghost missing after resize: %s", output.screenPlain())
	}
	writePTY(t, terminal, "\t\x1b[Cengine")
	waitPTYContains(t, command, output, "HINT_TYPED")
	if strings.Contains(output.screenPlain(), "<package>") {
		t.Fatalf("ghost survived real argument: %s", output.screenPlain())
	}
	mark := output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYContainsAfter(t, command, output, mark, "HINT_EMPTY")
	writePTY(t, terminal, "/quit\r")
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-done
		waited = true
		t.Fatal("PTY quit timed out")
	}
	_ = terminal.Close()
	<-readDone
	raw := output.raw()
	for _, expected := range []string{"HINT_PTY_RESTORED", "\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("missing restoration %q", expected)
		}
	}
}
