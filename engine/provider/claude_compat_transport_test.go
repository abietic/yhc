package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticclaude"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClaudeCompatibilityTransportRewritesOnlyDeepSeekCustomTools(t *testing.T) {
	t.Parallel()

	const input = `{"model":"haiku","metadata":{"type":"custom"},"messages":[{"role":"user","content":[{"type":"custom","text":"hello"}]}],"tools":[{"type":"custom","name":"Read","description":"read","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"search"}]}`
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "retained")
	var captured *http.Request
	transport := newDeepSeekAnthropicTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    request,
		}, nil
	}))
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.deepseek.com/anthropic/v1/messages",
		strings.NewReader(input),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Fixture", "retained")

	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close() //nolint:errcheck
	if captured == nil {
		t.Fatal("delegate request was not captured")
	}
	if captured.Context().Value(contextKey{}) != "retained" {
		t.Fatal("request context was not retained")
	}
	if captured.Header.Get("X-Fixture") != "retained" {
		t.Fatal("request headers were not retained")
	}
	body, err := io.ReadAll(captured.Body)
	if err != nil {
		t.Fatal(err)
	}
	if captured.ContentLength != int64(len(body)) {
		t.Fatalf("content length = %d, want %d", captured.ContentLength, len(body))
	}
	got := string(body)
	for _, retained := range []string{
		`"model":"claude-haiku"`,
		`"metadata":{"type":"custom"}`,
		`"content":[{"type":"custom","text":"hello"}]`,
		`"name":"Read"`,
		`"description":"read"`,
		`"input_schema":{"type":"object"}`,
		`"type":"web_search_20250305"`,
	} {
		if !strings.Contains(got, retained) {
			t.Fatalf("rewritten request does not retain %s: %s", retained, got)
		}
	}
	if strings.Contains(got, `{"type":"custom","name":"Read"`) ||
		strings.Contains(got, `"name":"Read","type":"custom"`) {
		t.Fatalf("custom function-tool discriminator was retained: %s", got)
	}
}

func TestClaudeCompatibilityTransportRewritesAgenticClaudeToolPayload(t *testing.T) {
	t.Parallel()

	var captured []byte
	client := &http.Client{Transport: newDeepSeekAnthropicTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		captured, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_fixture","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
			Request: request,
		}, nil
	}))}
	chatModel, err := agenticclaude.New(t.Context(), &agenticclaude.Config{
		HTTPClient: client,
		BaseURL:    deepSeekAnthropicBaseURL,
		APIKey:     "fixture-key",
		Model:      "haiku",
		MaxTokens:  128,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chatModel.Generate(
		t.Context(),
		[]*schema.AgenticMessage{schema.UserAgenticMessage("fixture prompt")},
		model.WithTools([]*schema.ToolInfo{{
			Name:        "Read",
			Desc:        "read a file",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
		}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Model string                       `json:"model"`
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(payload.Tools))
	}
	if payload.Model != "claude-haiku" {
		t.Fatalf("wire model = %q, want claude-haiku", payload.Model)
	}
	if _, exists := payload.Tools[0]["type"]; exists {
		t.Fatal("agenticclaude custom tool discriminator reached DeepSeek transport")
	}
	for _, field := range []string{"name", "description", "input_schema"} {
		if _, exists := payload.Tools[0][field]; !exists {
			t.Fatalf("tool field %q was not retained", field)
		}
	}
}

func TestClaudeCompatibilityTransportLeavesOtherRequestsByteExact(t *testing.T) {
	t.Parallel()

	const withTools = ` { "tools" : [ { "type" : "custom", "name" : "Read" } ] } `
	tests := []struct {
		name   string
		method string
		url    string
		body   string
	}{
		{name: "official Anthropic", method: http.MethodPost, url: "https://api.anthropic.com/v1/messages", body: withTools},
		{name: "other compatible endpoint", method: http.MethodPost, url: "https://proxy.example/v1/messages", body: withTools},
		{name: "DeepSeek non-message path", method: http.MethodPost, url: "https://api.deepseek.com/anthropic/v1/complete", body: withTools},
		{name: "DeepSeek non-POST", method: http.MethodGet, url: "https://api.deepseek.com/anthropic/v1/messages", body: withTools},
		{name: "DeepSeek no tools or bare model", method: http.MethodPost, url: "https://api.deepseek.com/anthropic/v1/messages", body: ` { "model" : "claude-haiku", "messages" : [] } `},
		{name: "DeepSeek non-custom tool", method: http.MethodPost, url: "https://api.deepseek.com/anthropic/v1/messages", body: ` { "tools" : [ { "type" : "CUSTOM", "name" : "Read" } ] } `},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var captured string
			transport := newDeepSeekAnthropicTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				captured = string(body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("ok")),
					Request:    request,
				}, nil
			}))
			request, err := http.NewRequest(test.method, test.url, strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close() //nolint:errcheck
			if captured != test.body {
				t.Fatalf("request body changed: got %q, want %q", captured, test.body)
			}
		})
	}
}

func TestClaudeCompatibilityTransportMapsOnlyExactBareDeepSeekModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "haiku", want: "claude-haiku"},
		{input: "sonnet", want: "claude-sonnet"},
		{input: "opus", want: "claude-opus"},
		{input: "claude-haiku", want: "claude-haiku"},
		{input: "deepseek-v4-flash", want: "deepseek-v4-flash"},
		{input: "Haiku", want: "Haiku"},
		{input: "haiku[1m]", want: "haiku[1m]"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			input := []byte(`{"model":"` + test.input + `","metadata":{"model":"haiku"},"messages":[]}`)
			got, changed, err := rewriteDeepSeekAnthropicRequest(input)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Model    string `json:"model"`
				Metadata struct {
					Model string `json:"model"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(got, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Model != test.want || payload.Metadata.Model != "haiku" {
				t.Fatalf("rewritten payload = %#v, want model %q and nested model unchanged", payload, test.want)
			}
			wantChanged := test.input != test.want
			if changed != wantChanged {
				t.Fatalf("changed = %v, want %v", changed, wantChanged)
			}
			if !wantChanged && string(got) != string(input) {
				t.Fatalf("unchanged model body changed: got %q want %q", got, input)
			}
		})
	}
}

func TestClaudeCompatibilityTransportFailsClosedForMalformedDeepSeekJSON(t *testing.T) {
	t.Parallel()

	const malformed = `{"tools":[{"type":"custom","name":"private-fixture"}`
	called := false
	transport := newDeepSeekAnthropicTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}))
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.deepseek.com/anthropic/v1/messages",
		strings.NewReader(malformed),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		defer response.Body.Close() //nolint:errcheck
	}
	if err == nil || !strings.Contains(err.Error(), "invalid DeepSeek Anthropic request JSON") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "private-fixture") {
		t.Fatalf("error leaked request body: %v", err)
	}
	if called {
		t.Fatal("malformed request reached the delegate transport")
	}
}

func TestClaudeCompatibilityTransportRedactsBodyReadFailure(t *testing.T) {
	t.Parallel()

	const fixtureSecret = "fixture-secret-must-not-leak"
	readErr := errors.New("fixture read failure: " + fixtureSecret)
	body := &failingReadCloser{err: readErr}
	called := false
	transport := newDeepSeekAnthropicTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}))
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.deepseek.com/anthropic/v1/messages",
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		defer response.Body.Close() //nolint:errcheck
	}
	if !errors.Is(err, errDeepSeekAnthropicRequestBodyRead) {
		t.Fatalf("error = %v, want redacted body-read failure", err)
	}
	if strings.Contains(err.Error(), fixtureSecret) {
		t.Fatalf("error leaked body-reader detail: %v", err)
	}
	if called {
		t.Fatal("failed body reached the delegate transport")
	}
	if !body.closed {
		t.Fatal("failed request body was not closed")
	}
}

func TestClaudeHTTPClientUsesCompatibilityOnlyForCanonicalDeepSeekEndpoint(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"https://api.deepseek.com/anthropic",
		" " + strings.ToUpper("https://api.deepseek.com:443") + "/anthropic/ ",
		"https://api.deepseek.com/a/../anthropic",
	} {
		if client := claudeHTTPClientForBaseURL(endpoint); client == nil {
			t.Fatalf("endpoint %q did not select compatibility transport", endpoint)
		}
	}
	for _, endpoint := range []string{
		"",
		"http://api.deepseek.com/anthropic",
		"https://api.deepseek.com",
		"https://api.deepseek.com/anthropic?dialect=custom",
		"https://api.anthropic.com",
		"https://proxy.example/anthropic",
	} {
		if client := claudeHTTPClientForBaseURL(endpoint); client != nil {
			t.Fatalf("endpoint %q unexpectedly selected compatibility transport", endpoint)
		}
	}
}

type failingReadCloser struct {
	err    error
	closed bool
}

func (reader *failingReadCloser) Read([]byte) (int, error) {
	return 0, reader.err
}

func (reader *failingReadCloser) Close() error {
	reader.closed = true
	return nil
}
