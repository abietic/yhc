package appserver

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

const (
	defaultTranscriptPageLimit = 64
	maxTranscriptPageLimit     = 128
	maxTranscriptPageCursors   = 256
)

var (
	errTranscriptUnavailable   = errors.New("session transcript is unavailable")
	errTranscriptCursorInvalid = errors.New("session transcript cursor is invalid")
)

type transcriptCursorState struct {
	path         string
	snapshotSize int64
	boundary     transcript.MessagePageBoundary
	fileInfo     os.FileInfo
	seenRecords  map[string]int64
	nextToken    string
}

type transcriptPager struct {
	mu sync.Mutex

	path   string
	states map[string]transcriptCursorState
	order  []string
}

func newTranscriptPager(path string) *transcriptPager {
	return &transcriptPager{
		path:   strings.TrimSpace(path),
		states: make(map[string]transcriptCursorState),
	}
}

func (p *transcriptPager) page(cursor string, limit int) (TranscriptPageResponse, error) {
	if p == nil || p.path == "" {
		return TranscriptPageResponse{}, errTranscriptUnavailable
	}
	cursor = strings.TrimSpace(cursor)
	limit = normalizeTranscriptPageLimit(limit)

	state := transcriptCursorState{}
	if cursor != "" {
		var ok bool
		state, ok = p.cursor(cursor)
		if !ok {
			return TranscriptPageResponse{}, errTranscriptCursorInvalid
		}
	}
	request := transcript.MessagePageRequest{
		Path:  p.path,
		Limit: limit,
		Scope: transcript.MessagePageScopeAudit,
	}
	if cursor != "" {
		request.SnapshotSize = state.snapshotSize
		request.Boundary = state.boundary
		request.ExpectedFile = state.fileInfo
	}
	page, err := transcript.LoadMessagePage(request)
	if err != nil {
		if cursor == "" && errors.Is(err, os.ErrNotExist) {
			return TranscriptPageResponse{Messages: []SnapshotMessage{}}, nil
		}
		return TranscriptPageResponse{}, err
	}

	seen := cloneTranscriptSeenRecords(state.seenRecords)
	if seen == nil {
		seen = make(map[string]int64, len(page.Entries))
	}
	messages, err := snapshotTranscriptEntries(page.Entries, seen)
	if err != nil {
		return TranscriptPageResponse{}, err
	}
	response := TranscriptPageResponse{
		Messages:     messages,
		HasMore:      page.HasMore,
		SnapshotSize: page.SnapshotSize,
		BytesRead:    page.BytesRead + page.CompatibilityBytes,
		Corruptions:  page.Corruptions,
	}
	if page.HasMore {
		next := transcriptCursorState{
			path:         p.path,
			snapshotSize: page.SnapshotSize,
			boundary:     page.Next,
			fileInfo:     page.FileInfo,
			seenRecords:  seen,
		}
		var cursorErr error
		if cursor == "" {
			response.NextCursor, cursorErr = p.add(next)
		} else {
			response.NextCursor, cursorErr = p.addNext(cursor, next)
		}
		if cursorErr != nil {
			return TranscriptPageResponse{}, cursorErr
		}
	}
	return response, nil
}

func normalizeTranscriptPageLimit(limit int) int {
	if limit <= 0 {
		return defaultTranscriptPageLimit
	}
	if limit > maxTranscriptPageLimit {
		return maxTranscriptPageLimit
	}
	return limit
}

func (p *transcriptPager) latest(limit int) ([]SnapshotMessage, error) {
	if p == nil || p.path == "" {
		return nil, errTranscriptUnavailable
	}
	page, err := transcript.LoadMessagePage(transcript.MessagePageRequest{
		Path:  p.path,
		Limit: normalizeTranscriptPageLimit(limit),
		Scope: transcript.MessagePageScopeAudit,
	})
	if errors.Is(err, os.ErrNotExist) {
		return []SnapshotMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	return snapshotTranscriptEntries(page.Entries, nil)
}

func snapshotTranscriptEntries(
	entries []transcript.MessagePageEntry,
	seenRecords map[string]int64,
) ([]SnapshotMessage, error) {
	messages := make([]SnapshotMessage, 0, len(entries))
	for _, entry := range entries {
		if seenRecords != nil {
			recordKey := entry.Identity.Record.Key()
			if offset, exists := seenRecords[recordKey]; exists && offset != entry.RecordOffset {
				return nil, fmt.Errorf(
					"%w: durable record %s moved",
					transcript.ErrTranscriptEntryIdentityConflict,
					recordKey,
				)
			}
			seenRecords[recordKey] = entry.RecordOffset
		}
		message, visible := snapshotTranscriptMessage(entry)
		if visible {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func snapshotTranscriptMessage(
	entry transcript.MessagePageEntry,
) (SnapshotMessage, bool) {
	message := entry.Message
	if message == nil || message.Role == schema.System {
		return SnapshotMessage{}, false
	}
	if isMeta, _ := message.Extra["is_meta"].(bool); isMeta {
		return SnapshotMessage{}, false
	}
	toolCalls := make([]SnapshotToolCall, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		toolCalls = append(toolCalls, SnapshotToolCall{
			ID:           call.ID,
			Name:         call.Function.Name,
			InputPreview: truncateSnapshotText(call.Function.Arguments, 512),
			Input:        call.Function.Arguments,
		})
	}
	return SnapshotMessage{
		ID:               entry.Identity.Key(),
		Role:             string(message.Role),
		Content:          message.Content,
		ReasoningContent: message.ReasoningContent,
		ToolCallID:       message.ToolCallID,
		ToolName:         message.ToolName,
		ToolCalls:        toolCalls,
		Completed:        true,
		Timestamp:        entry.Timestamp,
		Kind:             entry.Kind,
		Source:           "durable",
	}, true
}

func (p *transcriptPager) cursor(token string) (transcriptCursorState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.states[token]
	if !ok {
		return transcriptCursorState{}, false
	}
	state.seenRecords = cloneTranscriptSeenRecords(state.seenRecords)
	return state, true
}

func (p *transcriptPager) add(state transcriptCursorState) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	token = "transcript-" + token
	p.addLocked(token, state)
	return token, nil
}

func (p *transcriptPager) addNext(
	parentToken string,
	state transcriptCursorState,
) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	parent, ok := p.states[parentToken]
	if ok && parent.nextToken != "" {
		if _, exists := p.states[parent.nextToken]; exists {
			return parent.nextToken, nil
		}
	}
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	token = "transcript-" + token
	p.addLocked(token, state)
	if ok {
		parent.nextToken = token
		p.states[parentToken] = parent
	}
	return token, nil
}

func (p *transcriptPager) addLocked(token string, state transcriptCursorState) {
	state.seenRecords = cloneTranscriptSeenRecords(state.seenRecords)
	p.states[token] = state
	p.order = append(p.order, token)
	for len(p.order) > maxTranscriptPageCursors {
		oldest := p.order[0]
		p.order = p.order[1:]
		delete(p.states, oldest)
	}
}

func cloneTranscriptSeenRecords(source map[string]int64) map[string]int64 {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]int64, len(source))
	for key, offset := range source {
		cloned[key] = offset
	}
	return cloned
}

func isTranscriptPagingConflict(err error) bool {
	return errors.Is(err, transcript.ErrTranscriptRevisionChanged) ||
		errors.Is(err, transcript.ErrTranscriptPageCursorInvalid) ||
		errors.Is(err, transcript.ErrTranscriptEntryIdentityConflict)
}

func isTranscriptRecordTooLarge(err error) bool {
	return errors.Is(err, transcript.ErrTranscriptPageRecordTooLarge)
}
