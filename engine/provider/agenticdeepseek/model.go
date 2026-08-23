package agenticdeepseek

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

const modelType = "AgenticDeepSeek"

var _ model.AgenticModel = (*Model)(nil)

// Model is an immutable Eino AgenticModel backed by DeepSeek's Responses API.
// Per-call tools and overrides are carried by model.Option, so one instance is
// safe to share across concurrent requests.
type Model struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string

	model            string
	maxOutputTokens  *int
	temperature      *float32
	topP             *float32
	topLogProbs      *int
	reasoningEffort  ReasoningEffort
	textFormat       *TextFormat
	userID           string
	maxSSEEventBytes int
}

// New creates a dedicated DeepSeek Responses AgenticModel. It performs only
// local validation and never contacts the provider.
func New(_ context.Context, config *Config) (*Model, error) {
	if config == nil {
		return nil, conversionError(-1, -1, "config_nil")
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, conversionError(-1, -1, "api_key_missing")
	}
	modelID := strings.TrimSpace(config.Model)
	if modelID == "" {
		return nil, conversionError(-1, -1, "model_missing")
	}
	endpoint, err := responsesEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	maxOutputTokens, err := configuredMaxOutputTokens(config.MaxTokens, config.MaxOutputTokens)
	if err != nil {
		return nil, err
	}
	if maxOutputTokens != nil && *maxOutputTokens <= 0 {
		return nil, conversionError(-1, -1, "max_output_tokens_invalid")
	}
	if config.Temperature != nil && (*config.Temperature < 0 || *config.Temperature > 2) {
		return nil, conversionError(-1, -1, "temperature_out_of_range")
	}
	if config.TopP != nil && (*config.TopP < 0 || *config.TopP > 1) {
		return nil, conversionError(-1, -1, "top_p_out_of_range")
	}
	if config.TopLogProbs != nil && (*config.TopLogProbs < 0 || *config.TopLogProbs > 20) {
		return nil, conversionError(-1, -1, "top_logprobs_out_of_range")
	}
	if config.ReasoningEffort != "" && !validReasoningEffort(config.ReasoningEffort) {
		return nil, conversionError(-1, -1, "reasoning_effort_invalid")
	}
	textFormat, err := configuredTextFormat(config.TextFormat, config.ResponseFormatType)
	if err != nil {
		return nil, err
	}
	if textFormat != nil {
		if _, err := convertTextFormat(textFormat); err != nil {
			return nil, err
		}
	}
	if len(config.Stop) > 0 || config.PresencePenalty != nil || config.FrequencyPenalty != nil || config.LogProbs != nil {
		return nil, conversionError(-1, -1, "chat_completions_config_unsupported")
	}
	if err := validateUserID(config.UserID); err != nil {
		return nil, err
	}
	if config.Timeout < 0 {
		return nil, conversionError(-1, -1, "timeout_invalid")
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
	return &Model{
		httpClient:       httpClient,
		endpoint:         endpoint,
		apiKey:           apiKey,
		model:            modelID,
		maxOutputTokens:  cloneInt(maxOutputTokens),
		temperature:      cloneFloat32(config.Temperature),
		topP:             cloneFloat32(config.TopP),
		topLogProbs:      cloneInt(config.TopLogProbs),
		reasoningEffort:  config.ReasoningEffort,
		textFormat:       cloneTextFormat(textFormat),
		userID:           config.UserID,
		maxSSEEventBytes: maxEventBytes,
	}, nil
}

// Generate invokes DeepSeek Responses without streaming.
func (m *Model) Generate(
	ctx context.Context,
	input []*schema.AgenticMessage,
	opts ...model.Option,
) (out *schema.AgenticMessage, err error) {
	ctx = callbacks.EnsureRunInfo(ctx, m.GetType(), components.ComponentOfAgenticModel)
	common, specific := m.options(opts...)
	req, err := buildResponseRequest(input, common, specific, false)
	if err != nil {
		return nil, err
	}
	body, err := marshalRequest(req)
	if err != nil {
		return nil, err
	}
	config := callbackConfig(req)
	ctx = callbacks.OnStart(ctx, &model.AgenticCallbackInput{
		Messages: input,
		Tools:    common.Tools,
		Config:   config,
	})
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
	var object responseObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, &ProtocolError{ReasonCode: "response_json_invalid"}
	}
	out, err = responseToAgentic(&object)
	if err != nil {
		return nil, err
	}
	callbacks.OnEnd(ctx, &model.AgenticCallbackOutput{
		Message:    out,
		Config:     config,
		TokenUsage: callbackTokenUsage(out.ResponseMeta),
	})
	return out, nil
}

// Stream invokes DeepSeek Responses with semantic SSE enabled.
func (m *Model) Stream(
	ctx context.Context,
	input []*schema.AgenticMessage,
	opts ...model.Option,
) (out *schema.StreamReader[*schema.AgenticMessage], err error) {
	ctx = callbacks.EnsureRunInfo(ctx, m.GetType(), components.ComponentOfAgenticModel)
	common, specific := m.options(opts...)
	req, err := buildResponseRequest(input, common, specific, true)
	if err != nil {
		return nil, err
	}
	body, err := marshalRequest(req)
	if err != nil {
		return nil, err
	}
	config := callbackConfig(req)
	ctx = callbacks.OnStart(ctx, &model.AgenticCallbackInput{
		Messages: input,
		Tools:    common.Tools,
		Config:   config,
	})
	defer func() {
		if err != nil {
			callbacks.OnError(ctx, err)
		}
	}()

	response, err := m.do(ctx, body, true) //nolint:bodyclose // parser goroutine owns and closes a successful stream body
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
		parseErr := parseResponseStream(response.Body, m.maxSSEEventBytes, func(message *schema.AgenticMessage) bool {
			return callbackWriter.Send(&model.AgenticCallbackOutput{
				Message:    message,
				Config:     config,
				TokenUsage: callbackTokenUsage(message.ResponseMeta),
			}, nil)
		})
		if parseErr != nil {
			callbackWriter.Send(nil, parseErr)
		}
	}()

	_, callbackStream := callbacks.OnEndWithStreamOutput(ctx, schema.StreamReaderWithConvert(
		callbackReader,
		func(src *model.AgenticCallbackOutput) (callbacks.CallbackOutput, error) {
			return src, nil
		},
	))
	out = schema.StreamReaderWithConvert(
		callbackStream,
		func(src callbacks.CallbackOutput) (*schema.AgenticMessage, error) {
			chunk, ok := src.(*model.AgenticCallbackOutput)
			if !ok || chunk == nil || chunk.Message == nil {
				return nil, &ProtocolError{ReasonCode: "callback_output_invalid"}
			}
			return chunk.Message, nil
		},
	)
	return out, nil
}

// GetType identifies the provider transport used by Eino callbacks.
func (m *Model) GetType() string {
	return modelType
}

// IsCallbacksEnabled reports that Generate and Stream emit Eino callbacks.
func (m *Model) IsCallbacksEnabled() bool {
	return true
}

func (m *Model) options(opts ...model.Option) (*model.Options, *callOptions) {
	modelID := m.model
	common := model.GetCommonOptions(&model.Options{
		Model:       &modelID,
		MaxTokens:   cloneInt(m.maxOutputTokens),
		Temperature: cloneFloat32(m.temperature),
		TopP:        cloneFloat32(m.topP),
	}, opts...)
	specific := model.GetImplSpecificOptions(&callOptions{
		reasoningEffort: m.reasoningEffort,
		textFormat:      cloneTextFormat(m.textFormat),
		userID:          stringPointer(m.userID),
		topLogProbs:     cloneInt(m.topLogProbs),
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

func responsesEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", conversionError(-1, -1, "base_url_invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", conversionError(-1, -1, "base_url_invalid")
	}
	cleanedPath := strings.TrimSuffix(parsed.Path, "/")
	if !strings.HasSuffix(cleanedPath, "/responses") {
		cleanedPath = path.Join(cleanedPath, "responses")
	}
	if !strings.HasPrefix(cleanedPath, "/") {
		cleanedPath = "/" + cleanedPath
	}
	parsed.Path = cleanedPath
	parsed.RawPath = ""
	return parsed.String(), nil
}

func marshalRequest(req *responseRequest) ([]byte, error) {
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
		return &APIError{StatusCode: response.StatusCode, RequestID: requestID(response.Header)}
	}
	var envelope struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code := rawScalar(envelope.Error.Code)
	if code == "" {
		code = envelope.Error.Type
	}
	message := redactExact(envelope.Error.Message, m.apiKey, m.endpoint)
	return &APIError{
		StatusCode: response.StatusCode,
		Code:       boundedText(code, 128),
		Message:    boundedText(message, 1024),
		RequestID:  requestID(response.Header),
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

func requestID(header http.Header) string {
	for _, key := range []string{"x-request-id", "request-id", "cf-ray"} {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return boundedText(value, 256)
		}
	}
	return ""
}

func callbackConfig(req *responseRequest) *model.AgenticConfig {
	config := &model.AgenticConfig{Model: req.Model}
	if req.MaxOutputTokens != nil {
		config.MaxTokens = *req.MaxOutputTokens
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

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat32(value *float32) *float32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func configuredMaxOutputTokens(legacy, exact *int) (*int, error) {
	if legacy != nil && exact != nil {
		return nil, conversionError(-1, -1, "max_tokens_ambiguous")
	}
	if exact != nil {
		return exact, nil
	}
	return legacy, nil
}

func configuredTextFormat(exact *TextFormat, legacy ResponseFormatType) (*TextFormat, error) {
	if exact != nil && legacy != "" {
		return nil, conversionError(-1, -1, "text_format_ambiguous")
	}
	if exact != nil {
		return cloneTextFormat(exact), nil
	}
	switch legacy {
	case "":
		return nil, nil
	case ResponseFormatTypeText:
		return &TextFormat{Type: TextFormatText}, nil
	case ResponseFormatTypeJSONObject:
		return &TextFormat{Type: TextFormatJSONObject}, nil
	default:
		return nil, conversionError(-1, -1, "response_format_type_invalid")
	}
}
