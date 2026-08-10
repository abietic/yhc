package workboard

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	CurrentVersion = 1

	MaxItems            = 1_024
	MaxDependencyRefs   = 4_096
	MaxFieldBytes       = 64 * 1_024
	MaxEncodedJSONBytes = 4 * 1_024 * 1_024
	MaxDiagnostics      = 128
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

type SourcePartition struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	LegacyID  string `json:"legacy_id,omitempty"`
}

type WorkItem struct {
	ID             string          `json:"id"`
	Revision       uint64          `json:"revision"`
	Source         SourcePartition `json:"source"`
	Order          int             `json:"order"`
	Title          string          `json:"title"`
	Description    string          `json:"description,omitempty"`
	ActiveForm     string          `json:"active_form,omitempty"`
	Status         Status          `json:"status"`
	Owner          string          `json:"owner,omitempty"`
	Blocks         []string        `json:"blocks,omitempty"`
	BlockedBy      []string        `json:"blocked_by,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	ResultSummary  string          `json:"result_summary,omitempty"`
	TerminalReason string          `json:"terminal_reason,omitempty"`
}

type Board struct {
	Revision   uint64     `json:"revision"`
	NextTodoID uint64     `json:"next_todo_id"`
	Items      []WorkItem `json:"items"`
}

type Diagnostic struct {
	Sequence uint64 `json:"sequence"`
	Kind     string `json:"kind"`
	Stage    string `json:"stage,omitempty"`
	ItemID   string `json:"item_id,omitempty"`
	Message  string `json:"message"`
}

type Record struct {
	Version     int          `json:"version"`
	SessionID   string       `json:"session_id"`
	BoardID     string       `json:"board_id"`
	Candidate   *Board       `json:"candidate"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (r Record) clone() Record {
	cloned := r
	if r.Candidate != nil {
		board := cloneBoard(*r.Candidate)
		cloned.Candidate = &board
	}
	cloned.Diagnostics = append([]Diagnostic(nil), r.Diagnostics...)
	return cloned
}

func cloneBoard(board Board) Board {
	cloned := board
	cloned.Items = make([]WorkItem, len(board.Items))
	for index := range board.Items {
		cloned.Items[index] = cloneWorkItem(board.Items[index])
	}
	return cloned
}

func cloneWorkItem(item WorkItem) WorkItem {
	cloned := item
	cloned.Blocks = append([]string(nil), item.Blocks...)
	cloned.BlockedBy = append([]string(nil), item.BlockedBy...)
	cloned.Metadata = append(json.RawMessage(nil), item.Metadata...)
	return cloned
}

func validStatus(status Status) bool {
	switch status {
	case StatusPending,
		StatusInProgress,
		StatusCompleted,
		StatusFailed,
		StatusCancelled:
		return true
	default:
		return false
	}
}

func validateRecord(record Record, expectedSessionID string) error {
	if record.Version != CurrentVersion {
		return fmt.Errorf("workboard: unsupported version %d", record.Version)
	}
	if strings.TrimSpace(record.SessionID) == "" {
		return fmt.Errorf("workboard: session identity is empty")
	}
	if expectedSessionID != "" && record.SessionID != expectedSessionID {
		return fmt.Errorf(
			"workboard: session identity mismatch: got %q",
			record.SessionID,
		)
	}
	if strings.TrimSpace(record.BoardID) == "" {
		return fmt.Errorf("workboard: board identity is empty")
	}
	if len(record.SessionID) > MaxFieldBytes ||
		len(record.BoardID) > MaxFieldBytes {
		return fmt.Errorf(
			"workboard: record identity exceeds %d bytes",
			MaxFieldBytes,
		)
	}
	if record.Candidate == nil {
		return fmt.Errorf("workboard: candidate is missing")
	}
	if len(record.Diagnostics) > MaxDiagnostics {
		return fmt.Errorf(
			"workboard: diagnostics exceed limit %d",
			MaxDiagnostics,
		)
	}
	for _, diagnostic := range record.Diagnostics {
		if diagnostic.Sequence == 0 {
			return fmt.Errorf("workboard: diagnostic sequence is zero")
		}
		if strings.TrimSpace(diagnostic.Kind) == "" {
			return fmt.Errorf("workboard: diagnostic kind is empty")
		}
		for field, value := range map[string]string{
			"diagnostic kind":    diagnostic.Kind,
			"diagnostic stage":   diagnostic.Stage,
			"diagnostic item ID": diagnostic.ItemID,
			"diagnostic message": diagnostic.Message,
		} {
			if len(value) > MaxFieldBytes {
				return fmt.Errorf(
					"workboard: %s exceeds %d bytes",
					field,
					MaxFieldBytes,
				)
			}
		}
	}
	return validateBoard(*record.Candidate)
}

func validateBoard(board Board) error {
	if len(board.Items) > MaxItems {
		return fmt.Errorf("workboard: items exceed limit %d", MaxItems)
	}
	ids := make(map[string]struct{}, len(board.Items))
	dependencyCount := 0
	for index := range board.Items {
		item := board.Items[index]
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("workboard: item %d identity is empty", index)
		}
		if _, duplicate := ids[item.ID]; duplicate {
			return fmt.Errorf("workboard: duplicate item identity %q", item.ID)
		}
		ids[item.ID] = struct{}{}
		if item.Revision == 0 {
			return fmt.Errorf("workboard: item %q revision is zero", item.ID)
		}
		if item.Source.Kind != "task" && item.Source.Kind != "todo" {
			return fmt.Errorf(
				"workboard: item %q source kind %q is invalid",
				item.ID,
				item.Source.Kind,
			)
		}
		if item.Source.Kind == "task" &&
			strings.TrimSpace(item.Source.LegacyID) == "" {
			return fmt.Errorf("workboard: task item %q has no legacy ID", item.ID)
		}
		if item.Source.Kind == "todo" &&
			strings.TrimSpace(item.Source.SessionID) == "" {
			return fmt.Errorf("workboard: todo item %q has no session ID", item.ID)
		}
		if !validStatus(item.Status) {
			return fmt.Errorf(
				"workboard: item %q status %q is invalid",
				item.ID,
				item.Status,
			)
		}
		for field, value := range map[string]string{
			"ID":               item.ID,
			"title":            item.Title,
			"description":      item.Description,
			"active form":      item.ActiveForm,
			"owner":            item.Owner,
			"result summary":   item.ResultSummary,
			"terminal reason":  item.TerminalReason,
			"source session":   item.Source.SessionID,
			"source agent":     item.Source.AgentID,
			"source legacy ID": item.Source.LegacyID,
		} {
			if len(value) > MaxFieldBytes {
				return fmt.Errorf(
					"workboard: item %q %s exceeds %d bytes",
					item.ID,
					field,
					MaxFieldBytes,
				)
			}
		}
		if len(item.Metadata) > MaxFieldBytes {
			return fmt.Errorf(
				"workboard: item %q metadata exceeds %d bytes",
				item.ID,
				MaxFieldBytes,
			)
		}
		if len(item.Metadata) > 0 && !json.Valid(item.Metadata) {
			return fmt.Errorf("workboard: item %q metadata is invalid JSON", item.ID)
		}
		dependencyCount += len(item.Blocks) + len(item.BlockedBy)
		if dependencyCount > MaxDependencyRefs {
			return fmt.Errorf(
				"workboard: dependency references exceed limit %d",
				MaxDependencyRefs,
			)
		}
	}
	for index := range board.Items {
		item := board.Items[index]
		for _, dependency := range append(
			append([]string(nil), item.Blocks...),
			item.BlockedBy...,
		) {
			if len(dependency) > MaxFieldBytes {
				return fmt.Errorf(
					"workboard: item %q dependency identity exceeds %d bytes",
					item.ID,
					MaxFieldBytes,
				)
			}
			if dependency == item.ID {
				return fmt.Errorf(
					"workboard: item %q has a self dependency",
					item.ID,
				)
			}
			if _, exists := ids[dependency]; !exists {
				return fmt.Errorf(
					"workboard: item %q references missing item %q",
					item.ID,
					dependency,
				)
			}
		}
	}
	if cycle := firstDependencyCycle(board.Items); cycle != "" {
		return fmt.Errorf("workboard: dependency cycle includes item %q", cycle)
	}
	return nil
}

func firstDependencyCycle(items []WorkItem) string {
	edgeSets := make(map[string]map[string]struct{}, len(items))
	for _, item := range items {
		edgeSets[item.ID] = make(map[string]struct{})
	}
	for _, item := range items {
		for _, blocked := range item.Blocks {
			edgeSets[item.ID][blocked] = struct{}{}
		}
		for _, blocker := range item.BlockedBy {
			edgeSets[blocker][item.ID] = struct{}{}
		}
	}
	edges := make(map[string][]string, len(edgeSets))
	for id, set := range edgeSets {
		for target := range set {
			edges[id] = append(edges[id], target)
		}
		sort.Strings(edges[id])
	}
	state := make(map[string]uint8, len(items))
	var visit func(string) string
	visit = func(id string) string {
		switch state[id] {
		case 1:
			return id
		case 2:
			return ""
		}
		state[id] = 1
		for _, next := range edges[id] {
			if cycle := visit(next); cycle != "" {
				return cycle
			}
		}
		state[id] = 2
		return ""
	}
	for _, item := range items {
		if cycle := visit(item.ID); cycle != "" {
			return cycle
		}
	}
	return ""
}
