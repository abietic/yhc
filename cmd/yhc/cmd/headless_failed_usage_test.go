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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecStrictBudgetAccountsFailedResponseReceipt(t *testing.T) {
	const valid = `{"input_tokens":20,"output_tokens":2,"total_tokens":22,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":1}}`
	for _, tc := range []struct {
		name, usage string
		known       bool
	}{
		{"valid", valid, true},
		{"missing", `null`, false},
		{"empty", `{}`, false},
		{"missing_input", strings.Replace(valid, `"input_tokens":20,`, "", 1), false},
		{"missing_output", strings.Replace(valid, `"output_tokens":2,`, "", 1), false},
		{"missing_total", strings.Replace(valid, `"total_tokens":22,`, "", 1), false},
		{"missing_cache", strings.Replace(valid, `"cached_tokens":4`, "", 1), false},
		{"missing_thinking", strings.Replace(valid, `"reasoning_tokens":1`, "", 1), false},
		{"negative_input", strings.Replace(valid, `"input_tokens":20`, `"input_tokens":-1`, 1), false},
		{"short_total", strings.Replace(valid, `"total_tokens":22`, `"total_tokens":21`, 1), false},
		{"cache_over_input", strings.Replace(valid, `"cached_tokens":4`, `"cached_tokens":21`, 1), false},
		{"thinking_over_output", strings.Replace(valid, `"reasoning_tokens":1`, `"reasoning_tokens":3`, 1), false},
	} {
		for _, format := range []string{"json", "jsonl"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				repo := prepareHeadlessJSONLProviderTest(t)
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":0,\"response\":{\"id\":\"failed-receipt\",\"object\":\"response\",\"status\":\"failed\",\"model\":\"deepseek-v4-flash\",\"output\":[],\"error\":{\"code\":\"fixture_failure\",\"message\":\"Synthetic failure\"},\"usage\":%s}}\n\n", tc.usage)
				}))
				defer server.Close()
				card := filepath.Join(repo, "public-price-card.json")
				const pricing = `{"version":1,"currency":"CNY","source":"https://example.org/pricing","as_of":"2026-10-23T00:00:00Z","window":"upper_bound","routes":[{"provider":"agenticdeepseek","model":"legacy:deepseek-v4-flash","api_model":"deepseek-v4-flash","cached_input_per_million":"1","uncached_input_per_million":"2","output_per_million":"4"}]}`
				if err := os.WriteFile(card, []byte(pricing), 0o600); err != nil {
					t.Fatal(err)
				}
				var out, stderr bytes.Buffer
				command := newRootCommand()
				command.SetOut(&out)
				command.SetErr(&stderr)
				command.SetIn(bytes.NewReader(nil))
				command.SetArgs([]string{
					"exec", "failed receipt fixture", "--output-format", format,
					"--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL,
					"--api-key", p430FakeKey, "--price-card", card, "--max-provider-calls", "1",
					"--max-estimated-cost", "5", "--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "4",
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
				if decodeErr := json.Unmarshal(data, &result); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if err == nil || result.Status != "failed" || result.Error == nil || calls.Load() != 1 {
					t.Fatalf("failed response succeeded or retried: calls=%d result=%+v err=%v", calls.Load(), result, err)
				}
				usage := result.Usage
				if usage == nil || usage.ProviderCalls != 1 || usage.Cost.Reservation.BoundViolations != 0 {
					t.Fatalf("missing usage or bound violation: %+v", usage)
				}
				if tc.known {
					if !usage.Complete || usage.KnownCalls != 1 || usage.UnknownCalls != 0 ||
						usage.PromptTokens != 20 || usage.CompletionTokens != 2 || usage.TotalTokens != 22 ||
						usage.CachedPromptTokens != 4 || usage.ReasoningTokens != 1 || !usage.Cost.Complete ||
						usage.Cost.KnownEstimatedAmount != "0.000044000000" || usage.Cost.Reservation.RetainedAmount != "0.000000000000" ||
						!strings.Contains(result.Error.Message, "fixture_failure") {
						t.Fatalf("authoritative failed-response receipt lost: result=%+v usage=%+v", result, usage)
					}
				} else if usage.Complete || usage.KnownCalls != 0 || usage.UnknownCalls != 1 ||
					usage.Cost.Reservation.RetainedAmount != "2.097168000000" || result.TerminalReason != "run_usage_unknown" {
					t.Fatalf("unproven usage released reservation: result=%+v usage=%+v", result, usage)
				}
			})
		}
	}
}
