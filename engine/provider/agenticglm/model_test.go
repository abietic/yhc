package agenticglm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestGenerateUsesNativeGLMContract(t *testing.T) {
	type capturedRequest struct {
		Path          string
		Authorization string
		Body          map[string]any
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- capturedRequest{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"glm-response-1",
			"request_id":"glm-request-1",
			"model":"glm-5.3-flash",
			"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"checked","content":"done","tool_calls":[{"id":"call-2","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"next\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":4}}
		}`)
	}))
	defer server.Close()

	client, err := New(context.Background(), &Config{
		APIKey:        "glm-secret",
		BaseURL:       server.URL + "/api/paas/v4/",
		Model:         ModelGLM53Flash,
		ClearThinking: boolPointer(false),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	input := []*schema.AgenticMessage{
		schema.SystemAgenticMessage("system"),
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: "inspect"}),
				schema.NewContentBlock(&schema.UserInputImage{URL: "https://example.com/screen.png"}),
			},
		},
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.Reasoning{Text: "prior-thought"}),
				schema.NewContentBlock(&schema.FunctionToolCall{CallID: "call-1", Name: "lookup", Arguments: `{"query":"old"}`}),
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: "call-1",
						Name:   "lookup",
						Content: []*schema.FunctionToolResultContentBlock{{
							Type: schema.FunctionToolResultContentBlockTypeText,
							Text: &schema.UserInputText{Text: `{"ok":true}`},
						}},
					},
				},
			},
		},
	}
	out, err := client.Generate(
		context.Background(),
		input,
		model.WithTools([]*schema.ToolInfo{{Name: "lookup", Desc: "look up a value"}}),
		WithReasoningEffort(ReasoningEffortHigh),
	)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	request := <-captured
	if request.Path != "/api/paas/v4/chat/completions" {
		t.Fatalf("request path = %q", request.Path)
	}
	if request.Authorization != "Bearer glm-secret" {
		t.Fatalf("authorization = %q", request.Authorization)
	}
	if request.Body["model"] != ModelGLM53Flash || request.Body["stream"] != false {
		t.Fatalf("request identity = %#v", request.Body)
	}
	if request.Body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v", request.Body["reasoning_effort"])
	}
	thinking, ok := request.Body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" || thinking["clear_thinking"] != false {
		t.Fatalf("thinking = %#v", request.Body["thinking"])
	}
	if _, exists := request.Body["tool_stream"]; exists {
		t.Fatalf("non-stream request unexpectedly contains tool_stream: %#v", request.Body)
	}
	tools, ok := request.Body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", request.Body["tools"])
	}
	messages, ok := request.Body["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("messages = %#v", request.Body["messages"])
	}
	assistant := messages[2].(map[string]any)
	if assistant["reasoning_content"] != "prior-thought" {
		t.Fatalf("assistant reasoning = %#v", assistant)
	}
	toolResult := messages[3].(map[string]any)
	if toolResult["role"] != "tool" || toolResult["tool_call_id"] != "call-1" {
		t.Fatalf("tool result = %#v", toolResult)
	}

	if out.Role != schema.AgenticRoleTypeAssistant || len(out.ContentBlocks) != 3 {
		t.Fatalf("output = %#v", out)
	}
	if got := out.ContentBlocks[0].Reasoning.Text; got != "checked" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := out.ContentBlocks[1].AssistantGenText.Text; got != "done" {
		t.Fatalf("text = %q", got)
	}
	call := out.ContentBlocks[2].FunctionToolCall
	if call.CallID != "call-2" || call.Name != "lookup" || call.Arguments != `{"query":"next"}` {
		t.Fatalf("tool call = %#v", call)
	}
	ext, ok := out.ResponseMeta.Extension.(*ResponseMetaExtension)
	if !ok || ext.FinishReason != "tool_calls" || ext.ResponseID != "glm-response-1" || ext.RequestID != "glm-request-1" {
		t.Fatalf("response extension = %#v", out.ResponseMeta.Extension)
	}
	usage := out.ResponseMeta.TokenUsage
	if usage == nil || usage.PromptTokens != 11 || usage.PromptTokenDetails.CachedTokens != 3 || usage.CompletionTokensDetails.ReasoningTokens != 4 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestStreamRequiresDoneAndEmitsReasoningTextToolAndTerminalMeta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body["stream"] != true || body["tool_stream"] != true {
			http.Error(w, "stream flags missing", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"glm-stream","model":"glm-5.3-flash","choices":[{"index":0,"delta":{"reasoning_content":"think"},"finish_reason":null}]}

data: {"id":"glm-stream","model":"glm-5.3-flash","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}

data: {"id":"glm-stream","model":"glm-5.3-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-stream","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}

data: {"id":"glm-stream","model":"glm-5.3-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}

data: [DONE]

`)
	}))
	defer server.Close()

	client, err := New(context.Background(), &Config{APIKey: "key", BaseURL: server.URL, Model: ModelGLM53Flash})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	reader, err := client.Stream(
		context.Background(),
		[]*schema.AgenticMessage{schema.UserAgenticMessage("go")},
		model.WithTools([]*schema.ToolInfo{{Name: "lookup", Desc: "lookup"}}),
	)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer reader.Close()

	var chunks []*schema.AgenticMessage
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() error = %v", recvErr)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 5 {
		t.Fatalf("chunk count = %d, chunks=%#v", len(chunks), chunks)
	}
	if chunks[0].ContentBlocks[0].Reasoning.Text != "think" || chunks[1].ContentBlocks[0].AssistantGenText.Text != "answer" {
		t.Fatalf("content chunks = %#v", chunks[:2])
	}
	firstCall := chunks[2].ContentBlocks[0].FunctionToolCall
	secondCall := chunks[3].ContentBlocks[0].FunctionToolCall
	if firstCall.CallID != "call-stream" || firstCall.Name != "lookup" || firstCall.Arguments != `{"q":` || secondCall.Arguments != "1}" {
		t.Fatalf("tool chunks = %#v %#v", firstCall, secondCall)
	}
	ext, ok := chunks[4].ResponseMeta.Extension.(*ResponseMetaExtension)
	if !ok || ext.FinishReason != "tool_calls" || chunks[4].ResponseMeta.TokenUsage.TotalTokens != 5 {
		t.Fatalf("terminal chunk = %#v", chunks[4])
	}
}

func TestStreamRejectsTruncatedSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"truncated\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer server.Close()
	client, err := New(context.Background(), &Config{APIKey: "key", BaseURL: server.URL, Model: ModelGLM53Flash})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	reader, err := client.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("go")})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer reader.Close()
	if _, err := reader.Recv(); err != nil {
		t.Fatalf("first Recv() error = %v", err)
	}
	if _, err := reader.Recv(); err == nil || !strings.Contains(err.Error(), "stream_done_missing") {
		t.Fatalf("terminal error = %v", err)
	}
}

func TestStreamDoesNotPublishTerminalMetadataBeforeDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"truncated-terminal\",\"model\":\"glm-5.3-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	client, err := New(context.Background(), &Config{APIKey: "key", BaseURL: server.URL, Model: ModelGLM53Flash})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := client.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if message, recvErr := reader.Recv(); recvErr == nil || message != nil || !strings.Contains(recvErr.Error(), "stream_done_missing") {
		t.Fatalf("Recv() = %#v, %v", message, recvErr)
	}
}

func TestNewRejectsUnsupportedModelAndRedactsCredential(t *testing.T) {
	if _, err := New(context.Background(), &Config{APIKey: "key", Model: "glm-4.7"}); err == nil || !strings.Contains(err.Error(), "model_unsupported") {
		t.Fatalf("unsupported model error = %v", err)
	}

	const secret = "glm-super-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"auth_failed","message":"credential glm-super-secret rejected"}}`)
	}))
	defer server.Close()
	client, err := New(context.Background(), &Config{APIKey: secret, BaseURL: server.URL, Model: ModelGLM53Flash})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("go")})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Generate() error = %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "auth_failed" {
		t.Fatalf("API error = %#v", err)
	}
}

func boolPointer(value bool) *bool { return &value }
