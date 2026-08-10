//go:build unix

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/tools"
)

const p245aPlainGoalPTYHelperEnv = "YHC_P245A_PLAIN_GOAL_PTY_HELPER"

func TestP245aPlainGoalWorkflowPTY(t *testing.T) {
	if os.Getenv(p245aPlainGoalPTYHelperEnv) == "1" {
		runP245aPlainGoalPTYHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestP245aPlainGoalWorkflowPTY$")
	command.Env = append(os.Environ(), p245aPlainGoalPTYHelperEnv+"=1")
	terminal, err := pty.StartWithSize(
		command,
		&pty.Winsize{Cols: 100, Rows: 28},
	)
	if err != nil {
		t.Fatalf("start Plain Goal PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck

	var output bytes.Buffer
	var outputMu sync.Mutex
	changed := make(chan struct{}, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 8192)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				outputMu.Lock()
				_, _ = output.Write(buffer[:count])
				outputMu.Unlock()
				select {
				case changed <- struct{}{}:
				default:
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitP245aPlainPTYContains(t, command, &output, &outputMu, changed, "> ")
	if _, err := terminal.Write([]byte(
		"/goal finish the Plain PTY workflow\r",
	)); err != nil {
		t.Fatalf("write Plain Goal command: %v", err)
	}
	waitP245aPlainPTYContains(
		t,
		command,
		&output,
		&outputMu,
		changed,
		"P245A_GOAL_DONE",
	)
	if _, err := terminal.Write([]byte{0x04}); err != nil {
		t.Fatalf("write Plain Goal EOF: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf(
				"Plain Goal PTY helper failed: %v\n%s",
				waitErr,
				lockedP245aPlainPTYOutput(&output, &outputMu),
			)
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		<-waitDone
		t.Fatalf(
			"Plain Goal PTY helper timed out\n%s",
			lockedP245aPlainPTYOutput(&output, &outputMu),
		)
	}
	_ = terminal.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("Plain Goal PTY reader did not finish")
	}

	raw := lockedP245aPlainPTYOutput(&output, &outputMu)
	if count := strings.Count(raw, "[Goal continuation]"); count != 1 {
		t.Fatalf("Plain Goal PTY continuation count = %d\n%q", count, raw)
	}
	if !strings.Contains(raw, "P245A_PTY_EXIT status=complete items=0") {
		t.Fatalf("Plain Goal PTY exit state missing\n%q", raw)
	}
}

func runP245aPlainGoalPTYHelper(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	model := &headlessRecoveryModel{responses: []*schema.Message{
		{
			Role:    schema.Assistant,
			Content: "first PTY Goal step",
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{TotalTokens: 8},
			},
		},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "plain-pty-goal-complete",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      tools.UpdateGoalToolName,
					Arguments: `{"status":"complete"}`,
				},
			}},
			ResponseMeta: &schema.ResponseMeta{
				FinishReason: "tool_calls",
				Usage:        &schema.TokenUsage{TotalTokens: 4},
			},
		},
		{
			Role:    schema.Assistant,
			Content: "P245A_GOAL_DONE",
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{TotalTokens: 3},
			},
		},
	}}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "p24-5a-plain-goal-pty",
		ThreadID:          "p24-5a-plain-goal-pty-thread",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointPlain,
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled: true,
		},
		ChatModel:    model,
		ToolRegistry: registry,
		MaxTurns:     4,
	})
	defer eng.Close()
	input := newPlainInputBroker(bufio.NewReader(os.Stdin))
	prompt := makePlainPermissionPrompt(input, os.Stdout)
	if err := drivePlainREPL(
		context.Background(),
		eng,
		eng.GetCommandRegistry(),
		input,
		prompt,
		os.Stdout,
		os.Stderr,
	); err != nil {
		t.Fatal(err)
	}
	goal, available := eng.GoalSnapshot()
	if !available {
		t.Fatal("Plain Goal PTY lost Goal state")
	}
	fmt.Fprintf(
		os.Stdout,
		"P245A_PTY_EXIT status=%s items=%d",
		goal.Status,
		len(eng.RuntimeItems()),
	)
}

func waitP245aPlainPTYContains(
	t *testing.T,
	command *exec.Cmd,
	output *bytes.Buffer,
	outputMu *sync.Mutex,
	changed <-chan struct{},
	marker string,
) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		if strings.Contains(
			lockedP245aPlainPTYOutput(output, outputMu),
			marker,
		) {
			return
		}
		select {
		case <-changed:
		case <-timeout.C:
			_ = command.Process.Kill()
			t.Fatalf(
				"Plain Goal PTY did not reach %q\n%q",
				marker,
				lockedP245aPlainPTYOutput(output, outputMu),
			)
		}
	}
}

func lockedP245aPlainPTYOutput(
	output *bytes.Buffer,
	outputMu *sync.Mutex,
) string {
	outputMu.Lock()
	defer outputMu.Unlock()
	return output.String()
}
