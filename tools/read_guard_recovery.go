package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	maxReadGuardFileBytes  = 4 << 20
	maxReadGuardEntries    = 1024
	maxReadGuardTotalBytes = 64 << 20
)

var ErrReadGuardRecovery = errors.New("file read guard state is incomplete or changed")

type ReadGuardEntry struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	IsPartial bool   `json:"is_partial"`
}

func (entry *ReadGuardEntry) UnmarshalJSON(raw []byte) error {
	var value struct {
		Path      *string `json:"path"`
		SHA256    *string `json:"sha256"`
		Size      *int64  `json:"size"`
		IsPartial *bool   `json:"is_partial"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Path == nil || value.SHA256 == nil || value.Size == nil || value.IsPartial == nil {
		return ErrReadGuardRecovery
	}
	*entry = ReadGuardEntry{Path: *value.Path, SHA256: *value.SHA256, Size: *value.Size, IsPartial: *value.IsPartial}
	return nil
}

type ReadGuardSnapshot struct {
	Version int              `json:"version"`
	CWD     string           `json:"cwd"`
	Entries []ReadGuardEntry `json:"entries"`
}

type ReadGuardCapture struct {
	Version  int                `json:"version"`
	Complete bool               `json:"complete"`
	Snapshot *ReadGuardSnapshot `json:"snapshot,omitempty"`
}

type fileReadGuardsContextKey struct{}

// NewFileReadGuards creates an isolated owner. Existing callers continue using
// the process-global legacy guard unless they explicitly bind this owner.
func NewFileReadGuards() *FileReadGuards { return &FileReadGuards{m: make(map[string]*ReadFileEntry)} }

func WithFileReadGuards(ctx context.Context, guards *FileReadGuards) context.Context {
	return context.WithValue(ctx, fileReadGuardsContextKey{}, guards)
}

func fileReadGuardsFromContext(ctx context.Context) *FileReadGuards {
	if ctx != nil {
		if guards, ok := ctx.Value(fileReadGuardsContextKey{}).(*FileReadGuards); ok && guards != nil {
			return guards
		}
	}
	return readFileState
}

func recordFileReadObserved(ctx context.Context, path string, partial bool, data []byte) {
	guards := fileReadGuardsFromContext(ctx)
	if guards == readFileState {
		RecordFileRead(path, partial)
		return
	}
	hash := sha256.Sum256(data)
	entry := &ReadFileEntry{Timestamp: time.Now().UnixMilli(), IsPartial: partial, sha256: hex.EncodeToString(hash[:]), size: int64(len(data))}
	guards.Lock()
	defer guards.Unlock()
	if previous := guards.m[path]; previous != nil && !previous.IsPartial && partial {
		// Preserve the exact earlier full-read evidence. A partial view of
		// changed bytes must not manufacture fresh complete-read authority.
		entry.IsPartial, entry.sha256, entry.size = false, previous.sha256, previous.size
	}
	guards.m[path] = entry
	guards.revision++
}

func recordFileReadUnverified(ctx context.Context, path string, partial bool) {
	guards := fileReadGuardsFromContext(ctx)
	if guards == readFileState {
		RecordFileRead(path, partial)
		return
	}
	guards.Lock()
	defer guards.Unlock()
	guards.m[path] = &ReadFileEntry{Timestamp: time.Now().UnixMilli(), IsPartial: partial}
	guards.revision++
}

func hasFileBeenReadContext(ctx context.Context, path string) bool {
	guards := fileReadGuardsFromContext(ctx)
	guards.RLock()
	value := guards.m[path]
	if value == nil || value.IsPartial {
		guards.RUnlock()
		return false
	}
	entry, root, restored := *value, guards.root, guards.restored
	guards.RUnlock()
	if !restored {
		return true
	}
	actual, err := readGuardFile(root, path)
	return err == nil && actual.SHA256 == entry.sha256 && actual.Size == entry.size
}

// Snapshot verifies bytes against observations made by the actual file tools.
// It never reconstructs read credit from a path, timestamp or transcript text.
func (guards *FileReadGuards) Snapshot(cwd string) (ReadGuardSnapshot, error) {
	result := ReadGuardSnapshot{Version: 1, CWD: cwd, Entries: make([]ReadGuardEntry, 0)}
	if guards == nil || guards == readFileState {
		return result, ErrReadGuardRecovery
	}
	if readGuardRoot(cwd) != nil {
		return result, ErrReadGuardRecovery
	}
	guards.RLock()
	revision := guards.revision
	entries := make(map[string]ReadFileEntry, len(guards.m))
	for path, value := range guards.m {
		entries[path] = *value
	}
	guards.RUnlock()
	if len(entries) > maxReadGuardEntries {
		return result, ErrReadGuardRecovery
	}
	var total int64
	for path, entry := range entries {
		if entry.size < 0 || entry.size > maxReadGuardFileBytes || entry.size > maxReadGuardTotalBytes-total {
			return result, ErrReadGuardRecovery
		}
		total += entry.size
		actual, err := readGuardFile(cwd, path)
		if err != nil || actual.SHA256 != entry.sha256 || actual.Size != entry.size {
			return result, ErrReadGuardRecovery
		}
		actual.IsPartial = entry.IsPartial
		result.Entries = append(result.Entries, actual)
	}
	guards.RLock()
	changed := revision != guards.revision
	guards.RUnlock()
	if changed {
		return result, ErrReadGuardRecovery
	}
	slices.SortFunc(result.Entries, func(a, b ReadGuardEntry) int { return strings.Compare(a.Path, b.Path) })
	return result, nil
}

// RestoreFileReadGuards validates the whole snapshot before creating a new
// owner. It never merges with or mutates an existing/global guard map.
func RestoreFileReadGuards(snapshot ReadGuardSnapshot) (*FileReadGuards, error) {
	if snapshot.Version != 1 || snapshot.Entries == nil || len(snapshot.Entries) > maxReadGuardEntries {
		return nil, ErrReadGuardRecovery
	}
	state := NewFileReadGuards()
	state.root, state.restored = snapshot.CWD, true
	previous := ""
	var total int64
	for _, entry := range snapshot.Entries {
		if entry.Path <= previous || entry.Size < 0 || entry.Size > maxReadGuardFileBytes || entry.Size > maxReadGuardTotalBytes-total {
			return nil, ErrReadGuardRecovery
		}
		total += entry.Size
		path := filepath.Join(snapshot.CWD, entry.Path)
		actual, err := readGuardFile(snapshot.CWD, path)
		if err != nil || actual.Path != entry.Path || actual.SHA256 != entry.SHA256 || actual.Size != entry.Size {
			return nil, ErrReadGuardRecovery
		}
		state.m[path] = &ReadFileEntry{Timestamp: time.Now().UnixMilli(), IsPartial: entry.IsPartial, sha256: entry.SHA256, size: entry.Size}
		previous = entry.Path
	}
	// Empty snapshots still require a canonical existing root.
	if err := readGuardRoot(snapshot.CWD); err != nil {
		return nil, err
	}
	return state, nil
}

func readGuardRoot(cwd string) error {
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || canonical != cwd {
		return ErrReadGuardRecovery
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return ErrReadGuardRecovery
	}
	return nil
}

func readGuardFile(cwd, path string) (ReadGuardEntry, error) {
	var result ReadGuardEntry
	if readGuardRoot(cwd) != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return result, ErrReadGuardRecovery
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return result, ErrReadGuardRecovery
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return result, ErrReadGuardRecovery
	}
	defer root.Close() //nolint:errcheck // Read-only pinned root.
	parts := strings.Split(relative, string(filepath.Separator))
	for index := range parts {
		info, err := root.Lstat(filepath.Join(parts[:index+1]...))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || index == len(parts)-1 && !info.Mode().IsRegular() || index < len(parts)-1 && !info.IsDir() {
			return result, ErrReadGuardRecovery
		}
	}
	file, err := openReadGuardFile(root, relative)
	if err != nil {
		return result, ErrReadGuardRecovery
	}
	defer file.Close() //nolint:errcheck // Read-only file, contents validated below.
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxReadGuardFileBytes || !readGuardSingleOwner(file) {
		return result, ErrReadGuardRecovery
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReadGuardFileBytes+1))
	after, statErr := root.Lstat(relative)
	if err != nil || statErr != nil || !os.SameFile(before, after) || before.Size() != int64(len(data)) || after.Size() != before.Size() || !before.ModTime().Equal(after.ModTime()) {
		return result, ErrReadGuardRecovery
	}
	hash := sha256.Sum256(data)
	return ReadGuardEntry{Path: filepath.ToSlash(relative), Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}, nil
}
