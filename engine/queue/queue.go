package queue

import (
	"context"
	"sync"
	"time"
)

// ItemType distinguishes the kind of queued item.
type ItemType string

const (
	// ItemTypeMessage represents a queued user message.
	ItemTypeMessage ItemType = "message"
	// ItemTypeCommand represents a queued slash command (higher priority).
	ItemTypeCommand ItemType = "command"
)

// TurnItem is a single entry in a TurnQueue.
type TurnItem struct {
	Type      ItemType
	Content   string
	Timestamp time.Time
	Meta      map[string]any
}

// TurnQueue manages ordered message processing for a single session turn.
// Commands are prioritized over messages during dequeue operations.
type TurnQueue struct {
	mu     sync.Mutex
	items  []TurnItem
	notify chan struct{} // signals when items are available
	closed bool
}

// NewTurnQueue creates a new TurnQueue ready for use.
func NewTurnQueue() *TurnQueue {
	return &TurnQueue{
		items:  make([]TurnItem, 0),
		notify: make(chan struct{}, 1),
	}
}

// EnqueueMessage adds a user message to the queue.
func (q *TurnQueue) EnqueueMessage(content string, meta map[string]any) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.items = append(q.items, TurnItem{
		Type:      ItemTypeMessage,
		Content:   content,
		Timestamp: time.Now(),
		Meta:      meta,
	})
	q.signal()
}

// EnqueueCommand adds a slash command to the queue (higher priority).
func (q *TurnQueue) EnqueueCommand(content string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.items = append(q.items, TurnItem{
		Type:      ItemTypeCommand,
		Content:   content,
		Timestamp: time.Now(),
	})
	q.signal()
}

// signal sends a non-blocking notification that items are available.
func (q *TurnQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Dequeue returns the next item, prioritizing commands over messages.
// Returns nil if the queue is empty.
func (q *TurnQueue) Dequeue() *TurnItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return nil
	}

	idx := q.nextIndex()
	item := q.items[idx]
	q.items = append(q.items[:idx], q.items[idx+1:]...)
	return &item
}

// DequeueAll returns all items in priority order (commands first, then messages).
// Each group preserves insertion order.
func (q *TurnQueue) DequeueAll() []TurnItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return nil
	}

	result := make([]TurnItem, 0, len(q.items))

	// Commands first, preserving order.
	for _, item := range q.items {
		if item.Type == ItemTypeCommand {
			result = append(result, item)
		}
	}
	// Then messages, preserving order.
	for _, item := range q.items {
		if item.Type == ItemTypeMessage {
			result = append(result, item)
		}
	}

	q.items = q.items[:0]
	return result
}

// Wait blocks until an item is available or the queue is closed.
// Returns the next item (prioritizing commands) or an error if the context
// is cancelled or the queue is closed.
func (q *TurnQueue) Wait(ctx context.Context) (*TurnItem, error) {
	for {
		// Fast path: check if there's already an item.
		if item := q.Dequeue(); item != nil {
			return item, nil
		}

		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return nil, context.Canceled
		}
		q.mu.Unlock()

		// Block until signaled, closed, or context cancelled.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case _, ok := <-q.notify:
			if !ok {
				return nil, context.Canceled
			}
			// Item was signaled; loop back to dequeue.
		}
	}
}

// Len returns the number of items in the queue.
func (q *TurnQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// HasCommands returns true if there are queued commands.
func (q *TurnQueue) HasCommands() bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, item := range q.items {
		if item.Type == ItemTypeCommand {
			return true
		}
	}
	return false
}

// Peek returns the next item without removing it, prioritizing commands.
// Returns nil if the queue is empty.
func (q *TurnQueue) Peek() *TurnItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return nil
	}

	idx := q.nextIndex()
	item := q.items[idx]
	return &item
}

// Close closes the queue, unblocking any waiters.
func (q *TurnQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.notify)
	}
}

// nextIndex returns the index of the next item to dequeue.
// Commands have priority; within the same type, FIFO order is preserved.
// Must be called with q.mu held.
func (q *TurnQueue) nextIndex() int {
	for i, item := range q.items {
		if item.Type == ItemTypeCommand {
			return i
		}
	}
	return 0
}

// QueueManager coordinates multiple TurnQueues, one per session.
type QueueManager struct {
	mu     sync.RWMutex
	queues map[string]*TurnQueue
}

// NewQueueManager creates a new QueueManager.
func NewQueueManager() *QueueManager {
	return &QueueManager{
		queues: make(map[string]*TurnQueue),
	}
}

// GetQueue returns the TurnQueue for the given session, creating one if needed.
func (m *QueueManager) GetQueue(sessionID string) *TurnQueue {
	m.mu.RLock()
	if q, ok := m.queues[sessionID]; ok {
		m.mu.RUnlock()
		return q
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock.
	if q, ok := m.queues[sessionID]; ok {
		return q
	}

	q := NewTurnQueue()
	m.queues[sessionID] = q
	return q
}

// RemoveQueue removes and closes the TurnQueue for the given session.
func (m *QueueManager) RemoveQueue(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if q, ok := m.queues[sessionID]; ok {
		q.Close()
		delete(m.queues, sessionID)
	}
}
