package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Scripted responses prove the native delivery boundary, not whether a live
// model writes a complete handoff or chooses a sound verification verdict.
func TestVerificationHandoffReachesNativeChild(t *testing.T) {
	const baseline = "BASELINE_FIXTURE_VALUE=1\n"
	const current = "CURRENT_FIXTURE_VALUE=2\n"
	const rootOnly = "ROOT_ONLY_NOTE_NOT_DELEGATED"
	required := []string{
		"TASK_PRESERVE_FIXTURE_ORDER",
		"CONSTRAINT_NO_EXTERNAL_NETWORK",
		"BASELINE_REFERENCE_WITH_DIGEST",
		"PRIOR_CHECK_EVIDENCE_UNCHANGED",
		"OPEN_QUESTION_REQUIRES_INDEPENDENT_CHECK",
	}
	for _, test := range []struct {
		name            string
		baselinePath    string
		baselineMissing bool
		verdict         string
	}{
		{name: "available baseline", baselinePath: "baseline/original.txt", verdict: "PASS"},
		{name: "missing baseline remains partial", baselinePath: "baseline/missing.txt", baselineMissing: true, verdict: "PARTIAL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handoff := fmt.Sprintf("Task: %s.\nConstraint: %s.\nBaseline: %s %s sha256=%s.\nPrior check: %s; fixture status only, not a correctness proof.\nOpen question: %s.\n",
				required[0], required[1], required[2], test.baselinePath, hashBytes(baseline), required[3], required[4])
			agentArgs, err := json.Marshal(map[string]string{
				"description": "Verify fixture observations", "prompt": handoff, "subagent_type": "verification",
			})
			if err != nil {
				t.Fatal(err)
			}
			readArgs, _ := json.Marshal(map[string]string{"file_path": test.baselinePath})
			baselinePrior := strings.TrimSpace(baseline)
			if test.baselineMissing {
				baselinePrior = "read: file not found:"
			}
			parentTools, childTools := []string{"Agent", "Read"}, []string{"Read"}
			script := providerScript{
				SchemaVersion: 2,
				Parent: &providerLane{Name: "parent", ExpectTools: parentTools, Steps: []providerStep{
					{Kind: "tool_call", CallID: "verify", Tool: "Agent", Arguments: agentArgs, ExpectModel: "deepseek-flash"},
					{
						Kind: "final", Text: "Verification outcome: " + test.verdict, ExpectModel: "deepseek-flash",
						ExpectPrior: []priorToolResult{{CallID: "verify", Contains: "VERDICT: " + test.verdict}},
					},
				}},
				Child: &providerLane{Name: "child", ExpectTools: childTools, Steps: []providerStep{
					{Kind: "tool_call", CallID: "baseline", Tool: "Read", Arguments: readArgs, ExpectModel: "deepseek-flash"},
					{
						Kind: "tool_call", CallID: "current", Tool: "Read", Arguments: json.RawMessage(`{"file_path":"current/value.txt"}`), ExpectModel: "deepseek-flash",
						ExpectPrior: []priorToolResult{{CallID: "baseline", Contains: baselinePrior}},
					},
					{
						Kind: "final", Text: "VERDICT: " + test.verdict, ExpectModel: "deepseek-flash",
						ExpectPrior: []priorToolResult{{CallID: "baseline", Contains: baselinePrior}, {CallID: "current", Contains: strings.TrimSpace(current)}},
					},
				}},
			}
			if err := validateScript(script); err != nil {
				t.Fatal("invalid native handoff script")
			}
			s := scenario{
				SchemaVersion: 1, ID: "verification-handoff", PermissionMode: "bypass", MaxTurns: 8, TimeoutMillis: 30000,
				Prompt: "Review the prepared fixture. " + required[0] + " " + required[1] + " Root-only note: " + rootOnly,
				Tools:  parentTools, Expected: expected{Status: "completed", TerminalReason: "completed", GitStatus: []string{}},
			}
			if err := validateScenario(s); err != nil {
				t.Fatal("invalid native handoff scenario")
			}
			files := map[string]string{
				"go.mod": "module fixture\n\ngo 1.27.0\n", "baseline/original.txt": baseline, "current/value.txt": current,
			}
			root, repo := scenarioRepo(t, files)
			var mu sync.Mutex
			childRequests, parentRequests := 0, 0
			rootNoteObserved, privateNoteForwarded, badInput := false, false, false
			var firstChildCounts []int
			firstChildHandoffCopies := 0
			provider := newProviderWithHandler(t, script, parentTools, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
					if err != nil || len(raw) > manifestLimit {
						http.Error(w, "bound", http.StatusBadRequest)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(raw))
					var request struct {
						Input json.RawMessage `json:"input"`
						Tools []struct {
							Name string `json:"name"`
						} `json:"tools"`
					}
					if err := json.Unmarshal(raw, &request); err != nil {
						http.Error(w, "contract", http.StatusBadRequest)
						return
					}
					names := make([]string, 0, len(request.Tools))
					for _, tool := range request.Tools {
						names = append(names, tool.Name)
					}
					slices.Sort(names)
					mu.Lock()
					switch {
					case slices.Equal(names, parentTools):
						parentRequests++
						rootNoteObserved = rootNoteObserved || bytes.Contains(request.Input, []byte(rootOnly))
					case slices.Equal(names, childTools):
						if childRequests == 0 {
							var messages []struct {
								Role    string
								Content []struct{ Text string }
							}
							if err := json.Unmarshal(request.Input, &messages); err != nil {
								badInput = true
							} else {
								firstChildCounts = make([]int, len(required))
								for _, message := range messages {
									if message.Role != "user" {
										continue
									}
									for _, part := range message.Content {
										firstChildHandoffCopies += strings.Count(part.Text, strings.TrimSpace(handoff))
										for i, marker := range required {
											firstChildCounts[i] += strings.Count(part.Text, marker)
										}
									}
								}
							}
						}
						childRequests++
						privateNoteForwarded = privateNoteForwarded || bytes.Contains(request.Input, []byte(rootOnly))
					default:
						badInput = true
					}
					mu.Unlock()
					// Retain counts only. Neither request text nor tool output is
					// added to the fixture's structural journal or failure logs.
					next.ServeHTTP(w, r)
				})
			})
			defer provider.server.Close()
			binary := e2eBinary(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := runInvocation(ctx, t, binary, root, repo, s, provider.server.URL,
				"--provider", "agenticdeepseek", "--model", "deepseek-flash")
			if err != nil {
				t.Fatalf("native handoff failed: %v provider_requests=%d", err, provider.requestCount())
			}
			if out.Status != "completed" || out.ExitCode != 0 || out.TerminalReason != "completed" || out.Output != "Verification outcome: "+test.verdict {
				t.Fatal("native handoff completion or verification result changed")
			}
			mu.Lock()
			defer mu.Unlock()
			if badInput || parentRequests != 2 || childRequests != 3 || !rootNoteObserved || privateNoteForwarded {
				t.Fatalf("handoff isolation: parent=%d child=%d root_note_seen=%t private_note_forwarded=%t invalid_lane=%t",
					parentRequests, childRequests, rootNoteObserved, privateNoteForwarded, badInput)
			}
			if firstChildHandoffCopies != 1 || !slices.Equal(firstChildCounts, []int{1, 1, 1, 1, 1}) {
				t.Fatalf("explicit handoff copies = %d component copy counts = %v", firstChildHandoffCopies, firstChildCounts)
			}
			journal := provider.journalSnapshot()
			if len(journal) != 5 || journal[0].Lane != "parent" || journal[1].Lane != "child" || journal[2].Lane != "child" || journal[3].Lane != "child" || journal[4].Lane != "parent" {
				t.Fatal("foreground verification ordering or completion delivery changed")
			}
			for name, content := range files {
				if hash(t, filepath.Join(repo, filepath.FromSlash(name))) != hashBytes(content) {
					t.Fatalf("verification changed fixture file %s", name)
				}
			}
			if len(workingTreeStatus(t, repo)) != 0 {
				t.Fatal("verification left unexpected workspace changes")
			}
			if test.baselineMissing {
				if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(test.baselinePath))); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing baseline did not remain absent")
				}
			}
		})
	}
}
