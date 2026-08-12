package appserver

import (
	"errors"
	"sync"
)

var errReplayGap = errors.New("requested event cursor is no longer buffered")

type eventLog struct {
	mu          sync.Mutex
	capacity    int
	nextID      uint64
	events      []WireEvent
	subscribers map[chan WireEvent]struct{}
	closed      bool
}

func newEventLog(capacity int) *eventLog {
	if capacity <= 0 {
		capacity = defaultEventBuffer
	}
	return &eventLog{
		capacity:    capacity,
		nextID:      1,
		events:      make([]WireEvent, 0, capacity),
		subscribers: make(map[chan WireEvent]struct{}),
	}
}

func (l *eventLog) publish(event WireEvent) (WireEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return WireEvent{}, false
	}
	event.ID = l.nextID
	l.nextID++
	l.events = append(l.events, event)
	if len(l.events) > l.capacity {
		copy(l.events, l.events[len(l.events)-l.capacity:])
		l.events = l.events[:l.capacity]
	}
	for subscriber := range l.subscribers {
		select {
		case subscriber <- event:
		default:
			// A slow client reconnects from the bounded replay ring. Disconnecting
			// it keeps the engine event pump non-blocking without losing authority.
			delete(l.subscribers, subscriber)
			close(subscriber)
		}
	}
	return event, true
}

func (l *eventLog) subscribe(after uint64) ([]WireEvent, <-chan WireEvent, func(), ReplayGap, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	latest := l.nextID - 1
	earliest := latest + 1
	if len(l.events) > 0 {
		earliest = l.events[0].ID
	}
	if after > latest || (after > 0 && after+1 < earliest) {
		return nil, nil, nil, ReplayGap{Earliest: earliest, Latest: latest}, errReplayGap
	}
	replay := make([]WireEvent, 0, len(l.events))
	for _, event := range l.events {
		if event.ID > after {
			replay = append(replay, event)
		}
	}
	if l.closed {
		closed := make(chan WireEvent)
		close(closed)
		return replay, closed, func() {}, ReplayGap{}, nil
	}
	ch := make(chan WireEvent, 128)
	l.subscribers[ch] = struct{}{}
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			l.mu.Lock()
			if _, ok := l.subscribers[ch]; ok {
				delete(l.subscribers, ch)
				close(ch)
			}
			l.mu.Unlock()
		})
	}
	return replay, ch, unsubscribe, ReplayGap{}, nil
}

func (l *eventLog) latestCursor() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextID - 1
}

func (l *eventLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	for subscriber := range l.subscribers {
		close(subscriber)
		delete(l.subscribers, subscriber)
	}
}
