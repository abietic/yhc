package fixture

import "sync"

type Store struct {
	mu     sync.Mutex
	values map[string][]byte
}

func NewStore() *Store { return &Store{values: make(map[string][]byte)} }

func (store *Store) Update(key string, transform func([]byte) []byte) []byte {
	store.mu.Lock()
	defer store.mu.Unlock()
	next := transform(store.values[key])
	store.values[key] = next
	return next
}

func (store *Store) Get(key string) []byte {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.values[key]
}
