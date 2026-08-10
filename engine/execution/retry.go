package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxRetries is the default number of retry attempts for transient errors.
	// Mirrors withRetry.ts DEFAULT_MAX_RETRIES.
	DefaultMaxRetries = 10

	// BaseDelayMS is the initial backoff delay in milliseconds.
	// Mirrors withRetry.ts BASE_DELAY_MS.
	BaseDelayMS = 500

	// Max529Retries is the legacy same-route overload ceiling retained by the
	// canonical attempt coordinator.
	Max529Retries = 3

	// PersistentMaxBackoffMS is the maximum backoff for persistent retry mode (5 minutes).
	PersistentMaxBackoffMS = 5 * 60 * 1000

	// PersistentResetCapMS is the total retry duration cap for persistent mode (6 hours).
	PersistentResetCapMS = 6 * 60 * 60 * 1000
)

// RetryConfig configures the retry wrapper behavior.
// Mirrors withRetry.ts RetryOptions.
type RetryConfig struct {
	MaxRetries                   int
	MaxConsecutiveOverloadErrors int
	BaseDelay                    time.Duration // Override base delay (for testing); 0 uses BaseDelayMS.
	Budget                       *ModelAttemptBudget
	BeforeDispatch               func(context.Context, int) error
}

// RetryWaitInfo describes a retry wait event for callers to surface to the user.
type RetryWaitInfo struct {
	Error   error
	Attempt int
	Delay   time.Duration
	Is529   bool
	Is429   bool
}

var (
	ErrProviderCallBudgetExhausted = errors.New(
		"model provider-call budget exhausted",
	)
	ErrModelAttemptDeadlineExceeded = errors.New(
		"model attempt deadline exceeded",
	)
)

// ModelFailureClass is the bounded project-owned failure taxonomy used by
// retry, failover, and safe runtime traces.
type ModelFailureClass string

const (
	ModelFailureUnknown              ModelFailureClass = "unknown"
	ModelFailureOverloaded           ModelFailureClass = "overloaded"
	ModelFailureRateLimited          ModelFailureClass = "rate_limited"
	ModelFailureTimeout              ModelFailureClass = "timeout"
	ModelFailureTransportUnavailable ModelFailureClass = "transport_unavailable"
	ModelFailureAuthentication       ModelFailureClass = "authentication"
	ModelFailureAuthorization        ModelFailureClass = "authorization"
	ModelFailureInvalidRequest       ModelFailureClass = "invalid_request"
	ModelFailurePolicyRejected       ModelFailureClass = "policy_rejected"
	ModelFailureContextTooLong       ModelFailureClass = "context_too_long"
	ModelFailureCancelled            ModelFailureClass = "cancelled"
	ModelFailureUsageAmbiguous       ModelFailureClass = "usage_ambiguous"
	ModelFailureBudgetExhausted      ModelFailureClass = "budget_exhausted"
)

// ModelAttemptBudget owns one logical request's provider-call count and
// absolute deadline. It is shared by every same-route retry and profile
// attempt; candidate admission never touches it.
type ModelAttemptBudget struct {
	mu               sync.Mutex
	maxProviderCalls int
	providerCalls    int
	deadline         time.Time
	now              func() time.Time
	wait             func(context.Context, time.Duration) error
}

// NewModelAttemptBudget creates one shared budget. Non-positive calls or
// elapsed durations produce an already-exhausted budget.
func NewModelAttemptBudget(
	maxProviderCalls int,
	maxElapsed time.Duration,
) *ModelAttemptBudget {
	now := time.Now
	return &ModelAttemptBudget{
		maxProviderCalls: maxProviderCalls,
		deadline:         now().Add(maxElapsed),
		now:              now,
		wait:             waitForRetry,
	}
}

// ProviderCalls returns the number of actual dispatch reservations.
func (b *ModelAttemptBudget) ProviderCalls() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.providerCalls
}

// ReserveProviderCall consumes exactly one dispatch reservation.
func (b *ModelAttemptBudget) ReserveProviderCall(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.now().Before(b.deadline) {
		return ErrModelAttemptDeadlineExceeded
	}
	if b.maxProviderCalls <= 0 ||
		b.providerCalls >= b.maxProviderCalls {
		return ErrProviderCallBudgetExhausted
	}
	b.providerCalls++
	return nil
}

func (b *ModelAttemptBudget) waitRetry(
	ctx context.Context,
	delay time.Duration,
) error {
	if b == nil {
		return waitForRetry(ctx, delay)
	}
	b.mu.Lock()
	remaining := b.deadline.Sub(b.now())
	wait := b.wait
	b.mu.Unlock()
	if remaining <= 0 {
		return ErrModelAttemptDeadlineExceeded
	}
	if delay > remaining {
		delay = remaining
	}
	if err := wait(ctx, delay); err != nil {
		return err
	}
	b.mu.Lock()
	expired := !b.now().Before(b.deadline)
	b.mu.Unlock()
	if expired {
		return ErrModelAttemptDeadlineExceeded
	}
	return nil
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CallModelWithRetry wraps a model call with exponential backoff retry logic.
// Mirrors withRetry.ts:withRetry generator.
func CallModelWithRetry(
	ctx context.Context,
	config RetryConfig,
	operation func(ctx context.Context, attempt int) (*CallModelResult, error),
	onRetryWait func(info RetryWaitInfo),
) (*CallModelResult, error) {
	maxRetries := config.MaxRetries
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}
	if envMax := os.Getenv("CLAUDE_CODE_MAX_RETRIES"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n >= 0 {
			maxRetries = n
		}
	}

	persistent := isPersistentRetryEnabled()
	consecutive529 := 0
	totalElapsed := time.Duration(0)

	for attempt := 0; ; attempt++ {
		if config.BeforeDispatch != nil {
			if err := config.BeforeDispatch(ctx, attempt); err != nil {
				return nil, err
			}
		}
		result, err := operation(ctx, attempt)
		if err == nil {
			return result, nil
		}
		if IsProviderUsageTerminalError(err) {
			return nil, err
		}

		// Context cancelled — abort immediately
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Classify the error
		overloaded := IsOverloadedError(err)
		rateLimit := IsRateLimitError(err)
		transient := overloaded || rateLimit

		if !transient {
			// Non-transient errors are not retried
			return nil, err
		}

		// Track consecutive 529s
		if overloaded {
			consecutive529++
			if config.MaxConsecutiveOverloadErrors > 0 &&
				consecutive529 >= config.MaxConsecutiveOverloadErrors {
				return nil, err
			}
		} else {
			// 429 resets the consecutive 529 counter
			consecutive529 = 0
		}

		// Check retry budget
		if !persistent && attempt >= maxRetries {
			return nil, err
		}

		// Persistent mode cap
		if persistent && totalElapsed >= time.Duration(PersistentResetCapMS)*time.Millisecond {
			return nil, err
		}

		// Calculate backoff delay
		delay := RetryDelay(attempt, persistent, config.BaseDelay)

		// Notify caller about the wait (for UI display)
		if onRetryWait != nil {
			onRetryWait(RetryWaitInfo{
				Error:   err,
				Attempt: attempt,
				Delay:   delay,
				Is529:   overloaded,
				Is429:   rateLimit,
			})
		}

		if err := config.Budget.waitRetry(ctx, delay); err != nil {
			return nil, err
		}

		totalElapsed += delay
	}
}

// RetryDelay calculates the project-owned retry delay used by both the legacy
// query loop and project Graph kernel. It mirrors withRetry.ts
// getRetryDelay.
func RetryDelay(attempt int, persistent bool, baseDelay time.Duration) time.Duration {
	base := float64(BaseDelayMS)
	if baseDelay > 0 {
		base = float64(baseDelay / time.Millisecond)
	}
	exp := math.Pow(2, float64(attempt))
	delayMs := base * exp

	// Cap the delay
	maxDelay := float64(60 * 1000) // 60 seconds default cap
	if persistent {
		maxDelay = float64(PersistentMaxBackoffMS)
	}
	if delayMs > maxDelay {
		delayMs = maxDelay
	}

	// Add jitter: ±25%
	jitter := delayMs * 0.25 * (2*rand.Float64() - 1)
	delayMs += jitter

	if delayMs < 0 {
		delayMs = float64(BaseDelayMS)
	}

	return time.Duration(delayMs) * time.Millisecond
}

// isPersistentRetryEnabled checks if unattended/persistent retry mode is active.
// Mirrors withRetry.ts isPersistentRetryEnabled.
func isPersistentRetryEnabled() bool {
	v := strings.TrimSpace(os.Getenv("CLAUDE_CODE_UNATTENDED_RETRY"))
	return v == "1" || strings.EqualFold(v, "true")
}

// IsOverloadedError detects a 529 overloaded error from the error chain.
// Checks both HTTP status code presence and "overloaded_error" type in the message.
// The anthropic SDK's internal error type cannot be imported directly,
// so we use string matching on the error message which has a stable format.
func IsOverloadedError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "overloaded_error") ||
		strings.Contains(msg, ": 529 ")
}

// IsRateLimitError detects a 429 rate limit error from the error chain.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "rate_limit_error") ||
		strings.Contains(msg, ": 429 ")
}

// IsTransientAPIError returns true if the error is a transient API error
// that should be retried (429 or 529).
func IsTransientAPIError(err error) bool {
	return IsOverloadedError(err) || IsRateLimitError(err)
}

// ClassifyModelFailure maps one error to the bounded project taxonomy. Only
// the overloaded class is failover-eligible; all broad compatibility matches
// remain terminal.
func ClassifyModelFailure(err error) ModelFailureClass {
	if err == nil {
		return ModelFailureUnknown
	}
	switch {
	case IsProviderUsageTerminalError(err):
		return ModelFailureUsageAmbiguous
	case errors.Is(err, ErrProviderCallBudgetExhausted),
		errors.Is(err, ErrModelAttemptDeadlineExceeded):
		return ModelFailureBudgetExhausted
	case errors.Is(err, context.Canceled):
		return ModelFailureCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return ModelFailureTimeout
	case IsOverloadedError(err):
		return ModelFailureOverloaded
	case IsRateLimitError(err):
		return ModelFailureRateLimited
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "authentication"),
		strings.Contains(message, "unauthorized"),
		strings.Contains(message, "invalid api key"):
		return ModelFailureAuthentication
	case strings.Contains(message, "forbidden"),
		strings.Contains(message, "authorization"):
		return ModelFailureAuthorization
	case strings.Contains(message, "context length"),
		strings.Contains(message, "context too long"):
		return ModelFailureContextTooLong
	case strings.Contains(message, "invalid_request"),
		strings.Contains(message, "unsupported parameter"):
		return ModelFailureInvalidRequest
	case strings.Contains(message, "content policy"),
		strings.Contains(message, "policy rejection"):
		return ModelFailurePolicyRejected
	case strings.Contains(message, "connection refused"),
		strings.Contains(message, "connection reset"),
		strings.Contains(message, "no such host"):
		return ModelFailureTransportUnavailable
	default:
		return ModelFailureUnknown
	}
}

// FormatRetryMessage creates a user-facing message about the retry state.
func FormatRetryMessage(info RetryWaitInfo) string {
	if info.Is529 {
		return fmt.Sprintf("Server overloaded (attempt %d), retrying in %s...",
			info.Attempt+1, info.Delay.Round(time.Millisecond))
	}
	if info.Is429 {
		return fmt.Sprintf("Rate limited (attempt %d), retrying in %s...",
			info.Attempt+1, info.Delay.Round(time.Millisecond))
	}
	return fmt.Sprintf("Transient error (attempt %d), retrying in %s...",
		info.Attempt+1, info.Delay.Round(time.Millisecond))
}
