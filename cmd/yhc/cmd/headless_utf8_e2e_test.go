package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHeadlessJSONLBashExternalBytesCompleteLifecycle(t *testing.T) {
	prepareHeadlessJSONLProviderTest(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		var request p430ProviderRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var output []any
		switch calls.Add(1) {
		case 1:
			arguments, _ := json.Marshal(map[string]string{"command": `sleep 1; printf '\377tail\n'`})
			item := map[string]any{"type": "function_call", "id": "item-bytes", "call_id": "call-bytes", "name": "Bash", "arguments": string(arguments), "status": "completed"}
			data, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "sequence_number": 0, "output_index": 0, "item": item})
			_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: %s\n\n", data)
			output = []any{item}
		case 2:
			result, ok, err := p430FunctionOutput(request.Input, "call-bytes")
			if err != nil || !ok || !strings.Contains(result, "�tail") {
				t.Errorf("Bash result = %q, present=%v, err=%v", result, ok, err)
			}
			output = []any{map[string]any{"type": "message", "id": "message-done", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "done"}}}}
		default:
			t.Error("unexpected extra provider call")
			return
		}
		data, _ := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": 1, "response": map[string]any{"id": "response-bytes", "object": "response", "status": "completed", "model": "deepseek-v4-flash", "output": output}})
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	root := newRootCommand()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"exec", "exercise shell byte output", "--output-format", "jsonl", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--max-turns", "2", "--tools", "Bash", "--sandbox", "danger-full-access", "-y"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("headless Bash execution: %v; stderr=%s", err, stderr.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", calls.Load())
	}
	records := decodeHeadlessLifecycleRecords(t, stdout.String())
	progressSeen, terminalSeen, resultCount := false, false, 0
	for _, record := range records {
		if record.Event != nil && record.Event.Tool != nil {
			if record.Event.Kind == "tool_progress" {
				content := record.Event.Tool.Content
				if !utf8.ValidString(content) {
					t.Fatalf("invalid UTF-8 progress = %q", content)
				}
				progressSeen = progressSeen || strings.Contains(content, "�tail")
			}
			terminalSeen = terminalSeen || record.Event.Kind == "tool_terminal"
		}
		if record.Result != nil {
			resultCount++
			if record.Result.Status != "completed" || record.Result.ExitCode != 0 {
				t.Fatalf("final result = %+v", record.Result)
			}
		}
	}
	if !progressSeen || !terminalSeen || resultCount != 1 || records[len(records)-1].Result == nil {
		t.Fatalf("incomplete lifecycle: progress=%v terminal=%v results=%d", progressSeen, terminalSeen, resultCount)
	}
}
