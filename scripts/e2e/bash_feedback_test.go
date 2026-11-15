package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBashFailureFeedbackReachesNextModelRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires Unix")
	}
	// The expected diagnostic exists only in the fixture's output, not in the
	// echoed command. Seeing the command cannot satisfy the provider oracle.
	const check = "printf 'check started\\n'\nprintf '%12000s' ' '\nprintf '\\nFINAL_ASSERTION_FAILED\\n'\nexit 7\n"
	digest := sha256.Sum256([]byte(check))
	for _, test := range []struct {
		name    string
		command string
		model   string
		extra   []string
		masked  bool
	}{
		{name: "large output", command: "bash check.sh"},
		{name: "command preview and large output", command: "bash check.sh\n# " + strings.Repeat("padding ", 750)},
		{
			name:    "native DeepSeek command preview and large output",
			command: "bash check.sh\n# " + strings.Repeat("padding ", 750),
			model:   "deepseek-flash",
			extra:   []string{"--provider", "agenticdeepseek", "--model", "deepseek-flash"},
		},
		{
			name:    "native DeepSeek failure before successful followup",
			command: "bash check.sh\ntrue",
			model:   "deepseek-flash",
			extra:   []string{"--provider", "agenticdeepseek", "--model", "deepseek-flash"},
			masked:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"command": test.command})
			if err != nil {
				t.Fatal(err)
			}
			s := scenario{
				SchemaVersion: 1, ID: "bash-failure-feedback", Prompt: "Run the fixture check and report its result.",
				Tools: []string{"Bash"}, PermissionMode: "bypass", MaxTurns: 3, TimeoutMillis: 30000,
				Expected: expected{
					Status: "completed", TerminalReason: "completed",
					FileSHA256: map[string]string{"check.sh": hex.EncodeToString(digest[:])}, GitStatus: []string{},
				},
			}
			prior := []priorToolResult{{CallID: "fixture-check", Contains: "FINAL_ASSERTION_FAILED"}}
			if !test.masked {
				prior = append(prior, priorToolResult{CallID: "fixture-check", Contains: "[exit code: 7]"})
			}
			// A compound command reports its final command's status. Its earlier
			// failed assertion must still reach the model as execution evidence.
			p := providerScript{SchemaVersion: 1, Steps: []providerStep{
				{Kind: "tool_call", CallID: "fixture-check", Tool: "Bash", Arguments: args, ExpectModel: test.model},
				{Kind: "final", Text: "The fixture check failed.", ExpectModel: test.model, ExpectPrior: prior},
			}}
			runScenarioData(t, s, p, map[string]string{"check.sh": check}, test.extra...)
		})
	}
}

func TestBashTruncatedOutputCanBeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires Unix")
	}
	// Both streams are merged by the persistent shell. The failed assertion
	// lands in the omitted middle, not in the command or either preview end.
	const check = "printf 'run\\n' >> \"$TMPDIR/check-invocations\"\nprintf 'CHECK_STARTED\\n'\nprintf '%12000s' ' '\nprintf '\\nMIDDLE_ASSERTION_FAILURE\\n'\nprintf '%12000s' ' ' >&2\nprintf '\\nDIAGNOSTIC_TAIL\\n' >&2\nexit 7\n"
	for _, native := range []bool{false, true} {
		name := "openai"
		if native {
			name = "native-deepseek"
		}
		t.Run(name, func(t *testing.T) {
			// This contract needs an ordinary directory, not Git. Keep it runnable
			// in the minimal Linux images used for native-agent verification.
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			for _, name := range []string{"repo", "home", "cfg", "cache", "tmp", "transcripts"} {
				if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte(check), 0o600); err != nil {
				t.Fatal(err)
			}
			s := scenario{
				SchemaVersion: 1, ID: "bash-output-readback", Prompt: "Run the check and inspect its diagnostic.",
				Tools: []string{"Bash", "Read"}, PermissionMode: "bypass", MaxTurns: 4,
			}
			script := providerScript{SchemaVersion: 1, Steps: []providerStep{
				{Kind: "tool_call", CallID: "run-check", Tool: "Bash", Arguments: json.RawMessage(`{"command":"bash check.sh"}`)},
				{
					Kind: "tool_call", CallID: "read-check-output", Tool: "Read", Arguments: json.RawMessage(`{}`),
					ExpectPrior: []priorToolResult{{CallID: "run-check", Contains: "[exit code: 7]"}},
				},
				{
					Kind: "final", Text: "The check failed at its middle assertion.",
					ExpectPrior: []priorToolResult{{CallID: "read-check-output", Contains: "MIDDLE_ASSERTION_FAILURE"}},
				},
			}}
			receipts := make(chan string, 1)
			var provider *provider
			provider = newProviderWithHandler(t, script, s.Tools, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
					if err != nil || len(body) > manifestLimit {
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					if provider.requestCount() == 1 {
						var request struct {
							Input json.RawMessage `json:"input"`
						}
						if json.Unmarshal(body, &request) != nil {
							t.Error("invalid next model request")
							http.Error(w, "invalid request", http.StatusBadRequest)
							return
						}
						prior, _ := priorOutputs(request.Input)
						feedback := prior["run-check"]
						_, tail, found := strings.Cut(feedback, "Full output saved to: ")
						if !found || strings.Contains(feedback, "MIDDLE_ASSERTION_FAILURE") {
							t.Error("truncated Bash feedback has no full-output receipt")
							http.Error(w, "missing output receipt", http.StatusBadRequest)
							return
						}
						path := strings.SplitN(tail, "\n", 2)[0]
						canonicalRoot, rootErr := filepath.EvalSymlinks(root)
						canonicalPath, pathErr := filepath.EvalSymlinks(path)
						relative, relErr := filepath.Rel(canonicalRoot, canonicalPath)
						if rootErr != nil || pathErr != nil || relErr != nil || relative == ".." ||
							strings.HasPrefix(relative, ".."+string(os.PathSeparator)) ||
							filepath.Base(filepath.Dir(path)) != "tool_results" {
							t.Error("output receipt is not session-owned")
							http.Error(w, "invalid output receipt", http.StatusBadRequest)
							return
						}
						data, err := os.ReadFile(path)
						info, statErr := os.Stat(path)
						if err != nil || statErr != nil || info.Mode().Perm() != 0o600 ||
							!bytes.Contains(data, []byte("MIDDLE_ASSERTION_FAILURE")) ||
							!bytes.Contains(data, []byte("DIAGNOSTIC_TAIL")) {
							t.Error("full output was not privately retained")
							http.Error(w, "unreadable output", http.StatusBadRequest)
							return
						}
						args, _ := json.Marshal(map[string]string{"file_path": path})
						provider.mu.Lock()
						provider.script.Steps[1].Arguments = args
						provider.mu.Unlock()
						receipts <- path
					}
					next.ServeHTTP(w, r)
				})
			})
			defer provider.server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var extra []string
			if native {
				extra = []string{"--provider", "agenticdeepseek", "--model", "deepseek-flash"}
			}
			result, err := runInvocation(ctx, t, e2eBinary(t), root, repo, s, provider.server.URL, extra...)
			if err != nil || result.Status != "completed" || provider.requestCount() != 3 {
				t.Fatalf("output readback status=%s calls=%d err=%v", result.Status, provider.requestCount(), err)
			}
			path := <-receipts
			if filepath.Base(filepath.Dir(filepath.Dir(path))) != result.SessionID {
				t.Fatal("output is not bound to the actual CLI session")
			}
			count, err := os.ReadFile(filepath.Join(root, "tmp", "check-invocations"))
			if err != nil || string(count) != "run\n" {
				t.Fatal("output readback reran the command")
			}
		})
	}
}
