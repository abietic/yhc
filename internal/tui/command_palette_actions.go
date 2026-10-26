package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

type commandPaletteTarget struct {
	engine       *engine.QueryEngine
	registry     *commands.Registry
	thread, view string
	revision     uint64
}

func (a *App) commandPaletteTargetMatches() bool {
	target := a.commandPaletteTarget
	return target.engine == a.engine && target.registry == a.commandRegistry && target.view == a.activeThreadViewID() && target.revision == a.composerRevision && (a.engine == nil || target.thread == a.engine.ThreadID())
}

func (a *App) handleCommandPaletteKey(msg tea.KeyPressMsg) tea.Cmd {
	p := a.commandPalette
	if !a.commandPaletteTargetMatches() {
		p.Close()
		a.popDialog(StateCommandPalette)
		a.showNotification("Command target or draft changed; reopen the palette", NotifyWarning)
		return nil
	}
	p.commandContext = a.commandCapabilityContext()
	// Refresh metadata from the current in-memory catalog, preserving list focus.
	previous := ""
	if p.cursor >= 0 && p.cursor < len(p.filtered) {
		previous = p.filtered[p.cursor].command.Name
	}
	p.all = nil
	p.operations = make(map[string]commands.OperationDescription)
	for _, operation := range a.commandRegistry.DescribeOperations(context.Background(), commands.EntrypointTUI, p.commandContext) {
		p.operations[operation.Command.Name] = operation
		p.all = append(p.all, operation.Command)
	}
	p.applyFilter()
	for i, item := range p.filtered {
		if item.command.Name == previous {
			p.cursor = i
			break
		}
	}
	if p.form != nil {
		p.refreshForm()
	}
	selected, dismissed := p.HandleKey(msg)
	if !dismissed {
		return nil
	}
	a.popDialog(StateCommandPalette)
	if selected == "" {
		return nil
	}
	input := selected
	if !strings.HasPrefix(input, "/") {
		input = "/" + input
	}
	name, args := commands.ParseCommandInput(input)
	cmdCtx := a.commandCapabilityContext()
	operation, ok := a.commandRegistry.DescribeOperation(context.Background(), commands.EntrypointTUI, cmdCtx, name, args)
	if !ok || operation.Token != p.selectionToken || operation.Command.Availability != commands.AvailabilitySupported || operation.Command.ValidateArgs(args) != nil {
		a.showNotification("Command changed or is unavailable; reopen the palette", NotifyWarning)
		return nil
	}
	if strings.HasPrefix(operation.Command.Source, "skill:") {
		before := a.captureComposerUndoEntry()
		a.textarea.SetValue(input + " ")
		a.reconcileComposerElements(before.Text, a.textarea.Value())
		a.markComposerChanged()
		a.recordComposerUndo(before)
		a.textarea.CursorEnd()
		a.syncInputModeFromText()
		return nil
	}
	a.beginCommandPaletteSubmission(operation.Command.Name)
	if operation.Command.Name == "changes" || operation.Command.Name == "recap" {
		return a.dispatchHistoryView(input, cmdCtx, a.claimCommandPaletteSubmission(operation.Command.Name), true)
	}
	if operation.Command.Name == "btw" {
		return a.dispatchSideQuestion(input, cmdCtx, a.claimCommandPaletteSubmission("btw"), true)
	}
	a.textarea.SetValue(input)
	a.composerElements = nil
	a.inputMode = InputCommand
	return a.sendMessage()
}
