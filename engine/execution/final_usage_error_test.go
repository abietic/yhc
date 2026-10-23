package execution

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type finalUsageFixtureError struct {
	cause error
	usage *schema.TokenUsage
}

func (e *finalUsageFixtureError) Error() string { return e.cause.Error() }
func (e *finalUsageFixtureError) Unwrap() error { return e.cause }
func (e *finalUsageFixtureError) FinalProviderUsage() (*schema.TokenUsage, string) {
	return e.usage, "deepseek-flash"
}

func TestFinalUsageErrorSettlesReceiptAndPreservesFailure(t *testing.T) {
	cause := errors.New("provider failed")
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			call := &p242bUsageCall{}
			receipt := &finalUsageFixtureError{cause: cause}
			if known {
				receipt.usage = &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22}
			}
			err := MarkProviderUsageAmbiguous(call, fmt.Errorf("stream: %w", receipt))
			if !errors.Is(err, cause) {
				t.Fatalf("original failure was lost: %v", err)
			}
			if known {
				if IsProviderUsageTerminalError(err) || call.completed != 1 || call.ambiguous != 0 || call.usage.TotalTokens != 22 {
					t.Fatalf("known error receipt treated as ambiguous: err=%v call=%+v", err, call)
				}
			} else if !IsProviderUsageTerminalError(err) || call.completed != 0 || call.ambiguous != 1 {
				t.Fatalf("missing receipt did not fail closed: err=%v call=%+v", err, call)
			}
		})
	}
}

func TestFinalUsageErrorStillEnforcesReservedBounds(t *testing.T) {
	run, descriptor := reservationFixture(t, "1")
	call, err := run.AdmitProviderUsage(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("provider failed")
	err = MarkProviderUsageAmbiguous(call, &finalUsageFixtureError{
		cause: cause,
		usage: &schema.TokenUsage{PromptTokens: 21, CompletionTokens: 1, TotalTokens: 22},
	})
	if !errors.Is(err, cause) || !errors.Is(err, ErrRunUsageUnknown) || !IsProviderUsageTerminalError(err) {
		t.Fatalf("bound violation did not preserve both causes: %v", err)
	}
	usage := run.Snapshot()
	// The receipt still establishes token usage. A bound violation makes its
	// price unsafe, not its counters absent; financial exposure stays retained.
	if usage.KnownCalls != 1 || usage.UnknownCalls != 0 || !usage.Complete || usage.Cost.Complete ||
		usage.Cost.Reservation.RetainedAmount != "0.000080000000" || usage.Cost.Reservation.BoundViolations != 1 {
		t.Fatalf("bound violation released failed-request exposure: %+v", usage)
	}
}
