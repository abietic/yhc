package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallModelWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	result, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			return &CallModelResult{Model: "claude-sonnet-4-20250514"}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if result.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("unexpected model: %s", result.Model)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestCallModelWithRetry_SuccessAfterTransientError(t *testing.T) {
	calls := 0
	result, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{MaxRetries: 5, BaseDelay: time.Millisecond},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			if attempt < 2 {
				return nil, fmt.Errorf("POST \"https://api.anthropic.com/v1/messages\": 429 Too Many Requests")
			}
			return &CallModelResult{Model: "claude-sonnet-4-20250514"}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if result == nil {
		t.Fatal("expected non-nil result")
		return
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestCallModelWithRetry_MaxRetriesExhausted(t *testing.T) {
	calls := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			return nil, fmt.Errorf("POST \"url\": 429 Too Many Requests rate_limit_error")
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
		return
	}
	// MaxRetries=3 means attempts 0,1,2,3 = 4 total calls
	if calls != 4 {
		t.Fatalf("expected 4 calls (initial + 3 retries), got %d", calls)
	}
}

func TestCallModelWithRetry_Consecutive529ReturnsOverloadToCoordinator(
	t *testing.T,
) {
	calls := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries:                   10,
			MaxConsecutiveOverloadErrors: Max529Retries,
			BaseDelay:                    time.Millisecond,
		},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			return nil, fmt.Errorf("model stream: POST \"url\": 529 overloaded_error")
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if ClassifyModelFailure(err) != ModelFailureOverloaded {
		t.Fatalf("failure class = %q, want overloaded", ClassifyModelFailure(err))
	}
	if calls != Max529Retries {
		t.Fatalf("expected %d calls before coordinator decision, got %d", Max529Retries, calls)
	}
}

func TestCallModelWithRetry_NoCoordinatorCeilingExhaustsRetries(t *testing.T) {
	calls := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries: 3,
			BaseDelay:  time.Millisecond,
		},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			return nil, fmt.Errorf("overloaded_error 529")
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if ClassifyModelFailure(err) != ModelFailureOverloaded {
		t.Fatalf("failure class = %q, want overloaded", ClassifyModelFailure(err))
	}
	if calls != 4 { // initial + 3 retries
		t.Fatalf("expected 4 calls, got %d", calls)
	}
}

func TestCallModelWithRetry_429Resets529Counter(t *testing.T) {
	calls := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries:                   10,
			MaxConsecutiveOverloadErrors: Max529Retries,
			BaseDelay:                    time.Millisecond,
		},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			// Alternate: 529, 529, 429, 529, 529, 429 — never 3 consecutive 529s
			if attempt%3 == 2 {
				return nil, fmt.Errorf("POST \"url\": 429 Too Many Requests rate_limit_error")
			}
			return nil, fmt.Errorf("POST \"url\": 529 overloaded_error")
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if ClassifyModelFailure(err) != ModelFailureOverloaded {
		t.Fatalf("failure class = %q, want overloaded", ClassifyModelFailure(err))
	}
	if calls != 11 { // initial + 10 retries
		t.Fatalf("expected 11 calls, got %d", calls)
	}
}

func TestCallModelWithRetry_NonTransientErrorNotRetried(t *testing.T) {
	calls := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{MaxRetries: 5, BaseDelay: time.Millisecond},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			return nil, fmt.Errorf("invalid_request_error: messages.0.content too long")
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry for non-transient error), got %d", calls)
	}
}

func TestCallModelWithRetry_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := int32(0)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := CallModelWithRetry(
		ctx,
		RetryConfig{MaxRetries: 100, BaseDelay: time.Millisecond},
		func(callCtx context.Context, attempt int) (*CallModelResult, error) {
			atomic.AddInt32(&calls, 1)
			return nil, fmt.Errorf("POST \"url\": 429 rate_limit_error")
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestCallModelWithRetrySharesProviderCallBudget(t *testing.T) {
	budget := NewModelAttemptBudget(2, time.Minute)
	calls := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries: 10,
			BaseDelay:  time.Millisecond,
			Budget:     budget,
		},
		func(ctx context.Context, _ int) (*CallModelResult, error) {
			if err := budget.ReserveProviderCall(ctx); err != nil {
				return nil, err
			}
			calls++
			return nil, errors.New("429 rate_limit_error")
		},
		nil,
	)
	if !errors.Is(err, ErrProviderCallBudgetExhausted) {
		t.Fatalf("error = %v, want provider-call exhaustion", err)
	}
	if calls != 2 || budget.ProviderCalls() != 2 {
		t.Fatalf(
			"operation calls=%d budget calls=%d, want 2/2",
			calls,
			budget.ProviderCalls(),
		)
	}
}

func TestCallModelWithRetryCancellationStopsWaitAndNewDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	budget := NewModelAttemptBudget(6, time.Minute)
	calls := 0
	_, err := CallModelWithRetry(
		ctx,
		RetryConfig{
			MaxRetries: 10,
			BaseDelay:  time.Hour,
			Budget:     budget,
		},
		func(callCtx context.Context, _ int) (*CallModelResult, error) {
			if err := budget.ReserveProviderCall(callCtx); err != nil {
				return nil, err
			}
			calls++
			return nil, errors.New("529 overloaded_error")
		},
		func(RetryWaitInfo) {
			cancel()
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if calls != 1 || budget.ProviderCalls() != 1 {
		t.Fatalf(
			"operation calls=%d budget calls=%d, want 1/1",
			calls,
			budget.ProviderCalls(),
		)
	}
}

func TestModelAttemptBudgetCapsWaitAtAbsoluteDeadline(t *testing.T) {
	base := time.Unix(100, 0)
	now := base
	budget := NewModelAttemptBudget(6, time.Second)
	budget.now = func() time.Time { return now }
	budget.deadline = base.Add(time.Second)
	var waited time.Duration
	budget.wait = func(
		_ context.Context,
		delay time.Duration,
	) error {
		waited = delay
		now = now.Add(delay)
		return nil
	}
	err := budget.waitRetry(context.Background(), 5*time.Second)
	if !errors.Is(err, ErrModelAttemptDeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exhaustion", err)
	}
	if waited != time.Second {
		t.Fatalf("waited = %s, want 1s", waited)
	}
}

func TestP294ClassifyModelFailureTaxonomy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ModelFailureClass
	}{
		{
			name: "overloaded",
			err:  errors.New("POST \"url\": 529 overloaded_error"),
			want: ModelFailureOverloaded,
		},
		{
			name: "rate limited",
			err:  errors.New("POST \"url\": 429 rate_limit_error"),
			want: ModelFailureRateLimited,
		},
		{name: "timeout", err: context.DeadlineExceeded, want: ModelFailureTimeout},
		{
			name: "transport",
			err:  errors.New("dial tcp: connection refused"),
			want: ModelFailureTransportUnavailable,
		},
		{
			name: "authentication",
			err:  errors.New("401 unauthorized: invalid API key"),
			want: ModelFailureAuthentication,
		},
		{
			name: "authorization",
			err:  errors.New("403 forbidden"),
			want: ModelFailureAuthorization,
		},
		{
			name: "invalid request",
			err:  errors.New("invalid_request: unsupported parameter"),
			want: ModelFailureInvalidRequest,
		},
		{
			name: "policy",
			err:  errors.New("content policy rejection"),
			want: ModelFailurePolicyRejected,
		},
		{
			name: "context",
			err:  errors.New("context length exceeds limit"),
			want: ModelFailureContextTooLong,
		},
		{name: "cancelled", err: context.Canceled, want: ModelFailureCancelled},
		{
			name: "usage ambiguous",
			err: &ProviderUsageTerminalError{
				Err: errors.New("settlement failed"),
			},
			want: ModelFailureUsageAmbiguous,
		},
		{
			name: "budget",
			err:  ErrProviderCallBudgetExhausted,
			want: ModelFailureBudgetExhausted,
		},
		{name: "unknown", err: errors.New("opaque failure"), want: ModelFailureUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyModelFailure(test.err); got != test.want {
				t.Fatalf("classification = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCallModelWithRetry_OnRetryWaitCalled(t *testing.T) {
	var waitInfos []RetryWaitInfo
	_, _ = CallModelWithRetry(
		context.Background(),
		RetryConfig{MaxRetries: 2, BaseDelay: 10 * time.Millisecond},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			if attempt < 2 {
				return nil, fmt.Errorf("POST \"url\": 529 overloaded_error")
			}
			return &CallModelResult{Model: "claude-sonnet-4-20250514"}, nil
		},
		func(info RetryWaitInfo) {
			waitInfos = append(waitInfos, info)
		},
	)
	if len(waitInfos) != 2 {
		t.Fatalf("expected 2 wait callbacks, got %d", len(waitInfos))
	}
	for i, info := range waitInfos {
		if !info.Is529 {
			t.Fatalf("waitInfo[%d] expected Is529=true", i)
		}
		if info.Is429 {
			t.Fatalf("waitInfo[%d] expected Is429=false", i)
		}
		if info.Attempt != i {
			t.Fatalf("waitInfo[%d] expected Attempt=%d, got %d", i, i, info.Attempt)
		}
		if info.Delay < time.Millisecond {
			t.Fatalf("waitInfo[%d] expected delay >= 1ms, got %v", i, info.Delay)
		}
	}
	// Second delay should be larger than first (exponential)
	if waitInfos[1].Delay < waitInfos[0].Delay {
		t.Fatalf("expected exponential increase: delay[0]=%v, delay[1]=%v", waitInfos[0].Delay, waitInfos[1].Delay)
	}
}

func TestCallModelWithRetry_PersistentMode(t *testing.T) {
	_ = os.Setenv("CLAUDE_CODE_UNATTENDED_RETRY", "true")
	defer os.Unsetenv("CLAUDE_CODE_UNATTENDED_RETRY") //nolint:errcheck

	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, _ = CallModelWithRetry(
		ctx,
		RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond},
		func(callCtx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			if attempt >= 5 {
				return &CallModelResult{Model: "claude-sonnet-4-20250514"}, nil
			}
			return nil, fmt.Errorf("POST \"url\": 429 rate_limit_error")
		},
		nil,
	)
	// In persistent mode, MaxRetries is ignored — should retry beyond 2
	if calls <= 3 {
		t.Fatalf("persistent mode should retry beyond MaxRetries, got %d calls", calls)
	}
}

func TestCallModelWithRetry_EnvMaxRetries(t *testing.T) {
	_ = os.Setenv("CLAUDE_CODE_MAX_RETRIES", "1")
	defer os.Unsetenv("CLAUDE_CODE_MAX_RETRIES") //nolint:errcheck

	calls := 0
	_, _ = CallModelWithRetry(
		context.Background(),
		RetryConfig{MaxRetries: 10, BaseDelay: time.Millisecond},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			calls++
			return nil, fmt.Errorf("POST \"url\": 429 rate_limit_error")
		},
		nil,
	)
	// ENV overrides config: MaxRetries=1 → 2 calls total
	if calls != 2 {
		t.Fatalf("expected 2 calls with CLAUDE_CODE_MAX_RETRIES=1, got %d", calls)
	}
}

func TestIsOverloadedError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"unrelated", fmt.Errorf("connection refused"), false},
		{"529 status", fmt.Errorf("POST \"url\": 529 Overloaded"), true},
		{"overloaded_error type", fmt.Errorf("model stream: overloaded_error in response"), true},
		{"429 status", fmt.Errorf("POST \"url\": 429 Too Many Requests"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOverloadedError(tt.err); got != tt.expected {
				t.Fatalf("IsOverloadedError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"unrelated", fmt.Errorf("invalid_request_error"), false},
		{"429 status", fmt.Errorf("POST \"url\": 429 Too Many Requests"), true},
		{"rate_limit_error type", fmt.Errorf("model stream: rate_limit_error"), true},
		{"529 status", fmt.Errorf("POST \"url\": 529 Overloaded"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRateLimitError(tt.err); got != tt.expected {
				t.Fatalf("IsRateLimitError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestGetRetryDelay(t *testing.T) {
	// Verify exponential growth with default base delay
	d0 := RetryDelay(0, false, 0)
	d1 := RetryDelay(1, false, 0)
	d2 := RetryDelay(2, false, 0)

	// With jitter, d1 should be roughly 2x d0, d2 roughly 4x d0
	// Allow wide margin for jitter (±25% on each)
	if d0 < 200*time.Millisecond || d0 > 900*time.Millisecond {
		t.Fatalf("attempt 0 delay out of range: %v", d0)
	}
	if d1 < 400*time.Millisecond || d1 > 1800*time.Millisecond {
		t.Fatalf("attempt 1 delay out of range: %v", d1)
	}
	if d2 < 800*time.Millisecond || d2 > 3600*time.Millisecond {
		t.Fatalf("attempt 2 delay out of range: %v", d2)
	}

	// High attempt should be capped at 60s (non-persistent)
	d10 := RetryDelay(10, false, 0)
	if d10 > 80*time.Second { // 60s + 25% jitter
		t.Fatalf("attempt 10 delay should be capped, got: %v", d10)
	}

	// Custom base delay
	d0Custom := RetryDelay(0, false, 10*time.Millisecond)
	if d0Custom > 20*time.Millisecond {
		t.Fatalf("custom base delay should produce small delays, got: %v", d0Custom)
	}
}

func TestFormatRetryMessage(t *testing.T) {
	msg := FormatRetryMessage(RetryWaitInfo{
		Attempt: 2,
		Delay:   2 * time.Second,
		Is529:   true,
	})
	if msg == "" {
		t.Fatal("expected non-empty message")
	}
	if !contains(msg, "overloaded") && !contains(msg, "Overloaded") {
		t.Fatal("expected message to contain overloaded/Overloaded")
	}

	msg429 := FormatRetryMessage(RetryWaitInfo{
		Attempt: 0,
		Delay:   500 * time.Millisecond,
		Is429:   true,
	})
	if msg429 == "" {
		t.Fatal("expected non-empty message for 429")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
