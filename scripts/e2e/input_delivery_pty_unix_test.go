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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The provider barrier holds the first task open while the real CLI edits and
// withdraws queued input. A real Read tool then crosses the steering safe point.
func TestInputDeliveryRealCLIWorkflowPTY(t *testing.T) {
	root, repo := scenarioRepo(t, map[string]string{"fixture.txt": "TOOL_FILE_MARKER\n"})
	writeCLIContextSettings(t, root)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var primary, auxiliary atomic.Int32
	validation := make(chan error, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(reason string) {
			t.Errorf("delivery provider: %s", reason)
			validation <- fmt.Errorf("delivery provider: %s", reason)
			http.Error(w, reason, http.StatusBadRequest)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			fail("route")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
		if err != nil || len(body) > manifestLimit {
			fail("request bound")
			return
		}
		var request struct {
			Input json.RawMessage   `json:"input"`
			Tools []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(body, &request) != nil {
			fail("json")
			return
		}
		if inputDeliveryExtractionRequest(request.Input) {
			if len(request.Tools) != 0 || auxiliary.Add(1) > 2 {
				fail("auxiliary contract")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"memory-empty","object":"response","status":"completed","model":"gpt-4o","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`)
			return
		}
		call := primary.Add(1)
		text := cliContextJSONText(request.Input)
		if strings.Contains(text, "QUEUE_OLD") || strings.Contains(text, "WITHDRAW_ME") {
			fail("edited or withdrawn input leaked")
			return
		}
		switch call {
		case 1:
			if !strings.Contains(text, "INITIAL_WORK") {
				fail("initial input")
				return
			}
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			inputDeliveryToolSSE(w)
		case 2:
			if !strings.Contains(text, "STEER_ACTIVE") || !strings.Contains(text, "TOOL_FILE_MARKER") || strings.Contains(text, "QUEUE_NEW") {
				fail("safe-point isolation")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			cliContextSSE(w, call, "ACTIVE_DONE")
		case 3:
			latest, latestErr := inputDeliveryLatestUserText(request.Input)
			if latestErr != nil || latest != "QUEUE_NEW" || strings.Count(text, "QUEUE_NEW") != 1 {
				fail(fmt.Sprintf("next-turn identity: latest=%q error=%v count=%d", latest, latestErr, strings.Count(text, "QUEUE_NEW")))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			cliContextSSE(w, call, "QUEUED_DONE")
		default:
			fail("unexpected primary request")
		}
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); provider.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e2eBinary(t), "--provider", "openai", "--model", "gpt-4o", "--api-key", "e2e-key", "--base-url", provider.URL, "--tools", "Read", "--sandbox", "danger-full-access")
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "cfg"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "TMPDIR=" + filepath.Join(root, "tmp"), "CLAUDE_TRANSCRIPT_DIR=" + filepath.Join(root, "transcripts"), "NO_PROXY=127.0.0.1,localhost", "GOTOOLCHAIN=local", "TERM=xterm-256color"}
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	output := newCLIContextPTYOutput(100, 30)
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
	cliContextWritePTY(t, terminal, "INITIAL_WORK\r")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("initial provider barrier was not reached")
	}

	queue := func(text string) {
		t.Helper()
		cliContextWritePTY(t, terminal, text+"\r")
		cliContextWaitScreenContains(t, cmd, output, "Delivery: next turn")
		cliContextWritePTY(t, terminal, "\r")
		cliContextWaitScreenAbsent(t, cmd, output, "Esc keep draft")
		cliContextWaitScreenContains(t, cmd, output, "[next turn / pending] "+text)
	}
	queue("QUEUE_OLD")
	cliContextWritePTY(t, terminal, "/queue\r")
	cliContextWaitScreenContains(t, cmd, output, "Input deliveries")
	cliContextWritePTY(t, terminal, "e")
	cliContextWaitScreenAbsent(t, cmd, output, "Input deliveries")
	cliContextWaitScreenContains(t, cmd, output, "QUEUE_OLD")
	cliContextWritePTY(t, terminal, strings.Repeat("\x7f", len("QUEUE_OLD")))
	queue("QUEUE_NEW")
	queue("WITHDRAW_ME")
	cliContextWritePTY(t, terminal, "/queue\r")
	cliContextWaitScreenContains(t, cmd, output, "Input deliveries")
	cliContextWritePTY(t, terminal, "d")
	cliContextWaitScreenContains(t, cmd, output, "withdrawn")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenAbsent(t, cmd, output, "Input deliveries")
	cliContextWritePTY(t, terminal, "STEER_ACTIVE\r")
	cliContextWaitScreenContains(t, cmd, output, "Delivery: next turn")
	cliContextWritePTY(t, terminal, "\t")
	cliContextWaitScreenContains(t, cmd, output, "Delivery: steer now")
	cliContextWritePTY(t, terminal, "\r")
	cliContextWaitScreenAbsent(t, cmd, output, "Esc keep draft")
	cliContextWaitScreenContains(t, cmd, output, "[steer now / pending]")
	releaseOnce.Do(func() { close(release) })
	cliContextWait(t, cmd, output, "ACTIVE_DONE")
	cliContextWait(t, cmd, output, "QUEUED_DONE")
	cliContextWritePTY(t, terminal, "/queue\r")
	cliContextWaitScreenContains(t, cmd, output, "Input deliveries")
	cliContextWaitScreenContains(t, cmd, output, "delivered")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenAbsent(t, cmd, output, "Input deliveries")
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
		t.Fatal("CLI quit timed out")
	}
	_ = terminal.Close()
	<-readDone
	select {
	case err := <-validation:
		t.Fatal(err)
	default:
	}
	if primary.Load() != 3 || auxiliary.Load() > 2 {
		t.Fatalf("provider requests primary=%d auxiliary=%d", primary.Load(), auxiliary.Load())
	}
	for _, sequence := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), sequence) {
			t.Fatalf("terminal restoration missing %q", sequence)
		}
	}
}

func inputDeliveryToolSSE(w http.ResponseWriter) {
	event := func(name string, value map[string]any) {
		encoded, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	item := map[string]any{"type": "function_call", "id": "item-read", "call_id": "read", "name": "Read", "arguments": `{"file_path":"fixture.txt"}`, "status": "completed"}
	event("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	event("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	event("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "response-read", "object": "response", "status": "completed", "model": "gpt-4o", "output": []any{}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}}})
}

func inputDeliveryLatestUserText(input json.RawMessage) (string, error) {
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(cliContextLatestUser(input), &message); err != nil {
		return "", err
	}
	var plain string
	if err := json.Unmarshal(message.Content, &plain); err == nil {
		return plain, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		return "", err
	}
	if len(parts) != 1 || parts[0].Type != "input_text" {
		return "", fmt.Errorf("expected one text part")
	}
	return parts[0].Text, nil
}

// Each completed invocation may trigger the existing durable-memory service.
// Its fixed system instruction and user-only conversation strings distinguish
// it from tool-enabled primary requests without assuming a conversation length.
func inputDeliveryExtractionRequest(input json.RawMessage) bool {
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(input, &messages) != nil || len(messages) < 4 || messages[0].Role != "system" {
		return false
	}
	for _, message := range messages[1:] {
		if message.Role != "user" {
			return false
		}
	}
	return strings.Contains(cliContextJSONText(messages[0].Content), "Extract any durable learnings from this coding session that would be useful\nfor future sessions.")
}
