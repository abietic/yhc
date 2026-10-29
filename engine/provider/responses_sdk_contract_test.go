package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const responsesSDKFixture = `{"id":"resp-sdk-fixture","object":"response","model":"gpt-4o","status":"completed","output":[{"id":"msg-sdk-fixture","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}}`

func TestResponsesSDKDefaultAndPerCallExtraFields(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		select {
		case requests <- body:
		default:
			t.Error("unexpected extra SDK request")
			http.Error(w, "unexpected extra SDK request", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responsesSDKFixture)
	}))
	defer server.Close()
	defaults := map[string]any{"fixture_extra": "default"}
	maxRetries := 0
	inner, err := agenticopenai.NewResponsesModel(t.Context(), &agenticopenai.ResponsesConfig{
		BaseURL: server.URL, APIKey: "sentinel-provider-credential", Model: "gpt-4o",
		MaxRetries: &maxRetries, ExtraFields: defaults,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := wrapAgenticModel(inner)
	for _, tc := range []struct {
		name string
		opts []model.Option
		want any
	}{
		{name: "constructor defaults", want: "default"},
		{name: "clear per call", opts: []model.Option{agenticopenai.WithExtraFields(map[string]any{})}},
		{name: "override per call", opts: []model.Option{agenticopenai.WithExtraFields(map[string]any{"fixture_extra": "override"})}, want: "override"},
		{name: "defaults remain intact", want: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := client.Generate(t.Context(), []*schema.Message{schema.UserMessage("fixture")}, tc.opts...)
			if err != nil || out.Content != "ok" {
				t.Fatalf("Generate = %#v, %v", out, err)
			}
			body := <-requests
			if body["fixture_extra"] != tc.want {
				t.Fatalf("wire extra field = %v, want %v", body["fixture_extra"], tc.want)
			}
		})
	}
	if !reflect.DeepEqual(defaults, map[string]any{"fixture_extra": "default"}) {
		t.Fatal("per-call options mutated the constructor defaults")
	}
}

func TestResponsesSDKFactoryPreservesCacheWriteUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					http.NotFound(w, r)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", responsesSDKFixture)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, responsesSDKFixture)
				}
			}))
			defer server.Close()
			client, err := NewChatModel(t.Context(), Config{
				Provider: ProviderAgenticOpenAI, BaseURL: server.URL, Model: "gpt-4o", APIKey: "sentinel-provider-credential",
			})
			if err != nil {
				t.Fatal(err)
			}
			input := []*schema.Message{schema.UserMessage("fixture")}
			var tokenUsage *schema.TokenUsage
			if stream {
				reader, err := client.Stream(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				for {
					chunk, err := reader.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
						tokenUsage = chunk.ResponseMeta.Usage
					}
				}
			} else {
				out, err := client.Generate(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				if out.ResponseMeta != nil {
					tokenUsage = out.ResponseMeta.Usage
				}
			}
			if tokenUsage == nil {
				t.Fatal("missing SDK usage at the classic bridge")
			}
			encoded, err := json.Marshal(tokenUsage)
			if err != nil {
				t.Fatal(err)
			}
			var usage struct {
				Prompt int `json:"prompt_tokens"`
				Output int `json:"completion_tokens"`
				Total  int `json:"total_tokens"`
				Input  struct {
					Cached int  `json:"cached_tokens"`
					Write  *int `json:"cache_write_tokens"`
				} `json:"prompt_token_details"`
			}
			if err := json.Unmarshal(encoded, &usage); err != nil {
				t.Fatal(err)
			}
			if usage.Prompt != 8 || usage.Output != 2 || usage.Total != 10 || usage.Input.Cached != 2 || usage.Input.Write == nil || *usage.Input.Write != 3 {
				t.Fatalf("SDK usage was dropped or double-counted: %s", encoded)
			}
		})
	}
}
