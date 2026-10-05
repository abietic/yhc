package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecRunUsageAndOptionalLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flags  []string
		calls  int32
		failed bool
	}{
		{name: "default-off", calls: 2},
		{name: "call-limit", flags: []string{"--max-provider-calls", "1"}, calls: 1, failed: true},
		{name: "token-threshold", flags: []string{"--max-total-tokens", "1"}, calls: 1, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := prepareHeadlessJSONLProviderTest(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				item := `{"type":"message","id":"message-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]}`
				if call == 1 {
					item = p430Function("usage-write", filepath.Join(repo, "usage.txt"), "ok")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if call == 1 {
					_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":%s}\n\n", item)
				} else {
					_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"item_id\":\"message-final\",\"output_index\":0,\"content_index\":0,\"delta\":\"done\"}\n\n")
				}
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"usage-response\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"deepseek-v4-flash\",\"output\":[%s],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"total_tokens\":14,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n", item)
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			cmd := newRootCommand()
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			cmd.SetIn(bytes.NewReader(nil))
			args := []string{"exec", "exercise usage", "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--permission-mode", "acceptEdits", "--tools", "Write", "--max-turns", "4"}
			cmd.SetArgs(append(args, tc.flags...))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := cmd.ExecuteContext(ctx)
			if (err != nil) != tc.failed {
				t.Fatalf("err=%v stderr=%s output=%s", err, stderr.String(), out.String())
			}
			var result headlessEnvelope
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			u := result.Usage
			if calls.Load() != tc.calls || u == nil || u.ProviderCalls != uint64(tc.calls) || u.TotalTokens != uint64(tc.calls)*14 || u.CachedPromptTokens != uint64(tc.calls)*6 || u.ReasoningTokens != uint64(tc.calls)*2 || !u.Complete {
				t.Fatalf("calls=%d usage=%+v", calls.Load(), u)
			}
			if tc.failed && (result.Error == nil || result.Error.Code != "run_budget_exceeded" || result.ExitCode != 1) {
				t.Fatalf("result=%+v", result)
			}
			if !tc.failed && (u.Limits.MaxProviderCalls != 0 || u.Limits.MaxTotalTokens != 0) {
				t.Fatal("default enabled a limit")
			}
		})
	}
}

func TestExecTimeoutCancelsProviderAndPreservesUnknownUsage(t *testing.T) {
	prepareHeadlessJSONLProviderTest(t)
	var calls atomic.Int32
	var sawDeadline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(body)
		sawDeadline.Store(bytes.Contains(encoded, []byte("time_remaining_seconds=")))
		<-r.Context().Done()
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetArgs([]string{"exec", "deadline probe", "--timeout", "1s", "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("timeout became success")
	}
	var result headlessEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v output=%s stderr=%s", err, out.String(), stderr.String())
	}
	if calls.Load() != 1 || !sawDeadline.Load() {
		t.Fatalf("calls=%d deadline=%v", calls.Load(), sawDeadline.Load())
	}
	if result.Status != "cancelled" || result.ExitCode != 130 || result.Usage == nil || result.Usage.Complete || result.Usage.UnknownCalls != 1 || result.Usage.InFlight != 0 {
		t.Fatalf("result=%+v usage=%+v", result, result.Usage)
	}
}

func TestExecRejectsNegativeTimeoutBeforeProviderSetup(t *testing.T) {
	var out, stderr bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"exec", "probe", "--timeout=-1s", "--output-format", "json"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("negative timeout accepted")
	}
	var result headlessEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Code != "usage_error" {
		t.Fatalf("result=%+v", result)
	}
}
