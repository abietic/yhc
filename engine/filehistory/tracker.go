package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileChange records a single file modification event.
type FileChange struct {
	Path         string    `json:"path"`
	Operation    string    `json:"operation"` // "create", "edit", "delete"
	Timestamp    time.Time `json:"timestamp"`
	TurnNumber   int       `json:"turn_number"`
	ToolName     string    `json:"tool_name"`
	OriginalHash string    `json:"original_hash,omitempty"`
	NewHash      string    `json:"new_hash,omitempty"`
}

// FileSnapshot captures the state of a file at a point in time.
type FileSnapshot struct {
	Path    string
	Content []byte
	ModTime time.Time
	Exists  bool // false if file didn't exist before
}

// FileTracker tracks file modifications during a session, maintaining
// original snapshots for undo/revert and recording change metadata.
type FileTracker struct {
	mu        sync.RWMutex
	changes   []FileChange
	snapshots map[string]*FileSnapshot // original state, keyed by path
	cwd       string
}

// NewFileTracker creates a new FileTracker rooted at the given working directory.
func NewFileTracker(cwd string) *FileTracker {
	return &FileTracker{
		changes:   make([]FileChange, 0),
		snapshots: make(map[string]*FileSnapshot),
		cwd:       cwd,
	}
}

// RecordChange records a file modification event.
func (t *FileTracker) RecordChange(path, operation, toolName string, turnNumber int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	absPath := t.resolvePath(path)

	change := FileChange{
		Path:       absPath,
		Operation:  operation,
		Timestamp:  time.Now(),
		TurnNumber: turnNumber,
		ToolName:   toolName,
	}

	// Compute hashes based on current file state.
	if snap, exists := t.snapshots[absPath]; exists && snap.Exists {
		change.OriginalHash = fileHash(snap.Content)
	}
	if content, err := os.ReadFile(absPath); err == nil {
		change.NewHash = fileHash(content)
	}

	t.changes = append(t.changes, change)
}

// SnapshotBefore captures the current state of a file before modification.
// Should be called before any write/edit/delete operation. If a snapshot
// already exists for the path, it is not overwritten (preserving the original).
func (t *FileTracker) SnapshotBefore(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	absPath := t.resolvePath(path)

	// Only capture the first snapshot (the original state).
	if _, exists := t.snapshots[absPath]; exists {
		return nil
	}

	snap := &FileSnapshot{
		Path: absPath,
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			snap.Exists = false
			t.snapshots[absPath] = snap
			return nil
		}
		return fmt.Errorf("failed to stat file %s: %w", absPath, err)
	}

	snap.Exists = true
	snap.ModTime = info.ModTime()

	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", absPath, err)
	}
	snap.Content = content

	t.snapshots[absPath] = snap
	return nil
}

// GetChanges returns all recorded file changes.
func (t *FileTracker) GetChanges() []FileChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]FileChange, len(t.changes))
	copy(result, t.changes)
	return result
}

// GetChangedFiles returns unique file paths that were modified.
func (t *FileTracker) GetChangedFiles() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	seen := make(map[string]struct{})
	for _, c := range t.changes {
		seen[c.Path] = struct{}{}
	}

	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

// GetSnapshot returns the original snapshot of a file (before any modifications).
func (t *FileTracker) GetSnapshot(path string) *FileSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	absPath := t.resolvePath(path)
	return t.snapshots[absPath]
}

// CanRevert returns true if the file can be reverted to its original state.
func (t *FileTracker) CanRevert(path string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	absPath := t.resolvePath(path)
	_, exists := t.snapshots[absPath]
	return exists
}

// Revert restores a file to its original state.
func (t *FileTracker) Revert(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	absPath := t.resolvePath(path)

	snap, exists := t.snapshots[absPath]
	if !exists {
		return fmt.Errorf("no snapshot found for %s", absPath)
	}

	if !snap.Exists {
		// File didn't exist before; remove it.
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove file %s: %w", absPath, err)
		}
	} else {
		// Restore original content.
		dir := filepath.Dir(absPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		if err := os.WriteFile(absPath, snap.Content, 0o644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", absPath, err)
		}
		if err := os.Chtimes(absPath, snap.ModTime, snap.ModTime); err != nil {
			return fmt.Errorf("failed to restore mod time for %s: %w", absPath, err)
		}
	}

	return nil
}

// RevertAll restores all modified files to their original states.
func (t *FileTracker) RevertAll() []error {
	t.mu.Lock()
	paths := make([]string, 0, len(t.snapshots))
	for path := range t.snapshots {
		paths = append(paths, path)
	}
	t.mu.Unlock()

	sort.Strings(paths)

	var errs []error
	for _, path := range paths {
		if err := t.Revert(path); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Summary returns a human-readable summary of all changes.
func (t *FileTracker) Summary() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.changes) == 0 {
		return "No file changes recorded."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "File changes (%d total):\n", len(t.changes))

	// Group changes by file.
	byFile := make(map[string][]FileChange)
	for _, c := range t.changes {
		byFile[c.Path] = append(byFile[c.Path], c)
	}

	paths := make([]string, 0, len(byFile))
	for path := range byFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		changes := byFile[path]
		ops := make([]string, 0, len(changes))
		for _, c := range changes {
			ops = append(ops, c.Operation)
		}
		fmt.Fprintf(&sb, "  %s: %s (via %s)\n", path, strings.Join(ops, ", "), changes[len(changes)-1].ToolName)
	}

	return sb.String()
}

// Export writes the change history to a JSON file.
func (t *FileTracker) Export(outputPath string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	data, err := json.MarshalIndent(t.changes, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal changes: %w", err)
	}

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write export file %s: %w", outputPath, err)
	}

	return nil
}

// CopyForResume creates a serializable copy of file history state
// that can be persisted and restored on session resume.
func (t *FileTracker) CopyForResume() []FileChange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]FileChange, len(t.changes))
	copy(result, t.changes)
	return result
}

// fileHash computes a SHA-256 hash of file content for change detection.
func fileHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// resolvePath converts a relative path to an absolute path using the tracker's cwd.
func (t *FileTracker) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(t.cwd, path))
}
