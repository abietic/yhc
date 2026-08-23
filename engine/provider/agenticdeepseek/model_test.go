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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestGenerateUsesResponsesWithVisionToolsAndUsage(t *testing.T) {
	t.Parallel()

	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fixture-key" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp-1",
			"object":"response",
			"created_at":1,
			"status":"completed",
			"model":"deepseek-v4-flash-vision-exp",
			"output":[
				{"type":"reasoning","id":"rs-1","status":"completed","content":[{"type":"reasoning_text","text":"think"}]},
				{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
				{"type":"function_call","id":"fc-2","status":"completed","call_id":"call-2","name":"next_tool","arguments":"{\"q\":\"x\"}"}
			],
			"usage":{
				"input_tokens":20,
				"input_tokens_details":{"cached_tokens":7},
				"output_tokens":11,
				"output_tokens_details":{"reasoning_tokens":3},
				"total_tokens":31
			}
		}`)
	}))
	defer server.Close()

	m, err := New(context.Background(), &Config{
		APIKey:  "fixture-key",
		BaseURL: server.URL,
		Model:   VisionModel,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	input := []*schema.AgenticMessage{
		schema.SystemAgenticMessage("system"),
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: "inspect"}),
				schema.NewContentBlock(&schema.UserInputImage{
					Base64Data: "aGVsbG8=",
					MIMEType:   "image/png",
					Detail:     schema.ImageURLDetail("original"),
				}),
			},
		},
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.Reasoning{Text: "prior reasoning"}),
				schema.NewContentBlock(&schema.AssistantGenText{Text: "taking screenshot"}),
				schema.NewContentBlock(&schema.FunctionToolCall{
					CallID:    "call-1",
					Name:      "take_screenshot",
					Arguments: "{}",
				}),
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.FunctionToolResult{
					CallID: "call-1",
					Name:   "take_screenshot",
					Content: []*schema.FunctionToolResultContentBlock{
						{
							Type: schema.FunctionToolResultContentBlockTypeText,
							Text: &schema.UserInputText{Text: "captured"},
						},
						NewFileIDToolResultImage("file-api-image"),
					},
				}),
			},
		},
	}
	tool := &schema.ToolInfo{Name: "next_tool", Desc: "continue"}
	choice := &schema.AgenticToolChoice{
		Type: schema.ToolChoiceForced,
		Forced: &schema.AgenticForcedToolChoice{Tools: []*schema.AllowedTool{
			{FunctionName: "next_tool"},
		}},
	}
	out, err := m.Generate(
		context.Background(),
		input,
		model.WithTools([]*schema.ToolInfo{tool}),
		model.WithAgenticToolChoice(choice),
		WithReasoningEffort(ReasoningEffortMax),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got := requestBody["model"]; got != VisionModel {
		t.Fatalf("model = %#v", got)
	}
	reasoning, ok := requestBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "max" {
		t.Fatalf("reasoning = %#v", requestBody["reasoning"])
	}
	tools, ok := requestBody["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0].(map[string]any)["name"] != "next_tool" {
		t.Fatalf("tools = %#v", requestBody["tools"])
	}
	toolChoice, ok := requestBody["tool_choice"].(map[string]any)
	if !ok || toolChoice["type"] != "function" || toolChoice["name"] != "next_tool" {
		t.Fatalf("tool_choice = %#v", requestBody["tool_choice"])
	}
	items, ok := requestBody["input"].([]any)
	if !ok || len(items) != 6 {
		t.Fatalf("input items = %#v", requestBody["input"])
	}
	userContent := items[1].(map[string]any)["content"].([]any)
	image := userContent[1].(map[string]any)
	if image["type"] != "input_image" || image["image_url"] != "data:image/png;base64,aGVsbG8=" || image["detail"] != "original" {
		t.Fatalf("user image = %#v", image)
	}
	toolOutput := items[5].(map[string]any)
	outputBlocks := toolOutput["output"].([]any)
	fileImage := outputBlocks[1].(map[string]any)
	if fileImage["type"] != "input_image" || fileImage["file_id"] != "file-api-image" {
		t.Fatalf("tool result image = %#v", fileImage)
	}

	if out.Role != schema.AgenticRoleTypeAssistant || len(out.ContentBlocks) != 3 {
		t.Fatalf("output = %#v", out)
	}
	if got := out.ContentBlocks[0].Reasoning.Text; got != "think" {
		t.Errorf("reasoning = %q", got)
	}
	if got := out.ContentBlocks[1].AssistantGenText.Text; got != "answer" {
		t.Errorf("text = %q", got)
	}
	call := out.ContentBlocks[2].FunctionToolCall
	if call == nil || call.CallID != "call-2" || call.Name != "next_tool" || call.Arguments != `{"q":"x"}` {
		t.Errorf("tool call = %#v", call)
	}
	if out.ResponseMeta == nil || out.ResponseMeta.TokenUsage == nil {
		t.Fatalf("response meta = %#v", out.ResponseMeta)
	}
	usage := out.ResponseMeta.TokenUsage
	if usage.PromptTokens != 20 || usage.PromptTokenDetails.CachedTokens != 7 || usage.CompletionTokens != 11 || usage.CompletionTokensDetails.ReasoningTokens != 3 || usage.TotalTokens != 31 {
		t.Errorf("usage = %#v", usage)
	}
	ext, ok := out.ResponseMeta.Extension.(*ResponseMetaExtension)
	if !ok || ext.ResponseID != "resp-1" || ext.Status != ResponseStatusCompleted || ext.FinishReason != "tool_calls" {
		t.Errorf("response extension = %#v", out.ResponseMeta.Extension)
	}
}

func TestStreamParsesSemanticEventsKeepAliveAndTerminalUsage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["stream"] != true {
			t.Fatalf("stream = %#v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, strings.Join([]string{
			": keep-alive\n\n",
			"event: response.created\n",
			`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp-stream","object":"response","status":"in_progress","model":"deepseek-v4-flash"}}` + "\n\n",
			"event: response.output_item.added\n",
			`data: {"type":"response.output_item.added","sequence_number":1,"output_index":2,"item":{"type":"function_call","id":"fc-1","status":"in_progress","call_id":"call-1","name":"lookup","arguments":""}}` + "\n\n",
			"event: response.reasoning_text.delta\n",
			`data: {"type":"response.reasoning_text.delta","sequence_number":2,"item_id":"rs-1","output_index":0,"content_index":0,"delta":"think "}` + "\n\n",
			"event: response.output_text.delta\n",
			`data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg-1","output_index":1,"content_index":0,"delta":"answer"}` + "\n\n",
			"event: response.function_call_arguments.delta\n",
			`data: {"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc-1","output_index":2,"delta":"{\"q\":"}` + "\n\n",
			": keep-alive\n\n",
			"event: response.function_call_arguments.delta\n",
			`data: {"type":"response.function_call_arguments.delta","sequence_number":5,"item_id":"fc-1","output_index":2,"delta":"\"x\"}"}` + "\n\n",
			"event: response.completed\n",
			`data: {"type":"response.completed","sequence_number":6,"response":{"id":"resp-stream","object":"response","status":"completed","model":"deepseek-v4-flash","output":[{"type":"function_call","id":"fc-1","status":"completed","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":9,"input_tokens_details":{"cached_tokens":4},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":14}}}` + "\n\n",
		}, ""))
	}))
	defer server.Close()

	m, err := New(context.Background(), &Config{
		APIKey:  "fixture-key",
		BaseURL: server.URL,
		Model:   "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sr, err := m.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	var reasoning, text, args string
	var callID, callName string
	var terminal *ResponseMetaExtension
	var terminalUsage *schema.TokenUsage
	for {
		chunk, recvErr := sr.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		if chunk.ResponseMeta != nil {
			terminal, _ = chunk.ResponseMeta.Extension.(*ResponseMetaExtension)
			terminalUsage = chunk.ResponseMeta.TokenUsage
			continue
		}
		for _, block := range chunk.ContentBlocks {
			switch block.Type {
			case schema.ContentBlockTypeReasoning:
				reasoning += block.Reasoning.Text
			case schema.ContentBlockTypeAssistantGenText:
				text += block.AssistantGenText.Text
			case schema.ContentBlockTypeFunctionToolCall:
				callID = block.FunctionToolCall.CallID
				callName = block.FunctionToolCall.Name
				args += block.FunctionToolCall.Arguments
			}
		}
	}

	if reasoning != "think " || text != "answer" {
		t.Fatalf("reasoning=%q text=%q", reasoning, text)
	}
	if callID != "call-1" || callName != "lookup" || args != `{"q":"x"}` {
		t.Fatalf("tool call id=%q name=%q args=%q", callID, callName, args)
	}
	if terminal == nil || terminal.ResponseID != "resp-stream" || terminal.FinishReason != "tool_calls" {
		t.Fatalf("terminal = %#v", terminal)
	}
	if terminalUsage == nil || terminalUsage.PromptTokens != 9 || terminalUsage.PromptTokenDetails.CachedTokens != 4 || terminalUsage.CompletionTokens != 5 || terminalUsage.CompletionTokensDetails.ReasoningTokens != 2 || terminalUsage.TotalTokens != 14 {
		t.Fatalf("terminal usage = %#v", terminalUsage)
	}
}

func TestGenerateReturnsTypedBoundedAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"server_busy","message":"busy super-secret-key"},"private":"must-not-leak"}`)
	}))
	defer server.Close()

	m, err := New(context.Background(), &Config{
		APIKey:  "super-secret-key",
		BaseURL: server.URL,
		Model:   "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = m.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable || apiErr.Code != "server_busy" || apiErr.Message != "busy [redacted]" {
		t.Fatalf("api error = %#v", apiErr)
	}
	if apiErr.HTTPStatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("HTTPStatusCode() = %d", apiErr.HTTPStatusCode())
	}
	formatted := err.Error()
	for _, forbidden := range []string{"super-secret-key", "must-not-leak", server.URL} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("error leaked %q: %s", forbidden, formatted)
		}
	}
	if !strings.Contains(formatted, "HTTP 503") || !strings.Contains(formatted, "server_busy") {
		t.Fatalf("error lacks actionable status: %s", formatted)
	}
}

func TestImageInputFailsBeforeDispatchForNonVisionModel(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	m, err := New(context.Background(), &Config{
		APIKey:  "fixture-key",
		BaseURL: server.URL,
		Model:   "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = m.Generate(context.Background(), []*schema.AgenticMessage{
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputImage{URL: "https://example.com/image.png"}),
			},
		},
	})
	if err == nil {
		t.Fatal("expected conversion error")
	}
	var conversionErr *ConversionError
	if !errors.As(err, &conversionErr) || conversionErr.ReasonCode != "image_model_unsupported" {
		t.Fatalf("error = %T %#v", err, err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}

func TestStreamFailedEventReturnsTypedError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.failed\n"+
			`data: {"type":"response.failed","sequence_number":1,"response":{"id":"resp-failed","object":"response","status":"failed","model":"deepseek-v4-flash","output":[],"error":{"code":"server_busy","message":"busy"}}}`+
			"\n\n")
	}))
	defer server.Close()

	m, err := New(context.Background(), &Config{
		APIKey:  "fixture-key",
		BaseURL: server.URL,
		Model:   "deepseek-v4-flash",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sr, err := m.Stream(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("hello")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()
	_, err = sr.Recv()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "server_busy" || apiErr.Message != "busy" {
		t.Fatalf("stream error = %T %#v", err, err)
	}
}
