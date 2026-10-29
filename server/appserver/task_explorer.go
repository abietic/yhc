package appserver

import (
	"net/http"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
)

const (
	taskExplorerHTTPRows     = 128
	taskExplorerHTTPMapItems = 32
	taskExplorerHTTPRunes    = 512
)

type sessionTaskExplorerReader interface {
	TaskExplorerSnapshot() engine.TaskExplorerSnapshot
}

type taskExplorerHTTPResponse struct {
	SessionID         string                       `json:"session_id"`
	Available         bool                         `json:"available"`
	UnavailableReason string                       `json:"unavailable_reason"`
	BoardID           string                       `json:"board_id"`
	Revision          taskExplorerHTTPRevision     `json:"revision"`
	WorkItems         []taskExplorerHTTPWorkItem   `json:"work_items"`
	Executions        []taskExplorerHTTPExecution  `json:"executions"`
	Links             []taskExplorerHTTPLink       `json:"links"`
	Attention         []taskExplorerHTTPAttention  `json:"attention"`
	Diagnostics       []taskExplorerHTTPDiagnostic `json:"diagnostics"`
	Hidden            taskExplorerHTTPHidden       `json:"hidden"`
}

type taskExplorerHTTPRevision struct {
	Board   string `json:"board"`
	Runtime string `json:"runtime"`
}

type taskExplorerHTTPWorkItem struct {
	BoardID        string   `json:"board_id"`
	ID             string   `json:"id"`
	Revision       string   `json:"revision"`
	Order          int      `json:"order"`
	Status         string   `json:"status"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	ActiveForm     string   `json:"active_form"`
	Owner          string   `json:"owner"`
	ResultSummary  string   `json:"result_summary"`
	TerminalReason string   `json:"terminal_reason"`
	Blocks         []string `json:"blocks"`
	BlockedBy      []string `json:"blocked_by"`
	Attention      []string `json:"attention"`
}

type taskExplorerHTTPExecution struct {
	AgentID            string   `json:"agent_id"`
	ExecutionSessionID string   `json:"execution_session_id"`
	ThreadID           string   `json:"thread_id"`
	Generation         string   `json:"generation"`
	Name               string   `json:"name"`
	Task               string   `json:"task"`
	Description        string   `json:"description"`
	Activity           string   `json:"activity"`
	Status             string   `json:"status"`
	Phase              string   `json:"phase"`
	DisplayMode        string   `json:"display_mode"`
	LastToolName       string   `json:"last_tool_name"`
	ToolUseCount       int      `json:"tool_use_count"`
	TokenCount         int      `json:"token_count"`
	ReplayOnly         bool     `json:"replay_only"`
	Predispatch        bool     `json:"predispatch"`
	ObservationOrdinal string   `json:"observation_ordinal"`
	OrdinalPresent     bool     `json:"ordinal_present"`
	Attention          []string `json:"attention"`
}

type taskExplorerHTTPLink struct {
	BoardID           string `json:"board_id"`
	WorkItemID        string `json:"work_item_id"`
	AgentID           string `json:"agent_id"`
	Generation        string `json:"generation"`
	State             string `json:"state"`
	UnavailableReason string `json:"unavailable_reason"`
}

type taskExplorerHTTPAttention struct {
	Category   string `json:"category"`
	WorkItemID string `json:"work_item_id"`
	AgentID    string `json:"agent_id"`
	Generation string `json:"generation"`
	Sequence   string `json:"sequence"`
	Reason     string `json:"reason"`
}

type taskExplorerHTTPDiagnostic struct {
	Sequence string `json:"sequence"`
	Kind     string `json:"kind"`
	ItemID   string `json:"item_id"`
	Message  string `json:"message"`
}

type taskExplorerHTTPHidden struct {
	WorkItems                   map[string]int `json:"work_items"`
	Executions                  map[string]int `json:"executions"`
	Links                       int            `json:"links"`
	Attention                   map[string]int `json:"attention"`
	WorkBoardOutsidePrimary     int            `json:"work_board_outside_primary"`
	RuntimeEventsDropped        string         `json:"runtime_events_dropped"`
	ExecutionGenerationsEvicted string         `json:"execution_generations_evicted"`
	HiddenLiveExecutions        string         `json:"hidden_live_executions"`
}

func (s *Server) handleTaskExplorer(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "task explorer does not accept query parameters")
		return
	}
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	owned.turnAdmissionMu.Lock()
	defer owned.turnAdmissionMu.Unlock()
	owned.mu.Lock()
	closed := owned.closed
	owned.mu.Unlock()
	if closed {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	reader, ok := owned.engine.(sessionTaskExplorerReader)
	if !ok {
		writeError(w, http.StatusNotImplemented, "task_explorer_unavailable", "task explorer is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, taskExplorerHTTPProjection(owned.id, reader.TaskExplorerSnapshot()))
}

func taskExplorerHTTPProjection(sessionID string, snapshot engine.TaskExplorerSnapshot) taskExplorerHTTPResponse {
	workItems, workItemsOverflow := taskExplorerHTTPWorkItems(snapshot.WorkItems)
	executions, executionsOverflow := taskExplorerHTTPExecutions(snapshot.Executions)
	links, linksOverflow := taskExplorerHTTPLinks(snapshot.Links)
	attention, attentionOverflow := taskExplorerHTTPAttentionRows(snapshot.Attention)
	diagnostics, _ := taskExplorerHTTPDiagnostics(snapshot.Diagnostics)
	return taskExplorerHTTPResponse{
		SessionID:         taskExplorerHTTPText(sessionID),
		Available:         snapshot.Available,
		UnavailableReason: taskExplorerHTTPText(snapshot.UnavailableReason),
		BoardID:           taskExplorerHTTPText(snapshot.BoardID),
		Revision: taskExplorerHTTPRevision{
			Board:   strconv.FormatUint(snapshot.Revision.Board, 10),
			Runtime: strconv.FormatUint(snapshot.Revision.Runtime, 10),
		},
		WorkItems:   workItems,
		Executions:  executions,
		Links:       links,
		Attention:   attention,
		Diagnostics: diagnostics,
		Hidden: taskExplorerHTTPHidden{
			WorkItems:                   taskExplorerHTTPHiddenMap(snapshot.Hidden.WorkItems, workItemsOverflow),
			Executions:                  taskExplorerHTTPHiddenMap(snapshot.Hidden.Executions, executionsOverflow),
			Links:                       taskExplorerHTTPCountSum(snapshot.Hidden.Links, linksOverflow),
			Attention:                   taskExplorerHTTPHiddenMap(snapshot.Hidden.Attention, attentionOverflow),
			WorkBoardOutsidePrimary:     taskExplorerHTTPCount(snapshot.Hidden.WorkBoardOutsidePrimary),
			RuntimeEventsDropped:        strconv.FormatUint(snapshot.Hidden.RuntimeEventsDropped, 10),
			ExecutionGenerationsEvicted: strconv.FormatUint(snapshot.Hidden.ExecutionGenerationsEvicted, 10),
			HiddenLiveExecutions:        strconv.FormatUint(snapshot.Hidden.HiddenLiveExecutions, 10),
		},
	}
}

func taskExplorerHTTPWorkItems(rows []engine.TaskExplorerWorkItem) ([]taskExplorerHTTPWorkItem, int) {
	visible, overflow := taskExplorerHTTPLimit(rows)
	result := make([]taskExplorerHTTPWorkItem, 0, len(visible))
	for _, row := range visible {
		result = append(result, taskExplorerHTTPWorkItem{BoardID: taskExplorerHTTPText(row.BoardID), ID: taskExplorerHTTPText(row.WorkItemID), Revision: strconv.FormatUint(row.Revision, 10), Order: row.Order, Status: taskExplorerHTTPText(row.Status), Title: taskExplorerHTTPText(row.Title), Description: taskExplorerHTTPText(row.Description), ActiveForm: taskExplorerHTTPText(row.ActiveForm), Owner: taskExplorerHTTPText(row.Owner), ResultSummary: taskExplorerHTTPText(row.ResultSummary), TerminalReason: taskExplorerHTTPText(row.TerminalReason), Blocks: taskExplorerHTTPStrings(row.Blocks), BlockedBy: taskExplorerHTTPStrings(row.BlockedBy), Attention: taskExplorerHTTPStrings(row.Attention)})
	}
	return result, overflow
}

func taskExplorerHTTPExecutions(rows []engine.TaskExplorerExecution) ([]taskExplorerHTTPExecution, int) {
	visible, overflow := taskExplorerHTTPLimit(rows)
	result := make([]taskExplorerHTTPExecution, 0, len(visible))
	for _, row := range visible {
		result = append(result, taskExplorerHTTPExecution{AgentID: taskExplorerHTTPText(row.Key.AgentID), ExecutionSessionID: taskExplorerHTTPExactLineage(row.SessionID), ThreadID: taskExplorerHTTPExactLineage(row.ThreadID), Generation: strconv.FormatInt(row.Key.Generation, 10), Name: taskExplorerHTTPText(row.Name), Task: taskExplorerHTTPText(row.Task), Description: taskExplorerHTTPText(row.Description), Activity: taskExplorerHTTPText(row.Activity), Status: taskExplorerHTTPText(row.Status), Phase: taskExplorerHTTPText(string(row.Phase)), DisplayMode: taskExplorerHTTPText(row.DisplayMode), LastToolName: taskExplorerHTTPText(row.LastToolName), ToolUseCount: taskExplorerHTTPCount(row.ToolUseCount), TokenCount: taskExplorerHTTPCount(row.TokenCount), ReplayOnly: row.ReplayOnly, Predispatch: row.Predispatch, ObservationOrdinal: strconv.FormatUint(row.ObservationOrdinal, 10), OrdinalPresent: row.OrdinalPresent, Attention: taskExplorerHTTPStrings(row.Attention)})
	}
	return result, overflow
}

func taskExplorerHTTPExactLineage(value string) string {
	if !agentExecutionDetailID(value) {
		return ""
	}
	return value
}

func taskExplorerHTTPLinks(rows []engine.TaskExplorerLink) ([]taskExplorerHTTPLink, int) {
	visible, overflow := taskExplorerHTTPLimit(rows)
	result := make([]taskExplorerHTTPLink, 0, len(visible))
	for _, row := range visible {
		result = append(result, taskExplorerHTTPLink{BoardID: taskExplorerHTTPText(row.BoardID), WorkItemID: taskExplorerHTTPText(row.WorkItemID), AgentID: taskExplorerHTTPText(row.AgentID), Generation: strconv.FormatInt(row.Generation, 10), State: taskExplorerHTTPText(string(row.State)), UnavailableReason: taskExplorerHTTPText(row.UnavailableReason)})
	}
	return result, overflow
}

func taskExplorerHTTPAttentionRows(rows []engine.TaskExplorerAttention) ([]taskExplorerHTTPAttention, int) {
	visible, overflow := taskExplorerHTTPLimit(rows)
	result := make([]taskExplorerHTTPAttention, 0, len(visible))
	for _, row := range visible {
		result = append(result, taskExplorerHTTPAttention{Category: taskExplorerHTTPText(row.Category), WorkItemID: taskExplorerHTTPText(row.WorkItemID), AgentID: taskExplorerHTTPText(row.AgentID), Generation: strconv.FormatInt(row.Generation, 10), Sequence: strconv.FormatUint(row.Sequence, 10), Reason: taskExplorerHTTPText(row.Reason)})
	}
	return result, overflow
}

func taskExplorerHTTPDiagnostics(rows []engine.TaskExplorerDiagnostic) ([]taskExplorerHTTPDiagnostic, int) {
	visible, overflow := taskExplorerHTTPLimit(rows)
	result := make([]taskExplorerHTTPDiagnostic, 0, len(visible))
	for _, row := range visible {
		result = append(result, taskExplorerHTTPDiagnostic{Sequence: strconv.FormatUint(row.Sequence, 10), Kind: taskExplorerHTTPText(row.Kind), ItemID: taskExplorerHTTPText(row.ItemID), Message: taskExplorerHTTPText(row.Message)})
	}
	return result, overflow
}

func taskExplorerHTTPStrings(values []string) []string {
	visible, _ := taskExplorerHTTPLimit(values)
	result := make([]string, 0, len(visible))
	for _, value := range visible {
		result = append(result, taskExplorerHTTPText(value))
	}
	return result
}

func taskExplorerHTTPHiddenMap(values map[string]int, overflow int) map[string]int {
	combined := make(map[string]int, len(values)+1)
	for key, value := range values {
		key = taskExplorerHTTPTextLimit(key, 128)
		combined[key] = taskExplorerHTTPCountSum(combined[key], value)
	}
	if overflow > 0 {
		combined["other"] = taskExplorerHTTPCountSum(combined["other"], overflow)
	}
	keys := make([]string, 0, len(combined))
	for key := range combined {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > taskExplorerHTTPMapItems {
		omitted := 0
		selected := make([]string, 0, taskExplorerHTTPMapItems)
		for _, key := range keys {
			if key == "other" {
				continue
			}
			if len(selected) < taskExplorerHTTPMapItems-1 {
				selected = append(selected, key)
				continue
			}
			omitted = taskExplorerHTTPCountSum(omitted, combined[key])
		}
		combined["other"] = taskExplorerHTTPCountSum(combined["other"], omitted)
		selected = append(selected, "other")
		keys = selected
	}
	result := make(map[string]int, len(keys))
	for _, key := range keys {
		result[key] = taskExplorerHTTPCount(combined[key])
	}
	return result
}

func taskExplorerHTTPText(value string) string {
	return taskExplorerHTTPTextLimit(value, taskExplorerHTTPRunes)
}

func taskExplorerHTTPTextLimit(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func taskExplorerHTTPCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func taskExplorerHTTPCountSum(left, right int) int {
	left = taskExplorerHTTPCount(left)
	right = taskExplorerHTTPCount(right)
	maximum := int(^uint(0) >> 1)
	if left > maximum-right {
		return maximum
	}
	return left + right
}

func taskExplorerHTTPLimit[T any](values []T) ([]T, int) {
	if len(values) <= taskExplorerHTTPRows {
		return values, 0
	}
	return values[:taskExplorerHTTPRows], len(values) - taskExplorerHTTPRows
}
