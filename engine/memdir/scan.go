package memdir

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	MaxMemoryFiles      = 200
	FrontmatterMaxLines = 30
)

// MemoryHeader describes a memory file's metadata from its frontmatter.
type MemoryHeader struct {
	Filename    string
	FilePath    string
	MtimeMs     int64
	Description string
	Type        MemoryType
}

// ScanMemoryFiles scans the auto-memory directory for .md files,
// reads their frontmatter, and returns headers sorted newest-first.
// Mirrors scanMemoryFiles from memoryScan.ts.
func ScanMemoryFiles(memoryDir string) ([]MemoryHeader, error) {
	if _, err := os.Stat(memoryDir); os.IsNotExist(err) {
		return nil, nil
	}

	var headers []MemoryHeader

	err := filepath.Walk(memoryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		if filepath.Base(path) == "MEMORY.md" {
			return nil
		}

		relPath, _ := filepath.Rel(memoryDir, path)
		header := MemoryHeader{
			Filename: relPath,
			FilePath: path,
			MtimeMs:  info.ModTime().UnixMilli(),
		}

		// Read frontmatter from first N lines.
		data, readErr := readFirstLines(path, FrontmatterMaxLines)
		if readErr == nil {
			fm := ParseFrontmatter(string(data))
			header.Description = fm.Description
			header.Type = ParseMemoryType(fm.Type)
		}

		headers = append(headers, header)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort newest-first.
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].MtimeMs > headers[j].MtimeMs
	})

	// Cap at MaxMemoryFiles.
	if len(headers) > MaxMemoryFiles {
		headers = headers[:MaxMemoryFiles]
	}

	return headers, nil
}

// MemoryAgeDays returns the number of days since the given mtime (floor-rounded).
func MemoryAgeDays(mtimeMs int64) int {
	elapsed := time.Now().UnixMilli() - mtimeMs
	days := int(math.Floor(float64(elapsed) / 86_400_000))
	if days < 0 {
		return 0
	}
	return days
}

// MemoryAge returns a human-readable age string.
func MemoryAge(mtimeMs int64) string {
	d := MemoryAgeDays(mtimeMs)
	if d == 0 {
		return "today"
	}
	if d == 1 {
		return "yesterday"
	}
	return fmt.Sprintf("%d days ago", d)
}

// Frontmatter holds parsed YAML frontmatter from a memory file.
type Frontmatter struct {
	Name        string
	Description string
	Type        string
}

// ParseFrontmatter extracts frontmatter from markdown content.
func ParseFrontmatter(content string) Frontmatter {
	var fm Frontmatter
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fm
	}

	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return fm
	}

	for i := 1; i < endIdx; i++ {
		line := lines[i]
		if kv := parseYAMLLine(line); kv != nil {
			switch kv[0] {
			case "name":
				fm.Name = kv[1]
			case "description":
				fm.Description = kv[1]
			case "type":
				fm.Type = kv[1]
			}
		}
	}
	return fm
}

func parseYAMLLine(line string) []string {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return nil
	}
	key := strings.TrimSpace(line[:idx])
	val := strings.TrimSpace(line[idx+1:])
	// Strip quotes.
	if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
		val = val[1 : len(val)-1]
	}
	return []string{key, val}
}

func readFirstLines(path string, maxLines int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.SplitN(string(data), "\n", maxLines+1)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return []byte(strings.Join(lines, "\n")), nil
}
