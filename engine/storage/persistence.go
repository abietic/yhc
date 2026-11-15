// Package storage provides file persistence for large tool results.
// Results exceeding the inline size threshold are written to disk with
// a generated preview for display, avoiding excessive memory use in
// conversation context.
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// DefaultMaxInlineSize is the threshold (in characters) below which
	// tool results are kept inline rather than persisted to disk.
	DefaultMaxInlineSize = 50000

	// DefaultMaxPreviewSize is the maximum number of characters included
	// in a preview of a persisted result.
	DefaultMaxPreviewSize = 2000

	// MaxCompleteOutputBytes bounds each pre-preview execution output. Existing
	// session ownership and Cleanup govern retention; this is not a disk quota.
	MaxCompleteOutputBytes = 64 << 20

	// toolResultsSubdir is the subdirectory name within the session dir.
	toolResultsSubdir = "tool_results"
)

// StoredResult represents a tool result that has been persisted to disk.
type StoredResult struct {
	ID        string
	ToolName  string
	FilePath  string
	Preview   string
	FullSize  int
	CreatedAt time.Time
}

// StorageStats holds summary statistics about stored results.
type StorageStats struct {
	Count       int
	TotalBytes  int
	OldestEntry time.Time
}

// ResultStorage manages persistence of large tool results to disk.
type ResultStorage struct {
	baseDir        string
	maxInlineSize  int
	maxPreviewSize int
	results        map[string]*StoredResult
	mu             sync.RWMutex
	// Deterministic namespace-replacement fault points; production leaves nil.
	beforeCompleteRootOpenForTest func()
	beforeCompletePublishForTest  func()
}

// NewResultStorage creates a ResultStorage that writes to
// <sessionDir>/tool_results/ with default size thresholds.
func NewResultStorage(sessionDir string) *ResultStorage {
	return &ResultStorage{
		baseDir:        filepath.Join(sessionDir, toolResultsSubdir),
		maxInlineSize:  DefaultMaxInlineSize,
		maxPreviewSize: DefaultMaxPreviewSize,
		results:        make(map[string]*StoredResult),
	}
}

// ShouldStore returns true if the result is large enough to warrant
// disk persistence (i.e. exceeds maxInlineSize).
func (s *ResultStorage) ShouldStore(result string) bool {
	return len(result) > s.maxInlineSize
}

// Store persists a large tool result to disk. If the result is smaller
// than maxInlineSize, it returns (nil, nil) indicating the result should
// remain inline. Otherwise it writes the full content to a file with a
// UUID-based filename and returns a StoredResult with a generated preview.
func (s *ResultStorage) Store(toolName, result string) (*StoredResult, error) {
	if !s.ShouldStore(result) {
		return nil, nil
	}

	// Ensure base directory exists
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create dir: %w", err)
	}

	id := uuid.New().String()
	filePath := filepath.Join(s.baseDir, id+".txt")

	if err := os.WriteFile(filePath, []byte(result), 0o644); err != nil {
		return nil, fmt.Errorf("storage: write file: %w", err)
	}

	preview := GeneratePreview(result, s.maxPreviewSize)

	stored := &StoredResult{
		ID:        id,
		ToolName:  toolName,
		FilePath:  filePath,
		Preview:   preview,
		FullSize:  len(result),
		CreatedAt: time.Now(),
	}

	s.mu.Lock()
	s.results[id] = stored
	s.mu.Unlock()

	return stored, nil
}

// StoreComplete retains output that a tool is about to truncate, including
// outputs below the generic inline threshold. Chunks are bounded before being
// joined. Only a complete private, no-replace file yields a readable receipt.
func (s *ResultStorage) StoreComplete(toolName string, parts ...string) (*StoredResult, error) {
	size := 0
	for _, part := range parts {
		if len(part) > MaxCompleteOutputBytes-size {
			return nil, fmt.Errorf("storage: complete output exceeds retention limit")
		}
		size += len(part)
	}
	baseDir, err := filepath.Abs(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve session directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(baseDir), 0o700); err != nil {
		return nil, fmt.Errorf("storage: create session directory: %w", err)
	}
	parent, err := os.OpenRoot(filepath.Dir(baseDir))
	if err != nil {
		return nil, fmt.Errorf("storage: open session directory: %w", err)
	}
	defer parent.Close()
	if err := parent.Mkdir(toolResultsSubdir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("storage: create output directory: %w", err)
	}
	info, err := parent.Lstat(toolResultsSubdir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("storage: output directory is not private")
	}
	if before := s.beforeCompleteRootOpenForTest; before != nil {
		before()
	}
	directory, err := parent.OpenRoot(toolResultsSubdir)
	if err != nil {
		return nil, fmt.Errorf("storage: open output directory: %w", err)
	}
	defer directory.Close()
	opened, err := directory.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("storage: output directory changed")
	}
	id := uuid.New().String()
	name := id + ".txt"
	file, err := directory.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("storage: create complete output: %w", err)
	}
	created, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("storage: identify complete output: %w", err)
	}
	published := false
	defer func() {
		_ = file.Close()
		if !published {
			current, err := directory.Lstat(name)
			if err == nil && os.SameFile(created, current) {
				_ = directory.Remove(name)
			}
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("storage: protect complete output: %w", err)
	}
	for _, part := range parts {
		if _, err := file.WriteString(part); err != nil {
			return nil, fmt.Errorf("storage: write complete output: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("storage: sync complete output: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("storage: close complete output: %w", err)
	}
	if before := s.beforeCompletePublishForTest; before != nil {
		before()
	}
	current, err := parent.Lstat(toolResultsSubdir)
	if err != nil || !current.IsDir() || current.Mode().Perm()&0o022 != 0 || !os.SameFile(opened, current) {
		return nil, fmt.Errorf("storage: output directory changed before publication")
	}
	currentFile, err := directory.Lstat(name)
	if err != nil || !currentFile.Mode().IsRegular() || !os.SameFile(created, currentFile) || currentFile.Size() != int64(size) {
		return nil, fmt.Errorf("storage: complete output changed before publication")
	}
	stored := &StoredResult{
		ID: id, ToolName: toolName, FilePath: filepath.Join(baseDir, name),
		Preview: GeneratePreview(strings.Join(parts, ""), s.maxPreviewSize), FullSize: size, CreatedAt: time.Now(),
	}
	s.mu.Lock()
	s.results[id] = stored
	s.mu.Unlock()
	published = true
	return stored, nil
}

// Retrieve reads the full content of a previously stored result from disk.
func (s *ResultStorage) Retrieve(id string) (string, error) {
	s.mu.RLock()
	stored, ok := s.results[id]
	s.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("storage: result %q not found", id)
	}

	data, err := os.ReadFile(stored.FilePath)
	if err != nil {
		return "", fmt.Errorf("storage: read file: %w", err)
	}

	return string(data), nil
}

// GetPreview returns the cached preview for a stored result without
// performing any disk I/O. The boolean indicates whether the id was found.
func (s *ResultStorage) GetPreview(id string) (string, bool) {
	s.mu.RLock()
	stored, ok := s.results[id]
	s.mu.RUnlock()

	if !ok {
		return "", false
	}
	return stored.Preview, true
}

// Cleanup removes stored results older than maxAge from both disk and
// the in-memory index.
func (s *ResultStorage) Cleanup(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)

	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []string
	for id, stored := range s.results {
		if stored.CreatedAt.Before(cutoff) {
			if err := os.Remove(stored.FilePath); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("remove %s: %v", stored.FilePath, err))
				continue
			}
			delete(s.results, id)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("storage: cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Stats returns summary statistics about currently stored results.
func (s *ResultStorage) Stats() StorageStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := StorageStats{}
	for _, stored := range s.results {
		stats.Count++
		stats.TotalBytes += stored.FullSize
		if stats.OldestEntry.IsZero() || stored.CreatedAt.Before(stats.OldestEntry) {
			stats.OldestEntry = stored.CreatedAt
		}
	}
	return stats
}

// GeneratePreview produces a truncated preview of content. It takes up to
// maxLen characters, tries to break at a line boundary within the last 50%
// of the allowed range, and appends a suffix indicating how many characters
// were omitted.
func GeneratePreview(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}

	end := maxLen
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	truncated := content[:end]

	// Try to find a newline in the latter half to avoid cutting mid-line
	halfPoint := end / 2
	lastNewline := strings.LastIndex(truncated[halfPoint:], "\n")
	cutPoint := end
	if lastNewline >= 0 {
		cutPoint = halfPoint + lastNewline + 1
	}

	remaining := len(content) - cutPoint
	return content[:cutPoint] + fmt.Sprintf("[...%d more characters]", remaining)
}
