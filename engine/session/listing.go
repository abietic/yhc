// Package session provides session listing, metadata extraction, and storage
// path management. Mirrors the reference listSessionsImpl.ts and
// sessionStoragePortable.ts behavior.
//
// This is a partial port: it implements the core listing/metadata extraction
// behavior compatible with the existing transcript persistence format
// (engine/transcript/persist.go). The existing format uses JSONL entries with
// {timestamp, kind, message, replacements} fields. This module also recognizes
// extended metadata entries (custom-title, tag, last-prompt, git-branch, cwd)
// for forward compatibility with the reference's richer entry types.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abietic/yhc/internal/identity"
)

// liteReadBufSize is the head/tail read buffer size for lightweight metadata
// extraction. Mirrors LITE_READ_BUF_SIZE (64KB) from the reference.
const liteReadBufSize = 65536

// maxSanitizedLength is the maximum path component length before hash truncation.
// Mirrors MAX_SANITIZED_LENGTH from the reference.
const maxSanitizedLength = 200

// DurableMediaState is the bounded lightweight listing projection. Listing
// never resolves private media bytes.
type DurableMediaState string

const (
	DurableMediaNone          DurableMediaState = "none"
	DurableMediaRefs          DurableMediaState = "refs"
	DurableMediaRecordCorrupt DurableMediaState = "record_corrupt"
	DurableMediaUnknown       DurableMediaState = "unknown"
)

// ErrLegacySessionImportRequired reports that a read-only legacy discovery
// result must complete the owner-coordinated bundle import before any operation
// that can mutate the transcript, WorkBoard, catalog, or derived state.
var ErrLegacySessionImportRequired = errors.New("legacy session import required")

// SessionInfo holds metadata about a session extracted from stat + head/tail reads.
// Mirrors the reference SessionInfo type from listSessionsImpl.ts.
type SessionInfo struct {
	SessionID       string
	Summary         string
	LastModified    time.Time
	FileSize        int64
	CustomTitle     string
	FirstPrompt     string
	GitBranch       string
	CWD             string
	Tag             string
	CreatedAt       time.Time
	ParentSessionID string // Set when this session was branched from another.
	ParentThreadID  string // Parent runtime thread for Agent/sidechain sessions.
	ParentAgentID   string // Parent Agent identity for nested Agent sessions.
	BranchName      string // Human-readable branch name (if set).
	Model           string // Model identifier when recoverable from metadata.
	Provider        string // Model provider when recoverable from metadata.
	ModelBinding    ModelBindingProjection
	ThreadID        string // Runtime thread identity when persisted.
	AgentID         string // Local Agent identity when this is an Agent transcript.
	AgentName       string // User-facing Agent name/nickname.
	AgentRole       string // Agent role/type.
	Status          string // Last persisted session/Agent status.
	PermissionMode  string // Last persisted permission mode.
	WorktreePath    string // Associated worktree path when present.
	WorktreeBranch  string // Associated worktree branch when present.
	TranscriptDir   string // Source store for cross-project resume.
	TranscriptPath  string // Exact source file; not persisted into the transcript.
	ReadOnly        bool   // Discovery source is visible but cannot be mutated in place.
	NeedsImport     bool   // A canonical session bundle must commit before mutation.
	DurableMedia    DurableMediaState
	sourceCWD       string // Physical catalog/current-root owner; never projected on a wire.
}

// StableKey identifies a picker row even when pages move because a transcript
// receives new activity. Session IDs are normally unique, while the source path
// disambiguates imported/copied transcripts.
func (s SessionInfo) StableKey() string {
	if s.TranscriptPath != "" {
		return filepath.Clean(s.TranscriptPath)
	}
	if s.TranscriptDir != "" {
		return filepath.Join(filepath.Clean(s.TranscriptDir), s.SessionID+".jsonl")
	}
	return s.SessionID
}

// HasResolvedSource reports whether this value retains the non-wire physical
// root provenance attached by QuerySessions or ResolveSession.
func (s SessionInfo) HasResolvedSource() bool { return s.sourceCWD != "" }

// SortOrder specifies how sessions are sorted in listing results.
type SortOrder int

const (
	// SortNewestFirst sorts sessions by last modified time, newest first (default).
	SortNewestFirst SortOrder = iota
	// SortOldestFirst sorts sessions by last modified time, oldest first.
	SortOldestFirst
	// SortMostMessages sorts sessions by file size descending (proxy for message count).
	SortMostMessages
)

// ListOptions configures session listing behavior.
type ListOptions struct {
	// Dir is the project directory to list sessions for.
	// When empty, uses the transcript directory directly.
	Dir string
	// Limit is the maximum number of sessions to return. 0 means no limit.
	Limit int
	// Offset is the number of sessions to skip for pagination.
	Offset int
	// TranscriptDir overrides the default session storage directory.
	// When set, sessions are listed from this directory directly.
	TranscriptDir string
	// Filter applies filtering criteria to the session list.
	Filter *ListFilter
	// Sort specifies the sort order for results. Default is SortNewestFirst.
	Sort SortOrder
}

// ListFilter specifies filtering criteria for session listing.
// All non-zero fields are ANDed together.
type ListFilter struct {
	// After filters sessions modified after this time (inclusive).
	After time.Time
	// Before filters sessions modified before this time (inclusive).
	Before time.Time
	// Model filters sessions that used this model (substring match, case-insensitive).
	Model string
	// GitBranch filters sessions associated with this git branch (exact match).
	GitBranch string
	// Search performs a case-insensitive substring search in the session summary/first prompt.
	Search string
}

// ListResult contains the session listing results with pagination metadata.
type ListResult struct {
	// Sessions is the filtered and sorted list of sessions for the current page.
	Sessions []SessionInfo
	// Total is the total number of sessions matching the filter (before pagination).
	Total int
	// HasMore indicates whether there are more sessions beyond the current page.
	HasMore bool
}

// ListSessions returns session metadata sorted by last-modified descending.
// It scans the transcript directory for .jsonl session files, extracts
// metadata from each via lightweight head/tail reads, and applies
// filtering, sorting, and pagination. Mirrors listSessionsImpl from the reference.
func ListSessions(opts ListOptions) ([]SessionInfo, error) {
	result, err := ListSessionsWithResult(opts)
	if err != nil {
		return nil, err
	}
	return result.Sessions, nil
}

// ListSessionsWithResult returns session metadata with pagination info.
// This is the full-featured listing function supporting filters, sort, and pagination.
// Consistent results regardless of caller (TUI/headless/ACP).
func ListSessionsWithResult(opts ListOptions) (*ListResult, error) {
	dir := opts.TranscriptDir
	if dir == "" {
		dir = GetSessionDir(opts.Dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ListResult{}, nil
		}
		return nil, fmt.Errorf("read session dir: %w", err)
	}

	// Collect candidates: stat each .jsonl file with a valid session ID name.
	type candidate struct {
		sessionID string
		path      string
		mtime     time.Time
		size      int64
	}

	var candidates []candidate
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(name, ".jsonl")
		if !isValidSessionFileID(sessionID) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Size() == 0 {
			continue
		}

		// Apply time-range pre-filter on mtime before reading file content.
		if opts.Filter != nil {
			if !opts.Filter.After.IsZero() && info.ModTime().Before(opts.Filter.After) {
				continue
			}
			if !opts.Filter.Before.IsZero() && info.ModTime().After(opts.Filter.Before) {
				continue
			}
		}

		candidates = append(candidates, candidate{
			sessionID: sessionID,
			path:      filepath.Join(dir, name),
			mtime:     info.ModTime(),
			size:      info.Size(),
		})
	}

	// Read metadata from each candidate and apply content-based filters.
	var sessions []SessionInfo
	for _, c := range candidates {
		info, err := ReadSessionLite(c.path)
		if err != nil || info == nil {
			continue
		}
		// Use stat mtime as the authoritative last-modified time.
		info.LastModified = c.mtime
		info.FileSize = c.size
		info.SessionID = c.sessionID
		info.TranscriptDir = dir
		info.TranscriptPath = c.path

		// Apply content-based filters.
		if opts.Filter != nil && !matchesFilter(info, opts.Filter) {
			continue
		}

		sessions = append(sessions, *info)
	}

	// Sort based on the requested order.
	sortSessions(sessions, opts.Sort)

	// Calculate pagination.
	total := len(sessions)
	hasMore := false

	// Apply offset.
	if opts.Offset > 0 {
		if opts.Offset >= len(sessions) {
			return &ListResult{Total: total, HasMore: false}, nil
		}
		sessions = sessions[opts.Offset:]
	}

	// Apply limit.
	if opts.Limit > 0 && len(sessions) > opts.Limit {
		hasMore = true
		sessions = sessions[:opts.Limit]
	}

	return &ListResult{
		Sessions: sessions,
		Total:    total,
		HasMore:  hasMore,
	}, nil
}

// matchesFilter checks if a session matches the content-based filter criteria.
func matchesFilter(info *SessionInfo, filter *ListFilter) bool {
	if filter.Model != "" {
		model := strings.ToLower(filter.Model)
		if !strings.Contains(strings.ToLower(info.Model), model) &&
			!strings.Contains(strings.ToLower(info.Provider), model) {
			return false
		}
	}

	if filter.GitBranch != "" {
		if info.GitBranch != filter.GitBranch {
			return false
		}
	}

	if filter.Search != "" {
		searchLower := strings.ToLower(filter.Search)
		summaryLower := strings.ToLower(info.Summary)
		firstPromptLower := strings.ToLower(info.FirstPrompt)
		titleLower := strings.ToLower(info.CustomTitle)
		if !strings.Contains(summaryLower, searchLower) &&
			!strings.Contains(firstPromptLower, searchLower) &&
			!strings.Contains(titleLower, searchLower) &&
			!strings.Contains(strings.ToLower(info.SessionID), searchLower) &&
			!strings.Contains(strings.ToLower(info.CWD), searchLower) &&
			!strings.Contains(strings.ToLower(info.Tag), searchLower) &&
			!strings.Contains(strings.ToLower(info.ParentSessionID), searchLower) &&
			!strings.Contains(strings.ToLower(info.AgentName), searchLower) &&
			!strings.Contains(strings.ToLower(info.AgentRole), searchLower) &&
			!strings.Contains(strings.ToLower(info.WorktreeBranch), searchLower) {
			return false
		}
	}

	return true
}

// sortSessions sorts the session list according to the specified order.
func sortSessions(sessions []SessionInfo, order SortOrder) {
	switch order {
	case SortOldestFirst:
		sort.Slice(sessions, func(i, j int) bool {
			if !sessions[i].LastModified.Equal(sessions[j].LastModified) {
				return sessions[i].LastModified.Before(sessions[j].LastModified)
			}
			return sessions[i].SessionID < sessions[j].SessionID
		})
	case SortMostMessages:
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].FileSize != sessions[j].FileSize {
				return sessions[i].FileSize > sessions[j].FileSize
			}
			return sessions[i].SessionID > sessions[j].SessionID
		})
	default: // SortNewestFirst
		sort.Slice(sessions, func(i, j int) bool {
			if !sessions[i].LastModified.Equal(sessions[j].LastModified) {
				return sessions[i].LastModified.After(sessions[j].LastModified)
			}
			return sessions[i].SessionID > sessions[j].SessionID
		})
	}
}

func isValidSessionFileID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return filepath.Base(id) == id && !strings.ContainsAny(id, `/\\`)
}

// GetSessionDir returns the canonical project-owned session storage directory.
// The explicit CLAUDE_TRANSCRIPT_DIR compatibility override remains available
// only when no project path was supplied; it never creates an implicit third
// discovery root.
func GetSessionDir(projectPath string) string {
	if projectPath == "" {
		if dir := os.Getenv("CLAUDE_TRANSCRIPT_DIR"); dir != "" {
			return dir
		}
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		projectPath = cwd
	}
	return filepath.Join(projectPath, identity.ProjectDirName, "transcripts")
}

// SanitizeProjectPath converts an absolute project path to a safe directory name.
// Replaces all non-alphanumeric characters with hyphens. For paths exceeding
// maxSanitizedLength, truncates and appends a hash suffix for uniqueness.
// Mirrors sanitizePath from sessionStoragePortable.ts.
func SanitizeProjectPath(name string) string {
	sanitized := nonAlphanumericRe.ReplaceAllString(name, "-")
	if len(sanitized) <= maxSanitizedLength {
		return sanitized
	}
	hash := djb2Hash(name)
	return sanitized[:maxSanitizedLength] + "-" + strconv.FormatUint(uint64(hash), 36)
}

// ReadSessionLite reads only the first and last portions of a session file
// to extract metadata without parsing the entire file. For small files
// (<=liteReadBufSize), reads the full content. Returns nil if the session
// should be filtered out (e.g., sidechain sessions, metadata-only sessions
// with no extractable summary).
// Mirrors readSessionLite + parseSessionInfoFromLite from the reference.
func ReadSessionLite(filePath string) (*SessionInfo, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() == 0 {
		return nil, nil
	}

	fileSize := stat.Size()

	// Read head.
	headBuf := make([]byte, liteReadBufSize)
	headN, err := f.ReadAt(headBuf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	head := string(headBuf[:headN])

	// Read tail (same as head for small files).
	tail := head
	tailOffset := fileSize - int64(liteReadBufSize)
	if tailOffset > 0 {
		tailBuf := make([]byte, liteReadBufSize)
		tailN, err := f.ReadAt(tailBuf, tailOffset)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		tail = string(tailBuf[:tailN])
	}

	mediaState := detectLiteDurableMediaState(head, tail, fileSize)
	info := parseSessionInfo(head, tail, stat)
	if info == nil {
		summary := durableMediaSummary(mediaState)
		if summary == "" {
			return nil, nil
		}
		info = &SessionInfo{
			Summary:      summary,
			FirstPrompt:  summary,
			LastModified: stat.ModTime(),
			FileSize:     stat.Size(),
		}
	}
	info.DurableMedia = mediaState
	return info, nil
}

func durableMediaSummary(state DurableMediaState) string {
	switch state {
	case DurableMediaRefs:
		return "[media input]"
	case DurableMediaRecordCorrupt:
		return "[corrupt media record]"
	default:
		return ""
	}
}

func detectLiteDurableMediaState(
	head string,
	tail string,
	fileSize int64,
) DurableMediaState {
	if fileSize <= liteReadBufSize {
		return durableMediaStateFromCompleteJSONL(head)
	}
	for _, region := range []string{
		completeHeadLines(head),
		completeTailLines(tail),
	} {
		state := durableMediaStateFromCompleteJSONL(region)
		if state == DurableMediaRecordCorrupt || state == DurableMediaRefs {
			return state
		}
	}
	return DurableMediaUnknown
}

func completeHeadLines(content string) string {
	if strings.HasSuffix(content, "\n") {
		return content
	}
	if index := strings.LastIndexByte(content, '\n'); index >= 0 {
		return content[:index+1]
	}
	return ""
}

func completeTailLines(content string) string {
	if content == "" {
		return ""
	}
	if index := strings.IndexByte(content, '\n'); index >= 0 {
		return content[index+1:]
	}
	return ""
}

func durableMediaStateFromCompleteJSONL(content string) DurableMediaState {
	type mediaRecord struct {
		Kind           string          `json:"kind"`
		UserPrompt     json.RawMessage `json:"user_prompt"`
		PromptMessages json.RawMessage `json:"prompt_messages"`
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, `"user_prompt"`) &&
			!strings.Contains(line, `"prompt_messages"`) &&
			!strings.Contains(line, `"user-prompt"`) {
			continue
		}
		var record mediaRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return DurableMediaRecordCorrupt
		}
		hasUserPrompt := len(record.UserPrompt) > 0 &&
			string(record.UserPrompt) != "null"
		hasPromptMessages := len(record.PromptMessages) > 0 &&
			string(record.PromptMessages) != "null" &&
			string(record.PromptMessages) != "[]"
		if record.Kind == "user-prompt" && !hasUserPrompt {
			return DurableMediaRecordCorrupt
		}
		if hasUserPrompt || hasPromptMessages {
			return DurableMediaRefs
		}
	}
	return DurableMediaNone
}

// parseSessionInfo extracts SessionInfo fields from head/tail content and file stat.
// Mirrors parseSessionInfoFromLite from the reference.
func parseSessionInfo(head, tail string, stat os.FileInfo) *SessionInfo {
	// Check for sidechain sessions (first line).
	firstNewline := strings.IndexByte(head, '\n')
	firstLine := head
	if firstNewline >= 0 {
		firstLine = head[:firstNewline]
	}
	if strings.Contains(firstLine, `"isSidechain":true`) ||
		strings.Contains(firstLine, `"isSidechain": true`) {
		return nil
	}

	info := &SessionInfo{
		LastModified: stat.ModTime(),
		FileSize:     stat.Size(),
	}

	// Extract custom title (last occurrence in tail, then head).
	info.CustomTitle = extractLastJSONStringField(tail, "customTitle")
	if info.CustomTitle == "" {
		info.CustomTitle = extractLastJSONStringField(head, "customTitle")
	}
	// Fall back to AI title.
	if info.CustomTitle == "" {
		info.CustomTitle = extractLastJSONStringField(tail, "aiTitle")
		if info.CustomTitle == "" {
			info.CustomTitle = extractLastJSONStringField(head, "aiTitle")
		}
	}

	// Extract first prompt from head.
	info.FirstPrompt = extractFirstPromptFromHead(head)

	// Extract createdAt from first entry's timestamp.
	firstTimestamp := extractJSONStringField(head, "timestamp")
	if firstTimestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, firstTimestamp); err == nil {
			info.CreatedAt = t
		} else if t, err := time.Parse(time.RFC3339, firstTimestamp); err == nil {
			info.CreatedAt = t
		}
	}

	// Build summary: customTitle > lastPrompt > summary > firstPrompt.
	info.Summary = info.CustomTitle
	if info.Summary == "" {
		info.Summary = extractLastJSONStringField(tail, "lastPrompt")
	}
	if info.Summary == "" {
		info.Summary = extractLastJSONStringField(tail, "summary")
	}
	if info.Summary == "" {
		info.Summary = info.FirstPrompt
	}

	// Skip metadata-only sessions with no summary.
	if info.Summary == "" {
		return nil
	}

	// Extract git branch.
	info.GitBranch = extractLastJSONStringField(tail, "gitBranch")
	if info.GitBranch == "" {
		info.GitBranch = extractJSONStringField(head, "gitBranch")
	}
	// Also check metadata entry format: "meta_key":"git_branch","meta_value":"..."
	if info.GitBranch == "" {
		info.GitBranch = extractMetaValue(tail, "git_branch")
	}
	if info.GitBranch == "" {
		info.GitBranch = extractMetaValue(head, "git_branch")
	}

	// Extract CWD.
	info.CWD = extractJSONStringField(head, "cwd")
	// Also check metadata entry format.
	if info.CWD == "" {
		info.CWD = extractMetaValue(head, "cwd")
	}
	if info.CWD == "" {
		info.CWD = extractMetaValue(tail, "cwd")
	}

	// Prefer the latest full metadata entry, then fall back to message extras.
	full := extractSessionMetadataFull(tail)
	if full == nil {
		full = extractSessionMetadataFull(head)
	}
	if full != nil {
		info.Model = full.Model
		info.Provider = full.Provider
		info.ModelBinding = SafeModelBindingProjection(full.ModelBinding)
		if full.CWD != "" {
			info.CWD = full.CWD
		}
		if full.GitBranch != "" {
			info.GitBranch = full.GitBranch
		}
		if full.ParentSessionID != "" {
			info.ParentSessionID = full.ParentSessionID
		}
		info.ParentThreadID = full.ParentThreadID
		info.ParentAgentID = full.ParentAgentID
		info.ThreadID = full.ThreadID
		info.AgentID = full.AgentID
		info.AgentName = full.AgentName
		info.AgentRole = full.AgentRole
		info.Status = full.Status
		info.PermissionMode = full.PermissionMode
		info.WorktreePath = full.WorktreePath
		info.WorktreeBranch = full.WorktreeBranch
	}
	if info.Model == "" {
		info.Model = extractLastJSONStringField(tail, "model")
	}
	if info.Model == "" {
		info.Model = extractLastJSONStringField(head, "model")
	}
	if info.Provider == "" {
		info.Provider = extractLastJSONStringField(tail, "provider")
	}
	if info.Provider == "" {
		info.Provider = extractLastJSONStringField(head, "provider")
	}

	// Extract tag (scoped to {"type":"tag"} lines to avoid collision).
	info.Tag = extractTagFromLines(tail)

	// Extract branch lineage metadata (parent_session_id and branch_name).
	// These are stored as metadata entries: {"kind":"metadata","meta_key":"parent_session_id","meta_value":"..."}
	info.ParentSessionID = extractMetaValue(head, "parent_session_id")
	if info.ParentSessionID == "" {
		info.ParentSessionID = extractMetaValue(tail, "parent_session_id")
	}
	info.BranchName = extractMetaValue(head, "branch_name")
	if info.BranchName == "" {
		info.BranchName = extractMetaValue(tail, "branch_name")
	}
	if info.Status == "" {
		info.Status = extractMetaValue(tail, "status")
	}
	if info.ModelBinding.State == "" {
		info.ModelBinding.State = ModelBindingStateAbsent
	}

	return info
}

// extractTagFromLines searches for the last {"type":"tag"} line and extracts
// the tag field. This avoids collision with tool_use inputs containing tag params.
func extractTagFromLines(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], `{"type":"tag"`) {
			if tag := extractLastJSONStringField(lines[i], "tag"); tag != "" {
				return tag
			}
		}
	}
	return ""
}

// extractMetaValue extracts the value of a metadata entry from JSONL text.
// Metadata entries use the format: {"kind":"metadata","meta_key":"<key>","meta_value":"<value>"}
func extractMetaValue(text, metaKey string) string {
	// Look for the pattern: "meta_key":"<metaKey>","meta_value":"<value>"
	// or with spaces: "meta_key": "<metaKey>", "meta_value": "<value>"
	patterns := []string{
		fmt.Sprintf(`"meta_key":"%s","meta_value":"`, metaKey),
		fmt.Sprintf(`"meta_key": "%s", "meta_value": "`, metaKey),
		fmt.Sprintf(`"meta_key":"%s", "meta_value":"`, metaKey),
		fmt.Sprintf(`"meta_key": "%s","meta_value": "`, metaKey),
	}
	for _, pattern := range patterns {
		idx := strings.Index(text, pattern)
		if idx < 0 {
			continue
		}
		valueStart := idx + len(pattern)
		return extractQuotedValue(text, valueStart)
	}
	return ""
}

func extractSessionMetadataFull(text string) *SessionMetadataFull {
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if !strings.Contains(lines[index], `"meta_key":"session_metadata_full"`) &&
			!strings.Contains(lines[index], `"meta_key": "session_metadata_full"`) {
			continue
		}
		var entry struct {
			MetaKey   string `json:"meta_key"`
			MetaValue string `json:"meta_value"`
		}
		if err := json.Unmarshal([]byte(lines[index]), &entry); err != nil || entry.MetaValue == "" {
			continue
		}
		var metadata SessionMetadataFull
		if err := json.Unmarshal([]byte(entry.MetaValue), &metadata); err == nil {
			return &metadata
		}
	}
	return nil
}

// extractFirstPromptFromHead extracts the first meaningful user prompt from
// the JSONL head chunk. Skips tool_result messages, isMeta, isCompactSummary,
// and auto-generated patterns. Mirrors extractFirstPromptFromHead from
// sessionStoragePortable.ts.
func extractFirstPromptFromHead(head string) string {
	start := 0
	for start < len(head) {
		newlineIdx := strings.IndexByte(head[start:], '\n')
		var line string
		if newlineIdx >= 0 {
			line = head[start : start+newlineIdx]
			start = start + newlineIdx + 1
		} else {
			line = head[start:]
			start = len(head)
		}

		// Only process user messages.
		if !strings.Contains(line, `"type":"user"`) &&
			!strings.Contains(line, `"type": "user"`) {
			// Also check for the existing Go format which uses "kind":"user".
			if !strings.Contains(line, `"kind":"user"`) &&
				!strings.Contains(line, `"kind": "user"`) {
				continue
			}
		}

		// Skip tool_result messages.
		if strings.Contains(line, `"tool_result"`) {
			continue
		}
		// Skip isMeta messages.
		if strings.Contains(line, `"isMeta":true`) || strings.Contains(line, `"isMeta": true`) {
			continue
		}
		// Skip compact summary messages.
		if strings.Contains(line, `"isCompactSummary":true`) || strings.Contains(line, `"isCompactSummary": true`) {
			continue
		}

		// Try to parse and extract text content.
		text := extractUserTextFromLine(line)
		if text == "" {
			continue
		}

		// Flatten newlines and trim.
		result := strings.Join(strings.Fields(text), " ")
		if result == "" {
			continue
		}

		// Skip non-meaningful messages (XML tags, interrupt markers).
		if skipFirstPromptPattern.MatchString(result) {
			continue
		}

		// Truncate to 200 chars.
		if len(result) > 200 {
			result = result[:200] + "\u2026"
		}

		return result
	}
	return ""
}

// extractUserTextFromLine extracts the text content from a user message JSONL line.
// Handles both the reference format (type/message/content) and the existing Go
// format (kind/message with Role/Content fields from eino schema).
func extractUserTextFromLine(line string) string {
	// Quick extraction: try to find text content without full JSON parse.
	// For the existing Go format, message content is in message.Content array.
	var entry struct {
		Kind    string          `json:"kind"`
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return ""
	}

	// Parse the message field.
	var msg struct {
		Content json.RawMessage `json:"content"`
		Role    string          `json:"role"`
	}
	if err := json.Unmarshal(entry.Message, &msg); err != nil {
		return ""
	}

	// Try content as string.
	var strContent string
	if err := json.Unmarshal(msg.Content, &strContent); err == nil {
		return strContent
	}

	// Try content as array of content blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}

	return ""
}

// extractJSONStringField extracts the first occurrence of a JSON string field
// from raw text without full parsing. Looks for "key":"value" patterns.
// Mirrors extractJsonStringField from the reference.
func extractJSONStringField(text, key string) string {
	patterns := []string{
		fmt.Sprintf(`"%s":"`, key),
		fmt.Sprintf(`"%s": "`, key),
	}
	for _, pattern := range patterns {
		idx := strings.Index(text, pattern)
		if idx < 0 {
			continue
		}
		valueStart := idx + len(pattern)
		return extractQuotedValue(text, valueStart)
	}
	return ""
}

// extractLastJSONStringField finds the LAST occurrence of a JSON string field.
// Useful for fields that are appended (customTitle, tag, etc.).
// Mirrors extractLastJsonStringField from the reference.
func extractLastJSONStringField(text, key string) string {
	patterns := []string{
		fmt.Sprintf(`"%s":"`, key),
		fmt.Sprintf(`"%s": "`, key),
	}
	var lastValue string
	for _, pattern := range patterns {
		searchFrom := 0
		for {
			idx := strings.Index(text[searchFrom:], pattern)
			if idx < 0 {
				break
			}
			absIdx := searchFrom + idx
			valueStart := absIdx + len(pattern)
			if v := extractQuotedValue(text, valueStart); v != "" {
				lastValue = v
			}
			searchFrom = valueStart
		}
	}
	return lastValue
}

// extractQuotedValue reads a JSON string value starting at the given position
// (after the opening quote). Handles escape sequences.
func extractQuotedValue(text string, start int) string {
	i := start
	for i < len(text) {
		if text[i] == '\\' {
			i += 2
			continue
		}
		if text[i] == '"' {
			raw := text[start:i]
			return unescapeJSONString(raw)
		}
		i++
	}
	return ""
}

// unescapeJSONString unescapes a JSON string value.
func unescapeJSONString(raw string) string {
	if !strings.Contains(raw, `\`) {
		return raw
	}
	var s string
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &s); err != nil {
		return raw
	}
	return s
}

// djb2Hash computes a DJB2 hash of the input string.
// Used for hash truncation of long sanitized paths.
func djb2Hash(s string) uint32 {
	var hash uint32 = 5381
	for i := 0; i < len(s); i++ {
		hash = ((hash << 5) + hash) + uint32(s[i])
	}
	return hash
}

// nonAlphanumericRe matches any character that is not alphanumeric.
var nonAlphanumericRe = regexp.MustCompile(`[^a-zA-Z0-9]`)

// skipFirstPromptPattern matches auto-generated or system messages that should
// be skipped when looking for the first meaningful user prompt.
var skipFirstPromptPattern = regexp.MustCompile(
	`^(?:\s*<[a-z][\w-]*[\s>]|\[Request interrupted by user[^\]]*\])`,
)
