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
	testWorkspaceHistoryRealCLIPTY(t, "openai", "gpt-4o")
}

func testWorkspaceHistoryRealCLIPTY(t *testing.T, providerName, model string) {
	t.Helper()
	deepSeek := providerName == "agenticdeepseek"
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
			historyTextSSE(w, deepSeek, "auxiliary", "No durable findings.")
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
			historyReadToolSSE(w, deepSeek, filepath.Join(canonicalRepo, "README.md"))
		case 2:
			historyWriteToolSSE(w, deepSeek, path, content)
		case 3:
			if strings.Contains(latest, "Captured change context") {
				fail("captured context appeared before explicit submission")
				return
			}
			historyTextSSE(w, deepSeek, fmt.Sprintf("%d", call), "WRITE_HISTORY_COMPLETE")
		case 4:
			if !strings.Contains(latest, "Captured change context") || !strings.Contains(latest, "CAPTURED_HISTORY_BYTES") || !strings.Contains(latest, "captured.txt") {
				fail("explicit submission lacks captured change context")
				return
			}
			historyTextSSE(w, deepSeek, fmt.Sprintf("%d", call), "HISTORY_CONTEXT_ACCEPTED")
		default:
			fail("unexpected primary request %d", call)
		}
	}))
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// This workflow exercises explicitly authorized local file recovery. Pin
	// its execution policy rather than depending on the host sandbox probe.
	cmd := exec.CommandContext(ctx, e2eBinary(t),
		"--provider", providerName, "--model", model, "--api-key", "e2e-key", "--base-url", provider.URL,
		"--tools", "Read,Write,Edit", "--permission-mode", "acceptEdits", "--sandbox", "danger-full-access")
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
	cliContextWaitScreenContains(t, cmd, output, "Write")
	cliContextWaitScreenAbsent(t, cmd, output, "Write running")
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != content {
		t.Fatalf("real Write result = %q, %v", got, readErr)
	}
	if primary.Load() != 3 {
		t.Fatalf("first turn provider requests = %d, want 3", primary.Load())
	}

	// /recap is a local fact view; it must not make a provider request.
	cliContextWritePTY(t, terminal, "/recap\r")
	cliContextWaitScreenContains(t, cmd, output, "Verification receipts")
	cliContextWaitScreenContains(t, cmd, output, "Captured file history")
	cliContextWaitScreenContains(t, cmd, output, "Write")
	cliContextWaitScreenAbsent(t, cmd, output, "Write running")
	if primary.Load() != 3 {
		t.Fatalf("/recap dispatched a model request: %d", primary.Load())
	}
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenAbsent(t, cmd, output, "Verification receipts")
	cliContextWaitScreenContains(t, cmd, output, "Write")
	cliContextWaitScreenAbsent(t, cmd, output, "Write running")

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
	cliContextWaitScreenContains(t, cmd, output, "Tab hunk a add r restore Esc list")
	cliContextWritePTY(t, terminal, "\t ")
	cliContextWaitScreenContains(t, cmd, output, "[x] hunk")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenContains(t, cmd, output, "Enter view")
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
	if primary.Load() != 3 {
		t.Fatalf("adding a captured hunk submitted a request: %d", primary.Load())
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != content {
		t.Fatalf("captured hunk mutated real file: %q, %v", got, readErr)
	}

	cliContextWritePTY(t, terminal, "\r")
	cliContextWait(t, cmd, output, "HISTORY_CONTEXT_ACCEPTED")
	// Recovery is a separate local action: preview, explicit confirmation,
	// then a fresh allow-once permission. Neither UI step dispatches the model.
	cliContextWritePTY(t, terminal, "/changes\r")
	cliContextWaitScreenContains(t, cmd, output, "Captured changes")
	cliContextWritePTY(t, terminal, "\x1b[Fr") // last captured file -> restore preview
	cliContextWaitScreenContains(t, cmd, output, "Enter confirm")
	if got, err := os.ReadFile(path); err != nil || string(got) != content {
		t.Fatal("restore preview wrote the workspace")
	}
	cliContextWritePTY(t, terminal, "\r")
	cliContextWaitScreenContains(t, cmd, output, "WorkspaceRestore")
	cliContextWaitScreenContains(t, cmd, output, "Tool use")
	if primary.Load() != 4 {
		t.Fatal("restore confirmation dispatched a model")
	}
	cliContextWritePTY(t, terminal, "n")
	cliContextWaitScreenContains(t, cmd, output, "not authorized")
	if got, err := os.ReadFile(path); err != nil || string(got) != content {
		t.Fatal("denied restore changed file")
	}
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenContains(t, cmd, output, "Captured changes")
	cliContextWritePTY(t, terminal, "\x1b[Fr")
	cliContextWaitScreenContains(t, cmd, output, "Enter confirm")
	cliContextWritePTY(t, terminal, "\r")
	cliContextWaitScreenContains(t, cmd, output, "Tool use")
	cliContextWritePTY(t, terminal, "y")
	cliContextWaitScreenContains(t, cmd, output, "completed 1/1")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("created file was not removed by confirmed restore: %v", err)
	}
	if primary.Load() != 4 {
		t.Fatal("applying restore called the provider")
	}
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenContains(t, cmd, output, "Captured changes")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenAbsent(t, cmd, output, "Captured changes")
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
	if primary.Load() != 4 {
		t.Fatalf("provider primary=%d auxiliary=%d", primary.Load(), auxiliary.Load())
	}
	for _, sequence := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), sequence) {
			t.Fatalf("terminal restoration missing %q", sequence)
		}
	}
}

func TestWorkspaceHistoryAgenticDeepSeekRealCLIPTY(t *testing.T) {
	testWorkspaceHistoryRealCLIPTY(t, "agenticdeepseek", "deepseek-flash")
}

func historyWriteToolSSE(w http.ResponseWriter, deepSeek bool, path, content string) {
	sequence := int64(0)
	historyToolSSE(w, deepSeek, &sequence, "history-write", "history-write-item", "Write", map[string]string{"file_path": path, "content": content})
}

func historyReadToolSSE(w http.ResponseWriter, deepSeek bool, path string) {
	sequence := int64(0)
	historyToolSSE(w, deepSeek, &sequence, "history-read", "history-read-item", "Read", map[string]string{"file_path": path})
}

func historyToolSSE(w http.ResponseWriter, deepSeek bool, sequence *int64, callID, itemID, name string, arguments map[string]string) {
	argumentsJSON, _ := json.Marshal(arguments)
	item := map[string]any{"type": "function_call", "id": itemID, "call_id": callID, "name": name, "arguments": string(argumentsJSON), "status": "completed"}
	historyEvent(w, deepSeek, sequence, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	historyEvent(w, deepSeek, sequence, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	output := []any{}
	if deepSeek {
		output = append(output, item)
	}
	historyEvent(w, deepSeek, sequence, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": callID + "-response", "object": "response", "status": "completed", "model": historyModel(deepSeek), "output": output, "usage": historyUsage()}})
}

func historyTextSSE(w http.ResponseWriter, deepSeek bool, id, text string) {
	sequence := int64(0)
	item := map[string]any{"type": "message", "id": "message-" + id, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	historyEvent(w, deepSeek, &sequence, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "message-" + id, "output_index": 0, "content_index": 0, "delta": text})
	historyEvent(w, deepSeek, &sequence, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	output := []any{}
	if deepSeek {
		output = append(output, item)
	}
	historyEvent(w, deepSeek, &sequence, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "response-" + id, "object": "response", "status": "completed", "model": historyModel(deepSeek), "output": output, "usage": historyUsage()}})
}

func historyEvent(w http.ResponseWriter, deepSeek bool, sequence *int64, name string, value map[string]any) {
	if deepSeek {
		value["sequence_number"] = *sequence
		*sequence++
	}
	encoded, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
}

func historyModel(deepSeek bool) string {
	if deepSeek {
		return "deepseek-flash"
	}
	return "gpt-4o"
}

func historyUsage() map[string]any {
	return map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}
}
