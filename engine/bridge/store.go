package bridge

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultHistorySize is the default number of recent state changes to buffer.
	DefaultHistorySize = 256

	// DefaultObserverBuffer is the default channel buffer size for observers.
	DefaultObserverBuffer = 64
)

// StoreOption configures the StateStore at creation time.
type StoreOption func(*StateStore)

// WithHistorySize sets the history ring buffer capacity.
func WithHistorySize(n int) StoreOption {
	return func(s *StateStore) {
		if n > 0 {
			s.historySize = n
		}
	}
}

// WithObserverBuffer sets the default channel buffer size for new observers.
func WithObserverBuffer(n int) StoreOption {
	return func(s *StateStore) {
		if n > 0 {
			s.observerBuffer = n
		}
	}
}

// StateStore is a thread-safe centralized store that holds the current state of
// the agent system and supports reactive observation via channel-based subscriptions.
//
// It mirrors the reference implementation's Store<AppState> from src/state/store.ts,
// adapted for Go's concurrency model. State changes are atomic and observers receive
// non-blocking notifications on subscribed topics.
type StateStore struct {
	// version is the monotonically increasing change counter.
	version atomic.Uint64

	// mu protects fields and observers.
	mu sync.RWMutex

	// fields holds the current state values.
	fields map[StateField]any

	// observers is the set of active observers.
	observers map[*Observer]struct{}

	// history is a ring buffer of recent state changes.
	history []StateChange

	// historyHead is the write position in the ring buffer.
	historyHead int

	// historyLen tracks how many slots in history are filled.
	historyLen int

	// historySize is the capacity of the ring buffer.
	historySize int

	// observerBuffer is the default channel buffer for new observers.
	observerBuffer int

	// clients is the registry of connected clients.
	clientsMu sync.RWMutex
	clients   map[string]*Client
}

// NewStateStore creates a new StateStore with the given options.
func NewStateStore(opts ...StoreOption) *StateStore {
	s := &StateStore{
		fields:         make(map[StateField]any),
		observers:      make(map[*Observer]struct{}),
		historySize:    DefaultHistorySize,
		observerBuffer: DefaultObserverBuffer,
		clients:        make(map[string]*Client),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.history = make([]StateChange, s.historySize)
	return s
}

// Get returns the current value of a state field.
func (s *StateStore) Get(field StateField) any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fields[field]
}

// GetString returns the current string value of a state field.
func (s *StateStore) GetString(field StateField) string {
	v, _ := s.Get(field).(string)
	return v
}

// GetInt returns the current int value of a state field.
func (s *StateStore) GetInt(field StateField) int {
	v, _ := s.Get(field).(int)
	return v
}

// GetBool returns the current bool value of a state field.
func (s *StateStore) GetBool(field StateField) bool {
	v, _ := s.Get(field).(bool)
	return v
}

// Set updates a single state field and notifies observers.
// Returns the StateChange that was applied.
func (s *StateStore) Set(field StateField, value any) StateChange {
	s.mu.Lock()
	oldValue := s.fields[field]
	s.fields[field] = value

	ver := s.version.Add(1)
	change := StateChange{
		Field:     field,
		Topic:     TopicForField(field),
		OldValue:  oldValue,
		NewValue:  value,
		Timestamp: time.Now(),
		Version:   ver,
	}

	s.appendHistory(change)
	observers := s.collectObservers()
	s.mu.Unlock()

	s.notifyObservers(observers, []StateChange{change})
	return change
}

// Update applies a batch of field changes atomically. All fields are updated
// under a single lock acquisition and observers receive a single batch notification.
// This prevents observers from seeing intermediate states.
func (s *StateStore) Update(changes map[StateField]any) []StateChange {
	if len(changes) == 0 {
		return nil
	}

	s.mu.Lock()
	now := time.Now()
	result := make([]StateChange, 0, len(changes))

	for field, value := range changes {
		oldValue := s.fields[field]
		s.fields[field] = value

		ver := s.version.Add(1)
		change := StateChange{
			Field:     field,
			Topic:     TopicForField(field),
			OldValue:  oldValue,
			NewValue:  value,
			Timestamp: now,
			Version:   ver,
		}
		s.appendHistory(change)
		result = append(result, change)
	}

	observers := s.collectObservers()
	s.mu.Unlock()

	s.notifyObservers(observers, result)
	return result
}

// Snapshot returns an immutable point-in-time copy of the full state.
// Modifications to the returned snapshot do not affect the store.
func (s *StateStore) Snapshot() *StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fields := make(map[StateField]any, len(s.fields))
	for k, v := range s.fields {
		fields[k] = v
	}

	return &StateSnapshot{
		Version:   s.version.Load(),
		Timestamp: time.Now(),
		Fields:    fields,
	}
}

// SnapshotSlice returns a point-in-time copy containing only fields belonging
// to the specified topics.
func (s *StateStore) SnapshotSlice(topics ...Topic) *StateSnapshot {
	topicSet := make(map[Topic]bool, len(topics))
	for _, t := range topics {
		if t == TopicAll {
			return s.Snapshot()
		}
		topicSet[t] = true
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	fields := make(map[StateField]any)
	for field, value := range s.fields {
		if topicSet[TopicForField(field)] {
			fields[field] = value
		}
	}

	return &StateSnapshot{
		Version:   s.version.Load(),
		Timestamp: time.Now(),
		Fields:    fields,
	}
}

// Version returns the current store version (number of state changes applied).
func (s *StateStore) Version() uint64 {
	return s.version.Load()
}

// History returns up to the last n state changes, ordered oldest to newest.
// If n <= 0 or exceeds the history buffer size, all available history is returned.
func (s *StateStore) History(n int) []StateChange {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n <= 0 || n > s.historyLen {
		n = s.historyLen
	}
	if n == 0 {
		return nil
	}

	result := make([]StateChange, n)
	// Read from the ring buffer, starting from the oldest of the last n entries.
	start := (s.historyHead - n + s.historySize) % s.historySize
	for i := 0; i < n; i++ {
		idx := (start + i) % s.historySize
		result[i] = s.history[idx]
	}
	return result
}

// HistoryForTopic returns up to the last n state changes for a specific topic.
func (s *StateStore) HistoryForTopic(topic Topic, n int) []StateChange {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.historyLen == 0 {
		return nil
	}

	var result []StateChange
	count := s.historyLen
	start := (s.historyHead - count + s.historySize) % s.historySize

	for i := 0; i < count; i++ {
		idx := (start + i) % s.historySize
		entry := s.history[idx]
		if topic == TopicAll || entry.Topic == topic {
			result = append(result, entry)
			if n > 0 && len(result) >= n {
				break
			}
		}
	}
	return result
}

// appendHistory adds a change to the ring buffer. Caller must hold s.mu.
func (s *StateStore) appendHistory(change StateChange) {
	s.history[s.historyHead] = change
	s.historyHead = (s.historyHead + 1) % s.historySize
	if s.historyLen < s.historySize {
		s.historyLen++
	}
}

// collectObservers returns a snapshot of active observers. Caller must hold s.mu (read or write).
func (s *StateStore) collectObservers() []*Observer {
	if len(s.observers) == 0 {
		return nil
	}
	obs := make([]*Observer, 0, len(s.observers))
	for o := range s.observers {
		obs = append(obs, o)
	}
	return obs
}

// notifyObservers sends state changes to matching observers without blocking.
// If an observer's channel is full, the change is dropped (non-blocking send).
func (s *StateStore) notifyObservers(observers []*Observer, changes []StateChange) {
	for _, obs := range observers {
		for _, change := range changes {
			if obs.matchesTopic(change.Topic) {
				obs.send(change)
			}
		}
	}
}
