package agenticglm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const modelType = "AgenticGLM"

var _ model.AgenticModel = (*Model)(nil)

// Model is an immutable Eino AgenticModel backed by GLM's native Chat
// Completion endpoint. Per-call tools and controls are carried by model.Option.
type Model struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string

	model            string
	maxTokens        *int
	temperature      *float32
	topP             *float32
	stop             []string
	doSample         *bool
	reasoningEffort  ReasoningEffort
	clearThinking    *bool
	toolStream       *bool
	requestID        string
	userID           string
	maxSSEEventBytes int
}

// New creates a dedicated GLM-5.3-Flash AgenticModel. It performs only local
// validation and never contacts the provider.
func New(_ context.Context, config *Config) (*Model, error) {
	if config == nil {
		return nil, conversionError(-1, -1, "config_nil")
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, conversionError(-1, -1, "api_key_missing")
	}
	modelID := strings.ToLower(strings.TrimSpace(config.Model))
	if modelID != ModelGLM53Flash {
		return nil, conversionError(-1, -1, "model_unsupported")
	}
	endpoint, err := chatCompletionsEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.Timeout < 0 {
		return nil, conversionError(-1, -1, "timeout_invalid")
	}
	if config.MaxTokens != nil && (*config.MaxTokens < 1 || *config.MaxTokens > 131072) {
		return nil, conversionError(-1, -1, "max_tokens_out_of_range")
	}
	if config.Temperature != nil && (*config.Temperature < 0 || *config.Temperature > 1) {
		return nil, conversionError(-1, -1, "temperature_out_of_range")
	}
	if config.TopP != nil && (*config.TopP < 0.01 || *config.TopP > 1) {
		return nil, conversionError(-1, -1, "top_p_out_of_range")
	}
	if len(config.Stop) > 4 {
		return nil, conversionError(-1, -1, "stop_count_exceeded")
	}
	effort := config.ReasoningEffort
	if effort == "" {
		effort = ReasoningEffortMax
	}
	if !validReasoningEffort(effort) {
		return nil, conversionError(-1, -1, "reasoning_effort_invalid")
	}
	if err := validateRequestID(strings.TrimSpace(config.RequestID)); err != nil {
		return nil, err
	}
	if err := validateUserID(strings.TrimSpace(config.UserID)); err != nil {
		return nil, err
	}
	maxEventBytes := config.MaxSSEEventBytes
	if maxEventBytes == 0 {
		maxEventBytes = defaultMaxSSEEventBytes
	}
	if maxEventBytes < 1024 || maxEventBytes > maxResponseBytes {
		return nil, conversionError(-1, -1, "max_sse_event_bytes_out_of_range")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: config.Timeout}
	}
	clearThinking := config.ClearThinking
	if clearThinking == nil {
		clearThinking = boolPtr(false)
	}
	return &Model{
		httpClient:       httpClient,
		endpoint:         endpoint,
		apiKey:           apiKey,
		model:            modelID,
		maxTokens:        cloneInt(config.MaxTokens),
		temperature:      cloneFloat32(config.Temperature),
		topP:             cloneFloat32(config.TopP),
		stop:             append([]string(nil), config.Stop...),
		doSample:         cloneBool(config.DoSample),
		reasoningEffort:  effort,
		clearThinking:    cloneBool(clearThinking),
		toolStream:       cloneBool(config.ToolStream),
		requestID:        strings.TrimSpace(config.RequestID),
		userID:           strings.TrimSpace(config.UserID),
		maxSSEEventBytes: maxEventBytes,
	}, nil
}

// Generate invokes GLM Chat Completion without streaming.
func (m *Model) Generate(
	ctx context.Context,
	input []*schema.AgenticMessage,
	opts ...model.Option,
) (out *schema.AgenticMessage, err error) {
	ctx = callbacks.EnsureRunInfo(ctx, m.GetType(), components.ComponentOfAgenticModel)
	common, specific := m.options(opts...)
	req, err := buildChatRequest(input, common, specific, false)
	if err != nil {
		return nil, err
	}
	body, err := marshalRequest(req)
	if err != nil {
		return nil, err
	}
	config := callbackConfig(req)
	ctx = callbacks.OnStart(ctx, &model.AgenticCallbackInput{Messages: input, Tools: common.Tools, Config: config})
	defer func() {
		if err != nil {
			callbacks.OnError(ctx, err)
		}
	}()

	response, err := m.do(ctx, body, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, m.decodeAPIError(response)
	}
	raw, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var object chatResponse
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, &ProtocolError{ReasonCode: "response_json_invalid"}
	}
	out, err = responseToAgentic(&object)
	if err != nil {
		return nil, err
	}
	callbacks.OnEnd(ctx, &model.AgenticCallbackOutput{
		Message: out, Config: config, TokenUsage: callbackTokenUsage(out.ResponseMeta),
	})
	return out, nil
}

// Stream invokes GLM Chat Completion with provider SSE enabled.
func (m *Model) Stream(
	ctx context.Context,
	input []*schema.AgenticMessage,
	opts ...model.Option,
) (out *schema.StreamReader[*schema.AgenticMessage], err error) {
	ctx = callbacks.EnsureRunInfo(ctx, m.GetType(), components.ComponentOfAgenticModel)
	common, specific := m.options(opts...)
	req, err := buildChatRequest(input, common, specific, true)
	if err != nil {
		return nil, err
	}
	body, err := marshalRequest(req)
	if err != nil {
		return nil, err
	}
	config := callbackConfig(req)
	ctx = callbacks.OnStart(ctx, &model.AgenticCallbackInput{Messages: input, Tools: common.Tools, Config: config})
	defer func() {
		if err != nil {
			callbacks.OnError(ctx, err)
		}
	}()

	response, err := m.do(ctx, body, true) //nolint:bodyclose // parser goroutine owns a successful stream body
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		return nil, m.decodeAPIError(response)
	}
	mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "text/event-stream" {
		response.Body.Close()
		return nil, &ProtocolError{ReasonCode: "stream_content_type_invalid"}
	}

	callbackReader, callbackWriter := schema.Pipe[*model.AgenticCallbackOutput](1)
	go func() {
		defer response.Body.Close()
		defer callbackWriter.Close()
		defer func() {
			if recover() != nil {
				callbackWriter.Send(nil, &ProtocolError{ReasonCode: "stream_parser_panic"})
			}
		}()
		parseErr := parseChatStream(response.Body, m.maxSSEEventBytes, func(message *schema.AgenticMessage) bool {
			return callbackWriter.Send(&model.AgenticCallbackOutput{
				Message: message, Config: config, TokenUsage: callbackTokenUsage(message.ResponseMeta),
			}, nil)
		})
		if parseErr != nil {
			callbackWriter.Send(nil, parseErr)
		}
	}()

	_, callbackStream := callbacks.OnEndWithStreamOutput(ctx, schema.StreamReaderWithConvert(
		callbackReader,
		func(src *model.AgenticCallbackOutput) (callbacks.CallbackOutput, error) { return src, nil },
	))
	out = schema.StreamReaderWithConvert(callbackStream, func(src callbacks.CallbackOutput) (*schema.AgenticMessage, error) {
		chunk, ok := src.(*model.AgenticCallbackOutput)
		if !ok || chunk == nil || chunk.Message == nil {
			return nil, &ProtocolError{ReasonCode: "callback_output_invalid"}
		}
		return chunk.Message, nil
	})
	return out, nil
}

// GetType identifies the provider transport used by Eino callbacks.
func (m *Model) GetType() string { return modelType }

// IsCallbacksEnabled reports that Generate and Stream emit Eino callbacks.
func (m *Model) IsCallbacksEnabled() bool { return true }

func (m *Model) options(opts ...model.Option) (*model.Options, *callOptions) {
	modelID := m.model
	common := model.GetCommonOptions(&model.Options{
		Model:       &modelID,
		MaxTokens:   cloneInt(m.maxTokens),
		Temperature: cloneFloat32(m.temperature),
		TopP:        cloneFloat32(m.topP),
		Stop:        append([]string(nil), m.stop...),
	}, opts...)
	specific := model.GetImplSpecificOptions(&callOptions{
		reasoningEffort: m.reasoningEffort,
		clearThinking:   cloneBool(m.clearThinking),
		toolStream:      cloneBool(m.toolStream),
		requestID:       optionalStringPointer(m.requestID),
		userID:          optionalStringPointer(m.userID),
		doSample:        cloneBool(m.doSample),
	}, opts...)
	return common, specific
}

func (m *Model) do(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "request_build_failed"}
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	response, err := m.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &transportError{err: ctxErr}
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			err = urlErr.Err
		}
		return nil, &transportError{err: err}
	}
	return response, nil
}

func chatCompletionsEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", conversionError(-1, -1, "base_url_invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", conversionError(-1, -1, "base_url_invalid")
	}
	cleanedPath := strings.TrimSuffix(parsed.Path, "/")
	if !strings.HasSuffix(cleanedPath, "/chat/completions") {
		cleanedPath = path.Join(cleanedPath, "chat/completions")
	}
	if !strings.HasPrefix(cleanedPath, "/") {
		cleanedPath = "/" + cleanedPath
	}
	parsed.Path = cleanedPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

func marshalRequest(req *chatRequest) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "request_json_invalid"}
	}
	if len(body) > maxRequestBytes {
		return nil, conversionError(-1, -1, "request_body_too_large")
	}
	return body, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "response_read_failed"}
	}
	if int64(len(raw)) > limit {
		return nil, &ProtocolError{ReasonCode: "response_body_too_large"}
	}
	return raw, nil
}

func (m *Model) decodeAPIError(response *http.Response) error {
	raw, readErr := readBounded(response.Body, maxErrorBytes)
	if readErr != nil {
		return &APIError{StatusCode: response.StatusCode, RequestID: responseRequestID(response.Header)}
	}
	var envelope struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
			Message string          `json:"message"`
		} `json:"error"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code := rawScalar(envelope.Error.Code)
	if code == "" {
		code = envelope.Error.Type
	}
	if code == "" {
		code = rawScalar(envelope.Code)
	}
	message := envelope.Error.Message
	if message == "" {
		message = envelope.Message
	}
	message = redactExact(message, m.apiKey, m.endpoint)
	return &APIError{
		StatusCode: response.StatusCode,
		Code:       boundedText(code, 128),
		Message:    boundedText(message, 1024),
		RequestID:  responseRequestID(response.Header),
	}
}

func redactExact(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func responseRequestID(header http.Header) string {
	for _, key := range []string{"x-request-id", "request-id", "cf-ray"} {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return boundedText(value, 256)
		}
	}
	return ""
}

func callbackConfig(req *chatRequest) *model.AgenticConfig {
	config := &model.AgenticConfig{Model: req.Model}
	if req.MaxTokens != nil {
		config.MaxTokens = *req.MaxTokens
	}
	if req.Temperature != nil {
		config.Temperature = *req.Temperature
	}
	if req.TopP != nil {
		config.TopP = *req.TopP
	}
	return config
}

func callbackTokenUsage(meta *schema.AgenticResponseMeta) *model.TokenUsage {
	if meta == nil || meta.TokenUsage == nil {
		return nil
	}
	return &model.TokenUsage{
		PromptTokens: meta.TokenUsage.PromptTokens,
		PromptTokenDetails: model.PromptTokenDetails{
			CachedTokens: meta.TokenUsage.PromptTokenDetails.CachedTokens,
		},
		CompletionTokens: meta.TokenUsage.CompletionTokens,
		CompletionTokensDetails: model.CompletionTokensDetails{
			ReasoningTokens: meta.TokenUsage.CompletionTokensDetails.ReasoningTokens,
		},
		TotalTokens: meta.TokenUsage.TotalTokens,
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func optionalStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return stringPtr(value)
}
