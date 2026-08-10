package bridge

import (
	"fmt"
	"sync"
)

// PluginStateHandler is the callback signature for plugins that want to be
// notified when specific state topics change. The handler receives the
// StateChange that triggered the notification.
type PluginStateHandler func(change StateChange)

// PluginStateWriter provides a scoped write interface for plugins to emit
// their own state changes. It namespaces plugin-emitted fields to prevent
// collisions with core state fields.
type PluginStateWriter struct {
	pluginName string
	store      *StateStore
}

// Set updates a plugin-scoped state field and notifies observers.
// The field name is automatically prefixed with "plugin.<pluginName>." to
// prevent collisions with core state fields.
func (w *PluginStateWriter) Set(field string, value any) StateChange {
	scopedField := StateField(fmt.Sprintf("plugin.%s.%s", w.pluginName, field))
	return w.store.Set(scopedField, value)
}

// Update applies a batch of plugin-scoped field changes atomically.
func (w *PluginStateWriter) Update(changes map[string]any) []StateChange {
	if len(changes) == 0 {
		return nil
	}
	scoped := make(map[StateField]any, len(changes))
	for field, value := range changes {
		scopedField := StateField(fmt.Sprintf("plugin.%s.%s", w.pluginName, field))
		scoped[scopedField] = value
	}
	return w.store.Update(scoped)
}

// PluginName returns the name of the plugin this writer belongs to.
func (w *PluginStateWriter) PluginName() string {
	return w.pluginName
}

// PluginEventHooks manages bidirectional state event subscriptions for plugins.
// Plugins can:
//  1. Subscribe to state topics and receive notifications when those topics change.
//  2. Emit their own state changes through a scoped writer.
//
// All plugin-emitted state goes through the standard observer pipeline, so other
// observers (including TUI bridge and other plugins) see the changes.
type PluginEventHooks struct {
	store *StateStore

	mu            sync.RWMutex
	subscriptions map[string]*pluginSubscription // plugin name -> subscription
	writers       map[string]*PluginStateWriter  // plugin name -> writer
}

// pluginSubscription tracks a plugin's topic subscriptions and handlers.
type pluginSubscription struct {
	observer *Observer
	handlers []PluginStateHandler
	stopCh   chan struct{}
	done     chan struct{}
}

// NewPluginEventHooks creates a new PluginEventHooks instance tied to the given store.
func NewPluginEventHooks(store *StateStore) *PluginEventHooks {
	return &PluginEventHooks{
		store:         store,
		subscriptions: make(map[string]*pluginSubscription),
		writers:       make(map[string]*PluginStateWriter),
	}
}

// Subscribe registers a plugin to receive notifications for the specified topics.
// The handler is called asynchronously (in a dedicated goroutine) whenever a
// matching state change occurs. Multiple calls to Subscribe for the same plugin
// add additional handlers.
//
// Returns an error if the plugin name is empty.
func (h *PluginEventHooks) Subscribe(pluginName string, handler PluginStateHandler, topics ...Topic) error {
	if pluginName == "" {
		return fmt.Errorf("plugin name cannot be empty")
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	sub, exists := h.subscriptions[pluginName]
	if !exists {
		// Create a new subscription for this plugin.
		var obs *Observer
		if len(topics) > 0 {
			obs = h.store.SubscribeWithID(fmt.Sprintf("plugin-%s", pluginName), topics...)
		} else {
			obs = h.store.SubscribeWithID(fmt.Sprintf("plugin-%s", pluginName))
		}

		sub = &pluginSubscription{
			observer: obs,
			handlers: []PluginStateHandler{handler},
			stopCh:   make(chan struct{}),
			done:     make(chan struct{}),
		}
		h.subscriptions[pluginName] = sub

		// Start the dispatch goroutine.
		go h.dispatchLoop(sub)
	} else {
		sub.handlers = append(sub.handlers, handler)
	}

	return nil
}

// Writer returns a scoped state writer for the given plugin. The writer allows
// the plugin to emit state changes that go through the standard observer path.
// If the plugin already has a writer, the existing one is returned.
func (h *PluginEventHooks) Writer(pluginName string) *PluginStateWriter {
	h.mu.Lock()
	defer h.mu.Unlock()

	if w, exists := h.writers[pluginName]; exists {
		return w
	}

	w := &PluginStateWriter{
		pluginName: pluginName,
		store:      h.store,
	}
	h.writers[pluginName] = w
	return w
}

// Unsubscribe removes a plugin's subscription and stops its dispatch goroutine.
// It is safe to call for plugins that are not subscribed.
func (h *PluginEventHooks) Unsubscribe(pluginName string) {
	h.mu.Lock()
	sub, exists := h.subscriptions[pluginName]
	if !exists {
		h.mu.Unlock()
		return
	}
	delete(h.subscriptions, pluginName)
	delete(h.writers, pluginName)
	h.mu.Unlock()

	// Signal the dispatch goroutine to stop.
	close(sub.stopCh)
	// Close the observer (this also unblocks the dispatch loop).
	sub.observer.Close()
	// Wait for the dispatch goroutine to finish.
	<-sub.done
}

// UnsubscribeAll removes all plugin subscriptions and stops all dispatch goroutines.
func (h *PluginEventHooks) UnsubscribeAll() {
	h.mu.Lock()
	subs := make(map[string]*pluginSubscription, len(h.subscriptions))
	for k, v := range h.subscriptions {
		subs[k] = v
	}
	h.subscriptions = make(map[string]*pluginSubscription)
	h.writers = make(map[string]*PluginStateWriter)
	h.mu.Unlock()

	for _, sub := range subs {
		close(sub.stopCh)
		sub.observer.Close()
		<-sub.done
	}
}

// SubscribedPlugins returns the names of all currently subscribed plugins.
func (h *PluginEventHooks) SubscribedPlugins() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	names := make([]string, 0, len(h.subscriptions))
	for name := range h.subscriptions {
		names = append(names, name)
	}
	return names
}

// dispatchLoop reads changes from the observer channel and dispatches them
// to all registered handlers for the plugin. It runs until the stopCh is
// closed or the observer channel is closed.
func (h *PluginEventHooks) dispatchLoop(sub *pluginSubscription) {
	defer close(sub.done)

	for {
		select {
		case <-sub.stopCh:
			return
		case change, ok := <-sub.observer.Changes():
			if !ok {
				return
			}
			// Dispatch to all handlers. Read handlers under lock to handle
			// concurrent Subscribe calls adding new handlers.
			h.mu.RLock()
			handlers := make([]PluginStateHandler, len(sub.handlers))
			copy(handlers, sub.handlers)
			h.mu.RUnlock()

			for _, handler := range handlers {
				h.safeCall(handler, change)
			}
		}
	}
}

// safeCall invokes a handler with panic recovery.
func (h *PluginEventHooks) safeCall(handler PluginStateHandler, change StateChange) {
	defer func() {
		_ = recover() // Swallow panics from plugin handlers to prevent cascade.
	}()
	handler(change)
}
