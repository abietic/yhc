package permission

import (
	"context"
	"sync"
	"time"
)

// DenialLimits defines thresholds for falling back to prompting.
// Mirrors src/utils/permissions/denialTracking.ts.
var DenialLimits = struct {
	MaxConsecutive int
	MaxTotal       int
}{
	MaxConsecutive: 3,
	MaxTotal:       20,
}

// DenialRecord captures a single permission denial event with metadata.
type DenialRecord struct {
	ToolName  string
	Input     map[string]any
	Reason    PermissionReason
	Message   string
	Timestamp time.Time
}

// DenialTrackingState tracks consecutive and total permission denials,
// with per-operation rate-limiting to avoid repeated prompts.
type DenialTrackingState struct {
	ConsecutiveDenials int
	TotalDenials       int

	// History stores recent denial records for inspection.
	History []DenialRecord

	// rateLimiter tracks when each (tool, input-fingerprint) was last denied
	// to suppress repeated prompts for the same operation.
	rateLimiter map[string]time.Time
	mu          sync.Mutex
}

// RateLimitWindow is the minimum interval between permission prompts for the
// same tool+input combination. If a denial was recorded within this window,
// the same operation is auto-denied without re-prompting.
// Mirrors the reference behavior of not re-prompting for the same denied action.
const RateLimitWindow = 30 * time.Second

// MaxHistorySize is the maximum number of denial records to retain.
const MaxHistorySize = 100

// NewDenialTrackingState creates a fresh denial tracking state.
func NewDenialTrackingState() *DenialTrackingState {
	return &DenialTrackingState{
		rateLimiter: make(map[string]time.Time),
	}
}

// RecordDenial increments both consecutive and total denial counters
// and appends a record to the denial history.
func (s *DenialTrackingState) RecordDenial() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ConsecutiveDenials++
	s.TotalDenials++
}

// RecordDenialWithDetails records a denial with full metadata including
// tool name, input, reason, and timestamp. This feeds the denial history
// and the rate limiter.
func (s *DenialTrackingState) RecordDenialWithDetails(toolName string, input map[string]any, reason PermissionReason, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ConsecutiveDenials++
	s.TotalDenials++

	now := time.Now()

	record := DenialRecord{
		ToolName:  toolName,
		Input:     input,
		Reason:    reason,
		Message:   message,
		Timestamp: now,
	}

	// Append to history, trimming if needed.
	s.History = append(s.History, record)
	if len(s.History) > MaxHistorySize {
		// Keep the most recent records.
		s.History = s.History[len(s.History)-MaxHistorySize:]
	}

	// Update rate limiter.
	key := denialRateLimitKey(toolName, input)
	s.rateLimiter[key] = now
}

// RecordSuccess resets the consecutive denial counter (total remains).
func (s *DenialTrackingState) RecordSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ConsecutiveDenials = 0
}

// ShouldFallbackToPrompting returns true when denial counts exceed thresholds.
func (s *DenialTrackingState) ShouldFallbackToPrompting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ConsecutiveDenials >= DenialLimits.MaxConsecutive ||
		s.TotalDenials >= DenialLimits.MaxTotal
}

// IsRateLimited returns true if the same (tool, input) was denied within
// the RateLimitWindow, meaning we should auto-deny without re-prompting.
// This prevents the same operation from generating repeated permission prompts.
func (s *DenialTrackingState) IsRateLimited(toolName string, input map[string]any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := denialRateLimitKey(toolName, input)
	lastDenied, exists := s.rateLimiter[key]
	if !exists {
		return false
	}

	return time.Since(lastDenied) < RateLimitWindow
}

// ClearRateLimit removes the rate limit entry for a specific tool+input,
// allowing the next invocation to prompt normally. Used when circumstances
// change (e.g., user modifies rules).
func (s *DenialTrackingState) ClearRateLimit(toolName string, input map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := denialRateLimitKey(toolName, input)
	delete(s.rateLimiter, key)
}

// ClearAllRateLimits removes all rate limit entries, allowing all operations
// to prompt normally. Used on mode changes or rule updates.
func (s *DenialTrackingState) ClearAllRateLimits() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rateLimiter = make(map[string]time.Time)
}

// Reset clears all denial state, returning to a fresh state.
// Used on mode transitions to prevent state leaking between modes.
func (s *DenialTrackingState) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ConsecutiveDenials = 0
	s.TotalDenials = 0
	s.History = nil
	s.rateLimiter = make(map[string]time.Time)
}

// GetHistory returns a snapshot of the denial history.
func (s *DenialTrackingState) GetHistory() []DenialRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.History) == 0 {
		return nil
	}
	result := make([]DenialRecord, len(s.History))
	copy(result, s.History)
	return result
}

// denialRateLimitKey creates a string key for rate-limiting lookups.
// Uses tool name + the primary input field (command for Bash, file_path for
// file tools) as the fingerprint. This is intentionally coarse — we want to
// suppress repeated prompts for the *same* operation, not track every
// possible input combination.
func denialRateLimitKey(toolName string, input map[string]any) string {
	var inputKey string
	if input != nil {
		switch toolName {
		case "Bash":
			if cmd, ok := input["command"].(string); ok {
				inputKey = cmd
			}
		case "Read", "Write", "Edit":
			if fp, ok := input["file_path"].(string); ok {
				inputKey = fp
			}
		default:
			// For other tools, use tool name only (coarse grouping).
			inputKey = ""
		}
	}
	return toolName + "\x00" + inputKey
}

// fallbackPromptKey is the context key signaling fallback-to-prompting.
type fallbackPromptKey struct{}

// WithFallbackPrompting annotates a context to signal that the permission check
// should prompt the user interactively instead of auto-classifying.
// The SDK consumer's CanUseTool callback should check IsFallbackPrompting(ctx)
// and present an interactive prompt when true.
// Mirrors permissions.ts:879-901 (auto mode → default mode degradation).
func WithFallbackPrompting(ctx context.Context) context.Context {
	return context.WithValue(ctx, fallbackPromptKey{}, true)
}

// IsFallbackPrompting returns true if the context has been annotated with the
// fallback-to-prompting signal. SDK consumers should check this in their
// CanUseTool callback to decide whether to auto-classify or prompt the user.
func IsFallbackPrompting(ctx context.Context) bool {
	v, _ := ctx.Value(fallbackPromptKey{}).(bool)
	return v
}

// hookAllowedKey is the context key signaling that a pre-tool hook approved
// this tool call. The permission checker should still evaluate deny rules
// but may skip the interactive prompt (inc-4788 invariant).
type hookAllowedKey struct{}

// WithHookAllowed annotates a context to signal that a pre-tool hook has
// approved this tool call. Rule-based deny decisions still take precedence,
// but interactive prompting may be skipped.
// Mirrors TS resolveHookPermissionDecision: hook allow ≠ bypass deny rules.
func WithHookAllowed(ctx context.Context) context.Context {
	return context.WithValue(ctx, hookAllowedKey{}, true)
}

// IsHookAllowed returns true if a pre-tool hook has approved this tool call.
// Permission checkers should still evaluate deny rules when this is true,
// but may skip interactive prompting for "ask" decisions.
func IsHookAllowed(ctx context.Context) bool {
	v, _ := ctx.Value(hookAllowedKey{}).(bool)
	return v
}
