// Package errors provides a typed error system for the YHC engine.
// Each error carries a machine-readable code, human-readable message,
// optional wrapped cause, and classification flags (retryable, user-facing).
package errors

import (
	"errors"
	"fmt"
	"time"
)

// Error code constants identify each error category.
const (
	CodeAbort      = "ABORT"
	CodeOverflow   = "OVERFLOW"
	CodeShellError = "SHELL_ERROR"
	CodeToolError  = "TOOL_ERROR"
	CodePermission = "PERMISSION_DENIED"
	CodeRateLimit  = "RATE_LIMIT"
	CodeNetwork    = "NETWORK_ERROR"
	CodeModel      = "MODEL_ERROR"
	CodeConfig     = "CONFIG_ERROR"
	CodeSession    = "SESSION_ERROR"
	CodeMaxTurns   = "MAX_TURNS"
)

// AgentError is the base error type for all agent engine errors.
// It implements the error interface and supports unwrapping via errors.Is/As.
type AgentError struct {
	Code       string
	Message    string
	Cause      error
	Retryable  bool
	UserFacing bool
}

// Error formats the error as "[CODE] message" or "[CODE] message: cause".
func (e *AgentError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause, enabling errors.Is and errors.As
// to traverse the error chain.
func (e *AgentError) Unwrap() error {
	return e.Cause
}

// NewAbortError creates an error indicating the user cancelled or interrupted
// the operation.
func NewAbortError(message string) *AgentError {
	return &AgentError{
		Code:       CodeAbort,
		Message:    message,
		Retryable:  false,
		UserFacing: true,
	}
}

// NewOverflowError creates an error indicating the context window is too large
// (analogous to HTTP 413). tokenCount records the token count that triggered it.
func NewOverflowError(message string, tokenCount int) *AgentError {
	return &AgentError{
		Code:       CodeOverflow,
		Message:    fmt.Sprintf("%s (tokens: %d)", message, tokenCount),
		Retryable:  false,
		UserFacing: true,
	}
}

// NewShellError creates an error for a failed shell command execution.
func NewShellError(command string, exitCode int, stderr string) *AgentError {
	msg := fmt.Sprintf("command %q exited with code %d", command, exitCode)
	if stderr != "" {
		msg += ": " + stderr
	}
	return &AgentError{
		Code:       CodeShellError,
		Message:    msg,
		Retryable:  false,
		UserFacing: true,
	}
}

// NewToolError creates an error for a tool execution failure.
func NewToolError(toolName, message string, cause error) *AgentError {
	return &AgentError{
		Code:       CodeToolError,
		Message:    fmt.Sprintf("tool %q: %s", toolName, message),
		Cause:      cause,
		Retryable:  true,
		UserFacing: true,
	}
}

// NewPermissionError creates an error indicating permission was denied for a
// tool invocation.
func NewPermissionError(toolName, message string) *AgentError {
	return &AgentError{
		Code:       CodePermission,
		Message:    fmt.Sprintf("tool %q: %s", toolName, message),
		Retryable:  false,
		UserFacing: true,
	}
}

// NewRateLimitError creates an error indicating the API rate limit was hit.
// retryAfter hints how long to wait before retrying.
func NewRateLimitError(retryAfter time.Duration) *AgentError {
	return &AgentError{
		Code:       CodeRateLimit,
		Message:    fmt.Sprintf("rate limited, retry after %s", retryAfter),
		Retryable:  true,
		UserFacing: true,
	}
}

// NewNetworkError creates an error for network connectivity issues.
func NewNetworkError(message string, cause error) *AgentError {
	return &AgentError{
		Code:       CodeNetwork,
		Message:    message,
		Cause:      cause,
		Retryable:  true,
		UserFacing: true,
	}
}

// NewModelError creates an error for model/API failures. statusCode is the
// HTTP status code returned by the model API (0 if unavailable).
func NewModelError(message string, statusCode int) *AgentError {
	msg := message
	if statusCode != 0 {
		msg = fmt.Sprintf("%s (status %d)", message, statusCode)
	}
	return &AgentError{
		Code:       CodeModel,
		Message:    msg,
		Retryable:  statusCode >= 500 || statusCode == 429,
		UserFacing: true,
	}
}

// NewConfigError creates an error for configuration problems.
func NewConfigError(message string) *AgentError {
	return &AgentError{
		Code:       CodeConfig,
		Message:    message,
		Retryable:  false,
		UserFacing: true,
	}
}

// NewSessionError creates an error for session management failures.
func NewSessionError(message string) *AgentError {
	return &AgentError{
		Code:       CodeSession,
		Message:    message,
		Retryable:  false,
		UserFacing: true,
	}
}

// NewMaxTurnsError creates an error indicating the agent exceeded the maximum
// allowed number of turns.
func NewMaxTurnsError(turnCount int) *AgentError {
	return &AgentError{
		Code:       CodeMaxTurns,
		Message:    fmt.Sprintf("exceeded maximum turns (%d)", turnCount),
		Retryable:  false,
		UserFacing: true,
	}
}

// ---------------------------------------------------------------------------
// Classification helpers
// ---------------------------------------------------------------------------

// asAgentError extracts an *AgentError from err's chain.
func asAgentError(err error) (*AgentError, bool) {
	var ae *AgentError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// IsRetryable reports whether the error (or any in its chain) is retryable.
func IsRetryable(err error) bool {
	if ae, ok := asAgentError(err); ok {
		return ae.Retryable
	}
	return false
}

// IsUserFacing reports whether the error message is safe to display to a user.
func IsUserFacing(err error) bool {
	if ae, ok := asAgentError(err); ok {
		return ae.UserFacing
	}
	return false
}

// IsAbort reports whether the error represents a user abort/cancellation.
func IsAbort(err error) bool {
	if ae, ok := asAgentError(err); ok {
		return ae.Code == CodeAbort
	}
	return false
}

// IsOverflow reports whether the error represents a context overflow.
func IsOverflow(err error) bool {
	if ae, ok := asAgentError(err); ok {
		return ae.Code == CodeOverflow
	}
	return false
}

// IsRateLimit reports whether the error represents a rate limit.
func IsRateLimit(err error) bool {
	if ae, ok := asAgentError(err); ok {
		return ae.Code == CodeRateLimit
	}
	return false
}

// GetCode extracts the error code from an AgentError in the chain.
// Returns an empty string if err is not an AgentError.
func GetCode(err error) string {
	if ae, ok := asAgentError(err); ok {
		return ae.Code
	}
	return ""
}
