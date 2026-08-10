package bridge

import (
	"sync"
)

// Observer represents a subscription to state changes. It receives notifications
// on its channel for changes matching its subscribed topics. The channel is non-blocking;
// if the observer cannot keep up, changes are dropped.
type Observer struct {
	// id uniquely identifies this observer.
	id string

	// ch is the channel where state changes are delivered.
	ch chan StateChange

	// topics is the set of topics this observer is subscribed to.
	topics map[Topic]bool

	// sendMu serializes channel sends and close to prevent data races.
	// Both send() and Close() acquire this lock before touching ch.
	sendMu sync.Mutex

	// closed indicates whether this observer has been unsubscribed.
	// Protected by sendMu.
	closed bool

	// dropped counts the number of changes that could not be delivered.
	// Protected by sendMu for writes.
	dropped uint64

	// store is the parent store (for unsubscribe).
	store *StateStore

	// once ensures Close is idempotent.
	once sync.Once
}

// Changes returns the read-only channel that delivers state changes to this observer.
func (o *Observer) Changes() <-chan StateChange {
	return o.ch
}

// Close unsubscribes this observer from the store and closes the delivery channel.
// It is safe to call multiple times.
func (o *Observer) Close() {
	o.once.Do(func() {
		// First remove from the store so no new notify calls reach us.
		o.store.removeObserver(o)

		// Then mark closed and close the channel under the send lock.
		o.sendMu.Lock()
		o.closed = true
		close(o.ch)
		o.sendMu.Unlock()
	})
}

// Dropped returns the number of state changes that were dropped because the
// observer's channel buffer was full.
func (o *Observer) Dropped() uint64 {
	o.sendMu.Lock()
	d := o.dropped
	o.sendMu.Unlock()
	return d
}

// ID returns the observer's unique identifier.
func (o *Observer) ID() string {
	return o.id
}

// matchesTopic returns true if this observer should receive changes for the given topic.
func (o *Observer) matchesTopic(topic Topic) bool {
	// Fast path: check closed without lock (harmless false positive leads to
	// a lock in send() which then rechecks closed).
	if o.topics[TopicAll] {
		return true
	}
	return o.topics[topic]
}

// send attempts a non-blocking send of a state change to the observer's channel.
// If the channel is full, the change is dropped and the drop counter is incremented.
// This is serialized with Close() to prevent send-on-closed-channel races.
func (o *Observer) send(change StateChange) {
	o.sendMu.Lock()
	if o.closed {
		o.sendMu.Unlock()
		return
	}
	select {
	case o.ch <- change:
	default:
		o.dropped++
	}
	o.sendMu.Unlock()
}

// Subscribe creates a new observer that receives state changes for the specified topics.
// If no topics are provided, the observer subscribes to all topics (TopicAll).
// The returned Observer must be closed when no longer needed to avoid resource leaks.
func (s *StateStore) Subscribe(topics ...Topic) *Observer {
	return s.SubscribeWithID("", topics...)
}

// SubscribeWithID creates a new observer with a specific ID. This is useful for
// associating observers with specific clients. If id is empty, a default is used.
func (s *StateStore) SubscribeWithID(id string, topics ...Topic) *Observer {
	topicSet := make(map[Topic]bool, len(topics))
	if len(topics) == 0 {
		topicSet[TopicAll] = true
	} else {
		for _, t := range topics {
			topicSet[t] = true
		}
	}

	obs := &Observer{
		id:     id,
		ch:     make(chan StateChange, s.observerBuffer),
		topics: topicSet,
		store:  s,
	}

	s.mu.Lock()
	s.observers[obs] = struct{}{}
	s.mu.Unlock()

	return obs
}

// removeObserver removes an observer from the store's observer set.
func (s *StateStore) removeObserver(obs *Observer) {
	s.mu.Lock()
	delete(s.observers, obs)
	s.mu.Unlock()
}

// ObserverCount returns the number of active observers.
func (s *StateStore) ObserverCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.observers)
}
