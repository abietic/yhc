package tools

import "context"

type threadIDContextKey struct{}

// WithThreadID carries the current runtime thread identity into tool execution.
func WithThreadID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, threadIDContextKey{}, id)
}

// ThreadIDFromCtx returns the current runtime thread identity.
func ThreadIDFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(threadIDContextKey{}).(string)
	return id
}

type toolUseIDContextKey struct{}

// WithToolUseID carries the spawning tool-call identity into context-aware tool
// implementations such as Agent.
func WithToolUseID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolUseIDContextKey{}, id)
}

// ToolUseIDFromCtx returns the current tool-call identity.
func ToolUseIDFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(toolUseIDContextKey{}).(string)
	return id
}

type shellManagerContextKey struct{}

// WithShellManager carries the QueryEngine-owned persistent shell manager into
// Bash-family tool execution.
func WithShellManager(ctx context.Context, manager *ShellManager) context.Context {
	return context.WithValue(ctx, shellManagerContextKey{}, manager)
}

// ShellManagerFromCtx returns the QueryEngine-owned shell manager.
func ShellManagerFromCtx(ctx context.Context) *ShellManager {
	if ctx == nil {
		return nil
	}
	manager, _ := ctx.Value(shellManagerContextKey{}).(*ShellManager)
	return manager
}
