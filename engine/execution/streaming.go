package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// StreamingToolExecutorConfig configures streamed tool execution.
type StreamingToolExecutorConfig struct {
	Execute           func(toolCall *schema.ToolCall) *ToolResult
	IsConcurrencySafe func(toolCall *schema.ToolCall) bool
	MaxConcurrency    int
	// DeferExecution keeps stream commit/reject classification and rejected
	// call results, but leaves committed calls for an external lifecycle owner.
	DeferExecution bool
	// PrepareForExecution runs synchronously in model-observation order before
	// a tool goroutine starts. It may return an execution-only clone carrying
	// admission metadata; implementations must not block.
	PrepareForExecution func(toolCall *schema.ToolCall) *schema.ToolCall
	// GetInterruptBehavior returns "cancel" or "block" for a given tool name.
	// If nil, all tools default to "cancel" on interrupt (current behavior).
	GetInterruptBehavior func(toolName string) string
	// ExecuteWithContext is the context-aware variant of Execute. When set, it
	// is preferred over Execute and receives a context that is cancelled on
	// sibling abort. This enables running tools to detect cancellation early.
	ExecuteWithContext func(ctx context.Context, toolCall *schema.ToolCall) *ToolResult
	// Ctx is the parent context for tool execution. Required for sibling
	// cancellation to propagate. If nil, context.Background() is used.
	Ctx context.Context
	// IsInterrupted is evaluated under the scheduler lock when selecting the
	// winning result. Cancel-behavior tools receive a synthetic interruption
	// even when a non-cooperative executor returns success before the
	// cancellation callback acquires that lock. If nil, Ctx.Err is used.
	IsInterrupted func() bool
	// OnInterrupt synchronously settles an engine-owned interaction before a
	// cancel-behavior tool receives its synthetic interruption result.
	OnInterrupt func(toolCall *schema.ToolCall)
}

// StreamingToolExecutor tracks tool_use blocks observed during streaming. It
// releases the complete set only after ProcessStream commits the model turn,
// then preserves the existing scheduler and yielded-result ordering.
type StreamingToolExecutor struct {
	mu             sync.Mutex
	cond           *sync.Cond
	tools          []*trackedTool
	index          map[string]int
	discard        bool
	commitState    toolCommitState
	execute        func(toolCall *schema.ToolCall) *ToolResult
	executeWithCtx func(ctx context.Context, toolCall *schema.ToolCall) *ToolResult
	prepare        func(toolCall *schema.ToolCall) *schema.ToolCall
	isSafe         func(toolCall *schema.ToolCall) bool
	maxConcurrency int
	deferExecution bool
	// bashErrored tracks whether a Bash tool has errored, triggering sibling abort.
	// Only Bash errors cascade to cancel queued siblings (mirrors TS StreamingToolExecutor.ts:359).
	bashErrored bool
	// getInterruptBehavior returns "cancel" or "block" for a tool.
	// "block" tools complete naturally on interrupt; "cancel" tools get synthesized.
	getInterruptBehavior func(toolName string) string
	isInterrupted        func() bool
	onInterrupt          func(toolCall *schema.ToolCall)
	// siblingCtx is cancelled when a Bash sibling errors. All executing tools
	// share this context so they can detect sibling cancellation.
	siblingCtx    context.Context
	siblingCancel context.CancelFunc
}

type toolCommitState uint8

const (
	toolCommitPending toolCommitState = iota
	toolCommitCommitted
	toolCommitRejected
)

type toolStatus string

const (
	toolStatusQueued    toolStatus = "queued"
	toolStatusExecuting toolStatus = "executing"
	toolStatusCompleted toolStatus = "completed"
	toolStatusYielded   toolStatus = "yielded"
)

type trackedTool struct {
	ID                string
	ToolCall          *schema.ToolCall
	Message           *schema.Message
	Result            *ToolResult
	Status            toolStatus
	IsConcurrencySafe bool
	// cancelFunc cancels the context for an executing tool. Used for sibling
	// cancellation so that running tools receive context.Canceled rather than
	// running to completion when a Bash sibling errors.
	cancelFunc context.CancelFunc
}

// ToolResult holds a completed tool execution result and related emitted
// messages for a single tool call.
type ToolResult struct {
	ToolCallID          string
	ToolName            string
	Result              string
	Message             *schema.Message
	IsError             bool
	BeforeMessages      []*schema.Message
	AfterMessages       []*schema.Message
	PreventContinuation bool
	ContextModifier     func() (func(), error)
	ContextPublisher    func()
}

// NewStreamingToolExecutor creates a new executor.
func NewStreamingToolExecutor(configs ...StreamingToolExecutorConfig) *StreamingToolExecutor {
	cfg := StreamingToolExecutorConfig{}
	if len(configs) > 0 {
		cfg = configs[0]
	}
	maxConcurrency := cfg.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = maxStreamingToolUseConcurrency()
	}
	parentCtx := cfg.Ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	isInterrupted := cfg.IsInterrupted
	if isInterrupted == nil {
		isInterrupted = func() bool {
			return parentCtx.Err() != nil
		}
	}
	siblingCtx, siblingCancel := context.WithCancel(parentCtx)
	exec := &StreamingToolExecutor{
		tools:                make([]*trackedTool, 0),
		index:                make(map[string]int),
		execute:              cfg.Execute,
		executeWithCtx:       cfg.ExecuteWithContext,
		prepare:              cfg.PrepareForExecution,
		isSafe:               cfg.IsConcurrencySafe,
		maxConcurrency:       maxConcurrency,
		deferExecution:       cfg.DeferExecution,
		getInterruptBehavior: cfg.GetInterruptBehavior,
		isInterrupted:        isInterrupted,
		onInterrupt:          cfg.OnInterrupt,
		siblingCtx:           siblingCtx,
		siblingCancel:        siblingCancel,
	}
	exec.cond = sync.NewCond(&exec.mu)
	return exec
}

// ExecuteCommittedToolCalls runs one already-classified complete call set
// through the same stable scheduler used by streamed execution. The caller
// must own the terminal commit decision; this helper never observes or
// reclassifies a model stream.
func ExecuteCommittedToolCalls(
	ctx context.Context,
	toolCalls []*schema.ToolCall,
	config StreamingToolExecutorConfig,
) []*ToolResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.Ctx == nil {
		config.Ctx = ctx
	}
	if config.IsInterrupted == nil {
		config.IsInterrupted = func() bool {
			return ctx.Err() != nil
		}
	}
	executor := NewStreamingToolExecutor(config)
	for _, toolCall := range toolCalls {
		executor.AddTool(toolCall, nil)
	}
	if !executor.commit(ctx) {
		return executor.GetRemainingResults(ctx.Err() != nil)
	}
	return executor.GetRemainingResultsContext(ctx)
}

// AddTool registers or updates a streamed tool call.
func (e *StreamingToolExecutor) AddTool(toolCall *schema.ToolCall, assistantMessage *schema.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discard || e.commitState != toolCommitPending || toolCall == nil {
		return
	}
	id := toolCallKey(*toolCall)
	if id == "" {
		return
	}
	cloned := cloneToolCall(toolCall)
	if idx, ok := e.index[id]; ok {
		tracked := e.tools[idx]
		if tracked == nil {
			return
		}
		if tracked.Status == toolStatusQueued {
			tracked.ToolCall = cloned
			tracked.IsConcurrencySafe = e.toolConcurrencySafe(cloned)
		}
		if assistantMessage != nil {
			tracked.Message = assistantMessage
		}
		return
	}
	e.index[id] = len(e.tools)
	e.tools = append(e.tools, &trackedTool{
		ID:                id,
		ToolCall:          cloned,
		Message:           assistantMessage,
		Status:            toolStatusQueued,
		IsConcurrencySafe: e.toolConcurrencySafe(cloned),
	})
}

// commit releases the complete streamed call set to the existing scheduler.
// It is intentionally package-private so every production caller must pass
// through ProcessStream's shared terminal classifier.
func (e *StreamingToolExecutor) commit(ctx context.Context) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discard || e.commitState == toolCommitRejected {
		return false
	}
	if e.commitState == toolCommitCommitted {
		return true
	}
	if ctx != nil && ctx.Err() != nil {
		e.rejectPendingLocked("Interrupted by user")
		return false
	}
	e.commitState = toolCommitCommitted
	if !e.deferExecution {
		e.startRunnableLocked(false)
	}
	e.cond.Broadcast()
	return true
}

// rejectPending converts every uncommitted call into a model-ordered error
// result without crossing the tool side-effect boundary.
func (e *StreamingToolExecutor) rejectPending(message string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rejectPendingLocked(message)
}

func (e *StreamingToolExecutor) rejectPendingLocked(message string) bool {
	if e.discard || e.commitState != toolCommitPending {
		return false
	}
	e.commitState = toolCommitRejected
	for _, tracked := range e.tools {
		if tracked == nil || tracked.Status == toolStatusYielded {
			continue
		}
		toolCallID := tracked.ID
		if tracked.ToolCall != nil && tracked.ToolCall.ID != "" {
			toolCallID = tracked.ToolCall.ID
		}
		tracked.Result = newToolResult(toolCallID, toolNameFromCall(tracked.ToolCall), message, true)
		tracked.Status = toolStatusCompleted
	}
	e.cond.Broadcast()
	return true
}

// Complete marks a tool as completed with its result.
func (e *StreamingToolExecutor) Complete(toolCallID, result string, isError bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discard || e.commitState == toolCommitRejected {
		return
	}
	idx, ok := e.index[toolCallID]
	if !ok {
		return
	}
	tracked := e.tools[idx]
	if tracked == nil || tracked.Result != nil || tracked.Status == toolStatusYielded {
		return
	}
	toolName := ""
	if tracked.ToolCall != nil {
		toolName = tracked.ToolCall.Function.Name
	}
	tracked.Result = newToolResult(toolCallID, toolName, result, isError)
	tracked.Status = toolStatusCompleted
	e.cond.Broadcast()
	e.startRunnableLocked(false)
}

// GetCompleted returns all completed tool results since the last call in a
// deterministic order without waiting for unfinished tools.
func (e *StreamingToolExecutor) GetCompleted() []*ToolResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.commitState == toolCommitPending {
		return nil
	}
	return e.collectReadyResultsLocked(true)
}

// GetRemainingResults returns all non-yielded results. An uncommitted set fails
// closed. After commit, normal completion waits for queued/executing tools to
// settle; when ctxDone is true it generates synthetic interruption results for
// pending tools with "cancel" behavior and waits for "block" tools naturally.
func (e *StreamingToolExecutor) GetRemainingResults(ctxDone bool) []*ToolResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discard {
		return nil
	}
	if e.commitState == toolCommitPending {
		message := "Tool call rejected: model stream did not commit"
		if ctxDone {
			message = "Interrupted by user"
		}
		e.rejectPendingLocked(message)
	}
	if e.commitState == toolCommitRejected {
		return e.collectReadyResultsLocked(true)
	}
	if ctxDone {
		e.synthesizeCancelBehaviorLocked("Interrupted by user")
		// Wait for remaining "block" tools to finish naturally.
		for !e.allSettledLocked() {
			e.cond.Wait()
		}
		return e.collectReadyResultsLocked(true)
	}
	if e.execute == nil && e.executeWithCtx == nil {
		return e.collectReadyResultsLocked(true)
	}
	for {
		e.startRunnableLocked(true)
		if e.allSettledLocked() {
			break
		}
		e.cond.Wait()
	}
	return e.collectReadyResultsLocked(true)
}

// GetRemainingResultsContext waits for one committed call set while observing
// cancellation that happens after commit. Cancel-behavior tools receive
// synthetic interruption results, while block-behavior tools are allowed to
// settle before the complete model-ordered result set is returned.
func (e *StreamingToolExecutor) GetRemainingResultsContext(
	ctx context.Context,
) []*ToolResult {
	if ctx == nil {
		ctx = context.Background()
	}
	stop := context.AfterFunc(ctx, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.interruptRemainingLocked()
	})
	defer stop()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discard {
		return nil
	}
	if e.commitState == toolCommitPending {
		if ctx.Err() != nil {
			e.rejectPendingLocked("Interrupted by user")
		} else {
			e.rejectPendingLocked(
				"Tool call rejected: model stream did not commit",
			)
		}
	}
	if e.commitState == toolCommitRejected {
		return e.collectReadyResultsLocked(true)
	}
	if ctx.Err() != nil {
		e.interruptRemainingLocked()
	}
	if e.execute == nil && e.executeWithCtx == nil {
		return e.collectReadyResultsLocked(true)
	}
	for {
		e.startRunnableLocked(true)
		if e.allSettledLocked() {
			break
		}
		e.cond.Wait()
	}
	return e.collectReadyResultsLocked(true)
}

func (e *StreamingToolExecutor) interruptRemainingLocked() {
	if e.discard {
		return
	}
	if e.commitState == toolCommitPending {
		e.rejectPendingLocked("Interrupted by user")
		return
	}
	if e.commitState != toolCommitCommitted {
		return
	}
	// A call that never crossed the execution boundary has no natural
	// completion to protect. Reject every queued call instead of starting new
	// side effects after cancellation, regardless of its running-tool
	// interrupt behavior.
	for _, tracked := range e.tools {
		if tracked == nil || tracked.Status != toolStatusQueued {
			continue
		}
		toolCallID := tracked.ID
		if tracked.ToolCall != nil && tracked.ToolCall.ID != "" {
			toolCallID = tracked.ToolCall.ID
		}
		tracked.Result = newToolResult(
			toolCallID,
			toolNameFromCall(tracked.ToolCall),
			"Interrupted by user",
			true,
		)
		tracked.Status = toolStatusCompleted
	}
	e.synthesizeCancelBehaviorLocked("Interrupted by user")
	e.cond.Broadcast()
}

func (e *StreamingToolExecutor) collectReadyResultsLocked(stopOnPending bool) []*ToolResult { //nolint:unparam
	results := make([]*ToolResult, 0)
	for _, tracked := range e.tools {
		if tracked == nil || tracked.Status == toolStatusYielded {
			continue
		}
		if tracked.Result == nil || tracked.Status != toolStatusCompleted {
			if stopOnPending {
				break
			}
			continue
		}
		results = append(results, tracked.Result)
		tracked.Status = toolStatusYielded
	}
	return results
}

// synthesizeCancelBehaviorLocked synthesizes interrupt results only for tools
// whose interrupt behavior is "cancel" (or when no behavior lookup is configured).
// Tools with "block" behavior are left to complete naturally.
// Mirrors TS StreamingToolExecutor.ts:210-241.
func (e *StreamingToolExecutor) synthesizeCancelBehaviorLocked(message string) {
	for _, tracked := range e.tools {
		if tracked == nil || tracked.Status == toolStatusYielded || tracked.Result != nil {
			continue
		}
		toolName := toolNameFromCall(tracked.ToolCall)
		// If we have an interrupt behavior lookup and the tool says "block", skip it.
		if e.getInterruptBehavior != nil && toolName != "" {
			if e.getInterruptBehavior(toolName) == "block" {
				continue
			}
		}
		e.notifyInterrupt(tracked.ToolCall)
		if tracked.cancelFunc != nil {
			tracked.cancelFunc()
			tracked.cancelFunc = nil
		}
		toolCallID := tracked.ID
		if tracked.ToolCall != nil && tracked.ToolCall.ID != "" {
			toolCallID = tracked.ToolCall.ID
		}
		tracked.Result = newToolResult(toolCallID, toolName, message, true)
		tracked.Status = toolStatusCompleted
	}
	e.cond.Broadcast()
}

func (e *StreamingToolExecutor) notifyInterrupt(toolCall *schema.ToolCall) {
	if e == nil || e.onInterrupt == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	e.onInterrupt(cloneToolCall(toolCall))
}

// synthesizeQueuedSiblingsLocked cancels all queued (not-yet-started) sibling tools
// after a Bash error. Tools already executing are left to complete on their own.
// Mirrors TS StreamingToolExecutor.ts sibling error cascading.
func (e *StreamingToolExecutor) synthesizeQueuedSiblingsLocked(errorSource string) {
	msg := fmt.Sprintf("Cancelled: parallel tool call %s errored", errorSource)
	for _, tracked := range e.tools {
		if tracked == nil || tracked.Status != toolStatusQueued {
			continue
		}
		toolCallID := tracked.ID
		toolName := toolNameFromCall(tracked.ToolCall)
		tracked.Result = newToolResult(toolCallID, toolName, msg, true)
		tracked.Status = toolStatusCompleted
	}
}

func (e *StreamingToolExecutor) allSettledLocked() bool {
	for _, tracked := range e.tools {
		if tracked == nil {
			continue
		}
		switch tracked.Status {
		case toolStatusQueued, toolStatusExecuting:
			return false
		}
	}
	return true
}

func (e *StreamingToolExecutor) toolConcurrencySafe(toolCall *schema.ToolCall) bool {
	if e.isSafe == nil || toolCall == nil {
		return false
	}
	defer func() {
		if recover() != nil { //nolint:staticcheck // intentional empty recover
			// intentionally empty: caller falls back to false via named return
		}
	}()
	return e.isSafe(toolCall)
}

func (e *StreamingToolExecutor) startRunnableLocked(force bool) {
	if e.discard || e.commitState != toolCommitCommitted || (e.execute == nil && e.executeWithCtx == nil) {
		return
	}
	for _, tracked := range e.tools {
		if tracked == nil {
			continue
		}
		if tracked.Status != toolStatusQueued {
			continue
		}
		if !force && !toolCallReadyForExecution(tracked.ToolCall) {
			break
		}
		if !e.canExecuteLocked(tracked.IsConcurrencySafe) {
			break
		}
		e.startToolLocked(tracked)
		if !tracked.IsConcurrencySafe {
			break
		}
	}
}

func (e *StreamingToolExecutor) canExecuteLocked(isConcurrencySafe bool) bool {
	executingCount := 0
	for _, tracked := range e.tools {
		if tracked == nil || tracked.Status != toolStatusExecuting {
			continue
		}
		executingCount++
		if !tracked.IsConcurrencySafe {
			return false
		}
	}
	if executingCount == 0 {
		return true
	}
	if !isConcurrencySafe {
		return false
	}
	return executingCount < e.maxConcurrency
}

func (e *StreamingToolExecutor) startToolLocked(tracked *trackedTool) {
	if tracked == nil || tracked.Status != toolStatusQueued || tracked.ToolCall == nil {
		return
	}
	tracked.Status = toolStatusExecuting
	toolID := tracked.ID
	toolCall := cloneToolCall(tracked.ToolCall)
	if e.prepare != nil {
		if prepared := e.prepare(toolCall); prepared != nil {
			toolCall = prepared
		}
	}

	// Create a per-tool context derived from the sibling context so that
	// cancelling siblingCtx propagates to all executing tools.
	toolCtx, toolCancel := context.WithCancel(e.siblingCtx)
	tracked.cancelFunc = toolCancel

	go func() {
		var result *ToolResult
		if e.executeWithCtx != nil {
			result = e.executeWithCtx(toolCtx, toolCall)
		} else if e.execute != nil {
			result = e.execute(toolCall)
		}
		if result == nil {
			result = newToolResult(toolID, toolCall.Function.Name, "tool execution returned no result", true)
		}
		toolCancel() // clean up per-tool context
		e.finish(toolID, result)
	}()
}

func (e *StreamingToolExecutor) finish(toolID string, result *ToolResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discard || e.commitState != toolCommitCommitted {
		return
	}
	idx, ok := e.index[toolID]
	if !ok {
		return
	}
	tracked := e.tools[idx]
	if tracked == nil {
		return
	}
	if tracked.Result != nil || tracked.Status == toolStatusYielded {
		// Synthetic or manually completed result already won.
		e.cond.Broadcast()
		return
	}
	if e.isInterrupted != nil && e.isInterrupted() {
		// Observe cancellation while holding the same scheduler lock used by
		// finish and the async interrupt callback. This makes cancellation the
		// deterministic winner for cancel-behavior tools even when a
		// non-cooperative executor returns success after ctx.Done().
		e.interruptRemainingLocked()
		if tracked.Result != nil {
			return
		}
	}
	tracked.Result = normalizeToolResult(toolID, tracked.ToolCall, result)
	commitToolResultContextModifier(tracked.Result)
	tracked.Status = toolStatusCompleted
	tracked.cancelFunc = nil // context already cancelled in goroutine
	// Sibling abort on Bash error: cancel queued siblings and signal executing
	// siblings via siblingCtx cancellation when Bash fails.
	// Only Bash errors trigger this (mirrors TS StreamingToolExecutor.ts:359-364).
	if tracked.Result.IsError && tracked.Result.ToolName == "Bash" && !e.bashErrored {
		e.bashErrored = true
		// Cancel the shared sibling context — executing tools that check ctx will observe cancellation.
		e.siblingCancel()
		e.synthesizeQueuedSiblingsLocked(tracked.Result.ToolName)
	}
	e.cond.Broadcast()
	e.startRunnableLocked(false)
}

func commitToolResultContextModifier(result *ToolResult) {
	if result == nil || result.ContextModifier == nil {
		return
	}
	publish, err := result.ContextModifier()
	result.ContextModifier = nil
	if err == nil {
		result.ContextPublisher = publish
		return
	}
	result.ContextPublisher = nil
	result.IsError = true
	result.Result = "tool state transition failed: " + err.Error()
	if result.Message == nil {
		result.Message = &schema.Message{
			Role:       schema.Tool,
			ToolCallID: result.ToolCallID,
			ToolName:   result.ToolName,
		}
	}
	result.Message.Content = result.Result
	if result.Message.Extra == nil {
		result.Message.Extra = map[string]any{}
	}
	result.Message.Extra["is_error"] = true
}

func normalizeToolResult(toolID string, toolCall *schema.ToolCall, result *ToolResult) *ToolResult {
	if result == nil {
		return newToolResult(toolID, toolNameFromCall(toolCall), "tool execution returned no result", true)
	}
	cloned := *result
	if cloned.ToolCallID == "" {
		cloned.ToolCallID = toolID
	}
	if cloned.ToolName == "" {
		cloned.ToolName = toolNameFromCall(toolCall)
	}
	if cloned.Message == nil {
		cloned.Message = &schema.Message{
			Role:       schema.Tool,
			Content:    cloned.Result,
			ToolCallID: cloned.ToolCallID,
			ToolName:   cloned.ToolName,
		}
		if cloned.IsError {
			cloned.Message.Extra = map[string]any{"is_error": true}
		}
	} else {
		if cloned.Message.ToolCallID == "" {
			cloned.Message.ToolCallID = cloned.ToolCallID
		}
		if cloned.Message.ToolName == "" {
			cloned.Message.ToolName = cloned.ToolName
		}
		if cloned.Message.Role == "" {
			cloned.Message.Role = schema.Tool
		}
		if cloned.IsError {
			if cloned.Message.Extra == nil {
				cloned.Message.Extra = map[string]any{}
			}
			cloned.Message.Extra["is_error"] = true
		}
	}
	return &cloned
}

func toolCallReadyForExecution(toolCall *schema.ToolCall) bool {
	if toolCall == nil || strings.TrimSpace(toolCall.Function.Name) == "" {
		return false
	}
	args := strings.TrimSpace(toolCall.Function.Arguments)
	if args == "" {
		return false
	}
	if args == "{}" {
		return false
	}
	return json.Valid([]byte(args))
}

func toolNameFromCall(toolCall *schema.ToolCall) string {
	if toolCall == nil {
		return ""
	}
	return toolCall.Function.Name
}

func newToolResult(toolCallID, toolName, result string, isError bool) *ToolResult {
	msg := &schema.Message{
		Role:       schema.Tool,
		Content:    result,
		ToolCallID: toolCallID,
		ToolName:   toolName,
	}
	if isError {
		msg.Extra = map[string]any{"is_error": true}
	}
	return &ToolResult{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Result:     result,
		Message:    msg,
		IsError:    isError,
	}
}

func cloneToolCall(toolCall *schema.ToolCall) *schema.ToolCall {
	if toolCall == nil {
		return nil
	}
	cloned := *toolCall
	cloned.Function = toolCall.Function
	if toolCall.Extra != nil {
		cloned.Extra = make(map[string]any, len(toolCall.Extra))
		for k, v := range toolCall.Extra {
			cloned.Extra[k] = v
		}
	}
	return &cloned
}

// Discard clears all tracked state. Used when a streaming attempt is abandoned
// and its pending tool results must not leak into the replacement attempt.
func (e *StreamingToolExecutor) Discard() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.discard = true
	// Cancel sibling context to signal all executing tools.
	e.siblingCancel()
	// Cancel any individual tool contexts still active.
	for _, tracked := range e.tools {
		if tracked != nil && tracked.cancelFunc != nil {
			tracked.cancelFunc()
			tracked.cancelFunc = nil
		}
	}
	e.tools = nil
	e.index = make(map[string]int)
	e.cond.Broadcast()
}

func maxStreamingToolUseConcurrency() int {
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
