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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecStrictBudgetRejectsIncompleteTerminalUsage(t *testing.T) {
	const valid = `{"input_tokens":20,"output_tokens":2,"total_tokens":22,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":1}}`
	const zero = `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`
	for _, status := range []string{"completed", "incomplete"} {
		for _, tc := range []struct {
			name, usage string
			known       bool
		}{
			{"valid", valid, true},
			{"explicit_zero", zero, true},
			{"empty", `{}`, false},
			{"missing_input", strings.Replace(valid, `"input_tokens":20,`, "", 1), false},
			{"missing_cache", strings.Replace(valid, `"cached_tokens":4`, "", 1), false},
			{"null_thinking", strings.Replace(valid, `"reasoning_tokens":1`, `"reasoning_tokens":null`, 1), false},
			{"short_total", strings.Replace(valid, `"total_tokens":22`, `"total_tokens":21`, 1), false},
		} {
			for _, format := range []string{"json", "jsonl"} {
				t.Run(status+"/"+tc.name+"/"+format, func(t *testing.T) {
					repo := prepareHeadlessJSONLProviderTest(t)
					artifact := filepath.Join(repo, "receipt-side-effect.txt")
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						item := p430Function("receipt-write", artifact, "kept progress")
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":%s}\n\n", item)
						_, _ = fmt.Fprintf(w, "event: response.%s\ndata: {\"type\":\"response.%s\",\"sequence_number\":1,\"response\":{\"id\":\"terminal-receipt\",\"object\":\"response\",\"status\":%q,\"model\":\"deepseek-v4-flash\",\"output\":[%s],\"usage\":%s}}\n\n", status, status, status, item, tc.usage)
					}))
					defer server.Close()
					path := writeHeadlessPriceCard(t, repo)
					data, err := os.ReadFile(path)
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
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
					var out, stderr bytes.Buffer
					command := newRootCommand()
					command.SetOut(&out)
					command.SetErr(&stderr)
					command.SetIn(bytes.NewReader(nil))
					command.SetArgs([]string{
						"exec", "terminal receipt fixture", "--output-format", format,
						"--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL,
						"--api-key", p430FakeKey, "--permission-mode", "acceptEdits", "--tools", "Write",
						"--price-card", path, "--max-provider-calls", "1", "--max-estimated-cost", "5",
						"--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "4",
					})
					ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
					defer cancel()
					err = command.ExecuteContext(ctx)
					data = out.Bytes()
					if format == "jsonl" {
						records := decodeHeadlessLifecycleRecords(t, out.String())
						data, _ = json.Marshal(records[len(records)-1].Result)
					}
					var result headlessEnvelope
					if err := json.Unmarshal(data, &result); err != nil {
						t.Fatal(err)
					}
					if err == nil || result.Status != "failed" || result.Error == nil || calls.Load() != 1 || result.SessionID == "" {
						t.Fatalf("unexpected invocation result: calls=%d result=%+v err=%v", calls.Load(), result, err)
					}
					usage := result.Usage
					if usage == nil || usage.ProviderCalls != 1 || usage.Cost.Reservation.BoundViolations != 0 || usage.Cost.Reservation.OutstandingAmount != "0.000000000000" {
						t.Fatalf("invalid final accounting: %+v", usage)
					}
					if tc.known {
						wantCost := "0.000044000000"
						if tc.name == "explicit_zero" {
							wantCost = "0.000000000000"
						}
						if !usage.Complete || usage.KnownCalls != 1 || usage.UnknownCalls != 0 || !usage.Cost.Complete ||
							usage.Cost.KnownEstimatedAmount != wantCost || usage.Cost.Reservation.RetainedAmount != "0.000000000000" || result.TerminalReason != "run_budget_exceeded" {
							t.Fatalf("explicit receipt not settled: result=%+v usage=%+v", result, usage)
						}
						data, err := os.ReadFile(artifact)
						if err != nil || string(data) != "kept progress" {
							t.Fatalf("completed tool lost: %q %v", data, err)
						}
					} else {
						if usage.Complete || usage.KnownCalls != 0 || usage.UnknownCalls != 1 || usage.Cost.Complete ||
							usage.Cost.Reservation.RetainedAmount != "2.097168000000" || result.TerminalReason != "run_usage_unknown" {
							t.Errorf("unproven receipt released exposure: result=%+v usage=%+v", result, usage)
						}
						if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("unaccounted response executed a tool: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestExecUnbudgetedInvalidUsagePreservesOutput(t *testing.T) {
	for _, format := range []string{"json", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			prepareHeadlessJSONLProviderTest(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"output_index\":0,\"delta\":\"kept output\"}\n\n")
				_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"unbudgeted-receipt\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"deepseek-v4-flash\",\"output\":[],\"usage\":{}}}\n\n")
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			command := newRootCommand()
			command.SetOut(&out)
			command.SetErr(&stderr)
			command.SetIn(bytes.NewReader(nil))
			command.SetArgs([]string{
				"exec", "unbudgeted receipt fixture", "--output-format", format,
				"--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey,
			})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			err := command.ExecuteContext(ctx)
			data := out.Bytes()
			if format == "jsonl" {
				records := decodeHeadlessLifecycleRecords(t, out.String())
				data, _ = json.Marshal(records[len(records)-1].Result)
			}
			var result headlessEnvelope
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if err != nil || result.Status != "completed" || result.Error != nil || result.Output != "kept output" || calls.Load() != 1 {
				t.Fatalf("observer-only usage changed completion: result=%+v calls=%d err=%v", result, calls.Load(), err)
			}
			usage := result.Usage
			if usage == nil || usage.Complete || usage.KnownCalls != 0 || usage.UnknownCalls != 1 || usage.Cost != nil ||
				usage.Limits.MaxProviderCalls != 0 || usage.Limits.MaxTotalTokens != 0 || usage.Limits.MaxEstimatedCost != "" {
				t.Fatalf("default installed a limit or invented known-zero usage: %+v", usage)
			}
		})
	}
}
