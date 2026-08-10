package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var storeLockRegistry sync.Map

var validRecordID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

const (
	maxDiscoveredRecordFiles = 4096
	maxDiscoveryDiagnostics  = 64
	maxRecordBytes           = 1 << 20
)

type Store struct {
	root string
	mu   *sync.Mutex
}

func NewStore(root string) *Store {
	clean := filepath.Clean(root)
	value, _ := storeLockRegistry.LoadOrStore(clean, &sync.Mutex{})
	return &Store{root: clean, mu: value.(*sync.Mutex)}
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *Store) Create(ctx context.Context, record Record) (Record, error) {
	if s == nil {
		return Record{}, errors.New("worktree: nil record store")
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	if err := validateRecordID(record.ID); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	path := s.recordPath(record.ID)
	if _, err := os.Lstat(path); err == nil {
		return Record{}, fmt.Errorf("worktree: record %q already exists: %w", record.ID, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	if err := writeRecordAtomic(path, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) Get(ctx context.Context, id string) (Record, bool, error) {
	if s == nil {
		return Record{}, false, errors.New("worktree: nil record store")
	}
	if err := validateRecordID(id); err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return Record{}, false, err
	}
	return s.readLocked(id)
}

// List reads only regular versioned JSON records from the configured store.
// Malformed, symlinked, and identity-mismatched files remain bounded
// diagnostics and never become lifecycle authority.
func (s *Store) List(
	ctx context.Context,
) ([]Record, []DiscoveryDiagnostic, error) {
	if s == nil {
		return nil, nil, errors.New("worktree: nil record store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return nil, nil, err
	}
	root, expected, exists, err := openRecordRoot(s.root)
	if err != nil || !exists {
		return nil, nil, err
	}
	defer root.Close() //nolint:errcheck
	records, diagnostics, err := s.listRootLocked(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	if !revalidateRecordRoot(s.root, root, expected) {
		return nil, nil, errors.New("worktree: record directory changed during discovery")
	}
	return records, diagnostics, nil
}

// listFromRoot reads records through a caller-pinned directory. The caller
// remains responsible for revalidating the complete ancestor chain.
func (s *Store) listFromRoot(
	ctx context.Context,
	root *os.Root,
) ([]Record, []DiscoveryDiagnostic, error) {
	if s == nil || root == nil {
		return nil, nil, errors.New("worktree: nil record store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return nil, nil, err
	}
	return s.listRootLocked(ctx, root)
}

func (s *Store) listRootLocked(
	ctx context.Context,
	root *os.Root,
) ([]Record, []DiscoveryDiagnostic, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf("worktree: open record directory: %w", err)
	}
	defer directory.Close() //nolint:errcheck
	entries, err := directory.ReadDir(maxDiscoveredRecordFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("worktree: read record directory: %w", err)
	}
	if len(entries) > maxDiscoveredRecordFiles {
		return nil, []DiscoveryDiagnostic{{
			Message: "record discovery limit reached",
		}}, nil
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	records := make([]Record, 0, len(entries))
	diagnostics := make([]DiscoveryDiagnostic, 0)
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return nil, nil, err
		}
		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		info, infoErr := root.Lstat(name)
		if infoErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			message := "record is not a regular file"
			if infoErr != nil {
				message = boundedDiscoveryMessage(infoErr)
			}
			diagnostics = appendDiscoveryDiagnostic(
				diagnostics,
				DiscoveryDiagnostic{
					RecordID: id,
					Message:  message,
				},
			)
			continue
		}
		if err := validateRecordID(id); err != nil {
			diagnostics = appendDiscoveryDiagnostic(
				diagnostics,
				DiscoveryDiagnostic{
					RecordID: id,
					Message:  boundedDiscoveryMessage(err),
				},
			)
			continue
		}
		record, found, readErr := s.readRootLocked(root, id, info)
		if readErr != nil {
			diagnostics = appendDiscoveryDiagnostic(
				diagnostics,
				DiscoveryDiagnostic{
					RecordID: id,
					Message:  boundedDiscoveryMessage(readErr),
				},
			)
			continue
		}
		if found {
			records = append(records, record)
		}
	}
	return records, diagnostics, nil
}

func openRecordRoot(path string) (*os.Root, os.FileInfo, bool, error) {
	expected, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil || expected.Mode()&os.ModeSymlink != 0 || !expected.IsDir() {
		return nil, nil, false, errors.New("worktree: record directory is unsafe")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, false, errors.New("worktree: open record directory failed")
	}
	if !revalidateRecordRoot(path, root, expected) {
		_ = root.Close()
		return nil, nil, false, errors.New("worktree: record directory is unsafe")
	}
	return root, expected, true, nil
}

func revalidateRecordRoot(path string, root *os.Root, expected os.FileInfo) bool {
	if root == nil || expected == nil {
		return false
	}
	opened, openErr := root.Stat(".")
	current, pathErr := os.Lstat(path)
	return openErr == nil && pathErr == nil &&
		opened.IsDir() && current.IsDir() &&
		opened.Mode()&os.ModeSymlink == 0 && current.Mode()&os.ModeSymlink == 0 &&
		os.SameFile(expected, opened) && os.SameFile(expected, current)
}

func (s *Store) Update(
	ctx context.Context,
	id string,
	mutate func(Record) (Record, error),
) (Record, error) {
	if s == nil {
		return Record{}, errors.New("worktree: nil record store")
	}
	if mutate == nil {
		return Record{}, errors.New("worktree: record mutation is required")
	}
	if err := validateRecordID(id); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	current, found, err := s.readLocked(id)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf("worktree: record %q not found: %w", id, os.ErrNotExist)
	}
	next, err := mutate(current)
	if err != nil {
		return Record{}, err
	}
	if next.ID != current.ID || !next.Owner.Equal(current.Owner) ||
		next.Version != current.Version || next.CreatedAt != current.CreatedAt {
		return Record{}, errors.New("worktree: immutable record identity changed")
	}
	if next.Revision != current.Revision+1 {
		return Record{}, fmt.Errorf(
			"worktree: record %q revision %d, want %d",
			id,
			next.Revision,
			current.Revision+1,
		)
	}
	if err := next.Validate(); err != nil {
		return Record{}, err
	}
	if err := writeRecordAtomic(s.recordPath(id), next); err != nil {
		return Record{}, err
	}
	return next, nil
}

func (s *Store) readLocked(id string) (Record, bool, error) {
	data, err := os.ReadFile(s.recordPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, false, fmt.Errorf("worktree: decode record %q: %w", id, err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, false, fmt.Errorf("worktree: validate record %q: %w", id, err)
	}
	if record.ID != id {
		return Record{}, false, fmt.Errorf(
			"worktree: record filename %q contains identity %q",
			id,
			record.ID,
		)
	}
	return record, true, nil
}

func (s *Store) readRootLocked(
	root *os.Root,
	id string,
	expected os.FileInfo,
) (Record, bool, error) {
	name := id + ".json"
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil || expected == nil ||
		before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		!os.SameFile(expected, before) {
		return Record{}, false, errors.New("worktree: record file is unsafe")
	}
	file, err := root.Open(name)
	if err != nil {
		return Record{}, false, errors.New("worktree: open record file failed")
	}
	defer file.Close() //nolint:errcheck
	opened, statErr := file.Stat()
	current, pathErr := root.Lstat(name)
	if statErr != nil || pathErr != nil ||
		!opened.Mode().IsRegular() || !current.Mode().IsRegular() ||
		opened.Mode()&os.ModeSymlink != 0 || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(before, opened) || !os.SameFile(before, current) {
		return Record{}, false, errors.New("worktree: record file changed during discovery")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil || len(data) > maxRecordBytes {
		return Record{}, false, errors.New("worktree: record file is unreadable or too large")
	}
	current, pathErr = root.Lstat(name)
	if pathErr != nil || !os.SameFile(before, current) {
		return Record{}, false, errors.New("worktree: record file changed during discovery")
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, false, fmt.Errorf("worktree: decode record %q: %w", id, err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, false, fmt.Errorf("worktree: validate record %q: %w", id, err)
	}
	if record.ID != id {
		return Record{}, false, fmt.Errorf(
			"worktree: record filename %q contains identity %q",
			id,
			record.ID,
		)
	}
	return record, true, nil
}

func (s *Store) recordPath(id string) string {
	return filepath.Join(s.root, id+".json")
}

func validateRecordID(id string) error {
	if strings.TrimSpace(id) != id || !validRecordID.MatchString(id) {
		return fmt.Errorf("worktree: invalid record ID %q", id)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func boundedDiscoveryMessage(err error) string {
	const maxBytes = 512
	message := strings.TrimSpace(err.Error())
	if len(message) > maxBytes {
		return message[:maxBytes]
	}
	return message
}

func appendDiscoveryDiagnostic(
	diagnostics []DiscoveryDiagnostic,
	diagnostic DiscoveryDiagnostic,
) []DiscoveryDiagnostic {
	if len(diagnostics) >= maxDiscoveryDiagnostics {
		return diagnostics
	}
	return append(diagnostics, diagnostic)
}

func writeRecordAtomic(path string, record Record) (retErr error) {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("worktree: encode record: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("worktree: create record directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("worktree: create record temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceRecordFile(tempPath, path); err != nil {
		return fmt.Errorf("worktree: commit record: %w", err)
	}
	return nil
}
