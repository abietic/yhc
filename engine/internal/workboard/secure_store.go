package workboard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const privateTranscriptDirectoryMode os.FileMode = 0o700

type transcriptDirectoryObservation struct {
	info    os.FileInfo
	missing bool
}

func preparePrivateTranscriptDirectory(dir string) error {
	observation, err := observeTranscriptDirectory(dir)
	if err != nil {
		return err
	}
	_, err = prepareObservedPrivateTranscriptDirectory(
		dir,
		observation,
		nil,
	)
	return err
}

func preparePrivateTranscriptDirectoryWithHook(
	dir string,
	afterOpen func(),
) error {
	observation, err := observeTranscriptDirectory(dir)
	if err != nil {
		return err
	}
	_, err = prepareObservedPrivateTranscriptDirectory(
		dir,
		observation,
		afterOpen,
	)
	return err
}

func observeTranscriptDirectory(
	dir string,
) (transcriptDirectoryObservation, error) {
	if strings.TrimSpace(dir) == "" {
		return transcriptDirectoryObservation{}, fmt.Errorf(
			"workboard authority: transcript directory is empty",
		)
	}
	dir = filepath.Clean(dir)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return transcriptDirectoryObservation{missing: true}, nil
	}
	if err != nil {
		return transcriptDirectoryObservation{}, fmt.Errorf(
			"workboard authority: inspect transcript directory: %w",
			err,
		)
	}
	return transcriptDirectoryObservation{info: info}, nil
}

func prepareObservedPrivateTranscriptDirectory(
	dir string,
	observation transcriptDirectoryObservation,
	afterOpen func(),
) (os.FileInfo, error) {
	dir = filepath.Clean(dir)
	info, err := os.Lstat(dir)
	if observation.missing {
		if !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return nil, fmt.Errorf(
					"workboard authority: inspect transcript directory: %w",
					err,
				)
			}
			return nil, fmt.Errorf(
				"workboard authority: transcript directory changed while securing",
			)
		}
		if err := os.MkdirAll(
			filepath.Dir(dir),
			privateTranscriptDirectoryMode,
		); err != nil {
			return nil, fmt.Errorf(
				"workboard authority: create transcript directory parent: %w",
				err,
			)
		}
		if err := os.Mkdir(dir, privateTranscriptDirectoryMode); err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf(
					"workboard authority: transcript directory changed while securing",
				)
			}
			return nil, fmt.Errorf(
				"workboard authority: create transcript directory: %w",
				err,
			)
		}
		info, err = os.Lstat(dir)
	} else if err == nil && !os.SameFile(observation.info, info) {
		return nil, fmt.Errorf(
			"workboard authority: transcript directory changed while securing",
		)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"workboard authority: inspect transcript directory: %w",
			err,
		)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf(
			"workboard authority: transcript directory is not a directory",
		)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf(
			"workboard authority: open transcript directory: %w",
			err,
		)
	}
	defer root.Close() //nolint:errcheck
	if afterOpen != nil {
		afterOpen()
	}
	opened, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf(
			"workboard authority: inspect opened transcript directory: %w",
			err,
		)
	}
	current, err := os.Lstat(dir)
	if err != nil ||
		!os.SameFile(info, opened) ||
		!os.SameFile(current, opened) {
		return nil, fmt.Errorf(
			"workboard authority: transcript directory changed while securing",
		)
	}
	if err := root.Chmod(".", privateTranscriptDirectoryMode); err != nil {
		return nil, fmt.Errorf(
			"workboard authority: chmod transcript directory: %w",
			err,
		)
	}
	secured, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf(
			"workboard authority: inspect secured transcript directory: %w",
			err,
		)
	}
	current, err = os.Lstat(dir)
	if err != nil ||
		!os.SameFile(opened, secured) ||
		!os.SameFile(current, secured) {
		return nil, fmt.Errorf(
			"workboard authority: transcript directory changed while securing",
		)
	}
	if secured.Mode().Perm() != privateTranscriptDirectoryMode {
		return nil, fmt.Errorf(
			"workboard authority: transcript directory mode is not 0700",
		)
	}
	return secured, nil
}

type ArtifactKind string

const (
	ArtifactAuthority ArtifactKind = "authority"
	ArtifactMarker    ArtifactKind = "marker"
	ArtifactBackup    ArtifactKind = "backup"
)

type FailureStage string

const (
	FailureCreate   FailureStage = "create"
	FailureChmod    FailureStage = "chmod"
	FailureWrite    FailureStage = "write"
	FailureSync     FailureStage = "sync"
	FailureClose    FailureStage = "close"
	FailureRename   FailureStage = "rename"
	FailureDirSync  FailureStage = "dir-sync"
	FailureRollback FailureStage = "rollback"
)

type FailureHook func(ArtifactKind, FailureStage) error

// DurabilityUncertainError means a replacement was visible after rename and
// the store could not prove that compensating rollback was durable.
type DurabilityUncertainError struct {
	Kind        ArtifactKind
	Quarantined bool
	Cause       error
}

func (e *DurabilityUncertainError) Error() string {
	state := "quarantine failed"
	if e.Quarantined {
		state = "session is persistently quarantined"
	}
	return fmt.Sprintf(
		"workboard authority: %s durability is uncertain (%s): %v",
		e.Kind,
		state,
		e.Cause,
	)
}

func (e *DurabilityUncertainError) Unwrap() error {
	return e.Cause
}

func IsDurabilityUncertain(err error) bool {
	var uncertain *DurabilityUncertainError
	return errors.As(err, &uncertain)
}

// ArtifactStore provides anchored, exact-path I/O. Higher-level cutover and
// lifecycle code decides when an artifact becomes authoritative.
type ArtifactStore struct {
	dir               string
	sessionID         string
	hook              FailureHook
	directoryIdentity os.FileInfo
}

type ArtifactPaths struct {
	Authority string
	Marker    string
	Backup    string
}

func NewArtifactStore(
	dir string,
	sessionID string,
	hook FailureHook,
) (*ArtifactStore, error) {
	return newArtifactStore(dir, sessionID, hook, nil)
}

func newArtifactStore(
	dir string,
	sessionID string,
	hook FailureHook,
	directoryIdentity os.FileInfo,
) (*ArtifactStore, error) {
	sessionID = strings.TrimSpace(sessionID)
	if !validArtifactSessionID(sessionID) {
		return nil, fmt.Errorf("workboard authority: invalid SessionID")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf(
			"workboard authority: transcript directory is empty",
		)
	}
	store := &ArtifactStore{
		dir:               filepath.Clean(dir),
		sessionID:         sessionID,
		hook:              hook,
		directoryIdentity: directoryIdentity,
	}
	if err := store.validateDirectory(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ArtifactStore) Paths() ArtifactPaths {
	return ArtifactPaths{
		Authority: filepath.Join(s.dir, s.sessionID+AuthorityRecordSuffix),
		Marker:    filepath.Join(s.dir, s.sessionID+AuthorityMarkerSuffix),
		Backup:    filepath.Join(s.dir, s.sessionID+LegacyBackupSuffix),
	}
}

func (s *ArtifactStore) Path(kind ArtifactKind) (string, error) {
	paths := s.Paths()
	switch kind {
	case ArtifactAuthority:
		return paths.Authority, nil
	case ArtifactMarker:
		return paths.Marker, nil
	case ArtifactBackup:
		return paths.Backup, nil
	default:
		return "", fmt.Errorf(
			"workboard authority: unknown artifact kind %q",
			kind,
		)
	}
}

func (s *ArtifactStore) Read(kind ArtifactKind) ([]byte, error) {
	return s.readWithMode(kind, 0o600)
}

func (s *ArtifactStore) readWithMode(
	kind ArtifactKind,
	mode os.FileMode,
) ([]byte, error) {
	if err := s.validateDirectory(); err != nil {
		return nil, err
	}
	path, err := s.Path(kind)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"workboard authority: %s artifact is not a regular file",
			kind,
		)
	}
	if info.Mode().Perm() != mode {
		return nil, fmt.Errorf(
			"workboard authority: %s artifact mode is not %04o",
			kind,
			mode,
		)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf(
			"workboard authority: %s artifact changed while opening",
			kind,
		)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxEncodedJSONBytes+1))
	if err != nil {
		return nil, fmt.Errorf(
			"workboard authority: read %s artifact: %w",
			kind,
			err,
		)
	}
	if len(data) > MaxEncodedJSONBytes {
		return nil, fmt.Errorf(
			"workboard authority: encoded %s exceeds %d bytes",
			kind,
			MaxEncodedJSONBytes,
		)
	}
	return data, nil
}

func (s *ArtifactStore) Write(kind ArtifactKind, data []byte) error {
	if len(data) > MaxEncodedJSONBytes {
		return fmt.Errorf(
			"workboard authority: encoded %s exceeds %d bytes",
			kind,
			MaxEncodedJSONBytes,
		)
	}
	if err := s.validateDirectory(); err != nil {
		return err
	}
	targetPath, err := s.Path(kind)
	if err != nil {
		return err
	}
	expected, err := preflightArtifactTarget(targetPath, kind)
	if err != nil {
		return err
	}
	var previous []byte
	if expected != nil {
		previous, err = s.Read(kind)
		if err != nil {
			return fmt.Errorf(
				"workboard authority: read prior %s artifact: %w",
				kind,
				err,
			)
		}
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return fmt.Errorf("workboard authority: open transcript root: %w", err)
	}
	defer root.Close() //nolint:errcheck
	dirInfo, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf("workboard authority: revalidate directory: %w", err)
	}
	openedDirInfo, err := root.Stat(".")
	if err != nil ||
		!os.SameFile(dirInfo, openedDirInfo) ||
		(s.directoryIdentity != nil &&
			!os.SameFile(s.directoryIdentity, openedDirInfo)) {
		return fmt.Errorf(
			"workboard authority: transcript directory changed while opening",
		)
	}
	directory, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf(
			"workboard authority: open parent for %s sync: %w",
			kind,
			err,
		)
	}
	defer directory.Close() //nolint:errcheck
	syncInfo, err := directory.Stat()
	if err != nil || !os.SameFile(dirInfo, syncInfo) {
		return fmt.Errorf(
			"workboard authority: parent changed before %s replacement",
			kind,
		)
	}

	targetName := filepath.Base(targetPath)
	tempName := "." + targetName + ".tmp-" + uuid.NewString()
	if err := s.fail(kind, FailureCreate); err != nil {
		return err
	}
	file, err := root.OpenFile(
		tempName,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf(
			"workboard authority: create %s temp: %w",
			kind,
			err,
		)
	}
	cleanup := true
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if cleanup {
			_ = root.Remove(tempName)
		}
	}()
	if err := s.fail(kind, FailureChmod); err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf(
			"workboard authority: chmod %s temp: %w",
			kind,
			err,
		)
	}
	if err := s.fail(kind, FailureWrite); err != nil {
		return err
	}
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf(
			"workboard authority: write %s temp: %w",
			kind,
			err,
		)
	}
	if err := s.fail(kind, FailureSync); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf(
			"workboard authority: sync %s temp: %w",
			kind,
			err,
		)
	}
	if err := s.fail(kind, FailureClose); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		file = nil
		return fmt.Errorf(
			"workboard authority: close %s temp: %w",
			kind,
			err,
		)
	}
	file = nil
	if err := s.fail(kind, FailureRename); err != nil {
		return err
	}
	if err := revalidateArtifactTarget(targetPath, expected, kind); err != nil {
		return err
	}
	tempInfo, err := root.Stat(tempName)
	if err != nil {
		return fmt.Errorf(
			"workboard authority: inspect prepared %s artifact: %w",
			kind,
			err,
		)
	}
	if err := root.Rename(tempName, targetName); err != nil {
		return fmt.Errorf(
			"workboard authority: rename %s artifact: %w",
			kind,
			err,
		)
	}
	cleanup = false
	syncErr := s.fail(kind, FailureDirSync)
	if syncErr == nil {
		syncErr = directory.Sync()
	}
	if syncErr != nil {
		rollbackErr := s.rollbackReplacement(
			root,
			directory,
			targetName,
			tempInfo,
			expected != nil,
			previous,
			kind,
		)
		if rollbackErr != nil {
			quarantineErr := s.quarantineAfterRollbackFailure(
				root,
				targetName,
				tempInfo,
				kind,
			)
			cause := errors.Join(syncErr, rollbackErr)
			if quarantineErr != nil {
				cause = errors.Join(cause, quarantineErr)
			}
			return &DurabilityUncertainError{
				Kind:        kind,
				Quarantined: quarantineErr == nil,
				Cause:       cause,
			}
		}
		return fmt.Errorf(
			"workboard authority: sync %s parent: %w",
			kind,
			syncErr,
		)
	}
	return nil
}

func (s *ArtifactStore) quarantineAfterRollbackFailure(
	root *os.Root,
	targetName string,
	installed os.FileInfo,
	kind ArtifactKind,
) error {
	quarantineName := targetName
	quarantineInfo := installed
	quarantineKind := kind
	if kind != ArtifactMarker {
		markerPath, err := s.Path(ArtifactMarker)
		if err != nil {
			return err
		}
		markerInfo, err := preflightArtifactTarget(
			markerPath,
			ArtifactMarker,
		)
		if err != nil {
			return fmt.Errorf(
				"workboard authority: inspect marker for quarantine: %w",
				err,
			)
		}
		if markerInfo != nil {
			quarantineName = filepath.Base(markerPath)
			quarantineInfo = markerInfo
			quarantineKind = ArtifactMarker
		}
	}

	return quarantineRootFile(
		root,
		quarantineName,
		quarantineInfo,
		quarantineKind,
	)
}

func quarantineRootFile(
	root *os.Root,
	name string,
	expected os.FileInfo,
	kind ArtifactKind,
) error {
	file, err := root.Open(name)
	if err != nil {
		return fmt.Errorf(
			"workboard authority: open %s for quarantine: %w",
			kind,
			err,
		)
	}
	defer file.Close() //nolint:errcheck
	opened, err := file.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		return fmt.Errorf(
			"workboard authority: %s changed before quarantine",
			kind,
		)
	}
	if err := file.Chmod(0o400); err != nil {
		return fmt.Errorf(
			"workboard authority: quarantine %s mode: %w",
			kind,
			err,
		)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf(
			"workboard authority: sync %s quarantine: %w",
			kind,
			err,
		)
	}
	current, err := root.Stat(name)
	if err != nil ||
		!os.SameFile(expected, current) ||
		current.Mode().Perm() != 0o400 {
		return fmt.Errorf(
			"workboard authority: %s quarantine was not retained",
			kind,
		)
	}
	return nil
}

func (s *ArtifactStore) rollbackReplacement(
	root *os.Root,
	directory *os.File,
	targetName string,
	installed os.FileInfo,
	hadPrevious bool,
	previous []byte,
	kind ArtifactKind,
) error {
	if err := s.fail(kind, FailureRollback); err != nil {
		return err
	}
	targetPath := filepath.Join(s.dir, targetName)
	if err := revalidateArtifactTarget(targetPath, installed, kind); err != nil {
		return fmt.Errorf(
			"workboard authority: revalidate %s before rollback: %w",
			kind,
			err,
		)
	}
	if !hadPrevious {
		if err := root.Remove(targetName); err != nil {
			return fmt.Errorf(
				"workboard authority: remove %s during rollback: %w",
				kind,
				err,
			)
		}
		if err := directory.Sync(); err != nil {
			return fmt.Errorf(
				"workboard authority: sync removed %s rollback: %w",
				kind,
				err,
			)
		}
		return nil
	}

	tempName := "." + targetName + ".rollback-" + uuid.NewString()
	file, err := root.OpenFile(
		tempName,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf(
			"workboard authority: create %s rollback temp: %w",
			kind,
			err,
		)
	}
	cleanup := true
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if cleanup {
			_ = root.Remove(tempName)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf(
			"workboard authority: chmod %s rollback temp: %w",
			kind,
			err,
		)
	}
	if err := writeAll(file, previous); err != nil {
		return fmt.Errorf(
			"workboard authority: write %s rollback temp: %w",
			kind,
			err,
		)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf(
			"workboard authority: sync %s rollback temp: %w",
			kind,
			err,
		)
	}
	if err := file.Close(); err != nil {
		file = nil
		return fmt.Errorf(
			"workboard authority: close %s rollback temp: %w",
			kind,
			err,
		)
	}
	file = nil
	if err := revalidateArtifactTarget(targetPath, installed, kind); err != nil {
		return fmt.Errorf(
			"workboard authority: %s changed during rollback: %w",
			kind,
			err,
		)
	}
	if err := root.Rename(tempName, targetName); err != nil {
		return fmt.Errorf(
			"workboard authority: restore %s rollback: %w",
			kind,
			err,
		)
	}
	cleanup = false
	if err := directory.Sync(); err != nil {
		return fmt.Errorf(
			"workboard authority: sync restored %s rollback: %w",
			kind,
			err,
		)
	}
	return nil
}

func (s *ArtifactStore) Remove(kind ArtifactKind) (bool, error) {
	if err := s.validateDirectory(); err != nil {
		return false, err
	}
	path, err := s.Path(kind)
	if err != nil {
		return false, err
	}
	info, err := preflightArtifactTarget(path, kind)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info == nil {
		return false, nil
	}
	if err := revalidateArtifactTarget(path, info, kind); err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func (s *ArtifactStore) validateDirectory() error {
	info, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf(
			"workboard authority: inspect transcript directory: %w",
			err,
		)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf(
			"workboard authority: transcript directory is not a directory",
		)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf(
			"workboard authority: transcript directory mode is not 0700",
		)
	}
	if s.directoryIdentity != nil &&
		!os.SameFile(s.directoryIdentity, info) {
		return fmt.Errorf(
			"workboard authority: transcript directory changed after preparation",
		)
	}
	return nil
}

func (s *ArtifactStore) fail(
	kind ArtifactKind,
	stage FailureStage,
) error {
	if s.hook == nil {
		return nil
	}
	if err := s.hook(kind, stage); err != nil {
		return fmt.Errorf(
			"workboard authority: injected %s %s failure: %w",
			kind,
			stage,
			err,
		)
	}
	return nil
}

func validArtifactSessionID(sessionID string) bool {
	return sessionID != "" &&
		sessionID == filepath.Base(sessionID) &&
		sessionID != "." &&
		sessionID != ".." &&
		!strings.ContainsAny(sessionID, "/\\\x00")
}

func preflightArtifactTarget(
	path string,
	kind ArtifactKind,
) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"workboard authority: %s artifact is not a regular file",
			kind,
		)
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf(
			"workboard authority: %s artifact mode is not 0600",
			kind,
		)
	}
	return info, nil
}

func revalidateArtifactTarget(
	path string,
	expected os.FileInfo,
	kind ArtifactKind,
) error {
	current, err := os.Lstat(path)
	if expected == nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf(
			"workboard authority: %s artifact appeared before replacement",
			kind,
		)
	}
	if err != nil {
		return fmt.Errorf(
			"workboard authority: revalidate %s artifact: %w",
			kind,
			err,
		)
	}
	if current.Mode()&os.ModeSymlink != 0 ||
		!current.Mode().IsRegular() ||
		current.Mode().Perm() != 0o600 ||
		!os.SameFile(expected, current) {
		return fmt.Errorf(
			"workboard authority: %s artifact changed before replacement",
			kind,
		)
	}
	return nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
