package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

const (
	runtimeInputEnvelopeLegacyVersion    = 1
	runtimeInputEnvelopeVersion          = 2
	runtimeItemVersion                   = 1
	runtimeInputAdmissionReceiptVersion  = 1
	runtimeInputAdmissionDigestAlgorithm = "sha256-v1"
	runtimeGoalContinuationLegacyVersion = 1
	runtimeGoalContinuationVersion       = 2
	runtimeItemRejectionVersion          = 1
	runtimeInlineImagesDecodeOnly        = "runtime input coordinator: inline user images are decode-only; use EnqueuePromptInput"
)

// RuntimeInputPriority is the explicit scheduling priority for a RuntimeItem.
// Lower ordinal values are claimed first; FIFO is preserved within one
// priority. Presentation metadata never changes scheduling order.
type RuntimeInputPriority string

const (
	RuntimePriorityNow   RuntimeInputPriority = "now"
	RuntimePriorityNext  RuntimeInputPriority = "next"
	RuntimePriorityLater RuntimeInputPriority = "later"
)

// RuntimeItemKind identifies one checkpoint-safe input variant.
type RuntimeItemKind string

const (
	RuntimeItemUserPrompt         RuntimeItemKind = "user_prompt"
	RuntimeItemSteering           RuntimeItemKind = "steering"
	RuntimeItemAgentMessage       RuntimeItemKind = "agent_message"
	RuntimeItemAgentNotification  RuntimeItemKind = "agent_notification"
	RuntimeItemAsyncRewake        RuntimeItemKind = "async_rewake"
	RuntimeItemPermissionDecision RuntimeItemKind = "permission_decision"
	RuntimeItemGoalContinuation   RuntimeItemKind = "goal_continuation"
	RuntimeItemStop               RuntimeItemKind = "stop"
)

// RuntimeItemState is the durable coordinator lifecycle for one item.
type RuntimeItemState string

const (
	RuntimeItemPending    RuntimeItemState = "pending"
	RuntimeItemProcessing RuntimeItemState = "processing"
	RuntimeItemRejected   RuntimeItemState = "rejected"
)

// RuntimeInputConflictError reports reuse of one durable runtime-item identity
// with a different payload. Callers must not retry the conflicting payload
// under a fresh identity automatically because the original request may have
// crossed the delivery boundary already.
type RuntimeInputConflictError struct {
	ID string
}

func (e *RuntimeInputConflictError) Error() string {
	if e == nil || strings.TrimSpace(e.ID) == "" {
		return "runtime input coordinator: item identity conflicts with an existing payload"
	}
	return fmt.Sprintf(
		"runtime input coordinator: item ID %q conflicts with an existing payload",
		e.ID,
	)
}

// RuntimeStopMode distinguishes a safe-boundary stop from an immediate
// cancellation request. Immediate still relies on the active model/tool
// cancellation path and does not claim unsupported preemption.
type RuntimeStopMode string

const (
	RuntimeStopGraceful  RuntimeStopMode = "graceful"
	RuntimeStopImmediate RuntimeStopMode = "immediate"
)

// RuntimeInputScope routes one item to the main session or one child Agent.
type RuntimeInputScope struct {
	SessionID string `json:"session_id"`
	ThreadID  string `json:"thread_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
}

// RuntimeUserPrompt is the durable user-input variant.
type RuntimeUserPrompt struct {
	Display string      `json:"display,omitempty"`
	Prompt  string      `json:"prompt"`
	Images  []UserImage `json:"images,omitempty"`

	durablePrompt     *promptrecord.Record
	materializedInput *UntrustedPromptInput
	writer            *runtimePromptWriter
	writerItemID      string
}

// RuntimeAgentMessage is the durable SendMessage variant.
type RuntimeAgentMessage struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Content string `json:"content"`
}

// RuntimeAgentNotification is the durable child-terminal notification variant.
type RuntimeAgentNotification struct {
	ReceiptVersion   int    `json:"receipt_version"`
	CompletionID     string `json:"completion_id"`
	AgentID          string `json:"agent_id"`
	ToolUseID        string `json:"tool_use_id,omitempty"`
	Status           string `json:"status"`
	Description      string `json:"description,omitempty"`
	OutputFile       string `json:"output_file,omitempty"`
	Message          string `json:"message"`
	Generation       int64  `json:"generation"`
	TerminalSequence uint64 `json:"terminal_sequence"`
}

// RuntimeAsyncRewake is the durable async-hook wakeup variant.
type RuntimeAsyncRewake struct {
	HookID      string `json:"hook_id"`
	Event       string `json:"event,omitempty"`
	HookName    string `json:"hook_name,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	ModelPrompt string `json:"model_prompt"`
}

// RuntimePermissionDecision is a user intent targeted at one exact persisted
// ProjectGraph interrupt. The decision is never approval authority by itself:
// resume re-evaluates the current invocation and policy before committing it.
type RuntimePermissionDecision struct {
	Version            int                          `json:"version"`
	RequestID          string                       `json:"request_id"`
	InterruptID        string                       `json:"interrupt_id"`
	InvocationDigest   string                       `json:"invocation_digest"`
	PolicyRevision     string                       `json:"policy_revision"`
	DecisionConstraint PermissionDecisionConstraint `json:"decision_constraint,omitempty"`
	Result             PermissionInteractionResult  `json:"result"`
}

// RuntimeGoalContinuation is the immutable delivery claim for one exact
// terminal-derived Goal cursor. It is dormant until an engine-owned consumer
// explicitly claims it.
type RuntimeGoalContinuation struct {
	Version                     int       `json:"version"`
	CheckpointID                string    `json:"checkpoint_id"`
	ContinuationTurnID          string    `json:"continuation_turn_id"`
	GoalID                      string    `json:"goal_id"`
	GoalSchemaVersion           uint16    `json:"goal_schema_version"`
	ObjectiveRevision           uint64    `json:"objective_revision"`
	GoalRevision                uint64    `json:"goal_revision"`
	GoalStatus                  string    `json:"goal_status"`
	RootSessionID               string    `json:"root_session_id"`
	RootThreadID                string    `json:"root_thread_id"`
	RootAgentID                 string    `json:"root_agent_id,omitempty"`
	PredecessorGoalTurnID       string    `json:"predecessor_goal_turn_id"`
	PredecessorTerminalSequence uint64    `json:"predecessor_terminal_sequence"`
	PredecessorTerminalReason   string    `json:"predecessor_terminal_reason"`
	PredecessorTerminalAt       time.Time `json:"predecessor_terminal_at"`
	TokenBudget                 *uint64   `json:"token_budget,omitempty"`
	TokensUsed                  uint64    `json:"tokens_used"`
	UsageLedgerRevision         uint64    `json:"usage_ledger_revision"`
	ContinuationOrdinal         uint64    `json:"continuation_ordinal"`
	RuntimeRevision             uint64    `json:"runtime_revision"`
}

// RuntimeItemRejection is a typed durable disposition written before a
// permanently rejected Goal continuation is settled from the coordinator.
type RuntimeItemRejection struct {
	Version    int       `json:"version"`
	Code       string    `json:"code"`
	RejectedAt time.Time `json:"rejected_at"`
}

// RuntimeStop is the durable control variant.
type RuntimeStop struct {
	Mode   RuntimeStopMode `json:"mode"`
	Reason string          `json:"reason,omitempty"`
}

// RuntimeItem is plain, versioned invocation data. Exactly one payload matching
// Kind is populated. It may be persisted by the coordinator or copied into a
// Graph checkpoint; it never contains contexts, channels, mutexes, callbacks,
// models, registries, or tool owners.
type RuntimeItem struct {
	Version    int                  `json:"version"`
	ID         string               `json:"id"`
	Kind       RuntimeItemKind      `json:"kind"`
	Priority   RuntimeInputPriority `json:"priority"`
	Scope      RuntimeInputScope    `json:"scope"`
	Sequence   uint64               `json:"sequence"`
	EnqueuedAt time.Time            `json:"enqueued_at"`
	State      RuntimeItemState     `json:"state"`
	IsMeta     bool                 `json:"is_meta,omitempty"`
	Origin     string               `json:"origin,omitempty"`
	Provenance string               `json:"provenance,omitempty"`

	UserPrompt         *RuntimeUserPrompt         `json:"user_prompt,omitempty"`
	AgentMessage       *RuntimeAgentMessage       `json:"agent_message,omitempty"`
	AgentNotification  *RuntimeAgentNotification  `json:"agent_notification,omitempty"`
	AsyncRewake        *RuntimeAsyncRewake        `json:"async_rewake,omitempty"`
	PermissionDecision *RuntimePermissionDecision `json:"permission_decision,omitempty"`
	GoalContinuation   *RuntimeGoalContinuation   `json:"goal_continuation,omitempty"`
	Stop               *RuntimeStop               `json:"stop,omitempty"`
	Rejection          *RuntimeItemRejection      `json:"rejection,omitempty"`
}

// RuntimeInputCoordinatorConfig defines one session-scoped coordinator.
// Path may be empty for an invocation-local coordinator used by direct Query.
type RuntimeInputCoordinatorConfig struct {
	SessionID      string
	ThreadID       string
	AgentID        string
	Path           string
	Clock          func() time.Time
	DeliveryLookup func([]string) (map[string]struct{}, error)
	// DeferRecoveryPersistence keeps crash recovery in memory until the
	// restore-staging owner commits.
	DeferRecoveryPersistence bool
	mediaStore               *mediastore.Store
}

type runtimeInputEnvelope struct {
	Version           int                            `json:"version"`
	Revision          uint64                         `json:"revision"`
	NextSequence      uint64                         `json:"next_sequence"`
	Items             []RuntimeItem                  `json:"items"`
	AdmissionReceipts []runtimeInputAdmissionReceipt `json:"admission_receipts,omitempty"`
}

// runtimeInputAdmissionReceipt is a durable acknowledgement tombstone for one
// retry-stable caller identity. It retains only a versioned payload digest and
// original ordering metadata; the prompt remains owned by the runtime ledger
// while pending and by the transcript after delivery.
type runtimeInputAdmissionReceipt struct {
	Version         int               `json:"version"`
	ID              string            `json:"id"`
	Kind            RuntimeItemKind   `json:"kind"`
	Scope           RuntimeInputScope `json:"scope"`
	DigestAlgorithm string            `json:"digest_algorithm"`
	PayloadDigest   string            `json:"payload_digest"`
	Sequence        uint64            `json:"sequence"`
	EnqueuedAt      time.Time         `json:"enqueued_at"`
}

// RuntimeInputCoordinator is the single live buffer around query-kernel runs.
// File state is committed before memory state, so callers never observe an
// enqueue, claim, cancellation, or settlement that was not durably accepted.
type RuntimeInputCoordinator struct {
	mu                sync.Mutex
	scope             RuntimeInputScope
	path              string
	clock             func() time.Time
	revision          uint64
	nextSequence      uint64
	items             []RuntimeItem
	admissionReceipts map[string]runtimeInputAdmissionReceipt
	delivered         map[string]struct{}
	deliveryLookup    func([]string) (map[string]struct{}, error)
	mediaStore        *mediastore.Store
	promptWriter      *runtimePromptWriter
	submitting        map[string]struct{}
	recoveryPending   bool
	notify            chan struct{}
	goalNotify        chan struct{}
}

// NewRuntimeInputCoordinator constructs and recovers one coordinator. Delivered
// IDs come from the authoritative transcript. Processing items without a
// transcript delivery are restored to pending; stale stop requests never
// control a new process.
func NewRuntimeInputCoordinator(
	config RuntimeInputCoordinatorConfig,
	deliveredIDs map[string]struct{},
) (*RuntimeInputCoordinator, error) {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	coordinator := &RuntimeInputCoordinator{
		scope: RuntimeInputScope{
			SessionID: strings.TrimSpace(config.SessionID),
			ThreadID:  strings.TrimSpace(config.ThreadID),
			AgentID:   strings.TrimSpace(config.AgentID),
		},
		path:              strings.TrimSpace(config.Path),
		clock:             clock,
		admissionReceipts: make(map[string]runtimeInputAdmissionReceipt),
		delivered:         make(map[string]struct{}, len(deliveredIDs)),
		deliveryLookup:    config.DeliveryLookup,
		mediaStore:        config.mediaStore,
		promptWriter:      &runtimePromptWriter{},
		submitting:        make(map[string]struct{}),
		notify:            make(chan struct{}, 1),
		goalNotify:        make(chan struct{}, 1),
	}
	for id := range deliveredIDs {
		coordinator.delivered[id] = struct{}{}
	}
	if coordinator.path == "" {
		return coordinator, nil
	}
	envelope, err := loadRuntimeInputEnvelope(coordinator.path)
	if err != nil {
		return nil, err
	}
	coordinator.revision = envelope.Revision
	coordinator.nextSequence = envelope.NextSequence
	coordinator.items = cloneRuntimeItems(envelope.Items)
	receiptSequences := make(map[uint64]string, len(envelope.AdmissionReceipts))
	for _, receipt := range envelope.AdmissionReceipts {
		if err := validateRuntimeInputAdmissionReceipt(receipt); err != nil {
			return nil, fmt.Errorf(
				"runtime input coordinator: recover admission receipt %q: %w",
				receipt.ID,
				err,
			)
		}
		if !runtimeScopesEqual(receipt.Scope, coordinator.scope) {
			return nil, fmt.Errorf(
				"runtime input coordinator: admission receipt %q has invalid scope",
				receipt.ID,
			)
		}
		if receipt.Sequence > coordinator.nextSequence {
			return nil, fmt.Errorf(
				"runtime input coordinator: admission receipt %q exceeds next sequence",
				receipt.ID,
			)
		}
		if previousID, duplicate := receiptSequences[receipt.Sequence]; duplicate {
			return nil, fmt.Errorf(
				"runtime input coordinator: admission receipts %q and %q share sequence %d",
				previousID,
				receipt.ID,
				receipt.Sequence,
			)
		}
		if _, duplicate := coordinator.admissionReceipts[receipt.ID]; duplicate {
			return nil, fmt.Errorf(
				"runtime input coordinator: duplicate admission receipt %q",
				receipt.ID,
			)
		}
		receiptSequences[receipt.Sequence] = receipt.ID
		coordinator.admissionReceipts[receipt.ID] = receipt
	}
	ledgerIDs := make([]string, 0, len(coordinator.items))
	for _, item := range coordinator.items {
		ledgerIDs = append(ledgerIDs, item.ID)
	}
	covered, err := coordinator.resolveDelivered(ledgerIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"runtime input coordinator: resolve transcript delivery: %w",
			err,
		)
	}

	recovered := make([]RuntimeItem, 0, len(coordinator.items))
	changed := envelope.Version != runtimeInputEnvelopeVersion
	for _, item := range coordinator.items {
		if item.UserPrompt != nil && item.UserPrompt.durablePrompt != nil {
			item.UserPrompt.writer = coordinator.promptWriter
			item.UserPrompt.writerItemID = item.ID
		}
		if normalizeRuntimeItemPayload(&item) {
			changed = true
		}
		if err := coordinator.validateItem(item); err != nil {
			return nil, fmt.Errorf("runtime input coordinator: recover item %q: %w", item.ID, err)
		}
		if receipt, exists := coordinator.admissionReceipts[item.ID]; exists {
			matches, receiptErr := runtimeInputAdmissionReceiptMatches(receipt, item)
			if receiptErr != nil || !matches ||
				receipt.Sequence != item.Sequence ||
				!receipt.EnqueuedAt.Equal(item.EnqueuedAt) {
				return nil, fmt.Errorf(
					"runtime input coordinator: item %q does not match its admission receipt",
					item.ID,
				)
			}
		} else {
			if receipt, receiptErr := runtimeInputAdmissionReceiptFor(item); receiptErr == nil {
				coordinator.admissionReceipts[item.ID] = receipt
				changed = true
			}
		}
		if item.State == RuntimeItemRejected {
			changed = true
			continue
		}
		if _, delivered := covered[item.ID]; delivered {
			changed = true
			continue
		}
		if item.Kind == RuntimeItemStop {
			changed = true
			continue
		}
		if item.State == RuntimeItemProcessing {
			item.State = RuntimeItemPending
			changed = true
		}
		recovered = append(recovered, item)
	}
	coordinator.items = recovered
	if changed {
		if config.DeferRecoveryPersistence {
			coordinator.recoveryPending = true
		} else {
			if err := coordinator.persistLocked(
				coordinator.items,
				coordinator.nextSequence,
				coordinator.revision+1,
			); err != nil {
				return nil, fmt.Errorf("runtime input coordinator: persist recovery: %w", err)
			}
			coordinator.revision++
		}
	}
	if hasTransportEligiblePending(coordinator.items) {
		coordinator.signal()
	}
	if hasPendingGoalContinuation(coordinator.items) {
		coordinator.signalGoal()
	}
	return coordinator, nil
}

func (c *RuntimeInputCoordinator) commitDeferredRecovery() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.recoveryPending {
		return nil
	}
	if err := c.persistLocked(
		c.items,
		c.nextSequence,
		c.revision+1,
	); err != nil {
		return fmt.Errorf("runtime input coordinator: persist recovery: %w", err)
	}
	c.revision++
	c.recoveryPending = false
	return nil
}

// RuntimeInputPersistencePath returns the session-scoped ledger path.
func RuntimeInputPersistencePath(transcriptPath string) string {
	transcriptPath = strings.TrimSpace(transcriptPath)
	if transcriptPath == "" {
		return ""
	}
	return transcriptPath + ".runtime-inputs.json"
}

// Enqueue atomically accepts one item. Repeating the same ID and payload is
// idempotent; reusing an ID for a different payload fails closed.
func (c *RuntimeInputCoordinator) Enqueue(item RuntimeItem) (RuntimeItem, error) {
	items, err := c.EnqueueBatch([]RuntimeItem{item})
	if err != nil {
		return RuntimeItem{}, err
	}
	if len(items) != 1 {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator: enqueue returned %d items", len(items))
	}
	return items[0], nil
}

// EnqueueBounded atomically accepts one item only when fewer than maxPending
// pending items of the same kind and scope already exist. Idempotent retries do
// not consume additional capacity.
func (c *RuntimeInputCoordinator) EnqueueBounded(
	item RuntimeItem,
	maxPending int,
) (RuntimeItem, error) {
	if c == nil {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator is unavailable")
	}
	if maxPending <= 0 {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator: max pending must be positive")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	items, err := c.enqueueBatchLocked(
		[]RuntimeItem{item},
		item.Kind,
		maxPending,
		true,
		false,
	)
	if err != nil {
		return RuntimeItem{}, err
	}
	if len(items) != 1 {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator: enqueue returned %d items", len(items))
	}
	return items[0], nil
}

// EnqueueBoundedWithAdmissionReceipt atomically persists one plain text user
// prompt and its retry acknowledgement. The receipt outlives cancellation and
// settlement so a lost transport ACK cannot create duplicate work.
func (c *RuntimeInputCoordinator) EnqueueBoundedWithAdmissionReceipt(
	item RuntimeItem,
	maxPending int,
) (RuntimeItem, error) {
	if c == nil {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator is unavailable")
	}
	if maxPending <= 0 {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator: max pending must be positive")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	items, err := c.enqueueBatchLocked(
		[]RuntimeItem{item},
		RuntimeItemUserPrompt,
		maxPending,
		true,
		true,
	)
	if err != nil {
		return RuntimeItem{}, err
	}
	if len(items) != 1 {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator: enqueue returned %d items", len(items))
	}
	return items[0], nil
}

// HasAdmissionReceipt reports whether one exact retry-stable plain text item
// was previously accepted. Payload reuse under the same identity fails closed.
func (c *RuntimeInputCoordinator) HasAdmissionReceipt(
	item RuntimeItem,
) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("runtime input coordinator is unavailable")
	}
	item = cloneRuntimeItem(item)
	item.ID = strings.TrimSpace(item.ID)
	normalizeRuntimeItemPayload(&item)
	item.Version = runtimeItemVersion
	item.Priority = normalizedRuntimePriority(item.Priority)
	item.Scope = c.normalizeScope(item.Scope)
	c.mu.Lock()
	defer c.mu.Unlock()
	receipt, ok := c.admissionReceipts[item.ID]
	if !ok {
		return false, nil
	}
	matches, err := runtimeInputAdmissionReceiptMatches(receipt, item)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, &RuntimeInputConflictError{ID: item.ID}
	}
	return true, nil
}

// enqueueDormantGoalContinuation durably accepts one Goal continuation without
// publishing the generic transport wake used by current TUI/plain/ACP
// consumers. P24.4 owns the first production consumer.
func (c *RuntimeInputCoordinator) enqueueDormantGoalContinuation(
	item RuntimeItem,
) (RuntimeItem, error) {
	if c == nil {
		return RuntimeItem{}, fmt.Errorf("runtime input coordinator is unavailable")
	}
	if item.Kind != RuntimeItemGoalContinuation {
		return RuntimeItem{}, fmt.Errorf(
			"runtime input coordinator: dormant enqueue requires a Goal continuation",
		)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	items, err := c.enqueueBatchLocked(
		[]RuntimeItem{item},
		RuntimeItemGoalContinuation,
		1,
		false,
		false,
	)
	if err != nil {
		return RuntimeItem{}, err
	}
	if len(items) != 1 {
		return RuntimeItem{}, fmt.Errorf(
			"runtime input coordinator: dormant enqueue returned %d items",
			len(items),
		)
	}
	c.signalGoal()
	return items[0], nil
}

// EnqueueBatch atomically accepts a FIFO batch, used for transactional transfer
// from child-message and Agent-notification outboxes.
func (c *RuntimeInputCoordinator) EnqueueBatch(items []RuntimeItem) ([]RuntimeItem, error) {
	if c == nil {
		return nil, fmt.Errorf("runtime input coordinator is unavailable")
	}
	if len(items) == 0 {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enqueueBatchLocked(items, "", 0, true, false)
}

func (c *RuntimeInputCoordinator) enqueueBatchLocked(
	items []RuntimeItem,
	boundedKind RuntimeItemKind,
	maxPending int,
	publishSignal bool,
	recordAdmission bool,
) ([]RuntimeItem, error) {
	candidate := cloneRuntimeItems(c.items)
	candidateReceipts := cloneRuntimeInputAdmissionReceipts(c.admissionReceipts)
	nextSequence := c.nextSequence
	accepted := make([]RuntimeItem, 0, len(items))
	changed := false
	for _, item := range items {
		item = cloneRuntimeItem(item)
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			return nil, fmt.Errorf("runtime input coordinator: item ID is required")
		}
		normalizeRuntimeItemPayload(&item)
		if item.UserPrompt != nil && len(item.UserPrompt.Images) > 0 {
			return nil, errors.New(runtimeInlineImagesDecodeOnly)
		}
		item.Version = runtimeItemVersion
		item.Priority = normalizedRuntimePriority(item.Priority)
		item.Scope = c.normalizeScope(item.Scope)
		if recordAdmission {
			if receipt, ok := candidateReceipts[item.ID]; ok {
				matches, err := runtimeInputAdmissionReceiptMatches(receipt, item)
				if err != nil {
					return nil, err
				}
				if !matches {
					return nil, &RuntimeInputConflictError{ID: item.ID}
				}
				if existing, exists := runtimeItemByID(candidate, item.ID); exists {
					normalized := item
					normalized.Version = existing.Version
					normalized.Sequence = existing.Sequence
					normalized.EnqueuedAt = existing.EnqueuedAt
					normalized.State = existing.State
					if !runtimeItemsEqual(existing, normalized) {
						return nil, &RuntimeInputConflictError{ID: item.ID}
					}
					accepted = append(accepted, cloneRuntimeItem(existing))
					continue
				}
				item.Sequence = receipt.Sequence
				item.EnqueuedAt = receipt.EnqueuedAt
				item.State = RuntimeItemProcessing
				if err := c.validateItem(item); err != nil {
					return nil, err
				}
				accepted = append(accepted, item)
				continue
			}
		}
		if _, delivered := c.delivered[item.ID]; delivered {
			if recordAdmission {
				return nil, &RuntimeInputConflictError{ID: item.ID}
			}
			if err := validateRuntimeItemUserImages(item); err != nil {
				return nil, err
			}
			item.State = RuntimeItemProcessing
			accepted = append(accepted, item)
			continue
		}
		if existing, ok := runtimeItemByID(candidate, item.ID); ok {
			normalized := item
			normalized.Version = existing.Version
			normalized.Sequence = existing.Sequence
			normalized.EnqueuedAt = existing.EnqueuedAt
			normalized.State = existing.State
			if !runtimeItemsEqual(existing, normalized) {
				return nil, &RuntimeInputConflictError{ID: item.ID}
			}
			if recordAdmission {
				receipt, err := runtimeInputAdmissionReceiptFor(existing)
				if err != nil {
					return nil, err
				}
				candidateReceipts[item.ID] = receipt
				changed = true
			}
			accepted = append(accepted, cloneRuntimeItem(existing))
			continue
		}
		if boundedKind != "" && item.Kind == boundedKind {
			pending := 0
			for _, candidateItem := range candidate {
				if candidateItem.State == RuntimeItemPending &&
					candidateItem.Kind == boundedKind &&
					runtimeScopesEqual(candidateItem.Scope, item.Scope) {
					pending++
				}
			}
			if pending >= maxPending {
				return nil, fmt.Errorf(
					"runtime input coordinator is full (maximum %d %s items)",
					maxPending,
					boundedKind,
				)
			}
		}
		nextSequence++
		item.Sequence = nextSequence
		if item.EnqueuedAt.IsZero() {
			item.EnqueuedAt = c.clock().UTC()
		}
		item.State = RuntimeItemPending
		if err := c.validateItem(item); err != nil {
			return nil, err
		}
		if recordAdmission {
			receipt, err := runtimeInputAdmissionReceiptFor(item)
			if err != nil {
				return nil, err
			}
			candidateReceipts[item.ID] = receipt
		}
		candidate = append(candidate, item)
		accepted = append(accepted, cloneRuntimeItem(item))
		changed = true
	}
	if !changed {
		return accepted, nil
	}
	revision := c.revision + 1
	if err := c.persistStateLocked(
		candidate,
		nextSequence,
		revision,
		candidateReceipts,
	); err != nil {
		return nil, err
	}
	c.items = candidate
	c.admissionReceipts = candidateReceipts
	c.nextSequence = nextSequence
	c.revision = revision
	if publishSignal && hasTransportEligiblePending(candidate) {
		c.signal()
	}
	return accepted, nil
}

func normalizeRuntimeItemPayload(item *RuntimeItem) bool {
	if item == nil {
		return false
	}
	changed := false
	if (item.Kind == RuntimeItemUserPrompt ||
		item.Kind == RuntimeItemSteering) &&
		item.UserPrompt != nil {
		for index := range item.UserPrompt.Images {
			if item.UserPrompt.Images[index].Name != "" ||
				item.UserPrompt.Images[index].Path != "" {
				changed = true
			}
			item.UserPrompt.Images[index].Name = ""
			item.UserPrompt.Images[index].Path = ""
		}
	}
	if item.Kind != RuntimeItemAgentNotification ||
		item.AgentNotification == nil {
		return changed
	}
	notification := item.AgentNotification
	if notification.ReceiptVersion == 0 {
		notification.ReceiptVersion = transcript.AgentCompletionReceiptVersion
	}
	if strings.TrimSpace(notification.CompletionID) == "" {
		notification.CompletionID = strings.TrimSpace(item.ID)
	}
	if notification.Generation <= 0 {
		notification.Generation = 1
	}
	if notification.TerminalSequence == 0 {
		notification.TerminalSequence = uint64(notification.Generation)
	}
	return changed
}

// ClaimSafePoint claims every eligible item for one query safe point. User
// prompts are excluded at prepare boundaries to preserve queued-turn behavior;
// includeUser is enabled after a completed tool round. allowLater is enabled
// only by the existing Sleep boundary.
func (c *RuntimeInputCoordinator) ClaimSafePoint(
	scope RuntimeInputScope,
	includeUser bool,
	allowLater bool,
) ([]RuntimeItem, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	indexes := make([]int, 0)
	for index, item := range c.items {
		if item.State != RuntimeItemPending ||
			item.Kind == RuntimeItemStop ||
			item.Kind == RuntimeItemGoalContinuation ||
			!runtimeScopesEqual(item.Scope, c.normalizeScope(scope)) {
			continue
		}
		if item.Kind == RuntimeItemUserPrompt && !includeUser {
			continue
		}
		if item.Priority == RuntimePriorityLater && !allowLater {
			continue
		}
		indexes = append(indexes, index)
	}
	if len(indexes) == 0 {
		return nil, nil
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		left, right := c.items[indexes[i]], c.items[indexes[j]]
		if runtimePriorityOrdinal(left.Priority) != runtimePriorityOrdinal(right.Priority) {
			return runtimePriorityOrdinal(left.Priority) < runtimePriorityOrdinal(right.Priority)
		}
		return left.Sequence < right.Sequence
	})
	candidate := cloneRuntimeItems(c.items)
	claimed := make([]RuntimeItem, 0, len(indexes))
	for _, index := range indexes {
		candidate[index].State = RuntimeItemProcessing
		materialized, err := c.materializeRuntimePrompt(candidate[index])
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, materialized)
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return nil, err
	}
	c.items = candidate
	c.revision = revision
	return claimed, nil
}

// ClaimNextIdle claims one item for a fresh runtime invocation.
func (c *RuntimeInputCoordinator) ClaimNextIdle(scope RuntimeInputScope) (RuntimeItem, bool, error) {
	if c == nil {
		return RuntimeItem{}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	normalizedScope := c.normalizeScope(scope)
	best := -1
	for index, item := range c.items {
		if item.State != RuntimeItemPending ||
			item.Kind == RuntimeItemStop ||
			item.Kind == RuntimeItemGoalContinuation ||
			!runtimeScopesEqual(item.Scope, normalizedScope) {
			continue
		}
		if best < 0 ||
			runtimePriorityOrdinal(item.Priority) < runtimePriorityOrdinal(c.items[best].Priority) ||
			(runtimePriorityOrdinal(item.Priority) == runtimePriorityOrdinal(c.items[best].Priority) &&
				item.Sequence < c.items[best].Sequence) {
			best = index
		}
	}
	if best < 0 {
		return RuntimeItem{}, false, nil
	}
	candidate := cloneRuntimeItems(c.items)
	candidate[best].State = RuntimeItemProcessing
	materialized, err := c.materializeRuntimePrompt(candidate[best])
	if err != nil {
		return RuntimeItem{}, false, err
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return RuntimeItem{}, false, err
	}
	c.items = candidate
	c.revision = revision
	return materialized, true, nil
}

func (c *RuntimeInputCoordinator) claimGoalContinuation(
	id string,
	scope RuntimeInputScope,
) (RuntimeItem, bool, error) {
	if c == nil || strings.TrimSpace(id) == "" {
		return RuntimeItem{}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	normalizedScope := c.normalizeScope(scope)
	index := -1
	for candidateIndex, item := range c.items {
		if item.State == RuntimeItemPending &&
			item.ID == strings.TrimSpace(id) &&
			item.Kind == RuntimeItemGoalContinuation &&
			runtimeScopesEqual(item.Scope, normalizedScope) {
			index = candidateIndex
			continue
		}
		if item.State == RuntimeItemPending &&
			runtimeScopesEqual(item.Scope, normalizedScope) &&
			(item.Kind == RuntimeItemUserPrompt ||
				item.Kind == RuntimeItemSteering) {
			return RuntimeItem{}, false, nil
		}
	}
	if index < 0 {
		return RuntimeItem{}, false, nil
	}
	candidate := cloneRuntimeItems(c.items)
	candidate[index].State = RuntimeItemProcessing
	materialized, err := c.materializeRuntimePrompt(candidate[index])
	if err != nil {
		return RuntimeItem{}, false, err
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return RuntimeItem{}, false, err
	}
	c.items = candidate
	c.revision = revision
	return materialized, true, nil
}

func (c *RuntimeInputCoordinator) withGoalContinuationAdmission(
	id string,
	scope RuntimeInputScope,
	admit func(RuntimeItem, uint64) error,
) error {
	if c == nil || admit == nil {
		return fmt.Errorf("runtime input coordinator: Goal admission is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	normalizedScope := c.normalizeScope(scope)
	var selected *RuntimeItem
	for index := range c.items {
		item := c.items[index]
		if item.ID == strings.TrimSpace(id) &&
			item.Kind == RuntimeItemGoalContinuation &&
			item.State == RuntimeItemProcessing &&
			runtimeScopesEqual(item.Scope, normalizedScope) {
			cloned := cloneRuntimeItem(item)
			selected = &cloned
			continue
		}
		if item.State == RuntimeItemPending &&
			runtimeScopesEqual(item.Scope, normalizedScope) &&
			(item.Kind == RuntimeItemUserPrompt ||
				item.Kind == RuntimeItemSteering) {
			return fmt.Errorf(
				"%w: explicit user input is pending",
				errGoalContinuationPermanentlyRejected,
			)
		}
	}
	if selected == nil {
		return fmt.Errorf(
			"goal continuation %q is not the claimed processing item",
			strings.TrimSpace(id),
		)
	}
	return admit(*selected, c.revision)
}

func (c *RuntimeInputCoordinator) claimByID(id string) (RuntimeItem, bool, error) {
	if c == nil || strings.TrimSpace(id) == "" {
		return RuntimeItem{}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	index := -1
	for candidateIndex, item := range c.items {
		if item.ID == id && item.State == RuntimeItemPending {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return RuntimeItem{}, false, nil
	}
	candidate := cloneRuntimeItems(c.items)
	candidate[index].State = RuntimeItemProcessing
	materialized, err := c.materializeRuntimePrompt(candidate[index])
	if err != nil {
		return RuntimeItem{}, false, err
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return RuntimeItem{}, false, err
	}
	c.items = candidate
	c.revision = revision
	return materialized, true, nil
}

// Release returns one processing item to pending when invocation admission
// fails before its prompt is durably recorded.
func (c *RuntimeInputCoordinator) Release(id string) (bool, error) {
	if c == nil || strings.TrimSpace(id) == "" {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	index := -1
	for candidateIndex, item := range c.items {
		if item.ID == id && item.State == RuntimeItemProcessing {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return false, nil
	}
	candidate := cloneRuntimeItems(c.items)
	candidate[index].State = RuntimeItemPending
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return false, err
	}
	c.items = candidate
	c.revision = revision
	delete(c.submitting, id)
	if candidate[index].Kind == RuntimeItemGoalContinuation {
		c.signalGoal()
	} else {
		c.signal()
	}
	return true, nil
}

// rejectGoalContinuation durably records one permanent rejection before the
// item is settled. Recovery discards rejected items instead of returning them
// to pending.
func (c *RuntimeInputCoordinator) rejectGoalContinuation(
	id string,
	code string,
	at time.Time,
) error {
	if c == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf(
			"runtime input coordinator: Goal continuation rejection code is required",
		)
	}
	if at.IsZero() {
		at = c.clock().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	index := -1
	for candidateIndex, item := range c.items {
		if item.ID == strings.TrimSpace(id) &&
			item.Kind == RuntimeItemGoalContinuation {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return nil
	}
	existing := c.items[index]
	if existing.State == RuntimeItemRejected {
		if existing.Rejection == nil ||
			existing.Rejection.Version != runtimeItemRejectionVersion {
			return fmt.Errorf(
				"runtime input coordinator: Goal continuation %q has corrupt rejection",
				id,
			)
		}
		return nil
	}
	if existing.State != RuntimeItemPending &&
		existing.State != RuntimeItemProcessing {
		return fmt.Errorf(
			"runtime input coordinator: Goal continuation %q cannot be rejected from state %q",
			id,
			existing.State,
		)
	}
	candidate := cloneRuntimeItems(c.items)
	candidate[index].State = RuntimeItemRejected
	candidate[index].Rejection = &RuntimeItemRejection{
		Version:    runtimeItemRejectionVersion,
		Code:       code,
		RejectedAt: at.UTC(),
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return err
	}
	c.items = candidate
	c.revision = revision
	return nil
}

// ClaimStop claims the highest-priority pending stop request for a scope.
func (c *RuntimeInputCoordinator) ClaimStop(scope RuntimeInputScope) (RuntimeItem, bool, error) {
	if c == nil {
		return RuntimeItem{}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	normalizedScope := c.normalizeScope(scope)
	best := -1
	for index, item := range c.items {
		if item.State != RuntimeItemPending ||
			item.Kind != RuntimeItemStop ||
			!runtimeScopesEqual(item.Scope, normalizedScope) {
			continue
		}
		if best < 0 || item.Sequence < c.items[best].Sequence {
			best = index
		}
	}
	if best < 0 {
		return RuntimeItem{}, false, nil
	}
	candidate := cloneRuntimeItems(c.items)
	candidate[best].State = RuntimeItemProcessing
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return RuntimeItem{}, false, err
	}
	c.items = candidate
	c.revision = revision
	return cloneRuntimeItem(candidate[best]), true, nil
}

// Settle removes delivered or explicitly handled items. It is idempotent.
func (c *RuntimeInputCoordinator) Settle(ids ...string) error {
	if c == nil || len(ids) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	candidate := make([]RuntimeItem, 0, len(c.items))
	changed := false
	for _, item := range c.items {
		if _, ok := set[item.ID]; ok {
			changed = true
			continue
		}
		candidate = append(candidate, cloneRuntimeItem(item))
	}
	if !changed {
		for id := range set {
			c.delivered[id] = struct{}{}
			delete(c.submitting, id)
		}
		return nil
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return err
	}
	c.items = candidate
	c.revision = revision
	for id := range set {
		c.delivered[id] = struct{}{}
		delete(c.submitting, id)
	}
	return nil
}

func (c *RuntimeInputCoordinator) settleStopRequests(scope RuntimeInputScope) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	normalizedScope := c.normalizeScope(scope)
	ids := make([]string, 0)
	for _, item := range c.items {
		if item.Kind == RuntimeItemStop && runtimeScopesEqual(item.Scope, normalizedScope) {
			ids = append(ids, item.ID)
		}
	}
	c.mu.Unlock()
	return c.Settle(ids...)
}

// Cancel removes one still-pending item. Processing items have crossed the
// ownership boundary and cannot be edited or cancelled through the queue UI.
func (c *RuntimeInputCoordinator) Cancel(id string) (bool, error) {
	if c == nil || strings.TrimSpace(id) == "" {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	candidate := make([]RuntimeItem, 0, len(c.items))
	cancelled := false
	for _, item := range c.items {
		if item.ID == id && item.State == RuntimeItemPending {
			cancelled = true
			continue
		}
		candidate = append(candidate, cloneRuntimeItem(item))
	}
	if !cancelled {
		return false, nil
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		return false, err
	}
	c.items = candidate
	c.revision = revision
	return true, nil
}

func (c *RuntimeInputCoordinator) editPendingUserPrompt(
	id string,
	scope RuntimeInputScope,
) (QueuedPromptDraft, bool, error) {
	if c == nil || strings.TrimSpace(id) == "" {
		return QueuedPromptDraft{}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	normalizedScope := c.normalizeScope(scope)
	index := -1
	var selected RuntimeItem
	for candidateIndex, item := range c.items {
		if item.ID != strings.TrimSpace(id) ||
			item.Kind != RuntimeItemUserPrompt ||
			item.State != RuntimeItemPending ||
			!runtimeScopesEqual(item.Scope, normalizedScope) {
			continue
		}
		index = candidateIndex
		selected = cloneRuntimeItem(item)
		break
	}
	if index < 0 {
		return QueuedPromptDraft{}, false, nil
	}
	if selected.UserPrompt != nil && selected.UserPrompt.durablePrompt != nil {
		materialized, err := c.materializeRuntimePrompt(selected)
		if err != nil {
			return QueuedPromptDraft{}, false, err
		}
		selected = materialized
	}
	draft, err := queuedPromptDraft(selected)
	if err != nil {
		return QueuedPromptDraft{}, false, err
	}
	candidate := make([]RuntimeItem, 0, len(c.items)-1)
	for candidateIndex, item := range c.items {
		if candidateIndex == index {
			continue
		}
		candidate = append(candidate, cloneRuntimeItem(item))
	}
	revision := c.revision + 1
	if err := c.persistLocked(candidate, c.nextSequence, revision); err != nil {
		clearQueuedPromptDraft(&draft)
		return QueuedPromptDraft{}, false, err
	}
	c.items = candidate
	c.revision = revision
	return draft, true, nil
}

// Snapshot returns detached pending items in scheduling order.
func (c *RuntimeInputCoordinator) Snapshot(scope RuntimeInputScope) []RuntimeItem {
	items, _ := c.snapshotWithRevision(scope)
	return items
}

func (c *RuntimeInputCoordinator) snapshotWithRevision(
	scope RuntimeInputScope,
) ([]RuntimeItem, uint64) {
	if c == nil {
		return nil, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	normalizedScope := c.normalizeScope(scope)
	items := make([]RuntimeItem, 0, len(c.items))
	for _, item := range c.items {
		if item.State == RuntimeItemPending && runtimeScopesEqual(item.Scope, normalizedScope) {
			items = append(items, cloneRuntimeItem(item))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if runtimePriorityOrdinal(items[i].Priority) != runtimePriorityOrdinal(items[j].Priority) {
			return runtimePriorityOrdinal(items[i].Priority) < runtimePriorityOrdinal(items[j].Priority)
		}
		return items[i].Sequence < items[j].Sequence
	})
	return items, c.revision
}

func queuedPromptDraft(item RuntimeItem) (QueuedPromptDraft, error) {
	if item.UserPrompt == nil {
		return QueuedPromptDraft{}, fmt.Errorf("queued prompt is unavailable")
	}
	draft := QueuedPromptDraft{ID: item.ID, Display: item.UserPrompt.Display}
	var input UntrustedPromptInput
	if item.UserPrompt.materializedInput != nil {
		input = cloneUntrustedPromptInput(*item.UserPrompt.materializedInput)
	} else {
		parts := make([]UntrustedPromptPart, 0, len(item.UserPrompt.Images)+1)
		if item.UserPrompt.Prompt != "" {
			parts = append(parts, NewPromptTextPart(item.UserPrompt.Prompt))
		}
		for _, image := range item.UserPrompt.Images {
			parts = append(parts, NewPromptImagePart(
				image.Base64Data,
				image.MIMEType,
				PromptImageDetailAuto,
			))
		}
		input = NewUntrustedPromptInput(parts...)
	}
	draft.Parts = make([]QueuedPromptDraftPart, 0, len(input.Parts))
	for _, part := range input.Parts {
		switch typed := part.(type) {
		case untrustedPromptTextPart:
			draft.Parts = append(draft.Parts, QueuedPromptDraftPart{
				Kind: QueuedPromptPartText,
				Text: typed.text,
			})
		case untrustedPromptImagePart:
			decoded, reason := decodeUserImageBase64(typed.base64Data)
			if reason != "" {
				clearQueuedPromptDraft(&draft)
				return QueuedPromptDraft{}, newPromptAdmissionError(
					len(draft.Parts),
					string(promptPartImage),
					reason,
					"",
					"",
				)
			}
			mimeType, reason := normalizedUserImageMIME(typed.mimeType)
			if reason != "" {
				clear(decoded)
				clearQueuedPromptDraft(&draft)
				return QueuedPromptDraft{}, newPromptAdmissionError(
					len(draft.Parts),
					string(promptPartImage),
					reason,
					"",
					"",
				)
			}
			draft.Parts = append(draft.Parts, QueuedPromptDraftPart{
				Kind: QueuedPromptPartImage,
				Image: &QueuedPromptDraftImage{
					MIMEType: mimeType,
					Data:     decoded,
					Detail:   typed.detail,
				},
			})
		case untrustedPromptResourceLinkPart:
			resource := clonePromptResourceLink(typed.resource)
			draft.Parts = append(draft.Parts, QueuedPromptDraftPart{
				Kind:         QueuedPromptPartResourceLink,
				ResourceLink: &resource,
			})
		case untrustedPromptEmbeddedTextPart:
			resource := clonePromptEmbeddedText(typed.resource)
			draft.Parts = append(draft.Parts, QueuedPromptDraftPart{
				Kind:         QueuedPromptPartEmbeddedText,
				EmbeddedText: &resource,
			})
		case untrustedPromptEmbeddedBlobPart:
			decoded, reason := decodeUserImageBase64(
				typed.resource.Base64Data,
			)
			if reason != "" {
				clearQueuedPromptDraft(&draft)
				return QueuedPromptDraft{}, newPromptAdmissionError(
					len(draft.Parts),
					string(promptPartEmbeddedBlob),
					reason,
					"",
					"",
				)
			}
			mimeType, reason := normalizedUserImageMIME(
				typed.resource.MIMEType,
			)
			if reason != "" {
				clear(decoded)
				clearQueuedPromptDraft(&draft)
				return QueuedPromptDraft{}, newPromptAdmissionError(
					len(draft.Parts),
					string(promptPartEmbeddedBlob),
					reason,
					"",
					"",
				)
			}
			draft.Parts = append(draft.Parts, QueuedPromptDraftPart{
				Kind: QueuedPromptPartEmbeddedBlob,
				EmbeddedBlob: &QueuedPromptDraftEmbeddedBlob{
					URI:      typed.resource.URI,
					MIMEType: mimeType,
					Data:     decoded,
					Detail:   typed.resource.Detail,
					Annotations: clonePromptAnnotations(
						typed.resource.Annotations,
					),
				},
			})
		default:
			clearQueuedPromptDraft(&draft)
			return QueuedPromptDraft{}, fmt.Errorf(
				"queued prompt has invalid materialized part",
			)
		}
	}
	return draft, nil
}

func clearQueuedPromptDraft(draft *QueuedPromptDraft) {
	if draft == nil {
		return
	}
	for index := range draft.Parts {
		if draft.Parts[index].Image != nil {
			clear(draft.Parts[index].Image.Data)
			draft.Parts[index].Image.Data = nil
		}
		if draft.Parts[index].EmbeddedBlob != nil {
			clear(draft.Parts[index].EmbeddedBlob.Data)
			draft.Parts[index].EmbeddedBlob.Data = nil
		}
	}
	draft.Parts = nil
}

// Subscribe returns a coalesced ready signal. Callers must re-snapshot or
// claim after every signal.
func (c *RuntimeInputCoordinator) Subscribe() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.notify
}

// SubscribeGoalContinuation returns the independent Goal-ready signal. Generic
// transports never observe this channel.
func (c *RuntimeInputCoordinator) SubscribeGoalContinuation() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.goalNotify
}

// Revision returns the current durable generation.
func (c *RuntimeInputCoordinator) Revision() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revision
}

// Durable reports whether mutations are file-backed.
func (c *RuntimeInputCoordinator) Durable() bool {
	return c != nil && c.path != ""
}

// Known reports whether an identity is already delivered or present in the
// durable coordinator ledger. It does not consult transcript storage.
func (c *RuntimeInputCoordinator) Known(id string) bool {
	if c == nil || strings.TrimSpace(id) == "" {
		return false
	}
	id = strings.TrimSpace(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, delivered := c.delivered[id]; delivered {
		return true
	}
	for _, item := range c.items {
		if item.ID == id {
			return true
		}
	}
	return false
}

// ResolveDelivered checks the authoritative parent transcript for the
// requested identities and caches positive results. The lookup runs outside
// the coordinator lock because transcript persistence has an independent
// mutex and may be performing I/O.
func (c *RuntimeInputCoordinator) ResolveDelivered(
	ids []string,
) (map[string]struct{}, error) {
	if c == nil {
		return map[string]struct{}{}, nil
	}
	return c.resolveDelivered(ids)
}

func (c *RuntimeInputCoordinator) resolveDelivered(
	ids []string,
) (map[string]struct{}, error) {
	requested := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			requested[id] = struct{}{}
		}
	}
	covered := make(map[string]struct{}, len(requested))
	if len(requested) == 0 {
		return covered, nil
	}

	c.mu.Lock()
	unresolved := make([]string, 0, len(requested))
	for id := range requested {
		if _, delivered := c.delivered[id]; delivered {
			covered[id] = struct{}{}
			continue
		}
		unresolved = append(unresolved, id)
	}
	lookup := c.deliveryLookup
	c.mu.Unlock()

	if len(unresolved) == 0 || lookup == nil {
		return covered, nil
	}
	found, err := lookup(unresolved)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for id := range found {
		if _, wanted := requested[id]; !wanted {
			continue
		}
		c.delivered[id] = struct{}{}
		covered[id] = struct{}{}
	}
	c.mu.Unlock()
	return covered, nil
}

func (c *RuntimeInputCoordinator) normalizeScope(scope RuntimeInputScope) RuntimeInputScope {
	if strings.TrimSpace(scope.SessionID) == "" {
		scope.SessionID = c.scope.SessionID
	}
	if strings.TrimSpace(scope.ThreadID) == "" {
		scope.ThreadID = c.scope.ThreadID
	}
	if strings.TrimSpace(scope.AgentID) == "" && c.scope.AgentID != "" {
		scope.AgentID = c.scope.AgentID
	}
	scope.SessionID = strings.TrimSpace(scope.SessionID)
	scope.ThreadID = strings.TrimSpace(scope.ThreadID)
	scope.AgentID = strings.TrimSpace(scope.AgentID)
	return scope
}

func (c *RuntimeInputCoordinator) validateItem(item RuntimeItem) error {
	if item.Version != runtimeItemVersion {
		return fmt.Errorf("runtime input coordinator: item %q has unsupported version %d", item.ID, item.Version)
	}
	switch item.State {
	case RuntimeItemPending, RuntimeItemProcessing:
		if item.Rejection != nil {
			return fmt.Errorf(
				"runtime input coordinator: item %q has rejection outside rejected state",
				item.ID,
			)
		}
	case RuntimeItemRejected:
		if item.Kind != RuntimeItemGoalContinuation ||
			item.Rejection == nil ||
			item.Rejection.Version != runtimeItemRejectionVersion ||
			strings.TrimSpace(item.Rejection.Code) == "" ||
			item.Rejection.RejectedAt.IsZero() {
			return fmt.Errorf(
				"runtime input coordinator: item %q has invalid rejection",
				item.ID,
			)
		}
	default:
		return fmt.Errorf(
			"runtime input coordinator: item %q has invalid state %q",
			item.ID,
			item.State,
		)
	}
	switch item.Priority {
	case RuntimePriorityNow, RuntimePriorityNext, RuntimePriorityLater:
	default:
		return fmt.Errorf("runtime input coordinator: item %q has invalid priority %q", item.ID, item.Priority)
	}
	if item.Scope.SessionID == "" || item.Scope.SessionID != c.scope.SessionID {
		return fmt.Errorf("runtime input coordinator: item %q has invalid session scope", item.ID)
	}
	if item.Scope.ThreadID != c.scope.ThreadID {
		return fmt.Errorf("runtime input coordinator: item %q has invalid thread scope", item.ID)
	}
	if item.Scope.AgentID != c.scope.AgentID {
		return fmt.Errorf("runtime input coordinator: item %q has invalid Agent scope", item.ID)
	}
	payloadCount := 0
	if item.UserPrompt != nil {
		payloadCount++
	}
	if item.AgentMessage != nil {
		payloadCount++
	}
	if item.AgentNotification != nil {
		payloadCount++
	}
	if item.AsyncRewake != nil {
		payloadCount++
	}
	if item.PermissionDecision != nil {
		payloadCount++
	}
	if item.GoalContinuation != nil {
		payloadCount++
	}
	if item.Stop != nil {
		payloadCount++
	}
	if payloadCount != 1 {
		return fmt.Errorf("runtime input coordinator: item %q must contain exactly one payload", item.ID)
	}
	switch item.Kind {
	case RuntimeItemUserPrompt, RuntimeItemSteering:
		if item.UserPrompt == nil ||
			item.UserPrompt.durablePrompt == nil &&
				strings.TrimSpace(runtimeItemPrompt(item)) == "" {
			return fmt.Errorf("runtime input coordinator: item %q has no user prompt", item.ID)
		}
		if item.UserPrompt.durablePrompt != nil &&
			(item.UserPrompt.writer != c.promptWriter ||
				item.UserPrompt.writerItemID != item.ID) {
			return fmt.Errorf(
				"runtime input coordinator: item %q has unauthorized durable prompt",
				item.ID,
			)
		}
		if err := validateRuntimeItemUserImages(item); err != nil {
			return err
		}
	case RuntimeItemAgentMessage:
		if item.AgentMessage == nil || strings.TrimSpace(item.AgentMessage.Content) == "" {
			return fmt.Errorf("runtime input coordinator: item %q has no Agent message", item.ID)
		}
	case RuntimeItemAgentNotification:
		if item.AgentNotification == nil ||
			item.AgentNotification.ReceiptVersion != transcript.AgentCompletionReceiptVersion ||
			strings.TrimSpace(item.AgentNotification.CompletionID) == "" ||
			item.AgentNotification.CompletionID != item.ID ||
			strings.TrimSpace(item.AgentNotification.AgentID) == "" ||
			strings.TrimSpace(item.AgentNotification.Message) == "" ||
			item.AgentNotification.Generation <= 0 ||
			item.AgentNotification.TerminalSequence == 0 {
			return fmt.Errorf("runtime input coordinator: item %q has invalid Agent notification", item.ID)
		}
	case RuntimeItemAsyncRewake:
		if item.AsyncRewake == nil ||
			strings.TrimSpace(item.AsyncRewake.HookID) == "" ||
			strings.TrimSpace(item.AsyncRewake.ModelPrompt) == "" {
			return fmt.Errorf("runtime input coordinator: item %q has invalid async rewake", item.ID)
		}
	case RuntimeItemPermissionDecision:
		if item.PermissionDecision == nil ||
			item.PermissionDecision.Version != projectGraphHITLDecisionVersion ||
			strings.TrimSpace(item.PermissionDecision.RequestID) == "" ||
			strings.TrimSpace(item.PermissionDecision.InterruptID) == "" ||
			strings.TrimSpace(item.PermissionDecision.InvocationDigest) == "" ||
			strings.TrimSpace(item.PermissionDecision.PolicyRevision) == "" ||
			!item.PermissionDecision.DecisionConstraint.valid() {
			return fmt.Errorf(
				"runtime input coordinator: item %q has invalid permission decision",
				item.ID,
			)
		}
		switch item.PermissionDecision.Result.Decision {
		case PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways,
			PermissionDeny, PermissionCancelled, PermissionTimedOut:
		default:
			return fmt.Errorf(
				"runtime input coordinator: item %q has invalid permission result",
				item.ID,
			)
		}
	case RuntimeItemGoalContinuation:
		if item.GoalContinuation == nil {
			return fmt.Errorf(
				"runtime input coordinator: item %q has no Goal continuation",
				item.ID,
			)
		}
		if err := validateRuntimeGoalContinuation(
			item.ID,
			item.Scope,
			*item.GoalContinuation,
		); err != nil {
			return fmt.Errorf(
				"runtime input coordinator: item %q has invalid Goal continuation: %w",
				item.ID,
				err,
			)
		}
	case RuntimeItemStop:
		if item.Stop == nil {
			return fmt.Errorf("runtime input coordinator: item %q has no stop payload", item.ID)
		}
		switch item.Stop.Mode {
		case RuntimeStopGraceful, RuntimeStopImmediate:
		default:
			return fmt.Errorf("runtime input coordinator: item %q has invalid stop mode", item.ID)
		}
	default:
		return fmt.Errorf("runtime input coordinator: item %q has invalid kind %q", item.ID, item.Kind)
	}
	return nil
}

func validateRuntimeItemUserImages(item RuntimeItem) error {
	if item.Kind != RuntimeItemUserPrompt && item.Kind != RuntimeItemSteering {
		return nil
	}
	if item.UserPrompt == nil {
		return nil
	}
	if item.UserPrompt.durablePrompt != nil {
		if len(item.UserPrompt.Images) != 0 ||
			strings.TrimSpace(item.UserPrompt.Prompt) != "" {
			return fmt.Errorf(
				"runtime input coordinator: item %q mixes durable and inline prompt media",
				item.ID,
			)
		}
		if err := item.UserPrompt.durablePrompt.Validate(); err != nil {
			return fmt.Errorf(
				"runtime input coordinator: item %q has invalid durable prompt",
				item.ID,
			)
		}
		return nil
	}
	if err := validateUserImages(item.UserPrompt.Images); err != nil {
		return fmt.Errorf(
			"runtime input coordinator: item %q has invalid user image: %w",
			item.ID,
			err,
		)
	}
	return nil
}

func (c *RuntimeInputCoordinator) persistLocked(
	items []RuntimeItem,
	nextSequence uint64,
	revision uint64,
) error {
	return c.persistStateLocked(
		items,
		nextSequence,
		revision,
		c.admissionReceipts,
	)
}

func (c *RuntimeInputCoordinator) persistStateLocked(
	items []RuntimeItem,
	nextSequence uint64,
	revision uint64,
	admissionReceipts map[string]runtimeInputAdmissionReceipt,
) error {
	if c.path == "" {
		return nil
	}
	envelope := runtimeInputEnvelope{
		Version:           runtimeInputEnvelopeVersion,
		Revision:          revision,
		NextSequence:      nextSequence,
		Items:             cloneRuntimeItems(items),
		AdmissionReceipts: orderedRuntimeInputAdmissionReceipts(admissionReceipts),
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("runtime input coordinator: marshal ledger: %w", err)
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("runtime input coordinator: create ledger directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(c.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("runtime input coordinator: create ledger temp file: %w", err)
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("runtime input coordinator: protect ledger temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("runtime input coordinator: write ledger temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("runtime input coordinator: sync ledger temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("runtime input coordinator: close ledger temp file: %w", err)
	}
	if err := os.Rename(tempPath, c.path); err != nil {
		return fmt.Errorf("runtime input coordinator: replace ledger: %w", err)
	}
	removeTemp = false
	if directory, openErr := os.Open(dir); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func (c *RuntimeInputCoordinator) signal() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *RuntimeInputCoordinator) signalGoal() {
	if c == nil {
		return
	}
	select {
	case c.goalNotify <- struct{}{}:
	default:
	}
}

func hasPendingGoalContinuation(items []RuntimeItem) bool {
	for _, item := range items {
		if item.State == RuntimeItemPending &&
			item.Kind == RuntimeItemGoalContinuation {
			return true
		}
	}
	return false
}

func hasTransportEligiblePending(items []RuntimeItem) bool {
	for _, item := range items {
		if item.State == RuntimeItemPending &&
			item.Kind != RuntimeItemGoalContinuation {
			return true
		}
	}
	return false
}

func loadRuntimeInputEnvelope(path string) (runtimeInputEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return runtimeInputEnvelope{Version: runtimeInputEnvelopeVersion}, nil
		}
		return runtimeInputEnvelope{}, fmt.Errorf("runtime input coordinator: read ledger: %w", err)
	}
	var envelope runtimeInputEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return runtimeInputEnvelope{}, fmt.Errorf("runtime input coordinator: decode ledger: %w", err)
	}
	if envelope.Version != runtimeInputEnvelopeLegacyVersion &&
		envelope.Version != runtimeInputEnvelopeVersion {
		return runtimeInputEnvelope{}, fmt.Errorf(
			"runtime input coordinator: unsupported ledger version %d",
			envelope.Version,
		)
	}
	return envelope, nil
}

func runtimePriorityOrdinal(priority RuntimeInputPriority) int {
	switch priority {
	case RuntimePriorityNow:
		return 0
	case RuntimePriorityNext:
		return 1
	case RuntimePriorityLater:
		return 2
	default:
		return 1
	}
}

func normalizedRuntimePriority(priority RuntimeInputPriority) RuntimeInputPriority {
	if priority == "" {
		return RuntimePriorityNext
	}
	return priority
}

func runtimeScopesEqual(left, right RuntimeInputScope) bool {
	return left.SessionID == right.SessionID &&
		left.ThreadID == right.ThreadID &&
		left.AgentID == right.AgentID
}

func runtimeItemByID(items []RuntimeItem, id string) (RuntimeItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return RuntimeItem{}, false
}

type runtimeInputAdmissionPayload struct {
	Version    int                  `json:"version"`
	Kind       RuntimeItemKind      `json:"kind"`
	Priority   RuntimeInputPriority `json:"priority"`
	Scope      RuntimeInputScope    `json:"scope"`
	IsMeta     bool                 `json:"is_meta,omitempty"`
	Origin     string               `json:"origin,omitempty"`
	Provenance string               `json:"provenance,omitempty"`
	Display    string               `json:"display,omitempty"`
	Prompt     string               `json:"prompt"`
}

func runtimeInputAdmissionDigest(item RuntimeItem) (string, error) {
	if item.Kind != RuntimeItemUserPrompt || item.UserPrompt == nil ||
		len(item.UserPrompt.Images) != 0 ||
		item.UserPrompt.durablePrompt != nil ||
		item.UserPrompt.materializedInput != nil {
		return "", fmt.Errorf("admission receipt requires one plain text user prompt")
	}
	prompt := strings.TrimSpace(item.UserPrompt.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("admission receipt prompt is required")
	}
	display := strings.TrimSpace(item.UserPrompt.Display)
	if display == "" {
		display = prompt
	}
	payload, err := json.Marshal(runtimeInputAdmissionPayload{
		Version:    runtimeInputAdmissionReceiptVersion,
		Kind:       item.Kind,
		Priority:   normalizedRuntimePriority(item.Priority),
		Scope:      item.Scope,
		IsMeta:     item.IsMeta,
		Origin:     strings.TrimSpace(item.Origin),
		Provenance: strings.TrimSpace(item.Provenance),
		Display:    display,
		Prompt:     prompt,
	})
	if err != nil {
		return "", fmt.Errorf("marshal admission payload: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func runtimeInputAdmissionReceiptFor(
	item RuntimeItem,
) (runtimeInputAdmissionReceipt, error) {
	digest, err := runtimeInputAdmissionDigest(item)
	if err != nil {
		return runtimeInputAdmissionReceipt{}, err
	}
	if item.Sequence == 0 || item.EnqueuedAt.IsZero() {
		return runtimeInputAdmissionReceipt{}, fmt.Errorf(
			"admission receipt requires original ordering metadata",
		)
	}
	receipt := runtimeInputAdmissionReceipt{
		Version:         runtimeInputAdmissionReceiptVersion,
		ID:              strings.TrimSpace(item.ID),
		Kind:            item.Kind,
		Scope:           item.Scope,
		DigestAlgorithm: runtimeInputAdmissionDigestAlgorithm,
		PayloadDigest:   digest,
		Sequence:        item.Sequence,
		EnqueuedAt:      item.EnqueuedAt.UTC(),
	}
	if err := validateRuntimeInputAdmissionReceipt(receipt); err != nil {
		return runtimeInputAdmissionReceipt{}, err
	}
	return receipt, nil
}

func validateRuntimeInputAdmissionReceipt(
	receipt runtimeInputAdmissionReceipt,
) error {
	if receipt.Version != runtimeInputAdmissionReceiptVersion {
		return fmt.Errorf("unsupported version %d", receipt.Version)
	}
	if strings.TrimSpace(receipt.ID) == "" {
		return fmt.Errorf("identity is required")
	}
	if receipt.Kind != RuntimeItemUserPrompt {
		return fmt.Errorf("unsupported kind %q", receipt.Kind)
	}
	if strings.TrimSpace(receipt.Scope.SessionID) == "" {
		return fmt.Errorf("session scope is required")
	}
	if receipt.DigestAlgorithm != runtimeInputAdmissionDigestAlgorithm {
		return fmt.Errorf("unsupported digest algorithm %q", receipt.DigestAlgorithm)
	}
	decoded, err := hex.DecodeString(receipt.PayloadDigest)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("payload digest is invalid")
	}
	if receipt.Sequence == 0 || receipt.EnqueuedAt.IsZero() {
		return fmt.Errorf("original ordering metadata is required")
	}
	return nil
}

func runtimeInputAdmissionReceiptMatches(
	receipt runtimeInputAdmissionReceipt,
	item RuntimeItem,
) (bool, error) {
	if err := validateRuntimeInputAdmissionReceipt(receipt); err != nil {
		return false, err
	}
	digest, err := runtimeInputAdmissionDigest(item)
	if err != nil {
		return false, err
	}
	return receipt.ID == strings.TrimSpace(item.ID) &&
		receipt.Kind == item.Kind &&
		runtimeScopesEqual(receipt.Scope, item.Scope) &&
		receipt.PayloadDigest == digest, nil
}

func cloneRuntimeInputAdmissionReceipts(
	receipts map[string]runtimeInputAdmissionReceipt,
) map[string]runtimeInputAdmissionReceipt {
	cloned := make(map[string]runtimeInputAdmissionReceipt, len(receipts))
	for id, receipt := range receipts {
		cloned[id] = receipt
	}
	return cloned
}

func orderedRuntimeInputAdmissionReceipts(
	receipts map[string]runtimeInputAdmissionReceipt,
) []runtimeInputAdmissionReceipt {
	ordered := make([]runtimeInputAdmissionReceipt, 0, len(receipts))
	for _, receipt := range receipts {
		ordered = append(ordered, receipt)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Sequence != ordered[j].Sequence {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

func runtimeItemsEqual(left, right RuntimeItem) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func cloneRuntimeItems(items []RuntimeItem) []RuntimeItem {
	cloned := make([]RuntimeItem, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, cloneRuntimeItem(item))
	}
	return cloned
}

func cloneRuntimeItem(item RuntimeItem) RuntimeItem {
	cloned := item
	if item.UserPrompt != nil {
		payload := *item.UserPrompt
		payload.Images = append([]UserImage(nil), item.UserPrompt.Images...)
		if item.UserPrompt.durablePrompt != nil {
			record := item.UserPrompt.durablePrompt.Clone()
			payload.durablePrompt = &record
		}
		if item.UserPrompt.materializedInput != nil {
			input := cloneUntrustedPromptInput(*item.UserPrompt.materializedInput)
			payload.materializedInput = &input
		}
		cloned.UserPrompt = &payload
	}
	if item.AgentMessage != nil {
		payload := *item.AgentMessage
		cloned.AgentMessage = &payload
	}
	if item.AgentNotification != nil {
		payload := *item.AgentNotification
		cloned.AgentNotification = &payload
	}
	if item.AsyncRewake != nil {
		payload := *item.AsyncRewake
		cloned.AsyncRewake = &payload
	}
	if item.PermissionDecision != nil {
		payload := *item.PermissionDecision
		payload.Result = clonePermissionInteractionResult(item.PermissionDecision.Result)
		cloned.PermissionDecision = &payload
	}
	if item.GoalContinuation != nil {
		payload := *item.GoalContinuation
		payload.TokenBudget = cloneUint64(item.GoalContinuation.TokenBudget)
		cloned.GoalContinuation = &payload
	}
	if item.Stop != nil {
		payload := *item.Stop
		cloned.Stop = &payload
	}
	if item.Rejection != nil {
		rejection := *item.Rejection
		cloned.Rejection = &rejection
	}
	return cloned
}

func runtimeItemIDsFromMessages(messages []*schema.Message) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, message := range messages {
		if message == nil || message.Extra == nil {
			continue
		}
		for _, key := range []string{"runtime_item_id", "command_uuid"} {
			if id, ok := message.Extra[key].(string); ok && strings.TrimSpace(id) != "" {
				ids[strings.TrimSpace(id)] = struct{}{}
			}
		}
	}
	return ids
}

func runtimeItemIDsFromLoadedTranscript(
	loaded *transcript.LoadResult,
) map[string]struct{} {
	if loaded == nil {
		return nil
	}
	ids := runtimeItemIDsFromMessages(loaded.Messages)
	for _, receipt := range loaded.AgentCompletionReceipts {
		if id := strings.TrimSpace(receipt.CompletionID); id != "" {
			// Unknown receipt versions deliberately block reinjection for the
			// identity they describe while the child terminal remains available
			// as a separate replay diagnostic.
			ids[id] = struct{}{}
		}
	}
	return ids
}

func runtimeItemPrompt(item RuntimeItem) string {
	switch item.Kind {
	case RuntimeItemUserPrompt, RuntimeItemSteering:
		if item.UserPrompt != nil {
			if item.UserPrompt.durablePrompt != nil {
				return durablePromptText(*item.UserPrompt.durablePrompt)
			}
			return item.UserPrompt.Prompt
		}
	case RuntimeItemAgentMessage:
		if item.AgentMessage != nil {
			return item.AgentMessage.Content
		}
	case RuntimeItemAgentNotification:
		if item.AgentNotification != nil {
			return item.AgentNotification.Message
		}
	case RuntimeItemAsyncRewake:
		if item.AsyncRewake != nil {
			return item.AsyncRewake.ModelPrompt
		}
	case RuntimeItemPermissionDecision:
		return ""
	}
	return ""
}

func runtimeItemMetadata(item RuntimeItem) map[string]any {
	extra := map[string]any{
		"runtime_item_id":       item.ID,
		"runtime_item_kind":     string(item.Kind),
		"runtime_item_priority": string(item.Priority),
		"command_uuid":          item.ID,
		"command_priority":      string(item.Priority),
		"is_meta":               item.IsMeta,
	}
	switch item.Kind {
	case RuntimeItemUserPrompt, RuntimeItemSteering, RuntimeItemAgentMessage:
		extra["command_mode"] = "prompt"
	case RuntimeItemAgentNotification:
		extra["command_mode"] = "task-notification"
	case RuntimeItemAsyncRewake:
		extra["command_mode"] = "async-rewake"
	case RuntimeItemPermissionDecision:
		extra["command_mode"] = "graph-resume"
	case RuntimeItemGoalContinuation:
		extra["command_mode"] = "goal-continuation"
	}
	if item.Origin != "" {
		extra["command_origin"] = item.Origin
	}
	if item.Provenance != "" {
		extra["command_provenance"] = item.Provenance
	}
	if item.Scope.AgentID != "" {
		extra["command_agent_id"] = item.Scope.AgentID
	}
	if item.AgentMessage != nil {
		extra["payload_from"] = item.AgentMessage.From
		extra["payload_to"] = item.AgentMessage.To
	}
	if item.AgentNotification != nil {
		extra["attachment_kind"] = "queued_command"
		extra["task_notification_status"] = item.AgentNotification.Status
		extra["task_notification_agent_id"] = item.AgentNotification.AgentID
		extra["task_notification_tool_use_id"] = item.AgentNotification.ToolUseID
		extra["task_notification_description"] = item.AgentNotification.Description
		extra["task_notification_output_file"] = item.AgentNotification.OutputFile
		extra["agent_completion_id"] = item.AgentNotification.CompletionID
		extra["agent_completion_terminal_sequence"] = item.AgentNotification.TerminalSequence
		extra[transcript.AgentCompletionReceiptExtraKey()] = transcript.AgentCompletionReceipt{
			Version:          item.AgentNotification.ReceiptVersion,
			CompletionID:     item.AgentNotification.CompletionID,
			AgentID:          item.AgentNotification.AgentID,
			Generation:       item.AgentNotification.Generation,
			TerminalStatus:   item.AgentNotification.Status,
			TerminalSequence: item.AgentNotification.TerminalSequence,
			ParentSessionID:  item.Scope.SessionID,
			ParentThreadID:   item.Scope.ThreadID,
			ParentAgentID:    item.Scope.AgentID,
			ParentToolUseID:  item.AgentNotification.ToolUseID,
			DeliveredAt:      item.EnqueuedAt.UTC(),
		}
	}
	if item.AsyncRewake != nil {
		extra["attachment_kind"] = "async_hook_response"
		extra["hook_id"] = item.AsyncRewake.HookID
		extra["hook_event"] = item.AsyncRewake.Event
		extra["hook_name"] = item.AsyncRewake.HookName
		extra["tool_name"] = item.AsyncRewake.ToolName
		extra["hook_outcome"] = item.AsyncRewake.Outcome
		extra["hook_exit_code"] = item.AsyncRewake.ExitCode
		extra["async_rewake"] = true
	}
	if item.PermissionDecision != nil {
		extra["permission_request_id"] = item.PermissionDecision.RequestID
		extra["graph_interrupt_id"] = item.PermissionDecision.InterruptID
	}
	if item.GoalContinuation != nil {
		extra["attachment_kind"] = "goal_continuation"
		extra["goal_continuation"] = true
		extra["goal_id"] = item.GoalContinuation.GoalID
		extra["goal_objective_revision"] = item.GoalContinuation.ObjectiveRevision
		extra["goal_continuation_ordinal"] = item.GoalContinuation.ContinuationOrdinal
		extra["goal_predecessor_turn_id"] = item.GoalContinuation.PredecessorGoalTurnID
	}
	if _, ok := extra["attachment_kind"]; !ok {
		extra["attachment_kind"] = "queued_command"
	}
	return extra
}

func runtimeItemToAttachmentMessage(item RuntimeItem) *schema.Message {
	if item.UserPrompt != nil && item.UserPrompt.materializedInput != nil {
		return untrustedPromptMessage(
			*item.UserPrompt.materializedInput,
			runtimeItemMetadata(item),
		)
	}
	return newUserMessage(
		runtimeItemPrompt(item),
		runtimeItemMetadata(item),
		runtimeItemUserImages(item),
	)
}
