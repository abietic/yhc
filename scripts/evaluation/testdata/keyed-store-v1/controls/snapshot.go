package fixture

import (
	"bytes"
	"sync"
)

// Reads may observe the previous committed value while its key is updating.
// The task permits this as well as waiting for the update to complete.
type item struct {
	update sync.Mutex
	value  []byte
}

type Store struct {
	mu    sync.Mutex
	items map[string]*item
}

func NewStore() *Store { return &Store{items: make(map[string]*item)} }

func (store *Store) find(key string) *item {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.items[key] == nil {
		store.items[key] = &item{}
	}
	return store.items[key]
}

func (store *Store) Update(key string, transform func([]byte) []byte) []byte {
	entry := store.find(key)
	entry.update.Lock()
	defer entry.update.Unlock()
	store.mu.Lock()
	previous := bytes.Clone(entry.value)
	store.mu.Unlock()
	next := transform(previous)
	store.mu.Lock()
	entry.value = bytes.Clone(next)
	store.mu.Unlock()
	return bytes.Clone(next)
}

func (store *Store) Get(key string) []byte {
	entry := store.find(key)
	store.mu.Lock()
	defer store.mu.Unlock()
	return bytes.Clone(entry.value)
}
