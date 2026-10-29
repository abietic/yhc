package commands

import (
	"context"
	"fmt"
)

const (
	ActionWorkspaceChanges CommandAction = "workspace_changes"
	ActionExecutionRecap   CommandAction = "execution_recap"
)

func registerWorkspaceHistoryCommands(r *Registry) {
	for _, spec := range []struct {
		name, description string
		category          CommandCategory
		order             int
		action            CommandAction
	}{
		{"changes", "Review captured file changes and the turn timeline", CommandCategoryWorkspace, 4050, ActionWorkspaceChanges},
		{"recap", "Inspect execution facts, current tasks, pending input and evidence", CommandCategorySession, 1050, ActionExecutionRecap},
	} {
		r.mustRegister(&Command{
			Name: spec.name, Description: spec.description, Usage: "/" + spec.name, Category: spec.category, DisplayOrder: spec.order, DiscoveryTier: DiscoveryTierSecondary, PhaseScope: PhaseScopeAny, Kind: CommandKindQuery, Entrypoints: EntrypointsTUI, ExecutionOwner: ExecutionOwnerEntrypoint, ResultKind: ResultKindUI, SideEffect: SideEffectNone, ModelInvocation: ModelInvocationNone,
			ResolveAvailability: func(_ context.Context, ctx *CommandContext) (AvailabilityState, string) {
				if ctx == nil {
					return AvailabilityUnavailable, "History requires an interactive engine."
				}
				if leader, ok := ctx.Extra["leader_view"].(bool); ok && !leader {
					return AvailabilityUnavailable, "History views use the leader session."
				}
				owner, ok := ctx.Engine.(interface{ WorkspaceHistoryAvailability() (bool, string) })
				if !ok {
					return AvailabilityUnavailable, "History requires an interactive engine."
				}
				if available, reason := owner.WorkspaceHistoryAvailability(); !available {
					return AvailabilityUnavailable, reason
				}
				return AvailabilitySupported, ""
			},
			Execute: func(_ context.Context, ctx *CommandContext) (*CommandResult, error) {
				if len(ctx.Args) > 0 {
					return nil, fmt.Errorf("usage: /%s", spec.name)
				}
				return &CommandResult{Action: spec.action}, nil
			},
		})
	}
}
