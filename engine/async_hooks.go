package engine

import (
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/hooks"
)

func (e *QueryEngine) handleAsyncShellHookCompletion(completion hooks.AsyncShellHookCompletion) {
	if e == nil {
		return
	}
	durationMS := int64(0)
	if !completion.CompletedAt.IsZero() {
		durationMS = completion.CompletedAt.Sub(completion.StartedAt).Milliseconds()
	}
	turnID := strings.TrimSpace(completion.TurnID)
	if turnID == "" && e.runtimeState != nil {
		e.mu.Lock()
		threadID := e.config.ThreadID
		e.mu.Unlock()
		if thread, ok := e.runtimeState.ThreadSnapshot(threadID); ok {
			turnID = thread.ActiveTurnID
		}
	}
	if turnID == "" {
		turnID = "async-hook-" + completion.ID
	}
	event := e.decorateRuntimeEvent(turnID, QueryEvent{
		Type: EventHookResponse,
		HookResponse: &HookResponseEvent{
			HookID: completion.ID, HookName: completion.HookName,
			HookEvent: completion.Event, ToolName: completion.ToolName,
			StatusMessage: completion.StatusMessage, Outcome: completion.Outcome(),
			ExitCode: completion.Result.ExitCode, AsyncRewake: completion.AsyncRewake,
			Phase:      completion.Phase,
			DurationMS: durationMS,
		},
	})
	coordinator, scope, coordinatorErr := e.runtimeInputOwner()
	if completion.Phase == "completed" &&
		completion.AsyncRewake &&
		completion.Result.ExitCode == 2 &&
		coordinatorErr == nil {
		if message := completion.ModelMessage(); message != nil {
			_, enqueueErr := coordinator.Enqueue(RuntimeItem{
				ID:         "async-rewake:" + completion.ID,
				Kind:       RuntimeItemAsyncRewake,
				Priority:   RuntimePriorityLater,
				Scope:      scope,
				IsMeta:     true,
				Origin:     "async-hook",
				Provenance: "async_rewake",
				AsyncRewake: &RuntimeAsyncRewake{
					HookID:      completion.ID,
					Event:       completion.Event,
					HookName:    completion.HookName,
					ToolName:    completion.ToolName,
					Outcome:     completion.Outcome(),
					ExitCode:    completion.Result.ExitCode,
					ModelPrompt: message.Content,
				},
			})
			if enqueueErr == nil {
				e.config.HookExecutor.AcknowledgeAsyncShellDelivery(
					completion.ID,
				)
			} else {
				e.mu.Lock()
				if e.inputCoordinator == coordinator &&
					e.inputCoordinatorErr == nil {
					e.inputCoordinatorErr = enqueueErr
				}
				e.mu.Unlock()
			}
		}
	}
	e.asyncHookMu.Lock()
	closed := e.asyncHookClosed
	subscribed := e.asyncHookSubscribed
	done := e.asyncHookDone
	events := e.asyncHookEvents
	if closed || !subscribed {
		e.asyncHookMu.Unlock()
		return
	}
	e.asyncHookWG.Add(1)
	e.asyncHookMu.Unlock()
	defer e.asyncHookWG.Done()
	if completion.Phase == "running" {
		select {
		case events <- event:
		default:
		}
		return
	}
	select {
	case events <- event:
	case <-done:
	}
}

// SubscribeAsyncHookEvents enables the single active transport consumer for
// executor-owned background hook events. The channel closes with the engine.
func (e *QueryEngine) SubscribeAsyncHookEvents() <-chan QueryEvent {
	if e == nil {
		return nil
	}
	e.asyncHookMu.Lock()
	if !e.asyncHookClosed {
		e.asyncHookSubscribed = true
	}
	e.asyncHookMu.Unlock()
	return e.asyncHookEvents
}

func drainAsyncHookMessages(executor *hooks.Executor) []*schema.Message {
	if executor == nil {
		return nil
	}
	return executor.DrainAsyncShellMessages()
}
