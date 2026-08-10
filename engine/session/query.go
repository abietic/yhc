package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultSessionPageSize = 25
	maxSessionPageSize     = 100
	defaultSessionScanCap  = 512
	maxSessionScanCap      = 10_000
	sessionCursorVersion   = 1
)

// ErrSessionCursorInvalid reports an opaque cursor that cannot safely
// continue the requested query.
var ErrSessionCursorInvalid = errors.New("session cursor is invalid")

// SessionScope controls which registered transcript roots participate in a query.
type SessionScope string

const (
	SessionScopeCWD        SessionScope = "cwd"
	SessionScopeRepository SessionScope = "repository"
	SessionScopeAll        SessionScope = "all"
)

// SessionQuery is the bounded, cursor-based session listing contract used by
// interactive pickers. Offset-based ListSessions remains for compatibility.
type SessionQuery struct {
	Scope         SessionScope
	CWD           string
	TranscriptDir string
	CatalogPath   string
	// LegacyCatalogPath is an optional read-only default catalog. Exact catalog
	// overrides leave it empty and therefore receive no implicit union.
	LegacyCatalogPath string
	Limit             int
	Cursor            string
	ScanLimit         int
	Filter            ListFilter
	Sort              SortOrder
	// ActiveOverlay is a caller-frozen process-local projection merged under
	// the same scope, ordering, de-duplication, and page bounds as durable rows.
	ActiveOverlay []SessionInfo
	// BindCandidateGenerations makes cursor continuation fail closed when the
	// durable candidate set or process-local overlay changes.
	BindCandidateGenerations bool
}

// SessionPage is one bounded query result. Total is intentionally omitted:
// computing an exact filtered total would require eagerly reading every file.
type SessionPage struct {
	Sessions   []SessionInfo
	NextCursor string
	HasMore    bool
	Scanned    int
}

type sessionCursor struct {
	Version           int    `json:"v"`
	Fingerprint       string `json:"q"`
	DurableGeneration string `json:"d,omitempty"`
	ActiveGeneration  string `json:"a,omitempty"`
	SortValue         int64  `json:"s"`
	SessionID         string `json:"id"`
	Path              string `json:"p"`
}

type sessionCandidate struct {
	root      SessionRoot
	sessionID string
	path      string
	mtimeNano int64
	size      int64
	active    *SessionInfo
	legacy    bool
}

type sessionQueryRoot struct {
	SessionRoot
	legacy bool
}

// QuerySessions discovers candidates with stat-only IO, then reads head/tail
// metadata incrementally until the page is full or the scan cap is reached.
func QuerySessions(query SessionQuery) (*SessionPage, error) {
	query = normalizeSessionQuery(query)
	roots, err := querySessionRoots(query)
	if err != nil {
		return nil, err
	}
	candidates, err := gatherSessionCandidates(roots, query.Filter)
	if err != nil {
		return nil, err
	}
	candidates = preferCanonicalSessionCandidates(candidates)
	durableGeneration := sessionCandidateGeneration(candidates)
	activeCandidates := activeSessionCandidates(query, roots)
	activeGeneration := sessionCandidateGeneration(activeCandidates)
	candidates = mergeSessionCandidates(candidates, activeCandidates)
	candidates = preferCanonicalSessionCandidates(candidates)
	sortSessionCandidates(candidates, query.Sort)

	fingerprint := sessionQueryFingerprint(query)
	cursor, err := decodeSessionCursor(
		query.Cursor,
		fingerprint,
		query.BindCandidateGenerations,
		durableGeneration,
		activeGeneration,
	)
	if err != nil {
		return nil, err
	}
	start := candidateStart(candidates, cursor, query.Sort)
	page := &SessionPage{Sessions: make([]SessionInfo, 0, query.Limit)}
	seen := make(map[string]struct{}, query.Limit)
	lastScanned := -1
	for index := start; index < len(candidates) && page.Scanned < query.ScanLimit; index++ {
		candidate := candidates[index]
		page.Scanned++
		lastScanned = index
		info, readErr := readSessionCandidate(candidate)
		if readErr != nil || info == nil {
			continue
		}
		if !matchesFilter(info, &query.Filter) {
			continue
		}
		key := info.StableKey()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		page.Sessions = append(page.Sessions, *info)
		if len(page.Sessions) == query.Limit {
			break
		}
	}
	if lastScanned >= 0 && lastScanned+1 < len(candidates) {
		page.HasMore = true
		page.NextCursor, err = encodeSessionCursor(
			fingerprint,
			durableGeneration,
			activeGeneration,
			candidates[lastScanned],
			query.Sort,
		)
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

// ResolveSession returns the exact durable row for one session identity using
// the same canonical/legacy source policy as QuerySessions. A legacy-only row
// remains discoverable but is marked read-only and import-required. Multiple
// same-priority sources remain ambiguous.
func ResolveSession(query SessionQuery, sessionID string) (SessionInfo, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !isValidSessionFileID(sessionID) {
		return SessionInfo{}, errors.New("session ID is invalid")
	}
	query = normalizeSessionQuery(query)
	roots, err := querySessionRoots(query)
	if err != nil {
		return SessionInfo{}, err
	}
	candidates := make([]sessionCandidate, 0, len(roots))
	for _, root := range roots {
		path := filepath.Join(root.TranscriptDir, sessionID+".jsonl")
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return SessionInfo{}, fmt.Errorf("stat session transcript: %w", statErr)
		}
		if !info.Mode().IsRegular() {
			return SessionInfo{}, fmt.Errorf(
				"session transcript is not a regular file: %s",
				path,
			)
		}
		candidates = append(candidates, sessionCandidate{
			root:      root.SessionRoot,
			sessionID: sessionID,
			path:      path,
			mtimeNano: info.ModTime().UnixNano(),
			size:      info.Size(),
			legacy:    root.legacy,
		})
	}
	candidates = preferCanonicalSessionCandidates(candidates)
	matches := make(map[string]SessionInfo)
	for _, candidate := range candidates {
		if candidate.sessionID != sessionID {
			continue
		}
		info, readErr := readSessionCandidate(candidate)
		if readErr != nil {
			return SessionInfo{}, readErr
		}
		if info == nil {
			info = &SessionInfo{
				SessionID:      sessionID,
				CWD:            candidate.root.CWD,
				LastModified:   time.Unix(0, candidate.mtimeNano),
				FileSize:       candidate.size,
				TranscriptDir:  candidate.root.TranscriptDir,
				TranscriptPath: candidate.path,
				ReadOnly:       candidate.legacy,
				NeedsImport:    candidate.legacy,
				sourceCWD:      candidate.root.CWD,
			}
		}
		matches[candidateStableKey(candidate)] = *info
	}
	if len(matches) > 1 {
		return SessionInfo{}, fmt.Errorf(
			"session ID %q is ambiguous across %d transcript roots",
			sessionID,
			len(matches),
		)
	}
	for _, info := range matches {
		return info, nil
	}
	return SessionInfo{}, fmt.Errorf(
		"session %q was not found: %w",
		sessionID,
		os.ErrNotExist,
	)
}

func normalizeSessionQuery(query SessionQuery) SessionQuery {
	switch query.Scope {
	case SessionScopeCWD, SessionScopeRepository, SessionScopeAll:
	default:
		query.Scope = SessionScopeRepository
	}
	if query.Limit <= 0 {
		query.Limit = defaultSessionPageSize
	} else if query.Limit > maxSessionPageSize {
		query.Limit = maxSessionPageSize
	}
	if query.ScanLimit <= 0 {
		query.ScanLimit = defaultSessionScanCap
	} else if query.ScanLimit > maxSessionScanCap {
		query.ScanLimit = maxSessionScanCap
	}
	return query
}

func querySessionRoots(query SessionQuery) ([]sessionQueryRoot, error) {
	cwd, err := canonicalPath(query.CWD)
	if err != nil {
		return nil, err
	}
	transcriptDir := query.TranscriptDir
	if transcriptDir == "" {
		transcriptDir = GetSessionDir(cwd)
	}
	transcriptDir, err = canonicalPath(transcriptDir)
	if err != nil {
		return nil, err
	}
	current := sessionQueryRoot{SessionRoot: SessionRoot{
		CWD:           cwd,
		TranscriptDir: transcriptDir,
		RepositoryKey: repositoryKey(cwd),
	}}
	canonicalRoots, err := LoadSessionRoots(query.CatalogPath)
	if err != nil {
		return nil, err
	}
	roots := make([]sessionQueryRoot, 0, len(canonicalRoots)+2)
	for _, root := range canonicalRoots {
		roots = append(roots, sessionQueryRoot{SessionRoot: root})
	}
	roots = append(roots, current)
	if strings.TrimSpace(query.LegacyCatalogPath) != "" &&
		!samePath(query.LegacyCatalogPath, query.CatalogPath) {
		legacyRoots, loadErr := LoadSessionRoots(query.LegacyCatalogPath)
		if loadErr != nil {
			return nil, loadErr
		}
		for _, root := range legacyRoots {
			roots = append(roots, sessionQueryRoot{
				SessionRoot: root,
				legacy:      true,
			})
		}
	}

	unique := make(map[string]sessionQueryRoot, len(roots))
	for _, root := range roots {
		rootCWD, cwdErr := canonicalPath(root.CWD)
		rootDir, dirErr := canonicalPath(root.TranscriptDir)
		if cwdErr != nil || dirErr != nil || rootDir == "" {
			continue
		}
		root.CWD, root.TranscriptDir = rootCWD, rootDir
		if root.RepositoryKey == "" {
			root.RepositoryKey = repositoryKey(rootCWD)
		}
		switch query.Scope {
		case SessionScopeCWD:
			if !samePath(root.CWD, current.CWD) {
				continue
			}
		case SessionScopeRepository:
			if current.RepositoryKey == "" {
				if !samePath(root.CWD, current.CWD) {
					continue
				}
			} else if root.RepositoryKey != current.RepositoryKey {
				continue
			}
		case SessionScopeAll:
		}
		existing, exists := unique[root.TranscriptDir]
		if !exists || (existing.legacy && !root.legacy) {
			unique[root.TranscriptDir] = root
		}
	}
	result := make([]sessionQueryRoot, 0, len(unique))
	for _, root := range unique {
		result = append(result, root)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].legacy != result[j].legacy {
			return !result[i].legacy
		}
		return result[i].TranscriptDir < result[j].TranscriptDir
	})
	return result, nil
}

func gatherSessionCandidates(roots []sessionQueryRoot, filter ListFilter) ([]sessionCandidate, error) {
	var candidates []sessionCandidate
	for _, root := range roots {
		entries, err := os.ReadDir(root.TranscriptDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read session dir %s: %w", root.TranscriptDir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			sessionID := strings.TrimSuffix(name, ".jsonl")
			if !isValidSessionFileID(sessionID) {
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil || info.Size() == 0 {
				continue
			}
			if !filter.After.IsZero() && info.ModTime().Before(filter.After) {
				continue
			}
			if !filter.Before.IsZero() && info.ModTime().After(filter.Before) {
				continue
			}
			candidates = append(candidates, sessionCandidate{
				root:      root.SessionRoot,
				sessionID: sessionID,
				path:      filepath.Join(root.TranscriptDir, name),
				mtimeNano: info.ModTime().UnixNano(),
				size:      info.Size(),
				legacy:    root.legacy,
			})
		}
	}
	return candidates, nil
}

func activeSessionCandidates(
	query SessionQuery,
	roots []sessionQueryRoot,
) []sessionCandidate {
	overlay := append([]SessionInfo(nil), query.ActiveOverlay...)
	candidates := make([]sessionCandidate, 0, len(overlay))
	for index := range overlay {
		info := overlay[index]
		if strings.TrimSpace(info.SessionID) == "" {
			continue
		}
		if info.CWD == "" {
			info.CWD = query.CWD
		}
		root, ok := activeSessionRoot(info, roots)
		if !ok {
			continue
		}
		info.CWD = root.CWD
		info.TranscriptDir = root.TranscriptDir
		info.TranscriptPath = filepath.Join(
			root.TranscriptDir,
			info.SessionID+".jsonl",
		)
		if info.LastModified.IsZero() {
			info.LastModified = info.CreatedAt
		}
		if !query.Filter.After.IsZero() &&
			info.LastModified.Before(query.Filter.After) {
			continue
		}
		if !query.Filter.Before.IsZero() &&
			info.LastModified.After(query.Filter.Before) {
			continue
		}
		copied := info
		candidates = append(candidates, sessionCandidate{
			root:      root,
			sessionID: copied.SessionID,
			path:      copied.StableKey(),
			mtimeNano: copied.LastModified.UnixNano(),
			size:      copied.FileSize,
			active:    &copied,
		})
	}
	return candidates
}

func activeSessionRoot(
	info SessionInfo,
	roots []sessionQueryRoot,
) (SessionRoot, bool) {
	activeCWD, err := canonicalPath(info.CWD)
	if err != nil {
		return SessionRoot{}, false
	}
	for _, root := range roots {
		if !root.legacy && samePath(activeCWD, root.CWD) {
			return root.SessionRoot, true
		}
	}
	return SessionRoot{}, false
}

func mergeSessionCandidates(
	durable []sessionCandidate,
	active []sessionCandidate,
) []sessionCandidate {
	merged := make([]sessionCandidate, 0, len(durable)+len(active))
	seen := make(map[string]struct{}, len(durable)+len(active))
	for _, candidate := range durable {
		key := candidateStableKey(candidate)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, candidate)
	}
	for _, candidate := range active {
		key := candidateStableKey(candidate)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, candidate)
	}
	return merged
}

// preferCanonicalSessionCandidates removes only a legacy candidate shadowed by
// the same repository/session identity in a canonical root. Same-priority
// duplicates remain visible so mutation resolution can retain its existing
// ambiguity refusal.
func preferCanonicalSessionCandidates(candidates []sessionCandidate) []sessionCandidate {
	canonical := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !candidate.legacy {
			canonical[sessionCandidateIdentity(candidate)] = struct{}{}
		}
	}
	filtered := make([]sessionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.legacy {
			if _, shadowed := canonical[sessionCandidateIdentity(candidate)]; shadowed {
				continue
			}
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func sessionCandidateIdentity(candidate sessionCandidate) string {
	repository := strings.TrimSpace(candidate.root.RepositoryKey)
	if repository == "" {
		repository, _ = canonicalPath(candidate.root.CWD)
	}
	return repository + "\x00" + candidate.sessionID
}

func candidateStableKey(candidate sessionCandidate) string {
	if candidate.path != "" {
		return filepath.Clean(candidate.path)
	}
	return candidate.sessionID
}

func sessionCandidateGeneration(candidates []sessionCandidate) string {
	type generationRow struct {
		Active        bool   `json:"active"`
		Legacy        bool   `json:"legacy"`
		SessionID     string `json:"session_id"`
		Path          string `json:"path"`
		CWD           string `json:"cwd"`
		TranscriptDir string `json:"transcript_dir"`
		Modified      int64  `json:"modified"`
		Size          int64  `json:"size"`
	}
	rows := make([]generationRow, 0, len(candidates))
	for _, candidate := range candidates {
		rows = append(rows, generationRow{
			Active:        candidate.active != nil,
			Legacy:        candidate.legacy,
			SessionID:     candidate.sessionID,
			Path:          candidate.path,
			CWD:           candidate.root.CWD,
			TranscriptDir: candidate.root.TranscriptDir,
			Modified:      candidate.mtimeNano,
			Size:          candidate.size,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].SessionID < rows[j].SessionID
	})
	data, _ := json.Marshal(rows)
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func readSessionCandidate(
	candidate sessionCandidate,
) (*SessionInfo, error) {
	if candidate.active != nil {
		copied := *candidate.active
		copied.sourceCWD = candidate.root.CWD
		return &copied, nil
	}
	info, err := ReadSessionLite(candidate.path)
	if err != nil || info == nil {
		return info, err
	}
	info.SessionID = candidate.sessionID
	info.LastModified = time.Unix(0, candidate.mtimeNano)
	info.FileSize = candidate.size
	info.TranscriptDir = candidate.root.TranscriptDir
	info.TranscriptPath = candidate.path
	info.ReadOnly = candidate.legacy
	info.NeedsImport = candidate.legacy
	info.sourceCWD = candidate.root.CWD
	if info.CWD == "" {
		info.CWD = candidate.root.CWD
	}
	return info, nil
}

func sortSessionCandidates(candidates []sessionCandidate, order SortOrder) {
	sort.Slice(candidates, func(i, j int) bool {
		return sessionCandidateLess(candidates[i], candidates[j], order)
	})
}

func sessionCandidateLess(left, right sessionCandidate, order SortOrder) bool {
	switch order {
	case SortOldestFirst:
		if left.mtimeNano != right.mtimeNano {
			return left.mtimeNano < right.mtimeNano
		}
	case SortMostMessages:
		if left.size != right.size {
			return left.size > right.size
		}
	default:
		if left.mtimeNano != right.mtimeNano {
			return left.mtimeNano > right.mtimeNano
		}
	}
	if left.sessionID != right.sessionID {
		return left.sessionID > right.sessionID
	}
	return left.path < right.path
}

func candidateStart(candidates []sessionCandidate, cursor *sessionCursor, order SortOrder) int {
	if cursor == nil {
		return 0
	}
	for index := range candidates {
		candidate := candidates[index]
		if candidateSortValue(candidate, order) == cursor.SortValue &&
			candidate.sessionID == cursor.SessionID && candidate.path == cursor.Path {
			return index + 1
		}
	}
	// If the anchor disappeared, compare against its stable sort tuple. This
	// keeps moving/deleted files from resetting pagination to the first page.
	anchor := sessionCandidate{sessionID: cursor.SessionID, path: cursor.Path}
	if order == SortMostMessages {
		anchor.size = cursor.SortValue
	} else {
		anchor.mtimeNano = cursor.SortValue
	}
	for index := range candidates {
		if candidateComesAfter(candidates[index], anchor, order) {
			return index
		}
	}
	return len(candidates)
}

func candidateComesAfter(candidate, anchor sessionCandidate, order SortOrder) bool {
	return sessionCandidateLess(anchor, candidate, order)
}

func sessionQueryFingerprint(query SessionQuery) string {
	contextPath, _ := canonicalPath(query.CWD)
	transcriptDir, _ := canonicalPath(query.TranscriptDir)
	catalogPath := ""
	if strings.TrimSpace(query.CatalogPath) != "" {
		catalogPath, _ = canonicalPath(query.CatalogPath)
	}
	legacyCatalogPath := ""
	if strings.TrimSpace(query.LegacyCatalogPath) != "" {
		legacyCatalogPath, _ = canonicalPath(query.LegacyCatalogPath)
	}
	payload := struct {
		Scope           SessionScope `json:"scope"`
		Sort            SortOrder    `json:"sort"`
		Filter          ListFilter   `json:"filter"`
		Context         string       `json:"context"`
		TranscriptDir   string       `json:"transcript_dir"`
		Catalog         string       `json:"catalog"`
		LegacyCatalog   string       `json:"legacy_catalog"`
		BindGenerations bool         `json:"bind_generations"`
	}{
		query.Scope,
		query.Sort,
		query.Filter,
		contextPath,
		transcriptDir,
		catalogPath,
		legacyCatalogPath,
		query.BindCandidateGenerations,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func encodeSessionCursor(
	fingerprint string,
	durableGeneration string,
	activeGeneration string,
	candidate sessionCandidate,
	order SortOrder,
) (string, error) {
	cursor := sessionCursor{
		Version:           sessionCursorVersion,
		Fingerprint:       fingerprint,
		DurableGeneration: durableGeneration,
		ActiveGeneration:  activeGeneration,
		SortValue:         candidateSortValue(candidate, order),
		SessionID:         candidate.sessionID,
		Path:              candidate.path,
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeSessionCursor(
	value string,
	fingerprint string,
	bindGenerations bool,
	durableGeneration string,
	activeGeneration string,
) (*sessionCursor, error) {
	if value == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrSessionCursorInvalid, err)
	}
	var cursor sessionCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, fmt.Errorf(
			"%w: decode payload: %w",
			ErrSessionCursorInvalid,
			err,
		)
	}
	if cursor.Version != sessionCursorVersion || cursor.Fingerprint != fingerprint {
		return nil, fmt.Errorf(
			"%w: cursor does not match the current query",
			ErrSessionCursorInvalid,
		)
	}
	if bindGenerations &&
		(cursor.DurableGeneration != durableGeneration ||
			cursor.ActiveGeneration != activeGeneration) {
		return nil, fmt.Errorf(
			"%w: candidate generation changed",
			ErrSessionCursorInvalid,
		)
	}
	return &cursor, nil
}

func candidateSortValue(candidate sessionCandidate, order SortOrder) int64 {
	if order == SortMostMessages {
		return candidate.size
	}
	return candidate.mtimeNano
}
