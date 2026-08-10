package transcript

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// GoalUsageRecordVersion is the first append-only Goal provider-usage
	// record schema. Unknown versions remain readable but are rejected by Goal
	// recovery before another provider call can be admitted.
	GoalUsageRecordVersion uint16 = 1
	GoalUsageRecordKind           = "goal-provider-usage"
)

// GoalUsageRecord attributes one final provider-reported usage value to the
// exact root Goal and executing root/descendant generation that admitted it.
// It deliberately contains no provider credentials, callbacks, or authority.
type GoalUsageRecord struct {
	Version                  uint16 `json:"version"`
	LedgerRevision           uint64 `json:"ledger_revision"`
	GoalID                   string `json:"goal_id"`
	ObjectiveRevision        uint64 `json:"objective_revision"`
	RootSessionID            string `json:"root_session_id"`
	RootThreadID             string `json:"root_thread_id"`
	RootAgentID              string `json:"root_agent_id,omitempty"`
	ExecutingSessionID       string `json:"executing_session_id"`
	ExecutingThreadID        string `json:"executing_thread_id"`
	ExecutingAgentID         string `json:"executing_agent_id,omitempty"`
	ExecutingAgentGeneration int64  `json:"executing_agent_generation,omitempty"`
	GoalTurnID               string `json:"goal_turn_id"`
	LogicalRoundID           string `json:"logical_round_id"`
	LogicalRequestID         string `json:"logical_request_id,omitempty"`
	ModelAttemptID           string `json:"model_attempt_id,omitempty"`
	ModelAttemptIndex        int    `json:"model_attempt_index,omitempty"`
	ModelProfile             string `json:"model_profile,omitempty"`
	ModelRetryIndex          int    `json:"model_retry_index,omitempty"`
	ProviderCallID           string `json:"provider_call_id"`
	PromptTokens             uint64 `json:"prompt_tokens"`
	CachedPromptTokens       uint64 `json:"cached_prompt_tokens,omitempty"`
	CompletionTokens         uint64 `json:"completion_tokens"`
	ReasoningTokens          uint64 `json:"reasoning_tokens,omitempty"`
	TotalTokens              uint64 `json:"total_tokens"`
	BillableTokens           uint64 `json:"billable_tokens"`
}

// ValidateGoalUsageRecord checks the provider-neutral durable shape. The
// engine separately validates the record against the current Goal cursor and
// exact in-memory child capability.
func ValidateGoalUsageRecord(record GoalUsageRecord) error {
	if record.Version != GoalUsageRecordVersion {
		return fmt.Errorf("unsupported Goal usage record version %d", record.Version)
	}
	if record.LedgerRevision == 0 || record.ObjectiveRevision == 0 {
		return errors.New("goal usage record requires positive revisions")
	}
	for name, value := range map[string]string{
		"Goal ID":              record.GoalID,
		"root Session ID":      record.RootSessionID,
		"root thread ID":       record.RootThreadID,
		"executing Session ID": record.ExecutingSessionID,
		"executing thread ID":  record.ExecutingThreadID,
		"Goal turn ID":         record.GoalTurnID,
		"logical round ID":     record.LogicalRoundID,
		"provider call ID":     record.ProviderCallID,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) ||
			strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("invalid %s", name)
		}
	}
	if record.LogicalRequestID != "" ||
		record.ModelAttemptID != "" ||
		record.ModelProfile != "" {
		for name, value := range map[string]string{
			"logical request ID": record.LogicalRequestID,
			"model attempt ID":   record.ModelAttemptID,
			"model profile":      record.ModelProfile,
		} {
			if strings.TrimSpace(value) == "" ||
				value != strings.TrimSpace(value) ||
				strings.ContainsRune(value, '\x00') {
				return fmt.Errorf("invalid %s", name)
			}
		}
		if record.ModelAttemptIndex < 0 || record.ModelRetryIndex < 0 {
			return errors.New("invalid model attempt or retry index")
		}
	}
	if strings.TrimSpace(record.RootAgentID) != "" {
		return errors.New("root Goal usage identity must not name a child Agent")
	}
	if record.ExecutingAgentID == "" {
		if record.ExecutingAgentGeneration != 0 ||
			record.ExecutingSessionID != record.RootSessionID ||
			record.ExecutingThreadID != record.RootThreadID {
			return errors.New("invalid root Goal usage execution identity")
		}
	} else if record.ExecutingAgentID != strings.TrimSpace(record.ExecutingAgentID) ||
		strings.ContainsRune(record.ExecutingAgentID, '\x00') ||
		record.ExecutingAgentGeneration <= 0 {
		return errors.New("invalid descendant Goal usage execution identity")
	}
	if record.CachedPromptTokens > record.PromptTokens {
		return errors.New("cached prompt tokens exceed prompt tokens")
	}
	if record.ReasoningTokens > record.CompletionTokens {
		return errors.New("reasoning tokens exceed completion tokens")
	}
	if record.PromptTokens > ^uint64(0)-record.CompletionTokens {
		return errors.New("goal usage token total overflows")
	}
	if record.TotalTokens < record.PromptTokens+record.CompletionTokens {
		return errors.New("goal usage total is below prompt plus completion")
	}
	if record.TotalTokens < record.CachedPromptTokens ||
		record.BillableTokens != record.TotalTokens-record.CachedPromptTokens {
		return errors.New("invalid Goal usage billable token formula")
	}
	return nil
}

// RecordGoalUsage appends and fsyncs one exact Goal provider-usage record.
// The successful return is the usage-ledger commit point.
func (r *Recorder) RecordGoalUsage(record GoalUsageRecord) error {
	if r == nil {
		return errors.New("goal usage record requires a transcript recorder")
	}
	if err := ValidateGoalUsageRecord(record); err != nil {
		return err
	}
	if r.Path() == "" {
		return errors.New("goal usage record requires a transcript path")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureFileOpen(); err != nil {
		return err
	}
	copied := record
	if err := r.encodeEntryLocked(recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      GoalUsageRecordKind,
		GoalUsage: &copied,
	}, "encode Goal usage record"); err != nil {
		return err
	}
	if err := r.syncOpenFileAndParent(); err != nil {
		r.closeFileAfterDurabilityFailure()
		return err
	}
	return nil
}
