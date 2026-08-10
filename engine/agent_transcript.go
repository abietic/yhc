package engine

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/transcript"
)

const (
	defaultAgentTranscriptPageSize = 32
	maxAgentTranscriptPageSize     = 128
	maxAgentTranscriptCursors      = 256
)

var (
	// ErrAgentTranscriptCursorInvalid reports an unknown or malformed opaque
	// selector cursor.
	ErrAgentTranscriptCursorInvalid = errors.New("agent transcript cursor is invalid")
	// ErrAgentTranscriptSelectionChanged reports cursor reuse against a
	// different child identity, generation, transcript, or current projection.
	ErrAgentTranscriptSelectionChanged = errors.New("agent transcript selection changed")
)

// AgentTranscriptPageRequest addresses one exact child generation. Cursor is
// process-local and opaque; callers must retain AgentID and Generation on every
// async request so stale selection results fail closed.
type AgentTranscriptPageRequest struct {
	AgentID    string
	Generation int64
	Cursor     string
	Limit      int
}

// AgentTranscriptMessage is one bounded durable child transcript row.
type AgentTranscriptMessage struct {
	ID                string
	TranscriptEntryID string
	Role              string
	Content           string
	ReasoningContent  string
	ToolCallID        string
	ToolName          string
	ToolCalls         []RuntimeToolCallSnapshot
	Completed         bool
	Timestamp         time.Time
	Kind              string
	Source            string
	Replay            bool
	PromptParts       []AgentTranscriptPromptPart
}

// AgentTranscriptPromptPart is a presentation-safe ordered prompt part.
type AgentTranscriptPromptPart struct {
	Kind        string
	Text        string
	MIMEType    string
	SizeBytes   int64
	Width       int
	Height      int
	ImageDetail string
}

// AgentTranscriptPage is a read-only selector result. Runtime state owns the
// child lifecycle; this page owns only bounded transcript evidence.
type AgentTranscriptPage struct {
	Revision     uint64
	AgentID      string
	SessionID    string
	ThreadID     string
	Generation   int64
	AttachMode   ThreadAttachmentMode
	Replay       bool
	Storage      string
	Messages     []AgentTranscriptMessage
	NextCursor   string
	HasMore      bool
	BytesRead    int64
	Corruptions  int
	SnapshotSize int64
}

type agentTranscriptCursorState struct {
	agentID      string
	sessionID    string
	threadID     string
	generation   int64
	path         string
	snapshotSize int64
	boundary     transcript.MessagePageBoundary
	fileInfo     os.FileInfo
	seenRecords  map[string]int64
	nextToken    string
}

type agentTranscriptSelector struct {
	mu     sync.Mutex
	states map[string]agentTranscriptCursorState
	order  []string
}

// AgentTranscriptPage returns one bounded page for the exact current child
// generation. It never calls AgentRunner, restore, model, tool, input,
// permission, callback, or control paths.
func (e *QueryEngine) AgentTranscriptPage(
	request AgentTranscriptPageRequest,
) (AgentTranscriptPage, bool, error) {
	if e == nil || e.runtimeState == nil {
		return AgentTranscriptPage{}, false, nil
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.Cursor = strings.TrimSpace(request.Cursor)
	if request.AgentID == "" {
		return AgentTranscriptPage{}, false, errors.New("agent transcript page requires an Agent ID")
	}
	if request.Generation <= 0 {
		return AgentTranscriptPage{}, false, errors.New("agent transcript page requires a positive generation")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = defaultAgentTranscriptPageSize
	} else if limit > maxAgentTranscriptPageSize {
		limit = maxAgentTranscriptPageSize
	}

	agent, thread, revision, found := e.runtimeState.AgentThreadSnapshot(request.AgentID)
	if !found {
		if request.Cursor != "" {
			return AgentTranscriptPage{}, false, fmt.Errorf("%w: Agent is no longer present", ErrAgentTranscriptSelectionChanged)
		}
		return AgentTranscriptPage{}, false, nil
	}
	if err := validateAgentTranscriptSelection(agent, thread, request); err != nil {
		return AgentTranscriptPage{}, true, err
	}
	mode, err := agentTranscriptAttachmentMode(agent, thread)
	if err != nil {
		return AgentTranscriptPage{}, true, err
	}

	selector := e.agentTranscriptSelectorState()
	state := agentTranscriptCursorState{}
	if request.Cursor != "" {
		var ok bool
		state, ok = selector.cursor(request.Cursor)
		if !ok {
			return AgentTranscriptPage{}, true, ErrAgentTranscriptCursorInvalid
		}
		if err := validateAgentTranscriptCursor(state, agent, request); err != nil {
			return AgentTranscriptPage{}, true, err
		}
	}

	reserveLive := request.Cursor == "" && mode == ThreadModeLiveAttach &&
		thread.LiveMessage != nil && !thread.LiveMessage.Completed
	durableLimit := limit
	if reserveLive {
		durableLimit--
	}
	pageRequest := transcript.MessagePageRequest{
		Path:  agent.TranscriptPath,
		Limit: durableLimit,
	}
	if request.Cursor != "" {
		pageRequest.SnapshotSize = state.snapshotSize
		pageRequest.Boundary = state.boundary
		pageRequest.ExpectedFile = state.fileInfo
	}
	durable, err := transcript.LoadMessagePage(pageRequest)
	if err != nil {
		return AgentTranscriptPage{}, true, err
	}
	seen := cloneAgentTranscriptSeenRecords(state.seenRecords)
	if seen == nil {
		seen = make(map[string]int64, len(durable.Entries))
	}
	for _, entry := range durable.Entries {
		recordKey := entry.Identity.Record.Key()
		if offset, exists := seen[recordKey]; exists && offset != entry.RecordOffset {
			return AgentTranscriptPage{}, true, fmt.Errorf(
				"%w: %s",
				transcript.ErrTranscriptEntryIdentityConflict,
				recordKey,
			)
		}
		seen[recordKey] = entry.RecordOffset
	}
	durableMessages, err := agentTranscriptMessagesFromDurable(
		durable.Entries,
		mode != ThreadModeLiveAttach,
	)
	if err != nil {
		return AgentTranscriptPage{}, true, err
	}

	response := AgentTranscriptPage{
		Revision:     revision,
		AgentID:      agent.AgentID,
		SessionID:    agent.SessionID,
		ThreadID:     agent.ThreadID,
		Generation:   agent.Generation,
		AttachMode:   mode,
		Replay:       mode != ThreadModeLiveAttach,
		Storage:      "durable",
		Messages:     durableMessages,
		HasMore:      durable.HasMore,
		BytesRead:    durable.BytesRead + durable.CompatibilityBytes,
		Corruptions:  durable.Corruptions,
		SnapshotSize: durable.SnapshotSize,
	}
	if request.Cursor == "" && mode == ThreadModeLiveAttach {
		response.Messages = mergeAgentTranscriptRuntime(response.Messages, thread, limit)
	}
	if durable.HasMore {
		nextState := agentTranscriptCursorState{
			agentID:      agent.AgentID,
			sessionID:    agent.SessionID,
			threadID:     agent.ThreadID,
			generation:   agent.Generation,
			path:         agent.TranscriptPath,
			snapshotSize: durable.SnapshotSize,
			boundary:     durable.Next,
			fileInfo:     durable.FileInfo,
			seenRecords:  seen,
		}
		if request.Cursor == "" {
			response.NextCursor = selector.add(nextState)
		} else {
			response.NextCursor = selector.addNext(request.Cursor, nextState)
		}
	}
	return response, true, nil
}

func (e *QueryEngine) agentTranscriptSelectorState() *agentTranscriptSelector {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.agentTranscriptSelector == nil {
		e.agentTranscriptSelector = &agentTranscriptSelector{
			states: make(map[string]agentTranscriptCursorState),
		}
	}
	return e.agentTranscriptSelector
}

func validateAgentTranscriptSelection(
	agent RuntimeAgentSnapshot,
	thread RuntimeThreadSnapshot,
	request AgentTranscriptPageRequest,
) error {
	if agent.AgentID != request.AgentID || agent.Generation != request.Generation {
		return fmt.Errorf("%w: requested Agent generation is stale", ErrAgentTranscriptSelectionChanged)
	}
	if strings.TrimSpace(agent.SessionID) == "" ||
		strings.TrimSpace(agent.ThreadID) == "" ||
		strings.TrimSpace(agent.TranscriptPath) == "" {
		return fmt.Errorf("%w: durable Agent identity is incomplete", ErrAgentTranscriptSelectionChanged)
	}
	if thread.ThreadID != "" &&
		(thread.ThreadID != agent.ThreadID || thread.SessionID != agent.SessionID || thread.AgentID != agent.AgentID) {
		return fmt.Errorf("%w: runtime thread identity does not match Agent metadata", ErrAgentTranscriptSelectionChanged)
	}
	return nil
}

func validateAgentTranscriptCursor(
	state agentTranscriptCursorState,
	agent RuntimeAgentSnapshot,
	request AgentTranscriptPageRequest,
) error {
	if state.agentID != request.AgentID || state.generation != request.Generation ||
		state.agentID != agent.AgentID || state.sessionID != agent.SessionID ||
		state.threadID != agent.ThreadID || state.path != agent.TranscriptPath ||
		state.fileInfo == nil {
		return ErrAgentTranscriptSelectionChanged
	}
	return nil
}

func agentTranscriptAttachmentMode(
	agent RuntimeAgentSnapshot,
	thread RuntimeThreadSnapshot,
) (ThreadAttachmentMode, error) {
	if thread.ThreadID == "" {
		return ThreadModeEvictedTranscript, nil
	}
	if thread.ThreadID != agent.ThreadID {
		return "", fmt.Errorf("%w: runtime thread changed", ErrAgentTranscriptSelectionChanged)
	}
	if isRuntimeTerminalStatus(thread.Status) && len(thread.PendingInteractions) == 0 {
		return ThreadModeReplayOnly, nil
	}
	return ThreadModeLiveAttach, nil
}

func agentTranscriptMessagesFromDurable(
	entries []transcript.MessagePageEntry,
	replay bool,
) ([]AgentTranscriptMessage, error) {
	messages := make([]AgentTranscriptMessage, 0, len(entries))
	for _, entry := range entries {
		if entry.Message == nil {
			continue
		}
		message := AgentTranscriptMessage{
			ID:                entry.Identity.Key(),
			TranscriptEntryID: entry.Identity.Key(),
			Role:              string(entry.Message.Role),
			Content:           truncateRuntimeText(entry.Message.Content, maxRuntimeMessageRunes),
			ReasoningContent:  truncateRuntimeText(entry.Message.ReasoningContent, maxRuntimeReasoningRunes),
			ToolCallID:        entry.Message.ToolCallID,
			ToolName:          entry.Message.ToolName,
			Completed:         true,
			Timestamp:         entry.Timestamp,
			Kind:              entry.Kind,
			Source:            "durable",
			Replay:            replay,
		}
		if entry.PromptRecord != nil {
			descriptor, err := entry.PromptRecord.Describe()
			if err != nil {
				return nil, fmt.Errorf(
					"project durable prompt descriptor: %w",
					err,
				)
			}
			for _, part := range descriptor.Parts {
				projected := AgentTranscriptPromptPart{
					Kind: part.Kind,
					Text: truncateRuntimeText(part.Text, maxRuntimeMessageRunes),
				}
				if part.Image != nil {
					projected.MIMEType = part.Image.MIMEType
					projected.SizeBytes = part.Image.SizeBytes
					projected.Width = part.Image.Width
					projected.Height = part.Image.Height
					projected.ImageDetail = part.Image.Detail
				}
				message.PromptParts = append(message.PromptParts, projected)
			}
		}
		for index, call := range entry.Message.ToolCalls {
			if index >= maxRuntimeMessageToolCalls {
				break
			}
			message.ToolCalls = append(message.ToolCalls, RuntimeToolCallSnapshot{
				ID:           call.ID,
				Name:         call.Function.Name,
				InputPreview: truncateRuntimeText(call.Function.Arguments, maxRuntimeToolPreviewRunes),
			})
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func mergeAgentTranscriptRuntime(
	durable []AgentTranscriptMessage,
	thread RuntimeThreadSnapshot,
	limit int,
) []AgentTranscriptMessage {
	positions := make(map[string]int, len(durable))
	for index := range durable {
		positions[durable[index].TranscriptEntryID] = index
	}
	runtimeMessages := append([]RuntimeMessageSnapshot(nil), thread.Messages...)
	if thread.LiveMessage != nil {
		runtimeMessages = append(runtimeMessages, *thread.LiveMessage)
	}
	for _, runtimeMessage := range runtimeMessages {
		identity := strings.TrimSpace(runtimeMessage.TranscriptEntryID)
		if identity == "" {
			continue
		}
		if position, exists := positions[identity]; exists {
			durable[position] = mergeAgentTranscriptMessage(durable[position], runtimeMessage)
		}
	}
	if thread.LiveMessage != nil && !thread.LiveMessage.Completed && len(durable) < limit {
		identity := strings.TrimSpace(thread.LiveMessage.TranscriptEntryID)
		if _, matched := positions[identity]; identity == "" || !matched {
			durable = append(durable, agentTranscriptMessageFromRuntime(*thread.LiveMessage))
		}
	}
	return durable
}

func mergeAgentTranscriptMessage(
	durable AgentTranscriptMessage,
	live RuntimeMessageSnapshot,
) AgentTranscriptMessage {
	if live.Content != "" || durable.Content == "" {
		durable.Content = live.Content
	}
	if live.ReasoningContent != "" || durable.ReasoningContent == "" {
		durable.ReasoningContent = live.ReasoningContent
	}
	if live.ToolCallID != "" {
		durable.ToolCallID = live.ToolCallID
	}
	if live.ToolName != "" {
		durable.ToolName = live.ToolName
	}
	if len(live.ToolCalls) > 0 {
		durable.ToolCalls = append([]RuntimeToolCallSnapshot(nil), live.ToolCalls...)
	}
	durable.Completed = live.Completed
	if !live.Timestamp.IsZero() {
		durable.Timestamp = live.Timestamp
	}
	durable.Source = "durable+runtime"
	durable.Replay = false
	return durable
}

func agentTranscriptMessageFromRuntime(message RuntimeMessageSnapshot) AgentTranscriptMessage {
	id := strings.TrimSpace(message.TranscriptEntryID)
	if id == "" {
		id = message.ID
	}
	return AgentTranscriptMessage{
		ID:                id,
		TranscriptEntryID: message.TranscriptEntryID,
		Role:              message.Role,
		Content:           message.Content,
		ReasoningContent:  message.ReasoningContent,
		ToolCallID:        message.ToolCallID,
		ToolName:          message.ToolName,
		ToolCalls:         append([]RuntimeToolCallSnapshot(nil), message.ToolCalls...),
		Completed:         message.Completed,
		Timestamp:         message.Timestamp,
		Source:            "runtime",
	}
}

func (s *agentTranscriptSelector) cursor(token string) (agentTranscriptCursorState, bool) {
	if s == nil {
		return agentTranscriptCursorState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[token]
	if !ok {
		return agentTranscriptCursorState{}, false
	}
	state.seenRecords = cloneAgentTranscriptSeenRecords(state.seenRecords)
	return state, true
}

func (s *agentTranscriptSelector) add(state agentTranscriptCursorState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := "agent-transcript-" + generateUUID()
	s.addLocked(token, state)
	return token
}

func (s *agentTranscriptSelector) addNext(
	parentToken string,
	state agentTranscriptCursorState,
) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.states[parentToken]
	if ok && parent.nextToken != "" {
		if _, exists := s.states[parent.nextToken]; exists {
			return parent.nextToken
		}
	}
	token := "agent-transcript-" + generateUUID()
	s.addLocked(token, state)
	if ok {
		parent.nextToken = token
		s.states[parentToken] = parent
	}
	return token
}

func (s *agentTranscriptSelector) addLocked(
	token string,
	state agentTranscriptCursorState,
) {
	if s.states == nil {
		s.states = make(map[string]agentTranscriptCursorState)
	}
	state.seenRecords = cloneAgentTranscriptSeenRecords(state.seenRecords)
	s.states[token] = state
	s.order = append(s.order, token)
	for len(s.order) > maxAgentTranscriptCursors {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.states, oldest)
	}
}

func cloneAgentTranscriptSeenRecords(source map[string]int64) map[string]int64 {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]int64, len(source))
	for key, offset := range source {
		cloned[key] = offset
	}
	return cloned
}
