package promptctx

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ClaudeMdFile represents a discovered CLAUDE.md file.
type ClaudeMdFile struct {
	Path    string
	Content string
	Source  string // "project", "project-dot-claude", "user", "parent"
}

// cacheEntry holds a cached file's content and metadata for staleness detection.
type cacheEntry struct {
	content string
	modTime time.Time
	size    int64
}

// ClaudeMdCache provides simple file-content caching to avoid re-reading files
// on every turn. Entries are invalidated when the file modification time changes.
type ClaudeMdCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

// NewClaudeMdCache creates a new empty cache.
func NewClaudeMdCache() *ClaudeMdCache {
	return &ClaudeMdCache{
		entries: make(map[string]*cacheEntry),
	}
}

// Get retrieves a cached file content. Returns the content and true if the
// cached entry is still valid (file mod time unchanged), or empty string and
// false if the cache is stale or has no entry.
func (c *ClaudeMdCache) Get(path string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.entries[path]
	c.mu.RUnlock()

	if !ok {
		return "", false
	}

	info, err := os.Stat(path)
	if err != nil {
		// File gone — remove from cache
		c.mu.Lock()
		delete(c.entries, path)
		c.mu.Unlock()
		return "", false
	}

	if info.ModTime().Equal(entry.modTime) && info.Size() == entry.size {
		return entry.content, true
	}

	// Stale
	c.mu.Lock()
	delete(c.entries, path)
	c.mu.Unlock()
	return "", false
}

// Set stores a file's content in the cache along with its current stat info.
func (c *ClaudeMdCache) Set(path, content string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}

	c.mu.Lock()
	c.entries[path] = &cacheEntry{
		content: content,
		modTime: info.ModTime(),
		size:    info.Size(),
	}
	c.mu.Unlock()
}

// Invalidate removes a single entry from the cache.
func (c *ClaudeMdCache) Invalidate(path string) {
	c.mu.Lock()
	delete(c.entries, path)
	c.mu.Unlock()
}

// Clear removes all entries from the cache.
func (c *ClaudeMdCache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string]*cacheEntry)
	c.mu.Unlock()
}

// defaultCache is a package-level cache used by the convenience functions.
var defaultCache = NewClaudeMdCache()

// DiscoverClaudeMds finds all applicable CLAUDE.md files for the given working
// directory. Files are returned in priority order (lowest to highest):
//
//  1. User home: ~/.claude/CLAUDE.md
//  2. Parent directories: walking from root down to CWD (for monorepo support)
//  3. Project root .claude dir: <cwd>/.claude/CLAUDE.md
//  4. Project root: <cwd>/CLAUDE.md
//
// Missing files are silently skipped.
func DiscoverClaudeMds(cwd string) ([]ClaudeMdFile, error) {
	return DiscoverClaudeMdsWithCache(cwd, defaultCache)
}

// DiscoverClaudeMdsWithCache is like DiscoverClaudeMds but uses a provided cache.
func DiscoverClaudeMdsWithCache(cwd string, cache *ClaudeMdCache) ([]ClaudeMdFile, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolving cwd: %w", err)
	}

	var files []ClaudeMdFile

	// 0. Managed path: /etc/claude-code/CLAUDE.md (organizational policies, highest priority source loaded first)
	managedPath := "/etc/claude-code/CLAUDE.md"
	if f := readClaudeMdFile(managedPath, "managed", cache); f != nil {
		f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
		files = append(files, *f)
	}

	// 1. User home: ~/.claude/CLAUDE.md
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".claude", "CLAUDE.md")
		if f := readClaudeMdFile(userPath, "user", cache); f != nil {
			f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
			files = append(files, *f)
		}
		// User rules: ~/.claude/rules/*.md
		userRules := loadRulesDir(filepath.Join(home, ".claude", "rules"), "user-rule", cache)
		files = append(files, userRules...)
	}

	// 2. Parent directories: walk from root toward CWD.
	// Collect dirs from CWD up to root, then reverse to walk root-to-CWD.
	parentDirs := collectParentDirs(absCwd)
	for _, dir := range parentDirs {
		// CLAUDE.md in parent dir
		parentPath := filepath.Join(dir, "CLAUDE.md")
		if f := readClaudeMdFile(parentPath, "parent", cache); f != nil {
			f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
			files = append(files, *f)
		}
		// CLAUDE.local.md in parent dir
		parentLocalPath := filepath.Join(dir, "CLAUDE.local.md")
		if f := readClaudeMdFile(parentLocalPath, "parent-local", cache); f != nil {
			f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
			files = append(files, *f)
		}
		// .claude/CLAUDE.md in parent dir
		parentDotPath := filepath.Join(dir, ".claude", "CLAUDE.md")
		if f := readClaudeMdFile(parentDotPath, "parent", cache); f != nil {
			f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
			files = append(files, *f)
		}
	}

	// 3. Project .claude dir: <cwd>/.claude/CLAUDE.md
	dotClaudePath := filepath.Join(absCwd, ".claude", "CLAUDE.md")
	if f := readClaudeMdFile(dotClaudePath, "project-dot-claude", cache); f != nil {
		f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
		files = append(files, *f)
	}

	// 4. Project root: <cwd>/CLAUDE.md (highest priority — loaded last)
	projectPath := filepath.Join(absCwd, "CLAUDE.md")
	if f := readClaudeMdFile(projectPath, "project", cache); f != nil {
		f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
		files = append(files, *f)
	}

	// 5. Project CLAUDE.local.md (private project-level overrides, not committed)
	projectLocalPath := filepath.Join(absCwd, "CLAUDE.local.md")
	if f := readClaudeMdFile(projectLocalPath, "project-local", cache); f != nil {
		f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
		files = append(files, *f)
	}
	dotClaudeLocalPath := filepath.Join(absCwd, ".claude", "CLAUDE.local.md")
	if f := readClaudeMdFile(dotClaudeLocalPath, "project-local", cache); f != nil {
		f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
		files = append(files, *f)
	}

	// 6. Project rules: <cwd>/.claude/rules/*.md
	projectRules := loadRulesDir(filepath.Join(absCwd, ".claude", "rules"), "project-rule", cache)
	files = append(files, projectRules...)

	return files, nil
}

// DiscoverAgentsMds finds AGENTS.md files (and all .md files) in the
// .claude/agents/ directory relative to cwd. This mirrors the reference
// implementation's loadMarkdownFilesForSubdir("agents", cwd).
func DiscoverAgentsMds(cwd string) ([]ClaudeMdFile, error) {
	return DiscoverAgentsMdsWithCache(cwd, defaultCache)
}

// DiscoverAgentsMdsWithCache is like DiscoverAgentsMds but uses a provided cache.
func DiscoverAgentsMdsWithCache(cwd string, cache *ClaudeMdCache) ([]ClaudeMdFile, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolving cwd: %w", err)
	}

	agentsDir := filepath.Join(absCwd, ".claude", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading agents dir: %w", err)
	}

	var files []ClaudeMdFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fullPath := filepath.Join(agentsDir, entry.Name())
		if f := readClaudeMdFile(fullPath, "agent", cache); f != nil {
			files = append(files, *f)
		}
	}
	return files, nil
}

// LoadClaudeMdContent loads and concatenates CLAUDE.md content in priority order.
// Content from each file is separated by clear section markers.
func LoadClaudeMdContent(cwd string) (string, error) {
	return LoadClaudeMdContentWithCache(cwd, defaultCache)
}

// LoadClaudeMdContentWithCache is like LoadClaudeMdContent but uses a provided cache.
func LoadClaudeMdContentWithCache(cwd string, cache *ClaudeMdCache) (string, error) {
	files, err := DiscoverClaudeMdsWithCache(cwd, cache)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	return FormatClaudeMdContent(files), nil
}

// FormatClaudeMdContent formats discovered CLAUDE.md files into a single string
// with clear section separators, suitable for inclusion in a system prompt.
func FormatClaudeMdContent(files []ClaudeMdFile) string {
	if len(files) == 0 {
		return ""
	}

	const header = "Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written."

	var sections []string
	for _, f := range files {
		trimmed := strings.TrimSpace(f.Content)
		if trimmed == "" {
			continue
		}
		desc := sourceDescription(f.Source)
		sections = append(sections, fmt.Sprintf("Contents of %s%s:\n\n%s", f.Path, desc, trimmed))
	}

	if len(sections) == 0 {
		return ""
	}

	return header + "\n\n" + strings.Join(sections, "\n\n")
}

// ComposeSystemPromptWithClaudeMd composes the full system prompt by inserting
// CLAUDE.md content between the base prompt and dynamic context. This extends
// the existing ComposeSystemPrompt with CLAUDE.md awareness.
func ComposeSystemPromptWithClaudeMd(base, appendPrompt, claudeMdContent string, userContext, systemContext map[string]string) string {
	sections := make([]string, 0, 5)
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		sections = append(sections, trimmed)
	}
	if trimmed := strings.TrimSpace(claudeMdContent); trimmed != "" {
		sections = append(sections, trimmed)
	}
	if rendered := renderContextBlock("User context", userContext); rendered != "" {
		sections = append(sections, rendered)
	}
	if rendered := renderContextBlock("System context", systemContext); rendered != "" {
		sections = append(sections, rendered)
	}
	if trimmed := strings.TrimSpace(appendPrompt); trimmed != "" {
		sections = append(sections, trimmed)
	}
	return strings.Join(sections, "\n\n")
}

// --- Internal helpers ---

// collectParentDirs returns directories from root to CWD (exclusive of CWD).
// This gives the "parent directories" in root-to-leaf order for consistent
// priority (deeper = higher priority = loaded later).
func collectParentDirs(absCwd string) []string {
	var dirs []string
	dir := filepath.Dir(absCwd)
	for dir != absCwd {
		dirs = append(dirs, dir)
		absCwd = dir
		dir = filepath.Dir(dir)
	}
	// dirs is CWD-to-root; reverse to root-to-CWD
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// readClaudeMdFile attempts to read a single CLAUDE.md file. Returns nil if the
// file does not exist or is empty. Uses the cache when available.
func readClaudeMdFile(path, source string, cache *ClaudeMdCache) *ClaudeMdFile {
	if cache != nil {
		if content, ok := cache.Get(path); ok {
			if strings.TrimSpace(content) == "" {
				return nil
			}
			return &ClaudeMdFile{
				Path:    path,
				Content: content,
				Source:  source,
			}
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	content := string(data)
	if cache != nil {
		cache.Set(path, content)
	}

	if strings.TrimSpace(content) == "" {
		return nil
	}

	return &ClaudeMdFile{
		Path:    path,
		Content: content,
		Source:  source,
	}
}

// sourceDescription returns a human-readable description for each source type.
func sourceDescription(source string) string {
	switch source {
	case "project":
		return " (project instructions, checked into the codebase)"
	case "project-dot-claude":
		return " (project instructions, checked into the codebase)"
	case "project-local":
		return " (project-local private instructions, not committed)"
	case "user":
		return " (user's private global instructions for all projects)"
	case "user-rule":
		return " (user rule)"
	case "project-rule":
		return " (project rule)"
	case "parent":
		return " (project instructions from parent directory)"
	case "parent-local":
		return " (private instructions from parent directory)"
	case "managed":
		return " (organizational managed instructions)"
	case "agent":
		return " (agent definition)"
	default:
		return ""
	}
}

// --- @include directive processing ---

// includeRe matches @include directives outside code blocks.
// Format: @include ./relative/path or @include /absolute/path
var includeRe = regexp.MustCompile(`(?m)^@include\s+(.+)$`)

// maxIncludeDepth prevents infinite recursion.
const maxIncludeDepth = 5

// processIncludes resolves @include directives in content, replacing them with
// the content of the referenced file. Handles recursion up to maxIncludeDepth
// levels and detects cycles via the visited set.
// Mirrors the reference implementation's @include handling in CLAUDE.md files.
func processIncludes(content, baseDir string, visited map[string]bool, depth int) string {
	if depth >= maxIncludeDepth {
		return content
	}
	if visited == nil {
		visited = make(map[string]bool)
	}

	// Skip @include directives that appear inside fenced code blocks.
	lines := strings.Split(content, "\n")
	var result strings.Builder
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track code fence boundaries.
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			result.WriteString(line)
			result.WriteByte('\n')
			continue
		}

		// Only process @include outside code blocks.
		if !inCodeBlock && includeRe.MatchString(line) {
			matches := includeRe.FindStringSubmatch(line)
			if len(matches) >= 2 {
				includePath := strings.TrimSpace(matches[1])
				resolved := resolveIncludePath(includePath, baseDir)

				if resolved != "" && !visited[resolved] {
					visited[resolved] = true
					includeContent, err := os.ReadFile(resolved)
					if err == nil {
						// Recursively process includes in the included file.
						processed := processIncludes(
							string(includeContent),
							filepath.Dir(resolved),
							visited,
							depth+1,
						)
						result.WriteString(processed)
						if !strings.HasSuffix(processed, "\n") {
							result.WriteByte('\n')
						}
						continue
					}
				}
				// If include failed (file not found, cycle, etc.), leave directive as-is.
			}
		}

		result.WriteString(line)
		result.WriteByte('\n')
	}

	// Trim trailing newline to avoid double-newline accumulation.
	out := result.String()
	if strings.HasSuffix(out, "\n") && !strings.HasSuffix(content, "\n") {
		out = out[:len(out)-1]
	}
	return out
}

// resolveIncludePath resolves an include path relative to a base directory.
// Supports both relative (./path, ../path) and absolute paths.
func resolveIncludePath(includePath, baseDir string) string {
	if includePath == "" {
		return ""
	}
	if filepath.IsAbs(includePath) {
		return filepath.Clean(includePath)
	}
	return filepath.Clean(filepath.Join(baseDir, includePath))
}

// --- Rules directory loading ---

// loadRulesDir loads all .md files from a rules directory in alphabetical order.
// Each file becomes a separate ClaudeMdFile entry. Mirrors the reference
// implementation's .claude/rules/ loading.
func loadRulesDir(dir, source string, cache *ClaudeMdCache) []ClaudeMdFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// Sort entries alphabetically for deterministic ordering.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var files []ClaudeMdFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		if f := readClaudeMdFile(fullPath, source, cache); f != nil {
			// Process includes within rule files too.
			f.Content = processIncludes(f.Content, filepath.Dir(f.Path), nil, 0)
			files = append(files, *f)
		}
	}
	return files
}
