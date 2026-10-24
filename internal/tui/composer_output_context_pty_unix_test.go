//go:build unix

package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
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

type outputContextPTYModel struct {
	calls atomic.Int32
	valid atomic.Bool
}

func (m *outputContextPTYModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("output context fixture requires the streaming production path")
}

func (m *outputContextPTYModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	var input strings.Builder
	for _, message := range messages {
		if message != nil && message.Role == schema.User {
			input.WriteString(message.Content)
		}
	}
	value := input.String()
	m.valid.Store(strings.Count(value, `"kind":"terminal_output"`) == 2 &&
		strings.Contains(value, `"content":"selected error"`) && strings.Contains(value, `"content":"selected expanded error"`) &&
		strings.Contains(value, `"source":"chat_selection"`) && strings.Contains(value, `"source":"expanded_transcript_selection"`) &&
		!strings.Contains(value, "outside-before") && !strings.Contains(value, "outside-after") && !strings.Contains(value, "make test"))
	m.calls.Add(1)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "Output selections received."}}), nil
}

func TestComposerOutputContextPTY(t *testing.T) {
	const helperEnv = "YHC_OUTPUT_CONTEXT_PTY_HELPER"
	if os.Getenv(helperEnv) == "1" {
		root := t.TempDir()
		resolveHistoryLocations = func() (historyLocations, error) {
			return historyLocations{projectRoot: root, compatibilityConfigDir: filepath.Join(root, "history-config")}, nil
		}
		probe := &outputContextPTYModel{}
		eng := engine.NewQueryEngine(engine.QueryEngineConfig{
			CWD: root, TranscriptDir: t.TempDir(), ChatModel: probe, Model: "fixture-model", CommandEntrypoint: commands.EntrypointTUI,
		})
		defer eng.Close()
		caps := terminalcap.Capabilities{Platform: "linux", Terminal: "xterm", Interactive: true, Mouse: true, BracketedPaste: true}
		app := New(Config{Engine: eng, Resumed: true, Fullscreen: true, MouseEnabled: true, ReducedMotion: true, TerminalCaps: &caps, KeybindingsConfigDir: t.TempDir()})
		app.textarea.SetValue("diagnose ")
		app.textarea.CursorEnd()
		app.chat.AppendOrUpdateTool("output-context-pty", "Bash", `{"command":"make test"}`)
		app.chat.UpdateToolResult("output-context-pty", "Bash", "outside-before\nselected error\nselected expanded error\noutside-after")
		app.clipboard = newClipboardService(context.Background(), io.Discard, clipboardEnvironment{goos: "fixture"}, func(string) (string, error) {
			return "", errors.New("native clipboard is isolated in this fixture")
		}, nil, time.Second)
		app.statusLineHook = func(_, _ string) (string, string) {
			state := "C"
			if app.state == StateExpand {
				state = "E"
			}
			if probe.calls.Load() > 0 && !app.running {
				state = "S"
			}
			selection := 0
			if !app.selection.IsDragging() && (app.selection.HasSelection() || app.selection.HasExpandSelection()) {
				selection = 1
			}
			return fmt.Sprintf("OC_%s n%d c%d s%d %dx%d", state, len(app.composerElements), probe.calls.Load(), selection, app.width, app.height), ""
		}
		program := tea.NewProgram(app)
		app.SetProgram(program)
		if _, err := program.Run(); err != nil {
			t.Fatal(err)
		}
		if probe.calls.Load() != 1 || !probe.valid.Load() {
			t.Fatal("mouse-selected context did not reach the model as exactly two literal excerpts")
		}
		fmt.Fprint(os.Stdout, "OC_PTY_RESTORED")
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestComposerOutputContextPTY$")
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
	waitPTYContains(t, command, output, "OC_C n0 c0 s0")
	dragOutputPTY(t, terminal, output, "selected error")
	waitPTYContains(t, command, output, "OC_C n0 c0 s1")
	writePTY(t, terminal, "\x1ba")
	waitPTYContains(t, command, output, "OC_C n1 c0 s0")
	writePTY(t, terminal, "\x7f")
	waitPTYContains(t, command, output, "OC_C n0 c0 s0")
	writePTY(t, terminal, "\x1a")
	waitPTYContains(t, command, output, "OC_C n1 c0 s0")
	writePTY(t, terminal, "\x0f")
	waitPTYContains(t, command, output, "[EXPANDED]")
	dragOutputPTY(t, terminal, output, "selected expanded error")
	writePTY(t, terminal, "\x1ba")
	waitPTYContains(t, command, output, "OC_C n2 c0 s0")
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	output.setSize(40, 24)
	waitPTYContains(t, command, output, "40x24")
	writePTY(t, terminal, "\r")
	waitPTYContains(t, command, output, "OC_S n0 c1 s0")
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
	for _, expected := range []string{"OC_PTY_RESTORED", "\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), expected) {
			t.Fatalf("missing restoration %q", expected)
		}
	}
}

func dragOutputPTY(t *testing.T, terminal *os.File, output *lockedPTYOutput, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for row, line := range strings.Split(output.screenPlain(), "\n") {
			if index := strings.Index(line, text); index >= 0 {
				start := DefaultDisplayCellProfile().measure(line[:index], 0) + 1
				end := start + DefaultDisplayCellProfile().measure(text, 0)
				writePTY(t, terminal, fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<32;%d;%dM\x1b[<0;%d;%dm", start, row+1, end, row+1, end, row+1))
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output selection text not visible: %q\n%s", text, output.screenPlain())
}
