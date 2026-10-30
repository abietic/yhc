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
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestCommandPaletteRealCLIFormPTY(t *testing.T) {
	root, repo := scenarioRepo(t, map[string]string{
		".agents/skills/form-check/SKILL.md": "---\nname: form-check\ndescription: Review form input\nargs:\n  - name: target\n    required: true\n  - name: mode\n    default: safe\n    choices: [safe, fast]\n---\nFORM_ACCEPT {{target}} mode={{mode}}",
	})
	writeCLIContextSettings(t, root)
	var calls atomic.Int32
	var accepted atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var payload struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("provider request JSON: %v", err)
		}
		text := cliContextJSONText(cliContextLatestUser(payload.Input))
		if strings.Contains(text, "FORM_ACCEPT package with space mode=safe") {
			accepted.Store(true)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		cliContextSSE(w, 1, "PALETTE_FORM_ACCEPTED")
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
	resize := func(width, height int) {
		output.mu.Lock()
		output.emulator.Resize(width, height)
		output.width, output.height = width, height
		output.mu.Unlock()
		if err := pty.Setsize(terminal, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
			t.Fatal(err)
		}
	}
	cliContextWait(t, cmd, output, "Ask anything...")
	cliContextWritePTY(t, terminal, "saved draft\x0b")
	cliContextWaitScreenContains(t, cmd, output, "Command Palette")
	cliContextWritePTY(t, terminal, "form-check")
	cliContextWaitScreenContains(t, cmd, output, "Source: skill:project")
	cliContextWritePTY(t, terminal, "\x1bOP")
	cliContextWaitScreenContains(t, cmd, output, "Command details /skill:form-check")
	cliContextWaitScreenContains(t, cmd, output, "SKILL.md")
	cliContextWaitScreenContains(t, cmd, output, "Model: yes, after prompt submit")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenContains(t, cmd, output, "Command Palette")
	cliContextWritePTY(t, terminal, "\r")
	cliContextWaitScreenContains(t, cmd, output, "Parameters /skill:form-check")
	cliContextWritePTY(t, terminal, "\x1b[200~package with space\x1b[201~")
	cliContextWaitScreenContains(t, cmd, output, "package with space")
	resize(40, 12)
	cliContextWaitScreenContains(t, cmd, output, "Effect: none")
	cliContextWaitScreenContains(t, cmd, output, "Model: yes")
	cliContextWaitScreenContains(t, cmd, output, "Esc")
	resize(100, 28)
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenContains(t, cmd, output, "Command Palette")
	cliContextWritePTY(t, terminal, "\x1b")
	cliContextWaitScreenAbsent(t, cmd, output, "Command Palette")
	cliContextWaitScreenContains(t, cmd, output, "saved draft")
	cliContextWritePTY(t, terminal, "\x0b")
	cliContextWaitScreenContains(t, cmd, output, "Command Palette")
	cliContextWritePTY(t, terminal, "form-check\r")
	cliContextWaitScreenContains(t, cmd, output, "Parameters /skill:form-check")
	cliContextWritePTY(t, terminal, "\x1b[200~package with space\x1b[201~\r")
	cliContextWaitScreenAbsent(t, cmd, output, "Parameters /skill:form-check")
	cliContextWaitScreenContains(t, cmd, output, `/skill:form-check "package with space" safe`)
	if calls.Load() != 0 {
		t.Fatal("discovery, form, cancel or staging called model")
	}
	cliContextWritePTY(t, terminal, "\r")
	cliContextWait(t, cmd, output, "PALETTE_FORM_ACCEPTED")
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
	if calls.Load() != 1 || !accepted.Load() {
		t.Fatalf("model calls=%d accepted=%v", calls.Load(), accepted.Load())
	}
	for _, sequence := range []string{"\x1b[?25h", "\x1b[?1049l"} {
		if !strings.Contains(output.raw(), sequence) {
			t.Fatalf("terminal restoration missing %q", sequence)
		}
	}
}
