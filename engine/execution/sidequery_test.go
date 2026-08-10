package execution

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type sideQueryCaptureModel struct {
	streamMessages []*schema.Message
	streamOptions  []model.Option
	boundTools     []*schema.ToolInfo
	response       []*schema.Message
}

func (m *sideQueryCaptureModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *sideQueryCaptureModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamMessages = append([]*schema.Message(nil), input...)
	m.streamOptions = append([]model.Option(nil), opts...)
	return schema.StreamReaderFromArray(m.response), nil
}

func (m *sideQueryCaptureModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.boundTools = append([]*schema.ToolInfo(nil), tools...)
	return m, nil
}

func TestSideQueryReturnsMergedAssistantMessageAndNamedToolChoice(t *testing.T) {
	chatModel := &sideQueryCaptureModel{response: []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_explain_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name: "explain_command",
				},
			}},
		},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_explain_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "explain_command",
					Arguments: `{"riskLevel":"LOW","explanation":"Lists the working directory","reasoning":"I need to inspect the repo layout","risk":"May expose filenames"}`,
				},
			}},
		},
	}}

	maxTokens := 321
	msg, err := SideQuery(context.Background(), chatModel, SideQueryOptions{
		SystemPrompt:    "Classify the command.",
		Messages:        []*schema.Message{{Role: schema.User, Content: "Explain pwd"}},
		Tools:           []*schema.ToolInfo{{Name: "explain_command"}},
		Model:           "mini-model",
		ForcedToolName:  " explain_command ",
		MaxOutputTokens: &maxTokens,
		QuerySource:     "permission_explainer",
	})
	if err != nil {
		t.Fatalf("SideQuery returned error: %v", err)
		return
	}
	if msg == nil {
		t.Fatal("expected assistant message")
		return
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected one merged tool call, got %#v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Function.Name != "explain_command" {
		t.Fatalf("expected explain_command tool call, got %#v", msg.ToolCalls[0])
	}
	if !strings.Contains(msg.ToolCalls[0].Function.Arguments, `"riskLevel":"LOW"`) {
		t.Fatalf("expected merged tool args, got %q", msg.ToolCalls[0].Function.Arguments)
	}
	if len(chatModel.boundTools) != 1 || chatModel.boundTools[0].Name != "explain_command" {
		t.Fatalf("expected tool binding before streaming, got %#v", chatModel.boundTools)
	}
	if len(chatModel.streamMessages) != 2 {
		t.Fatalf("expected system prompt plus user message, got %#v", chatModel.streamMessages)
	}
	if chatModel.streamMessages[0].Role != schema.System || chatModel.streamMessages[0].Content != "Classify the command." {
		t.Fatalf("unexpected system prompt message: %#v", chatModel.streamMessages[0])
	}

	common := model.GetCommonOptions(nil, chatModel.streamOptions...)
	if common.Model == nil || *common.Model != "mini-model" {
		t.Fatalf("expected side query model override, got %#v", common.Model)
		return
	}
	if common.MaxTokens == nil || *common.MaxTokens != 321 {
		t.Fatalf("expected side query max tokens, got %#v", common.MaxTokens)
		return
	}
	if common.ToolChoice == nil || *common.ToolChoice != schema.ToolChoiceForced {
		t.Fatalf("expected forced tool choice, got %#v", common.ToolChoice)
		return
	}
	if len(common.AllowedToolNames) != 1 || common.AllowedToolNames[0] != "explain_command" {
		t.Fatalf("expected named tool constraint, got %#v", common.AllowedToolNames)
	}
}

func TestSideQueryReturnsErrorForWithheldAPIMessage(t *testing.T) {
	chatModel := &sideQueryCaptureModel{response: []*schema.Message{{
		Role:    schema.Assistant,
		Content: "Prompt is too long",
		Extra: map[string]any{
			"api_error":  true,
			"error_type": "invalid_request",
		},
	}}}

	_, err := SideQuery(context.Background(), chatModel, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected withheld api error")
		return
	}
	if !strings.Contains(err.Error(), "invalid_request") || !strings.Contains(err.Error(), "Prompt is too long") {
		t.Fatalf("unexpected side query error: %v", err)
	}
}

// --- SideQueryWithRetry tests ---

// sideQueryRetryModel simulates transient errors then succeeds.
type sideQueryRetryModel struct {
	calls    int
	failFor  int    // number of initial calls that return transient error
	errType  string // "429" or "529"
	response []*schema.Message
}

func (m *sideQueryRetryModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *sideQueryRetryModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	if m.calls <= m.failFor {
		switch m.errType {
		case "529":
			return nil, fmt.Errorf("529 overloaded_error: server is overloaded")
		default:
			return nil, fmt.Errorf("429 rate_limit_error: too many requests")
		}
	}
	return schema.StreamReaderFromArray(m.response), nil
}

func (m *sideQueryRetryModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestSideQueryWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	mdl := &sideQueryRetryModel{
		failFor:  0,
		response: []*schema.Message{{Role: schema.Assistant, Content: "hello"}},
	}
	msg, err := SideQueryWithRetry(context.Background(), mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, &SideQueryRetryConfig{BaseDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if msg.Content != "hello" {
		t.Fatalf("expected 'hello', got %q", msg.Content)
	}
	if mdl.calls != 1 {
		t.Fatalf("expected 1 call, got %d", mdl.calls)
	}
}

func TestSideQueryWithRetry_RetriesTransientThenSucceeds(t *testing.T) {
	mdl := &sideQueryRetryModel{
		failFor:  2,
		errType:  "429",
		response: []*schema.Message{{Role: schema.Assistant, Content: "ok"}},
	}
	msg, err := SideQueryWithRetry(context.Background(), mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, &SideQueryRetryConfig{BaseDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if msg.Content != "ok" {
		t.Fatalf("expected 'ok', got %q", msg.Content)
	}
	if mdl.calls != 3 { // 2 failures + 1 success
		t.Fatalf("expected 3 calls, got %d", mdl.calls)
	}
}

func TestSideQueryWithRetry_MaxRetriesExhausted(t *testing.T) {
	mdl := &sideQueryRetryModel{
		failFor:  10,
		errType:  "429",
		response: []*schema.Message{{Role: schema.Assistant, Content: "ok"}},
	}
	_, err := SideQueryWithRetry(context.Background(), mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, &SideQueryRetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
		return
	}
	if !strings.Contains(err.Error(), "rate_limit_error") {
		t.Fatalf("expected rate limit error, got: %v", err)
	}
	// 1 initial + 2 retries = 3 total calls
	if mdl.calls != 3 {
		t.Fatalf("expected 3 calls (1 initial + 2 retries), got %d", mdl.calls)
	}
}

func TestSideQueryWithRetry_NonTransientErrorNotRetried(t *testing.T) {
	// Use a model that returns a non-transient error (no model provided = nil model)
	// Instead, use a model that returns a withheld error which is non-transient
	mdl := &sideQueryCaptureModel{response: []*schema.Message{{
		Role:    schema.Assistant,
		Content: "Bad request",
		Extra: map[string]any{
			"api_error":  true,
			"error_type": "invalid_request",
		},
	}}}

	_, err := SideQueryWithRetry(context.Background(), mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, &SideQueryRetryConfig{BaseDelay: time.Millisecond})
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("expected non-transient error, got: %v", err)
	}
}

func TestSideQueryWithRetry_ContextCancellation(t *testing.T) {
	mdl := &sideQueryRetryModel{
		failFor: 100,
		errType: "529",
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the retry wait select picks up cancellation
	cancel()

	_, err := SideQueryWithRetry(ctx, mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, &SideQueryRetryConfig{BaseDelay: time.Millisecond})
	if err == nil {
		t.Fatal("expected context cancellation error")
		return
	}
	if ctx.Err() == nil {
		t.Fatal("expected context to be cancelled")
		return
	}
}

func TestSideQueryWithRetry_DefaultConfig(t *testing.T) {
	// Passing nil retryConfig should use defaults (3 retries) and not panic.
	mdl := &sideQueryRetryModel{
		failFor:  0,
		response: []*schema.Message{{Role: schema.Assistant, Content: "ok"}},
	}
	msg, err := SideQueryWithRetry(context.Background(), mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if msg.Content != "ok" {
		t.Fatalf("expected 'ok', got %q", msg.Content)
	}
}

func TestSideQueryWithRetry_DefaultMaxRetries(t *testing.T) {
	// Default max retries is 3: 1 initial + 3 retries = 4 calls
	mdl := &sideQueryRetryModel{
		failFor:  10,
		errType:  "429",
		response: []*schema.Message{{Role: schema.Assistant, Content: "ok"}},
	}
	_, err := SideQueryWithRetry(context.Background(), mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, &SideQueryRetryConfig{BaseDelay: time.Millisecond})
	if err == nil {
		t.Fatal("expected error after exhausting default retries")
		return
	}
	if mdl.calls != 4 {
		t.Fatalf("expected 4 calls with default 3 retries, got %d", mdl.calls)
	}
}

func TestSideQueryWithRetry_529Errors(t *testing.T) {
	mdl := &sideQueryRetryModel{
		failFor:  1,
		errType:  "529",
		response: []*schema.Message{{Role: schema.Assistant, Content: "recovered"}},
	}
	msg, err := SideQueryWithRetry(context.Background(), mdl, SideQueryOptions{
		Messages: []*schema.Message{{Role: schema.User, Content: "hi"}},
	}, &SideQueryRetryConfig{BaseDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if msg.Content != "recovered" {
		t.Fatalf("expected 'recovered', got %q", msg.Content)
	}
	if mdl.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", mdl.calls)
	}
}
