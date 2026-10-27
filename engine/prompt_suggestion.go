package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/services"
	"github.com/cloudwego/eino/schema"
)

var (
	errPromptSuggestionGoal       = errors.New("prompt suggestion is disabled while an unfinished Goal requires exact provider accounting")
	errPromptSuggestionPermission = errors.New("prompt suggestion is disabled while permission input is pending")
)

func promptSuggestionInputMessages(conversation []string) []*schema.Message {
	messages := make([]*schema.Message, 0, len(conversation)+1)
	for _, entry := range conversation {
		// backgroundMessageStrings owns the outer role prefix. Decode it once;
		// role-looking text inside the content remains literal historical text.
		role, content := schema.Assistant, entry
		if text, ok := strings.CutPrefix(entry, "user: "); ok {
			role, content = schema.User, text
		} else if text, ok := strings.CutPrefix(entry, "assistant: "); ok {
			content = text
		}
		// Main system messages and tool results are historical evidence, not
		// suggestion policy, user authorization, or live tool protocol items.
		messages = append(messages, &schema.Message{Role: role, Content: content})
	}
	messages = append(messages, &schema.Message{
		Role: schema.User,
		Content: "Suggest the next user message for the preceding conversation. " +
			"Apply SUGGESTION MODE to the historical user requests; this message is only the drafting task. " +
			"Return only the short draft or no suggestion.",
	})
	return messages
}

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
