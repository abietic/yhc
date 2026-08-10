package engine

import (
	"context"
	"sync"
)

// CancellationChain manages hierarchical context cancellation for the query loop.
// When a user interrupts (Ctrl+C), the chain propagates cancellation through:
//   - In-flight model calls
//   - Running tool executions
//   - Pending tool queue items
//   - Sub-agent executions
//
// This mirrors the AbortController chain pattern from the reference implementation,
// where each layer derives a child context from the parent AbortController's signal.
//
// Usage:
//
//	chain := NewCancellationChain(parentCtx)
//	modelCtx := chain.ModelContext()     // cancel model call on interrupt
//	toolCtx := chain.ToolContext("id")   // cancel specific tool
//	chain.Cancel("user_abort")           // propagates to all layers
//	chain.CancelTool("id")              // cancel one tool without affecting others
type CancellationChain struct {
	mu sync.Mutex

	// parentCtx is the top-level context (e.g., from SubmitMessage)
	parentCtx context.Context

	// rootCtx and rootCancel control the entire chain
	rootCtx    context.Context
	rootCancel context.CancelFunc

	// modelCtx is derived from rootCtx for in-flight model calls
	modelCtx    context.Context
	modelCancel context.CancelFunc

	// toolContexts tracks per-tool cancellation
	toolContexts map[string]*toolCancelEntry

	// reason stores why cancellation was triggered
	reason string

	// cancelled tracks whether Cancel was called
	cancelled bool
}

// toolCancelEntry tracks a single tool's cancellation context.
type toolCancelEntry struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewCancellationChain creates a hierarchical cancellation chain rooted at parentCtx.
// When parentCtx is cancelled (external cancellation), all derived contexts also cancel.
func NewCancellationChain(parentCtx context.Context) *CancellationChain {
	rootCtx, rootCancel := context.WithCancel(parentCtx)
	modelCtx, modelCancel := context.WithCancel(rootCtx)

	return &CancellationChain{
		parentCtx:    parentCtx,
		rootCtx:      rootCtx,
		rootCancel:   rootCancel,
		modelCtx:     modelCtx,
		modelCancel:  modelCancel,
		toolContexts: make(map[string]*toolCancelEntry),
	}
}

// ModelContext returns a context for in-flight model calls.
// When the chain is cancelled, this context is cancelled too.
func (c *CancellationChain) ModelContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.modelCtx
}

// ToolContext returns (or creates) a context for a specific tool execution.
// Each tool gets its own cancellation scope so individual tools can be
// cancelled without affecting the entire chain.
func (c *CancellationChain) ToolContext(toolUseID string) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.toolContexts[toolUseID]; ok {
		return entry.ctx
	}

	// Derive from rootCtx so chain-level cancel propagates to tools
	ctx, cancel := context.WithCancel(c.rootCtx)
	c.toolContexts[toolUseID] = &toolCancelEntry{ctx: ctx, cancel: cancel}
	return ctx
}

// Cancel cancels the entire chain with a reason. This propagates to:
// - The model context (cancels in-flight model call)
// - All tool contexts (cancels running tool executions)
// - Any future contexts derived from this chain
//
// This is idempotent — calling Cancel multiple times is safe.
func (c *CancellationChain) Cancel(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cancelled {
		return
	}
	c.cancelled = true
	c.reason = reason

	// Cancel root — this propagates to model and all tool contexts
	c.rootCancel()
}

// CancelModel cancels only the in-flight model call without affecting tools.
// This is used when we want to abort the current model call but let running
// tools complete (e.g., streaming timeout recovery).
func (c *CancellationChain) CancelModel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.modelCancel()
}

// CancelTool cancels a specific tool execution by its tool_use_id.
// Other tools and the model call continue unaffected.
func (c *CancellationChain) CancelTool(toolUseID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.toolContexts[toolUseID]; ok {
		entry.cancel()
	}
}

// ReleaseTool removes a tool from the tracking map after it completes.
// This prevents memory leaks when many tools are executed over a long session.
func (c *CancellationChain) ReleaseTool(toolUseID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.toolContexts[toolUseID]; ok {
		entry.cancel() // ensure cleanup
		delete(c.toolContexts, toolUseID)
	}
}

// Cancelled returns true if Cancel was called on this chain.
func (c *CancellationChain) Cancelled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelled
}

// Reason returns the reason for cancellation (empty if not cancelled).
func (c *CancellationChain) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// RootContext returns the root context of the chain.
// This is cancelled when Cancel is called or parentCtx is cancelled.
func (c *CancellationChain) RootContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rootCtx
}

// ActiveToolCount returns the number of tools currently tracked.
func (c *CancellationChain) ActiveToolCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.toolContexts)
}

// Reset creates a fresh model context from the root.
// Used between turns when the previous model call completed and a new one starts.
// Does NOT reset the root or tool contexts.
func (c *CancellationChain) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Only reset if the chain itself is not cancelled
	if c.cancelled {
		return
	}

	// Cancel old model context
	c.modelCancel()

	// Create fresh model context for the next turn
	c.modelCtx, c.modelCancel = context.WithCancel(c.rootCtx)
}
