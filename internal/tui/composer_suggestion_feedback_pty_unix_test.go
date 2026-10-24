//go:build unix

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

type suggestionPTYModel struct{ calls atomic.Int32 }

func (m *suggestionPTYModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("direct generation is outside the suggestion contract")
}

func (m *suggestionPTYModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	text := "run the focused tests"
	if m.calls.Add(1) == 1 {
		text = " "
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: text,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}},
	}}), nil
}

func TestComposerSuggestionManualPTY(t *testing.T) {
	const helperEnv = "YHC_SUGGESTION_FEEDBACK_PTY_HELPER"
	if os.Getenv(helperEnv) == "1" {
		t.Setenv("HOME", t.TempDir())
		probe := &suggestionPTYModel{}
		eng := engine.NewQueryEngine(engine.QueryEngineConfig{
			CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: probe, Model: "fixture-model",
			CommandEntrypoint: commands.EntrypointTUI, EnablePromptSuggestions: true,
		})
		defer eng.Close()
		eng.SetResumedMessages([]*schema.Message{{Role: schema.Assistant, Content: "first"}, {Role: schema.Assistant, Content: "second"}})
		app := New(Config{Engine: eng, Resumed: true})
		app.fullscreen, app.reducedMotion = true, true
		app.terminalCaps = terminalcap.Capabilities{Platform: "linux", Terminal: "xterm", Interactive: true, BracketedPaste: true}
		app.statusLineHook = func(_, _ string) (string, string) {
			state := "EMPTY"
			if app.visibleComposerSuggestion() != "" {
				state = "GHOST"
			}
			if app.textarea.Value() == "run the focused tests" {
				state = "DRAFT"
			}
			if strings.Contains(app.composerSuggestion.Feedback, "No next step") {
				state = "NO_STEP"
			}
			return fmt.Sprintf("SUG_%s calls=%d %dx%d", state, probe.calls.Load(), app.width, app.height), ""
		}
		program := tea.NewProgram(app)
		app.SetProgram(program)
		if _, err := program.Run(); err != nil {
			t.Fatal(err)
		}
		if probe.calls.Load() != 4 || len(eng.GetMessages()) != 2 {
			t.Fatal("suggestion gesture dispatched a turn or changed conversation")
		}
		fmt.Fprint(os.Stdout, "SUG_PTY_RESTORED")
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestComposerSuggestionManualPTY$")
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
	waitPTYContains(t, command, output, "SUG_EMPTY calls=0")
	writePTY(t, terminal, "\x00")
	waitPTYContains(t, command, output, "SUG_NO_STEP calls=1")
	writePTY(t, terminal, "\x00")
	waitPTYContains(t, command, output, "SUG_GHOST calls=2")
	writePTY(t, terminal, "\t")
	waitPTYContains(t, command, output, "SUG_DRAFT calls=2")
	writePTY(t, terminal, "\x1a")
	waitPTYContains(t, command, output, "SUG_EMPTY calls=2")
	writePTY(t, terminal, "\x00")
	waitPTYContains(t, command, output, "SUG_GHOST calls=3")
	writePTY(t, terminal, "\r")
	waitPTYContains(t, command, output, "SUG_EMPTY calls=3")
	writePTY(t, terminal, "\x00")
	waitPTYContains(t, command, output, "SUG_GHOST calls=4")
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	output.setSize(40, 24)
	waitPTYContains(t, command, output, "40x24")
	writePTY(t, terminal, "\x1b")
	waitPTYContains(t, command, output, "SUG_EMPTY calls=4")
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
	for _, expected := range []string{"SUG_PTY_RESTORED", "\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), expected) {
			t.Fatalf("missing restoration %q", expected)
		}
	}
}
