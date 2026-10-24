// Package agentickimi implements the bounded Kimi K3 Responses contract.
package agentickimi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	DefaultBaseURL = "https://api.moonshot.ai/v1"
	ModelK3        = "kimi-k3"
	// The initial product slice deliberately caps generation at the API default.
	DefaultMaxOutputTokens  = 131072
	MaxOutputTokens         = 131072
	maxRequestBytes         = 32 << 20
	maxResponseBytes        = 32 << 20
	defaultMaxSSEEventBytes = 16 << 20
	maxTools                = 128
)

type ReasoningEffort string

const (
	ReasoningEffortLow  ReasoningEffort = "low"
	ReasoningEffortHigh ReasoningEffort = "high"
	ReasoningEffortMax  ReasoningEffort = "max"
)

type Config struct {
	APIKey           string
	BaseURL          string
	Model            string
	MaxTokens        int
	ReasoningEffort  ReasoningEffort
	HTTPClient       *http.Client
	Timeout          time.Duration
	MaxSSEEventBytes int
}

type callOptions struct{ reasoningEffort ReasoningEffort }

func WithReasoningEffort(value ReasoningEffort) model.Option {
	return model.WrapImplSpecificOptFn(func(o *callOptions) { o.reasoningEffort = value })
}

// ResponseMetaExtension retains Kimi terminal metadata without granting opaque
// continuation authority. Kimi reasoning is replayed as documented plain text.
type ResponseMetaExtension struct {
	ID               string
	Model            string
	FinishReason     string
	CacheTTL         string
	cacheWriteTokens *int
}

// ProviderCacheWriteReceipt preserves explicit zero separately from missing usage.
func (e *ResponseMetaExtension) ProviderCacheWriteReceipt() (int, bool, string) {
	if e == nil || e.cacheWriteTokens == nil {
		return 0, false, ""
	}
	return *e.cacheWriteTokens, true, e.CacheTTL
}

type ConversionError struct {
	MessageIndex, BlockIndex int
	ReasonCode               string
}

func (e *ConversionError) Error() string {
	return fmt.Sprintf("agentickimi: input conversion: message=%d block=%d reason=%s", e.MessageIndex, e.BlockIndex, e.ReasonCode)
}

func conversionError(message, block int, reason string) *ConversionError {
	return &ConversionError{MessageIndex: message, BlockIndex: block, ReasonCode: reason}
}

type ProtocolError struct{ ReasonCode string }

func (e *ProtocolError) Error() string {
	return "agentickimi: invalid Responses protocol: " + e.ReasonCode
}

type APIError struct {
	StatusCode    int
	Code          string
	finalUsage    *schema.TokenUsage
	responseModel string
	cacheTTL      string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("agentickimi: Responses API error: HTTP %d code=%s", e.StatusCode, e.Code)
}
func (e *APIError) HTTPStatusCode() int { return e.StatusCode }
func (e *APIError) FinalProviderUsage() (*schema.TokenUsage, string) {
	if e == nil || e.finalUsage == nil {
		return nil, ""
	}
	u := *e.finalUsage
	return &u, e.responseModel
}

func (e *APIError) ProviderCacheWriteReceipt() (int, bool, string) {
	if e == nil || e.finalUsage == nil {
		return 0, false, ""
	}
	return e.finalUsage.PromptTokenDetails.CacheWriteTokens, true, e.cacheTTL
}

type transportError struct{ err error }

func (e *transportError) Error() string { return "agentickimi: API transport failed" }
func (e *transportError) Unwrap() error { return e.err }

type (
	wirePart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	wireItem struct {
		Type             string     `json:"type"`
		ID               string     `json:"id,omitempty"`
		Role             string     `json:"role,omitempty"`
		Status           string     `json:"status,omitempty"`
		Content          []wirePart `json:"content,omitempty"`
		Summary          []wirePart `json:"summary,omitempty"`
		EncryptedContent *string    `json:"encrypted_content,omitempty"`
		CallID           string     `json:"call_id,omitempty"`
		Name             string     `json:"name,omitempty"`
		Namespace        string     `json:"namespace,omitempty"`
		Arguments        string     `json:"arguments,omitempty"`
		Output           *string    `json:"output,omitempty"`
	}
)

type wireTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
	Strict      bool   `json:"strict"`
}
type (
	cacheOptions struct {
		Mode string `json:"mode"`
		TTL  string `json:"ttl"`
	}
	wireRequest struct {
		Model           string     `json:"model"`
		Input           []wireItem `json:"input"`
		Stream          bool       `json:"stream"`
		MaxOutputTokens int        `json:"max_output_tokens"`
		Reasoning       struct {
			Effort ReasoningEffort `json:"effort"`
		} `json:"reasoning"`
		Tools              []wireTool   `json:"tools,omitempty"`
		ToolChoice         string       `json:"tool_choice,omitempty"`
		PromptCacheOptions cacheOptions `json:"prompt_cache_options"`
	}
)

type wireUsage struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
	TotalTokens  *int `json:"total_tokens"`
	InputDetails struct {
		CachedTokens     *int `json:"cached_tokens"`
		CacheWriteTokens *int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}
type wireResponse struct {
	ID                string     `json:"id"`
	Object            string     `json:"object"`
	Model             string     `json:"model"`
	Status            string     `json:"status"`
	Output            []wireItem `json:"output"`
	Usage             *wireUsage `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	PromptCacheOptions *cacheOptions `json:"prompt_cache_options"`
}
