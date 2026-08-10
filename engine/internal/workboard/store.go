package workboard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

type AuthorityMode string

const (
	AuthorityModeLegacy    AuthorityMode = "legacy"
	AuthorityModeWorkBoard AuthorityMode = "workboard"
)

type StoreStage string

const (
	StoreStageBackupEncode    StoreStage = "backup-encode"
	StoreStageAuthorityEncode StoreStage = "authority-encode"
	StoreStageMarkerEncode    StoreStage = "marker-encode"
	StoreStageMarkerReread    StoreStage = "marker-reread"
	StoreStageInstall         StoreStage = "authority-install"
	StoreStageFirstMutation   StoreStage = "first-mutation"
)

type StoreFailureHook func(StoreStage) error

type StoreConfig struct {
	Dir         string
	SessionID   string
	FileFailure FailureHook
	Failure     StoreFailureHook
}

type AuthorityState struct {
	Mode   AuthorityMode
	Record AuthorityRecord
	Backup LegacyBackup
}

type Store struct {
	config            StoreConfig
	directoryIdentity os.FileInfo
}

func NewStore(config StoreConfig) (*Store, error) {
	if !validArtifactSessionID(config.SessionID) {
		return nil, fmt.Errorf("workboard authority: invalid SessionID")
	}
	if config.Dir == "" {
		return nil, fmt.Errorf(
			"workboard authority: transcript directory is empty",
		)
	}
	config.Dir = filepath.Clean(config.Dir)
	return &Store{config: config}, nil
}

// Inspect treats marker absence as legacy authority even when interrupted
// prepared files exist. A visible marker requires a complete valid v2 set.
func (s *Store) Inspect() (AuthorityState, error) {
	markerPath := filepath.Join(
		s.config.Dir,
		s.config.SessionID+AuthorityMarkerSuffix,
	)
	info, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		dirInfo, dirErr := os.Lstat(s.config.Dir)
		if errors.Is(dirErr, os.ErrNotExist) {
			return AuthorityState{Mode: AuthorityModeLegacy}, nil
		} else if dirErr != nil {
			return AuthorityState{}, fmt.Errorf(
				"workboard authority: inspect transcript directory: %w",
				dirErr,
			)
		}
		if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
			return AuthorityState{}, fmt.Errorf(
				"workboard authority: transcript path is not a directory",
			)
		}
		prepared := false
		for _, candidate := range []struct {
			kind   ArtifactKind
			suffix string
		}{
			{
				kind:   ArtifactAuthority,
				suffix: AuthorityRecordSuffix,
			},
			{
				kind:   ArtifactBackup,
				suffix: LegacyBackupSuffix,
			},
		} {
			path := filepath.Join(
				s.config.Dir,
				s.config.SessionID+candidate.suffix,
			)
			preparedInfo, pathErr := preflightArtifactTarget(
				path,
				candidate.kind,
			)
			if pathErr != nil {
				return AuthorityState{}, fmt.Errorf(
					"workboard authority: unsafe prepared %s artifact: %w",
					candidate.kind,
					pathErr,
				)
			}
			prepared = prepared || preparedInfo != nil
		}
		if !prepared {
			return AuthorityState{Mode: AuthorityModeLegacy}, nil
		}
		if _, artifactErr := s.artifacts(false); artifactErr != nil {
			return AuthorityState{}, artifactErr
		}
		return AuthorityState{Mode: AuthorityModeLegacy}, nil
	}
	if err != nil {
		return AuthorityState{}, fmt.Errorf(
			"workboard authority: inspect marker: %w",
			err,
		)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return AuthorityState{}, fmt.Errorf(
			"workboard authority: marker artifact is not a regular file",
		)
	}
	artifacts, err := s.artifacts(false)
	if err != nil {
		return AuthorityState{}, err
	}
	return s.loadCommitted(artifacts)
}

func (s *Store) Cutover(
	seed AuthorityRecord,
	backup LegacyBackup,
) (AuthorityState, error) {
	if err := validateAuthorityRecord(seed, s.config.SessionID); err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	if err := validateLegacyBackup(backup, s.config.SessionID); err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	if !reflect.DeepEqual(backupFromRecord(seed), backup) {
		return AuthorityState{Mode: AuthorityModeLegacy}, fmt.Errorf(
			"workboard authority: cutover backup does not match the seed",
		)
	}
	artifacts, err := s.artifacts(true)
	if err != nil {
		return AuthorityState{}, err
	}
	if err := s.fail(StoreStageBackupEncode); err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	backupData, err := EncodeLegacyBackup(backup)
	if err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	if err := artifacts.Write(ArtifactBackup, backupData); err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	if err := s.fail(StoreStageAuthorityEncode); err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	recordData, err := EncodeAuthorityRecord(seed)
	if err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	if err := artifacts.Write(ArtifactAuthority, recordData); err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	marker := AuthorityMarker{
		Version:       AuthorityMarkerVersion,
		SessionID:     s.config.SessionID,
		MinimumReader: MinimumReaderV2,
	}
	if err := s.fail(StoreStageMarkerEncode); err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	markerData, err := EncodeAuthorityMarker(marker)
	if err != nil {
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	if err := artifacts.Write(ArtifactMarker, markerData); err != nil {
		if committed, inspectErr := s.Inspect(); inspectErr == nil &&
			committed.Mode == AuthorityModeWorkBoard {
			return committed, err
		}
		return AuthorityState{Mode: AuthorityModeLegacy}, err
	}
	committed := AuthorityState{
		Mode:   AuthorityModeWorkBoard,
		Record: cloneAuthorityRecord(seed),
		Backup: cloneLegacyBackup(backup),
	}
	if err := s.fail(StoreStageMarkerReread); err != nil {
		return committed, err
	}
	loaded, err := s.loadCommitted(artifacts)
	if err != nil {
		return committed, err
	}
	if err := s.fail(StoreStageInstall); err != nil {
		return loaded, err
	}
	return loaded, nil
}

func (s *Store) Commit(
	expectedBoardID string,
	expectedRevision uint64,
	next AuthorityRecord,
) (AuthorityRecord, error) {
	artifacts, err := s.artifacts(false)
	if err != nil {
		return AuthorityRecord{}, err
	}
	current, err := s.loadCommitted(artifacts)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if current.Record.BoardID != expectedBoardID ||
		current.Record.Board.Revision != expectedRevision {
		return AuthorityRecord{}, fmt.Errorf(
			"workboard authority: board changed before mutation",
		)
	}
	if current.Record.Version != next.Version {
		return AuthorityRecord{}, fmt.Errorf("workboard authority: record version transition requires upgrade")
	}
	if next.SessionID != s.config.SessionID ||
		next.BoardID != expectedBoardID ||
		next.Board.Revision != expectedRevision+1 {
		return AuthorityRecord{}, fmt.Errorf(
			"workboard authority: invalid next board identity or revision",
		)
	}
	if err := s.fail(StoreStageAuthorityEncode); err != nil {
		return AuthorityRecord{}, err
	}
	data, err := EncodeAuthorityRecord(next)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if writeErr := artifacts.Write(ArtifactAuthority, data); writeErr != nil {
		loaded, inspectErr := s.loadCommitted(artifacts)
		if inspectErr == nil && reflect.DeepEqual(loaded.Record, next) {
			return cloneAuthorityRecord(next), writeErr
		}
		return AuthorityRecord{}, writeErr
	}
	return cloneAuthorityRecord(next), nil
}

// UpgradeExecutionLinks is the one-way v2-to-v3 marker-last transition.
func (s *Store) UpgradeExecutionLinks(expectedBoardID string, expectedRevision uint64, next AuthorityRecord) (AuthorityRecord, error) {
	artifacts, err := s.artifacts(false)
	if err != nil {
		return AuthorityRecord{}, err
	}
	current, err := s.loadCommitted(artifacts)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if current.Record.Version != AuthorityRecordVersion || current.Record.BoardID != expectedBoardID || current.Record.Board.Revision != expectedRevision || next.Version != AuthorityRecordVersionV3 || next.Board.Revision != expectedRevision+1 {
		return AuthorityRecord{}, fmt.Errorf("workboard authority: invalid execution-link upgrade")
	}
	if err := s.fail(StoreStageAuthorityEncode); err != nil {
		return AuthorityRecord{}, err
	}
	data, err := EncodeAuthorityRecord(next)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if err := artifacts.Write(ArtifactAuthority, data); err != nil {
		return AuthorityRecord{}, err
	}
	marker := AuthorityMarker{Version: AuthorityMarkerVersionV2, SessionID: s.config.SessionID, MinimumReader: MinimumReaderV3}
	if err := s.fail(StoreStageMarkerEncode); err != nil {
		return cloneAuthorityRecord(next), err
	}
	data, err = EncodeAuthorityMarker(marker)
	if err != nil {
		return cloneAuthorityRecord(next), err
	}
	if err := artifacts.Write(ArtifactMarker, data); err != nil {
		return cloneAuthorityRecord(next), err
	}
	if err := s.fail(StoreStageMarkerReread); err != nil {
		return cloneAuthorityRecord(next), err
	}
	state, err := s.loadCommitted(artifacts)
	if err != nil {
		return cloneAuthorityRecord(next), err
	}
	return state.Record, nil
}

func (s *Store) Recover(
	expectedBoardID string,
	expectedRevision uint64,
	newBoardID string,
) (AuthorityRecord, error) {
	artifacts, err := s.artifacts(false)
	if err != nil {
		return AuthorityRecord{}, err
	}
	current, err := s.loadCommitted(artifacts)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if current.Record.BoardID != expectedBoardID ||
		current.Record.Board.Revision != expectedRevision {
		return AuthorityRecord{}, fmt.Errorf(
			"workboard authority: recovery identity or revision mismatch",
		)
	}
	if current.Record.Version == AuthorityRecordVersionV3 {
		return AuthorityRecord{}, fmt.Errorf("workboard authority: recovery rejects linked record")
	}
	if newBoardID == "" || newBoardID == current.Record.BoardID {
		return AuthorityRecord{}, fmt.Errorf(
			"workboard authority: recovery requires a fresh BoardID",
		)
	}
	next := AuthorityRecord{
		Version:       AuthorityRecordVersion,
		SessionID:     s.config.SessionID,
		BoardID:       newBoardID,
		Board:         cloneBoard(current.Backup.Board),
		Compatibility: cloneCompatibility(current.Backup.Compatibility),
	}
	if next.Board.Revision == 0 {
		next.Board.Revision = 1
	}
	if err := s.fail(StoreStageAuthorityEncode); err != nil {
		return AuthorityRecord{}, err
	}
	data, err := EncodeAuthorityRecord(next)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if writeErr := artifacts.Write(ArtifactAuthority, data); writeErr != nil {
		loaded, inspectErr := s.loadCommitted(artifacts)
		if inspectErr == nil && reflect.DeepEqual(loaded.Record, next) {
			return cloneAuthorityRecord(next), writeErr
		}
		return AuthorityRecord{}, writeErr
	}
	return cloneAuthorityRecord(next), nil
}

func (s *Store) RemoveOwnedArtifacts() (removed int, err error) {
	artifacts, err := s.artifacts(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	for _, kind := range []ArtifactKind{
		ArtifactMarker,
		ArtifactAuthority,
		ArtifactBackup,
	} {
		didRemove, removeErr := artifacts.Remove(kind)
		if removeErr != nil {
			return removed, removeErr
		}
		if didRemove {
			removed++
		}
	}
	return removed, nil
}

func (s *Store) loadCommitted(
	artifacts *ArtifactStore,
) (AuthorityState, error) {
	markerData, err := artifacts.Read(ArtifactMarker)
	if err != nil {
		return AuthorityState{}, fmt.Errorf(
			"workboard authority: read marker: %w",
			err,
		)
	}
	marker, err := DecodeAuthorityMarker(
		markerData,
		s.config.SessionID,
	)
	if err != nil {
		return AuthorityState{}, err
	}
	recordData, err := artifacts.Read(ArtifactAuthority)
	if err != nil {
		return AuthorityState{}, fmt.Errorf(
			"workboard authority: read v2 record: %w",
			err,
		)
	}
	record, err := DecodeAuthorityRecord(recordData, s.config.SessionID)
	if err != nil {
		return AuthorityState{}, err
	}
	backupData, err := artifacts.Read(ArtifactBackup)
	if err != nil {
		return AuthorityState{}, fmt.Errorf(
			"workboard authority: read legacy backup: %w",
			err,
		)
	}
	backup, err := DecodeLegacyBackup(backupData, s.config.SessionID)
	if err != nil {
		return AuthorityState{}, err
	}
	if backup.BoardID != record.BoardID || backup.SessionID != record.SessionID || backup.Board.Revision > record.Board.Revision {
		return AuthorityState{}, fmt.Errorf("workboard authority: backup lineage mismatch")
	}
	if marker.Version == AuthorityMarkerVersion && record.Version == AuthorityRecordVersionV3 {
		// A prior marker-last upgrade wrote v3 successfully but did not publish m2.
		upgrade := AuthorityMarker{Version: AuthorityMarkerVersionV2, SessionID: s.config.SessionID, MinimumReader: MinimumReaderV3}
		data, encodeErr := EncodeAuthorityMarker(upgrade)
		if encodeErr != nil {
			return AuthorityState{}, encodeErr
		}
		if writeErr := artifacts.Write(ArtifactMarker, data); writeErr != nil {
			return AuthorityState{}, writeErr
		}
		return s.loadCommitted(artifacts)
	}
	if (marker.Version == AuthorityMarkerVersion && record.Version != AuthorityRecordVersion) || (marker.Version == AuthorityMarkerVersionV2 && record.Version != AuthorityRecordVersionV3) {
		return AuthorityState{}, fmt.Errorf("workboard authority: marker and record version mismatch")
	}
	return AuthorityState{
		Mode:   AuthorityModeWorkBoard,
		Record: record,
		Backup: backup,
	}, nil
}

func (s *Store) artifacts(create bool) (*ArtifactStore, error) {
	if s.directoryIdentity != nil {
		info, err := os.Lstat(s.config.Dir)
		if err != nil || !os.SameFile(s.directoryIdentity, info) {
			return nil, fmt.Errorf(
				"workboard authority: transcript directory changed after preparation",
			)
		}
	} else if create {
		if err := os.MkdirAll(s.config.Dir, 0o700); err != nil {
			return nil, fmt.Errorf(
				"workboard authority: create transcript directory: %w",
				err,
			)
		}
	}
	return newArtifactStore(
		s.config.Dir,
		s.config.SessionID,
		s.config.FileFailure,
		s.directoryIdentity,
	)
}

func (s *Store) bindDirectoryIdentity(info os.FileInfo) error {
	if info == nil {
		return fmt.Errorf(
			"workboard authority: transcript directory identity is unavailable",
		)
	}
	current, err := os.Lstat(s.config.Dir)
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf(
			"workboard authority: transcript directory changed after preparation",
		)
	}
	if current.Mode()&os.ModeSymlink != 0 ||
		!current.IsDir() ||
		current.Mode().Perm() != privateTranscriptDirectoryMode {
		return fmt.Errorf(
			"workboard authority: prepared transcript directory is not private",
		)
	}
	s.directoryIdentity = info
	return nil
}

func (s *Store) fail(stage StoreStage) error {
	if s.config.Failure == nil {
		return nil
	}
	if err := s.config.Failure(stage); err != nil {
		return fmt.Errorf(
			"workboard authority: injected %s failure: %w",
			stage,
			err,
		)
	}
	return nil
}
