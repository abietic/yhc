// Package api provides a low-level Claude API client with request building,
// retry logic, and response parsing.
// Mirrors src/services/api/client.ts and src/services/api/withRetry.ts from
// the reference implementation.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// -------------------------------------------------------------------
// Configuration
// -------------------------------------------------------------------

const (
	// DefaultBaseURL is the default Anthropic API endpoint.
	DefaultBaseURL = "https://api.anthropic.com"

	// DefaultMaxRetries is the default number of retry attempts for transient errors.
	// Mirrors withRetry.ts DEFAULT_MAX_RETRIES.
	DefaultMaxRetries = 3

	// DefaultTimeout is the default HTTP request timeout (10 minutes).
	// Mirrors client.ts API_TIMEOUT_MS default of 600_000.
	DefaultTimeout = 10 * time.Minute

	// AnthropicVersion is the API version header value.
	AnthropicVersion = "2023-06-01"

	// messagesPath is the API endpoint path for message creation.
	messagesPath = "/v1/messages"

	// baseDelayMS is the initial backoff delay in milliseconds.
	// Mirrors withRetry.ts BASE_DELAY_MS.
	baseDelayMS = 500

	// maxDelayMS is the maximum backoff delay in milliseconds (32 seconds).
	maxDelayMS = 32000
)

// ClientConfig configures a Claude API client.
// Mirrors the options passed to getAnthropicClient in client.ts.
type ClientConfig struct {
	APIKey        string
	BaseURL       string // defaults to "https://api.anthropic.com"
	Model         string
	MaxRetries    int // defaults to 3
	Timeout       time.Duration
	BetaFeatures  []string
	ExtraHeaders  map[string]string
	SkipCacheRead bool
	Organization  string
}

// -------------------------------------------------------------------
// Client
// -------------------------------------------------------------------

// Client is a low-level Claude API client that handles request building,
// retry logic with exponential backoff, and response parsing.
// Mirrors the Anthropic SDK client configuration in client.ts.
type Client struct {
	config     ClientConfig
	httpClient *http.Client
	mu         sync.Mutex
}

// NewClient creates a new Claude API Client with the given configuration.
// Applies defaults for BaseURL, MaxRetries, and Timeout if not set.
func NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = DefaultMaxRetries
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}

	return &Client{
		config: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// -------------------------------------------------------------------
// Request / Response types
// -------------------------------------------------------------------

// MessageRequest is the request payload for the Claude messages API.
// Mirrors BetaMessageStreamParams in the reference SDK types.
type MessageRequest struct {
	Model         string         `json:"model"`
	Messages      []APIMessage   `json:"messages"`
	System        string         `json:"system,omitempty"`
	MaxTokens     int            `json:"max_tokens"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	Stream        bool           `json:"stream"`
	Tools         []APITool      `json:"tools,omitempty"`
	ToolChoice    any            `json:"tool_choice,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	StopSequences []string       `json:"stop_sequences,omitempty"`
}

// APIMessage represents a message in the conversation.
type APIMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []ContentBlock
}

// APITool represents a tool definition for the Claude API.
type APITool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// MessageResponse is the response from the Claude messages API.
// Mirrors BetaMessage in the reference SDK types.
type MessageResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// ContentBlock represents a block in the response content array.
// Supports "text", "tool_use", and "thinking" block types.
type ContentBlock struct {
	Type  string `json:"type"` // "text", "tool_use", "thinking"
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

// Usage tracks token consumption for the request.
// Mirrors BetaUsage in the reference SDK types.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// -------------------------------------------------------------------
// Error types
// -------------------------------------------------------------------

// APIError represents an error response from the Claude API.
// Carries status code and error classification for retry decisions.
type APIError struct {
	StatusCode int    `json:"-"`
	Type       string `json:"type"`
	Message    string `json:"message"`
	Retryable  bool   `json:"-"`
	RequestID  string `json:"-"`
}

// apiErrorResponse is the wire format of an error response from the API.
type apiErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("claude api error (status %d, request %s): %s: %s",
			e.StatusCode, e.RequestID, e.Type, e.Message)
	}
	return fmt.Sprintf("claude api error (status %d): %s: %s",
		e.StatusCode, e.Type, e.Message)
}

// IsRetryable returns true for errors that should be retried:
// 429 (rate limit), 500, 502, 503 (server errors), 529 (overloaded).
// Mirrors shouldRetry in withRetry.ts.
func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	return false
}

// IsOverloaded returns true for 529 overloaded errors.
// Mirrors is529Error in withRetry.ts.
func IsOverloaded(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 529 ||
			apiErr.Type == "overloaded_error"
	}
	return false
}

// IsRateLimited returns true for 429 rate limit errors.
func IsRateLimited(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// IsPromptTooLong returns true for prompt_too_long errors.
func IsPromptTooLong(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return strings.Contains(apiErr.Type, "prompt_too_long") ||
			strings.Contains(apiErr.Message, "prompt is too long")
	}
	return false
}

// classifyRetryable determines whether an HTTP status code represents
// a retryable error condition.
// Mirrors shouldRetry logic in withRetry.ts.
func classifyRetryable(statusCode int) bool {
	switch statusCode {
	case 429: // rate limit
		return true
	case 500, 502, 503: // server errors
		return true
	case 529: // overloaded
		return true
	default:
		return false
	}
}

// -------------------------------------------------------------------
// SendMessage — the main API call method
// -------------------------------------------------------------------

// SendMessage sends a message request to the Claude API with retry logic.
// Builds the HTTP request with proper headers, retries on retryable errors
// with exponential backoff, and parses the response or error.
// Mirrors the withRetry + operation pattern in claude.ts.
func (c *Client) SendMessage(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	// Apply model from config if not set on request.
	if req.Model == "" {
		req.Model = c.config.Model
	}

	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Wait before retry (not on first attempt).
		if attempt > 0 {
			delay := getRetryDelay(attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := c.doRequest(ctx, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		// Only retry on retryable errors.
		if !IsRetryable(err) {
			return nil, err
		}
	}

	return nil, lastErr
}

// -------------------------------------------------------------------
// Internal: HTTP request execution
// -------------------------------------------------------------------

// doRequest performs a single HTTP request to the Claude messages API.
func (c *Client) doRequest(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.config.BaseURL, "/") + messagesPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Build and set headers.
	c.buildHeaders(httpReq)

	// Execute the request.
	c.mu.Lock()
	httpClient := c.httpClient
	c.mu.Unlock()

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer httpResp.Body.Close() //nolint:errcheck

	// Read the response body.
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Extract request ID for debugging/tracking.
	requestID := httpResp.Header.Get("request-id")

	// Handle error responses.
	if httpResp.StatusCode >= 400 {
		return nil, parseErrorResponse(httpResp.StatusCode, respBody, requestID)
	}

	// Parse success response.
	var msgResp MessageResponse
	if err := json.Unmarshal(respBody, &msgResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &msgResp, nil
}

// buildHeaders sets all required headers on the HTTP request.
// Mirrors defaultHeaders construction in client.ts and header setup in claude.ts.
func (c *Client) buildHeaders(req *http.Request) {
	// Required headers.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.config.APIKey)
	req.Header.Set("anthropic-version", AnthropicVersion)

	// Attribution header — mirrors x-app in client.ts defaultHeaders.
	req.Header.Set("x-app", "cli")

	// Per-request correlation ID — mirrors x-client-request-id in client.ts buildFetch.
	req.Header.Set("x-client-request-id", uuid.New().String())

	// Beta features header.
	if len(c.config.BetaFeatures) > 0 {
		req.Header.Set("anthropic-beta", strings.Join(c.config.BetaFeatures, ","))
	}

	// Organization header.
	if c.config.Organization != "" {
		req.Header.Set("anthropic-organization", c.config.Organization)
	}

	// Cache control — mirrors DISABLE_PROMPT_CACHING / skip-cache-read patterns.
	if c.config.SkipCacheRead {
		req.Header.Set("anthropic-skip-cache-read", "true")
	}

	// Extra headers — user-configurable, applied last so they can override.
	for key, value := range c.config.ExtraHeaders {
		req.Header.Set(key, value)
	}
}

// -------------------------------------------------------------------
// Internal: error parsing
// -------------------------------------------------------------------

// parseErrorResponse parses an API error response body into an *APIError.
func parseErrorResponse(statusCode int, body []byte, requestID string) *APIError {
	var errResp apiErrorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		// If we can't parse the error, create a generic one.
		return &APIError{
			StatusCode: statusCode,
			Type:       "unknown_error",
			Message:    string(body),
			Retryable:  classifyRetryable(statusCode),
			RequestID:  requestID,
		}
	}

	apiErr := &APIError{
		StatusCode: statusCode,
		Type:       errResp.Error.Type,
		Message:    errResp.Error.Message,
		Retryable:  classifyRetryable(statusCode),
		RequestID:  requestID,
	}

	// 529 may come with type "overloaded_error" — ensure Retryable is set.
	if apiErr.Type == "overloaded_error" {
		apiErr.Retryable = true
	}

	return apiErr
}

// -------------------------------------------------------------------
// Internal: retry delay calculation
// -------------------------------------------------------------------

// getRetryDelay calculates exponential backoff with jitter.
// Mirrors getRetryDelay in withRetry.ts.
func getRetryDelay(attempt int) time.Duration {
	baseDelay := float64(baseDelayMS) * math.Pow(2, float64(attempt-1))
	if baseDelay > float64(maxDelayMS) {
		baseDelay = float64(maxDelayMS)
	}

	// Add jitter: 0-25% of base delay.
	// Mirrors: const jitter = Math.random() * 0.25 * baseDelay
	jitter := rand.Float64() * 0.25 * baseDelay
	delayMs := baseDelay + jitter

	return time.Duration(delayMs) * time.Millisecond
}
