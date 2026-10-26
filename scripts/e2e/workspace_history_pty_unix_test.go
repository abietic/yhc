//go:build unix

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestWorkspaceHistoryRealCLIPTY proves the public terminal workflow keeps a
// captured Write snapshot distinct from both disk state and model dispatch.
func TestWorkspaceHistoryRealCLIPTY(t *testing.T) {
	root, repo := scenarioRepo(t, map[string]string{"README.md": "history fixture\n"})
	writeCLIContextSettings(t, root)
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonicalRepo, "captured.txt")
	const content = "CAPTURED_HISTORY_BYTES\n"
	var primary, auxiliary atomic.Int32
	validation := make(chan error, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(format string, args ...any) {
			err := fmt.Errorf(format, args...)
			select {
			case validation <- err:
			default:
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			fail("unexpected provider route %s %s", r.Method, r.URL.Path)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
		if err != nil || len(body) > manifestLimit {
			fail("invalid bounded provider request: %v", err)
			return
		}
		var request struct {
			Input json.RawMessage   `json:"input"`
			Tools []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(body, &request) != nil {
			fail("invalid provider JSON")
			return
		}
		// The engine may extract durable memory after two user turns; it is an
		// auxiliary lifecycle request, not a history-view or user-turn request.
		if cliContextExtractionRequest(request.Input) || (len(request.Tools) == 0 && strings.Contains(string(body), "Extract any durable learnings from this coding session")) {
			if len(request.Tools) != 0 {
				fail("unexpected auxiliary request")
				return
			}
			auxiliary.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			cliContextSSE(w, 4, "No durable findings.")
			return
		}
		call := primary.Add(1)
		latest := cliContextJSONText(cliContextLatestUser(request.Input))
		w.Header().Set("Content-Type", "text/event-stream")
		switch call {
		case 1:
			if !strings.Contains(latest, "WRITE_HISTORY_FIXTURE") {
				fail("first request did not contain fixture prompt")
				return
			}
			historyWriteToolSSE(w, path, content)
		case 2:
			if strings.Contains(latest, "Captured change context") {
				fail("captured context appeared before explicit submission")
				return
			}
			cliContextSSE(w, call, "WRITE_HISTORY_COMPLETE")
		case 3:
			if !strings.Contains(latest, "Captured change context") || !strings.Contains(latest, "CAPTURED_HISTORY_BYTES") || !strings.Contains(latest, "captured.txt") {
				fail("explicit submission lacks captured change context")
				return
			}
			cliContextSSE(w, call, "HISTORY_CONTEXT_ACCEPTED")
		default:
			fail("unexpected primary request %d", call)
		}
	}))
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e2eBinary(t),
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "e2e-key", "--base-url", provider.URL,
		"--tools", "Write", "--permission-mode", "acceptEdits")
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "cfg"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "TMPDIR=" + filepath.Join(root, "tmp"), "CLAUDE_TRANSCRIPT_DIR=" + filepath.Join(root, "transcripts"), "NO_PROXY=127.0.0.1,localhost", "GOTOOLCHAIN=local", "TERM=xterm-256color"}
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
			n, readErr := terminal.Read(buffer)
			if n > 0 {
				output.append(buffer[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()
	waited := false
	t.Cleanup(func() {
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
	cliContextWritePTY(t, terminal, "WRITE_HISTORY_FIXTURE\r")
	cliContextWait(t, cmd, output, "WRITE_HISTORY_COMPLETE")
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != content {
		t.Fatalf("real Write result = %q, %v", got, readErr)
	}
	if primary.Load() != 2 {
		t.Fatalf("first turn provider requests = %d, want 2", primary.Load())
	}

	// /recap is a local fact view; it must not make a provider request.
	cliContextWritePTY(t, terminal, "/recap\r")
	cliContextWaitScreenContains(t, cmd, output, "Verification receipts")
	cliContextWaitScreenContains(t, cmd, output, "Captured file history")
	if primary.Load() != 2 {
		t.Fatalf("/recap dispatched a model request: %d", primary.Load())
	}
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenAbsent(t, cmd, output, "Verification receipts")

	cliContextWritePTY(t, terminal, "/changes\r")
	cliContextWaitScreenContains(t, cmd, output, "Captured changes")
	cliContextWritePTY(t, terminal, "\x1b[B\r") // turn row -> captured Write record -> preview
	cliContextWaitScreenContains(t, cmd, output, "captured.txt")
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 40, Rows: 12}); err != nil {
		t.Fatal(err)
	}
	output.mu.Lock()
	output.emulator.Resize(40, 12)
	output.width, output.height = 40, 12
	output.mu.Unlock()
	cliContextWaitScreenContains(t, cmd, output, "Tab hunk  Space mark  a add  Esc list")
	cliContextWritePTY(t, terminal, "\t ")
	cliContextWaitScreenContains(t, cmd, output, "[x] hunk")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenContains(t, cmd, output, "Enter open")
	// Reopen after Esc, then add the literal captured hunk. Adding only mutates
	// the draft, so the primary request count must remain at the completed turn.
	cliContextWritePTY(t, terminal, "\x1b[B\r\t a")
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 100, Rows: 28}); err != nil {
		t.Fatal(err)
	}
	output.mu.Lock()
	output.emulator.Resize(100, 28)
	output.width, output.height = 100, 28
	output.mu.Unlock()
	cliContextWaitScreenContains(t, cmd, output, "[Pasted Content")
	if primary.Load() != 2 {
		t.Fatalf("adding a captured hunk submitted a request: %d", primary.Load())
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != content {
		t.Fatalf("captured hunk mutated real file: %q, %v", got, readErr)
	}

	cliContextWritePTY(t, terminal, "\r")
	cliContextWait(t, cmd, output, "HISTORY_CONTEXT_ACCEPTED")
	cliContextWritePTY(t, terminal, "/quit\r")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatalf("CLI exit: %v", err)
		}
	case <-time.After(12 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		waited = true
		t.Fatal("CLI exit timed out")
	}
	_ = terminal.Close()
	<-readDone
	select {
	case err := <-validation:
		t.Fatal(err)
	default:
	}
	if primary.Load() != 3 {
		t.Fatalf("provider primary=%d auxiliary=%d", primary.Load(), auxiliary.Load())
	}
	for _, sequence := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), sequence) {
			t.Fatalf("terminal restoration missing %q", sequence)
		}
	}
}

func historyWriteToolSSE(w http.ResponseWriter, path, content string) {
	event := func(name string, value map[string]any) {
		encoded, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}
	arguments, _ := json.Marshal(map[string]string{"file_path": path, "content": content})
	item := map[string]any{"type": "function_call", "id": "history-write-item", "call_id": "history-write", "name": "Write", "arguments": string(arguments), "status": "completed"}
	event("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	event("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	event("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "history-write-response", "object": "response", "status": "completed", "model": "gpt-4o", "output": []any{}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}}})
}
