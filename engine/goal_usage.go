package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

type goalUsageSubject struct {
	SessionID       string
	ThreadID        string
	AgentID         string
	AgentGeneration int64
}

type goalUsageAdmission struct {
	Version                  uint16
	LedgerRevision           uint64
	GoalID                   string
	ObjectiveRevision        uint64
	RootSessionID            string
	RootThreadID             string
	RootAgentID              string
	ExecutingSessionID       string
	ExecutingThreadID        string
	ExecutingAgentID         string
	ExecutingAgentGeneration int64
	GoalTurnID               string
	LogicalRoundID           string
	LogicalRequestID         string
	ModelAttemptID           string
	ModelAttemptIndex        int
	ModelProfile             string
	ModelRetryIndex          int
	ProviderCallID           string
	AdmittedAt               time.Time
}

type goalUsageReporter struct {
	service *goalService
	binding goalExecutionIdentity
	subject goalUsageSubject
	newID   func() string
	revoked atomic.Bool
}

func (r *goalUsageReporter) NewLogicalRoundID() string {
	if r == nil || r.revoked.Load() {
		return ""
	}
	return r.nextID()
}

func (r *goalUsageReporter) AdmitProviderUsage(
	ctx context.Context,
	descriptor execution.ProviderUsageDescriptor,
) (execution.ProviderUsageCall, error) {
	if r == nil || r.service == nil || r.revoked.Load() {
		return nil, errGoalUsageCapabilityUnavailable
	}
	if strings.TrimSpace(descriptor.LogicalRoundID) == "" {
		return nil, fmt.Errorf("goal usage logical round identity is required")
	}
	providerCallID := r.nextID()
	if strings.TrimSpace(providerCallID) == "" {
		return nil, fmt.Errorf("goal usage provider call identity is unavailable")
	}
	return r.service.admitProviderUsage(
		ctx,
		r,
		descriptor,
		providerCallID,
	)
}

func (r *goalUsageReporter) nextID() string {
	if r != nil && r.newID != nil {
		return r.newID()
	}
	return generateUUID()
}

func (r *goalUsageReporter) revoke() {
	if r != nil {
		r.revoked.Store(true)
	}
}

type goalProviderUsageCall struct {
	service   *goalService
	admission goalUsageAdmission
	gate      chan struct{}
	settled   atomic.Bool
}

func (c *goalProviderUsageCall) ProviderCallID() string {
	if c == nil {
		return ""
	}
	return c.admission.ProviderCallID
}

func (c *goalProviderUsageCall) CompleteProviderUsage(
	usage *schema.TokenUsage,
) error {
	return c.settle(func() error {
		return c.service.completeProviderUsage(c.admission, usage)
	})
}

func (c *goalProviderUsageCall) ReleaseProviderUsageBeforeDispatch() error {
	return c.settle(func() error {
		return c.service.releaseProviderUsageBeforeDispatch(c.admission)
	})
}

func (c *goalProviderUsageCall) MarkProviderUsageAmbiguous(cause error) error {
	return c.settle(func() error {
		return c.service.markProviderUsageAmbiguous(c.admission, cause)
	})
}

func (c *goalProviderUsageCall) settle(run func() error) error {
	if c == nil || c.service == nil {
		return errGoalUsageCapabilityUnavailable
	}
	if !c.settled.CompareAndSwap(false, true) {
		return nil
	}
	defer func() {
		if c.gate != nil {
			c.gate <- struct{}{}
		}
	}()
	return run()
}

func (s *goalService) providerUsageGate() chan struct{} {
	if s == nil {
		return nil
	}
	s.usageGateOnce.Do(func() {
		s.usageGate = make(chan struct{}, 1)
		s.usageGate <- struct{}{}
	})
	return s.usageGate
}

func (s *goalService) admitProviderUsage(
	ctx context.Context,
	reporter *goalUsageReporter,
	descriptor execution.ProviderUsageDescriptor,
	providerCallID string,
) (_ execution.ProviderUsageCall, err error) {
	if s == nil || s.engine == nil || reporter == nil {
		return nil, errGoalUsageCapabilityUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	gate := s.providerUsageGate()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-gate:
	}
	releaseGate := true
	defer func() {
		if releaseGate {
			gate <- struct{}{}
		}
	}()
	if s.usageUncertain.Load() {
		return nil, errGoalUsageCoverageIncomplete
	}

	e := s.engine
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()

	current := e.goalState
	if err := validateGoalUsageReporter(current, reporter); err != nil {
		return nil, err
	}
	if current.Status != goalStatusActive {
		return nil, fmt.Errorf(
			"goal provider admission requires active status, got %s",
			current.Status,
		)
	}
	if current != nil && current.PendingUsageAdmission != nil {
		reconciled, changed, reconcileErr := reconcileGoalUsageState(
			current,
			e.loadGoalUsageLedgerLocked(),
			e.goalUsageNow(),
		)
		if changed {
			if persistErr := e.persistSessionCheckpointMessagesLocked(
				"",
				nil,
				reconciled,
			); persistErr != nil {
				return nil, fmt.Errorf(
					"persist Goal usage reconciliation: %w",
					persistErr,
				)
			}
			e.goalState = cloneGoalState(reconciled)
			current = e.goalState
		}
		if reconcileErr != nil {
			return nil, reconcileErr
		}
	}
	if err := validateGoalUsageReporter(current, reporter); err != nil {
		return nil, err
	}
	if current.Status != goalStatusActive {
		return nil, fmt.Errorf(
			"goal provider admission requires active status, got %s",
			current.Status,
		)
	}
	if current.PendingUsageAdmission != nil {
		return nil, errGoalUsageAdmissionPending
	}
	if current.UsageLedgerRevision == math.MaxUint64 {
		return nil, fmt.Errorf("goal usage ledger revision exhausted")
	}
	now := e.goalUsageNow()
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return nil, err
	}
	admission := goalUsageAdmission{
		Version:                  session.PersistedGoalUsageAdmissionVersion,
		LedgerRevision:           current.UsageLedgerRevision + 1,
		GoalID:                   reporter.binding.GoalID,
		ObjectiveRevision:        reporter.binding.ObjectiveRevision,
		RootSessionID:            reporter.binding.RootSessionID,
		RootThreadID:             reporter.binding.RootThreadID,
		RootAgentID:              reporter.binding.RootAgentID,
		ExecutingSessionID:       reporter.subject.SessionID,
		ExecutingThreadID:        reporter.subject.ThreadID,
		ExecutingAgentID:         reporter.subject.AgentID,
		ExecutingAgentGeneration: reporter.subject.AgentGeneration,
		GoalTurnID:               reporter.binding.GoalTurnID,
		LogicalRoundID:           strings.TrimSpace(descriptor.LogicalRoundID),
		LogicalRequestID:         strings.TrimSpace(descriptor.LogicalRequestID),
		ModelAttemptID:           strings.TrimSpace(descriptor.ModelAttemptID),
		ModelAttemptIndex:        descriptor.ModelAttemptIndex,
		ModelProfile:             strings.TrimSpace(descriptor.ModelProfile),
		ModelRetryIndex:          descriptor.ModelRetryIndex,
		ProviderCallID:           providerCallID,
		AdmittedAt:               now,
	}
	if err := validateGoalUsageAdmission(&admission); err != nil {
		return nil, err
	}
	next.PendingUsageAdmission = cloneGoalUsageAdmission(&admission)
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return nil, fmt.Errorf("persist Goal provider admission: %w", err)
	}
	e.goalState = cloneGoalState(next)
	releaseGate = false
	return &goalProviderUsageCall{
		service:   s,
		admission: admission,
		gate:      gate,
	}, nil
}

func (s *goalService) completeProviderUsage(
	admission goalUsageAdmission,
	usage *schema.TokenUsage,
) error {
	normalized, err := normalizeGoalProviderUsage(usage)
	if err != nil {
		limitErr := s.markProviderUsageAmbiguous(admission, err)
		return errors.Join(err, limitErr)
	}
	record := transcript.GoalUsageRecord{
		Version:                  transcript.GoalUsageRecordVersion,
		LedgerRevision:           admission.LedgerRevision,
		GoalID:                   admission.GoalID,
		ObjectiveRevision:        admission.ObjectiveRevision,
		RootSessionID:            admission.RootSessionID,
		RootThreadID:             admission.RootThreadID,
		RootAgentID:              admission.RootAgentID,
		ExecutingSessionID:       admission.ExecutingSessionID,
		ExecutingThreadID:        admission.ExecutingThreadID,
		ExecutingAgentID:         admission.ExecutingAgentID,
		ExecutingAgentGeneration: admission.ExecutingAgentGeneration,
		GoalTurnID:               admission.GoalTurnID,
		LogicalRoundID:           admission.LogicalRoundID,
		LogicalRequestID:         admission.LogicalRequestID,
		ModelAttemptID:           admission.ModelAttemptID,
		ModelAttemptIndex:        admission.ModelAttemptIndex,
		ModelProfile:             admission.ModelProfile,
		ModelRetryIndex:          admission.ModelRetryIndex,
		ProviderCallID:           admission.ProviderCallID,
		PromptTokens:             normalized.PromptTokens,
		CachedPromptTokens:       normalized.CachedPromptTokens,
		CompletionTokens:         normalized.CompletionTokens,
		ReasoningTokens:          normalized.ReasoningTokens,
		TotalTokens:              normalized.TotalTokens,
		BillableTokens:           normalized.BillableTokens,
	}
	if err := transcript.ValidateGoalUsageRecord(record); err != nil {
		limitErr := s.markProviderUsageAmbiguous(admission, err)
		return errors.Join(err, limitErr)
	}
	if s == nil || s.engine == nil {
		return errGoalUsageCapabilityUnavailable
	}
	if err := s.recordGoalUsage(record); err != nil {
		commitErr := fmt.Errorf("commit Goal usage record: %w", err)
		limitErr := s.markProviderUsageAmbiguous(admission, commitErr)
		return errors.Join(commitErr, limitErr)
	}

	e := s.engine
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	current := e.goalState
	if current == nil {
		return errGoalNotFound
	}
	if current.PendingUsageAdmission == nil {
		if current.UsageLedgerRevision >= record.LedgerRevision {
			return nil
		}
		return errGoalUsageAdmissionPending
	}
	if !sameGoalUsageAdmission(current.PendingUsageAdmission, &admission) {
		return fmt.Errorf("stale Goal provider usage completion")
	}
	next, err := applyGoalUsageRecord(current, record, e.goalUsageNow())
	if err != nil {
		return err
	}
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return fmt.Errorf("persist Goal usage aggregate: %w", err)
	}
	e.goalState = cloneGoalState(next)
	phase := GoalLifecycleUsageRecorded
	if next.Status == goalStatusBudgetLimited {
		phase = GoalLifecycleBudgetLimited
	}
	e.projectGoalLifecycle(phase, next, admission.GoalTurnID, next.UpdatedAt)
	return nil
}

func (s *goalService) releaseProviderUsageBeforeDispatch(
	admission goalUsageAdmission,
) error {
	if s == nil || s.engine == nil {
		return errGoalUsageCapabilityUnavailable
	}
	e := s.engine
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	current := e.goalState
	if current == nil || current.PendingUsageAdmission == nil ||
		!sameGoalUsageAdmission(current.PendingUsageAdmission, &admission) {
		return fmt.Errorf("stale Goal pre-dispatch release")
	}
	next, err := nextGoalRevision(current, e.goalUsageNow())
	if err != nil {
		return err
	}
	next.PendingUsageAdmission = nil
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return fmt.Errorf("persist Goal pre-dispatch release: %w", err)
	}
	e.goalState = cloneGoalState(next)
	e.projectGoalLifecycle(
		GoalLifecycleUsageAdmissionReleased,
		next,
		admission.GoalTurnID,
		next.UpdatedAt,
	)
	return nil
}

func (s *goalService) markProviderUsageAmbiguous(
	admission goalUsageAdmission,
	cause error,
) error {
	if s == nil || s.engine == nil {
		return errGoalUsageCapabilityUnavailable
	}
	s.usageUncertain.Store(true)
	e := s.engine
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	current := e.goalState
	if current == nil || current.PendingUsageAdmission == nil ||
		!sameGoalUsageAdmission(current.PendingUsageAdmission, &admission) {
		return fmt.Errorf("stale Goal ambiguous provider usage")
	}
	next, err := nextGoalRevision(current, e.goalUsageNow())
	if err != nil {
		return err
	}
	next.Status = goalStatusUsageLimited
	next.StatusReasonCode = goalReasonUsageCoverageIncomplete
	next.StatusReason = "provider dispatch may have occurred without exact final usage"
	if cause != nil && errors.Is(cause, context.Canceled) {
		next.StatusReason = "provider dispatch was canceled without exact final usage"
	}
	resetGoalTurnEvidence(next)
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return fmt.Errorf("persist Goal usage-limited state: %w", err)
	}
	e.goalState = cloneGoalState(next)
	e.projectGoalLifecycle(
		GoalLifecycleUsageLimited,
		next,
		admission.GoalTurnID,
		next.UpdatedAt,
	)
	return nil
}

func (s *goalService) recordGoalUsage(
	record transcript.GoalUsageRecord,
) error {
	if s == nil || s.engine == nil {
		return errGoalUsageCapabilityUnavailable
	}
	if s.recordGoalUsageFn != nil {
		return s.recordGoalUsageFn(record)
	}
	if s.engine.transcript == nil {
		return errGoalUsageCapabilityUnavailable
	}
	return s.engine.transcript.RecordGoalUsage(record)
}

func validateGoalUsageReporter(
	state *goalState,
	reporter *goalUsageReporter,
) error {
	if state == nil {
		return errGoalNotFound
	}
	if state.unavailable {
		return errGoalUnavailable
	}
	if reporter == nil || reporter.revoked.Load() ||
		!reporter.binding.valid() {
		return errGoalUsageCapabilityUnavailable
	}
	if state.GoalID != reporter.binding.GoalID ||
		state.ObjectiveRevision != reporter.binding.ObjectiveRevision ||
		state.LastGoalTurnID != reporter.binding.GoalTurnID {
		return fmt.Errorf("stale Goal usage binding")
	}
	subject := reporter.subject
	if strings.TrimSpace(subject.SessionID) == "" ||
		strings.TrimSpace(subject.ThreadID) == "" {
		return fmt.Errorf("goal usage execution identity is incomplete")
	}
	if subject.AgentID == "" {
		if subject.AgentGeneration != 0 ||
			subject.SessionID != reporter.binding.RootSessionID ||
			subject.ThreadID != reporter.binding.RootThreadID {
			return fmt.Errorf("invalid root Goal usage subject")
		}
	} else if subject.AgentGeneration <= 0 {
		return fmt.Errorf("invalid descendant Goal usage generation")
	}
	return nil
}

func validateGoalUsageAdmission(admission *goalUsageAdmission) error {
	if admission == nil {
		return nil
	}
	record := transcript.GoalUsageRecord{
		Version:                  transcript.GoalUsageRecordVersion,
		LedgerRevision:           admission.LedgerRevision,
		GoalID:                   admission.GoalID,
		ObjectiveRevision:        admission.ObjectiveRevision,
		RootSessionID:            admission.RootSessionID,
		RootThreadID:             admission.RootThreadID,
		RootAgentID:              admission.RootAgentID,
		ExecutingSessionID:       admission.ExecutingSessionID,
		ExecutingThreadID:        admission.ExecutingThreadID,
		ExecutingAgentID:         admission.ExecutingAgentID,
		ExecutingAgentGeneration: admission.ExecutingAgentGeneration,
		GoalTurnID:               admission.GoalTurnID,
		LogicalRoundID:           admission.LogicalRoundID,
		LogicalRequestID:         admission.LogicalRequestID,
		ModelAttemptID:           admission.ModelAttemptID,
		ModelAttemptIndex:        admission.ModelAttemptIndex,
		ModelProfile:             admission.ModelProfile,
		ModelRetryIndex:          admission.ModelRetryIndex,
		ProviderCallID:           admission.ProviderCallID,
	}
	if admission.Version != session.PersistedGoalUsageAdmissionVersion ||
		admission.AdmittedAt.IsZero() {
		return fmt.Errorf("invalid Goal usage admission version or timestamp")
	}
	// The final token fields are zero at admission and satisfy the durable
	// identity validation without inventing provider usage.
	return transcript.ValidateGoalUsageRecord(record)
}

func applyGoalUsageRecord(
	current *goalState,
	record transcript.GoalUsageRecord,
	now time.Time,
) (*goalState, error) {
	if current == nil || current.PendingUsageAdmission == nil {
		return nil, errGoalUsageAdmissionPending
	}
	if err := transcript.ValidateGoalUsageRecord(record); err != nil {
		return nil, err
	}
	if !goalUsageRecordMatchesAdmission(record, current.PendingUsageAdmission) {
		return nil, fmt.Errorf("goal usage record does not match pending admission")
	}
	if current.TokensUsed > math.MaxUint64-record.BillableTokens {
		return nil, fmt.Errorf("goal token usage overflow")
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return nil, err
	}
	next.TokensUsed += record.BillableTokens
	next.UsageLedgerRevision = record.LedgerRevision
	next.PendingUsageAdmission = nil
	if next.TokenBudget != nil && next.TokensUsed >= *next.TokenBudget {
		next.Status = goalStatusBudgetLimited
		next.StatusReasonCode = goalReasonBudgetExhausted
		next.StatusReason = "the Goal token budget has been reached"
		resetGoalTurnEvidence(next)
	}
	return next, nil
}

func reconcileGoalUsageState(
	current *goalState,
	loaded *transcript.LoadResult,
	now time.Time,
) (*goalState, bool, error) {
	if current == nil {
		return nil, false, nil
	}
	records, err := inspectGoalUsageLedger(current, loaded)
	if err != nil {
		next, changed, limitErr := usageLimitedGoalState(
			current,
			now,
			"goal provider usage coverage is incomplete",
		)
		return next, changed, errors.Join(err, limitErr)
	}
	if current.PendingUsageAdmission == nil {
		return cloneGoalState(current), false, nil
	}
	record, found := records[current.PendingUsageAdmission.LedgerRevision]
	if !found {
		next, revisionErr := nextGoalRevision(current, now)
		if revisionErr != nil {
			return cloneGoalState(current), false, revisionErr
		}
		next.Status = goalStatusUsageLimited
		next.StatusReasonCode = goalReasonUsageCoverageIncomplete
		next.StatusReason = "pending provider admission has no exact durable usage record"
		resetGoalTurnEvidence(next)
		return next, true, errGoalUsageCoverageIncomplete
	}
	next, err := applyGoalUsageRecord(current, record, now)
	if err != nil {
		limited, changed, limitErr := usageLimitedGoalState(
			current,
			now,
			"pending provider admission has a stale or invalid usage record",
		)
		return limited, changed, errors.Join(err, limitErr)
	}
	return next, true, nil
}

func usageLimitedGoalState(
	current *goalState,
	now time.Time,
	reason string,
) (*goalState, bool, error) {
	if current == nil || current.Status == goalStatusComplete {
		return cloneGoalState(current), false, nil
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return cloneGoalState(current), false, err
	}
	next.Status = goalStatusUsageLimited
	next.StatusReasonCode = goalReasonUsageCoverageIncomplete
	next.StatusReason = reason
	resetGoalTurnEvidence(next)
	return next, true, nil
}

func inspectGoalUsageLedger(
	state *goalState,
	loaded *transcript.LoadResult,
) (map[uint64]transcript.GoalUsageRecord, error) {
	records := make(map[uint64]transcript.GoalUsageRecord)
	if state == nil {
		return records, nil
	}
	if loaded == nil {
		if state.UsageLedgerRevision > 0 || state.PendingUsageAdmission != nil {
			return nil, errGoalUsageCoverageIncomplete
		}
		return records, nil
	}
	if len(loaded.GoalUsageCorruptions) > 0 &&
		(state.UsageLedgerRevision > 0 || state.PendingUsageAdmission != nil) {
		return nil, fmt.Errorf("goal usage transcript contains corrupt records")
	}
	for _, record := range loaded.GoalUsageRecords {
		if record.GoalID != state.GoalID {
			continue
		}
		if err := transcript.ValidateGoalUsageRecord(record); err != nil {
			return nil, err
		}
		if record.ObjectiveRevision > state.ObjectiveRevision {
			return nil, fmt.Errorf("goal usage record has a future objective revision")
		}
		if existing, duplicate := records[record.LedgerRevision]; duplicate {
			if existing != record {
				return nil, fmt.Errorf(
					"conflicting Goal usage records at ledger revision %d",
					record.LedgerRevision,
				)
			}
			continue
		}
		records[record.LedgerRevision] = record
	}
	pendingRevision := uint64(0)
	if state.PendingUsageAdmission != nil {
		if state.UsageLedgerRevision == math.MaxUint64 ||
			state.PendingUsageAdmission.LedgerRevision !=
				state.UsageLedgerRevision+1 {
			return nil, fmt.Errorf("pending Goal usage ledger revision is not next")
		}
		pendingRevision = state.PendingUsageAdmission.LedgerRevision
	}
	var committedRecords uint64
	for revision := range records {
		switch {
		case revision <= state.UsageLedgerRevision:
			committedRecords++
		case pendingRevision != 0 && revision == pendingRevision:
		default:
			return nil, fmt.Errorf(
				"goal usage ledger has unauthorized future revision %d",
				revision,
			)
		}
	}
	if committedRecords != state.UsageLedgerRevision {
		return nil, fmt.Errorf(
			"goal usage ledger is incomplete: committed_records=%d state_revision=%d",
			committedRecords,
			state.UsageLedgerRevision,
		)
	}
	return records, nil
}

func goalUsageRecordMatchesAdmission(
	record transcript.GoalUsageRecord,
	admission *goalUsageAdmission,
) bool {
	if admission == nil {
		return false
	}
	return record.LedgerRevision == admission.LedgerRevision &&
		record.GoalID == admission.GoalID &&
		record.ObjectiveRevision == admission.ObjectiveRevision &&
		record.RootSessionID == admission.RootSessionID &&
		record.RootThreadID == admission.RootThreadID &&
		record.RootAgentID == admission.RootAgentID &&
		record.ExecutingSessionID == admission.ExecutingSessionID &&
		record.ExecutingThreadID == admission.ExecutingThreadID &&
		record.ExecutingAgentID == admission.ExecutingAgentID &&
		record.ExecutingAgentGeneration == admission.ExecutingAgentGeneration &&
		record.GoalTurnID == admission.GoalTurnID &&
		record.LogicalRoundID == admission.LogicalRoundID &&
		record.LogicalRequestID == admission.LogicalRequestID &&
		record.ModelAttemptID == admission.ModelAttemptID &&
		record.ModelAttemptIndex == admission.ModelAttemptIndex &&
		record.ModelProfile == admission.ModelProfile &&
		record.ModelRetryIndex == admission.ModelRetryIndex &&
		record.ProviderCallID == admission.ProviderCallID
}

func sameGoalUsageAdmission(left, right *goalUsageAdmission) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneGoalUsageAdmission(
	admission *goalUsageAdmission,
) *goalUsageAdmission {
	if admission == nil {
		return nil
	}
	cloned := *admission
	return &cloned
}

func persistedGoalUsageAdmission(
	admission *goalUsageAdmission,
) *session.PersistedGoalUsageAdmission {
	if admission == nil {
		return nil
	}
	return &session.PersistedGoalUsageAdmission{
		Version:                  admission.Version,
		LedgerRevision:           admission.LedgerRevision,
		GoalID:                   admission.GoalID,
		ObjectiveRevision:        admission.ObjectiveRevision,
		RootSessionID:            admission.RootSessionID,
		RootThreadID:             admission.RootThreadID,
		RootAgentID:              admission.RootAgentID,
		ExecutingSessionID:       admission.ExecutingSessionID,
		ExecutingThreadID:        admission.ExecutingThreadID,
		ExecutingAgentID:         admission.ExecutingAgentID,
		ExecutingAgentGeneration: admission.ExecutingAgentGeneration,
		GoalTurnID:               admission.GoalTurnID,
		LogicalRoundID:           admission.LogicalRoundID,
		LogicalRequestID:         admission.LogicalRequestID,
		ModelAttemptID:           admission.ModelAttemptID,
		ModelAttemptIndex:        admission.ModelAttemptIndex,
		ModelProfile:             admission.ModelProfile,
		ModelRetryIndex:          admission.ModelRetryIndex,
		ProviderCallID:           admission.ProviderCallID,
		AdmittedAt:               admission.AdmittedAt,
	}
}

func goalUsageAdmissionFromPersisted(
	admission *session.PersistedGoalUsageAdmission,
) *goalUsageAdmission {
	if admission == nil {
		return nil
	}
	return &goalUsageAdmission{
		Version:                  admission.Version,
		LedgerRevision:           admission.LedgerRevision,
		GoalID:                   admission.GoalID,
		ObjectiveRevision:        admission.ObjectiveRevision,
		RootSessionID:            admission.RootSessionID,
		RootThreadID:             admission.RootThreadID,
		RootAgentID:              admission.RootAgentID,
		ExecutingSessionID:       admission.ExecutingSessionID,
		ExecutingThreadID:        admission.ExecutingThreadID,
		ExecutingAgentID:         admission.ExecutingAgentID,
		ExecutingAgentGeneration: admission.ExecutingAgentGeneration,
		GoalTurnID:               admission.GoalTurnID,
		LogicalRoundID:           admission.LogicalRoundID,
		LogicalRequestID:         admission.LogicalRequestID,
		ModelAttemptID:           admission.ModelAttemptID,
		ModelAttemptIndex:        admission.ModelAttemptIndex,
		ModelProfile:             admission.ModelProfile,
		ModelRetryIndex:          admission.ModelRetryIndex,
		ProviderCallID:           admission.ProviderCallID,
		AdmittedAt:               admission.AdmittedAt,
	}
}

func (e *QueryEngine) loadGoalUsageLedgerLocked() *transcript.LoadResult {
	if e == nil || e.transcript == nil {
		return nil
	}
	loaded, err := e.transcript.LoadFull()
	if err != nil {
		return nil
	}
	return loaded
}

func (e *QueryEngine) goalUsageNow() time.Time {
	if e == nil {
		return time.Now().UTC()
	}
	e.mu.Lock()
	clock := e.config.Clock
	e.mu.Unlock()
	if clock == nil {
		clock = time.Now
	}
	return clock().UTC()
}

func goalUsageCoverage(state *goalState) string {
	switch {
	case state == nil:
		return ""
	case state.unavailable:
		return "unavailable"
	case state.PendingUsageAdmission != nil:
		return "pending"
	case state.Status == goalStatusUsageLimited:
		return "incomplete"
	default:
		return "complete"
	}
}

func goalUsageAdmissionSnapshot(
	admission *goalUsageAdmission,
) *GoalUsageAdmissionSnapshot {
	if admission == nil {
		return nil
	}
	return &GoalUsageAdmissionSnapshot{
		Version:                  admission.Version,
		LedgerRevision:           admission.LedgerRevision,
		GoalID:                   admission.GoalID,
		ObjectiveRevision:        admission.ObjectiveRevision,
		RootSessionID:            admission.RootSessionID,
		RootThreadID:             admission.RootThreadID,
		RootAgentID:              admission.RootAgentID,
		ExecutingSessionID:       admission.ExecutingSessionID,
		ExecutingThreadID:        admission.ExecutingThreadID,
		ExecutingAgentID:         admission.ExecutingAgentID,
		ExecutingAgentGeneration: admission.ExecutingAgentGeneration,
		GoalTurnID:               admission.GoalTurnID,
		LogicalRoundID:           admission.LogicalRoundID,
		ProviderCallID:           admission.ProviderCallID,
		AdmittedAt:               admission.AdmittedAt,
	}
}

func goalUsageAdmissionFromSnapshot(
	admission *GoalUsageAdmissionSnapshot,
) *goalUsageAdmission {
	if admission == nil {
		return nil
	}
	return &goalUsageAdmission{
		Version:                  admission.Version,
		LedgerRevision:           admission.LedgerRevision,
		GoalID:                   admission.GoalID,
		ObjectiveRevision:        admission.ObjectiveRevision,
		RootSessionID:            admission.RootSessionID,
		RootThreadID:             admission.RootThreadID,
		RootAgentID:              admission.RootAgentID,
		ExecutingSessionID:       admission.ExecutingSessionID,
		ExecutingThreadID:        admission.ExecutingThreadID,
		ExecutingAgentID:         admission.ExecutingAgentID,
		ExecutingAgentGeneration: admission.ExecutingAgentGeneration,
		GoalTurnID:               admission.GoalTurnID,
		LogicalRoundID:           admission.LogicalRoundID,
		ProviderCallID:           admission.ProviderCallID,
		AdmittedAt:               admission.AdmittedAt,
	}
}

func (e *QueryEngine) currentGoalProviderUsageReporter() *goalUsageReporter {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	configured := e.config.goalUsageReporter
	subject := goalUsageSubject{
		SessionID:       e.config.SessionID,
		ThreadID:        e.config.ThreadID,
		AgentID:         e.config.AgentID,
		AgentGeneration: e.config.AgentGeneration,
	}
	configBinding := cloneGoalExecutionIdentity(e.config.goalBinding)
	e.mu.Unlock()
	if configured != nil {
		return configured
	}
	binding := e.currentGoalExecutionIdentity()
	if binding == nil {
		if configBinding == nil || !configBinding.valid() {
			return nil
		}
		binding = configBinding
	}
	service := e.goalService
	if subject.AgentID != "" && configBinding != nil {
		// A restored Goal-bound descendant without its root process receives a
		// deny-only capability. It cannot silently relabel usage as non-Goal.
		service = nil
	}
	return &goalUsageReporter{
		service: service,
		binding: *binding,
		subject: subject,
	}
}

func (e *QueryEngine) currentGoalProviderUsageAdmitter() execution.ProviderUsageAdmitter {
	reporter := e.currentGoalProviderUsageReporter()
	if reporter == nil {
		return nil
	}
	return reporter
}

func (e *QueryEngine) goalProviderUsageRequired() bool {
	if e == nil {
		return false
	}
	if e.currentGoalExecutionIdentity() != nil {
		return true
	}
	state := e.goalService.snapshot()
	return state != nil &&
		(state.unavailable || state.Status != goalStatusComplete)
}

func (e *QueryEngine) providerUsageForPotentialGoalCall() (
	execution.ProviderUsageAdmitter,
	error,
) {
	admitter := e.currentGoalProviderUsageAdmitter()
	if admitter != nil {
		return admitter, nil
	}
	if e.goalProviderUsageRequired() {
		return nil, errGoalUsageCapabilityUnavailable
	}
	return nil, nil
}

func (e *QueryEngine) bindGoalUsageReporterForChild(
	binding *goalExecutionIdentity,
	subject goalUsageSubject,
) *goalUsageReporter {
	if e == nil || binding == nil || !binding.valid() {
		return nil
	}
	e.mu.Lock()
	parentReporter := e.config.goalUsageReporter
	parentBinding := cloneGoalExecutionIdentity(e.config.goalBinding)
	e.mu.Unlock()
	service := e.goalService
	if parentReporter != nil {
		service = parentReporter.service
		if parentReporter.binding != *binding {
			return &goalUsageReporter{
				binding: *binding,
				subject: subject,
			}
		}
	} else if parentBinding != nil && *parentBinding != *binding {
		return &goalUsageReporter{
			binding: *binding,
			subject: subject,
		}
	}
	return &goalUsageReporter{
		service: service,
		binding: *binding,
		subject: subject,
	}
}
