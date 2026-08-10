package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// turnEventEmitter serializes all event sources for one SubmitMessage call.
// Query-loop, hook, command, permission, and terminal events therefore share
// one sequence and one runtime reducer path.
type turnEventEmitter struct {
	engine *QueryEngine
	ctx    context.Context
	events chan<- QueryEvent
	turnID string
	// identity is frozen when SubmitMessage starts. Session-changing commands
	// may update the engine for the next turn, but the current command/result
	// turn must still terminate on its source thread.
	identity RuntimeEventEnvelope
	clock    func() time.Time

	mu     sync.Mutex
	closed bool
}

func newTurnEventEmitter(ctx context.Context, engine *QueryEngine, events chan<- QueryEvent, turnID string) *turnEventEmitter {
	if ctx == nil {
		ctx = context.Background()
	}
	identity, clock := engine.runtimeIdentitySnapshot()
	return &turnEventEmitter{
		engine:   engine,
		ctx:      ctx,
		events:   events,
		turnID:   turnID,
		identity: identity,
		clock:    clock,
	}
}

// Emit decorates and reduces the event before publishing it. Interactive and
// terminal events use lossless delivery; high-frequency events retain the
// historical context-cancelled best-effort channel behavior while still being
// recorded in the engine snapshot.
func (e *turnEventEmitter) Emit(evt QueryEvent) bool {
	if e == nil || e.engine == nil || e.events == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	ownsGoalTurn := e.identity.GoalID != "" &&
		e.identity.SessionID == e.identity.GoalRootSessionID &&
		e.identity.ThreadID == e.identity.GoalRootThreadID &&
		e.identity.AgentID == e.identity.GoalRootAgentID
	if evt.Type == EventPermissionRequest &&
		evt.PermissionRequest != nil &&
		ownsGoalTurn {
		waitEvent, changed, err := e.engine.goalService.pauseTurnFor(
			"permission:"+evt.PermissionRequest.ToolUseID,
			GoalLifecyclePermissionWaiting,
			e.clock().UTC(),
		)
		if err != nil {
			e.engine.recordRuntimeStateError(err)
		} else if changed {
			_, _, _ = e.emitOneLocked(waitEvent)
		}
	}
	if evt.Type == EventTerminal &&
		evt.TerminalInfo != nil &&
		ownsGoalTurn {
		if evt.TerminalInfo.Reason == TerminalWaitingInput {
			// ProjectGraph permission handling returns control to the
			// transport and later resumes through a new query turn. Keep the
			// logical Goal turn and its excluded wait interval open.
			_, _, delivered := e.emitOneLocked(evt)
			return delivered
		}
		goalEvent, terminalEvent, err := e.engine.commitGoalTerminalEvents(
			e.turnID,
			e.identity,
			e.clock,
			evt,
		)
		if err != nil {
			e.engine.goalService.abandonTurn(e.identity.GoalTurnID)
			failure := Terminal{
				Reason:    TerminalPersistenceError,
				TurnCount: evt.TerminalInfo.TurnCount,
				MaxTurns:  evt.TerminalInfo.MaxTurns,
				Err:       fmt.Errorf("commit Goal terminal boundary: %w", err),
			}
			_, _, delivered := e.emitOneLocked(QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: &failure,
			})
			return delivered
		}
		e.events <- goalEvent
		e.events <- terminalEvent
		return true
	}
	decorated, reducerAccepted, delivered := e.emitOneLocked(evt)
	if reducerAccepted {
		derived := e.engine.deriveSubagentProgressEvent(evt)
		if derived != nil {
			_, _, _ = e.emitOneLocked(*derived)
		}
	}
	if reducerAccepted &&
		evt.Type == EventPermissionResolved &&
		evt.PermissionResolved != nil &&
		ownsGoalTurn {
		resumeEvent, changed, err := e.engine.goalService.resumeTurnFor(
			"permission:"+evt.PermissionResolved.ToolUseID,
			GoalLifecyclePermissionResumed,
			decorated.Timestamp,
		)
		if err != nil {
			e.engine.recordRuntimeStateError(err)
		} else if changed {
			_, _, _ = e.emitOneLocked(resumeEvent)
		}
	}
	return delivered
}

func (e *turnEventEmitter) emitOneLocked(evt QueryEvent) (QueryEvent, bool, bool) {
	var reducerAccepted bool
	evt, reducerAccepted = e.engine.decorateRuntimeEventWithIdentity(
		e.turnID,
		e.identity,
		e.clock,
		evt,
	)
	if !reducerAccepted {
		return evt, false, false
	}
	if evt.Type == EventTerminal && evt.TerminalInfo != nil {
		_ = e.engine.persistSessionCheckpoint(string(evt.TerminalInfo.Reason))
	}
	if IsLosslessRuntimeEvent(evt.Type) {
		e.events <- evt
		return evt, true, true
	}
	select {
	case e.events <- evt:
		return evt, true, true
	case <-e.ctx.Done():
		return evt, true, false
	}
}

// BindGoal freezes exact root Goal attribution for this already-admitted
// turn. It must run before the first turn event is emitted.
func (e *turnEventEmitter) BindGoal(identity *goalExecutionIdentity) {
	if e == nil || identity == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	identity.decorate(&e.identity)
}

func (e *turnEventEmitter) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
}

func (e *QueryEngine) decorateRuntimeEvent(turnID string, evt QueryEvent) QueryEvent {
	identity, clock := e.runtimeIdentitySnapshot()
	decorated, _ := e.decorateRuntimeEventWithIdentity(
		turnID,
		identity,
		clock,
		evt,
	)
	return decorated
}

func (e *QueryEngine) runtimeIdentitySnapshot() (RuntimeEventEnvelope, func() time.Time) {
	e.mu.Lock()
	identity := RuntimeEventEnvelope{
		SessionID:       e.config.SessionID,
		ThreadID:        e.config.ThreadID,
		AgentID:         e.config.AgentID,
		ParentSessionID: e.config.ParentSessionID,
		ParentThreadID:  e.config.ParentThreadID,
		ParentAgentID:   e.config.ParentAgentID,
		ParentToolUseID: e.config.ParentToolUseID,
		AgentGeneration: e.config.AgentGeneration,
	}
	if e.config.goalBinding != nil {
		e.config.goalBinding.decorate(&identity)
	}
	clock := e.config.Clock
	e.mu.Unlock()
	return identity, clock
}

func (e *QueryEngine) recordRuntimeStateError(err error) {
	if e == nil || err == nil {
		return
	}
	e.runtimeMu.Lock()
	if e.runtimeStateErr == nil {
		e.runtimeStateErr = err
	}
	e.runtimeMu.Unlock()
}

func (e *QueryEngine) decorateRuntimeEventWithIdentity(
	turnID string,
	identity RuntimeEventEnvelope,
	clock func() time.Time,
	evt QueryEvent,
) (QueryEvent, bool) {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	return e.decorateRuntimeEventWithIdentityLocked(
		turnID,
		identity,
		clock,
		evt,
	)
}

func (e *QueryEngine) decorateRuntimeEventWithIdentityLocked(
	turnID string,
	identity RuntimeEventEnvelope,
	clock func() time.Time,
	evt QueryEvent,
) (QueryEvent, bool) {
	sequence := e.runtimeSequences[identity.ThreadID]
	if e.runtimeState != nil {
		// A shared child QueryEngine can advance the same thread between
		// lifecycle transitions emitted by this engine-owned service.
		if shared := e.runtimeState.LastSequence(identity.ThreadID); shared > sequence {
			sequence = shared
		}
	}
	sequence++

	causationID := strings.TrimSpace(evt.CausationID)
	if causationID == "" {
		causationID = runtimeEventCausationID(evt)
	}
	if causationID == "" {
		causationID = identity.ParentToolUseID
	}

	identity.TurnID = turnID
	identity.Sequence = sequence
	identity.Timestamp = clock().UTC()
	identity.CausationID = causationID
	evt.RuntimeEventEnvelope = identity
	if e.runtimeState != nil {
		if err := e.runtimeState.Apply(evt); err != nil {
			if e.runtimeStateErr == nil {
				e.runtimeStateErr = err
			}
			return evt, false
		}
	}
	e.runtimeSequences[identity.ThreadID] = sequence
	return evt, true
}

func runtimeEventCausationID(evt QueryEvent) string {
	_, toolUseID := runtimeEventToolIdentity(evt)
	if toolUseID != "" {
		return toolUseID
	}
	switch {
	case evt.TaskProgress != nil && evt.TaskProgress.TaskID != "":
		return evt.TaskProgress.TaskID
	case evt.TaskLifecycle != nil && evt.TaskLifecycle.TaskID != "":
		return evt.TaskLifecycle.TaskID
	case evt.CommandLifecycle != nil:
		return evt.CommandLifecycle.CommandUUID
	case evt.CommandResult != nil:
		return "command:" + evt.CommandResult.Command
	case evt.HookResponse != nil:
		return evt.HookResponse.HookID
	case evt.ToolUseSummary != nil:
		return evt.ToolUseSummary.UUID
	case evt.PlanStateTransition != nil:
		return evt.PlanStateTransition.RequestID
	case evt.GoalLifecycle != nil:
		return fmt.Sprintf(
			"goal:%s:%d",
			evt.GoalLifecycle.Goal.GoalID,
			evt.GoalLifecycle.Goal.Revision,
		)
	case evt.WorktreeLifecycle != nil:
		return evt.WorktreeLifecycle.RecordID
	case evt.ModelAttempt != nil &&
		evt.ModelAttempt.AttemptID != "":
		return evt.ModelAttempt.AttemptID
	case evt.TombstoneUUID != "":
		return evt.TombstoneUUID
	default:
		return ""
	}
}

// RuntimeSnapshot returns the canonical engine-owned runtime read model.
func (e *QueryEngine) RuntimeSnapshot() RuntimeSnapshot {
	if e == nil {
		return RuntimeSnapshot{Threads: map[string]RuntimeThreadSnapshot{}, Agents: map[string]RuntimeAgentSnapshot{}, Tasks: map[string]RuntimeTaskSnapshot{}, Worktrees: map[string]RuntimeWorktreeSnapshot{}}
	}
	e.mu.Lock()
	threadID := e.config.ThreadID
	e.mu.Unlock()
	return e.runtimeState.Snapshot(threadID)
}

// RuntimeThreadTiming returns one thread's engine-owned cumulative active
// execution time across turns.
func (e *QueryEngine) RuntimeThreadTiming(threadID string) (RuntimeThreadTimingSnapshot, bool) {
	if e == nil || e.runtimeState == nil {
		return RuntimeThreadTimingSnapshot{}, false
	}
	return e.runtimeState.ThreadTimingSnapshot(threadID)
}

// RuntimeStateError reports the first internal reducer rejection, if any.
// Engine-generated event streams should always return nil.
func (e *QueryEngine) RuntimeStateError() error {
	if e == nil {
		return nil
	}
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	return e.runtimeStateErr
}
