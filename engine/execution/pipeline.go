package execution

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// DefaultMaxToolConcurrency is the default limit for parallel tool execution.
// Mirrors the reference's CLAUDE_CODE_MAX_TOOL_USE_CONCURRENCY default (10).
const DefaultMaxToolConcurrency = 8

// ToolBatchConfig configures a tool batch execution pipeline.
type ToolBatchConfig struct {
	// MaxConcurrency limits how many tools run in parallel. Default: 8.
	MaxConcurrency int

	// CancelOnBashError when true causes sibling tools to be cancelled when
	// a Bash tool returns an error. Mirrors the reference behavior where only
	// Bash errors cascade (Read/WebFetch failures are independent).
	CancelOnBashError bool

	// NormalizationConfig controls result normalization behavior.
	NormalizationConfig ResultNormalizationConfig

	// PreToolHook is called before each tool begins execution.
	// The hook receives the tool name and tool call ID.
	// If it returns a non-nil error, that error message becomes the tool result
	// and execution is skipped. Hook panics are recovered and logged.
	PreToolHook func(ctx context.Context, toolName, toolCallID string) error

	// PostToolHook is called after each tool completes execution.
	// It receives the tool name, tool call ID, the result, and whether it errored.
	// Hook panics are recovered. Hook errors do not prevent the result from being
	// returned (they are reported via OnHookError if configured).
	PostToolHook func(ctx context.Context, toolName, toolCallID, result string, isError bool)

	// OnHookError is called when a hook panics or returns an unexpected error.
	// If nil, hook errors are silently swallowed.
	OnHookError func(hookPhase, toolName string, err error)
}

// ToolBatchItem represents a single tool call in a batch to be executed.
type ToolBatchItem struct {
	ToolCall *schema.ToolCall
	// Execute is the function that runs the tool. It receives a context that
	// will be cancelled if sibling cancellation fires.
	Execute func(ctx context.Context) (string, error)
}

// ToolBatchResult holds the result of a single tool execution in a batch.
type ToolBatchResult struct {
	ToolCallID string
	ToolName   string
	// Normalized is the normalized result of tool execution.
	Normalized NormalizedResult
	// Cancelled indicates the tool was cancelled due to a sibling error.
	Cancelled bool
	// CancelReason is set when Cancelled is true.
	CancelReason string
}

// ExecuteToolBatch runs a batch of tool calls with concurrency control, sibling
// cancellation, result normalization, and hook ordering. Results are returned
// in the same order as the input items.
//
// Concurrency control: at most config.MaxConcurrency tools run simultaneously.
// Sibling cancellation: when CancelOnBashError is true and a Bash tool errors,
// the shared context is cancelled and remaining tools receive synthetic errors.
// Result normalization: all results pass through NormalizeToolResult.
// Hook ordering: PreToolHook fires before execution; PostToolHook fires after.
//
// This function blocks until all tools in the batch have completed or been
// cancelled.
func ExecuteToolBatch(ctx context.Context, items []ToolBatchItem, config ToolBatchConfig) []ToolBatchResult {
	if len(items) == 0 {
		return nil
	}

	// Apply defaults.
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = DefaultMaxToolConcurrency
	}
	if config.NormalizationConfig.MaxResultSize <= 0 {
		config.NormalizationConfig = DefaultResultNormalizationConfig()
	}

	results := make([]ToolBatchResult, len(items))

	// For a single item, skip all concurrency machinery.
	if len(items) == 1 {
		results[0] = executeSingleTool(ctx, items[0], config)
		return results
	}

	// Create a shared context for sibling cancellation.
	batchCtx, batchCancel := context.WithCancel(ctx)
	defer batchCancel()

	// Semaphore for concurrency limiting (buffered channel).
	sem := make(chan struct{}, config.MaxConcurrency)

	var wg sync.WaitGroup
	var cancelOnce sync.Once
	cancelReason := ""
	var cancelReasonMu sync.Mutex

	for i, item := range items {

		wg.Add(1)

		go func() {
			defer wg.Done()

			// Acquire concurrency slot (or bail if context cancelled).
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-batchCtx.Done():
				// Context cancelled before we got a slot — this is sibling cancellation.
				cancelReasonMu.Lock()
				reason := cancelReason
				cancelReasonMu.Unlock()
				results[i] = ToolBatchResult{
					ToolCallID:   toolCallIDFromItem(item),
					ToolName:     toolNameFromItem(item),
					Cancelled:    true,
					CancelReason: reason,
					Normalized: NormalizedResult{
						Content: fmt.Sprintf("Cancelled: %s", reason),
						IsError: true,
					},
				}
				return
			}

			// Check context again after acquiring slot.
			if batchCtx.Err() != nil {
				cancelReasonMu.Lock()
				reason := cancelReason
				cancelReasonMu.Unlock()
				results[i] = ToolBatchResult{
					ToolCallID:   toolCallIDFromItem(item),
					ToolName:     toolNameFromItem(item),
					Cancelled:    true,
					CancelReason: reason,
					Normalized: NormalizedResult{
						Content: fmt.Sprintf("Cancelled: %s", reason),
						IsError: true,
					},
				}
				return
			}

			results[i] = executeSingleTool(batchCtx, item, config)

			// Sibling cancellation: if this was a Bash error and CancelOnBashError is enabled.
			if config.CancelOnBashError && results[i].Normalized.IsError && results[i].ToolName == "Bash" {
				cancelOnce.Do(func() {
					cancelReasonMu.Lock()
					cancelReason = fmt.Sprintf("parallel tool call %s errored", describeToolCall(item))
					cancelReasonMu.Unlock()
					batchCancel()
				})
			}
		}()
	}

	wg.Wait()
	return results
}

// executeSingleTool runs a single tool with hooks and normalization.
func executeSingleTool(ctx context.Context, item ToolBatchItem, config ToolBatchConfig) ToolBatchResult {
	toolName := toolNameFromItem(item)
	toolCallID := toolCallIDFromItem(item)

	// Pre-tool hook.
	if config.PreToolHook != nil {
		if err := safeCallPreHook(ctx, config, toolName, toolCallID); err != nil {
			normalized := NormalizeToolResult(toolName, err.Error(), true, config.NormalizationConfig)
			return ToolBatchResult{
				ToolCallID: toolCallID,
				ToolName:   toolName,
				Normalized: normalized,
			}
		}
	}

	// Execute the tool.
	var result string
	var execErr error
	if item.Execute != nil {
		result, execErr = item.Execute(ctx)
	} else {
		execErr = fmt.Errorf("no executor provided for tool %s", toolName)
	}

	isError := execErr != nil
	rawResult := result
	if isError {
		rawResult = execErr.Error()
	}

	// Normalize the result.
	normalized := NormalizeToolResult(toolName, rawResult, isError, config.NormalizationConfig)

	// Post-tool hook.
	if config.PostToolHook != nil {
		safeCallPostHook(ctx, config, toolName, toolCallID, normalized.Content, normalized.IsError)
	}

	return ToolBatchResult{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Normalized: normalized,
	}
}

// safeCallPreHook calls the pre-tool hook with panic recovery.
func safeCallPreHook(ctx context.Context, config ToolBatchConfig, toolName, toolCallID string) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("pre-tool hook panicked for %s: %v", toolName, r)
			if config.OnHookError != nil {
				config.OnHookError("PreToolUse", toolName, err)
			}
			// Hook panic does NOT block execution — just report.
			retErr = nil
		}
	}()
	return config.PreToolHook(ctx, toolName, toolCallID)
}

// safeCallPostHook calls the post-tool hook with panic recovery.
func safeCallPostHook(ctx context.Context, config ToolBatchConfig, toolName, toolCallID, result string, isError bool) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("post-tool hook panicked for %s: %v", toolName, r)
			if config.OnHookError != nil {
				config.OnHookError("PostToolUse", toolName, err)
			}
		}
	}()
	config.PostToolHook(ctx, toolName, toolCallID, result, isError)
}

// toolCallIDFromItem extracts the tool call ID from a batch item.
func toolCallIDFromItem(item ToolBatchItem) string {
	if item.ToolCall != nil {
		return item.ToolCall.ID
	}
	return ""
}

// toolNameFromItem extracts the tool name from a batch item.
func toolNameFromItem(item ToolBatchItem) string {
	if item.ToolCall != nil {
		return item.ToolCall.Function.Name
	}
	return ""
}

// describeToolCall creates a short human-readable description of a tool call
// for use in cancellation messages. Mirrors TS getToolDescription().
func describeToolCall(item ToolBatchItem) string {
	if item.ToolCall == nil {
		return "unknown"
	}
	name := item.ToolCall.Function.Name
	if name == "" {
		return "unknown"
	}
	return name
}
