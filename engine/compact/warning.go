package compact

import "sync"

// CompactWarningState tracks whether the "context left until autocompact"
// warning should be suppressed. Suppressed immediately after successful
// compaction since token counts aren't accurate until the next API response.
//
// Reference: src/services/compact/compactWarningState.ts (18 lines)
type CompactWarningState struct {
	mu         sync.RWMutex
	suppressed bool
	listeners  []func(bool)
}

// NewCompactWarningState creates a new warning state tracker.
func NewCompactWarningState() *CompactWarningState {
	return &CompactWarningState{}
}

// IsSuppressed returns whether the compact warning is currently suppressed.
func (s *CompactWarningState) IsSuppressed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.suppressed
}

// Suppress suppresses the compact warning. Call after successful compaction.
func (s *CompactWarningState) Suppress() {
	s.mu.Lock()
	s.suppressed = true
	listeners := make([]func(bool), len(s.listeners))
	copy(listeners, s.listeners)
	s.mu.Unlock()
	for _, cb := range listeners {
		cb(true)
	}
}

// Clear clears the compact warning suppression. Called at start of new compact attempt.
func (s *CompactWarningState) Clear() {
	s.mu.Lock()
	s.suppressed = false
	listeners := make([]func(bool), len(s.listeners))
	copy(listeners, s.listeners)
	s.mu.Unlock()
	for _, cb := range listeners {
		cb(false)
	}
}

// OnChange registers a listener for suppression state changes.
func (s *CompactWarningState) OnChange(cb func(bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, cb)
}
