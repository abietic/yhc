package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SyncRuntimeItems transfers pending external outbox records into the
// session-scoped coordinator. Source acknowledgment happens only after the
// complete batch is durably accepted.
func (e *QueryEngine) SyncRuntimeItems(ctx context.Context) error {
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return err
	}
	return e.collectRuntimeItemsWith(ctx, coordinator, scope)
}

func (e *QueryEngine) collectRuntimeItemsWith(
	_ context.Context,
	coordinator *RuntimeInputCoordinator,
	scope RuntimeInputScope,
) error {
	if e == nil || coordinator == nil || e.agentRunner == nil {
		return nil
	}
	if scope.AgentID != "" {
		if err := e.collectAgentMessages(coordinator, scope); err != nil {
			return err
		}
	}
	return e.collectAgentNotifications(coordinator, scope)
}

func (e *QueryEngine) collectAgentMessages(
	coordinator *RuntimeInputCoordinator,
	scope RuntimeInputScope,
) error {
	if _, ok := e.agentRunner.GetAgent(scope.AgentID); !ok {
		// Direct SubAgentExecutor calls do not register a background runner
		// outbox. They still use the same query runtime, but have no
		// SendMessage source to transfer.
		return nil
	}
	messages, err := e.agentRunner.PendingAgentMessages(scope.AgentID)
	if err != nil {
		return fmt.Errorf("collect Agent messages for %s: %w", scope.AgentID, err)
	}
	items := make([]RuntimeItem, 0, len(messages))
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		id, _ := message.Metadata["command_uuid"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf(
				"collect Agent messages for %s: pending message has no command UUID",
				scope.AgentID,
			)
		}
		items = append(items, RuntimeItem{
			ID:         id,
			Kind:       RuntimeItemAgentMessage,
			Priority:   RuntimePriorityNext,
			Scope:      scope,
			IsMeta:     true,
			Origin:     "coordinator",
			Provenance: "send_message",
			AgentMessage: &RuntimeAgentMessage{
				From:    message.From,
				To:      message.To,
				Content: message.Content,
			},
		})
		ids = append(ids, id)
	}
	if len(items) == 0 {
		return nil
	}
	if _, err := coordinator.EnqueueBatch(items); err != nil {
		return fmt.Errorf("persist Agent message batch: %w", err)
	}
	acknowledged, err := e.agentRunner.AcknowledgeAgentMessages(scope.AgentID, ids)
	if err != nil {
		return fmt.Errorf("acknowledge Agent message batch: %w", err)
	}
	if acknowledged != len(ids) {
		return fmt.Errorf(
			"acknowledge Agent message batch: accepted %d item(s), removed %d",
			len(ids),
			acknowledged,
		)
	}
	return nil
}

func (e *QueryEngine) collectAgentNotifications(
	coordinator *RuntimeInputCoordinator,
	scope RuntimeInputScope,
) error {
	notifications, err := e.agentRunner.PendingAgentNotificationsForParent(
		scope.SessionID,
		scope.ThreadID,
		scope.AgentID,
	)
	if err != nil {
		return fmt.Errorf("reconstruct Agent completion notifications: %w", err)
	}
	items := make([]RuntimeItem, 0, len(notifications))
	ids := make([]string, 0, len(notifications))
	lookupIDs := make([]string, 0, len(notifications)*2)
	for _, notification := range notifications {
		if !isTerminalAgentNotificationStatus(notification.Status) ||
			strings.TrimSpace(notification.Message) == "" {
			continue
		}
		id := notification.DeliveryID()
		legacyID := notification.LegacyDeliveryID()
		if coordinator.Known(id) ||
			(legacyID != "" && coordinator.Known(legacyID)) {
			continue
		}
		lookupIDs = append(lookupIDs, id)
		if legacyID != "" {
			lookupIDs = append(lookupIDs, legacyID)
		}
	}
	delivered, err := coordinator.ResolveDelivered(lookupIDs)
	if err != nil {
		return fmt.Errorf("resolve Agent completion delivery: %w", err)
	}
	for _, notification := range notifications {
		if !isTerminalAgentNotificationStatus(notification.Status) ||
			strings.TrimSpace(notification.Message) == "" {
			continue
		}
		id := notification.DeliveryID()
		legacyID := notification.LegacyDeliveryID()
		if coordinator.Known(id) ||
			(legacyID != "" && coordinator.Known(legacyID)) {
			continue
		}
		if _, ok := delivered[id]; ok {
			continue
		}
		if _, ok := delivered[legacyID]; legacyID != "" && ok {
			if err := coordinator.Settle(id); err != nil {
				return fmt.Errorf(
					"cache migrated Agent completion delivery %s: %w",
					id,
					err,
				)
			}
			continue
		}
		items = append(items, RuntimeItem{
			ID:         id,
			Kind:       RuntimeItemAgentNotification,
			Priority:   RuntimePriorityNext,
			Scope:      scope,
			IsMeta:     true,
			Origin:     "task-notification",
			Provenance: "agent_notification",
			AgentNotification: &RuntimeAgentNotification{
				ReceiptVersion:   notification.CompletionVersion,
				CompletionID:     id,
				AgentID:          notification.AgentID,
				ToolUseID:        notification.ToolUseID,
				Status:           notification.Status,
				Description:      notification.Description,
				OutputFile:       notification.OutputFile,
				Message:          notification.Message,
				Generation:       notification.Generation,
				TerminalSequence: notification.TerminalSequence,
			},
		})
		ids = append(ids, id)
	}
	if len(items) == 0 {
		return nil
	}
	if _, err := coordinator.EnqueueBatch(items); err != nil {
		return fmt.Errorf("persist Agent notification batch: %w", err)
	}
	e.agentRunner.AcknowledgeAgentNotifications(ids)
	return nil
}

// SubmitRuntimeItem starts one idle-claimed input through the normal
// QueryEngine entrypoint. The runtime item ID is recorded on the initial user
// message so transcript durability can atomically settle the coordinator.
func (e *QueryEngine) SubmitRuntimeItem(
	ctx context.Context,
	item RuntimeItem,
) (<-chan QueryEvent, Terminal) {
	if e == nil {
		events := make(chan QueryEvent)
		close(events)
		return events, Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("query engine is nil"),
		}
	}
	if item.Kind == RuntimeItemPermissionDecision {
		if item.PermissionDecision == nil {
			events := make(chan QueryEvent, 1)
			terminal := Terminal{
				Reason: TerminalModelError,
				Err: fmt.Errorf(
					"runtime item %q has no permission decision",
					item.ID,
				),
			}
			events <- QueryEvent{Type: EventTerminal, TerminalInfo: &terminal}
			close(events)
			return events, terminal
		}
		return e.submitMessageWithRuntimeItem(
			ctx,
			"",
			runtimeItemMetadata(item),
			nil,
			&item,
			nil,
		)
	}
	if item.Kind == RuntimeItemUserPrompt &&
		item.UserPrompt != nil &&
		item.UserPrompt.durablePrompt != nil {
		coordinator, _, err := e.runtimeInputOwner()
		if err != nil {
			return closedPromptInputError(err)
		}
		authoritative, err := coordinator.processingDurableRuntimePrompt(item)
		if err != nil {
			if !errors.Is(err, errRuntimePromptAlreadySubmitting) {
				e.releaseRuntimeItem(item.ID)
			}
			return closedPromptInputError(err)
		}
		item = authoritative
		admitted, err := e.admitPromptInput(
			ctx,
			*item.UserPrompt.materializedInput,
			"",
		)
		if err != nil {
			e.releaseRuntimeItem(item.ID)
			return closedPromptInputError(err)
		}
		return e.submitMessageWithRuntimeItem(
			ctx,
			admitted.textForHook(),
			runtimeItemMetadata(item),
			nil,
			&item,
			admitted,
		)
	}
	prompt := strings.TrimSpace(runtimeItemPrompt(item))
	if prompt == "" {
		events := make(chan QueryEvent, 1)
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("runtime item %q has no model prompt", item.ID),
		}
		events <- QueryEvent{Type: EventTerminal, TerminalInfo: &terminal}
		close(events)
		return events, terminal
	}
	var images []UserImage
	if item.UserPrompt != nil {
		images = append([]UserImage(nil), item.UserPrompt.Images...)
	}
	return e.submitMessage(ctx, prompt, runtimeItemMetadata(item), images)
}

func (e *QueryEngine) preflightClaimedRuntimeItem(
	ctx context.Context,
	item RuntimeItem,
) error {
	if item.Kind != RuntimeItemUserPrompt ||
		item.UserPrompt == nil ||
		item.UserPrompt.durablePrompt == nil {
		return nil
	}
	if item.UserPrompt.materializedInput == nil {
		return fmt.Errorf("durable queued prompt was not materialized")
	}
	admitted, err := e.admitPromptInput(
		ctx,
		*item.UserPrompt.materializedInput,
		"",
	)
	if err != nil {
		return err
	}
	e.releaseAdmittedPrompt(admitted)
	return nil
}

// SubmitGoalContinuation admits a cursor claimed through the dedicated Goal
// API. Generic SubmitRuntimeItem continues to reject this kind.
func (e *QueryEngine) SubmitGoalContinuation(
	ctx context.Context,
	item RuntimeItem,
) (<-chan QueryEvent, Terminal) {
	if !e.goalWorkflowEnabled() {
		return closedRuntimeItemError(
			fmt.Errorf(
				"goal continuation requires an enabled saved root TUI, Plain, dedicated headless Goal, or negotiated ACP session",
			),
		)
	}
	return e.submitGoalContinuation(ctx, item)
}

// RequestStop records one explicit stop control. Immediate mode also triggers
// the existing abort controller; graceful mode waits for the next safe round
// boundary.
func (e *QueryEngine) RequestStop(mode RuntimeStopMode, reason string) error {
	if mode != RuntimeStopGraceful && mode != RuntimeStopImmediate {
		return fmt.Errorf("unsupported runtime stop mode %q", mode)
	}
	if e == nil {
		return nil
	}
	if err := e.goalService.pauseForCancellation(
		reason,
		e.config.Clock().UTC(),
	); err != nil {
		return err
	}
	e.mu.Lock()
	abortController := e.abortController
	if abortController == nil {
		e.mu.Unlock()
		return nil
	}
	coordinator := e.inputCoordinator
	scope := RuntimeInputScope{
		SessionID: e.config.SessionID,
		ThreadID:  e.config.ThreadID,
		AgentID:   e.config.AgentID,
	}
	if e.inputCoordinatorErr != nil {
		err := fmt.Errorf(
			"runtime input coordinator is unavailable: %w",
			e.inputCoordinatorErr,
		)
		e.mu.Unlock()
		return err
	}
	if coordinator == nil {
		e.mu.Unlock()
		return fmt.Errorf("runtime input coordinator is unavailable")
	}
	_, err := coordinator.Enqueue(RuntimeItem{
		ID:       generateUUID(),
		Kind:     RuntimeItemStop,
		Priority: RuntimePriorityNow,
		Scope:    scope,
		IsMeta:   true,
		Origin:   "control",
		Stop: &RuntimeStop{
			Mode:   mode,
			Reason: strings.TrimSpace(reason),
		},
	})
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if mode == RuntimeStopImmediate {
		abortController.Abort()
	}
	e.mu.Unlock()
	return nil
}

func (e *QueryEngine) finishRuntimeInputTurn(
	abortController *AbortController,
	coordinator *RuntimeInputCoordinator,
	scope RuntimeInputScope,
) {
	if e == nil || abortController == nil {
		return
	}
	e.mu.Lock()
	if e.abortController != abortController {
		e.mu.Unlock()
		return
	}
	e.abortController = nil
	e.mu.Unlock()
	if coordinator == nil {
		return
	}
	if err := coordinator.settleStopRequests(scope); err != nil {
		e.mu.Lock()
		if e.inputCoordinator == coordinator && e.inputCoordinatorErr == nil {
			e.inputCoordinatorErr = err
		}
		e.mu.Unlock()
	}
}
