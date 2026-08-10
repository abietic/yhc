package engine

import (
	"context"
	"testing"
	"time"
)

func TestCancellationChainModelContextCancelled(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	modelCtx := chain.ModelContext()
	if modelCtx.Err() != nil {
		t.Fatal("model context should not be cancelled initially")
		return
	}

	chain.Cancel("user_abort")

	// Model context should now be cancelled
	select {
	case <-modelCtx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("model context should be cancelled after chain.Cancel()")
	}
}

func TestCancellationChainToolContextCancelled(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	toolCtx := chain.ToolContext("tool_001")
	if toolCtx.Err() != nil {
		t.Fatal("tool context should not be cancelled initially")
		return
	}

	chain.Cancel("interrupt")

	select {
	case <-toolCtx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("tool context should be cancelled after chain.Cancel()")
	}
}

func TestCancellationChainCancelToolIndependently(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	tool1Ctx := chain.ToolContext("tool_001")
	tool2Ctx := chain.ToolContext("tool_002")

	// Cancel only tool 1
	chain.CancelTool("tool_001")

	select {
	case <-tool1Ctx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("tool_001 should be cancelled")
	}

	// tool 2 should still be active
	if tool2Ctx.Err() != nil {
		t.Fatal("tool_002 should NOT be cancelled")
		return
	}

	// Model context should still be active
	if chain.ModelContext().Err() != nil {
		t.Fatal("model context should NOT be cancelled")
		return
	}
}

func TestCancellationChainCancelModelOnly(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	modelCtx := chain.ModelContext()
	toolCtx := chain.ToolContext("tool_001")

	chain.CancelModel()

	select {
	case <-modelCtx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("model context should be cancelled")
	}

	// Tool should still be active
	if toolCtx.Err() != nil {
		t.Fatal("tool context should NOT be cancelled when only model is cancelled")
		return
	}
}

func TestCancellationChainParentCancelPropagates(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	chain := NewCancellationChain(parentCtx)

	modelCtx := chain.ModelContext()
	toolCtx := chain.ToolContext("tool_001")

	// Cancel parent
	parentCancel()

	select {
	case <-modelCtx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("model context should be cancelled when parent is cancelled")
	}

	select {
	case <-toolCtx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("tool context should be cancelled when parent is cancelled")
	}
}

func TestCancellationChainIdempotentCancel(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	// Cancel twice — should not panic
	chain.Cancel("first")
	chain.Cancel("second")

	if chain.Reason() != "first" {
		t.Fatalf("expected reason 'first', got %q", chain.Reason())
	}
}

func TestCancellationChainReleaseTool(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	chain.ToolContext("tool_001")
	chain.ToolContext("tool_002")

	if chain.ActiveToolCount() != 2 {
		t.Fatalf("expected 2 active tools, got %d", chain.ActiveToolCount())
	}

	chain.ReleaseTool("tool_001")

	if chain.ActiveToolCount() != 1 {
		t.Fatalf("expected 1 active tool after release, got %d", chain.ActiveToolCount())
	}
}

func TestCancellationChainReset(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	oldModelCtx := chain.ModelContext()

	// Reset creates a fresh model context
	chain.Reset()

	newModelCtx := chain.ModelContext()

	// Old model context should be cancelled
	select {
	case <-oldModelCtx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("old model context should be cancelled after Reset()")
	}

	// New model context should be fresh
	if newModelCtx.Err() != nil {
		t.Fatal("new model context should not be cancelled")
		return
	}
}

func TestCancellationChainResetAfterCancel(t *testing.T) {
	chain := NewCancellationChain(context.Background())
	chain.Cancel("abort")

	// Reset should be no-op after chain-level cancel
	chain.Reset()

	// Model context should still be cancelled (chain is cancelled)
	if chain.ModelContext().Err() == nil {
		t.Fatal("model context should remain cancelled after chain-level cancel + Reset")
		return
	}
}

func TestCancellationChainCancelledState(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	if chain.Cancelled() {
		t.Fatal("should not be cancelled initially")
	}
	if chain.Reason() != "" {
		t.Fatal("should have empty reason initially")
	}

	chain.Cancel("test_reason")

	if !chain.Cancelled() {
		t.Fatal("should be cancelled after Cancel()")
	}
	if chain.Reason() != "test_reason" {
		t.Fatalf("expected reason 'test_reason', got %q", chain.Reason())
	}
}

func TestCancellationChainToolContextReuse(t *testing.T) {
	chain := NewCancellationChain(context.Background())

	ctx1 := chain.ToolContext("tool_001")
	ctx2 := chain.ToolContext("tool_001")

	// Should return the same context for the same tool ID
	if ctx1 != ctx2 {
		t.Fatal("expected same context for same tool ID")
	}
}
