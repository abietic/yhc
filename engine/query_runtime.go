package engine

import (
	"github.com/abietic/yhc/engine/attachments"
	"github.com/abietic/yhc/engine/budget"
	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/prefetch"
)

// canonicalQueryRuntime owns the live, per-invocation ProjectGraph resources.
// It must never be copied into Graph local state or a durable checkpoint.
type canonicalQueryRuntime struct {
	params               QueryParams
	deps                 *QueryDeps
	consumedCommandUUIDs *[]string
	beforeModelRound     func(*ToolUseContext) error

	hookExecutor        *hooks.Executor
	attachmentProcessor *attachments.Processor
	memoryPrefetch      *prefetch.MemoryPrefetch
	tokenBudget         *budget.TokenBudget
	state               *QueryState
	taskBudgetRemaining *int
	compactTracking     *compact.CompactTracking
	turnTracker         *TurnTracker
	recoveryManager     *RecoveryManager
	cancellationChain   *CancellationChain
	eventValidator      *EventOrderValidator
	yield               func(QueryEvent)

	prepared canonicalRoundPreparationResult
	model    canonicalModelRoundResult
	tool     canonicalToolRoundResult
	terminal *Terminal

	// A targeted durable decision reconstructs only the interrupted tool node.
	// Later tool nodes in the same invocation must keep the history accumulated
	// after resume instead of resetting to the submit-time message snapshot.
	projectGraphResumeRestored bool
}

func newCanonicalQueryRuntime(
	params QueryParams,
	deps *QueryDeps,
	consumedCommandUUIDs *[]string,
	yield func(QueryEvent),
) *canonicalQueryRuntime {
	hookExecutor := params.HookExecutor
	if hookExecutor == nil {
		hookExecutor = hooks.NewExecutor()
	}
	memoryPrefetch := prefetch.NewMemoryPrefetch(params.MemoryStore, 2000)
	memoryPrefetch.Start(params.Messages)
	tokenBudget := params.TokenBudgetTracker
	if tokenBudget == nil {
		tokenBudget = budget.NewTokenBudget(200000)
	}
	maxTurns := 0
	if params.MaxTurns != nil {
		maxTurns = *params.MaxTurns
	}
	eventValidator := NewEventOrderValidator()
	debugEventValidator.Store(eventValidator)
	originalYield := yield
	if originalYield == nil {
		originalYield = func(QueryEvent) {}
	}
	validatedYield := func(event QueryEvent) {
		eventValidator.Observe(event)
		originalYield(event)
	}
	if consumedCommandUUIDs == nil {
		empty := make([]string, 0)
		consumedCommandUUIDs = &empty
	}
	return &canonicalQueryRuntime{
		params:               params,
		deps:                 deps,
		consumedCommandUUIDs: consumedCommandUUIDs,
		hookExecutor:         hookExecutor,
		attachmentProcessor:  attachments.NewProcessor(),
		memoryPrefetch:       memoryPrefetch,
		tokenBudget:          tokenBudget,
		state: &QueryState{
			Messages:                params.Messages,
			ToolUseContext:          params.ToolUseContext,
			TurnCount:               1,
			MaxOutputTokensOverride: params.MaxOutputTokensOverride,
		},
		turnTracker:     NewTurnTracker(maxTurns),
		recoveryManager: NewRecoveryManager(),
		eventValidator:  eventValidator,
		yield:           validatedYield,
	}
}
