package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// rgPath is the path to the ripgrep binary.
// It checks the bundled location first, then falls back to system PATH.
var rgPath = findRgPath()

func findRgPath() string {
	// Check bundled ripgrep location (matches claude-code-ripe layout)
	home, _ := os.UserHomeDir()
	if home != "" {
		bundled := filepath.Join(home, ".cache", "coco", "ripgrep", "rg")
		if _, err := os.Stat(bundled); err == nil {
			return bundled
		}
	}
	// Fall back to system rg
	if p, err := exec.LookPath("rg"); err == nil {
		return p
	}
	return "rg"
}

// VCS directories excluded from grep searches to avoid noise.
var vcsDirectoriesToExclude = []string{
	".git", ".svn", ".hg", ".bzr", ".jj", ".sl",
}

const (
	defaultHeadLimit   = 250
	maxOutputCharsGrep = 20000
	defaultOutputMode  = "files_with_matches"
)

func GrepTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Grep",
			Desc: `A powerful search tool built on ripgrep.

Usage:
- Supports full regex syntax (e.g., "log.*Error", "function\\s+\\w+")
- Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or type parameter (e.g., "js", "py", "rust")
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
- Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping
- Multiline matching: By default patterns match within single lines only. For cross-line patterns, use multiline: true`,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"pattern":     {Type: schema.String, Desc: "The regular expression pattern to search for in file contents", Required: true},
				"path":        {Type: schema.String, Desc: "File or directory to search in (rg PATH). Defaults to current working directory."},
				"glob":        {Type: schema.String, Desc: `Glob pattern to filter files (e.g. "*.js", "*.{ts,tsx}") - maps to rg --glob`},
				"output_mode": {Type: schema.String, Desc: `Output mode: "content" shows matching lines, "files_with_matches" shows file paths (default), "count" shows match counts.`},
				"-B":          {Type: schema.Integer, Desc: "Number of lines to show before each match (rg -B). Requires output_mode: \"content\"."},
				"-A":          {Type: schema.Integer, Desc: "Number of lines to show after each match (rg -A). Requires output_mode: \"content\"."},
				"-C":          {Type: schema.Integer, Desc: "Number of lines to show before and after each match (rg -C). Requires output_mode: \"content\". Alias for context."},
				"context":     {Type: schema.Integer, Desc: "Number of lines to show before and after each match (rg -C). Requires output_mode: \"content\", ignored otherwise."},
				"-n":          {Type: schema.Boolean, Desc: "Show line numbers in output (rg -n). Requires output_mode: \"content\". Defaults to true."},
				"-i":          {Type: schema.Boolean, Desc: "Case insensitive search (rg -i)"},
				"type":        {Type: schema.String, Desc: "File type to search (rg --type). Common types: js, py, rust, go, java, etc."},
				"head_limit":  {Type: schema.Integer, Desc: `Limit output to first N lines/entries. Defaults to 250. Pass 0 for unlimited.`},
				"offset":      {Type: schema.Integer, Desc: "Skip first N lines/entries before applying head_limit. Defaults to 0."},
				"multiline":   {Type: schema.Boolean, Desc: "Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false."},
			}),
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
		Execute: func(input string) (string, error) {
			var params struct {
				Pattern      string `json:"pattern"`
				Path         string `json:"path"`
				Glob         string `json:"glob"`
				OutputMode   string `json:"output_mode"`
				Before       *int   `json:"-B"`
				After        *int   `json:"-A"`
				ContextC     *int   `json:"-C"`
				ContextAlias *int   `json:"context"`
				LineNums     *bool  `json:"-n"`
				CaseInsens   *bool  `json:"-i"`
				Type         string `json:"type"`
				HeadLimit    *int   `json:"head_limit"`
				Offset       *int   `json:"offset"`
				Multiline    *bool  `json:"multiline"`
			}
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("grep: invalid params: %w", err)
			}
			if params.Pattern == "" {
				return "", fmt.Errorf("grep: pattern is required")
			}

			outputMode := params.OutputMode
			if outputMode == "" {
				outputMode = defaultOutputMode
			}

			// Determine search path
			searchPath := params.Path
			if searchPath == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return "", fmt.Errorf("grep: cannot determine working directory: %w", err)
				}
				searchPath = cwd
			}

			// Validate path exists
			if _, err := os.Stat(searchPath); err != nil {
				return "", fmt.Errorf("grep: path does not exist: %s", searchPath)
			}

			// Build rg arguments
			args := []string{"--hidden"}

			// Exclude VCS directories
			for _, dir := range vcsDirectoriesToExclude {
				args = append(args, "--glob", "!"+dir)
			}

			// Limit line length to prevent base64/minified content from cluttering output
			args = append(args, "--max-columns", "500")

			// Multiline mode
			multiline := false
			if params.Multiline != nil && *params.Multiline {
				multiline = true
				args = append(args, "-U", "--multiline-dotall")
			}
			_ = multiline

			// Case insensitive
			if params.CaseInsens != nil && *params.CaseInsens {
				args = append(args, "-i")
			}

			// Output mode flags
			switch outputMode {
			case "files_with_matches":
				args = append(args, "-l")
			case "count":
				args = append(args, "-c")
			}

			// Line numbers (default true for content mode)
			showLineNumbers := true
			if params.LineNums != nil {
				showLineNumbers = *params.LineNums
			}
			if showLineNumbers && outputMode == "content" {
				args = append(args, "-n")
			}

			// Context flags (only for content mode). context takes precedence over -C.
			if outputMode == "content" {
				if params.ContextAlias != nil {
					args = append(args, "-C", strconv.Itoa(*params.ContextAlias))
				} else if params.ContextC != nil {
					args = append(args, "-C", strconv.Itoa(*params.ContextC))
				} else {
					if params.Before != nil {
						args = append(args, "-B", strconv.Itoa(*params.Before))
					}
					if params.After != nil {
						args = append(args, "-A", strconv.Itoa(*params.After))
					}
				}
			}

			// Pattern (handle patterns starting with dash)
			if strings.HasPrefix(params.Pattern, "-") {
				args = append(args, "-e", params.Pattern)
			} else {
				args = append(args, params.Pattern)
			}

			// Type filter
			if params.Type != "" {
				args = append(args, "--type", params.Type)
			}

			// Glob filter
			if params.Glob != "" {
				// Split on spaces, preserving brace patterns
				rawPatterns := strings.Fields(params.Glob)
				for _, raw := range rawPatterns {
					if strings.Contains(raw, "{") && strings.Contains(raw, "}") {
						args = append(args, "--glob", raw)
					} else {
						// Split on commas for patterns without braces
						for _, p := range strings.Split(raw, ",") {
							if p != "" {
								args = append(args, "--glob", p)
							}
						}
					}
				}
			}

			// Add search path as the last argument
			args = append(args, searchPath)

			// Execute ripgrep
			cmd := exec.Command(rgPath, args...)
			output, err := cmd.Output()
			// rg exit code 1 means no matches (not an error)
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					if exitErr.ExitCode() == 1 {
						// No matches
						return formatGrepNoMatches(outputMode), nil
					}
					// Exit code 2 = usage/pattern error
					stderr := string(exitErr.Stderr)
					if exitErr.ExitCode() == 2 {
						return "", fmt.Errorf("grep: ripgrep error: %s", strings.TrimSpace(stderr))
					}
					// Other errors - return partial results if available
					if len(output) == 0 {
						return "", fmt.Errorf("grep: ripgrep failed (exit %d): %s", exitErr.ExitCode(), strings.TrimSpace(stderr))
					}
				} else {
					return "", fmt.Errorf("grep: failed to execute ripgrep: %w", err)
				}
			}

			// Parse results
			rawOutput := strings.TrimSpace(string(output))
			if rawOutput == "" {
				return formatGrepNoMatches(outputMode), nil
			}

			lines := strings.Split(rawOutput, "\n")
			// Clean up CR characters
			for i, line := range lines {
				lines[i] = strings.TrimRight(line, "\r")
			}

			// Apply head_limit and offset
			headLimit := defaultHeadLimit
			if params.HeadLimit != nil {
				if *params.HeadLimit == 0 {
					headLimit = 0 // unlimited
				} else {
					headLimit = *params.HeadLimit
				}
			}
			offset := 0
			if params.Offset != nil {
				offset = *params.Offset
			}

			// Apply offset
			if offset > 0 {
				if offset >= len(lines) {
					return formatGrepNoMatches(outputMode), nil
				}
				lines = lines[offset:]
			}

			// Apply head_limit (0 = unlimited)
			truncated := false
			if headLimit > 0 && len(lines) > headLimit {
				lines = lines[:headLimit]
				truncated = true
			}

			// Format output based on mode
			result := formatGrepResult(outputMode, lines, truncated, headLimit, offset)

			// Truncate if too long
			if len(result) > maxOutputCharsGrep {
				result = result[:maxOutputCharsGrep] + "\n\n[Output truncated at 20000 characters]"
			}

			return result, nil
		},
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		rewritten, err := rewriteExecutionPathInput(ctx, input, "path", true)
		if err != nil {
			return "", fmt.Errorf("grep: %w", err)
		}
		return impl.Execute(rewritten)
	}
	return impl
}

func formatGrepNoMatches(mode string) string {
	switch mode {
	case "content":
		return "No matches found"
	case "count":
		return "No matches found"
	default:
		return "No files found"
	}
}

func formatGrepResult(mode string, lines []string, truncated bool, limit, offset int) string {
	var b strings.Builder

	switch mode {
	case "content":
		b.WriteString(strings.Join(lines, "\n"))
		if truncated {
			fmt.Fprintf(&b, "\n\n[Showing results with pagination = limit: %d", limit)
			if offset > 0 {
				fmt.Fprintf(&b, ", offset: %d", offset)
			}
			b.WriteString("]")
		}

	case "count":
		// Parse count output to extract total matches and file count
		totalMatches := 0
		fileCount := 0
		for _, line := range lines {
			colonIdx := strings.LastIndex(line, ":")
			if colonIdx > 0 {
				countStr := line[colonIdx+1:]
				if c, err := strconv.Atoi(strings.TrimSpace(countStr)); err == nil {
					totalMatches += c
					fileCount++
				}
			}
		}

		b.WriteString(strings.Join(lines, "\n"))
		fmt.Fprintf(&b, "\n\nFound %d total %s across %d %s.",
			totalMatches, plural(totalMatches, "occurrence", "occurrences"),
			fileCount, plural(fileCount, "file", "files"))
		if truncated {
			fmt.Fprintf(&b, " with pagination = limit: %d", limit)
			if offset > 0 {
				fmt.Fprintf(&b, ", offset: %d", offset)
			}
		}

	default: // files_with_matches
		numFiles := len(lines)
		fmt.Fprintf(&b, "Found %d %s", numFiles, plural(numFiles, "file", "files"))
		if truncated {
			fmt.Fprintf(&b, " (limit: %d", limit)
			if offset > 0 {
				fmt.Fprintf(&b, ", offset: %d", offset)
			}
			b.WriteString(")")
		}
		b.WriteString("\n")
		b.WriteString(strings.Join(lines, "\n"))
	}

	return b.String()
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
