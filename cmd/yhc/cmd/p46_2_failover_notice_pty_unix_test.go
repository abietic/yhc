//go:build unix

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine"
)

const p462PlainFallbackPTYHelperEnv = "YHC_P462_PLAIN_FALLBACK_PTY_HELPER"

func TestP462PlainFallbackNoticePTY(t *testing.T) {
	if os.Getenv(p462PlainFallbackPTYHelperEnv) == "1" {
		runP462PlainFallbackPTYHelper(t)
		return
	}

	command := exec.Command(
		os.Args[0],
		"-test.run=^TestP462PlainFallbackNoticePTY$",
	)
	command.Env = append(
		os.Environ(),
		p462PlainFallbackPTYHelperEnv+"=1",
	)
	terminal, err := pty.StartWithSize(
		command,
		&pty.Winsize{Rows: 24, Cols: 80},
	)
	if err != nil {
		t.Fatalf("start P46.2 Plain PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck

	var output bytes.Buffer
	var outputMu sync.Mutex
	readDone := make(chan struct{})
	go func() {
		buffer := make([]byte, 4096)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				outputMu.Lock()
				_, _ = output.Write(buffer[:count])
				outputMu.Unlock()
			}
			if readErr != nil {
				break
			}
		}
		close(readDone)
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		_ = terminal.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Fatal("P46.2 Plain PTY reader did not finish")
		}
		outputMu.Lock()
		got := output.String()
		outputMu.Unlock()
		if waitErr != nil {
			t.Fatalf("P46.2 Plain PTY helper failed: %v\n%s", waitErr, got)
		}
		assertP462PlainFallbackPTYOutput(t, got)
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-waitDone
		_ = terminal.Close()
		<-readDone
		outputMu.Lock()
		got := output.String()
		outputMu.Unlock()
		t.Fatalf("P46.2 Plain PTY helper timed out\n%s", got)
	}
}

func runP462PlainFallbackPTYHelper(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "p46-2-plain-pty",
		TranscriptDir: t.TempDir(),
		CWD:           t.TempDir(),
	})
	t.Cleanup(eng.Close)
	events := make(chan engine.QueryEvent, 3)
	events <- engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID: "alternate", AttemptIndex: 1,
			Profile: "fallback.profile", Phase: engine.ModelAttemptStarted,
			SwitchCount: 1, APIModel: "secret-api-model",
			Provider: "secret-provider", RouteIdentityDigest: "secret-route",
		},
	}
	events <- engine.QueryEvent{
		Type:             engine.EventAssistant,
		AssistantMessage: &schema.Message{Content: "P462_ASSISTANT_OUTPUT"},
	}
	events <- engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalCompleted,
		},
	}
	close(events)
	if err := drivePlainQueryEvents(
		context.Background(),
		eng,
		nil,
		os.Stdout,
		os.Stderr,
		events,
	); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "P462_PTY_EXIT")
}

func assertP462PlainFallbackPTYOutput(t *testing.T, output string) {
	t.Helper()
	const notice = "Model fallback: profile fallback.profile after overload (switch 1)"
	if strings.Count(output, notice) != 1 ||
		strings.Count(output, "P462_ASSISTANT_OUTPUT") != 1 ||
		!strings.Contains(output, "P462_PTY_EXIT") {
		t.Fatalf("P46.2 Plain PTY projection = %q", output)
	}
	for _, secret := range []string{
		"secret-api-model",
		"secret-provider",
		"secret-route",
		"529 overloaded_error",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("P46.2 Plain PTY leaked %q: %q", secret, output)
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r", ""), "\n") {
		if strings.Contains(line, notice) && len(line) > 80 {
			t.Fatalf("P46.2 Plain PTY fallback line exceeded width: %q", line)
		}
	}
}
