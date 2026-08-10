package config

import "testing"

func TestDefaultMaxTurnsIsUnlimited(t *testing.T) {
	if got := DefaultConfig().MaxTurns; got != DefaultMaxTurns {
		t.Fatalf("DefaultConfig MaxTurns = %d, want %d", got, DefaultMaxTurns)
	}
	if got := DefaultSettings().MaxTurns; got != DefaultMaxTurns {
		t.Fatalf("DefaultSettings MaxTurns = %d, want %d", got, DefaultMaxTurns)
	}
	if DefaultMaxTurns != 0 {
		t.Fatalf("DefaultMaxTurns = %d, want unlimited (0)", DefaultMaxTurns)
	}
}
