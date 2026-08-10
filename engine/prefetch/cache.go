package prefetch

import (
	"sort"
	"sync"
	"time"
)

// PrefetchItem represents a single prefetched piece of context.
type PrefetchItem struct {
	Type     string        // "memory", "skill", "file", "git"
	Content  string        // The prefetched content
	Priority int           // Higher priority items are returned first
	Source   string        // Origin identifier (e.g. file path, skill name)
	CachedAt time.Time     // When this item was cached
	TTL      time.Duration // How long this item remains valid
}

// isExpired returns true if the item has exceeded its TTL.
func (item *PrefetchItem) isExpired(now time.Time) bool {
	if item.TTL <= 0 {
		return false
	}
	return now.After(item.CachedAt.Add(item.TTL))
}

// PrefetchCache provides a thread-safe cache for prefetched context items.
type PrefetchCache struct {
	items    map[string]*PrefetchItem
	mu       sync.RWMutex
	maxItems int
	maxAge   time.Duration
}

// NewPrefetchCache creates a new PrefetchCache with the given capacity and max age.
func NewPrefetchCache(maxItems int, maxAge time.Duration) *PrefetchCache {
	return &PrefetchCache{
		items:    make(map[string]*PrefetchItem),
		maxItems: maxItems,
		maxAge:   maxAge,
	}
}

// Set adds or replaces an item in the cache. If the cache is at capacity,
// the lowest-priority item is evicted.
func (c *PrefetchCache) Set(key string, item *PrefetchItem) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Set CachedAt if not already set.
	if item.CachedAt.IsZero() {
		item.CachedAt = time.Now()
	}

	// If key already exists, just replace it.
	if _, exists := c.items[key]; exists {
		c.items[key] = item
		return
	}

	// If at capacity, evict the lowest-priority item.
	if c.maxItems > 0 && len(c.items) >= c.maxItems {
		c.evictLowestPriority()
	}

	c.items[key] = item
}

// Get returns the item for the given key if it exists and is not expired.
func (c *PrefetchCache) Get(key string) (*PrefetchItem, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return nil, false
	}

	now := time.Now()

	// Check item-level TTL.
	if item.isExpired(now) {
		return nil, false
	}

	// Check cache-level maxAge.
	if c.maxAge > 0 && now.After(item.CachedAt.Add(c.maxAge)) {
		return nil, false
	}

	return item, true
}

// Invalidate removes a specific key from the cache.
func (c *PrefetchCache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
}

// InvalidateByType removes all items of the given type from the cache.
func (c *PrefetchCache) InvalidateByType(itemType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, item := range c.items {
		if item.Type == itemType {
			delete(c.items, key)
		}
	}
}

// Prune removes all expired items from the cache.
func (c *PrefetchCache) Prune() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, item := range c.items {
		if item.isExpired(now) {
			delete(c.items, key)
			continue
		}
		if c.maxAge > 0 && now.After(item.CachedAt.Add(c.maxAge)) {
			delete(c.items, key)
		}
	}
}

// All returns all valid (non-expired) items sorted by priority (highest first).
func (c *PrefetchCache) All() []*PrefetchItem {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	result := make([]*PrefetchItem, 0, len(c.items))

	for _, item := range c.items {
		if item.isExpired(now) {
			continue
		}
		if c.maxAge > 0 && now.After(item.CachedAt.Add(c.maxAge)) {
			continue
		}
		result = append(result, item)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Priority > result[j].Priority
	})

	return result
}

// evictLowestPriority removes the item with the lowest priority.
// Caller must hold c.mu write lock.
func (c *PrefetchCache) evictLowestPriority() {
	var lowestKey string
	var lowestPriority int
	first := true

	for key, item := range c.items {
		if first || item.Priority < lowestPriority {
			lowestKey = key
			lowestPriority = item.Priority
			first = false
		}
	}

	if !first {
		delete(c.items, lowestKey)
	}
}
