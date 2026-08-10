package statemigration

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
)

// ExactFileSpec declares one regular file which may be read from a legacy
// directory. Files not named here are deliberately outside the snapshot.
type ExactFileSpec struct {
	Name     string
	Required bool
	MaxBytes int64
}

// FileSetSpec owns validation of one bounded, flat legacy directory snapshot.
type FileSetSpec struct {
	Owner      string
	Scope      string
	SourceDir  string
	LegacyMode LegacyMode
	Files      []ExactFileSpec
	Validate   func(context.Context, Snapshot) error
}

// PreparedFileSet retains a pinned source directory and its immutable exact
// allowlist snapshot. It is prepare-only: callers own all later journaling and
// promotion decisions.
type PreparedFileSet struct {
	directory *pinnedDirectory
	spec      FileSetSpec
	snapshot  *artifactSnapshot
}

// PrepareFileSet pins SourceDir and captures exactly its declared regular
// files twice. It never mutates legacy state.
func PrepareFileSet(ctx context.Context, spec FileSetSpec) (*PreparedFileSet, Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateFileSetSpec(spec); err != nil || ctx.Err() != nil {
		return nil, StatusUnsafe, errMigrationUnsafe
	}
	directoryMode, _, err := legacyModePolicies(spec.LegacyMode)
	if err != nil {
		return nil, StatusUnsafe, errMigrationUnsafe
	}
	directory, exists, err := openPinnedDirectory(spec.SourceDir, directoryMode)
	if err != nil {
		return nil, StatusUnsafe, errMigrationUnsafe
	}
	if !exists {
		return nil, StatusAbsent, nil
	}
	first, absent, err := captureFileSet(ctx, directory, spec)
	if err != nil {
		_ = directory.Close()
		return nil, StatusUnsafe, errMigrationUnsafe
	}
	if absent {
		_ = directory.Close()
		return nil, StatusAbsent, nil
	}
	if err := validateFileSet(ctx, spec, first); err != nil {
		_ = directory.Close()
		return nil, StatusUnsafe, errMigrationUnsafe
	}
	second, absent, err := captureFileSet(ctx, directory, spec)
	if err != nil || absent || compareSnapshots(first, second) != nil ||
		validateFileSet(ctx, spec, second) != nil {
		_ = directory.Close()
		return nil, StatusUnsafe, errMigrationUnsafe
	}
	return &PreparedFileSet{directory: directory, spec: spec, snapshot: second}, StatusReady, nil
}

// Snapshot returns the immutable prepared view, without exposing source paths
// or retained directory handles.
func (prepared *PreparedFileSet) Snapshot() Snapshot {
	if prepared == nil {
		return nil
	}
	return prepared.snapshot
}

// Revalidate proves the pinned source still has precisely the prepared files
// and reruns owner validation. It does not discover or inspect unlisted files.
func (prepared *PreparedFileSet) Revalidate(ctx context.Context) error {
	if prepared == nil || prepared.directory == nil || prepared.snapshot == nil {
		return errMigrationUnsafe
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current, absent, err := captureFileSet(ctx, prepared.directory, prepared.spec)
	if err != nil || absent || compareSnapshots(prepared.snapshot, current) != nil ||
		validateFileSet(ctx, prepared.spec, current) != nil {
		return errMigrationUnsafe
	}
	return nil
}

// Close releases the pinned source directory.
func (prepared *PreparedFileSet) Close() error {
	if prepared == nil || prepared.directory == nil {
		return nil
	}
	err := prepared.directory.Close()
	prepared.directory = nil
	return err
}

func validateFileSetSpec(spec FileSetSpec) error {
	if !ownerPattern.MatchString(spec.Owner) || len(spec.Owner) > 64 ||
		(spec.Scope != "project" && spec.Scope != "user") ||
		!validAbsoluteRoot(spec.SourceDir) ||
		(spec.LegacyMode != LegacyPrivate && spec.LegacyMode != LegacyOwnerControlled) ||
		len(spec.Files) < 1 || len(spec.Files) > 16 || spec.Validate == nil {
		return errMigrationUnsafe
	}
	seen := make(map[string]struct{}, len(spec.Files))
	for _, file := range spec.Files {
		if !safeNativeSegment(file.Name) || file.MaxBytes <= 0 {
			return errMigrationUnsafe
		}
		if _, exists := seen[file.Name]; exists {
			return errMigrationUnsafe
		}
		seen[file.Name] = struct{}{}
	}
	return nil
}

func captureFileSet(ctx context.Context, directory *pinnedDirectory, spec FileSetSpec) (*artifactSnapshot, bool, error) {
	if directory == nil || ctx.Err() != nil || directory.revalidate() != nil {
		return nil, false, errMigrationUnsafe
	}
	_, regularMode, err := legacyModePolicies(spec.LegacyMode)
	if err != nil {
		return nil, false, errMigrationUnsafe
	}
	entries := make([]snapshotEntry, 0, len(spec.Files))
	for _, file := range spec.Files {
		if ctx.Err() != nil {
			return nil, false, errMigrationUnsafe
		}
		name := filepath.FromSlash(file.Name)
		if _, err := directory.root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			if file.Required {
				return nil, true, nil
			}
			continue
		} else if err != nil {
			return nil, false, errMigrationUnsafe
		}
		entry, err := captureRegularFile(directory.root, name, file.Name, file.MaxBytes, regularMode)
		if err != nil {
			return nil, false, errMigrationUnsafe
		}
		entries = append(entries, entry)
	}
	if directory.revalidate() != nil {
		return nil, false, errMigrationUnsafe
	}
	return newArtifactSnapshot(RegularFile, entries), false, nil
}

func validateFileSet(ctx context.Context, spec FileSetSpec, snapshot Snapshot) error {
	if ctx.Err() != nil || spec.Validate(ctx, snapshot) != nil {
		return errMigrationUnsafe
	}
	return nil
}
