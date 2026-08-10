package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	globMaxResults     = 100
	maxOutputCharsGlob = 100000
)

func GlobTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Glob",
			Desc: `Fast file pattern matching tool that works with any codebase size.
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths in lexical order
- Use this tool when you need to find files by name patterns`,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"pattern": {Type: schema.String, Desc: "The glob pattern to match files against", Required: true},
				"path":    {Type: schema.String, Desc: "The directory to search in. If not specified, the current working directory will be used."},
			}),
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
		Execute: func(input string) (string, error) {
			var params struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("glob: invalid params: %w", err)
			}
			if params.Pattern == "" {
				return "", fmt.Errorf("glob: pattern is required")
			}

			// Determine search directory
			searchDir := params.Path
			if searchDir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return "", fmt.Errorf("glob: cannot determine working directory: %w", err)
				}
				searchDir = cwd
			}

			// Validate path exists and is a directory
			info, err := os.Stat(searchDir)
			if err != nil {
				return "", fmt.Errorf("glob: directory does not exist: %s", searchDir)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("glob: path is not a directory: %s", searchDir)
			}

			// Handle absolute patterns by extracting base directory
			searchPattern := params.Pattern
			if filepath.IsAbs(searchPattern) {
				baseDir, relPattern := extractGlobBaseDir(searchPattern)
				if baseDir != "" {
					searchDir = baseDir
					searchPattern = relPattern
				}
			}

			// Use ripgrep for file listing with glob matching
			// This mirrors the reference implementation which uses rg --files --glob
			files, err := globWithRipgrep(searchDir, searchPattern)
			if err != nil {
				// Fall back to Go's filepath.Walk for cases where rg fails
				files, err = globWithWalk(searchDir, searchPattern)
				if err != nil {
					return "", fmt.Errorf("glob: %w", err)
				}
			}

			// Sort lexically
			sort.Strings(files)

			// Truncate results
			truncated := false
			if len(files) > globMaxResults {
				files = files[:globMaxResults]
				truncated = true
			}

			// Format output
			if len(files) == 0 {
				return "No files found", nil
			}

			var b strings.Builder
			b.WriteString(strings.Join(files, "\n"))
			if truncated {
				b.WriteString("\n(Results are truncated. Consider using a more specific path or pattern.)")
			}

			result := b.String()
			if len(result) > maxOutputCharsGlob {
				result = result[:maxOutputCharsGlob] + "\n\n[Output truncated]"
			}

			return result, nil
		},
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		rewritten, err := rewriteExecutionPathInput(ctx, input, "path", true)
		if err != nil {
			return "", fmt.Errorf("glob: %w", err)
		}
		return impl.Execute(rewritten)
	}
	return impl
}

// extractGlobBaseDir extracts the static base directory from a glob pattern.
// Returns the directory portion before any glob special characters and the remaining pattern.
func extractGlobBaseDir(pattern string) (string, string) {
	// Find the first glob special character: *, ?, [, {
	specialIdx := -1
	for i, ch := range pattern {
		if ch == '*' || ch == '?' || ch == '[' || ch == '{' {
			specialIdx = i
			break
		}
	}

	if specialIdx == -1 {
		// No glob characters - literal path
		dir := filepath.Dir(pattern)
		base := filepath.Base(pattern)
		return dir, base
	}

	// Get everything before the first glob character
	staticPrefix := pattern[:specialIdx]

	// Find the last path separator in the static prefix
	lastSep := strings.LastIndex(staticPrefix, "/")
	if lastSep == -1 {
		lastSep = strings.LastIndex(staticPrefix, string(filepath.Separator))
	}

	if lastSep == -1 {
		return "", pattern
	}

	baseDir := staticPrefix[:lastSep]
	relPattern := pattern[lastSep+1:]

	if baseDir == "" && lastSep == 0 {
		baseDir = "/"
	}

	return baseDir, relPattern
}

// globWithRipgrep uses ripgrep's --files mode with a glob pattern.
// This mirrors the reference implementation.
func globWithRipgrep(dir, pattern string) ([]string, error) {
	args := []string{
		"--files",
		"--glob", pattern,
		"--hidden",
	}

	// Exclude VCS directories
	for _, vcsDir := range vcsDirectoriesToExclude {
		args = append(args, "--glob", "!"+vcsDir)
	}

	args = append(args, dir)

	cmd := exec.Command(rgPath, args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Exit code 1 = no matches (normal)
			if exitErr.ExitCode() == 1 {
				return nil, nil
			}
			return nil, fmt.Errorf("ripgrep error (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, err
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" {
		return nil, nil
	}

	lines := strings.Split(raw, "\n")
	results := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			results = append(results, line)
		}
	}

	return results, nil
}

// globWithWalk is a fallback that uses Go's filepath.Walk for glob matching.
// It supports ** patterns via manual recursive matching.
func globWithWalk(dir, pattern string) ([]string, error) {
	var results []string

	// Common directories to skip
	skipDirs := map[string]bool{
		".git":         true,
		".svn":         true,
		".hg":          true,
		"node_modules": true,
		".bzr":         true,
		".jj":          true,
		".sl":          true,
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}

		// Skip common noise directories
		if info.IsDir() && skipDirs[info.Name()] {
			return filepath.SkipDir
		}

		if info.IsDir() {
			return nil
		}

		// Get relative path from search dir
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}

		// Match against pattern
		matched, err := matchGlob(pattern, relPath)
		if err != nil || !matched {
			return nil
		}

		results = append(results, path)
		return nil
	})

	return results, err
}

// matchGlob matches a pattern against a path, supporting ** for recursive matching.
func matchGlob(pattern, path string) (bool, error) {
	// Handle ** patterns by converting to a simpler check
	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, path), nil
	}
	return filepath.Match(pattern, path)
}

// matchDoublestar handles glob patterns with ** (recursive directory matching).
func matchDoublestar(pattern, path string) bool {
	// Split pattern and path into components
	patternParts := strings.Split(filepath.ToSlash(pattern), "/")
	pathParts := strings.Split(filepath.ToSlash(path), "/")

	return matchParts(patternParts, pathParts)
}

func matchParts(patternParts, pathParts []string) bool {
	if len(patternParts) == 0 {
		return len(pathParts) == 0
	}

	if patternParts[0] == "**" {
		// ** matches zero or more path segments
		remainingPattern := patternParts[1:]

		// Try matching ** against 0, 1, 2, ... path segments
		for i := 0; i <= len(pathParts); i++ {
			if matchParts(remainingPattern, pathParts[i:]) {
				return true
			}
		}
		return false
	}

	if len(pathParts) == 0 {
		return false
	}

	// Match current segment using filepath.Match
	matched, err := filepath.Match(patternParts[0], pathParts[0])
	if err != nil || !matched {
		return false
	}

	return matchParts(patternParts[1:], pathParts[1:])
}
