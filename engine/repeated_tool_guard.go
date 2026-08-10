package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/cloudwego/eino/schema"
)

const repeatedToolTicketExtraKey = "eino_internal_repeated_tool_ticket"

type repeatedToolDecision int

const (
	repeatedToolAllow repeatedToolDecision = iota
	repeatedToolRequestOverride
	repeatedToolBlock
)

type repeatedToolCallGuard struct {
	mu                sync.Mutex
	tail              chan struct{}
	fingerprint       string
	streak            int
	overrideRequested bool
}

type repeatedToolCallTicket struct {
	guard *repeatedToolCallGuard
	prev  <-chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newRepeatedToolCallGuard() *repeatedToolCallGuard {
	closed := make(chan struct{})
	close(closed)
	return &repeatedToolCallGuard{tail: closed}
}

// reserve is deliberately non-blocking so callers can assign tickets in model
// order before launching concurrent tool goroutines.
func (g *repeatedToolCallGuard) reserve() *repeatedToolCallTicket {
	g.mu.Lock()
	defer g.mu.Unlock()
	done := make(chan struct{})
	ticket := &repeatedToolCallTicket{guard: g, prev: g.tail, done: done}
	g.tail = done
	return ticket
}

// await waits for the preceding reservation, evaluates this call, and keeps
// successors blocked until release records the one-shot override decision.
func (t *repeatedToolCallTicket) await(ctx context.Context, fingerprint string) (repeatedToolDecision, int, error) {
	if t == nil || t.guard == nil {
		return repeatedToolAllow, 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		t.releaseAfterPredecessor()
		return repeatedToolBlock, 0, ctx.Err()
	case <-t.prev:
	}
	if err := ctx.Err(); err != nil {
		t.release(false)
		return repeatedToolBlock, 0, err
	}
	decision, attempt := t.guard.evaluate(fingerprint)
	return decision, attempt, nil
}

func (t *repeatedToolCallTicket) release(overridden bool) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if overridden && t.guard != nil {
			t.guard.resetState()
		}
		close(t.done)
	})
}

func (t *repeatedToolCallTicket) releaseAfterPredecessor() {
	go func() {
		<-t.prev
		t.release(false)
	}()
}

func (g *repeatedToolCallGuard) reset() {
	if g == nil {
		return
	}
	g.resetState()
}

func (g *repeatedToolCallGuard) resetState() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fingerprint = ""
	g.streak = 0
	g.overrideRequested = false
}

func (g *repeatedToolCallGuard) evaluate(fingerprint string) (repeatedToolDecision, int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if fingerprint == "" {
		return repeatedToolAllow, 0
	}
	if fingerprint != g.fingerprint {
		g.fingerprint = fingerprint
		g.streak = 1
		g.overrideRequested = false
		return repeatedToolAllow, g.streak
	}
	if g.streak < 3 {
		g.streak++
	}
	if g.streak < 3 {
		return repeatedToolAllow, g.streak
	}
	if !g.overrideRequested {
		g.overrideRequested = true
		return repeatedToolRequestOverride, g.streak
	}
	return repeatedToolBlock, g.streak
}

func repeatedToolCallFingerprint(toolName string, input map[string]any) string {
	canonical, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(toolName))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return hex.EncodeToString(hash.Sum(nil))
}

func reserveRepeatedToolCall(toolCall *schema.ToolCall, guard *repeatedToolCallGuard) *schema.ToolCall {
	if toolCall == nil || guard == nil {
		return toolCall
	}
	cloned := *toolCall
	cloned.Function = toolCall.Function
	cloned.Extra = make(map[string]any, len(toolCall.Extra)+1)
	for key, value := range toolCall.Extra {
		cloned.Extra[key] = value
	}
	cloned.Extra[repeatedToolTicketExtraKey] = guard.reserve()
	return &cloned
}

func repeatedToolTicket(toolCall *schema.ToolCall) *repeatedToolCallTicket {
	if toolCall == nil || toolCall.Extra == nil {
		return nil
	}
	ticket, _ := toolCall.Extra[repeatedToolTicketExtraKey].(*repeatedToolCallTicket)
	return ticket
}

func reserveRepeatedToolCalls(toolCalls []*schema.ToolCall, guard *repeatedToolCallGuard) []*schema.ToolCall {
	if guard == nil {
		return toolCalls
	}
	reserved := make([]*schema.ToolCall, len(toolCalls))
	for index, toolCall := range toolCalls {
		reserved[index] = reserveRepeatedToolCall(toolCall, guard)
	}
	return reserved
}
