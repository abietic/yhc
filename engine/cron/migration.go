package cron

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

// MigrationStatus is a value-free cron migration outcome.
type MigrationStatus string

const (
	MigrationAbsent            MigrationStatus = "absent"
	MigrationReady             MigrationStatus = "ready"
	MigrationImported          MigrationStatus = "imported"
	MigrationDestinationExists MigrationStatus = "destination_exists"
	MigrationLegacyBusy        MigrationStatus = "legacy_busy"
	MigrationUnsafe            MigrationStatus = "unsafe"
)

var (
	ErrLegacyStoppedAttestationRequired = errors.New("legacy cron producer stopped attestation is required")
	ErrCronMigrationUnsafe              = errors.New("cron migration is unsafe")
)

const legacyStabilityInterval = 100 * time.Millisecond

var (
	migrationProcessAlive     = isProcessAlive
	migrationWaitForStability = waitForCronStability
)

// LegacyInspection exposes only a status and task count, never prompts or paths.
type LegacyInspection struct {
	Status    MigrationStatus `json:"status"`
	TaskCount int             `json:"task_count"`
}

// ImportRequest authorizes one explicit, attested legacy cron import.
type ImportRequest struct {
	ProjectDir           string
	ConfirmLegacyStopped bool
}

type legacyCronSnapshot struct {
	tasks      []Task
	taskDigest [sha256.Size]byte
	lock       *LockInfo
}

// InspectLegacy strictly inspects legacy cron state without creating canonical
// roots, locks, stages, or files.
func InspectLegacy(ctx context.Context, projectDir string) (LegacyInspection, error) {
	prepared, roots, status, err := prepareLegacyCron(ctx, projectDir)
	if err != nil {
		return LegacyInspection{Status: MigrationUnsafe}, err
	}
	if prepared == nil {
		return LegacyInspection{Status: status}, nil
	}
	defer prepared.Close() //nolint:errcheck

	legacy, err := readLegacyCronSnapshot(prepared.Snapshot())
	if err != nil {
		return LegacyInspection{Status: MigrationUnsafe}, ErrCronMigrationUnsafe
	}
	inspection := LegacyInspection{Status: MigrationReady, TaskCount: len(legacy.tasks)}
	collision, err := canonicalCronExists(roots)
	if err != nil {
		return LegacyInspection{Status: MigrationUnsafe}, ErrCronMigrationUnsafe
	}
	if collision {
		inspection.Status = MigrationDestinationExists
		return inspection, nil
	}
	if legacy.lock != nil && migrationProcessAlive(legacy.lock.PID) {
		inspection.Status = MigrationLegacyBusy
	}
	return inspection, nil
}

// ImportLegacy imports one strictly valid, explicitly quiesced legacy cron
// file. It never acquires, removes, or rewrites the legacy scheduler lock.
func ImportLegacy(ctx context.Context, request ImportRequest) (LegacyInspection, error) {
	if !request.ConfirmLegacyStopped {
		return LegacyInspection{Status: MigrationUnsafe}, ErrLegacyStoppedAttestationRequired
	}
	prepared, roots, status, err := prepareLegacyCron(ctx, request.ProjectDir)
	if err != nil {
		return LegacyInspection{Status: MigrationUnsafe}, err
	}
	if prepared == nil {
		return LegacyInspection{Status: status}, nil
	}
	defer prepared.Close() //nolint:errcheck

	legacy, err := readLegacyCronSnapshot(prepared.Snapshot())
	if err != nil {
		return LegacyInspection{Status: MigrationUnsafe}, ErrCronMigrationUnsafe
	}
	inspection := LegacyInspection{Status: MigrationReady, TaskCount: len(legacy.tasks)}
	if legacy.lock != nil && migrationProcessAlive(legacy.lock.PID) {
		inspection.Status = MigrationLegacyBusy
		return inspection, nil
	}
	if err := migrationWaitForStability(ctx, legacyStabilityInterval); err != nil ||
		prepared.Revalidate(ctx) != nil {
		return LegacyInspection{Status: MigrationUnsafe}, ErrCronMigrationUnsafe
	}
	stable, err := readLegacyCronSnapshot(prepared.Snapshot())
	if err != nil || stable.taskDigest != legacy.taskDigest ||
		(stable.lock != nil && migrationProcessAlive(stable.lock.PID)) {
		if err == nil && stable.lock != nil && migrationProcessAlive(stable.lock.PID) {
			inspection.Status = MigrationLegacyBusy
			return inspection, nil
		}
		return LegacyInspection{Status: MigrationUnsafe}, ErrCronMigrationUnsafe
	}
	collision, err := canonicalCronExists(roots)
	if err != nil {
		return LegacyInspection{Status: MigrationUnsafe}, ErrCronMigrationUnsafe
	}
	if collision {
		inspection.Status = MigrationDestinationExists
		return inspection, nil
	}

	spec := cronArtifactSpec(prepared, legacy.taskDigest)
	result, err := (statemigration.Importer{}).Import(ctx, roots, spec)
	if err != nil {
		return LegacyInspection{Status: MigrationUnsafe}, ErrCronMigrationUnsafe
	}
	inspection.Status = MigrationStatus(result.Status)
	return inspection, nil
}

func prepareLegacyCron(
	ctx context.Context,
	projectDir string,
) (*statemigration.PreparedFileSet, statepath.Roots, MigrationStatus, error) {
	roots, err := statepath.ProjectRoots(projectDir)
	if err != nil {
		return nil, statepath.Roots{}, MigrationUnsafe, ErrCronMigrationUnsafe
	}
	prepared, status, err := statemigration.PrepareFileSet(ctx, statemigration.FileSetSpec{
		Owner:      "cron",
		Scope:      "project",
		SourceDir:  roots.Legacy,
		LegacyMode: statemigration.LegacyOwnerControlled,
		Files: []statemigration.ExactFileSpec{
			{Name: cronFileName, Required: true, MaxBytes: maxCronFileBytes},
			{Name: schedulerLockName, Required: false, MaxBytes: 128},
		},
		Validate: func(_ context.Context, snapshot statemigration.Snapshot) error {
			_, err := readLegacyCronSnapshot(snapshot)
			return err
		},
	})
	if err != nil {
		return nil, roots, MigrationUnsafe, ErrCronMigrationUnsafe
	}
	return prepared, roots, MigrationStatus(status), nil
}

func cronArtifactSpec(
	prepared *statemigration.PreparedFileSet,
	expectedDigest [sha256.Size]byte,
) statemigration.ArtifactSpec {
	return statemigration.ArtifactSpec{
		Owner:      "cron",
		Scope:      "project",
		SourceRel:  cronFileName,
		TargetRel:  cronFileName,
		Kind:       statemigration.RegularFile,
		LegacyMode: statemigration.LegacyOwnerControlled,
		MaxFiles:   1,
		MaxBytes:   maxCronFileBytes,
		Validate: func(_ context.Context, snapshot statemigration.Snapshot) error {
			_, _, err := readTaskArtifact(snapshot)
			return err
		},
		Stage: func(_ context.Context, snapshot statemigration.Snapshot, target *os.Root) error {
			data, _, err := readTaskArtifact(snapshot)
			if err != nil {
				return err
			}
			return target.WriteFile(cronFileName, data, 0o600)
		},
		Quiescent: func(ctx context.Context, snapshot statemigration.Snapshot) (bool, error) {
			if prepared.Revalidate(ctx) != nil {
				return false, ErrCronMigrationUnsafe
			}
			_, digest, err := readTaskArtifact(snapshot)
			if err != nil || digest != expectedDigest {
				return false, ErrCronMigrationUnsafe
			}
			legacy, err := readLegacyCronSnapshot(prepared.Snapshot())
			if err != nil {
				return false, ErrCronMigrationUnsafe
			}
			return legacy.lock == nil || !migrationProcessAlive(legacy.lock.PID), nil
		},
	}
}

func readLegacyCronSnapshot(snapshot statemigration.Snapshot) (legacyCronSnapshot, error) {
	var result legacyCronSnapshot
	data, digest, err := readNamedTaskArtifact(snapshot, cronFileName)
	if err != nil {
		return result, ErrCronMigrationUnsafe
	}
	result.taskDigest = digest
	result.tasks, err = decodeStrictTasks(data)
	if err != nil {
		return legacyCronSnapshot{}, ErrCronMigrationUnsafe
	}
	hasLock := false
	if err := snapshot.Walk(func(relative string, _ os.DirEntry) error {
		if relative == schedulerLockName {
			hasLock = true
		}
		return nil
	}); err != nil {
		return legacyCronSnapshot{}, ErrCronMigrationUnsafe
	}
	if !hasLock {
		return result, nil
	}
	reader, _, err := snapshot.Open(schedulerLockName)
	if err != nil {
		return legacyCronSnapshot{}, ErrCronMigrationUnsafe
	}
	lockData, readErr := io.ReadAll(io.LimitReader(reader, 129))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(lockData) > 128 {
		return legacyCronSnapshot{}, ErrCronMigrationUnsafe
	}
	result.lock, err = parseLock(lockData)
	if err != nil {
		return legacyCronSnapshot{}, ErrCronMigrationUnsafe
	}
	return result, nil
}

func readTaskArtifact(snapshot statemigration.Snapshot) ([]byte, [sha256.Size]byte, error) {
	return readNamedTaskArtifact(snapshot, ".")
}

func readNamedTaskArtifact(
	snapshot statemigration.Snapshot,
	name string,
) ([]byte, [sha256.Size]byte, error) {
	reader, info, err := snapshot.Open(name)
	if err != nil || info.Size() < 0 || info.Size() > maxCronFileBytes {
		return nil, [sha256.Size]byte{}, ErrCronMigrationUnsafe
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, maxCronFileBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() {
		return nil, [sha256.Size]byte{}, ErrCronMigrationUnsafe
	}
	if _, err := decodeStrictTasks(data); err != nil {
		return nil, [sha256.Size]byte{}, ErrCronMigrationUnsafe
	}
	return data, sha256.Sum256(data), nil
}

func decodeStrictTasks(data []byte) ([]Task, error) {
	var file struct {
		Tasks []Task `json:"tasks"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, ErrCronMigrationUnsafe
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrCronMigrationUnsafe
	}
	for _, task := range file.Tasks {
		if _, err := ParseCronExpression(task.Cron); err != nil {
			return nil, ErrCronMigrationUnsafe
		}
	}
	return file.Tasks, nil
}

func canonicalCronExists(roots statepath.Roots) (bool, error) {
	store, exists, err := statemigration.OpenCanonicalStore(roots.Canonical, ".", false)
	if err != nil || !exists {
		return false, err
	}
	defer store.Close() //nolint:errcheck
	file, _, exists, err := store.OpenRegular(cronFileName, os.O_RDONLY, false)
	if file != nil {
		_ = file.Close()
	}
	return exists, err
}

func waitForCronStability(ctx context.Context, interval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
