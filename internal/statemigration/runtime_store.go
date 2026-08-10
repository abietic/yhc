package statemigration

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const canonicalRegularModeValue = 0o600

// CanonicalStore pins one private canonical state root and an exact directory
// below it. Rooted callers must Revalidate after their complete operation and
// must not retain Root beyond Close.
type CanonicalStore struct {
	base      *pinnedDirectory
	directory *pinnedRelativeDirectory
}

// OpenCanonicalStore opens or securely creates a 0700 canonical state root
// and one exact 0700 relative directory. It rejects symlinked, replaced, or
// non-private components. The boolean is false only when create is false and
// the root or relative directory is absent.
func OpenCanonicalStore(
	rootPath string,
	relative string,
	create bool,
) (*CanonicalStore, bool, error) {
	var (
		base   *pinnedDirectory
		exists bool
		err    error
	)
	if create {
		base, err = ensureCanonicalDirectory(rootPath)
		exists = err == nil
	} else {
		base, exists, err = openPinnedDirectory(rootPath, canonicalDirectoryMode)
	}
	if err != nil || !exists {
		return nil, false, err
	}
	directory, directoryExists, err := openRelativeDirectory(
		base,
		relative,
		create,
		canonicalDirectoryMode,
	)
	if err != nil || !directoryExists {
		_ = base.Close()
		return nil, false, err
	}
	store := &CanonicalStore{base: base, directory: directory}
	if err := store.Revalidate(); err != nil {
		_ = store.Close()
		return nil, false, err
	}
	return store, true, nil
}

// Root returns the pinned directory handle. Names passed to it remain inside
// the pinned directory even if the pathname is replaced concurrently.
func (store *CanonicalStore) Root() *os.Root {
	if store == nil || store.directory == nil {
		return nil
	}
	return store.directory.root
}

// Revalidate proves that the pinned root and every relative directory are
// still reachable through their original canonical path and identity.
func (store *CanonicalStore) Revalidate() error {
	if store == nil || store.base == nil || store.directory == nil {
		return errMigrationUnsafe
	}
	return store.directory.revalidate()
}

// Sync durably flushes the pinned directory metadata where supported.
func (store *CanonicalStore) Sync() error {
	if err := store.Revalidate(); err != nil {
		return err
	}
	return syncRootDirectory(store.directory.root)
}

// Lock serializes one caller-owned canonical migration action in this pinned
// directory. The caller must release the returned function.
func (store *CanonicalStore) Lock(ctx context.Context, name string) (func(), error) {
	if store == nil || store.directory == nil || !safeNativeSegment(name) || store.Revalidate() != nil {
		return nil, errMigrationUnsafe
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return acquireMigrationLock(ctx, store.directory, name)
}

// PromoteRegularFromNoReplace moves one expected 0600 regular file between
// pinned stores without overwriting a target. Once rename succeeds this method
// deliberately does not roll back: the engine/session external journal owns
// recovery from an interrupted promotion.
func (store *CanonicalStore) PromoteRegularFromNoReplace(
	source *CanonicalStore,
	from string,
	to string,
	expected os.FileInfo,
) (collision bool, err error) {
	if store == nil || source == nil || !safeNativeSegment(from) || !safeNativeSegment(to) ||
		expected == nil || !canonicalRegularMode(expected) ||
		store.Revalidate() != nil || source.Revalidate() != nil {
		return false, errMigrationUnsafe
	}
	if _, targetErr := store.Root().Lstat(filepath.FromSlash(to)); targetErr == nil {
		return true, nil
	} else if !errors.Is(targetErr, fs.ErrNotExist) {
		return false, errMigrationUnsafe
	}
	file, opened, exists, openErr := source.OpenRegular(from, os.O_RDONLY, false)
	if openErr != nil || !exists || !os.SameFile(expected, opened) {
		if file != nil {
			_ = file.Close()
		}
		return false, errMigrationUnsafe
	}
	if closeErr := file.Close(); closeErr != nil {
		return false, errMigrationUnsafe
	}
	sourceDirectory, sourceErr := source.Root().Open(".")
	targetDirectory, targetErr := store.Root().Open(".")
	if sourceErr != nil || targetErr != nil {
		if sourceDirectory != nil {
			_ = sourceDirectory.Close()
		}
		if targetDirectory != nil {
			_ = targetDirectory.Close()
		}
		return false, errMigrationUnsafe
	}
	renameErr := renameNoReplace(sourceDirectory, filepath.FromSlash(from), targetDirectory, filepath.FromSlash(to))
	closeErr := errors.Join(sourceDirectory.Close(), targetDirectory.Close())
	if renameErr != nil {
		if errors.Is(renameErr, fs.ErrExist) {
			return true, nil
		}
		return false, errMigrationUnsafe
	}
	if closeErr != nil || store.Sync() != nil || source.Sync() != nil {
		return false, errMigrationUnsafe
	}
	target, targetErr := store.Root().Lstat(filepath.FromSlash(to))
	_, sourceErr = source.Root().Lstat(filepath.FromSlash(from))
	if targetErr != nil || !canonicalRegularMode(target) || !os.SameFile(expected, target) ||
		!errors.Is(sourceErr, fs.ErrNotExist) || store.Revalidate() != nil || source.Revalidate() != nil {
		return false, errMigrationUnsafe
	}
	return false, nil
}

// OpenRegular opens one exact 0600 single-link file under the pinned store.
// When create is true, an absent file is created with O_EXCL; an existing file
// is never replaced or followed through a symlink.
func (store *CanonicalStore) OpenRegular(
	name string,
	flag int,
	create bool,
) (*os.File, os.FileInfo, bool, error) {
	if store == nil || !safeNativeSegment(name) ||
		flag&(os.O_CREATE|os.O_EXCL|os.O_TRUNC) != 0 {
		return nil, nil, false, errMigrationUnsafe
	}
	for {
		if err := store.Revalidate(); err != nil {
			return nil, nil, false, errMigrationUnsafe
		}
		before, err := store.Root().Lstat(filepath.FromSlash(name))
		created := false
		openFlag := flag
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return nil, nil, false, nil
			}
			before = nil
			created = true
			openFlag |= os.O_CREATE | os.O_EXCL
		} else if err != nil || !canonicalRegularMode(before) {
			return nil, nil, false, errMigrationUnsafe
		}
		file, err := store.Root().OpenFile(
			filepath.FromSlash(name),
			openFlag,
			canonicalRegularModeValue,
		)
		if created && errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, nil, false, errMigrationUnsafe
		}
		opened, openErr := file.Stat()
		current, pathErr := store.Root().Lstat(filepath.FromSlash(name))
		valid := openErr == nil && pathErr == nil &&
			canonicalRegularMode(opened) && canonicalRegularMode(current) &&
			os.SameFile(opened, current) && validateSingleLink(file) == nil &&
			store.Revalidate() == nil
		if before != nil {
			valid = valid && os.SameFile(before, opened)
		}
		if !valid {
			_ = file.Close()
			if created && openErr == nil {
				store.removeRegularIfSame(name, opened)
			}
			return nil, nil, false, errMigrationUnsafe
		}
		return file, opened, true, nil
	}
}

// PromoteRegular atomically renames one opened staged file over an optional
// existing regular target, then verifies and synchronizes the pinned store.
func (store *CanonicalStore) PromoteRegular(
	from string,
	to string,
	expected os.FileInfo,
) error {
	if store == nil || !safeNativeSegment(from) || !safeNativeSegment(to) ||
		expected == nil || !canonicalRegularMode(expected) || store.Revalidate() != nil {
		return errMigrationUnsafe
	}
	if target, err := store.Root().Lstat(filepath.FromSlash(to)); err == nil {
		if !canonicalRegularMode(target) {
			return errMigrationUnsafe
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errMigrationUnsafe
	}
	if err := store.Root().Rename(filepath.FromSlash(from), filepath.FromSlash(to)); err != nil {
		return errMigrationUnsafe
	}
	current, err := store.Root().Lstat(filepath.FromSlash(to))
	if err != nil || !canonicalRegularMode(current) || !os.SameFile(expected, current) ||
		store.Sync() != nil || store.Revalidate() != nil {
		return errMigrationUnsafe
	}
	return nil
}

// ValidateRegular rechecks one exact file identity after a rooted operation.
func (store *CanonicalStore) ValidateRegular(name string, expected os.FileInfo) error {
	if store == nil || !safeNativeSegment(name) || expected == nil || store.Revalidate() != nil {
		return errMigrationUnsafe
	}
	file, opened, exists, err := store.OpenRegular(name, os.O_RDONLY, false)
	if err != nil || !exists || !os.SameFile(expected, opened) {
		if file != nil {
			_ = file.Close()
		}
		return errMigrationUnsafe
	}
	if err := file.Close(); err != nil || store.Revalidate() != nil {
		return errMigrationUnsafe
	}
	return nil
}

// RemoveRegularIfSame removes a temporary file only while it still has the
// identity created by the caller.
func (store *CanonicalStore) RemoveRegularIfSame(name string, expected os.FileInfo) {
	store.removeRegularIfSame(name, expected)
}

func (store *CanonicalStore) removeRegularIfSame(name string, expected os.FileInfo) {
	if store == nil || !safeNativeSegment(name) || expected == nil || store.Root() == nil {
		return
	}
	current, err := store.Root().Lstat(filepath.FromSlash(name))
	if err == nil && canonicalRegularMode(current) && os.SameFile(expected, current) {
		_ = store.Root().Remove(filepath.FromSlash(name))
	}
}

// Close releases every retained directory handle.
func (store *CanonicalStore) Close() error {
	if store == nil {
		return nil
	}
	directoryErr := store.directory.Close()
	baseErr := store.base.Close()
	store.directory = nil
	store.base = nil
	return errors.Join(directoryErr, baseErr)
}
