package workboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	AuthorityRecordVersion   = 2
	AuthorityRecordVersionV3 = 3
	AuthorityMarkerVersion   = 1
	AuthorityMarkerVersionV2 = 2
	LegacyBackupVersion      = 1
	MinimumReaderV2          = "workboard/v2"
	MinimumReaderV3          = "workboard/v3"
	MaxExecutionLinks        = 4_096

	AuthorityRecordSuffix = ".workboard-v2.json"
	AuthorityMarkerSuffix = ".workboard-authority-v1.json"
	LegacyBackupSuffix    = ".workboard-legacy-backup-v1.json"
)

// TaskCompatibility retains the exact legacy Task projection when the
// canonical board must use a stricter status or dependency shape.
type TaskCompatibility struct {
	ID                  string          `json:"id"`
	Subject             string          `json:"subject"`
	Description         string          `json:"description"`
	ActiveForm          string          `json:"active_form,omitempty"`
	LegacyStatus        string          `json:"legacy_status"`
	Owner               string          `json:"owner,omitempty"`
	Blocks              []string        `json:"blocks,omitempty"`
	BlockedBy           []string        `json:"blocked_by,omitempty"`
	UnresolvedBlocks    []string        `json:"unresolved_blocks,omitempty"`
	UnresolvedBlockedBy []string        `json:"unresolved_blocked_by,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	Output              string          `json:"output,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// TodoScopeCompatibility identifies the ordered current legacy Todo view.
// Completed and omitted WorkItems may remain on the canonical board without
// appearing in CurrentItemIDs.
type TodoScopeCompatibility struct {
	SessionID      string   `json:"session_id"`
	AgentID        string   `json:"agent_id,omitempty"`
	CurrentItemIDs []string `json:"current_item_ids"`
}

// CompatibilityPayload is typed authority data. It is never embedded into
// user-controlled Task metadata.
type CompatibilityPayload struct {
	NextTaskID int                      `json:"next_task_id"`
	Tasks      []TaskCompatibility      `json:"tasks"`
	TodoScopes []TodoScopeCompatibility `json:"todo_scopes"`
}

// AuthorityRecord is the sole logical-work record after a marker commit.
type AuthorityRecord struct {
	Version        int                  `json:"version"`
	SessionID      string               `json:"session_id"`
	BoardID        string               `json:"board_id"`
	Board          Board                `json:"board"`
	Compatibility  CompatibilityPayload `json:"compatibility"`
	Diagnostics    []Diagnostic         `json:"diagnostics,omitempty"`
	ExecutionLinks []ExecutionLink      `json:"execution_links,omitempty"`
}

// ExecutionLink is an immutable admission of one delegated execution attempt.
// It is intentionally independent of runner state; later engine layers own dispatch.
type ExecutionLink struct {
	BoardID          string    `json:"board_id"`
	WorkItemID       string    `json:"work_item_id"`
	WorkItemRevision uint64    `json:"work_item_revision"`
	AgentID          string    `json:"agent_id"`
	Generation       uint64    `json:"generation"`
	Actor            string    `json:"actor"`
	ParentSessionID  string    `json:"parent_session_id"`
	ParentThreadID   string    `json:"parent_thread_id"`
	ParentAgentID    string    `json:"parent_agent_id"`
	ParentToolUseID  string    `json:"parent_tool_use_id"`
	AdmittedAt       time.Time `json:"admitted_at"`
}

// AuthorityMarker is the forward-only reader-floor commit point. It
// intentionally contains neither BoardID nor a mutable revision.
type AuthorityMarker struct {
	Version       int    `json:"version"`
	SessionID     string `json:"session_id"`
	MinimumReader string `json:"minimum_reader"`
}

// LegacyBackup is the immutable cutover or fork-time recovery baseline.
type LegacyBackup struct {
	Version       int                  `json:"version"`
	SessionID     string               `json:"session_id"`
	BoardID       string               `json:"board_id"`
	Board         Board                `json:"board"`
	Compatibility CompatibilityPayload `json:"compatibility"`
}

func cloneAuthorityRecord(record AuthorityRecord) AuthorityRecord {
	cloned := record
	cloned.Board = cloneBoard(record.Board)
	cloned.Compatibility = cloneCompatibility(record.Compatibility)
	cloned.Diagnostics = append([]Diagnostic(nil), record.Diagnostics...)
	cloned.ExecutionLinks = append([]ExecutionLink(nil), record.ExecutionLinks...)
	return cloned
}

func cloneLegacyBackup(backup LegacyBackup) LegacyBackup {
	cloned := backup
	cloned.Board = cloneBoard(backup.Board)
	cloned.Compatibility = cloneCompatibility(backup.Compatibility)
	return cloned
}

func cloneCompatibility(payload CompatibilityPayload) CompatibilityPayload {
	cloned := payload
	cloned.Tasks = make([]TaskCompatibility, len(payload.Tasks))
	for index := range payload.Tasks {
		cloned.Tasks[index] = cloneTaskCompatibility(payload.Tasks[index])
	}
	cloned.TodoScopes = make(
		[]TodoScopeCompatibility,
		len(payload.TodoScopes),
	)
	for index := range payload.TodoScopes {
		cloned.TodoScopes[index] = payload.TodoScopes[index]
		cloned.TodoScopes[index].CurrentItemIDs = append(
			[]string(nil),
			payload.TodoScopes[index].CurrentItemIDs...,
		)
	}
	return cloned
}

func cloneTaskCompatibility(task TaskCompatibility) TaskCompatibility {
	cloned := task
	cloned.Blocks = append([]string(nil), task.Blocks...)
	cloned.BlockedBy = append([]string(nil), task.BlockedBy...)
	cloned.UnresolvedBlocks = append([]string(nil), task.UnresolvedBlocks...)
	cloned.UnresolvedBlockedBy = append(
		[]string(nil),
		task.UnresolvedBlockedBy...,
	)
	cloned.Metadata = append(json.RawMessage(nil), task.Metadata...)
	return cloned
}

func validateAuthorityRecord(
	record AuthorityRecord,
	expectedSessionID string,
) error {
	if record.Version != AuthorityRecordVersion && record.Version != AuthorityRecordVersionV3 {
		return fmt.Errorf(
			"workboard authority: unsupported record version %d",
			record.Version,
		)
	}
	if err := validateAuthorityIdentity(
		record.SessionID,
		record.BoardID,
		expectedSessionID,
	); err != nil {
		return err
	}
	if record.Board.Revision == 0 {
		return fmt.Errorf("workboard authority: board revision is zero")
	}
	if err := validateBoard(record.Board); err != nil {
		return fmt.Errorf("workboard authority: %w", err)
	}
	if err := validateCompatibility(record.Board, record.Compatibility); err != nil {
		return err
	}
	if err := validateAuthorityDiagnostics(record.Diagnostics); err != nil {
		return err
	}
	return validateExecutionLinks(record)
}

func validateLegacyBackup(
	backup LegacyBackup,
	expectedSessionID string,
) error {
	if backup.Version != LegacyBackupVersion {
		return fmt.Errorf(
			"workboard authority: unsupported backup version %d",
			backup.Version,
		)
	}
	if err := validateAuthorityIdentity(
		backup.SessionID,
		backup.BoardID,
		expectedSessionID,
	); err != nil {
		return err
	}
	if backup.Board.Revision == 0 {
		return fmt.Errorf("workboard authority: backup board revision is zero")
	}
	if err := validateBoard(backup.Board); err != nil {
		return fmt.Errorf("workboard authority: %w", err)
	}
	return validateCompatibility(backup.Board, backup.Compatibility)
}

func validateAuthorityMarker(
	marker AuthorityMarker,
	expectedSessionID string,
) error {
	if marker.Version != AuthorityMarkerVersion && marker.Version != AuthorityMarkerVersionV2 {
		return fmt.Errorf(
			"workboard authority: unsupported marker version %d",
			marker.Version,
		)
	}
	if strings.TrimSpace(marker.SessionID) == "" {
		return fmt.Errorf("workboard authority: marker SessionID is empty")
	}
	if expectedSessionID != "" && marker.SessionID != expectedSessionID {
		return fmt.Errorf(
			"workboard authority: marker SessionID mismatch: got %q",
			marker.SessionID,
		)
	}
	if len(marker.SessionID) > MaxFieldBytes {
		return fmt.Errorf(
			"workboard authority: marker SessionID exceeds %d bytes",
			MaxFieldBytes,
		)
	}
	if (marker.Version == AuthorityMarkerVersion && marker.MinimumReader != MinimumReaderV2) ||
		(marker.Version == AuthorityMarkerVersionV2 && marker.MinimumReader != MinimumReaderV3) {
		return fmt.Errorf(
			"workboard authority: unsupported minimum reader %q",
			marker.MinimumReader,
		)
	}
	return nil
}

func validateExecutionLinks(record AuthorityRecord) error {
	if record.Version == AuthorityRecordVersion {
		if len(record.ExecutionLinks) != 0 {
			return fmt.Errorf("workboard authority: v2 record has execution links")
		}
		return nil
	}
	if len(record.ExecutionLinks) == 0 {
		return fmt.Errorf("workboard authority: v3 record has no execution links")
	}
	if len(record.ExecutionLinks) > MaxExecutionLinks {
		return fmt.Errorf("workboard authority: execution links exceed limit %d", MaxExecutionLinks)
	}
	items := make(map[string]WorkItem, len(record.Board.Items))
	for _, item := range record.Board.Items {
		items[item.ID] = item
	}
	keys := make(map[string]ExecutionLink, len(record.ExecutionLinks))
	for _, link := range record.ExecutionLinks {
		if link.BoardID != record.BoardID || link.Actor != "agent_launch_admission" || link.Generation == 0 || link.AdmittedAt.IsZero() || link.AdmittedAt.Location() != time.UTC || strings.TrimSpace(link.WorkItemID) == "" || strings.TrimSpace(link.AgentID) == "" || strings.TrimSpace(link.ParentSessionID) == "" || strings.TrimSpace(link.ParentThreadID) == "" || strings.TrimSpace(link.ParentToolUseID) == "" {
			return fmt.Errorf("workboard authority: invalid execution link")
		}
		for field, value := range map[string]string{
			"board ID":           link.BoardID,
			"WorkItem ID":        link.WorkItemID,
			"Agent ID":           link.AgentID,
			"actor":              link.Actor,
			"parent Session ID":  link.ParentSessionID,
			"parent thread ID":   link.ParentThreadID,
			"parent Agent ID":    link.ParentAgentID,
			"parent tool-use ID": link.ParentToolUseID,
		} {
			if len(value) > MaxFieldBytes {
				return fmt.Errorf(
					"workboard authority: execution link %s exceeds %d bytes",
					field,
					MaxFieldBytes,
				)
			}
		}
		if _, ok := items[link.WorkItemID]; !ok || link.WorkItemRevision == 0 {
			return fmt.Errorf("workboard authority: execution link WorkItem revision mismatch")
		}
		key := link.AgentID + "\x00" + strconv.FormatUint(link.Generation, 10)
		if prior, ok := keys[key]; ok {
			if prior != link {
				return fmt.Errorf("workboard authority: conflicting execution link key")
			}
			return fmt.Errorf("workboard authority: duplicate execution link key")
		}
		keys[key] = link
	}
	return nil
}

func validateAuthorityIdentity(
	sessionID string,
	boardID string,
	expectedSessionID string,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("workboard authority: SessionID is empty")
	}
	if expectedSessionID != "" && sessionID != expectedSessionID {
		return fmt.Errorf(
			"workboard authority: SessionID mismatch: got %q",
			sessionID,
		)
	}
	if strings.TrimSpace(boardID) == "" {
		return fmt.Errorf("workboard authority: BoardID is empty")
	}
	if len(sessionID) > MaxFieldBytes || len(boardID) > MaxFieldBytes {
		return fmt.Errorf(
			"workboard authority: identity exceeds %d bytes",
			MaxFieldBytes,
		)
	}
	return nil
}

func validateCompatibility(board Board, payload CompatibilityPayload) error {
	if payload.NextTaskID <= 0 {
		return fmt.Errorf("workboard authority: next Task ID must be positive")
	}
	if len(payload.Tasks) > MaxItems {
		return fmt.Errorf(
			"workboard authority: compatibility tasks exceed limit %d",
			MaxItems,
		)
	}
	items := make(map[string]WorkItem, len(board.Items))
	taskItems := make(map[string]WorkItem)
	for _, item := range board.Items {
		items[item.ID] = item
		if item.Source.Kind == "task" {
			taskItems[item.Source.LegacyID] = item
		}
	}
	seenTasks := make(map[string]struct{}, len(payload.Tasks))
	maxTaskID := 0
	for index, task := range payload.Tasks {
		if strings.TrimSpace(task.ID) == "" {
			return fmt.Errorf(
				"workboard authority: compatibility task %d ID is empty",
				index,
			)
		}
		if _, duplicate := seenTasks[task.ID]; duplicate {
			return fmt.Errorf(
				"workboard authority: duplicate compatibility task %q",
				task.ID,
			)
		}
		numericID, err := strconv.Atoi(task.ID)
		if err != nil || numericID <= 0 {
			return fmt.Errorf(
				"workboard authority: compatibility task %q ID is not positive numeric",
				task.ID,
			)
		}
		if numericID > maxTaskID {
			maxTaskID = numericID
		}
		seenTasks[task.ID] = struct{}{}
	}
	if payload.NextTaskID <= maxTaskID {
		return fmt.Errorf(
			"workboard authority: next Task ID %d does not follow existing Task %d",
			payload.NextTaskID,
			maxTaskID,
		)
	}
	dependencyCount := 0
	for _, task := range payload.Tasks {
		item, exists := taskItems[task.ID]
		if !exists {
			return fmt.Errorf(
				"workboard authority: compatibility task %q has no WorkItem",
				task.ID,
			)
		}
		if item.Status != canonicalStatusForLegacy(task.LegacyStatus) {
			return fmt.Errorf(
				"workboard authority: compatibility task %q status mismatch",
				task.ID,
			)
		}
		for field, value := range map[string]string{
			"ID":            task.ID,
			"subject":       task.Subject,
			"description":   task.Description,
			"active form":   task.ActiveForm,
			"legacy status": task.LegacyStatus,
			"owner":         task.Owner,
			"output":        task.Output,
		} {
			if len(value) > MaxFieldBytes {
				return fmt.Errorf(
					"workboard authority: task %q %s exceeds %d bytes",
					task.ID,
					field,
					MaxFieldBytes,
				)
			}
		}
		if task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
			return fmt.Errorf(
				"workboard authority: task %q timestamp is zero",
				task.ID,
			)
		}
		if len(task.Metadata) > MaxFieldBytes {
			return fmt.Errorf(
				"workboard authority: task %q metadata exceeds %d bytes",
				task.ID,
				MaxFieldBytes,
			)
		}
		if len(task.Metadata) > 0 && !json.Valid(task.Metadata) {
			return fmt.Errorf(
				"workboard authority: task %q metadata is invalid JSON",
				task.ID,
			)
		}
		if len(task.Metadata) > 0 {
			var metadata map[string]any
			if len(bytes.TrimSpace(task.Metadata)) == 0 ||
				bytes.TrimSpace(task.Metadata)[0] != '{' ||
				json.Unmarshal(task.Metadata, &metadata) != nil {
				return fmt.Errorf(
					"workboard authority: task %q metadata is not an object",
					task.ID,
				)
			}
		}
		dependencies := [][]string{
			task.Blocks,
			task.BlockedBy,
			task.UnresolvedBlocks,
			task.UnresolvedBlockedBy,
		}
		for _, group := range dependencies {
			dependencyCount += len(group)
			seenDependencies := make(map[string]struct{}, len(group))
			for _, dependency := range group {
				if strings.TrimSpace(dependency) == "" {
					return fmt.Errorf(
						"workboard authority: task %q has empty dependency ID",
						task.ID,
					)
				}
				if len(dependency) > MaxFieldBytes {
					return fmt.Errorf(
						"workboard authority: task %q dependency exceeds %d bytes",
						task.ID,
						MaxFieldBytes,
					)
				}
				if _, duplicate := seenDependencies[dependency]; duplicate {
					return fmt.Errorf(
						"workboard authority: task %q has duplicate dependency %q",
						task.ID,
						dependency,
					)
				}
				seenDependencies[dependency] = struct{}{}
			}
		}
		if !stringSubset(task.UnresolvedBlocks, task.Blocks) ||
			!stringSubset(task.UnresolvedBlockedBy, task.BlockedBy) {
			return fmt.Errorf(
				"workboard authority: task %q unresolved dependency is not in the legacy projection",
				task.ID,
			)
		}
		expectedItem := workItemFromTask(task, item.Order)
		expectedItem.Revision = item.Revision
		if !sameWorkItemContent(item, expectedItem) {
			return fmt.Errorf(
				"workboard authority: task %q compatibility does not match its WorkItem",
				task.ID,
			)
		}
	}
	if dependencyCount > MaxDependencyRefs {
		return fmt.Errorf(
			"workboard authority: dependency references exceed limit %d",
			MaxDependencyRefs,
		)
	}
	if len(taskItems) != len(seenTasks) {
		return fmt.Errorf(
			"workboard authority: WorkBoard Task compatibility is incomplete",
		)
	}
	seenScopes := make(map[string]struct{}, len(payload.TodoScopes))
	for _, scope := range payload.TodoScopes {
		if strings.TrimSpace(scope.SessionID) == "" {
			return fmt.Errorf("workboard authority: Todo scope SessionID is empty")
		}
		scopeKey := scope.SessionID + "\x00" + scope.AgentID
		if _, duplicate := seenScopes[scopeKey]; duplicate {
			return fmt.Errorf(
				"workboard authority: duplicate Todo scope %q",
				scopeKey,
			)
		}
		seenScopes[scopeKey] = struct{}{}
		if len(scope.SessionID) > MaxFieldBytes ||
			len(scope.AgentID) > MaxFieldBytes {
			return fmt.Errorf(
				"workboard authority: Todo scope exceeds %d bytes",
				MaxFieldBytes,
			)
		}
		seenCurrent := make(map[string]struct{}, len(scope.CurrentItemIDs))
		for _, itemID := range scope.CurrentItemIDs {
			if strings.TrimSpace(itemID) == "" {
				return fmt.Errorf(
					"workboard authority: Todo item identity is empty",
				)
			}
			if len(itemID) > MaxFieldBytes {
				return fmt.Errorf(
					"workboard authority: Todo item identity exceeds %d bytes",
					MaxFieldBytes,
				)
			}
			if _, duplicate := seenCurrent[itemID]; duplicate {
				return fmt.Errorf(
					"workboard authority: duplicate current Todo item %q",
					itemID,
				)
			}
			seenCurrent[itemID] = struct{}{}
			item, exists := items[itemID]
			if !exists ||
				item.Source.Kind != "todo" ||
				item.Source.SessionID != scope.SessionID ||
				item.Source.AgentID != scope.AgentID {
				return fmt.Errorf(
					"workboard authority: Todo item %q does not match its scope",
					itemID,
				)
			}
		}
	}
	for _, item := range board.Items {
		if item.Source.Kind != "todo" {
			continue
		}
		scopeKey := item.Source.SessionID + "\x00" + item.Source.AgentID
		if _, exists := seenScopes[scopeKey]; !exists {
			return fmt.Errorf(
				"workboard authority: Todo WorkItem %q has no compatibility scope",
				item.ID,
			)
		}
	}
	return nil
}

func stringSubset(subset, set []string) bool {
	allowed := make(map[string]struct{}, len(set))
	for _, value := range set {
		allowed[value] = struct{}{}
	}
	for _, value := range subset {
		if _, exists := allowed[value]; !exists {
			return false
		}
	}
	return true
}

func canonicalStatusForLegacy(status string) Status {
	switch status {
	case string(StatusPending):
		return StatusPending
	case string(StatusInProgress), "running":
		return StatusInProgress
	case string(StatusCompleted):
		return StatusCompleted
	case string(StatusFailed):
		return StatusFailed
	default:
		return StatusCancelled
	}
}

func validateAuthorityDiagnostics(diagnostics []Diagnostic) error {
	if len(diagnostics) > MaxDiagnostics {
		return fmt.Errorf(
			"workboard authority: diagnostics exceed limit %d",
			MaxDiagnostics,
		)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Sequence == 0 {
			return fmt.Errorf(
				"workboard authority: diagnostic sequence is zero",
			)
		}
		if strings.TrimSpace(diagnostic.Kind) == "" {
			return fmt.Errorf("workboard authority: diagnostic kind is empty")
		}
		for field, value := range map[string]string{
			"kind":    diagnostic.Kind,
			"stage":   diagnostic.Stage,
			"item ID": diagnostic.ItemID,
			"message": diagnostic.Message,
		} {
			if len(value) > MaxFieldBytes {
				return fmt.Errorf(
					"workboard authority: diagnostic %s exceeds %d bytes",
					field,
					MaxFieldBytes,
				)
			}
		}
	}
	return nil
}
