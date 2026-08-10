package prefetch

import (
	"fmt"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/compact"
	"github.com/cloudwego/eino/schema"
)

// MemoryPrefetch prefetches relevant memory entries from the session memory store.
// It loads entries from disk in a background goroutine and formats them for injection.
type MemoryPrefetch struct {
	store     *compact.MemoryStore
	maxTokens int

	mu      sync.Mutex
	done    chan struct{}
	result  []*schema.Message
	started bool
}

// NewMemoryPrefetch creates a MemoryPrefetch with the given store and token budget.
func NewMemoryPrefetch(store *compact.MemoryStore, maxTokens int) *MemoryPrefetch {
	if maxTokens <= 0 {
		maxTokens = 2000
	}
	return &MemoryPrefetch{
		store:     store,
		maxTokens: maxTokens,
		done:      make(chan struct{}),
	}
}

// Start begins a non-blocking memory prefetch. Loads memory from the store
// and formats recent entries for injection into the conversation.
func (p *MemoryPrefetch) Start(messages []*schema.Message) {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	go func() {
		defer close(p.done)

		if p.store == nil {
			return
		}
		if err := p.store.Load(); err != nil {
			return
		}

		query := latestUserQuery(messages)
		entries := p.store.GetRelevant(query, 50)
		if len(entries) == 0 {
			return
		}

		// Format entries, respecting token budget (rough: 4 chars per token).
		charBudget := p.maxTokens * 4
		var sb strings.Builder
		sb.WriteString("[Session Memory — context from previous interactions:]\n\n")
		used := sb.Len()

		for _, entry := range entries {
			line := fmt.Sprintf("- [%s] %s\n", entry.Category, entry.Content)
			if used+len(line) > charBudget {
				break
			}
			sb.WriteString(line)
			used += len(line)
		}

		content := sb.String()
		if strings.TrimSpace(content) == "[Session Memory — context from previous interactions:]" {
			return
		}

		p.mu.Lock()
		p.result = []*schema.Message{
			{
				Role:    schema.System,
				Content: content,
				Extra: map[string]any{
					"is_meta":         true,
					"attachment_kind": "memory_prefetch",
				},
			},
		}
		p.mu.Unlock()
	}()
}

func latestUserQuery(messages []*schema.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message != nil && message.Role == schema.User && strings.TrimSpace(message.Content) != "" {
			return message.Content
		}
	}
	return ""
}

// Collect returns prefetched results. Blocks until prefetch completes.
// Safe to call even if Start() was never called or the struct was zero-initialized.
func (p *MemoryPrefetch) Collect() []*schema.Message {
	if p.done == nil {
		return nil
	}
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}
