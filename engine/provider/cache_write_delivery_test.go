package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

func TestKimiCacheWriteReceiptsReachRunUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			written    int
			missing    bool
			missingTTL bool
			failed     bool
		}{
			{name: "positive", written: 3},
			{name: "explicit-zero"},
			{name: "missing-ttl", written: 3, missingTTL: true},
			{name: "missing-usage", missing: true},
			{name: "failed-terminal", failed: true},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				var body map[string]any
				if err := json.Unmarshal([]byte(kimiDeliveryResponse), &body); err != nil {
					t.Fatal(err)
				}
				body["usage"].(map[string]any)["input_tokens_details"].(map[string]any)["cache_write_tokens"] = tc.written
				if tc.missing {
					delete(body, "usage")
				}
				if tc.missingTTL {
					delete(body, "prompt_cache_options")
				}
				if tc.failed {
					body["status"] = "failed"
					body["output"] = []any{}
					body["error"] = map[string]string{"code": "provider_error"}
				}
				response, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "event: response.%s\ndata: {\"type\":\"response.%s\",\"sequence_number\":0,\"response\":%s}\n\n", body["status"], body["status"], response)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(response)
					}
				}))
				defer server.Close()
				client, err := NewChatModel(t.Context(), Config{Provider: ProviderAgenticKimi, Model: "kimi-k3", APIKey: "sentinel-provider-credential", BaseURL: server.URL + "/v1"})
				if err != nil {
					t.Fatal(err)
				}
				usage, err := execution.NewRunUsage(execution.RunUsageLimits{})
				if err != nil {
					t.Fatal(err)
				}
				messages := []*schema.Message{schema.UserMessage("fixture")}
				if stream {
					_, err = execution.SideQuery(t.Context(), client, execution.SideQueryOptions{Messages: messages, ProviderUsage: usage, UsageLogicalRoundID: usage.NewLogicalRoundID(), Provider: "agentickimi", Model: "kimi-k3"})
				} else {
					_, err = execution.GenerateWithUsage(t.Context(), client, messages, usage, execution.ProviderUsageDescriptor{Provider: "agentickimi", Model: "kimi-k3"})
				}
				if (err != nil) != tc.failed || calls.Load() != 1 {
					t.Fatalf("calls=%d err=%v", calls.Load(), err)
				}
				s := usage.Snapshot()
				record := s.CallLedger.Records[0]
				if tc.missing {
					if s.KnownCalls != 0 || s.UnknownCalls != 1 || s.CacheWriteReportedCalls != 0 || record.Tokens != nil || record.CacheTTL != "" {
						t.Fatalf("missing usage became known zero: %+v", s)
					}
					return
				}
				if s.KnownCalls != 1 || s.CacheWriteReportedCalls != 1 || s.TotalTokens != 10 || s.CacheWritePromptTokens != uint64(tc.written) || record.Tokens.CacheWritePromptTokens == nil || *record.Tokens.CacheWritePromptTokens != uint64(tc.written) {
					t.Fatalf("terminal receipt lost or counted twice: %+v", s)
				}
				wantTTL := "5m"
				if tc.missingTTL {
					wantTTL = ""
				}
				if record.CacheTTL != wantTTL {
					t.Fatalf("TTL inferred from request: got %q want %q", record.CacheTTL, wantTTL)
				}
				encoded, _ := json.Marshal(s)
				if !json.Valid(encoded) || record.ResolvedModel != "kimi-k3" {
					t.Fatal("public usage snapshot lost model identity")
				}
			})
		}
	}
}
