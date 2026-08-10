package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultHTTPHookTimeout is the default timeout for HTTP hook execution.
// Mirrors DEFAULT_HTTP_HOOK_TIMEOUT_MS in the reference (10 minutes).
const DefaultHTTPHookTimeout = 10 * time.Minute

// HTTPHookResult captures the outcome of an HTTP hook execution.
type HTTPHookResult struct {
	// OK is true if the HTTP response status was 2xx.
	OK bool

	// StatusCode is the HTTP response status code (0 if request failed).
	StatusCode int

	// Body is the response body text.
	Body string

	// Error is set when the request fails (network error, timeout, etc.).
	Error string

	// Aborted is true if the request was cancelled via context.
	Aborted bool
}

// HTTPHookExecutor executes HTTP hooks by POSTing event payloads to configured URLs.
// Mirrors execHttpHook from the reference (src/utils/hooks/execHttpHook.ts).
type HTTPHookExecutor struct {
	// Client is the HTTP client used for requests. If nil, a default client
	// with the hook timeout is used.
	Client *http.Client
}

// NewHTTPHookExecutor creates a new HTTP hook executor with default settings.
func NewHTTPHookExecutor() *HTTPHookExecutor {
	return &HTTPHookExecutor{}
}

// Execute runs an HTTP hook by POSTing the JSON input to the configured URL.
//
// Parameters:
//   - ctx: context for cancellation
//   - hook: the hook command (must be type="http")
//   - jsonInput: the JSON-encoded event payload to POST
//
// Returns the HTTP response result.
func (e *HTTPHookExecutor) Execute(ctx context.Context, hook *HookCommand, jsonInput string) *HTTPHookResult {
	if hook == nil || hook.Type != HookCommandTypeHTTP {
		return &HTTPHookResult{
			OK:    false,
			Error: "invalid hook: must be type http",
		}
	}

	if hook.URL == "" {
		return &HTTPHookResult{
			OK:    false,
			Error: "HTTP hook URL is empty",
		}
	}

	// SSRF guard: validate URL does not target private/link-local addresses.
	if err := ValidateHookURL(hook.URL); err != nil {
		return &HTTPHookResult{
			OK:    false,
			Error: err.Error(),
		}
	}

	// Determine timeout.
	timeout := DefaultHTTPHookTimeout
	if hook.Timeout > 0 {
		timeout = time.Duration(hook.Timeout) * time.Second
	}

	// Create a child context with timeout.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build the request.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewBufferString(jsonInput))
	if err != nil {
		return &HTTPHookResult{
			OK:    false,
			Error: fmt.Sprintf("create request: %s", err.Error()),
		}
	}

	// Set headers.
	req.Header.Set("Content-Type", "application/json")
	if hook.Headers != nil {
		allowedVars := buildAllowedEnvVars(hook.AllowedEnvVars)
		for name, value := range hook.Headers {
			interpolated := interpolateEnvVars(value, allowedVars)
			req.Header.Set(name, interpolated)
		}
	}

	// Execute the request.
	client := e.Client
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return &HTTPHookResult{
				OK:      false,
				Aborted: true,
			}
		}
		return &HTTPHookResult{
			OK:    false,
			Error: fmt.Sprintf("http request failed: %s", err.Error()),
		}
	}
	defer resp.Body.Close() //nolint:errcheck

	// Read response body (capped at 1MB to prevent OOM).
	const maxBodySize = 1 << 20 // 1MB
	bodyReader := io.LimitReader(resp.Body, maxBodySize)
	bodyBytes, err := io.ReadAll(bodyReader)
	if err != nil {
		return &HTTPHookResult{
			OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
			StatusCode: resp.StatusCode,
			Body:       "",
			Error:      fmt.Sprintf("read response body: %s", err.Error()),
		}
	}

	return &HTTPHookResult{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		StatusCode: resp.StatusCode,
		Body:       string(bodyBytes),
	}
}

// ExecuteWithPayload is a convenience method that marshals the payload to JSON
// and executes the HTTP hook.
func (e *HTTPHookExecutor) ExecuteWithPayload(ctx context.Context, hook *HookCommand, payload map[string]any) *HTTPHookResult {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return &HTTPHookResult{
			OK:    false,
			Error: fmt.Sprintf("marshal payload: %s", err.Error()),
		}
	}
	return e.Execute(ctx, hook, string(jsonData))
}

// ---------------------------------------------------------------------------
// Env var interpolation
// ---------------------------------------------------------------------------

// envVarPattern matches $VAR_NAME and ${VAR_NAME} patterns.
var envVarPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}|\$([A-Z_][A-Z0-9_]*)`)

// interpolateEnvVars replaces $VAR_NAME and ${VAR_NAME} patterns in a string
// using environment variables, but only for names in the allowed set.
// References to variables not in the allowlist are replaced with empty strings.
//
// The result is sanitized to strip CR/LF/NUL to prevent header injection.
//
// Mirrors interpolateEnvVars from the reference (src/utils/hooks/execHttpHook.ts).
func interpolateEnvVars(value string, allowedVars map[string]bool) string {
	result := envVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		// Extract variable name from $VAR or ${VAR} syntax.
		submatch := envVarPattern.FindStringSubmatch(match)
		varName := ""
		if len(submatch) > 1 && submatch[1] != "" {
			varName = submatch[1] // ${VAR_NAME} form
		} else if len(submatch) > 2 && submatch[2] != "" {
			varName = submatch[2] // $VAR_NAME form
		}

		if varName == "" {
			return ""
		}

		if !allowedVars[varName] {
			return ""
		}

		return os.Getenv(varName)
	})

	return sanitizeHeaderValue(result)
}

// sanitizeHeaderValue strips CR, LF, and NUL bytes from a header value
// to prevent HTTP header injection (CRLF injection).
//
// Mirrors sanitizeHeaderValue from the reference.
func sanitizeHeaderValue(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1 // Drop the character
		}
		return r
	}, value)
}

// buildAllowedEnvVars converts a slice of env var names into a lookup set.
func buildAllowedEnvVars(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]bool, len(names))
	for _, name := range names {
		m[name] = true
	}
	return m
}
