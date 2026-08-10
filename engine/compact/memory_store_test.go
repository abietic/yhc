package compact

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreAddLoadsExistingEntriesBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	first := NewMemoryStore(dir, 10)
	if err := first.Add(MemoryEntry{Content: "existing", CreatedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	second := NewMemoryStore(dir, 10)
	if err := second.Add(MemoryEntry{Content: "new", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	reloaded := NewMemoryStore(dir, 10)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	entries := reloaded.GetAll()
	if len(entries) != 2 || entries[0].Content != "existing" || entries[1].Content != "new" {
		t.Fatalf("add replaced persisted memory instead of merging: %#v", entries)
	}
}

func TestMemoryStoreConcurrentInstancesDoNotLoseEntries(t *testing.T) {
	dir := t.TempDir()
	const writers = 20
	var wait sync.WaitGroup
	errors := make(chan error, writers)
	for index := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			store := NewMemoryStore(dir, writers)
			errors <- store.Add(MemoryEntry{Content: fmt.Sprintf("entry-%d", index), CreatedAt: time.Now()})
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	store := NewMemoryStore(dir, writers)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if entries := store.GetAll(); len(entries) != writers {
		t.Fatalf("concurrent stores retained %d entries, want %d", len(entries), writers)
	}
}

func TestMemoryStoreRelevantOlderEntryOutranksRecentFallback(t *testing.T) {
	store := NewMemoryStore(t.TempDir(), 10)
	now := time.Now()
	_ = store.AddAll([]MemoryEntry{
		{Content: "provider fallback retries invalid credentials", Category: "recovery", CreatedAt: now.Add(-24 * time.Hour)},
		{Content: "updated welcome colors", Category: "ui", CreatedAt: now},
	})

	ranked := store.GetRelevant("provider fallback", 2)
	if len(ranked) != 2 || ranked[0].Category != "recovery" {
		t.Fatalf("relevance ranking did not beat recency: %#v", ranked)
	}
}
