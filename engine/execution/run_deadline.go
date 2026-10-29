package execution

import (
	"context"
	"time"
)

type runDeadlineKey struct{}

// WithRunDeadline declares an invocation-wide cutoff. Unlike the foreground
// wait's cancellation lease, it survives context.WithoutCancel when a child
// detaches. Zero adds no limit; nested declarations may only shorten it.
func WithRunDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if previous, ok := ctx.Value(runDeadlineKey{}).(time.Time); ok && (deadline.IsZero() || previous.Before(deadline)) {
		deadline = previous
	}
	if deadline.IsZero() {
		return ctx, func() {}
	}
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	ctx = context.WithValue(ctx, runDeadlineKey{}, deadline)
	return BindRunDeadline(ctx)
}

// BindRunDeadline reapplies the invocation cutoff at a child query boundary.
// Ordinary unmarked parent cancellation and detach semantics remain unchanged.
func BindRunDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Value(runDeadlineKey{}).(time.Time)
	if !ok {
		return ctx, func() {}
	}
	if current, ok := ctx.Deadline(); ok && !current.After(deadline) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline)
}
