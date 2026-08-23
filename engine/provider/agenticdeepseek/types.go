// Package agenticdeepseek implements DeepSeek's stateless Responses API as an
// Eino AgenticModel. It intentionally owns DeepSeek request, response, error,
// and semantic-SSE types instead of routing through an OpenAI compatibility
// client.
package agenticdeepseek

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
	// DefaultBaseURL is the official DeepSeek API origin.
	DefaultBaseURL = "https://api.deepseek.com"
	// VisionModel is the only official DeepSeek Responses model that consumes
	// image input rather than replacing it with placeholder text.
	VisionModel = "deepseek-v4-flash-vision-exp"

	defaultMaxSSEEventBytes = 16 << 20
	maxResponseBytes        = 32 << 20
	maxRequestBytes         = 48 << 20
	maxErrorBytes           = 64 << 10
	maxSingleImageBytes     = 32 << 20
	maxImagesPerRequest     = 600
	maxToolsPerRequest      = 128
	imageFileIDExtraKey     = "_agenticdeepseek_image_file_id"
)

// ReasoningEffort is DeepSeek's Responses reasoning.effort value.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
	ReasoningEffortMax     ReasoningEffort = "max"
)

// ResponseStatus is the terminal or in-progress DeepSeek Responses status.
type ResponseStatus string

const (
	ResponseStatusInProgress ResponseStatus = "in_progress"
	ResponseStatusCompleted  ResponseStatus = "completed"
	ResponseStatusIncomplete ResponseStatus = "incomplete"
	ResponseStatusFailed     ResponseStatus = "failed"
)

// TextFormatType controls DeepSeek Responses structured text output.
type TextFormatType string

const (
	TextFormatText       TextFormatType = "text"
	TextFormatJSONObject TextFormatType = "json_object"
	TextFormatJSONSchema TextFormatType = "json_schema"
)

// ResponseFormatType preserves the small eino-ext constructor surface while
// lowering it to Responses text.format. Prefer TextFormat for new code.
type ResponseFormatType string

const (
	ResponseFormatTypeText       ResponseFormatType = "text"
	ResponseFormatTypeJSONObject ResponseFormatType = "json_object"
)

// TextFormat is the typed Responses text.format payload. Schema is required
// only for json_schema.
type TextFormat struct {
	Type   TextFormatType
	Name   string
	Schema json.RawMessage
}

// Config configures one immutable DeepSeek Responses AgenticModel.
type Config struct {
	APIKey string
	// BaseURL is an API root such as https://api.deepseek.com. The model appends
	// /responses while preserving an existing path prefix.
	BaseURL string
	Model   string

	// HTTPClient takes precedence over Timeout when supplied.
	HTTPClient *http.Client
	Timeout    time.Duration

	// MaxTokens keeps source compatibility with eino-ext's Config. It maps to
	// Responses max_output_tokens. New code may use MaxOutputTokens instead;
	// setting both is rejected.
	MaxTokens          *int
	MaxOutputTokens    *int
	Temperature        *float32
	TopP               *float32
	TopLogProbs        *int
	ReasoningEffort    ReasoningEffort
	TextFormat         *TextFormat
	ResponseFormatType ResponseFormatType
	UserID             string

	// These Chat Completions fields remain only so constructor migrations fail
	// with a typed local error instead of a compile error. DeepSeek Responses
	// does not define them.
	Stop             []string
	PresencePenalty  *float32
	FrequencyPenalty *float32
	LogProbs         *bool

	// MaxSSEEventBytes bounds one semantic SSE event, including multiline data.
	// Zero selects a 16 MiB default.
	MaxSSEEventBytes int
}

type callOptions struct {
	reasoningEffort ReasoningEffort
	textFormat      *TextFormat
	userID          *string
	topLogProbs     *int
}

// WithReasoningEffort sets DeepSeek Responses reasoning.effort for one call.
func WithReasoningEffort(effort ReasoningEffort) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.reasoningEffort = effort
	})
}

// WithTextFormat sets the typed Responses text.format for one call.
func WithTextFormat(format TextFormat) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		copied := cloneTextFormat(&format)
		opts.textFormat = copied
	})
}

// WithUserID sets DeepSeek's privacy-sensitive end-user isolation key for one
// call. Callers must not place user PII in this value.
func WithUserID(userID string) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.userID = &userID
	})
}

// WithTopLogProbs sets the number of returned top log probabilities.
func WithTopLogProbs(count int) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		opts.topLogProbs = &count
	})
}

// NewFileIDImageBlock creates a user image block backed by DeepSeek Files API.
// The returned block is valid only for user/developer input on VisionModel.
func NewFileIDImageBlock(fileID string) *schema.ContentBlock {
	return &schema.ContentBlock{
		Type:           schema.ContentBlockTypeUserInputImage,
		UserInputImage: &schema.UserInputImage{},
		Extra:          map[string]any{imageFileIDExtraKey: fileID},
	}
}

// NewFileIDToolResultImage creates an image result block backed by DeepSeek
// Files API. Responses accepts this inside function_call_output.output.
func NewFileIDToolResultImage(fileID string) *schema.FunctionToolResultContentBlock {
	return &schema.FunctionToolResultContentBlock{
		Type:  schema.FunctionToolResultContentBlockTypeImage,
		Image: &schema.UserInputImage{},
		Extra: map[string]any{imageFileIDExtraKey: fileID},
	}
}

// ResponseMetaExtension retains DeepSeek Responses terminal semantics without
// exposing raw provider bodies.
type ResponseMetaExtension struct {
	ResponseID       string         `json:"response_id,omitempty"`
	Status           ResponseStatus `json:"status,omitempty"`
	FinishReason     string         `json:"finish_reason,omitempty"`
	IncompleteReason string         `json:"incomplete_reason,omitempty"`
	ErrorCode        string         `json:"error_code,omitempty"`
	Model            string         `json:"model,omitempty"`
}

// APIError is a bounded, typed DeepSeek API or response.failed error.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	if e == nil {
		return "agenticdeepseek: API error"
	}
	parts := []string{"agenticdeepseek: DeepSeek Responses API error"}
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

// HTTPStatusCode exposes the provider status without forcing retry and
// recovery owners to parse a user-facing error string.
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
		"agenticdeepseek: input conversion failed: message=%d block=%d reason=%s",
		e.MessageIndex,
		e.BlockIndex,
		e.ReasonCode,
	)
}

// ProtocolError identifies a bounded malformed DeepSeek response or stream.
type ProtocolError struct {
	ReasonCode string
}

func (e *ProtocolError) Error() string {
	return "agenticdeepseek: invalid DeepSeek Responses protocol: " + e.ReasonCode
}

type transportError struct {
	err error
}

func (e *transportError) Error() string {
	return "agenticdeepseek: DeepSeek Responses transport failed"
}

func (e *transportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func cloneTextFormat(in *TextFormat) *TextFormat {
	if in == nil {
		return nil
	}
	out := *in
	out.Schema = append(json.RawMessage(nil), in.Schema...)
	return &out
}
