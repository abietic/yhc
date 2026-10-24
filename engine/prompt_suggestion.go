package engine

import (
	"context"
	"errors"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/services"
	"github.com/cloudwego/eino/schema"
)

var (
	errPromptSuggestionGoal       = errors.New("prompt suggestion is disabled while an unfinished Goal requires exact provider accounting")
	errPromptSuggestionPermission = errors.New("prompt suggestion is disabled while permission input is pending")
)

// GeneratePromptSuggestionResult is the shared root-interactive contract. It
// preserves route admission, tool-free dispatch, auxiliary accounting and
// cancellation, while exposing only stable reason codes to client projections.
func (e *QueryEngine) GeneratePromptSuggestionResult(ctx context.Context) (services.PromptSuggestionResult, error) {
	suppressed := func(reason string) services.PromptSuggestionResult {
		return services.PromptSuggestionResult{Status: services.PromptSuggestionSuppressed, Reason: reason}
	}
	if e == nil || ctx == nil {
		return suppressed("unavailable"), nil
	}
	if err := ctx.Err(); err != nil {
		return services.PromptSuggestionErrorResult(err), err
	}
	e.mu.Lock()
	service := e.promptSuggestionService
	enabled := e.config.EnablePromptSuggestions
	mode := e.config.PermissionMode
	routeGeneration := e.promptRouteGeneration
	conversation := backgroundMessageStrings(e.messages)
	assistantTurnCount := 0
	for _, message := range e.messages {
		if message != nil && message.Role == schema.Assistant {
			assistantTurnCount++
		}
	}
	e.mu.Unlock()
	if !enabled {
		return suppressed("disabled"), nil
	}
	if service == nil {
		return suppressed("unavailable"), nil
	}
	hasPendingPermission := e.permissionCoordinator != nil && e.permissionCoordinator.PendingCount() > 0
	if reason := services.SuggestionSuppressReason(service.IsEnabled(), mode == permission.ModePlan, hasPendingPermission); reason != "" {
		return suppressed(reason), nil
	}
	if e.goalProviderUsageRequired() {
		return suppressed("goal_active"), nil
	}
	result, err := service.GenerateSuggestionResult(ctx, conversation, assistantTurnCount)
	switch {
	case errors.Is(err, errPromptSuggestionGoal):
		return suppressed("goal_active"), nil
	case errors.Is(err, errPromptSuggestionPermission):
		return suppressed("pending_permission"), nil
	}
	e.mu.Lock()
	routeUnchanged := e.promptRouteGeneration == routeGeneration
	e.mu.Unlock()
	if !routeUnchanged {
		return services.PromptSuggestionResult{Status: services.PromptSuggestionStale}, nil
	}
	return result, err
}
