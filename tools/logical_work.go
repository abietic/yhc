package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// ErrMissingToolOwner is returned by direct and non-Session compatibility
// calls that do not explicitly bind their runtime owner.
var ErrMissingToolOwner = errors.New(
	"tool owner: explicit runtime owner binding required",
)

// TodoScope identifies one exact legacy Todo projection within a root Session.
type TodoScope struct {
	SessionID string
	AgentID   string
}

// TodoAuthority is the QueryEngine-bound Todo compatibility surface. Its
// implementation owns persistence and returns only the current legacy view.
type TodoAuthority interface {
	Todos(TodoScope) ([]TodoItem, error)
	ReplaceTodos(TodoScope, []TodoItem) error
}

// EphemeralTodoAuthority is an isolated process-lifetime Todo owner for
// explicit non-Session embeddings such as one standalone MCP server.
type EphemeralTodoAuthority struct {
	mu    sync.RWMutex
	items map[TodoScope][]TodoItem
}

// NewEphemeralTodoAuthority returns one fresh isolated in-memory Todo owner.
func NewEphemeralTodoAuthority() *EphemeralTodoAuthority {
	return &EphemeralTodoAuthority{
		items: make(map[TodoScope][]TodoItem),
	}
}

// Todos returns a defensive copy for one exact compatibility scope.
func (a *EphemeralTodoAuthority) Todos(scope TodoScope) ([]TodoItem, error) {
	if a == nil {
		return nil, ErrMissingToolOwner
	}
	scope = normalizeTodoScope(scope)
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneTodoItems(a.items[scope]), nil
}

// ReplaceTodos atomically replaces one exact compatibility scope.
func (a *EphemeralTodoAuthority) ReplaceTodos(
	scope TodoScope,
	items []TodoItem,
) error {
	if a == nil {
		return ErrMissingToolOwner
	}
	scope = normalizeTodoScope(scope)
	a.mu.Lock()
	defer a.mu.Unlock()
	allCompleted := len(items) > 0
	for _, item := range items {
		if item.Status != "completed" {
			allCompleted = false
			break
		}
	}
	if allCompleted {
		items = nil
	}
	if len(items) == 0 {
		delete(a.items, scope)
		return nil
	}
	a.items[scope] = cloneTodoItems(items)
	return nil
}

type (
	logicalWorkAuthorityCtxKey       struct{}
	nonSessionLogicalWorkScopeCtxKey struct{}
)

// WithLogicalWorkAuthority binds the root-lineage logical-work owner to tool
// execution. A durable Session context without this binding fails closed.
func WithLogicalWorkAuthority(
	ctx context.Context,
	authority TodoAuthority,
) context.Context {
	if authority == nil {
		return ctx
	}
	return context.WithValue(ctx, logicalWorkAuthorityCtxKey{}, authority)
}

// WithNonSessionLogicalWorkScope marks a direct embedding as an opaque
// process-local compatibility scope. It never names or persists a Session.
func WithNonSessionLogicalWorkScope(
	ctx context.Context,
	scope string,
) context.Context {
	return context.WithValue(
		ctx,
		nonSessionLogicalWorkScopeCtxKey{},
		strings.TrimSpace(scope),
	)
}

func logicalWorkAuthorityFromCtx(ctx context.Context) TodoAuthority {
	if ctx == nil {
		return nil
	}
	authority, _ := ctx.Value(logicalWorkAuthorityCtxKey{}).(TodoAuthority)
	return authority
}

func nonSessionLogicalWorkScopeFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	scope, _ := ctx.Value(nonSessionLogicalWorkScopeCtxKey{}).(string)
	return strings.TrimSpace(scope)
}

func durableLogicalWorkScopeError(ctx context.Context) error {
	if strings.TrimSpace(SessionIDFromCtx(ctx)) == "" {
		return nil
	}
	return errors.New(
		"workboard authority: durable Session scope has no LogicalWorkAdapter",
	)
}

func todoScopeFromCtx(ctx context.Context) TodoScope {
	scope := TodoScope{
		SessionID: strings.TrimSpace(SessionIDFromCtx(ctx)),
		AgentID:   strings.TrimSpace(AgentIDFromCtx(ctx)),
	}
	if scope.SessionID == "" {
		scope.SessionID = nonSessionLogicalWorkScopeFromCtx(ctx)
	}
	return normalizeTodoScope(scope)
}

func normalizeTodoScope(scope TodoScope) TodoScope {
	scope.SessionID = strings.TrimSpace(scope.SessionID)
	scope.AgentID = strings.TrimSpace(scope.AgentID)
	return scope
}

func cloneTodoItems(items []TodoItem) []TodoItem {
	if len(items) == 0 {
		return nil
	}
	return append([]TodoItem(nil), items...)
}
