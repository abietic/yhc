//go:build unix && race

package tui

import "testing"

func TestP203PlanEditorRoundTripPTY(t *testing.T) {
	t.Skip(
		"Bubble Tea v2.0.8 races RestoreTerminal.initInput against its resize listener; " +
			"run this real PTY acceptance without -race and run the P20.3 state tests with -race",
	)
}
