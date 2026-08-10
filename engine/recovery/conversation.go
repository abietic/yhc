package recovery

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// RecoveryAction defines the action to take when recovering from an API failure.
type RecoveryAction string

const (
	ActionRetry    RecoveryAction = "retry"
	ActionCompact  RecoveryAction = "compact"
	ActionTruncate RecoveryAction = "truncate"
	ActionAbort    RecoveryAction = "abort"
	ActionSkip     RecoveryAction = "skip"
)

// RecoveryContext carries all information needed to decide how to recover
// from an API call failure.
type RecoveryContext struct {
	Error        error
	StatusCode   int
	Messages     []*schema.Message
	AttemptCount int
	MaxAttempts  int
	ModelName    string
	TokenCount   int
}

// RecoveryDecision describes what recovery action to take and any
// modified state needed for that action.
type RecoveryDecision struct {
	Action           RecoveryAction
	Reason           string
	ModifiedMessages []*schema.Message
	RetryAfter       time.Duration
}

// DetermineRecovery inspects the error context and returns a decision on how
// to recover from the API failure. The decision tree is:
//
//   - 413 (Request too large) -> compact or truncate
//   - 429 (Rate limit) -> retry with exponential backoff
//   - 500/502/503 (Server error) -> retry
//   - 400 (Bad request) -> check if message format issue, else abort
//   - Network timeout -> retry
//   - Max attempts exceeded -> abort
func DetermineRecovery(ctx *RecoveryContext) *RecoveryDecision {
	// Check max attempts first — if exceeded, abort regardless of error type
	if ctx.AttemptCount >= ctx.MaxAttempts {
		return &RecoveryDecision{
			Action: ActionAbort,
			Reason: fmt.Sprintf("max recovery attempts exceeded (%d/%d)", ctx.AttemptCount, ctx.MaxAttempts),
		}
	}

	// Network timeout errors -> retry
	if isNetworkTimeout(ctx.Error) {
		delay := retryBackoff(ctx.AttemptCount)
		return &RecoveryDecision{
			Action:     ActionRetry,
			Reason:     "network timeout, retrying",
			RetryAfter: delay,
		}
	}

	switch ctx.StatusCode {
	case http.StatusRequestEntityTooLarge:
		// Request too large — try compaction first, fall back to truncation
		if ctx.TokenCount > 0 && len(ctx.Messages) > 3 {
			return &RecoveryDecision{
				Action: ActionCompact,
				Reason: "request too large (413), attempting emergency compaction",
			}
		}
		return &RecoveryDecision{
			Action: ActionTruncate,
			Reason: "request too large (413), truncating conversation",
		}

	case http.StatusTooManyRequests:
		// Rate limit — retry with backoff
		delay := retryBackoff(ctx.AttemptCount)
		// Use longer delays for rate limits
		delay = time.Duration(float64(delay) * 1.5)
		return &RecoveryDecision{
			Action:     ActionRetry,
			Reason:     "rate limited (429), backing off",
			RetryAfter: delay,
		}

	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		// Server errors — retry with standard backoff
		delay := retryBackoff(ctx.AttemptCount)
		return &RecoveryDecision{
			Action:     ActionRetry,
			Reason:     fmt.Sprintf("server error (%d), retrying", ctx.StatusCode),
			RetryAfter: delay,
		}

	case http.StatusBadRequest:
		// Bad request — check if it's a message format issue
		if isMessageFormatError(ctx.Error) {
			return &RecoveryDecision{
				Action: ActionSkip,
				Reason: "bad request (400) due to message format issue, skipping problematic message",
			}
		}
		return &RecoveryDecision{
			Action: ActionAbort,
			Reason: "bad request (400), cannot recover",
		}

	default:
		// For unknown errors, check if it looks transient
		if ctx.StatusCode >= 500 {
			delay := retryBackoff(ctx.AttemptCount)
			return &RecoveryDecision{
				Action:     ActionRetry,
				Reason:     fmt.Sprintf("server error (%d), retrying", ctx.StatusCode),
				RetryAfter: delay,
			}
		}
		return &RecoveryDecision{
			Action: ActionAbort,
			Reason: fmt.Sprintf("unrecoverable error (status %d)", ctx.StatusCode),
		}
	}
}

// ApplyRecovery applies the recovery decision to the message list, returning
// the (potentially modified) messages or an error if recovery is not possible.
func ApplyRecovery(decision *RecoveryDecision, messages []*schema.Message) ([]*schema.Message, error) {
	switch decision.Action {
	case ActionCompact:
		// Run emergency compaction — remove older messages while preserving
		// system prompt and recent context. Use ModifiedMessages if the caller
		// has already computed a compacted set.
		if decision.ModifiedMessages != nil {
			return decision.ModifiedMessages, nil
		}
		// Default: aggressive truncation targeting 50% reduction
		targetTokens := 0 // 0 means "halve the conversation"
		return EmergencyTruncate(messages, targetTokens), nil

	case ActionTruncate:
		if decision.ModifiedMessages != nil {
			return decision.ModifiedMessages, nil
		}
		// Aggressive truncation targeting 50% of current message count
		targetTokens := 0
		return EmergencyTruncate(messages, targetTokens), nil

	case ActionRetry:
		// Return messages unchanged — the caller should retry after the delay
		return messages, nil

	case ActionSkip:
		// Remove the last problematic message while preserving conversation validity
		return skipProblematicMessage(messages), nil

	case ActionAbort:
		return nil, fmt.Errorf("conversation recovery aborted: %s", decision.Reason)

	default:
		return nil, fmt.Errorf("unknown recovery action: %s", decision.Action)
	}
}

// EmergencyTruncate aggressively removes messages from the start of the
// conversation to fit within the target token count. It preserves:
//   - The system prompt (first message if role is system)
//   - The last 2 user/assistant message exchanges minimum
//   - Tool call/result pair integrity
//
// If targetTokens is 0, it removes approximately half the non-system messages.
func EmergencyTruncate(messages []*schema.Message, targetTokens int) []*schema.Message {
	if len(messages) <= 3 {
		return messages
	}

	// Identify system prompt
	systemMessages := extractSystemMessages(messages)
	nonSystemMessages := extractNonSystemMessages(messages)

	if len(nonSystemMessages) <= 2 {
		// Nothing to truncate — already at minimum
		return messages
	}

	// Determine how many messages to keep
	keepCount := len(nonSystemMessages) / 2
	if keepCount < 2 {
		keepCount = 2
	}

	// If we have a target token count, try to meet it
	if targetTokens > 0 {
		keepCount = findKeepCountForTarget(nonSystemMessages, targetTokens)
		if keepCount < 2 {
			keepCount = 2
		}
	}

	// Take messages from the end
	kept := nonSystemMessages[len(nonSystemMessages)-keepCount:]

	// Ensure tool call/result pairs stay together
	kept = repairToolCallPairs(kept, nonSystemMessages)

	// Reassemble: system messages + kept messages
	result := make([]*schema.Message, 0, len(systemMessages)+len(kept))
	result = append(result, systemMessages...)
	result = append(result, kept...)

	return result
}

// IsRecoverableError reports whether the given error represents a condition
// that the conversation recovery system can potentially handle.
func IsRecoverableError(err error) bool {
	if err == nil {
		return false
	}

	// Network timeouts are recoverable
	if isNetworkTimeout(err) {
		return true
	}

	// Check for status-code-bearing errors
	statusCode := extractStatusCode(err)
	if statusCode == 0 {
		// Check error message heuristics for common recoverable patterns
		msg := err.Error()
		return strings.Contains(msg, "timeout") ||
			strings.Contains(msg, "connection refused") ||
			strings.Contains(msg, "connection reset") ||
			strings.Contains(msg, "EOF") ||
			strings.Contains(msg, "broken pipe")
	}

	// Recoverable status codes
	switch statusCode {
	case 413, 429, 500, 502, 503, 529:
		return true
	case http.StatusBadRequest:
		return isMessageFormatError(err)
	default:
		return statusCode >= 500
	}
}

// --- Internal helpers ---

// retryBackoff computes an exponential backoff duration with jitter.
func retryBackoff(attempt int) time.Duration {
	baseMs := 1000.0
	exp := math.Pow(2, float64(attempt))
	delayMs := baseMs * exp

	// Cap at 60 seconds
	if delayMs > 60000 {
		delayMs = 60000
	}

	// Add ±20% jitter using a deterministic-ish source (good enough for delays)
	jitterFactor := 0.2
	jitterMs := delayMs * jitterFactor * (float64(time.Now().UnixNano()%100)/50.0 - 1.0)
	delayMs += jitterMs

	if delayMs < 100 {
		delayMs = 100
	}

	return time.Duration(delayMs) * time.Millisecond
}

// isNetworkTimeout detects network timeout errors in the error chain.
func isNetworkTimeout(err error) bool {
	if err == nil {
		return false
	}

	// Check for net.Error timeout
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	// Check for os.ErrDeadlineExceeded
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	// Heuristic: check error message for timeout indicators
	msg := err.Error()
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context deadline exceeded")
}

// isMessageFormatError checks if a 400 error is caused by malformed message
// content (e.g., invalid tool use structure, unsupported content type).
func isMessageFormatError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid_request_error") ||
		strings.Contains(msg, "messages") ||
		strings.Contains(msg, "content") ||
		strings.Contains(msg, "tool_use") ||
		strings.Contains(msg, "tool_result")
}

// extractStatusCode attempts to extract an HTTP status code from the error.
// Returns 0 if no status code can be determined.
func extractStatusCode(err error) int {
	if err == nil {
		return 0
	}

	// Try common error message patterns
	msg := err.Error()

	// Pattern: "status NNN" or ": NNN "
	for _, code := range []int{400, 401, 403, 413, 429, 500, 502, 503, 504, 529} {
		pattern := fmt.Sprintf("status %d", code)
		if strings.Contains(msg, pattern) {
			return code
		}
		pattern = fmt.Sprintf(": %d ", code)
		if strings.Contains(msg, pattern) {
			return code
		}
		pattern = fmt.Sprintf("(%d)", code)
		if strings.Contains(msg, pattern) {
			return code
		}
	}

	return 0
}

// extractSystemMessages returns all leading system messages.
func extractSystemMessages(messages []*schema.Message) []*schema.Message {
	var result []*schema.Message
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.System {
			result = append(result, msg)
		} else {
			break
		}
	}
	return result
}

// extractNonSystemMessages returns all messages after the leading system messages.
func extractNonSystemMessages(messages []*schema.Message) []*schema.Message {
	startIdx := 0
	for i, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role != schema.System {
			startIdx = i
			break
		}
		startIdx = i + 1
	}
	if startIdx >= len(messages) {
		return nil
	}
	return messages[startIdx:]
}

// findKeepCountForTarget binary-searches for the minimum number of trailing
// messages whose rough token estimate fits within targetTokens.
func findKeepCountForTarget(messages []*schema.Message, targetTokens int) int {
	n := len(messages)
	for keepCount := 2; keepCount <= n; keepCount++ {
		slice := messages[n-keepCount:]
		tokens := roughEstimateTokens(slice)
		if tokens > targetTokens {
			// The previous count fit
			if keepCount > 2 {
				return keepCount - 1
			}
			return 2
		}
	}
	return n
}

// roughEstimateTokens provides a quick token estimate for a slice of messages.
// Uses ~4 chars per token as a rough heuristic.
func roughEstimateTokens(messages []*schema.Message) int {
	total := 0
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		total += 8 // per-message overhead
		total += roughTextTokens(msg.Content)
		for _, tc := range msg.ToolCalls {
			total += 12
			total += roughTextTokens(tc.Function.Arguments)
		}
	}
	return total
}

func roughTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / 4.0))
}

// repairToolCallPairs ensures that the kept messages don't start with orphaned
// tool results (role=Tool without a preceding assistant tool call) and don't
// end with an assistant tool call without its results.
func repairToolCallPairs(kept, allNonSystem []*schema.Message) []*schema.Message {
	if len(kept) == 0 {
		return kept
	}

	// Collect tool call IDs present in kept assistant messages
	toolCallIDs := make(map[string]bool)
	for _, msg := range kept {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Assistant {
			for _, tc := range msg.ToolCalls {
				toolCallIDs[tc.ID] = true
			}
		}
	}

	// Remove leading tool-result messages whose tool call is not in the kept set
	startIdx := 0
	for startIdx < len(kept) {
		msg := kept[startIdx]
		if msg == nil {
			startIdx++
			continue
		}
		if msg.Role == schema.Tool && msg.ToolCallID != "" && !toolCallIDs[msg.ToolCallID] {
			startIdx++
			continue
		}
		break
	}
	kept = kept[startIdx:]

	// Check if the last message is an assistant with tool calls —
	// we need to find and include the corresponding tool results
	if len(kept) > 0 {
		last := kept[len(kept)-1]
		if last != nil && last.Role == schema.Assistant && len(last.ToolCalls) > 0 {
			// Collect IDs of tool calls in the last assistant message
			lastCallIDs := make(map[string]bool)
			for _, tc := range last.ToolCalls {
				lastCallIDs[tc.ID] = true
			}

			// Look for corresponding tool results in allNonSystem that follow
			// the kept messages
			for _, msg := range allNonSystem {
				if msg == nil {
					continue
				}
				if msg.Role == schema.Tool && lastCallIDs[msg.ToolCallID] {
					// Check if already in kept
					found := false
					for _, k := range kept {
						if k == msg {
							found = true
							break
						}
					}
					if !found {
						kept = append(kept, msg)
					}
					delete(lastCallIDs, msg.ToolCallID)
				}
				if len(lastCallIDs) == 0 {
					break
				}
			}
		}
	}

	return kept
}

// skipProblematicMessage removes the last non-system message from the
// conversation, preserving tool call/result pair integrity.
func skipProblematicMessage(messages []*schema.Message) []*schema.Message {
	if len(messages) <= 1 {
		return messages
	}

	// Find the last non-system message
	lastIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role != schema.System {
			lastIdx = i
			break
		}
	}

	if lastIdx < 0 {
		return messages
	}

	lastMsg := messages[lastIdx]

	// If it's a tool result, also remove the corresponding tool call from
	// the preceding assistant message to maintain pair integrity
	if lastMsg.Role == schema.Tool && lastMsg.ToolCallID != "" {
		// Find and remove all tool results with same or related tool call IDs,
		// then find the assistant message that issued the call
		removeFrom := lastIdx
		for i := lastIdx - 1; i >= 0; i-- {
			msg := messages[i]
			if msg == nil {
				continue
			}
			if msg.Role == schema.Tool {
				removeFrom = i
				continue
			}
			if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
				removeFrom = i
			}
			break
		}
		result := make([]*schema.Message, 0, len(messages)-(lastIdx-removeFrom+1))
		result = append(result, messages[:removeFrom]...)
		if lastIdx+1 < len(messages) {
			result = append(result, messages[lastIdx+1:]...)
		}
		return result
	}

	// Simple case: just remove the last message
	result := make([]*schema.Message, 0, len(messages)-1)
	result = append(result, messages[:lastIdx]...)
	if lastIdx+1 < len(messages) {
		result = append(result, messages[lastIdx+1:]...)
	}
	return result
}
