package statemigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
)

type snapshotEntry struct {
	relative        string
	storageRelative string
	info            os.FileInfo
	data            []byte
}

type artifactSnapshot struct {
	kind    Kind
	entries map[string]snapshotEntry
	order   []string
	digest  string
}

type frozenDirEntry struct {
	name string
	info os.FileInfo
}

var errArtifactAbsent = errors.New("state migration artifact is absent")

func openLegacySnapshot(
	legacyPath string,
	spec ArtifactSpec,
) (*pinnedDirectory, *artifactSnapshot, bool, error) {
	directoryMode, _, err := legacyModePolicies(spec.LegacyMode)
	if err != nil {
		return nil, nil, false, errMigrationUnsafe
	}
	legacy, exists, err := openPinnedDirectory(legacyPath, directoryMode)
	if err != nil || !exists {
		return nil, nil, !exists && err == nil, err
	}
	snapshot, err := captureSourceSnapshot(legacy, spec)
	if errors.Is(err, errArtifactAbsent) {
		_ = legacy.Close()
		return nil, nil, true, nil
	}
	if err != nil {
		_ = legacy.Close()
		return nil, nil, false, errMigrationUnsafe
	}
	if err := legacy.revalidate(); err != nil {
		_ = legacy.Close()
		return nil, nil, false, errMigrationUnsafe
	}
	return legacy, snapshot, false, nil
}

func captureSourceSnapshot(
	legacy *pinnedDirectory,
	spec ArtifactSpec,
) (*artifactSnapshot, error) {
	if err := legacy.revalidate(); err != nil {
		return nil, errMigrationUnsafe
	}
	directoryMode, regularMode, err := legacyModePolicies(spec.LegacyMode)
	if err != nil {
		return nil, errMigrationUnsafe
	}
	switch spec.Kind {
	case RegularFile:
		parent, exists, err := openRelativeDirectory(
			legacy,
			path.Dir(spec.SourceRel),
			false,
			directoryMode,
		)
		if err != nil || !exists {
			if err == nil {
				return nil, errArtifactAbsent
			}
			return nil, errMigrationUnsafe
		}
		defer parent.Close() //nolint:errcheck
		if _, err := parent.root.Lstat(filepath.FromSlash(path.Base(spec.SourceRel))); os.IsNotExist(err) {
			return nil, errArtifactAbsent
		} else if err != nil {
			return nil, errMigrationUnsafe
		}
		entry, err := captureRegularFile(
			parent.root,
			filepath.FromSlash(path.Base(spec.SourceRel)),
			".",
			spec.MaxBytes,
			regularMode,
		)
		if err != nil || spec.MaxFiles < 1 {
			return nil, errMigrationUnsafe
		}
		if err := parent.revalidate(); err != nil {
			return nil, errMigrationUnsafe
		}
		return newArtifactSnapshot(RegularFile, []snapshotEntry{entry}), nil
	case DirectoryTree:
		artifact, exists, err := openRelativeDirectory(
			legacy,
			spec.SourceRel,
			false,
			directoryMode,
		)
		if err != nil || !exists {
			if err == nil {
				return nil, errArtifactAbsent
			}
			return nil, errMigrationUnsafe
		}
		defer artifact.Close() //nolint:errcheck
		return captureDirectorySnapshot(
			artifact,
			spec.MaxFiles,
			spec.MaxBytes,
			directoryMode,
			regularMode,
		)
	default:
		return nil, errMigrationUnsafe
	}
}

func captureDirectorySnapshot(
	artifact *pinnedRelativeDirectory,
	maxFiles int,
	maxBytes int64,
	directoryMode directoryModePolicy,
	regularMode func(os.FileInfo) bool,
) (*artifactSnapshot, error) {
	if err := artifact.revalidate(); err != nil {
		return nil, errMigrationUnsafe
	}
	entries := make([]snapshotEntry, 0)
	var bytesRead int64
	entryCount := 0
	err := fs.WalkDir(artifact.root.FS(), ".", func(relative string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil || !fs.ValidPath(relative) {
			return errMigrationUnsafe
		}
		storageRelative := filepath.FromSlash(relative)
		info, err := artifact.root.Lstat(storageRelative)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errMigrationUnsafe
		}
		if relative != "." {
			entryCount++
			if entryCount > maxFiles {
				return errMigrationUnsafe
			}
		}
		switch {
		case info.IsDir():
			if !directoryMode(info) {
				return errMigrationUnsafe
			}
			opened, err := artifact.root.OpenRoot(storageRelative)
			if err != nil {
				return errMigrationUnsafe
			}
			openedInfo, statErr := opened.Stat(".")
			closeErr := opened.Close()
			if statErr != nil || closeErr != nil || !directoryMode(openedInfo) || !os.SameFile(info, openedInfo) {
				return errMigrationUnsafe
			}
			entries = append(entries, snapshotEntry{
				relative: relative, storageRelative: storageRelative, info: info,
			})
			return nil
		case info.Mode().IsRegular():
			entry, err := captureRegularFile(
				artifact.root,
				storageRelative,
				relative,
				maxBytes-bytesRead,
				regularMode,
			)
			if err != nil {
				return errMigrationUnsafe
			}
			bytesRead += int64(len(entry.data))
			entries = append(entries, entry)
			return nil
		default:
			return errMigrationUnsafe
		}
	})
	if err != nil || bytesRead > maxBytes {
		return nil, errMigrationUnsafe
	}
	if err := artifact.revalidate(); err != nil {
		return nil, errMigrationUnsafe
	}
	return newArtifactSnapshot(DirectoryTree, entries), nil
}

func captureRegularFile(
	root *os.Root,
	storageRelative string,
	snapshotRelative string,
	remainingBytes int64,
	mode func(os.FileInfo) bool,
) (snapshotEntry, error) {
	if remainingBytes < 0 {
		return snapshotEntry{}, errMigrationUnsafe
	}
	before, err := root.Lstat(storageRelative)
	if err != nil || !mode(before) || before.Size() < 0 || before.Size() > remainingBytes {
		return snapshotEntry{}, errMigrationUnsafe
	}
	file, err := root.Open(storageRelative)
	if err != nil {
		return snapshotEntry{}, errMigrationUnsafe
	}
	defer file.Close() //nolint:errcheck
	opened, err := file.Stat()
	if err != nil || !mode(opened) || !os.SameFile(before, opened) {
		return snapshotEntry{}, errMigrationUnsafe
	}
	if err := validateSingleLink(file); err != nil {
		return snapshotEntry{}, errMigrationUnsafe
	}
	if before.Size() == math.MaxInt64 {
		return snapshotEntry{}, errMigrationUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(file, before.Size()+1))
	if err != nil || int64(len(data)) != before.Size() || int64(len(data)) > remainingBytes {
		return snapshotEntry{}, errMigrationUnsafe
	}
	afterOpen, openErr := file.Stat()
	afterPath, pathErr := root.Lstat(storageRelative)
	if openErr != nil || pathErr != nil || !mode(afterOpen) || !mode(afterPath) ||
		!os.SameFile(before, afterOpen) || !os.SameFile(before, afterPath) ||
		afterOpen.Size() != before.Size() || !afterOpen.ModTime().Equal(before.ModTime()) ||
		afterPath.Size() != before.Size() || !afterPath.ModTime().Equal(before.ModTime()) {
		return snapshotEntry{}, errMigrationUnsafe
	}
	return snapshotEntry{
		relative: snapshotRelative, storageRelative: storageRelative,
		info: before, data: data,
	}, nil
}

func newArtifactSnapshot(kind Kind, entries []snapshotEntry) *artifactSnapshot {
	snapshot := &artifactSnapshot{
		kind: kind, entries: make(map[string]snapshotEntry, len(entries)),
		order: make([]string, 0, len(entries)),
	}
	for _, entry := range entries {
		snapshot.entries[entry.relative] = entry
		snapshot.order = append(snapshot.order, entry.relative)
	}
	sort.Strings(snapshot.order)
	hash := sha256.New()
	for _, relative := range snapshot.order {
		entry := snapshot.entries[relative]
		_, _ = fmt.Fprintf(
			hash,
			"%d:%s:%s:%o:%d:%d:",
			len(relative),
			relative,
			entry.storageRelative,
			entry.info.Mode(),
			entry.info.Size(),
			entry.info.ModTime().UnixNano(),
		)
		_, _ = hash.Write(entry.data)
	}
	snapshot.digest = hex.EncodeToString(hash.Sum(nil))
	return snapshot
}

func compareSnapshots(expected, current *artifactSnapshot) error {
	if expected == nil || current == nil ||
		expected.kind != current.kind || expected.digest != current.digest ||
		len(expected.entries) != len(current.entries) {
		return errMigrationUnsafe
	}
	for relative, before := range expected.entries {
		after, ok := current.entries[relative]
		if !ok || !os.SameFile(before.info, after.info) ||
			before.info.Mode() != after.info.Mode() ||
			before.info.Size() != after.info.Size() ||
			!before.info.ModTime().Equal(after.info.ModTime()) ||
			!bytes.Equal(before.data, after.data) {
			return errMigrationUnsafe
		}
	}
	return nil
}

func (snapshot *artifactSnapshot) Open(relative string) (io.ReadCloser, fs.FileInfo, error) {
	if snapshot == nil || (relative != "." && !fs.ValidPath(relative)) {
		return nil, nil, errMigrationUnsafe
	}
	entry, ok := snapshot.entries[relative]
	if !ok || !entry.info.Mode().IsRegular() {
		return nil, nil, errMigrationUnsafe
	}
	data := append([]byte(nil), entry.data...)
	return io.NopCloser(bytes.NewReader(data)), entry.info, nil
}

func (snapshot *artifactSnapshot) Walk(
	walk func(relative string, entry fs.DirEntry) error,
) error {
	if snapshot == nil || walk == nil {
		return errMigrationUnsafe
	}
	for _, relative := range snapshot.order {
		entry := snapshot.entries[relative]
		if err := walk(relative, frozenDirEntry{
			name: path.Base(relative),
			info: entry.info,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (snapshot *artifactSnapshot) Digest() string {
	if snapshot == nil {
		return ""
	}
	return snapshot.digest
}

func (entry frozenDirEntry) Name() string               { return entry.name }
func (entry frozenDirEntry) IsDir() bool                { return entry.info.IsDir() }
func (entry frozenDirEntry) Type() fs.FileMode          { return entry.info.Mode().Type() }
func (entry frozenDirEntry) Info() (fs.FileInfo, error) { return entry.info, nil }

func revalidateSourceSnapshot(
	legacy *pinnedDirectory,
	spec ArtifactSpec,
	expected *artifactSnapshot,
) error {
	if err := legacy.revalidate(); err != nil {
		return errMigrationUnsafe
	}
	current, err := captureSourceSnapshot(legacy, spec)
	if err != nil {
		return errMigrationUnsafe
	}
	return compareSnapshots(expected, current)
}
