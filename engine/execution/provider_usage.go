package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/providercost"
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
	Provider          string
	QuerySource       string
	ModelRole         string
	ModelProfile      string
	ReasoningEffort   string
	CostBound         *providercost.Bound
	// Request identifies a standalone, token-free API request. It must settle
	// through CompleteRequestUsage rather than inferred token usage.
	Request bool
}

// RequestUsageAvailable checks whether a paid request can be accounted for.
// An unpriced optional service may use a free fallback without poisoning the
// model run's admission state. Actual admission remains atomic and authoritative.
func RequestUsageAvailable(owner ProviderUsageAdmitter, d ProviderUsageDescriptor) bool {
	if policy, ok := owner.(interface {
		RequestUsageAvailable(ProviderUsageDescriptor) bool
	}); ok {
		return policy.RequestUsageAvailable(d)
	}
	return true
}

// CompleteRequestUsage records one known request, with either one billable unit
// or zero (for example, an empty search). It never invents model token usage.
func CompleteRequestUsage(call ProviderUsageCall, billable bool) error {
	if call == nil {
		return nil
	}
	var err error
	if request, ok := call.(interface{ CompleteRequestUsage(bool) error }); ok {
		err = request.CompleteRequestUsage(billable)
	} else {
		// Durable token-only Goal owners still observe the call and zero tokens.
		err = call.CompleteProviderUsage(&schema.TokenUsage{})
	}
	if err != nil {
		return &ProviderUsageTerminalError{Err: err}
	}
	return nil
}

// ProviderUsageAdmitter is the narrow provider-facing accounting capability.
// Goal and invocation owners may compose it without exposing their state controls.
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

// CompleteProviderUsage extracts the final cumulative usage from a completed
// provider message and commits it through the admitted call.
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
			usage = message.ResponseMeta.Usage
		}
		break
	}
	return completeProviderUsage(call, usage)
}

// CompleteStreamProviderUsage settles the final cumulative stream receipt even
// when the provider produced no assistant content. A stream error must still
// use MarkProviderUsageAmbiguous, which accepts only explicitly authoritative
// terminal-error receipts and never settles partial stream counters.
func CompleteStreamProviderUsage(call ProviderUsageCall, result *StreamResult) error {
	var usage *schema.TokenUsage
	if result != nil {
		usage = result.Usage
	}
	return completeProviderUsage(call, usage)
}

func completeProviderUsage(call ProviderUsageCall, usage *schema.TokenUsage) error {
	if call == nil {
		return nil
	}
	if usage != nil {
		copied := *usage
		usage = &copied
	}
	if err := call.CompleteProviderUsage(usage); err != nil {
		return &ProviderUsageTerminalError{Err: err}
	}
	return nil
}

// MarkProviderUsageAmbiguous settles an explicit final error receipt, retaining
// the provider failure. Without one it fails closed unless the call explicitly
// opts into observation-only ambiguity. Goal owners remain strict by default.
func MarkProviderUsageAmbiguous(call ProviderUsageCall, cause error) error {
	if call == nil {
		return cause
	}
	var receipt providercost.FinalUsageError
	if errors.As(cause, &receipt) {
		if usage, responseModel := receipt.FinalProviderUsage(); usage != nil {
			var cacheReceipt interface{ ProviderCacheWriteReceipt() (int, bool, string) }
			if errors.As(cause, &cacheReceipt) {
				if tokens, known, ttl := cacheReceipt.ProviderCacheWriteReceipt(); known {
					if observer, ok := call.(ProviderCacheWriteObserver); ok {
						observer.ObserveProviderCacheWrite(tokens, ttl)
					}
				}
			}
			if observer, ok := call.(interface{ ObserveProviderResponseModel(string) }); ok {
				observer.ObserveProviderResponseModel(responseModel)
			}
			if err := completeProviderUsage(call, usage); err != nil {
				return errors.Join(cause, err)
			}
			return cause
		}
	}
	accountingErr := call.MarkProviderUsageAmbiguous(cause)
	if policy, ok := call.(interface{ FailClosedOnAmbiguousUsage() bool }); ok && !policy.FailClosedOnAmbiguousUsage() && accountingErr == nil {
		return cause
	}
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
