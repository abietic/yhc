package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/execution"
)

func TestExecUsageCheckpointBeforeDispatchAndBeforeTools(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private checkpoint durability requires Linux or Darwin")
	}
	for _, breakTerminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal-write-failure-%t", breakTerminal), func(t *testing.T) {
			repo := prepareHeadlessJSONLProviderTest(t)
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(parent, "invocation")
			artifact := filepath.Join(repo, "checkpoint-tool.txt")
			price := writeHeadlessPriceCard(t, repo)
			data, err := os.ReadFile(price)
			if err != nil {
				t.Fatal(err)
			}
			var card map[string]any
			if err := json.Unmarshal(data, &card); err != nil {
				t.Fatal(err)
			}
			card["window"] = "upper_bound"
			card["routes"].([]any)[0].(map[string]any)["api_model"] = "deepseek-v4-flash"
			data, err = json.Marshal(card)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(price, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				call := calls.Add(1)
				owner, readErr := os.ReadFile(filepath.Join(directory, "owner.json"))
				var identity struct {
					SegmentID string `json:"segment_id"`
				}
				if readErr != nil || json.Unmarshal(owner, &identity) != nil {
					t.Error("provider entered without durable owner")
					w.WriteHeader(500)
					return
				}
				view, readErr := execution.ReadRunUsageCheckpointDir(directory, identity.SegmentID)
				if readErr != nil || view.Phase != "admitted" || view.Snapshot.InFlight != 1 || view.Snapshot.ProviderCalls != uint64(call) || view.Snapshot.Cost.Reservation.OutstandingAmount == "0.000000000000" {
					t.Errorf("provider entered without durable full admission: %+v, %v", view, readErr)
					w.WriteHeader(500)
					return
				}
				if breakTerminal {
					if err := os.Rename(directory, directory+"-retained"); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
				}
				item := p430Function("durable-write-once", artifact, "saved")
				if call > 1 {
					item = `{"type":"message","id":"checkpoint-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]}`
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":%s}\n\n", item)
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"checkpoint-response\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"deepseek-v4-flash\",\"output\":[%s],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"total_tokens\":14,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n", item)
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			cmd := newRootCommand()
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetIn(bytes.NewReader(nil))
			cmd.SetArgs([]string{"exec", "owned checkpoint tool fixture", "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--permission-mode", "acceptEdits", "--tools", "Write", "--max-turns", "3", "--max-provider-calls", "2", "--price-card", price, "--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "16", "--max-estimated-cost", "5", "--usage-checkpoint-dir", directory})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			runErr := cmd.ExecuteContext(ctx)
			var result headlessEnvelope
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("decode: %v stdout=%s stderr=%s", err, out.String(), stderr.String())
			}
			if (runErr != nil) != breakTerminal || result.Usage == nil || result.Usage.Checkpoint == nil {
				t.Fatalf("result=%+v err=%v", result, runErr)
			}
			usage := result.Usage
			if !usage.Complete || usage.Checkpoint.Failed != breakTerminal {
				t.Fatalf("known facts or persistence status lost: %+v", usage)
			}
			retained := directory
			if breakTerminal {
				retained += "-retained"
				if calls.Load() != 1 || usage.TotalTokens != 14 || result.Error == nil || result.Error.Code != "run_usage_unknown" {
					t.Fatalf("write failure did not stop: %+v", result)
				}
				if _, err := os.Stat(artifact); !os.IsNotExist(err) {
					t.Fatalf("tool ran after unpersisted terminal: %v", err)
				}
			} else {
				if calls.Load() != 2 || usage.TotalTokens != 28 || result.Status != "completed" {
					t.Fatalf("healthy tool round failed: %+v", result)
				}
				if data, err := os.ReadFile(artifact); err != nil || string(data) != "saved" {
					t.Fatalf("tool artifact=%q err=%v", data, err)
				}
			}
			var inspected bytes.Buffer
			inspect := newRootCommand()
			inspect.SetOut(&inspected)
			inspect.SetErr(&stderr)
			inspect.SetArgs([]string{"usage", "inspect", "--checkpoint-dir", retained, "--segment-id", usage.CallLedger.SegmentID})
			if err := inspect.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			var view execution.RunUsageCheckpoint
			if err := json.Unmarshal(inspected.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if view.CanResumeAgent || view.NewSpendingAuthorized || view.AccountingComplete == breakTerminal {
				t.Fatalf("inspection granted authority or lost accounting: %+v", view)
			}
			if breakTerminal && (view.Snapshot.InFlight != 1 || view.Snapshot.Cost.Reservation.OutstandingAmount == "0.000000000000") {
				t.Fatal("uncertain durable reservation released")
			}
		})
	}
}

func TestExecUsageCheckpointRejectsOldNamespaceBeforeProvider(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private checkpoint durability requires Linux or Darwin")
	}
	prepareHeadlessJSONLProviderTest(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "invocation")
	u, err := execution.NewRunUsage(execution.RunUsageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.EnableCheckpointDir(directory); err != nil {
		t.Fatal(err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(directory, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"exec", "must not dispatch", "--usage-checkpoint-dir", directory, "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey})
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("old namespace accepted")
	}
	var result headlessEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Error == nil || result.Error.Code != "usage_error" || calls.Load() != 0 {
		t.Fatalf("old namespace did not fail before provider setup: result=%+v calls=%d decode=%v", result, calls.Load(), err)
	}
	after, err := os.ReadFile(filepath.Join(directory, "checkpoint.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("old history overwritten")
	}
}
