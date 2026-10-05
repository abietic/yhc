package engine

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/services"
)

const maxCommandArgumentSuggestionInputRunes = 2048

type commandArgumentSuggestionSnapshot struct {
	command string
	prefix  string
	context string
}

// GenerateCommandArgumentSuggestion returns one model-assisted extension for a
// slash-command argument. Callers must invoke it only for an explicit user
// request; it never executes or submits the command.
func (e *QueryEngine) GenerateCommandArgumentSuggestion(
	ctx context.Context,
	request commands.CompletionRequest,
) string {
	if e == nil || ctx == nil || ctx.Err() != nil ||
		utf8.RuneCountInString(request.Input) > maxCommandArgumentSuggestionInputRunes ||
		request.Cursor != utf8.RuneCountInString(request.Input) {
		return ""
	}

	snapshot, ok := e.commandArgumentSuggestionSnapshot(ctx, request)
	if !ok {
		return ""
	}
	requestContext, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	output, err := e.generateAuxiliarySuggestionProvider(
		requestContext,
		[]string{snapshot.context},
		services.GetCommandArgumentSuggestionPrompt(),
		"command_argument_suggestion",
	)
	if err != nil || requestContext.Err() != nil {
		return ""
	}
	after, ok := e.commandArgumentSuggestionSnapshot(ctx, request)
	if !ok || after != snapshot {
		return ""
	}
	return services.ParseCommandArgumentSuggestion(output, snapshot.prefix)
}

func (e *QueryEngine) commandArgumentSuggestionSnapshot(
	ctx context.Context,
	request commands.CompletionRequest,
) (commandArgumentSuggestionSnapshot, bool) {
	if e == nil || ctx == nil || ctx.Err() != nil ||
		utf8.RuneCountInString(request.Input) > maxCommandArgumentSuggestionInputRunes ||
		request.Cursor != utf8.RuneCountInString(request.Input) ||
		e.PermissionMode() == permission.ModePlan ||
		e.commandArgumentSuggestionBlocked() {
		return commandArgumentSuggestionSnapshot{}, false
	}
	commandContext := e.CommandContext()
	registry := e.GetCommandRegistry()
	if registry == nil {
		return commandArgumentSuggestionSnapshot{}, false
	}
	result, _, ok := registry.PrepareArgumentCompletion(ctx, request, commandContext)
	if !ok || !result.AllowSuggestion {
		return commandArgumentSuggestionSnapshot{}, false
	}
	command := registry.GetForContext(ctx, commands.EntrypointTUI, commandContext, result.Command)
	if command == nil {
		return commandArgumentSuggestionSnapshot{}, false
	}
	return commandArgumentSuggestionSnapshot{
		command: result.Command,
		prefix:  result.Prefix,
		context: services.BuildCommandArgumentSuggestionContext(
			result.Command,
			command.FormatHelpFor(commands.EntrypointTUI),
			command.Usage,
			result.Prefix,
			result.ArgumentIndex,
		),
	}, true
}

func (e *QueryEngine) commandArgumentSuggestionBlocked() bool {
	if e == nil {
		return true
	}
	if e.permissionCoordinator != nil && e.permissionCoordinator.PendingCount() > 0 {
		return true
	}
	e.planMu.Lock()
	activeTurn := strings.TrimSpace(e.planActiveTurnID) != ""
	pendingApproval := e.planState.Phase == PlanPhaseAwaitingApproval
	e.planMu.Unlock()
	return activeTurn || pendingApproval
}
