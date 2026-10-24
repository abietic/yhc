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
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestExecDeepSeekKeepsBudgetedRequestPrefix(t *testing.T) {
	repo := prepareHeadlessJSONLProviderTest(t)
	artifact := filepath.Join(repo, "prefix-probe.txt")
	var mu sync.Mutex
	var requests []map[string]json.RawMessage
	var outputs [][]map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		seq := 0
		emit := func(kind string, fields map[string]any) {
			fields["type"] = kind
			fields["sequence_number"] = seq
			seq++
			b, _ := json.Marshal(fields)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b)
		}
		var output []map[string]any
		if n <= 2 {
			for _, delta := range []string{"inspect ", "fixture"} {
				emit("response.reasoning_text.delta", map[string]any{"item_id": "rs", "output_index": 0, "content_index": 0, "delta": delta})
			}
			for _, delta := range []string{"taking ", "action"} {
				emit("response.output_text.delta", map[string]any{"item_id": "msg", "output_index": 1, "content_index": 0, "delta": delta})
			}
			name := "Write"
			args, _ := json.Marshal(map[string]string{"file_path": artifact, "content": "saved fixture"})
			if n == 2 {
				name = "Read"
				args, _ = json.Marshal(map[string]string{"file_path": artifact})
			}
			call := map[string]any{"type": "function_call", "id": fmt.Sprintf("fc-%d", n), "call_id": fmt.Sprintf("call-%d", n), "name": name, "arguments": "", "status": "in_progress"}
			emit("response.output_item.added", map[string]any{"output_index": 2, "item": call})
			for _, delta := range []string{string(args[:len(args)/2]), string(args[len(args)/2:])} {
				emit("response.function_call_arguments.delta", map[string]any{"item_id": fmt.Sprintf("fc-%d", n), "output_index": 2, "delta": delta})
			}
			call["arguments"] = string(args)
			call["status"] = "completed"
			emit("response.output_item.done", map[string]any{"output_index": 2, "item": call})
			output = []map[string]any{{"type": "reasoning", "content": []map[string]string{{"type": "reasoning_text", "text": "inspect fixture"}}}, {"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": "taking action"}}}, call}
		} else {
			emit("response.output_text.delta", map[string]any{"item_id": "final", "output_index": 0, "content_index": 0, "delta": "done"})
			output = []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": "done"}}}}
		}
		mu.Lock()
		outputs = append(outputs, output)
		mu.Unlock()
		emit("response.completed", map[string]any{"response": map[string]any{"id": fmt.Sprintf("response-%d", n), "object": "response", "status": "completed", "model": "deepseek-flash", "output": output, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}}})
	}))
	defer s.Close()
	var out, stderr bytes.Buffer
	command := newRootCommand()
	command.SetOut(&out)
	command.SetErr(&stderr)
	command.SetIn(bytes.NewReader(nil))
	command.SetArgs([]string{"exec", "exercise append-only history", "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-flash", "--base-url", s.URL, "--api-key", p430FakeKey, "--permission-mode", "acceptEdits", "--tools", "Read,Write", "--max-turns", "4", "--max-provider-calls", "3", "--max-total-tokens", "10000"})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("exec: %v stderr=%s", err, stderr.String())
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "saved fixture" {
		t.Fatalf("tool artifact=%q err=%v", data, err)
	}
	var result headlessEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Usage == nil || !result.Usage.Complete ||
		result.Usage.ProviderCalls != 3 || result.Usage.TotalTokens != 360 {
		t.Fatalf("status=%s usage=%+v", result.Status, result.Usage)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests=%d want=3", len(requests))
	}
	for i := 1; i < len(requests); i++ {
		for _, k := range []string{"model", "tools", "reasoning", "max_output_tokens"} {
			if !bytes.Equal(requests[0][k], requests[i][k]) {
				t.Errorf("round %d changed %s", i+1, k)
			}
		}
		var previous, current []map[string]any
		if err := json.Unmarshal(requests[i-1]["input"], &previous); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(requests[i]["input"], &current); err != nil {
			t.Fatal(err)
		}
		if len(current) < len(previous)+4 {
			t.Fatalf("round %d has %d items, prior=%d", i+1, len(current), len(previous))
		}
		if !reflect.DeepEqual(previous, current[:len(previous)]) {
			t.Errorf("round %d rewrote earlier input prefix", i+1)
		}
		expected := outputs[i-1]
		// IDs/status are transport metadata; compare semantic content as explicit literals.
		got := current[len(previous) : len(previous)+3]
		for j := range got {
			for _, k := range []string{"type", "role", "content", "call_id", "name", "arguments"} {
				a, aok := got[j][k]
				b, bok := expected[j][k]
				ab, _ := json.Marshal(a)
				bb, _ := json.Marshal(b)
				if aok != bok || !bytes.Equal(ab, bb) {
					t.Errorf("round %d output item %d changed %s", i+1, j, k)
				}
			}
		}
		t.Logf("round=%d previous_input_items=%d new_items=%d reasoning_text_and_tool_arguments_checked=true", i+1, len(previous), len(current)-len(previous))
	}
}
