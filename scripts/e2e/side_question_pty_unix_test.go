//go:build unix

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestSideQuestionRealCLIPTY(t *testing.T) {
	root, repo := scenarioRepo(t, map[string]string{"README.md": "Side question CLI fixture\n"})
	writeCLIContextSettings(t, root)
	var mainCalls, sideCalls atomic.Int32
	var staged atomic.Bool
	mainStarted, releaseMain := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseMain) }) }
	defer release()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var payload struct {
			Input           json.RawMessage   `json:"input"`
			Tools           []json.RawMessage `json:"tools"`
			MaxOutputTokens int               `json:"max_output_tokens"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(body), "SIDE QUESTION MODE") {
			sideCalls.Add(1)
			if len(payload.Tools) != 0 || payload.MaxOutputTokens != 2048 {
				t.Error("side question must use no tools and bounded output")
			}
			cliContextSSE(w, 2, "SIDE_ANSWER_FIXTURE")
			return
		}
		// The real TUI's existing long-session service extracts memories after
		// two main turns. Keep that distinct from both main and side requests.
		if strings.Contains(string(body), "Extract any durable learnings") {
			cliContextSSE(w, 4, "No durable findings.")
			return
		}
		call := mainCalls.Add(1)
		if call == 1 {
			close(mainStarted)
			select {
			case <-releaseMain:
			case <-r.Context().Done():
				return
			}
			cliContextSSE(w, 1, "MAIN_FINISHED_FIXTURE")
			return
		}
		text := cliContextJSONText(cliContextLatestUser(payload.Input))
		staged.Store(strings.Contains(text, "Side question context") && strings.Contains(text, "SIDE_ANSWER_FIXTURE"))
		cliContextSSE(w, 3, "STAGED_CONTEXT_RECEIVED")
	}))
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e2eBinary(t), "--provider", "openai", "--model", "gpt-4o", "--api-key", "e2e-key", "--base-url", provider.URL, "--tools", "Read", "--sandbox", "danger-full-access")
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "cfg"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "TMPDIR=" + filepath.Join(root, "tmp"), "CLAUDE_TRANSCRIPT_DIR=" + filepath.Join(root, "transcripts"), "NO_PROXY=127.0.0.1,localhost", "TERM=xterm-256color"}
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 28})
	if err != nil {
		t.Fatal(err)
	}
	output := newCLIContextPTYOutput(100, 28)
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
		release()
		if !waited && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = terminal.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("PTY reader did not exit")
		}
		output.close()
	})
	cliContextWait(t, cmd, output, "Ask anything...")
	cliContextWritePTY(t, terminal, "MAIN_TASK_FIXTURE\r")
	select {
	case <-mainStarted:
	case <-ctx.Done():
		t.Fatal("main request did not start")
	}
	cliContextWritePTY(t, terminal, "/btw why is this running?\r")
	cliContextWaitScreenContains(t, cmd, output, "SIDE_ANSWER_FIXTURE")
	if mainCalls.Load() != 1 || sideCalls.Load() != 1 {
		t.Fatal("side question interrupted or resubmitted the main request")
	}
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 12}); err != nil {
		t.Fatal(err)
	}
	output.mu.Lock()
	output.emulator.Resize(40, 12)
	output.width, output.height = 40, 12
	output.mu.Unlock()
	cliContextWaitScreenContains(t, cmd, output, "Esc")
	cliContextWritePTY(t, terminal, "\r") // Explicitly add the answer to the draft.
	cliContextWaitScreenContains(t, cmd, output, "Answer: SIDE_ANSWER_FIXTURE")
	if mainCalls.Load() != 1 {
		t.Fatal("adding answer to draft submitted it automatically")
	}
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 100, Rows: 28}); err != nil {
		t.Fatal(err)
	}
	output.mu.Lock()
	output.emulator.Resize(100, 28)
	output.width, output.height = 100, 28
	output.mu.Unlock()
	release()
	cliContextWait(t, cmd, output, "MAIN_FINISHED_FIXTURE")
	cliContextWaitScreenContains(t, cmd, output, "● default")
	cliContextWritePTY(t, terminal, "\r")
	cliContextWait(t, cmd, output, "STAGED_CONTEXT_RECEIVED")
	cliContextWaitScreenContains(t, cmd, output, "● default")
	cliContextWritePTY(t, terminal, "/quit\r")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatalf("CLI exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CLI exit timed out")
	}
	_ = terminal.Close()
	<-readDone
	if mainCalls.Load() != 2 || sideCalls.Load() != 1 || !staged.Load() {
		t.Fatalf("main=%d side=%d staged=%v", mainCalls.Load(), sideCalls.Load(), staged.Load())
	}
	for _, sequence := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), sequence) {
			t.Fatalf("terminal restoration missing %q", sequence)
		}
	}
}
