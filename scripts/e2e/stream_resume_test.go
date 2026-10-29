package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/execution"
)

func TestDeepSeekInterruptedStreamColdResumeKeepsOnlyCommittedWork(t *testing.T) {
	for _, fault := range []string{"eof", "tcp_reset", "missing_usage"} {
		t.Run(fault, func(t *testing.T) {
			s, script, files := loadFixture(t, "session-resume")
			root, repo := scenarioRepo(t, files)
			script.Steps[1] = providerStep{
				Kind: "tool_call", CallID: "uncommitted-write", Tool: "Write",
				Arguments:   json.RawMessage(`{"file_path":"uncommitted.txt","content":"never committed\n"}`),
				ExpectPrior: []priorToolResult{{CallID: "write", Contains: "Wrote 7 bytes to "}},
			}
			for i := range script.Steps {
				script.Steps[i].ExpectModel = "deepseek-flash"
			}
			if err := validateScript(script); err != nil {
				t.Fatal("invalid interruption script")
			}
			provider := newProviderWithHandler(t, script, s.Tools, interruptedResponse(t, fault))
			defer provider.server.Close()
			binary := e2eBinary(t)
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			flags := []string{"--provider", "agenticdeepseek", "--model", "deepseek-flash", "--max-provider-calls", "3", "--max-total-tokens", "100"}
			first, err := runInvocation(ctx, t, binary, root, repo, s, provider.server.URL, flags...)
			var processError *exec.ExitError
			if !errors.As(err, &processError) || processError.ExitCode() != 1 || first.SchemaVersion != 1 ||
				first.ExitCode != 1 || first.Status != "failed" || first.TerminalReason != "run_usage_unknown" ||
				first.Error == nil || first.Error.Code != "run_usage_unknown" || first.SessionID == "" {
				t.Fatalf("interrupted CLI lost its failed result: schema=%d exit=%d status=%q terminal=%q process=%v",
					first.SchemaVersion, first.ExitCode, first.Status, first.TerminalReason, err)
			}
			var stopped execution.RunUsageSnapshot
			if json.Unmarshal(first.Usage, &stopped) != nil {
				t.Fatal("missing interrupted usage snapshot")
			}
			if provider.requestCount() != 2 || stopped.ProviderCalls != 2 || stopped.KnownCalls != 1 || stopped.UnknownCalls != 1 ||
				stopped.InFlight != 0 || stopped.Complete || stopped.TotalTokens != 2 || stopped.StopReason != "run_usage_unknown" {
				t.Fatalf("interruption settled or retried unknown usage: requests=%d known=%d unknown=%d total=%d complete=%t",
					provider.requestCount(), stopped.KnownCalls, stopped.UnknownCalls, stopped.TotalTokens, stopped.Complete)
			}
			if len(stopped.CallLedger.Records) != 2 || stopped.CallLedger.Records[0].State != "known" ||
				stopped.CallLedger.Records[0].Tokens == nil || stopped.CallLedger.Records[1].State != "unknown" ||
				stopped.CallLedger.Records[1].Tokens != nil {
				t.Fatal("interruption lost the distinction between known and unknown receipts")
			}
			marker := filepath.Join(repo, "marker.txt")
			before, err := os.Stat(marker)
			if err != nil || hash(t, marker) != s.Expected.FileSHA256["marker.txt"] {
				t.Fatal("interruption lost the previously committed write")
			}
			assertOnlyCommittedFiles(t, repo, s)

			// A new process receives an explicit fresh invocation allowance. This
			// does not reconcile, reuse, or release any previous unknown receipt.
			resumeFlags := append(append([]string(nil), flags...), "--max-provider-calls", "1", "--resume", first.SessionID)
			second, err := runInvocation(ctx, t, binary, root, repo, s, provider.server.URL, resumeFlags...)
			if err != nil || second.SchemaVersion != 1 || second.Status != "completed" || second.ExitCode != 0 ||
				second.TerminalReason != "completed" || second.SessionID != first.SessionID || second.Output != "resumed" {
				t.Fatalf("fresh-process resume failed: status=%q terminal=%q process=%v", second.Status, second.TerminalReason, err)
			}
			var resumed execution.RunUsageSnapshot
			if json.Unmarshal(second.Usage, &resumed) != nil || !resumed.Complete || resumed.ProviderCalls != 1 ||
				resumed.KnownCalls != 1 || resumed.UnknownCalls != 0 || resumed.TotalTokens != 2 ||
				resumed.Limits.MaxProviderCalls != 1 || resumed.Limits.MaxTotalTokens != 100 ||
				stopped.CallLedger.SegmentID == "" || resumed.CallLedger.SegmentID == "" ||
				stopped.CallLedger.SegmentID == resumed.CallLedger.SegmentID {
				t.Fatal("resume reused or erased the old invocation's unknown ledger")
			}
			journal := provider.journalSnapshot()
			_, partialErr := os.Lstat(filepath.Join(repo, "uncommitted.txt"))
			t.Logf("resume diagnostic: requests=%d journal=%d partial_file_exists=%t", provider.requestCount(), len(journal), partialErr == nil)
			if provider.requestCount() != 3 || len(journal) != 3 || strings.Join(journal[2].Prior, ",") != "write" {
				t.Fatal("resume replayed or omitted committed tool history")
			}
			if count, consistent := provider.priorObservationSnapshot("write"); count != 2 || !consistent {
				t.Fatal("resume changed the exact committed tool result")
			}
			after, err := os.Stat(marker)
			if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) ||
				hash(t, marker) != s.Expected.FileSHA256["marker.txt"] {
				t.Fatal("resume rewrote the completed write")
			}
			assertOnlyCommittedFiles(t, repo, s)
			t.Logf("fault=%s first_calls=2 first_known=1 first_unknown=1 resume_calls=1 tool_writes=1", fault)
		})
	}
}

func assertOnlyCommittedFiles(t *testing.T, repo string, s scenario) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(repo, "uncommitted.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsuccessful streamed tool call reached the filesystem")
	}
	if strings.Join(workingTreeStatus(t, repo), "\n") != strings.Join(s.Expected.GitStatus, "\n") {
		t.Fatal("interruption or resume changed unrelated repository state")
	}
}

func interruptedResponse(t *testing.T, fault string) func(http.Handler) http.Handler {
	t.Helper()
	return func(next http.Handler) http.Handler {
		var ordinal atomic.Int32
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := ordinal.Add(1)
			if n == 3 {
				// Independently decode the outgoing wire shape: a partial tool
				// from the failed response must not enter resumed model history.
				raw, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
				if err != nil || len(raw) > manifestLimit {
					t.Error("resumed request exceeds fixture bounds")
					return
				}
				var request struct {
					Input []struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
					} `json:"input"`
				}
				var calls []string
				if json.Unmarshal(raw, &request) != nil {
					t.Error("resumed request is not valid JSON")
					return
				}
				for _, item := range request.Input {
					if item.Type == "function_call" {
						calls = append(calls, item.CallID)
					}
				}
				if strings.Join(calls, ",") != "write" {
					t.Errorf("resumed request call shape: total=%d committed=%d uncommitted=%d", len(calls), countCallID(calls, "write"), countCallID(calls, "uncommitted-write"))
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
			}
			if n != 2 {
				next.ServeHTTP(w, r)
				return
			}
			recorder := httptest.NewRecorder()
			next.ServeHTTP(recorder, r)
			raw := recorder.Body.Bytes()
			terminal := bytes.Index(raw, []byte("event: response.completed\n"))
			if terminal <= 0 {
				t.Error("fault fixture did not contain a complete control response")
				return
			}
			for key, values := range recorder.Header() {
				w.Header()[key] = append([]string(nil), values...)
			}
			if _, err := w.Write(raw[:terminal]); err != nil {
				t.Error("fault prefix write failed")
				return
			}
			if fault == "missing_usage" {
				dataStart := bytes.Index(raw[terminal:], []byte("data: "))
				var event struct {
					Type     string         `json:"type"`
					Sequence int64          `json:"sequence_number"`
					Response map[string]any `json:"response"`
				}
				if dataStart < 0 || json.Unmarshal(raw[terminal+dataStart+len("data: "):], &event) != nil || event.Response == nil {
					t.Error("invalid complete-receipt control")
					return
				}
				event.Response["usage"] = map[string]any{}
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Error("invalid missing-receipt fault")
					return
				}
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", encoded)
				return
			}
			controller := http.NewResponseController(w)
			if err := controller.Flush(); err != nil {
				t.Error("fault prefix flush failed")
				return
			}
			if fault == "tcp_reset" {
				connection, _, err := controller.Hijack()
				if err != nil {
					t.Error("TCP reset hijack failed")
					return
				}
				defer connection.Close()
				tcp, ok := connection.(*net.TCPConn)
				if !ok || tcp.SetLinger(0) != nil {
					t.Error("TCP reset unavailable")
				}
			}
		})
	}
}

func countCallID(calls []string, want string) int {
	n := 0
	for _, id := range calls {
		if id == want {
			n++
		}
	}
	return n
}
