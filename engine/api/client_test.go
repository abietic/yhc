package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClientDefaults(t *testing.T) {
	client := NewClient(ClientConfig{APIKey: "key", Model: "claude"})
	if client.config.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q", client.config.BaseURL)
	}
	if client.config.MaxRetries != DefaultMaxRetries {
		t.Fatalf("MaxRetries = %d", client.config.MaxRetries)
	}
	if client.config.Timeout != DefaultTimeout || client.httpClient.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %s http=%s", client.config.Timeout, client.httpClient.Timeout)
	}
}

func TestSendMessageBuildsRequestHeadersAndParsesResponse(t *testing.T) {
	var captured struct {
		Headers http.Header
		Body    MessageRequest
		Path    string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Headers = r.Header.Clone()
		captured.Path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Fatalf("decode request: %v", err)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"content":[{"type":"text","text":"hello"}],
			"model":"claude-test",
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":3}
		}`))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		APIKey:        "api-key",
		BaseURL:       server.URL + "/",
		Model:         "claude-default",
		MaxRetries:    1,
		BetaFeatures:  []string{"beta-a", "beta-b"},
		Organization:  "org-1",
		SkipCacheRead: true,
		ExtraHeaders:  map[string]string{"x-extra": "value", "x-app": "override"},
	})
	resp, err := client.SendMessage(context.Background(), &MessageRequest{
		Messages:  []APIMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
		Stream:    false,
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
		return
	}
	if captured.Path != messagesPath {
		t.Fatalf("path = %q", captured.Path)
	}
	if captured.Body.Model != "claude-default" || captured.Body.MaxTokens != 100 || len(captured.Body.Messages) != 1 {
		t.Fatalf("request body mismatch: %#v", captured.Body)
	}
	for key, want := range map[string]string{
		"Content-Type":              "application/json",
		"x-api-key":                 "api-key",
		"anthropic-version":         AnthropicVersion,
		"x-app":                     "override",
		"anthropic-beta":            "beta-a,beta-b",
		"anthropic-organization":    "org-1",
		"anthropic-skip-cache-read": "true",
		"x-extra":                   "value",
	} {
		if got := captured.Headers.Get(key); got != want {
			t.Fatalf("header %s = %q want %q", key, got, want)
		}
	}
	if captured.Headers.Get("x-client-request-id") == "" {
		t.Fatal("missing client request id")
	}
	if resp.ID != "msg_1" || resp.Content[0].Text != "hello" || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("response mismatch: %#v", resp)
	}
}

func TestSendMessageParsesAPIErrorAndDoesNotRetryNonRetryable(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("request-id", "req-1")
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`, http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{BaseURL: server.URL, MaxRetries: 3})
	_, err := client.SendMessage(context.Background(), &MessageRequest{Model: "claude", MaxTokens: 1})
	if err == nil {
		t.Fatal("expected API error")
		return
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T %v", err, err)
	}
	if apiErr.StatusCode != 400 || apiErr.Type != "invalid_request_error" || apiErr.Message != "bad request" || apiErr.RequestID != "req-1" || apiErr.Retryable {
		t.Fatalf("unexpected api error: %#v", apiErr)
	}
	if calls != 1 {
		t.Fatalf("non-retryable error should not retry, calls=%d", calls)
	}
	if !strings.Contains(apiErr.Error(), "req-1") {
		t.Fatalf("error string should include request id: %s", apiErr.Error())
	}
}

func TestSendMessageRetriesRetryableErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("request-id", "req-retry")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"try again"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_retry","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{BaseURL: server.URL, MaxRetries: 1})
	resp, err := client.SendMessage(context.Background(), &MessageRequest{Model: "claude", MaxTokens: 1})
	if err != nil {
		t.Fatalf("retry should eventually succeed: %v", err)
		return
	}
	if calls != 2 || resp.ID != "msg_retry" {
		t.Fatalf("unexpected retry result calls=%d resp=%#v", calls, resp)
	}
}

func TestSendMessageContextCancellation(t *testing.T) {
	client := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1", MaxRetries: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.SendMessage(ctx, &MessageRequest{Model: "claude", MaxTokens: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestErrorClassifiersAndRetryDelay(t *testing.T) {
	retryable := &APIError{StatusCode: 529, Type: "overloaded_error", Message: "busy", Retryable: true}
	if !IsRetryable(retryable) || !IsOverloaded(retryable) || IsRateLimited(retryable) {
		t.Fatalf("unexpected retryable classifier result")
	}
	rateLimited := &APIError{StatusCode: 429, Type: "rate_limit_error", Retryable: true}
	if !IsRateLimited(rateLimited) || !IsRetryable(rateLimited) {
		t.Fatalf("unexpected rate limit classifier result")
	}
	promptTooLong := &APIError{StatusCode: 400, Type: "invalid_request_error", Message: "prompt is too long"}
	if !IsPromptTooLong(promptTooLong) {
		t.Fatal("prompt-too-long message should be classified")
	}
	if IsRetryable(errors.New("plain")) || IsOverloaded(errors.New("plain")) || IsRateLimited(errors.New("plain")) || IsPromptTooLong(errors.New("plain")) {
		t.Fatal("plain errors should not match API classifiers")
	}
	if !classifyRetryable(500) || !classifyRetryable(502) || !classifyRetryable(503) || !classifyRetryable(529) || !classifyRetryable(429) || classifyRetryable(400) {
		t.Fatal("classifyRetryable status mapping mismatch")
	}
	for attempt := 1; attempt <= 8; attempt++ {
		delay := getRetryDelay(attempt)
		if delay <= 0 || delay > (maxDelayMS+maxDelayMS/4)*time.Millisecond {
			t.Fatalf("retry delay out of bounds for attempt %d: %s", attempt, delay)
		}
	}
}

func TestParseErrorResponseFallbacks(t *testing.T) {
	unknown := parseErrorResponse(503, []byte("not-json"), "req-x")
	if unknown.Type != "unknown_error" || unknown.Message != "not-json" || !unknown.Retryable || unknown.RequestID != "req-x" {
		t.Fatalf("unexpected unknown error: %#v", unknown)
	}
	overloaded := parseErrorResponse(400, []byte(`{"error":{"type":"overloaded_error","message":"busy"}}`), "")
	if !overloaded.Retryable || !IsOverloaded(overloaded) {
		t.Fatalf("overloaded type should force retryable: %#v", overloaded)
	}
}
