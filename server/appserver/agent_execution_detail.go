package appserver

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
)

const agentExecutionDetailOutputBytes = 64 * 1024

type sessionAgentExecutionDetailReader interface {
	sessionTaskExplorerReader
	AgentExecutionDetail(engine.AgentExecutionDetailRequest) (engine.AgentExecutionDetail, bool, error)
}

type agentExecutionDetailResponse struct {
	SessionID          string                       `json:"session_id"`
	AgentID            string                       `json:"agent_id"`
	Generation         string                       `json:"generation"`
	ExecutionSessionID string                       `json:"execution_session_id"`
	ThreadID           string                       `json:"thread_id"`
	Revision           string                       `json:"revision"`
	Metadata           agentExecutionDetailMetadata `json:"metadata"`
	Output             agentExecutionDetailOutput   `json:"output"`
}
type agentExecutionDetailMetadata struct {
	Name            string `json:"name"`
	Task            string `json:"task"`
	Description     string `json:"description"`
	Activity        string `json:"activity"`
	Status          string `json:"status"`
	LastToolName    string `json:"last_tool_name"`
	ToolUseCount    int    `json:"tool_use_count"`
	TokenCount      int    `json:"token_count"`
	ParentSessionID string `json:"parent_session_id"`
	ParentThreadID  string `json:"parent_thread_id"`
	ParentAgentID   string `json:"parent_agent_id"`
	ParentToolUseID string `json:"parent_tool_use_id"`
}
type agentExecutionDetailOutput struct {
	Requested     bool   `json:"requested"`
	Text          string `json:"text"`
	Truncated     bool   `json:"truncated"`
	ErrorCategory string `json:"error_category"`
}

func (s *Server) handleAgentExecutionDetail(w http.ResponseWriter, r *http.Request) {
	req, ok := agentExecutionDetailRequestFromHTTP(w, r)
	if !ok {
		return
	}
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, 404, "session_not_found", "session not found")
		return
	}
	owned.turnAdmissionMu.Lock()
	defer owned.turnAdmissionMu.Unlock()
	owned.mu.Lock()
	closed := owned.closed
	owned.mu.Unlock()
	if closed {
		writeError(w, 404, "session_not_found", "session not found")
		return
	}
	reader, ok := owned.engine.(sessionAgentExecutionDetailReader)
	if !ok {
		writeError(w, 501, "agent_execution_detail_unavailable", "agent execution detail is unavailable")
		return
	}
	snapshot := reader.TaskExplorerSnapshot()
	row, result := selectAgentExecution(snapshot.Executions, req)
	if result == 0 {
		writeError(w, 404, "agent_execution_not_found", "agent execution not found")
		return
	}
	if result != 1 {
		writeError(w, 409, "agent_execution_selection_changed", "agent execution selection changed")
		return
	}
	detail, found, err := reader.AgentExecutionDetail(req)
	if errors.Is(err, engine.ErrAgentExecutionDetailSelectionChanged) {
		writeError(w, 409, "agent_execution_selection_changed", "agent execution selection changed")
		return
	}
	if err != nil {
		writeError(w, 500, "agent_execution_detail_failed", "agent execution detail failed")
		return
	}
	if !found {
		writeError(w, 404, "agent_execution_not_found", "agent execution not found")
		return
	}
	if !agentDetailIdentityMatches(detail.Agent, req) {
		writeError(w, 409, "agent_execution_selection_changed", "agent execution selection changed")
		return
	}
	writeJSON(w, 200, projectAgentExecutionDetail(owned.id, row, detail, req.IncludeOutput))
}

// result: 0 absent, 1 exact eligible, 2 selection changed.
func selectAgentExecution(rows []engine.TaskExplorerExecution, req engine.AgentExecutionDetailRequest) (engine.TaskExplorerExecution, int) {
	var candidate engine.TaskExplorerExecution
	matches := 0
	for _, row := range rows {
		if row.Key.AgentID == req.AgentID && row.Key.Generation == req.Generation {
			matches++
			candidate = row
		}
	}
	if matches == 0 {
		return candidate, 0
	}
	if matches != 1 || candidate.SessionID != req.SessionID || candidate.ThreadID != req.ThreadID || candidate.Predispatch || candidate.ReplayOnly || candidate.SessionID == "" || candidate.ThreadID == "" {
		return candidate, 2
	}
	return candidate, 1
}

func agentDetailIdentityMatches(agent engine.RuntimeAgentSnapshot, req engine.AgentExecutionDetailRequest) bool {
	return agent.AgentID == req.AgentID && agent.Generation == req.Generation && agent.SessionID == req.SessionID && agent.ThreadID == req.ThreadID
}

func agentExecutionDetailRequestFromHTTP(w http.ResponseWriter, r *http.Request) (engine.AgentExecutionDetailRequest, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid_request", "invalid query")
		return engine.AgentExecutionDetailRequest{}, false
	}
	allowed := map[string]bool{"agent_id": true, "generation": true, "execution_session_id": true, "thread_id": true, "include_output": true}
	for key, value := range values {
		if !allowed[key] || len(value) != 1 {
			writeError(w, 400, "invalid_request", "invalid query")
			return engine.AgentExecutionDetailRequest{}, false
		}
	}
	agent, session, thread := values.Get("agent_id"), values.Get("execution_session_id"), values.Get("thread_id")
	if !agentExecutionDetailID(agent) || !agentExecutionDetailID(session) || !agentExecutionDetailID(thread) {
		writeError(w, 400, "invalid_request", "invalid identity")
		return engine.AgentExecutionDetailRequest{}, false
	}
	generationText := values.Get("generation")
	if generationText == "" || generationText[0] == '0' {
		writeError(w, 400, "invalid_request", "invalid generation")
		return engine.AgentExecutionDetailRequest{}, false
	}
	generation, err := strconv.ParseInt(generationText, 10, 64)
	if err != nil || generation <= 0 || strconv.FormatInt(generation, 10) != generationText {
		writeError(w, 400, "invalid_request", "invalid generation")
		return engine.AgentExecutionDetailRequest{}, false
	}
	include := false
	if value, exists := values["include_output"]; exists {
		if value[0] == "true" {
			include = true
		} else if value[0] != "false" {
			writeError(w, 400, "invalid_request", "invalid include_output")
			return engine.AgentExecutionDetailRequest{}, false
		}
	}
	return engine.AgentExecutionDetailRequest{AgentID: agent, Generation: generation, SessionID: session, ThreadID: thread, IncludeOutput: include}, true
}

func agentExecutionDetailID(value string) bool {
	return value != "" && utf8.ValidString(value) && value == strings.TrimSpace(value) && utf8.RuneCountInString(value) <= taskExplorerHTTPRunes && !strings.ContainsFunc(value, unicode.IsControl)
}

func projectAgentExecutionDetail(owned string, row engine.TaskExplorerExecution, detail engine.AgentExecutionDetail, outputRequested bool) agentExecutionDetailResponse {
	a := detail.Agent
	output := agentExecutionDetailOutput{Requested: outputRequested}
	if outputRequested && detail.LoadError == "" {
		output.Text, output.Truncated = agentExecutionDetailOutputText(detail.Output, detail.OutputTruncated)
	} else if outputRequested && detail.LoadError != "" {
		output.ErrorCategory = "output_read_failed"
	}
	return agentExecutionDetailResponse{SessionID: taskExplorerHTTPText(owned), AgentID: taskExplorerHTTPText(row.Key.AgentID), Generation: strconv.FormatInt(row.Key.Generation, 10), ExecutionSessionID: taskExplorerHTTPText(row.SessionID), ThreadID: taskExplorerHTTPText(row.ThreadID), Revision: strconv.FormatUint(detail.Revision, 10), Metadata: agentExecutionDetailMetadata{Name: taskExplorerHTTPText(a.Name), Task: taskExplorerHTTPText(a.Task), Description: taskExplorerHTTPText(a.Description), Activity: taskExplorerHTTPText(a.Progress.Summary), Status: taskExplorerHTTPText(a.Status), LastToolName: taskExplorerHTTPText(a.Progress.LastToolName), ToolUseCount: taskExplorerHTTPCount(a.Progress.ToolUses), TokenCount: taskExplorerHTTPCount(a.Progress.TotalTokens), ParentSessionID: taskExplorerHTTPText(a.ParentSessionID), ParentThreadID: taskExplorerHTTPText(a.ParentThreadID), ParentAgentID: taskExplorerHTTPText(a.ParentAgentID), ParentToolUseID: taskExplorerHTTPText(a.ParentToolUseID)}, Output: output}
}

func agentExecutionDetailOutputText(value string, truncated bool) (string, bool) {
	if !utf8.ValidString(value) {
		return "", true
	}
	if len(value) <= agentExecutionDetailOutputBytes {
		return value, truncated
	}
	tail := value[len(value)-agentExecutionDetailOutputBytes:]
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return tail, true
}
