package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/recovery"
	"github.com/abietic/yhc/engine/transcript"
)

type mediaRecoveryContext struct {
	current              recovery.CurrentTurn
	selectedModel        string
	commitBoundary       func(context.Context, []*schema.Message) error
	prepareFallbackRoute func(string) error
	fallbackRouteGuard   func(string) error
}

func (e *QueryEngine) bindMediaRecoveryContext(
	turnID string,
	message *schema.Message,
	input *AdmittedPromptInput,
) *mediaRecoveryContext {
	if e == nil ||
		input == nil ||
		input.binding == nil ||
		!input.hasImages() {
		return nil
	}
	current, err := recovery.BindCurrentTurn(turnID, message)
	if err != nil {
		return nil
	}
	var candidate *promptRouteBinding
	bound := &mediaRecoveryContext{
		current:       current,
		selectedModel: input.binding.selectedModelSpec,
	}
	bound.prepareFallbackRoute = func(currentModel string) error {
		next, bindErr := e.bindAdmittedPromptRecoveryRoute(
			input,
			currentModel,
		)
		if bindErr != nil {
			return bindErr
		}
		if next.provider == input.binding.provider &&
			next.model == input.binding.model {
			return newPromptAdmissionError(
				input.binding.firstImagePart,
				string(input.binding.firstImageKind),
				"route_not_distinct",
				"",
				"",
			)
		}
		candidate = next
		return nil
	}
	bound.fallbackRouteGuard = func(currentModel string) error {
		if candidate == nil {
			return newPromptAdmissionError(
				input.binding.firstImagePart,
				string(input.binding.firstImageKind),
				"route_unknown",
				"",
				"",
			)
		}
		return e.checkAdmittedPromptBinding(
			input,
			candidate,
			currentModel,
		)
	}
	return bound
}

type mediaRecoveryOutcome struct {
	continueRound bool
	terminal      *Terminal
}

func handleMediaSizeFailure(
	input canonicalAfterModelInput,
	modelRound canonicalModelRoundResult,
	messagesForQuery []*schema.Message,
) mediaRecoveryOutcome {
	state := input.state
	if state == nil {
		return mediaRecoveryOutcome{
			terminal: &Terminal{
				Reason: TerminalModelError,
				Err:    errors.New("media recovery requires query state"),
			},
		}
	}
	ctx := input.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	binding := input.params.mediaRecovery
	attempt := modelRound.mediaRecoveryAttempt
	if !state.MediaRecovery.ProjectionAttempted &&
		attempt == mediaRecoveryAttemptNone {
		if binding == nil ||
			input.params.promptRouteGuard == nil ||
			strings.TrimSpace(binding.selectedModel) == "" ||
			modelRound.attemptedModel != binding.selectedModel {
			return mediaRecoveryFailure(
				input,
				recovery.MediaStageInitial,
				"identity_unavailable",
			)
		}
		if err := mediaRecoveryContextError(ctx); err != nil {
			return canceledMediaRecovery(input, err)
		}
		if err := input.params.promptRouteGuard(
			binding.selectedModel,
		); err != nil {
			return mediaRecoveryFailure(
				input,
				recovery.MediaStageInitial,
				"route_stale",
			)
		}
		candidate, err := recovery.BuildMediaCandidate(
			ctx,
			messagesForQuery,
			binding.current,
			boundMediaRecoveryRecords(input.deps, messagesForQuery),
		)
		if err != nil {
			if errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				return canceledMediaRecovery(input, err)
			}
			return mediaRecoveryFailure(
				input,
				recovery.MediaStageInitial,
				"projection_ineligible",
			)
		}
		transferred := false
		defer func() {
			if !transferred {
				recovery.ClearProviderMessages(candidate.ProviderMessages)
			}
		}()

		boundary := recovery.BoundaryMessage(
			candidate.OmittedImageCount,
			candidate.OmittedTurnCount,
		)
		activeMessages := make(
			[]*schema.Message,
			0,
			len(candidate.CanonicalMessages)+1,
		)
		activeMessages = append(activeMessages, boundary)
		activeMessages = append(
			activeMessages,
			candidate.CanonicalMessages...,
		)
		if err := mediaRecoveryContextError(ctx); err != nil {
			return canceledMediaRecovery(input, err)
		}
		if err := commitMediaRecoveryBoundary(
			ctx,
			input,
			activeMessages,
		); err != nil {
			return mediaRecoveryOutcome{
				terminal: &Terminal{
					Reason: TerminalPersistenceError,
					Err: errors.New(
						"media recovery boundary persistence failed",
					),
				},
			}
		}
		// The durable boundary is the authority. Once it commits, mirror it
		// in QueryState even if cancellation arrives before presentation.
		state.Messages = activeMessages
		candidate.CanonicalMessages = activeMessages
		candidate.ProviderMessages = append(
			[]*schema.Message{recovery.BoundaryMessage(
				candidate.OmittedImageCount,
				candidate.OmittedTurnCount,
			)},
			candidate.ProviderMessages...,
		)
		state.MediaRecovery.ProjectionAttempted = true
		state.MediaRecovery.PendingAttempt = mediaRecoveryAttemptSelected
		state.MediaRecovery.CanonicalMessages = candidate.CanonicalMessages
		state.MediaRecovery.ProviderMessages = candidate.ProviderMessages
		state.MediaRecovery.RouteModel = binding.selectedModel
		state.MediaRecovery.OmittedImageCount = candidate.OmittedImageCount
		state.MediaRecovery.DerivativeImageCount = candidate.DerivativeImageCount
		state.MediaRecovery.UsageLogicalRoundID = modelRound.usageLogicalRoundID
		state.Transition = ContinueMediaRecovery
		transferred = true

		if err := mediaRecoveryContextError(ctx); err != nil {
			releasePendingMediaRecovery(state)
			return canceledMediaRecovery(input, err)
		}
		input.yield(QueryEvent{
			Type:                     EventCompactBoundary,
			CompactBoundaryMessage:   boundary,
			compactBoundaryMessages:  state.Messages,
			compactBoundaryCommitted: true,
		})
		if err := mediaRecoveryContextError(ctx); err != nil {
			releasePendingMediaRecovery(state)
			return canceledMediaRecovery(input, err)
		}
		input.yield(QueryEvent{
			Type: EventAttachment,
			AttachmentMessage: recovery.AttachmentMessage(
				recovery.MediaStageSelected,
				candidate.OmittedImageCount,
				candidate.DerivativeImageCount,
				false,
			),
		})
		return mediaRecoveryOutcome{continueRound: true}
	}

	if attempt == mediaRecoveryAttemptSelected &&
		state.MediaRecovery.ProjectionAttempted &&
		!state.MediaRecovery.FallbackAttempted {
		fallback := strings.TrimSpace(input.params.FallbackModel)
		if binding == nil ||
			fallback == "" ||
			fallback == binding.selectedModel ||
			binding.prepareFallbackRoute == nil ||
			binding.fallbackRouteGuard == nil {
			return mediaRecoveryFailure(
				input,
				recovery.MediaStageFallbackEligibility,
				"fallback_ineligible",
			)
		}
		if err := mediaRecoveryContextError(ctx); err != nil {
			return canceledMediaRecovery(input, err)
		}
		if err := binding.prepareFallbackRoute(fallback); err != nil {
			return mediaRecoveryFailure(
				input,
				recovery.MediaStageFallbackEligibility,
				"fallback_ineligible",
			)
		}
		providerMessages, derivativeCount, err := recovery.PrepareProviderMessages(
			ctx,
			messagesForQuery,
			binding.current,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				return canceledMediaRecovery(input, err)
			}
			return mediaRecoveryFailure(
				input,
				recovery.MediaStageFallback,
				"preparation_failed",
			)
		}
		if err := mediaRecoveryContextError(ctx); err != nil {
			recovery.ClearProviderMessages(providerMessages)
			return canceledMediaRecovery(input, err)
		}
		state.MediaRecovery.FallbackAttempted = true
		state.MediaRecovery.PendingAttempt = mediaRecoveryAttemptFallback
		state.MediaRecovery.CanonicalMessages = messagesForQuery
		state.MediaRecovery.ProviderMessages = providerMessages
		state.MediaRecovery.RouteModel = fallback
		state.MediaRecovery.DerivativeImageCount = derivativeCount
		state.Transition = ContinueMediaRecovery
		input.yield(QueryEvent{
			Type: EventAttachment,
			AttachmentMessage: recovery.AttachmentMessage(
				recovery.MediaStageFallback,
				state.MediaRecovery.OmittedImageCount,
				derivativeCount,
				true,
			),
		})
		return mediaRecoveryOutcome{continueRound: true}
	}

	stage := recovery.MediaStageInitial
	switch attempt {
	case mediaRecoveryAttemptSelected:
		stage = recovery.MediaStageSelected
	case mediaRecoveryAttemptFallback:
		stage = recovery.MediaStageFallback
	}
	return mediaRecoveryFailure(input, stage, "exhausted")
}

func handleActiveMediaRecoveryFailure(
	input canonicalAfterModelInput,
	attempt mediaRecoveryAttempt,
) mediaRecoveryOutcome {
	stage := recovery.MediaStageSelected
	if attempt == mediaRecoveryAttemptFallback {
		stage = recovery.MediaStageFallback
	}
	return mediaRecoveryFailure(input, stage, "provider_rejected")
}

func mediaRecoveryFailure(
	input canonicalAfterModelInput,
	stage string,
	category string,
) mediaRecoveryOutcome {
	releasePendingMediaRecovery(input.state)
	message := recovery.TerminalMessage(stage)
	input.yield(QueryEvent{
		Type:             EventAssistant,
		AssistantMessage: message,
	})
	if input.hookExecutor != nil {
		input.hookExecutor.ExecuteStopFailure(message)
	}
	return mediaRecoveryOutcome{
		terminal: &Terminal{
			Reason: TerminalImageError,
			Err:    recovery.NewMediaError(stage, category),
		},
	}
}

func canceledMediaRecovery(
	input canonicalAfterModelInput,
	err error,
) mediaRecoveryOutcome {
	releasePendingMediaRecovery(input.state)
	input.yield(QueryEvent{
		Type:                EventUserInterruption,
		InterruptionToolUse: false,
	})
	return mediaRecoveryOutcome{
		terminal: &Terminal{
			Reason: TerminalAbortedStreaming,
			Err:    err,
		},
	}
}

func releasePendingMediaRecovery(state *QueryState) {
	if state == nil {
		return
	}
	recovery.ClearProviderMessages(state.MediaRecovery.ProviderMessages)
	state.MediaRecovery.ProviderMessages = nil
	state.MediaRecovery.PendingAttempt = mediaRecoveryAttemptNone
	state.MediaRecovery.RouteModel = ""
}

func boundMediaRecoveryRecords(
	deps *QueryDeps,
	messages []*schema.Message,
) []recovery.BoundPromptRecord {
	if deps == nil || deps.Transcript == nil {
		return nil
	}
	bindings := deps.Transcript.PromptRecordBindings(messages)
	result := make([]recovery.BoundPromptRecord, 0, len(bindings))
	for _, binding := range bindings {
		if binding.MessageIndex < 0 ||
			binding.MessageIndex >= len(messages) ||
			messages[binding.MessageIndex] == nil {
			continue
		}
		result = append(result, recovery.BoundPromptRecord{
			Message: messages[binding.MessageIndex],
			Record:  binding.Record,
		})
	}
	return result
}

func commitMediaRecoveryBoundary(
	ctx context.Context,
	input canonicalAfterModelInput,
	messages []*schema.Message,
) error {
	if err := mediaRecoveryContextError(ctx); err != nil {
		return err
	}
	if input.params.mediaRecovery != nil &&
		input.params.mediaRecovery.commitBoundary != nil {
		return input.params.mediaRecovery.commitBoundary(ctx, messages)
	}
	if input.deps != nil && input.deps.Transcript != nil {
		return input.deps.Transcript.RecordLifecycleBoundary(
			transcript.LifecycleCompact,
			messages,
			nil,
			nil,
		)
	}
	return nil
}

func mediaRecoveryContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
