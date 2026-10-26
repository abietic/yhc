package commands

import (
	"context"
	"strings"
)

const ActionSideQuestion CommandAction = "side_question"

func registerSideQuestionCommand(r *Registry) {
	r.mustRegister(&Command{
		Name: "btw", Description: "Ask an independent question about the current conversation", Usage: "/btw [question]",
		Category: CommandCategorySession, DiscoveryTier: DiscoveryTierSecondary, DisplayOrder: 1040, PhaseScope: PhaseScopeAny,
		Kind: CommandKindQuery, Entrypoints: EntrypointsTUI, SideEffect: SideEffectNone, ResultKind: ResultKindUI, ExecutionOwner: ExecutionOwnerEntrypoint, ModelInvocation: ModelInvocationPossible,
		Args: []ArgDef{{Name: "question", Type: "string", Description: "Question for one tool-free model call; empty opens the editor"}},
		ResolveAvailability: func(_ context.Context, cmdCtx *CommandContext) (AvailabilityState, string) {
			if cmdCtx == nil {
				return AvailabilityUnavailable, "Side questions require an interactive engine."
			}
			if leader, ok := cmdCtx.Extra["leader_view"].(bool); ok && !leader {
				return AvailabilityUnavailable, "Side questions use the leader conversation."
			}
			provider, ok := cmdCtx.Engine.(interface{ SideQuestionAvailability() (bool, string) })
			if !ok {
				return AvailabilityUnavailable, "Side questions require an interactive engine."
			}
			if available, reason := provider.SideQuestionAvailability(); !available {
				return AvailabilityUnavailable, reason
			}
			return AvailabilitySupported, ""
		},
		Execute: func(_ context.Context, cmdCtx *CommandContext) (*CommandResult, error) {
			return &CommandResult{Action: ActionSideQuestion, Data: map[string]any{"question": strings.TrimSpace(strings.Join(cmdCtx.Args, " "))}}, nil
		},
	})
}
