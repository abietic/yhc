package tui

import (
	"sort"
	"sync"

	"charm.land/glamour/v2"
)

const defaultMarkdownRendererPoolCapacity = 32

type markdownRendererEntry struct {
	key      rendererKey
	renderer *glamour.TermRenderer
	mu       sync.Mutex
	lastUse  uint64
}

type markdownRendererPool struct {
	mu        sync.Mutex
	capacity  int
	entries   map[rendererKey]*markdownRendererEntry
	useSeq    uint64
	creates   uint64
	evictions uint64
	peakSize  int
	factory   markdownRendererFactory
}

type markdownRendererPoolStats struct {
	Size      int
	Capacity  int
	Creates   uint64
	Evictions uint64
	PeakSize  int
}

type markdownRendererFactory func() (*glamour.TermRenderer, error)

func newMarkdownRendererPool(capacity int) *markdownRendererPool {
	if capacity <= 0 {
		capacity = defaultMarkdownRendererPoolCapacity
	}
	return &markdownRendererPool{
		capacity: capacity,
		entries:  make(map[rendererKey]*markdownRendererEntry, capacity),
	}
}

// acquire returns one atomic renderer-and-lock entry. Lookup, construction,
// insertion, and LRU ownership changes are serialized by the pool mutex. The
// entry mutex is independent so rendering never holds the pool mutex.
func (p *markdownRendererPool) acquire(
	key rendererKey,
	create markdownRendererFactory,
) *markdownRendererEntry {
	if p == nil || create == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitializedLocked()
	if p.factory != nil {
		create = p.factory
	}

	if entry, ok := p.entries[key]; ok {
		entry.lastUse = p.nextUseSequenceLocked()
		return entry
	}

	renderer, err := create()
	if err != nil || renderer == nil {
		return nil
	}

	if len(p.entries) >= p.capacity {
		p.evictLeastRecentlyUsedLocked()
	}
	entry := &markdownRendererEntry{
		key:      key,
		renderer: renderer,
		lastUse:  p.nextUseSequenceLocked(),
	}
	p.entries[key] = entry
	p.creates++
	if len(p.entries) > p.peakSize {
		p.peakSize = len(p.entries)
	}
	return entry
}

func (p *markdownRendererPool) ensureInitializedLocked() {
	if p.capacity <= 0 {
		p.capacity = defaultMarkdownRendererPoolCapacity
	}
	if p.entries == nil {
		p.entries = make(map[rendererKey]*markdownRendererEntry, p.capacity)
	}
}

func (p *markdownRendererPool) evictLeastRecentlyUsedLocked() {
	var oldest *markdownRendererEntry
	for _, entry := range p.entries {
		if oldest == nil || entry.lastUse < oldest.lastUse {
			oldest = entry
		}
	}
	if oldest == nil {
		return
	}
	delete(p.entries, oldest.key)
	p.evictions++
}

func (p *markdownRendererPool) nextUseSequenceLocked() uint64 {
	if p.useSeq == ^uint64(0) {
		entries := make([]*markdownRendererEntry, 0, len(p.entries))
		for _, entry := range p.entries {
			entries = append(entries, entry)
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].lastUse < entries[j].lastUse
		})
		for i, entry := range entries {
			entry.lastUse = uint64(i + 1)
		}
		p.useSeq = uint64(len(entries))
	}
	p.useSeq++
	return p.useSeq
}

// stats is a package-private verification seam. It is not a public runtime
// diagnostic and does not expose renderer or key contents.
func (p *markdownRendererPool) stats() markdownRendererPoolStats {
	if p == nil {
		return markdownRendererPoolStats{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitializedLocked()
	return markdownRendererPoolStats{
		Size:      len(p.entries),
		Capacity:  p.capacity,
		Creates:   p.creates,
		Evictions: p.evictions,
		PeakSize:  p.peakSize,
	}
}
