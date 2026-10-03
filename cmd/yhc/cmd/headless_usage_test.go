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
	"sync/atomic"
	"testing"
	"time"
)

func TestExecBudgetResumeKeepsCompletedToolAndFreshAllowance(t *testing.T) {
	repo := prepareHeadlessJSONLProviderTest(t)
	artifact := filepath.Join(repo, "budget-progress.txt")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		item := p430Function("budget-write-once", artifact, "saved progress")
		if call == 2 {
			if !bytes.Contains(body, []byte("budget-write-once")) || !bytes.Contains(body, []byte("saved progress")) {
				t.Errorf("resume lost completed tool history: %s", body)
			}
			item = `{"type":"message","id":"budget-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"continued","annotations":[]}]}`
		} else if call != 1 {
			t.Errorf("unexpected replay or retry, call %d", call)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":%s}\n\n", item)
		} else {
			_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"item_id\":\"budget-final\",\"output_index\":0,\"content_index\":0,\"delta\":\"continued\"}\n\n")
		}
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"budget-response\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"deepseek-v4-flash\",\"output\":[%s],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"total_tokens\":14}}}\n\n", item)
	}))
	defer server.Close()
	run := func(resume string) (headlessEnvelope, error) {
		var out, stderr bytes.Buffer
		cmd := newRootCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetIn(bytes.NewReader(nil))
		args := []string{"exec", "continue saved work", "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--permission-mode", "acceptEdits", "--tools", "Write", "--max-provider-calls", "1"}
		if resume != "" {
			args = append(args, "--resume", resume)
		}
		cmd.SetArgs(args)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := cmd.ExecuteContext(ctx)
		var result headlessEnvelope
		if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
			t.Fatalf("decode result: %v stdout=%s stderr=%s", decodeErr, out.String(), stderr.String())
		}
		return result, err
	}
	first, err := run("")
	if err == nil || first.Error == nil || first.Error.Code != "run_budget_exceeded" || first.SessionID == "" {
		t.Fatalf("first result=%+v err=%v", first, err)
	}
	before, err := os.Stat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	second, err := run(first.SessionID)
	if err != nil || second.Status != "completed" || second.SessionID != first.SessionID {
		t.Fatalf("second result=%+v err=%v", second, err)
	}
	after, err := os.Stat(artifact)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("completed write was replayed: before=%v after=%v err=%v", before, after, err)
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "saved progress" || calls.Load() != 2 {
		t.Fatalf("artifact=%q calls=%d err=%v", data, calls.Load(), err)
	}
	for _, result := range []headlessEnvelope{first, second} {
		if result.Usage == nil || !result.Usage.Complete || result.Usage.ProviderCalls != 1 || result.Usage.TotalTokens != 14 || result.Usage.Limits.MaxProviderCalls != 1 {
			t.Fatalf("segment allowance/usage=%+v", result.Usage)
		}
	}
}

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
