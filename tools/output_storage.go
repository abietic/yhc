package tools

import "context"

// ToolOutputPersister retains complete execution output before a tool creates
// its bounded preview. The host supplies session-owned storage; tools never
// choose a storage directory or bypass ordinary Read permissions.
type ToolOutputPersister func(context.Context, string, ...string) (string, error)

type toolOutputPersisterKey struct{}

// WithToolOutputPersister binds the current session's output owner. A nil
// persister explicitly removes an inherited owner.
func WithToolOutputPersister(ctx context.Context, persist ToolOutputPersister) context.Context {
	return context.WithValue(ctx, toolOutputPersisterKey{}, persist)
}

func toolOutputPersisterFromContext(ctx context.Context) ToolOutputPersister {
	if ctx == nil {
		return nil
	}
	persist, _ := ctx.Value(toolOutputPersisterKey{}).(ToolOutputPersister)
	return persist
}
