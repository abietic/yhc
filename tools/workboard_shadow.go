package tools

import "context"

// WorkBoardShadowObserver receives detached snapshots after an accepted legacy
// logical-work mutation. It is observational only: callers must freeze and
// return the legacy result regardless of observer behavior.
type WorkBoardShadowObserver interface {
	ObserveTasks([]*TaskRecord)
	ObserveTodos(WorkBoardTodoScope, []TodoItem)
}

// WorkBoardTodoScope is the trusted legacy TodoWrite partition identity.
type WorkBoardTodoScope struct {
	SessionID string
	AgentID   string
}

type workBoardShadowObserverCtxKey struct{}

// WithWorkBoardShadowObserver binds the optional observer owned by the current
// root QueryEngine lineage.
func WithWorkBoardShadowObserver(
	ctx context.Context,
	observer WorkBoardShadowObserver,
) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, workBoardShadowObserverCtxKey{}, observer)
}

func workBoardShadowObserverFromCtx(ctx context.Context) WorkBoardShadowObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(workBoardShadowObserverCtxKey{}).(WorkBoardShadowObserver)
	return observer
}

func observeWorkBoardTasks(ctx context.Context, manager *TaskManager) {
	observer := workBoardShadowObserverFromCtx(ctx)
	if observer == nil || manager == nil {
		return
	}
	safelyObserveWorkBoard(func() {
		observer.ObserveTasks(manager.List())
	})
}

func safelyObserveWorkBoard(observe func()) {
	defer func() {
		_ = recover()
	}()
	observe()
}
