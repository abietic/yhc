//go:build unix

package tui

import (
	"context"
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

type fileRangePTYModel struct {
	calls atomic.Int32
	valid atomic.Bool
}

func (m *fileRangePTYModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("file range fixture requires the streaming production path")
}

func (m *fileRangePTYModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	var input strings.Builder
	for _, message := range messages {
		if message != nil && message.Role == schema.User {
			input.WriteString(message.Content)
		}
	}
	value := input.String()
	m.valid.Store(strings.Contains(value, `"content":"selected two\nselected three\n"`) &&
		strings.Contains(value, `"start_line":2`) && strings.Contains(value, `"end_line":3`) &&
		!strings.Contains(value, "outside-before") && !strings.Contains(value, "outside-after") &&
		!strings.Contains(value, "changed-disk"))
	m.calls.Add(1)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "Selected range received."}}), nil
}

func TestComposerFileRangePTY(t *testing.T) {
	const helperEnv = "YHC_FILE_RANGE_PTY_HELPER"
	if os.Getenv(helperEnv) == "1" {
		root := os.Getenv("YHC_FILE_RANGE_PTY_ROOT")
		resolveHistoryLocations = func() (historyLocations, error) {
			return historyLocations{projectRoot: root, compatibilityConfigDir: filepath.Join(root, "history-config")}, nil
		}
		probe := &fileRangePTYModel{}
		eng := engine.NewQueryEngine(engine.QueryEngineConfig{
			CWD: root, TranscriptDir: t.TempDir(), ChatModel: probe, Model: "fixture-model",
			CommandEntrypoint: commands.EntrypointTUI,
		})
		defer eng.Close()
		app := New(Config{Engine: eng, Resumed: true, KeybindingsConfigDir: t.TempDir()})
		app.fullscreen, app.reducedMotion = true, true
		app.terminalCaps = terminalcap.Capabilities{Platform: "linux", Terminal: "xterm", Interactive: true, BracketedPaste: true}
		app.mentionIndex = composerMentionIndex{loaded: true, files: []string{"main.go"}}
		app.statusLineHook = func(_, _ string) (string, string) {
			state := "EMPTY"
			if len(app.composerElements) == 1 && app.composerElements[0].Data != "" {
				state = "READY"
			} else if app.textarea.Value() == "inspect " {
				state = "REMOVED"
			}
			if probe.calls.Load() > 0 && !app.running {
				state = "SENT"
			}
			return fmt.Sprintf("RG_%s c=%d %dx%d", state, probe.calls.Load(), app.width, app.height), ""
		}
		program := tea.NewProgram(app)
		app.SetProgram(program)
		if _, err := program.Run(); err != nil {
			t.Fatal(err)
		}
		if probe.calls.Load() != 1 || !probe.valid.Load() {
			t.Fatal("file range keyboard path did not submit exactly one captured excerpt")
		}
		fmt.Fprint(os.Stdout, "RG_PTY_RESTORED")
		return
	}
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("outside-before\nselected two\nselected three\noutside-after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestComposerFileRangePTY$")
	command.Env = append(os.Environ(), helperEnv+"=1", "YHC_FILE_RANGE_PTY_ROOT="+root, "TERM=xterm-256color")
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
	waitPTYContains(t, command, output, "RG_EMPTY c=0")
	writePTY(t, terminal, "inspect @ma#2-3\t")
	waitPTYContains(t, command, output, "RG_READY c=0")
	waitPTYContains(t, command, output, "selected three")
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	output.setSize(40, 24)
	waitPTYContains(t, command, output, "40x24")
	writePTY(t, terminal, "\x7f")
	waitPTYContains(t, command, output, "RG_REMOVED c=0")
	writePTY(t, terminal, "\x1a")
	waitPTYContains(t, command, output, "RG_READY c=0")
	if err := os.WriteFile(path, []byte("changed-disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePTY(t, terminal, "\r")
	waitPTYContains(t, command, output, "RG_SENT c=1")
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
	for _, expected := range []string{"RG_PTY_RESTORED", "\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), expected) {
			t.Fatalf("missing restoration %q", expected)
		}
	}
}
