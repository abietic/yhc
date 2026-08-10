package hooks

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Session lifecycle hooks
// ---------------------------------------------------------------------------

// SessionStartHookResult is the aggregated result from session-start hooks.
type SessionStartHookResult struct {
	Attachments []*schema.Message
	SkipDefault bool
}

// SessionStartHook fires when a session begins or resumes.
type SessionStartHook func(ctx context.Context, sessionID string, isResume bool) *SessionStartHookResult

// SessionEndHookResult is the aggregated result from session-end hooks.
// Empty for now; exists for future extension.
type SessionEndHookResult struct{}

// SessionEndHook fires when a session ends.
type SessionEndHook func(ctx context.Context, sessionID, reason string)

// ---------------------------------------------------------------------------
// Compaction hooks
// ---------------------------------------------------------------------------

// PreCompactHookResult is the aggregated result from pre-compaction hooks.
type PreCompactHookResult struct {
	MemoryEntries []string // additional memory to preserve
	Cancel        bool     // if true, cancel compaction
}

// PreCompactHook fires before compaction runs.
type PreCompactHook func(ctx context.Context, messageCount, tokenCount int) *PreCompactHookResult

// PostCompactHookResult is the aggregated result from post-compaction hooks.
type PostCompactHookResult struct {
	Attachments []*schema.Message
}

// PostCompactHook fires after compaction completes.
type PostCompactHook func(ctx context.Context, tokensBefore, tokensAfter int) *PostCompactHookResult

// ---------------------------------------------------------------------------
// Notification hook
// ---------------------------------------------------------------------------

// NotificationHook fires for runtime notifications (info, warning, error, etc.).
type NotificationHook func(ctx context.Context, level, message string, data map[string]any)

// ---------------------------------------------------------------------------
// Command hook
// ---------------------------------------------------------------------------

// CommandHookResult is the aggregated result from command hooks.
type CommandHookResult struct {
	Output  string
	Handled bool // if true, don't run the default command handler
}

// CommandHook fires when a slash command is issued.
type CommandHook func(ctx context.Context, command, args string) *CommandHookResult

// ---------------------------------------------------------------------------
// Turn lifecycle hooks
// ---------------------------------------------------------------------------

// TurnStartHookResult is the aggregated result from turn-start hooks.
type TurnStartHookResult struct {
	Attachments []*schema.Message
}

// TurnStartHook fires at the beginning of each agent turn.
type TurnStartHook func(ctx context.Context, turnNumber int, userMessage string) *TurnStartHookResult

// TurnEndHookResult is the aggregated result from turn-end hooks.
type TurnEndHookResult struct{}

// TurnEndHook fires at the end of each agent turn.
type TurnEndHook func(ctx context.Context, turnNumber int, assistantMessage string)

// ---------------------------------------------------------------------------
// Registration methods
// ---------------------------------------------------------------------------

// RegisterSessionStart registers a session-start hook.
func (e *Executor) RegisterSessionStart(h SessionStartHook) {
	e.sessionStartHooks = append(e.sessionStartHooks, h)
}

// RegisterSessionEnd registers a session-end hook.
func (e *Executor) RegisterSessionEnd(h SessionEndHook) {
	e.sessionEndHooks = append(e.sessionEndHooks, h)
}

// RegisterPreCompact registers a pre-compaction hook.
func (e *Executor) RegisterPreCompact(h PreCompactHook) {
	e.preCompactHooks = append(e.preCompactHooks, h)
}

// RegisterPostCompact registers a post-compaction hook.
func (e *Executor) RegisterPostCompact(h PostCompactHook) {
	e.postCompactHooks = append(e.postCompactHooks, h)
}

// RegisterNotification registers a notification hook.
func (e *Executor) RegisterNotification(h NotificationHook) {
	e.notificationHooks = append(e.notificationHooks, h)
}

// RegisterCommand registers a command hook.
func (e *Executor) RegisterCommand(h CommandHook) {
	e.commandHooks = append(e.commandHooks, h)
}

// RegisterTurnStart registers a turn-start hook.
func (e *Executor) RegisterTurnStart(h TurnStartHook) {
	e.turnStartHooks = append(e.turnStartHooks, h)
}

// RegisterTurnEnd registers a turn-end hook.
func (e *Executor) RegisterTurnEnd(h TurnEndHook) {
	e.turnEndHooks = append(e.turnEndHooks, h)
}

// ---------------------------------------------------------------------------
// Execution methods
// ---------------------------------------------------------------------------

// ExecuteSessionStart runs all session-start hooks and returns the aggregated result.
func (e *Executor) ExecuteSessionStart(ctx context.Context, sessionID string, isResume bool) *SessionStartHookResult {
	result := &SessionStartHookResult{}
	for _, h := range e.sessionStartHooks {
		r := h(ctx, sessionID, isResume)
		if r == nil {
			continue
		}
		if r.SkipDefault {
			result.SkipDefault = true
		}
		result.Attachments = append(result.Attachments, r.Attachments...)
	}
	return result
}

// ExecuteSessionEnd runs all session-end hooks.
func (e *Executor) ExecuteSessionEnd(ctx context.Context, sessionID, reason string) {
	for _, h := range e.sessionEndHooks {
		h(ctx, sessionID, reason)
	}
}

// ExecutePreCompact runs all pre-compaction hooks and returns the aggregated result.
func (e *Executor) ExecutePreCompact(ctx context.Context, messageCount, tokenCount int) *PreCompactHookResult {
	result := &PreCompactHookResult{}
	for _, h := range e.preCompactHooks {
		r := h(ctx, messageCount, tokenCount)
		if r == nil {
			continue
		}
		if r.Cancel {
			result.Cancel = true
		}
		result.MemoryEntries = append(result.MemoryEntries, r.MemoryEntries...)
	}
	return result
}

// ExecutePostCompact runs all post-compaction hooks and returns the aggregated result.
func (e *Executor) ExecutePostCompact(ctx context.Context, tokensBefore, tokensAfter int) *PostCompactHookResult {
	result := &PostCompactHookResult{}
	for _, h := range e.postCompactHooks {
		r := h(ctx, tokensBefore, tokensAfter)
		if r == nil {
			continue
		}
		result.Attachments = append(result.Attachments, r.Attachments...)
	}
	return result
}

// ExecuteNotification runs all notification hooks.
func (e *Executor) ExecuteNotification(ctx context.Context, level, message string, data map[string]any) {
	for _, h := range e.notificationHooks {
		h(ctx, level, message, data)
	}
}

// ExecuteCommand runs all command hooks and returns the aggregated result.
// The first hook that sets Handled=true wins; subsequent hooks are not called.
func (e *Executor) ExecuteCommand(ctx context.Context, command, args string) *CommandHookResult {
	result := &CommandHookResult{}
	for _, h := range e.commandHooks {
		r := h(ctx, command, args)
		if r == nil {
			continue
		}
		if r.Handled {
			result.Output = r.Output
			result.Handled = true
			return result
		}
		if r.Output != "" {
			result.Output = r.Output
		}
	}
	return result
}

// ExecuteTurnStart runs all turn-start hooks and returns the aggregated result.
func (e *Executor) ExecuteTurnStart(ctx context.Context, turnNumber int, userMessage string) *TurnStartHookResult {
	result := &TurnStartHookResult{}
	for _, h := range e.turnStartHooks {
		r := h(ctx, turnNumber, userMessage)
		if r == nil {
			continue
		}
		result.Attachments = append(result.Attachments, r.Attachments...)
	}
	return result
}

// ExecuteTurnEnd runs all turn-end hooks.
func (e *Executor) ExecuteTurnEnd(ctx context.Context, turnNumber int, assistantMessage string) {
	for _, h := range e.turnEndHooks {
		h(ctx, turnNumber, assistantMessage)
	}
}
