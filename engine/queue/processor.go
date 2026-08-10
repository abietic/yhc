package queue

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

// Source priorities: higher numeric value = processed first.
const (
	PrioritySourceTool    = 30
	PrioritySourceUser    = 50
	PrioritySourceCommand = 80
	PrioritySourceSystem  = 100
)

// SourcePriority returns the default priority for a given source string.
func SourcePriority(source string) int {
	switch source {
	case "system":
		return PrioritySourceSystem
	case "command":
		return PrioritySourceCommand
	case "user":
		return PrioritySourceUser
	case "tool":
		return PrioritySourceTool
	default:
		return PrioritySourceUser
	}
}

// QueueItem represents an item in the priority queue processor.
type QueueItem struct {
	ID         string
	Content    string
	Priority   int // higher = process first
	Source     string
	Retries    int
	MaxRetries int
	CreatedAt  time.Time
	Metadata   map[string]any

	// index is maintained by the heap implementation.
	index int
}

// priorityHeap implements heap.Interface for []*QueueItem.
// Items with higher Priority values are dequeued first.
// Ties are broken by CreatedAt (earlier = first, FIFO within same priority).
type priorityHeap []*QueueItem

func (h priorityHeap) Len() int { return len(h) }

func (h priorityHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority > h[j].Priority // higher priority first
	}
	return h[i].CreatedAt.Before(h[j].CreatedAt) // FIFO for same priority
}

func (h priorityHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *priorityHeap) Push(x any) {
	item := x.(*QueueItem)
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *priorityHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid memory leak
	item.index = -1
	*h = old[:n-1]
	return item
}

// PriorityQueue is a thread-safe priority queue backed by container/heap.
type PriorityQueue struct {
	mu   sync.Mutex
	heap priorityHeap
}

// NewPriorityQueue creates a new empty PriorityQueue.
func NewPriorityQueue() *PriorityQueue {
	pq := &PriorityQueue{
		heap: make(priorityHeap, 0),
	}
	heap.Init(&pq.heap)
	return pq
}

// Push adds an item to the priority queue.
func (pq *PriorityQueue) Push(item *QueueItem) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	heap.Push(&pq.heap, item)
}

// Pop removes and returns the highest-priority item, or nil if empty.
func (pq *PriorityQueue) Pop() *QueueItem {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.heap.Len() == 0 {
		return nil
	}
	return heap.Pop(&pq.heap).(*QueueItem)
}

// Peek returns the highest-priority item without removing it, or nil if empty.
func (pq *PriorityQueue) Peek() *QueueItem {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.heap.Len() == 0 {
		return nil
	}
	return pq.heap[0]
}

// Len returns the number of items in the queue.
func (pq *PriorityQueue) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.heap.Len()
}

// Clear removes all items from the queue.
func (pq *PriorityQueue) Clear() {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.heap = make(priorityHeap, 0)
}

// QueueProcessor manages processing of prioritized queue items with retry support.
type QueueProcessor struct {
	queue      *PriorityQueue
	processing bool
	mu         sync.Mutex
	handler    func(item *QueueItem) error
	onError    func(item *QueueItem, err error)
	maxRetries int
	ctx        context.Context
	cancel     context.CancelFunc

	// notify signals the processing loop that new items are available.
	notify chan struct{}
}

// NewQueueProcessor creates a new QueueProcessor with the given handler.
// The handler is called for each item in priority order.
func NewQueueProcessor(handler func(*QueueItem) error) *QueueProcessor {
	return &QueueProcessor{
		queue:      NewPriorityQueue(),
		handler:    handler,
		maxRetries: 3,
		notify:     make(chan struct{}, 1),
	}
}

// SetOnError sets an error callback invoked when an item fails after all retries.
func (p *QueueProcessor) SetOnError(fn func(item *QueueItem, err error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onError = fn
}

// SetMaxRetries sets the default maximum retry count for items without their own MaxRetries.
func (p *QueueProcessor) SetMaxRetries(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxRetries = n
}

// Enqueue adds an item to the queue. If the processor is running, it will
// be picked up by the background loop. If Priority is zero and Source is set,
// the priority is inferred from the source.
func (p *QueueProcessor) Enqueue(item *QueueItem) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.Priority == 0 && item.Source != "" {
		item.Priority = SourcePriority(item.Source)
	}
	if item.MaxRetries == 0 {
		p.mu.Lock()
		item.MaxRetries = p.maxRetries
		p.mu.Unlock()
	}
	p.queue.Push(item)
	p.signal()
}

// signal notifies the processing loop (non-blocking).
func (p *QueueProcessor) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Process processes all currently queued items in priority order.
// It handles each item via the handler, retrying on failure up to MaxRetries.
// Returns the first unrecoverable error (after retries exhausted), or nil.
func (p *QueueProcessor) Process(ctx context.Context) error {
	for {
		item := p.queue.Pop()
		if item == nil {
			return nil
		}

		if err := ctx.Err(); err != nil {
			// Context cancelled; re-queue the item and return.
			p.queue.Push(item)
			return err
		}

		if err := p.handler(item); err != nil {
			item.Retries++
			if item.Retries < item.MaxRetries {
				// Re-enqueue for retry.
				p.queue.Push(item)
				continue
			}
			// Exhausted retries — invoke error callback if set.
			p.mu.Lock()
			onErr := p.onError
			p.mu.Unlock()
			if onErr != nil {
				onErr(item, err)
			}
			return err
		}
	}
}

// Start starts the background processing loop. It processes items as they
// arrive until Stop is called or the provided context is cancelled.
func (p *QueueProcessor) Start(ctx context.Context) {
	p.mu.Lock()
	if p.processing {
		p.mu.Unlock()
		return
	}
	p.processing = true
	p.ctx, p.cancel = context.WithCancel(ctx)
	p.mu.Unlock()

	go p.run()
}

// run is the background processing goroutine.
func (p *QueueProcessor) run() {
	for {
		p.mu.Lock()
		ctx := p.ctx
		p.mu.Unlock()

		// Process any available items.
		if p.queue.Len() > 0 {
			// Process ignoring errors in background mode — errors are
			// reported via onError callback, but don't stop the loop.
			_ = p.processOne(ctx)
		}

		// Wait for new items or cancellation.
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.processing = false
			p.mu.Unlock()
			return
		case <-p.notify:
			// New item signaled, loop back to process.
		}
	}
}

// processOne pops and processes a single item with retry logic.
func (p *QueueProcessor) processOne(ctx context.Context) error {
	item := p.queue.Pop()
	if item == nil {
		return nil
	}

	if err := ctx.Err(); err != nil {
		p.queue.Push(item)
		return err
	}

	if err := p.handler(item); err != nil {
		item.Retries++
		if item.Retries < item.MaxRetries {
			p.queue.Push(item)
			p.signal()
			return nil // will retry
		}
		p.mu.Lock()
		onErr := p.onError
		p.mu.Unlock()
		if onErr != nil {
			onErr(item, err)
		}
		return err
	}
	return nil
}

// Stop stops the background processing loop.
func (p *QueueProcessor) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.processing = false
}

// Pending returns the number of items waiting to be processed.
func (p *QueueProcessor) Pending() int {
	return p.queue.Len()
}

// Drain removes and returns all pending items from the queue in priority order.
func (p *QueueProcessor) Drain() []*QueueItem {
	var items []*QueueItem
	for {
		item := p.queue.Pop()
		if item == nil {
			break
		}
		items = append(items, item)
	}
	return items
}
