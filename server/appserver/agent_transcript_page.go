package appserver

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/transcript"
)

const (
	agentTranscriptHTTPDefaultLimit = 32
	agentTranscriptHTTPMaxLimit     = 32
	agentTranscriptHTTPCursorBytes  = 128
	agentTranscriptHTTPContentBytes = 16 << 10
	agentTranscriptHTTPReasonBytes  = 8 << 10
	agentTranscriptHTTPPreviewBytes = 4 << 10
)

type sessionAgentTranscriptPageReader interface {
	sessionTaskExplorerReader
	AgentTranscriptPage(engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error)
}

type agentTranscriptHTTPResponse struct {
	SessionID          string                       `json:"session_id"`
	AgentID            string                       `json:"agent_id"`
	Generation         string                       `json:"generation"`
	ExecutionSessionID string                       `json:"execution_session_id"`
	ThreadID           string                       `json:"thread_id"`
	Revision           string                       `json:"revision"`
	Mode               string                       `json:"mode"`
	Replay             bool                         `json:"replay"`
	Storage            string                       `json:"storage"`
	Messages           []agentTranscriptHTTPMessage `json:"messages"`
	NextCursor         string                       `json:"next_cursor"`
	HasMore            bool                         `json:"has_more"`
	Corruptions        int                          `json:"corruptions"`
}

type agentTranscriptHTTPMessage struct {
	ID                string                          `json:"id"`
	TranscriptEntryID string                          `json:"transcript_entry_id"`
	Role              string                          `json:"role"`
	Content           string                          `json:"content"`
	ReasoningContent  string                          `json:"reasoning_content"`
	ToolName          string                          `json:"tool_name"`
	ToolCallID        string                          `json:"tool_call_id"`
	ToolCalls         []agentTranscriptHTTPToolCall   `json:"tool_calls"`
	Completed         bool                            `json:"completed"`
	Timestamp         string                          `json:"timestamp"`
	Source            string                          `json:"source"`
	Replay            bool                            `json:"replay"`
	PromptParts       []agentTranscriptHTTPPromptPart `json:"prompt_parts"`
	Truncated         bool                            `json:"truncated"`
}

type agentTranscriptHTTPToolCall struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	InputPreview string `json:"input_preview"`
}

type agentTranscriptHTTPPromptPart struct {
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	MIMEType    string `json:"mime_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ImageDetail string `json:"image_detail"`
}

func (s *Server) handleAgentTranscriptPage(w http.ResponseWriter, r *http.Request) {
	req, cursor, limit, ok := agentTranscriptPageRequestFromHTTP(w, r)
	if !ok {
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
	reader, ok := owned.engine.(sessionAgentTranscriptPageReader)
	if !ok {
		writeError(w, http.StatusNotImplemented, "agent_transcript_unavailable", "agent transcript is unavailable")
		return
	}
	row, selection := selectAgentExecution(reader.TaskExplorerSnapshot().Executions, req)
	if selection == 0 {
		writeError(w, http.StatusNotFound, "agent_transcript_not_found", "agent transcript not found")
		return
	}
	if selection != 1 {
		writeError(w, http.StatusConflict, "agent_transcript_selection_changed", "agent transcript selection changed")
		return
	}
	page, found, err := reader.AgentTranscriptPage(engine.AgentTranscriptPageRequest{
		AgentID: req.AgentID, Generation: req.Generation, SessionID: req.SessionID, ThreadID: req.ThreadID, Cursor: cursor, Limit: limit,
	})
	if errors.Is(err, engine.ErrAgentTranscriptSelectionChanged) || errors.Is(err, transcript.ErrTranscriptRevisionChanged) {
		writeError(w, http.StatusConflict, "agent_transcript_selection_changed", "agent transcript selection changed")
		return
	}
	if errors.Is(err, engine.ErrAgentTranscriptCursorInvalid) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "invalid cursor")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agent_transcript_failed", "agent transcript failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "agent_transcript_not_found", "agent transcript not found")
		return
	}
	if err := validateAgentTranscriptPage(page, req, cursor); err != nil {
		writeError(w, http.StatusConflict, "agent_transcript_selection_changed", "agent transcript selection changed")
		return
	}
	response, err := projectAgentTranscriptPage(owned.id, row, page, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "agent_transcript_failed", "agent transcript failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func agentTranscriptPageRequestFromHTTP(w http.ResponseWriter, r *http.Request) (engine.AgentExecutionDetailRequest, string, int, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid_request", "invalid query")
		return engine.AgentExecutionDetailRequest{}, "", 0, false
	}
	allowed := map[string]bool{"agent_id": true, "generation": true, "execution_session_id": true, "thread_id": true, "cursor": true, "limit": true}
	for key, value := range values {
		if !allowed[key] || len(value) != 1 {
			writeError(w, 400, "invalid_request", "invalid query")
			return engine.AgentExecutionDetailRequest{}, "", 0, false
		}
	}
	agent, session, thread := values.Get("agent_id"), values.Get("execution_session_id"), values.Get("thread_id")
	if !agentExecutionDetailID(agent) || !agentExecutionDetailID(session) || !agentExecutionDetailID(thread) {
		writeError(w, 400, "invalid_request", "invalid identity")
		return engine.AgentExecutionDetailRequest{}, "", 0, false
	}
	generation, ok := agentTranscriptHTTPPositiveInt64(values.Get("generation"))
	if !ok {
		writeError(w, 400, "invalid_request", "invalid generation")
		return engine.AgentExecutionDetailRequest{}, "", 0, false
	}
	limit := agentTranscriptHTTPDefaultLimit
	if text, exists := values["limit"]; exists {
		var valid bool
		if limit, valid = agentTranscriptHTTPBoundedInt(text[0], 1, agentTranscriptHTTPMaxLimit); !valid {
			writeError(w, 400, "invalid_request", "invalid limit")
			return engine.AgentExecutionDetailRequest{}, "", 0, false
		}
	}
	cursor := values.Get("cursor")
	if _, exists := values["cursor"]; exists && !agentTranscriptHTTPCursor(cursor) {
		writeError(w, 400, "invalid_cursor", "invalid cursor")
		return engine.AgentExecutionDetailRequest{}, "", 0, false
	}
	return engine.AgentExecutionDetailRequest{AgentID: agent, Generation: generation, SessionID: session, ThreadID: thread}, cursor, limit, true
}

func agentTranscriptHTTPPositiveInt64(value string) (int64, bool) {
	if value == "" || value[0] == '0' {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func agentTranscriptHTTPBoundedInt(value string, min, max int) (int, bool) {
	if value == "" || value[0] == '0' {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed >= min && parsed <= max && strconv.Itoa(parsed) == value
}

func agentTranscriptHTTPCursor(value string) bool {
	if value == "" || len(value) > agentTranscriptHTTPCursorBytes {
		return false
	}
	for _, r := range value {
		if r < '!' || r > '~' {
			return false
		}
	}
	return true
}

func validateAgentTranscriptPage(page engine.AgentTranscriptPage, req engine.AgentExecutionDetailRequest, cursor string) error {
	if page.AgentID != req.AgentID || page.Generation != req.Generation || page.SessionID != req.SessionID || page.ThreadID != req.ThreadID ||
		(page.AttachMode != engine.ThreadModeLiveAttach && page.AttachMode != engine.ThreadModeReplayOnly && page.AttachMode != engine.ThreadModeEvictedTranscript) ||
		(page.Replay != (page.AttachMode != engine.ThreadModeLiveAttach)) ||
		(page.HasMore && !agentTranscriptHTTPCursor(page.NextCursor)) || (!page.HasMore && page.NextCursor != "") ||
		(cursor != "" && page.HasMore && page.NextCursor == cursor) {
		return errors.New("invalid transcript page identity")
	}
	return nil
}

func projectAgentTranscriptPage(owned string, row engine.TaskExplorerExecution, page engine.AgentTranscriptPage, limit int) (agentTranscriptHTTPResponse, error) {
	if len(page.Messages) > limit {
		return agentTranscriptHTTPResponse{}, errors.New("page exceeds requested limit")
	}
	if page.Storage != "durable" {
		return agentTranscriptHTTPResponse{}, errors.New("invalid transcript storage")
	}
	messages := make([]agentTranscriptHTTPMessage, 0, len(page.Messages))
	messageIDs := make(map[string]struct{}, len(page.Messages))
	for _, message := range page.Messages {
		if _, exists := messageIDs[message.ID]; exists {
			return agentTranscriptHTTPResponse{}, errors.New("duplicate transcript message identity")
		}
		messageIDs[message.ID] = struct{}{}
		projected, err := projectAgentTranscriptMessage(message)
		if err != nil {
			return agentTranscriptHTTPResponse{}, err
		}
		messages = append(messages, projected)
	}
	return agentTranscriptHTTPResponse{SessionID: owned, AgentID: row.Key.AgentID, Generation: strconv.FormatInt(row.Key.Generation, 10), ExecutionSessionID: row.SessionID, ThreadID: row.ThreadID, Revision: strconv.FormatUint(page.Revision, 10), Mode: string(page.AttachMode), Replay: page.Replay, Storage: page.Storage, Messages: messages, NextCursor: page.NextCursor, HasMore: page.HasMore, Corruptions: taskExplorerHTTPCount(page.Corruptions)}, nil
}

func projectAgentTranscriptMessage(message engine.AgentTranscriptMessage) (agentTranscriptHTTPMessage, error) {
	if !agentExecutionDetailID(message.ID) || !agentTranscriptHTTPRole(message.Role) || !agentTranscriptHTTPSource(message.Source) || !agentTranscriptHTTPIdentifier(message.ToolName) || !agentTranscriptHTTPIdentifier(message.ToolCallID) ||
		(message.TranscriptEntryID == "" && message.Source != "runtime") || (message.TranscriptEntryID != "" && !agentExecutionDetailID(message.TranscriptEntryID)) {
		return agentTranscriptHTTPMessage{}, errors.New("invalid transcript message")
	}
	content, truncated := agentTranscriptHTTPText(message.Content, agentTranscriptHTTPContentBytes)
	reasoning, reasoningTruncated := agentTranscriptHTTPText(message.ReasoningContent, agentTranscriptHTTPReasonBytes)
	truncated = truncated || reasoningTruncated
	callLimit, partLimit := min(len(message.ToolCalls), 8), min(len(message.PromptParts), 16)
	truncated = truncated || len(message.ToolCalls) > callLimit || len(message.PromptParts) > partLimit
	calls := make([]agentTranscriptHTTPToolCall, 0, callLimit)
	for _, call := range message.ToolCalls[:callLimit] {
		if !agentTranscriptHTTPIdentifier(call.ID) || !agentTranscriptHTTPIdentifier(call.Name) {
			return agentTranscriptHTTPMessage{}, errors.New("invalid tool call")
		}
		preview, cut := agentTranscriptHTTPText(call.InputPreview, agentTranscriptHTTPPreviewBytes)
		truncated = truncated || cut
		calls = append(calls, agentTranscriptHTTPToolCall{ID: call.ID, Name: call.Name, InputPreview: preview})
	}
	parts := make([]agentTranscriptHTTPPromptPart, 0, partLimit)
	for _, part := range message.PromptParts[:partLimit] {
		if !agentTranscriptHTTPIdentifier(part.Kind) || !agentTranscriptHTTPBoundedIdentifier(part.MIMEType, 128) || !agentTranscriptHTTPBoundedIdentifier(part.ImageDetail, 64) {
			return agentTranscriptHTTPMessage{}, errors.New("invalid prompt part")
		}
		text, cut := agentTranscriptHTTPText(part.Text, agentTranscriptHTTPPreviewBytes)
		truncated = truncated || cut
		parts = append(parts, agentTranscriptHTTPPromptPart{Kind: part.Kind, Text: text, MIMEType: part.MIMEType, SizeBytes: maxInt64(part.SizeBytes, 0), Width: maxInt(part.Width, 0), Height: maxInt(part.Height, 0), ImageDetail: part.ImageDetail})
	}
	return agentTranscriptHTTPMessage{ID: message.ID, TranscriptEntryID: message.TranscriptEntryID, Role: message.Role, Content: content, ReasoningContent: reasoning, ToolName: message.ToolName, ToolCallID: message.ToolCallID, ToolCalls: calls, Completed: message.Completed, Timestamp: message.Timestamp.UTC().Format(time.RFC3339Nano), Source: message.Source, Replay: message.Replay, PromptParts: parts, Truncated: truncated}, nil
}

func agentTranscriptHTTPRole(value string) bool {
	return value == "user" || value == "assistant" || value == "tool" || value == "system"
}

func agentTranscriptHTTPSource(value string) bool {
	return value == "durable" || value == "runtime" || value == "durable+runtime"
}

func agentTranscriptHTTPIdentifier(value string) bool {
	return agentTranscriptHTTPBoundedIdentifier(value, taskExplorerHTTPRunes)
}

func agentTranscriptHTTPBoundedIdentifier(value string, limit int) bool {
	return value == "" || (utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && value == strings.TrimSpace(value) && !strings.ContainsFunc(value, unicode.IsControl))
}

func agentTranscriptHTTPText(value string, limit int) (string, bool) {
	truncated := !utf8.ValidString(value)
	value = strings.ToValidUTF8(value, "")
	if len(value) <= limit {
		return value, truncated
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func maxInt(value, min int) int {
	if value < min {
		return min
	}
	return value
}

func maxInt64(value, min int64) int64 {
	if value < min {
		return min
	}
	return value
}
