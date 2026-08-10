package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// ProviderUsageDescriptor names one logical model round before an actual
// provider attempt is admitted. The root Goal service supplies the durable
// provider-call identity.
type ProviderUsageDescriptor struct {
	LogicalRoundID    string
	LogicalRequestID  string
	ModelAttemptID    string
	ModelAttemptIndex int
	ModelRetryIndex   int
	Model             string
	QuerySource       string
	ModelRole         string
	ModelProfile      string
	ReasoningEffort   string
}

// ProviderUsageAdmitter is the narrow provider-facing capability exposed by a
// root Goal. It cannot change Goal budget, status, objective, or continuation.
type ProviderUsageAdmitter interface {
	NewLogicalRoundID() string
	AdmitProviderUsage(context.Context, ProviderUsageDescriptor) (ProviderUsageCall, error)
}

type providerUsageScopeKey struct{}

type providerUsageScope struct {
	admitter ProviderUsageAdmitter
	required bool
}

// WithProviderUsageScope carries a narrow Goal provider-usage capability into
// helper/tool code. required=true means a provider call must fail before
// dispatch when no capability is available.
func WithProviderUsageScope(
	ctx context.Context,
	admitter ProviderUsageAdmitter,
	required bool,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if admitter == nil && !required {
		return ctx
	}
	return context.WithValue(ctx, providerUsageScopeKey{}, providerUsageScope{
		admitter: admitter,
		required: required,
	})
}

// ProviderUsageScopeFromContext returns the Goal accounting capability and
// whether provider entry is forbidden without it.
func ProviderUsageScopeFromContext(
	ctx context.Context,
) (ProviderUsageAdmitter, bool) {
	if ctx == nil {
		return nil, false
	}
	scope, _ := ctx.Value(providerUsageScopeKey{}).(providerUsageScope)
	return scope.admitter, scope.required
}

// ProviderUsageCall owns one durable provider admission until it is finalized,
// proven not dispatched, or marked ambiguous.
type ProviderUsageCall interface {
	ProviderCallID() string
	CompleteProviderUsage(*schema.TokenUsage) error
	ReleaseProviderUsageBeforeDispatch() error
	MarkProviderUsageAmbiguous(error) error
}

// ProviderUsageTerminalError prevents retry/fallback from issuing a second
// provider call after accounting became ambiguous or failed closed.
type ProviderUsageTerminalError struct {
	Err error
}

func (e *ProviderUsageTerminalError) Error() string {
	if e == nil || e.Err == nil {
		return "provider usage accounting failed closed"
	}
	return "provider usage accounting failed closed: " + e.Err.Error()
}

func (e *ProviderUsageTerminalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsProviderUsageTerminalError reports whether retry/fallback must stop even
// when the underlying provider error otherwise resembles a transient error.
func IsProviderUsageTerminalError(err error) bool {
	var terminal *ProviderUsageTerminalError
	return errors.As(err, &terminal)
}

// CompleteProviderUsage extracts the one final cumulative usage snapshot
// produced by ProcessStream and commits it through the admitted Goal call.
func CompleteProviderUsage(
	call ProviderUsageCall,
	messages []*schema.Message,
) error {
	if call == nil {
		return nil
	}
	var usage *schema.TokenUsage
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
			copied := *message.ResponseMeta.Usage
			usage = &copied
		}
		break
	}
	if err := call.CompleteProviderUsage(usage); err != nil {
		return &ProviderUsageTerminalError{Err: err}
	}
	return nil
}

// MarkProviderUsageAmbiguous fails closed after provider dispatch may have
// occurred but no exact final usage record can be committed.
func MarkProviderUsageAmbiguous(call ProviderUsageCall, cause error) error {
	if call == nil {
		return cause
	}
	accountingErr := call.MarkProviderUsageAmbiguous(cause)
	return &ProviderUsageTerminalError{
		Err: errors.Join(cause, accountingErr),
	}
}

// ReleaseProviderUsageBeforeDispatch clears only a proven non-dispatched
// admission, preserving the caller's original cancellation/error.
func ReleaseProviderUsageBeforeDispatch(
	call ProviderUsageCall,
	cause error,
) error {
	if call == nil {
		return cause
	}
	if err := call.ReleaseProviderUsageBeforeDispatch(); err != nil {
		return &ProviderUsageTerminalError{
			Err: errors.Join(cause, fmt.Errorf("release pre-dispatch admission: %w", err)),
		}
	}
	return cause
}
