// Package storage provides file persistence for large tool results.
// Results exceeding the inline size threshold are written to disk with
// a generated preview for display, avoiding excessive memory use in
// conversation context.
package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultMaxInlineSize is the threshold (in characters) below which
	// tool results are kept inline rather than persisted to disk.
	DefaultMaxInlineSize = 50000

	// DefaultMaxPreviewSize is the maximum number of characters included
	// in a preview of a persisted result.
	DefaultMaxPreviewSize = 2000

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

	truncated := content[:maxLen]

	// Try to find a newline in the latter half to avoid cutting mid-line
	halfPoint := maxLen / 2
	lastNewline := strings.LastIndex(truncated[halfPoint:], "\n")
	cutPoint := maxLen
	if lastNewline >= 0 {
		cutPoint = halfPoint + lastNewline + 1
	}

	remaining := len(content) - cutPoint
	return content[:cutPoint] + fmt.Sprintf("[...%d more characters]", remaining)
}
