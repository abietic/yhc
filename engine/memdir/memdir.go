package memdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	EntrypointName     = "MEMORY.md"
	MaxEntrypointLines = 200
	MaxEntrypointBytes = 25000
)

// EntrypointTruncation contains the result of truncating MEMORY.md content.
type EntrypointTruncation struct {
	Content          string
	LineCount        int
	ByteCount        int
	WasLineTruncated bool
	WasByteTruncated bool
}

// TruncateEntrypointContent truncates MEMORY.md content to line and byte caps.
// Mirrors truncateEntrypointContent from memdir.ts.
func TruncateEntrypointContent(raw string) EntrypointTruncation {
	trimmed := strings.TrimSpace(raw)
	lines := strings.Split(trimmed, "\n")
	lineCount := len(lines)
	byteCount := len(trimmed)

	wasLineTruncated := lineCount > MaxEntrypointLines
	wasByteTruncated := byteCount > MaxEntrypointBytes

	if !wasLineTruncated && !wasByteTruncated {
		return EntrypointTruncation{
			Content:          trimmed,
			LineCount:        lineCount,
			ByteCount:        byteCount,
			WasLineTruncated: false,
			WasByteTruncated: false,
		}
	}

	truncated := trimmed
	if wasLineTruncated {
		truncated = strings.Join(lines[:MaxEntrypointLines], "\n")
	}

	if len(truncated) > MaxEntrypointBytes {
		cutAt := strings.LastIndex(truncated[:MaxEntrypointBytes], "\n")
		if cutAt > 0 {
			truncated = truncated[:cutAt]
		} else {
			truncated = truncated[:MaxEntrypointBytes]
		}
	}

	var reason string
	switch {
	case wasByteTruncated && !wasLineTruncated:
		reason = fmt.Sprintf("%s (limit: %s) — index entries are too long",
			formatFileSize(byteCount), formatFileSize(MaxEntrypointBytes))
	case wasLineTruncated && !wasByteTruncated:
		reason = fmt.Sprintf("%d lines (limit: %d)", lineCount, MaxEntrypointLines)
	default:
		reason = fmt.Sprintf("%d lines and %s", lineCount, formatFileSize(byteCount))
	}

	truncated += fmt.Sprintf("\n\n> WARNING: %s is %s. Only part of it was loaded. Keep index entries to one line under ~200 chars; move detail into topic files.",
		EntrypointName, reason)

	return EntrypointTruncation{
		Content:          truncated,
		LineCount:        lineCount,
		ByteCount:        byteCount,
		WasLineTruncated: wasLineTruncated,
		WasByteTruncated: wasByteTruncated,
	}
}

// EnsureMemoryDirExists creates the auto-memory directory if it doesn't exist.
func EnsureMemoryDirExists() error {
	dir := GetAutoMemPath()
	return os.MkdirAll(dir, 0o700)
}

// BuildMemoryPrompt reads MEMORY.md and returns its content for system prompt injection.
// Returns empty string if auto-memory is disabled or the file doesn't exist.
func BuildMemoryPrompt() string {
	if !IsAutoMemoryEnabled() {
		return ""
	}

	entrypoint := GetAutoMemEntrypoint()
	data, err := os.ReadFile(entrypoint)
	if err != nil {
		return ""
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return ""
	}

	truncation := TruncateEntrypointContent(content)
	return truncation.Content
}

// ListMemoryFiles returns all .md files in the auto-memory directory.
func ListMemoryFiles() ([]string, error) {
	dir := GetAutoMemPath()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if !info.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func formatFileSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}
