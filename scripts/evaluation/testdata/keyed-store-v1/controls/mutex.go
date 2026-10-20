package fixture

import (
	"bytes"
	"sync"
)

type item struct {
	mu    sync.Mutex
	value []byte
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
	entry.mu.Lock()
	defer entry.mu.Unlock()
	next := transform(bytes.Clone(entry.value))
	entry.value = bytes.Clone(next)
	return bytes.Clone(entry.value)
}

func (store *Store) Get(key string) []byte {
	entry := store.find(key)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return bytes.Clone(entry.value)
}
