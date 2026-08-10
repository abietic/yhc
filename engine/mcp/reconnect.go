package mcp

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Reconnection configuration defaults.
const (
	defaultInitialBackoff   = 1 * time.Second
	defaultMaxBackoff       = 30 * time.Second
	defaultBackoffFactor    = 2.0
	defaultMaxAttempts      = 10
	defaultHealthInterval   = 30 * time.Second
	envMaxReconnectAttempts = "MCP_MAX_RECONNECT_ATTEMPTS"
)

// ReconnectConfig configures automatic reconnection behavior.
type ReconnectConfig struct {
	// InitialBackoff is the delay before the first reconnection attempt.
	InitialBackoff time.Duration
	// MaxBackoff is the maximum delay between reconnection attempts.
	MaxBackoff time.Duration
	// BackoffFactor is the multiplier applied to backoff after each failure.
	BackoffFactor float64
	// MaxAttempts is the maximum number of reconnection attempts before giving up.
	// Zero means use the default (10). Negative means unlimited.
	MaxAttempts int
	// HealthCheckInterval is how often to send ping/heartbeat checks.
	// Zero disables health monitoring.
	HealthCheckInterval time.Duration
	// OnReconnecting is called when a reconnection attempt starts.
	OnReconnecting func(attempt int, delay time.Duration)
	// OnReconnected is called after a successful reconnection.
	OnReconnected func(attempt int)
	// OnReconnectFailed is called when all reconnection attempts are exhausted.
	OnReconnectFailed func(lastErr error)
}

// DefaultReconnectConfig returns a ReconnectConfig with sensible defaults.
func DefaultReconnectConfig() ReconnectConfig {
	maxAttempts := defaultMaxAttempts
	if envVal := os.Getenv(envMaxReconnectAttempts); envVal != "" {
		if parsed, err := strconv.Atoi(envVal); err == nil && parsed > 0 {
			maxAttempts = parsed
		}
	}

	return ReconnectConfig{
		InitialBackoff:      defaultInitialBackoff,
		MaxBackoff:          defaultMaxBackoff,
		BackoffFactor:       defaultBackoffFactor,
		MaxAttempts:         maxAttempts,
		HealthCheckInterval: defaultHealthInterval,
	}
}

// ReconnectState tracks the state of reconnection attempts.
type ReconnectState struct {
	// Attempts is the number of reconnection attempts so far.
	Attempts int
	// LastError is the most recent error that triggered reconnection.
	LastError error
	// LastAttemptAt is when the last reconnection attempt was made.
	LastAttemptAt time.Time
	// Connected indicates whether the reconnection succeeded.
	Connected bool
}

// reconnectionManager handles automatic reconnection with exponential backoff.
type reconnectionManager struct {
	config ReconnectConfig
	client *MCPClient

	mu            sync.Mutex
	active        atomic.Bool
	state         ReconnectState
	cancelHealth  context.CancelFunc
	cancelReconn  context.CancelFunc
	healthRunning atomic.Bool

	// subscriptions holds resource URIs to re-subscribe after reconnect.
	subscriptions []string
}

// newReconnectionManager creates a reconnection manager for the given client.
func newReconnectionManager(client *MCPClient, config ReconnectConfig) *reconnectionManager {
	return &reconnectionManager{
		config: config,
		client: client,
	}
}

// Start enables automatic reconnection monitoring.
// Call this after initial successful connection.
func (rm *reconnectionManager) Start(ctx context.Context) {
	if rm.active.Load() {
		return
	}
	rm.active.Store(true)

	// Start health monitoring if configured.
	if rm.config.HealthCheckInterval > 0 {
		rm.startHealthMonitor(ctx)
	}
}

// Stop disables automatic reconnection and health monitoring.
func (rm *reconnectionManager) Stop() {
	rm.active.Store(false)
	rm.mu.Lock()
	if rm.cancelHealth != nil {
		rm.cancelHealth()
		rm.cancelHealth = nil
	}
	if rm.cancelReconn != nil {
		rm.cancelReconn()
		rm.cancelReconn = nil
	}
	rm.mu.Unlock()
}

// IsActive returns whether the reconnection manager is currently active.
func (rm *reconnectionManager) IsActive() bool {
	return rm.active.Load()
}

// State returns the current reconnection state.
func (rm *reconnectionManager) State() ReconnectState {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.state
}

// AddSubscription records a resource URI to re-subscribe after reconnection.
func (rm *reconnectionManager) AddSubscription(uri string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for _, s := range rm.subscriptions {
		if s == uri {
			return
		}
	}
	rm.subscriptions = append(rm.subscriptions, uri)
}

// RemoveSubscription removes a resource URI from the re-subscribe list.
func (rm *reconnectionManager) RemoveSubscription(uri string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for i, s := range rm.subscriptions {
		if s == uri {
			rm.subscriptions = append(rm.subscriptions[:i], rm.subscriptions[i+1:]...)
			return
		}
	}
}

// TriggerReconnect initiates the reconnection process due to a detected failure.
func (rm *reconnectionManager) TriggerReconnect(ctx context.Context, reason error) {
	if !rm.active.Load() {
		return
	}

	rm.mu.Lock()
	// Avoid multiple concurrent reconnection loops.
	if rm.state.LastError != nil && !rm.state.Connected {
		rm.mu.Unlock()
		return
	}
	rm.state = ReconnectState{
		LastError: reason,
		Connected: false,
	}

	reconnCtx, cancel := context.WithCancel(ctx)
	rm.cancelReconn = cancel
	rm.mu.Unlock()

	go rm.reconnectLoop(reconnCtx)
}

// reconnectLoop performs exponential backoff reconnection.
func (rm *reconnectionManager) reconnectLoop(ctx context.Context) {
	backoff := rm.config.InitialBackoff
	maxAttempts := rm.config.MaxAttempts

	for attempt := 1; maxAttempts < 0 || attempt <= maxAttempts; attempt++ {
		if !rm.active.Load() {
			return
		}

		rm.mu.Lock()
		rm.state.Attempts = attempt
		rm.state.LastAttemptAt = time.Now()
		rm.mu.Unlock()

		// Notify about reconnection attempt.
		if rm.config.OnReconnecting != nil {
			rm.config.OnReconnecting(attempt, backoff)
		}

		// Wait for backoff duration.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Attempt reconnection.
		err := rm.client.Reconnect(ctx)
		if err == nil {
			// Success - restore state.
			rm.mu.Lock()
			rm.state.Connected = true
			rm.state.LastError = nil
			subscriptions := make([]string, len(rm.subscriptions))
			copy(subscriptions, rm.subscriptions)
			rm.mu.Unlock()

			// Re-subscribe to resources.
			rm.restoreSubscriptions(ctx, subscriptions)

			// Notify success.
			if rm.config.OnReconnected != nil {
				rm.config.OnReconnected(attempt)
			}

			// Restart health monitoring.
			if rm.config.HealthCheckInterval > 0 {
				rm.startHealthMonitor(ctx)
			}
			return
		}

		rm.mu.Lock()
		rm.state.LastError = err
		rm.mu.Unlock()

		// Calculate next backoff with exponential increase.
		backoff = time.Duration(float64(backoff) * rm.config.BackoffFactor)
		if backoff > rm.config.MaxBackoff {
			backoff = rm.config.MaxBackoff
		}
	}

	// All attempts exhausted.
	rm.mu.Lock()
	lastErr := rm.state.LastError
	rm.mu.Unlock()

	if rm.config.OnReconnectFailed != nil {
		rm.config.OnReconnectFailed(lastErr)
	}
}

// restoreSubscriptions re-subscribes to resources after reconnection.
func (rm *reconnectionManager) restoreSubscriptions(ctx context.Context, uris []string) {
	for _, uri := range uris {
		if !rm.active.Load() {
			return
		}
		// Best-effort re-subscription; failures are non-fatal.
		_ = rm.client.Subscribe(ctx, uri)
	}
}

// startHealthMonitor launches a background goroutine that periodically pings
// the server to detect connection loss early.
func (rm *reconnectionManager) startHealthMonitor(ctx context.Context) {
	if rm.healthRunning.Load() {
		return
	}

	healthCtx, cancel := context.WithCancel(ctx)
	rm.mu.Lock()
	rm.cancelHealth = cancel
	rm.mu.Unlock()
	rm.healthRunning.Store(true)

	go func() {
		defer rm.healthRunning.Store(false)
		ticker := time.NewTicker(rm.config.HealthCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-healthCtx.Done():
				return
			case <-ticker.C:
				if !rm.active.Load() {
					return
				}
				err := rm.client.Ping(healthCtx)
				if err != nil && rm.active.Load() {
					rm.TriggerReconnect(ctx, fmt.Errorf("health check failed: %w", err))
					return
				}
			}
		}
	}()
}

// calculateBackoff computes the backoff duration for a given attempt number.
func calculateBackoff(attempt int, initial, max time.Duration, factor float64) time.Duration {
	if attempt <= 0 {
		return initial
	}
	backoff := float64(initial) * math.Pow(factor, float64(attempt-1))
	if time.Duration(backoff) > max {
		return max
	}
	return time.Duration(backoff)
}
