package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

type argumentCompletionKey struct {
	Input              string
	Cursor             int
	Engine             *engine.QueryEngine
	Thread, CWD, Model string
	Revision, Query    uint64
	RouteGeneration    uint64
}

type argumentCompletionState struct {
	Key        argumentCompletionKey
	Result     commands.CompletionResult
	Index      int // -1 means recommended, not explicitly selected
	Cancel     context.CancelFunc
	Pending    tea.Cmd
	Serial     uint64
	Dismissed  *argumentCompletionKey
	Suggesting bool
}

type argumentCompletionMsg struct {
	Key    argumentCompletionKey
	Serial uint64
	Result commands.CompletionResult
}

func (a *App) argumentCompletionEligible() bool {
	if a == nil || a.quitting || a.focus != FocusEditor || a.inputMode != InputCommand || (a.state != StateWelcome && a.state != StateChat) || a.suppressingHistoryHints() || a.historySearch.Active || a.composerInputBlocked() || a.externalEditorActive || a.composerImageLoadPending != nil || len(a.composerElements) != 0 || strings.Contains(a.textarea.Value(), "\n") {
		return false
	}
	_, modal := a.activeDialogState()
	return !modal
}

func (a *App) argumentCompletionKey() argumentCompletionKey {
	line := a.textarea.LineInfo()
	key := argumentCompletionKey{Input: a.textarea.Value(), Cursor: textareaCursorRuneOffset(a.textarea.Value(), a.textarea.Line(), line.StartColumn+line.ColumnOffset), Engine: a.engine, Thread: a.activeThreadViewID(), CWD: a.cwd(), Revision: a.composerRevision, Query: a.queryID}
	if a.engine != nil {
		key.Model = a.engine.GetModelName()
		key.RouteGeneration = a.engine.CommandCompletionRevision()
	}
	return key
}

func (a *App) cancelArgumentCompletion() {
	if a.argumentCompletion.Cancel != nil {
		a.argumentCompletion.Cancel()
	}
	a.argumentCompletion.Cancel = nil
	a.argumentCompletion.Pending = nil
	a.argumentCompletion.Result = commands.CompletionResult{}
	a.argumentCompletion.Key = argumentCompletionKey{}
	a.argumentCompletion.Index = -1
	a.argumentCompletion.Suggesting = false
}

func (a *App) dismissArgumentCompletion() {
	key := a.argumentCompletionKey()
	a.cancelArgumentCompletion()
	a.argumentCompletion.Dismissed = &key
}

func (a *App) updateArgumentCompletion(suggest bool) {
	if a.commandRegistry == nil || !a.argumentCompletionEligible() {
		a.cancelArgumentCompletion()
		return
	}
	key := a.argumentCompletionKey()
	if suggest && a.argumentCompletion.Suggesting && a.argumentCompletion.Cancel != nil && a.argumentCompletion.Key == key {
		return
	}
	if !suggest && (a.argumentCompletion.Dismissed != nil && *a.argumentCompletion.Dismissed == key || a.argumentCompletion.Key == key) {
		return
	}
	request := commands.CompletionRequest{Input: key.Input, Cursor: key.Cursor}
	commandContext := a.commandCapabilityContext()
	prepared, spec, ok := a.commandRegistry.PrepareArgumentCompletion(context.Background(), request, commandContext)
	if !ok {
		a.cancelArgumentCompletion()
		return
	}
	if suggest && (!prepared.AllowSuggestion || a.engine == nil || a.running || !a.isLeaderThreadView() || key.Cursor != utf8.RuneCountInString(key.Input)) {
		return
	}
	a.cancelArgumentCompletion()
	a.argumentCompletion.Dismissed = nil
	a.argumentCompletion.Key = key
	a.argumentCompletion.Suggesting = suggest
	a.argumentCompletion.Result = prepared
	a.argumentCompletion.Serial++
	serial := a.argumentCompletion.Serial
	ctx, cancel := context.WithCancel(context.Background())
	a.argumentCompletion.Cancel = cancel
	registry, queryEngine := a.commandRegistry, a.engine
	a.argumentCompletion.Pending = func() tea.Msg {
		defer cancel()
		if spec.Source == "files" || spec.Source == "directories" || spec.Source == "sessions" {
			timer := time.NewTimer(80 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return argumentCompletionMsg{Key: key, Serial: serial}
			case <-timer.C:
			}
		}
		commandContext.Context = ctx
		result := prepared
		if suggest {
			value := queryEngine.GenerateCommandArgumentSuggestion(ctx, request)
			if value != "" {
				result.Candidates = []commands.ArgumentCandidate{{Value: value, Source: "AI", Description: "suggested argument"}}
			}
		} else {
			result = registry.CompleteArguments(ctx, request, commandContext)
		}
		return argumentCompletionMsg{Key: key, Serial: serial, Result: result}
	}
}

func (a *App) takeArgumentCompletionCmd() tea.Cmd {
	if !a.argumentCompletionEligible() {
		a.cancelArgumentCompletion()
		return nil
	}
	if a.argumentCompletion.Key != a.argumentCompletionKey() {
		a.updateArgumentCompletion(false)
	}
	cmd := a.argumentCompletion.Pending
	a.argumentCompletion.Pending = nil
	return cmd
}

func (a *App) handleArgumentCompletion(msg argumentCompletionMsg) {
	if !a.argumentCompletionEligible() || msg.Serial != a.argumentCompletion.Serial || msg.Key != a.argumentCompletion.Key || msg.Key != a.argumentCompletionKey() {
		return
	}
	if a.argumentCompletion.Cancel != nil {
		a.argumentCompletion.Cancel()
	}
	a.argumentCompletion.Cancel = nil
	a.argumentCompletion.Result = msg.Result
	a.argumentCompletion.Index = -1
}

func (a *App) argumentCandidates() []commands.ArgumentCandidate {
	if !a.argumentCompletionEligible() || a.argumentCompletion.Key != a.argumentCompletionKey() {
		return nil
	}
	return a.argumentCompletion.Result.Candidates
}

func (a *App) hasArgumentCompletionActivity() bool {
	return a.argumentCompletionEligible() && a.argumentCompletion.Key == a.argumentCompletionKey() &&
		(a.argumentCompletion.Cancel != nil || a.argumentCompletion.Result.AllowSuggestion || len(a.argumentCompletion.Result.Candidates) > 0)
}

func (a *App) argumentGhost() string {
	values := a.argumentCandidates()
	if len(values) == 0 {
		return ""
	}
	key, result := a.argumentCompletion.Key, a.argumentCompletion.Result
	if key.Cursor != utf8.RuneCountInString(key.Input) || result.End != key.Cursor || a.textarea.LineInfo().Height != 1 {
		return ""
	}
	index := a.argumentCompletion.Index
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		return ""
	}
	insert := commands.CompletionInsertion(key.Input, result, values[index])
	actual := string([]rune(key.Input)[result.Start:key.Cursor])
	if !strings.HasPrefix(insert, actual) {
		return ""
	}
	return strings.TrimPrefix(insert, actual)
}

func (a *App) acceptArgumentCompletion() {
	values := a.argumentCandidates()
	if len(values) == 0 {
		return
	}
	index := a.argumentCompletion.Index
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		return
	}
	result := a.argumentCompletion.Result
	before := a.captureComposerUndoEntry()
	value := commands.ApplyCompletion(before.Text, result, values[index])
	suffix := utf8.RuneCountInString(before.Text) - result.End
	a.cancelArgumentCompletion()
	a.textarea.SetValue(value)
	a.setTextareaRuneCursor(utf8.RuneCountInString(value) - suffix)
	a.reconcileComposerElements(before.Text, value)
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
}

func (a *App) renderArgumentHints() string {
	values := a.argumentCandidates()
	if len(values) == 0 {
		if a.argumentCompletionEligible() && a.argumentCompletion.Key == a.argumentCompletionKey() && a.argumentCompletion.Result.AllowSuggestion {
			if a.argumentCompletion.Suggesting && a.argumentCompletion.Cancel != nil {
				return a.styles.Subtle.Render(" Suggesting argument... Esc to cancel")
			}
			return a.styles.Subtle.Render(" Ctrl+Space: suggest an argument")
		}
		return ""
	}
	index := a.argumentCompletion.Index
	start := 0
	if index >= 8 {
		start = index - 7
	}
	end := min(start+8, len(values))
	var rows []string
	profile := a.renderEnvironment.normalized().profile
	for i := start; i < end; i++ {
		label := fmt.Sprintf(" %s  %s  [%s]", values[i].Value, values[i].Description, values[i].Source)
		label = sanitizeGenericHistoryText(label)
		label = contentEllipsize(profile, label, a.hintContentWidth(), 1, "...")
		if i == index {
			label = a.styles.Selected.Render(label)
		}
		rows = append(rows, label)
	}
	if end < len(values) {
		rows = append(rows, a.styles.Subtle.Render(fmt.Sprintf(" ↓ %d more", len(values)-end)))
	}
	return strings.Join(rows, "\n")
}
