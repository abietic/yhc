package bridge

import (
	"sync"
	"time"
)

// ClientType identifies the kind of connected client.
type ClientType string

const (
	ClientTypeTUI ClientType = "tui"
	ClientTypeACP ClientType = "acp"
	ClientTypeIDE ClientType = "ide"
	ClientTypeMCP ClientType = "mcp"
)

// ClientStatus represents the health state of a connected client.
type ClientStatus string

const (
	ClientStatusActive       ClientStatus = "active"
	ClientStatusUnresponsive ClientStatus = "unresponsive"
	ClientStatusDisconnected ClientStatus = "disconnected"
)

// Client represents a connected client in the registry.
type Client struct {
	// ID uniquely identifies this client.
	ID string

	// Type is the kind of client (TUI, ACP, IDE, MCP).
	Type ClientType

	// Name is an optional human-readable name for the client.
	Name string

	// ConnectedAt is when the client first connected.
	ConnectedAt time.Time

	// LastSeen is the last time we received activity from this client.
	LastSeen time.Time

	// Status is the current health state.
	Status ClientStatus

	// Topics is the set of topics this client is subscribed to.
	Topics []Topic

	// observer is the client's associated observer (if any).
	observer *Observer

	// mu protects mutable fields (LastSeen, Status).
	mu sync.Mutex
}

// Touch updates the client's LastSeen timestamp to now.
func (c *Client) Touch() {
	c.mu.Lock()
	c.LastSeen = time.Now()
	c.mu.Unlock()
}

// SetStatus updates the client's health status.
func (c *Client) SetStatus(status ClientStatus) {
	c.mu.Lock()
	c.Status = status
	c.mu.Unlock()
}

// GetStatus returns the client's current status.
func (c *Client) GetStatus() ClientStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Status
}

// Observer returns the client's associated state observer, or nil if none.
func (c *Client) Observer() *Observer {
	return c.observer
}

// RegisterClientOpts configures a new client registration.
type RegisterClientOpts struct {
	ID     string
	Type   ClientType
	Name   string
	Topics []Topic
}

// RegisterClient adds a new client to the registry and creates an associated
// observer subscribed to the client's specified topics.
func (s *StateStore) RegisterClient(opts RegisterClientOpts) *Client {
	now := time.Now()

	// Create the observer for this client.
	var obs *Observer
	if len(opts.Topics) > 0 {
		obs = s.SubscribeWithID(opts.ID, opts.Topics...)
	} else {
		obs = s.SubscribeWithID(opts.ID)
	}

	client := &Client{
		ID:          opts.ID,
		Type:        opts.Type,
		Name:        opts.Name,
		ConnectedAt: now,
		LastSeen:    now,
		Status:      ClientStatusActive,
		Topics:      opts.Topics,
		observer:    obs,
	}

	s.clientsMu.Lock()
	s.clients[opts.ID] = client
	s.clientsMu.Unlock()

	// Notify about client connection.
	s.Set(FieldConnectedClients, s.ClientCount())

	return client
}

// UnregisterClient removes a client from the registry and cleans up its observer.
func (s *StateStore) UnregisterClient(id string) {
	s.clientsMu.Lock()
	client, ok := s.clients[id]
	if ok {
		delete(s.clients, id)
	}
	s.clientsMu.Unlock()

	if !ok {
		return
	}

	// Close the observer to stop delivery and free resources.
	if client.observer != nil {
		client.observer.Close()
	}

	client.SetStatus(ClientStatusDisconnected)

	// Notify about client disconnection.
	s.Set(FieldConnectedClients, s.ClientCount())
}

// GetClient returns a client by ID, or nil if not found.
func (s *StateStore) GetClient(id string) *Client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	return s.clients[id]
}

// ListClients returns a slice of all currently registered clients.
func (s *StateStore) ListClients() []*Client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	result := make([]*Client, 0, len(s.clients))
	for _, c := range s.clients {
		result = append(result, c)
	}
	return result
}

// ClientCount returns the number of currently registered clients.
func (s *StateStore) ClientCount() int {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	return len(s.clients)
}

// CheckClientHealth inspects all registered clients and marks those whose
// LastSeen exceeds the given timeout as unresponsive.
func (s *StateStore) CheckClientHealth(timeout time.Duration) []*Client {
	now := time.Now()
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	var stale []*Client
	for _, c := range s.clients {
		c.mu.Lock()
		if c.Status == ClientStatusActive && now.Sub(c.LastSeen) > timeout {
			c.Status = ClientStatusUnresponsive
			stale = append(stale, c)
		}
		c.mu.Unlock()
	}
	return stale
}

// DisconnectStaleClients unregisters all clients whose LastSeen exceeds the
// given timeout. Returns the IDs of disconnected clients.
func (s *StateStore) DisconnectStaleClients(timeout time.Duration) []string {
	now := time.Now()

	// Collect stale client IDs while holding the read lock.
	s.clientsMu.RLock()
	var staleIDs []string
	for id, c := range s.clients {
		c.mu.Lock()
		if now.Sub(c.LastSeen) > timeout {
			staleIDs = append(staleIDs, id)
		}
		c.mu.Unlock()
	}
	s.clientsMu.RUnlock()

	// Unregister each stale client (this acquires the write lock per call).
	for _, id := range staleIDs {
		s.UnregisterClient(id)
	}
	return staleIDs
}
