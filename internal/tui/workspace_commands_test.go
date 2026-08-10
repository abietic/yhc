package tui

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestCopyDiscoveryUsesTUITerminalCapabilitySnapshot(t *testing.T) {
	nonInteractive := terminalcap.Capabilities{}
	app := New(Config{Resumed: true, TerminalCaps: &nonInteractive})
	if app.commandRegistry.GetForContext(
		context.Background(),
		commands.EntrypointTUI,
		app.commandCapabilityContext(),
		"copy",
	) != nil {
		t.Fatal("/copy discovered without an interactive terminal capability")
	}

	interactive := terminalcap.Capabilities{Interactive: true}
	app = New(Config{Resumed: true, TerminalCaps: &interactive})
	if app.commandRegistry.GetForContext(
		context.Background(),
		commands.EntrypointTUI,
		app.commandCapabilityContext(),
		"copy",
	) == nil {
		t.Fatal("/copy absent with an interactive terminal capability")
	}
}
