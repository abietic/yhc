package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/google/uuid"

	"github.com/abietic/yhc/internal/statemigration"
)

const (
	maxRunUsageCheckpointBytes = 8 << 20
	maxRunUsageCheckpointOwner = 4 << 10
)

var errRunUsageCheckpointStorage = errors.New("run usage checkpoint storage error")

type runUsageCheckpointStore interface {
	WriteCheckpoint([]byte) error
	Close() error
}

type runUsageCheckpointOwner struct {
	Version        int    `json:"version"`
	SegmentID      string `json:"segment_id"`
	MetadataSHA256 string `json:"metadata_sha256"`
}

type runUsageCheckpointFileStore struct {
	mu         sync.Mutex
	store      *statemigration.CanonicalStore
	owner      os.FileInfo
	segmentID  string
	metadata   string
	checkpoint os.FileInfo
	poisoned   bool
	closed     bool
}

func newRunUsageCheckpointFileStore(path, segmentID, metadataSHA256 string) (runUsageCheckpointStore, error) {
	if !runUsageCheckpointStoreSupported() || !validRunUsageCheckpointPath(path) ||
		!validRunUsageCheckpointUUID(segmentID) || !validRunUsageCheckpointHash(metadataSHA256) {
		return nil, runUsageCheckpointStorageError()
	}
	store, exists, err := statemigration.OpenCanonicalStore(filepath.Dir(path), filepath.Base(path), true)
	if err != nil || !exists {
		return nil, runUsageCheckpointStorageError()
	}
	fail := func() (runUsageCheckpointStore, error) {
		_ = store.Close()
		return nil, runUsageCheckpointStorageError()
	}
	entries, err := fs.ReadDir(store.Root().FS(), ".")
	if err != nil || len(entries) != 0 || store.Revalidate() != nil {
		return fail()
	}
	owner := runUsageCheckpointOwner{Version: 1, SegmentID: segmentID, MetadataSHA256: metadataSHA256}
	encoded, err := json.Marshal(owner)
	if err != nil {
		return fail()
	}
	file, err := store.Root().OpenFile("owner.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fail()
	}
	if written, err := file.Write(encoded); err != nil || written != len(encoded) {
		_ = file.Close()
		return fail()
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fail()
	}
	ownerInfo, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || store.ValidateRegular("owner.json", ownerInfo) != nil || store.Sync() != nil || store.Revalidate() != nil {
		return fail()
	}
	return &runUsageCheckpointFileStore{store: store, owner: ownerInfo, segmentID: segmentID, metadata: metadataSHA256}, nil
}

func (s *runUsageCheckpointFileStore) WriteCheckpoint(snapshot []byte) error {
	if s == nil {
		return runUsageCheckpointStorageError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.poisoned {
		return runUsageCheckpointStorageError()
	}
	if len(snapshot) == 0 || len(snapshot) > maxRunUsageCheckpointBytes || s.store == nil || s.owner == nil {
		s.poisoned = true
		return runUsageCheckpointStorageError()
	}
	if s.store.Revalidate() != nil || s.validateOwner() != nil ||
		(s.checkpoint != nil && s.store.ValidateRegular("checkpoint.json", s.checkpoint) != nil) {
		s.poisoned = true
		return runUsageCheckpointStorageError()
	}
	entries, err := fs.ReadDir(s.store.Root().FS(), ".")
	valid := err == nil && len(entries) == 1 && entries[0].Name() == "owner.json"
	if s.checkpoint != nil {
		valid = err == nil && len(entries) == 2 && runUsageCheckpointEntries(entries)
	}
	if !valid {
		s.poisoned = true
		return runUsageCheckpointStorageError()
	}
	if err := s.writeCheckpoint(snapshot); err != nil {
		s.poisoned = true
		return runUsageCheckpointStorageError()
	}
	return nil
}

func (s *runUsageCheckpointFileStore) validateOwner() error {
	data, info, err := runUsageCheckpointReadRegular(s.store, "owner.json", maxRunUsageCheckpointOwner)
	if err != nil {
		return err
	}
	owner, err := decodeRunUsageCheckpointOwner(data)
	if err != nil || !os.SameFile(s.owner, info) || owner.SegmentID != s.segmentID || owner.MetadataSHA256 != s.metadata ||
		s.store.ValidateRegular("owner.json", s.owner) != nil {
		return runUsageCheckpointStorageError()
	}
	return nil
}

func (s *runUsageCheckpointFileStore) writeCheckpoint(snapshot []byte) error {
	pending := "pending-" + uuid.NewString() + ".json"
	file, err := s.store.Root().OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if written, err := file.Write(snapshot); err != nil || written != len(snapshot) {
		_ = file.Close()
		return runUsageCheckpointStorageError()
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || s.store.ValidateRegular(pending, info) != nil {
		return runUsageCheckpointStorageError()
	}
	if err := s.store.PromoteRegular(pending, "checkpoint.json", info); err != nil {
		return err
	}
	if err := s.store.Sync(); err != nil || s.store.Revalidate() != nil {
		return runUsageCheckpointStorageError()
	}
	s.checkpoint = info
	return nil
}

func (s *runUsageCheckpointFileStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.store == nil || s.store.Close() != nil {
		return runUsageCheckpointStorageError()
	}
	return nil
}

func readRunUsageCheckpointFile(path, expectedSegmentID string) ([]byte, string, error) {
	if !runUsageCheckpointStoreSupported() || !validRunUsageCheckpointPath(path) || !validRunUsageCheckpointUUID(expectedSegmentID) {
		return nil, "", runUsageCheckpointStorageError()
	}
	store, exists, err := statemigration.OpenCanonicalStore(filepath.Dir(path), filepath.Base(path), false)
	if err != nil || !exists {
		return nil, "", runUsageCheckpointStorageError()
	}
	defer store.Close() //nolint:errcheck // Read-only failures below already fail closed.
	entries, err := fs.ReadDir(store.Root().FS(), ".")
	if err != nil || len(entries) != 2 || !runUsageCheckpointEntries(entries) || store.Revalidate() != nil {
		return nil, "", runUsageCheckpointStorageError()
	}
	ownerBytes, ownerInfo, err := runUsageCheckpointReadRegular(store, "owner.json", maxRunUsageCheckpointOwner)
	if err != nil {
		return nil, "", runUsageCheckpointStorageError()
	}
	owner, err := decodeRunUsageCheckpointOwner(ownerBytes)
	if err != nil || owner.SegmentID != expectedSegmentID {
		return nil, "", runUsageCheckpointStorageError()
	}
	snapshot, checkpointInfo, err := runUsageCheckpointReadRegular(store, "checkpoint.json", maxRunUsageCheckpointBytes)
	if err != nil || len(snapshot) == 0 ||
		store.ValidateRegular("owner.json", ownerInfo) != nil ||
		store.ValidateRegular("checkpoint.json", checkpointInfo) != nil || store.Revalidate() != nil {
		return nil, "", runUsageCheckpointStorageError()
	}
	return snapshot, owner.MetadataSHA256, nil
}

func runUsageCheckpointReadRegular(store *statemigration.CanonicalStore, name string, limit int64) ([]byte, os.FileInfo, error) {
	file, info, exists, err := store.OpenRegular(name, os.O_RDONLY, false)
	if err != nil || !exists {
		return nil, nil, runUsageCheckpointStorageError()
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) > limit {
		return nil, nil, runUsageCheckpointStorageError()
	}
	return data, info, nil
}

func decodeRunUsageCheckpointOwner(data []byte) (runUsageCheckpointOwner, error) {
	var owner runUsageCheckpointOwner
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&owner); err != nil {
		return runUsageCheckpointOwner{}, runUsageCheckpointStorageError()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || owner.Version != 1 ||
		!validRunUsageCheckpointUUID(owner.SegmentID) || !validRunUsageCheckpointHash(owner.MetadataSHA256) {
		return runUsageCheckpointOwner{}, runUsageCheckpointStorageError()
	}
	return owner, nil
}

func runUsageCheckpointEntries(entries []fs.DirEntry) bool {
	seenOwner, seenCheckpoint := false, false
	for _, entry := range entries {
		if entry.IsDir() {
			return false
		}
		switch entry.Name() {
		case "owner.json":
			if seenOwner {
				return false
			}
			seenOwner = true
		case "checkpoint.json":
			if seenCheckpoint {
				return false
			}
			seenCheckpoint = true
		default:
			return false
		}
	}
	return seenOwner && seenCheckpoint
}

func runUsageCheckpointStoreSupported() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

func validRunUsageCheckpointPath(path string) bool {
	if !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return false
	}
	base := filepath.Base(path)
	return base != "." && base != string(filepath.Separator) && filepath.Dir(path) != path
}

func validRunUsageCheckpointUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validRunUsageCheckpointHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func runUsageCheckpointStorageError() error {
	return fmt.Errorf("%w: unavailable", errRunUsageCheckpointStorage)
}
