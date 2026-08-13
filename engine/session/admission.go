package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

// LegacySessionImportRequiredError carries only the stable session identity in
// its public projection. In-process interactive owners may recover the
// value-only import target through LegacySessionImportTarget.
type LegacySessionImportRequiredError struct {
	SessionID string
	target    LegacySessionTarget
}

func (err *LegacySessionImportRequiredError) Error() string {
	if err == nil {
		return ErrLegacySessionImportRequired.Error()
	}
	return fmt.Sprintf("legacy_session_import_required: session %s", err.SessionID)
}

func (err *LegacySessionImportRequiredError) Unwrap() error {
	return ErrLegacySessionImportRequired
}

// ResumeAdmissionRequest identifies one exact read-only resolution boundary.
// It never registers or refreshes a catalog root.
type ResumeAdmissionRequest struct {
	SessionID         string
	CWD               string
	CatalogPath       string
	LegacyCatalogPath string
	UserRoots         statepath.Roots
}

// ResolvedResumeAdmissionRequest validates one exact row returned by session
// discovery without resolving its Session ID again.
type ResolvedResumeAdmissionRequest struct {
	Info        SessionInfo
	CatalogPath string
	UserRoots   statepath.Roots
}

// ImportDiscoveredLegacySessionRequest carries one previously discovered,
// read-only legacy row through the explicit stopped-producer import boundary.
// It is deliberately session-only: it neither constructs a runtime nor
// acquires a live-session lease.
type ImportDiscoveredLegacySessionRequest struct {
	Info                 SessionInfo
	CatalogPath          string
	LegacyCatalogPath    string
	UserRoots            statepath.Roots
	ConfirmLegacyStopped bool
	Now                  time.Time
}

// ImportDiscoveredLegacySession imports one provenance-bearing legacy
// discovery row into the canonical default store and returns a freshly
// admitted canonical row. Explicit catalog overrides are intentionally not
// eligible: a legacy import always binds the default YHC/legacy catalog pair.
func ImportDiscoveredLegacySession(
	ctx context.Context,
	request ImportDiscoveredLegacySessionRequest,
) (SessionInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SessionInfo{}, fmt.Errorf("import discovered legacy session: %w", err)
	}
	if !request.Info.HasResolvedSource() {
		return SessionInfo{}, importUnsafe(errors.New("legacy session source provenance is unavailable"))
	}
	if err := requireDefaultSessionImportPair(
		request.CatalogPath,
		request.LegacyCatalogPath,
		request.UserRoots,
	); err != nil {
		return SessionInfo{}, importUnsafe(err)
	}
	if err := RequireCanonicalSession(request.Info); err == nil {
		return SessionInfo{}, importUnsafe(errors.New("session is not a legacy import candidate"))
	} else if !errors.Is(err, ErrLegacySessionImportRequired) {
		return SessionInfo{}, err
	}

	physicalCWD, err := canonicalPath(request.Info.sourceCWD)
	if err != nil {
		return SessionInfo{}, importUnsafe(errors.New("legacy session source provenance is unavailable"))
	}
	legacyOwner, err := ResolveSession(SessionQuery{
		Scope:         SessionScopeCWD,
		CWD:           physicalCWD,
		TranscriptDir: request.Info.TranscriptDir,
	}, request.Info.SessionID)
	if err != nil {
		return SessionInfo{}, importUnsafe(fmt.Errorf(
			"refresh legacy session owner %s: %w",
			request.Info.SessionID,
			err,
		))
	}
	if err := requireSameLegacyImportOwner(request.Info, legacyOwner); err != nil {
		return SessionInfo{}, importUnsafe(err)
	}
	fresh, err := ResolveSession(SessionQuery{
		Scope:             SessionScopeCWD,
		CWD:               physicalCWD,
		CatalogPath:       request.CatalogPath,
		LegacyCatalogPath: request.LegacyCatalogPath,
	}, request.Info.SessionID)
	if err != nil {
		return SessionInfo{}, importUnsafe(fmt.Errorf(
			"refresh legacy session %s: %w",
			request.Info.SessionID,
			err,
		))
	}
	if fresh.ReadOnly || fresh.NeedsImport {
		if err := requireSameLegacyImportOwner(request.Info, fresh); err != nil {
			return SessionInfo{}, importUnsafe(err)
		}
	} else if fresh.SessionID != request.Info.SessionID || !fresh.HasResolvedSource() ||
		!samePath(fresh.sourceCWD, physicalCWD) {
		return SessionInfo{}, importUnsafe(errors.New("canonical session discovery changed before import"))
	}

	target := LegacySessionTarget{
		SessionID:     legacyOwner.SessionID,
		CWD:           legacyOwner.sourceCWD,
		TranscriptDir: legacyOwner.TranscriptDir,
		ReadOnly:      true,
		NeedsImport:   true,
	}
	_, err = ImportSessionForResume(ctx, ImportRequest{
		Target:               target,
		UserRoots:            request.UserRoots,
		ConfirmLegacyStopped: request.ConfirmLegacyStopped,
		Now:                  request.Now,
	})
	if err != nil && !errors.Is(err, ErrSessionImportAlreadyCommitted) {
		return SessionInfo{}, err
	}
	return AdmitSessionResume(ctx, ResumeAdmissionRequest{
		SessionID:         legacyOwner.SessionID,
		CWD:               legacyOwner.sourceCWD,
		CatalogPath:       request.CatalogPath,
		LegacyCatalogPath: request.LegacyCatalogPath,
		UserRoots:         request.UserRoots,
	})
}

func requireDefaultSessionImportPair(
	catalogPath string,
	legacyCatalogPath string,
	userRoots statepath.Roots,
) error {
	defaultCatalogPath, defaultLegacyCatalogPath := DefaultCatalogPaths()
	if defaultCatalogPath == "" || defaultLegacyCatalogPath == "" ||
		!samePath(catalogPath, defaultCatalogPath) ||
		!samePath(legacyCatalogPath, defaultLegacyCatalogPath) {
		return errors.New("legacy session import requires the default catalog pair")
	}
	defaultUserRoots, err := DefaultSessionImportUserRoots()
	if err != nil || !samePath(userRoots.Canonical, defaultUserRoots.Canonical) ||
		!samePath(userRoots.Legacy, defaultUserRoots.Legacy) {
		return errors.New("legacy session import requires the default user roots")
	}
	return nil
}

func requireSameLegacyImportOwner(initial, fresh SessionInfo) error {
	if fresh.SessionID != initial.SessionID || !fresh.HasResolvedSource() ||
		!samePath(fresh.sourceCWD, initial.sourceCWD) ||
		!samePath(fresh.TranscriptDir, initial.TranscriptDir) {
		return errors.New("legacy session discovery changed before import")
	}
	return nil
}

// AdmitSessionResume resolves one exact source and returns it only when the
// source is canonical and writable. Legacy discovery returns a typed import
// requirement before QueryEngine construction.
func AdmitSessionResume(
	ctx context.Context,
	request ResumeAdmissionRequest,
) (SessionInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SessionInfo{}, fmt.Errorf("admit session resume: %w", err)
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	if request.SessionID == "" || !isValidSessionFileID(request.SessionID) {
		return SessionInfo{}, errors.New("session ID is invalid")
	}
	if strings.TrimSpace(request.CWD) == "" || strings.ContainsRune(request.CWD, '\x00') {
		return SessionInfo{}, errors.New("session resume CWD is invalid")
	}
	projectRoots, err := statepath.ProjectRoots(request.CWD)
	if err != nil {
		return SessionInfo{}, errors.New("session resume roots are invalid")
	}
	canonicalCWD, err := canonicalPath(request.CWD)
	if err != nil {
		return SessionInfo{}, errors.New("session resume CWD is invalid")
	}
	canonicalTranscriptDir := filepath.Join(projectRoots.Canonical, "transcripts")
	info, err := ResolveSession(SessionQuery{
		Scope:             SessionScopeCWD,
		CWD:               canonicalCWD,
		TranscriptDir:     canonicalTranscriptDir,
		CatalogPath:       request.CatalogPath,
		LegacyCatalogPath: request.LegacyCatalogPath,
	}, request.SessionID)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("resolve session %s: %w", request.SessionID, err)
	}
	if err := AdmitResolvedSessionResume(ctx, ResolvedResumeAdmissionRequest{
		Info:        info,
		CatalogPath: request.CatalogPath,
		UserRoots:   request.UserRoots,
	}); err != nil {
		return SessionInfo{}, err
	}
	physicalCWD, err := canonicalPath(info.sourceCWD)
	if err != nil || !samePath(physicalCWD, canonicalCWD) ||
		!samePath(info.TranscriptDir, canonicalTranscriptDir) {
		return SessionInfo{}, importUnsafe(errors.New("session resume source is not canonical"))
	}
	if err := ctx.Err(); err != nil {
		return SessionInfo{}, fmt.Errorf("admit session resume: %w", err)
	}
	return info, nil
}

// AdmitResolvedSessionResume validates the physical canonical owner and any
// import commit evidence attached to one discovery row. Values constructed by
// callers do not carry source provenance and are rejected by this boundary.
func AdmitResolvedSessionResume(
	ctx context.Context,
	request ResolvedResumeAdmissionRequest,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("admit resolved session resume: %w", err)
	}
	if err := RequireCanonicalSession(request.Info); err != nil {
		return err
	}
	physicalCWD, err := canonicalPath(request.Info.sourceCWD)
	if err != nil {
		return importUnsafe(errors.New("session resume source provenance is unavailable"))
	}
	projectRoots, err := statepath.ProjectRoots(physicalCWD)
	if err != nil {
		return importUnsafe(errors.New("session resume source roots are invalid"))
	}
	canonicalTranscriptDir := filepath.Join(projectRoots.Canonical, "transcripts")
	legacyTranscriptDir := filepath.Join(projectRoots.Legacy, "transcripts")
	if samePath(request.Info.TranscriptDir, legacyTranscriptDir) {
		legacy := request.Info
		legacy.ReadOnly = true
		legacy.NeedsImport = true
		return RequireCanonicalSession(legacy)
	}
	if !samePath(request.Info.TranscriptDir, canonicalTranscriptDir) {
		// Existing embedded engines may own an explicit non-default transcript
		// store. Session import never targets those stores, so there is no bundle
		// journal to validate here. Entrypoint admission separately requires the
		// canonical default and cannot take this compatibility branch.
		return nil
	}
	userRoots, rootsKnown, err := resumeAdmissionUserRoots(
		request.CatalogPath,
		request.UserRoots,
	)
	if err != nil {
		return importUnsafe(err)
	}
	return verifyCanonicalSessionImportAdmission(
		ctx,
		request.Info,
		physicalCWD,
		projectRoots,
		userRoots,
		rootsKnown,
	)
}

// AdmitDefaultSessionResume selects the canonical project transcript root and
// the effective default catalog pair without creating either one.
func AdmitDefaultSessionResume(
	ctx context.Context,
	cwd string,
	sessionID string,
) (SessionInfo, error) {
	if _, err := statepath.ProjectRoots(cwd); err != nil {
		return SessionInfo{}, errors.New("session resume roots are invalid")
	}
	catalogPath, legacyCatalogPath := DefaultCatalogPaths()
	userRoots, _ := DefaultSessionImportUserRoots()
	return AdmitSessionResume(ctx, ResumeAdmissionRequest{
		SessionID:         sessionID,
		CWD:               cwd,
		CatalogPath:       catalogPath,
		LegacyCatalogPath: legacyCatalogPath,
		UserRoots:         userRoots,
	})
}

func resumeAdmissionUserRoots(
	catalogPath string,
	requestRoots statepath.Roots,
) (statepath.Roots, bool, error) {
	if requestRoots.Canonical != "" || requestRoots.Legacy != "" {
		if err := validateResumeAdmissionUserRoots(requestRoots); err != nil {
			return statepath.Roots{}, false, err
		}
		return requestRoots, true, nil
	}
	if catalogPath == "" {
		return statepath.Roots{}, false, nil
	}
	home := filepath.Dir(filepath.Dir(catalogPath))
	roots, err := statepath.UserRoots(home)
	if err != nil || !samePath(
		catalogPath,
		filepath.Join(roots.Canonical, "session-roots.json"),
	) {
		return statepath.Roots{}, false, nil
	}
	return roots, true, nil
}

func validateResumeAdmissionUserRoots(roots statepath.Roots) error {
	if !filepath.IsAbs(roots.Canonical) || !filepath.IsAbs(roots.Legacy) ||
		filepath.Clean(roots.Canonical) != roots.Canonical ||
		filepath.Clean(roots.Legacy) != roots.Legacy {
		return errors.New("session resume user roots are invalid")
	}
	expected, err := statepath.UserRoots(filepath.Dir(roots.Canonical))
	if err != nil || !samePath(expected.Canonical, roots.Canonical) ||
		!samePath(expected.Legacy, roots.Legacy) {
		return errors.New("session resume user roots are not the default pair")
	}
	return nil
}

func verifyCanonicalSessionImportAdmission(
	ctx context.Context,
	info SessionInfo,
	physicalCWD string,
	projectRoots statepath.Roots,
	userRoots statepath.Roots,
	userRootsKnown bool,
) error {
	projectControl, projectControlExists, err := statemigration.OpenCanonicalStore(
		projectRoots.Canonical,
		sessionImportProjectControlRelative(),
		false,
	)
	if err != nil {
		return importUnsafe(err)
	}
	if projectControlExists {
		defer projectControl.Close() //nolint:errcheck
	}

	var marker sessionImportMarker
	markerExists := false
	if projectControlExists {
		marker, markerExists, err = readSessionImportMarker(projectControl, info.SessionID)
		if err != nil {
			return importUnsafe(err)
		}
	}
	if !userRootsKnown {
		if projectControlExists {
			return importUnsafe(errors.New("session import user roots are unavailable"))
		}
		return nil
	}

	journal, journalExists, err := findResumeAdmissionJournal(userRoots, info, physicalCWD)
	if err != nil {
		return importUnsafe(err)
	}
	if !markerExists && !journalExists {
		return nil
	}
	if !markerExists || !journalExists || !projectControlExists {
		return importUnsafe(errors.New("session import commit evidence is incomplete"))
	}
	normalized, err := normalizedImportFromJournal(journal, userRoots)
	if err != nil ||
		!samePath(normalized.root.CWD, physicalCWD) ||
		!samePath(normalized.root.TranscriptDir, info.TranscriptDir) {
		return importUnsafe(errors.New("session import journal does not match resume source"))
	}
	targetStore, targetStoreExists, err := statemigration.OpenCanonicalStore(
		projectRoots.Canonical,
		"transcripts",
		false,
	)
	if err != nil || !targetStoreExists {
		return importUnsafe(errors.New("session import target store is unavailable"))
	}
	defer targetStore.Close() //nolint:errcheck
	catalogStore, catalogStoreExists, err := statemigration.OpenCanonicalStore(
		userRoots.Canonical,
		".",
		false,
	)
	if err != nil {
		return importUnsafe(err)
	}
	if !catalogStoreExists {
		return importUnsafe(errors.New("session import catalog store is unavailable"))
	}
	defer catalogStore.Close() //nolint:errcheck
	fileName := filepath.Base(journal.CanonicalCatalog)
	roots, err := loadSessionRootsFromStore(catalogStore, fileName)
	if err != nil {
		return importUnsafe(err)
	}
	catalog := &sessionCatalogTransaction{
		path:     journal.CanonicalCatalog,
		fileName: fileName,
		store:    catalogStore,
		roots:    roots,
	}
	if err := verifyCommittedSessionImportAdmission(
		ctx,
		journal,
		projectControl,
		targetStore,
		catalog,
		normalized.root,
	); err != nil {
		return err
	}
	if !sameSessionImportMarker(marker, markerFromJournal(journal)) {
		return importUnsafe(errors.New("session import marker does not match journal"))
	}
	return nil
}

func findResumeAdmissionJournal(
	userRoots statepath.Roots,
	info SessionInfo,
	physicalCWD string,
) (ImportJournal, bool, error) {
	base, exists, err := statemigration.OpenCanonicalStore(
		userRoots.Canonical,
		filepath.ToSlash(filepath.Join("session-imports", "v1")),
		false,
	)
	if err != nil || !exists {
		return ImportJournal{}, false, err
	}
	defer base.Close() //nolint:errcheck
	entries, err := fs.ReadDir(base.Root().FS(), ".")
	if err != nil {
		return ImportJournal{}, false, err
	}
	var selected ImportJournal
	found := false
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 64 || !validLowerHex(entry.Name()) {
			continue
		}
		repositoryStore, repositoryExists, err := statemigration.OpenCanonicalStore(
			userRoots.Canonical,
			sessionImportJournalRelative(entry.Name()),
			false,
		)
		if err != nil {
			return ImportJournal{}, false, err
		}
		if !repositoryExists {
			return ImportJournal{}, false, errors.New("session import journal store disappeared")
		}
		journal, journalExists, readErr := readImportJournal(repositoryStore, info.SessionID)
		closeErr := repositoryStore.Close()
		if readErr != nil {
			return ImportJournal{}, false, readErr
		}
		if closeErr != nil {
			return ImportJournal{}, false, closeErr
		}
		if !journalExists ||
			!samePath(journal.CanonicalProject, physicalCWD) ||
			!samePath(journal.CanonicalTranscriptDir, info.TranscriptDir) {
			continue
		}
		if journal.RepositoryKey != entry.Name() || found {
			return ImportJournal{}, false, errors.New("session import journal is ambiguous")
		}
		selected = journal
		found = true
	}
	return selected, found, nil
}

// RequireCanonicalSession rejects every value marked as a read-only legacy
// discovery row while preserving an in-process value-only import target.
func RequireCanonicalSession(info SessionInfo) error {
	if strings.TrimSpace(info.SessionID) == "" {
		return errors.New("session ID is required")
	}
	if !info.ReadOnly && !info.NeedsImport {
		return nil
	}
	transcriptDir := info.TranscriptDir
	if transcriptDir == "" && info.TranscriptPath != "" {
		transcriptDir = filepath.Dir(info.TranscriptPath)
	}
	return &LegacySessionImportRequiredError{
		SessionID: info.SessionID,
		target: LegacySessionTarget{
			SessionID:     info.SessionID,
			CWD:           info.CWD,
			TranscriptDir: transcriptDir,
			ReadOnly:      true,
			NeedsImport:   true,
		},
	}
}

// LegacySessionImportTarget extracts the value-only target from a typed import
// requirement. Wire projections must use only the public SessionID.
func LegacySessionImportTarget(err error) (LegacySessionTarget, bool) {
	var required *LegacySessionImportRequiredError
	if !errors.As(err, &required) || required == nil {
		return LegacySessionTarget{}, false
	}
	return required.target, true
}

// DefaultSessionImportUserRoots returns the default canonical/legacy user pair
// accepted by the recoverable session-bundle transaction.
func DefaultSessionImportUserRoots() (statepath.Roots, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return statepath.Roots{}, errors.New("session import user root is unavailable")
	}
	return statepath.UserRoots(home)
}
