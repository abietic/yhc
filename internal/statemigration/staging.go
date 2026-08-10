package statemigration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const migrationControlRelative = ".migration/v1"

type migrationStage struct {
	name      string
	directory *pinnedRelativeDirectory
	snapshot  *artifactSnapshot
}

type stagedPromotion struct {
	sourceRoot    *os.Root
	sourceDirInfo os.FileInfo
	sourceName    string
	targetParent  *pinnedRelativeDirectory
	targetName    string
	artifactInfo  os.FileInfo
}

func importPrepared(
	ctx context.Context,
	canonicalPath string,
	legacy *pinnedDirectory,
	preflight *artifactSnapshot,
	spec ArtifactSpec,
) (Status, error) {
	canonical, err := ensureCanonicalDirectory(canonicalPath)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	defer canonical.Close() //nolint:errcheck
	migration, _, err := openRelativeDirectory(
		canonical,
		migrationControlRelative,
		true,
		canonicalDirectoryMode,
	)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	defer migration.Close() //nolint:errcheck

	lockName := spec.Owner + "-" + spec.Scope + ".lock"
	release, err := acquireMigrationLock(ctx, migration, lockName)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	defer release()
	if err := cleanupOwnedStages(migration, spec); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := canonical.revalidate(); err != nil || legacy.revalidate() != nil {
		return StatusUnsafe, errMigrationUnsafe
	}

	current, err := captureSourceSnapshot(legacy, spec)
	if err != nil || compareSnapshots(preflight, current) != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := validateOwnerSnapshot(ctx, spec, current); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	quiescent, err := ownerQuiescent(ctx, spec, current)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if !quiescent {
		return StatusLegacyBusy, nil
	}
	if err := fireImporterFailureHook(ctx, failureAfterSourceSnapshot); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := revalidateSourceSnapshot(legacy, spec, current); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}

	targetParent, _, err := openRelativeDirectory(
		canonical,
		path.Dir(spec.TargetRel),
		true,
		canonicalDirectoryMode,
	)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	defer targetParent.Close() //nolint:errcheck
	targetName := filepath.FromSlash(path.Base(spec.TargetRel))
	collision, err := targetExists(targetParent, targetName)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if collision {
		return StatusDestinationExists, nil
	}

	stage, err := createMigrationStage(migration, spec)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	defer cleanupMigrationStage(migration, stage) //nolint:errcheck
	if err := spec.Stage(ctx, current, stage.directory.root); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	stage.snapshot, err = captureStagedSnapshot(stage.directory, spec)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := validateOwnerSnapshot(ctx, spec, stage.snapshot); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := syncStagedSnapshot(ctx, stage, spec); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := revalidateSourceSnapshot(legacy, spec, current); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := syncRootDirectory(targetParent.root); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := fireImporterFailureHook(ctx, failureAfterTargetParentSync); err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := canonical.revalidate(); err != nil ||
		migration.revalidate() != nil || targetParent.revalidate() != nil ||
		revalidateSourceSnapshot(legacy, spec, current) != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	collision, err = targetExists(targetParent, targetName)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if collision {
		return StatusDestinationExists, nil
	}

	promotion, err := preparePromotion(migration, stage, targetParent, targetName, spec)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	collision, err = applyPromotion(promotion)
	if err != nil {
		return StatusUnsafe, errMigrationUnsafe
	}
	if collision {
		return StatusDestinationExists, nil
	}
	rollback := func() error {
		return rollbackPromotion(promotion)
	}
	if err := fireImporterFailureHook(ctx, failureAfterRename); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return StatusUnsafe, errMigrationUnsafe
		}
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := canonical.revalidate(); err != nil ||
		targetParent.revalidate() != nil ||
		revalidateSourceSnapshot(legacy, spec, current) != nil ||
		verifyPromotedSnapshot(targetParent, targetName, stage.snapshot, spec) != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return StatusUnsafe, errMigrationUnsafe
		}
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := syncPromotionParents(ctx, promotion); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return StatusUnsafe, errMigrationUnsafe
		}
		return StatusUnsafe, errMigrationUnsafe
	}
	if err := canonical.revalidate(); err != nil ||
		targetParent.revalidate() != nil ||
		revalidateSourceSnapshot(legacy, spec, current) != nil ||
		verifyPromotedSnapshot(targetParent, targetName, stage.snapshot, spec) != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return StatusUnsafe, errMigrationUnsafe
		}
		return StatusUnsafe, errMigrationUnsafe
	}
	return StatusImported, nil
}

func createMigrationStage(
	migration *pinnedRelativeDirectory,
	spec ArtifactSpec,
) (*migrationStage, error) {
	prefix := spec.Owner + "-" + spec.Scope + ".stage-"
	for attempts := 0; attempts < 16; attempts++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, errMigrationUnsafe
		}
		name := prefix + hex.EncodeToString(token[:])
		if err := migration.root.Mkdir(name, 0o700); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return nil, errMigrationUnsafe
		}
		if err := syncRootDirectory(migration.root); err != nil {
			return nil, errMigrationUnsafe
		}
		directory, exists, err := openRelativeDirectory(
			migration,
			name,
			false,
			canonicalDirectoryMode,
		)
		if err != nil || !exists {
			return nil, errMigrationUnsafe
		}
		return &migrationStage{name: name, directory: directory}, nil
	}
	return nil, errMigrationUnsafe
}

func captureStagedSnapshot(
	stage *pinnedRelativeDirectory,
	spec ArtifactSpec,
) (*artifactSnapshot, error) {
	if err := stage.revalidate(); err != nil {
		return nil, errMigrationUnsafe
	}
	switch spec.Kind {
	case RegularFile:
		entries, err := fs.ReadDir(stage.root.FS(), ".")
		name := filepath.FromSlash(path.Base(spec.TargetRel))
		if err != nil || len(entries) != 1 || entries[0].Name() != name {
			return nil, errMigrationUnsafe
		}
		entry, err := captureRegularFile(
			stage.root,
			name,
			".",
			spec.MaxBytes,
			canonicalRegularMode,
		)
		if err != nil || spec.MaxFiles < 1 {
			return nil, errMigrationUnsafe
		}
		return newArtifactSnapshot(RegularFile, []snapshotEntry{entry}), nil
	case DirectoryTree:
		return captureDirectorySnapshot(
			stage,
			spec.MaxFiles,
			spec.MaxBytes,
			canonicalDirectoryMode,
			canonicalRegularMode,
		)
	default:
		return nil, errMigrationUnsafe
	}
}

func syncStagedSnapshot(
	ctx context.Context,
	stage *migrationStage,
	spec ArtifactSpec,
) error {
	for _, relative := range stage.snapshot.order {
		entry := stage.snapshot.entries[relative]
		if !entry.info.Mode().IsRegular() {
			continue
		}
		file, err := stage.directory.root.OpenFile(entry.storageRelative, os.O_RDWR, 0)
		if err != nil {
			return errMigrationUnsafe
		}
		opened, statErr := file.Stat()
		linkErr := validateSingleLink(file)
		syncErr := file.Sync()
		closeErr := file.Close()
		current, pathErr := stage.directory.root.Lstat(entry.storageRelative)
		if statErr != nil || linkErr != nil || syncErr != nil || closeErr != nil || pathErr != nil ||
			!canonicalRegularMode(opened) || !canonicalRegularMode(current) ||
			!os.SameFile(entry.info, opened) || !os.SameFile(entry.info, current) {
			return errMigrationUnsafe
		}
		if err := fireImporterFailureHook(ctx, failureAfterStagedWrite); err != nil {
			return errMigrationUnsafe
		}
	}
	directories := make([]snapshotEntry, 0)
	for _, relative := range stage.snapshot.order {
		entry := stage.snapshot.entries[relative]
		if entry.info.IsDir() {
			directories = append(directories, entry)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i].storageRelative, string(filepath.Separator)) >
			strings.Count(directories[j].storageRelative, string(filepath.Separator))
	})
	for _, entry := range directories {
		directory, err := stage.directory.root.Open(entry.storageRelative)
		if err != nil {
			return errMigrationUnsafe
		}
		opened, statErr := directory.Stat()
		syncErr := syncDirectoryFile(directory)
		closeErr := directory.Close()
		current, pathErr := stage.directory.root.Lstat(entry.storageRelative)
		if statErr != nil || syncErr != nil || closeErr != nil || pathErr != nil ||
			!canonicalDirectoryMode(opened) || !canonicalDirectoryMode(current) ||
			!os.SameFile(entry.info, opened) || !os.SameFile(entry.info, current) {
			return errMigrationUnsafe
		}
	}
	if spec.Kind == RegularFile {
		if err := syncRootDirectory(stage.directory.root); err != nil {
			return errMigrationUnsafe
		}
	}
	current, err := captureStagedSnapshot(stage.directory, spec)
	if err != nil || compareSnapshots(stage.snapshot, current) != nil {
		return errMigrationUnsafe
	}
	if err := stage.directory.revalidate(); err != nil {
		return errMigrationUnsafe
	}
	if err := fireImporterFailureHook(ctx, failureAfterStageSync); err != nil {
		return errMigrationUnsafe
	}
	return nil
}

func preparePromotion(
	migration *pinnedRelativeDirectory,
	stage *migrationStage,
	targetParent *pinnedRelativeDirectory,
	targetName string,
	spec ArtifactSpec,
) (*stagedPromotion, error) {
	promotion := &stagedPromotion{
		targetParent: targetParent,
		targetName:   targetName,
	}
	switch spec.Kind {
	case RegularFile:
		entry, ok := stage.snapshot.entries["."]
		if !ok {
			return nil, errMigrationUnsafe
		}
		promotion.sourceRoot = stage.directory.root
		promotion.sourceDirInfo = stage.directory.info
		promotion.sourceName = entry.storageRelative
		promotion.artifactInfo = entry.info
	case DirectoryTree:
		entry, ok := stage.snapshot.entries["."]
		if !ok || !entry.info.IsDir() {
			return nil, errMigrationUnsafe
		}
		promotion.sourceRoot = migration.root
		promotion.sourceDirInfo = migration.info
		promotion.sourceName = stage.name
		promotion.artifactInfo = stage.directory.info
	default:
		return nil, errMigrationUnsafe
	}
	return promotion, nil
}

func applyPromotion(promotion *stagedPromotion) (bool, error) {
	sourceDirectory, err := openPinnedDirectoryFile(
		promotion.sourceRoot,
		promotion.sourceDirInfo,
	)
	if err != nil {
		return false, errMigrationUnsafe
	}
	defer sourceDirectory.Close() //nolint:errcheck
	targetDirectory, err := openPinnedDirectoryFile(
		promotion.targetParent.root,
		promotion.targetParent.info,
	)
	if err != nil {
		return false, errMigrationUnsafe
	}
	defer targetDirectory.Close() //nolint:errcheck
	before, err := promotion.sourceRoot.Lstat(promotion.sourceName)
	if err != nil || !os.SameFile(promotion.artifactInfo, before) {
		return false, errMigrationUnsafe
	}
	if err := renameNoReplace(
		sourceDirectory,
		promotion.sourceName,
		targetDirectory,
		promotion.targetName,
	); err != nil {
		collision, collisionErr := targetExists(promotion.targetParent, promotion.targetName)
		if collisionErr == nil && collision {
			return true, nil
		}
		return false, errMigrationUnsafe
	}
	current, err := promotion.targetParent.root.Lstat(promotion.targetName)
	if err != nil || !os.SameFile(promotion.artifactInfo, current) {
		_ = rollbackPromotion(promotion)
		return false, errMigrationUnsafe
	}
	return false, nil
}

func syncPromotionParents(ctx context.Context, promotion *stagedPromotion) error {
	// Persist the destination entry before the source removal. If power is lost
	// between the two fsyncs, retaining both names is safer than retaining
	// neither; restart cleanup is owner-scoped and collision remains canonical.
	if err := syncRootDirectory(promotion.targetParent.root); err != nil {
		return errMigrationUnsafe
	}
	if err := fireImporterFailureHook(ctx, failureAfterPromotionTargetSync); err != nil {
		return errMigrationUnsafe
	}
	if err := syncRootDirectory(promotion.sourceRoot); err != nil {
		return errMigrationUnsafe
	}
	if err := fireImporterFailureHook(ctx, failureAfterPromotionSourceSync); err != nil {
		return errMigrationUnsafe
	}
	return nil
}

func rollbackPromotion(promotion *stagedPromotion) error {
	targetCurrent, err := promotion.targetParent.root.Lstat(promotion.targetName)
	if err != nil || !os.SameFile(promotion.artifactInfo, targetCurrent) {
		return errMigrationUnsafe
	}
	if _, err := promotion.sourceRoot.Lstat(promotion.sourceName); !os.IsNotExist(err) {
		return errMigrationUnsafe
	}
	targetDirectory, err := openPinnedDirectoryFile(
		promotion.targetParent.root,
		promotion.targetParent.info,
	)
	if err != nil {
		return errMigrationUnsafe
	}
	defer targetDirectory.Close() //nolint:errcheck
	sourceDirectory, err := openPinnedDirectoryFile(
		promotion.sourceRoot,
		promotion.sourceDirInfo,
	)
	if err != nil {
		return errMigrationUnsafe
	}
	defer sourceDirectory.Close() //nolint:errcheck
	if err := renameNoReplace(
		targetDirectory,
		promotion.targetName,
		sourceDirectory,
		promotion.sourceName,
	); err != nil {
		return errMigrationUnsafe
	}
	// The rollback destination is the original stage. Persist its reappearance
	// before persisting removal of the canonical name.
	if err := syncDirectoryFile(sourceDirectory); err != nil {
		return errMigrationUnsafe
	}
	if err := syncDirectoryFile(targetDirectory); err != nil {
		return errMigrationUnsafe
	}
	if _, err := promotion.targetParent.root.Lstat(promotion.targetName); !os.IsNotExist(err) {
		return errMigrationUnsafe
	}
	return nil
}

func verifyPromotedSnapshot(
	targetParent *pinnedRelativeDirectory,
	targetName string,
	expected *artifactSnapshot,
	spec ArtifactSpec,
) error {
	var current *artifactSnapshot
	switch spec.Kind {
	case RegularFile:
		entry, err := captureRegularFile(
			targetParent.root,
			targetName,
			".",
			spec.MaxBytes,
			canonicalRegularMode,
		)
		if err != nil {
			return errMigrationUnsafe
		}
		current = newArtifactSnapshot(RegularFile, []snapshotEntry{entry})
	case DirectoryTree:
		tree, exists, err := openRelativeDirectory(
			targetParent,
			targetName,
			false,
			canonicalDirectoryMode,
		)
		if err != nil || !exists {
			return errMigrationUnsafe
		}
		defer tree.Close() //nolint:errcheck
		current, err = captureDirectorySnapshot(
			tree,
			spec.MaxFiles,
			spec.MaxBytes,
			canonicalDirectoryMode,
			canonicalRegularMode,
		)
		if err != nil {
			return errMigrationUnsafe
		}
	default:
		return errMigrationUnsafe
	}
	return compareSnapshots(expected, current)
}

func targetExists(parent *pinnedRelativeDirectory, name string) (bool, error) {
	if !safeNativeSegment(name) || parent.revalidate() != nil {
		return false, errMigrationUnsafe
	}
	_, err := parent.root.Lstat(name)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, errMigrationUnsafe
}

func openPinnedDirectoryFile(root *os.Root, expected os.FileInfo) (*os.File, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, errMigrationUnsafe
	}
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(expected, opened) {
		_ = directory.Close()
		return nil, errMigrationUnsafe
	}
	return directory, nil
}

func cleanupOwnedStages(
	migration *pinnedRelativeDirectory,
	spec ArtifactSpec,
) error {
	prefix := spec.Owner + "-" + spec.Scope + ".stage-"
	entries, err := fs.ReadDir(migration.root.FS(), ".")
	if err != nil {
		return errMigrationUnsafe
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if !safeNativeSegment(entry.Name()) {
			return errMigrationUnsafe
		}
		info, err := migration.root.Lstat(entry.Name())
		if err != nil || !canonicalDirectoryMode(info) {
			return errMigrationUnsafe
		}
		opened, err := migration.root.OpenRoot(entry.Name())
		if err != nil {
			return errMigrationUnsafe
		}
		openedInfo, statErr := opened.Stat(".")
		closeErr := opened.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(info, openedInfo) {
			return errMigrationUnsafe
		}
		if err := migration.root.RemoveAll(entry.Name()); err != nil {
			return errMigrationUnsafe
		}
	}
	if err := syncRootDirectory(migration.root); err != nil {
		return errMigrationUnsafe
	}
	return migration.revalidate()
}

func cleanupMigrationStage(
	migration *pinnedRelativeDirectory,
	stage *migrationStage,
) error {
	if stage == nil {
		return nil
	}
	if stage.directory != nil {
		_ = stage.directory.Close()
	}
	info, err := migration.root.Lstat(stage.name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !canonicalDirectoryMode(info) ||
		(stage.directory != nil && !os.SameFile(stage.directory.info, info)) {
		return errMigrationUnsafe
	}
	if err := migration.root.RemoveAll(stage.name); err != nil {
		return errMigrationUnsafe
	}
	return syncRootDirectory(migration.root)
}
