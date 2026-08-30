package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/internal/promptrecord"
)

const maxQueuedUserInputs = 32

// UserTurnInput is an immutable TUI/headless input snapshot suitable for
// queueing while a turn is running.
type UserTurnInput struct {
	Display string
	Prompt  string
	Images  []UserImage
}

// QueuedUserInput is the public projection of a pending user queue command.
type QueuedUserInput struct {
	ID         string
	Display    string
	Prompt     string
	Images     []UserImage
	EnqueuedAt time.Time
	State      RuntimeItemState
}

// QueuedPromptPartKind is the presentation-safe part vocabulary exposed by
// pending rich input. It carries no dispatchable media identity or bytes.
type QueuedPromptPartKind string

const (
	QueuedPromptPartText         QueuedPromptPartKind = "text"
	QueuedPromptPartImage        QueuedPromptPartKind = "image"
	QueuedPromptPartResourceLink QueuedPromptPartKind = "resource_link"
	QueuedPromptPartEmbeddedText QueuedPromptPartKind = "embedded_text"
	QueuedPromptPartEmbeddedBlob QueuedPromptPartKind = "embedded_blob"
)

// QueuedPromptImageDescriptor is safe for queue list and restart projection.
// It deliberately excludes base64, local paths, media IDs, refs, and digests.
type QueuedPromptImageDescriptor struct {
	MIMEType  string
	SizeBytes int64
	Width     int
	Height    int
	Detail    PromptImageDetail
}

// QueuedPromptPart is one ordered, presentation-safe pending-input part.
type QueuedPromptPart struct {
	Kind     QueuedPromptPartKind
	Text     string
	MIMEType string
	Image    *QueuedPromptImageDescriptor
}

// QueuedPromptSnapshot is the non-dispatchable public projection of one
// pending prompt. Parts retain their exact logical order without exposing
// private media identity or embedded payload content.
type QueuedPromptSnapshot struct {
	ID          string
	Display     string
	Parts       []QueuedPromptPart
	EnqueuedAt  time.Time
	State       RuntimeItemState
	Unavailable bool
}

// QueuedPromptState binds one presentation-safe pending queue snapshot to the
// coordinator revision observed under the same lock. Entrypoint adapters use
// the revision to reject out-of-order transport projections.
type QueuedPromptState struct {
	Revision uint64
	Items    []QueuedPromptSnapshot
}

// QueuedPromptDraftImage is a detached, ephemeral queue-edit result. Data is
// copied from the session MediaStore and never aliases the durable record.
type QueuedPromptDraftImage struct {
	MIMEType string
	Data     []byte
	Detail   PromptImageDetail
}

// QueuedPromptDraftEmbeddedBlob is the editable form of one embedded raster.
// URI remains metadata only; Data is detached and cleared by the caller.
type QueuedPromptDraftEmbeddedBlob struct {
	URI         string
	MIMEType    string
	Data        []byte
	Detail      PromptImageDetail
	Annotations *PromptResourceAnnotations
}

// QueuedPromptDraftPart is one ordered queue-edit part.
type QueuedPromptDraftPart struct {
	Kind         QueuedPromptPartKind
	Text         string
	Image        *QueuedPromptDraftImage
	ResourceLink *PromptResourceLink
	EmbeddedText *PromptEmbeddedTextResource
	EmbeddedBlob *QueuedPromptDraftEmbeddedBlob
}

// QueuedPromptDraft is returned only after the exact still-pending item has
// been durably removed. Callers own and should clear returned image bytes.
type QueuedPromptDraft struct {
	ID      string
	Display string
	Parts   []QueuedPromptDraftPart
}

// EnqueuePromptInput admits one ordered prompt and commits one engine-owned
// pending item. Rich input has no inline or process-local fallback.
func (e *QueryEngine) EnqueuePromptInput(
	ctx context.Context,
	display string,
	input UntrustedPromptInput,
) (QueuedPromptSnapshot, error) {
	e.goalProviderBoundary.RLock()
	defer e.goalProviderBoundary.RUnlock()
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return QueuedPromptSnapshot{}, err
	}
	display = strings.TrimSpace(display)
	admitted, err := e.admitPromptInput(ctx, input, "")
	if err != nil {
		return QueuedPromptSnapshot{}, err
	}
	defer e.releaseAdmittedPrompt(admitted)
	prompt := strings.TrimSpace(admitted.textForHook())
	if !admitted.requiresDurablePrompt() {
		queued, enqueueErr := e.enqueueUserInputWithOwner(
			coordinator,
			scope,
			generateUUID(),
			UserTurnInput{Display: display, Prompt: prompt},
			false,
		)
		if enqueueErr != nil {
			return QueuedPromptSnapshot{}, enqueueErr
		}
		return queuedPromptSnapshotFromLegacy(queued), nil
	}
	if !coordinator.Durable() || coordinator.mediaStore == nil {
		return QueuedPromptSnapshot{}, fmt.Errorf(
			"durable rich-input queue is unavailable",
		)
	}
	if err := e.beginMediaLifecycleWrite(); err != nil {
		return QueuedPromptSnapshot{}, err
	}
	defer e.endMediaLifecycleWrite()
	if admitted.hasImages() {
		if err := e.checkAdmittedPromptRoute(
			admitted,
			admitted.binding.selectedModelSpec,
		); err != nil {
			return QueuedPromptSnapshot{}, err
		}
	}
	itemID := generateUUID()
	record, err := buildDurableRuntimePromptFromAdmitted(
		ctx,
		coordinator.mediaStore,
		generateUUID(),
		admitted,
	)
	if err != nil {
		return QueuedPromptSnapshot{}, err
	}
	if admitted.hasImages() {
		if err := e.checkAdmittedPromptRoute(
			admitted,
			admitted.binding.selectedModelSpec,
		); err != nil {
			return QueuedPromptSnapshot{}, err
		}
	}
	item := RuntimeItem{
		ID:         itemID,
		Kind:       RuntimeItemUserPrompt,
		Priority:   RuntimePriorityNext,
		Scope:      scope,
		IsMeta:     false,
		Origin:     "user",
		Provenance: "tui_busy_ordered_submit",
		UserPrompt: &RuntimeUserPrompt{Display: display},
	}
	accepted, err := coordinator.enqueueDurableUserPrompt(
		item,
		record,
		maxQueuedUserInputs,
	)
	if err != nil {
		return QueuedPromptSnapshot{}, err
	}
	return queuedPromptSnapshot(accepted)
}

// EnqueueUserInput adds a user input to the engine-owned durable coordinator.
// It never starts or interrupts a turn by itself.
func (e *QueryEngine) EnqueueUserInput(input UserTurnInput) (QueuedUserInput, error) {
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Display = strings.TrimSpace(input.Display)
	if input.Prompt == "" {
		return QueuedUserInput{}, fmt.Errorf("queued input is empty")
	}
	if input.Display == "" {
		input.Display = input.Prompt
	}
	if len(input.Images) > 0 {
		parts := make([]UntrustedPromptPart, 0, len(input.Images)+1)
		parts = append(parts, NewPromptTextPart(input.Prompt))
		for _, image := range input.Images {
			parts = append(parts, NewPromptImagePart(
				image.Base64Data,
				image.MIMEType,
				PromptImageDetailAuto,
			))
		}
		snapshot, err := e.EnqueuePromptInput(
			context.Background(),
			input.Display,
			NewUntrustedPromptInput(parts...),
		)
		if err != nil {
			return QueuedUserInput{}, err
		}
		images := append([]UserImage(nil), input.Images...)
		for index := range images {
			images[index].Name = ""
			images[index].Path = ""
		}
		return QueuedUserInput{
			ID:         snapshot.ID,
			Display:    snapshot.Display,
			Prompt:     input.Prompt,
			Images:     images,
			EnqueuedAt: snapshot.EnqueuedAt,
			State:      snapshot.State,
		}, nil
	}
	e.goalProviderBoundary.RLock()
	defer e.goalProviderBoundary.RUnlock()
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return QueuedUserInput{}, err
	}
	return e.enqueueUserInputWithOwner(coordinator, scope, generateUUID(), input, false)
}

// EnqueueUserInputWithID durably admits one text prompt under a caller-owned
// stable identity. Repeating the same identity and payload is idempotent;
// reusing the identity for different input fails closed. Entrypoint adapters
// use this only after validating their public request identity.
func (e *QueryEngine) EnqueueUserInputWithID(
	id string,
	input UserTurnInput,
) (QueuedUserInput, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return QueuedUserInput{}, fmt.Errorf("queued input ID is required")
	}
	if len(input.Images) > 0 {
		return QueuedUserInput{}, fmt.Errorf(
			"ID-bound queued rich input requires typed prompt admission",
		)
	}
	e.goalProviderBoundary.RLock()
	defer e.goalProviderBoundary.RUnlock()
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return QueuedUserInput{}, err
	}
	return e.enqueueUserInputWithOwner(coordinator, scope, id, input, true)
}

// HasQueuedUserInputAdmission reports whether one exact ID-bound text request
// was already accepted. It never creates or requeues work; a payload mismatch
// returns RuntimeInputConflictError.
func (e *QueryEngine) HasQueuedUserInputAdmission(
	id string,
	input UserTurnInput,
) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, fmt.Errorf("queued input ID is required")
	}
	if len(input.Images) > 0 {
		return false, fmt.Errorf(
			"ID-bound queued rich input requires typed prompt admission",
		)
	}
	e.goalProviderBoundary.RLock()
	defer e.goalProviderBoundary.RUnlock()
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return false, err
	}
	item, err := queuedUserRuntimeItem(scope, id, input)
	if err != nil {
		return false, err
	}
	return coordinator.HasAdmissionReceipt(item)
}

func (e *QueryEngine) enqueueUserInputWithOwner(
	coordinator *RuntimeInputCoordinator,
	scope RuntimeInputScope,
	itemID string,
	input UserTurnInput,
	retryStable bool,
) (QueuedUserInput, error) {
	item, err := queuedUserRuntimeItem(scope, itemID, input)
	if err != nil {
		return QueuedUserInput{}, err
	}
	if retryStable {
		item, err = coordinator.EnqueueBoundedWithAdmissionReceipt(
			item,
			maxQueuedUserInputs,
		)
	} else {
		item, err = coordinator.EnqueueBounded(item, maxQueuedUserInputs)
	}
	if err != nil {
		return QueuedUserInput{}, err
	}
	return queuedUserInput(item), nil
}

func queuedUserRuntimeItem(
	scope RuntimeInputScope,
	itemID string,
	input UserTurnInput,
) (RuntimeItem, error) {
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Display = strings.TrimSpace(input.Display)
	if input.Prompt == "" {
		return RuntimeItem{}, fmt.Errorf("queued input is empty")
	}
	if len(input.Images) > 0 {
		return RuntimeItem{}, fmt.Errorf(
			"queued rich input requires typed prompt admission",
		)
	}
	if input.Display == "" {
		input.Display = input.Prompt
	}
	return RuntimeItem{
		ID:         itemID,
		Kind:       RuntimeItemUserPrompt,
		Priority:   RuntimePriorityNext,
		Scope:      scope,
		IsMeta:     false,
		Origin:     "user",
		Provenance: "tui_busy_submit",
		UserPrompt: &RuntimeUserPrompt{
			Display: input.Display,
			Prompt:  input.Prompt,
		},
	}, nil
}

// QueuedPromptInputs returns ordered, presentation-safe pending prompt
// snapshots. It never materializes media bytes.
func (e *QueryEngine) QueuedPromptInputs() ([]QueuedPromptSnapshot, error) {
	state, err := e.QueuedPromptState()
	return state.Items, err
}

// QueuedPromptState returns the same pending prompt projection plus its
// atomic engine-owned coordinator revision. It never materializes media bytes.
func (e *QueryEngine) QueuedPromptState() (QueuedPromptState, error) {
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return QueuedPromptState{}, err
	}
	items, revision := coordinator.snapshotWithRevision(scope)
	result := make([]QueuedPromptSnapshot, 0, len(items))
	for _, item := range items {
		if item.Kind != RuntimeItemUserPrompt {
			continue
		}
		snapshot, snapshotErr := queuedPromptSnapshot(item)
		if snapshotErr != nil {
			snapshot = QueuedPromptSnapshot{
				ID:          item.ID,
				EnqueuedAt:  item.EnqueuedAt,
				State:       item.State,
				Unavailable: true,
			}
		}
		result = append(result, snapshot)
	}
	return QueuedPromptState{Revision: revision, Items: result}, nil
}

// QueuedUserInputs returns defensive snapshots of pending user inputs for this
// engine/thread.
func (e *QueryEngine) QueuedUserInputs() []QueuedUserInput {
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return nil
	}
	items := coordinator.Snapshot(scope)
	result := make([]QueuedUserInput, 0, len(items))
	for _, item := range items {
		if item.Kind != RuntimeItemUserPrompt {
			continue
		}
		if item.UserPrompt != nil && item.UserPrompt.durablePrompt != nil {
			materialized, materializeErr := coordinator.materializeRuntimePrompt(item)
			if materializeErr == nil {
				item = materialized
			}
		}
		result = append(result, queuedUserInput(item))
	}
	return result
}

// CancelQueuedPrompt durably removes one still-pending user prompt.
func (e *QueryEngine) CancelQueuedPrompt(id string) (bool, error) {
	if strings.TrimSpace(id) == "" {
		return false, nil
	}
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return false, err
	}
	for _, item := range coordinator.Snapshot(scope) {
		if item.ID != id || item.Kind != RuntimeItemUserPrompt {
			continue
		}
		return coordinator.Cancel(id)
	}
	return false, nil
}

// CancelQueuedUserInput removes one still-pending user input.
func (e *QueryEngine) CancelQueuedUserInput(id string) bool {
	cancelled, err := e.CancelQueuedPrompt(id)
	return err == nil && cancelled
}

// EditQueuedPrompt atomically materializes and durably removes one exact
// pending prompt. A claim, cancellation, validation failure, or persistence
// failure leaves the queue unchanged.
func (e *QueryEngine) EditQueuedPrompt(id string) (QueuedPromptDraft, bool, error) {
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return QueuedPromptDraft{}, false, err
	}
	if err := e.beginMediaLifecycleWrite(); err != nil {
		return QueuedPromptDraft{}, false, err
	}
	defer e.endMediaLifecycleWrite()
	return coordinator.editPendingUserPrompt(id, scope)
}

// ClaimNextQueuedUserInput transfers the next pending user input out of the
// coordinator for compatibility callers that own submission themselves.
//
// Deprecated: use ClaimNextRuntimeItem followed by SubmitRuntimeItem so
// transcript persistence remains the delivery-settlement boundary.
func (e *QueryEngine) ClaimNextQueuedUserInput() (QueuedUserInput, bool) {
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return QueuedUserInput{}, false
	}
	items := coordinator.Snapshot(scope)
	for _, item := range items {
		if item.Kind != RuntimeItemUserPrompt {
			continue
		}
		claimed, ok, err := coordinator.claimByID(item.ID)
		if err != nil || !ok {
			return QueuedUserInput{}, false
		}
		if err := coordinator.Settle(claimed.ID); err != nil {
			_, _ = coordinator.Release(claimed.ID)
			return QueuedUserInput{}, false
		}
		return queuedUserInput(claimed), true
	}
	return QueuedUserInput{}, false
}

// ClaimNextRuntimeItem atomically claims the next pending input for an idle
// transport. TUI uses this to auto-rewake; transports that cannot legally
// originate a prompt leave the item pending until their next inbound request.
func (e *QueryEngine) ClaimNextRuntimeItem() (RuntimeItem, bool, error) {
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return RuntimeItem{}, false, err
	}
	e.mu.Lock()
	checkpoint := e.projectGraphCheckpoint
	e.mu.Unlock()
	if checkpoint != nil {
		if active, waiting := checkpoint.ActiveInterrupt(); waiting {
			// A durable Graph interrupt owns the next invocation. Leave Agent
			// notifications, queued prompts, and hook wakeups pending until the
			// exact permission decision resumes or terminates that Graph run.
			for _, item := range coordinator.Snapshot(scope) {
				if item.Kind != RuntimeItemPermissionDecision ||
					item.PermissionDecision == nil {
					continue
				}
				if err := validateProjectGraphResumeDecision(
					active,
					*item.PermissionDecision,
				); err != nil {
					return RuntimeItem{}, false, err
				}
				return coordinator.claimByID(item.ID)
			}
			return RuntimeItem{}, false, nil
		}
	}
	item, ok, err := coordinator.ClaimNextIdle(scope)
	if err != nil || !ok {
		return item, ok, err
	}
	if err := e.preflightClaimedRuntimeItem(
		context.Background(),
		item,
	); err != nil {
		if _, releaseErr := coordinator.Release(item.ID); releaseErr != nil {
			return RuntimeItem{}, false, releaseErr
		}
		return RuntimeItem{}, false, err
	}
	return item, true, nil
}

// RuntimeItems returns detached pending inputs for the current runtime scope.
func (e *QueryEngine) RuntimeItems() []RuntimeItem {
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return nil
	}
	return coordinator.Snapshot(scope)
}

// SubscribeRuntimeItems exposes a coalesced idle-rewake signal.
func (e *QueryEngine) SubscribeRuntimeItems() <-chan struct{} {
	coordinator, _, err := e.runtimeInputOwner()
	if err != nil {
		return nil
	}
	return coordinator.Subscribe()
}

// SubscribeGoalContinuations exposes the dedicated interactive Goal wake
// channel without widening the generic runtime-item subscription.
func (e *QueryEngine) SubscribeGoalContinuations() <-chan struct{} {
	if !e.goalWorkflowEnabled() {
		return nil
	}
	coordinator, _, err := e.runtimeInputOwner()
	if err != nil {
		return nil
	}
	return coordinator.SubscribeGoalContinuation()
}

// ClaimNextGoalContinuation transfers only the exact active Goal cursor. It
// does not widen ClaimNextRuntimeItem or any generic transport contract.
func (e *QueryEngine) ClaimNextGoalContinuation() (RuntimeItem, bool, error) {
	if !e.goalWorkflowEnabled() {
		return RuntimeItem{}, false, fmt.Errorf(
			"goal continuation requires an enabled saved root TUI, Plain, dedicated headless Goal, or negotiated ACP session",
		)
	}
	e.goalControlMu.Lock()
	defer e.goalControlMu.Unlock()
	return e.claimGoalContinuation()
}

func (e *QueryEngine) runtimeInputOwner() (
	*RuntimeInputCoordinator,
	RuntimeInputScope,
	error,
) {
	if e == nil {
		return nil, RuntimeInputScope{}, fmt.Errorf("runtime input coordinator is unavailable")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	scope := RuntimeInputScope{
		SessionID: e.config.SessionID,
		ThreadID:  e.config.ThreadID,
		AgentID:   e.config.AgentID,
	}
	if e.inputCoordinatorErr != nil {
		return nil, scope, fmt.Errorf(
			"runtime input coordinator is unavailable: %w",
			e.inputCoordinatorErr,
		)
	}
	if e.inputCoordinator == nil {
		return nil, scope, fmt.Errorf("runtime input coordinator is unavailable")
	}
	return e.inputCoordinator, scope, nil
}

func (e *QueryEngine) runtimeInputScope() RuntimeInputScope {
	_, scope, _ := e.runtimeInputOwner()
	return scope
}

func queuedUserInput(item RuntimeItem) QueuedUserInput {
	if item.UserPrompt == nil {
		return QueuedUserInput{}
	}
	return QueuedUserInput{
		ID: item.ID, Display: item.UserPrompt.Display, Prompt: runtimeItemPrompt(item),
		Images:     runtimeItemUserImages(item),
		EnqueuedAt: item.EnqueuedAt, State: item.State,
	}
}

func queuedPromptSnapshot(item RuntimeItem) (QueuedPromptSnapshot, error) {
	if item.UserPrompt == nil {
		return QueuedPromptSnapshot{}, fmt.Errorf("queued prompt is unavailable")
	}
	snapshot := QueuedPromptSnapshot{
		ID:         item.ID,
		Display:    item.UserPrompt.Display,
		EnqueuedAt: item.EnqueuedAt,
		State:      item.State,
	}
	if item.UserPrompt.durablePrompt == nil {
		legacy := queuedUserInput(item)
		return queuedPromptSnapshotFromLegacy(legacy), nil
	}
	descriptor, err := item.UserPrompt.durablePrompt.Describe()
	if err != nil {
		return QueuedPromptSnapshot{}, err
	}
	snapshot.Parts = make([]QueuedPromptPart, 0, len(descriptor.Parts))
	for _, part := range descriptor.Parts {
		switch part.Kind {
		case promptrecord.PartText:
			snapshot.Parts = append(snapshot.Parts, QueuedPromptPart{
				Kind: QueuedPromptPartText,
				Text: part.Text,
			})
		case promptrecord.PartImage, promptrecord.PartEmbeddedBlob:
			if part.Image == nil {
				return QueuedPromptSnapshot{}, fmt.Errorf(
					"queued prompt image descriptor is unavailable",
				)
			}
			kind := QueuedPromptPartImage
			if part.Kind == promptrecord.PartEmbeddedBlob {
				kind = QueuedPromptPartEmbeddedBlob
			}
			snapshot.Parts = append(snapshot.Parts, QueuedPromptPart{
				Kind:     kind,
				MIMEType: part.MIMEType,
				Image: &QueuedPromptImageDescriptor{
					MIMEType:  part.Image.MIMEType,
					SizeBytes: part.Image.SizeBytes,
					Width:     part.Image.Width,
					Height:    part.Image.Height,
					Detail:    PromptImageDetail(part.Image.Detail),
				},
			})
		case promptrecord.PartResourceLink:
			snapshot.Parts = append(snapshot.Parts, QueuedPromptPart{
				Kind:     QueuedPromptPartResourceLink,
				MIMEType: part.MIMEType,
			})
		case promptrecord.PartEmbeddedText:
			snapshot.Parts = append(snapshot.Parts, QueuedPromptPart{
				Kind:     QueuedPromptPartEmbeddedText,
				MIMEType: part.MIMEType,
			})
		default:
			return QueuedPromptSnapshot{}, fmt.Errorf(
				"queued prompt descriptor has invalid part",
			)
		}
	}
	return snapshot, nil
}

func queuedPromptSnapshotFromLegacy(input QueuedUserInput) QueuedPromptSnapshot {
	snapshot := QueuedPromptSnapshot{
		ID:         input.ID,
		Display:    input.Display,
		EnqueuedAt: input.EnqueuedAt,
		State:      input.State,
	}
	if input.Prompt != "" {
		snapshot.Parts = append(snapshot.Parts, QueuedPromptPart{
			Kind: QueuedPromptPartText,
			Text: input.Prompt,
		})
	}
	for _, image := range input.Images {
		snapshot.Parts = append(snapshot.Parts, QueuedPromptPart{
			Kind: QueuedPromptPartImage,
			Image: &QueuedPromptImageDescriptor{
				MIMEType: image.MIMEType,
				Detail:   PromptImageDetailAuto,
			},
		})
	}
	return snapshot
}
