package engine

import (
	"context"

	"github.com/abietic/yhc/engine/commands"
)

// CompleteCommandInput is a read-only projection of this engine's command
// entrypoint. It neither creates a session nor submits a turn.
func (e *QueryEngine) CompleteCommandInput(ctx context.Context, request commands.CompletionRequest) commands.InputCompletion {
	empty := commands.InputCompletion{Candidates: []commands.InputCandidate{}}
	if e == nil || e.commandArgumentSuggestionBlocked() {
		return empty
	}
	result := e.GetCommandRegistry().CompleteInputFor(ctx, e.config.CommandEntrypoint, request, e.CommandContext())
	if e.commandArgumentSuggestionBlocked() {
		return empty
	}
	return result
}
