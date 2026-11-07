package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecNativeTruncationUsesBoundedRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, terminal, status, cost string
		limit, calls                 int
		unknown                      bool
	}{
		{"recovered", "completed", "completed", "0.000088000000", 8, 2, false},
		{"truncated_tool", "completed", "completed", "0.000088000000", 8, 2, false},
		{"exhausted", "model_error", "failed", "0.000220000000", 8, 5, false},
		{"call_limit", "run_budget_exceeded", "failed", "0.000044000000", 1, 1, false},
		{"unknown_usage", "run_usage_unknown", "failed", "0.000000000000", 8, 1, true},
	} {
		for _, format := range []string{"json", "jsonl"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				repo := prepareHeadlessJSONLProviderTest(t)
				t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
				artifact := filepath.Join(repo, "truncated-write.txt")
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					var body struct {
						MaxOutputTokens int `json:"max_output_tokens"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MaxOutputTokens <= 0 || body.MaxOutputTokens > 16 {
						t.Errorf("recovery exceeded the per-call output cap: %+v, %v", body, err)
					}
					usage := `{"input_tokens":20,"output_tokens":2,"total_tokens":22,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":1}}`
					if tc.unknown {
						usage = `null`
					}
					status, details := "incomplete", `,"incomplete_details":{"reason":"max_output_tokens"}`
					deltaType, delta := "reasoning_text", "synthetic planning"
					if (tc.name == "recovered" || tc.name == "truncated_tool") && n == 2 {
						status, details, deltaType, delta = "completed", "", "output_text", "finished"
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: response.%s.delta\ndata: {\"type\":\"response.%s.delta\",\"sequence_number\":0,\"output_index\":0,\"delta\":%q}\n\n", deltaType, deltaType, delta)
					seq, output := 1, "[]"
					if tc.name == "truncated_tool" && n == 1 {
						item := p430Function("truncated-write", artifact, "must not execute")
						_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"output_index\":1,\"item\":%s}\n\n", item)
						seq, output = 2, "["+item+"]"
					}
					_, _ = fmt.Fprintf(w, "event: response.%s\ndata: {\"type\":\"response.%s\",\"sequence_number\":%d,\"response\":{\"id\":\"truncated-%d\",\"object\":\"response\",\"status\":%q,\"model\":\"deepseek-flash\",\"output\":%s%s,\"usage\":%s}}\n\n", status, status, seq, n, status, output, details, usage)
				}))
				defer server.Close()
				card := filepath.Join(repo, "public-price-card.json")
				const pricing = `{"version":1,"currency":"CNY","source":"https://example.org/pricing","as_of":"2026-11-07T00:00:00Z","window":"upper_bound","routes":[{"provider":"agenticdeepseek","model":"legacy:deepseek-flash","api_model":"deepseek-flash","cached_input_per_million":"1","uncached_input_per_million":"2","output_per_million":"4"}]}`
				if err := os.WriteFile(card, []byte(pricing), 0o600); err != nil {
					t.Fatal(err)
				}
				var stdout, stderr bytes.Buffer
				command := newRootCommand()
				command.SetOut(&stdout)
				command.SetErr(&stderr)
				command.SetIn(bytes.NewReader(nil))
				command.SetArgs([]string{
					"exec", "offline output truncation fixture", "--output-format", format,
					"--provider", "deepseek", "--model", "deepseek-flash", "--base-url", server.URL,
					"--api-key", p430FakeKey, "--max-turns", "8", "--price-card", card,
					"--permission-mode", "acceptEdits", "--tools", "Write",
					"--max-provider-calls", strconv.Itoa(tc.limit), "--max-estimated-cost", "5",
					"--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "16",
				})
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				err := command.ExecuteContext(ctx)
				data := stdout.Bytes()
				if format == "jsonl" {
					records := decodeHeadlessLifecycleRecords(t, stdout.String())
					data, _ = json.Marshal(records[len(records)-1].Result)
				}
				var result headlessEnvelope
				if decodeErr := json.Unmarshal(data, &result); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if (err == nil) != (tc.status == "completed") || result.Status != tc.status || result.TerminalReason != tc.terminal || calls.Load() != int32(tc.calls) {
					t.Fatalf("wrong truncation outcome: calls=%d result=%+v err=%v", calls.Load(), result, err)
				}
				if tc.status == "failed" && (result.Error == nil || result.ExitCode != ExitFailure) {
					t.Fatal("truncation failure reported a successful exit")
				}
				if tc.name == "recovered" && result.Output != "finished" {
					t.Fatalf("recovery did not deliver the completed answer: %q", result.Output)
				}
				if _, statErr := os.Stat(artifact); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("truncated response committed a pending write")
				}
				usage := result.Usage
				if usage == nil || usage.ProviderCalls != uint64(tc.calls) || usage.Cost.KnownEstimatedAmount != tc.cost || usage.Cost.Reservation.OutstandingAmount != "0.000000000000" {
					t.Fatalf("recovery lost cumulative accounting: %+v", usage)
				}
				if tc.unknown {
					if usage.Complete || usage.UnknownCalls != 1 || usage.Cost.Reservation.RetainedAmount != "2.097216000000" {
						t.Fatalf("unknown truncation retried or released its reserve: %+v", usage)
					}
				} else if !usage.Complete || usage.KnownCalls != uint64(tc.calls) || usage.UnknownCalls != 0 || usage.PromptTokens != uint64(tc.calls)*20 || usage.CompletionTokens != uint64(tc.calls)*2 || usage.ReasoningTokens != uint64(tc.calls) || usage.Cost.Reservation.RetainedAmount != "0.000000000000" {
					t.Fatalf("known truncation lost terminal usage: %+v", usage)
				}
			})
		}
	}
}
