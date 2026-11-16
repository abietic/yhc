package agenticdeepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/internal/providercost"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const applicationResponse = `{"id":"resp-app","object":"response","status":"completed","model":"deepseek-flash","title":"Example title","output":[{"type":"web_search_call","id":"srv-1","status":"completed","kind":"read_link","action":{"type":"open_page","url":"https://example.com"},"page":{"url":"https://example.com","title":"Example"},"signature":"opaque-signature"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"中文 answer","annotations":[{"type":"url_citation","start_index":0,"end_index":2,"url":"https://example.com","title":"Example"}]}]}],"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":2},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":16,"server_tool_use":{"tool_search":2,"tool_fetch":1,"tool_image_search":3}}}`

func TestApplicationOptionsWireAndIsolation(t *testing.T) {
	m, err := New(t.Context(), &Config{APIKey: "fixture", Model: FlashModel, Application: &ApplicationConfig{WebSearch: true}})
	if err != nil {
		t.Fatal(err)
	}
	input := []*schema.AgenticMessage{schema.UserAgenticMessage("question")}
	common, specific := m.options(WithApplicationConfig(ApplicationConfig{ReadURL: true, SafetyCheck: SafetyLevelHigh, GenerateTitle: true}))
	req, err := buildResponseRequest(input, common, specific, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["tools"]) != `[{"type":"deepseek_read_url"}]` || string(wire["safety_check"]) != `{"level":"high"}` || string(wire["title"]) != `{"enabled":true}` {
		t.Fatalf("wire = %s", raw)
	}
	common, specific = m.options()
	req, err = buildResponseRequest(input, common, specific, false)
	if err != nil || len(req.Tools) != 1 || req.Tools[0].Type != "deepseek_web_search" || req.SafetyCheck != nil {
		t.Fatalf("config mutated: %#v, %v", req, err)
	}
}

func TestApplicationRejectsUnsupportedCombinations(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		opts []model.Option
	}{
		{"model", Config{Model: "deepseek-v4-pro"}, nil},
		{"safety", Config{Model: FlashModel, Application: &ApplicationConfig{SafetyCheck: "invalid"}}, nil},
		{"format", Config{Model: FlashModel, TextFormat: &TextFormat{Type: TextFormatJSONObject}}, nil},
		{"logprobs", Config{Model: FlashModel, TopLogProbs: intPointer(0)}, nil},
		{"forced", Config{Model: FlashModel}, []model.Option{model.WithTools([]*schema.ToolInfo{{Name: "local"}}), model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForced})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.APIKey = "fixture"
			if tc.cfg.Application == nil {
				tc.cfg.Application = &ApplicationConfig{WebSearch: true}
			}
			m, err := New(t.Context(), &tc.cfg)
			if err == nil {
				common, specific := m.options(tc.opts...)
				_, err = buildResponseRequest([]*schema.AgenticMessage{schema.UserAgenticMessage("q")}, common, specific, false)
			}
			var conversion *ConversionError
			if !errors.As(err, &conversion) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestApplicationResponseMetadataAndReplay(t *testing.T) {
	var response responseObject
	if err := json.Unmarshal([]byte(applicationResponse), &response); err != nil {
		t.Fatal(err)
	}
	out, err := responseToAgentic(&response)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ContentBlocks) != 1 || out.ContentBlocks[0].AssistantGenText.Text != "中文 answer" {
		t.Fatalf("output = %#v", out)
	}
	ext := out.ResponseMeta.Extension.(*ResponseMetaExtension)
	if ext.Title != "Example title" || len(ext.WebToolCalls) != 1 || len(ext.Citations) != 1 || ext.Citations[0].EndIndex != 2 || ext.ServerToolUsage.ToolImageSearch != 3 {
		t.Fatalf("metadata = %#v", ext)
	}
	// Persisted Extras decode to generic maps/slices rather than Go structs.
	raw, _ := json.Marshal(out)
	var restored schema.AgenticMessage
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	items, err := messagesToInputItems([]*schema.AgenticMessage{&restored, schema.UserAgenticMessage("follow up")}, FlashModel)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(items)
	if !strings.Contains(string(wire), `"signature":"opaque-signature"`) || !strings.Contains(string(wire), `"kind":"read_link"`) || len(items) != 3 {
		t.Fatalf("replay = %s", wire)
	}
}

func TestApplicationStreamMetadataAndSafetyReview(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			terminal := applicationResponse
			event := "response.completed"
			if refused {
				terminal = `{"id":"resp-app","object":"response","status":"incomplete","incomplete_details":{"reason":"content_filter"},"model":"deepseek-flash","output":[]}`
				event = "response.incomplete"
			}
			wire := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"unreviewed text\"}\n\n" + fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"sequence_number\":1,\"response\":%s}\n\n", event, event, terminal)
			m, err := New(t.Context(), &Config{APIKey: "fixture", Model: FlashModel, Application: &ApplicationConfig{SafetyCheck: SafetyLevelHigh}, HTTPClient: &http.Client{Transport: applicationTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			sr, err := m.Stream(t.Context(), []*schema.AgenticMessage{schema.UserAgenticMessage("q")})
			if err != nil {
				t.Fatal(err)
			}
			defer sr.Close()
			var text string
			var final *schema.AgenticMessage
			for {
				chunk, err := sr.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, b := range chunk.ContentBlocks {
					if b.AssistantGenText != nil {
						text += b.AssistantGenText.Text
					}
				}
				if chunk.ResponseMeta != nil {
					final = chunk
				}
			}
			if refused && text != "" {
				t.Fatalf("refused text leaked: %q", text)
			}
			if !refused && (text != "unreviewed text" || final.ResponseMeta.Extension.(*ResponseMetaExtension).Title != "Example title") {
				t.Fatalf("text=%q final=%#v", text, final)
			}
		})
	}
}

func TestApplicationHighReviewCancellationReleasesRequest(t *testing.T) {
	requestEnded := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"unreviewed\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(requestEnded)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m, err := New(ctx, &Config{APIKey: "fixture", BaseURL: server.URL, Model: FlashModel, Application: &ApplicationConfig{SafetyCheck: SafetyLevelHigh}})
	if err != nil {
		t.Fatal(err)
	}
	sr, err := m.Stream(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("q")})
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	// No terminal verdict will arrive. Cancellation must interrupt the HTTP
	// read and discard the buffered content without waiting for that verdict.
	cancel()
	chunk, err := sr.Recv()
	if !errors.Is(err, context.Canceled) || chunk != nil {
		t.Fatalf("chunk=%#v error=%v", chunk, err)
	}
	select {
	case <-requestEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not release the provider request")
	}
}

func TestBudgetPreparationRejectsUnboundedServerToolCharges(t *testing.T) {
	m, err := New(context.Background(), &Config{APIKey: "fixture", Model: FlashModel, Application: &ApplicationConfig{WebSearch: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.PrepareBudgetStream(t.Context(), []*schema.AgenticMessage{schema.UserAgenticMessage("q")}, 8)
	if !errors.Is(err, providercost.ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
	// Explicit none disables even declared server tools and their extra fees.
	if _, err := m.PrepareBudgetStream(t.Context(), []*schema.AgenticMessage{schema.UserAgenticMessage("q")}, 8, model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden})); err != nil {
		t.Fatalf("disabled tools: %v", err)
	}
}

func TestReviewedStreamDiscardsContentOnTruncationAndFailure(t *testing.T) {
	for _, tail := range []string{"", "event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\"}}}\n\n"} {
		wire := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"not reviewed\"}\n\n" + tail
		var emitted int
		err := parseReviewedStream(strings.NewReader(wire), defaultMaxSSEEventBytes, true, parseResponseStream, func(*schema.AgenticMessage) bool { emitted++; return false })
		if err == nil || emitted != 0 {
			t.Fatalf("error=%v emitted=%d", err, emitted)
		}
	}
}

func TestApplicationReplayPreservesCallerOwnedBlocks(t *testing.T) {
	const raw = `{"id":"r","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"before"}]},{"type":"function_call","id":"f","call_id":"c","name":"lookup","arguments":"{}"},{"type":"web_search_call","id":"w","signature":"keep"},{"type":"message","content":[{"type":"output_text","text":"after"}]}]}`
	var response responseObject
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	message, err := responseToAgentic(&response)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the classic schema's separation of text and local functions.
	blocks := message.ContentBlocks
	message.ContentBlocks = []*schema.ContentBlock{blocks[0], blocks[2], blocks[1]}
	before, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := convertAssistantMessage(0, message); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("replay reordered caller-owned content blocks")
	}
}

func TestApplicationReplayOrderingRequiresCompleteIndexes(t *testing.T) {
	for _, invalid := range []*schema.StreamingMeta{nil, {Index: -1}} {
		first := &schema.ContentBlock{StreamingMeta: &schema.StreamingMeta{Index: 3}}
		second := &schema.ContentBlock{StreamingMeta: invalid}
		blocks := []*schema.ContentBlock{first, second}
		ordered := restoreReplayBlockOrder(blocks)
		if ordered[0] != first || ordered[1] != second {
			t.Fatalf("partial/invalid metadata reordered canonical blocks: %#v", invalid)
		}
	}
	first := &schema.ContentBlock{StreamingMeta: &schema.StreamingMeta{Index: 1}}
	second := &schema.ContentBlock{StreamingMeta: &schema.StreamingMeta{Index: 1}}
	ordered := restoreReplayBlockOrder([]*schema.ContentBlock{first, second})
	if ordered[0] != first || ordered[1] != second {
		t.Fatal("same-item content order changed")
	}
}

func TestApplicationReplayRejectsClientToolInjection(t *testing.T) {
	_, err := replayWebToolCalls(map[string]any{applicationExtraKey: map[string]any{"web_tool_calls": []any{map[string]any{"type": "function_call", "id": "bad", "name": "Bash"}}, "web_tool_call_indexes": []int{0}}})
	if err == nil {
		t.Fatal("accepted a client function in server-tool replay")
	}
}

func TestApplicationReplayPreservesInterleavedOutputOrder(t *testing.T) {
	var response responseObject
	if err := json.Unmarshal([]byte(`{"id":"r","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"before"}]},{"type":"web_search_call","id":"s","kind":"read_link","signature":"keep-me","action":{"type":"open_page","url":"https://example.com"}},{"type":"message","content":[{"type":"output_text","text":"after"}]}]}`), &response); err != nil {
		t.Fatal(err)
	}
	out, err := responseToAgentic(&response)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var restored schema.AgenticMessage
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	items, err := convertAssistantMessage(0, &restored)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || len(items[0].Content) != 1 || items[0].Content[0].Text != "before" || !strings.Contains(string(items[1].raw), "keep-me") || items[2].Content[0].Text != "after" {
		wire, _ := json.Marshal(items)
		t.Fatalf("replay=%s", wire)
	}
}

type applicationTransport func(*http.Request) (*http.Response, error)

func (f applicationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func intPointer(value int) *int { return &value }
