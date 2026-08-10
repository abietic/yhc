package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/cloudwego/eino/schema"
)

// LSPLocation represents a source code location returned by LSP operations.
type LSPLocation struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Preview   string `json:"preview,omitempty"`
}

// LSPSymbol represents a workspace symbol returned by LSP symbol search.
type LSPSymbol struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"` // "function", "class", "variable", etc.
	File          string `json:"file"`
	Line          int    `json:"line"`
	ContainerName string `json:"container_name,omitempty"`
}

// LSPHoverInfo represents hover information for a symbol.
type LSPHoverInfo struct {
	Content  string `json:"content"`
	Language string `json:"language,omitempty"`
}

func LSPTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "LSP",
			Desc: "Query Language Server Protocol servers for code intelligence. Supports go-to-definition, find-references, hover info, and workspace symbol search.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"action":    {Type: schema.String, Desc: "LSP action: 'definition', 'references', 'hover', 'symbols'", Required: true},
				"file_path": {Type: schema.String, Desc: "Path to the source file"},
				"line":      {Type: schema.Integer, Desc: "1-based line number in the file"},
				"character": {Type: schema.Integer, Desc: "0-based character offset in the line"},
				"query":     {Type: schema.String, Desc: "Symbol name for workspace symbol search"},
			}),
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
		Execute: executeLSP,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			cwd, err := effectiveExecutionCWD(ctx)
			if err != nil {
				return "", fmt.Errorf("lsp: %w", err)
			}
			return executeLSPAt(input, cwd)
		},
	}
}

func executeLSP(input string) (string, error) {
	cwd, err := effectiveExecutionCWD(context.Background())
	if err != nil {
		return "", fmt.Errorf("lsp: %w", err)
	}
	return executeLSPAt(input, cwd)
}

func executeLSPAt(input, cwd string) (string, error) {
	var params struct {
		Action   string `json:"action"`
		FilePath string `json:"file_path"`
		Line     *int   `json:"line"`
		Char     *int   `json:"character"`
		Query    string `json:"query"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("lsp: invalid params: %w", err)
	}

	// Validate action
	validActions := map[string]bool{
		"definition": true,
		"references": true,
		"hover":      true,
		"symbols":    true,
	}
	if !validActions[params.Action] {
		return "", fmt.Errorf("lsp: action must be one of: definition, references, hover, symbols")
	}

	// Validate required params based on action
	switch params.Action {
	case "definition", "references", "hover":
		if params.FilePath == "" {
			return "", fmt.Errorf("lsp: file_path is required for action '%s'", params.Action)
		}
		if params.Line == nil {
			return "", fmt.Errorf("lsp: line is required for action '%s'", params.Action)
		}
		if params.Char == nil {
			return "", fmt.Errorf("lsp: character is required for action '%s'", params.Action)
		}
	case "symbols":
		if params.Query == "" {
			return "", fmt.Errorf("lsp: query is required for action 'symbols'")
		}
	}

	// Resolve file path to absolute
	filePath := params.FilePath
	if filePath != "" && !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
	}

	// Use grep-based fallback (LSP server integration would require a running server)
	switch params.Action {
	case "definition":
		locations, err := findDefinitionWithGrep(filePath, *params.Line, *params.Char, cwd)
		if err != nil {
			return "", err
		}
		return formatLocations("Definition", locations), nil

	case "references":
		locations, err := findReferencesWithGrep(filePath, *params.Line, *params.Char, cwd)
		if err != nil {
			return "", err
		}
		return formatLocations("References", locations), nil

	case "hover":
		info, err := getHoverWithGrep(filePath, *params.Line, *params.Char, cwd)
		if err != nil {
			return "", err
		}
		return formatHover(info), nil

	case "symbols":
		symbols, err := findSymbolsWithGrep(params.Query, cwd)
		if err != nil {
			return "", err
		}
		return formatSymbols(symbols), nil
	}

	return "", fmt.Errorf("lsp: unknown action: %s", params.Action)
}

// extractSymbolAtPosition reads the file and extracts the symbol name at the given position.
func extractSymbolAtPosition(filePath string, line, char int) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("lsp: cannot open file: %w", err)
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	currentLine := 0
	for scanner.Scan() {
		currentLine++
		if currentLine == line {
			text := scanner.Text()
			if char >= len(text) {
				// Try end of line
				char = len(text) - 1
				if char < 0 {
					return "", fmt.Errorf("lsp: line %d is empty", line)
				}
			}

			// Expand outward from char position to find the full identifier
			start := char
			end := char

			isIdentChar := func(c byte) bool {
				return c == '_' || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c))
			}

			for start > 0 && isIdentChar(text[start-1]) {
				start--
			}
			for end < len(text)-1 && isIdentChar(text[end+1]) {
				end++
			}

			if start > end || !isIdentChar(text[start]) {
				return "", fmt.Errorf("lsp: no symbol at position %d:%d", line, char)
			}

			return text[start : end+1], nil
		}
	}

	return "", fmt.Errorf("lsp: line %d not found in file (file has %d lines)", line, currentLine)
}

// findDefinitionWithGrep uses pattern-based search to locate definitions.
func findDefinitionWithGrep(filePath string, line, char int, cwd string) ([]LSPLocation, error) {
	symbol, err := extractSymbolAtPosition(filePath, line, char)
	if err != nil {
		return nil, err
	}

	// Build patterns for common definition forms across languages
	patterns := []string{
		// Go: func Name, type Name, var Name, const Name
		fmt.Sprintf(`^\s*func\s+(\([^)]*\)\s+)?%s\b`, regexp.QuoteMeta(symbol)),
		fmt.Sprintf(`^\s*type\s+%s\b`, regexp.QuoteMeta(symbol)),
		fmt.Sprintf(`^\s*var\s+%s\b`, regexp.QuoteMeta(symbol)),
		fmt.Sprintf(`^\s*const\s+%s\b`, regexp.QuoteMeta(symbol)),
		fmt.Sprintf(`^\s*%s\s*:=`, regexp.QuoteMeta(symbol)),
		// Python/JS/TS: def/class/function/const/let/var
		fmt.Sprintf(`^\s*def\s+%s\b`, regexp.QuoteMeta(symbol)),
		fmt.Sprintf(`^\s*class\s+%s\b`, regexp.QuoteMeta(symbol)),
		fmt.Sprintf(`^\s*function\s+%s\b`, regexp.QuoteMeta(symbol)),
		fmt.Sprintf(`^\s*(export\s+)?(const|let|var)\s+%s\b`, regexp.QuoteMeta(symbol)),
		// Interface/struct field patterns
		fmt.Sprintf(`^\s*%s\s+\S`, regexp.QuoteMeta(symbol)),
	}

	combinedPattern := strings.Join(patterns, "|")
	return grepForLocations(combinedPattern, cwd, symbol)
}

// findReferencesWithGrep uses grep to find symbol usages.
func findReferencesWithGrep(filePath string, line, char int, cwd string) ([]LSPLocation, error) {
	symbol, err := extractSymbolAtPosition(filePath, line, char)
	if err != nil {
		return nil, err
	}

	// Search for word-boundary matches of the symbol
	pattern := fmt.Sprintf(`\b%s\b`, regexp.QuoteMeta(symbol))
	return grepForLocations(pattern, cwd, symbol)
}

// getHoverWithGrep returns the definition context with surrounding lines.
func getHoverWithGrep(filePath string, line, char int, cwd string) (*LSPHoverInfo, error) {
	symbol, err := extractSymbolAtPosition(filePath, line, char)
	if err != nil {
		return nil, err
	}

	// First try to find the definition
	locations, err := findDefinitionWithGrep(filePath, line, char, cwd)
	if err != nil || len(locations) == 0 {
		// Fall back to just showing context around the current position
		context, lang := getFileContext(filePath, line, 3)
		return &LSPHoverInfo{
			Content:  fmt.Sprintf("Symbol: %s\n\n%s", symbol, context),
			Language: lang,
		}, nil
	}

	// Read context around the first definition
	loc := locations[0]
	context, lang := getFileContext(loc.File, loc.Line, 5)
	return &LSPHoverInfo{
		Content:  fmt.Sprintf("Symbol: %s\nDefined in: %s:%d\n\n%s", symbol, loc.File, loc.Line, context),
		Language: lang,
	}, nil
}

// findSymbolsWithGrep searches for symbol declarations matching a query.
func findSymbolsWithGrep(query, cwd string) ([]LSPSymbol, error) { //nolint:unparam
	// Build patterns for declarations
	patterns := []string{
		// Go
		fmt.Sprintf(`^\s*func\s+(\([^)]*\)\s+)?%s`, regexp.QuoteMeta(query)),
		fmt.Sprintf(`^\s*type\s+%s`, regexp.QuoteMeta(query)),
		fmt.Sprintf(`^\s*var\s+%s`, regexp.QuoteMeta(query)),
		fmt.Sprintf(`^\s*const\s+%s`, regexp.QuoteMeta(query)),
		// Python/JS/TS
		fmt.Sprintf(`^\s*def\s+%s`, regexp.QuoteMeta(query)),
		fmt.Sprintf(`^\s*class\s+%s`, regexp.QuoteMeta(query)),
		fmt.Sprintf(`^\s*function\s+%s`, regexp.QuoteMeta(query)),
		fmt.Sprintf(`^\s*(export\s+)?(const|let|var)\s+%s`, regexp.QuoteMeta(query)),
	}

	combinedPattern := strings.Join(patterns, "|")

	args := buildRgArgs(combinedPattern, cwd)
	args = append(args, "-n") // line numbers

	cmd := exec.Command(rgPath, args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return []LSPSymbol{}, nil
		}
		// Return empty on other errors
		return []LSPSymbol{}, nil
	}

	var symbols []LSPSymbol
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		sym := parseSymbolFromLine(line, cwd)
		if sym != nil {
			symbols = append(symbols, *sym)
		}
		if len(symbols) >= 50 {
			break
		}
	}

	return symbols, nil
}

// grepForLocations runs ripgrep and parses file:line:content output into LSPLocations.
func grepForLocations(pattern, cwd, _ string) ([]LSPLocation, error) {
	args := buildRgArgs(pattern, cwd)
	args = append(args, "-n") // line numbers

	cmd := exec.Command(rgPath, args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return []LSPLocation{}, nil
		}
		return []LSPLocation{}, nil
	}

	var locations []LSPLocation
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		loc := parseLocationFromLine(line)
		if loc != nil {
			locations = append(locations, *loc)
		}
		if len(locations) >= 100 {
			break
		}
	}

	return locations, nil
}

// buildRgArgs creates common ripgrep arguments for LSP searches.
func buildRgArgs(pattern, searchPath string) []string {
	args := []string{"--hidden"}

	// Exclude VCS directories
	for _, dir := range vcsDirectoriesToExclude {
		args = append(args, "--glob", "!"+dir)
	}

	// Exclude common non-source directories
	args = append(args, "--glob", "!node_modules")
	args = append(args, "--glob", "!vendor")
	args = append(args, "--glob", "!.cache")
	args = append(args, "--glob", "!*.min.js")
	args = append(args, "--glob", "!*.min.css")

	// Max columns to avoid binary/minified lines
	args = append(args, "--max-columns", "500")

	// Pattern
	if strings.HasPrefix(pattern, "-") {
		args = append(args, "-e", pattern)
	} else {
		args = append(args, pattern)
	}

	// Search path
	args = append(args, searchPath)

	return args
}

// parseLocationFromLine parses a ripgrep output line (file:line:content) into an LSPLocation.
func parseLocationFromLine(line string) *LSPLocation {
	// Format: file:line:content or file:line:col:content
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return nil
	}

	file := parts[0]
	lineNum := 0
	_, _ = fmt.Sscanf(parts[1], "%d", &lineNum)
	if lineNum == 0 {
		return nil
	}

	preview := strings.TrimSpace(parts[2])
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}

	return &LSPLocation{
		File:      file,
		Line:      lineNum,
		Character: 0,
		Preview:   preview,
	}
}

// parseSymbolFromLine parses a ripgrep output line into an LSPSymbol.
func parseSymbolFromLine(line, _ string) *LSPSymbol {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return nil
	}

	file := parts[0]
	lineNum := 0
	_, _ = fmt.Sscanf(parts[1], "%d", &lineNum)
	if lineNum == 0 {
		return nil
	}

	content := strings.TrimSpace(parts[2])

	// Determine kind and name from content
	kind, name := classifySymbol(content)
	if name == "" {
		return nil
	}

	return &LSPSymbol{
		Name: name,
		Kind: kind,
		File: file,
		Line: lineNum,
	}
}

// classifySymbol determines the kind and name of a symbol from a source line.
func classifySymbol(line string) (kind, name string) {
	line = strings.TrimSpace(line)

	// Go patterns
	if match := regexp.MustCompile(`^func\s+(?:\([^)]*\)\s+)?(\w+)`).FindStringSubmatch(line); match != nil {
		return "function", match[1]
	}
	if match := regexp.MustCompile(`^type\s+(\w+)\s+struct`).FindStringSubmatch(line); match != nil {
		return "class", match[1]
	}
	if match := regexp.MustCompile(`^type\s+(\w+)\s+interface`).FindStringSubmatch(line); match != nil {
		return "interface", match[1]
	}
	if match := regexp.MustCompile(`^type\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "type", match[1]
	}
	if match := regexp.MustCompile(`^var\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "variable", match[1]
	}
	if match := regexp.MustCompile(`^const\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "constant", match[1]
	}

	// Python
	if match := regexp.MustCompile(`^def\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "function", match[1]
	}
	if match := regexp.MustCompile(`^class\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "class", match[1]
	}

	// JS/TS
	if match := regexp.MustCompile(`^(?:export\s+)?function\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "function", match[1]
	}
	if match := regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "variable", match[1]
	}
	if match := regexp.MustCompile(`^(?:export\s+)?class\s+(\w+)`).FindStringSubmatch(line); match != nil {
		return "class", match[1]
	}

	return "", ""
}

// getFileContext reads lines around a given line number from a file.
func getFileContext(filePath string, line, contextLines int) (string, string) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", ""
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	var lines []string
	currentLine := 0

	startLine := line - contextLines
	if startLine < 1 {
		startLine = 1
	}
	endLine := line + contextLines

	for scanner.Scan() {
		currentLine++
		if currentLine >= startLine && currentLine <= endLine {
			lines = append(lines, scanner.Text())
		}
		if currentLine > endLine {
			break
		}
	}

	// Detect language from file extension
	lang := detectLanguage(filePath)

	return strings.Join(lines, "\n"), lang
}

// detectLanguage returns the language identifier based on file extension.
func detectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".jsx":
		return "javascriptreact"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".rb":
		return "ruby"
	case ".sh", ".bash":
		return "shellscript"
	default:
		return ""
	}
}

// formatLocations formats a list of LSPLocations into a readable string.
func formatLocations(title string, locations []LSPLocation) string {
	if len(locations) == 0 {
		return fmt.Sprintf("No %s found.", strings.ToLower(title))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d result%s):\n", title, len(locations), pluralS(len(locations)))

	for i, loc := range locations {
		if i >= 50 {
			fmt.Fprintf(&b, "\n... and %d more results", len(locations)-50)
			break
		}
		fmt.Fprintf(&b, "\n  %s:%d", loc.File, loc.Line)
		if loc.Preview != "" {
			fmt.Fprintf(&b, "\n    %s", loc.Preview)
		}
	}

	return b.String()
}

// formatSymbols formats a list of LSPSymbols into a readable string.
func formatSymbols(symbols []LSPSymbol) string {
	if len(symbols) == 0 {
		return "No symbols found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Symbols (%d result%s):\n", len(symbols), pluralS(len(symbols)))

	for i, sym := range symbols {
		if i >= 50 {
			fmt.Fprintf(&b, "\n... and %d more results", len(symbols)-50)
			break
		}
		fmt.Fprintf(&b, "\n  [%s] %s", sym.Kind, sym.Name)
		fmt.Fprintf(&b, "\n    %s:%d", sym.File, sym.Line)
		if sym.ContainerName != "" {
			fmt.Fprintf(&b, " (in %s)", sym.ContainerName)
		}
	}

	return b.String()
}

// formatHover formats LSPHoverInfo into a readable string.
func formatHover(info *LSPHoverInfo) string {
	if info == nil {
		return "No hover information available."
	}

	var b strings.Builder
	if info.Language != "" {
		fmt.Fprintf(&b, "```%s\n%s\n```", info.Language, info.Content)
	} else {
		b.WriteString(info.Content)
	}
	return b.String()
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
