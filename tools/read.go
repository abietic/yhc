package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	maxLinesToRead     = 2000 // hard maximum for any single read call
	defaultLineLimit   = 250  // default limit when no limit/offset specified; mirrors TS FileReadTool
	maxCharsPerLine    = 2000 // per-line truncation
	lineNumberPadWidth = 6    // right-align line numbers to this width
)

// blockedDevicePaths are device files that should never be read (infinite output, blocking, etc.).
var blockedDevicePaths = map[string]bool{
	"/dev/zero":    true,
	"/dev/random":  true,
	"/dev/urandom": true,
	"/dev/full":    true,
	"/dev/stdin":   true,
	"/dev/tty":     true,
	"/dev/console": true,
	"/dev/stdout":  true,
	"/dev/stderr":  true,
	"/dev/fd/0":    true,
	"/dev/fd/1":    true,
	"/dev/fd/2":    true,
}

// binaryExtensions are file extensions that indicate binary content.
var binaryExtensions = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true,
	".o": true, ".obj": true, ".bin": true, ".class": true, ".pyc": true,
	".pyo": true, ".wasm": true, ".lib": true, ".dat": true, ".db": true,
	".sqlite": true, ".sqlite3": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true,
	".7z": true, ".rar": true, ".jar": true, ".war": true,
	".ico": true, ".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".mkv": true, ".wav": true, ".flac": true, ".ogg": true,
	".m4a": true, ".m4v": true, ".webm": true,
}

// imageExtensions are allowed despite being binary (handled specially).
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".svg": true, ".tiff": true, ".tif": true,
}

func isBlockedDevicePath(path string) bool {
	if blockedDevicePaths[path] {
		return true
	}
	// Block /proc/*/fd/0, /proc/*/fd/1, /proc/*/fd/2
	if strings.HasPrefix(path, "/proc/") {
		if strings.HasSuffix(path, "/fd/0") || strings.HasSuffix(path, "/fd/1") || strings.HasSuffix(path, "/fd/2") {
			return true
		}
	}
	return false
}

func hasBinaryExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return binaryExtensions[ext]
}

func isImageExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return imageExtensions[ext]
}

func isPDFExtension(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".pdf"
}

func ReadTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Read",
			Desc: "Reads a file from the local filesystem.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"file_path": {Type: schema.String, Desc: "The absolute path to the file to read", Required: true},
				"limit":     {Type: schema.Integer, Desc: "The number of lines to read. Only provide if the file is too large to read at once."},
				"offset":    {Type: schema.Integer, Desc: "The line number to start reading from. Only provide if the file is too large to read at once"},
				"line":      {Type: schema.Integer, Desc: "Jump to a specific line number, centering the view around that line"},
				"pages":     {Type: schema.String, Desc: "Inclusive 1-indexed PDF page range such as 1-5 or 3; maximum 20 pages per request"},
			}),
		},
		SkipResultBudget: true,
		ValidateInput:    validatePDFPagesInput,
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
		Execute: func(input string) (string, error) {
			var params struct {
				FilePath string `json:"file_path"`
				Limit    *int   `json:"limit"`
				Offset   *int   `json:"offset"`
				Line     *int   `json:"line"`
				Pages    string `json:"pages"`
			}
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("read: invalid params: %w", err)
			}
			if params.FilePath == "" {
				return "", fmt.Errorf("read: file_path is required")
			}

			// Resolve absolute path.
			fullPath := params.FilePath
			if !filepath.IsAbs(fullPath) {
				if wd, err := os.Getwd(); err == nil {
					fullPath = filepath.Join(wd, fullPath)
				}
			}
			fullPath = filepath.Clean(fullPath)

			// Block device paths.
			if isBlockedDevicePath(fullPath) {
				return "", fmt.Errorf("read: blocked device path: %s", params.FilePath)
			}

			// Binary file detection (allow images and PDFs).
			if hasBinaryExtension(fullPath) && !isImageExtension(fullPath) && !isPDFExtension(fullPath) {
				return "", fmt.Errorf("read: this tool cannot read binary files: %s", params.FilePath)
			}
			if isPDFExtension(fullPath) {
				result, err := readPDFForTool(context.Background(), params.FilePath, fullPath, params.Pages, nil)
				if err != nil {
					return "", fmt.Errorf("read: %w", err)
				}
				RecordFileRead(fullPath, false)
				return result, nil
			}

			data, err := os.ReadFile(fullPath)
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("read: file not found: %s", params.FilePath)
				}
				return "", fmt.Errorf("read: %w", err)
			}

			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)

			// Determine if this is a partial view.
			hasExplicitRange := params.Offset != nil || params.Limit != nil || params.Line != nil

			// Handle `line` parameter: center view around the specified line.
			// Mirrors TS FileReadTool `line` parameter behavior.
			offset := 0
			if params.Line != nil {
				lineNum := *params.Line
				if lineNum < 1 {
					lineNum = 1
				}
				limit := defaultLineLimit
				if params.Limit != nil && *params.Limit > 0 {
					limit = *params.Limit
				}
				// Center the view around the target line.
				offset = lineNum - 1 - limit/2
				if offset < 0 {
					offset = 0
				}
				hasExplicitRange = true
			} else if params.Offset != nil {
				offset = *params.Offset
			}

			if offset >= totalLines {
				return "", nil
			}
			lines = lines[offset:]

			// Apply limit: explicit limit > default 250.
			limit := defaultLineLimit
			if params.Limit != nil && *params.Limit > 0 {
				limit = *params.Limit
				hasExplicitRange = true
			}
			// Clamp to hard maximum.
			if limit > maxLinesToRead {
				limit = maxLinesToRead
			}
			if limit > 0 && limit < len(lines) {
				lines = lines[:limit]
				hasExplicitRange = true
			}

			// Record file-read state for Edit/Write guards.
			isPartial := hasExplicitRange && (offset > 0 || len(lines) < totalLines)
			RecordFileRead(fullPath, isPartial)

			return formatReadResult(params.FilePath, offset, lines), nil
		},
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		var params struct {
			FilePath string `json:"file_path"`
			Pages    string `json:"pages"`
		}
		if err := json.Unmarshal([]byte(input), &params); err != nil {
			return "", fmt.Errorf("read: invalid params: %w", err)
		}
		if params.FilePath == "" {
			return "", fmt.Errorf("read: file_path is required")
		}
		fullPath, err := resolveExecutionPath(ctx, params.FilePath)
		if err != nil {
			return "", fmt.Errorf("read: %w", err)
		}
		if isBlockedDevicePath(fullPath) {
			return "", fmt.Errorf("read: blocked device path: %s", params.FilePath)
		}
		if isPDFExtension(fullPath) {
			result, err := readPDFForTool(ctx, params.FilePath, fullPath, params.Pages, nil)
			if err != nil {
				return "", fmt.Errorf("read: %w", err)
			}
			RecordFileRead(fullPath, false)
			return result, nil
		}
		rewritten, err := rewriteExecutionPathInput(ctx, input, "file_path", false)
		if err != nil {
			return "", fmt.Errorf("read: %w", err)
		}
		return impl.Execute(rewritten)
	}
	return impl
}

func formatReadResult(_ string, startLine int, lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		lineNum := startLine + i + 1
		numStr := strconv.Itoa(lineNum)

		// Expand tabs to spaces for consistent terminal rendering.
		line = expandTabs(line, 4)

		// Truncate long lines.
		if len(line) > maxCharsPerLine {
			line = line[:maxCharsPerLine] + "..."
		}

		// Right-aligned line number with arrow: "     N→"
		if len(numStr) >= lineNumberPadWidth {
			b.WriteString(numStr)
		} else {
			for pad := lineNumberPadWidth - len(numStr); pad > 0; pad-- {
				b.WriteByte(' ')
			}
			b.WriteString(numStr)
		}
		b.WriteString("→")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func expandTabs(s string, tabWidth int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, ch := range s {
		if ch == '\t' {
			spaces := tabWidth - (col % tabWidth)
			for j := 0; j < spaces; j++ {
				b.WriteByte(' ')
			}
			col += spaces
		} else {
			b.WriteRune(ch)
			col++
		}
	}
	return b.String()
}
