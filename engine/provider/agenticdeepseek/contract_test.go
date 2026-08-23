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
	"sync/atomic"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestResponsesEndpointPreservesPrefixAndRejectsSecretURLParts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{name: "origin", baseURL: "https://example.com", want: "https://example.com/responses"},
		{name: "path prefix", baseURL: "https://example.com/proxy/v1/", want: "https://example.com/proxy/v1/responses"},
		{name: "existing endpoint", baseURL: "https://example.com/proxy/responses/", want: "https://example.com/proxy/responses"},
		{name: "userinfo", baseURL: "https://user:secret@example.com", wantErr: true},
		{name: "query", baseURL: "https://example.com?token=secret", wantErr: true},
		{name: "fragment", baseURL: "https://example.com#secret", wantErr: true},
		{name: "non HTTP", baseURL: "file:///tmp/socket", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := responsesEndpoint(tc.baseURL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("responsesEndpoint() = %q, %v", got, err)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("responsesEndpoint() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCallOptionsUseTypedResponsesFields(t *testing.T) {
	t.Parallel()

	request := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		request <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-options","object":"response","status":"completed","model":"deepseek-v4-flash","output":[]}`)
	}))
	defer server.Close()

	maxTokens := 123
	model, err := New(context.Background(), &Config{
		APIKey:             "fixture-key",
		BaseURL:            server.URL,
		Model:              "deepseek-v4-flash",
		MaxTokens:          &maxTokens,
		ReasoningEffort:    ReasoningEffortHigh,
		ResponseFormatType: ResponseFormatTypeText,
		UserID:             "tenant_default",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(
		context.Background(),
		[]*schema.AgenticMessage{schema.UserAgenticMessage("hello")},
		WithReasoningEffort(ReasoningEffortNone),
		WithTextFormat(TextFormat{Type: TextFormatJSONObject}),
		WithUserID("tenant_override"),
		WithTopLogProbs(7),
	)
	if err != nil {
		t.Fatal(err)
	}
	body := <-request
	if body["reasoning"].(map[string]any)["effort"] != "none" {
		t.Fatalf("reasoning = %#v", body["reasoning"])
	}
	if body["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("text = %#v", body["text"])
	}
	if body["user"] != "tenant_override" || body["top_logprobs"] != float64(7) {
		t.Fatalf("user/top_logprobs = %#v/%#v", body["user"], body["top_logprobs"])
	}
	if body["max_output_tokens"] != float64(maxTokens) || model.GetType() != "AgenticDeepSeek" {
		t.Fatalf("compatibility surface max/type = %#v/%q", body["max_output_tokens"], model.GetType())
	}
	for _, legacy := range []string{"messages", "thinking", "reasoning_effort"} {
		if _, exists := body[legacy]; exists {
			t.Fatalf("legacy Chat Completions field %q leaked into request", legacy)
		}
	}
}

func TestNewRejectsAmbiguousOrUnsupportedChatConfiguration(t *testing.T) {
	t.Parallel()

	one, two := 1, 2
	for _, config := range []*Config{
		{APIKey: "key", Model: "deepseek-v4-flash", MaxTokens: &one, MaxOutputTokens: &two},
		{APIKey: "key", Model: "deepseek-v4-flash", Stop: []string{"stop"}},
		{APIKey: "key", Model: "deepseek-v4-flash", LogProbs: new(bool)},
		{APIKey: "key", Model: "deepseek-v4-flash", TextFormat: &TextFormat{Type: TextFormatText}, ResponseFormatType: ResponseFormatTypeText},
	} {
		if _, err := New(context.Background(), config); err == nil {
			t.Fatalf("New accepted ambiguous or unsupported config: %#v", config)
		}
	}
}

func TestGenerateRejectsNonTerminalResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-live","object":"response","status":"in_progress","model":"deepseek-v4-flash","output":[]}`)
	}))
	defer server.Close()

	model, err := New(context.Background(), &Config{APIKey: "fixture-key", BaseURL: server.URL, Model: "deepseek-v4-flash"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	assertProtocolReason(t, err, "response_status_not_terminal")
}

func TestStreamRejectsProtocolDowngradesAndTruncation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		body          string
		maxEventBytes int
		want          string
	}{
		{
			name: "Chat Completions done marker",
			body: "data: [DONE]\n\n",
			want: "chat_completions_done_marker",
		},
		{
			name: "missing terminal",
			body: "event: response.created\n" +
				`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp-truncated","object":"response","status":"in_progress","model":"deepseek-v4-flash"}}` + "\n\n",
			want: "stream_terminal_event_missing",
		},
		{
			name: "non increasing sequence",
			body: "event: response.created\n" +
				`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp-sequence","object":"response","status":"in_progress","model":"deepseek-v4-flash"}}` + "\n\n" +
				"event: response.in_progress\n" +
				`data: {"type":"response.in_progress","sequence_number":0,"response":{"id":"resp-sequence","object":"response","status":"in_progress","model":"deepseek-v4-flash"}}` + "\n\n",
			want: "stream_sequence_not_increasing",
		},
		{
			name:          "bounded event",
			body:          "data: " + strings.Repeat("x", 2048) + "\n\n",
			maxEventBytes: 1024,
			want:          "stream_event_too_large",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			model, err := New(context.Background(), &Config{
				APIKey:           "fixture-key",
				BaseURL:          server.URL,
				Model:            "deepseek-v4-flash",
				MaxSSEEventBytes: tc.maxEventBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := model.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_, err = stream.Recv()
			assertProtocolReason(t, err, tc.want)
		})
	}
}

func TestImageCountFailsBeforeDispatch(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	model, err := New(context.Background(), &Config{APIKey: "fixture-key", BaseURL: server.URL, Model: VisionModel})
	if err != nil {
		t.Fatal(err)
	}
	blocks := make([]*schema.ContentBlock, 0, maxImagesPerRequest+1)
	for range maxImagesPerRequest + 1 {
		blocks = append(blocks, schema.NewContentBlock(&schema.UserInputImage{URL: "https://example.com/image.png"}))
	}
	_, err = model.Generate(context.Background(), []*schema.AgenticMessage{{
		Role:          schema.AgenticRoleTypeUser,
		ContentBlocks: blocks,
	}})
	var conversionErr *ConversionError
	if !errors.As(err, &conversionErr) || conversionErr.ReasonCode != "image_count_exceeded" {
		t.Fatalf("error = %T %#v", err, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", calls.Load())
	}
}

func TestImageAdmissionRejectsInvalidSourcesBeforeDispatch(t *testing.T) {
	t.Parallel()

	fileIDBlock := NewFileIDImageBlock("invalid-file-id")
	tests := []struct {
		name   string
		block  *schema.ContentBlock
		reason string
	}{
		{
			name: "ambiguous source",
			block: schema.NewContentBlock(&schema.UserInputImage{
				URL:        "https://example.com/image.png",
				Base64Data: "aGVsbG8=",
				MIMEType:   "image/png",
			}),
			reason: "image_source_ambiguous",
		},
		{
			name:   "non HTTP URL",
			block:  schema.NewContentBlock(&schema.UserInputImage{URL: "file:///tmp/image.png"}),
			reason: "image_url_invalid",
		},
		{
			name:   "overlong URL",
			block:  schema.NewContentBlock(&schema.UserInputImage{URL: "https://example.com/" + strings.Repeat("x", 8192)}),
			reason: "image_url_too_long",
		},
		{
			name: "unsupported MIME",
			block: schema.NewContentBlock(&schema.UserInputImage{
				Base64Data: "PHN2Zz4=",
				MIMEType:   "image/svg+xml",
			}),
			reason: "image_mime_unsupported",
		},
		{
			name: "invalid base64",
			block: schema.NewContentBlock(&schema.UserInputImage{
				Base64Data: "not-base64!",
				MIMEType:   "image/png",
			}),
			reason: "image_base64_invalid",
		},
		{
			name: "invalid detail",
			block: schema.NewContentBlock(&schema.UserInputImage{
				URL:    "https://example.com/image.png",
				Detail: schema.ImageURLDetail("thumbnail"),
			}),
			reason: "image_detail_invalid",
		},
		{name: "invalid file ID", block: fileIDBlock, reason: "image_file_id_invalid"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				calls.Add(1)
			}))
			defer server.Close()
			model, err := New(context.Background(), &Config{
				APIKey:  "fixture-key",
				BaseURL: server.URL,
				Model:   VisionModel,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Generate(context.Background(), []*schema.AgenticMessage{{
				Role:          schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{tc.block},
			}})
			var conversionErr *ConversionError
			if !errors.As(err, &conversionErr) || conversionErr.ReasonCode != tc.reason {
				t.Fatalf("error = %T %#v, want %q", err, err, tc.reason)
			}
			if calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", calls.Load())
			}
		})
	}
}

func TestToolCountFailsBeforeDispatch(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	model, err := New(context.Background(), &Config{
		APIKey:  "fixture-key",
		BaseURL: server.URL,
		Model:   "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := make([]*schema.ToolInfo, 0, maxToolsPerRequest+1)
	for index := range maxToolsPerRequest + 1 {
		tools = append(tools, &schema.ToolInfo{Name: fmt.Sprintf("tool_%d", index)})
	}
	_, err = model.Generate(
		context.Background(),
		[]*schema.AgenticMessage{schema.UserAgenticMessage("hello")},
		einomodel.WithTools(tools),
	)
	var conversionErr *ConversionError
	if !errors.As(err, &conversionErr) || conversionErr.ReasonCode != "tool_count_exceeded" {
		t.Fatalf("error = %T %#v", err, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", calls.Load())
	}
}

func TestCanceledContextIsPreservedWithoutEndpointLeak(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	model, err := New(context.Background(), &Config{APIKey: "super-secret-key", BaseURL: server.URL, Model: "deepseek-v4-flash"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = model.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %T %v", err, err)
	}
	for _, secret := range []string{"super-secret-key", server.URL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("transport error leaked %q: %v", secret, err)
		}
	}
}

func assertProtocolReason(t *testing.T, err error, want string) {
	t.Helper()
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.ReasonCode != want {
		t.Fatalf("error = %T %#v, want protocol reason %q", err, err, want)
	}
}
