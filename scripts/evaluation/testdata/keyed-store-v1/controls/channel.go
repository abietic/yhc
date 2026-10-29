package fixture

import (
	"bytes"
	"sync"
)

type slot struct {
	token chan struct{}
	data  []byte
}

type Store struct {
	lookup sync.Mutex
	slots  map[string]*slot
}

func NewStore() *Store { return &Store{slots: make(map[string]*slot)} }

func (store *Store) slot(key string) *slot {
	store.lookup.Lock()
	defer store.lookup.Unlock()
	if store.slots[key] == nil {
		token := make(chan struct{}, 1)
		token <- struct{}{}
		store.slots[key] = &slot{token: token}
	}
	return store.slots[key]
}

func (store *Store) Update(key string, transform func([]byte) []byte) []byte {
	slot := store.slot(key)
	<-slot.token
	defer func() { slot.token <- struct{}{} }()
	result := transform(bytes.Clone(slot.data))
	slot.data = bytes.Clone(result)
	return bytes.Clone(slot.data)
}

func (store *Store) Get(key string) []byte {
	slot := store.slot(key)
	<-slot.token
	defer func() { slot.token <- struct{}{} }()
	return bytes.Clone(slot.data)
}
