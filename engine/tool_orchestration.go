package engine

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

type toolBatch struct {
	IsConcurrencySafe bool
	ToolCalls         []*schema.ToolCall
}

func partitionToolCalls(toolCalls []*schema.ToolCall, registry *tools.Registry) []toolBatch {
	batches := make([]toolBatch, 0)
	for _, toolCall := range toolCalls {
		isSafe := isToolConcurrencySafe(toolCall, registry)
		if isSafe && len(batches) > 0 && batches[len(batches)-1].IsConcurrencySafe {
			batches[len(batches)-1].ToolCalls = append(batches[len(batches)-1].ToolCalls, toolCall)
			continue
		}
		batches = append(batches, toolBatch{
			IsConcurrencySafe: isSafe,
			ToolCalls:         []*schema.ToolCall{toolCall},
		})
	}
	return batches
}

func isToolConcurrencySafe(toolCall *schema.ToolCall, registry *tools.Registry) (safe bool) {
	if toolCall == nil || registry == nil {
		return false
	}
	impl, ok := registry.Get(toolCall.Function.Name)
	if !ok || impl.IsConcurrencySafe == nil {
		return false
	}
	input, err := parseToolInput(toolCall.Function.Arguments)
	if err != nil {
		return false
	}
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	return impl.IsConcurrencySafe(cloneInputMap(input))
}

func executeToolBatch(
	ctx context.Context,
	params QueryParams,
	hookExecutor *hooks.Executor,
	toolCtx *ToolUseContext,
	batch toolBatch,
	yieldFn func(QueryEvent),
) []*toolExecutionOutcome {
	outcomes := make([]*toolExecutionOutcome, len(batch.ToolCalls))
	if !batch.IsConcurrencySafe || len(batch.ToolCalls) <= 1 {
		for i, toolCall := range batch.ToolCalls {
			outcomes[i] = executeToolCall(ctx, params, hookExecutor, toolCtx, toolCall, yieldFn)
		}
		return outcomes
	}

	limit := maxToolUseConcurrency()
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, toolCall := range batch.ToolCalls {
		wg.Add(1)
		go func(index int, tc *schema.ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			outcomes[index] = executeToolCall(ctx, params, hookExecutor, toolCtx, tc, yieldFn)
		}(i, toolCall)
	}
	wg.Wait()
	return outcomes
}

func maxToolUseConcurrency() int {
	raw := strings.TrimSpace(os.Getenv("CLAUDE_CODE_MAX_TOOL_USE_CONCURRENCY"))
	if raw == "" {
		return 10
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 10
	}
	return parsed
}
