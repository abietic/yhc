package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	deepSeekAnthropicBaseURL     = "https://api.deepseek.com/anthropic"
	deepSeekAnthropicMessagesURL = deepSeekAnthropicBaseURL + "/v1/messages"
)

var errDeepSeekAnthropicRequestBodyRead = errors.New(
	"read DeepSeek Anthropic request body",
)

// claudeHTTPClientForBaseURL selects the narrow request dialect required by
// DeepSeek's documented Anthropic-compatible endpoint. Other Claude routes
// retain the upstream agenticclaude transport and wire format unchanged.
func claudeHTTPClientForBaseURL(rawBaseURL string) *http.Client {
	canonical, err := canonicalRouteEndpoint(rawBaseURL)
	if err != nil || canonical != deepSeekAnthropicBaseURL {
		return nil
	}
	return &http.Client{
		Transport: newDeepSeekAnthropicTransport(http.DefaultTransport),
	}
}

type deepSeekAnthropicTransport struct {
	delegate http.RoundTripper
}

func newDeepSeekAnthropicTransport(delegate http.RoundTripper) http.RoundTripper {
	if delegate == nil {
		delegate = http.DefaultTransport
	}
	return &deepSeekAnthropicTransport{delegate: delegate}
}

func (transport *deepSeekAnthropicTransport) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	if !isDeepSeekAnthropicMessagesRequest(request) || request.Body == nil {
		return transport.delegate.RoundTrip(request)
	}

	body, err := io.ReadAll(request.Body)
	_ = request.Body.Close()
	if err != nil {
		return nil, errDeepSeekAnthropicRequestBodyRead
	}
	rewritten, changed, err := rewriteDeepSeekAnthropicRequest(body)
	if err != nil {
		return nil, err
	}
	if !changed {
		rewritten = body
	}

	forward := request.Clone(request.Context())
	forward.Body = io.NopCloser(bytes.NewReader(rewritten))
	forward.ContentLength = int64(len(rewritten))
	forward.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(rewritten)), nil
	}
	forward.Header = request.Header.Clone()
	forward.Header.Del("Content-Length")
	return transport.delegate.RoundTrip(forward)
}

func isDeepSeekAnthropicMessagesRequest(request *http.Request) bool {
	if request == nil || request.URL == nil || request.Method != http.MethodPost {
		return false
	}
	canonical, err := canonicalRouteEndpoint(request.URL.String())
	return err == nil && canonical == deepSeekAnthropicMessagesURL
}

func rewriteDeepSeekAnthropicRequest(body []byte) ([]byte, bool, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, false, errors.New("invalid DeepSeek Anthropic request JSON")
	}
	changed := rewriteDeepSeekAnthropicModel(document)
	toolsJSON, ok := document["tools"]
	if !ok {
		return encodeDeepSeekAnthropicRequest(document, body, changed)
	}

	var toolObjects []map[string]json.RawMessage
	if err := json.Unmarshal(toolsJSON, &toolObjects); err != nil {
		return nil, false, errors.New("invalid DeepSeek Anthropic request JSON")
	}
	for _, tool := range toolObjects {
		rawType, exists := tool["type"]
		if !exists {
			continue
		}
		var toolType string
		if err := json.Unmarshal(rawType, &toolType); err != nil || toolType != "custom" {
			continue
		}
		delete(tool, "type")
		changed = true
	}
	rewrittenTools, err := json.Marshal(toolObjects)
	if err != nil {
		return nil, false, fmt.Errorf("encode DeepSeek Anthropic tools: %w", err)
	}
	document["tools"] = rewrittenTools
	return encodeDeepSeekAnthropicRequest(document, body, changed)
}

func rewriteDeepSeekAnthropicModel(document map[string]json.RawMessage) bool {
	rawModel, exists := document["model"]
	if !exists {
		return false
	}
	var modelName string
	if err := json.Unmarshal(rawModel, &modelName); err != nil {
		return false
	}
	mapped, supported := map[string]string{
		"haiku":  "claude-haiku",
		"sonnet": "claude-sonnet",
		"opus":   "claude-opus",
	}[modelName]
	if !supported {
		return false
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		return false
	}
	document["model"] = encoded
	return true
}

func encodeDeepSeekAnthropicRequest(
	document map[string]json.RawMessage,
	body []byte,
	changed bool,
) ([]byte, bool, error) {
	if !changed {
		return body, false, nil
	}
	rewritten, err := json.Marshal(document)
	if err != nil {
		return nil, false, fmt.Errorf("encode DeepSeek Anthropic request: %w", err)
	}
	return rewritten, true, nil
}
