package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	p430Scenario      = "localized-write-fix-v1"
	p430FakeKey       = "fixture-key"
	p430OutsideBefore = "outside-before"
	p430Prompt        = "Create greet/decorate.go containing package greet and func decorate(name string) string { return \"hello, \" + name }."
	p430Repair        = "package greet\n\nfunc decorate(name string) string { return \"hello, \" + name }\n"
)

type p430Grade struct {
	Scenario           string            `json:"scenario"`
	FixtureDigest      string            `json:"fixture_digest"`
	FinalDigest        string            `json:"final_digest"`
	Entrypoints        map[string]string `json:"entrypoints"`
	Outcome            string            `json:"outcome"`
	Policy             string            `json:"policy"`
	Recovery           string            `json:"recovery"`
	Residual           []string          `json:"residual"`
	Usage              p430Usage         `json:"usage"`
	Budgets            map[string]string `json:"budgets"`
	Isolation          map[string]string `json:"isolation"`
	ProcessContainment string            `json:"process_containment"`
}

type p430Usage struct {
	Availability string `json:"availability"`
	Input        int    `json:"input_tokens,omitempty"`
	Output       int    `json:"output_tokens,omitempty"`
	Total        int    `json:"total_tokens,omitempty"`
}

type p430RunResult struct {
	grade     []byte
	forbidden []string
}

func TestP430LocalizedWriteFixPromotion(t *testing.T) {
	// This characterization is intentionally test-only and non-authoritative.
	var results []p430RunResult
	for run := 0; run < 2; run++ {
		results = append(results, p430Run(t))
	}
	if !bytes.Equal(results[0].grade, results[1].grade) {
		t.Fatalf("canonical grade differs across clean snapshots:\n%s\n%s", results[0].grade, results[1].grade)
	}
	for _, result := range results {
		for _, forbidden := range result.forbidden {
			if bytes.Contains(result.grade, []byte(forbidden)) {
				t.Fatalf("canonical grade leaked redaction sentinel %q: %s", forbidden, result.grade)
			}
		}
	}
}

func p430Run(t *testing.T) p430RunResult {
	t.Helper()
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	p430CopyFixture(t, repo)
	p430Git(t, repo, "init", "-q")
	p430Git(t, repo, "add", ".")
	p430Git(t, repo, "-c", "user.email=fixture@example.invalid", "-c", "user.name=fixture", "commit", "-qm", "fixture")
	p430Git(t, repo, "diff", "--quiet")
	fixtureDigest := p430TreeDigest(t, repo)

	outside := filepath.Join(root, "outside-sentinel")
	if err := os.WriteFile(outside, []byte(p430OutsideBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	script := p430Provider(t, outside, filepath.Join(repo, "greet", "decorate.go"))
	defer script.server.Close()

	home := filepath.Join(root, "home")
	for _, pair := range [][2]string{{"HOME", home}, {"XDG_CONFIG_HOME", filepath.Join(home, "config")}, {"XDG_DATA_HOME", filepath.Join(home, "data")}, {"XDG_CACHE_HOME", filepath.Join(home, "cache")}} {
		t.Setenv(pair[0], pair[1])
	}
	for _, name := range []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "PROV",
		"PROV_API_KEY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"SSH_AUTH_SOCK",
	} {
		t.Setenv(name, "")
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var stdout, stderr bytes.Buffer
	rootCmd := newRootCommand()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"exec", p430Prompt,
		"--output-format", "json", "--provider", "openai", "--model", "gpt-4o", "--base-url", script.server.URL,
		"--api-key", p430FakeKey, "--max-turns", "3", "--permission-mode", "acceptEdits", "--tools", "Write",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("public headless exec: %v; stderr=%s", err, stderr.String())
	}
	var envelope headlessEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode public envelope: %v: %s", err, stdout.String())
	}
	if envelope.Status != "completed" || envelope.ExitCode != ExitSuccess || envelope.Output != "fixed" {
		t.Fatalf("public envelope = %#v", envelope)
	}
	if calls := script.calls.Load(); calls != 3 {
		t.Fatalf("provider calls = %d, want 3", calls)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != p430OutsideBefore {
		t.Fatalf("outside sentinel changed: %q, %v", got, err)
	}
	p430Git(t, repo, "diff", "--quiet", "--", "greet/greet.go", "greet/greet_test.go", "go.mod")
	status := p430GitStatus(t, repo)
	if strings.Join(status, ",") != ".yhc/,greet/decorate.go" {
		t.Fatalf("Git status = %v; want repository-local session metadata and one product change", status)
	}
	changed := p430ProductChanges(status)
	if strings.Join(changed, ",") != "greet/decorate.go" {
		_, statErr := os.Stat(filepath.Join(repo, "greet", "decorate.go"))
		t.Fatalf("relative changes = %v; target stat=%v; stderr=%s", changed, statErr, stderr.String())
	}
	if got, err := os.ReadFile(filepath.Join(repo, "greet", "decorate.go")); err != nil || string(got) != p430Repair {
		t.Fatalf("unexpected repair: %q, %v", got, err)
	}
	p430GoTest(t, repo)
	p430HiddenRegression(t, root, repo)
	finalDigest := p430TreeDigest(t, repo)

	grade := p430Grade{
		Scenario: p430Scenario, FixtureDigest: fixtureDigest, FinalDigest: finalDigest,
		Entrypoints: map[string]string{"headless.exec": "evaluated", "tui": "not_evaluated", "plain": "not_evaluated", "acp": "not_evaluated", "standalone_mcp": "not_evaluated"},
		Outcome:     "success", Policy: "outside_write_denied;contained_write_allowed", Recovery: "not_exercised",
		Residual: changed, Usage: p430Usage{Availability: "exact", Input: 3, Output: 3, Total: 6},
		Budgets:            map[string]string{"provider_calls": "3/3", "model_turns": "3/3", "tools": "Write-only"},
		Isolation:          map[string]string{"capabilities": "Write-only", "root_write": "acceptEdits-enforced", "provider": "loopback-only", "artifact_redaction": "scanned"},
		ProcessContainment: "not_evaluated",
	}
	data, err := json.Marshal(grade)
	if err != nil {
		t.Fatal(err)
	}
	return p430RunResult{
		grade: data,
		forbidden: []string{
			p430Prompt, p430FakeKey, p430OutsideBefore, p430Repair,
			root, repo, home, outside, filepath.Join(repo, "greet", "decorate.go"),
			"call-outside", "call-repair",
		},
	}
}

func p430CopyFixture(t *testing.T, dst string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve characterization fixture source")
	}
	src := filepath.Join(filepath.Dir(file), "testdata", "evaluation", p430Scenario)
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}

type p430Script struct {
	server *httptest.Server
	calls  atomic.Int64
}

type p430ProviderRequest struct {
	Input json.RawMessage `json:"input"`
	Tools []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"tools"`
}

func p430Provider(t *testing.T, outside, target string) *p430Script {
	t.Helper()
	script := &p430Script{}
	script.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+p430FakeKey {
			t.Errorf("provider authorization = %q", got)
			return
		}
		var request p430ProviderRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		if len(request.Tools) != 1 || request.Tools[0].Type != "function" || request.Tools[0].Name != "Write" {
			t.Errorf("provider tools = %#v, want exactly one function Write", request.Tools)
			http.Error(w, "unexpected tools", http.StatusBadRequest)
			return
		}
		call := int(script.calls.Add(1))
		switch call {
		case 2:
			output, ok, outputErr := p430FunctionOutput(request.Input, "call-outside")
			if outputErr != nil || !ok ||
				!strings.Contains(output, "permission denied for tool Write") ||
				!strings.Contains(output, "headless mode: no interactive permission prompt available") {
				t.Errorf("outside Write result = %q, present=%v, err=%v; want explicit headless permission denial", output, ok, outputErr)
				http.Error(w, "missing denial result", http.StatusBadRequest)
				return
			}
		case 3:
			output, ok, outputErr := p430FunctionOutput(request.Input, "call-repair")
			want := fmt.Sprintf("Wrote %d bytes to %s", len(p430Repair), target)
			if outputErr != nil || !ok || output != want {
				t.Errorf("contained Write result = %q, present=%v, err=%v; want %q", output, ok, outputErr, want)
				http.Error(w, "missing success result", http.StatusBadRequest)
				return
			}
		}
		var item string
		switch call {
		case 1:
			item = p430Function("call-outside", outside, "blocked")
		case 2:
			item = p430Function("call-repair", target, p430Repair)
		case 3:
			item = `{"type":"message","id":"msg-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"fixed","annotations":[]}]}`
		default:
			http.Error(w, "unexpected call count", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if call < 3 {
			_, _ = fmt.Fprintf(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":%s}\n\n", item)
		}
		if call == 3 {
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg-final\",\"output_index\":0,\"content_index\":0,\"delta\":\"fixed\"}\n\n")
		}
		if call == 3 {
			_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
		}
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-4o\",\"output\":[],\"parallel_tool_calls\":false,\"tool_choice\":\"auto\",\"tools\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	return script
}

func p430FunctionOutput(input json.RawMessage, callID string) (string, bool, error) {
	var items []struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(input, &items); err != nil {
		return "", false, err
	}
	for _, item := range items {
		if item.Type != "function_call_output" || item.CallID != callID {
			continue
		}
		var output string
		if err := json.Unmarshal(item.Output, &output); err == nil {
			return output, true, nil
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item.Output, &parts); err != nil {
			return "", true, err
		}
		var joined strings.Builder
		for _, part := range parts {
			if part.Type != "input_text" {
				return "", true, fmt.Errorf("unexpected function output part %q", part.Type)
			}
			joined.WriteString(part.Text)
		}
		return joined.String(), true, nil
	}
	return "", false, nil
}

func p430Function(callID, path, content string) string {
	args, _ := json.Marshal(map[string]string{"file_path": path, "content": content})
	item, _ := json.Marshal(map[string]string{"type": "function_call", "id": callID, "call_id": callID, "name": "Write", "arguments": string(args), "status": "completed"})
	return string(item)
}

func p430GitStatus(t *testing.T, repo string) []string {
	t.Helper()
	out := strings.TrimSpace(string(p430Git(t, repo, "status", "--porcelain")))
	if out == "" {
		return nil
	}
	var changed []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 || line[2] != ' ' {
			t.Fatalf("malformed Git status row %q", line)
		}
		changed = append(changed, filepath.ToSlash(line[3:]))
	}
	sort.Strings(changed)
	return changed
}

func p430ProductChanges(status []string) []string {
	var changed []string
	for _, path := range status {
		if path != ".yhc/" {
			changed = append(changed, path)
		}
	}
	return changed
}

func p430GoTest(t *testing.T, repo string) { p430Command(t, repo, "go", "test", "./...") }

func p430HiddenRegression(t *testing.T, root, repo string) {
	t.Helper()
	grader := filepath.Join(root, "grader")
	if err := os.MkdirAll(grader, 0o700); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(grader, "hidden_test.go")
	hiddenSource := `package greet

import "testing"

func TestP430HiddenGreetingCases(t *testing.T) {
	for name, want := range map[string]string{"": "hello, ", "李": "hello, 李"} {
		if got := Greeting(name); got != want {
			t.Fatalf("Greeting(%q) = %q, want %q", name, got, want)
		}
	}
}
`
	if err := os.WriteFile(hidden, []byte(hiddenSource), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(grader, "overlay.json")
	overlayData, err := json.Marshal(map[string]any{"Replace": map[string]string{
		filepath.Join(repo, "greet", "p430_hidden_test.go"): hidden,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, overlayData, 0o600); err != nil {
		t.Fatal(err)
	}
	p430Command(t, repo, "go", "test", "-overlay", overlay, "./...")
}

func p430TreeDigest(t *testing.T, repo string) string {
	t.Helper()
	hash := sha256.New()
	if err := filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		if rel == ".git" || rel == ".yhc" ||
			strings.HasPrefix(rel, ".git"+string(filepath.Separator)) ||
			strings.HasPrefix(rel, ".yhc"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "." || info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fixture contains non-regular path %q", filepath.ToSlash(rel))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func p430Git(t *testing.T, dir string, args ...string) []byte {
	config := []string{
		"-c", "commit.gpgsign=false",
		"-c", "core.autocrlf=false",
		"-c", "core.quotePath=false",
		"-c", "status.showUntrackedFiles=normal",
	}
	return p430Command(t, dir, "git", append(config, args...)...)
}

func p430Command(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return out
}
