// Package session — resume.go implements session resume orchestration.
// Loads saved transcripts and rebuilds engine state for conversation continuation.
// Mirrors the core resume behavior from reference sessionRestore.ts and
// conversationRecovery.ts: locate session → load transcript → validate →
// rebuild metadata → estimate tokens → optionally truncate → return.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/transcript"
)

// ResumeOptions configures how a session is located and loaded for resume.
type ResumeOptions struct {
	// SessionID identifies the session to resume by UUID.
	SessionID string
	// SessionDir is an alternative to SessionID — direct path to the session directory.
	SessionDir string
	// ProjectDir is the project directory used to locate the default session storage.
	ProjectDir string
	// ValidateMessages controls whether message integrity checks run (role alternation,
	// tool call/result pairing).
	ValidateMessages bool
	// MaxMessages limits the number of messages to load from the transcript.
	// 0 means load all messages.
	MaxMessages int
}

// ResumedSession holds the rebuilt state of a resumed session.
type ResumedSession struct {
	// SessionID is the UUID of the resumed session.
	SessionID string
	// Messages are the loaded and optionally validated/truncated conversation messages.
	Messages []*schema.Message
	// Metadata holds session-level metadata extracted from the messages.
	Metadata SessionMetadata
	// TokenEstimate is the estimated token count for the loaded messages.
	TokenEstimate int
	// TruncatedAt is the index where messages were truncated (0 if no truncation).
	TruncatedAt int
	// Warnings collects non-fatal issues found during resume (e.g., validation warnings).
	Warnings []string
	// ActionableRequestIDs is the live-runtime intersection computed by the
	// engine. IDs found only on disk are never actionable.
	ActionableRequestIDs []string
	// RestoredAgents describes child threads reattached to live runtime or
	// reconstructed as replay-only transcript projections.
	RestoredAgents []RestoredAgent
}

// RestoredAgent reports the attachment mode chosen while restoring one Agent.
type RestoredAgent struct {
	AgentID  string
	ThreadID string
	Mode     string
	Status   string
}

// SessionMetadata holds metadata extracted or inferred from a resumed session's messages.
type SessionMetadata struct {
	// CreatedAt is when the session was first created (from first message timestamp or file stat).
	CreatedAt time.Time
	// LastActiveAt is the time of the last activity (from last message timestamp or file mtime).
	LastActiveAt time.Time
	// Model is the model identifier if recoverable from messages (empty if unknown).
	Model string
	// Provider is the model provider (e.g., "openai", "claude", "ark"). Empty if unknown.
	Provider string
	// TurnCount is the number of user/assistant turn pairs.
	TurnCount int
	// CompactCount is the number of compaction boundaries found in the transcript.
	CompactCount int
	// MessageCount is the total number of messages in the transcript.
	MessageCount int
	// ProjectDir is the project directory associated with this session.
	ProjectDir string
	// ParentSessionID is set when this session was branched from another.
	ParentSessionID string
	// BranchPoint is the message index where the branch occurred (0 if not a branch).
	BranchPoint int
	// IsLeaf is true if no other sessions have branched from this one.
	IsLeaf bool
	// TokenUsage is the estimated total token count for the session.
	TokenUsage int
	// GitBranch is the git branch associated with this session.
	GitBranch string
	// CWD is the working directory associated with this session.
	CWD                        string
	ThreadID                   string
	AgentID                    string
	AgentGeneration            int64
	AgentName                  string
	AgentRole                  string
	ModelRole                  string
	ParentThreadID             string
	ParentAgentID              string
	ParentToolUseID            string
	PermissionMode             string
	QueryKernelVersion         string
	QueryKernelStage           string
	QueryKernelIncompatibility string
	PlanState                  *PersistedPlanState
	GoalState                  *PersistedGoalState
	GoalBinding                *PersistedGoalBinding
	ModelBinding               *PersistedModelBinding
	GraphInterrupt             *PersistedGraphInterrupt
	WorktreePath               string
	WorktreeBranch             string
	AdditionalDirs             []string
	AgentIDs                   []string
	PendingRequestIDs          []string
	RuntimeRevision            uint64
	Status                     string
}

// ResumeSession loads and rebuilds session state for continuation.
// It locates the session directory (by ID or direct path), loads the transcript,
// validates message integrity, rebuilds metadata, estimates token count, and
// optionally truncates old messages. Mirrors the reference loadConversationForResume
// + processResumedConversation behavior.
func ResumeSession(ctx context.Context, opts ResumeOptions) (*ResumedSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resume canceled before load: %w", err)
	}

	// Step 1: Locate the session directory and transcript file.
	sessionDir, sessionID, err := resolveSessionLocation(opts)
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}

	// Step 2: Load transcript messages from disk.
	recorder := transcript.NewRecorder(sessionID, sessionDir)
	transcriptPath := recorder.Path()
	if transcriptPath == "" {
		return nil, errors.New("session transcript path is empty")
	}

	loadResult, err := recorder.LoadFullContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("load transcript: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resume canceled after load: %w", err)
	}
	if len(loadResult.Messages) == 0 &&
		len(loadResult.LifecycleBoundaries) == 0 {
		return nil, fmt.Errorf("session %s has no messages", sessionID)
	}

	messages := loadResult.Messages

	// Step 3: Validate message pairs if requested.
	var warnings []string
	if opts.ValidateMessages {
		warnings = ValidateMessageHistory(messages)
	}

	// Step 4: Rebuild session metadata from messages and transcript metadata.
	metadata := rebuildMetadata(messages, loadResult, transcriptPath, opts.ProjectDir)

	// Step 5: Apply MaxMessages limit (truncate from the front, keeping newest).
	truncatedAt := 0
	if opts.MaxMessages > 0 && len(messages) > opts.MaxMessages {
		truncatedAt = len(messages) - opts.MaxMessages
		messages = messages[truncatedAt:]
		warnings = append(warnings, fmt.Sprintf(
			"truncated %d oldest messages (MaxMessages=%d)", truncatedAt, opts.MaxMessages,
		))
	}

	// Step 6: Estimate token count.
	tokenEstimate := compact.EstimateTokenCount(messages)

	return &ResumedSession{
		SessionID:     sessionID,
		Messages:      messages,
		Metadata:      metadata,
		TokenEstimate: tokenEstimate,
		TruncatedAt:   truncatedAt,
		Warnings:      warnings,
	}, nil
}

// ValidateMessageHistory checks the structural integrity of a message sequence.
// It verifies role alternation, tool_use/tool_result pairing, and orphaned tool
// results. Returns a list of warnings (not hard errors) describing any issues.
// Mirrors the reference filterUnresolvedToolUses + checkResumeConsistency behavior.
func ValidateMessageHistory(messages []*schema.Message) []string {
	if len(messages) == 0 {
		return nil
	}

	var warnings []string

	// Check 1: Role alternation.
	// The reference enforces user/assistant alternation (with system/tool interspersed).
	// We check that consecutive messages don't have the same user/assistant role
	// (ignoring system and tool messages in the alternation check).
	prevConversationalRole := schema.RoleType("")
	for i, msg := range messages {
		if msg == nil {
			continue
		}
		role := msg.Role
		// Only check alternation for user and assistant roles.
		if role == schema.User || role == schema.Assistant {
			if role == prevConversationalRole {
				warnings = append(warnings, fmt.Sprintf(
					"message[%d]: consecutive %s role (expected alternation)", i, role,
				))
			}
			prevConversationalRole = role
		}
	}

	// Check 2: Tool call / tool result pairing.
	// Collect all tool_call IDs from assistant messages, then check that each
	// has a matching tool result message.
	pendingToolCalls := make(map[string]int) // toolCallID -> message index
	for i, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Assistant {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					pendingToolCalls[tc.ID] = i
				}
			}
		}
		// Tool result messages resolve pending tool calls.
		if msg.Role == schema.Tool && msg.ToolCallID != "" {
			delete(pendingToolCalls, msg.ToolCallID)
		}
	}

	// Report unresolved tool calls.
	for tcID, idx := range pendingToolCalls {
		warnings = append(warnings, fmt.Sprintf(
			"message[%d]: tool_call %q has no matching tool_result", idx, tcID,
		))
	}

	// Check 3: Orphaned tool results (tool results with no prior matching tool call).
	seenToolCalls := make(map[string]bool)
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Assistant {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					seenToolCalls[tc.ID] = true
				}
			}
		}
		if msg.Role == schema.Tool && msg.ToolCallID != "" {
			if !seenToolCalls[msg.ToolCallID] {
				warnings = append(warnings, fmt.Sprintf(
					"orphaned tool_result for tool_call_id %q (no prior tool_use found)", msg.ToolCallID,
				))
			}
		}
	}

	return warnings
}

// TruncateToTokenBudget keeps the newest messages that fit within the given
// token budget. It always preserves at least the last user/assistant pair.
// Returns the truncated message slice and the index in the original slice
// where truncation started (0 if no truncation occurred).
// Mirrors the reference's context window management during resume.
func TruncateToTokenBudget(messages []*schema.Message, maxTokens int) ([]*schema.Message, int) {
	if len(messages) == 0 || maxTokens <= 0 {
		return messages, 0
	}

	totalTokens := compact.EstimateTokenCount(messages)
	if totalTokens <= maxTokens {
		return messages, 0
	}

	// Find the minimum preserve boundary: at least the last user/assistant pair.
	minPreserve := findLastTurnPairStart(messages)

	// Walk backwards from end, accumulating tokens until budget is exceeded.
	accumulated := 0
	cutIndex := len(messages) // start of kept messages
	for i := len(messages) - 1; i >= 0; i-- {
		msgTokens := estimateSingleMessageTokens(messages[i])
		if accumulated+msgTokens > maxTokens && i < minPreserve {
			// Would exceed budget and we're past the minimum preserve boundary.
			cutIndex = i + 1
			break
		}
		accumulated += msgTokens
		cutIndex = i
	}

	// Ensure we never cut past the minimum preserve boundary.
	if cutIndex > minPreserve {
		cutIndex = minPreserve
	}

	if cutIndex <= 0 {
		return messages, 0
	}

	return messages[cutIndex:], cutIndex
}

// --- internal helpers ---

// resolveSessionLocation determines the session directory and ID from options.
func resolveSessionLocation(opts ResumeOptions) (dir, sessionID string, err error) {
	if opts.SessionDir != "" {
		// Direct path provided — extract session ID from directory contents.
		dir = opts.SessionDir
		sessionID = opts.SessionID
		if sessionID == "" {
			// Try to infer session ID from .jsonl files in the directory.
			sessionID, err = inferSessionIDFromDir(dir)
			if err != nil {
				return "", "", err
			}
		}
		if !isValidSessionFileID(sessionID) {
			return "", "", fmt.Errorf("invalid session ID %q", sessionID)
		}
		return dir, sessionID, nil
	}

	if opts.SessionID != "" {
		if !isValidSessionFileID(opts.SessionID) {
			return "", "", fmt.Errorf("invalid session ID %q", opts.SessionID)
		}
		// Locate by session ID using the standard session directory.
		dir = GetSessionDir(opts.ProjectDir)
		return dir, opts.SessionID, nil
	}

	return "", "", errors.New("either SessionID or SessionDir must be provided")
}

// inferSessionIDFromDir finds the most recent .jsonl file in a directory and
// extracts the session ID from its filename.
func inferSessionIDFromDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read session dir %s: %w", dir, err)
	}

	var bestID string
	var bestTime time.Time

	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".jsonl" {
			continue
		}
		id := name[:len(name)-len(".jsonl")]
		if !isValidSessionFileID(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if bestID == "" || info.ModTime().After(bestTime) {
			bestID = id
			bestTime = info.ModTime()
		}
	}

	if bestID == "" {
		return "", errors.New("no valid session transcript found in directory")
	}
	return bestID, nil
}

// rebuildMetadata extracts session metadata from messages, transcript metadata,
// and file stat.
func rebuildMetadata(messages []*schema.Message, loadResult *transcript.LoadResult, transcriptPath, projectDir string) SessionMetadata {
	meta := SessionMetadata{
		ProjectDir:   projectDir,
		MessageCount: len(messages),
		IsLeaf:       true, // Default to leaf until we discover otherwise.
	}

	// Extract timing from file stat.
	if info, err := os.Stat(transcriptPath); err == nil {
		meta.LastActiveAt = info.ModTime()
	}

	// Count turns and detect compact boundaries.
	turnCount := 0
	compactCount := 0
	if loadResult != nil {
		for _, boundary := range loadResult.LifecycleBoundaries {
			if boundary.Kind == transcript.LifecycleCompact {
				compactCount++
			}
		}
	}
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.User {
			turnCount++
		}
		// Detect compact boundary markers (system messages with compact_boundary subtype).
		if compactCount == 0 && msg.Role == schema.System && msg.Extra != nil {
			if subtype, ok := msg.Extra["subtype"]; ok {
				if subtype == "compact_boundary" {
					compactCount++
				}
			}
		}
	}
	meta.TurnCount = turnCount
	meta.CompactCount = compactCount

	// Try to extract CreatedAt from the first message's extra metadata or file creation.
	if len(messages) > 0 && messages[0] != nil && messages[0].Extra != nil {
		if ts, ok := messages[0].Extra["timestamp"]; ok {
			switch v := ts.(type) {
			case string:
				if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
					meta.CreatedAt = t
				} else if t, err := time.Parse(time.RFC3339, v); err == nil {
					meta.CreatedAt = t
				}
			case time.Time:
				meta.CreatedAt = v
			}
		}
	}
	// Fallback: if CreatedAt is zero, use LastActiveAt as approximation.
	if meta.CreatedAt.IsZero() && !meta.LastActiveAt.IsZero() {
		meta.CreatedAt = meta.LastActiveAt
	}

	// Try to extract model and provider from messages (look for model/provider fields in extra).
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil || msg.Extra == nil {
			continue
		}
		if meta.Model == "" {
			if model, ok := msg.Extra["model"]; ok {
				if s, ok := model.(string); ok && s != "" {
					meta.Model = s
				}
			}
		}
		if meta.Provider == "" {
			if provider, ok := msg.Extra["provider"]; ok {
				if s, ok := provider.(string); ok && s != "" {
					meta.Provider = s
				}
			}
		}
		if meta.Model != "" && meta.Provider != "" {
			break
		}
	}

	// Extract metadata from transcript metadata entries.
	if loadResult != nil {
		for _, m := range loadResult.Metadata {
			switch m.Key {
			case "parent_session_id":
				meta.ParentSessionID = m.Value
			case "branch_point":
				_, _ = fmt.Sscanf(m.Value, "%d", &meta.BranchPoint)
			case "model":
				if meta.Model == "" {
					meta.Model = m.Value
				}
			case "provider":
				if meta.Provider == "" {
					meta.Provider = m.Value
				}
			case "git_branch":
				meta.GitBranch = m.Value
			case "cwd":
				meta.CWD = m.Value
			}
		}

		// Check for full metadata entry (last-wins).
		if full := ReadSessionMetadataFull(loadResult); full != nil {
			if meta.ParentSessionID == "" {
				meta.ParentSessionID = full.ParentSessionID
			}
			if meta.BranchPoint == 0 {
				meta.BranchPoint = full.BranchPoint
			}
			if meta.Model == "" {
				meta.Model = full.Model
			}
			if meta.Provider == "" {
				meta.Provider = full.Provider
			}
			if meta.GitBranch == "" {
				meta.GitBranch = full.GitBranch
			}
			if meta.CWD == "" {
				meta.CWD = full.CWD
			}
			meta.ThreadID = full.ThreadID
			meta.AgentID = full.AgentID
			meta.AgentGeneration = full.AgentGeneration
			meta.AgentName = full.AgentName
			meta.AgentRole = full.AgentRole
			meta.ModelRole = full.ModelRole
			meta.ParentThreadID = full.ParentThreadID
			meta.ParentAgentID = full.ParentAgentID
			meta.ParentToolUseID = full.ParentToolUseID
			meta.PermissionMode = full.PermissionMode
			meta.QueryKernelVersion = full.QueryKernelVersion
			meta.QueryKernelStage = full.QueryKernelStage
			meta.QueryKernelIncompatibility = full.QueryKernelIncompatibility
			if full.PlanState != nil {
				planState := *full.PlanState
				meta.PlanState = &planState
			}
			if full.GoalState != nil {
				goalState := *full.GoalState
				if full.GoalState.TokenBudget != nil {
					tokenBudget := *full.GoalState.TokenBudget
					goalState.TokenBudget = &tokenBudget
				}
				if full.GoalState.PendingUsageAdmission != nil {
					admission := *full.GoalState.PendingUsageAdmission
					goalState.PendingUsageAdmission = &admission
				}
				if full.GoalState.Continuation != nil {
					continuation := *full.GoalState.Continuation
					if full.GoalState.Continuation.TokenBudget != nil {
						tokenBudget := *full.GoalState.Continuation.TokenBudget
						continuation.TokenBudget = &tokenBudget
					}
					goalState.Continuation = &continuation
				}
				goalState.BlockerTurnIDs = append(
					[]string(nil),
					full.GoalState.BlockerTurnIDs...,
				)
				meta.GoalState = &goalState
			}
			if full.GoalBinding != nil {
				goalBinding := *full.GoalBinding
				meta.GoalBinding = &goalBinding
			}
			meta.ModelBinding = full.ModelBinding.Clone()
			if full.GraphInterrupt != nil {
				graphInterrupt := *full.GraphInterrupt
				meta.GraphInterrupt = &graphInterrupt
			}
			meta.WorktreePath = full.WorktreePath
			meta.WorktreeBranch = full.WorktreeBranch
			meta.AdditionalDirs = append([]string(nil), full.AdditionalDirs...)
			meta.AgentIDs = append([]string(nil), full.AgentIDs...)
			meta.PendingRequestIDs = append([]string(nil), full.PendingRequestIDs...)
			meta.RuntimeRevision = full.RuntimeRevision
			meta.Status = full.Status
			meta.IsLeaf = full.IsLeaf
			if !full.CreatedAt.IsZero() {
				meta.CreatedAt = full.CreatedAt
			}
			if !full.UpdatedAt.IsZero() {
				meta.LastActiveAt = full.UpdatedAt
			}
			if full.TokenUsage > 0 {
				meta.TokenUsage = full.TokenUsage
			}
		}
	}

	// Estimate token usage if not already set from metadata.
	if meta.TokenUsage == 0 {
		meta.TokenUsage = compact.EstimateTokenCount(messages)
	}

	return meta
}

// findLastTurnPairStart finds the start index of the last user/assistant pair.
// Returns the index of the last user message that is followed (possibly not
// immediately) by an assistant message. If no pair exists, returns len-1 to
// preserve at least the last message.
func findLastTurnPairStart(messages []*schema.Message) int {
	if len(messages) == 0 {
		return 0
	}

	// Walk backwards to find the last assistant message.
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role == schema.Assistant {
			lastAssistantIdx = i
			break
		}
	}
	if lastAssistantIdx < 0 {
		// No assistant message at all — preserve everything from the last message.
		return len(messages) - 1
	}

	// Find the user message preceding this assistant.
	for i := lastAssistantIdx - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role == schema.User {
			return i
		}
	}

	// No preceding user message found — preserve from the assistant.
	return lastAssistantIdx
}

// estimateSingleMessageTokens estimates tokens for a single message using the
// compact package's heuristic.
func estimateSingleMessageTokens(msg *schema.Message) int {
	if msg == nil {
		return 0
	}
	return compact.EstimateTokenCount([]*schema.Message{msg})
}
