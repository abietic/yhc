//go:build unix

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestArgumentCompletionPTY(t *testing.T) {
	const helperEnv = "YHC_ARGUMENT_COMPLETION_PTY_HELPER"
	if os.Getenv(helperEnv) == "1" {
		t.Setenv("HOME", t.TempDir())
		app := New(Config{Resumed: true})
		calls := 0
		if err := app.commandRegistry.Register(&commands.Command{Name: "inspect", Completion: []commands.ArgumentCompletion{{Choices: []string{"quick", "thorough"}}}, Execute: func(_ context.Context, ctx *commands.CommandContext) (*commands.CommandResult, error) {
			calls++
			return &commands.CommandResult{Output: "EXECUTED " + strings.Join(ctx.Args, " ")}, nil
		}}); err != nil {
			t.Fatal(err)
		}
		app.fullscreen, app.reducedMotion = true, true
		app.terminalCaps = terminalcap.Capabilities{Platform: "linux", Terminal: "xterm", Interactive: true, BracketedPaste: true}
		app.statusLineHook = func(_, _ string) (string, string) {
			marker := "OTHER"
			switch app.textarea.Value() {
			case "":
				marker = "EMPTY"
			case "/inspect ":
				marker = "PREFIX"
			case "/inspect thorough ":
				marker = "ACCEPTED"
			case "/inspect q":
				marker = "TYPED"
			}
			return fmt.Sprintf("ARG_%s calls=%d visible=%t %dx%d", marker, calls, len(app.argumentCandidates()) > 0, app.width, app.height), ""
		}
		program := tea.NewProgram(app)
		app.SetProgram(program)
		if _, err := program.Run(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(os.Stdout, "ARG_PTY_RESTORED")
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestArgumentCompletionPTY$")
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
	waitPTYContains(t, command, output, "ARG_EMPTY")
	writePTY(t, terminal, "/inspect ")
	waitPTYContains(t, command, output, "ARG_PREFIX calls=0 visible=true")
	writePTY(t, terminal, "\x1b[B\x1b[B\r")
	waitPTYContains(t, command, output, "ARG_ACCEPTED calls=0")
	writePTY(t, terminal, "\r")
	waitPTYContains(t, command, output, "EXECUTED thorough")
	waitPTYContains(t, command, output, "ARG_EMPTY calls=1")
	writePTY(t, terminal, "/inspect q")
	waitPTYContains(t, command, output, "ARG_TYPED calls=1 visible=true")
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	output.setSize(40, 24)
	waitPTYContains(t, command, output, "40x24")
	mark := output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYContainsAfter(t, command, output, mark, "ARG_TYPED calls=1 visible=false")
	mark = output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYContainsAfter(t, command, output, mark, "ARG_EMPTY calls=1")
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
		t.Fatalf("PTY quit timed out: %s", output.screenPlain())
	}
	_ = terminal.Close()
	<-readDone
	for _, expected := range []string{"ARG_PTY_RESTORED", "\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), expected) {
			t.Fatalf("missing restoration %q", expected)
		}
	}
}
