package agentickimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const fixtureResponse = `{"id":"resp-kimi-fixture","object":"response","model":"kimi-k3","status":"completed","output":[{"type":"reasoning","id":"reason-fixture","status":"completed","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":null},{"type":"message","id":"msg-fixture","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}},"prompt_cache_options":{"mode":"implicit","ttl":"5m"}}`

func fixtureClient(t *testing.T, handler http.HandlerFunc) *Model {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(t.Context(), &Config{APIKey: "sentinel-provider-credential", BaseURL: server.URL + "/v1", Model: ModelK3})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func fixtureInput() []*schema.AgenticMessage {
	return []*schema.AgenticMessage{schema.UserAgenticMessage("fixture")}
}

func TestGenerateUsesKimiWirePolicyAndIsolatesOptions(t *testing.T) {
	requests := make(chan map[string]any, 1)
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer sentinel-provider-credential" {
			t.Error("wrong endpoint or authorization")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		select {
		case requests <- request:
		default:
			t.Error("unexpected request")
			http.Error(w, "unexpected", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fixtureResponse)
	})
	for _, effort := range []ReasoningEffort{ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax, ""} {
		var opts []model.Option
		if effort != "" {
			opts = append(opts, WithReasoningEffort(effort))
		}
		output, err := client.Generate(t.Context(), fixtureInput(), opts...)
		if err != nil {
			t.Fatal(err)
		}
		if output.ResponseMeta.TokenUsage.PromptTokenDetails.CacheWriteTokens != 3 || output.ResponseMeta.TokenUsage.TotalTokens != 10 {
			t.Fatal("usage dropped or double counted")
		}
		var request map[string]any
		select {
		case request = <-requests:
		case <-time.After(3 * time.Second):
			t.Fatal("missing request")
		}
		want := string(effort)
		if want == "" {
			want = "max"
		}
		if request["reasoning"].(map[string]any)["effort"] != want || request["max_output_tokens"] != float64(131072) {
			t.Fatalf("request policy changed: %#v", request)
		}
		for _, key := range []string{"temperature", "top_p", "store", "previous_response_id", "conversation", "background", "reasoning_effort"} {
			if _, found := request[key]; found {
				t.Errorf("unexpected wire field %s", key)
			}
		}
		cache := request["prompt_cache_options"].(map[string]any)
		if cache["mode"] != "implicit" || cache["ttl"] != "5m" {
			t.Fatal("cache policy changed")
		}
	}
}

func TestUnsupportedOptionsFailBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, fixtureResponse) })
	for _, option := range []model.Option{WithReasoningEffort("medium"), model.WithModel("kimi-k3.1-pro"), model.WithModel("kimi-k2.6"), model.WithTemperature(0), model.WithTopP(1), model.WithMaxTokens(131073), model.WithStop([]string{"stop"}), model.WithToolChoice(schema.ToolChoiceForced)} {
		if _, err := client.Generate(t.Context(), fixtureInput(), option); err == nil {
			t.Fatal("unsupported request reached Generate")
		}
		if _, err := client.Stream(t.Context(), fixtureInput(), option); err == nil {
			t.Fatal("unsupported request reached Stream")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("local rejections made %d requests", calls.Load())
	}
}

func TestUsagePresenceAndMalformedReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, usage    string
		known, invalid bool
	}{
		{name: "missing"},
		{name: "null", usage: `null`},
		{name: "zero", usage: `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`, known: true},
		{name: "partial", usage: `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, invalid: true},
		{name: "overlapping input subsets", usage: `{"input_tokens":8,"output_tokens":2,"total_tokens":10,"input_tokens_details":{"cached_tokens":6,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}`, invalid: true},
		{name: "negative", usage: `{"input_tokens":-1,"output_tokens":2,"total_tokens":1,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response map[string]json.RawMessage
			if err := json.Unmarshal([]byte(fixtureResponse), &response); err != nil {
				t.Fatal(err)
			}
			if tc.usage == "" {
				delete(response, "usage")
			} else {
				response["usage"] = json.RawMessage(tc.usage)
			}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
			})
			result, err := client.Generate(t.Context(), fixtureInput())
			if tc.invalid {
				var protocol *ProtocolError
				if !errors.As(err, &protocol) {
					t.Fatalf("invalid usage = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (result.ResponseMeta.TokenUsage != nil) != tc.known {
				t.Fatalf("usage presence = %#v", result.ResponseMeta.TokenUsage)
			}
		})
	}
}

func TestHTTPFailuresAreBoundedRedactedAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"sentinel-provider-credential","message":"sentinel-provider-credential"}}`)
	})
	_, err := client.Generate(t.Context(), fixtureInput())
	var api *APIError
	if !errors.As(err, &api) || api.HTTPStatusCode() != 429 || calls.Load() != 1 {
		t.Fatalf("HTTP result: %v; requests %d", err, calls.Load())
	}
	if strings.Contains(err.Error(), "sentinel-provider-credential") {
		t.Fatal("error leaked response content")
	}
	targetCalls := atomic.Int32{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	redirect := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	_, err = redirect.Generate(t.Context(), fixtureInput())
	if !errors.As(err, &api) || api.StatusCode != 307 || targetCalls.Load() != 0 {
		t.Fatalf("redirect replayed request: %v", err)
	}
}

func streamFixture(response string) string {
	return "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp-kimi-fixture\",\"object\":\"response\",\"model\":\"kimi-k3\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"sequence_number\":1,\"output_index\":0,\"item_id\":\"reason-fixture\",\"delta\":\"think\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"output_index\":1,\"item_id\":\"msg-fixture\",\"delta\":\"o\"}\n\n" +
		fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":%s}\n\n", response)
}

func TestSemanticStreamReconcilesOutputAndSettlesUsageOnce(t *testing.T) {
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, streamFixture(fixtureResponse))
	})
	reader, err := client.Stream(t.Context(), fixtureInput())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var text, reason string
	receipts := 0
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range chunk.ContentBlocks {
			if block.Reasoning != nil {
				reason += block.Reasoning.Text
			}
			if block.AssistantGenText != nil {
				text += block.AssistantGenText.Text
			}
		}
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.TokenUsage != nil {
			receipts++
			if chunk.ResponseMeta.TokenUsage.TotalTokens != 10 || chunk.ResponseMeta.TokenUsage.PromptTokenDetails.CacheWriteTokens != 3 {
				t.Fatal("bad final usage")
			}
		}
	}
	if text != "ok" || reason != "think" || receipts != 1 {
		t.Fatalf("stream text=%q reasoning=%q usage receipts=%d", text, reason, receipts)
	}
}

func TestSemanticStreamRejectsMissingTerminalAndContradictions(t *testing.T) {
	base := streamFixture(fixtureResponse)
	terminal := strings.Index(base, "event: response.completed")
	for name, stream := range map[string]string{
		"truncated":             base[:terminal],
		"chat done":             base[:terminal] + "data: [DONE]\n\n",
		"mismatched event":      strings.Replace(base, "event: response.completed", "event: response.incomplete", 1),
		"missing sequence":      strings.Replace(base, `"sequence_number":1,`, "", 1),
		"repeated sequence":     strings.Replace(base, `"sequence_number":2`, `"sequence_number":1`, 1),
		"changed text":          strings.Replace(base, `"delta":"o"`, `"delta":"x"`, 1),
		"changed response":      strings.Replace(base, `"sequence_number":3,"response":{"id":"resp-kimi-fixture"`, `"sequence_number":3,"response":{"id":"different"`, 1),
		"invalid opening usage": strings.Replace(base, `"status":"in_progress"}`, `"status":"in_progress","usage":{}}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, stream)
			})
			reader, err := client.Stream(t.Context(), fixtureInput())
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			for {
				_, err = reader.Recv()
				if err != nil {
					break
				}
			}
			if err == io.EOF {
				t.Fatal("invalid stream completed successfully")
			}
		})
	}
}

func TestCancellationClosesIdleHTTPBody(t *testing.T) {
	stopped := make(chan struct{})
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, err := client.Stream(ctx, fixtureInput())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = reader.Recv()
	reader.Close()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("idle HTTP body stayed open")
	}
}

func TestStreamToolCallsRequireAValidTerminalAndDeclaredTool(t *testing.T) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fixtureResponse), &response); err != nil {
		t.Fatal(err)
	}
	var output []json.RawMessage
	if err := json.Unmarshal(response["output"], &output); err != nil {
		t.Fatal(err)
	}
	output = append(output, json.RawMessage(`{"type":"function_call","id":"fc-fixture","status":"completed","call_id":"call-fixture","name":"fixture_tool","arguments":"{\"x\":1}"}`))
	response["output"], _ = json.Marshal(output)
	raw, _ := json.Marshal(response)
	stream := streamFixture(string(raw))
	terminal := strings.Index(stream, "event: response.completed")
	toolEvents := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":3,\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc-fixture\",\"call_id\":\"call-fixture\",\"name\":\"fixture_tool\",\"status\":\"in_progress\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":4,\"output_index\":2,\"item_id\":\"fc-fixture\",\"delta\":\"{\\\"x\\\":\"}\n\n"
	prefix := stream[:terminal] + toolEvents
	final := strings.Replace(stream[terminal:], `"sequence_number":3`, `"sequence_number":5`, 1)
	for _, tc := range []struct {
		name, body        string
		declared, success bool
	}{
		{name: "complete", body: prefix + final, declared: true, success: true},
		{name: "truncated", body: prefix, declared: true},
		{name: "undeclared", body: prefix + final},
		{name: "arguments changed", body: strings.Replace(prefix, `"delta":"{\"x\":`, `"delta":"{\"y\":`, 1) + final, declared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
			})
			var opts []model.Option
			if tc.declared {
				opts = append(opts, model.WithTools([]*schema.ToolInfo{{Name: "fixture_tool"}}))
			}
			reader, err := client.Stream(t.Context(), fixtureInput(), opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			calls := 0
			for {
				chunk, readErr := reader.Recv()
				if readErr != nil {
					err = readErr
					break
				}
				for _, block := range chunk.ContentBlocks {
					if block.FunctionToolCall != nil {
						calls++
						if chunk.ResponseMeta == nil || block.FunctionToolCall.CallID != "call-fixture" || block.FunctionToolCall.Arguments != `{"x":1}` {
							t.Fatal("tool committed before valid terminal or identity changed")
						}
					}
				}
			}
			if tc.success {
				if err != io.EOF || calls != 1 {
					t.Fatalf("calls=%d terminal=%v", calls, err)
				}
			} else if err == io.EOF || calls != 0 {
				t.Fatalf("invalid stream committed tool: calls=%d terminal=%v", calls, err)
			}
		})
	}
}

func TestLengthTerminalPreservesUsageWithoutPublishingPartialTools(t *testing.T) {
	response := strings.Replace(fixtureResponse, `"status":"completed"`, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, 1)
	response = strings.ReplaceAll(response, `"status":"completed"`, `"status":"in_progress"`)
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response), &object); err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(object["output"], &items); err != nil {
		t.Fatal(err)
	}
	items = append(items, json.RawMessage(`{"type":"function_call","id":"fc-partial","name":"fixture_tool","call_id":"partial-call","arguments":"{","status":"in_progress"}`))
	object["output"], _ = json.Marshal(items)
	raw, _ := json.Marshal(object)
	for _, tc := range []struct{ stream, conflict bool }{{}, {stream: true}, {stream: true, conflict: true}} {
		stream := tc.stream
		t.Run(fmt.Sprintf("stream=%t/conflict=%t", stream, tc.conflict), func(t *testing.T) {
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					delta := "{"
					if tc.conflict {
						delta = "["
					}
					_, _ = fmt.Fprintf(w, "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":0,\"output_index\":2,\"item_id\":\"fc-partial\",\"delta\":%q}\n\n", delta)
					_, _ = fmt.Fprintf(w, "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"sequence_number\":1,\"response\":%s}\n\n", raw)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				}
			})
			var result *schema.AgenticMessage
			var err error
			if stream {
				reader, e := client.Stream(t.Context(), fixtureInput())
				if e != nil {
					t.Fatal(e)
				}
				defer reader.Close()
				result, err = reader.Recv()
			} else {
				result, err = client.Generate(t.Context(), fixtureInput())
			}
			if tc.conflict {
				var protocol *ProtocolError
				if !errors.As(err, &protocol) {
					t.Fatalf("conflicting incomplete delta = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			extension := result.ResponseMeta.Extension.(*ResponseMetaExtension)
			if extension.FinishReason != "length" || result.ResponseMeta.TokenUsage == nil || result.ResponseMeta.TokenUsage.TotalTokens != 10 {
				t.Fatal("incomplete terminal lost its usage or length reason")
			}
			for _, block := range result.ContentBlocks {
				if block.FunctionToolCall != nil {
					t.Fatal("incomplete response published a callable tool")
				}
			}
		})
	}
}
