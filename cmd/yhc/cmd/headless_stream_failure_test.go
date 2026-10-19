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

func TestExecStrictBudgetPreservesStreamFailureDiagnostic(t *testing.T) {
	for _, format := range []string{"json", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			repo := prepareHeadlessJSONLProviderTest(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Length", "4096")
				_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"output_index\":0,\"delta\":\"partial\"}\n\n")
			}))
			defer server.Close()
			card := filepath.Join(repo, "public-price-card.json")
			const pricing = `{"version":1,"currency":"CNY","source":"https://example.org/pricing","as_of":"2026-10-16T00:00:00Z","window":"upper_bound","routes":[{"provider":"agenticdeepseek","model":"legacy:deepseek-v4-flash","api_model":"deepseek-v4-flash","cached_input_per_million":"1","uncached_input_per_million":"2","output_per_million":"4"}]}`
			if err := os.WriteFile(card, []byte(pricing), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			cmd := newRootCommand()
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetIn(bytes.NewReader(nil))
			cmd.SetArgs([]string{"exec", "stream failure probe", "--output-format", format, "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--price-card", card, "--max-estimated-cost", "5", "--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "4"})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			err := cmd.ExecuteContext(ctx)
			data := out.Bytes()
			if format == "jsonl" {
				records := decodeHeadlessLifecycleRecords(t, out.String())
				data, _ = json.Marshal(records[len(records)-1].Result)
			}
			var result headlessEnvelope
			if decodeErr := json.Unmarshal(data, &result); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if err == nil || result.Status != "failed" || result.TerminalReason != "run_usage_unknown" || result.Error == nil || result.Error.Code != "run_usage_unknown" {
				t.Fatalf("expected an ambiguous-usage failure: %+v, %v", result, err)
			}
			if !strings.Contains(result.Error.Message, "reason=connection_closed phase=response_body") {
				t.Fatalf("terminal envelope lost the safe stream diagnostic: %s", result.Error.Message)
			}
			if result.Output != "partial" {
				t.Fatalf("stream diagnostic replaced or extended model output: %q", result.Output)
			}
			if strings.Contains(result.Error.Message, p430FakeKey) || strings.Contains(result.Error.Message, server.URL) {
				t.Fatal("terminal diagnostic exposed credentials or endpoint")
			}
			usage := result.Usage
			if calls.Load() != 1 || usage == nil || usage.ProviderCalls != 1 || usage.UnknownCalls != 1 || usage.Complete || usage.Cost.Complete || usage.Cost.Reservation.RetainedAmount != "2.097168000000" {
				t.Fatalf("failure retried or released uncertain exposure: calls=%d usage=%+v", calls.Load(), usage)
			}
		})
	}
}

func TestExecStrictBudgetAccountsMetadataOnlyResponse(t *testing.T) {
	for _, format := range []string{"json", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			repo := prepareHeadlessJSONLProviderTest(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, `event: response.completed
data: {"type":"response.completed","sequence_number":0,"response":{"id":"metadata-only","object":"response","status":"completed","model":"deepseek-v4-flash","output":[],"usage":{"input_tokens":20,"output_tokens":2,"total_tokens":22,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":1}}}}

`)
			}))
			defer server.Close()
			card := filepath.Join(repo, "public-price-card.json")
			const pricing = `{"version":1,"currency":"CNY","source":"https://example.org/pricing","as_of":"2026-10-19T00:00:00Z","window":"upper_bound","routes":[{"provider":"agenticdeepseek","model":"legacy:deepseek-v4-flash","api_model":"deepseek-v4-flash","cached_input_per_million":"1","uncached_input_per_million":"2","output_per_million":"4"}]}`
			if err := os.WriteFile(card, []byte(pricing), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			cmd := newRootCommand()
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetIn(bytes.NewReader(nil))
			cmd.SetArgs([]string{"exec", "metadata-only fixture", "--output-format", format, "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--price-card", card, "--max-estimated-cost", "5", "--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "4"})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			if err := cmd.ExecuteContext(ctx); err != nil {
				t.Fatalf("complete usage must not become unknown: %v", err)
			}
			data := out.Bytes()
			if format == "jsonl" {
				records := decodeHeadlessLifecycleRecords(t, out.String())
				data, _ = json.Marshal(records[len(records)-1].Result)
			}
			var result headlessEnvelope
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			usage := result.Usage
			if calls.Load() != 1 || result.Status != "completed" || result.Output != "" || usage == nil ||
				usage.KnownCalls != 1 || usage.UnknownCalls != 0 || !usage.Complete ||
				usage.PromptTokens != 20 || usage.CompletionTokens != 2 || usage.CachedPromptTokens != 4 || usage.ReasoningTokens != 1 ||
				!usage.Cost.Complete || usage.Cost.KnownEstimatedAmount != "0.000044000000" || usage.Cost.Reservation.RetainedAmount != "0.000000000000" {
				t.Fatalf("metadata-only usage or output changed: calls=%d result=%+v", calls.Load(), result)
			}
		})
	}
}
