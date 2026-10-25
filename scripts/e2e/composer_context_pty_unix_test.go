//go:build unix

package e2e

import (
	"bytes"
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

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// TestComposerContextRealCLIWorkflowPTY exercises the public binary rather
// than a TUI fixture. It proves that loaded file ranges and a mouse-selected
// output excerpt are immutable attachments, and that the context manager can
// remove one attachment before the next real provider request.
func TestComposerContextRealCLIWorkflowPTY(t *testing.T) {
	var base, changed strings.Builder
	for i := 1; i <= 35; i++ {
		fmt.Fprintf(&base, "base %02d\n", i)
		switch i {
		case 2:
			changed.WriteString("KEEP_DIFF_HUNK\n")
		case 30:
			changed.WriteString("DROP_DIFF_HUNK\n")
		default:
			fmt.Fprintf(&changed, "base %02d\n", i)
		}
	}
	root, repo := scenarioRepo(t, map[string]string{
		"keep.txt":   "outside-before\nKEEP_LINE_TWO\nKEEP_LINE_THREE\noutside-after\n",
		"remove.txt": "REMOVE_FILE_BODY\n",
		"diff.txt":   base.String(),
	})
	if err := os.WriteFile(filepath.Join(repo, "diff.txt"), []byte(changed.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCLIContextSettings(t, root)
	stubCLIContextClipboard(t, root)

	provider := newCLIContextProvider(t)
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e2eBinary(t),
		"--provider", "openai", "--model", "gpt-4o", "--api-key", "e2e-key", "--base-url", provider.URL,
		"--tools", "Read", "--sandbox", "danger-full-access")
	cmd.Dir = repo
	cmd.Env = []string{
		"PATH=" + filepath.Join(root, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "cfg"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"CLAUDE_TRANSCRIPT_DIR=" + filepath.Join(root, "transcripts"),
		"NO_PROXY=127.0.0.1,localhost",
		"GOTOOLCHAIN=local",
		"TERM=xterm-256color",
	}
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
	cliContextWritePTY(t, terminal, "first\r")
	cliContextWait(t, cmd, output, "KEEP_OUTPUT")

	// Tab commits each exact file-range mention into a captured composer element.
	cliContextWritePTY(t, terminal, "@keep.txt#2-3\t @remove.txt#1-1\t")
	cliContextWait(t, cmd, output, "Full selected range attached")
	cliContextDragOutput(t, terminal, output, "KEEP_OUTPUT")
	cliContextWritePTY(t, terminal, "\x1ba") // Alt+A: attach selected output.
	cliContextWait(t, cmd, output, "Full selected text attached")
	cliContextWaitScreenContains(t, cmd, output, "KEEP_OUTPUT")

	// Draft order is keep, remove, selected output. Remove the middle entry
	// through the real manager keyboard path, not by editing the composer text.
	cliContextWritePTY(t, terminal, "\x1bc")
	cliContextWait(t, cmd, output, "Attached context (3)")
	cliContextWritePTY(t, terminal, "\x1b[H\x1b[B\x1b[3~") // Home, Down, Delete.
	cliContextWait(t, cmd, output, "Attached context (2)")
	cliContextWritePTY(t, terminal, "d")
	cliContextWaitScreenContains(t, cmd, output, "diff.txt")
	cliContextWritePTY(t, terminal, "\t")
	cliContextWaitScreenContains(t, cmd, output, "KEEP_DIFF_HUNK")
	cliContextWritePTY(t, terminal, "\r")
	cliContextWait(t, cmd, output, "Captured Git diff attached to draft")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenAbsent(t, cmd, output, "Search:")
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("DISK_CHANGED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "diff.txt"), []byte("DISK_CHANGED_DIFF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cliContextWritePTY(t, terminal, "\r")
	cliContextWait(t, cmd, output, "CLI_CONTEXT_ACCEPTED")
	cliContextWritePTY(t, terminal, "/quit\r")

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatalf("real CLI PTY exited: %v (terminal bytes=%d)", err, output.size())
		}
	case <-time.After(12 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		waited = true
		t.Fatal("real CLI PTY quit timed out")
	}
	_ = terminal.Close()
	<-readDone
	if err := provider.validation(); err != nil {
		t.Fatal(err)
	}
	if got := provider.calls.Load(); got != 2 || provider.auxiliary.Load() > 1 {
		t.Fatalf("provider primary=%d auxiliary=%d, want 2 primary and at most 1 extraction", got, provider.auxiliary.Load())
	}
	for _, sequence := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), sequence) {
			t.Fatalf("missing terminal restoration sequence %q", sequence)
		}
	}
}

type cliContextProvider struct {
	*httptest.Server
	calls     atomic.Int32
	auxiliary atomic.Int32
	mu        sync.Mutex
	err       error
	kinds     []bool
}

func newCLIContextProvider(t *testing.T) *cliContextProvider {
	t.Helper()
	p := &cliContextProvider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.Error(w, "contract", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
		if err != nil || len(body) > manifestLimit {
			http.Error(w, "bound", http.StatusBadRequest)
			return
		}
		var request struct {
			Model string            `json:"model"`
			Input json.RawMessage   `json:"input"`
			Tools []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(body, &request) != nil || request.Model != "gpt-4o" {
			http.Error(w, "contract", http.StatusBadRequest)
			return
		}
		// The real TUI also runs a lifecycle-owned durable-memory extraction
		// after two turns. Keep it separate from the two user-request oracles.
		if cliContextExtractionRequest(request.Input) {
			if len(request.Tools) != 0 || p.auxiliary.Add(1) > 1 {
				p.mu.Lock()
				p.err = fmt.Errorf("unexpected extraction tools or repetition")
				p.mu.Unlock()
				http.Error(w, "auxiliary_contract", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"memory-empty","object":"response","status":"completed","model":"gpt-4o","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`)
			return
		}
		call := p.calls.Add(1)
		p.mu.Lock()
		p.kinds = append(p.kinds, bytes.Contains(body, []byte("composer_context")))
		p.mu.Unlock()
		if call == 2 {
			p.recordValidation(cliContextLatestUser(request.Input))
		}
		if call > 2 {
			http.Error(w, "unexpected_request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		text := "KEEP_OUTPUT\nDROP_OUTPUT"
		if call == 2 {
			text = "CLI_CONTEXT_ACCEPTED"
		}
		cliContextSSE(w, call, text)
	}))
	return p
}

func (p *cliContextProvider) recordValidation(user json.RawMessage) {
	value := cliContextJSONText(user)
	checks := []struct {
		present bool
		needle  string
	}{
		{true, "KEEP_LINE_TWO"},
		{true, "KEEP_LINE_THREE"},
		{true, "KEEP_OUTPUT"},
		{true, `"kind":"file"`},
		{true, `"start_line":2`},
		{true, `"end_line":3`},
		{true, `"kind":"terminal_output"`},
		{true, `"kind":"git_diff"`},
		{true, "KEEP_DIFF_HUNK"},
		{false, "REMOVE_FILE_BODY"},
		{false, "DROP_OUTPUT"},
		{false, "DISK_CHANGED"},
		{false, "DROP_DIFF_HUNK"},
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for index, check := range checks {
		if strings.Contains(value, check.needle) != check.present {
			p.err = fmt.Errorf("latest user composer context failed required attachment contract (check=%d has_context=%t output=%t)", index, strings.Contains(value, "composer_context"), strings.Contains(value, "KEEP_OUTPUT"))
			return
		}
	}
	contexts, err := cliContextEnvelopes(value)
	if err != nil || len(contexts) != 3 {
		p.err = fmt.Errorf("latest user needs exactly 3 valid composer envelopes: count=%d err=%v", len(contexts), err)
		return
	}
	if contexts[0].Kind != "file" || contexts[0].Content != "KEEP_LINE_TWO\nKEEP_LINE_THREE\n" || contexts[0].StartLine != 2 || contexts[0].EndLine != 3 ||
		contexts[1].Kind != "terminal_output" || contexts[1].Content != "KEEP_OUTPUT" ||
		contexts[2].Kind != "git_diff" || contexts[2].MIMEType != "text/x-diff" || contexts[2].Source != "git:all:diff.txt#hunk=1" || strings.Count(contexts[2].Content, "\n@@ ") != 1 ||
		!strings.Contains(contexts[2].Content, "--- a/diff.txt\n+++ b/diff.txt\n") {
		p.err = fmt.Errorf("latest user composer envelope fields or ordering changed")
	}
}

type cliContextEnvelope struct {
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	MIMEType  string `json:"mime_type"`
	Content   string `json:"content"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func cliContextEnvelopes(value string) ([]cliContextEnvelope, error) {
	const open, close = "<composer_context>", "</composer_context>"
	var contexts []cliContextEnvelope
	for {
		start := strings.Index(value, open)
		if start < 0 {
			return contexts, nil
		}
		value = value[start+len(open):]
		end := strings.Index(value, close)
		if end < 0 {
			return nil, fmt.Errorf("unclosed composer envelope")
		}
		var item cliContextEnvelope
		if err := json.Unmarshal([]byte(value[:end]), &item); err != nil {
			return nil, err
		}
		contexts = append(contexts, item)
		value = value[end+len(close):]
	}
}

func cliContextExtractionRequest(input json.RawMessage) bool {
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(input, &messages) != nil || len(messages) != 5 || messages[0].Role != "system" {
		return false
	}
	for _, message := range messages[1:] {
		if message.Role != "user" {
			return false
		}
	}
	return strings.Contains(cliContextJSONText(messages[0].Content), "Extract any durable learnings from this coding session that would be useful\nfor future sessions.")
}

func (p *cliContextProvider) validation() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *cliContextProvider) requestKinds() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bool(nil), p.kinds...)
}

func cliContextLatestUser(input json.RawMessage) json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return nil
	}
	for i := len(items) - 1; i >= 0; i-- {
		var item struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(items[i], &item) == nil && item.Role == "user" {
			return items[i]
		}
	}
	return nil
}

func cliContextJSONText(value json.RawMessage) string {
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return ""
	}
	var stringsFound []string
	var walk func(any)
	walk = func(node any) {
		switch node := node.(type) {
		case string:
			stringsFound = append(stringsFound, node)
		case []any:
			for _, child := range node {
				walk(child)
			}
		case map[string]any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(decoded)
	return strings.Join(stringsFound, "\n")
}

func cliContextSSE(w http.ResponseWriter, call int32, text string) {
	event := func(name string, value map[string]any) {
		encoded, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}
	event("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": fmt.Sprintf("message-%d", call), "output_index": 0, "content_index": 0, "delta": text})
	item := map[string]any{"type": "message", "id": fmt.Sprintf("message-%d", call), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	event("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	event("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("response-%d", call), "object": "response", "status": "completed", "model": "gpt-4o", "output": []any{}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}}})
}

func writeCLIContextSettings(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "home", ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"prompt_suggestions":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stubCLIContextClipboard(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pbcopy", "xclip", "xsel", "wl-copy"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\ncat >/dev/null\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

type cliContextPTYOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	emulator *vt.Emulator
	width    int
	height   int
}

func newCLIContextPTYOutput(width, height int) *cliContextPTYOutput {
	emulator := vt.NewEmulator(width, height)
	go func() { _, _ = io.Copy(io.Discard, emulator) }()
	return &cliContextPTYOutput{emulator: emulator, width: width, height: height}
}

func (o *cliContextPTYOutput) append(value []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.buffer.Write(value)
	_, _ = o.emulator.Write(value)
}

func (o *cliContextPTYOutput) raw() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

func (o *cliContextPTYOutput) size() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Len()
}

func (o *cliContextPTYOutput) screen() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.emulator.String()
}

func (o *cliContextPTYOutput) rows() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	rows := make([]string, o.height)
	for y := 0; y < o.height; y++ {
		var line strings.Builder
		line.Grow(o.width)
		for x := 0; x < o.width; x++ {
			cell := o.emulator.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				line.WriteByte(' ')
				continue
			}
			line.WriteString(cell.String())
		}
		rows[y] = line.String()
	}
	return rows
}

func (o *cliContextPTYOutput) close() { _ = o.emulator.Close() }

func cliContextWritePTY(t *testing.T, terminal *os.File, input string) {
	t.Helper()
	if _, err := terminal.WriteString(input); err != nil {
		t.Fatal(err)
	}
}

func cliContextWait(t *testing.T, command *exec.Cmd, output *cliContextPTYOutput, needle string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.raw(), needle) || strings.Contains(output.screen(), needle) {
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf("real CLI exited before %q (terminal bytes=%d)", needle, output.size())
		}
		time.Sleep(10 * time.Millisecond)
	}
	screen := output.screen()
	if len(screen) > 4096 {
		screen = screen[:4096]
	}
	t.Fatalf("timeout waiting for %q (terminal bytes=%d, screen=%q)", needle, output.size(), screen)
}

func cliContextWaitScreenAbsent(t *testing.T, command *exec.Cmd, output *cliContextPTYOutput, needle string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(output.screen(), needle) {
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf("real CLI exited before closing %q (terminal bytes=%d)", needle, output.size())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout closing %q (terminal bytes=%d)", needle, output.size())
}

func cliContextWaitScreenContains(t *testing.T, command *exec.Cmd, output *cliContextPTYOutput, needle string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(output.rows(), "\n"), needle) {
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf("real CLI exited before displaying %q (terminal bytes=%d)", needle, output.size())
		}
		time.Sleep(10 * time.Millisecond)
	}
	screen := output.screen()
	if len(screen) > 4096 {
		screen = screen[:4096]
	}
	t.Fatalf("timeout displaying %q (terminal bytes=%d, screen=%q)", needle, output.size(), screen)
}

func cliContextDragOutput(t *testing.T, terminal *os.File, output *cliContextPTYOutput, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for row, line := range output.rows() {
			if index := strings.Index(line, text); index >= 0 {
				start := ansi.StringWidth(line[:index]) + 1
				end := start + ansi.StringWidth(text)
				cliContextWritePTY(t, terminal, fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<32;%d;%dM\x1b[<0;%d;%dm", start, row+1, end, row+1, end, row+1))
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output selection text did not become visible (terminal bytes=%d)", output.size())
}
