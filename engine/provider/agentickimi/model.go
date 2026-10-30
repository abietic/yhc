package agentickimi

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

var _ model.AgenticModel = (*Model)(nil)

// Model is an immutable, provider-specific K3 Responses client. It performs no
// hidden retry, session storage, server tool execution, or protocol fallback.
type Model struct {
	httpClient       *http.Client
	endpoint         string
	apiKey           string
	model            string
	maxTokens        int
	reasoningEffort  ReasoningEffort
	maxSSEEventBytes int
}

// New validates local configuration without contacting Kimi.
func New(_ context.Context, cfg *Config) (*Model, error) {
	if cfg == nil {
		return nil, conversionError(-1, -1, "config_nil")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, conversionError(-1, -1, "api_key_invalid")
	}
	modelID := strings.TrimSpace(cfg.Model)
	if modelID == "" {
		modelID = ModelK3
	}
	if modelID != ModelK3 {
		return nil, conversionError(-1, -1, "model_unsupported")
	}
	endpoint, err := responsesEndpoint(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultMaxOutputTokens
	}
	if maxTokens < 1 || maxTokens > MaxOutputTokens {
		return nil, conversionError(-1, -1, "max_output_tokens_invalid")
	}
	effort := cfg.ReasoningEffort
	if effort == "" {
		effort = ReasoningEffortMax
	}
	if !validReasoningEffort(effort) {
		return nil, conversionError(-1, -1, "reasoning_effort_invalid")
	}
	if cfg.Timeout < 0 {
		return nil, conversionError(-1, -1, "timeout_invalid")
	}
	limit := cfg.MaxSSEEventBytes
	if limit == 0 {
		limit = defaultMaxSSEEventBytes
	}
	if limit < 1024 || limit > maxResponseBytes {
		return nil, conversionError(-1, -1, "sse_limit_invalid")
	}
	client := &http.Client{Timeout: cfg.Timeout}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		client = &copied
	}
	// Redirects must not replay a provider request or forward its authorization.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Model{httpClient: client, endpoint: endpoint, apiKey: strings.TrimSpace(cfg.APIKey), model: modelID, maxTokens: maxTokens, reasoningEffort: effort, maxSSEEventBytes: limit}, nil
}

func (m *Model) GetType() string          { return "AgenticKimi" }
func (m *Model) IsCallbacksEnabled() bool { return true }

func (m *Model) request(input []*schema.AgenticMessage, stream bool, opts ...model.Option) (*wireRequest, *model.Options, error) {
	common := model.GetCommonOptions(&model.Options{Model: &m.model, MaxTokens: &m.maxTokens}, opts...)
	specific := model.GetImplSpecificOptions(&callOptions{reasoningEffort: m.reasoningEffort}, opts...)
	if common.Model == nil || *common.Model != ModelK3 {
		return nil, nil, conversionError(-1, -1, "model_unsupported")
	}
	if common.MaxTokens == nil || *common.MaxTokens < 1 || *common.MaxTokens > MaxOutputTokens {
		return nil, nil, conversionError(-1, -1, "max_output_tokens_invalid")
	}
	if common.Temperature != nil && *common.Temperature != 1 || common.TopP != nil && *common.TopP != 0.95 || len(common.Stop) > 0 {
		return nil, nil, conversionError(-1, -1, "sampling_unsupported")
	}
	if !validReasoningEffort(specific.reasoningEffort) {
		return nil, nil, conversionError(-1, -1, "reasoning_effort_invalid")
	}
	items, err := convertInput(input)
	if err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		return nil, nil, conversionError(-1, -1, "input_empty")
	}
	if common.ToolChoice != nil {
		if common.AgenticToolChoice != nil {
			return nil, nil, conversionError(-1, -1, "tool_choice_ambiguous")
		}
		common.AgenticToolChoice = &schema.AgenticToolChoice{Type: *common.ToolChoice}
	}
	tools, err := convertTools(common)
	if err != nil {
		return nil, nil, err
	}
	req := &wireRequest{Model: *common.Model, Input: items, Stream: stream, MaxOutputTokens: *common.MaxTokens, Tools: tools, PromptCacheOptions: cacheOptions{Mode: "implicit", TTL: "5m"}}
	req.Reasoning.Effort = specific.reasoningEffort
	if len(tools) > 0 {
		req.ToolChoice = "auto"
	}
	return req, common, nil
}

func (m *Model) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (out *schema.AgenticMessage, err error) {
	ctx = callbacks.EnsureRunInfo(ctx, m.GetType(), components.ComponentOfAgenticModel)
	req, common, err := m.request(input, false, opts...)
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
	stop := context.AfterFunc(ctx, func() { _ = response.Body.Close() })
	defer stop()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeAPIError(response)
	}
	raw, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return nil, readError(ctx, err)
	}
	parsed, err := decodeResponse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Status == "failed" {
		return nil, terminalAPIError(parsed)
	}
	if err := validateResponseTools(parsed, req.Tools); err != nil {
		return nil, err
	}
	out, err = responseMessage(parsed)
	if err != nil {
		return nil, err
	}
	callbacks.OnEnd(ctx, &model.AgenticCallbackOutput{Message: out, Config: config, TokenUsage: callbackUsage(out.ResponseMeta)})
	return out, nil
}

// Stream reads semantic SSE. The caller must cancel its context as well as
// close an abandoned reader, including when the HTTP connection is idle.
func (m *Model) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (out *schema.StreamReader[*schema.AgenticMessage], err error) {
	ctx = callbacks.EnsureRunInfo(ctx, m.GetType(), components.ComponentOfAgenticModel)
	req, common, err := m.request(input, true, opts...)
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
	response, err := m.do(ctx, body, true) //nolint:bodyclose // parser goroutine owns the successful SSE body
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, decodeAPIError(response)
	}
	mediaType, _, mimeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mimeErr != nil || mediaType != "text/event-stream" {
		_ = response.Body.Close()
		return nil, &ProtocolError{ReasonCode: "stream_content_type_invalid"}
	}
	reader, writer := schema.Pipe[*model.AgenticCallbackOutput](1)
	go func() {
		defer writer.Close()
		defer response.Body.Close()
		stop := context.AfterFunc(ctx, func() { _ = response.Body.Close() })
		defer stop()
		defer func() {
			if recover() != nil {
				e := &ProtocolError{ReasonCode: "stream_parser_panic"}
				callbacks.OnError(ctx, e)
				writer.Send(nil, e)
			}
		}()
		parseErr := parseResponseStream(response.Body, m.maxSSEEventBytes, req.Tools, func(msg *schema.AgenticMessage) bool {
			return writer.Send(&model.AgenticCallbackOutput{Message: msg, Config: config, TokenUsage: callbackUsage(msg.ResponseMeta)}, nil)
		})
		if parseErr != nil {
			parseErr = readError(ctx, parseErr)
			callbacks.OnError(ctx, parseErr)
			writer.Send(nil, parseErr)
		}
	}()
	_, stream := callbacks.OnEndWithStreamOutput(ctx, schema.StreamReaderWithConvert(reader, func(value *model.AgenticCallbackOutput) (callbacks.CallbackOutput, error) { return value, nil }))
	out = schema.StreamReaderWithConvert(stream, func(value callbacks.CallbackOutput) (*schema.AgenticMessage, error) {
		converted, ok := value.(*model.AgenticCallbackOutput)
		if !ok || converted == nil || converted.Message == nil {
			return nil, &ProtocolError{ReasonCode: "callback_output_invalid"}
		}
		return converted.Message, nil
	})
	return out, nil
}

func (m *Model) do(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &ProtocolError{ReasonCode: "request_invalid"}
	}
	request.GetBody = nil
	request.Header.Set("Authorization", "Bearer "+m.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		return nil, readError(ctx, err)
	}
	return response, nil
}

func responsesEndpoint(base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", conversionError(-1, -1, "base_url_invalid")
	}
	if !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/responses") {
		parsed.Path = path.Join(parsed.Path, "responses")
	}
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validReasoningEffort(e ReasoningEffort) bool {
	return e == ReasoningEffortLow || e == ReasoningEffortHigh || e == ReasoningEffortMax
}

func marshalRequest(req *wireRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, conversionError(-1, -1, "request_json_invalid")
	}
	if len(raw) > maxRequestBytes {
		return nil, conversionError(-1, -1, "request_too_large")
	}
	return raw, nil
}

func readBounded(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, &ProtocolError{ReasonCode: "response_too_large"}
	}
	return data, nil
}

func readError(ctx context.Context, err error) error {
	if cause := ctx.Err(); cause != nil {
		return &transportError{err: cause}
	}
	var protocol *ProtocolError
	if errors.As(err, &protocol) {
		return err
	}
	var api *APIError
	if errors.As(err, &api) {
		return err
	}
	return &transportError{err: err}
}

func decodeResponse(raw []byte) (*wireResponse, error) {
	var response wireResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, &ProtocolError{ReasonCode: "response_json_invalid"}
	}
	if err := validateWireResponse(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func safeCode(code string) string {
	switch code {
	case "context_length_exceeded", "rate_limit_reached", "rate_limit_exceeded", "invalid_authentication_error", "overloaded_error", "server_error", "invalid_request_error":
		return code
	default:
		return "provider_error"
	}
}

func decodeAPIError(response *http.Response) *APIError {
	raw, _ := readBounded(response.Body, 64<<10)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code := envelope.Error.Code
	if code == "" {
		code = envelope.Error.Type
	}
	return &APIError{StatusCode: response.StatusCode, Code: safeCode(code)}
}

func terminalAPIError(response *wireResponse) error {
	usage, err := responseTokenUsage(response)
	if err != nil {
		return err
	}
	code := "provider_error"
	if response.Error != nil {
		code = safeCode(response.Error.Code)
	}
	ttl := ""
	if response.PromptCacheOptions != nil {
		ttl = response.PromptCacheOptions.TTL
	}
	return &APIError{Code: code, finalUsage: usage, responseModel: response.Model, cacheTTL: ttl}
}

func callbackConfig(req *wireRequest) *model.AgenticConfig {
	return &model.AgenticConfig{Model: req.Model, MaxTokens: req.MaxOutputTokens, Temperature: 1, TopP: 0.95}
}

func callbackUsage(meta *schema.AgenticResponseMeta) *model.TokenUsage {
	if meta == nil || meta.TokenUsage == nil {
		return nil
	}
	u := meta.TokenUsage
	return &model.TokenUsage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens, PromptTokenDetails: model.PromptTokenDetails{CachedTokens: u.PromptTokenDetails.CachedTokens, CacheWriteTokens: u.PromptTokenDetails.CacheWriteTokens}, CompletionTokensDetails: model.CompletionTokensDetails{ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens}}
}

func validateResponseTools(response *wireResponse, tools []wireTool) error {
	if response.Status != "completed" {
		return nil
	}
	allowed := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		allowed[tool.Name] = struct{}{}
	}
	for _, item := range response.Output {
		if item.Type == "function_call" {
			if _, ok := allowed[item.Name]; !ok {
				return &ProtocolError{ReasonCode: "response_tool_not_declared"}
			}
		}
	}
	return nil
}
