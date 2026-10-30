// Package providercost carries a prepared, single-dispatch provider request.
// Bounds are supplied by the concrete adapter, never by routing labels alone.
package providercost

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var ErrUnsupported = errors.New("provider does not support bounded cost dispatch")

// FinalUsageError carries an authoritative terminal receipt despite a failed
// provider response. Partial stream counters must never implement this contract.
// A nil usage means no complete receipt is available. Callers still return the
// original provider error after accounting for a non-nil receipt.
type FinalUsageError interface {
	error
	FinalProviderUsage() (usage *schema.TokenUsage, responseModel string)
}

type Bound struct {
	Provider     string
	Model        string
	InputTokens  uint64
	OutputTokens uint64
}

// Preparer freezes the final route, tools, options and wire body without HTTP.
// Stream must dispatch that body at most once, without billable hidden retries.
type Preparer interface {
	PrepareBudgetStream(context.Context, []*schema.Message, int, ...model.Option) (Stream, error)
}

type Stream interface {
	CostBound() Bound
	Stream(context.Context) (*schema.StreamReader[*schema.Message], error)
}

type AgenticPreparer interface {
	PrepareBudgetStream(context.Context, []*schema.AgenticMessage, int, ...model.Option) (AgenticStream, error)
}

type AgenticStream interface {
	CostBound() Bound
	Stream(context.Context) (*schema.StreamReader[*schema.AgenticMessage], error)
}
