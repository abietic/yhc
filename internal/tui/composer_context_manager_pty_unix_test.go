//go:build unix

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/tui/terminalcap"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/creack/pty"
)

type contextManagerPTYModel struct {
	calls atomic.Int32
	valid atomic.Bool
}

func (m *contextManagerPTYModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("context manager fixture requires streaming")
}

func (m *contextManagerPTYModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	var input strings.Builder
	for _, message := range messages {
		if message != nil && message.Role == schema.User {
			input.WriteString(message.Content)
		}
	}
	value := input.String()
	m.valid.Store(strings.Count(value, `"kind":"file"`) == 1 && strings.Count(value, `"kind":"terminal_output"`) == 1 && strings.Contains(value, `"content":"kept file"`) && strings.Contains(value, `"content":"retained output"`) && !strings.Contains(value, "removed file") && !strings.Contains(value, "batch removed"))
	m.calls.Add(1)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "Managed contexts received."}}), nil
}

func TestComposerContextManagerPTY(t *testing.T) {
	const helperEnv = "YHC_CONTEXT_MANAGER_PTY_HELPER"
	if os.Getenv(helperEnv) == "1" {
		root := t.TempDir()
		resolveHistoryLocations = func() (historyLocations, error) {
			return historyLocations{projectRoot: root, compatibilityConfigDir: filepath.Join(root, "history-config")}, nil
		}
		probe := &contextManagerPTYModel{}
		eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: root, TranscriptDir: t.TempDir(), ChatModel: probe, Model: "gpt-4o", CommandEntrypoint: commands.EntrypointTUI})
		defer eng.Close()
		caps := terminalcap.Capabilities{Platform: "linux", Terminal: "xterm", Interactive: true, Mouse: true, BracketedPaste: true}
		app := New(Config{Engine: eng, Resumed: true, Fullscreen: true, MouseEnabled: true, ReducedMotion: true, TerminalCaps: &caps, KeybindingsConfigDir: t.TempDir()})
		contextManagerFixture(app, []string{"@same", "@same", "[Output #1]", "@batch-one", "@batch-two"}, []string{"file", "file", "terminal_output", "file", "file"}, []string{"kept file", "removed file", "retained output", "batch removed one", "batch removed two" + strings.Repeat("x", 128000*4)})
		app.statusLineHook = func(_, _ string) (string, string) {
			state := "C"
			if app.hasDialog(StateContextManager) {
				state = "M"
			}
			if probe.calls.Load() > 0 && !app.running {
				state = "S"
			}
			return fmt.Sprintf("CM_%s n%d c%d %dx%d", state, len(app.composerElements), probe.calls.Load(), app.width, app.height), ""
		}
		program := tea.NewProgram(app)
		app.SetProgram(program)
		if _, err := program.Run(); err != nil {
			t.Fatal(err)
		}
		if probe.calls.Load() != 1 || !probe.valid.Load() {
			t.Fatal("managed context submission changed or retained a removed payload")
		}
		fmt.Fprint(os.Stdout, "CM_PTY_RESTORED")
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestComposerContextManagerPTY$")
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
	waitPTYContains(t, command, output, "CM_C n5 c0")
	writePTY(t, terminal, "\x1bc")
	waitPTYContains(t, command, output, "Attached context (5)")
	waitPTYContains(t, command, output, "Attachments: ~")
	waitPTYContains(t, command, output, "Window: 128.0k")
	waitPTYContains(t, command, output, "of window")
	waitPTYContains(t, command, output, "Alert: attached est. >=100% window")
	writePTY(t, terminal, "s\x1b[H")
	waitPTYContains(t, command, output, "Sort: Tokens high")
	waitPTYContains(t, command, output, "1. @batch-two")
	writePTY(t, terminal, "s")
	waitPTYContains(t, command, output, "Sort: Tokens low")
	writePTY(t, terminal, "s")
	waitPTYContains(t, command, output, "Sort: Draft")
	writePTY(t, terminal, "\x1b[H\x1b[B")
	waitPTYContains(t, command, output, "removed file")
	writePTY(t, terminal, "\x1b[3~")
	waitPTYContains(t, command, output, "CM_M n4 c0")
	undoOffset := output.size()
	writePTY(t, terminal, "\x1a")
	waitPTYContainsAfter(t, command, output, undoOffset, "CM_M n5 c0")
	writePTY(t, terminal, "\x1b[H\x1b[B\x1b[3~")
	waitPTYContains(t, command, output, "CM_M n4 c0")
	// Search is a separate input owner. Filter ANDs the query; Ctrl+A marks
	// every match, and one undo restores both removed payloads together.
	writePTY(t, terminal, "s/batch\r\t\x01")
	waitPTYContains(t, command, output, "Type: Files | 2 marked")
	writePTY(t, terminal, "\x1b[3~")
	waitPTYContains(t, command, output, "CM_M n2 c0")
	waitPTYContains(t, command, output, "No matching context")
	if strings.Contains(output.screenPlain(), "Alert:") {
		t.Fatal("removed sorted batch left a stale budget warning")
	}
	undoOffset = output.size()
	writePTY(t, terminal, "\x1a")
	waitPTYContainsAfter(t, command, output, undoOffset, "CM_M n4 c0")
	waitPTYContainsAfter(t, command, output, undoOffset, "Alert: attached est. >=100% window")
	writePTY(t, terminal, "\x01\x1b[3~")
	waitPTYContains(t, command, output, "CM_M n2 c0")
	// Clear the query and select Output without altering the remaining file.
	writePTY(t, terminal, "/\x15\r\t")
	waitPTYContains(t, command, output, "Type: Output | 0 marked")
	waitPTYContains(t, command, output, "retained output")
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	output.setSize(40, 24)
	waitPTYContains(t, command, output, "40x24")
	writePTY(t, terminal, "\x1b")
	waitPTYContains(t, command, output, "CM_C n2 c0")
	writePTY(t, terminal, "\r")
	waitPTYContains(t, command, output, "CM_S n0 c1")
	writePTY(t, terminal, "/quit\r")
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatalf("PTY: %v: %s", err, output.raw())
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-done
		waited = true
		t.Fatal("PTY quit timed out")
	}
	_ = terminal.Close()
	<-readDone
	for _, expected := range []string{"CM_PTY_RESTORED", "\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), expected) {
			t.Fatalf("missing restoration %q", expected)
		}
	}
}
