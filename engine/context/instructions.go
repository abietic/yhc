package promptctx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// InstructionFile represents a discovered instruction file (AGENTS.md, CLAUDE.md,
// or similar) with metadata about its location, scope, and priority.
// This extends ClaudeMdFile with scope-awareness and priority ordering,
// mirroring the reference implementation's treatment of instruction files
// as having a "scope" rooted at their containing directory.
type InstructionFile struct {
	// Path is the absolute path to the instruction file.
	Path string
	// Content is the file's text content (after include processing).
	Content string
	// Source classifies where the file came from (same taxonomy as ClaudeMdFile.Source).
	Source string
	// Filename is the base filename (e.g., "AGENTS.md", "CLAUDE.md").
	Filename string
	// ScopeRoot is the directory that this instruction file applies to.
	// All files under ScopeRoot are governed by this instruction file.
	ScopeRoot string
	// Depth is the nesting depth from the filesystem root. Deeper files have
	// higher priority (loaded later, override shallower).
	Depth int
	// ModTime is the last modification time when the file was read.
	ModTime time.Time
	// Size is the file size when last read.
	Size int64
}

// InstructionDiscovery handles the discovery, caching, and scope-tracking of
// instruction files (AGENTS.md, CLAUDE.md, CLAUDE.local.md) across the
// filesystem hierarchy. It mirrors the reference implementation's directory walk
// from CWD up to root, collecting instruction files and applying
// deeper-takes-precedence rules.
type InstructionDiscovery struct {
	mu    sync.RWMutex
	cache *ClaudeMdCache
	// files holds the most recently discovered instruction files, ordered by
	// priority (lowest first, highest last — deeper files are later).
	files []InstructionFile
	// lastDiscoveryCWD records what CWD was used for the last discovery pass.
	lastDiscoveryCWD string
	// lastDiscoveryTime records when the last discovery pass occurred.
	lastDiscoveryTime time.Time
	// recognizedFilenames lists all filenames that are treated as instruction files.
	recognizedFilenames []string
}

// DefaultInstructionFilenames lists the filenames recognized as instruction files.
// AGENTS.md is the primary cross-tool standard; CLAUDE.md is the Claude-specific
// format. Both are discovered and loaded.
var DefaultInstructionFilenames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"CLAUDE.local.md",
}

// NewInstructionDiscovery creates a new discovery instance with the given cache.
// If cache is nil, a new cache is created.
func NewInstructionDiscovery(cache *ClaudeMdCache) *InstructionDiscovery {
	if cache == nil {
		cache = NewClaudeMdCache()
	}
	return &InstructionDiscovery{
		cache:               cache,
		recognizedFilenames: DefaultInstructionFilenames,
	}
}

// Discover walks from cwd up to the filesystem root, finding all instruction
// files (AGENTS.md, CLAUDE.md, CLAUDE.local.md). Files are returned in priority
// order: shallowest (lowest priority) first, deepest (highest priority) last.
// This matches the reference implementation's "files closer to CWD have higher
// priority" rule.
//
// The discovery also checks:
//   - User home: ~/.claude/CLAUDE.md
//   - .claude/ subdirectories at each level
//   - .claude/rules/*.md at the CWD level
func (d *InstructionDiscovery) Discover(cwd string) ([]InstructionFile, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolving cwd: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	var files []InstructionFile

	// 1. User home instructions (lowest priority)
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".claude", "CLAUDE.md")
		if f := d.readInstructionFile(userPath, "user", home); f != nil {
			files = append(files, *f)
		}
	}

	// 2. Walk from root toward CWD, collecting instruction files.
	// Collect all ancestor directories (CWD exclusive), then reverse to
	// walk root-to-CWD for correct priority ordering.
	ancestors := collectAncestorDirs(absCwd)
	for _, dir := range ancestors {
		files = append(files, d.discoverInDir(dir, "parent")...)
	}

	// 3. CWD-level files (highest priority for project scope)
	files = append(files, d.discoverInDir(absCwd, "project")...)

	// 4. .claude/rules/*.md at CWD level
	rulesDir := filepath.Join(absCwd, ".claude", "rules")
	files = append(files, d.discoverRulesDir(rulesDir, "project-rule", absCwd)...)

	d.files = files
	d.lastDiscoveryCWD = absCwd
	d.lastDiscoveryTime = time.Now()

	return files, nil
}

// discoverInDir finds instruction files in a single directory, including the
// .claude/ subdirectory variant.
func (d *InstructionDiscovery) discoverInDir(dir, source string) []InstructionFile {
	var files []InstructionFile

	for _, filename := range d.recognizedFilenames {
		// Direct path: dir/AGENTS.md, dir/CLAUDE.md, etc.
		directPath := filepath.Join(dir, filename)
		if f := d.readInstructionFile(directPath, source, dir); f != nil {
			files = append(files, *f)
		}

		// .claude/ variant: dir/.claude/AGENTS.md, dir/.claude/CLAUDE.md, etc.
		// (skip CLAUDE.local.md in .claude/ to avoid double-loading)
		if filename != "CLAUDE.local.md" {
			dotClaudePath := filepath.Join(dir, ".claude", filename)
			if f := d.readInstructionFile(dotClaudePath, source, dir); f != nil {
				files = append(files, *f)
			}
		}
	}

	return files
}

// discoverRulesDir loads all .md files from a rules directory.
func (d *InstructionDiscovery) discoverRulesDir(dir, source, scopeRoot string) []InstructionFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var files []InstructionFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		if f := d.readInstructionFile(fullPath, source, scopeRoot); f != nil {
			files = append(files, *f)
		}
	}
	return files
}

// readInstructionFile reads a single instruction file, using the cache.
// Returns nil if the file doesn't exist or is empty.
func (d *InstructionDiscovery) readInstructionFile(path, source, scopeRoot string) *InstructionFile {
	// Try cache first
	if content, ok := d.cache.Get(path); ok {
		if strings.TrimSpace(content) == "" {
			return nil
		}
		info, _ := os.Stat(path)
		depth := countPathComponents(scopeRoot)
		f := &InstructionFile{
			Path:      path,
			Content:   content,
			Source:    source,
			Filename:  filepath.Base(path),
			ScopeRoot: scopeRoot,
			Depth:     depth,
		}
		if info != nil {
			f.ModTime = info.ModTime()
			f.Size = info.Size()
		}
		return f
	}

	// Read from disk
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return nil
	}

	// Process @include directives
	content = processIncludes(content, filepath.Dir(path), nil, 0)

	// Cache the processed content
	d.cache.Set(path, content)

	info, _ := os.Stat(path)
	depth := countPathComponents(scopeRoot)
	f := &InstructionFile{
		Path:      path,
		Content:   content,
		Source:    source,
		Filename:  filepath.Base(path),
		ScopeRoot: scopeRoot,
		Depth:     depth,
	}
	if info != nil {
		f.ModTime = info.ModTime()
		f.Size = info.Size()
	}
	return f
}

// GetActiveFiles returns all currently discovered instruction files.
func (d *InstructionDiscovery) GetActiveFiles() []InstructionFile {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]InstructionFile, len(d.files))
	copy(result, d.files)
	return result
}

// FilesForPath returns instruction files whose scope includes the given path.
// A file's scope includes targetPath if targetPath is under file.ScopeRoot.
// Results are in priority order (deepest scope last = highest priority).
func (d *InstructionDiscovery) FilesForPath(targetPath string) []InstructionFile {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		absTarget = targetPath
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	var matching []InstructionFile
	for _, f := range d.files {
		if pathIsUnder(absTarget, f.ScopeRoot) {
			matching = append(matching, f)
		}
	}
	return matching
}

// HasChanged checks if any discovered instruction file has been modified since
// last discovery. This is a cheap stat-only check (no re-reads) suitable for
// calling between turns to decide if a refresh is needed.
func (d *InstructionDiscovery) HasChanged() bool {
	d.mu.RLock()
	files := d.files
	d.mu.RUnlock()

	for _, f := range files {
		info, err := os.Stat(f.Path)
		if err != nil {
			// File was deleted — counts as a change.
			return true
		}
		if !info.ModTime().Equal(f.ModTime) || info.Size() != f.Size {
			return true
		}
	}

	// Also check if new instruction files have appeared in the CWD hierarchy.
	d.mu.RLock()
	cwd := d.lastDiscoveryCWD
	d.mu.RUnlock()

	if cwd != "" {
		if d.hasNewFiles(cwd) {
			return true
		}
	}

	return false
}

// hasNewFiles checks if any instruction files exist on disk that weren't in the
// last discovery pass. This catches the case where a user creates a new AGENTS.md.
func (d *InstructionDiscovery) hasNewFiles(cwd string) bool {
	d.mu.RLock()
	existingPaths := make(map[string]bool, len(d.files))
	for _, f := range d.files {
		existingPaths[f.Path] = true
	}
	d.mu.RUnlock()

	// Check CWD level
	for _, filename := range d.recognizedFilenames {
		path := filepath.Join(cwd, filename)
		if !existingPaths[path] {
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
		dotPath := filepath.Join(cwd, ".claude", filename)
		if !existingPaths[dotPath] {
			if _, err := os.Stat(dotPath); err == nil {
				return true
			}
		}
	}

	return false
}

// Refresh re-discovers instruction files if any have changed. Returns true if
// the context was actually refreshed (files changed), false otherwise.
// This is the main entry point for the query loop to call between turns.
func (d *InstructionDiscovery) Refresh(cwd string) (bool, error) {
	if cwd == "" {
		d.mu.RLock()
		cwd = d.lastDiscoveryCWD
		d.mu.RUnlock()
	}
	if cwd == "" {
		return false, nil
	}

	if !d.HasChanged() {
		return false, nil
	}

	// Clear cache for stale entries and re-discover
	d.cache.Clear()
	_, err := d.Discover(cwd)
	if err != nil {
		return false, err
	}
	return true, nil
}

// --- Scope-based instruction assembly ---

// InstructionPriority defines the priority levels for prompt assembly.
// Higher values take precedence.
type InstructionPriority int

const (
	// PriorityDefault is the base system prompt priority.
	PriorityDefault InstructionPriority = 0
	// PriorityProjectInstructions is for AGENTS.md/CLAUDE.md from the project.
	PriorityProjectInstructions InstructionPriority = 10
	// PriorityUserInstructions is for user-specified custom instructions.
	PriorityUserInstructions InstructionPriority = 20
)

// PromptComponent represents a single section of the assembled system prompt.
// The assembly system collects components from various sources and orders them
// for inclusion in the final prompt.
type PromptComponent struct {
	// Label identifies this component (for debugging/inspection).
	Label string
	// Content is the text to include.
	Content string
	// Priority determines ordering — higher priority sections are placed
	// later in the prompt (models typically pay more attention to later content).
	Priority InstructionPriority
	// Source tracks where this component originated.
	Source string
}

// PromptAssembly holds the complete set of components that make up a system
// prompt. It provides a provider-neutral representation that any model adapter
// can format appropriately.
type PromptAssembly struct {
	// Components are the prompt sections in assembly order.
	Components []PromptComponent
}

// AssemblePrompt combines all discovered instruction files with the base system
// prompt and any additional context into a PromptAssembly. The assembly follows
// the reference implementation's priority ordering:
//
//  1. Base system prompt (identity, environment, capabilities)
//  2. Project instructions (AGENTS.md/CLAUDE.md — deeper = higher priority)
//  3. User custom instructions (explicit user overrides)
//
// Within each priority level, components preserve their discovery order
// (root-to-CWD for project instructions).
func AssemblePrompt(base string, discovery *InstructionDiscovery, customInstructions string) *PromptAssembly {
	var components []PromptComponent

	// 1. Base system prompt
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		components = append(components, PromptComponent{
			Label:    "base_system_prompt",
			Content:  trimmed,
			Priority: PriorityDefault,
			Source:   "system",
		})
	}

	// 2. Project instructions from discovery
	if discovery != nil {
		files := discovery.GetActiveFiles()
		for _, f := range files {
			trimmed := strings.TrimSpace(f.Content)
			if trimmed == "" {
				continue
			}
			desc := sourceDescription(f.Source)
			content := fmt.Sprintf("Contents of %s%s:\n\n%s", f.Path, desc, trimmed)
			components = append(components, PromptComponent{
				Label:    fmt.Sprintf("instruction:%s", f.Path),
				Content:  content,
				Priority: PriorityProjectInstructions,
				Source:   f.Source,
			})
		}
	}

	// 3. User custom instructions
	if trimmed := strings.TrimSpace(customInstructions); trimmed != "" {
		components = append(components, PromptComponent{
			Label:    "custom_instructions",
			Content:  trimmed,
			Priority: PriorityUserInstructions,
			Source:   "user",
		})
	}

	return &PromptAssembly{Components: components}
}

// AssemblePromptForPath is like AssemblePrompt but filters instructions to only
// include those whose scope covers the given target path. This is used when the
// agent is working on a specific file and should only obey instructions that
// apply to that file's location.
func AssemblePromptForPath(base string, discovery *InstructionDiscovery, customInstructions, targetPath string) *PromptAssembly {
	var components []PromptComponent

	// 1. Base system prompt
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		components = append(components, PromptComponent{
			Label:    "base_system_prompt",
			Content:  trimmed,
			Priority: PriorityDefault,
			Source:   "system",
		})
	}

	// 2. Scope-filtered project instructions
	if discovery != nil {
		files := discovery.FilesForPath(targetPath)
		for _, f := range files {
			trimmed := strings.TrimSpace(f.Content)
			if trimmed == "" {
				continue
			}
			desc := sourceDescription(f.Source)
			content := fmt.Sprintf("Contents of %s%s:\n\n%s", f.Path, desc, trimmed)
			components = append(components, PromptComponent{
				Label:    fmt.Sprintf("instruction:%s", f.Path),
				Content:  content,
				Priority: PriorityProjectInstructions,
				Source:   f.Source,
			})
		}
	}

	// 3. User custom instructions
	if trimmed := strings.TrimSpace(customInstructions); trimmed != "" {
		components = append(components, PromptComponent{
			Label:    "custom_instructions",
			Content:  trimmed,
			Priority: PriorityUserInstructions,
			Source:   "user",
		})
	}

	return &PromptAssembly{Components: components}
}

// Render produces the final system prompt text from the assembly. Components
// are sorted by priority (ascending) then rendered with double-newline
// separators. This is the provider-neutral output that any model adapter can
// consume directly.
func (a *PromptAssembly) Render() string {
	if a == nil || len(a.Components) == 0 {
		return ""
	}

	// Stable sort by priority to preserve discovery order within same priority.
	sorted := make([]PromptComponent, len(a.Components))
	copy(sorted, a.Components)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})

	sections := make([]string, 0, len(sorted))
	for _, c := range sorted {
		if trimmed := strings.TrimSpace(c.Content); trimmed != "" {
			sections = append(sections, trimmed)
		}
	}
	return strings.Join(sections, "\n\n")
}

// RenderWithHeader produces the final prompt with the standard instruction
// override header prepended (when instruction files are present).
func (a *PromptAssembly) RenderWithHeader() string {
	if a == nil || len(a.Components) == 0 {
		return ""
	}

	hasInstructions := false
	for _, c := range a.Components {
		if c.Priority == PriorityProjectInstructions {
			hasInstructions = true
			break
		}
	}

	rendered := a.Render()
	if !hasInstructions {
		return rendered
	}

	const header = "Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written."
	return header + "\n\n" + rendered
}

// Labels returns the labels of all components in the assembly (for debugging).
func (a *PromptAssembly) Labels() []string {
	if a == nil {
		return nil
	}
	labels := make([]string, len(a.Components))
	for i, c := range a.Components {
		labels[i] = c.Label
	}
	return labels
}

// --- Helper functions ---

// collectAncestorDirs returns directories from root to dir (exclusive of dir),
// in root-to-leaf order. This is used for priority ordering: shallower
// directories come first (lower priority).
func collectAncestorDirs(absDir string) []string {
	var dirs []string
	current := filepath.Dir(absDir)
	for current != absDir {
		dirs = append(dirs, current)
		absDir = current
		current = filepath.Dir(current)
	}
	// dirs is leaf-to-root; reverse for root-to-leaf
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// pathIsUnder returns true if targetPath is equal to or a descendant of root.
func pathIsUnder(targetPath, root string) bool {
	if root == "" {
		return false
	}
	// Clean both paths for reliable comparison
	cleanTarget := filepath.Clean(targetPath)
	cleanRoot := filepath.Clean(root)

	if cleanTarget == cleanRoot {
		return true
	}

	// Ensure root ends with separator for prefix check
	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(cleanTarget, prefix)
}

// countPathComponents counts the number of path components (depth) for a path.
// Used for precedence ordering — deeper paths get higher priority.
func countPathComponents(path string) int {
	cleaned := filepath.Clean(path)
	if cleaned == "/" || cleaned == "." {
		return 0
	}
	return strings.Count(cleaned, string(filepath.Separator))
}
