package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

func TestDeepSeekV4ReasoningEffortReachesActualRequestBody(t *testing.T) {
	for _, tc := range []struct {
		effort        string
		thinking      string
		wireEffort    string
		hasWireEffort bool
	}{
		{effort: "none", thinking: "disabled"},
		{effort: "high", thinking: "enabled", wireEffort: "high", hasWireEffort: true},
		{effort: "max", thinking: "enabled", wireEffort: "max", hasWireEffort: true},
	} {
		t.Run(tc.effort, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(
					w,
					`data: {"id":"fixture","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n"+
						"data: [DONE]\n\n",
				)
			}))
			defer server.Close()

			inner, err := newAgenticDeepSeek(context.Background(), Config{
				Provider: ProviderAgenticDeepSeek,
				BaseURL:  server.URL,
				APIKey:   "test-key",
				Model:    "deepseek-v4-flash",
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := execution.CallModel(
				context.Background(),
				wrapAgenticModel(inner),
				[]*schema.Message{{Role: schema.User, Content: "hello"}},
				nil,
				nil,
				execution.CallModelOptions{
					Provider:    string(ProviderAgenticDeepSeek),
					Model:       "deepseek-v4-flash",
					EffortValue: tc.effort,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer result.StreamReader.Close()
			if _, err := result.StreamReader.Recv(); err != nil {
				t.Fatalf("receive fixture response: %v", err)
			}

			body := <-requests
			thinking, ok := body["thinking"].(map[string]any)
			if !ok || thinking["type"] != tc.thinking {
				t.Fatalf("actual DeepSeek thinking body = %#v", body["thinking"])
			}
			wireEffort, hasWireEffort := body["reasoning_effort"]
			if hasWireEffort != tc.hasWireEffort ||
				(tc.hasWireEffort && wireEffort != tc.wireEffort) {
				t.Fatalf(
					"actual DeepSeek reasoning_effort = %#v, present=%t",
					wireEffort,
					hasWireEffort,
				)
			}
		})
	}
}
