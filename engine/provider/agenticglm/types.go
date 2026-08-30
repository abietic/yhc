// Package agenticglm implements GLM-5.3-Flash's native Chat Completion API as
// an Eino AgenticModel. The package owns the provider wire contract and does
// not route requests through an OpenAI compatibility client.
package agenticglm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	// DefaultBaseURL is the domestic Zhipu AI API root.
	DefaultBaseURL = "https://open.bigmodel.cn/api/paas/v4"
	// ModelGLM53Flash is the exact GLM model whose native multimodal and
	// preserved-thinking contract is implemented by this package.
	ModelGLM53Flash = "glm-5.3-flash"

	defaultMaxSSEEventBytes = 8 << 20
	maxResponseBytes        = 32 << 20
	maxRequestBytes         = 64 << 20
	maxErrorBytes           = 64 << 10
	maxInlineImageBytes     = 5 << 20
	maxInlineFileBytes      = 50 << 20
	maxImagesPerRequest     = 50
	maxFilesPerRequest      = 50
	maxToolsPerRequest      = 128
	fileIDExtraKey          = "_agenticglm_file_id"
)

// ReasoningEffort is GLM-5.3-Flash's reasoning_effort value.
type ReasoningEffort string

const (
	ReasoningEffortLow  ReasoningEffort = "low"
	ReasoningEffortHigh ReasoningEffort = "high"
	ReasoningEffortMax  ReasoningEffort = "max"
)

// Config follows the Eino Ext provider convention while exposing the GLM
// fields that cannot be represented by generic model options.
type Config struct {
	APIKey string
	// BaseURL is an API root such as https://open.bigmodel.cn/api/paas/v4.
	// The model appends /chat/completions while preserving a path prefix.
	BaseURL string
	Model   string

	// HTTPClient takes precedence over Timeout when supplied.
	HTTPClient *http.Client
	Timeout    time.Duration

	MaxTokens   *int
	Temperature *float32
	TopP        *float32
	Stop        []string
	DoSample    *bool

	ReasoningEffort ReasoningEffort
	// ClearThinking controls whether the provider removes historical
	// reasoning_content. Nil selects false, the GLM-5.3-Flash recommendation
	// for preserved thinking in agentic tool loops.
	ClearThinking *bool
	// ToolStream controls incremental tool arguments for streaming calls. Nil
	// enables it whenever tools are present, as recommended by the model guide.
	ToolStream *bool
	RequestID  string
	UserID     string

	// MaxSSEEventBytes bounds one data event. Zero selects an 8 MiB default.
	MaxSSEEventBytes int
}

type callOptions struct {
	reasoningEffort ReasoningEffort
	clearThinking   *bool
	toolStream      *bool
	requestID       *string
	userID          *string
	doSample        *bool
}

// WithReasoningEffort sets GLM-5.3-Flash reasoning_effort for one call.
func WithReasoningEffort(effort ReasoningEffort) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.reasoningEffort = effort
	})
}

// WithClearThinking controls historical reasoning preservation for one call.
func WithClearThinking(clear bool) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.clearThinking = boolPtr(clear)
	})
}

// WithToolStream controls incremental tool-argument output for one call.
func WithToolStream(enabled bool) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.toolStream = boolPtr(enabled)
	})
}

// WithRequestID sets the provider request correlation ID for one call.
func WithRequestID(requestID string) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.requestID = stringPtr(requestID)
	})
}

// WithUserID sets the privacy-sensitive end-user isolation value for one call.
// Callers must not place PII in this value.
func WithUserID(userID string) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.userID = stringPtr(userID)
	})
}

// WithDoSample controls GLM sampling for one call.
func WithDoSample(enabled bool) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.doSample = boolPtr(enabled)
	})
}

// NewFileIDBlock creates a user file block backed by Zhipu's Files API.
func NewFileIDBlock(fileID, filename string) *schema.ContentBlock {
	return &schema.ContentBlock{
		Type:          schema.ContentBlockTypeUserInputFile,
		UserInputFile: &schema.UserInputFile{Name: filename},
		Extra:         map[string]any{fileIDExtraKey: fileID},
	}
}

// ResponseMetaExtension retains provider terminal metadata without exposing a
// raw response body.
type ResponseMetaExtension struct {
	ResponseID   string `json:"response_id,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	Model        string `json:"model,omitempty"`
}

// APIError is a bounded GLM API error.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	if e == nil {
		return "agenticglm: API error"
	}
	parts := []string{"agenticglm: GLM Chat Completion API error"}
	if e.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.StatusCode))
	}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, ": ")
}

// HTTPStatusCode exposes the provider status to retry classification.
func (e *APIError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

// ConversionError identifies a local request rejection without formatting
// user-controlled content.
type ConversionError struct {
	MessageIndex int
	BlockIndex   int
	ReasonCode   string
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf(
		"agenticglm: input conversion failed: message=%d block=%d reason=%s",
		e.MessageIndex,
		e.BlockIndex,
		e.ReasonCode,
	)
}

// ProtocolError identifies a bounded malformed GLM response or SSE stream.
type ProtocolError struct {
	ReasonCode string
}

func (e *ProtocolError) Error() string {
	return "agenticglm: invalid GLM Chat Completion protocol: " + e.ReasonCode
}

type transportError struct{ err error }

func (e *transportError) Error() string { return "agenticglm: GLM transport failed" }
func (e *transportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	Stream          bool            `json:"stream"`
	ToolStream      *bool           `json:"tool_stream,omitempty"`
	Thinking        thinkingConfig  `json:"thinking"`
	ReasoningEffort ReasoningEffort `json:"reasoning_effort,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	Temperature     *float32        `json:"temperature,omitempty"`
	TopP            *float32        `json:"top_p,omitempty"`
	Stop            []string        `json:"stop,omitempty"`
	DoSample        *bool           `json:"do_sample,omitempty"`
	Tools           []chatTool      `json:"tools,omitempty"`
	RequestID       string          `json:"request_id,omitempty"`
	UserID          string          `json:"user_id,omitempty"`
}

type thinkingConfig struct {
	Type          string `json:"type"`
	ClearThinking bool   `json:"clear_thinking"`
}

type chatMessage struct {
	Role             string         `json:"role"`
	Content          any            `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type multimodalPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *mediaURL    `json:"image_url,omitempty"`
	VideoURL *mediaURL    `json:"video_url,omitempty"`
	File     *fileContent `json:"file,omitempty"`
}

type mediaURL struct {
	URL string `json:"url"`
}

type fileContent struct {
	FileID   string `json:"file_id,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatResponse struct {
	ID        string         `json:"id"`
	RequestID string         `json:"request_id"`
	Model     string         `json:"model"`
	Choices   []chatChoice   `json:"choices"`
	Usage     *responseUsage `json:"usage"`
}

type chatChoice struct {
	Index        int       `json:"index"`
	Message      chatReply `json:"message"`
	Delta        chatReply `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatReply struct {
	Role             string         `json:"role"`
	Content          *string        `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        []chatToolCall `json:"tool_calls"`
}

type responseUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func conversionError(messageIndex, blockIndex int, reason string) *ConversionError {
	return &ConversionError{MessageIndex: messageIndex, BlockIndex: blockIndex, ReasonCode: reason}
}

func boolPtr(value bool) *bool       { return &value }
func stringPtr(value string) *string { return &value }
