package config

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/abietic/yhc/engine/model"
)

// ConnectivityStatus represents the result of a provider connectivity check.
type ConnectivityStatus string

const (
	// StatusReachable indicates the provider endpoint is accessible and responding.
	StatusReachable ConnectivityStatus = "reachable"
	// StatusUnreachable indicates the provider endpoint cannot be reached.
	StatusUnreachable ConnectivityStatus = "unreachable"
	// StatusAuthFailed indicates the endpoint is reachable but authentication failed.
	StatusAuthFailed ConnectivityStatus = "auth_failed"
	// StatusTimeout indicates the connectivity check timed out.
	StatusTimeout ConnectivityStatus = "timeout"
)

// ConnectivityResult holds the detailed result of a connectivity check.
type ConnectivityResult struct {
	// Provider is the provider that was checked.
	Provider model.ProviderID
	// Status is the connectivity status.
	Status ConnectivityStatus
	// Message is a human-readable description of the result.
	Message string
	// Latency is the time taken to get a response (zero if unreachable/timeout).
	Latency time.Duration
	// HTTPStatus is the HTTP status code received (zero if no response).
	HTTPStatus int
}

// IsOK returns true if the connectivity check indicates the provider is usable.
func (cr *ConnectivityResult) IsOK() bool {
	return cr.Status == StatusReachable
}

// ConnectivityCheckOptions configures a connectivity check.
type ConnectivityCheckOptions struct {
	// Timeout is the maximum time to wait for a response. Default: 5s.
	Timeout time.Duration
	// HTTPClient is an optional custom HTTP client. If nil, a default client is used.
	HTTPClient *http.Client
	// Context cancels the check. Defaults to context.Background().
	Context context.Context
	// BaseURL overrides the provider default endpoint.
	BaseURL string
}

// DefaultConnectivityTimeout is the default timeout for connectivity checks.
const DefaultConnectivityTimeout = 5 * time.Second

// CheckConnectivity verifies that a provider endpoint is reachable and the API key
// is valid. It performs a lightweight HTTP request (GET to the base URL or a minimal
// API call) to determine connectivity status.
//
// The check is intentionally lightweight — it does not consume API tokens or make
// full inference calls.
func CheckConnectivity(provider model.ProviderID, apiKey string, opts *ConnectivityCheckOptions) *ConnectivityResult {
	if opts == nil {
		opts = &ConnectivityCheckOptions{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultConnectivityTimeout
	}

	envCfg := model.GetProviderEnvConfig(provider)
	if envCfg == nil {
		return &ConnectivityResult{
			Provider: provider,
			Status:   StatusUnreachable,
			Message:  fmt.Sprintf("unknown provider %q: no endpoint configuration available", provider),
		}
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = envCfg.DefaultBaseURL
	}
	checkURL := buildCheckURL(provider, baseURL)

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: opts.Timeout,
		}
	}

	parentCtx := opts.Context
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return &ConnectivityResult{
			Provider: provider,
			Status:   StatusUnreachable,
			Message:  fmt.Sprintf("failed to create request: %v", err),
		}
	}

	// Set authentication headers based on provider.
	setAuthHeaders(req, provider, apiKey)

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)

	if err != nil {
		// Distinguish between timeout and other errors.
		if ctx.Err() == context.DeadlineExceeded {
			return &ConnectivityResult{
				Provider: provider,
				Status:   StatusTimeout,
				Message:  fmt.Sprintf("request timed out after %s", opts.Timeout),
				Latency:  latency,
			}
		}
		return &ConnectivityResult{
			Provider: provider,
			Status:   StatusUnreachable,
			Message:  fmt.Sprintf("connection failed: %v", err),
			Latency:  latency,
		}
	}
	defer resp.Body.Close() //nolint:errcheck

	return interpretResponse(provider, resp, latency)
}

// CheckProviderConnectivity is a convenience wrapper that resolves the API key
// from environment and checks connectivity for the given provider.
func CheckProviderConnectivity(provider model.ProviderID) *ConnectivityResult {
	envCfg := model.GetProviderEnvConfig(provider)
	if envCfg == nil {
		return &ConnectivityResult{
			Provider: provider,
			Status:   StatusUnreachable,
			Message:  fmt.Sprintf("unknown provider %q", provider),
		}
	}

	// Resolve API key from environment.
	var apiKey string
	for _, envVar := range envCfg.APIKeyEnvVars {
		if v := os.Getenv(envVar); v != "" {
			apiKey = v
			break
		}
	}

	return CheckConnectivity(provider, apiKey, nil)
}

// buildCheckURL constructs the URL to check for each provider.
// Uses lightweight endpoints that don't consume tokens.
func buildCheckURL(provider model.ProviderID, baseURL string) string {
	switch provider {
	case model.ProviderOpenAI:
		return baseURL + "/models"
	case model.ProviderAnthropic:
		return baseURL + "/v1/models"
	case model.ProviderGoogle:
		return baseURL + "/v1beta/models"
	case model.ProviderDeepSeek:
		return baseURL + "/models"
	case model.ProviderQwen:
		return baseURL + "/models"
	case model.ProviderArk:
		return baseURL + "/models"
	default:
		return baseURL
	}
}

// setAuthHeaders adds provider-specific authentication headers to the request.
func setAuthHeaders(req *http.Request, provider model.ProviderID, apiKey string) {
	if apiKey == "" {
		return
	}

	switch provider {
	case model.ProviderAnthropic:
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case model.ProviderOpenAI, model.ProviderDeepSeek, model.ProviderQwen, model.ProviderArk:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	case model.ProviderGoogle:
		// Google uses query parameter for API key.
		q := req.URL.Query()
		q.Set("key", apiKey)
		req.URL.RawQuery = q.Encode()
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// interpretResponse maps HTTP response status to a ConnectivityResult.
func interpretResponse(provider model.ProviderID, resp *http.Response, latency time.Duration) *ConnectivityResult {
	result := &ConnectivityResult{
		Provider:   provider,
		Latency:    latency,
		HTTPStatus: resp.StatusCode,
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		result.Status = StatusReachable
		result.Message = fmt.Sprintf("provider %s is reachable (HTTP %d, latency %s)", provider, resp.StatusCode, latency.Round(time.Millisecond))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		result.Status = StatusAuthFailed
		result.Message = fmt.Sprintf("authentication failed for provider %s (HTTP %d): check your API key", provider, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		// A 404 often means the endpoint exists but the specific path doesn't.
		// This still indicates the provider is reachable.
		result.Status = StatusReachable
		result.Message = fmt.Sprintf("provider %s is reachable (HTTP %d — endpoint exists but path not found)", provider, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		// Rate limited — still reachable, just throttled.
		result.Status = StatusReachable
		result.Message = fmt.Sprintf("provider %s is reachable but rate limited (HTTP 429)", provider)
	case resp.StatusCode >= 500:
		result.Status = StatusUnreachable
		result.Message = fmt.Sprintf("provider %s returned server error (HTTP %d)", provider, resp.StatusCode)
	default:
		result.Status = StatusUnreachable
		result.Message = fmt.Sprintf("provider %s returned unexpected status (HTTP %d)", provider, resp.StatusCode)
	}

	return result
}
