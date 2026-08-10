package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

const sessionImportTranscriptMaxBytes int64 = 512 * 1024 * 1024

var (
	ErrSessionImportAttestationRequired = errors.New("session_import_attestation_required")
	ErrSessionImportUnsafe              = errors.New("session_import_unsafe")
	ErrSessionImportCollision           = errors.New("session_import_collision")
	ErrSessionImportAlreadyCommitted    = errors.New("session_import_already_committed")
)

// LegacySessionTarget is the read-only session identity eligible for explicit
// bundle import. It is intentionally value-only and carries no writable owner.
type LegacySessionTarget struct {
	SessionID     string
	CWD           string
	TranscriptDir string
	ReadOnly      bool
	NeedsImport   bool
}

// ImportRequest authorizes one explicit stopped-producer migration. UserRoots
// must be the canonical/default YHC and legacy roots, not an exact override.
type ImportRequest struct {
	Target               LegacySessionTarget
	UserRoots            statepath.Roots
	ConfirmLegacyStopped bool
	Now                  time.Time
}

type sessionImportFailureStage string

const (
	failureAfterFirstSourceSnapshot sessionImportFailureStage = "after-first-source-snapshot"
	failureAfterJournalWrite        sessionImportFailureStage = "after-journal-write"
	failureAfterStageFileWrite      sessionImportFailureStage = "after-stage-file-write"
	failureAfterStageFileSync       sessionImportFailureStage = "after-stage-file-sync"
	failureAfterStageParentSync     sessionImportFailureStage = "after-stage-parent-sync"
	failureAfterFileRename          sessionImportFailureStage = "after-file-rename"
	failureAfterFileSync            sessionImportFailureStage = "after-file-sync"
	failureAfterMarkerWrite         sessionImportFailureStage = "after-marker-write"
	failureAfterMarkerSync          sessionImportFailureStage = "after-marker-sync"
	failureAfterCatalogReplace      sessionImportFailureStage = "after-catalog-replace"
	failureAfterCatalogSync         sessionImportFailureStage = "after-catalog-sync"
)

type (
	sessionImportFailureHook    func(sessionImportFailureStage, string) error
	sessionImportFailureHookKey struct{}
)

func withSessionImportFailureHook(
	ctx context.Context,
	hook sessionImportFailureHook,
) context.Context {
	return context.WithValue(ctx, sessionImportFailureHookKey{}, hook)
}

func fireSessionImportFailureHook(
	ctx context.Context,
	stage sessionImportFailureStage,
	detail string,
) error {
	if ctx == nil {
		return nil
	}
	hook, _ := ctx.Value(sessionImportFailureHookKey{}).(sessionImportFailureHook)
	if hook == nil {
		return nil
	}
	if err := hook(stage, detail); err != nil {
		return fmt.Errorf("%w: injected %s failure", ErrSessionImportUnsafe, stage)
	}
	return nil
}

type normalizedSessionImport struct {
	target              LegacySessionTarget
	userRoots           statepath.Roots
	project             string
	projectStateRoot    string
	legacyTranscript    string
	canonicalTranscript string
	canonicalCatalog    string
	repositoryKey       string
	root                SessionRoot
}

// ImportSessionForResume completes one recoverable bundle transaction. The
// attestation is checked before any canonical root or lock is created.
func ImportSessionForResume(ctx context.Context, request ImportRequest) (SessionRoot, error) {
	if !request.ConfirmLegacyStopped {
		return SessionRoot{}, ErrSessionImportAttestationRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeSessionImportRequest(request)
	if err != nil {
		return SessionRoot{}, importUnsafe(err)
	}
	return runLockedSessionImport(ctx, normalized, true)
}

// InspectLegacySessions lists only the exact transcript roots registered by a
// legacy catalog. It never creates, repairs, or refreshes that catalog.
func InspectLegacySessions(
	ctx context.Context,
	legacyCatalogPath string,
) ([]LegacySessionTarget, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	roots, err := LoadSessionRoots(legacyCatalogPath)
	if err != nil {
		return nil, err
	}
	targets := make([]LegacySessionTarget, 0)
	seen := make(map[string]struct{})
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sessions, err := ListSessions(ListOptions{TranscriptDir: root.TranscriptDir})
		if err != nil {
			return nil, err
		}
		for _, info := range sessions {
			key := root.CWD + "\x00" + root.TranscriptDir + "\x00" + info.SessionID
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, LegacySessionTarget{
				SessionID:     info.SessionID,
				CWD:           root.CWD,
				TranscriptDir: root.TranscriptDir,
				ReadOnly:      true,
				NeedsImport:   true,
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].CWD != targets[j].CWD {
			return targets[i].CWD < targets[j].CWD
		}
		if targets[i].TranscriptDir != targets[j].TranscriptDir {
			return targets[i].TranscriptDir < targets[j].TranscriptDir
		}
		return targets[i].SessionID < targets[j].SessionID
	})
	return targets, nil
}

// RecoverSessionImports completes only transactions whose durable stage or
// promoted files already match the journal. It never recaptures legacy state;
// an incomplete prepared stage still requires a fresh explicit attestation.
func RecoverSessionImports(ctx context.Context, roots statepath.Roots) error {
	if ctx == nil {
		ctx = context.Background()
	}
	base, exists, err := statemigration.OpenCanonicalStore(
		roots.Canonical,
		filepath.ToSlash(filepath.Join("session-imports", "v1")),
		false,
	)
	if err != nil || !exists {
		return err
	}
	entries, err := fs.ReadDir(base.Root().FS(), ".")
	if closeErr := base.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return importUnsafe(err)
	}
	var recoveryErr error
	for _, entry := range entries {
		if ctx.Err() != nil {
			return errors.Join(recoveryErr, ctx.Err())
		}
		if !entry.IsDir() || len(entry.Name()) != 64 || !validLowerHex(entry.Name()) {
			recoveryErr = errors.Join(recoveryErr, ErrSessionImportUnsafe)
			continue
		}
		repositoryStore, repositoryExists, openErr := statemigration.OpenCanonicalStore(
			roots.Canonical,
			sessionImportJournalRelative(entry.Name()),
			false,
		)
		if openErr != nil || !repositoryExists {
			recoveryErr = errors.Join(recoveryErr, importUnsafe(openErr))
			continue
		}
		journals, readErr := fs.ReadDir(repositoryStore.Root().FS(), ".")
		if readErr != nil {
			_ = repositoryStore.Close()
			recoveryErr = errors.Join(recoveryErr, importUnsafe(readErr))
			continue
		}
		for _, candidate := range journals {
			if candidate.IsDir() || !strings.HasSuffix(candidate.Name(), ".json") {
				continue
			}
			sessionID := strings.TrimSuffix(candidate.Name(), ".json")
			if !isValidSessionFileID(sessionID) {
				recoveryErr = errors.Join(recoveryErr, ErrSessionImportUnsafe)
				continue
			}
			journal, journalExists, journalErr := readImportJournal(repositoryStore, sessionID)
			if journalErr != nil || !journalExists || journal.RepositoryKey != entry.Name() {
				recoveryErr = errors.Join(recoveryErr, importUnsafe(journalErr))
				continue
			}
			normalized, normalizeErr := normalizedImportFromJournal(journal, roots)
			if normalizeErr != nil {
				recoveryErr = errors.Join(recoveryErr, importUnsafe(normalizeErr))
				continue
			}
			_ = repositoryStore.Close()
			_, recoverErr := runLockedSessionImport(ctx, normalized, false)
			if recoverErr != nil && !errors.Is(recoverErr, ErrSessionImportAlreadyCommitted) {
				recoveryErr = errors.Join(recoveryErr, recoverErr)
			}
			repositoryStore, repositoryExists, openErr = statemigration.OpenCanonicalStore(
				roots.Canonical,
				sessionImportJournalRelative(entry.Name()),
				false,
			)
			if openErr != nil || !repositoryExists {
				recoveryErr = errors.Join(recoveryErr, importUnsafe(openErr))
				break
			}
		}
		_ = repositoryStore.Close()
	}
	return recoveryErr
}

func normalizedImportFromJournal(
	journal ImportJournal,
	roots statepath.Roots,
) (normalizedSessionImport, error) {
	normalized, err := normalizeSessionImportRequest(ImportRequest{
		Target: LegacySessionTarget{
			SessionID:     journal.SessionID,
			CWD:           journal.CanonicalProject,
			TranscriptDir: filepath.Dir(journal.LegacyTranscript),
			ReadOnly:      true,
			NeedsImport:   true,
		},
		UserRoots:            roots,
		ConfirmLegacyStopped: true,
		Now:                  journal.CatalogUpdatedAt,
	})
	if err != nil {
		return normalizedSessionImport{}, err
	}
	if err := journalMatchesImport(journal, normalized); err != nil {
		return normalizedSessionImport{}, err
	}
	return normalized, nil
}

func normalizeSessionImportRequest(request ImportRequest) (normalizedSessionImport, error) {
	target := request.Target
	if !target.ReadOnly || !target.NeedsImport || !isValidSessionFileID(target.SessionID) ||
		strings.ContainsRune(target.CWD, '\x00') || strings.ContainsRune(target.TranscriptDir, '\x00') {
		return normalizedSessionImport{}, errors.New("legacy session target is invalid")
	}
	project, err := canonicalPath(target.CWD)
	if err != nil {
		return normalizedSessionImport{}, err
	}
	projectInfo, err := os.Lstat(project)
	if err != nil || projectInfo.Mode()&os.ModeSymlink != 0 || !projectInfo.IsDir() {
		return normalizedSessionImport{}, errors.New("legacy session project is invalid")
	}
	expectedProjectRoots, err := statepath.ProjectRoots(project)
	if err != nil {
		return normalizedSessionImport{}, err
	}
	legacyTranscript, err := canonicalPath(target.TranscriptDir)
	if err != nil || !samePath(legacyTranscript, filepath.Join(expectedProjectRoots.Legacy, "transcripts")) {
		return normalizedSessionImport{}, errors.New("legacy transcript root is not the project default")
	}
	if !filepath.IsAbs(request.UserRoots.Canonical) || !filepath.IsAbs(request.UserRoots.Legacy) ||
		filepath.Clean(request.UserRoots.Canonical) != request.UserRoots.Canonical ||
		filepath.Clean(request.UserRoots.Legacy) != request.UserRoots.Legacy {
		return normalizedSessionImport{}, errors.New("user state roots are invalid")
	}
	expectedUserRoots, err := statepath.UserRoots(filepath.Dir(request.UserRoots.Canonical))
	if err != nil || !samePath(expectedUserRoots.Canonical, request.UserRoots.Canonical) ||
		!samePath(expectedUserRoots.Legacy, request.UserRoots.Legacy) {
		return normalizedSessionImport{}, errors.New("user state roots are not the default pair")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	canonicalTranscript := filepath.Join(expectedProjectRoots.Canonical, "transcripts")
	root := SessionRoot{
		CWD:           project,
		TranscriptDir: canonicalTranscript,
		RepositoryKey: repositoryKey(project),
		UpdatedAt:     now,
	}
	return normalizedSessionImport{
		target:              target,
		userRoots:           request.UserRoots,
		project:             project,
		projectStateRoot:    expectedProjectRoots.Canonical,
		legacyTranscript:    legacyTranscript,
		canonicalTranscript: canonicalTranscript,
		canonicalCatalog:    filepath.Join(request.UserRoots.Canonical, "session-roots.json"),
		repositoryKey:       sessionImportRepositoryKey(root),
		root:                root,
	}, nil
}

func sessionImportRepositoryKey(root SessionRoot) string {
	source := "cwd\x00" + root.CWD
	if root.RepositoryKey != "" {
		source = "git\x00" + root.RepositoryKey
	}
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func runLockedSessionImport(
	ctx context.Context,
	normalized normalizedSessionImport,
	allowSourcePrepare bool,
) (SessionRoot, error) {
	journalStore, _, err := statemigration.OpenCanonicalStore(
		normalized.userRoots.Canonical,
		sessionImportJournalRelative(normalized.repositoryKey),
		true,
	)
	if err != nil {
		return SessionRoot{}, importUnsafe(err)
	}
	defer journalStore.Close() //nolint:errcheck
	releaseJournal, err := journalStore.Lock(ctx, sessionImportLockName(normalized.target.SessionID))
	if err != nil {
		return SessionRoot{}, importUnsafe(err)
	}
	defer releaseJournal()

	var imported SessionRoot
	err = withPrivateSessionCatalogLocked(ctx, normalized.canonicalCatalog, func(catalog *sessionCatalogTransaction) error {
		projectControl, _, openErr := statemigration.OpenCanonicalStore(
			normalized.projectStateRoot,
			sessionImportProjectControlRelative(),
			true,
		)
		if openErr != nil {
			return openErr
		}
		defer projectControl.Close() //nolint:errcheck
		releaseProject, lockErr := projectControl.Lock(
			ctx,
			sessionImportLockName(normalized.target.SessionID),
		)
		if lockErr != nil {
			return lockErr
		}
		defer releaseProject()

		targetStore, _, targetErr := statemigration.OpenCanonicalStore(
			normalized.projectStateRoot,
			"transcripts",
			true,
		)
		if targetErr != nil {
			return targetErr
		}
		defer targetStore.Close() //nolint:errcheck
		imported, targetErr = executeSessionImport(
			ctx,
			normalized,
			journalStore,
			projectControl,
			targetStore,
			catalog,
			allowSourcePrepare,
		)
		return targetErr
	})
	if err != nil {
		if errors.Is(err, ErrSessionImportAlreadyCommitted) ||
			errors.Is(err, ErrSessionImportCollision) ||
			errors.Is(err, ErrSessionImportUnsafe) {
			return SessionRoot{}, err
		}
		return SessionRoot{}, importUnsafe(err)
	}
	return imported, nil
}

func executeSessionImport(
	ctx context.Context,
	normalized normalizedSessionImport,
	journalStore *statemigration.CanonicalStore,
	projectControl *statemigration.CanonicalStore,
	targetStore *statemigration.CanonicalStore,
	catalog *sessionCatalogTransaction,
	allowSourcePrepare bool,
) (SessionRoot, error) {
	journal, exists, err := readImportJournal(journalStore, normalized.target.SessionID)
	if err != nil {
		return SessionRoot{}, importUnsafe(err)
	}
	if exists {
		if err := journalMatchesImport(journal, normalized); err != nil {
			return SessionRoot{}, fmt.Errorf("%w: journal request mismatch", ErrSessionImportCollision)
		}
		normalized.root.UpdatedAt = journal.CatalogUpdatedAt
		if journal.Phase == PhaseCatalogCommitted {
			if err := verifyCommittedSessionImport(ctx, journal, projectControl, targetStore, catalog, normalized.root); err != nil {
				return SessionRoot{}, importUnsafe(err)
			}
			return SessionRoot{}, ErrSessionImportAlreadyCommitted
		}
	} else {
		if err := ensureNewSessionDestinationEmpty(normalized.target.SessionID, projectControl, targetStore); err != nil {
			return SessionRoot{}, err
		}
		if !allowSourcePrepare {
			return SessionRoot{}, ErrSessionImportAttestationRequired
		}
		prepared, files, prepareErr := prepareLegacySessionBundle(ctx, normalized)
		if prepareErr != nil {
			return SessionRoot{}, prepareErr
		}
		defer prepared.Close() //nolint:errcheck
		token, tokenErr := newSessionImportToken()
		if tokenErr != nil {
			return SessionRoot{}, importUnsafe(tokenErr)
		}
		journal = ImportJournal{
			Version:                sessionImportJournalVersion,
			SessionID:              normalized.target.SessionID,
			RepositoryKey:          normalized.repositoryKey,
			LegacyTranscript:       filepath.Join(normalized.legacyTranscript, normalized.target.SessionID+".jsonl"),
			CanonicalProject:       normalized.project,
			CanonicalTranscriptDir: normalized.canonicalTranscript,
			CanonicalCatalog:       normalized.canonicalCatalog,
			CatalogUpdatedAt:       normalized.root.UpdatedAt,
			StageRelative:          sessionImportStageRelative(token),
			Files:                  files,
			Phase:                  PhasePrepared,
		}
		if err := persistImportJournal(ctx, journalStore, journal); err != nil {
			return SessionRoot{}, err
		}
		if err := stageAndPromoteSessionBundle(
			ctx, normalized, journalStore, targetStore, prepared, &journal,
		); err != nil {
			return SessionRoot{}, err
		}
	}

	if journal.Phase == PhasePrepared {
		allPromoted, stateErr := allBundleFilesMatch(targetStore, journal.Files)
		if stateErr != nil {
			return SessionRoot{}, stateErr
		}
		if allPromoted {
			if err := validateSessionBundleDirectory(ctx, normalized.canonicalTranscript, journal.SessionID); err != nil {
				return SessionRoot{}, importUnsafe(err)
			}
			journal.Phase = PhaseFilesPromoted
			if err := persistImportJournal(ctx, journalStore, journal); err != nil {
				return SessionRoot{}, err
			}
		} else {
			var prepared *statemigration.PreparedFileSet
			if allowSourcePrepare {
				var files []BundleFile
				prepared, files, err = prepareLegacySessionBundle(ctx, normalized)
				if err != nil {
					return SessionRoot{}, err
				}
				defer prepared.Close() //nolint:errcheck
				if !sameBundleFiles(files, journal.Files) {
					return SessionRoot{}, fmt.Errorf("%w: legacy bundle changed after prepare", ErrSessionImportUnsafe)
				}
			}
			if err := stageAndPromoteSessionBundle(
				ctx, normalized, journalStore, targetStore, prepared, &journal,
			); err != nil {
				return SessionRoot{}, err
			}
		}
	}

	if journal.Phase == PhaseFilesPromoted {
		if err := verifyPromotedSessionFiles(ctx, normalized, targetStore, journal); err != nil {
			return SessionRoot{}, err
		}
		expected := markerFromJournal(journal)
		marker, markerExists, markerErr := readSessionImportMarker(projectControl, journal.SessionID)
		if markerErr != nil {
			return SessionRoot{}, importUnsafe(markerErr)
		}
		if markerExists {
			if !sameSessionImportMarker(marker, expected) {
				return SessionRoot{}, fmt.Errorf("%w: project marker collision", ErrSessionImportCollision)
			}
		} else {
			if err := writeSessionImportMarker(projectControl, expected); err != nil {
				return SessionRoot{}, importUnsafe(err)
			}
			if err := fireSessionImportFailureHook(ctx, failureAfterMarkerWrite, journal.SessionID); err != nil {
				return SessionRoot{}, err
			}
			if err := projectControl.Sync(); err != nil {
				return SessionRoot{}, importUnsafe(err)
			}
			if err := fireSessionImportFailureHook(ctx, failureAfterMarkerSync, journal.SessionID); err != nil {
				return SessionRoot{}, err
			}
		}
		journal.Phase = PhaseMarkerCommitted
		if err := persistImportJournal(ctx, journalStore, journal); err != nil {
			return SessionRoot{}, err
		}
	}

	if journal.Phase == PhaseMarkerCommitted {
		if err := verifyProjectMarkerAndFiles(ctx, journal, projectControl, targetStore); err != nil {
			return SessionRoot{}, err
		}
		root := normalized.root
		root.UpdatedAt = journal.CatalogUpdatedAt
		if !catalog.containsRootIdentity(root) {
			catalog.upsert(root)
			if err := catalog.commit(sessionCatalogWriteHooks{
				afterReplace: func() error {
					return fireSessionImportFailureHook(ctx, failureAfterCatalogReplace, journal.SessionID)
				},
				afterParentSync: func() error {
					return fireSessionImportFailureHook(ctx, failureAfterCatalogSync, journal.SessionID)
				},
			}); err != nil {
				return SessionRoot{}, err
			}
		} else if catalog.store != nil {
			if err := catalog.store.Sync(); err != nil {
				return SessionRoot{}, importUnsafe(err)
			}
		} else if err := syncSessionCatalogDirectory(filepath.Dir(journal.CanonicalCatalog)); err != nil {
			return SessionRoot{}, importUnsafe(err)
		}
		loaded, err := LoadSessionRoots(journal.CanonicalCatalog)
		if err != nil || !sessionRootsContain(loaded, root) {
			return SessionRoot{}, importUnsafe(errors.New("canonical catalog reread mismatch"))
		}
		journal.Phase = PhaseCatalogCommitted
		if err := persistImportJournal(ctx, journalStore, journal); err != nil {
			return SessionRoot{}, err
		}
	}

	if err := verifyCommittedSessionImport(ctx, journal, projectControl, targetStore, catalog, normalized.root); err != nil {
		return SessionRoot{}, err
	}
	cleanupSessionImportStage(normalized.projectStateRoot, journal)
	return normalized.root, nil
}

func sessionBundleNames(sessionID string) []string {
	return []string{
		sessionID + ".jsonl",
		sessionID + workboard.AuthorityRecordSuffix,
		sessionID + workboard.AuthorityMarkerSuffix,
		sessionID + workboard.LegacyBackupSuffix,
	}
}

func prepareLegacySessionBundle(
	ctx context.Context,
	normalized normalizedSessionImport,
) (*statemigration.PreparedFileSet, []BundleFile, error) {
	names := sessionBundleNames(normalized.target.SessionID)
	validateCalls := 0
	spec := statemigration.FileSetSpec{
		Owner:      "session",
		Scope:      "project",
		SourceDir:  normalized.legacyTranscript,
		LegacyMode: statemigration.LegacyOwnerControlled,
		Files: []statemigration.ExactFileSpec{
			{Name: names[0], Required: true, MaxBytes: sessionImportTranscriptMaxBytes},
			{Name: names[1], MaxBytes: workboard.MaxEncodedJSONBytes},
			{Name: names[2], MaxBytes: workboard.MaxEncodedJSONBytes},
			{Name: names[3], MaxBytes: workboard.MaxEncodedJSONBytes},
		},
		Validate: func(validateCtx context.Context, snapshot statemigration.Snapshot) error {
			if err := validateSessionBundleSnapshot(snapshot, names[0]); err != nil {
				return err
			}
			validateCalls++
			if validateCalls == 1 {
				return fireSessionImportFailureHook(
					validateCtx, failureAfterFirstSourceSnapshot, normalized.target.SessionID,
				)
			}
			return nil
		},
	}
	prepared, status, err := statemigration.PrepareFileSet(ctx, spec)
	if err != nil {
		return nil, nil, importUnsafe(err)
	}
	if status != statemigration.StatusReady || prepared == nil {
		return nil, nil, importUnsafe(errors.New("legacy session bundle is absent"))
	}
	files, err := manifestFromSnapshot(prepared.Snapshot())
	if err != nil {
		_ = prepared.Close()
		return nil, nil, importUnsafe(err)
	}
	return prepared, files, nil
}

func validateSessionBundleSnapshot(snapshot statemigration.Snapshot, transcriptName string) error {
	foundTranscript := false
	count := 0
	err := snapshot.Walk(func(relative string, entry fs.DirEntry) error {
		if entry.IsDir() || filepath.Base(relative) != relative {
			return errors.New("session bundle snapshot is not flat")
		}
		count++
		foundTranscript = foundTranscript || relative == transcriptName
		return nil
	})
	if err != nil || !foundTranscript || count < 1 || count > 4 {
		return errors.New("session bundle snapshot is invalid")
	}
	return nil
}

func manifestFromSnapshot(snapshot statemigration.Snapshot) ([]BundleFile, error) {
	files := make([]BundleFile, 0, 4)
	err := snapshot.Walk(func(relative string, entry fs.DirEntry) error {
		reader, info, err := snapshot.Open(relative)
		if err != nil {
			return err
		}
		defer reader.Close() //nolint:errcheck
		hash, size, err := hashSessionImportReader(reader, info.Size())
		if err != nil {
			return err
		}
		files = append(files, BundleFile{
			RelativePath: relative,
			SHA256:       hash,
			Mode:         0o600,
			Size:         size,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return files, nil
}

func stageAndPromoteSessionBundle(
	ctx context.Context,
	normalized normalizedSessionImport,
	journalStore *statemigration.CanonicalStore,
	targetStore *statemigration.CanonicalStore,
	prepared *statemigration.PreparedFileSet,
	journal *ImportJournal,
) error {
	stageStore, _, err := statemigration.OpenCanonicalStore(
		normalized.projectStateRoot,
		journal.StageRelative,
		true,
	)
	if err != nil {
		return importUnsafe(err)
	}
	defer stageStore.Close() //nolint:errcheck
	if prepared != nil {
		if err := copySessionSnapshotToStage(ctx, prepared, stageStore, journal.Files); err != nil {
			return err
		}
	} else if err := reconstructSessionStageFromCanonicalTargets(
		targetStore,
		stageStore,
		journal.Files,
	); err != nil {
		return err
	}
	if err := validateExactStoreManifest(stageStore, journal.Files); err != nil {
		return importUnsafe(err)
	}
	stagePath := filepath.Join(normalized.projectStateRoot, filepath.FromSlash(journal.StageRelative))
	if err := validateSessionBundleDirectory(ctx, stagePath, journal.SessionID); err != nil {
		return importUnsafe(err)
	}
	if prepared != nil {
		if err := prepared.Revalidate(ctx); err != nil {
			return importUnsafe(err)
		}
	}
	for _, bundleFile := range journal.Files {
		matches, _, err := canonicalFileMatches(targetStore, bundleFile)
		if err != nil {
			return fmt.Errorf("%w: canonical bundle collision", ErrSessionImportCollision)
		}
		if matches {
			continue
		}
		stageMatches, info, err := canonicalFileMatches(stageStore, bundleFile)
		if err != nil || !stageMatches {
			return importUnsafe(errors.New("staged bundle file changed"))
		}
		if prepared != nil {
			if err := prepared.Revalidate(ctx); err != nil {
				return importUnsafe(err)
			}
		}
		collision, err := targetStore.PromoteRegularFromNoReplace(
			stageStore,
			bundleFile.RelativePath,
			bundleFile.RelativePath,
			info,
		)
		if err != nil {
			return importUnsafe(err)
		}
		if collision {
			return fmt.Errorf("%w: canonical bundle target exists", ErrSessionImportCollision)
		}
		if err := fireSessionImportFailureHook(ctx, failureAfterFileRename, bundleFile.RelativePath); err != nil {
			return err
		}
		if err := fireSessionImportFailureHook(ctx, failureAfterFileSync, bundleFile.RelativePath); err != nil {
			return err
		}
	}
	if prepared != nil {
		if err := prepared.Revalidate(ctx); err != nil {
			return importUnsafe(err)
		}
	}
	if err := validateStoreManifest(targetStore, journal.Files); err != nil {
		return importUnsafe(err)
	}
	if err := validateSessionBundleDirectory(ctx, normalized.canonicalTranscript, journal.SessionID); err != nil {
		return importUnsafe(err)
	}
	journal.Phase = PhaseFilesPromoted
	return persistImportJournal(ctx, journalStore, *journal)
}

func copySessionSnapshotToStage(
	ctx context.Context,
	prepared *statemigration.PreparedFileSet,
	stageStore *statemigration.CanonicalStore,
	files []BundleFile,
) error {
	for _, bundleFile := range files {
		matches, existing, err := canonicalFileMatches(stageStore, bundleFile)
		if err != nil {
			return importUnsafe(err)
		}
		if matches {
			file, _, _, openErr := stageStore.OpenRegular(bundleFile.RelativePath, os.O_RDWR, false)
			if openErr != nil {
				return importUnsafe(openErr)
			}
			syncErr := file.Sync()
			closeErr := file.Close()
			if syncErr != nil || closeErr != nil {
				return importUnsafe(errors.Join(syncErr, closeErr))
			}
			continue
		}
		if existing != nil {
			stageStore.RemoveRegularIfSame(bundleFile.RelativePath, existing)
			if err := stageStore.Sync(); err != nil {
				return importUnsafe(err)
			}
		}
		source, info, err := prepared.Snapshot().Open(bundleFile.RelativePath)
		if err != nil {
			return importUnsafe(err)
		}
		data, err := io.ReadAll(io.LimitReader(source, bundleFile.Size+1))
		closeErr := source.Close()
		if err != nil || closeErr != nil || int64(len(data)) != bundleFile.Size || info.Size() != bundleFile.Size {
			return importUnsafe(errors.New("prepared session bundle changed"))
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != bundleFile.SHA256 {
			return importUnsafe(errors.New("prepared session bundle hash mismatch"))
		}
		file, _, created, err := stageStore.OpenRegular(bundleFile.RelativePath, os.O_WRONLY, true)
		if err != nil || !created {
			return importUnsafe(errors.New("create staged session bundle file"))
		}
		if err := writeAllSessionImport(file, data); err != nil {
			_ = file.Close()
			return importUnsafe(err)
		}
		if err := fireSessionImportFailureHook(ctx, failureAfterStageFileWrite, bundleFile.RelativePath); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return importUnsafe(err)
		}
		if err := fireSessionImportFailureHook(ctx, failureAfterStageFileSync, bundleFile.RelativePath); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return importUnsafe(err)
		}
	}
	if err := stageStore.Sync(); err != nil {
		return importUnsafe(err)
	}
	if err := fireSessionImportFailureHook(ctx, failureAfterStageParentSync, "stage"); err != nil {
		return err
	}
	if err := prepared.Revalidate(ctx); err != nil {
		return importUnsafe(err)
	}
	return validateExactStoreManifest(stageStore, files)
}

func reconstructSessionStageFromCanonicalTargets(
	targetStore *statemigration.CanonicalStore,
	stageStore *statemigration.CanonicalStore,
	files []BundleFile,
) error {
	for _, bundleFile := range files {
		stageMatches, stageInfo, err := canonicalFileMatches(stageStore, bundleFile)
		if err != nil {
			return importUnsafe(err)
		}
		if stageMatches {
			continue
		}
		if stageInfo != nil {
			return importUnsafe(errors.New("journal-owned stage file changed"))
		}
		targetMatches, _, err := canonicalFileMatches(targetStore, bundleFile)
		if err != nil {
			return fmt.Errorf("%w: promoted session file is unsafe", ErrSessionImportCollision)
		}
		if !targetMatches {
			return importUnsafe(errors.New("prepared import has no recoverable file union"))
		}
		data, err := readCanonicalBundleFile(targetStore, bundleFile)
		if err != nil {
			return importUnsafe(err)
		}
		file, _, created, err := stageStore.OpenRegular(bundleFile.RelativePath, os.O_WRONLY, true)
		if err != nil || !created {
			return importUnsafe(errors.New("recreate journal-owned stage file"))
		}
		if err := writeAllSessionImport(file, data); err != nil {
			_ = file.Close()
			return importUnsafe(err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return importUnsafe(err)
		}
		if err := file.Close(); err != nil {
			return importUnsafe(err)
		}
	}
	if err := stageStore.Sync(); err != nil {
		return importUnsafe(err)
	}
	return validateExactStoreManifest(stageStore, files)
}

func readCanonicalBundleFile(
	store *statemigration.CanonicalStore,
	expected BundleFile,
) ([]byte, error) {
	file, info, exists, err := store.OpenRegular(expected.RelativePath, os.O_RDONLY, false)
	if err != nil || !exists {
		return nil, errors.New("open canonical bundle file")
	}
	data, err := io.ReadAll(io.LimitReader(file, expected.Size+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || int64(len(data)) != expected.Size {
		return nil, errors.New("read canonical bundle file")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected.SHA256 ||
		store.ValidateRegular(expected.RelativePath, info) != nil {
		return nil, errors.New("canonical bundle file changed while reading")
	}
	return data, nil
}

func ensureNewSessionDestinationEmpty(
	sessionID string,
	projectControl *statemigration.CanonicalStore,
	targetStore *statemigration.CanonicalStore,
) error {
	for _, name := range sessionBundleNames(sessionID) {
		if _, err := targetStore.Root().Lstat(name); err == nil {
			return fmt.Errorf("%w: canonical session file exists", ErrSessionImportCollision)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return importUnsafe(err)
		}
	}
	if _, err := projectControl.Root().Lstat(sessionImportMarkerName(sessionID)); err == nil {
		return fmt.Errorf("%w: project marker exists", ErrSessionImportCollision)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return importUnsafe(err)
	}
	return nil
}

func canonicalFileMatches(
	store *statemigration.CanonicalStore,
	expected BundleFile,
) (bool, os.FileInfo, error) {
	file, info, exists, err := store.OpenRegular(expected.RelativePath, os.O_RDONLY, false)
	if err != nil || !exists {
		return false, info, err
	}
	defer file.Close() //nolint:errcheck
	hash, size, err := hashSessionImportReader(file, expected.Size)
	if err != nil {
		return false, info, err
	}
	if err := store.ValidateRegular(expected.RelativePath, info); err != nil {
		return false, info, err
	}
	return hash == expected.SHA256 && size == expected.Size && info.Mode().Perm() == os.FileMode(expected.Mode), info, nil
}

func hashSessionImportReader(reader io.Reader, expectedSize int64) (string, int64, error) {
	if expectedSize < 0 {
		return "", 0, errors.New("session import file size is invalid")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(reader, expectedSize+1))
	if err != nil || written != expectedSize {
		return "", written, errors.New("session import file size changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func validateStoreManifest(store *statemigration.CanonicalStore, files []BundleFile) error {
	for _, file := range files {
		matches, _, err := canonicalFileMatches(store, file)
		if err != nil || !matches {
			return errors.New("session import file does not match its manifest")
		}
	}
	return store.Revalidate()
}

func validateMutableStoreManifest(
	store *statemigration.CanonicalStore,
	files []BundleFile,
) error {
	for _, expected := range files {
		file, _, exists, err := store.OpenRegular(expected.RelativePath, os.O_RDONLY, false)
		if err != nil || !exists {
			if file != nil {
				_ = file.Close()
			}
			return errors.New("committed session import file is unavailable")
		}
		if err := file.Close(); err != nil {
			return errors.New("committed session import file is unavailable")
		}
	}
	return store.Revalidate()
}

func validateExactStoreManifest(store *statemigration.CanonicalStore, files []BundleFile) error {
	entries, err := fs.ReadDir(store.Root().FS(), ".")
	if err != nil || len(entries) != len(files) {
		return errors.New("session import stage has unexpected entries")
	}
	expected := make(map[string]struct{}, len(files))
	for _, file := range files {
		expected[file.RelativePath] = struct{}{}
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return errors.New("session import stage contains a directory")
		}
		if _, ok := expected[entry.Name()]; !ok {
			return errors.New("session import stage contains an unowned file")
		}
	}
	return validateStoreManifest(store, files)
}

func allBundleFilesMatch(
	store *statemigration.CanonicalStore,
	files []BundleFile,
) (bool, error) {
	matched := 0
	for _, file := range files {
		matches, info, err := canonicalFileMatches(store, file)
		if err != nil {
			return false, fmt.Errorf("%w: canonical session path is unsafe", ErrSessionImportCollision)
		}
		if info != nil && !matches {
			return false, fmt.Errorf("%w: canonical session file differs", ErrSessionImportCollision)
		}
		if matches {
			matched++
		}
	}
	return matched == len(files), nil
}

func validateSessionBundleDirectory(ctx context.Context, directory, sessionID string) error {
	if _, err := transcript.NewRecorder(sessionID, directory).LoadFullContext(ctx); err != nil {
		return fmt.Errorf("validate imported transcript: %w", err)
	}
	store, err := workboard.NewStore(workboard.StoreConfig{Dir: directory, SessionID: sessionID})
	if err != nil {
		return err
	}
	_, err = store.Inspect()
	return err
}

func persistImportJournal(
	ctx context.Context,
	store *statemigration.CanonicalStore,
	journal ImportJournal,
) error {
	if err := writeImportJournal(store, journal); err != nil {
		return importUnsafe(err)
	}
	return fireSessionImportFailureHook(ctx, failureAfterJournalWrite, string(journal.Phase))
}

func verifyPromotedSessionFiles(
	ctx context.Context,
	normalized normalizedSessionImport,
	targetStore *statemigration.CanonicalStore,
	journal ImportJournal,
) error {
	if err := validateStoreManifest(targetStore, journal.Files); err != nil {
		return importUnsafe(err)
	}
	if err := validateSessionBundleDirectory(ctx, normalized.canonicalTranscript, journal.SessionID); err != nil {
		return importUnsafe(err)
	}
	return nil
}

func verifyProjectMarkerAndFiles(
	ctx context.Context,
	journal ImportJournal,
	projectControl *statemigration.CanonicalStore,
	targetStore *statemigration.CanonicalStore,
) error {
	marker, exists, err := readSessionImportMarker(projectControl, journal.SessionID)
	if err != nil || !exists || !sameSessionImportMarker(marker, markerFromJournal(journal)) {
		return importUnsafe(errors.New("session import project marker mismatch"))
	}
	if err := validateStoreManifest(targetStore, journal.Files); err != nil {
		return importUnsafe(err)
	}
	return validateSessionBundleDirectory(ctx, journal.CanonicalTranscriptDir, journal.SessionID)
}

func verifyCommittedSessionImport(
	ctx context.Context,
	journal ImportJournal,
	projectControl *statemigration.CanonicalStore,
	targetStore *statemigration.CanonicalStore,
	catalog *sessionCatalogTransaction,
	root SessionRoot,
) error {
	if journal.Phase != PhaseCatalogCommitted {
		return importUnsafe(errors.New("session import is not catalog committed"))
	}
	if err := verifyProjectMarkerAndFiles(ctx, journal, projectControl, targetStore); err != nil {
		return err
	}
	root.UpdatedAt = journal.CatalogUpdatedAt
	if !catalog.containsRootIdentity(root) {
		return importUnsafe(errors.New("session import catalog entry mismatch"))
	}
	loaded, err := catalog.loadPrivateRoots()
	if err != nil || !sessionRootsContain(loaded, root) {
		return importUnsafe(errors.New("session import catalog reread mismatch"))
	}
	return nil
}

func verifyCommittedSessionImportAdmission(
	ctx context.Context,
	journal ImportJournal,
	projectControl *statemigration.CanonicalStore,
	targetStore *statemigration.CanonicalStore,
	catalog *sessionCatalogTransaction,
	root SessionRoot,
) error {
	// The marker and journal prove the original transaction. Once committed,
	// transcript and WorkBoard files become ordinary mutable canonical state,
	// so admission revalidates their owner contracts instead of freezing the
	// import-time hashes and catalog timestamp forever.
	if journal.Phase != PhaseCatalogCommitted {
		return importUnsafe(errors.New("session import is not catalog committed"))
	}
	marker, exists, err := readSessionImportMarker(projectControl, journal.SessionID)
	if err != nil || !exists || !sameSessionImportMarker(marker, markerFromJournal(journal)) {
		return importUnsafe(errors.New("session import project marker mismatch"))
	}
	if err := validateMutableStoreManifest(targetStore, journal.Files); err != nil {
		return importUnsafe(err)
	}
	if err := validateSessionBundleDirectory(ctx, journal.CanonicalTranscriptDir, journal.SessionID); err != nil {
		return importUnsafe(err)
	}
	if !catalog.containsRootIdentity(root) {
		return importUnsafe(errors.New("session import catalog entry mismatch"))
	}
	loaded, err := catalog.loadPrivateRoots()
	if err != nil || !sessionRootsContain(loaded, root) {
		return importUnsafe(errors.New("session import catalog reread mismatch"))
	}
	return nil
}

func journalMatchesImport(journal ImportJournal, normalized normalizedSessionImport) error {
	if journal.SessionID != normalized.target.SessionID ||
		journal.RepositoryKey != normalized.repositoryKey ||
		!samePath(journal.LegacyTranscript, filepath.Join(normalized.legacyTranscript, normalized.target.SessionID+".jsonl")) ||
		!samePath(journal.CanonicalProject, normalized.project) ||
		!samePath(journal.CanonicalTranscriptDir, normalized.canonicalTranscript) ||
		!samePath(journal.CanonicalCatalog, normalized.canonicalCatalog) {
		return errors.New("session import journal belongs to another request")
	}
	return nil
}

func sameBundleFiles(left, right []BundleFile) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameSessionImportMarker(left, right sessionImportMarker) bool {
	return left.Version == right.Version && left.SessionID == right.SessionID &&
		left.RepositoryKey == right.RepositoryKey &&
		samePath(left.CanonicalTranscriptDir, right.CanonicalTranscriptDir) &&
		samePath(left.CanonicalCatalog, right.CanonicalCatalog) &&
		sameBundleFiles(left.Files, right.Files)
}

func sessionRootsContain(roots []SessionRoot, expected SessionRoot) bool {
	for _, root := range roots {
		if sameSessionRootIdentity(root, expected) {
			return true
		}
	}
	return false
}

func cleanupSessionImportStage(projectStateRoot string, journal ImportJournal) {
	stageStore, exists, err := statemigration.OpenCanonicalStore(
		projectStateRoot,
		journal.StageRelative,
		false,
	)
	if err != nil || !exists {
		return
	}
	for _, bundleFile := range journal.Files {
		file, info, fileExists, openErr := stageStore.OpenRegular(
			bundleFile.RelativePath,
			os.O_RDONLY,
			false,
		)
		if openErr != nil || !fileExists {
			continue
		}
		_ = file.Close()
		stageStore.RemoveRegularIfSame(bundleFile.RelativePath, info)
	}
	_ = stageStore.Sync()
	_ = stageStore.Close()
	stagePath := filepath.Join(projectStateRoot, filepath.FromSlash(journal.StageRelative))
	if err := os.Remove(stagePath); err == nil {
		_ = syncSessionCatalogDirectory(filepath.Dir(stagePath))
	}
}

func importUnsafe(err error) error {
	if err == nil {
		return ErrSessionImportUnsafe
	}
	if errors.Is(err, ErrSessionImportUnsafe) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrSessionImportUnsafe, err)
}
