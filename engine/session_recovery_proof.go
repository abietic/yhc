package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

const canonicalAdministrationRestoreScope = "canonical_administration_restore"

// SessionRecoveryProof is an opt-in attestation of the provider-free canonical
// administration restore. It deliberately proves only loaded local state; it
// never authorizes a later Agent activation or provider call.
type SessionRecoveryProof struct {
	Version                int                             `json:"version"`
	Scope                  string                          `json:"scope"`
	SessionID              string                          `json:"session_id"`
	ThreadID               string                          `json:"thread_id"`
	CWD                    string                          `json:"cwd"`
	MessageCount           int                             `json:"message_count"`
	MessagesSHA256         string                          `json:"messages_sha256"`
	NativeUsage            transcript.UsageSummary         `json:"native_usage"`
	ProcessNonce           string                          `json:"process_nonce"`
	ValidationComplete     bool                            `json:"validation_complete"`
	BlockingReasons        []string                        `json:"blocking_reasons"`
	CanResumeAgent         bool                            `json:"can_resume_agent"`
	ToolBoundary           *SessionToolBoundary            `json:"tool_boundary,omitempty"`
	WorkspaceHistory       *filehistory.ColdHistoryProof   `json:"workspace_history,omitempty"`
	WorkspaceHistoryOrigin *filehistory.ColdHistoryReceipt `json:"workspace_history_origin,omitempty"`
}

// ResumeInfoWithRecoveryProof executes the same canonical restore as ResumeInfo
// and records a proof from the resulting administration engine state.
func (s *SessionService) ResumeInfoWithRecoveryProof(
	ctx context.Context,
	info session.SessionInfo,
) (*session.ResumedSession, SessionRecoveryProof, error) {
	if s == nil || s.engine == nil || !s.engine.administrationOnly {
		return nil, SessionRecoveryProof{}, errors.New("recovery proof requires a session administration engine")
	}
	resumed, err := s.ResumeInfo(ctx, info)
	if err != nil {
		return nil, SessionRecoveryProof{}, err
	}
	loadedMessages := s.engine.GetMessages()
	loadedDigest, err := recoveryProofMessagesSHA256(loadedMessages)
	if err != nil {
		return nil, SessionRecoveryProof{}, err
	}
	resumedDigest, err := recoveryProofMessagesSHA256(resumed.Messages)
	if err != nil {
		return nil, SessionRecoveryProof{}, err
	}
	if loadedDigest != resumedDigest {
		return nil, SessionRecoveryProof{}, errors.New("recovery proof loaded messages do not match resumed session")
	}

	usage := s.engine.providerUsageSummary()
	proof := SessionRecoveryProof{
		Version:        1,
		Scope:          canonicalAdministrationRestoreScope,
		SessionID:      s.engine.SessionID(),
		ThreadID:       s.engine.ThreadID(),
		CWD:            s.engine.GetCWD(),
		MessageCount:   len(loadedMessages),
		MessagesSHA256: loadedDigest,
		NativeUsage:    usage,
		ProcessNonce:   uuid.NewString(),
		CanResumeAgent: false,
	}
	proof.BlockingReasons = recoveryProofBlockers(resumed, s.engine.RuntimeItems(), usage)
	var blocker string
	proof.WorkspaceHistory, proof.WorkspaceHistoryOrigin, blocker = s.engine.recoveryProofWorkspaceHistory()
	if blocker != "" {
		proof.BlockingReasons = append(proof.BlockingReasons, blocker)
	}
	proof.ToolBoundary = recoveryProofToolBoundary(loadedMessages, proof.CWD)
	proof.ValidationComplete = len(proof.BlockingReasons) == 0
	return resumed, proof, nil
}

// Inspect only an existing journal: proving a Session with no history must not
// create one. Reuse its owning validator without rebinding filesystem identity
// or replaying a restore. Restore operations require a separate cold-state proof.
func (e *QueryEngine) recoveryProofWorkspaceHistoryBlocker() string {
	_, _, blocker := e.recoveryProofWorkspaceHistory()
	return blocker
}

func (e *QueryEngine) recoveryProofWorkspaceHistory() (*filehistory.ColdHistoryProof, *filehistory.ColdHistoryReceipt, string) {
	identity, c := e.lockWorkspaceRevisions()
	defer c.mu.Unlock()
	if c.restoreCancel != nil || c.restorePreview != nil || c.externalWriters != 0 {
		return nil, nil, "workspace_restore_state_unverified"
	}
	historyDir := filepath.Join(identity.dir, identity.sessionID+".workspace-history")
	if filehistory.CheckColdHistoryGate(historyDir) != nil {
		return nil, nil, "workspace_history_migration_incomplete"
	}
	_, err := os.Lstat(historyDir)
	if os.IsNotExist(err) && !c.hadRestoreAuthority {
		return nil, nil, ""
	}
	if err != nil {
		return nil, nil, "workspace_history_unavailable"
	}
	journal := c.journalLocked(identity)
	if journal == nil {
		return nil, nil, "workspace_history_unavailable"
	}
	if _, _, err := journal.LoadRestoreOperation(); err != nil {
		return nil, nil, "workspace_restore_state_unverified"
	}
	observed, err := journal.ExportColdState()
	if err != nil {
		return nil, nil, "workspace_history_state_unverified"
	}
	origin, err := journal.ColdOrigin(observed.OriginSHA256)
	if err != nil {
		return nil, nil, "workspace_history_state_unverified"
	}
	return &observed, origin, ""
}

func recoveryProofMessagesSHA256(messages any) (string, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func recoveryProofBlockers(
	resumed *session.ResumedSession,
	runtimeItems []RuntimeItem,
	usage transcript.UsageSummary,
) []string {
	blockers := make([]string, 0, 12)
	if resumed == nil {
		return append(blockers, "missing_resumed_session")
	}
	if len(resumed.Warnings) > 0 {
		blockers = append(blockers, "resume_warnings")
	}
	if !recoveryProofHasClosedTurn(resumed.Messages) {
		blockers = append(blockers, "native_turn_incomplete")
	}
	metadata := resumed.Metadata
	if metadata.GoalState != nil {
		blockers = append(blockers, "goal_state")
	}
	if metadata.GraphInterrupt != nil {
		blockers = append(blockers, "graph_interrupt")
	}
	if len(metadata.AgentIDs) > 0 {
		blockers = append(blockers, "agent_ids")
	}
	if metadata.ParentSessionID != "" {
		blockers = append(blockers, "parent_session_id")
	}
	if metadata.ParentThreadID != "" {
		blockers = append(blockers, "parent_thread_id")
	}
	if metadata.ParentAgentID != "" {
		blockers = append(blockers, "parent_agent_id")
	}
	if metadata.ParentToolUseID != "" {
		blockers = append(blockers, "parent_tool_use_id")
	}
	if len(metadata.PendingRequestIDs) > 0 {
		blockers = append(blockers, "pending_request_ids")
	}
	if metadata.WorktreePath != "" {
		blockers = append(blockers, "worktree_path")
	}
	if metadata.WorktreeBranch != "" {
		blockers = append(blockers, "worktree_branch")
	}
	if len(metadata.AdditionalDirs) > 0 {
		blockers = append(blockers, "additional_dirs")
	}
	if len(runtimeItems) > 0 {
		blockers = append(blockers, "runtime_input_items")
	}
	if usage.Version != transcript.UsageSummaryVersion ||
		usage.UnsupportedSnapshotVersion != 0 ||
		usage.ResponsesWithoutMetadata != 0 ||
		usage.LegacyBoundariesWithoutUsage != 0 ||
		!usage.CurrentContextUsageKnown || !usage.LastResponseHadUsageMetadata {
		blockers = append(blockers, "native_usage_incomplete")
	}
	counts := []int{
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		usage.ResponsesWithMetadata, usage.ResponsesWithoutMetadata, usage.LastPromptTokens,
		usage.LastCompletionTokens, usage.LastTotalTokens, usage.CurrentContextPromptTokens,
	}
	for _, count := range counts {
		if count < 0 {
			return append(blockers, "native_usage_invalid")
		}
	}
	if usage.PromptTokens > usage.TotalTokens || usage.CompletionTokens > usage.TotalTokens-usage.PromptTokens ||
		usage.LastPromptTokens > usage.PromptTokens || usage.LastCompletionTokens > usage.CompletionTokens ||
		usage.LastTotalTokens > usage.TotalTokens || usage.CurrentContextPromptTokens > usage.PromptTokens {
		blockers = append(blockers, "native_usage_invalid")
	}
	return blockers
}

// A well-formed transcript can end at an unfinished user/tool boundary. Known
// usage belongs to completed responses, not necessarily the latest user turn.
// This stricter cold-proof condition does not change ordinary Session resume.
func recoveryProofHasClosedTurn(messages []*schema.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.System, schema.RoleType("developer"):
			continue
		case schema.User, schema.Tool, schema.Assistant:
		default:
			return false
		}
		if meta, _ := message.Extra["is_meta"].(bool); meta {
			continue
		}
		if apiError, _ := message.Extra["api_error"].(bool); message.Role == schema.Assistant && apiError {
			continue
		}
		switch message.Role {
		case schema.User, schema.Tool:
			return false
		case schema.Assistant:
			return len(message.ToolCalls) == 0
		}
	}
	return false
}
