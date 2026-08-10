package bridge

import (
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	// DefaultDebounceInterval is the default debounce window for batching
	// rapid state changes before delivering them to the TUI. 16ms corresponds
	// to ~60fps, preventing UI flicker from high-frequency state updates.
	DefaultDebounceInterval = 16 * time.Millisecond
)

// StateUpdateMsg is a Bubble Tea message carrying batched state changes from
// the bridge StateStore to the TUI's Update loop. The TUI model should handle
// this message type to reflect state changes in the UI.
type StateUpdateMsg struct {
	// Changes contains the batched state changes since the last delivery.
	Changes []StateChange

	// Snapshot is an optional point-in-time state snapshot taken at delivery time.
	// It is only populated if WithSnapshot was set on the TUIBridge.
	Snapshot *StateSnapshot
}

// TUIBridgeOption configures the TUIBridge at creation time.
type TUIBridgeOption func(*TUIBridge)

// WithDebounceInterval sets the debounce window for batching state changes.
// A value of 0 disables debouncing (immediate delivery).
func WithDebounceInterval(d time.Duration) TUIBridgeOption {
	return func(b *TUIBridge) {
		b.debounceInterval = d
	}
}

// WithTUITopics limits the TUI subscription to specific topics.
// By default, the TUI subscribes to all topics.
func WithTUITopics(topics ...Topic) TUIBridgeOption {
	return func(b *TUIBridge) {
		b.topics = topics
	}
}

// WithSnapshotOnDelivery causes each StateUpdateMsg to include a full state
// snapshot at delivery time. This is useful when the TUI needs the complete
// current state rather than just deltas.
func WithSnapshotOnDelivery() TUIBridgeOption {
	return func(b *TUIBridge) {
		b.includeSnapshot = true
	}
}

// TUIBridge translates StateStore changes into Bubble Tea messages for the TUI.
// It subscribes to the StateStore, debounces rapid changes, and produces
// tea.Msg values that the TUI can consume in its Update loop.
//
// Usage:
//
//	bridge := NewTUIBridge(store, WithTUITopics(TopicConversation, TopicTokens))
//	// In your Bubble Tea program, use bridge.Listen() as a tea.Cmd:
//	func (m model) Init() tea.Cmd {
//	    return bridge.Listen()
//	}
//	func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
//	    switch msg := msg.(type) {
//	    case StateUpdateMsg:
//	        // Handle state changes...
//	        return m, bridge.Listen() // re-subscribe for next batch
//	    }
//	    return m, nil
//	}
type TUIBridge struct {
	store    *StateStore
	observer *Observer

	debounceInterval time.Duration
	topics           []Topic
	includeSnapshot  bool

	mu      sync.Mutex
	started bool
	stopped bool
	stopCh  chan struct{}
}

// NewTUIBridge creates a TUI bridge that subscribes to the given StateStore.
// The bridge does not start consuming changes until Listen() is called.
func NewTUIBridge(store *StateStore, opts ...TUIBridgeOption) *TUIBridge {
	b := &TUIBridge{
		store:            store,
		debounceInterval: DefaultDebounceInterval,
		stopCh:           make(chan struct{}),
	}
	for _, opt := range opts {
		opt(b)
	}

	// Create the observer with the configured topics.
	if len(b.topics) > 0 {
		b.observer = store.Subscribe(b.topics...)
	} else {
		b.observer = store.Subscribe() // all topics
	}

	b.started = true
	return b
}

// Listen returns a tea.Cmd that waits for the next batch of state changes
// and delivers them as a StateUpdateMsg. The TUI should call this in Init()
// and again after processing each StateUpdateMsg to maintain the subscription.
//
// If the bridge has been stopped, Listen returns nil (no-op command).
func (b *TUIBridge) Listen() tea.Cmd {
	return func() tea.Msg {
		b.mu.Lock()
		if b.stopped {
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()

		return b.waitForChanges()
	}
}

// Stop closes the bridge's observer and stops the subscription.
// After Stop, Listen() returns nil commands. It is safe to call multiple times.
func (b *TUIBridge) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return
	}
	b.stopped = true
	close(b.stopCh)

	if b.observer != nil {
		b.observer.Close()
	}
}

// IsRunning returns whether the bridge is actively listening for changes.
func (b *TUIBridge) IsRunning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started && !b.stopped
}

// waitForChanges blocks until at least one state change arrives, then collects
// additional changes within the debounce window before returning a batch.
func (b *TUIBridge) waitForChanges() tea.Msg {
	ch := b.observer.Changes()

	// Wait for the first change (or stop signal).
	var first StateChange
	select {
	case change, ok := <-ch:
		if !ok {
			// Observer was closed.
			return nil
		}
		first = change
	case <-b.stopCh:
		return nil
	}

	// Collect additional changes within the debounce window.
	batch := []StateChange{first}

	if b.debounceInterval <= 0 {
		// No debouncing: deliver immediately.
		return b.buildMsg(batch)
	}

	timer := time.NewTimer(b.debounceInterval)
	defer timer.Stop()

	for {
		select {
		case change, ok := <-ch:
			if !ok {
				// Observer closed during debounce.
				return b.buildMsg(batch)
			}
			batch = append(batch, change)
		case <-timer.C:
			// Debounce window expired.
			return b.buildMsg(batch)
		case <-b.stopCh:
			return b.buildMsg(batch)
		}
	}
}

// buildMsg creates a StateUpdateMsg from the collected changes.
func (b *TUIBridge) buildMsg(changes []StateChange) tea.Msg {
	if len(changes) == 0 {
		return nil
	}

	msg := StateUpdateMsg{
		Changes: changes,
	}

	if b.includeSnapshot {
		msg.Snapshot = b.store.Snapshot()
	}

	return msg
}
