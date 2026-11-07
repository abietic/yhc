package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBashOutputWaitReachesNextModelRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires Unix")
	}
	for _, native := range []bool{false, true} {
		name := "openai"
		if native {
			name = "native-deepseek"
		}
		t.Run(name, func(t *testing.T) {
			// The assertion is only in the script's output, so an echoed command
			// cannot satisfy the next-request oracle.
			root, repo := scenarioRepo(t, map[string]string{"check.sh": "sleep 0.1\nprintf 'BACKGROUND_ASSERTION_FAILED\\n'\nexit 7\n"})
			s := scenario{SchemaVersion: 1, ID: "bash-output-wait", Prompt: "Run the fixture check in the background and wait for its result.", Tools: []string{"Bash", "BashOutput"}, PermissionMode: "bypass", MaxTurns: 4}
			script := providerScript{SchemaVersion: 1, Steps: []providerStep{
				{Kind: "tool_call", CallID: "start-check", Tool: "Bash", Arguments: json.RawMessage(`{"command":"bash check.sh","run_in_background":true}`)},
				{Kind: "tool_call", CallID: "wait-check", Tool: "BashOutput", Arguments: json.RawMessage(`{}`)},
				{Kind: "final", Text: "The background check failed.", ExpectPrior: []priorToolResult{
					{CallID: "wait-check", Contains: "Status: failed"},
					{CallID: "wait-check", Contains: "BACKGROUND_ASSERTION_FAILED"},
					{CallID: "wait-check", Contains: "[exit code: 7]"},
				}},
			}}
			var provider *provider
			provider = newProviderWithHandler(t, script, s.Tools, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
					if err != nil || len(body) > manifestLimit {
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					var request struct {
						Input json.RawMessage `json:"input"`
						Tools []struct {
							Name       string `json:"name"`
							Parameters struct {
								Properties map[string]json.RawMessage `json:"properties"`
							} `json:"parameters"`
						} `json:"tools"`
					}
					if err := json.Unmarshal(body, &request); err != nil {
						t.Errorf("decode request: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					for _, tool := range request.Tools {
						if tool.Name == "BashOutput" && (tool.Parameters.Properties["wait"] == nil || tool.Parameters.Properties["timeout_ms"] == nil) {
							t.Error("model-visible BashOutput omitted wait parameters")
						}
					}
					prior, _ := priorOutputs(request.Input)
					if provider.requestCount() == 1 {
						_, tail, ok := strings.Cut(prior["start-check"], "Shell ID: ")
						if !ok {
							t.Error("background command did not return a handle")
							http.Error(w, "missing handle", http.StatusBadRequest)
							return
						}
						args, _ := json.Marshal(map[string]any{"bash_id": strings.SplitN(tail, "\n", 2)[0], "wait": true, "timeout_ms": 5000})
						provider.mu.Lock()
						provider.script.Steps[1].Arguments = args
						provider.mu.Unlock()
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
			if err != nil || result.Status != "completed" || result.Output != "The background check failed." || provider.requestCount() != 3 {
				t.Fatalf("background wait CLI result: status=%s, calls=%d, err=%v", result.Status, provider.requestCount(), err)
			}
		})
	}
}
