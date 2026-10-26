//go:build darwin || linux

package filehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

func restoreFilesystemSupported() bool { return true }

// restorePathFilesystem runs under the journal lock, after a durable intent and
// fresh engine authorization. It never replaces an occupied destination: the
// original is preserved under its intent-bound backup name before installation.
func (j *RevisionJournal) restorePathFilesystem(p RestorePath, direction string) (string, error) {
	if err := j.checkRootIdentity(); err != nil {
		return "", err
	}
	if !validStoredPath(p.Path) || !validStoredPath(p.BackupPath) || !validStoredPath(p.TempPath) || filepath.Dir(p.Path) != filepath.Dir(p.BackupPath) || filepath.Dir(p.Path) != filepath.Dir(p.TempPath) {
		return "", fmt.Errorf("invalid restore paths")
	}
	expected, desired := p.Original, p.Target
	backup, temp := filepath.Base(p.BackupPath), filepath.Base(p.TempPath)
	if direction == "rollback" {
		expected, desired = desired, expected
		backup, temp = backup+".rollback", temp+".rollback"
	} else if direction != "restore" {
		return "", fmt.Errorf("invalid restore direction")
	}
	for _, state := range []RevisionFileState{expected, desired} {
		if !state.Captured || !validState(state) || (state.Exists && state.Mode&0o400 == 0) {
			return "", fmt.Errorf("unsupported restore file state")
		}
	}
	parent, err := j.openRestoreParent(p.Path)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	fd, name := int(parent.Fd()), filepath.Base(p.Path)
	current, err := readRestoreStateAt(fd, name)
	if err != nil {
		return "", err
	}
	if sameState(current, desired) {
		if err := j.revalidateRestoreParent(parent, p.Path); err != nil {
			return "", err
		}
		return "already_applied", nil
	}
	if !sameState(current, expected) {
		// A crash can leave the original safely backed up and its destination
		// absent. Only that exact known state permits continuing installation.
		if !restoreGapRecoverable(fd, p, direction, current, expected, desired, backup) {
			return "", fmt.Errorf("restore file state conflict")
		}
	}
	if desired.Exists {
		if err := j.prepareRestoreTemp(fd, temp, desired); err != nil {
			return "", err
		}
	}
	if err := j.revalidateRestoreParent(parent, p.Path); err != nil {
		return "", err
	}
	if current.Exists {
		// Rename without replacement preserves a concurrently substituted file
		// rather than overwriting it. Verify the moved bytes before proceeding.
		if err := restoreRenameNoReplace(fd, name, backup); err != nil {
			return "", fmt.Errorf("preserve restore backup: %w", err)
		}
		if err := unix.Fsync(fd); err != nil {
			return "", fmt.Errorf("sync restore backup: %w", err)
		}
		moved, err := readRestoreStateAt(fd, backup)
		if err != nil || !sameState(moved, expected) {
			return "", fmt.Errorf("restore source changed; backup preserved")
		}
	}
	if err := j.revalidateRestoreParent(parent, p.Path); err != nil {
		return "", err
	}
	if j.restoreBeforeInstall != nil {
		j.restoreBeforeInstall()
	}
	if err := j.revalidateRestoreParent(parent, p.Path); err != nil {
		return "", err
	}
	if desired.Exists {
		if err := restoreRenameNoReplace(fd, temp, name); err != nil {
			return "", fmt.Errorf("install restore without replacement: %w", err)
		}
	}
	if err := unix.Fsync(fd); err != nil {
		return "", fmt.Errorf("sync restored path: %w", err)
	}
	after, err := readRestoreStateAt(fd, name)
	if err != nil || !sameState(after, desired) {
		return "", fmt.Errorf("restore result is uncertain; backup preserved")
	}
	if err := j.revalidateRestoreParent(parent, p.Path); err != nil {
		return "", err
	}
	return "applied", nil
}

func restoreGapRecoverable(fd int, p RestorePath, direction string, current, expected, desired RevisionFileState, backup string) bool {
	if current.Exists || p.Status == "applied" || p.Status == "already_applied" || p.Status == "rolled_back" {
		return false
	}
	prior, err := readRestoreStateAt(fd, backup)
	if err == nil && expected.Exists && sameState(prior, expected) {
		return true
	}
	if direction == "rollback" {
		// Rollback can recover the gap left by an interrupted forward rename.
		// A completed path cannot use this exception for a later external delete.
		prior, err = readRestoreStateAt(fd, filepath.Base(p.BackupPath))
		return err == nil && desired.Exists && sameState(prior, desired)
	}
	return false
}

// preflightRestorePath is read-only and uses the same anchored capture as apply.
func (j *RevisionJournal) preflightRestorePath(p RestorePath, direction string, fresh bool) error {
	if err := j.checkRootIdentity(); err != nil {
		return err
	}
	if !validStoredPath(p.Path) || !validStoredPath(p.BackupPath) || !validStoredPath(p.TempPath) || filepath.Dir(p.Path) != filepath.Dir(p.BackupPath) || filepath.Dir(p.Path) != filepath.Dir(p.TempPath) {
		return fmt.Errorf("invalid restore paths")
	}
	parent, err := j.openRestoreParent(p.Path)
	if err != nil {
		return err
	}
	defer parent.Close()
	fd := int(parent.Fd())
	expected, desired := p.Original, p.Target
	backup := filepath.Base(p.BackupPath)
	if direction == "rollback" {
		expected, desired = desired, expected
		backup += ".rollback"
	} else if direction != "restore" {
		return fmt.Errorf("invalid restore direction")
	}
	for _, state := range []RevisionFileState{expected, desired} {
		if !state.Captured || !validState(state) || state.Exists && state.Mode&0o400 == 0 {
			return fmt.Errorf("unsupported restore file state")
		}
	}
	current, err := readRestoreStateAt(fd, filepath.Base(p.Path))
	if err != nil {
		return err
	}
	if !sameState(current, expected) && !sameState(current, desired) && !restoreGapRecoverable(fd, p, direction, current, expected, desired, backup) {
		return fmt.Errorf("restore file state conflict")
	}
	if fresh {
		for _, name := range []string{filepath.Base(p.BackupPath), filepath.Base(p.TempPath), filepath.Base(p.BackupPath) + ".rollback", filepath.Base(p.TempPath) + ".rollback"} {
			state, err := readRestoreStateAt(fd, name)
			if err != nil || state.Exists {
				return fmt.Errorf("restore temporary or backup path is occupied")
			}
		}
	}
	return j.revalidateRestoreParent(parent, p.Path)
}

func (j *RevisionJournal) openRestoreParent(path string) (*os.File, error) {
	if err := j.noSymlinkComponents(path); err != nil {
		return nil, err
	}
	root, err := j.root.Open(".")
	if err != nil {
		return nil, err
	}
	fd, err := unix.Dup(int(root.Fd()))
	_ = root.Close()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	for _, part := range strings.Split(filepath.Dir(path), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("open non-link restore parent: %w", openErr)
		}
		fd = next
	}
	parent := os.NewFile(uintptr(fd), "restore-parent")
	if err := j.revalidateRestoreParent(parent, path); err != nil {
		_ = parent.Close()
		return nil, err
	}
	return parent, nil
}

func (j *RevisionJournal) revalidateRestoreParent(parent *os.File, path string) error {
	if err := j.checkRootIdentity(); err != nil {
		return err
	}
	if err := j.noSymlinkComponents(path); err != nil {
		return err
	}
	current, err := j.root.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	opened, err := parent.Stat()
	if err != nil || !os.SameFile(opened, current) {
		return fmt.Errorf("restore parent changed")
	}
	return nil
}

func readRestoreStateAt(fd int, name string) (RevisionFileState, error) {
	return readRestoreStateAtWithSync(fd, name, nil)
}

func readRestoreStateAtWithSync(fd int, name string, syncFile func(*os.File) error) (RevisionFileState, error) {
	var initial unix.Stat_t
	err := unix.Fstatat(fd, name, &initial, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return RevisionFileState{Captured: true}, nil
	}
	if err != nil {
		return RevisionFileState{}, err
	}
	if initial.Mode&unix.S_IFMT != unix.S_IFREG || initial.Nlink != 1 || initial.Mode&0o7000 != 0 || initial.Size > maxRevisionBytes {
		return RevisionFileState{}, fmt.Errorf("restore requires a bounded regular file without links or special mode")
	}
	opened, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return RevisionFileState{}, err
	}
	f := os.NewFile(uintptr(opened), "restore-file")
	var before, after, named unix.Stat_t
	statErr := unix.Fstat(opened, &before)
	info, infoErr := f.Stat()
	if statErr != nil || infoErr != nil || !sameRestoreStat(initial, before) {
		_ = f.Close()
		return RevisionFileState{}, fmt.Errorf("restore file changed while opening")
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxRevisionBytes+1))
	endInfo, endErr := f.Stat()
	statErr = unix.Fstat(opened, &after)
	var syncErr error
	if syncFile != nil {
		syncErr = syncFile(f)
	}
	closeErr := f.Close()
	nameErr := unix.Fstatat(fd, name, &named, unix.AT_SYMLINK_NOFOLLOW)
	if readErr != nil || statErr != nil || syncErr != nil || closeErr != nil || nameErr != nil || endErr != nil || !sameRestoreStat(before, after) || !sameRestoreStat(after, named) || !info.ModTime().Equal(endInfo.ModTime()) || len(data) > maxRevisionBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return RevisionFileState{}, fmt.Errorf("restore file is unsupported or changed during capture")
	}
	digest := sha256.Sum256(data)
	return RevisionFileState{Captured: true, Exists: true, Mode: uint32(info.Mode().Perm()), Data: data, Digest: hex.EncodeToString(digest[:])}, nil
}

func sameRestoreStat(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Nlink == b.Nlink && a.Size == b.Size
}

func (j *RevisionJournal) prepareRestoreTemp(fd int, name string, desired RevisionFileState) error {
	opened, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		current, captureErr := readRestoreStateAtWithSync(fd, name, j.syncRestoreFile)
		if captureErr == nil && sameState(current, desired) {
			return unix.Fsync(fd)
		}
		return fmt.Errorf("restore temporary path is occupied")
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(opened), "restore-temp")
	n, writeErr := f.Write(desired.Data)
	modeErr := f.Chmod(os.FileMode(desired.Mode))
	syncErr := j.syncRestoreFile(f)
	closeErr := f.Close()
	if writeErr != nil || n != len(desired.Data) || modeErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("prepare restore temporary file failed")
	}
	return unix.Fsync(fd)
}

func (j *RevisionJournal) syncRestoreFile(file *os.File) error {
	if j.restoreSyncFile != nil {
		return j.restoreSyncFile(file)
	}
	return file.Sync()
}

func (j *RevisionJournal) captureRestorePath(path string) (RevisionFileState, error) {
	if !validStoredPath(path) {
		return RevisionFileState{}, fmt.Errorf("invalid restore path")
	}
	parent, err := j.openRestoreParent(path)
	if err != nil {
		return RevisionFileState{}, err
	}
	defer parent.Close()
	state, err := readRestoreStateAt(int(parent.Fd()), filepath.Base(path))
	if err != nil {
		return RevisionFileState{}, err
	}
	if err := j.revalidateRestoreParent(parent, path); err != nil {
		return RevisionFileState{}, err
	}
	return state, nil
}

// cleanupRestoreSiblings removes only intent-bound sibling artifacts whose
// complete captured state still matches. Unknown entries are retained.
func (j *RevisionJournal) cleanupRestoreSiblings(op RestoreOperation) error {
	for _, path := range op.Paths {
		parent, err := j.openRestoreParent(path.Path)
		if err != nil {
			return err
		}
		fd := int(parent.Fd())
		candidates := []struct {
			name  string
			state RevisionFileState
		}{
			{filepath.Base(path.BackupPath), path.Original},
			{filepath.Base(path.TempPath), path.Target},
			{filepath.Base(path.BackupPath) + ".rollback", path.Target},
			{filepath.Base(path.TempPath) + ".rollback", path.Original},
		}
		for _, candidate := range candidates {
			state, err := readRestoreStateAt(fd, candidate.name)
			if err != nil {
				parent.Close()
				return err
			}
			if !state.Exists {
				continue
			}
			if !sameState(state, candidate.state) {
				parent.Close()
				return fmt.Errorf("restore cleanup conflict; sibling preserved")
			}
			if err := j.revalidateRestoreParent(parent, path.Path); err != nil {
				parent.Close()
				return err
			}
			if err := unix.Unlinkat(fd, candidate.name, 0); err != nil {
				parent.Close()
				return err
			}
		}
		err = unix.Fsync(fd)
		parent.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (j *RevisionJournal) restoreSiblingsAbsent(op RestoreOperation) error {
	for _, path := range op.Paths {
		parent, err := j.openRestoreParent(path.Path)
		if err != nil {
			return err
		}
		fd := int(parent.Fd())
		for _, name := range []string{filepath.Base(path.BackupPath), filepath.Base(path.TempPath), filepath.Base(path.BackupPath) + ".rollback", filepath.Base(path.TempPath) + ".rollback"} {
			state, err := readRestoreStateAt(fd, name)
			if err != nil || state.Exists {
				parent.Close()
				return fmt.Errorf("restore sibling is still present")
			}
		}
		err = j.revalidateRestoreParent(parent, path.Path)
		parent.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
