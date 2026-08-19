package appserver

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/statepath"
)

const (
	defaultDurableSessionLimit = 50
	maxDurableSessionLimit     = 100
	maxDurableCursorBytes      = 4096
	maxDurableSearchBytes      = 256
	durableTranscriptPageSize  = 100
	maxDurableTranscriptPagers = 256
)

var (
	errDurableSessionNotFound  = errors.New("durable session not found")
	errDurableSessionAmbiguous = errors.New("durable session is ambiguous")
	errDurableServerClosing    = errors.New("app-server is shutting down")
)

func (s *Server) handleListDurableSessions(w http.ResponseWriter, r *http.Request) {
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if len(cursor) > maxDurableCursorBytes {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "session cursor is too long")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(search) > maxDurableSearchBytes {
		writeError(w, http.StatusBadRequest, "invalid_search", "session search is too long")
		return
	}
	limit := defaultDurableSessionLimit
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > maxDurableSessionLimit {
			writeError(
				w,
				http.StatusBadRequest,
				"invalid_limit",
				"session limit must be between 1 and 100",
			)
			return
		}
		limit = parsed
	}
	if s.sessionCatalogPath == "" {
		writeJSON(w, http.StatusOK, DurableSessionListResponse{
			Sessions: []DurableSessionSummary{},
		})
		return
	}
	discoveryCWD := s.discoveryCWD
	if discoveryCWD == "" {
		discoveryCWD = "."
	}
	page, err := enginesession.QuerySessions(s.durableSessionQuery(discoveryCWD, limit, cursor, search))
	if err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"durable_sessions_failed",
			"could not inspect durable sessions",
		)
		return
	}
	response := DurableSessionListResponse{
		Sessions:   make([]DurableSessionSummary, 0, len(page.Sessions)),
		NextCursor: page.NextCursor,
		HasMore:    page.HasMore,
		Scanned:    page.Scanned,
	}
	for _, info := range page.Sessions {
		cwd := strings.TrimSpace(info.CWD)
		status := strings.TrimSpace(info.Status)
		if status == "" {
			status = "saved"
		}
		response.Sessions = append(response.Sessions, DurableSessionSummary{
			ID:              info.SessionID,
			WorkspaceLabel:  workspaceLabel(cwd),
			Status:          status,
			UpdatedAt:       info.LastModified.UTC(),
			GitBranch:       truncateSnapshotText(info.GitBranch, 160),
			ParentSessionID: info.ParentSessionID,
			Resumable: !info.ReadOnly && !info.NeedsImport &&
				isSupportedSessionID(info.SessionID) && cwd != "" &&
				strings.TrimSpace(info.ParentSessionID) == "" &&
				strings.TrimSpace(info.AgentID) == "",
			ImportRequired: info.ReadOnly && info.NeedsImport &&
				isSupportedSessionID(info.SessionID) && cwd != "" &&
				strings.TrimSpace(info.ParentSessionID) == "" &&
				strings.TrimSpace(info.AgentID) == "",
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// durableSessionQuery derives every physical discovery root in-process. The
// renderer supplies only list filters and opaque cursors; it can never select
// a transcript directory or legacy/canonical source.
func (s *Server) durableSessionQuery(discoveryCWD string, limit int, cursor, search string) enginesession.SessionQuery {
	projectRoots, err := statepath.ProjectRoots(discoveryCWD)
	if err != nil {
		// QuerySessions will return the same invalid-root failure with its normal
		// public error projection. Keep the construction path total for callers.
		projectRoots = statepath.Roots{}
	}
	legacyCatalogPath := s.legacySessionCatalogPath()
	return enginesession.SessionQuery{
		Scope:             enginesession.SessionScopeAll,
		CWD:               discoveryCWD,
		TranscriptDir:     filepath.Join(projectRoots.Canonical, "transcripts"),
		CatalogPath:       s.sessionCatalogPath,
		LegacyCatalogPath: legacyCatalogPath,
		Limit:             limit,
		Cursor:            cursor,
		Filter: enginesession.ListFilter{
			Search: search,
		},
	}
}

func (s *Server) legacySessionCatalogPath() string {
	canonicalCatalogPath, legacyCatalogPath := enginesession.DefaultCatalogPaths()
	if canonicalCatalogPath != "" && s.sessionCatalogPath == canonicalCatalogPath {
		return legacyCatalogPath
	}
	return ""
}

// admitDurableSession performs a fresh, canonical server-side admission just
// before live activation. History lookup intentionally does not call it.
func (s *Server) admitDurableSession(
	ctx context.Context,
	discovered enginesession.SessionInfo,
) (enginesession.SessionInfo, error) {
	userRoots, err := enginesession.DefaultSessionImportUserRoots()
	if err != nil {
		return enginesession.SessionInfo{}, err
	}
	return enginesession.AdmitSessionResume(ctx, enginesession.ResumeAdmissionRequest{
		SessionID:         discovered.SessionID,
		CWD:               discovered.CWD,
		CatalogPath:       s.sessionCatalogPath,
		LegacyCatalogPath: s.legacySessionCatalogPath(),
		UserRoots:         userRoots,
	})
}

func durableSessionTitle(info enginesession.SessionInfo) string {
	for _, candidate := range []string{
		info.CustomTitle,
		info.Summary,
		info.FirstPrompt,
	} {
		if title := strings.TrimSpace(candidate); title != "" {
			return truncateSnapshotText(title, 160)
		}
	}
	return baseName(strings.TrimSpace(info.CWD))
}

func (s *Server) resolveDurableTranscript(sessionID string) (*transcriptPager, error) {
	candidate, err := s.resolveDurableSession(sessionID)
	if err != nil {
		return nil, err
	}
	return s.durableTranscriptPager(*candidate)
}

// resolveDurableSession returns only a server-discovered record. Callers must
// never reconstruct the CWD or durable identity from renderer input.
func (s *Server) resolveDurableSession(sessionID string) (*enginesession.SessionInfo, error) {
	sessionID = strings.TrimSpace(sessionID)
	if !isSupportedSessionID(sessionID) || s.sessionCatalogPath == "" {
		return nil, errDurableSessionNotFound
	}
	discoveryCWD := s.discoveryCWD
	if discoveryCWD == "" {
		discoveryCWD = "."
	}

	var candidate *enginesession.SessionInfo
	cursor := ""
	for {
		page, err := enginesession.QuerySessions(s.durableSessionQuery(
			discoveryCWD, durableTranscriptPageSize, cursor, sessionID,
		))
		if err != nil {
			return nil, err
		}
		for index := range page.Sessions {
			info := page.Sessions[index]
			if info.SessionID != sessionID {
				continue
			}
			if candidate != nil && candidate.StableKey() != info.StableKey() {
				return nil, errDurableSessionAmbiguous
			}
			copy := info
			candidate = &copy
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if candidate == nil {
		return nil, errDurableSessionNotFound
	}
	return candidate, nil
}

func (s *Server) durableTranscriptPager(info enginesession.SessionInfo) (*transcriptPager, error) {
	stableKey := info.StableKey()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, errDurableServerClosing
	}
	if pager := s.durableTranscripts[stableKey]; pager != nil {
		return pager, nil
	}
	pager := newTranscriptPager(info.TranscriptPath)
	s.durableTranscripts[stableKey] = pager
	s.durableTranscriptOrder = append(s.durableTranscriptOrder, stableKey)
	for len(s.durableTranscriptOrder) > maxDurableTranscriptPagers {
		oldest := s.durableTranscriptOrder[0]
		s.durableTranscriptOrder = s.durableTranscriptOrder[1:]
		delete(s.durableTranscripts, oldest)
	}
	return pager, nil
}
