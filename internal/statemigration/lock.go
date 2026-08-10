package statemigration

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"time"
)

const (
	migrationLockTimeout = 2 * time.Second
	migrationLockPoll    = 5 * time.Millisecond
)

func acquireMigrationLock(
	ctx context.Context,
	migration *pinnedRelativeDirectory,
	name string,
) (func(), error) {
	if !safeNativeSegment(name) {
		return nil, errMigrationUnsafe
	}
	deadline := time.Now().Add(migrationLockTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, errMigrationUnsafe
		}
		if err := migration.revalidate(); err != nil {
			return nil, errMigrationUnsafe
		}
		file, info, err := openOrCreateMigrationLock(migration.root, name)
		if err != nil {
			return nil, errMigrationUnsafe
		}
		locked, err := tryLockMigration(file)
		if err != nil {
			_ = file.Close()
			return nil, errMigrationUnsafe
		}
		if locked {
			current, statErr := migration.root.Lstat(name)
			opened, openErr := file.Stat()
			if statErr != nil || openErr != nil ||
				!canonicalRegularMode(current) || !canonicalRegularMode(opened) ||
				!os.SameFile(info, current) || !os.SameFile(info, opened) ||
				validateSingleLink(file) != nil || migration.revalidate() != nil {
				_ = unlockMigration(file)
				_ = file.Close()
				return nil, errMigrationUnsafe
			}
			return func() {
				_ = unlockMigration(file)
				_ = file.Close()
			}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			return nil, errMigrationUnsafe
		}
		timer := time.NewTimer(migrationLockPoll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, errMigrationUnsafe
		case <-timer.C:
		}
	}
}

func openOrCreateMigrationLock(
	root *os.Root,
	name string,
) (*os.File, os.FileInfo, error) {
	for attempts := 0; attempts < 4; attempts++ {
		info, err := root.Lstat(name)
		switch {
		case os.IsNotExist(err):
			file, createErr := root.OpenFile(
				name,
				os.O_CREATE|os.O_EXCL|os.O_RDWR,
				0o600,
			)
			if errors.Is(createErr, fs.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, nil, errMigrationUnsafe
			}
			if chmodErr := file.Chmod(0o600); chmodErr != nil {
				_ = file.Close()
				return nil, nil, errMigrationUnsafe
			}
			opened, statErr := file.Stat()
			current, pathErr := root.Lstat(name)
			if statErr != nil || pathErr != nil ||
				!canonicalRegularMode(opened) || !canonicalRegularMode(current) ||
				!os.SameFile(opened, current) || validateSingleLink(file) != nil {
				_ = file.Close()
				return nil, nil, errMigrationUnsafe
			}
			return file, opened, nil
		case err != nil:
			return nil, nil, errMigrationUnsafe
		case !canonicalRegularMode(info):
			return nil, nil, errMigrationUnsafe
		default:
			file, openErr := root.OpenFile(name, os.O_RDWR, 0)
			if openErr != nil {
				return nil, nil, errMigrationUnsafe
			}
			opened, statErr := file.Stat()
			current, pathErr := root.Lstat(name)
			if statErr != nil || pathErr != nil ||
				!canonicalRegularMode(opened) || !canonicalRegularMode(current) ||
				!os.SameFile(info, opened) || !os.SameFile(info, current) ||
				validateSingleLink(file) != nil {
				_ = file.Close()
				return nil, nil, errMigrationUnsafe
			}
			return file, info, nil
		}
	}
	return nil, nil, errMigrationUnsafe
}
