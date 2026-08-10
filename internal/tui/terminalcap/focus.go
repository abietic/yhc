package terminalcap

import "sync/atomic"

// FocusStatus is the observed terminal focus state.
type FocusStatus string

const (
	FocusUnknown FocusStatus = "unknown"
	FocusFocused FocusStatus = "focused"
	FocusBlurred FocusStatus = "blurred"
)

// FocusState shares focus observations between the Bubble Tea model and
// notification delivery without introducing a UI dependency into the engine.
type FocusState struct {
	reporting bool
	status    atomic.Uint32
}

// NewFocusState creates an unknown focus state. Unknown is intentionally
// treated as focused so unsupported terminals never receive notification spam.
func NewFocusState(reporting bool) *FocusState {
	return &FocusState{reporting: reporting}
}

// SetFocused records a reliable focus event.
func (s *FocusState) SetFocused(focused bool) {
	if s == nil {
		return
	}
	if focused {
		s.status.Store(1)
		return
	}
	s.status.Store(2)
}

// Reset marks focus as unknown after terminal ownership is reacquired.
func (s *FocusState) Reset() {
	if s != nil {
		s.status.Store(0)
	}
}

// Status returns the current focus state.
func (s *FocusState) Status() FocusStatus {
	if s == nil {
		return FocusUnknown
	}
	switch s.status.Load() {
	case 1:
		return FocusFocused
	case 2:
		return FocusBlurred
	default:
		return FocusUnknown
	}
}

// ExternalNotificationsAllowed reports whether an external notification may
// be sent. A reliable blur event is required.
func (s *FocusState) ExternalNotificationsAllowed() bool {
	return s != nil && s.reporting && s.Status() == FocusBlurred
}
