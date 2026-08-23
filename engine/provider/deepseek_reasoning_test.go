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
		effort string
	}{
		{effort: "none"},
		{effort: "high"},
		{effort: "max"},
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
					"event: response.created\n"+
						`data: {"type":"response.created","sequence_number":0,"response":{"id":"fixture","object":"response","status":"in_progress","model":"deepseek-v4-flash"}}`+"\n\n"+
						"event: response.output_text.delta\n"+
						`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg-1","output_index":0,"content_index":0,"delta":"ok"}`+"\n\n"+
						"event: response.completed\n"+
						`data: {"type":"response.completed","sequence_number":2,"response":{"id":"fixture","object":"response","status":"completed","model":"deepseek-v4-flash","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`+"\n\n",
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
			reasoning, ok := body["reasoning"].(map[string]any)
			if !ok || reasoning["effort"] != tc.effort {
				t.Fatalf("actual DeepSeek Responses reasoning = %#v", body["reasoning"])
			}
		})
	}
}

func TestDeepSeekVisionInputAndToolsReachResponsesWire(t *testing.T) {
	t.Parallel()

	requests := make(chan map[string]any, 1)
	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"event: response.created\n"+
				`data: {"type":"response.created","sequence_number":0,"response":{"id":"vision-fixture","object":"response","status":"in_progress","model":"deepseek-v4-flash-vision-exp"}}`+"\n\n"+
				"event: response.output_text.delta\n"+
				`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg-1","output_index":0,"content_index":0,"delta":"seen"}`+"\n\n"+
				"event: response.completed\n"+
				`data: {"type":"response.completed","sequence_number":2,"response":{"id":"vision-fixture","object":"response","status":"completed","model":"deepseek-v4-flash-vision-exp","output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"seen"}]}]}}`+"\n\n",
		)
	}))
	defer server.Close()

	inner, err := newAgenticDeepSeek(context.Background(), Config{
		Provider: ProviderAgenticDeepSeek,
		BaseURL:  server.URL,
		APIKey:   "test-key",
		Model:    "deepseek-v4-flash-vision-exp",
	})
	if err != nil {
		t.Fatal(err)
	}
	imageURL := "https://example.com/screenshot.png"
	tool := &schema.ToolInfo{Name: "inspect_page", Desc: "inspect the page"}
	result, err := execution.CallModel(
		context.Background(),
		wrapAgenticModel(inner),
		[]*schema.Message{{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "what is visible?"},
				{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{
						MessagePartCommon: schema.MessagePartCommon{URL: &imageURL},
						Detail:            schema.ImageURLDetail("original"),
					},
				},
			},
		}},
		nil,
		[]*schema.ToolInfo{tool},
		execution.CallModelOptions{
			Provider: string(ProviderAgenticDeepSeek),
			Model:    "deepseek-v4-flash-vision-exp",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer result.StreamReader.Close()
	chunk, err := result.StreamReader.Recv()
	if err != nil || chunk.Content != "seen" {
		t.Fatalf("vision response = %#v, %v", chunk, err)
	}

	if got := <-paths; got != "/responses" {
		t.Fatalf("request path = %q, want /responses", got)
	}
	body := <-requests
	if body["model"] != "deepseek-v4-flash-vision-exp" || body["stream"] != true {
		t.Fatalf("request header fields = %#v", body)
	}
	items := body["input"].([]any)
	content := items[0].(map[string]any)["content"].([]any)
	image := content[1].(map[string]any)
	if image["type"] != "input_image" || image["image_url"] != imageURL || image["detail"] != "original" {
		t.Fatalf("vision input = %#v", image)
	}
	tools := body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "inspect_page" {
		t.Fatalf("tools = %#v", body["tools"])
	}
}
