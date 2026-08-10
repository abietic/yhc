package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abietic/yhc/internal/statemigration"
)

const (
	sessionImportJournalVersion = 1
	sessionImportMarkerVersion  = 1
	maxSessionImportJSONBytes   = 2 * 1024 * 1024
)

// ImportPhase is the durable state of one session bundle transaction.
type ImportPhase string

const (
	PhasePrepared         ImportPhase = "prepared"
	PhaseFilesPromoted    ImportPhase = "files_promoted"
	PhaseMarkerCommitted  ImportPhase = "marker_committed"
	PhaseCatalogCommitted ImportPhase = "catalog_committed"
)

// BundleFile is one canonical 0600 file committed by a session import.
type BundleFile struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Mode         uint32 `json:"mode"`
	Size         int64  `json:"size"`
}

// ImportJournal is the canonical user-root recovery record. The legacy paths
// are evidence only and are never mutated by recovery.
type ImportJournal struct {
	Version                int          `json:"version"`
	SessionID              string       `json:"session_id"`
	RepositoryKey          string       `json:"repository_key"`
	LegacyTranscript       string       `json:"legacy_transcript"`
	CanonicalProject       string       `json:"canonical_project"`
	CanonicalTranscriptDir string       `json:"canonical_transcript_dir"`
	CanonicalCatalog       string       `json:"canonical_catalog"`
	CatalogUpdatedAt       time.Time    `json:"catalog_updated_at"`
	StageRelative          string       `json:"stage_relative"`
	Files                  []BundleFile `json:"files"`
	Phase                  ImportPhase  `json:"phase"`
}

type sessionImportMarker struct {
	Version                int          `json:"version"`
	SessionID              string       `json:"session_id"`
	RepositoryKey          string       `json:"repository_key"`
	CanonicalTranscriptDir string       `json:"canonical_transcript_dir"`
	CanonicalCatalog       string       `json:"canonical_catalog"`
	Files                  []BundleFile `json:"files"`
}

func sessionImportJournalRelative(repositoryKey string) string {
	return filepath.ToSlash(filepath.Join("session-imports", "v1", repositoryKey))
}

func sessionImportProjectControlRelative() string {
	return filepath.ToSlash(filepath.Join("transcripts", ".imports", "v1"))
}

func sessionImportStageRelative(token string) string {
	return filepath.ToSlash(filepath.Join(
		sessionImportProjectControlRelative(), "staging", token,
	))
}

func sessionImportJournalName(sessionID string) string { return sessionID + ".json" }
func sessionImportMarkerName(sessionID string) string  { return sessionID + ".json" }
func sessionImportLockName(sessionID string) string    { return sessionID + ".lock" }

func newSessionImportToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func markerFromJournal(journal ImportJournal) sessionImportMarker {
	return sessionImportMarker{
		Version:                sessionImportMarkerVersion,
		SessionID:              journal.SessionID,
		RepositoryKey:          journal.RepositoryKey,
		CanonicalTranscriptDir: journal.CanonicalTranscriptDir,
		CanonicalCatalog:       journal.CanonicalCatalog,
		Files:                  append([]BundleFile(nil), journal.Files...),
	}
}

func validateImportJournal(journal ImportJournal) error {
	if journal.Version != sessionImportJournalVersion ||
		!isValidSessionFileID(journal.SessionID) ||
		len(journal.RepositoryKey) != 64 ||
		!validLowerHex(journal.RepositoryKey) ||
		!filepath.IsAbs(journal.LegacyTranscript) ||
		!filepath.IsAbs(journal.CanonicalProject) ||
		!filepath.IsAbs(journal.CanonicalTranscriptDir) ||
		!filepath.IsAbs(journal.CanonicalCatalog) ||
		filepath.Clean(journal.LegacyTranscript) != journal.LegacyTranscript ||
		filepath.Clean(journal.CanonicalProject) != journal.CanonicalProject ||
		filepath.Clean(journal.CanonicalTranscriptDir) != journal.CanonicalTranscriptDir ||
		filepath.Clean(journal.CanonicalCatalog) != journal.CanonicalCatalog ||
		journal.CatalogUpdatedAt.IsZero() || journal.CatalogUpdatedAt.Location() != time.UTC ||
		strings.TrimSpace(journal.StageRelative) == "" {
		return errors.New("session import journal identity is invalid")
	}
	switch journal.Phase {
	case PhasePrepared, PhaseFilesPromoted, PhaseMarkerCommitted, PhaseCatalogCommitted:
	default:
		return errors.New("session import journal phase is invalid")
	}
	if len(journal.Files) < 1 || len(journal.Files) > 4 {
		return errors.New("session import journal file count is invalid")
	}
	stagePrefix := sessionImportProjectControlRelative() + "/staging/"
	stageToken := strings.TrimPrefix(journal.StageRelative, stagePrefix)
	if filepath.ToSlash(filepath.Clean(filepath.FromSlash(journal.StageRelative))) != journal.StageRelative ||
		!strings.HasPrefix(journal.StageRelative, stagePrefix) ||
		len(stageToken) != 32 || strings.Contains(stageToken, "/") || !validLowerHex(stageToken) {
		return errors.New("session import stage path is invalid")
	}
	allowedNames := make(map[string]struct{}, 4)
	for _, name := range sessionBundleNames(journal.SessionID) {
		allowedNames[name] = struct{}{}
	}
	transcriptName := journal.SessionID + ".jsonl"
	foundTranscript := false
	seen := make(map[string]struct{}, len(journal.Files))
	for _, file := range journal.Files {
		if filepath.Base(file.RelativePath) != file.RelativePath ||
			file.RelativePath == "." || file.RelativePath == ".." ||
			strings.ContainsAny(file.RelativePath, "/\\\x00") ||
			len(file.SHA256) != 64 || !validLowerHex(file.SHA256) ||
			file.Mode != 0o600 || file.Size < 0 {
			return errors.New("session import journal file is invalid")
		}
		if _, allowed := allowedNames[file.RelativePath]; !allowed {
			return errors.New("session import journal file is outside the bundle allowlist")
		}
		foundTranscript = foundTranscript || file.RelativePath == transcriptName
		if _, duplicate := seen[file.RelativePath]; duplicate {
			return errors.New("session import journal file is duplicated")
		}
		seen[file.RelativePath] = struct{}{}
	}
	if !foundTranscript {
		return errors.New("session import journal has no transcript")
	}
	sorted := append([]BundleFile(nil), journal.Files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelativePath < sorted[j].RelativePath })
	for index := range sorted {
		if sorted[index] != journal.Files[index] {
			return errors.New("session import journal files are not sorted")
		}
	}
	return nil
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return value != ""
}

func readImportJournal(
	store *statemigration.CanonicalStore,
	sessionID string,
) (ImportJournal, bool, error) {
	var journal ImportJournal
	exists, err := readCanonicalJSON(store, sessionImportJournalName(sessionID), &journal)
	if err != nil || !exists {
		return ImportJournal{}, exists, err
	}
	if err := validateImportJournal(journal); err != nil {
		return ImportJournal{}, true, err
	}
	return journal, true, nil
}

func writeImportJournal(
	store *statemigration.CanonicalStore,
	journal ImportJournal,
) error {
	if err := validateImportJournal(journal); err != nil {
		return err
	}
	return writeCanonicalJSON(store, sessionImportJournalName(journal.SessionID), journal, true)
}

func readSessionImportMarker(
	store *statemigration.CanonicalStore,
	sessionID string,
) (sessionImportMarker, bool, error) {
	var marker sessionImportMarker
	exists, err := readCanonicalJSON(store, sessionImportMarkerName(sessionID), &marker)
	if err != nil || !exists {
		return sessionImportMarker{}, exists, err
	}
	if validateSessionImportMarker(marker) != nil {
		return sessionImportMarker{}, true, errors.New("session import marker is invalid")
	}
	return marker, true, nil
}

func writeSessionImportMarker(
	store *statemigration.CanonicalStore,
	marker sessionImportMarker,
) error {
	if validateSessionImportMarker(marker) != nil {
		return errors.New("session import marker is invalid")
	}
	return writeCanonicalJSON(store, sessionImportMarkerName(marker.SessionID), marker, false)
}

func validateSessionImportMarker(marker sessionImportMarker) error {
	if marker.Version != sessionImportMarkerVersion ||
		!isValidSessionFileID(marker.SessionID) || len(marker.RepositoryKey) != 64 ||
		!validLowerHex(marker.RepositoryKey) || !filepath.IsAbs(marker.CanonicalTranscriptDir) ||
		!filepath.IsAbs(marker.CanonicalCatalog) ||
		filepath.Clean(marker.CanonicalTranscriptDir) != marker.CanonicalTranscriptDir ||
		filepath.Clean(marker.CanonicalCatalog) != marker.CanonicalCatalog ||
		len(marker.Files) < 1 || len(marker.Files) > 4 {
		return errors.New("session import marker identity is invalid")
	}
	seen := make(map[string]struct{}, len(marker.Files))
	allowedNames := make(map[string]struct{}, 4)
	for _, name := range sessionBundleNames(marker.SessionID) {
		allowedNames[name] = struct{}{}
	}
	foundTranscript := false
	for _, file := range marker.Files {
		if filepath.Base(file.RelativePath) != file.RelativePath ||
			strings.ContainsAny(file.RelativePath, "/\\\x00") || len(file.SHA256) != 64 ||
			!validLowerHex(file.SHA256) || file.Mode != 0o600 || file.Size < 0 {
			return errors.New("session import marker file is invalid")
		}
		if _, allowed := allowedNames[file.RelativePath]; !allowed {
			return errors.New("session import marker file is outside the bundle allowlist")
		}
		foundTranscript = foundTranscript || file.RelativePath == marker.SessionID+".jsonl"
		if _, duplicate := seen[file.RelativePath]; duplicate {
			return errors.New("session import marker file is duplicated")
		}
		seen[file.RelativePath] = struct{}{}
	}
	if !foundTranscript {
		return errors.New("session import marker has no transcript")
	}
	return nil
}

func readCanonicalJSON(store *statemigration.CanonicalStore, name string, target any) (bool, error) {
	file, info, exists, err := store.OpenRegular(name, os.O_RDONLY, false)
	if err != nil || !exists {
		return exists, err
	}
	defer file.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(file, maxSessionImportJSONBytes+1))
	if err != nil || len(data) > maxSessionImportJSONBytes {
		return true, errors.New("session import metadata exceeds its read budget")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return true, fmt.Errorf("decode session import metadata: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return true, errors.New("session import metadata has trailing data")
	}
	if err := store.ValidateRegular(name, info); err != nil {
		return true, errors.New("session import metadata changed while reading")
	}
	return true, nil
}

func writeCanonicalJSON(
	store *statemigration.CanonicalStore,
	name string,
	value any,
	replace bool,
) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxSessionImportJSONBytes {
		return errors.New("session import metadata exceeds its write budget")
	}
	token, err := newSessionImportToken()
	if err != nil {
		return err
	}
	tempName := "." + name + ".tmp-" + token
	file, info, exists, err := store.OpenRegular(tempName, os.O_WRONLY, true)
	if err != nil || !exists {
		return errors.New("create session import metadata temp file")
	}
	cleanup := true
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if cleanup {
			store.RemoveRegularIfSame(tempName, info)
			_ = store.Sync()
		}
	}()
	if err := writeAllSessionImport(file, data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		file = nil
		return err
	}
	file = nil
	if replace {
		if err := store.PromoteRegular(tempName, name, info); err != nil {
			return err
		}
	} else {
		collision, err := store.PromoteRegularFromNoReplace(store, tempName, name, info)
		if err != nil || collision {
			return errors.New("session import metadata collision")
		}
	}
	cleanup = false
	var reread json.RawMessage
	exists, err = readCanonicalJSON(store, name, &reread)
	if err != nil || !exists || !bytes.Equal(bytes.TrimSpace(reread), bytes.TrimSpace(data)) {
		return errors.New("session import metadata reread mismatch")
	}
	return nil
}

func writeAllSessionImport(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
