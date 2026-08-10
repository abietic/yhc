package engine

import (
	"context"
	"fmt"
)

type fixtureQueryKernelContextKey struct{}

type queryKernelKind string

const (
	queryKernelProjectGraph queryKernelKind = "project_graph"
)

type queryKernelRequest struct {
	params               QueryParams
	deps                 *QueryDeps
	consumedCommandUUIDs *[]string
	beforeModelRound     func(*ToolUseContext) error
	yield                func(QueryEvent)
}

// queryKernel is the internal boundary for the single ProjectGraph execution
// owner. Session metadata may retain retired kernel identities for fail-closed
// compatibility diagnostics, but those identities never select an executor.
type queryKernel interface {
	kind() queryKernelKind
	run(context.Context, queryKernelRequest) Terminal
}

type unavailableProjectGraphQueryKernel struct {
	err error
}

func (unavailableProjectGraphQueryKernel) kind() queryKernelKind {
	return queryKernelProjectGraph
}

func (kernel unavailableProjectGraphQueryKernel) run(
	_ context.Context,
	_ queryKernelRequest,
) Terminal {
	return Terminal{
		Reason: TerminalModelError,
		Err: fmt.Errorf(
			"engine: compile production project graph query kernel: %w",
			kernel.err,
		),
	}
}

func productionQueryKernel() queryKernel {
	kernel, err := productionProjectGraphQueryKernel()
	if err != nil {
		return unavailableProjectGraphQueryKernel{err: err}
	}
	return kernel
}

// withFixtureQueryKernel is package-private deterministic test plumbing. It
// does not add a QueryEngine configuration surface or permit a production
// session to switch kernels.
func withFixtureQueryKernel(
	ctx context.Context,
	kernel queryKernel,
) context.Context {
	return context.WithValue(ctx, fixtureQueryKernelContextKey{}, kernel)
}

func fixtureQueryKernelFromContext(ctx context.Context) queryKernel {
	if ctx == nil {
		return nil
	}
	kernel, _ := ctx.Value(fixtureQueryKernelContextKey{}).(queryKernel)
	return kernel
}
