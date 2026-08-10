package permission

import "sync"

// BypassKillswitch tracks whether bypass-permissions mode should be
// force-disabled. In the reference, this is gated behind a feature
// flag service (Statsig). In Go, it uses a simple check function.
//
// Reference: src/utils/permissions/bypassPermissionsKillswitch.ts (155 lines)
type BypassKillswitch struct {
	mu      sync.Mutex
	checked bool
	killed  bool
	checkFn func() bool
}

// NewBypassKillswitch creates a killswitch with the given check function.
// The check function returns true if bypass should be disabled.
func NewBypassKillswitch(checkFn func() bool) *BypassKillswitch {
	return &BypassKillswitch{checkFn: checkFn}
}

// CheckAndDisable runs the killswitch check once (idempotent).
// Returns true if bypass was disabled.
func (k *BypassKillswitch) CheckAndDisable() bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.checked {
		return k.killed
	}
	k.checked = true

	if k.checkFn != nil && k.checkFn() {
		k.killed = true
		return true
	}
	return false
}

// IsKilled returns whether bypass has been killed.
func (k *BypassKillswitch) IsKilled() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.killed
}

// Reset allows the check to run again (for testing or session restart).
func (k *BypassKillswitch) Reset() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.checked = false
	k.killed = false
}
